import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { createApp, h, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import OfferTitleBar from './OfferTitleBar.vue'
import { nextOfferZoomStep, OFFER_ZOOM_STEPS, parseOfferZoom } from './offerZoom'

describe('offer zoom steps', () => {
  it('covers 25 through 800 and stops at the ends', () => {
    expect(OFFER_ZOOM_STEPS[0]).toBe(25)
    expect(OFFER_ZOOM_STEPS[OFFER_ZOOM_STEPS.length - 1]).toBe(800)
    expect([...OFFER_ZOOM_STEPS]).toEqual([
      25, 50, 75, 100, 125, 150, 175, 200, 250, 300, 400, 600, 800,
    ])
    expect(OFFER_ZOOM_STEPS).not.toContain(500)
    expect(OFFER_ZOOM_STEPS).not.toContain(700)
    expect(parseOfferZoom('500')).toBe(500)
    expect(parseOfferZoom('700')).toBe(700)
    expect(parseOfferZoom('24')).toBeNull()
    expect(parseOfferZoom('801')).toBeNull()
    expect(parseOfferZoom('100.5')).toBeNull()
    expect(nextOfferZoomStep(100, 1)).toBe(125)
    expect(nextOfferZoomStep(100, -1)).toBe(75)
    expect(nextOfferZoomStep(200, 1)).toBe(250)
    expect(nextOfferZoomStep(200, -1)).toBe(175)
    expect(nextOfferZoomStep(30, -1)).toBe(25)
    expect(nextOfferZoomStep(30, 1)).toBe(50)
    expect(nextOfferZoomStep(25, -1)).toBeNull()
    expect(nextOfferZoomStep(10, -1)).toBeNull()
    expect(nextOfferZoomStep(10, 1)).toBe(25)
    expect(nextOfferZoomStep(800, 1)).toBeNull()
    expect(nextOfferZoomStep(900, 1)).toBeNull()
    expect(nextOfferZoomStep(900, -1)).toBe(800)
  })

  it('places zoom in the center slot with minus and plus on its right', async () => {
    const modes: string[] = []
    const el = document.createElement('div')
    document.body.appendChild(el)
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/crm', component: { template: '<div />' } }],
    })
    const app = createApp({
      setup() {
        return () =>
          h(OfferTitleBar, {
            backTo: '/crm',
            backLabel: 'Zurück',
            offerNo: 'A260924-01',
            hostname: 'paimos.test',
            editable: true,
            busy: false,
            loading: false,
            dirty: false,
            saveFailed: false,
            state: 'Gespeichert',
            savedAt: 'Heute, 09:00',
            savedAtIso: '2026-09-24T07:00:00Z',
            zoomMode: '100',
            zoom: 1,
            printDisabled: false,
            showPrint: true,
            actions: [],
            showInspector: true,
            inspectorOpen: true,
            'onUpdate:zoomMode': (value: string) => modes.push(value),
          })
      },
    })
    app.use(router)
    app.mount(el)
    await router.isReady()
    await nextTick()
    const source = readFileSync('src/components/offers/OfferTitleBar.vue', 'utf8')
    expect(source).toContain('grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr)')
    expect(source).toMatch(/\.offer-id-copy\s*\{[^}]*flex-direction:\s*row/)
    expect(source).not.toContain('context-row')
    expect(source).not.toContain('Listen und Punkte')
    const copy = el.querySelector('.offer-id-copy')
    expect(copy?.querySelector('.offer-number')?.textContent).toContain('A260924-01')
    expect(copy?.querySelector('.save-state')?.textContent).toContain('Gespeichert')
    const controls = [...el.querySelector('.zoom-controls')!.children]
    expect(controls.map((node) => node.tagName)).toEqual(['BUTTON', 'BUTTON', 'BUTTON'])
    expect(controls[0]?.getAttribute('aria-label')).toBe('Zoom')
    expect(controls[1]?.getAttribute('aria-label')).toBe('Verkleinern')
    expect(controls[2]?.getAttribute('aria-label')).toBe('Vergrößern')
    expect(el.querySelector('select')).toBeNull()
    controls[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    const popover = el.querySelector('[role="dialog"]')!
    const labels = [...popover.querySelectorAll('button')].map((option) => option.textContent?.trim())
    expect(labels).toContain('Seitenbreite')
    expect(labels).toContain('Ganze Seite')
    expect(labels).toContain('25 %')
    expect(labels).toContain('800 %')
    expect(labels).not.toContain('500 %')
    expect(labels).not.toContain('700 %')
    expect(popover.scrollHeight).toBeLessThanOrEqual(popover.clientHeight || popover.scrollHeight)
    const typed = popover.querySelector<HTMLInputElement>('[aria-label="Zoom in Prozent"]')!
    typed.value = '500'
    typed.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await nextTick()
    expect(modes).toContain('500')
    controls[2]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    controls[1]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(modes).toEqual(['500', '125', '75'])
    const collapse = el.querySelector<HTMLButtonElement>('[aria-label="Kopfzeilen einklappen"]')!
    expect(collapse.getAttribute('aria-expanded')).toBe('true')
    expect(el.textContent).not.toContain('Kopfzeilen einklappen')
    expect(el.querySelector('[aria-label="Einfügen"]')).toBeNull()
    app.unmount()
    el.remove()
  })

  it('disables the zoom step that would leave 25 or 800', async () => {
    async function ends(zoom: number) {
      const el = document.createElement('div')
      document.body.appendChild(el)
      const router = createRouter({
        history: createMemoryHistory(),
        routes: [{ path: '/crm', component: { template: '<div />' } }],
      })
      const app = createApp({
        setup() {
          return () =>
            h(OfferTitleBar, {
              backTo: '/crm',
              backLabel: 'Zurück',
              offerNo: 'A',
              hostname: 'paimos.test',
              editable: false,
              busy: false,
              loading: false,
              dirty: false,
              saveFailed: false,
              state: 'Gespeichert',
              savedAt: '',
              savedAtIso: '',
              zoomMode: String(Math.round(zoom * 100)),
              zoom,
              printDisabled: true,
              showPrint: false,
              actions: [],
              showInspector: false,
              inspectorOpen: false,
            })
        },
      })
      app.use(router)
      app.mount(el)
      await router.isReady()
      await nextTick()
      const minus = el.querySelector<HTMLButtonElement>('[aria-label="Verkleinern"]')!
      const plus = el.querySelector<HTMLButtonElement>('[aria-label="Vergrößern"]')!
      const result = { minus: minus.disabled, plus: plus.disabled }
      app.unmount()
      el.remove()
      return result
    }
    expect(await ends(0.25)).toEqual({ minus: true, plus: false })
    expect(await ends(8)).toEqual({ minus: false, plus: true })
  })
})
