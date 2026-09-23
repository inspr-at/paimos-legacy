<script setup lang="ts">
import { onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import {
  backspaceProse,
  bulletGlyph,
  clipboardPlain,
  createProseHistory,
  deleteForwardProse,
  editProseRange,
  enterProse,
  indentItem,
  insertProseText,
  insertSoftBreak,
  outdentItem,
  persistProse,
  proseNodes,
  rangeEnds,
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
  root.value?.querySelectorAll<HTMLElement>('[data-text]').forEach((el) => {
    const want = local.value[Number(el.dataset.index)]?.text ?? ''
    if (el.textContent !== want) el.textContent = want
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
  if (edit.nodes.length !== local.value.length) keys.value = edit.nodes.map(() => serial++)
  local.value = edit.nodes
  pending = edit.caret
  selection.value = { anchor: edit.caret, focus: edit.caret }
  notice.value = edit.error ?? ''
  pinned = null
  if (!edit.error) emit('update', persistProse(edit.nodes))
}
function apply(edit: ProseEdit) {
  if (edit.error || JSON.stringify(edit.nodes) === JSON.stringify(local.value)) {
    notice.value = edit.error ?? ''
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
    const range = currentRange()
    const op = event.shiftKey ? outdentItem : indentItem
    const nodes = editProseRange(local.value, range, op)
    const caret = rangeEnds(range).end
    apply({
      nodes,
      caret: {
        index: caret.index,
        offset: Math.min(caret.offset, nodes[caret.index]?.text.length ?? 0),
      },
    })
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
function onBeforeInput(event: InputEvent) {
  if (!props.editable || suppressInput || event.isComposing) return
  const type = event.inputType
  if (type === 'historyUndo') {
    event.preventDefault()
    undo()
    return
  }
  if (type === 'historyRedo') {
    event.preventDefault()
    redo()
    return
  }
  if (type === 'insertText') {
    event.preventDefault()
    apply(insertProseText(local.value, currentRange(), event.data ?? ''))
    return
  }
  if (type === 'insertLineBreak') {
    event.preventDefault()
    apply(insertSoftBreak(local.value, currentRange()))
    return
  }
  if (type === 'insertParagraph') {
    event.preventDefault()
    apply(enterProse(local.value, currentRange()))
    return
  }
  if (
    type === 'deleteContentBackward' ||
    type === 'deleteContentForward' ||
    type === 'deleteByCut'
  ) {
    event.preventDefault()
    const range = currentRange()
    apply(
      type === 'deleteContentForward'
        ? deleteForwardProse(local.value, range)
        : backspaceProse(local.value, range),
    )
    return
  }
  if (type === 'insertFromPaste' || type === 'insertFromDrop' || type.startsWith('format'))
    event.preventDefault()
}
function onPaste(event: ClipboardEvent) {
  if (!props.editable) return
  event.preventDefault()
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
  const nodes = editProseRange(local.value, range, op)
  const caret = rangeEnds(range).end
  apply({
    nodes,
    caret: {
      index: caret.index,
      offset: Math.min(caret.offset, nodes[caret.index]?.text.length ?? 0),
    },
  })
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
