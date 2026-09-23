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
  List,
  ListPlus,
  LoaderCircle,
  Minus,
  MoreHorizontal,
  Plus,
  Printer,
  Save,
  Scaling,
  Settings2,
} from 'lucide-vue-next'
import type { Component } from 'vue'
import type { OfferBulletMarker, OfferFooterLayout } from './types'
import type { ProseListKind } from './offerProse'
import { OFFER_BULLET_GLYPH } from './offerProse'
import { explicitFooter, OFFER_FOOTER_LOGO } from './offerLayout'
import { useOfferProseSession, type ProseCommand } from './offerProseSession'
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
  footer: OfferFooterLayout | null
}>()
const emit = defineEmits<{
  save: []
  'update:zoomMode': [value: string]
  print: []
  action: [id: OfferToolbarActionId]
  footer: [value: OfferFooterLayout]
}>()

const root = ref<HTMLElement>()
const listButton = ref<HTMLButtonElement>()
const listPopover = ref<HTMLElement>()
const menuButton = ref<HTMLButtonElement>()
const menuPopover = ref<HTMLElement>()
const layoutPopover = ref<HTMLElement>()
const widthField = ref<HTMLInputElement>()
const offsetField = ref<HTMLInputElement>()
const startField = ref<HTMLInputElement>()
const widthDraft = ref<string | null>(null)
const offsetDraft = ref<string | null>(null)
const startDraft = ref<string | null>(null)
const listOpen = ref(false)
const menuOpen = ref(false)
const layoutOpen = ref(false)
const session = useOfferProseSession()
const zoomSteps = [50, 75, 100, 125, 150, 175, 200]
const listKinds: { id: ProseListKind; label: string }[] = [
  { id: 'none', label: 'Ohne' },
  { id: 'bullet', label: 'Aufzählung' },
  { id: 'ordered', label: 'Nummerierung' },
]
const bullets: { id: OfferBulletMarker; label: string }[] = [
  { id: 'disc', label: 'Punkt' },
  { id: 'circle', label: 'Kreis' },
  { id: 'square', label: 'Quadrat' },
  { id: 'dash', label: 'Strich' },
]
const icons: Record<OfferToolbarActionId, Component> = {
  link: Link,
  settings: Settings2,
  position: ListPlus,
  duplicate: Copy,
  finalize: FileCheck2,
  layout: Scaling,
  chrome: ChevronDown,
}

const listEnabled = computed(() => !!session?.active.value)
const listState = computed(() => {
  const revision = session?.revision.value ?? 0
  const state = session?.active.value?.state() ?? {
    kind: 'mixed' as const,
    bullet: null,
    outline: false as const,
    continued: false as const,
    start: null,
  }
  return { ...state, revision }
})
const shownWidth = computed(() => props.footer?.logo_width_mm ?? OFFER_FOOTER_LOGO.defaultWidthMm)
const shownOffset = computed(() => props.footer?.logo_offset_mm ?? OFFER_FOOTER_LOGO.legacyOffsetMm)

watch(listEnabled, (on) => {
  if (!on) listOpen.value = false
})
watch(
  () => props.actions.some((action) => action.id === 'layout'),
  (on) => {
    if (!on) layoutOpen.value = false
  },
)
watch([listOpen, menuOpen, layoutOpen], async () => {
  await nextTick()
  if (listOpen.value) place(listButton.value, listPopover.value, 'start')
  if (menuOpen.value) place(menuButton.value, menuPopover.value, 'end')
  if (layoutOpen.value) place(menuButton.value, layoutPopover.value, 'end')
})

function place(
  anchor: HTMLElement | undefined,
  panel: HTMLElement | undefined,
  align: 'start' | 'end',
) {
  if (!anchor || !panel) return
  const rect = anchor.getBoundingClientRect()
  const width = panel.offsetWidth
  const height = panel.offsetHeight
  let left = align === 'end' ? rect.right - width : rect.left
  left = Math.max(8, Math.min(left, window.innerWidth - width - 8))
  let top = rect.bottom + 6
  if (top + height > window.innerHeight - 8) top = Math.max(8, rect.top - height - 6)
  panel.style.top = `${top}px`
  panel.style.left = `${left}px`
}
function prime(event: MouseEvent) {
  if (event.button !== 0) return
  session?.active.value?.remember()
  const target = event.target
  if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) return
  event.preventDefault()
}
function run(command: ProseCommand) {
  if (!(command.type === 'numbering' && command.mode === 'start')) startDraft.value = null
  session?.active.value?.apply(command)
}
function toggleList(event: MouseEvent) {
  if (!listEnabled.value) return
  menuOpen.value = false
  layoutOpen.value = false
  listOpen.value = !listOpen.value
  if (listOpen.value && event.detail === 0)
    void nextTick(() => listPopover.value?.querySelector('button')?.focus())
}
function toggleMenu(event: MouseEvent) {
  listOpen.value = false
  layoutOpen.value = false
  menuOpen.value = !menuOpen.value
  if (menuOpen.value && event.detail === 0)
    void nextTick(() =>
      menuPopover.value?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus(),
    )
}
function choose(action: OfferToolbarAction) {
  if (action.disabled) return
  if (action.id === 'layout') {
    menuOpen.value = false
    layoutOpen.value = true
    void nextTick(() => widthField.value?.focus())
    return
  }
  menuOpen.value = false
  emit('action', action.id)
}
function stepZoom(direction: number) {
  const current = Math.round(props.zoom * 100)
  const next =
    direction > 0
      ? (zoomSteps.find((level) => level > current) ?? 200)
      : ([...zoomSteps].reverse().find((level) => level < current) ?? 50)
  emit('update:zoomMode', String(next))
}
function completeMm(raw: string): number | null {
  const text = raw.trim().replace(',', '.')
  if (!/^-?\d+(\.\d+)?$/.test(text)) return null
  const value = Number(text)
  return Number.isFinite(value) ? value : null
}
function commitLayout(width: number, offset: number) {
  const next = explicitFooter(props.footer, { logo_width_mm: width, logo_offset_mm: offset })
  if (!next) return
  emit('footer', next)
}
function onWidth(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  widthDraft.value = raw
  const width = completeMm(raw)
  if (width == null) return
  commitLayout(width, shownOffset.value)
}
function onOffset(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  offsetDraft.value = raw
  const offset = completeMm(raw)
  if (offset == null) return
  commitLayout(shownWidth.value, offset)
}
function finishWidth() {
  const raw = widthDraft.value
  widthDraft.value = null
  if (raw == null) return
  const width = completeMm(raw)
  if (width == null) return
  commitLayout(width, shownOffset.value)
}
function finishOffset() {
  const raw = offsetDraft.value
  offsetDraft.value = null
  if (raw == null) return
  const offset = completeMm(raw)
  if (offset == null) return
  commitLayout(shownWidth.value, offset)
}
function completeStart(raw: string): number | null {
  if (!/^\d+$/.test(raw.trim())) return null
  const value = Number(raw)
  if (!Number.isInteger(value) || value < 1 || value > 9999) return null
  return value
}
function applyStart(raw: string) {
  const value = completeStart(raw)
  if (value == null) return
  run({ type: 'numbering', mode: 'start', start: value })
}
function onListStart(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  startDraft.value = raw
  applyStart(raw)
}
function finishListStart() {
  const raw = startDraft.value
  startDraft.value = null
  if (raw != null) applyStart(raw)
}
function onRadioKey(event: KeyboardEvent, kind: 'list' | 'bullet') {
  const key = event.key
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(key)) return
  event.preventDefault()
  const items = kind === 'list' ? listKinds : bullets
  const current =
    kind === 'list'
      ? listKinds.findIndex((item) => item.id === listState.value.kind)
      : bullets.findIndex((item) => item.id === listState.value.bullet)
  let next = current < 0 ? 0 : current
  if (key === 'ArrowRight' || key === 'ArrowDown') next = (next + 1) % items.length
  else if (key === 'ArrowLeft' || key === 'ArrowUp') next = (next - 1 + items.length) % items.length
  else if (key === 'Home') next = 0
  else next = items.length - 1
  const item = items[next]
  if (!item) return
  if (kind === 'list') run({ type: 'list', kind: item.id as ProseListKind })
  else run({ type: 'marker', marker: item.id as OfferBulletMarker })
  void nextTick(() => {
    const group = kind === 'list' ? 'list-kind' : 'bullet'
    listPopover.value?.querySelectorAll<HTMLButtonElement>(`[data-group="${group}"]`)[next]?.focus()
  })
}
function onDocPointer(event: PointerEvent) {
  const target = event.target
  if (!(target instanceof Node) || root.value?.contains(target)) return
  finishListStart()
  finishWidth()
  finishOffset()
  listOpen.value = false
  menuOpen.value = false
  layoutOpen.value = false
}
function onDocKey(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (layoutOpen.value) {
    finishWidth()
    finishOffset()
    layoutOpen.value = false
    menuButton.value?.focus()
    event.preventDefault()
    return
  }
  if (listOpen.value) {
    finishListStart()
    listOpen.value = false
    listButton.value?.focus()
    event.preventDefault()
    return
  }
  if (menuOpen.value) {
    menuOpen.value = false
    menuButton.value?.focus()
    event.preventDefault()
  }
}
function iconFor(action: OfferToolbarAction) {
  if (action.id === 'chrome' && action.label.startsWith('Kopfzeilen ein')) return ChevronUp
  return icons[action.id]
}
function reposition() {
  if (listOpen.value) place(listButton.value, listPopover.value, 'start')
  if (menuOpen.value) place(menuButton.value, menuPopover.value, 'end')
  if (layoutOpen.value) place(menuButton.value, layoutPopover.value, 'end')
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
    <RouterLink
      class="tool-button icon-only"
      :to="backTo"
      :title="backLabel"
      :aria-label="backLabel"
    >
      <ArrowLeft :size="16" />
    </RouterLink>
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
    <div class="offer-actions">
      <div class="list-anchor">
        <button
          ref="listButton"
          type="button"
          class="tool-button list-button"
          aria-haspopup="dialog"
          :aria-expanded="listOpen"
          aria-label="Listen und Punkte"
          :disabled="!listEnabled"
          @mousedown="prime"
          @click="toggleList"
        >
          <List :size="16" /><span class="list-label">Listen & Punkte</span>
        </button>
        <div
          v-if="listOpen"
          ref="listPopover"
          class="chrome-popover list-popover"
          data-offer-chrome
          role="dialog"
          aria-label="Listen und Punkte"
          @mousedown="prime"
        >
          <p class="popover-label">Listentyp</p>
          <div class="segment" role="radiogroup" aria-label="Listentyp">
            <button
              v-for="item in listKinds"
              :key="item.id"
              type="button"
              role="radio"
              data-group="list-kind"
              :aria-checked="listState.kind === item.id"
              :aria-label="item.label"
              @keydown="onRadioKey($event, 'list')"
              @click="run({ type: 'list', kind: item.id })"
            >
              {{ item.label }}
            </button>
          </div>
          <p class="popover-label">Aufzählungszeichen</p>
          <div class="bullets" role="radiogroup" aria-label="Aufzählungszeichen">
            <button
              v-for="item in bullets"
              :key="item.id"
              type="button"
              role="radio"
              data-group="bullet"
              :aria-checked="listState.bullet === item.id"
              :aria-label="item.label"
              @keydown="onRadioKey($event, 'bullet')"
              @click="run({ type: 'marker', marker: item.id })"
            >
              {{ OFFER_BULLET_GLYPH[item.id] }}
            </button>
          </div>
          <div class="indent-row">
            <button type="button" aria-label="Einrücken" @click="run({ type: 'indent' })">
              Einrücken
            </button>
            <button type="button" aria-label="Ausrücken" @click="run({ type: 'outdent' })">
              Ausrücken
            </button>
          </div>
          <p class="popover-label">Nummerierung</p>
          <div class="indent-row">
            <button
              type="button"
              :aria-pressed="
                listState.outline === true && listState.start === 1 && listState.continued !== true
              "
              @click="run({ type: 'numbering', mode: 'restart' })"
            >
              Neu beginnen
            </button>
            <button
              type="button"
              :aria-pressed="listState.continued === true"
              @click="run({ type: 'numbering', mode: 'continue' })"
            >
              Fortsetzen
            </button>
          </div>
          <label class="start-row">
            Beginnen bei
            <input
              ref="startField"
              type="number"
              inputmode="numeric"
              min="1"
              max="9999"
              step="1"
              :value="startDraft ?? (typeof listState.start === 'number' ? listState.start : 1)"
              aria-label="Nummerierung beginnen bei"
              @input="onListStart"
              @blur="finishListStart"
              @keydown.enter.prevent="finishListStart"
            />
          </label>
          <p class="hint">3, 3.1, 3.1.1. Fortsetzen gilt auch nach einem Absatz.</p>
        </div>
      </div>
      <div class="zoom-controls" role="group" aria-label="Dokumentzoom">
        <button
          type="button"
          class="tool-button icon-only"
          title="Verkleinern"
          aria-label="Verkleinern"
          :disabled="zoom <= 0.5"
          @click="stepZoom(-1)"
        >
          <Minus :size="14" />
        </button>
        <select
          :value="zoomMode"
          aria-label="Zoom"
          @change="emit('update:zoomMode', ($event.target as HTMLSelectElement).value)"
        >
          <option value="width">Seitenbreite</option>
          <option value="page">Ganze Seite</option>
          <option v-for="level in zoomSteps" :key="level" :value="String(level)">
            {{ level }} %
          </option>
        </select>
        <button
          type="button"
          class="tool-button icon-only"
          title="Vergrößern"
          aria-label="Vergrößern"
          :disabled="zoom >= 2"
          @click="stepZoom(1)"
        >
          <Plus :size="14" />
        </button>
      </div>
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
          :aria-expanded="menuOpen || layoutOpen"
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
        <div
          v-if="layoutOpen"
          ref="layoutPopover"
          class="chrome-popover layout-popover"
          role="dialog"
          aria-label="Fußzeilenlogo"
          @keydown.esc.prevent="layoutOpen = false"
        >
          <p class="popover-label">Fußzeilenlogo</p>
          <label>
            Breite
            <span class="mm">
              <input
                ref="widthField"
                type="text"
                inputmode="decimal"
                :value="widthDraft ?? shownWidth"
                aria-label="Breite des Fußzeilenlogos in Millimetern"
                @input="onWidth"
                @blur="finishWidth"
                @keydown.enter.prevent="finishWidth"
              />
              mm
            </span>
          </label>
          <label>
            Versatz
            <span class="mm">
              <input
                ref="offsetField"
                type="text"
                inputmode="decimal"
                :value="offsetDraft ?? shownOffset"
                aria-label="Vertikaler Versatz des Fußzeilenlogos in Millimetern. Negativ nach oben, positiv nach unten."
                @input="onOffset"
                @blur="finishOffset"
                @keydown.enter.prevent="finishOffset"
              />
              mm
            </span>
          </label>
          <p class="hint">
            Negativ nach oben, positiv nach unten. Nummer, Linie und Seitenzahl bleiben.
          </p>
        </div>
      </div>
    </div>
  </header>
</template>
<style scoped>
.offer-tools {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  padding: 6px 12px;
  background: var(--h-surface, #fffefa);
  color: var(--h-text, #203c3d);
  border-bottom: 1px solid var(--h-line, #d8e2df);
  position: sticky;
  top: 0;
  z-index: 20;
}
.offer-number {
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 24vw;
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
.chrome-popover button:focus-visible,
.chrome-popover input:focus-visible {
  outline: 1px solid var(--h-mint, #0e6f6c);
  outline-offset: 2px;
}
.tool-button:disabled {
  opacity: 0.45;
  cursor: default;
}
.tool-button[aria-expanded='true'] {
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
  color: var(--h-muted, #596e70);
  white-space: nowrap;
}
.save-state time {
  font-variant-numeric: tabular-nums;
}
.save-button {
  width: 27px;
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
.offer-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-left: auto;
  min-width: 0;
}
.list-anchor,
.menu-anchor {
  position: relative;
}
.zoom-controls {
  display: flex;
  align-items: center;
}
.zoom-controls select {
  max-width: 120px;
  height: 28px;
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
  width: min(292px, calc(100vw - 16px));
  padding: 10px;
  color: var(--h-text, #203c3d);
  background: var(--h-surface, #fffefa);
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 10px;
  box-shadow: 0 8px 24px #10232714;
}
.popover-label {
  margin: 0 0 6px;
  font-size: 11px;
  color: var(--h-muted, #596e70);
}
.segment,
.indent-row,
.bullets {
  display: flex;
  gap: 4px;
}
.segment {
  margin-bottom: 10px;
}
.segment button,
.indent-row button,
.bullets button {
  flex: 1;
  min-height: 30px;
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.segment button[aria-checked='true'],
.bullets button[aria-checked='true'] {
  background: var(--h-fill, #e0f3f0);
  color: var(--h-mint, #0e6f6c);
}
.bullets {
  margin-bottom: 8px;
}
.bullets button {
  font-size: 16px;
}
.indent-row button {
  font-size: 12px;
}
.indent-row button[aria-pressed='true'] {
  background: var(--h-fill, #e0f3f0);
  color: var(--h-mint, #0e6f6c);
}
.start-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 8px;
  font-size: 12px;
}
.start-row input {
  width: 72px;
  min-height: 30px;
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  padding: 0 6px;
}
.menu-popover {
  width: min(340px, calc(100vw - 16px));
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
.layout-popover label {
  display: grid;
  gap: 4px;
  margin-bottom: 8px;
  font-size: 12px;
}
.mm {
  display: flex;
  align-items: center;
  gap: 6px;
}
.mm input {
  width: 100%;
  height: 30px;
  padding: 0 8px;
  color: inherit;
  background: var(--h-surface, #fffefa);
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  font: inherit;
}
.hint {
  margin: 0;
  color: var(--h-muted, #596e70);
  font-size: 11px;
  line-height: 1.35;
}
@media (max-width: 1024px) {
  .list-label {
    display: none;
  }
}
@media (max-width: 729px) {
  .offer-tools {
    gap: 4px;
    padding: 6px 8px;
  }
  .zoom-controls select {
    max-width: 92px;
  }
  .save-clock {
    display: none;
  }
  .offer-number {
    max-width: 28vw;
  }
}
</style>
