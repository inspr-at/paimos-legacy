import { describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import OfferProse from './OfferProse.vue'
import OfferTitleBar from './OfferTitleBar.vue'
import { provideOfferProseSession } from './offerProseSession'
import type { OfferFooterLayout, OfferTextNode } from './types'
import type { OfferToolbarAction } from './offerToolbarActions'

const paragraphs: OfferTextNode[] = [
  { kind: 'paragraph', text: 'Alpha' },
  { kind: 'paragraph', text: 'Beta' },
  { kind: 'paragraph', text: 'Gamma' },
]

function click(target: Element) {
  target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }))
  target.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))
}

async function mount() {
  const updates: { body: string; nodes?: OfferTextNode[] }[] = []
  const footer = ref<OfferFooterLayout>({ logo_width_mm: 43.3, logo_offset_mm: 2 })
  const el = document.createElement('div')
  document.body.appendChild(el)
  const actions: OfferToolbarAction[] = [
    { id: 'layout', label: 'Fußzeilenlogo', detail: 'Versatz', disabled: false },
  ]
  const Host = defineComponent({
    setup() {
      provideOfferProseSession()
      const prose = ref<{ body: string; nodes: OfferTextNode[] | null }>({
        body: 'Alpha\nBeta\nGamma',
        nodes: paragraphs.map((node) => ({ ...node })),
      })
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
            actions,
            footer: footer.value,
            onFooter: (value: OfferFooterLayout) => {
              footer.value = value
            },
          }),
          h(OfferProse, {
            body: prose.value.body,
            nodes: prose.value.nodes ?? null,
            editable: true,
            label: 'Textbaustein 1',
            onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
              updates.push(value)
              prose.value = { body: value.body, nodes: value.nodes ?? null }
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
    updates,
    footer,
    unmount() {
      app.unmount()
      el.remove()
    },
  }
}

function labels(root: ParentNode) {
  return [...root.querySelectorAll<HTMLElement>('[data-bullet]')].map((node) =>
    node.getAttribute('data-bullet'),
  )
}

describe('offer list start and signed offset', () => {
  it('applies a typed start value and keeps it when the menu closes', async () => {
    const mounted = await mount()
    const prose = mounted.el.querySelector<HTMLElement>('.offer-prose')!
    prose.focus()
    prose.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    prose.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'a',
        metaKey: true,
        bubbles: true,
        cancelable: true,
      }),
    )
    click(mounted.el.querySelector('[aria-label="Listen und Punkte"]')!)
    await nextTick()
    click(mounted.el.querySelector('[aria-label="Nummerierung"]')!)
    await nextTick()
    expect(labels(prose)).toEqual(['1', '2', '3'])
    const beta = prose.querySelectorAll<HTMLElement>('[data-text]')[1]!
    prose.focus()
    const range = document.createRange()
    range.setStart(beta.firstChild ?? beta, 0)
    range.collapse(true)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    const start = mounted.el.querySelector<HTMLInputElement>(
      '[aria-label="Nummerierung beginnen bei"]',
    )!
    start.focus()
    start.value = '3'
    start.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(document.activeElement).toBe(start)
    expect(labels(prose)).toEqual(['1', '3', '4'])
    expect(mounted.updates[mounted.updates.length - 1]?.nodes?.[1]).toMatchObject({
      list_start: 3,
      numbering: 'outline',
    })
    start.value = '5'
    start.dispatchEvent(new Event('input', { bubbles: true }))
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    await nextTick()
    expect(labels(prose)).toEqual(['1', '5', '6'])
    expect(mounted.updates[mounted.updates.length - 1]?.nodes?.[1]?.list_start).toBe(5)
    start.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    await nextTick()
    expect(mounted.updates[mounted.updates.length - 1]?.nodes?.[1]?.list_start).toBe(5)
    mounted.unmount()
  })

  it('keeps a leading minus until the offset is a real number', async () => {
    const mounted = await mount()
    click(mounted.el.querySelector('[aria-label="Weitere Aktionen"]')!)
    await nextTick()
    const layout = [...mounted.el.querySelectorAll('button')].find((button) =>
      button.textContent?.includes('Fußzeilenlogo'),
    )
    click(layout!)
    await nextTick()
    const offset = mounted.el.querySelector<HTMLInputElement>(
      '[aria-label="Vertikaler Versatz des Fußzeilenlogos in Millimetern. Negativ nach oben, positiv nach unten."]',
    )!
    const width = mounted.el.querySelector<HTMLInputElement>(
      '[aria-label="Breite des Fußzeilenlogos in Millimetern"]',
    )!
    offset.focus()
    offset.value = '-'
    offset.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(offset.value).toBe('-')
    expect(mounted.footer.value.logo_offset_mm).toBe(2)
    offset.value = '-6'
    offset.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(offset.value).toBe('-6')
    expect(mounted.footer.value.logo_offset_mm).toBe(-6)
    width.focus()
    width.value = '2.'
    width.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(width.value).toBe('2.')
    expect(mounted.footer.value.logo_width_mm).toBe(43.3)
    width.value = '55.5'
    width.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(mounted.footer.value.logo_width_mm).toBe(55.5)
    expect(width.value).toBe('55.5')
    mounted.unmount()
  })
})
