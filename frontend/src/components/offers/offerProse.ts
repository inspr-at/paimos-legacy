import {
  OFFER_BULLET_MARKERS,
  type OfferBulletMarker,
  type OfferMarker,
  type OfferTextNode,
} from './types'

export const OFFER_PROSE_MAX_DEPTH = 5
export const OFFER_PROSE_MAX_NODES = 100
export const OFFER_PROSE_MAX_TEXT = 2000

export type Caret = { index: number; offset: number }
export type ProseRange = { anchor: Caret; focus: Caret }

export type ProseEdit = {
  nodes: OfferTextNode[]
  caret: Caret
  error?: string
}

export type ProseSnapshot = { nodes: OfferTextNode[]; caret: Caret }

function textLength(text: string): number {
  return [...text].length
}

export function bulletGlyph(depth: number): string {
  if (depth <= 0) return '•'
  if (depth === 1) return '◦'
  return '▪'
}

export const OFFER_BULLET_GLYPH: Record<OfferBulletMarker, string> = {
  disc: '•',
  circle: '◦',
  square: '▪',
  dash: '\u2013',
}

export type ProseListKind = 'none' | 'bullet' | 'ordered'
export type NumberingCommand = 'restart' | 'continue' | 'start' | 'follow'
export const OFFER_LIST_START_MAX = 9999
export type ProseListState = {
  kind: ProseListKind | 'mixed'
  bullet: OfferBulletMarker | 'mixed' | null
  outline: boolean | 'mixed'
  continued: boolean | 'mixed'
  start: number | 'mixed' | null
}

export function isOfferMarker(value: unknown): value is OfferMarker {
  return value === 'decimal' || (OFFER_BULLET_MARKERS as readonly string[]).includes(String(value))
}

function paragraph(text: string): OfferTextNode {
  return { kind: 'paragraph', text }
}

type ItemAnchor = {
  numbering?: 'outline'
  list_start?: number
  list_continue?: true
}

function anchorOf(node: OfferTextNode, keepAnchor: boolean): ItemAnchor | undefined {
  if (node.kind !== 'item') return undefined
  const anchor: ItemAnchor = {}
  if (node.numbering === 'outline') anchor.numbering = 'outline'
  if (keepAnchor && node.list_start && node.list_start > 0) anchor.list_start = node.list_start
  if (keepAnchor && node.list_continue) anchor.list_continue = true
  return anchor.numbering || anchor.list_start || anchor.list_continue ? anchor : undefined
}

function listItem(
  text: string,
  depth: number,
  marker?: OfferMarker,
  anchor?: ItemAnchor,
): OfferTextNode {
  const node: OfferTextNode = { kind: 'item', text }
  if (depth > 0) node.depth = depth
  if (marker) node.marker = marker
  if (marker === 'decimal' && anchor?.numbering === 'outline') node.numbering = 'outline'
  if (marker === 'decimal' && anchor?.list_start && anchor.list_start > 0)
    node.list_start = anchor.list_start
  else if (marker === 'decimal' && anchor?.list_continue) node.list_continue = true
  return node
}

function cloneNodes(nodes: OfferTextNode[]): OfferTextNode[] {
  return nodes.map((node) =>
    node.kind === 'item'
      ? listItem(node.text, node.depth ?? 0, node.marker, anchorOf(node, true))
      : paragraph(node.text),
  )
}

/** Tab and newline stay. Carriage return stays only in an untouched legacy paragraph. */
const ILLEGAL_NODE_TEXT = /[\u0000-\u0008\u000B-\u001F\u007F]/

function cleanText(text: string): string {
  return text.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '')
}

function normalizeLineEndings(text: string): string {
  return text.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
}

type GraphemeSegment = { index: number; segment: string }
type GraphemeSegmenter = { segment(input: string): Iterable<GraphemeSegment> }

const graphemeSegmenter = (() => {
  const intl = Intl as typeof Intl & {
    Segmenter?: new (locales?: string, options?: { granularity: 'grapheme' }) => GraphemeSegmenter
  }
  if (typeof intl.Segmenter !== 'function') return null
  return new intl.Segmenter(undefined, { granularity: 'grapheme' })
})()

function graphemeEdges(text: string): number[] {
  const edges = [0]
  if (graphemeSegmenter) {
    for (const part of graphemeSegmenter.segment(text)) {
      const end = part.index + part.segment.length
      if (end > (edges[edges.length - 1] ?? 0)) edges.push(end)
    }
  } else {
    for (let index = 0; index < text.length; ) {
      const code = text.codePointAt(index) ?? 0
      index += code > 0xffff ? 2 : 1
      edges.push(index)
    }
  }
  if (edges[edges.length - 1] !== text.length) edges.push(text.length)
  return edges
}

function floorEdge(edges: number[], offset: number): number {
  let edge = 0
  for (const candidate of edges) {
    if (candidate > offset) break
    edge = candidate
  }
  return edge
}

function ceilEdge(edges: number[], offset: number): number {
  for (const candidate of edges) if (candidate >= offset) return candidate
  return edges[edges.length - 1] ?? offset
}

function sameNode(node: OfferTextNode, text: string, keepAnchor = true): OfferTextNode {
  return node.kind === 'item'
    ? listItem(text, node.depth ?? 0, node.marker, anchorOf(node, keepAnchor))
    : paragraph(text)
}

function storedShape(nodes: OfferTextNode[]): OfferTextNode[] {
  const structured = nodes.length > 1 || nodes[0]?.kind === 'item'
  if (!structured) return nodes
  return nodes.map((node) => sameNode(node, normalizeLineEndings(node.text)))
}

/** A paragraph resets nesting. An item can be at most one level deeper than the previous item. */
export function clampProse(nodes: OfferTextNode[]): OfferTextNode[] {
  let previous = -1
  return storedShape(
    nodes.map((node) => {
      if (node.kind !== 'item') {
        previous = -1
        return paragraph(cleanText(node.text))
      }
      const requested = Math.max(0, Math.min(node.depth ?? 0, OFFER_PROSE_MAX_DEPTH))
      const max = previous < 0 ? 0 : Math.min(OFFER_PROSE_MAX_DEPTH, previous + 1)
      const depth = previous < 0 && node.list_continue ? requested : Math.min(requested, max)
      previous = depth
      return listItem(cleanText(node.text), depth, node.marker, anchorOf(node, true))
    }),
  )
}

function zeroFrom(values: number[], depth: number) {
  for (let i = depth; i < values.length; i++) values[i] = 0
}

/** One label per node. Plain decimals stay `1.`; outline decimals are `3`, `3.1`, `3.1.1`. */
export function proseMarkerLabels(nodes: OfferTextNode[]): string[] {
  const plain = Array<number>(OFFER_PROSE_MAX_DEPTH + 1).fill(0)
  const outline = Array<number>(OFFER_PROSE_MAX_DEPTH + 1).fill(0)
  const snaps: number[][] = []
  return nodes.map((node, index) => {
    if (node.kind !== 'item') {
      plain.fill(0)
      outline.fill(0)
      snaps[index] = []
      return ''
    }
    const depth = Math.max(0, Math.min(node.depth ?? 0, OFFER_PROSE_MAX_DEPTH))
    if (node.marker !== 'decimal') {
      zeroFrom(plain, depth)
      zeroFrom(outline, depth)
      snaps[index] = []
      return node.marker ? OFFER_BULLET_GLYPH[node.marker] : bulletGlyph(depth)
    }
    const levels = (node.numbering === 'outline' ? outline : plain).slice()
    for (let i = depth + 1; i < levels.length; i++) levels[i] = 0
    if (node.list_start && node.list_start > 0) levels[depth] = node.list_start
    else if (node.list_continue) {
      let previous = -1
      for (let j = index - 1; j >= 0; j--) {
        const earlier = nodes[j]
        if (
          earlier?.kind === 'item' &&
          earlier.marker === 'decimal' &&
          (earlier.depth ?? 0) === depth &&
          (earlier.numbering === 'outline') === (node.numbering === 'outline')
        ) {
          previous = j
          break
        }
      }
      const snap = previous >= 0 ? snaps[previous] : undefined
      if (snap) {
        for (let i = 0; i <= depth; i++) levels[i] = snap[i] ?? 0
      }
      levels[depth] = (levels[depth] ?? 0) + 1
    } else levels[depth] = (levels[depth] ?? 0) > 0 ? (levels[depth] ?? 0) + 1 : 1
    snaps[index] = levels
    if (node.numbering === 'outline') {
      outline.splice(0, outline.length, ...levels)
      const parts: number[] = []
      for (let i = 0; i <= depth; i++) parts.push((levels[i] ?? 0) > 0 ? levels[i]! : 1)
      return parts.join('.')
    }
    plain.splice(0, plain.length, ...levels)
    return `${levels[depth]}.`
  })
}

export function parseProseNodes(value: unknown): OfferTextNode[] | null {
  if (!Array.isArray(value) || value.length === 0 || value.length > OFFER_PROSE_MAX_NODES)
    return null
  const nodes: OfferTextNode[] = []
  let previous = -1
  for (const raw of value) {
    if (!raw || typeof raw !== 'object') return null
    const record = raw as {
      kind?: unknown
      text?: unknown
      depth?: unknown
      marker?: unknown
      numbering?: unknown
      list_start?: unknown
      list_continue?: unknown
    }
    if ((record.kind !== 'paragraph' && record.kind !== 'item') || typeof record.text !== 'string')
      return null
    if (textLength(record.text) > OFFER_PROSE_MAX_TEXT) return null
    if (ILLEGAL_NODE_TEXT.test(record.text)) return null
    const marker = record.marker
    if (marker != null && marker !== '' && !isOfferMarker(marker)) return null
    const numbering = record.numbering
    const listStart = record.list_start
    const listContinue = record.list_continue
    if (
      (numbering != null && numbering !== 'outline') ||
      (listStart != null &&
        (typeof listStart !== 'number' ||
          !Number.isInteger(listStart) ||
          listStart < 1 ||
          listStart > OFFER_LIST_START_MAX)) ||
      (listContinue != null && listContinue !== true && listContinue !== false)
    )
      return null
    if (record.kind === 'paragraph') {
      if (
        (record.depth != null && record.depth !== 0) ||
        (marker != null && marker !== '') ||
        numbering != null ||
        listStart != null ||
        listContinue === true
      )
        return null
      previous = -1
      nodes.push(paragraph(record.text))
      continue
    }
    const itemMarker = isOfferMarker(marker) ? marker : undefined
    if (
      itemMarker !== 'decimal' &&
      (numbering != null || listStart != null || listContinue === true)
    )
      return null
    if (listStart != null && listContinue === true) return null
    const depth = record.depth == null ? 0 : record.depth
    if (
      typeof depth !== 'number' ||
      !Number.isInteger(depth) ||
      depth < 0 ||
      depth > OFFER_PROSE_MAX_DEPTH
    )
      return null
    const max = previous < 0 ? 0 : Math.min(OFFER_PROSE_MAX_DEPTH, previous + 1)
    if (depth > max && !(previous < 0 && listContinue === true)) return null
    previous = depth
    nodes.push(
      listItem(record.text, depth, itemMarker, {
        numbering: numbering === 'outline' ? 'outline' : undefined,
        list_start: typeof listStart === 'number' ? listStart : undefined,
        list_continue: listContinue === true ? true : undefined,
      }),
    )
  }
  return nodes
}

/** Valid nodes win. Anything else stays one literal paragraph, including Markdown-like lines. */
export function proseNodes(body: string, nodes?: OfferTextNode[] | null): OfferTextNode[] {
  return parseProseNodes(nodes) ?? [paragraph(body ?? '')]
}

export function projectProse(nodes: OfferTextNode[]): string {
  const labels = proseMarkerLabels(nodes)
  return nodes
    .map((node, index) => {
      if (node.kind !== 'item') return node.text
      return `${'  '.repeat(node.depth ?? 0)}${labels[index]} ${node.text}`
    })
    .join('\n')
}

/** True when the value can be saved: a legacy body, or nodes the editor and server both accept. */
export function proseNodesStorable(nodes: OfferTextNode[]): boolean {
  const stored = persistProse(nodes)
  return !stored.nodes || parseProseNodes(stored.nodes) !== null
}

function offsetAfterStore(text: string, offset: number, structured: boolean): number {
  const bounded = Math.max(0, Math.min(offset, text.length))
  const head = text.slice(0, bounded)
  const stored = structured ? normalizeLineEndings(cleanText(head)) : cleanText(head)
  return snapCodeUnit(stored)
}

function snapCodeUnit(text: string): number {
  const end = text.length
  if (end === 0) return 0
  const prev = text.charCodeAt(end - 1)
  if (prev >= 0xd800 && prev <= 0xdbff) return end - 1
  return end
}

/** Map a toolbar range through the same newline normalization the stored nodes use. */
export function rangeAfterStore(
  before: OfferTextNode[],
  after: OfferTextNode[],
  range: ProseRange,
): ProseRange {
  const structured = after.length > 1 || after[0]?.kind === 'item'
  const mapCaret = (caret: Caret): Caret => {
    const index = Math.max(0, Math.min(caret.index, Math.max(0, after.length - 1)))
    const source =
      before[Math.max(0, Math.min(caret.index, Math.max(0, before.length - 1)))]?.text ?? ''
    const offset = offsetAfterStore(source, caret.offset, structured)
    const length = after[index]?.text.length ?? 0
    return { index, offset: Math.max(0, Math.min(offset, length)) }
  }
  return { anchor: mapCaret(range.anchor), focus: mapCaret(range.focus) }
}

/** A single paragraph is stored as plain `body` so legacy offers do not gain a nodes field. */
export function persistProse(nodes: OfferTextNode[]): { body: string; nodes?: OfferTextNode[] } {
  const clean = clampProse(nodes)
  const usable = clean.length > 0 ? clean : [paragraph('')]
  if (usable.length === 1 && usable[0]!.kind === 'paragraph') return { body: usable[0]!.text }
  return { body: projectProse(usable), nodes: usable }
}

export function offerBlockExceedsPage(
  blockHeight: number,
  available: number,
  continuationInset: number,
  sectionHeading: number,
): boolean {
  return blockHeight + (sectionHeading || continuationInset) > available
}

export function offerBlockOverflowMessage(index: number): string {
  return `Textbaustein ${index + 1} ist länger als eine Seite. Bitte kürzen oder auf mehrere Bausteine verteilen.`
}

export function proseRange(input: Caret | ProseRange): ProseRange {
  if ('anchor' in input && 'focus' in input) return input
  return { anchor: input, focus: input }
}

export function rangeEnds(range: ProseRange): { start: Caret; end: Caret } {
  const { anchor, focus } = range
  if (anchor.index < focus.index || (anchor.index === focus.index && anchor.offset <= focus.offset))
    return { start: anchor, end: focus }
  return { start: focus, end: anchor }
}

export function rangeCollapsed(range: ProseRange): boolean {
  const { start, end } = rangeEnds(range)
  return start.index === end.index && start.offset === end.offset
}

export function proseListState(nodes: OfferTextNode[], input: Caret | ProseRange): ProseListState {
  const { start, end } = rangeEnds(proseRange(input))
  const from = Math.max(0, Math.min(start.index, Math.max(0, nodes.length - 1)))
  const to = Math.max(0, Math.min(end.index, Math.max(0, nodes.length - 1)))
  const kinds = new Set<ProseListKind>()
  const bullets = new Set<OfferBulletMarker | 'depth'>()
  for (let index = from; index <= to; index++) {
    const node = nodes[index]
    if (!node || node.kind !== 'item') {
      kinds.add('none')
      continue
    }
    if (node.marker === 'decimal') {
      kinds.add('ordered')
      continue
    }
    kinds.add('bullet')
    bullets.add(node.marker ?? 'depth')
  }
  const outlines = new Set<boolean>()
  const continues = new Set<boolean>()
  const starts = new Set<number | null>()
  for (let index = from; index <= to; index++) {
    const node = nodes[index]
    if (!node || node.marker !== 'decimal') continue
    outlines.add(node.numbering === 'outline')
    continues.add(node.list_continue === true)
    starts.add(node.list_start && node.list_start > 0 ? node.list_start : null)
  }
  const kind = kinds.size === 1 ? [...kinds][0]! : 'mixed'
  const outline = outlines.size === 1 ? [...outlines][0]! : outlines.size > 1 ? 'mixed' : false
  const continued = continues.size === 1 ? [...continues][0]! : continues.size > 1 ? 'mixed' : false
  const startValue = starts.size === 1 ? [...starts][0]! : starts.size > 1 ? 'mixed' : null
  if (kind !== 'bullet') return { kind, bullet: null, outline, continued, start: startValue }
  if (bullets.size !== 1) return { kind, bullet: 'mixed', outline, continued, start: startValue }
  const only = [...bullets][0]!
  return {
    kind,
    bullet: only === 'depth' ? null : only,
    outline,
    continued,
    start: startValue,
  }
}

function clampCaret(nodes: OfferTextNode[], caret: Caret): Caret {
  const index = Math.max(0, Math.min(caret.index, Math.max(0, nodes.length - 1)))
  const length = nodes[index]?.text.length ?? 0
  return { index, offset: Math.max(0, Math.min(caret.offset, length)) }
}

function refuse(nodes: OfferTextNode[], caret: Caret, error: string): ProseEdit {
  return { nodes: cloneNodes(nodes), caret: clampCaret(nodes, caret), error }
}

/** Removes the selected text and keeps everything outside the range. */
export function deleteProseRange(nodes: OfferTextNode[], input: Caret | ProseRange): ProseEdit {
  const { start, end } = rangeEnds(proseRange(input))
  const from = clampCaret(nodes, start)
  const to = clampCaret(nodes, end)
  if (from.index === to.index && from.offset === to.offset)
    return { nodes: cloneNodes(nodes), caret: from }
  if (from.index === to.index) {
    const node = nodes[from.index]!
    const edges = graphemeEdges(node.text)
    const cut = floorEdge(edges, from.offset)
    const keep = ceilEdge(edges, to.offset)
    // Shrinking never creates a longer node. A legacy body may already be past the cap.
    if (cut === keep) return { nodes: cloneNodes(nodes), caret: { index: from.index, offset: cut } }
    const text = node.text.slice(0, cut) + node.text.slice(keep)
    const next = cloneNodes(nodes)
    next[from.index] = sameNode(node, text)
    return { nodes: clampProse(next), caret: { index: from.index, offset: cut } }
  }
  const left = nodes[from.index]!
  const right = nodes[to.index]!
  const cut = floorEdge(graphemeEdges(left.text), from.offset)
  const keep = ceilEdge(graphemeEdges(right.text), to.offset)
  const text = left.text.slice(0, cut) + right.text.slice(keep)
  if (textLength(text) > OFFER_PROSE_MAX_TEXT) {
    return refuse(
      nodes,
      { index: from.index, offset: cut },
      'Dieser Absatz ist zu lang. Nichts wurde gelöscht.',
    )
  }
  const next = clampProse([
    ...nodes.slice(0, from.index),
    sameNode(left, text),
    ...nodes.slice(to.index + 1),
  ])
  return {
    nodes: next.length > 0 ? next : [paragraph('')],
    caret: { index: from.index, offset: cut },
  }
}

export function toggleItem(nodes: OfferTextNode[], index: number): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node) return next
  if (node.kind === 'item') next[index] = paragraph(node.text)
  else {
    const prev = next[index - 1]
    next[index] = listItem(node.text, prev?.kind === 'item' ? (prev.depth ?? 0) : 0)
  }
  return clampProse(next)
}

function previousItemDepth(nodes: OfferTextNode[], index: number): number {
  let previous = -1
  for (let i = 0; i < index; i++) {
    const node = nodes[i]
    previous = node?.kind === 'item' ? (node.depth ?? 0) : -1
  }
  return previous
}

/** Depth the list structure allows, ignoring a continuation that is only holding its current level. */
function structuralDepth(nodes: OfferTextNode[], index: number, requested: number): number {
  const previous = previousItemDepth(nodes, index)
  const max = previous < 0 ? 0 : Math.min(OFFER_PROSE_MAX_DEPTH, previous + 1)
  return Math.max(0, Math.min(requested, max))
}

/** A real level change keeps outline style and continuation, and drops a level-specific start. */
function movedAnchor(node: OfferTextNode): ItemAnchor | undefined {
  const anchor: ItemAnchor = {}
  if (node.numbering === 'outline') anchor.numbering = 'outline'
  if (node.list_continue) anchor.list_continue = true
  return anchor.numbering || anchor.list_continue ? anchor : undefined
}

export function indentItem(nodes: OfferTextNode[], index: number): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node) return next
  if (node.kind !== 'item') {
    const prev = next[index - 1]
    const depth = prev?.kind === 'item' ? Math.min(OFFER_PROSE_MAX_DEPTH, (prev.depth ?? 0) + 1) : 0
    const marker = prev?.kind === 'item' ? prev.marker : undefined
    const anchor: ItemAnchor | undefined =
      prev?.kind === 'item' && prev.numbering === 'outline' ? { numbering: 'outline' } : undefined
    next[index] = listItem(node.text, depth, marker, anchor)
  } else {
    const current = node.depth ?? 0
    const nextDepth = structuralDepth(next, index, current + 1)
    if (nextDepth <= current) return next
    next[index] = listItem(node.text, nextDepth, node.marker, movedAnchor(node))
  }
  return clampProse(next)
}

export function outdentItem(nodes: OfferTextNode[], index: number): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node || node.kind !== 'item') return next
  const current = node.depth ?? 0
  if (current <= 0) {
    next[index] = paragraph(node.text)
    return clampProse(next)
  }
  const stepped = current - 1
  const structural = structuralDepth(next, index, stepped)
  const nextDepth = node.list_continue && structural < stepped ? stepped : structural
  if (nextDepth === current) return next
  next[index] = listItem(node.text, nextDepth, node.marker, movedAnchor(node))
  return clampProse(next)
}

function siblingDepth(nodes: OfferTextNode[], index: number): number {
  const prev = nodes[index - 1]
  return prev?.kind === 'item' ? (prev.depth ?? 0) : 0
}

export function setListKind(
  nodes: OfferTextNode[],
  index: number,
  kind: ProseListKind,
): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node) return next
  if (kind === 'none') {
    next[index] = paragraph(node.text)
    return clampProse(next)
  }
  const depth = node.kind === 'item' ? (node.depth ?? 0) : siblingDepth(next, index)
  if (kind === 'ordered') {
    const anchor: ItemAnchor = { numbering: 'outline' }
    if (node.kind === 'item' && node.marker === 'decimal') {
      if (node.list_start && node.list_start > 0) anchor.list_start = node.list_start
      else if (node.list_continue) anchor.list_continue = true
    }
    next[index] = listItem(node.text, depth, 'decimal', anchor)
    return clampProse(next)
  }
  const marker =
    node.kind === 'item' && node.marker && node.marker !== 'decimal' ? node.marker : undefined
  next[index] = listItem(node.text, depth, marker)
  return clampProse(next)
}

export function setBulletMarker(
  nodes: OfferTextNode[],
  index: number,
  marker: OfferBulletMarker,
): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node) return next
  const depth = node.kind === 'item' ? (node.depth ?? 0) : siblingDepth(next, index)
  next[index] = listItem(node.text, depth, marker)
  return clampProse(next)
}

/** Restart, continue, or a positive start. Follow keeps the outline style and drops anchors. */
export function setDecimalControl(
  nodes: OfferTextNode[],
  index: number,
  command: NumberingCommand,
  start?: number,
): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node) return next
  const depth = node.kind === 'item' ? (node.depth ?? 0) : siblingDepth(next, index)
  const anchor: ItemAnchor = { numbering: 'outline' }
  if (command === 'continue') anchor.list_continue = true
  else if (command === 'restart') anchor.list_start = 1
  else if (command === 'start') {
    if (start == null || !Number.isInteger(start) || start < 1 || start > OFFER_LIST_START_MAX)
      return next
    anchor.list_start = start
  }
  next[index] = listItem(node.text, depth, 'decimal', anchor)
  return clampProse(next)
}

export function editProseRange(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  op: (nodes: OfferTextNode[], index: number) => OfferTextNode[],
): OfferTextNode[] {
  const { start, end } = rangeEnds(proseRange(input))
  let next = cloneNodes(nodes)
  const last = Math.min(end.index, next.length - 1)
  for (let index = Math.max(0, start.index); index <= last; index++) next = op(next, index)
  return next
}

/** Toolbar and Tab. Refuses a list the editor or server would reject, without changing the text. */
export function applyStructure(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  op: (nodes: OfferTextNode[], index: number) => OfferTextNode[],
): ProseEdit {
  const range = proseRange(input)
  const next = editProseRange(nodes, range, op)
  const end = rangeEnds(range).end
  const structured = next.length > 1 || next[0]?.kind === 'item'
  const before = nodes[end.index]?.text ?? ''
  const caret = clampCaret(next, {
    index: end.index,
    offset: offsetAfterStore(before, end.offset, structured),
  })
  if (!proseNodesStorable(next)) return refuse(nodes, caret, 'Dieser Absatz ist zu lang.')
  return { nodes: next, caret }
}

export function enterProse(nodes: OfferTextNode[], input: Caret | ProseRange): ProseEdit {
  const cleared = deleteProseRange(nodes, input)
  if (cleared.error) return refuse(nodes, cleared.caret, cleared.error)
  const caret = cleared.caret
  const current = cleared.nodes[caret.index]
  if (!current) return cleared
  if (current.kind === 'item' && current.text === '') {
    return {
      nodes: outdentItem(cleared.nodes, caret.index),
      caret: { index: caret.index, offset: 0 },
    }
  }
  if (cleared.nodes.length >= OFFER_PROSE_MAX_NODES) {
    return refuse(nodes, caret, 'Die Aufzählung hat das Maximum von 100 Einträgen erreicht.')
  }
  const left = current.text.slice(0, caret.offset)
  const right = current.text.slice(caret.offset)
  const edit: ProseEdit = {
    nodes: clampProse([
      ...cleared.nodes.slice(0, caret.index),
      sameNode(current, left, true),
      sameNode(current, right, false),
      ...cleared.nodes.slice(caret.index + 1),
    ]),
    caret: { index: caret.index + 1, offset: 0 },
  }
  if (!proseNodesStorable(edit.nodes)) return refuse(nodes, caret, 'Dieser Absatz ist zu lang.')
  return edit
}

/** Keeps the same paragraph or item and inserts one newline. */
export function insertSoftBreak(nodes: OfferTextNode[], input: Caret | ProseRange): ProseEdit {
  const cleared = deleteProseRange(nodes, input)
  if (cleared.error) return refuse(nodes, cleared.caret, cleared.error)
  const caret = cleared.caret
  const node = cleared.nodes[caret.index]
  if (!node) return cleared
  const text = `${node.text.slice(0, caret.offset)}\n${node.text.slice(caret.offset)}`
  if (textLength(text) > OFFER_PROSE_MAX_TEXT)
    return refuse(nodes, caret, 'Dieser Absatz ist zu lang.')
  const next = cloneNodes(cleared.nodes)
  next[caret.index] = sameNode(node, text)
  return { nodes: next, caret: { index: caret.index, offset: caret.offset + 1 } }
}

export function backspaceProse(nodes: OfferTextNode[], input: Caret | ProseRange): ProseEdit {
  const range = proseRange(input)
  if (!rangeCollapsed(range)) return deleteProseRange(nodes, range)
  const caret = clampCaret(nodes, range.focus)
  const current = nodes[caret.index]
  if (!current) return { nodes: cloneNodes(nodes), caret }
  if (caret.offset > 0) {
    return deleteProseRange(nodes, {
      anchor: { index: caret.index, offset: caret.offset - 1 },
      focus: caret,
    })
  }
  if (current.kind === 'item')
    return { nodes: outdentItem(nodes, caret.index), caret: { index: caret.index, offset: 0 } }
  if (caret.index === 0) return { nodes: cloneNodes(nodes), caret }
  const prev = nodes[caret.index - 1]!
  if (textLength(prev.text + current.text) > OFFER_PROSE_MAX_TEXT)
    return refuse(nodes, caret, 'Dieser Absatz ist zu lang.')
  const merged = sameNode(prev, prev.text + current.text)
  return {
    nodes: clampProse([
      ...nodes.slice(0, caret.index - 1),
      merged,
      ...nodes.slice(caret.index + 1),
    ]),
    caret: { index: caret.index - 1, offset: prev.text.length },
  }
}

export function deleteForwardProse(nodes: OfferTextNode[], input: Caret | ProseRange): ProseEdit {
  const range = proseRange(input)
  if (!rangeCollapsed(range)) return deleteProseRange(nodes, range)
  const caret = clampCaret(nodes, range.focus)
  const current = nodes[caret.index]
  if (!current) return { nodes: cloneNodes(nodes), caret }
  if (caret.offset < current.text.length) {
    return deleteProseRange(nodes, {
      anchor: caret,
      focus: { index: caret.index, offset: caret.offset + 1 },
    })
  }
  if (caret.index >= nodes.length - 1) return { nodes: cloneNodes(nodes), caret }
  const nextNode = nodes[caret.index + 1]!
  if (textLength(current.text + nextNode.text) > OFFER_PROSE_MAX_TEXT)
    return refuse(nodes, caret, 'Dieser Absatz ist zu lang.')
  return {
    nodes: clampProse([
      ...nodes.slice(0, caret.index),
      sameNode(current, current.text + nextNode.text),
      ...nodes.slice(caret.index + 2),
    ]),
    caret,
  }
}

export function insertProseText(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  raw: string,
): ProseEdit {
  const caretBefore = clampCaret(nodes, rangeEnds(proseRange(input)).start)
  const originalLength = textLength(nodes[caretBefore.index]?.text ?? '')
  const cleared = deleteProseRange(nodes, input)
  if (cleared.error) return refuse(nodes, caretBefore, cleared.error)
  const caret = cleared.caret
  const current = cleared.nodes[caret.index]
  if (!current) return cleared
  const parts = cleanText(raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n')).split('\n')
  if (cleared.nodes.length - 1 + parts.length > OFFER_PROSE_MAX_NODES) {
    return refuse(
      nodes,
      caretBefore,
      'Die Aufzählung hat das Maximum von 100 Einträgen erreicht. Nichts wurde eingefügt.',
    )
  }
  const head = current.text.slice(0, caret.offset)
  const tail = current.text.slice(caret.offset)
  const created = parts.map((part, index) => {
    const text = `${index === 0 ? head : ''}${part}${index === parts.length - 1 ? tail : ''}`
    return sameNode(current, text, index === 0)
  })
  const limit = Math.max(OFFER_PROSE_MAX_TEXT, originalLength)
  const overLimit =
    created.length === 1
      ? textLength(created[0]!.text) > limit
      : created.some((node) => textLength(node.text) > OFFER_PROSE_MAX_TEXT)
  if (overLimit)
    return refuse(nodes, caretBefore, 'Dieser Absatz ist zu lang. Nichts wurde eingefügt.')
  const lastPart = parts[parts.length - 1] ?? ''
  const edit: ProseEdit = {
    nodes: clampProse([
      ...cleared.nodes.slice(0, caret.index),
      ...created,
      ...cleared.nodes.slice(caret.index + 1),
    ]),
    caret: {
      index: caret.index + created.length - 1,
      offset: (parts.length === 1 ? head + lastPart : lastPart).length,
    },
  }
  if (!proseNodesStorable(edit.nodes))
    return refuse(nodes, caretBefore, 'Dieser Absatz ist zu lang. Nichts wurde eingefügt.')
  return edit
}

/** Adopts browser-visible text after composition or a spelling replacement. Markup is ignored. */
export function reconcileProseTexts(
  nodes: OfferTextNode[],
  texts: string[],
  caret: Caret,
): ProseEdit {
  const safeCaret = clampCaret(nodes, caret)
  if (texts.length !== nodes.length) return refuse(nodes, safeCaret, 'Dieser Absatz ist zu lang.')
  const next = nodes.map((node, index) => sameNode(node, cleanText(texts[index] ?? '')))
  if (next.every((node, index) => node.text === nodes[index]?.text))
    return { nodes: cloneNodes(nodes), caret: safeCaret }
  for (let index = 0; index < next.length; index++) {
    const cap = Math.max(OFFER_PROSE_MAX_TEXT, textLength(nodes[index]?.text ?? ''))
    if (textLength(next[index]?.text ?? '') > cap)
      return refuse(nodes, safeCaret, 'Dieser Absatz ist zu lang.')
  }
  const edit: ProseEdit = { nodes: clampProse(next), caret: clampCaret(next, caret) }
  if (!proseNodesStorable(edit.nodes)) return refuse(nodes, safeCaret, 'Dieser Absatz ist zu lang.')
  return edit
}

const pastedBlocks = new Set([
  'P',
  'DIV',
  'LI',
  'UL',
  'OL',
  'H1',
  'H2',
  'H3',
  'H4',
  'H5',
  'H6',
  'TR',
  'BLOCKQUOTE',
  'PRE',
])

/** Plain text wins. HTML is reduced to lines of text and never kept as markup. */
export function clipboardPlain(plain: string, html = ''): string {
  if (plain) return plain.replace(/\u0000/g, '')
  if (!html || typeof DOMParser === 'undefined') return ''
  const doc = new DOMParser().parseFromString(html, 'text/html')
  doc.querySelectorAll('script,style,noscript').forEach((node) => node.remove())
  const lines: string[] = []
  const visibleLine = (value: string) =>
    value
      .replace(/\u00a0/g, ' ')
      .replace(/\s+/g, ' ')
      .trim()
  const walk = (el: Element) => {
    const blocks = [...el.children].filter((child) => pastedBlocks.has(child.tagName))
    if (blocks.length === 0) {
      const text = (el.textContent ?? '').replace(/\u00a0/g, ' ').trim()
      if (text) lines.push(text)
      return
    }
    let own = ''
    const flush = () => {
      const text = visibleLine(own)
      own = ''
      if (text) lines.push(text)
    }
    const containsBlock = (node: Element): boolean =>
      pastedBlocks.has(node.tagName) || [...node.children].some(containsBlock)
    const visit = (node: Node) => {
      if (node.nodeType === Node.TEXT_NODE) {
        own += node.textContent ?? ''
        return
      }
      if (!(node instanceof Element)) return
      if (pastedBlocks.has(node.tagName)) {
        flush()
        walk(node)
        return
      }
      if (containsBlock(node)) {
        for (const child of node.childNodes) visit(child)
        return
      }
      own += node.textContent ?? ''
    }
    for (const child of el.childNodes) visit(child)
    flush()
  }
  walk(doc.body)
  return lines.join('\n')
}

export type ProseHistory = {
  push(current: ProseSnapshot): void
  undo(current: ProseSnapshot): ProseSnapshot | null
  redo(current: ProseSnapshot): ProseSnapshot | null
}

/** Editor memory stays with the block object. It is not part of the saved document. */
export type SectionEditorMemory = { history: ProseHistory; caret: Caret | null }

export function createProseHistory(limit = 100): ProseHistory {
  const undoStack: ProseSnapshot[] = []
  const redoStack: ProseSnapshot[] = []
  const copy = (snap: ProseSnapshot): ProseSnapshot => ({
    nodes: snap.nodes.map((node) => ({ ...node })),
    caret: { ...snap.caret },
  })
  return {
    push(current: ProseSnapshot) {
      const next = copy(current)
      const prev = undoStack[undoStack.length - 1]
      if (prev && JSON.stringify(prev) === JSON.stringify(next)) return
      undoStack.push(next)
      if (undoStack.length > limit) undoStack.shift()
      redoStack.length = 0
    },
    undo(current: ProseSnapshot): ProseSnapshot | null {
      const item = undoStack.pop()
      if (!item) return null
      redoStack.push(copy(current))
      return copy(item)
    },
    redo(current: ProseSnapshot): ProseSnapshot | null {
      const item = redoStack.pop()
      if (!item) return null
      undoStack.push(copy(current))
      return copy(item)
    },
  }
}
