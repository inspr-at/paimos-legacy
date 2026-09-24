<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { focusCaretBox, revealCaretInEditor, visualLineOf } from './offerProseCaret'

const props = withDefaults(
  defineProps<{ modelValue: string; editable?: boolean; tag?: string; label?: string }>(),
  { tag: 'span', editable: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const element = ref<HTMLElement>()
let composing = false
function startComposition() {
  composing = true
}
function endComposition() {
  composing = false
}

function sync() {
  if (element.value && document.activeElement !== element.value)
    element.value.textContent = props.modelValue
}
onMounted(sync)
watch(() => props.modelValue, sync)
function input(e: Event) {
  emit('update:modelValue', (e.target as HTMLElement).innerText)
}
function paste(e: ClipboardEvent) {
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
function plain(root: HTMLElement): { node: Text | null; text: string } {
  const only = root.childNodes.length === 1 ? root.firstChild : null
  if (only instanceof Text) return { node: only, text: only.data }
  return { node: null, text: root.innerText.replace(/\r\n/g, '\n') }
}
function caretOffset(root: HTMLElement): number | null {
  const live = window.getSelection()
  if (!live?.focusNode || !root.contains(live.focusNode)) return null
  const text = plain(root)
  if (text.node && live.focusNode === text.node)
    return Math.max(0, Math.min(live.focusOffset, text.text.length))
  const range = document.createRange()
  try {
    range.setStart(root, 0)
    range.setEnd(live.focusNode, live.focusOffset)
  } catch {
    return null
  }
  return Math.max(0, Math.min(range.toString().length, text.text.length))
}
function glyphTop(textNode: Text, charIndex: number): number | null {
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
function placeCaret(root: HTMLElement, offset: number, shift: boolean) {
  const live = window.getSelection()
  if (!live) return
  const text = plain(root)
  const target = Math.max(0, Math.min(offset, text.text.length))
  if (!text.node) return
  if (shift && live.anchorNode && root.contains(live.anchorNode)) {
    live.setBaseAndExtent(live.anchorNode, live.anchorOffset, text.node, target)
    return
  }
  live.setBaseAndExtent(text.node, target, text.node, target)
}
function restorePage(saved: { x: number; y: number }) {
  if (window.scrollX !== saved.x || window.scrollY !== saved.y) window.scrollTo(saved.x, saved.y)
}
function onHomeEnd(event: KeyboardEvent) {
  if (event.key !== 'Home' && event.key !== 'End') return
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
  if (!native && before != null) {
    const text = plain(root)
    const line = visualLineOf(
      text.text,
      before,
      text.node ? (index) => glyphTop(text.node as Text, index) : undefined,
    )
    placeCaret(root, event.key === 'Home' ? line.start : line.end, event.shiftKey)
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
    @keydown="onHomeEnd"
    @input="input"
    @paste="paste"
    @blur="sync"
    @compositionstart="startComposition"
    @compositionend="endComposition"
  />
</template>
