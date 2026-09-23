import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import OfferView from './OfferView.vue'

const { api } = vi.hoisted(() => ({
  api: {
    get: vi.fn(),
    put: vi.fn(),
    post: vi.fn(),
  },
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
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAdmin: true }),
}))
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

const sender = (company: string, uid: string) => ({
  company,
  street: 'Testweg 1',
  postal_code: '1010',
  city: 'Wien',
  country: 'Österreich',
  register_no: '',
  register_court: '',
  email: 'office@example.test',
  phone: '',
  website: '',
  uid,
  bank_name: '',
  iban: '',
  bic: '',
  contact_person: 'Ada',
})

function draft() {
  return {
    id: 41,
    offer_no: 'A260923-41',
    customer_id: 7,
    status: 'draft',
    revision: 3,
    created_at: '2026-09-23T00:00:00Z',
    updated_at: '2026-09-23T00:00:00Z',
    sent_at: null,
    document: {
      title: 'Beratung',
      subtitle: '',
      project_ref: '',
      offer_date: '2026-09-23',
      valid_until: '2026-10-23',
      intro: 'Danke für den Entwurf.',
      blocks: [{ heading: 'Leistung', body: 'Analyse.' }],
      accept_text: 'Bitte annehmen.',
      vat_note: 'exklusive 20 % USt',
      net_total_cents: 10000,
      sender: sender('Entwurf GmbH', 'ATU00000002'),
      customer: {
        name: 'Testkunde',
        address: 'Gasse 2',
        contact: 'Eva',
        country: 'Österreich',
        customer_no: 'K26091',
        email: 'eva@example.test',
      },
      positions: [
        {
          short_text: 'Analyse',
          long_text: '',
          quantity: 1,
          unit: 'Pauschale',
          unit_price_cents: 10000,
          total_cents: 10000,
        },
      ],
    },
  }
}

function centralSettings() {
  return {
    sender: sender('Vorlage GmbH', 'ATU00000001'),
    defaults: {
      intro: 'Vorlage für künftige Angebote.',
      blocks: [],
      accept_text: '',
      vat_note: '',
    },
  }
}

describe('offer settings stay off the open draft', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('saves central defaults without changing or storing the current offer', async () => {
    const offer = draft()
    const before = JSON.stringify(offer.document)
    api.get.mockImplementation((path: string) => {
      if (path === '/offers/41') return Promise.resolve(structuredClone(offer))
      if (path === '/integrations/crm/offers') return Promise.resolve(centralSettings())
      return Promise.reject(new Error(path))
    })
    api.put.mockImplementation((path: string, body: unknown) => Promise.resolve(body ?? { path }))
    if (!HTMLDialogElement.prototype.showModal) {
      HTMLDialogElement.prototype.showModal = function showModal() {
        this.open = true
      }
      HTMLDialogElement.prototype.close = function close() {
        this.open = false
      }
    }
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
    expect(el.textContent).toContain('A260923-41')
    el.querySelector<HTMLButtonElement>('[aria-label="Einstellungen"]')!.click()
    await nextTick()
    const settingsItem = [...el.querySelectorAll('button')].find((button) =>
      button.textContent?.includes('Vorlagen für neue Angebote'),
    )!
    expect(settingsItem.textContent).toContain('geöffnete Angebot bleibt unverändert')
    settingsItem.click()
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    const intro = el.querySelector<HTMLTextAreaElement>('textarea')!
    expect(intro.value).toBe('Vorlage für künftige Angebote.')
    intro.value = 'Nur für das nächste Angebot.'
    intro.dispatchEvent(new Event('input'))
    el.querySelector('form')!.dispatchEvent(
      new Event('submit', { bubbles: true, cancelable: true }),
    )
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 800))
    const puts = api.put.mock.calls.map((call) => String(call[0]))
    expect(puts).toEqual(['/integrations/crm/offers'])
    expect(puts.some((path) => path.startsWith('/offers/'))).toBe(false)
    expect(JSON.stringify(offer.document)).toBe(before)
    expect(offer.document.sender).toMatchObject({ company: 'Entwurf GmbH', uid: 'ATU00000002' })
    expect(el.textContent).toContain('nur für neue Angebote')
    app.unmount()
  })
})
