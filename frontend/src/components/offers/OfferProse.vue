<script setup lang="ts">
import { onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import {
  applyStructure,
  backspaceProse,
  bulletGlyph,
  clipboardPlain,
  createProseHistory,
  deleteForwardProse,
  enterProse,
  indentItem,
  insertProseText,
  insertSoftBreak,
  outdentItem,
  persistProse,
  proseNodes,
  proseNodesStorable,
  rangeEnds,
  reconcileProseTexts,
  toggleItem,
  type Caret,
  type ProseEdit,
  type ProseRange,
} from './offerProse'
import type { OfferTextNode } from './types'

const props = withDefaults(
  defineProps<{
    body: string
    nodes?: OfferTextNode[] | null
    editable?: boolean
    label?: string
  }>(),
  { editable: false, label: 'Text' },
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
const history = createProseHistory()
let serial = 1
let pending: Caret | null = null
let pinned: ProseRange | null = null
let suppressInput = false
let composing = false

watch(
  () => [props.body, props.nodes] as const,
  () => {
    const next = proseNodes(props.body ?? '', props.nodes)
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
function restore() {
  if (!pending || !root.value) return
  const caret = pending
  const el = root.value.querySelector<HTMLElement>(`[data-text][data-index="${caret.index}"]`)
  if (!el) return
  pending = null
  const range = document.createRange()
  const text = [...el.childNodes].find((node) => node.nodeType === Node.TEXT_NODE)
  if (text) range.setStart(text, Math.min(caret.offset, text.textContent?.length ?? 0))
  else range.setStart(el, 0)
  range.collapse(true)
  const live = window.getSelection()
  live?.removeAllRanges()
  live?.addRange(range)
  selection.value = { anchor: caret, focus: caret }
}
onMounted(() => {
  paint()
  document.addEventListener('selectionchange', capture)
})
onBeforeUnmount(() => document.removeEventListener('selectionchange', capture))
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
function capture() {
  if (composing) return
  const range = readRange()
  if (range) selection.value = range
}
function currentRange(): ProseRange {
  return readRange() ?? pinned ?? selection.value
}
function snapshot() {
  const caret = rangeEnds(selection.value).end
  return { nodes: local.value.map((node) => ({ ...node })), caret }
}
function commit(edit: ProseEdit) {
  const stored = persistProse(edit.nodes)
  const nodes = stored.nodes ?? edit.nodes
  if (nodes.length !== local.value.length) keys.value = nodes.map(() => serial++)
  local.value = nodes.map((node) => ({ ...node }))
  pending = edit.caret
  selection.value = { anchor: edit.caret, focus: edit.caret }
  notice.value = edit.error ?? ''
  pinned = null
  if (!edit.error) emit('update', stored)
}
function apply(edit: ProseEdit) {
  if (edit.error || JSON.stringify(edit.nodes) === JSON.stringify(local.value)) {
    notice.value = edit.error ?? ''
    pinned = null
    return
  }
  if (!proseNodesStorable(edit.nodes)) {
    notice.value = 'Dieser Absatz ist zu lang.'
    pinned = null
    return
  }
  history.push({ nodes: local.value.map((node) => ({ ...node })), caret: currentRange().focus })
  commit(edit)
}
function restoreSnap(snap: { nodes: OfferTextNode[]; caret: Caret }) {
  if (snap.nodes.length !== local.value.length) keys.value = snap.nodes.map(() => serial++)
  local.value = snap.nodes.map((node) => ({ ...node }))
  pending = snap.caret
  selection.value = { anchor: snap.caret, focus: snap.caret }
  notice.value = ''
  emit('update', persistProse(local.value))
}
function undo() {
  const snap = history.undo(snapshot())
  if (snap) restoreSnap(snap)
}
function redo() {
  const snap = history.redo(snapshot())
  if (snap) restoreSnap(snap)
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
  if (stored) selection.value = stored
}
function onKey(event: KeyboardEvent) {
  if (!props.editable || event.isComposing) return
  const key = event.key.toLowerCase()
  if ((event.metaKey || event.ctrlKey) && key === 'a') {
    event.preventDefault()
    selectAll()
    return
  }
  if ((event.metaKey || event.ctrlKey) && key === 'z') {
    event.preventDefault()
    suppressInput = true
    queueMicrotask(() => {
      suppressInput = false
    })
    if (event.shiftKey) redo()
    else undo()
    return
  }
  if ((event.metaKey || event.ctrlKey) && key === 'y') {
    event.preventDefault()
    redo()
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
  if (!props.editable || suppressInput || composing || event.isComposing) return
  const type = event.inputType
  if (type === 'historyUndo') {
    event.preventDefault()
    holdNativeInput()
    undo()
    return
  }
  if (type === 'historyRedo') {
    event.preventDefault()
    holdNativeInput()
    redo()
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
function prepareTool(event: MouseEvent) {
  pinned = readRange() ?? selection.value
  selection.value = pinned
  event.preventDefault()
}
function act(kind: 'toggle' | 'indent' | 'outdent') {
  const range = pinned ?? readRange() ?? selection.value
  pinned = null
  const op = kind === 'toggle' ? toggleItem : kind === 'indent' ? indentItem : outdentItem
  apply(applyStructure(local.value, range, op))
}
function nodeClass(node: OfferTextNode, index: number) {
  return [node.kind === 'item' ? 'item' : 'paragraph', index > 0 ? 'spaced' : '']
}
</script>
<template>
  <div class="offer-prose-field" @mouseup="capture" @keyup="capture">
    <div
      v-if="editable"
      class="offer-prose-tools"
      role="toolbar"
      aria-label="Aufzählung"
      @mousedown="prepareTool"
    >
      <button
        type="button"
        :aria-pressed="local[selection.focus.index]?.kind === 'item'"
        aria-label="Liste"
        @click="act('toggle')"
      >
        Liste
      </button>
      <button type="button" aria-label="Einrücken" @click="act('indent')">Einrücken</button>
      <button type="button" aria-label="Ausrücken" @click="act('outdent')">Ausrücken</button>
    </div>
    <div
      ref="root"
      class="offer-prose"
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
        :data-bullet="node.kind === 'item' ? bulletGlyph(node.depth ?? 0) : undefined"
        :style="node.kind === 'item' ? { '--depth': String(node.depth ?? 0) } : undefined"
      >
        <span data-text :data-index="index" />
      </div>
    </div>
    <p v-if="notice" class="offer-prose-notice" role="alert">{{ notice }}</p>
  </div>
</template>
