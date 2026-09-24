import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { ApiError } from '@/api/client'
import OfferView from './OfferView.vue'

const { api } = vi.hoisted(() => ({
  api: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
}))

vi.mock('vue-router', async () => {
  const { h } = await import('vue')
  return {
    useRoute: () => ({ params: { id: '41' }, path: '/crm/offers/41' }),
    useRouter: () => ({ push: vi.fn() }),
    onBeforeRouteLeave: () => {},
    RouterLink: {
      name: 'RouterLink',
      props: { to: { type: [String, Object], default: '' } },
      setup(
        props: { to?: string },
        { slots }: { slots: { default?: () => import('vue').VNode[] } },
      ) {
        return () => h('a', { href: String(props.to ?? '') }, slots.default?.())
      },
    },
  }
})
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: true }) }))
vi.mock('@/api/instance', async () => {
  const { ref } = await import('vue')
  return {
    crmEnabled: ref(true),
    instanceHostname: ref('qa.example.test'),
    loadInstance: vi.fn().mockResolvedValue(undefined),
  }
})
vi.mock('@/api/client', () => ({
  api,
  errMsg: (error: unknown) => (error instanceof Error ? error.message : 'Fehler'),
  ApiError: class ApiError extends Error {
    status = 0
  },
}))

function draft(revision: number) {
  return {
    id: 41,
    offer_no: 'A260923-41',
    customer_id: 7,
    status: 'draft',
    revision,
    created_at: '2026-09-23T00:00:00Z',
    updated_at: '2026-09-23T00:00:00Z',
    sent_at: null,
    document: {
      title: 'Beratung',
      subtitle: '',
      project_ref: '',
      offer_date: '2026-09-23',
      valid_until: '2026-10-23',
      intro: 'Danke.',
      blocks: [{ heading: 'Leistung', body: 'Analyse.' }],
      accept_text: '',
      vat_note: '',
      net_total_cents: 0,
      sender: {
        company: 'Entwurf GmbH',
        street: 'Testweg 1',
        postal_code: '1010',
        city: 'Wien',
        country: 'Österreich',
        register_no: '',
        register_court: '',
        email: 'office@example.test',
        phone: '',
        website: '',
        uid: 'ATU00000002',
        bank_name: '',
        iban: '',
        bic: '',
        contact_person: 'Ada',
      },
      customer: {
        name: 'Testkunde',
        address: 'Gasse 2',
        contact: 'Eva',
        country: 'Österreich',
        customer_no: 'K1',
        email: 'eva@example.test',
      },
      positions: [],
    },
  }
}

describe('offer remote revision on the open page', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('shows Aktualisieren after another session and reloads a clean offer', async () => {
    const local = draft(3)
    const remote = draft(5)
    api.get.mockImplementation((path: string) => {
      if (path !== '/offers/41') return Promise.reject(new Error(path))
      return Promise.resolve(structuredClone(api.get.mock.calls.length === 1 ? local : remote))
    })
    Object.defineProperty(document, 'fonts', {
      configurable: true,
      value: { ready: Promise.resolve() },
    })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferView)
    app.mount(el)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    expect(el.querySelector('.remote-refresh')).toBeNull()
    window.dispatchEvent(new Event('focus'))
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const refresh = el.querySelector<HTMLButtonElement>('.remote-refresh')!
    expect(refresh.textContent).toContain('Extern geändert')
    expect(refresh.textContent).toContain('Aktualisieren')
    const getsBefore = api.get.mock.calls.length
    refresh.click()
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    expect(api.get.mock.calls.length).toBeGreaterThan(getsBefore)
    expect(el.querySelector('.remote-refresh')).toBeNull()
    app.unmount()
  })

  it('asks before a dirty reload and keeps the local edit on cancel', async () => {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.open = true
    }
    HTMLDialogElement.prototype.close = function close() {
      this.open = false
    }
    const local = draft(3)
    const remote = draft(9)
    api.get.mockImplementation((path: string) => {
      if (path !== '/offers/41') return Promise.reject(new Error(path))
      const first = api.get.mock.calls.length === 1
      return Promise.resolve(structuredClone(first ? local : remote))
    })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferView)
    app.mount(el)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const date = el.querySelector<HTMLInputElement>('[aria-label="Angebotsdatum"]')!
    date.value = '2026-09-01'
    date.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    window.dispatchEvent(new Event('focus'))
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const getsBefore = api.get.mock.calls.length
    el.querySelector<HTMLButtonElement>('.remote-refresh')!.click()
    await nextTick()
    await nextTick()
    const dialog = el.querySelector<HTMLDialogElement>('[aria-label="Extern geändert"]')!
    expect(dialog.open).toBe(true)
    expect(api.get.mock.calls.length).toBe(getsBefore)
    ;[...dialog.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Abbrechen')!.click()
    await nextTick()
    await nextTick()
    expect(dialog.open).toBe(false)
    expect(el.querySelector<HTMLInputElement>('[aria-label="Angebotsdatum"]')!.value).toBe(
      '2026-09-01',
    )
    expect(api.get.mock.calls.length).toBe(getsBefore)
    app.unmount()
  })

  it('keeps edits typed during a reload and ignores a stale save conflict', async () => {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.open = true
    }
    HTMLDialogElement.prototype.close = function close() {
      this.open = false
    }
    const local = draft(3)
    let releaseGet: (value: unknown) => void = () => {}
    let rejectPut: (error: unknown) => void = () => {}
    let gets = 0
    api.get.mockImplementation((path: string) => {
      if (path !== '/offers/41') return Promise.reject(new Error(path))
      gets += 1
      if (gets === 1) return Promise.resolve(structuredClone(local))
      if (gets === 2) return Promise.resolve(structuredClone(draft(5)))
      return new Promise((resolve) => {
        releaseGet = resolve
      })
    })
    api.put.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          rejectPut = reject
        }),
    )
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferView)
    app.mount(el)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    window.dispatchEvent(new Event('focus'))
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const date = el.querySelector<HTMLInputElement>('[aria-label="Angebotsdatum"]')!
    date.value = '2026-09-01'
    date.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    el.querySelector<HTMLButtonElement>('[aria-label="Jetzt speichern"]')!.click()
    await nextTick()
    el.querySelector<HTMLButtonElement>('.remote-refresh')!.click()
    await nextTick()
    await nextTick()
    const dialog = el.querySelector<HTMLDialogElement>('[aria-label="Extern geändert"]')!
    ;[...dialog.querySelectorAll('button')].find((button) => button.textContent?.includes('Aktualisieren'))!.click()
    await nextTick()
    expect(el.querySelector('[aria-label="Angebotsdatum"]')).toBeNull()
    const stale = new ApiError(409, 'conflict')
    stale.status = 409
    rejectPut(stale)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const title = el.querySelector<HTMLElement>('[aria-label="Angebotstitel"]')!
    title.textContent = 'Lokal während des Ladens'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    const server = draft(9)
    server.document.title = 'Serverstand'
    releaseGet(structuredClone(server))
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    expect(el.textContent).toContain('Lokal während des Ladens')
    expect(el.textContent).not.toContain('Serverstand')
    expect(el.textContent).not.toContain('Speicherkonflikt')
    expect(el.querySelector<HTMLInputElement>('[aria-label="Angebotsdatum"]')!.value).toBe('2026-09-01')
    app.unmount()
  })

  it('applies a confirmed reload only after the outstanding save and ignores its 409', async () => {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.open = true
    }
    HTMLDialogElement.prototype.close = function close() {
      this.open = false
    }
    let releaseGet: (value: unknown) => void = () => {}
    let rejectPut: (error: unknown) => void = () => {}
    let gets = 0
    api.get.mockImplementation((path: string) => {
      if (path !== '/offers/41') return Promise.reject(new Error(path))
      gets += 1
      if (gets === 1) return Promise.resolve(structuredClone(draft(3)))
      if (gets === 2) return Promise.resolve(structuredClone(draft(5)))
      return new Promise((resolve) => {
        releaseGet = resolve
      })
    })
    api.put.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          rejectPut = reject
        }),
    )
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferView)
    app.mount(el)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    window.dispatchEvent(new Event('focus'))
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const date = el.querySelector<HTMLInputElement>('[aria-label="Angebotsdatum"]')!
    date.value = '2026-09-01'
    date.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    el.querySelector<HTMLButtonElement>('[aria-label="Jetzt speichern"]')!.click()
    await nextTick()
    el.querySelector<HTMLButtonElement>('.remote-refresh')!.click()
    await nextTick()
    await nextTick()
    const dialog = el.querySelector<HTMLDialogElement>('[aria-label="Extern geändert"]')!
    ;[...dialog.querySelectorAll('button')].find((button) => button.textContent?.includes('Aktualisieren'))!.click()
    await nextTick()
    expect(api.get.mock.calls.length).toBe(2)
    const stale = new ApiError(409, 'conflict')
    stale.status = 409
    rejectPut(stale)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    expect(api.get.mock.calls.length).toBe(3)
    const server = draft(9)
    server.document.title = 'Serverstand'
    releaseGet(structuredClone(server))
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    expect(el.textContent).toContain('Serverstand')
    expect(el.textContent).not.toContain('Speicherkonflikt')
    expect(el.querySelector<HTMLInputElement>('[aria-label="Angebotsdatum"]')!.value).toBe('2026-09-23')
    app.unmount()
  })
})
