import { describe, expect, it } from 'vitest'
import {
  focusCaretBox,
  lineBoundaryTarget,
  logicalLineBounds,
  pointInTextNodes,
  revealCaretInEditor,
  visualLineOf,
} from './offerProseCaret'

const wrapped = '0123456789ABCDEF'
const tops = (index: number) => Math.floor(index / 8) * 20

describe('visual line bounds', () => {
  it('splits a wrapped line from glyph tops', () => {
    expect(visualLineOf(wrapped, 3, tops)).toEqual({ start: 0, end: 8 })
    expect(visualLineOf(wrapped, 11, tops)).toEqual({ start: 8, end: 16 })
    expect(visualLineOf(wrapped, 16, tops)).toEqual({ start: 8, end: 16 })
    // That boundary offset is also the next line's start on a fresh measurement.
    expect(visualLineOf(wrapped, 8, tops)).toEqual({ start: 8, end: 16 })
    const start = visualLineOf(wrapped, 11, tops).start
    expect(visualLineOf(wrapped, start, tops).start).toBe(start)
  })

  it('keeps a newline line when layout is flat or missing', () => {
    expect(logicalLineBounds('Eins\nZwei', 6)).toEqual({ start: 5, end: 9 })
    expect(visualLineOf('Eins\nZwei', 6)).toEqual({ start: 5, end: 9 })
    expect(visualLineOf('Eins\nZwei', 4)).toEqual({ start: 0, end: 4 })
    expect(visualLineOf('Eins\nZwei', 6, () => 0)).toEqual({ start: 5, end: 9 })
    expect(visualLineOf(wrapped, 3, () => null)).toEqual({ start: 0, end: wrapped.length })
    expect(visualLineOf('', 0)).toEqual({ start: 0, end: 0 })
    expect(logicalLineBounds('\nZwei', 0)).toEqual({ start: 0, end: 0 })
    expect(visualLineOf('\nZwei', 0)).toEqual({ start: 0, end: 0 })
    expect(logicalLineBounds('\nZwei', 1)).toEqual({ start: 1, end: 5 })
    expect(logicalLineBounds('Alpha\n\nBeta', 6)).toEqual({ start: 6, end: 6 })
    expect(logicalLineBounds('Eins\nZwei', 0)).toEqual({ start: 0, end: 4 })
  })

  it('scrolls only the editor container that clips the caret', () => {
    const scroller = document.createElement('div')
    scroller.style.overflow = 'auto'
    Object.defineProperty(scroller, 'clientHeight', { value: 80 })
    Object.defineProperty(scroller, 'clientWidth', { value: 120 })
    Object.defineProperty(scroller, 'scrollHeight', { value: 900 })
    Object.defineProperty(scroller, 'scrollWidth', { value: 900 })
    const editor = document.createElement('div')
    scroller.appendChild(editor)
    document.body.appendChild(scroller)
    const page = { x: window.scrollX, y: window.scrollY }
    revealCaretInEditor(
      editor,
      { top: 200, bottom: 216, left: 10, right: 18, width: 8, height: 16 },
      () => ({ top: 0, bottom: 80, left: 0, right: 120 }),
    )
    expect(scroller.scrollTop).toBe(136)
    expect(window.scrollX).toBe(page.x)
    expect(window.scrollY).toBe(page.y)
    scroller.scrollTop = 0
    revealCaretInEditor(
      editor,
      { top: 10, bottom: 26, left: 390, right: 400, width: 10, height: 16 },
      () => ({ top: 0, bottom: 80, left: 0, right: 100 }),
    )
    const wideEdge = scroller.scrollLeft
    scroller.scrollLeft = 0
    revealCaretInEditor(
      editor,
      { top: 10, bottom: 26, left: 20, right: 20, width: 0, height: 16 },
      () => ({ top: 0, bottom: 80, left: 0, right: 100 }),
    )
    expect(wideEdge).toBeGreaterThan(0)
    expect(scroller.scrollLeft).toBe(0)
    scroller.remove()
  })

  it('keeps a repeated End on the remembered wrapped line', () => {
    const text = '0123456789ABCDEF'
    const tops = (index: number) => Math.floor(index / 8) * 20
    const first = lineBoundaryTarget('End', 3, text, null, tops)
    expect(first.target).toBe(8)
    const second = lineBoundaryTarget('End', first.target, text, first.edge, tops)
    expect(second.target).toBe(8)
    expect(lineBoundaryTarget('End', 8, text, null, tops).target).toBe(16)
  })

  it('maps a linear offset onto an existing text node', () => {
    const first = document.createTextNode('0123')
    const second = document.createTextNode('456789')
    expect(pointInTextNodes([first, second], 0)).toEqual({ node: first, offset: 0 })
    expect(pointInTextNodes([first, second], 4)).toEqual({ node: first, offset: 4 })
    expect(pointInTextNodes([first, second], 6)).toEqual({ node: second, offset: 2 })
    expect(pointInTextNodes([first, second], 4)?.node.data).toBe('0123')
  })

  it('measures a collapsed caret at the focus, not the whole selection', () => {
    const text = document.createTextNode('abcdef')
    const host = document.createElement('div')
    host.appendChild(text)
    document.body.appendChild(host)
    const selection = window.getSelection()
    selection?.removeAllRanges()
    selection?.setBaseAndExtent(text, 0, text, 6)
    const originalRects = Range.prototype.getClientRects
    const originalBox = Range.prototype.getBoundingClientRect
    Range.prototype.getClientRects = function getClientRects() {
      const rect = this.collapsed ? new DOMRect(90, 4, 0, 16) : new DOMRect(0, 4, 120, 16)
      const list = [rect]
      return Object.assign(list, { item: (index: number) => list[index] ?? null }) as DOMRectList
    }
    Range.prototype.getBoundingClientRect = function getBoundingClientRect() {
      return this.collapsed ? new DOMRect(90, 4, 0, 16) : new DOMRect(0, 4, 120, 16)
    }
    const box = focusCaretBox(selection!)
    expect(box).toMatchObject({ left: 90, width: 0, height: 16 })
    expect(selection?.getRangeAt(0).getBoundingClientRect().width).toBe(120)
    Range.prototype.getClientRects = originalRects
    Range.prototype.getBoundingClientRect = originalBox
    host.remove()
  })

  it('treats a two-pixel difference as the same line and a larger gap as a wrap', () => {
    expect(visualLineOf('abcd', 1, (index) => (index < 2 ? 0 : 1))).toEqual({ start: 0, end: 4 })
    expect(visualLineOf('abcd', 1, (index) => (index < 2 ? 0 : 10))).toEqual({ start: 0, end: 2 })
  })
})
