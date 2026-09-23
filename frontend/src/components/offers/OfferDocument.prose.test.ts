import { afterEach, describe, expect, it } from 'vitest'
import { createApp, nextTick } from 'vue'
import OfferDocument from './OfferDocument.vue'
import type { Offer, OfferTextNode } from './types'

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

function offer(blocks: { heading: string; body: string; nodes?: OfferTextNode[] }[]): Offer {
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
      blocks,
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
      positions: [
        {
          short_text: 'Analyse',
          long_text: 'Beschreibung',
          quantity: 1,
          unit: 'Pauschale',
          unit_price_cents: 10000,
          total_cents: 10000,
        },
      ],
    },
  }
}

describe('offer document prose', () => {
  afterEach(() => {
    HTMLElement.prototype.getBoundingClientRect = originalRect
    document.body.innerHTML = ''
  })

  function installFonts() {
    Object.defineProperty(document, 'fonts', {
      configurable: true,
      value: { ready: Promise.resolve() },
    })
  }

  it('draws list markup in the measure tree and the page, and keeps an oversized block', async () => {
    HTMLElement.prototype.getBoundingClientRect = function () {
      const el = this as HTMLElement
      if (el.classList.contains('page-content')) return rect(1000)
      if (el.classList.contains('offer-cover')) return rect(120)
      if (el.dataset.sectionHeading != null) return rect(24)
      if (el.hasAttribute('data-page-inset')) return rect(16)
      if (el.classList.contains('offer-acceptance')) return rect(40)
      if (el.dataset.block === '1') return rect(4000)
      if (el.dataset.block != null) return rect(80)
      if (el.dataset.position != null) return rect(24)
      if (el.tagName === 'THEAD') return rect(18)
      return rect(8)
    }
    installFonts()
    const el = document.createElement('div')
    document.body.appendChild(el)
    const sample = offer([
      {
        heading: 'Leistung',
        body: 'ignored',
        nodes: [
          { kind: 'paragraph', text: 'Einleitung.' },
          { kind: 'item', text: 'Analyse' },
          { kind: 'item', text: 'Interviews', depth: 1 },
        ],
      },
      { heading: 'Alt', body: '- Punkt\n- Zweiter <script>alert(1)</script>' },
    ])
    const app = createApp(OfferDocument, { offer: sample, editable: true })
    const vm = app.mount(el) as unknown as { paginate: () => Promise<void> }
    await document.fonts.ready
    await vm.paginate()
    await nextTick()
    const measure = el.querySelector('.offer-measure')!
    const sheet = el.querySelector('.sheet')!
    expect(measure.querySelector('[data-bullet="•"]')?.textContent).toContain('Analyse')
    expect(measure.querySelector('[data-bullet="◦"]')?.textContent).toContain('Interviews')
    expect(measure.textContent).toContain('- Punkt\n- Zweiter <script>alert(1)</script>')
    expect(measure.querySelector('script')).toBeNull()
    expect(sheet.textContent).toContain('Analyse')
    expect(sheet.textContent).toContain('- Punkt')
    expect(sheet.textContent).toContain('Interviews')
    expect(el.querySelector('.offer-document')?.getAttribute('data-print-blocked')).toContain(
      'Textbaustein 2 ist länger als eine Seite',
    )
    expect(getComputedStyle(measure.querySelector('.sec')!).overflow).not.toBe('hidden')
    app.unmount()
  })
})
