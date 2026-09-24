import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import OfferText from './OfferText.vue'

afterEach(() => {
  document.body.replaceChildren()
  window.getSelection()?.removeAllRanges()
})

describe('OfferText Home and End', () => {
  async function mountField(initial: string, editable = true) {
    const value = ref(initial)
    const updates: string[] = []
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp({
      setup() {
        return () =>
          h(OfferText, {
            modelValue: value.value,
            editable,
            tag: 'div',
            label: 'Überschrift',
            'onUpdate:modelValue': (next: string) => {
              updates.push(next)
              value.value = next
            },
          })
      },
    })
    app.mount(el)
    await nextTick()
    const field = el.querySelector<HTMLElement>('[aria-label="Überschrift"]')!
    return {
      value,
      updates,
      field,
      unmount() {
        app.unmount()
        el.remove()
      },
    }
  }

  function place(field: HTMLElement, offset: number) {
    const text = field.firstChild
    if (!text || text.nodeType !== Node.TEXT_NODE) throw new Error('missing text')
    const range = document.createRange()
    range.setStart(text, offset)
    range.collapse(true)
    const selection = window.getSelection()
    selection?.removeAllRanges()
    selection?.addRange(range)
    field.focus()
  }

  function key(field: HTMLElement, init: KeyboardEventInit) {
    const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
    field.dispatchEvent(event)
    return event
  }

  it('moves within the current line and does not change the text', async () => {
    const mounted = await mountField('Eins\nZwei')
    place(mounted.field, 7)
    const home = key(mounted.field, { key: 'Home' })
    expect(home.defaultPrevented).toBe(true)
    expect(window.getSelection()?.focusOffset).toBe(5)
    key(mounted.field, { key: 'End' })
    expect(window.getSelection()?.focusOffset).toBe(9)
    key(mounted.field, { key: 'End' })
    expect(window.getSelection()?.focusOffset).toBe(9)
    expect(mounted.value.value).toBe('Eins\nZwei')
    expect(mounted.updates).toEqual([])
    mounted.unmount()
    const leading = await mountField('\nZwei')
    place(leading.field, 0)
    key(leading.field, { key: 'Home' })
    key(leading.field, { key: 'End' })
    expect(window.getSelection()?.focusOffset).toBe(0)
    expect(leading.updates).toEqual([])
    leading.unmount()
  })

  it('extends with Shift and leaves Ctrl or Meta alone', async () => {
    const mounted = await mountField('Eins\nZwei')
    place(mounted.field, 7)
    key(mounted.field, { key: 'Home', shiftKey: true })
    const selection = window.getSelection()
    expect(selection?.anchorOffset).toBe(7)
    expect(selection?.focusOffset).toBe(5)
    expect(mounted.updates).toEqual([])
    place(mounted.field, 7)
    const modified = key(mounted.field, { key: 'End', metaKey: true })
    expect(modified.defaultPrevented).toBe(false)
    expect(window.getSelection()?.focusOffset).toBe(7)
    mounted.unmount()
  })

  it('does not jump the page when readonly or composing', async () => {
    const scroller = document.createElement('div')
    scroller.style.overflow = 'auto'
    scroller.scrollTop = 40
    document.body.appendChild(scroller)
    const locked = await mountField('Eins\nZwei', false)
    scroller.appendChild(locked.field)
    const readonly = key(locked.field, { key: 'Home' })
    expect(readonly.defaultPrevented).toBe(true)
    expect(locked.updates).toEqual([])
    expect(scroller.scrollTop).toBe(40)
    locked.unmount()

    const mounted = await mountField('Eins\nZwei')
    place(mounted.field, 7)
    mounted.field.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    const composing = key(mounted.field, { key: 'Home' })
    expect(composing.defaultPrevented).toBe(false)
    expect(window.getSelection()?.focusOffset).toBe(7)
    mounted.unmount()
  })

  it('stays on a wrapped line and still moves after paste splits the text', async () => {
    const original = Range.prototype.getClientRects
    const wrapped = '0123456789ABCDEF'
    Range.prototype.getClientRects = function getClientRects() {
      const height = this.endOffset === this.startOffset + 1 ? 16 : 0
      const nodes = [...(this.startContainer.parentNode?.childNodes ?? [])].filter(
        (node) => node.nodeType === Node.TEXT_NODE,
      ) as Text[]
      let linear = this.startOffset
      for (const node of nodes) {
        if (node === this.startContainer) break
        linear += node.data.length
      }
      const rect = new DOMRect(0, Math.floor(linear / 8) * 20, 8, height)
      const list = height ? [rect] : []
      return Object.assign(list, { item: (index: number) => list[index] ?? null }) as DOMRectList
    }
    const mounted = await mountField(wrapped)
    place(mounted.field, 3)
    key(mounted.field, { key: 'End' })
    expect(window.getSelection()?.focusOffset).toBe(8)
    key(mounted.field, { key: 'End' })
    expect(window.getSelection()?.focusOffset).toBe(8)
    key(mounted.field, { key: 'ArrowRight' })
    key(mounted.field, { key: 'End' })
    expect(window.getSelection()?.focusOffset).toBe(16)
    mounted.unmount()
    const split = await mountField('x')
    const left = document.createTextNode('01234567')
    const right = document.createTextNode('89ABCDEF')
    split.field.replaceChildren(left, right)
    const at = (node: Text, offset: number) => {
      const range = document.createRange()
      range.setStart(node, offset)
      range.collapse(true)
      window.getSelection()?.removeAllRanges()
      window.getSelection()?.addRange(range)
    }
    at(left, 3)
    key(split.field, { key: 'End' })
    expect(window.getSelection()?.focusNode).toBe(left)
    expect(window.getSelection()?.focusOffset).toBe(8)
    key(split.field, { key: 'End' })
    expect(window.getSelection()?.focusNode).toBe(left)
    expect(window.getSelection()?.focusOffset).toBe(8)
    at(right, 3)
    key(split.field, { key: 'Home' })
    expect(window.getSelection()?.focusNode).toBe(left)
    expect(window.getSelection()?.focusOffset).toBe(8)
    expect(left.data).toBe('01234567')
    expect(right.data).toBe('89ABCDEF')
    split.unmount()
    Range.prototype.getClientRects = original

    const pasted = await mountField('0123456789')
    place(pasted.field, 4)
    const data = new DataTransfer()
    data.setData('text/plain', 'X')
    pasted.field.dispatchEvent(
      new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: data }),
    )
    const nodes = [...pasted.field.childNodes]
    expect(nodes.length).toBeGreaterThan(1)
    const first = nodes[0] as Text
    const last = nodes[nodes.length - 1] as Text
    const range = document.createRange()
    range.setStart(first, Math.min(4, first.data.length))
    range.collapse(true)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(range)
    key(pasted.field, { key: 'Home' })
    expect(window.getSelection()?.focusNode).toBe(first)
    expect(window.getSelection()?.focusOffset).toBe(0)
    key(pasted.field, { key: 'End' })
    expect(window.getSelection()?.focusNode).toBe(last)
    expect(window.getSelection()?.focusOffset).toBe(last.data.length)
    expect([...pasted.field.childNodes]).toEqual(nodes)
    expect(first.data).toBe('0123')
    expect(pasted.updates).toEqual(['0123X456789'])
    const anchor = last
    range.setStart(anchor, 1)
    range.collapse(true)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(range)
    key(pasted.field, { key: 'Home', shiftKey: true })
    expect(window.getSelection()?.anchorNode).toBe(anchor)
    expect(window.getSelection()?.anchorOffset).toBe(1)
    expect(window.getSelection()?.focusOffset).toBe(0)
    pasted.unmount()
  })
})
