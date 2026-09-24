import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import OfferInspector from './OfferInspector.vue'
import type { OfferSelection } from './types'

function button(root: ParentNode, label: string) {
  return [...root.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
}

async function mount(selection: OfferSelection, blockCount = 2) {
  const events: string[] = []
  const el = document.createElement('div')
  document.body.appendChild(el)
  const open = ref(true)
  const Host = defineComponent({
    setup() {
      return () =>
        h(OfferInspector, {
          open: open.value,
          footer: { logo_width_mm: 43.3, logo_offset_mm: 2 },
          canUndo: false,
          canRedo: true,
          selection,
          blockCount,
          onInsertSection: () => events.push('section'),
          onInsertPosition: () => events.push('position'),
          onSectionUp: () => events.push('up'),
          onSectionDown: () => events.push('down'),
          onAction: (id: string) => events.push(id),
        })
    },
  })
  const app = createApp(Host)
  app.mount(el)
  await nextTick()
  return {
    el,
    events,
    open,
    async hide() {
      open.value = false
      await nextTick()
    },
    unmount() {
      app.unmount()
      el.remove()
    },
  }
}

describe('offer inspector', () => {
  it('keeps insertion available and hides section tools until a section is selected', async () => {
    const css = readFileSync('src/components/offers/offer-document.css', 'utf8')
    const view = readFileSync('src/views/v6/OfferView.vue', 'utf8')
    expect(css).toMatch(/\.sheet-frame\s*\{[^}]*width:\s*max-content/)
    expect(css).toMatch(/\.sheet-frame\s*\{[^}]*min-width:\s*100%/)
    expect(css).not.toMatch(/\.sheet\s*\{[^}]*justify-items:\s*center/)
    expect(css).toMatch(/\.sheet \.page\s*\{[^}]*zoom:\s*var\(--offer-zoom/)
    expect(css).toMatch(/@media print\s*\{[\s\S]*\.offer-inspector\s*,/)
    expect(view).toContain('width: 340px')
    const none = await mount({ kind: 'none' })
    expect(none.el.querySelector('#offer-inspector')?.hasAttribute('hidden')).toBe(false)
    expect(button(none.el, 'Abschnitt am Ende')).toBeTruthy()
    expect(button(none.el, 'Leistungsposition')).toBeTruthy()
    expect(button(none.el, 'Nach oben')).toBeUndefined()
    expect(none.el.querySelector('[aria-label="Listentyp"]')).toBeNull()
    expect(
      none.el.querySelector('[aria-label="Breite des Fußzeilenlogos in Millimetern"]'),
    ).toBeNull()
    button(none.el, 'Leistungsposition')!.click()
    expect(none.events).toEqual(['position'])
    none.unmount()

    const heading = await mount({ kind: 'heading', index: 0, count: 2, heading: 'Leistung' })
    expect(heading.el.querySelector('h2')?.textContent).toBe('Abschnitt 1')
    expect(heading.el.textContent).toContain('Leistung')
    expect(button(heading.el, 'Abschnitt am Ende')).toBeUndefined()
    const up = heading.el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt nach oben"]')!
    const down = heading.el.querySelector<HTMLButtonElement>('[aria-label="Abschnitt nach unten"]')!
    expect(up.disabled).toBe(true)
    expect(down.disabled).toBe(false)
    down.click()
    expect(heading.events).toEqual(['down'])
    expect(heading.el.querySelector('#this-offer-heading')).toBeNull()
    heading.unmount()
  })

  it('labels the current footer separately from future templates and can hide', async () => {
    const mounted = await mount({ kind: 'footer' })
    expect(mounted.el.querySelector('#this-offer-heading')?.textContent).toBe(
      'Fußzeilenlogo dieses Angebots',
    )
    expect(mounted.el.textContent).toContain('nur für dieses Angebot')
    const templates = [...mounted.el.querySelectorAll('button')].find(
      (item) => item.textContent?.trim() === 'Vorlagen bearbeiten',
    )!
    expect(templates.textContent).not.toContain('Absender')
    expect(templates.closest('section')?.textContent).toContain('Dieses Angebot bleibt unverändert')
    expect(templates.closest('[aria-label="Vorlagen für neue Angebote"]')).toBeTruthy()
    expect(templates.closest('.this-offer')).toBeNull()
    templates.click()
    expect(mounted.events).toEqual(['settings'])
    await mounted.hide()
    expect(mounted.el.querySelector('#offer-inspector')?.hasAttribute('hidden')).toBe(true)
    mounted.unmount()
  })

  it('clears a numeric draft before undo so blur cannot write it back', async () => {
    const footer = ref({ logo_width_mm: 43.3, logo_offset_mm: 2 })
    const stack: { logo_width_mm: number; logo_offset_mm: number }[] = []
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        return () =>
          h(OfferInspector, {
            open: true,
            footer: footer.value,
            canUndo: true,
            canRedo: false,
            selection: { kind: 'footer' },
            blockCount: 0,
            onFooter: (value: { logo_width_mm: number; logo_offset_mm: number }) => {
              if (
                value.logo_offset_mm === footer.value.logo_offset_mm &&
                value.logo_width_mm === footer.value.logo_width_mm
              )
                return
              stack.push({ ...footer.value })
              footer.value = value
            },
            onUndo: () => {
              const previous = stack.pop()
              if (previous) footer.value = previous
            },
          })
      },
    })
    const app = createApp(Host)
    app.mount(el)
    await nextTick()
    const offset = el.querySelector<HTMLInputElement>(
      '[aria-label="Vertikaler Versatz des Fußzeilenlogos in Millimetern. Negativ nach oben, positiv nach unten."]',
    )!
    const type = (value: string) => {
      offset.focus()
      offset.value = value
      offset.dispatchEvent(new Event('input', { bubbles: true }))
    }
    type('-6')
    await nextTick()
    expect(footer.value.logo_offset_mm).toBe(-6)
    const undo = el.querySelector<HTMLButtonElement>('[aria-label="Rückgängig"]')!
    undo.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
    undo.click()
    await nextTick()
    expect(footer.value.logo_offset_mm).toBe(2)
    offset.dispatchEvent(new FocusEvent('blur', { bubbles: true }))
    await nextTick()
    expect(footer.value.logo_offset_mm).toBe(2)
    type('-4')
    await nextTick()
    const key = new KeyboardEvent('keydown', {
      key: 'z',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    })
    offset.dispatchEvent(key)
    await nextTick()
    expect(key.defaultPrevented).toBe(true)
    expect(footer.value.logo_offset_mm).toBe(2)
    offset.dispatchEvent(new FocusEvent('blur', { bubbles: true }))
    await nextTick()
    expect(footer.value.logo_offset_mm).toBe(2)
    app.unmount()
    el.remove()
  })
})
