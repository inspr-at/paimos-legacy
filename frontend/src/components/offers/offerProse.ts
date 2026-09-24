import {
  OFFER_BULLET_MARKERS,
  type OfferBulletMarker,
  type OfferInlineMark,
  type OfferMarker,
  type OfferTextNode,
} from './types'

export const OFFER_PROSE_MAX_DEPTH = 5
export const OFFER_PROSE_MAX_NODES = 100
export const OFFER_PROSE_MAX_TEXT = 2000
/**
 * Sent with offer and template saves so an older editor cannot drop newer prose.
 * 3 understands character marks. 2 still round-trips depth, symbols and marker layout.
 */
export const OFFER_PROSE_WRITER_VERSION = 3
export type { OfferInlineMark }
export type InlineMarkName = 'normal' | 'bold' | 'italic'
export type InlineMarkFlag = boolean | 'mixed'
export type InlineMarkState = { bold: InlineMarkFlag; italic: InlineMarkFlag }

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
export type NumberingCommand =
  | 'restart'
  | 'continue'
  | 'start'
  | 'follow'
  | 'section'
  | 'independent'
  | 'bound'
  | 'unbound'
export type NumberingMode = 'restart' | 'continue' | 'start' | 'section' | 'independent'
export const OFFER_LIST_START_MAX = 9999
export const MARKER_X_MM = { min: -30, max: 30 } as const
export const MARKER_Y_MM = { min: -20, max: 20 } as const
export const TEXT_START_MM = { min: -20, max: 40 } as const
/** Why indent or outdent would leave the current selection unchanged. */
export type LevelLimit = 'max-depth' | 'no-previous' | 'boundary' | 'not-list' | 'mixed'
export type ProseListState = {
  kind: ProseListKind | 'mixed'
  bullet: OfferBulletMarker | 'mixed' | null
  outline: boolean | 'mixed'
  continued: boolean | 'mixed'
  start: number | 'mixed' | null
  sectionBound: boolean | 'mixed'
  indent: boolean
  outdent: boolean
  indentLimit: LevelLimit | null
  outdentLimit: LevelLimit | null
  /** Item depth. The inspector shows this as level depth+1. */
  level: number | 'mixed' | null
  glyph: string | 'mixed' | null
  markerX: number | 'mixed' | null
  markerY: number | 'mixed' | null
  textStart: number | 'mixed' | null
  /** Computed marker of the focused item, including continuation and ancestor starts. */
  markerLabel: string | null
  /** Active when every covered character has the flag. Mixed when only some do. */
  marks?: InlineMarkState
}

/** First selected item keeps the command. Later items follow, except ownership which stays per item. */
export function numberingCommandForIndex(mode: NumberingMode, position: number): NumberingCommand {
  if (position <= 0) return mode
  if (mode === 'section') return 'bound'
  if (mode === 'independent') return 'unbound'
  return 'follow'
}

export function isOfferMarker(value: unknown): value is OfferMarker {
  return value === 'decimal' || (OFFER_BULLET_MARKERS as readonly string[]).includes(String(value))
}

function highSurrogate(code: number): boolean {
  return code >= 0xd800 && code <= 0xdbff
}

function lowSurrogate(code: number): boolean {
  return code >= 0xdc00 && code <= 0xdfff
}

/** A boundary may sit on a character edge, never between the two units of one pair. */
export function inlineBoundary(text: string, offset: number): boolean {
  if (offset <= 0 || offset >= text.length) return true
  return !(highSurrogate(text.charCodeAt(offset - 1)) && lowSurrogate(text.charCodeAt(offset)))
}

function styleBits(mark: OfferInlineMark): number {
  return (mark.bold ? 1 : 0) | (mark.italic ? 2 : 0)
}

function marksFromBits(bits: readonly number[]): OfferInlineMark[] | undefined {
  const marks: OfferInlineMark[] = []
  let index = 0
  while (index < bits.length) {
    const bitsAt = bits[index] ?? 0
    let end = index + 1
    while (end < bits.length && bits[end] === bitsAt) end += 1
    if (bitsAt) {
      const mark: OfferInlineMark = { start: index, end }
      if (bitsAt & 1) mark.bold = true
      if (bitsAt & 2) mark.italic = true
      marks.push(mark)
    }
    index = end
  }
  return marks.length ? marks : undefined
}

function bitsFromMarks(text: string, marks: readonly OfferInlineMark[] | undefined): number[] {
  const bits = Array<number>(text.length).fill(0)
  for (const mark of marks ?? []) {
    const style = styleBits(mark)
    const start = Math.max(0, mark.start)
    const end = Math.min(text.length, mark.end)
    for (let index = start; index < end; index += 1) bits[index] = style
  }
  return bits
}

function copyMarks(marks: readonly OfferInlineMark[] | undefined): OfferInlineMark[] | undefined {
  if (!marks?.length) return undefined
  return marks.map((mark) => ({ ...mark }))
}

function marksAfterInsert(
  text: string,
  marks: OfferInlineMark[] | undefined,
  at: number,
  length: number,
  style: number,
): OfferInlineMark[] | undefined {
  const bits = bitsFromMarks(text, marks)
  const extra = Array<number>(length).fill(style)
  return marksFromBits(bits.slice(0, at).concat(extra, bits.slice(at)))
}

export function typingStyleBits(nodes: readonly OfferTextNode[], caret: Caret): number {
  const index = Math.max(0, Math.min(caret.index, Math.max(0, nodes.length - 1)))
  const node = nodes[index]
  if (!node?.text) return 0
  const bits = bitsFromMarks(node.text, node.marks)
  const offset = Math.max(0, Math.min(caret.offset, bits.length))
  if (offset <= 0) return bits[0] ?? 0
  return bits[offset - 1] ?? 0
}

export function toggleTypingBits(bits: number, mark: InlineMarkName): number {
  if (mark === 'normal') return 0
  return bits ^ (mark === 'bold' ? 1 : 2)
}

export function markStateFromBits(bits: number): InlineMarkState {
  return { bold: !!(bits & 1), italic: !!(bits & 2) }
}

function reconcileMarkBits(
  oldText: string,
  newText: string,
  marks: OfferInlineMark[] | undefined,
  typing?: number,
): OfferInlineMark[] | undefined {
  let prefix = 0
  const limit = Math.min(oldText.length, newText.length)
  while (prefix < limit && oldText[prefix] === newText[prefix]) prefix += 1
  if (prefix > 0 && !inlineBoundary(oldText, prefix)) prefix -= 1
  let suffix = 0
  while (
    suffix < oldText.length - prefix &&
    suffix < newText.length - prefix &&
    oldText[oldText.length - 1 - suffix] === newText[newText.length - 1 - suffix]
  )
    suffix += 1
  if (suffix > 0 && !inlineBoundary(newText, newText.length - suffix)) suffix -= 1
  const bits = bitsFromMarks(oldText, marks)
  const head = bits.slice(0, prefix)
  const tail = suffix ? bits.slice(bits.length - suffix) : []
  const inherited = typing ?? (head.length ? head[head.length - 1]! : (tail[0] ?? 0))
  const mid = Array<number>(Math.max(0, newText.length - prefix - suffix)).fill(inherited)
  return marksFromBits(head.concat(mid, tail))
}

export function proseMarkState(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  typing?: number | null,
): InlineMarkState {
  const { start, end } = rangeEnds(proseRange(input))
  const last = Math.max(0, nodes.length - 1)
  const from = Math.max(0, Math.min(start.index, last))
  const to = Math.max(0, Math.min(end.index, last))
  const covered: number[] = []
  for (let index = from; index <= to; index += 1) {
    const node = nodes[index]
    if (!node) continue
    const bits = bitsFromMarks(node.text, node.marks)
    const sliceFrom = index === from ? Math.max(0, Math.min(start.offset, bits.length)) : 0
    const sliceTo = index === to ? Math.max(sliceFrom, Math.min(end.offset, bits.length)) : bits.length
    if (from === to && sliceFrom === sliceTo) {
      return markStateFromBits(typing ?? typingStyleBits(nodes, { index: from, offset: sliceFrom }))
    }
    covered.push(...bits.slice(sliceFrom, sliceTo))
  }
  if (!covered.length) {
    return markStateFromBits(typing ?? typingStyleBits(nodes, { index: from, offset: start.offset }))
  }
  const flag = (bit: number): InlineMarkFlag => {
    const some = covered.some((value) => value & bit)
    const every = covered.every((value) => value & bit)
    if (every) return true
    if (some) return 'mixed'
    return false
  }
  return { bold: flag(1), italic: flag(2) }
}

export function applyInlineStyle(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  mark: InlineMarkName,
): ProseEdit {
  const range = proseRange(input)
  const { start, end } = rangeEnds(range)
  const caret = clampCaret(nodes, end)
  if (rangeCollapsed(range)) return { nodes: cloneNodes(nodes), caret }
  const next = cloneNodes(nodes)
  const from = clampCaret(next, start)
  const to = clampCaret(next, end)
  const flag = mark === 'bold' ? 1 : mark === 'italic' ? 2 : 0
  const slices: { index: number; start: number; end: number; bits: number[] }[] = []
  let covered = 0
  let flagged = 0
  for (let index = from.index; index <= to.index; index += 1) {
    const node = next[index]
    if (!node) continue
    const sliceFrom = index === from.index ? from.offset : 0
    const sliceTo = index === to.index ? to.offset : node.text.length
    const snapped = snapInline(node.text, sliceFrom, sliceTo)
    if (snapped.start === snapped.end) continue
    const bits = bitsFromMarks(node.text, node.marks)
    slices.push({ index, start: snapped.start, end: snapped.end, bits })
    for (let unit = snapped.start; unit < snapped.end; unit += 1) {
      covered += 1
      if (flag && bits[unit]! & flag) flagged += 1
    }
  }
  const enable = flag !== 0 && flagged < covered
  for (const slice of slices) {
    const node = next[slice.index]
    if (!node) continue
    if (mark === 'normal') {
      for (let unit = slice.start; unit < slice.end; unit += 1) slice.bits[unit] = 0
    } else if (enable) {
      for (let unit = slice.start; unit < slice.end; unit += 1) slice.bits[unit] = slice.bits[unit]! | flag
    } else {
      for (let unit = slice.start; unit < slice.end; unit += 1)
        slice.bits[unit] = slice.bits[unit]! & ~flag
    }
    next[slice.index] = sameNode(node, node.text, true, marksFromBits(slice.bits) ?? null)
  }
  const edit: ProseEdit = { nodes: clampProse(next), caret }
  if (!proseNodesStorable(edit.nodes)) return refuse(nodes, caret, 'Dieser Absatz ist zu lang.')
  return edit
}

/** Rejects overlap, empty flags, out-of-range offsets and a split surrogate. Merges equal neighbours. */
export function parseInlineMarks(
  text: string,
  value: unknown,
): OfferInlineMark[] | undefined | null {
  if (value == null) return undefined
  if (!Array.isArray(value)) return null
  if (value.length === 0) return undefined
  const parsed: OfferInlineMark[] = []
  for (const raw of value) {
    if (!raw || typeof raw !== 'object') return null
    const record = raw as { start?: unknown; end?: unknown; bold?: unknown; italic?: unknown }
    if (
      typeof record.start !== 'number' ||
      typeof record.end !== 'number' ||
      !Number.isInteger(record.start) ||
      !Number.isInteger(record.end)
    )
      return null
    if (record.bold != null && record.bold !== true) return null
    if (record.italic != null && record.italic !== true) return null
    if (!record.bold && !record.italic) return null
    if (record.start < 0 || record.end <= record.start || record.end > text.length) return null
    if (!inlineBoundary(text, record.start) || !inlineBoundary(text, record.end)) return null
    const mark: OfferInlineMark = { start: record.start, end: record.end }
    if (record.bold) mark.bold = true
    if (record.italic) mark.italic = true
    parsed.push(mark)
  }
  parsed.sort((a, b) => a.start - b.start || a.end - b.end)
  for (let index = 1; index < parsed.length; index += 1) {
    if (parsed[index]!.start < parsed[index - 1]!.end) return null
  }
  return marksFromBits(bitsFromMarks(text, parsed))
}

function snapInline(text: string, start: number, end: number): { start: number; end: number } {
  let from = Math.max(0, Math.min(start, text.length))
  let to = Math.max(from, Math.min(end, text.length))
  if (from > 0 && !inlineBoundary(text, from)) from -= 1
  if (to < text.length && !inlineBoundary(text, to)) to += 1
  return { start: from, end: to }
}

function paragraph(text: string, marks?: OfferInlineMark[]): OfferTextNode {
  const node: OfferTextNode = { kind: 'paragraph', text }
  const stored = copyMarks(marks)
  if (stored) node.marks = stored
  return node
}

type ItemAnchor = {
  numbering?: 'outline'
  list_start?: number
  list_continue?: true
  section_bound?: true
}

type ItemLayout = {
  glyph?: string
  marker_x_mm?: number
  marker_y_mm?: number
  text_start_mm?: number
}

export function plainGlyph(value: string): string | null {
  const text = value.trim()
  if (!text) return ''
  if ([...text].length > 4) return null
  if (/[<>&]/.test(text) || /[\u0000-\u001F\u007F]/.test(text)) return null
  return text
}

/** One decimal place, half away from zero, then the inclusive range. −1.25 becomes −1.3. */
export function canonMarkerMm(value: number, min: number, max: number): number | null {
  if (!Number.isFinite(value)) return null
  const scaled = value * 10
  const away = Math.sign(scaled) * Math.round(Math.abs(scaled))
  const tenth = (away === 0 ? 0 : away) / 10
  if (tenth < min || tenth > max) return null
  return tenth
}

function anchorOf(node: OfferTextNode, keepAnchor: boolean): ItemAnchor | undefined {
  if (node.kind !== 'item') return undefined
  const anchor: ItemAnchor = {}
  if (node.numbering === 'outline') anchor.numbering = 'outline'
  if (node.section_bound) anchor.section_bound = true
  if (keepAnchor && node.list_start && node.list_start > 0) anchor.list_start = node.list_start
  if (keepAnchor && node.list_continue) anchor.list_continue = true
  return anchor.numbering || anchor.list_start || anchor.list_continue ? anchor : undefined
}

function layoutOf(node: OfferTextNode, glyph = true): ItemLayout | undefined {
  if (node.kind !== 'item') return undefined
  const layout: ItemLayout = {}
  if (glyph && node.glyph) layout.glyph = node.glyph
  if (node.marker_x_mm) layout.marker_x_mm = node.marker_x_mm
  if (node.marker_y_mm) layout.marker_y_mm = node.marker_y_mm
  if (node.text_start_mm) layout.text_start_mm = node.text_start_mm
  return layout.glyph || layout.marker_x_mm || layout.marker_y_mm || layout.text_start_mm
    ? layout
    : undefined
}

function listItem(
  text: string,
  depth: number,
  marker?: OfferMarker,
  anchor?: ItemAnchor,
  layout?: ItemLayout,
  marks?: OfferInlineMark[],
): OfferTextNode {
  const node: OfferTextNode = { kind: 'item', text }
  const stored = copyMarks(marks)
  if (stored) node.marks = stored
  if (depth > 0) node.depth = depth
  if (marker) node.marker = marker
  if (marker === 'decimal' && anchor?.numbering === 'outline') node.numbering = 'outline'
  if (marker === 'decimal' && anchor?.section_bound) node.section_bound = true
  if (marker === 'decimal' && anchor?.list_start && anchor.list_start > 0)
    node.list_start = anchor.list_start
  else if (marker === 'decimal' && anchor?.list_continue) node.list_continue = true
  if (marker !== 'decimal' && layout?.glyph) node.glyph = layout.glyph
  if (layout?.marker_x_mm) node.marker_x_mm = layout.marker_x_mm
  if (layout?.marker_y_mm) node.marker_y_mm = layout.marker_y_mm
  if (layout?.text_start_mm) node.text_start_mm = layout.text_start_mm
  return node
}

function cloneNodes(nodes: OfferTextNode[]): OfferTextNode[] {
  return nodes.map((node) =>
    node.kind === 'item'
      ? listItem(
          node.text,
          node.depth ?? 0,
          node.marker,
          anchorOf(node, true),
          layoutOf(node),
          node.marks,
        )
      : paragraph(node.text, node.marks),
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

function sameNode(
  node: OfferTextNode,
  text: string,
  keepAnchor = true,
  marks?: OfferInlineMark[] | null,
): OfferTextNode {
  const next = marks === undefined ? node.marks : (marks ?? undefined)
  return node.kind === 'item'
    ? listItem(text, node.depth ?? 0, node.marker, anchorOf(node, keepAnchor), layoutOf(node), next)
    : paragraph(text, next)
}

/** Map marks through a rewrite that deletes or replaces one code unit at a time. */
function remapMarks(
  source: string,
  marks: OfferInlineMark[] | undefined,
  map: readonly number[],
): OfferInlineMark[] | undefined {
  if (!marks?.length) return undefined
  const bits = bitsFromMarks(source, marks)
  const next = Array<number>(map[source.length] ?? 0).fill(0)
  for (let index = 0; index < source.length; index += 1) {
    const at = map[index] ?? 0
    const after = map[index + 1] ?? at
    if (after === at) continue
    for (let unit = at; unit < after; unit += 1) next[unit] = bits[index] ?? 0
  }
  return marksFromBits(next)
}

function rewriteTracked(text: string, clean: boolean, lines: boolean): { text: string; map: number[] } {
  let out = ''
  const map = [0]
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index]!
    const next = text[index + 1]
    if (lines && char === '\r' && next === '\n') {
      map.push(out.length)
      continue
    }
    if (clean && /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/.test(char)) {
      map.push(out.length)
      continue
    }
    out += lines && char === '\r' ? '\n' : char
    map.push(out.length)
  }
  return { text: out, map }
}

function nodeWithRewrite(node: OfferTextNode, clean: boolean, lines: boolean): OfferTextNode {
  const rewritten = rewriteTracked(node.text, clean, lines)
  return sameNode(node, rewritten.text, true, remapMarks(node.text, node.marks, rewritten.map))
}

function storedShape(nodes: OfferTextNode[]): OfferTextNode[] {
  const structured = nodes.length > 1 || nodes[0]?.kind === 'item' || !!nodes[0]?.marks?.length
  if (!structured) return nodes
  return nodes.map((node) => nodeWithRewrite(node, false, true))
}

/** A paragraph stays a paragraph. Item depth is kept up to the fixed maximum. */
export function clampProse(nodes: OfferTextNode[]): OfferTextNode[] {
  return storedShape(
    nodes.map((node) => {
      const cleaned = nodeWithRewrite(node, true, false)
      if (cleaned.kind !== 'item') return cleaned
      const depth = Math.max(0, Math.min(cleaned.depth ?? 0, OFFER_PROSE_MAX_DEPTH))
      return listItem(
        cleaned.text,
        depth,
        cleaned.marker,
        anchorOf(cleaned, true),
        layoutOf(cleaned),
        cleaned.marks,
      )
    }),
  )
}

function zeroFrom(values: number[], depth: number) {
  for (let i = depth; i < values.length; i++) values[i] = 0
}

export type OutlineColumn = { col: number; prefix: number; indent: string }

/** Hundredths of an em. These match the bullet and decimal columns in offer-document.css. */
const BULLET_STEP_EM = 135
const BULLET_TEXT_EM = 150
const DECIMAL_STEP_EM = 155
const DECIMAL_TEXT_EM = 230
const OUTLINE_GAP_EM = 40

function itemDepth(node: OfferTextNode): number {
  return Math.max(0, Math.min(node.depth ?? 0, OFFER_PROSE_MAX_DEPTH))
}

function isOutlineItem(node: OfferTextNode | undefined): boolean {
  return !!node && node.kind === 'item' && node.numbering === 'outline' && node.marker === 'decimal'
}

function findShallowerItem(nodes: readonly OfferTextNode[], index: number): number {
  const depth = itemDepth(nodes[index]!)
  for (let cursor = index - 1; cursor >= 0; cursor--) {
    const earlier = nodes[cursor]
    if (!earlier || earlier.kind !== 'item') continue
    if (itemDepth(earlier) < depth) return cursor
  }
  return -1
}

function hasMixedAncestor(nodes: readonly OfferTextNode[], index: number): boolean {
  let parent = findShallowerItem(nodes, index)
  while (parent >= 0) {
    if (!isOutlineItem(nodes[parent])) return true
    parent = findShallowerItem(nodes, parent)
  }
  return false
}

function formatEmHundredths(value: number): string {
  const whole = Math.trunc(value / 100)
  const frac = value % 100
  if (frac === 0) return String(whole)
  if (frac % 10 === 0) return `${whole}.${frac / 10}`
  return `${whole}.${String(frac).padStart(2, '0')}`
}

function outlineIndent(prefix: number, emHundredths: number, gaps: number): string {
  if (emHundredths === 0) return `calc(${prefix}ch + ${gaps} * 0.4em)`
  const em = formatEmHundredths(emHundredths)
  if (prefix === 0 && gaps === 0) return `calc(${em}em)`
  if (gaps === 0) return `calc(${prefix}ch + ${em}em)`
  if (prefix === 0) return `calc(${em}em + ${gaps} * 0.4em)`
  return `calc(${prefix}ch + ${em}em + ${gaps} * 0.4em)`
}

/**
 * Column width in `ch` for each outline level.
 * A pure outline child starts after the widest shallower label.
 * A child of a bullet or plain number starts where that parent's text starts.
 */
export function outlineMarkerColumns(
  labels: readonly string[],
  nodes: readonly OfferTextNode[],
): OutlineColumn[] {
  const widths = Array<number>(OFFER_PROSE_MAX_DEPTH + 1).fill(0)
  nodes.forEach((node, index) => {
    if (!isOutlineItem(node)) return
    const depth = itemDepth(node)
    widths[depth] = Math.max(widths[depth] ?? 0, [...(labels[index] ?? '')].length)
  })
  const leadOf = (index: number): { ch: number; em: number } => {
    const parent = findShallowerItem(nodes, index)
    if (parent < 0) return { ch: 0, em: 0 }
    const parentNode = nodes[parent]!
    if (isOutlineItem(parentNode)) {
      const base = leadOf(parent)
      const col = Math.max(widths[itemDepth(parentNode)] ?? 0, 1)
      return { ch: base.ch + col, em: base.em + OUTLINE_GAP_EM }
    }
    const depth = itemDepth(parentNode)
    if (parentNode.marker === 'decimal') return { ch: 0, em: depth * DECIMAL_STEP_EM + DECIMAL_TEXT_EM }
    return { ch: 0, em: depth * BULLET_STEP_EM + BULLET_TEXT_EM }
  }
  return nodes.map((node, index) => {
    if (!isOutlineItem(node)) return { col: 0, prefix: 0, indent: '' }
    const depth = itemDepth(node)
    const col = Math.max(widths[depth] ?? 0, 1)
    if (!hasMixedAncestor(nodes, index)) {
      let prefix = 0
      for (let level = 0; level < depth; level++) prefix += widths[level] ?? 0
      return { col, prefix, indent: outlineIndent(prefix, 0, depth) }
    }
    const lead = leadOf(index)
    return { col, prefix: lead.ch, indent: outlineIndent(lead.ch, lead.em, 0) }
  })
}

/** One label per node. Plain decimals stay `1.`; outline decimals are `3`, `3.1`, `3.1.1`. */
export function proseMarkerLabels(nodes: OfferTextNode[], sectionNumber = 0): string[] {
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
      if (node.glyph) return node.glyph
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
          (earlier.numbering === 'outline') === (node.numbering === 'outline') &&
          (earlier.section_bound === true) === (node.section_bound === true)
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
      const parts: string[] = []
      if (node.section_bound && sectionNumber > 0) parts.push(String(sectionNumber))
      for (let i = 0; i <= depth; i++) {
        let n = levels[i] ?? 0
        if (n <= 0) {
          if (i < depth) continue
          n = 1
        }
        parts.push(String(n))
      }
      return parts.join('.')
    }
    plain.splice(0, plain.length, ...levels)
    return `${levels[depth]}.`
  })
}

function readMarkerMm(value: unknown, min: number, max: number): number | null | undefined {
  if (value == null) return null
  if (typeof value !== 'number') return undefined
  const canon = canonMarkerMm(value, min, max)
  return canon == null ? undefined : canon
}

export function parseProseNodes(value: unknown): OfferTextNode[] | null {
  if (!Array.isArray(value) || value.length === 0 || value.length > OFFER_PROSE_MAX_NODES)
    return null
  const nodes: OfferTextNode[] = []
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
      section_bound?: unknown
      glyph?: unknown
      marker_x_mm?: unknown
      marker_y_mm?: unknown
      text_start_mm?: unknown
      marks?: unknown
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
    const sectionBound = record.section_bound
    if (
      (numbering != null && numbering !== 'outline') ||
      (sectionBound != null && sectionBound !== true && sectionBound !== false) ||
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
        listContinue === true ||
        sectionBound === true ||
        record.glyph != null ||
        record.marker_x_mm != null ||
        record.marker_y_mm != null ||
        record.text_start_mm != null
      )
        return null
      const paragraphMarks = parseInlineMarks(record.text, record.marks)
      if (paragraphMarks === null) return null
      nodes.push(paragraph(record.text, paragraphMarks))
      continue
    }
    const itemMarker = isOfferMarker(marker) ? marker : undefined
    if (
      itemMarker !== 'decimal' &&
      (numbering != null || listStart != null || listContinue === true || sectionBound === true)
    )
      return null
    if (sectionBound === true && numbering !== 'outline') return null
    if (listStart != null && listContinue === true) return null
    const depth = record.depth == null ? 0 : record.depth
    if (
      typeof depth !== 'number' ||
      !Number.isInteger(depth) ||
      depth < 0 ||
      depth > OFFER_PROSE_MAX_DEPTH
    )
      return null
    const glyph = record.glyph == null || record.glyph === '' ? '' : record.glyph
    if (typeof glyph !== 'string' || (glyph !== '' && plainGlyph(glyph) !== glyph)) return null
    if (itemMarker === 'decimal' && glyph) return null
    const markerX = readMarkerMm(record.marker_x_mm, MARKER_X_MM.min, MARKER_X_MM.max)
    const markerY = readMarkerMm(record.marker_y_mm, MARKER_Y_MM.min, MARKER_Y_MM.max)
    const textStart = readMarkerMm(record.text_start_mm, TEXT_START_MM.min, TEXT_START_MM.max)
    if (markerX === undefined || markerY === undefined || textStart === undefined) return null
    const itemMarks = parseInlineMarks(record.text, record.marks)
    if (itemMarks === null) return null
    nodes.push(
      listItem(
        record.text,
        depth,
        itemMarker,
        {
          numbering: numbering === 'outline' ? 'outline' : undefined,
          list_start: typeof listStart === 'number' ? listStart : undefined,
          list_continue: listContinue === true ? true : undefined,
          section_bound: sectionBound === true ? true : undefined,
        },
        {
          glyph: glyph || undefined,
          marker_x_mm: markerX ?? undefined,
          marker_y_mm: markerY ?? undefined,
          text_start_mm: textStart ?? undefined,
        },
        itemMarks,
      ),
    )
  }
  return nodes
}

/** Valid nodes win. Anything else stays one literal paragraph, including Markdown-like lines. */
export function proseNodes(body: string, nodes?: OfferTextNode[] | null): OfferTextNode[] {
  return parseProseNodes(nodes) ?? [paragraph(body ?? '')]
}

export function projectProse(nodes: OfferTextNode[], sectionNumber = 0): string {
  const labels = proseMarkerLabels(nodes, sectionNumber)
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
  const marked = (nodes: OfferTextNode[]) =>
    nodes.some((node) => node.kind === 'item' || !!node.marks?.length)
  const structured = after.length > 1 || marked(before) || marked(after)
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
export function persistProse(
  nodes: OfferTextNode[],
  sectionNumber = 0,
): { body: string; nodes?: OfferTextNode[] } {
  const clean = clampProse(nodes)
  const usable = clean.length > 0 ? clean : [paragraph('')]
  if (usable.length === 1 && usable[0]!.kind === 'paragraph' && !usable[0]!.marks?.length)
    return { body: usable[0]!.text }
  return { body: projectProse(usable, sectionNumber), nodes: usable }
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
  const bounds = new Set<boolean>()
  for (let index = from; index <= to; index++) {
    const node = nodes[index]
    if (!node || node.marker !== 'decimal') continue
    outlines.add(node.numbering === 'outline')
    continues.add(node.list_continue === true)
    starts.add(node.list_start && node.list_start > 0 ? node.list_start : null)
    bounds.add(node.section_bound === true)
  }
  const kind = kinds.size === 1 ? [...kinds][0]! : 'mixed'
  const outline: ProseListState['outline'] =
    outlines.size === 1 ? [...outlines][0]! : outlines.size > 1 ? 'mixed' : false
  const continued: ProseListState['continued'] =
    continues.size === 1 ? [...continues][0]! : continues.size > 1 ? 'mixed' : false
  const startValue: ProseListState['start'] =
    starts.size === 1 ? [...starts][0]! : starts.size > 1 ? 'mixed' : null
  const sectionBound: ProseListState['sectionBound'] =
    bounds.size === 1 ? [...bounds][0]! : bounds.size > 1 ? 'mixed' : false
  const level = proseLevelMoves(nodes, input)
  const placement = selectionLayout(nodes, from, to)
  const shared = {
    outline,
    continued,
    start: startValue,
    sectionBound,
    marks: proseMarkState(nodes, input),
    ...placement,
    indent: level.indent,
    outdent: level.outdent,
    indentLimit: level.indentLimit,
    outdentLimit: level.outdentLimit,
    markerLabel: null,
  }
  if (kind !== 'bullet') return { kind, bullet: null, ...shared }
  if (bullets.size !== 1) return { kind, bullet: 'mixed', ...shared }
  const only = [...bullets][0]!
  return { kind, bullet: only === 'depth' ? null : only, ...shared }
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
    next[from.index] = sameNode(
      node,
      text,
      true,
      marksFromBits(
        bitsFromMarks(node.text, node.marks).slice(0, cut).concat(bitsFromMarks(node.text, node.marks).slice(keep)),
      ) ?? null,
    )
    return { nodes: clampProse(next), caret: { index: from.index, offset: cut } }
  }
  const left = nodes[from.index]!
  const right = nodes[to.index]!
  const cut = floorEdge(graphemeEdges(left.text), from.offset)
  const keep = ceilEdge(graphemeEdges(right.text), to.offset)
  const text = left.text.slice(0, cut) + right.text.slice(keep)
  const joined =
    marksFromBits(
      bitsFromMarks(left.text, left.marks)
        .slice(0, cut)
        .concat(bitsFromMarks(right.text, right.marks).slice(keep)),
    ) ?? null
  if (textLength(text) > OFFER_PROSE_MAX_TEXT) {
    return refuse(
      nodes,
      { index: from.index, offset: cut },
      'Dieser Absatz ist zu lang. Nichts wurde gelöscht.',
    )
  }
  const next = clampProse([
    ...nodes.slice(0, from.index),
    sameNode(left, text, true, joined),
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
  if (node.kind === 'item') next[index] = paragraph(node.text, node.marks)
  else {
    const prev = next[index - 1]
    next[index] = listItem(
      node.text,
      prev?.kind === 'item' ? (prev.depth ?? 0) : 0,
      undefined,
      undefined,
      undefined,
      node.marks,
    )
  }
  return clampProse(next)
}

/** Depth allowed for an ordinary indent. A previous item is not required. */
function structuralDepth(_nodes: OfferTextNode[], _index: number, requested: number): number {
  return Math.max(0, Math.min(requested, OFFER_PROSE_MAX_DEPTH))
}

/** A real level change keeps outline style and continuation, and drops a level-specific start. */
function movedAnchor(node: OfferTextNode): ItemAnchor | undefined {
  const anchor: ItemAnchor = {}
  if (node.numbering === 'outline') anchor.numbering = 'outline'
  if (node.section_bound) anchor.section_bound = true
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
    next[index] = listItem(node.text, depth, marker, anchor, undefined, node.marks)
  } else {
    const current = node.depth ?? 0
    const nextDepth = structuralDepth(next, index, current + 1)
    if (nextDepth <= current) return next
    next[index] = listItem(
      node.text,
      nextDepth,
      node.marker,
      movedAnchor(node),
      layoutOf(node),
      node.marks,
    )
  }
  return clampProse(next)
}

export function outdentItem(nodes: OfferTextNode[], index: number): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node || node.kind !== 'item') return next
  const current = node.depth ?? 0
  if (current <= 0) {
    next[index] = paragraph(node.text, node.marks)
    return clampProse(next)
  }
  const stepped = current - 1
  const structural = structuralDepth(next, index, stepped)
  const nextDepth = node.list_continue && structural < stepped ? stepped : structural
  if (nextDepth === current) return next
  next[index] = listItem(node.text, nextDepth, node.marker, movedAnchor(node), layoutOf(node), node.marks)
  return clampProse(next)
}

function structureMoves(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  op: (nodes: OfferTextNode[], index: number) => OfferTextNode[],
): boolean {
  const edit = applyStructure(nodes, input, op)
  return !edit.error && JSON.stringify(edit.nodes) !== JSON.stringify(nodes)
}

function indentLimitAt(nodes: OfferTextNode[], index: number): LevelLimit | null {
  const node = nodes[index]
  if (!node) return 'boundary'
  if (node.kind !== 'item') return null
  if ((node.depth ?? 0) >= OFFER_PROSE_MAX_DEPTH) return 'max-depth'
  return null
}

function outdentLimitAt(nodes: OfferTextNode[], index: number): LevelLimit | null {
  const node = nodes[index]
  if (!node || node.kind !== 'item') return 'not-list'
  const current = node.depth ?? 0
  if (current <= 0) return null
  const stepped = current - 1
  const structural = structuralDepth(nodes, index, stepped)
  const nextDepth = node.list_continue && structural < stepped ? stepped : structural
  if (nextDepth === current) return 'boundary'
  return null
}

function aggregateLimit(reasons: Array<LevelLimit | null>): LevelLimit | null {
  if (reasons.some((reason) => reason == null)) return 'boundary'
  const limits = reasons.filter((reason): reason is LevelLimit => reason != null)
  if (limits.length === 0) return 'boundary'
  const unique = new Set(limits)
  if (unique.size === 1) return [...unique][0]!
  return 'mixed'
}

/** Same structural result as the indent and outdent commands, including a mixed selection. */
export function proseLevelMoves(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
): Pick<ProseListState, 'indent' | 'outdent' | 'indentLimit' | 'outdentLimit'> {
  const { start, end } = rangeEnds(proseRange(input))
  const last = Math.max(0, nodes.length - 1)
  const from = Math.max(0, Math.min(start.index, last))
  const to = Math.max(0, Math.min(end.index, last))
  const indent = structureMoves(nodes, input, indentItem)
  const outdent = structureMoves(nodes, input, outdentItem)
  const indentReasons: Array<LevelLimit | null> = []
  const outdentReasons: Array<LevelLimit | null> = []
  if (nodes.length === 0) {
    indentReasons.push('boundary')
    outdentReasons.push('not-list')
  } else {
    for (let index = from; index <= to; index++) {
      indentReasons.push(indentLimitAt(nodes, index))
      outdentReasons.push(outdentLimitAt(nodes, index))
    }
  }
  return {
    indent,
    outdent,
    indentLimit: indent ? null : aggregateLimit(indentReasons),
    outdentLimit: outdent ? null : aggregateLimit(outdentReasons),
  }
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
    next[index] = paragraph(node.text, node.marks)
    return clampProse(next)
  }
  const depth = node.kind === 'item' ? (node.depth ?? 0) : siblingDepth(next, index)
  if (kind === 'ordered') {
    const anchor: ItemAnchor = { numbering: 'outline' }
    if (node.kind === 'item' && node.marker === 'decimal') {
      if (node.list_start && node.list_start > 0) anchor.list_start = node.list_start
      else if (node.list_continue) anchor.list_continue = true
    }
    next[index] = listItem(node.text, depth, 'decimal', anchor, layoutOf(node, false), node.marks)
    return clampProse(next)
  }
  const marker =
    node.kind === 'item' && node.marker && node.marker !== 'decimal' ? node.marker : undefined
  next[index] = listItem(node.text, depth, marker, undefined, layoutOf(node), node.marks)
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
  next[index] = listItem(node.text, depth, marker, undefined, layoutOf(node, false), node.marks)
  return clampProse(next)
}

function replaceItemLayout(
  nodes: OfferTextNode[],
  index: number,
  layout: ItemLayout | undefined,
): OfferTextNode[] {
  const next = cloneNodes(nodes)
  const node = next[index]
  if (!node || node.kind !== 'item') return next
  next[index] = listItem(node.text, node.depth ?? 0, node.marker, anchorOf(node, true), layout, node.marks)
  return clampProse(next)
}

export function setItemGlyph(nodes: OfferTextNode[], index: number, glyph: string): OfferTextNode[] {
  const node = nodes[index]
  if (!node || node.kind !== 'item' || node.marker === 'decimal') return nodes
  const plain = plainGlyph(glyph)
  if (plain == null) return nodes
  const layout = layoutOf(node) ?? {}
  if (plain) layout.glyph = plain
  else delete layout.glyph
  return replaceItemLayout(nodes, index, layout)
}

export function setItemLayout(
  nodes: OfferTextNode[],
  index: number,
  axis: 'x' | 'y' | 'text',
  value: number,
): OfferTextNode[] {
  const node = nodes[index]
  if (!node || node.kind !== 'item') return nodes
  const bounds = axis === 'x' ? MARKER_X_MM : axis === 'y' ? MARKER_Y_MM : TEXT_START_MM
  const canon = canonMarkerMm(value, bounds.min, bounds.max)
  if (canon == null) return nodes
  const layout = layoutOf(node) ?? {}
  const key = axis === 'x' ? 'marker_x_mm' : axis === 'y' ? 'marker_y_mm' : 'text_start_mm'
  if (canon === 0) delete layout[key]
  else layout[key] = canon
  return replaceItemLayout(nodes, index, layout)
}

export function resetItemLayout(nodes: OfferTextNode[], index: number): OfferTextNode[] {
  const node = nodes[index]
  if (!node || node.kind !== 'item' || !layoutOf(node)) return nodes
  return replaceItemLayout(nodes, index, undefined)
}

function oneLayout<T>(values: Set<T>, count: number): T | 'mixed' | null {
  if (count === 0) return null
  if (values.size !== 1) return 'mixed'
  return [...values][0]!
}

function selectionLayout(nodes: OfferTextNode[], from: number, to: number) {
  const glyphs = new Set<string | null>()
  const xs = new Set<number | null>()
  const ys = new Set<number | null>()
  const starts = new Set<number | null>()
  const depths = new Set<number>()
  let items = 0
  for (let index = from; index <= to; index++) {
    const node = nodes[index]
    if (!node || node.kind !== 'item') continue
    items += 1
    depths.add(node.depth ?? 0)
    glyphs.add(node.glyph ?? null)
    xs.add(node.marker_x_mm ?? null)
    ys.add(node.marker_y_mm ?? null)
    starts.add(node.text_start_mm ?? null)
  }
  return {
    level: items === 0 ? null : depths.size === 1 ? [...depths][0]! : ('mixed' as const),
    glyph: oneLayout(glyphs, items),
    markerX: oneLayout(xs, items),
    markerY: oneLayout(ys, items),
    textStart: oneLayout(starts, items),
  }
}

/**
 * Restart, continue, or a positive start. Follow keeps the outline style and drops anchors.
 * Section and independent ownership keep each item's own start or continuation.
 */
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
  if (
    command === 'section' ||
    command === 'bound' ||
    (node.section_bound && command !== 'independent' && command !== 'unbound')
  )
    anchor.section_bound = true
  if (
    (command === 'section' ||
      command === 'independent' ||
      command === 'bound' ||
      command === 'unbound') &&
    node.kind === 'item' &&
    node.marker === 'decimal'
  ) {
    if (node.list_start && node.list_start > 0) anchor.list_start = node.list_start
    else if (node.list_continue) anchor.list_continue = true
  }
  if (command === 'continue') anchor.list_continue = true
  else if (command === 'restart') anchor.list_start = 1
  else if (command === 'start') {
    if (start == null || !Number.isInteger(start) || start < 1 || start > OFFER_LIST_START_MAX)
      return next
    anchor.list_start = start
  }
  next[index] = listItem(node.text, depth, 'decimal', anchor, layoutOf(node, false), node.marks)
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
  const split = bitsFromMarks(current.text, current.marks)
  const edit: ProseEdit = {
    nodes: clampProse([
      ...cleared.nodes.slice(0, caret.index),
      sameNode(current, left, true, marksFromBits(split.slice(0, caret.offset)) ?? null),
      sameNode(current, right, false, marksFromBits(split.slice(caret.offset)) ?? null),
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
  const style = typingStyleBits([node], { index: 0, offset: caret.offset })
  next[caret.index] = sameNode(
    node,
    text,
    true,
    marksAfterInsert(node.text, node.marks, caret.offset, 1, style) ?? null,
  )
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
  const merged = sameNode(
    prev,
    prev.text + current.text,
    true,
    marksFromBits(bitsFromMarks(prev.text, prev.marks).concat(bitsFromMarks(current.text, current.marks))) ??
      null,
  )
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
      sameNode(
        current,
        current.text + nextNode.text,
        true,
        marksFromBits(
          bitsFromMarks(current.text, current.marks).concat(bitsFromMarks(nextNode.text, nextNode.marks)),
        ) ?? null,
      ),
      ...nodes.slice(caret.index + 2),
    ]),
    caret,
  }
}

export function insertProseText(
  nodes: OfferTextNode[],
  input: Caret | ProseRange,
  raw: string,
  typing?: number,
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
  const existing = bitsFromMarks(current.text, current.marks)
  const headBits = existing.slice(0, caret.offset)
  const tailBits = existing.slice(caret.offset)
  const inherit = typing ?? typingStyleBits([current], { index: 0, offset: caret.offset })
  const created = parts.map((part, index) => {
    const text = `${index === 0 ? head : ''}${part}${index === parts.length - 1 ? tail : ''}`
    const bits = [
      ...(index === 0 ? headBits : []),
      ...Array<number>(part.length).fill(inherit),
      ...(index === parts.length - 1 ? tailBits : []),
    ]
    return sameNode(current, text, index === 0, marksFromBits(bits) ?? null)
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
  typing?: number,
): ProseEdit {
  const safeCaret = clampCaret(nodes, caret)
  if (texts.length !== nodes.length) return refuse(nodes, safeCaret, 'Dieser Absatz ist zu lang.')
  const next = nodes.map((node, index) => {
    const cleaned = cleanText(texts[index] ?? '')
    return sameNode(
      node,
      cleaned,
      true,
      cleaned === node.text
        ? node.marks
        : (reconcileMarkBits(node.text, cleaned, node.marks, typing) ?? null),
    )
  })
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
  push(current: ProseSnapshot): boolean
  undo(current: ProseSnapshot): ProseSnapshot | null
  redo(current: ProseSnapshot): ProseSnapshot | null
}

/** Editor memory stays with the block object. It is not part of the saved document. */
export type SectionEditorMemory = {
  history: ProseHistory
  caret: Caret | null
  record?: () => void
  requestUndo?: () => void
  requestRedo?: () => void
}

export function createProseHistory(limit = 100): ProseHistory {
  const undoStack: ProseSnapshot[] = []
  const redoStack: ProseSnapshot[] = []
  const copy = (snap: ProseSnapshot): ProseSnapshot => ({
    nodes: snap.nodes.map((node) => ({
      ...node,
      marks: node.marks?.map((mark) => ({ ...mark })),
    })),
    caret: { ...snap.caret },
  })
  return {
    push(current: ProseSnapshot) {
      const next = copy(current)
      const prev = undoStack[undoStack.length - 1]
      if (prev && JSON.stringify(prev) === JSON.stringify(next)) return false
      undoStack.push(next)
      if (undoStack.length > limit) undoStack.shift()
      redoStack.length = 0
      return true
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
