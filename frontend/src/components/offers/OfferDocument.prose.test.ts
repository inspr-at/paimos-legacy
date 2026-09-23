import { afterEach, describe, expect, it } from 'vitest'
import { createApp, nextTick, reactive } from 'vue'
import { readFileSync } from 'node:fs'
import OfferDocument from './OfferDocument.vue'
import { footerLogoBox } from './offerLayout'
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

  it('scales only the centered footer mark and leaves the row labels in place', async () => {
    HTMLElement.prototype.getBoundingClientRect = function () {
      const el = this as HTMLElement
      if (el.classList.contains('page-content')) return rect(1000)
      return rect(40)
    }
    installFonts()
    const sample = reactive(offer([{ heading: 'Leistung', body: 'Kurz.' }]))
    sample.document.sender.company = 'Augmentoring GmbH'
    const plain = document.createElement('div')
    document.body.appendChild(plain)
    const plainApp = createApp(OfferDocument, { offer: sample, editable: false })
    const plainVm = plainApp.mount(plain) as unknown as { paginate: () => Promise<void> }
    await document.fonts.ready
    await plainVm.paginate()
    await nextTick()
    const plainMark = plain.querySelector<HTMLElement>('.sheet .ftr .footmark')!
    expect(plainMark.classList.contains('is-set')).toBe(false)
    expect(getComputedStyle(plainMark).position).not.toBe('absolute')
    expect(getComputedStyle(plainMark.querySelector('.lockup')!).position).not.toBe('absolute')
    plainApp.unmount()
    sample.document.footer = { logo_width_mm: 96, logo_offset_mm: 10 }
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferDocument, { offer: sample, editable: false })
    const vm = app.mount(el) as unknown as { paginate: () => Promise<void> }
    await vm.paginate()
    await nextTick()
    const footer = el.querySelector<HTMLElement>('.sheet .ftr')!
    const mark = footer.querySelector<HTMLElement>('.footmark')!
    const box = footerLogoBox(96)
    const footerCss = readFileSync('src/components/offers/offer-document.css', 'utf8')
    expect(mark.classList.contains('is-set')).toBe(true)
    expect(mark.style.getPropertyValue('--mark-offset')).toBe('10mm')
    expect(mark.style.getPropertyValue('--word-w')).toBe(`${box.wordmarkWidthMm}mm`)
    expect(mark.style.getPropertyValue('--word-h')).toBe(`${box.wordmarkHeightMm}mm`)
    expect(footerCss).toMatch(/\.ftr \.footmark\.is-set\s*\{[^}]*height:\s*1em/)
    expect(footerCss).toMatch(/\.ftr \.footmark\.is-set \.lockup\s*\{[^}]*position:\s*absolute/)
    expect(footerCss).toMatch(
      /\.ftr \.footmark\.is-set \.lockup\s*\{[^}]*width:\s*max-content;[^}]*white-space:\s*nowrap/,
    )
    expect(footerCss).toContain('translate(-50%, calc(-50% + var(--mark-offset, 0mm)))')
    const named = offer([{ heading: 'Leistung', body: 'Kurz.' }])
    named.document.sender.company = 'Testberatung GmbH'
    named.document.footer = { logo_width_mm: 55, logo_offset_mm: 2 }
    const namedEl = document.createElement('div')
    document.body.appendChild(namedEl)
    const namedApp = createApp(OfferDocument, { offer: named, editable: false })
    const namedVm = namedApp.mount(namedEl) as unknown as { paginate: () => Promise<void> }
    await namedVm.paginate()
    await nextTick()
    const nameLockup = namedEl.querySelector<HTMLElement>('.sheet .ftr .footmark.is-set .lockup')!
    expect(nameLockup.textContent).toBe('Testberatung GmbH')
    expect(nameLockup.querySelector('*')).toBeNull()
    expect(nameLockup.getAttribute('role')).toBeNull()
    expect(namedEl.querySelector('[role="button"]')).toBeNull()
    namedApp.unmount()
    expect(footer.querySelector('.right')?.getAttribute('style')).toBeNull()
    expect(footer.querySelector('span')?.getAttribute('style')).toBeNull()
    sample.document.footer.logo_offset_mm = -6
    await nextTick()
    expect(mark.style.getPropertyValue('--mark-offset')).toBe('-6mm')
    const editableEl = document.createElement('div')
    document.body.appendChild(editableEl)
    const editableApp = createApp(OfferDocument, { offer: sample, editable: true })
    const editableVm = editableApp.mount(editableEl) as unknown as {
      paginate: () => Promise<void>
      selectFooter: () => void
    }
    await editableVm.paginate()
    await nextTick()
    const editableFooter = editableEl.querySelector<HTMLElement>('.sheet .ftr')!
    const editableMark = editableFooter.querySelector<HTMLElement>('.footmark')!
    const hit = editableFooter.querySelector<HTMLElement>('.footmark .lockup')!
    expect(editableMark.getAttribute('role')).toBeNull()
    expect(hit.getAttribute('role')).toBe('button')
    expect(hit.getAttribute('aria-label')).toBe('Fußzeilenlogo auswählen')
    expect(hit.getAttribute('tabindex')).toBe('0')
    hit.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    await nextTick()
    expect(document.activeElement).toBe(hit)
    const pages = [...editableEl.querySelectorAll<HTMLElement>('.sheet .page')]
    const hits = [
      ...editableEl.querySelectorAll<HTMLElement>('.sheet [aria-label="Fußzeilenlogo auswählen"]'),
    ]
    expect(pages.length).toBeGreaterThan(1)
    expect(hits).toHaveLength(pages.length)
    hits[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    expect(document.activeElement).toBe(hits[1])
    expect(document.activeElement?.closest('.page')?.getAttribute('aria-label')).toBe('Seite 2')
    hits[1]!.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    expect(document.activeElement).toBe(hits[1])
    editableVm.selectFooter()
    expect(document.activeElement).toBe(hits[0])
    editableApp.unmount()
    expect(footerCss).toMatch(/data-numbering='outline'/)
    expect(footerCss).toMatch(/\.section-tools/)
    app.unmount()
  })

  it('adds and moves only the current offer section', async () => {
    HTMLElement.prototype.getBoundingClientRect = function () {
      const el = this as HTMLElement
      if (el.classList.contains('page-content')) return rect(1000)
      return rect(40)
    }
    installFonts()
    const sample = reactive(
      offer([
        { heading: 'Eins', body: 'Alpha' },
        { heading: 'Zwei', body: 'Beta' },
      ]),
    )
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferDocument, { offer: sample, editable: true })
    const vm = app.mount(el) as unknown as { paginate: () => Promise<void> }
    await vm.paginate()
    await nextTick()
    el.querySelector<HTMLElement>('[aria-label="Überschrift Textbaustein 1"]')?.focus()
    await nextTick()
    const up = el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt nach oben"]')
    const down = el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt nach unten"]')
    expect(up?.disabled).toBe(true)
    expect(down?.disabled).toBe(false)
    down?.click()
    await nextTick()
    await nextTick()
    expect(sample.document.blocks.map((block) => block.heading)).toEqual(['Zwei', 'Eins'])
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Überschrift Textbaustein 2')
    el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt 1 danach hinzufügen"]')?.click()
    await nextTick()
    await nextTick()
    expect(sample.document.blocks.map((block) => block.heading)).toEqual(['Zwei', '', 'Eins'])
    expect(sample.document.blocks[1]).toEqual({ heading: '', body: '' })
    app.unmount()
    const locked = document.createElement('div')
    document.body.appendChild(locked)
    const lockedApp = createApp(OfferDocument, { offer: sample, editable: false })
    lockedApp.mount(locked)
    expect(locked.querySelector('.section-tools')).toBeNull()
    lockedApp.unmount()
  })

  it('undoes only the section that was edited after a move or insert', async () => {
    HTMLElement.prototype.getBoundingClientRect = function () {
      const el = this as HTMLElement
      if (el.classList.contains('page-content')) return rect(1000)
      return rect(40)
    }
    installFonts()
    const sample = reactive(
      offer([
        { heading: 'Eins', body: 'Alpha' },
        { heading: 'Zwei', body: 'Beta' },
      ]),
    )
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferDocument, { offer: sample, editable: true })
    const vm = app.mount(el) as unknown as { paginate: () => Promise<void> }
    await vm.paginate()
    await nextTick()
    const place = (root: HTMLElement, offset: number) => {
      root.focus()
      const text = root.querySelector('[data-text]')?.firstChild
      if (!text) throw new Error('missing section text')
      const range = document.createRange()
      range.setStart(text, offset)
      range.setEnd(text, offset)
      const selection = window.getSelection()
      selection?.removeAllRanges()
      selection?.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))
    }
    const type = (root: HTMLElement, data: string) => {
      root.dispatchEvent(
        new InputEvent('beforeinput', {
          bubbles: true,
          cancelable: true,
          inputType: 'insertText',
          data,
        }),
      )
    }
    const undo = (root: HTMLElement) => {
      root.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'z',
          metaKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
    }
    const bodies = () => sample.document.blocks.map((block) => block.body)
    const first = el.querySelector<HTMLElement>('[aria-label="Textbaustein 1"]')!
    const second = el.querySelector<HTMLElement>('[aria-label="Textbaustein 2"]')!
    place(first, 5)
    type(first, 'Y')
    place(second, 4)
    type(second, 'X')
    await nextTick()
    expect(bodies()).toEqual(['AlphaY', 'BetaX'])
    el.querySelector<HTMLElement>('[aria-label="Überschrift Textbaustein 1"]')?.focus()
    await nextTick()
    el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt nach unten"]')?.click()
    await nextTick()
    const moved = el.querySelector<HTMLElement>('[aria-label="Textbaustein 2"]')!
    expect(moved.textContent).toContain('AlphaY')
    undo(moved)
    await nextTick()
    expect(bodies()).toEqual(['AlphaY', 'BetaX'])
    undo(el.querySelector<HTMLElement>('[aria-label="Textbaustein 2"]')!)
    await nextTick()
    expect(bodies()).toEqual(['AlphaY', 'Beta'])
    undo(el.querySelector<HTMLElement>('[aria-label="Textbaustein 1"]')!)
    await nextTick()
    expect(bodies()).toEqual(['Alpha', 'Beta'])
    el.querySelector<HTMLElement>('[aria-label="Überschrift Textbaustein 1"]')?.focus()
    await nextTick()
    el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt 1 danach hinzufügen"]')?.click()
    await nextTick()
    await nextTick()
    expect(bodies()).toEqual(['Alpha', '', 'Beta'])
    undo(el.querySelector<HTMLElement>('[aria-label="Textbaustein 1"]')!)
    await nextTick()
    expect(bodies()).toEqual(['Alpha', 'Beta'])
    app.unmount()
  })

  it('keeps a section undo stack when the section changes page', async () => {
    HTMLElement.prototype.getBoundingClientRect = function () {
      const node = this as HTMLElement
      if (node.classList.contains('page-content')) return rect(260)
      if (node.classList.contains('offer-cover')) return rect(40)
      if (node.dataset.sectionHeading != null) return rect(24)
      if (node.hasAttribute('data-page-inset')) return rect(16)
      if (node.classList.contains('offer-acceptance')) return rect(20)
      if (node.dataset.block != null) {
        if (sample.document.blocks.length >= 3 && node.dataset.block === '1') return rect(400)
        return rect(150)
      }
      if (node.dataset.position != null) return rect(20)
      if (node.tagName === 'THEAD') return rect(16)
      return rect(8)
    }
    installFonts()
    const sample = reactive(
      offer([
        { heading: 'Eins', body: 'Alpha' },
        { heading: 'Zwei', body: 'Beta' },
      ]),
    )
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(OfferDocument, { offer: sample, editable: true })
    const vm = app.mount(el) as unknown as { paginate: () => Promise<void> }
    await vm.paginate()
    await nextTick()
    const place = (root: HTMLElement, offset: number) => {
      root.focus()
      const text = root.querySelector('[data-text]')?.firstChild
      if (!text) throw new Error('missing section text')
      const range = document.createRange()
      range.setStart(text, offset)
      range.setEnd(text, offset)
      window.getSelection()?.removeAllRanges()
      window.getSelection()?.addRange(range)
    }
    const type = (root: HTMLElement, data: string) => {
      root.dispatchEvent(
        new InputEvent('beforeinput', {
          bubbles: true,
          cancelable: true,
          inputType: 'insertText',
          data,
        }),
      )
    }
    const undo = (root: HTMLElement) => {
      root.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'z',
          metaKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
    }
    const bodies = () => sample.document.blocks.map((block) => block.body)
    const pageOf = (text: string) =>
      [...el.querySelectorAll('.sheet .page')].findIndex((page) => page.textContent?.includes(text))
    place(el.querySelector<HTMLElement>('[aria-label="Textbaustein 1"]')!, 5)
    type(el.querySelector<HTMLElement>('[aria-label="Textbaustein 1"]')!, 'Y')
    await nextTick()
    expect(bodies()).toEqual(['AlphaY', 'Beta'])
    expect(pageOf('AlphaY')).not.toBe(pageOf('Beta'))
    el.querySelector<HTMLElement>('[aria-label="Überschrift Textbaustein 1"]')?.focus()
    await nextTick()
    el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt nach unten"]')?.click()
    await vm.paginate()
    await nextTick()
    expect(bodies()).toEqual(['Beta', 'AlphaY'])
    expect(pageOf('AlphaY')).not.toBe(pageOf('Beta'))
    const moved = [...el.querySelectorAll('.sheet .page')]
      .find((page) => page.textContent?.includes('AlphaY'))
      ?.querySelector<HTMLElement>('.offer-prose')
    place(moved!, 6)
    type(moved!, 'Z')
    await nextTick()
    expect(bodies()).toEqual(['Beta', 'AlphaYZ'])
    undo(moved!)
    await nextTick()
    expect(bodies()).toEqual(['Beta', 'AlphaY'])
    app.unmount()
  })
})
