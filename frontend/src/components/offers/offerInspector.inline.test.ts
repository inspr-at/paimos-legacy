import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import OfferInlineStyleControls from './OfferInlineStyleControls.vue'
import OfferInspector from './OfferInspector.vue'
import OfferProse from './OfferProse.vue'
import { OFFER_PROSE_WRITER_VERSION } from './offerProse'
import { provideOfferProseSession } from './offerProseSession'
import type { OfferTextNode } from './types'

function click(target: Element) {
  target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
  target.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))
}

describe('inspector inline styles', () => {
  it('mounts the real character controls and keeps the selected range', async () => {
    const source = readFileSync('src/components/offers/OfferInspector.vue', 'utf8')
    expect(source).toContain('OfferInlineStyleControls')
    expect(source).toMatch(/\.zeichen :deep\(\.offer-inline-styles button\)\s*\{[^}]*white-space:\s*nowrap/)
    expect(readFileSync('src/components/offers/OfferInlineStyleControls.vue', 'utf8')).toContain(
      'height: 32px',
    )
    expect(OFFER_PROSE_WRITER_VERSION).toBe(3)

    const prose = ref<{ body: string; nodes: OfferTextNode[] | null }>({
      body: 'Hello',
      nodes: null,
    })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        provideOfferProseSession()
        return () =>
          h('div', [
            h(OfferInspector, {
              open: true,
              footer: { logo_width_mm: 43.3, logo_offset_mm: 0 },
              selection: { kind: 'text', index: 0, count: 1, heading: 'Leistung' },
              blockCount: 1,
            }),
            h(OfferProse, {
              body: prose.value.body,
              nodes: prose.value.nodes,
              editable: true,
              label: 'Textbaustein 1',
              onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
                prose.value = { body: value.body, nodes: value.nodes ?? null }
              },
            }),
          ])
      },
    })
    const app = createApp(Host)
    app.mount(el)
    await nextTick()

    const inspector = el.querySelector('#offer-inspector')!
    const styles = inspector.querySelector('.offer-inline-styles')!
    expect(styles).toBeInstanceOf(HTMLElement)
    expect(inspector.querySelector('.zeichen')?.contains(styles)).toBe(true)
    expect(styles.querySelector('[data-v-app]')).toBeNull()
    expect(OfferInlineStyleControls).toBeTruthy()
    const buttons = [...styles.querySelectorAll('button')]
    expect(buttons.map((button) => button.textContent?.trim())).toEqual(['Normal', 'Fett', 'Kursiv'])
    expect(buttons.every((button) => !button.textContent?.includes('\n'))).toBe(true)

    const root = el.querySelector<HTMLElement>('.offer-prose')!
    root.focus()
    root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    const text = root.querySelector<HTMLElement>('[data-text][data-index="0"]')!
    const node = text.firstChild!
    const range = document.createRange()
    range.setStart(node, 1)
    range.setEnd(node, 4)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()

    const fett = buttons.find((button) => button.textContent?.trim() === 'Fett')!
    const kursiv = buttons.find((button) => button.textContent?.trim() === 'Kursiv')!
    expect(fett.getAttribute('aria-pressed')).toBe('false')
    expect(fett.disabled).toBe(false)
    fett.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
    expect(selection.focusOffset).toBe(4)
    expect(selection.anchorOffset).toBe(1)
    click(fett)
    await nextTick()
    expect(prose.value.nodes?.[0]?.marks).toEqual([{ start: 1, end: 4, bold: true }])
    expect(text.querySelector('b')?.textContent).toBe('ell')
    expect(fett.getAttribute('aria-pressed')).toBe('true')
    expect(kursiv.getAttribute('aria-pressed')).toBe('false')

    const boldText = text.querySelector('b')!.firstChild!
    const again = document.createRange()
    again.setStart(boldText, 0)
    again.setEnd(boldText, 3)
    selection.removeAllRanges()
    selection.addRange(again)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    const keptAnchor = selection.anchorOffset
    const keptFocus = selection.focusOffset
    const indent = [...inspector.querySelectorAll('button')].find(
      (button) => button.textContent?.trim() === 'Einrücken',
    )!
    indent.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
    expect(selection.anchorOffset).toBe(keptAnchor)
    expect(selection.focusOffset).toBe(keptFocus)
    click(kursiv)
    await nextTick()
    expect(prose.value.nodes?.[0]?.marks).toEqual([{ start: 1, end: 4, bold: true, italic: true }])
    expect(text.querySelector('i, b i, i b')).toBeTruthy()
    expect(kursiv.getAttribute('aria-pressed')).toBe('true')

    app.unmount()
    el.remove()
    window.getSelection()?.removeAllRanges()
  })

  it('shows the computed marker 2.3 instead of a fabricated 2.1', async () => {
    const nodes: OfferTextNode[] = [0, 1, 2].map((index) => ({
      kind: 'item',
      text: `Punkt ${index + 1}`,
      marker: 'decimal',
      numbering: 'outline',
      section_bound: true,
      ...(index > 0 ? { list_continue: true } : {}),
    }))
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        provideOfferProseSession()
        const prose = ref({ body: nodes.map((node) => node.text).join('\n'), nodes })
        return () =>
          h('div', [
            h(OfferInspector, {
              open: true,
              footer: null,
              selection: { kind: 'text', index: 1, count: 2, heading: 'Leistung' },
              blockCount: 2,
            }),
            h(OfferProse, {
              body: prose.value.body,
              nodes: prose.value.nodes,
              editable: true,
              label: 'Textbaustein 2',
              sectionNumber: 2,
              onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
                prose.value = { body: value.body, nodes: value.nodes ?? prose.value.nodes }
              },
            }),
          ])
      },
    })
    const app = createApp(Host)
    app.mount(el)
    await nextTick()
    const root = el.querySelector<HTMLElement>('.offer-prose')!
    root.focus()
    root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    const text = root.querySelector<HTMLElement>('[data-text][data-index="2"]')!
    const range = document.createRange()
    range.setStart(text.firstChild ?? text, 0)
    range.collapse(true)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    expect(text.parentElement?.getAttribute('data-bullet') ?? root.querySelector('[data-node="2"]')?.getAttribute('data-bullet')).toBe('2.3')
    expect(el.querySelector('.num-example')?.textContent).toBe('2.3')
    app.unmount()
    el.remove()
  })

  it('applies the chosen typing style to a composed character', async () => {
    const prose = ref<{ body: string; nodes: OfferTextNode[] | null }>({ body: 'Hi', nodes: null })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        provideOfferProseSession()
        return () =>
          h('div', [
            h(OfferInspector, {
              open: true,
              footer: null,
              selection: { kind: 'text', index: 0, count: 1, heading: 'Leistung' },
              blockCount: 1,
            }),
            h(OfferProse, {
              body: prose.value.body,
              nodes: prose.value.nodes,
              editable: true,
              label: 'Textbaustein 1',
              onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
                prose.value = { body: value.body, nodes: value.nodes ?? null }
              },
            }),
          ])
      },
    })
    const app = createApp(Host)
    app.mount(el)
    await nextTick()
    const root = el.querySelector<HTMLElement>('.offer-prose')!
    root.focus()
    root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    const text = root.querySelector<HTMLElement>('[data-text][data-index="0"]')!
    const range = document.createRange()
    range.setStart(text.firstChild ?? text, 2)
    range.collapse(true)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    const fett = [...el.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Fett')!
    click(fett)
    await nextTick()
    text.textContent = 'Hié'
    root.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
    await nextTick()
    expect(prose.value.nodes?.[0]?.text).toBe('Hié')
    expect(prose.value.nodes?.[0]?.marks).toEqual([{ start: 2, end: 3, bold: true }])
    app.unmount()
    el.remove()
  })

  it('bolds a composed é inserted before a plain é', async () => {
    const prose = ref<{ body: string; nodes: OfferTextNode[] | null }>({ body: 'é', nodes: null })
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        provideOfferProseSession()
        return () =>
          h('div', [
            h(OfferInspector, {
              open: true,
              footer: null,
              selection: { kind: 'text', index: 0, count: 1, heading: 'Leistung' },
              blockCount: 1,
            }),
            h(OfferProse, {
              body: prose.value.body,
              nodes: prose.value.nodes,
              editable: true,
              label: 'Textbaustein 1',
              onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
                prose.value = { body: value.body, nodes: value.nodes ?? null }
              },
            }),
          ])
      },
    })
    const app = createApp(Host)
    app.mount(el)
    await nextTick()
    const root = el.querySelector<HTMLElement>('.offer-prose')!
    root.focus()
    root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    const text = root.querySelector<HTMLElement>('[data-text][data-index="0"]')!
    const place = (offset: number, end = offset) => {
      const range = document.createRange()
      range.setStart(text.firstChild ?? text, offset)
      range.setEnd(text.firstChild ?? text, end)
      window.getSelection()?.removeAllRanges()
      window.getSelection()?.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))
    }
    place(0)
    await nextTick()
    const fett = [...el.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Fett')!
    click(fett)
    await nextTick()
    root.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    text.textContent = 'éé'
    place(1)
    root.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: 'é' }))
    await nextTick()
    expect(prose.value.nodes?.[0]?.text).toBe('éé')
    expect(prose.value.nodes?.[0]?.marks).toEqual([{ start: 0, end: 1, bold: true }])
    app.unmount()
    el.remove()
  })
})
