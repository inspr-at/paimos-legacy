<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import {
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  FileCheck2,
  Link,
  ListPlus,
  LoaderCircle,
  Minus,
  MoreHorizontal,
  PanelRight,
  Plus,
  Printer,
  Save,
  Scaling,
  Trash2,
} from 'lucide-vue-next'
import type { Component } from 'vue'
import { nextOfferZoomStep, OFFER_ZOOM_STEPS } from './offerZoom'
import type { OfferToolbarAction, OfferToolbarActionId } from './offerToolbarActions'

const props = defineProps<{
  backTo: string
  backLabel: string
  offerNo: string
  hostname: string
  editable: boolean
  busy: boolean
  loading: boolean
  dirty: boolean
  saveFailed: boolean
  state: string
  savedAt: string
  savedAtIso: string
  zoomMode: string
  zoom: number
  printDisabled: boolean
  showPrint: boolean
  actions: OfferToolbarAction[]
  showInspector: boolean
  inspectorOpen: boolean
}>()
const emit = defineEmits<{
  save: []
  'update:zoomMode': [value: string]
  print: []
  action: [id: OfferToolbarActionId]
  'toggle-inspector': []
}>()

const root = ref<HTMLElement>()
const menuButton = ref<HTMLButtonElement>()
const menuPopover = ref<HTMLElement>()
const menuOpen = ref(false)
const icons: Record<OfferToolbarActionId, Component> = {
  link: Link,
  settings: Scaling,
  position: ListPlus,
  duplicate: Copy,
  finalize: FileCheck2,
  layout: Scaling,
  chrome: ChevronDown,
  delete: Trash2,
}
const zoomPercent = computed(() => Math.round(props.zoom * 100))
const canZoomOut = computed(() => nextOfferZoomStep(zoomPercent.value, -1) != null)
const canZoomIn = computed(() => nextOfferZoomStep(zoomPercent.value, 1) != null)

watch(
  () => props.actions.length,
  (count) => {
    if (!count) menuOpen.value = false
  },
)
watch(menuOpen, async (open) => {
  if (!open) return
  await nextTick()
  place(menuButton.value, menuPopover.value)
})

function place(anchor: HTMLElement | undefined, panel: HTMLElement | undefined) {
  if (!anchor || !panel) return
  const rect = anchor.getBoundingClientRect()
  const width = panel.offsetWidth
  const height = panel.offsetHeight
  let left = rect.right - width
  left = Math.max(8, Math.min(left, window.innerWidth - width - 8))
  const below = rect.bottom + 6
  const limit = Math.max(8, window.innerHeight - height - 8)
  panel.style.top = `${Math.min(below, limit)}px`
  panel.style.left = `${left}px`
}
function toggleMenu(event: MouseEvent) {
  menuOpen.value = !menuOpen.value
  if (menuOpen.value && event.detail === 0)
    void nextTick(() =>
      menuPopover.value?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus(),
    )
}
function choose(action: OfferToolbarAction) {
  if (action.disabled) return
  menuOpen.value = false
  emit('action', action.id)
}
function stepZoom(direction: 1 | -1) {
  const next = nextOfferZoomStep(zoomPercent.value, direction)
  if (next == null) return
  emit('update:zoomMode', String(next))
}
function onDocPointer(event: PointerEvent) {
  const target = event.target
  if (!(target instanceof Node) || root.value?.contains(target)) return
  menuOpen.value = false
}
function onDocKey(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !menuOpen.value) return
  menuOpen.value = false
  menuButton.value?.focus()
  event.preventDefault()
}
function iconFor(action: OfferToolbarAction) {
  if (action.id === 'chrome' && action.label.startsWith('Kopfzeilen ein')) return ChevronUp
  return icons[action.id]
}
function reposition() {
  if (menuOpen.value) place(menuButton.value, menuPopover.value)
}
onMounted(() => {
  window.addEventListener('pointerdown', onDocPointer)
  window.addEventListener('keydown', onDocKey)
  window.addEventListener('resize', reposition)
})
onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onDocPointer)
  window.removeEventListener('keydown', onDocKey)
  window.removeEventListener('resize', reposition)
})
defineExpose({ root })
</script>
<template>
  <header ref="root" class="offer-tools" data-offer-chrome>
    <div class="offer-identity">
      <RouterLink
        class="tool-button icon-only"
        :to="backTo"
        :title="backLabel"
        :aria-label="backLabel"
      >
        <ArrowLeft :size="16" />
      </RouterLink>
      <div class="offer-id-copy">
        <strong class="offer-number" :title="hostname">{{ offerNo || 'Angebot' }}</strong>
        <div class="save-state" role="status" aria-live="polite">
          <button
            v-if="editable"
            class="save-button tool-button"
            type="button"
            :disabled="busy || loading"
            title="Jetzt speichern"
            aria-label="Jetzt speichern"
            @click="emit('save')"
          >
            <LoaderCircle v-if="busy" :size="15" class="save-spinner" />
            <template v-else>
              <Check v-if="!dirty && !saveFailed" :size="15" class="save-check" />
              <Save :size="15" :class="{ 'save-hover': !dirty && !saveFailed }" />
            </template>
          </button>
          <LoaderCircle v-else-if="busy" :size="15" class="save-spinner" />
          <span>{{ state }}</span>
          <time
            v-if="savedAt"
            class="save-clock"
            :datetime="savedAtIso"
            :title="`Zuletzt gespeichert: ${savedAt}`"
            >{{ savedAt }}</time
          >
        </div>
      </div>
    </div>
    <div class="zoom-slot">
      <div class="zoom-controls" role="group" aria-label="Dokumentzoom">
        <select
          :value="zoomMode"
          aria-label="Zoom"
          @change="emit('update:zoomMode', ($event.target as HTMLSelectElement).value)"
        >
          <option value="width">Seitenbreite</option>
          <option value="page">Ganze Seite</option>
          <option v-for="level in OFFER_ZOOM_STEPS" :key="level" :value="String(level)">
            {{ level }} %
          </option>
        </select>
        <button
          type="button"
          class="tool-button icon-only"
          title="Verkleinern"
          aria-label="Verkleinern"
          :disabled="!canZoomOut"
          @click="stepZoom(-1)"
        >
          <Minus :size="14" />
        </button>
        <button
          type="button"
          class="tool-button icon-only"
          title="Vergrößern"
          aria-label="Vergrößern"
          :disabled="!canZoomIn"
          @click="stepZoom(1)"
        >
          <Plus :size="14" />
        </button>
      </div>
    </div>
    <div class="offer-actions">
      <button
        v-if="showPrint"
        type="button"
        class="tool-button print-button"
        title="Druckansicht / PDF"
        aria-label="Druckansicht / PDF"
        :disabled="printDisabled"
        @click="emit('print')"
      >
        <Printer :size="16" /><span>PDF</span>
      </button>
      <div v-if="actions.length" class="menu-anchor">
        <button
          ref="menuButton"
          type="button"
          class="tool-button icon-only"
          aria-haspopup="menu"
          :aria-expanded="menuOpen"
          aria-label="Weitere Aktionen"
          title="Weitere Aktionen"
          @click="toggleMenu"
        >
          <MoreHorizontal :size="16" />
        </button>
        <div
          v-if="menuOpen"
          ref="menuPopover"
          class="chrome-popover menu-popover"
          role="menu"
          aria-label="Weitere Aktionen"
        >
          <button
            v-for="action in actions"
            :key="action.id"
            type="button"
            role="menuitem"
            :disabled="action.disabled"
            @click="choose(action)"
          >
            <component :is="iconFor(action)" :size="16" aria-hidden="true" />
            <span>
              <strong>{{ action.label }}</strong>
              <small>{{ action.detail }}</small>
            </span>
          </button>
        </div>
      </div>
      <button
        v-if="showInspector"
        type="button"
        class="tool-button icon-only"
        :aria-pressed="inspectorOpen"
        aria-controls="offer-inspector"
        :aria-label="inspectorOpen ? 'Inspektor ausblenden' : 'Inspektor einblenden'"
        :title="inspectorOpen ? 'Inspektor ausblenden' : 'Inspektor einblenden'"
        @click="emit('toggle-inspector')"
      >
        <PanelRight :size="16" />
      </button>
    </div>
  </header>
</template>
<style scoped>
.offer-tools {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: center;
  column-gap: 8px;
  min-height: 48px;
  padding: 4px 8px;
  background: var(--h-surface, #fffefa);
  color: var(--h-text, #203c3d);
  border-bottom: 1px solid var(--h-line, #d8e2df);
  position: sticky;
  top: 0;
  z-index: 20;
}
.offer-identity {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  justify-self: start;
}
.offer-id-copy {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.offer-number {
  font-size: 12px;
  line-height: 1.2;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.zoom-slot {
  justify-self: center;
}
.offer-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  min-width: 0;
  justify-self: end;
}
.tool-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 30px;
  min-height: 30px;
  padding: 0 8px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
  text-decoration: none;
  cursor: pointer;
  white-space: nowrap;
}
.tool-button:hover:not(:disabled) {
  background: var(--h-fill, #e0f3f0);
}
.tool-button:focus-visible,
select:focus-visible,
.chrome-popover button:focus-visible {
  outline: 1px solid var(--h-mint, #0e6f6c);
  outline-offset: 2px;
}
.tool-button:disabled {
  opacity: 0.45;
  cursor: default;
}
.tool-button[aria-expanded='true'],
.tool-button[aria-pressed='true'] {
  background: var(--h-fill, #e0f3f0);
  color: var(--h-mint, #0e6f6c);
}
.icon-only {
  width: 30px;
  padding: 0;
}
.save-state {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  font-size: 11px;
  line-height: 1.2;
  color: var(--h-muted, #596e70);
  white-space: nowrap;
}
.save-state time {
  font-variant-numeric: tabular-nums;
}
.save-button {
  width: 22px;
  height: 22px;
  min-height: 22px;
  padding: 0;
}
.save-check {
  color: #248358;
}
.save-hover {
  display: none;
}
.save-button:hover .save-check,
.save-button:focus-visible .save-check {
  display: none;
}
.save-button:hover .save-hover,
.save-button:focus-visible .save-hover {
  display: block;
}
.save-spinner {
  animation: save-spin 1s linear infinite;
}
@keyframes save-spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .save-spinner {
    animation: none;
  }
}
.menu-anchor {
  position: relative;
}
.zoom-controls {
  display: inline-flex;
  align-items: center;
  gap: 0;
}
.zoom-controls select {
  max-width: 132px;
  height: 28px;
  margin-right: 2px;
  padding: 0 4px;
  font: inherit;
  font-size: 11px;
  color: inherit;
  border: 0;
  border-radius: 4px;
  background: transparent;
}
.print-button {
  color: var(--h-mint, #0e6f6c);
  background: var(--h-fill, #e0f3f0);
}
.chrome-popover {
  position: fixed;
  z-index: 40;
  width: min(340px, calc(100vw - 16px));
  max-height: calc(100dvh - 16px);
  overflow: auto;
  color: var(--h-text, #203c3d);
  background: var(--h-surface, #fffefa);
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 10px;
  box-shadow: 0 8px 24px #10232714;
}
.menu-popover {
  display: grid;
  gap: 2px;
  padding: 6px;
}
.menu-popover button {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  width: 100%;
  padding: 8px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.menu-popover button:hover:not(:disabled) {
  background: var(--h-fill, #e0f3f0);
}
.menu-popover button:disabled {
  opacity: 0.45;
  cursor: default;
}
.menu-popover strong,
.menu-popover small {
  display: block;
}
.menu-popover strong {
  font-size: 13px;
  font-weight: 650;
}
.menu-popover small {
  margin-top: 2px;
  color: var(--h-muted, #596e70);
  font-size: 11px;
  line-height: 1.35;
  white-space: normal;
}
@media (max-width: 760px) {
  .offer-tools {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      'identity actions'
      'zoom zoom';
    row-gap: 2px;
    padding: 6px 8px;
  }
  .offer-identity {
    grid-area: identity;
  }
  .zoom-slot {
    grid-area: zoom;
  }
  .offer-actions {
    grid-area: actions;
  }
  .save-state {
    flex-direction: column;
    align-items: flex-start;
    gap: 0;
  }
  .save-clock {
    display: none;
  }
  .zoom-controls select {
    max-width: 108px;
  }
}
</style>
