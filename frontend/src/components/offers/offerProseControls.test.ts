import { afterEach, describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import OfferInspector from './OfferInspector.vue'
import OfferProse from './OfferProse.vue'
import {
  createProseHistory,
  persistProse,
  proseNodes,
  type SectionEditorMemory,
} from './offerProse'
import { provideOfferProseSession } from './offerProseSession'
import type { OfferFooterLayout, OfferSelection, OfferTextNode } from './types'

const paragraph = (text: string): OfferTextNode => ({ kind: 'paragraph', text })
const item = (text: string): OfferTextNode => ({
  kind: 'item',
  text,
  marker: 'decimal',
  numbering: 'outline',
})

function place(root: ParentNode, index: number, offset: number) {
  const el = root.querySelector<HTMLElement>(`[data-text][data-index="${index}"]`)
  if (!el) throw new Error(`missing text ${index}`)
  const text = el.firstChild
  const target = text && text.nodeType === Node.TEXT_NODE ? text : el
  const range = document.createRange()
  range.setStart(target, target === el ? 0 : offset)
  range.collapse(true)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
  document.dispatchEvent(new Event('selectionchange'))
}

function key(root: HTMLElement, init: KeyboardEventInit) {
  root.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
}

function before(root: HTMLElement, inputType: string, data?: string) {
  root.dispatchEvent(
    new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType, data }),
  )
}

function button(root: ParentNode, label: string) {
  return [...root.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
}

function click(target: Element) {
  target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
  target.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))
}

describe('hosted prose history', () => {
  afterEach(() => {
    document.body.replaceChildren()
    window.getSelection()?.removeAllRanges()
  })

  async function mount(hosted: boolean) {
    const history = createProseHistory()
    const prose = ref<{ body: string; nodes: OfferTextNode[] | null }>({
      body: 'Alpha',
      nodes: null,
    })
    let undos = 0
    let redos = 0
    const memory: SectionEditorMemory = {
      history,
      caret: null,
      requestUndo() {
        undos += 1
        const prev = history.undo({
          nodes: proseNodes(prose.value.body, prose.value.nodes),
          caret: { index: 0, offset: prose.value.body.length },
        })
        if (!prev) return
        const stored = persistProse(prev.nodes)
        prose.value = { body: stored.body, nodes: stored.nodes ?? null }
      },
      requestRedo() {
        redos += 1
      },
    }
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp({
      setup() {
        return () =>
          h(OfferProse, {
            body: prose.value.body,
            nodes: prose.value.nodes,
            editable: true,
            label: 'Textbaustein 1',
            memory: hosted ? memory : null,
            onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
              prose.value = { body: value.body, nodes: value.nodes ?? null }
            },
          })
      },
    })
    app.mount(el)
    await nextTick()
    const root = el.querySelector<HTMLElement>('.offer-prose')!
    return {
      el,
      root,
      prose,
      undos: () => undos,
      redos: () => redos,
      unmount() {
        app.unmount()
        el.remove()
      },
    }
  }

  it('keeps local redo when the prose is not hosted', async () => {
    const mounted = await mount(false)
    place(mounted.root, 0, 5)
    before(mounted.root, 'insertText', 'X')
    await nextTick()
    expect(mounted.prose.value.body).toBe('AlphaX')
    key(mounted.root, { key: 'z', ctrlKey: true })
    await nextTick()
    expect(mounted.prose.value.body).toBe('Alpha')
    key(mounted.root, { key: 'y', ctrlKey: true })
    await nextTick()
    expect(mounted.prose.value.body).toBe('AlphaX')
    before(mounted.root, 'historyUndo')
    await nextTick()
    expect(mounted.prose.value.body).toBe('Alpha')
    before(mounted.root, 'historyRedo')
    await nextTick()
    expect(mounted.prose.value.body).toBe('AlphaX')
    mounted.unmount()
  })

  it('routes undo and redo through document history when hosted', async () => {
    const mounted = await mount(true)
    place(mounted.root, 0, 5)
    before(mounted.root, 'insertText', 'X')
    await nextTick()
    expect(mounted.prose.value.body).toBe('AlphaX')
    key(mounted.root, { key: 'z', ctrlKey: true })
    await nextTick()
    expect(mounted.undos()).toBe(1)
    expect(mounted.prose.value.body).toBe('Alpha')
    key(mounted.root, { key: 'y', ctrlKey: true })
    await nextTick()
    key(mounted.root, { key: 'y', metaKey: true })
    await nextTick()
    before(mounted.root, 'historyRedo')
    await nextTick()
    before(mounted.root, 'historyUndo')
    await nextTick()
    expect(mounted.undos()).toBe(2)
    expect(mounted.redos()).toBe(3)
    expect(mounted.prose.value.body).toBe('Alpha')
    expect(mounted.root.textContent).toBe('Alpha')
    mounted.unmount()
  })
})

describe('offer level controls and this-offer settings', () => {
  afterEach(() => {
    document.body.replaceChildren()
    window.getSelection()?.removeAllRanges()
  })

  async function mount(
    nodes: OfferTextNode[],
    selection: OfferSelection = {
      kind: 'text',
      index: 0,
      count: 1,
      heading: 'Leistung',
    },
    sectionNumber = 0,
  ) {
    const footer = ref<OfferFooterLayout>({ logo_width_mm: 43.3, logo_offset_mm: 2 })
    const actions: string[] = []
    const footerSelects = 0
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
              footer: footer.value,
              canUndo: false,
              canRedo: false,
              selection,
              blockCount: 1,
              onFooter: (value: OfferFooterLayout) => {
                footer.value = value
              },
              onAction: (id: string) => actions.push(id),
            }),
            h(OfferProse, {
              body: prose.value.body,
              nodes: prose.value.nodes,
              editable: true,
              label: 'Textbaustein 1',
              sectionNumber,
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
    return {
      el,
      footer,
      actions,
      footerSelects: () => footerSelects,
      root: el.querySelector<HTMLElement>('.offer-prose')!,
      unmount() {
        app.unmount()
        el.remove()
      },
    }
  }

  it('disables higher and lower level buttons when the selection cannot move', async () => {
    const mounted = await mount([item('A'), item('B')])
    const deeper = () => button(mounted.el, 'Einrücken') as HTMLButtonElement
    const higher = () => button(mounted.el, 'Ausrücken') as HTMLButtonElement
    expect(deeper().disabled).toBe(true)
    expect(higher().disabled).toBe(true)
    mounted.root.focus()
    mounted.root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    place(mounted.root, 1, 0)
    await nextTick()
    expect(deeper().disabled).toBe(false)
    expect(higher().disabled).toBe(false)
    expect(deeper().getAttribute('aria-label')).toBe('Eine Listenebene tiefer')
    expect(mounted.el.textContent).toContain('Listenebene 1 · 0× eingerückt')
    place(mounted.root, 0, 0)
    await nextTick()
    expect(deeper().disabled).toBe(false)
    expect(higher().disabled).toBe(false)
    expect(deeper().getAttribute('aria-label')).toBe('Eine Listenebene tiefer')
    expect(higher().getAttribute('aria-label')).toBe('Eine Listenebene höher')
    expect(mounted.el.textContent).toContain('Listenebene 1 · 0× eingerückt')
    const kept = window.getSelection()?.focusOffset
    deeper().dispatchEvent(
      new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }),
    )
    expect(window.getSelection()?.focusOffset).toBe(kept)
    click(deeper())
    await nextTick()
    expect(mounted.el.textContent).toContain('Listenebene 2 · 1× eingerückt')
    expect(mounted.root.querySelector<HTMLElement>('[data-node="0"]')?.style.getPropertyValue('--depth')).toBe(
      '1',
    )
    click(higher())
    await nextTick()
    expect(mounted.el.textContent).toContain('Listenebene 1 · 0× eingerückt')
    mounted.unmount()
  })

  it('places a custom bullet and keeps a long outline number on the selected items', async () => {
    const mounted = await mount(
      [
        {
          kind: 'item',
          text: 'Planungsrahmen',
          marker: 'decimal',
          numbering: 'outline',
          section_bound: true,
          list_start: 4,
        },
        { kind: 'item', text: 'Projektstart', marker: 'circle', depth: 1 },
        { kind: 'item', text: 'Monat', marker: 'square', depth: 2 },
        {
          kind: 'item',
          text: 'Weiter',
          marker: 'decimal',
          numbering: 'outline',
          section_bound: true,
        },
      ],
      { kind: 'text', index: 4, count: 5, heading: 'Planung' },
      5,
    )
    mounted.root.focus()
    mounted.root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    place(mounted.root, 1, 0)
    await nextTick()
    expect(mounted.el.textContent).toContain('Listenebene 2 · 1× eingerückt')
    expect(button(mounted.el, 'Einrücken')?.hasAttribute('disabled')).toBe(false)
    const glyph = mounted.el.querySelector<HTMLInputElement>(
      '[aria-label="Eigenes Aufzählungszeichen"]',
    )!
    glyph.value = '✓'
    glyph.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(mounted.root.querySelector('[data-node="1"]')?.getAttribute('data-bullet')).toBe('✓')
    glyph.value = '<b>x</b>'
    glyph.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(mounted.root.querySelector('[data-node="1"]')?.getAttribute('data-bullet')).toBe('✓')
    glyph.blur()
    await nextTick()
    expect(mounted.root.querySelector('[data-node="1"]')?.getAttribute('data-bullet')).toBe('✓')
    click(mounted.el.querySelector('[aria-label="Zeichen horizontal nach rechts"]')!)
    click(mounted.el.querySelector('[aria-label="Textbeginn nach rechts"]')!)
    await nextTick()
    const moved = mounted.root.querySelector<HTMLElement>('[data-node="1"]')!
    expect(moved.style.getPropertyValue('--marker-x')).toBe('0.5mm')
    expect(moved.style.getPropertyValue('--text-start')).toBe('0.5mm')
    expect(moved.getAttribute('data-marker')).toBe('circle')
    const horizontal = mounted.el.querySelector<HTMLInputElement>('[aria-label="Zeichen horizontal"]')!
    horizontal.value = '80'
    horizontal.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(moved.style.getPropertyValue('--marker-x')).toBe('0.5mm')
    click(button(mounted.el, 'Standard')!)
    await nextTick()
    expect(moved.style.getPropertyValue('--marker-x')).toBe('')
    expect(moved.getAttribute('data-bullet')).toBe('◦')
    click(button(mounted.el, 'Einrücken')!)
    await nextTick()
    expect(moved.style.getPropertyValue('--depth')).toBe('2')
    expect(moved.getAttribute('data-bullet')).toBe('◦')
    expect(mounted.el.textContent).toContain('Listenebene 3 · 2× eingerückt')
    click(button(mounted.el, 'Ausrücken')!)
    await nextTick()
    expect(moved.style.getPropertyValue('--depth')).toBe('1')
    const span = document.createRange()
    const from = mounted.root.querySelector('[data-text][data-index="1"]')!.firstChild!
    const to = mounted.root.querySelector('[data-text][data-index="2"]')!.firstChild!
    span.setStart(from, 0)
    span.setEnd(to, 1)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(span)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    expect(mounted.el.textContent).toContain('Gemischte Ebenen')
    place(mounted.root, 0, 0)
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Eigenes Aufzählungszeichen"]')).toBeNull()
    const parent = mounted.root.querySelector<HTMLElement>('[data-node="0"]')!
    const child = mounted.root.querySelector<HTMLElement>('[data-node="3"]')
    expect(parent.getAttribute('data-bullet')).toBe('5.4')
    expect(parent.getAttribute('data-numbering')).toBe('outline')
    expect(parent.style.getPropertyValue('--outline-col')).toBe('3ch')
    expect(parent.style.getPropertyValue('--outline-indent')).toBe('calc(0ch + 0 * 0.4em)')
    expect(child?.getAttribute('data-bullet')).toBe('5.5')
    expect(child?.style.getPropertyValue('--outline-col')).toBe('3ch')
    expect(mounted.root.querySelector('[data-node="1"]')?.getAttribute('style') ?? '').not.toContain(
      '--outline-col',
    )
    expect(mounted.el.textContent).toContain('Listenebene 1 · 0× eingerückt')
    mounted.unmount()
  })

  it('edits this offer footer from the inspector without opening templates', async () => {
    const mounted = await mount([paragraph('Alpha')], { kind: 'footer' })
    expect(mounted.el.querySelector('#this-offer-heading')?.textContent).toBe(
      'Fußzeilenlogo dieses Angebots',
    )
    expect(mounted.footerSelects()).toBe(0)
    const offset = mounted.el.querySelector<HTMLInputElement>(
      '[aria-label="Vertikaler Versatz des Fußzeilenlogos in Millimetern. Negativ nach oben, positiv nach unten."]',
    )!
    offset.value = '-6'
    offset.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(mounted.footer.value.logo_offset_mm).toBe(-6)
    expect(mounted.footerSelects()).toBe(0)
    const templates = [...mounted.el.querySelectorAll('button')].find(
      (item) => item.textContent?.trim() === 'Vorlagen bearbeiten',
    )!
    expect(templates.closest('[aria-label="Vorlagen für neue Angebote"]')).toBeTruthy()
    expect(templates.closest('.this-offer')).toBeNull()
    templates.click()
    await nextTick()
    expect(mounted.actions).toEqual(['settings'])
    expect(mounted.footerSelects()).toBe(0)
    mounted.unmount()
  })
})
