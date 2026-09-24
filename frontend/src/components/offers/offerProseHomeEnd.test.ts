import { afterEach, describe, expect, it } from 'vitest'
import { createApp, nextTick } from 'vue'
import OfferProse from './OfferProse.vue'
import type { OfferTextNode } from './types'

const originalRects = Range.prototype.getClientRects
const wrapped = '0123456789ABCDEF'

function rectList(top: number): DOMRectList {
  const rect = new DOMRect(0, top, 8, 16)
  const list = [rect]
  return Object.assign(list, { item: (index: number) => list[index] ?? null }) as DOMRectList
}

function installWrap() {
  Range.prototype.getClientRects = function getClientRects() {
    if (this.startContainer.nodeType !== Node.TEXT_NODE || this.endOffset !== this.startOffset + 1)
      return rectList(0)
    return rectList(Math.floor(this.startOffset / 8) * 20)
  }
}

function mount(body: string, nodes?: OfferTextNode[], editable = true) {
  const updates: unknown[] = []
  const el = document.createElement('div')
  document.body.appendChild(el)
  const app = createApp(OfferProse, {
    body,
    nodes,
    editable,
    label: 'Textbaustein 1',
    onUpdate: (value: unknown) => updates.push(value),
  })
  app.mount(el)
  return {
    updates,
    root: el.querySelector<HTMLElement>('.offer-prose')!,
    unmount() {
      app.unmount()
      el.remove()
    },
  }
}

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
  const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
  root.dispatchEvent(event)
  return event
}

function focusOffset() {
  return window.getSelection()?.focusOffset ?? null
}

function focusIndex(root: ParentNode) {
  const node = window.getSelection()?.focusNode
  const el = node instanceof Element ? node : node?.parentElement
  const text = el?.closest('[data-text]')
  if (!text || !root.contains(text)) return null
  return text.getAttribute('data-index')
}

const originalModify = Selection.prototype.modify

describe('OfferProse Home and End', () => {
  afterEach(() => {
    Range.prototype.getClientRects = originalRects
    Selection.prototype.modify = originalModify
    document.body.replaceChildren()
    window.getSelection()?.removeAllRanges()
  })

  it('moves to the visual line and stays there', async () => {
    installWrap()
    const nodes: OfferTextNode[] = [
      { kind: 'paragraph', text: wrapped },
      { kind: 'paragraph', text: wrapped },
    ]
    const mounted = mount(`${wrapped}\n${wrapped}`, nodes)
    await nextTick()
    place(mounted.root, 1, 11)
    key(mounted.root, { key: 'Home' })
    expect(focusIndex(mounted.root)).toBe('1')
    expect(focusOffset()).toBe(8)
    key(mounted.root, { key: 'Home' })
    expect(focusIndex(mounted.root)).toBe('1')
    expect(focusOffset()).toBe(8)
    place(mounted.root, 0, 3)
    key(mounted.root, { key: 'End' })
    expect(focusOffset()).toBe(8)
    key(mounted.root, { key: 'End' })
    expect(focusOffset()).toBe(8)
    key(mounted.root, { key: 'Home' })
    expect(focusOffset()).toBe(0)
    expect(mounted.updates).toEqual([])
    mounted.unmount()
  })

  it('stays on an empty line before a leading newline', async () => {
    const mounted = mount('\nZwei')
    await nextTick()
    place(mounted.root, 0, 0)
    key(mounted.root, { key: 'Home' })
    expect(focusOffset()).toBe(0)
    key(mounted.root, { key: 'End' })
    expect(focusOffset()).toBe(0)
    place(mounted.root, 0, 3)
    key(mounted.root, { key: 'Home' })
    expect(focusOffset()).toBe(1)
    mounted.unmount()
  })

  it('keeps a native line-boundary caret and falls back when it leaves the block', async () => {
    installWrap()
    const mounted = mount(wrapped, [
      { kind: 'paragraph', text: wrapped },
      { kind: 'paragraph', text: wrapped },
    ])
    await nextTick()
    const original = Selection.prototype.modify
    const calls: string[] = []
    Selection.prototype.modify = function modify(alter, direction, granularity) {
      calls.push(`${alter}:${direction}:${granularity}`)
      const text = mounted.root.querySelector('[data-index="0"]')?.firstChild
      if (text) this.setBaseAndExtent(text, 1, text, 1)
    }
    place(mounted.root, 0, 3)
    key(mounted.root, { key: 'Home' })
    expect(calls).toEqual(['move:backward:lineboundary'])
    expect(focusOffset()).toBe(1)
    Selection.prototype.modify = function modify() {
      const text = mounted.root.querySelector('[data-index="1"]')?.firstChild
      if (text) this.setBaseAndExtent(text, 4, text, 4)
    }
    place(mounted.root, 0, 3)
    key(mounted.root, { key: 'End' })
    expect(focusIndex(mounted.root)).toBe('0')
    expect(focusOffset()).toBe(8)
    Selection.prototype.modify = original
    mounted.unmount()
  })

  it('extends with Shift and leaves Ctrl or Meta to the browser', async () => {
    installWrap()
    const mounted = mount(wrapped, [{ kind: 'paragraph', text: wrapped }])
    await nextTick()
    place(mounted.root, 0, 11)
    key(mounted.root, { key: 'Home', shiftKey: true })
    const selection = window.getSelection()
    expect(selection?.anchorOffset).toBe(11)
    expect(selection?.focusOffset).toBe(8)
    place(mounted.root, 0, 4)
    const modified = key(mounted.root, { key: 'End', ctrlKey: true })
    expect(modified.defaultPrevented).toBe(false)
    expect(focusOffset()).toBe(4)
    const meta = key(mounted.root, { key: 'Home', metaKey: true })
    expect(meta.defaultPrevented).toBe(false)
    expect(focusOffset()).toBe(4)
    mounted.unmount()
  })

  it('ignores composition and does not scroll a readonly field or the viewport', async () => {
    const scroller = document.createElement('div')
    scroller.style.height = '20px'
    scroller.style.overflow = 'auto'
    const inner = document.createElement('div')
    inner.style.height = '400px'
    scroller.appendChild(inner)
    document.body.appendChild(scroller)
    scroller.scrollTop = 48
    const mounted = mount('Eins\nZwei')
    inner.appendChild(mounted.root)
    await nextTick()
    place(mounted.root, 0, 6)
    mounted.root.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    const composing = key(mounted.root, { key: 'Home' })
    expect(composing.defaultPrevented).toBe(false)
    expect(focusOffset()).toBe(6)
    mounted.root.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
    const home = key(mounted.root, { key: 'Home' })
    expect(home.defaultPrevented).toBe(true)
    expect(focusOffset()).toBe(5)
    expect(scroller.scrollTop).toBe(48)
    key(mounted.root, { key: 'End' })
    expect(focusOffset()).toBe(9)
    key(mounted.root, { key: 'End' })
    expect(focusOffset()).toBe(9)
    mounted.unmount()

    const locked = mount('Eins\nZwei', undefined, false)
    await nextTick()
    const readonly = key(locked.root, { key: 'Home' })
    expect(readonly.defaultPrevented).toBe(true)
    expect(locked.root.textContent).toBe('Eins\nZwei')
    locked.unmount()
  })
})
