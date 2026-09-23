import { afterEach, describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import OfferProse from './OfferProse.vue'
import OfferTitleBar from './OfferTitleBar.vue'
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

  async function mount(nodes: OfferTextNode[]) {
    const selection: OfferSelection = { kind: 'text', index: 0, count: 1 }
    const footer = ref<OfferFooterLayout>({ logo_width_mm: 43.3, logo_offset_mm: 2 })
    const actions: string[] = []
    let footerSelects = 0
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        provideOfferProseSession()
        const prose = ref({ body: nodes.map((node) => node.text).join('\n'), nodes })
        return () =>
          h('div', [
            h(OfferTitleBar, {
              backTo: '/crm',
              backLabel: 'Zurück',
              offerNo: 'A260923-01',
              hostname: 'paimos.test',
              editable: true,
              busy: false,
              loading: false,
              dirty: false,
              saveFailed: false,
              state: 'Gespeichert',
              savedAt: '',
              savedAtIso: '',
              zoomMode: 'page',
              zoom: 1,
              printDisabled: false,
              showPrint: true,
              actions: [],
              footer: footer.value,
              selection,
              onFooter: (value: OfferFooterLayout) => {
                footer.value = value
              },
              onAction: (id: string) => actions.push(id),
              onSelectFooter: () => {
                footerSelects += 1
              },
            }),
            h(OfferProse, {
              body: prose.value.body,
              nodes: prose.value.nodes,
              editable: true,
              label: 'Textbaustein 1',
              onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
                prose.value = { body: value.body, nodes: value.nodes ?? prose.value.nodes }
              },
            }),
          ])
      },
    })
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<div />' } },
        { path: '/crm', component: { template: '<div />' } },
      ],
    })
    const app = createApp(Host)
    app.use(router)
    app.mount(el)
    await router.isReady()
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
    const deeper = () => button(mounted.el, 'Ebene tiefer') as HTMLButtonElement
    const higher = () => button(mounted.el, 'Ebene höher') as HTMLButtonElement
    expect(deeper().disabled).toBe(true)
    expect(higher().disabled).toBe(true)
    mounted.root.focus()
    mounted.root.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    place(mounted.root, 1, 0)
    await nextTick()
    expect(deeper().disabled).toBe(false)
    expect(higher().disabled).toBe(false)
    expect(deeper().getAttribute('aria-label')).toBe('Eine Listenebene tiefer')
    place(mounted.root, 0, 0)
    await nextTick()
    expect(deeper().disabled).toBe(true)
    expect(higher().disabled).toBe(false)
    expect(deeper().parentElement?.getAttribute('data-tip')).toBe('Kein vorheriger Listeneintrag.')
    expect(deeper().getAttribute('aria-label')).toContain('Kein vorheriger Listeneintrag.')
    expect(higher().getAttribute('aria-label')).toBe('Eine Listenebene höher')
    const list = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Listen und Punkte"]')!
    list.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
    list.click()
    await nextTick()
    const indent = button(mounted.el, 'Einrücken') as HTMLButtonElement
    const outdent = button(mounted.el, 'Ausrücken') as HTMLButtonElement
    expect(indent.disabled).toBe(true)
    expect(outdent.disabled).toBe(false)
    expect(indent.getAttribute('aria-label')).toContain('Kein vorheriger Listeneintrag.')
    expect(indent.parentElement?.getAttribute('title')).toBe('Kein vorheriger Listeneintrag.')
    mounted.unmount()
  })

  it('edits this offer from the gear without selecting the footer', async () => {
    const mounted = await mount([paragraph('Alpha')])
    const gear = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Einstellungen"]')!
    gear.click()
    await nextTick()
    expect(mounted.el.querySelector('#this-offer-heading')?.textContent).toBe('Dieses Angebot')
    expect(mounted.footerSelects()).toBe(0)
    const offset = mounted.el.querySelector<HTMLInputElement>(
      '[aria-label="Vertikaler Versatz des Fußzeilenlogos in Millimetern. Negativ nach oben, positiv nach unten."]',
    )!
    offset.value = '-6'
    offset.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(mounted.footer.value.logo_offset_mm).toBe(-6)
    expect(mounted.footerSelects()).toBe(0)
    const templates = [...mounted.el.querySelectorAll('button')].find((item) =>
      item.textContent?.includes('Vorlagen für neue Angebote'),
    )!
    expect(templates.closest('.gear-future')).toBeTruthy()
    expect(templates.closest('.this-offer')).toBeNull()
    templates.click()
    await nextTick()
    expect(mounted.actions).toEqual(['settings'])
    expect(mounted.footerSelects()).toBe(0)
    mounted.unmount()
  })
})
