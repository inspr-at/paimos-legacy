/** Pixel tolerance for glyphs that sit on one wrapped line. */
export const VISUAL_LINE_EPSILON_PX = 2

/** Bounds of the logical line that contains `offset`, split only on newline characters. */
export function logicalLineBounds(text: string, offset: number): { start: number; end: number } {
  const length = text.length
  const clamped = Math.max(0, Math.min(offset, length))
  // Walk from the caret. lastIndexOf(fromIndex 0) would treat a leading newline as the previous line.
  let start = clamped
  while (start > 0 && text.charAt(start - 1) !== '\n') start -= 1
  let end = clamped
  while (end < length && text.charAt(end) !== '\n') end += 1
  return { start, end }
}

export type CaretBox = {
  top: number
  bottom: number
  left: number
  right: number
  width: number
  height: number
}

/**
 * Box of a collapsed caret at the selection focus.
 * The full selection rectangle is the wrong target: a selection wider than the
 * viewport makes Shift+End alternate between its two edges.
 */
export function focusCaretBox(selection: {
  focusNode: Node | null
  focusOffset: number
  rangeCount: number
}): CaretBox | null {
  const node = selection.focusNode
  if (!node || selection.rangeCount === 0) return null
  const range = document.createRange()
  try {
    const max =
      node.nodeType === Node.TEXT_NODE ? (node.textContent?.length ?? 0) : node.childNodes.length
    range.setStart(node, Math.max(0, Math.min(selection.focusOffset, max)))
    range.collapse(true)
  } catch {
    return null
  }
  const rects = range.getClientRects()
  const rect = rects.length ? rects[rects.length - 1] : range.getBoundingClientRect()
  if (!rect || (rect.width <= 0 && rect.height <= 0)) return null
  return {
    top: rect.top,
    bottom: rect.bottom,
    left: rect.left,
    right: rect.right,
    width: rect.width,
    height: rect.height,
  }
}

/** Scroll the nearest editor overflow ancestor just enough to show `caret`. The page itself stays put. */
export function revealCaretInEditor(
  editor: HTMLElement,
  caret: CaretBox,
  boxOf: (element: HTMLElement) => Pick<DOMRect, 'top' | 'bottom' | 'left' | 'right'> = (element) =>
    element.getBoundingClientRect(),
) {
  if (caret.width <= 0 && caret.height <= 0) return
  let node: HTMLElement | null = editor.parentElement
  while (node && node !== document.body && node !== document.documentElement) {
    const style = getComputedStyle(node)
    const overflowY = `${style.overflowY} ${node.style.overflowY} ${node.style.overflow}`
    const overflowX = `${style.overflowX} ${node.style.overflowX} ${node.style.overflow}`
    const canY = /auto|scroll/.test(overflowY) && node.scrollHeight > node.clientHeight + 1
    const canX = /auto|scroll/.test(overflowX) && node.scrollWidth > node.clientWidth + 1
    if (canY || canX) {
      const box = boxOf(node)
      if (canY) {
        if (caret.top < box.top) node.scrollTop -= box.top - caret.top
        else if (caret.bottom > box.bottom) node.scrollTop += caret.bottom - box.bottom
      }
      if (canX) {
        if (caret.left < box.left) node.scrollLeft -= box.left - caret.left
        else if (caret.right > box.right) node.scrollLeft += caret.right - box.right
      }
    }
    node = node.parentElement
  }
}

/**
 * Visual line containing the caret, using glyph tops when layout can measure them.
 * `glyphTopAt` receives a character index. A null top falls back to the logical line.
 * The result stays inside the current newline segment and the current text block.
 */
export type LineEdge = { offset: number; start: number; end: number }

/** Fallback Home/End target. A remembered edge keeps a second press on that wrapped line. */
export function lineBoundaryTarget(
  key: 'Home' | 'End',
  offset: number,
  text: string,
  remembered: LineEdge | null,
  glyphTopAt?: (charIndex: number) => number | null,
): { target: number; edge: LineEdge } {
  const clamped = Math.max(0, Math.min(offset, text.length))
  const line =
    remembered && remembered.offset === clamped
      ? remembered
      : visualLineOf(text, clamped, glyphTopAt)
  const target = key === 'Home' ? line.start : line.end
  return { target, edge: { offset: target, start: line.start, end: line.end } }
}

export function collectTextNodes(root: Node): Text[] {
  if (root.nodeType === Node.TEXT_NODE) return [root as Text]
  const nodes: Text[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let current = walker.nextNode()
  while (current) {
    nodes.push(current as Text)
    current = walker.nextNode()
  }
  return nodes
}

export function linearText(nodes: readonly Text[]): string {
  return nodes.map((node) => node.data).join('')
}

/** Caret offset counted through existing text nodes. Does not rewrite the DOM. */
export function offsetInTextNodes(
  nodes: readonly Text[],
  focus: Node,
  focusOffset: number,
): number | null {
  let cursor = 0
  for (const node of nodes) {
    if (node === focus) return cursor + Math.max(0, Math.min(focusOffset, node.data.length))
    cursor += node.data.length
  }
  return null
}

export function offsetInRoot(root: HTMLElement, focus: Node, focusOffset: number): number {
  const nodes = collectTextNodes(root)
  const direct = offsetInTextNodes(nodes, focus, focusOffset)
  if (direct != null) return direct
  if (focus !== root) return 0
  let count = 0
  for (let index = 0; index < focusOffset && index < root.childNodes.length; index += 1) {
    count += linearText(collectTextNodes(root.childNodes[index]!)).length
  }
  return count
}

/** Map a linear offset onto the text node that already holds that character. */
export function pointInTextNodes(
  nodes: readonly Text[],
  offset: number,
): { node: Text; offset: number } | null {
  if (!nodes.length) return null
  let cursor = 0
  for (const node of nodes) {
    const next = cursor + node.data.length
    if (offset <= next) return { node, offset: offset - cursor }
    cursor = next
  }
  const last = nodes[nodes.length - 1]!
  return { node: last, offset: last.data.length }
}

export function glyphTopAtLinear(nodes: readonly Text[], charIndex: number): number | null {
  let cursor = 0
  for (const node of nodes) {
    const next = cursor + node.data.length
    if (charIndex < next) {
      const local = charIndex - cursor
      const range = document.createRange()
      try {
        range.setStart(node, local)
        range.setEnd(node, local + 1)
        const rects = range.getClientRects()
        const rect = rects.length ? rects[rects.length - 1] : range.getBoundingClientRect()
        if (!rect || rect.height <= 0) return null
        return rect.top
      } catch {
        return null
      }
    }
    cursor = next
  }
  return null
}

export function visualLineOf(
  text: string,
  offset: number,
  glyphTopAt?: (charIndex: number) => number | null,
): { start: number; end: number } {
  const segment = logicalLineBounds(text, offset)
  if (!glyphTopAt || segment.end <= segment.start) return segment
  const probe = offset >= segment.end ? segment.end - 1 : offset
  if (probe < segment.start) return segment
  const origin = glyphTopAt(probe)
  if (origin == null) return segment
  const same = (charIndex: number) => {
    const top = glyphTopAt(charIndex)
    if (top == null) return null
    return Math.abs(top - origin) <= VISUAL_LINE_EPSILON_PX
  }
  let start = probe
  for (let index = probe; index >= segment.start; index -= 1) {
    const hit = same(index)
    if (hit == null) return segment
    if (!hit) break
    start = index
  }
  let last = probe
  for (let index = probe; index < segment.end; index += 1) {
    const hit = same(index)
    if (hit == null) return segment
    if (!hit) break
    last = index
  }
  return { start, end: last + 1 }
}
