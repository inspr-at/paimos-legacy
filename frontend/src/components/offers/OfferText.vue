<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  collectTextNodes,
  focusCaretBox,
  glyphTopAtLinear,
  lineBoundaryTarget,
  linearText,
  offsetInRoot,
  pointInTextNodes,
  revealCaretInEditor,
  type LineEdge,
} from './offerProseCaret'

const props = withDefaults(
  defineProps<{ modelValue: string; editable?: boolean; tag?: string; label?: string }>(),
  { tag: 'span', editable: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const element = ref<HTMLElement>()
let composing = false
let placing = false
let visualEdge: LineEdge | null = null
function startComposition() {
  composing = true
  visualEdge = null
}
function endComposition() {
  composing = false
}
function clearEdge() {
  visualEdge = null
}

function sync() {
  if (element.value && document.activeElement !== element.value)
    element.value.textContent = props.modelValue
}
onMounted(() => {
  sync()
  document.addEventListener('selectionchange', onSelectionChange)
})
onBeforeUnmount(() => document.removeEventListener('selectionchange', onSelectionChange))
watch(() => props.modelValue, sync)
function input(e: Event) {
  visualEdge = null
  emit('update:modelValue', (e.target as HTMLElement).innerText)
}
function paste(e: ClipboardEvent) {
  visualEdge = null
  if (!props.editable) return
  e.preventDefault()
  const selection = window.getSelection()
  if (!selection?.rangeCount) return
  const range = selection.getRangeAt(0)
  range.deleteContents()
  const node = document.createTextNode(e.clipboardData?.getData('text/plain') ?? '')
  range.insertNode(node)
  range.setStartAfter(node)
  range.collapse(true)
  selection.removeAllRanges()
  selection.addRange(range)
  if (element.value) emit('update:modelValue', element.value.innerText)
}
function caretOffset(root: HTMLElement): number | null {
  const live = window.getSelection()
  if (!live?.focusNode || !root.contains(live.focusNode)) return null
  return offsetInRoot(root, live.focusNode, live.focusOffset)
}
function placeCaret(root: HTMLElement, offset: number, shift: boolean) {
  const live = window.getSelection()
  const point = pointInTextNodes(collectTextNodes(root), offset)
  if (!live || !point) return
  const anchorNode = live.anchorNode
  const anchorOffset = live.anchorOffset
  placing = true
  try {
    if (shift && anchorNode && root.contains(anchorNode))
      live.setBaseAndExtent(anchorNode, anchorOffset, point.node, point.offset)
    else live.setBaseAndExtent(point.node, point.offset, point.node, point.offset)
  } finally {
    placing = false
  }
}
function onSelectionChange() {
  if (placing || !visualEdge || !element.value) return
  const live = window.getSelection()
  if (!live?.focusNode || !element.value.contains(live.focusNode)) {
    visualEdge = null
    return
  }
  if (offsetInRoot(element.value, live.focusNode, live.focusOffset) !== visualEdge.offset)
    visualEdge = null
}
function restorePage(saved: { x: number; y: number }) {
  if (window.scrollX !== saved.x || window.scrollY !== saved.y) window.scrollTo(saved.x, saved.y)
}
function onHomeEnd(event: KeyboardEvent) {
  if (event.key !== 'Home' && event.key !== 'End') {
    visualEdge = null
    return
  }
  if (event.isComposing || composing) return
  if (event.altKey || event.ctrlKey || event.metaKey) return
  event.preventDefault()
  const root = element.value
  if (!props.editable || !root) return
  const page = { x: window.scrollX, y: window.scrollY }
  const before = caretOffset(root)
  const live = window.getSelection()
  let native = false
  if (live && typeof live.modify === 'function' && before != null) {
    try {
      live.modify(
        event.shiftKey ? 'extend' : 'move',
        event.key === 'Home' ? 'backward' : 'forward',
        'lineboundary',
      )
      native = !!live.focusNode && root.contains(live.focusNode)
    } catch {
      native = false
    }
  }
  if (native) visualEdge = null
  if (!native && before != null) {
    const nodes = collectTextNodes(root)
    const boundary = lineBoundaryTarget(event.key, before, linearText(nodes), visualEdge, (index) =>
      glyphTopAtLinear(nodes, index),
    )
    placeCaret(root, boundary.target, event.shiftKey)
    visualEdge = boundary.edge
  }
  restorePage(page)
  if (live) {
    const box = focusCaretBox(live)
    if (box) revealCaretInEditor(root, box)
  }
  requestAnimationFrame(() => restorePage(page))
}
</script>
<template>
  <component
    :is="tag"
    ref="element"
    :contenteditable="editable ? 'plaintext-only' : undefined"
    :role="editable ? 'textbox' : undefined"
    :aria-label="label"
    :tabindex="editable ? 0 : undefined"
    @pointerdown="clearEdge"
    @keydown="onHomeEnd"
    @input="input"
    @paste="paste"
    @blur="sync"
    @compositionstart="startComposition"
    @compositionend="endComposition"
  />
</template>
