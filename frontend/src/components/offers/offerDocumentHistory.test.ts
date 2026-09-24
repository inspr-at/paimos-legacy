import { afterEach, describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, nextTick, reactive, ref } from 'vue'
import OfferDocument from './OfferDocument.vue'
import type { Offer, OfferPosition, OfferSelection } from './types'

const originalRect = HTMLElement.prototype.getBoundingClientRect

function rect(height: number): DOMRect {
  return {
    x: 0,
    y: 0,
    width: 640,
    height,
    top: 0,
    left: 0,
    bottom: height,
    right: 640,
    toJSON() {
      return {}
    },
  } as DOMRect
}

function row(short: string, quantity = 1): OfferPosition {
  return {
    short_text: short,
    long_text: '',
    quantity,
    unit: 'Std.',
    unit_price_cents: 10000,
    total_cents: quantity * 10000,
  }
}

function offer(): Offer {
  return {
    id: 1,
    offer_no: 'A260923-01',
    customer_id: 1,
    status: 'draft',
    revision: 1,
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
      blocks: [
        { heading: 'Eins', body: 'Alpha' },
        { heading: 'Zwei', body: 'Beta' },
      ],
      accept_text: 'Bitte annehmen.',
      vat_note: 'exklusive 20 % USt',
      net_total_cents: 10000,
      sender: {
        company: 'Example GmbH',
        street: 'Testweg 1',
        postal_code: '1010',
        city: 'Wien',
        country: 'Österreich',
        register_no: '',
        register_court: '',
        email: 'office@example.test',
        phone: '',
        website: '',
        uid: '',
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
        customer_no: 'K26091',
        email: 'eva@example.test',
      },
      positions: [row('A'), row('B')],
    },
  }
}

type DocApi = {
  paginate: () => Promise<void>
  undo: () => void
  redo: () => void
  canUndo: boolean | { value: boolean }
  canRedo: boolean | { value: boolean }
  resetHistory: () => void
  addSection: () => void
  moveSection: (direction: -1 | 1) => void
  askDeleteSection: (index?: number | null) => void
  move: (index: number, direction: number) => void
  remove: (index: number) => void
}

function enabled(value: boolean | { value: boolean } | undefined) {
  if (!value) return false
  return typeof value === 'boolean' ? value : value.value
}

function installFonts() {
  Object.defineProperty(document, 'fonts', {
    configurable: true,
    value: { ready: Promise.resolve() },
  })
  HTMLElement.prototype.getBoundingClientRect = function () {
    const el = this as HTMLElement
    if (el.classList.contains('page-content')) return rect(1000)
    return rect(40)
  }
}

async function mount(sample: Offer, editable = true) {
  const selected: OfferSelection[] = []
  let api: DocApi | null = null
  const editableRef = ref(editable)
  const el = document.createElement('div')
  document.body.appendChild(el)
  const Host = defineComponent({
    name: 'HistoryHost',
    setup() {
      return () =>
        h(OfferDocument, {
          offer: sample,
          editable: editableRef.value,
          ref: (value) => {
            api = value as DocApi | null
          },
          onSelect: (value: OfferSelection) => selected.push(value),
        })
    },
  })
  const app = createApp(Host)
  app.mount(el)
  await nextTick()
  const mounted = api as DocApi | null
  if (!mounted) throw new Error('document api missing')
  await mounted.paginate()
  await nextTick()
  return {
    el,
    selected,
    editableRef,
    get api() {
      if (!api) throw new Error('document api missing')
      return api
    },
    unmount() {
      app.unmount()
      el.remove()
    },
  }
}

describe('offer document history', () => {
  afterEach(() => {
    HTMLElement.prototype.getBoundingClientRect = originalRect
    document.body.innerHTML = ''
  })

  it('keeps an inserted position through undo and redo after a reorder', async () => {
    installFonts()
    const sample = reactive(offer())
    const view = await mount(sample)
    const shorts = () => sample.document.positions.map((position) => position.short_text)
    view.api.move(0, 1)
    await nextTick()
    expect(shorts()).toEqual(['B', 'A'])
    sample.document.positions.push(row('C'))
    await nextTick()
    expect(shorts()).toEqual(['B', 'A', 'C'])
    view.api.undo()
    expect(shorts()).toEqual(['B', 'A'])
    view.api.redo()
    expect(shorts()).toEqual(['B', 'A', 'C'])
    view.api.undo()
    view.api.undo()
    expect(shorts()).toEqual(['A', 'B'])
    view.api.redo()
    view.api.redo()
    expect(shorts()).toEqual(['B', 'A', 'C'])
    view.api.remove(2)
    await nextTick()
    expect(shorts()).toEqual(['B', 'A'])
    view.api.undo()
    expect(shorts()).toEqual(['B', 'A', 'C'])
    view.unmount()
  })

  it('records title, heading, metadata, footer, intro, acceptance and cells on one boundary', async () => {
    installFonts()
    const sample = reactive(offer())
    const view = await mount(sample)
    const doc = sample.document
    doc.title = 'Neu'
    await nextTick()
    doc.blocks[0]!.heading = 'Kopf'
    await nextTick()
    doc.intro = 'Andere Einleitung'
    await nextTick()
    doc.accept_text = 'Andere Annahme'
    await nextTick()
    doc.vat_note = 'inklusive USt'
    await nextTick()
    doc.subtitle = 'Untertitel'
    await nextTick()
    doc.project_ref = 'P-9'
    await nextTick()
    doc.offer_date = '2026-01-02'
    await nextTick()
    doc.customer.name = 'Andere GmbH'
    await nextTick()
    doc.sender.contact_person = 'Bea'
    await nextTick()
    doc.positions[0]!.short_text = 'Zelle'
    doc.positions[0]!.quantity = 4
    doc.net_total_cents = 40000
    await nextTick()
    doc.footer = { logo_width_mm: 40, logo_offset_mm: -2 }
    await nextTick()
    view.api.undo()
    expect(doc.footer).toBeUndefined()
    expect(doc.positions[0]?.short_text).toBe('Zelle')
    view.api.undo()
    expect(doc.positions[0]).toMatchObject({ short_text: 'A', quantity: 1 })
    expect(doc.net_total_cents).toBe(10000)
    view.api.undo()
    expect(doc.sender.contact_person).toBe('Ada')
    view.api.undo()
    expect(doc.customer.name).toBe('Testkunde')
    view.api.undo()
    expect(doc.offer_date).toBe('2026-09-23')
    view.api.undo()
    expect(doc.project_ref).toBe('')
    view.api.undo()
    expect(doc.subtitle).toBe('')
    view.api.undo()
    expect(doc.vat_note).toBe('exklusive 20 % USt')
    view.api.undo()
    expect(doc.accept_text).toBe('Bitte annehmen.')
    view.api.undo()
    expect(doc.intro).toBe('Danke.')
    view.api.undo()
    expect(doc.blocks[0]?.heading).toBe('Eins')
    view.api.undo()
    expect(doc.title).toBe('Beratung')
    expect(enabled(view.api.canUndo)).toBe(false)
    view.unmount()
  })

  it('resets history on document replacement and blocks undo while editing is off', async () => {
    installFonts()
    const sample = reactive(offer())
    const view = await mount(sample)
    const doc = sample.document
    doc.title = 'Lokal'
    await nextTick()
    expect(enabled(view.api.canUndo)).toBe(true)
    sample.revision = 4
    sample.updated_at = '2026-09-23T03:00:00Z'
    await nextTick()
    expect(enabled(view.api.canUndo)).toBe(true)
    view.api.undo()
    expect(doc.title).toBe('Beratung')
    doc.title = 'Lokal'
    await nextTick()
    view.editableRef.value = false
    await nextTick()
    expect(enabled(view.api.canUndo)).toBe(false)
    view.api.undo()
    expect(doc.title).toBe('Lokal')
    view.editableRef.value = true
    await nextTick()
    view.api.undo()
    expect(doc.title).toBe('Beratung')
    doc.title = 'Lokal'
    await nextTick()
    const server = JSON.parse(JSON.stringify(doc)) as Offer['document']
    server.title = 'Server'
    sample.document = server
    await nextTick()
    expect(enabled(view.api.canUndo)).toBe(false)
    expect(view.selected[view.selected.length - 1]?.kind).toBe('none')
    view.api.undo()
    expect(sample.document.title).toBe('Server')
    sample.document.title = 'Nach dem Laden'
    await nextTick()
    view.api.resetHistory()
    expect(enabled(view.api.canUndo)).toBe(false)
    view.api.undo()
    expect(sample.document.title).toBe('Nach dem Laden')
    view.editableRef.value = false
    await nextTick()
    sample.document.title = 'Nur lesen'
    await nextTick()
    view.editableRef.value = true
    await nextTick()
    expect(enabled(view.api.canUndo)).toBe(false)
    view.api.undo()
    expect(sample.document.title).toBe('Nur lesen')
    view.unmount()
  })

  it('undoes a section reorder when headings are blank or identical', async () => {
    installFonts()
    const bodies = (sample: Offer) => sample.document.blocks.map((block) => block.body)
    async function reorder(headings: [string, string]) {
      const sample = reactive(offer())
      sample.document.blocks[0]!.heading = headings[0]
      sample.document.blocks[1]!.heading = headings[1]
      sample.document.blocks[0]!.body = 'Alpha'
      sample.document.blocks[1]!.body = 'Beta'
      const view = await mount(sample)
      expect(enabled(view.api.canUndo)).toBe(false)
      view.el.querySelector<HTMLElement>('[aria-label="Überschrift Textbaustein 1"]')?.focus()
      await nextTick()
      view.api.moveSection(1)
      await nextTick()
      expect(bodies(sample)).toEqual(['Beta', 'Alpha'])
      expect(enabled(view.api.canUndo)).toBe(true)
      view.api.undo()
      expect(bodies(sample)).toEqual(['Alpha', 'Beta'])
      view.api.redo()
      expect(bodies(sample)).toEqual(['Beta', 'Alpha'])
      expect(enabled(view.api.canUndo)).toBe(true)
      view.unmount()
    }
    await reorder(['', ''])
    await reorder(['Gleich', 'Gleich'])
  })

  it('renders a deleted section and an undone insertion without a stale block index', async () => {
    installFonts()
    const sample = reactive(offer())
    const view = await mount(sample)
    const headings = () =>
      [...view.el.querySelectorAll('.sheet .sec h3')].map((node) => node.textContent?.trim())
    expect(headings()).toEqual(['Eins', 'Zwei'])
    view.api.askDeleteSection(1)
    await nextTick()
    view.el.querySelector<HTMLButtonElement>('.section-delete button')?.click()
    await nextTick()
    expect(sample.document.blocks.map((block) => block.heading)).toEqual(['Eins'])
    expect(headings()).toEqual(['Eins'])
    await nextTick()
    expect(headings()).toEqual(['Eins'])
    view.api.undo()
    await nextTick()
    expect(sample.document.blocks.map((block) => block.heading)).toEqual(['Eins', 'Zwei'])
    expect(headings()).toEqual(['Eins', 'Zwei'])
    view.api.redo()
    await nextTick()
    expect(sample.document.blocks.map((block) => block.heading)).toEqual(['Eins'])
    expect(headings()).toEqual(['Eins'])
    view.api.addSection()
    await nextTick()
    expect(sample.document.blocks).toHaveLength(2)
    expect(headings()).toHaveLength(2)
    view.api.undo()
    await nextTick()
    expect(sample.document.blocks.map((block) => block.heading)).toEqual(['Eins'])
    expect(headings()).toEqual(['Eins'])
    view.api.redo()
    await nextTick()
    expect(sample.document.blocks).toHaveLength(2)
    expect(headings()).toHaveLength(2)
    view.unmount()
  })
})
