<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import {
  applyStructure,
  backspaceProse,
  clipboardPlain,
  createProseHistory,
  deleteForwardProse,
  type SectionEditorMemory,
  enterProse,
  indentItem,
  insertProseText,
  insertSoftBreak,
  numberingCommandForIndex,
  outlineMarkerColumns,
  outdentItem,
  persistProse,
  proseListState,
  proseMarkerLabels,
  proseNodes,
  proseNodesStorable,
  rangeAfterStore,
  rangeEnds,
  reconcileProseTexts,
  resetItemLayout,
  setBulletMarker,
  setDecimalControl,
  setItemGlyph,
  setItemLayout,
  setListKind,
  type Caret,
  type ProseEdit,
  type ProseRange,
} from './offerProse'
import { focusCaretBox, lineBoundaryTarget, revealCaretInEditor } from './offerProseCaret'
import type { OfferTextNode } from './types'
import {
  isOfferChrome,
  takeOfferProseId,
  useOfferProseSession,
  type ProseCommand,
} from './offerProseSession'

const props = withDefaults(
  defineProps<{
    body: string
    nodes?: OfferTextNode[] | null
    editable?: boolean
    label?: string
    memory?: SectionEditorMemory | null
    sectionNumber?: number
  }>(),
  { editable: false, label: 'Text', memory: null, sectionNumber: 0 },
)
const emit = defineEmits<{ update: [value: { body: string; nodes?: OfferTextNode[] }] }>()
const root = ref<HTMLElement>()
const local = ref<OfferTextNode[]>([])
const keys = ref<number[]>([])
const notice = ref('')
const selection = ref<ProseRange>({
  anchor: { index: 0, offset: 0 },
  focus: { index: 0, offset: 0 },
})
const fallbackHistory = createProseHistory()
function historyStack() {
  return props.memory?.history ?? fallbackHistory
}
function rememberCaret() {
  if (!props.memory) return
  props.memory.caret = rangeEnds(selection.value).end
}
const session = useOfferProseSession()
const proseId = takeOfferProseId()
const markerLabels = computed(() => proseMarkerLabels(local.value, props.sectionNumber || 0))
const outlineColumns = computed(() => outlineMarkerColumns(markerLabels.value, local.value))
let serial = 1
let pendingRange: ProseRange | null = null
let held: ProseRange | null = null
let suppressInput = false
let composing = false
let pointerSelecting = false
// A wrapped line's end offset is also the next line's start, so the key that
// landed there keeps that line instead of stepping into the next one.
let visualEdge: {
  index: number
  offset: number
  start: number
  end: number
} | null = null

let hydrated = false
watch(
  () => [props.body, props.nodes] as const,
  () => {
    const next = proseNodes(props.body ?? '', props.nodes)
    if (!hydrated) {
      hydrated = true
      local.value = next
      keys.value = next.map(() => serial++)
      const caret = props.memory?.caret
      if (caret) selection.value = { anchor: caret, focus: caret }
      return
    }
    if (JSON.stringify(next) === JSON.stringify(local.value)) return
    local.value = next
    keys.value = next.map(() => serial++)
    selection.value = { anchor: { index: 0, offset: 0 }, focus: { index: 0, offset: 0 } }
  },
  { deep: true, immediate: true },
)

function paint() {
  if (composing) return
  root.value?.querySelectorAll<HTMLElement>('[data-text]').forEach((el) => {
    const want = local.value[Number(el.dataset.index)]?.text ?? ''
    const plain = el.childNodes.length === 1 && el.firstChild?.nodeType === Node.TEXT_NODE
    if (!plain || el.textContent !== want) el.textContent = want
  })
}
function pointAt(caret: Caret): { node: Node; offset: number } | null {
  const el = root.value?.querySelector<HTMLElement>(`[data-text][data-index="${caret.index}"]`)
  if (!el) return null
  const text = [...el.childNodes].find((node) => node.nodeType === Node.TEXT_NODE)
  if (!text) return { node: el, offset: 0 }
  return { node: text, offset: Math.min(caret.offset, text.textContent?.length ?? 0) }
}
function glyphTopAt(index: number, charIndex: number): number | null {
  const point = pointAt({ index, offset: charIndex })
  if (!point || point.node.nodeType !== Node.TEXT_NODE) return null
  const textNode = point.node as Text
  if (charIndex < 0 || charIndex >= textNode.data.length) return null
  const range = document.createRange()
  try {
    range.setStart(textNode, charIndex)
    range.setEnd(textNode, charIndex + 1)
    const rects = range.getClientRects()
    const rect = rects.length ? rects[rects.length - 1] : range.getBoundingClientRect()
    if (!rect || rect.height <= 0) return null
    return rect.top
  } catch {
    return null
  }
}
function pageOffset() {
  return { x: window.scrollX, y: window.scrollY }
}
function restorePageOffset(saved: { x: number; y: number }) {
  if (window.scrollX !== saved.x || window.scrollY !== saved.y) window.scrollTo(saved.x, saved.y)
}
function showCaretInEditor() {
  if (!root.value) return
  const live = window.getSelection()
  if (!live) return
  const rect = focusCaretBox(live)
  if (!rect) return
  revealCaretInEditor(root.value, rect)
}
function placeRange(range: ProseRange) {
  const anchor = pointAt(range.anchor)
  const focus = pointAt(range.focus)
  const live = window.getSelection()
  if (!anchor || !focus || !live) return
  live.removeAllRanges()
  if (typeof live.setBaseAndExtent === 'function') {
    try {
      live.setBaseAndExtent(anchor.node, anchor.offset, focus.node, focus.offset)
      return
    } catch {
      // Some engines reject a range that crosses replaced text nodes.
    }
  }
  const docRange = document.createRange()
  const { start, end } = rangeEnds(range)
  const startPoint = pointAt(start)
  const endPoint = pointAt(end)
  if (!startPoint || !endPoint) return
  docRange.setStart(startPoint.node, startPoint.offset)
  docRange.setEnd(endPoint.node, endPoint.offset)
  live.addRange(docRange)
}
function restore() {
  if (composing || !pendingRange || !root.value) return
  const range = pendingRange
  pendingRange = null
  placeRange(range)
  selection.value = range
  held = range
}
function remember() {
  const live = readRange()
  if (!live) return
  selection.value = live
  held = live
  if (
    visualEdge &&
    (visualEdge.index !== live.focus.index || visualEdge.offset !== live.focus.offset)
  )
    visualEdge = null
}
function toolbarRange(): ProseRange {
  return held ?? selection.value
}
function claim() {
  if (!session || !props.editable) return
  session.claim({
    id: proseId,
    state: () => proseListState(local.value, toolbarRange()),
    remember,
    apply: applyCommand,
  })
}
function editorOwnsFocus(): boolean {
  const active = document.activeElement
  if (!root.value || !active) return false
  return root.value.contains(active) || isOfferChrome(active)
}
function releaseTarget() {
  held = null
  if (session?.active.value?.id === proseId) session.release(proseId)
}
function dropStaleTarget() {
  if (editorOwnsFocus()) return
  releaseTarget()
}
function onSelectionChange() {
  if (composing || !props.editable || !root.value) return
  if (isOfferChrome(document.activeElement)) return
  if (!root.value.contains(document.activeElement)) {
    dropStaleTarget()
    return
  }
  remember()
  if (session?.active.value?.id === proseId) session.touch()
}
function onFocusIn(event: FocusEvent) {
  if (!props.editable || !root.value) return
  const target = event.target
  if (!(target instanceof Node)) return
  if (root.value.contains(target)) {
    claim()
    const fromChrome = isOfferChrome(event.relatedTarget)
    if (fromChrome && held && !pointerSelecting) pendingRange = held
    else remember()
    pointerSelecting = false
    return
  }
  pointerSelecting = false
  if (isOfferChrome(target)) return
  dropStaleTarget()
}
function onFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && (root.value?.contains(next) || isOfferChrome(next))) return
  queueMicrotask(() => {
    if (editorOwnsFocus()) return
    dropStaleTarget()
  })
}
function onDocumentPointerDown(event: PointerEvent) {
  if (!props.editable || !root.value || session?.active.value?.id !== proseId) return
  const target = event.target
  if (!(target instanceof Node)) return
  if (root.value.contains(target)) {
    pointerSelecting = true
    return
  }
  if (isOfferChrome(target)) return
  pointerSelecting = false
  window.getSelection()?.removeAllRanges()
  releaseTarget()
}
watch(
  () => props.editable,
  (on) => {
    if (!on) releaseTarget()
  },
)
onMounted(() => {
  paint()
  document.addEventListener('selectionchange', onSelectionChange)
  document.addEventListener('focusin', onFocusIn)
  document.addEventListener('focusout', onFocusOut)
  document.addEventListener('pointerdown', onDocumentPointerDown)
})
onBeforeUnmount(() => {
  rememberCaret()
  document.removeEventListener('selectionchange', onSelectionChange)
  document.removeEventListener('focusin', onFocusIn)
  document.removeEventListener('focusout', onFocusOut)
  document.removeEventListener('pointerdown', onDocumentPointerDown)
  session?.release(proseId)
})
onUpdated(() => {
  paint()
  restore()
})

function offsetWithin(el: HTMLElement, node: Node, offset: number): number {
  const range = document.createRange()
  range.selectNodeContents(el)
  try {
    range.setEnd(node, offset)
  } catch {
    return el.textContent?.length ?? 0
  }
  return range.toString().length
}
function caretFrom(node: Node | null, offset: number): Caret | null {
  if (!node || !root.value) return null
  const textEl = (node instanceof Element ? node : node.parentElement)?.closest<HTMLElement>(
    '[data-text]',
  )
  if (textEl && root.value.contains(textEl))
    return { index: Number(textEl.dataset.index), offset: offsetWithin(textEl, node, offset) }
  const block = (node instanceof Element ? node : node.parentElement)?.closest<HTMLElement>(
    '[data-node]',
  )
  if (!block || !root.value.contains(block)) return null
  const index = Number(block.dataset.node)
  return { index, offset: offset > 0 ? (local.value[index]?.text.length ?? 0) : 0 }
}
function readRange(): ProseRange | null {
  const live = window.getSelection()
  if (!live?.rangeCount || !root.value) return null
  const anchor = caretFrom(live.anchorNode, live.anchorOffset)
  const focus = caretFrom(live.focusNode, live.focusOffset)
  if (!anchor || !focus) return null
  return { anchor, focus }
}
function clampRange(nodes: OfferTextNode[], range: ProseRange): ProseRange {
  const clamp = (caret: Caret): Caret => {
    const index = Math.max(0, Math.min(caret.index, Math.max(0, nodes.length - 1)))
    const length = nodes[index]?.text.length ?? 0
    return { index, offset: Math.max(0, Math.min(caret.offset, length)) }
  }
  return { anchor: clamp(range.anchor), focus: clamp(range.focus) }
}
function currentRange(): ProseRange {
  return readRange() ?? held ?? selection.value
}
function snapshot() {
  const caret = rangeEnds(selection.value).end
  return { nodes: local.value.map((node) => ({ ...node })), caret }
}
function commit(edit: ProseEdit, keep?: ProseRange, restoreSelection = true) {
  visualEdge = null
  const stored = persistProse(edit.nodes, props.sectionNumber || 0)
  const nodes = stored.nodes ?? edit.nodes
  if (nodes.length !== local.value.length) keys.value = nodes.map(() => serial++)
  const previous = local.value
  local.value = nodes.map((node) => ({ ...node }))
  const range = keep
    ? clampRange(local.value, rangeAfterStore(previous, local.value, keep))
    : { anchor: edit.caret, focus: edit.caret }
  pendingRange = restoreSelection ? range : null
  selection.value = range
  held = range
  notice.value = edit.error ?? ''
  if (!edit.error) emit('update', stored)
  rememberCaret()
  if (session?.active.value?.id === proseId) session.touch()
}
function apply(edit: ProseEdit) {
  if (edit.error || JSON.stringify(edit.nodes) === JSON.stringify(local.value)) {
    notice.value = edit.error ?? ''
    return
  }
  if (!proseNodesStorable(edit.nodes)) {
    notice.value = 'Dieser Absatz ist zu lang.'
    return
  }
  if (
    historyStack().push({
      nodes: local.value.map((node) => ({ ...node })),
      caret: currentRange().focus,
    })
  )
    props.memory?.record?.()
  commit(edit)
}
function restoreSnap(snap: { nodes: OfferTextNode[]; caret: Caret }) {
  visualEdge = null
  if (snap.nodes.length !== local.value.length) keys.value = snap.nodes.map(() => serial++)
  local.value = snap.nodes.map((node) => ({ ...node }))
  const caret = snap.caret
  pendingRange = { anchor: caret, focus: caret }
  selection.value = { anchor: caret, focus: caret }
  held = selection.value
  notice.value = ''
  emit('update', persistProse(local.value, props.sectionNumber || 0))
  rememberCaret()
}
function undo() {
  const snap = historyStack().undo(snapshot())
  if (snap) restoreSnap(snap)
}
function redo() {
  const snap = historyStack().redo(snapshot())
  if (snap) restoreSnap(snap)
}
function dispatchUndo() {
  if (props.memory) {
    props.memory.requestUndo?.()
    return
  }
  undo()
}
function dispatchRedo() {
  if (props.memory) {
    props.memory.requestRedo?.()
    return
  }
  redo()
}
function selectAll() {
  const texts = [...(root.value?.querySelectorAll<HTMLElement>('[data-text]') ?? [])]
  const first = texts[0]
  const last = texts[texts.length - 1]
  if (!first || !last) return
  const range = document.createRange()
  range.setStart(first, 0)
  range.setEnd(last, last.childNodes.length)
  const live = window.getSelection()
  live?.removeAllRanges()
  live?.addRange(range)
  const stored = readRange()
  if (stored) {
    selection.value = stored
    held = stored
  }
}
function onEditorPointer() {
  pointerSelecting = true
  visualEdge = null
}
function nativeLineBoundary(event: KeyboardEvent, before: ProseRange): boolean {
  const live = window.getSelection()
  if (!live || typeof live.modify !== 'function') return false
  const alter = event.shiftKey ? 'extend' : 'move'
  const direction = event.key === 'Home' ? 'backward' : 'forward'
  try {
    live.modify(alter, direction, 'lineboundary')
  } catch {
    return false
  }
  const after = readRange()
  if (!after || after.focus.index !== before.focus.index) return false
  // Keep the browser caret, including its soft-wrap affinity. Rewriting it would drop that.
  selection.value = after
  held = after
  visualEdge = null
  return true
}
function onHomeEnd(event: KeyboardEvent) {
  if (event.isComposing || composing) return
  if (event.altKey || event.ctrlKey || event.metaKey) return
  event.preventDefault()
  if (!props.editable || !root.value) return
  const before = currentRange()
  const page = pageOffset()
  const moved = nativeLineBoundary(event, before)
  if (!moved) {
    const focus = before.focus
    const text = local.value[focus.index]?.text ?? ''
    const offset = Math.max(0, Math.min(focus.offset, text.length))
    const key = event.key === 'Home' ? 'Home' : 'End'
    const remembered =
      visualEdge && visualEdge.index === focus.index && visualEdge.offset === offset
        ? visualEdge
        : null
    const boundary = lineBoundaryTarget(key, offset, text, remembered, (charIndex) =>
      glyphTopAt(focus.index, charIndex),
    )
    const dest = { index: focus.index, offset: boundary.target }
    const range = { anchor: event.shiftKey ? before.anchor : dest, focus: dest }
    selection.value = range
    held = range
    placeRange(range)
    visualEdge = { index: focus.index, ...boundary.edge }
  }
  restorePageOffset(page)
  showCaretInEditor()
  requestAnimationFrame(() => restorePageOffset(page))
  if (session?.active.value?.id === proseId) session.touch()
}
function onKey(event: KeyboardEvent) {
  if (event.key === 'Home' || event.key === 'End') {
    onHomeEnd(event)
    return
  }
  visualEdge = null
  if (!props.editable || event.isComposing || composing) return
  const key = event.key.toLowerCase()
  if ((event.metaKey || event.ctrlKey) && key === 'a') {
    event.preventDefault()
    selectAll()
    return
  }
  if ((event.metaKey || event.ctrlKey) && (key === 'z' || key === 'y')) {
    event.preventDefault()
    holdNativeInput()
    if (key === 'y' || event.shiftKey) dispatchRedo()
    else dispatchUndo()
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    suppressInput = true
    queueMicrotask(() => {
      suppressInput = false
    })
    const range = currentRange()
    apply(event.shiftKey ? insertSoftBreak(local.value, range) : enterProse(local.value, range))
    return
  }
  if (event.key === 'Tab') {
    event.preventDefault()
    apply(applyStructure(local.value, currentRange(), event.shiftKey ? outdentItem : indentItem))
    return
  }
  if (event.key === 'Backspace' || event.key === 'Delete') {
    event.preventDefault()
    suppressInput = true
    queueMicrotask(() => {
      suppressInput = false
    })
    const range = currentRange()
    apply(
      event.key === 'Backspace'
        ? backspaceProse(local.value, range)
        : deleteForwardProse(local.value, range),
    )
  }
}
function holdNativeInput() {
  suppressInput = true
  queueMicrotask(() => {
    suppressInput = false
  })
}
function inputRange(event: InputEvent): ProseRange {
  const read = event.getTargetRanges
  if (typeof read === 'function') {
    try {
      const range = read.call(event)[0]
      if (range) {
        const anchor = caretFrom(range.startContainer, range.startOffset)
        const focus = caretFrom(range.endContainer, range.endOffset)
        if (anchor && focus) return { anchor, focus }
      }
    } catch {
      // Target ranges are optional and may be unavailable until the DOM changes.
    }
  }
  return currentRange()
}
function onBeforeInput(event: InputEvent) {
  visualEdge = null
  if (!props.editable || suppressInput || composing || event.isComposing) return
  const type = event.inputType
  if (type === 'historyUndo') {
    event.preventDefault()
    holdNativeInput()
    dispatchUndo()
    return
  }
  if (type === 'historyRedo') {
    event.preventDefault()
    holdNativeInput()
    dispatchRedo()
    return
  }
  if (type === 'insertText') {
    event.preventDefault()
    holdNativeInput()
    apply(insertProseText(local.value, currentRange(), event.data ?? ''))
    return
  }
  if (type === 'insertReplacementText' && typeof event.data === 'string') {
    event.preventDefault()
    holdNativeInput()
    apply(insertProseText(local.value, inputRange(event), event.data))
    return
  }
  if (type === 'insertLineBreak') {
    event.preventDefault()
    holdNativeInput()
    apply(insertSoftBreak(local.value, currentRange()))
    return
  }
  if (type === 'insertParagraph') {
    event.preventDefault()
    holdNativeInput()
    apply(enterProse(local.value, currentRange()))
    return
  }
  if (
    type === 'deleteContentBackward' ||
    type === 'deleteContentForward' ||
    type === 'deleteByCut'
  ) {
    event.preventDefault()
    holdNativeInput()
    const range = currentRange()
    apply(
      type === 'deleteContentForward'
        ? deleteForwardProse(local.value, range)
        : backspaceProse(local.value, range),
    )
    return
  }
  if (type === 'insertFromPaste' || type === 'insertFromDrop' || type.startsWith('format')) {
    event.preventDefault()
    holdNativeInput()
  }
}
function reconcileNative() {
  if (!props.editable || !root.value || composing) return
  const texts: string[] = []
  let markup = false
  for (let index = 0; index < local.value.length; index++) {
    const el = root.value.querySelector<HTMLElement>(`[data-text][data-index="${index}"]`)
    if (!el) return
    if ([...el.childNodes].some((node) => node.nodeType === Node.ELEMENT_NODE)) markup = true
    texts.push(el.textContent ?? '')
  }
  const edit = reconcileProseTexts(local.value, texts, currentRange().focus)
  if (edit.error) {
    notice.value = edit.error
    paint()
    return
  }
  const changed = edit.nodes.some((node, index) => node.text !== local.value[index]?.text)
  if (!changed) {
    if (markup) paint()
    return
  }
  apply(edit)
}
function onCompositionStart() {
  composing = true
}
function onCompositionEnd() {
  composing = false
  holdNativeInput()
  reconcileNative()
}
function onInput() {
  if (!props.editable || composing || suppressInput) return
  reconcileNative()
}
function onPaste(event: ClipboardEvent) {
  if (!props.editable) return
  event.preventDefault()
  holdNativeInput()
  const text = clipboardPlain(
    event.clipboardData?.getData('text/plain') ?? '',
    event.clipboardData?.getData('text/html') ?? '',
  )
  apply(insertProseText(local.value, currentRange(), text))
}
function itemStyle(node: OfferTextNode, index: number): Record<string, string> | undefined {
  if (node.kind !== 'item') return undefined
  const style: Record<string, string> = { '--depth': String(node.depth ?? 0) }
  if (node.marker_x_mm) style['--marker-x'] = `${node.marker_x_mm}mm`
  if (node.marker_y_mm) style['--marker-y'] = `${node.marker_y_mm}mm`
  if (node.text_start_mm) style['--text-start'] = `${node.text_start_mm}mm`
  if (node.numbering === 'outline' && node.marker === 'decimal') {
    const column = outlineColumns.value[index]
    const depth = node.depth ?? 0
    if (column) {
      style['--outline-col'] = `${column.col}ch`
      style['--outline-indent'] = `calc(${column.prefix}ch + ${depth} * 0.4em)`
    }
  }
  return style
}
function opFor(command: ProseCommand) {
  if (command.type === 'indent') return indentItem
  if (command.type === 'outdent') return outdentItem
  if (command.type === 'marker')
    return (nodes: OfferTextNode[], index: number) => setBulletMarker(nodes, index, command.marker)
  if (command.type === 'glyph')
    return (nodes: OfferTextNode[], index: number) => setItemGlyph(nodes, index, command.glyph)
  if (command.type === 'layout')
    return (nodes: OfferTextNode[], index: number) =>
      setItemLayout(nodes, index, command.axis, command.value)
  if (command.type === 'layout-reset')
    return (nodes: OfferTextNode[], index: number) => resetItemLayout(nodes, index)
  if (command.type === 'numbering') {
    let position = 0
    return (nodes: OfferTextNode[], index: number) => {
      const mode = numberingCommandForIndex(command.mode, position)
      position += 1
      return setDecimalControl(nodes, index, mode, command.start)
    }
  }
  return (nodes: OfferTextNode[], index: number) => setListKind(nodes, index, command.kind)
}
function applyCommand(command: ProseCommand) {
  if (!props.editable || !root.value) return
  const active = document.activeElement
  const inEditor = !!active && root.value.contains(active)
  const inChrome = isOfferChrome(active)
  if (!inEditor && !inChrome && !readRange()) {
    dropStaleTarget()
    return
  }
  if (session && session.active.value?.id !== proseId) return
  if (!inChrome) remember()
  const range = toolbarRange()
  if (!range) return
  const edit = applyStructure(local.value, range, opFor(command))
  if (edit.error || JSON.stringify(edit.nodes) === JSON.stringify(local.value)) {
    notice.value = edit.error ?? ''
    return
  }
  if (!proseNodesStorable(edit.nodes)) {
    notice.value = 'Dieser Absatz ist zu lang.'
    return
  }
  if (
    historyStack().push({
      nodes: local.value.map((node) => ({ ...node })),
      caret: rangeEnds(range).end,
    })
  )
    props.memory?.record?.()
  commit(edit, range, !inChrome)
}
defineExpose({ format: applyCommand })
function nodeClass(node: OfferTextNode, index: number) {
  return [node.kind === 'item' ? 'item' : 'paragraph', index > 0 ? 'spaced' : '']
}
</script>
<template>
  <div class="offer-prose-field">
    <div
      ref="root"
      class="offer-prose"
      @pointerdown="onEditorPointer"
      :data-prose="local.some((node) => node.kind === 'item') ? 'list' : 'plain'"
      :contenteditable="editable ? 'true' : 'false'"
      role="textbox"
      aria-multiline="true"
      :aria-label="label"
      :tabindex="editable ? 0 : undefined"
      @keydown="onKey"
      @beforeinput="onBeforeInput"
      @input="onInput"
      @compositionstart="onCompositionStart"
      @compositionend="onCompositionEnd"
      @paste="onPaste"
    >
      <div
        v-for="(node, index) in local"
        :key="keys[index]"
        :data-node="index"
        :class="nodeClass(node, index)"
        :data-bullet="node.kind === 'item' ? markerLabels[index] : undefined"
        :data-marker="node.marker || undefined"
        :data-numbering="node.numbering || undefined"
        :style="itemStyle(node, index)"
      >
        <span data-text :data-index="index" />
      </div>
    </div>
    <p v-if="notice" class="offer-prose-notice" role="alert">{{ notice }}</p>
  </div>
</template>
