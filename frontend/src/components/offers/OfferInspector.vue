<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  ArrowDown,
  ArrowLeftToLine,
  ArrowRightToLine,
  ArrowUp,
  ChevronDown,
  FileStack,
  List,
  ListEnd,
  ListOrdered,
  ListPlus,
  ListRestart,
  Minus,
  PanelRightClose,
  Plus,
  Redo2,
  RotateCcw,
  Trash2,
  Undo2,
} from 'lucide-vue-next'
import type { Component } from 'vue'
import type { OfferBulletMarker, OfferFooterLayout, OfferSelection } from './types'
import {
  canonMarkerMm,
  MARKER_X_MM,
  MARKER_Y_MM,
  OFFER_BULLET_GLYPH,
  plainGlyph,
  TEXT_START_MM,
  type LevelLimit,
  type ProseListKind,
} from './offerProse'
import { explicitFooter, OFFER_FOOTER_LOGO } from './offerLayout'
import { useOfferProseSession, type ProseCommand } from './offerProseSession'

const props = defineProps<{
  open: boolean
  footer: OfferFooterLayout | null
  canUndo?: boolean
  canRedo?: boolean
  selection?: OfferSelection | null
  blockCount: number
}>()
const emit = defineEmits<{
  footer: [value: OfferFooterLayout]
  undo: []
  redo: []
  hide: []
  action: [id: 'settings']
  'insert-section': []
  'insert-position': []
  'section-add': []
  'section-up': []
  'section-down': []
  'section-delete': []
  'position-up': []
  'position-down': []
  'position-delete': []
}>()

type InspectorTab = 'text' | 'section' | 'document'
const tab = ref<InspectorTab>('document')
const tabs: { id: InspectorTab; label: string }[] = [
  { id: 'text', label: 'Text' },
  { id: 'section', label: 'Abschnitt' },
  { id: 'document', label: 'Dokument' },
]
const widthDraft = ref<string | null>(null)
const offsetDraft = ref<string | null>(null)
const startDraft = ref<string | null>(null)
const glyphDraft = ref<string | null>(null)
const markerDraft = ref<{ axis: 'x' | 'y' | 'text'; raw: string } | null>(null)
const placeOpen = ref(true)
const insertOpen = ref(false)
const insertButton = ref<HTMLButtonElement>()
const insertMenu = ref<HTMLElement>()
const session = useOfferProseSession()
const maxBlocks = 20
const listKinds: { id: ProseListKind; label: string; name: string; icon: Component }[] = [
  { id: 'none', label: 'Ohne', name: 'Ohne', icon: Minus },
  { id: 'bullet', label: 'Liste', name: 'Aufzählung', icon: List },
  { id: 'ordered', label: 'Nummeriert', name: 'Nummerierung', icon: ListOrdered },
]
const bullets: { id: OfferBulletMarker; label: string }[] = [
  { id: 'disc', label: 'Punkt' },
  { id: 'circle', label: 'Kreis' },
  { id: 'square', label: 'Quadrat' },
  { id: 'dash', label: 'Strich' },
]

const listState = computed(() => {
  const revision = session?.revision.value ?? 0
  const state = session?.active.value?.state() ?? {
    kind: 'mixed' as const,
    bullet: null,
    outline: false as const,
    continued: false as const,
    start: null,
    sectionBound: false as const,
    indent: false,
    outdent: false,
    indentLimit: null,
    outdentLimit: null,
    level: null,
    glyph: null,
    markerX: null,
    markerY: null,
    textStart: null,
  }
  return { ...state, revision }
})
const canIndent = computed(() => listState.value.indent === true)
const canOutdent = computed(() => listState.value.outdent === true)
const sectionSelection = computed(() =>
  props.selection?.kind === 'heading' || props.selection?.kind === 'text' ? props.selection : null,
)
const contextTitle = computed(() => {
  const selection = props.selection
  if (!selection || selection.kind === 'none') return 'Dokument'
  if (selection.kind === 'heading' || selection.kind === 'text')
    return `Abschnitt ${selection.index + 1}`
  if (selection.kind === 'position') return `Position ${selection.index + 1}`
  return 'Fußzeilenlogo dieses Angebots'
})
const contextDetail = computed(() => {
  const selection = props.selection
  if (selection?.kind === 'heading') return selection.heading.trim() || 'ohne Überschrift'
  if (selection?.kind === 'text') {
    const heading = selection.heading.trim() || 'ohne Überschrift'
    return `Textauswahl · ${heading}`
  }
  if (selection?.kind === 'position') return 'Leistungsposition'
  if (selection?.kind === 'footer') return 'Fußzeilenlogo'
  return ''
})
const shownWidth = computed(() => props.footer?.logo_width_mm ?? OFFER_FOOTER_LOGO.defaultWidthMm)
const shownOffset = computed(() => props.footer?.logo_offset_mm ?? OFFER_FOOTER_LOGO.legacyOffsetMm)
const sectionFull = computed(() => (sectionSelection.value?.count ?? props.blockCount) >= maxBlocks)
const showNumbering = computed(
  () => listState.value.kind === 'ordered' || listState.value.kind === 'mixed',
)
const showStart = computed(() => showNumbering.value && listState.value.continued !== true)
const bezugValue = computed(() => (listState.value.sectionBound === false ? 'independent' : 'section'))
const numberingExample = computed(() => {
  if (!showNumbering.value) return ''
  const start = typeof listState.value.start === 'number' ? listState.value.start : 1
  if (listState.value.sectionBound === false) return String(start)
  const section = (sectionSelection.value?.index ?? 0) + 1
  const depth = typeof listState.value.level === 'number' ? listState.value.level : 0
  const parts = [String(section)]
  for (let i = 0; i < depth; i++) parts.push('1')
  parts.push(String(start))
  return parts.join('.')
})

function limitTip(limit: LevelLimit | null | undefined, direction: 'deeper' | 'higher'): string {
  switch (limit) {
    case 'max-depth':
      return 'Maximale Ebene ist erreicht.'
    case 'no-previous':
      return 'Kein vorheriger Listeneintrag.'
    case 'boundary':
      return direction === 'deeper'
        ? 'Nicht tiefer als der vorherige Eintrag.'
        : 'Diese Ebene kann nicht höher.'
    case 'not-list':
      return 'Nur Listeneinträge können eine Ebene höher.'
    case 'mixed':
      return direction === 'deeper'
        ? 'Die Auswahl kann nicht tiefer.'
        : 'Die Auswahl kann nicht höher.'
    default:
      return direction === 'deeper'
        ? 'Einrücken ist für diese Auswahl nicht möglich.'
        : 'Ausrücken ist für diese Auswahl nicht möglich.'
  }
}
const deeperTip = computed(() =>
  canIndent.value ? 'Eine Listenebene tiefer' : limitTip(listState.value.indentLimit, 'deeper'),
)
const higherTip = computed(() =>
  canOutdent.value ? 'Eine Listenebene höher' : limitTip(listState.value.outdentLimit, 'higher'),
)
const indentName = computed(() =>
  canIndent.value ? 'Eine Listenebene tiefer' : `Einrücken. ${deeperTip.value}`,
)
const outdentName = computed(() =>
  canOutdent.value ? 'Eine Listenebene höher' : `Ausrücken. ${higherTip.value}`,
)
const levelLabel = computed(() => {
  const level = listState.value.level
  if (level == null) return ''
  if (level === 'mixed') return 'Gemischte Ebenen'
  return `Listenebene ${level + 1} · ${level}× eingerückt`
})
const levelShort = computed(() => {
  const level = listState.value.level
  if (level == null) return ''
  if (level === 'mixed') return 'Gemischte Ebenen'
  return `Ebene ${level + 1}`
})
const showMarkerPlace = computed(() => listState.value.kind !== 'none' && listState.value.kind !== 'mixed')

watch(
  () => props.selection?.kind,
  (kind, previous) => {
    if (kind === 'text') tab.value = 'text'
    else if (kind === 'heading' || kind === 'position') tab.value = 'section'
    else tab.value = 'document'
    if (previous === 'footer' && kind !== 'footer') {
      finishWidth()
      finishOffset()
    }
    if (previous === 'text' && kind !== 'text') finishListStart()
  },
  { immediate: true },
)

function prime(event: MouseEvent) {
  if (event.button !== 0) return
  session?.active.value?.remember()
  const target = event.target
  if (!(target instanceof Element)) return
  if (target.closest('input, textarea, select, label')) return
  event.preventDefault()
}
function run(command: ProseCommand) {
  if (command.type === 'indent' && !canIndent.value) return
  if (command.type === 'outdent' && !canOutdent.value) return
  if (!(command.type === 'numbering' && command.mode === 'start')) startDraft.value = null
  session?.active.value?.apply(command)
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
  startDraft.value = null
}
function finishMarkerDrafts() {
  glyphDraft.value = null
  markerDraft.value = null
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
  const group = kind === 'list' ? 'list-kind' : 'bullet'
  const host = event.currentTarget instanceof HTMLElement ? event.currentTarget.parentElement : null
  host?.querySelectorAll<HTMLButtonElement>(`[data-group="${group}"]`)[next]?.focus()
}
function onTabKey(event: KeyboardEvent) {
  const key = event.key
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(key)) return
  event.preventDefault()
  const current = tabs.findIndex((item) => item.id === tab.value)
  let next = current < 0 ? 0 : current
  if (key === 'ArrowRight') next = (next + 1) % tabs.length
  else if (key === 'ArrowLeft') next = (next - 1 + tabs.length) % tabs.length
  else if (key === 'Home') next = 0
  else next = tabs.length - 1
  const item = tabs[next]
  if (!item) return
  tab.value = item.id
  const host = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  host?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus()
}
function onBezug(event: Event) {
  const value = (event.target as HTMLSelectElement).value
  run({ type: 'numbering', mode: value === 'independent' ? 'independent' : 'section' })
}
function finishNumericDrafts() {
  finishListStart()
  finishMarkerDrafts()
  finishWidth()
  finishOffset()
}
function shownGlyph() {
  if (glyphDraft.value != null) return glyphDraft.value
  return typeof listState.value.glyph === 'string' ? listState.value.glyph : ''
}
function shownMarker(axis: 'x' | 'y' | 'text') {
  if (markerDraft.value?.axis === axis) return markerDraft.value.raw
  const value =
    axis === 'x'
      ? listState.value.markerX
      : axis === 'y'
        ? listState.value.markerY
        : listState.value.textStart
  return typeof value === 'number' ? String(value) : '0'
}
function onGlyph(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  glyphDraft.value = raw
  const plain = plainGlyph(raw)
  if (plain == null) return
  run({ type: 'glyph', glyph: plain })
}
function applyMarker(axis: 'x' | 'y' | 'text', raw: string) {
  const parsed = completeMm(raw)
  if (parsed == null) return
  const bounds = axis === 'x' ? MARKER_X_MM : axis === 'y' ? MARKER_Y_MM : TEXT_START_MM
  const value = canonMarkerMm(parsed, bounds.min, bounds.max)
  if (value == null) return
  run({ type: 'layout', axis, value })
}
function onMarker(axis: 'x' | 'y' | 'text', event: Event) {
  const raw = (event.target as HTMLInputElement).value
  markerDraft.value = { axis, raw }
  applyMarker(axis, raw)
}
function nudgeMarker(axis: 'x' | 'y' | 'text', delta: number) {
  const current =
    axis === 'x'
      ? listState.value.markerX
      : axis === 'y'
        ? listState.value.markerY
        : listState.value.textStart
  const base = typeof current === 'number' ? current : 0
  markerDraft.value = null
  applyMarker(axis, String(base + delta))
}
function undoEdit() {
  finishNumericDrafts()
  emit('undo')
}
function redoEdit() {
  finishNumericDrafts()
  emit('redo')
}
function onInspectorKey(event: KeyboardEvent) {
  const key = event.key.toLowerCase()
  if (key === 'escape' && insertOpen.value) {
    insertOpen.value = false
    insertButton.value?.focus()
    event.preventDefault()
    return
  }
  if (!(event.metaKey || event.ctrlKey) || event.altKey || (key !== 'z' && key !== 'y')) return
  const target = event.target
  if (!(target instanceof Element) || !target.closest('input, textarea')) return
  event.preventDefault()
  if (key === 'y' || event.shiftKey) redoEdit()
  else undoEdit()
}
function openTemplates() {
  finishWidth()
  finishOffset()
  emit('action', 'settings')
}
function placeInsert() {
  const anchor = insertButton.value
  const panel = insertMenu.value
  if (!anchor || !panel) return
  const rect = anchor.getBoundingClientRect()
  const width = panel.offsetWidth
  const height = panel.offsetHeight
  const left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 8))
  const below = rect.bottom + 4
  const above = rect.top - height - 4
  const top = below + height <= window.innerHeight - 8 ? below : Math.max(8, above)
  panel.style.top = `${top}px`
  panel.style.left = `${left}px`
}
async function toggleInsert(event: MouseEvent) {
  insertOpen.value = !insertOpen.value
  if (!insertOpen.value) return
  await nextTick()
  placeInsert()
  if (event.detail === 0)
    insertMenu.value?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus()
}
function chooseInsert(kind: 'section' | 'position') {
  insertOpen.value = false
  if (kind === 'section') emit('insert-section')
  else emit('insert-position')
}
function onDocPointer(event: PointerEvent) {
  const target = event.target
  if (target instanceof Node && (event.currentTarget as Node | null) === target) return
  const root = document.getElementById('offer-inspector')
  if (target instanceof Node && root?.contains(target)) return
  insertOpen.value = false
  finishListStart()
  finishMarkerDrafts()
  finishWidth()
  finishOffset()
}
function reposition() {
  if (insertOpen.value) placeInsert()
}
onMounted(() => {
  window.addEventListener('pointerdown', onDocPointer)
  window.addEventListener('resize', reposition)
})
onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onDocPointer)
  window.removeEventListener('resize', reposition)
})
</script>
<template>
  <aside
    id="offer-inspector"
    class="offer-inspector"
    data-offer-chrome
    role="complementary"
    aria-label="Inspektor"
    :hidden="!open"
    @mousedown="prime"
    @keydown="onInspectorKey"
  >
    <header class="format-row">
      <p class="format-title">Format</p>
      <div class="format-actions" role="group" aria-label="Änderungen">
        <button type="button" :disabled="!canUndo" aria-label="Rückgängig" title="Rückgängig" @click="undoEdit">
          <Undo2 :size="14" aria-hidden="true" />
        </button>
        <button type="button" :disabled="!canRedo" aria-label="Wiederholen" title="Wiederholen" @click="redoEdit">
          <Redo2 :size="14" aria-hidden="true" />
        </button>
        <button type="button" aria-label="Inspektor ausblenden" title="Inspektor ausblenden" @click="emit('hide')">
          <PanelRightClose :size="14" aria-hidden="true" />
        </button>
      </div>
    </header>
    <div class="tabs" role="tablist" aria-label="Format" @keydown="onTabKey">
      <button
        v-for="item in tabs"
        :key="item.id"
        type="button"
        role="tab"
        :aria-selected="tab === item.id"
        :tabindex="tab === item.id ? 0 : -1"
        @click="tab = item.id"
      >
        {{ item.label }}
      </button>
    </div>
    <h2 :id="selection?.kind === 'footer' ? 'this-offer-heading' : undefined">{{ contextTitle }}</h2>
    <p v-if="contextDetail" class="context-detail">{{ contextDetail }}</p>

    <div v-show="tab === 'text'" class="panel" role="tabpanel" aria-label="Text">
      <div class="zeichen" aria-label="Zeichen">
        <p class="field-label">Zeichen</p>
        <!-- PAI-1068 mounts OfferInlineStyleControls in this slot. No local stub. -->
        <slot name="zeichen" />
      </div>
      <section v-if="selection?.kind === 'text'" class="group" aria-label="Listen und Einzug">
        <h3>Listen &amp; Einzug</h3>
        <div class="segment" role="radiogroup" aria-label="Listentyp" @keydown="onRadioKey($event, 'list')">
          <button
            v-for="item in listKinds"
            :key="item.id"
            type="button"
            role="radio"
            data-group="list-kind"
            :aria-checked="listState.kind === item.id"
            :aria-label="item.name"
            @click="run({ type: 'list', kind: item.id })"
          >
            <component :is="item.icon" :size="13" aria-hidden="true" />
            <span>{{ item.label }}</span>
          </button>
        </div>
        <div class="level-row">
          <p class="level-readout">
            {{ levelShort || 'Ebene' }}
            <span v-if="levelLabel && levelLabel !== levelShort" class="sr-only" aria-hidden="true">{{
              levelLabel
            }}</span>
          </p>
          <span class="level-tip" :data-tip="higherTip" :title="higherTip">
            <button
              type="button"
              :disabled="!canOutdent"
              :aria-label="outdentName"
              @click="run({ type: 'outdent' })"
            >
              <ArrowLeftToLine :size="13" aria-hidden="true" />
              Ausrücken
            </button>
          </span>
          <span class="level-tip" :data-tip="deeperTip" :title="deeperTip">
            <button
              type="button"
              :disabled="!canIndent"
              :aria-label="indentName"
              @click="run({ type: 'indent' })"
            >
              <ArrowRightToLine :size="13" aria-hidden="true" />
              Einrücken
            </button>
          </span>
        </div>
        <template v-if="listState.kind === 'bullet'">
          <div class="bullets" role="radiogroup" aria-label="Aufzählungszeichen" @keydown="onRadioKey($event, 'bullet')">
            <button
              v-for="item in bullets"
              :key="item.id"
              type="button"
              role="radio"
              data-group="bullet"
              :aria-checked="listState.bullet === item.id"
              :aria-label="item.label"
              @click="run({ type: 'marker', marker: item.id })"
            >
              {{ OFFER_BULLET_GLYPH[item.id] }}
            </button>
          </div>
          <label class="inline-field">
            Zeichen
            <input
              type="text"
              maxlength="8"
              :value="shownGlyph()"
              aria-label="Eigenes Aufzählungszeichen"
              @input="onGlyph"
              @blur="finishMarkerDrafts"
            />
          </label>
        </template>
        <template v-if="showNumbering">
          <label class="inline-field">
            Bezug
            <select aria-label="Bezug" :value="bezugValue" @change="onBezug">
              <option value="section">Abschnittsnummer</option>
              <option value="independent">Unabhängig</option>
            </select>
          </label>
          <p v-if="numberingExample" class="num-example">{{ numberingExample }}</p>
          <div class="pair">
            <button
              type="button"
              :aria-pressed="listState.continued === true"
              @click="run({ type: 'numbering', mode: 'continue' })"
            >
              <ListEnd :size="13" aria-hidden="true" />
              Fortsetzen
            </button>
            <button
              type="button"
              :aria-pressed="listState.outline === true && listState.start === 1 && listState.continued !== true"
              @click="run({ type: 'numbering', mode: 'restart' })"
            >
              <ListRestart :size="13" aria-hidden="true" />
              Neu beginnen
            </button>
          </div>
          <label v-if="showStart" class="inline-field">
            Beginn
            <input
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
        </template>
        <div v-if="showMarkerPlace" class="marker-place">
          <div class="place-head">
            <button
              type="button"
              class="disclosure"
              :aria-expanded="placeOpen"
              @click="placeOpen = !placeOpen"
            >
              <ChevronDown :size="13" aria-hidden="true" :class="{ folded: !placeOpen }" />
              Einzug &amp; Position
            </button>
            <button type="button" title="Einzug zurücksetzen" @click="run({ type: 'layout-reset' })">
              <RotateCcw :size="13" aria-hidden="true" />
              <span class="sr-only">Standard</span>
            </button>
          </div>
          <div v-show="placeOpen" class="marker-fields">
            <div class="marker-field">
              <span>Zeichen X</span>
              <span class="marker-move">
                <button type="button" aria-label="Zeichen horizontal nach links" @click="nudgeMarker('x', -0.5)">−</button>
                <input
                  type="text"
                  inputmode="decimal"
                  :value="shownMarker('x')"
                  aria-label="Zeichen horizontal"
                  @input="onMarker('x', $event)"
                  @blur="finishMarkerDrafts"
                />
                <button type="button" aria-label="Zeichen horizontal nach rechts" @click="nudgeMarker('x', 0.5)">+</button>
              </span>
              <span>mm</span>
            </div>
            <div class="marker-field">
              <span>Zeichen Y</span>
              <span class="marker-move">
                <button type="button" aria-label="Zeichen vertikal nach oben" @click="nudgeMarker('y', -0.5)">−</button>
                <input
                  type="text"
                  inputmode="decimal"
                  :value="shownMarker('y')"
                  aria-label="Zeichen vertikal"
                  @input="onMarker('y', $event)"
                  @blur="finishMarkerDrafts"
                />
                <button type="button" aria-label="Zeichen vertikal nach unten" @click="nudgeMarker('y', 0.5)">+</button>
              </span>
              <span>mm</span>
            </div>
            <div class="marker-field">
              <span>Textbeginn</span>
              <span class="marker-move">
                <button type="button" aria-label="Textbeginn nach links" @click="nudgeMarker('text', -0.5)">−</button>
                <input
                  type="text"
                  inputmode="decimal"
                  :value="shownMarker('text')"
                  aria-label="Textbeginn"
                  @input="onMarker('text', $event)"
                  @blur="finishMarkerDrafts"
                />
                <button type="button" aria-label="Textbeginn nach rechts" @click="nudgeMarker('text', 0.5)">+</button>
              </span>
              <span>mm</span>
            </div>
          </div>
        </div>
      </section>
    </div>

    <div v-show="tab === 'section'" class="panel" role="tabpanel" aria-label="Abschnitt">
      <section v-if="sectionSelection" class="group" aria-label="Abschnitt">
        <div class="action-grid">
          <button type="button" :disabled="sectionFull" @click="emit('section-add')">
            <Plus :size="13" aria-hidden="true" />
            Danach
          </button>
          <button
            type="button"
            :disabled="sectionSelection.index === 0"
            aria-label="Abschnitt nach oben"
            @click="emit('section-up')"
          >
            <ArrowUp :size="13" aria-hidden="true" />
            Nach oben
          </button>
          <button
            type="button"
            :disabled="sectionSelection.index >= sectionSelection.count - 1"
            aria-label="Abschnitt nach unten"
            @click="emit('section-down')"
          >
            <ArrowDown :size="13" aria-hidden="true" />
            Nach unten
          </button>
          <button type="button" aria-label="Abschnitt löschen" @click="emit('section-delete')">
            <Trash2 :size="13" aria-hidden="true" />
            Löschen
          </button>
        </div>
      </section>
      <section v-if="selection?.kind === 'position'" class="group" aria-label="Position">
        <div class="action-grid">
          <button
            type="button"
            :disabled="selection.index === 0"
            aria-label="Position nach oben"
            @click="emit('position-up')"
          >
            <ArrowUp :size="13" aria-hidden="true" />
            Nach oben
          </button>
          <button
            type="button"
            :disabled="selection.index >= selection.count - 1"
            aria-label="Position nach unten"
            @click="emit('position-down')"
          >
            <ArrowDown :size="13" aria-hidden="true" />
            Nach unten
          </button>
          <button type="button" aria-label="Position löschen" @click="emit('position-delete')">
            <Trash2 :size="13" aria-hidden="true" />
            Löschen
          </button>
        </div>
      </section>
      <p v-if="!sectionSelection && selection?.kind !== 'position'" class="hint">
        Abschnitt oder Position im Dokument wählen.
      </p>
    </div>

    <div v-show="tab === 'document'" class="panel" role="tabpanel" aria-label="Dokument">
      <section
        v-if="selection?.kind === 'footer'"
        class="group this-offer"
        aria-labelledby="this-offer-heading"
      >
        <p class="hint">Breite und Versatz nur für dieses Angebot.</p>
        <label class="inline-field">
          Fußzeilenlogo Breite
          <span class="mm">
            <input
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
        <label class="inline-field">
          Fußzeilenlogo Versatz
          <span class="mm">
            <input
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
      </section>
      <section class="group future-templates" aria-label="Vorlagen für neue Angebote">
        <h3>Vorlagen für neue Angebote</h3>
        <p class="hint">Für künftige Angebote. Dieses Angebot bleibt unverändert.</p>
        <button type="button" @click="openTemplates">
          <FileStack :size="13" aria-hidden="true" />
          Vorlagen bearbeiten
        </button>
      </section>
    </div>

    <div class="insert">
      <button
        ref="insertButton"
        type="button"
        class="insert-face"
        aria-haspopup="menu"
        :aria-expanded="insertOpen"
        @click="toggleInsert"
      >
        <Plus :size="13" aria-hidden="true" />
        Einfügen
        <ChevronDown :size="13" aria-hidden="true" />
      </button>
      <div
        ref="insertMenu"
        class="insert-menu"
        :class="{ 'is-open': insertOpen }"
        role="menu"
        aria-label="Einfügen"
        :aria-hidden="!insertOpen"
      >
        <button
          v-if="!sectionSelection"
          type="button"
          role="menuitem"
          :tabindex="insertOpen ? 0 : -1"
          :disabled="blockCount >= maxBlocks"
          @click="chooseInsert('section')"
        >
          <ListPlus :size="13" aria-hidden="true" />
          Abschnitt am Ende
        </button>
        <button
          type="button"
          role="menuitem"
          :tabindex="insertOpen ? 0 : -1"
          @click="chooseInsert('position')"
        >
          <Plus :size="13" aria-hidden="true" />
          Leistungsposition
        </button>
      </div>
    </div>
  </aside>
</template>
<style scoped>
.offer-inspector {
  display: flex;
  flex-direction: column;
  gap: 8px;
  box-sizing: border-box;
  width: 340px;
  padding: 10px;
  color: var(--h-text, #203c3d);
  background: var(--h-surface, #fffefa);
  border-left: 1px solid var(--h-line, #d8e2df);
  font-size: 12px;
}
.offer-inspector[hidden] {
  display: none !important;
}
.format-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.format-title,
.group h3,
.offer-inspector h2 {
  margin: 0;
  font-size: 13px;
  font-weight: 650;
  line-height: 1.2;
}
.format-title {
  color: var(--h-text, #203c3d);
}
.offer-inspector h2 {
  color: var(--h-mint, #0e6f6c);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.format-actions,
.level-row,
.pair,
.action-grid,
.segment,
.bullets,
.place-head {
  display: flex;
  align-items: center;
  gap: 4px;
}
.format-actions button,
.group button,
.insert-face,
.insert-menu button,
.marker-move button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  min-height: 28px;
  padding: 0 8px;
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
  line-height: 1;
  white-space: nowrap;
  cursor: pointer;
}
.format-actions button {
  width: 28px;
  padding: 0;
}
.tabs {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 2px;
  padding: 2px;
  border-radius: 8px;
  background: var(--h-canvas, #f7f6f2);
}
.tabs button {
  min-height: 26px;
  padding: 0 4px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--h-muted, #596e70);
  font: inherit;
  font-size: 12px;
  white-space: nowrap;
  cursor: pointer;
}
.tabs button[aria-selected='true'] {
  background: var(--h-surface, #fffefa);
  color: var(--h-mint, #0e6f6c);
  font-weight: 650;
}
.context-detail,
.hint,
.field-label,
.num-example {
  margin: 0;
  color: var(--h-muted, #596e70);
  font-size: 11px;
  line-height: 1.3;
}
.context-detail {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.panel,
.group,
.marker-place,
.marker-fields {
  display: grid;
  gap: 6px;
}
.group {
  padding-top: 8px;
  border-top: 1px solid var(--h-line, #d8e2df);
}
.group button:hover:not(:disabled),
.insert-face:hover,
.insert-menu button:hover:not(:disabled),
.format-actions button:hover:not(:disabled),
.tabs button:hover {
  background: var(--h-fill, #e0f3f0);
}
.group button:focus-visible,
.insert-face:focus-visible,
.insert-menu button:focus-visible,
.format-actions button:focus-visible,
.tabs button:focus-visible,
.inline-field input:focus-visible,
.inline-field select:focus-visible,
.marker-move input:focus-visible {
  outline: 1px solid var(--h-mint, #0e6f6c);
  outline-offset: 2px;
}
.group button:disabled,
.format-actions button:disabled,
.insert-menu button:disabled {
  opacity: 0.45;
  cursor: default;
}
.group button[aria-checked='true'],
.group button[aria-pressed='true'] {
  background: var(--h-fill, #e0f3f0);
  color: var(--h-mint, #0e6f6c);
}
.segment,
.action-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.action-grid {
  grid-template-columns: 1fr 1fr;
}
.segment button,
.action-grid button,
.pair button {
  min-width: 0;
}
.level-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) minmax(0, 1fr);
}
.level-row .level-tip,
.level-row .level-tip button {
  min-width: 0;
}
.level-tip {
  position: relative;
  display: flex;
}
.level-tip button {
  width: 100%;
}
.level-readout {
  margin: 0;
  font-size: 12px;
  font-weight: 650;
  line-height: 28px;
  white-space: nowrap;
}
.bullets {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
}
.bullets button {
  font-size: 15px;
}
.inline-field,
.mm {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
.inline-field input,
.inline-field select,
.mm input {
  width: 132px;
  min-height: 28px;
  padding: 0 6px;
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
}
.mm {
  width: 132px;
  justify-content: flex-end;
}
.mm input {
  width: 72px;
}
.num-example {
  font-variant-numeric: tabular-nums;
  color: var(--h-text, #203c3d);
}
.place-head {
  justify-content: space-between;
}
.disclosure {
  border: 0 !important;
  padding: 0 !important;
  background: transparent !important;
  font-weight: 650;
}
.disclosure svg {
  transition: transform 120ms linear;
}
.disclosure svg.folded {
  transform: rotate(-90deg);
}
.marker-field {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr) auto;
  gap: 4px;
  align-items: center;
}
.marker-move {
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr) 24px;
  gap: 2px;
  align-items: center;
}
.marker-move input {
  width: 100%;
  min-width: 0;
  min-height: 28px;
  padding: 0 4px;
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
}
.marker-move button {
  min-height: 28px;
  padding: 0;
}
.level-tip:hover::after,
.level-tip:focus-within::after {
  content: attr(data-tip);
  position: absolute;
  z-index: 5;
  left: 0;
  top: calc(100% + 4px);
  width: max-content;
  max-width: 220px;
  padding: 4px 6px;
  border-radius: 6px;
  background: var(--h-text, #203c3d);
  color: var(--h-surface, #fffefa);
  font-size: 11px;
  line-height: 1.3;
  white-space: normal;
  pointer-events: none;
}
.insert {
  position: relative;
  margin-top: auto;
}
.insert-face,
.group > button,
.insert-menu button {
  width: 100%;
}
.insert-menu {
  position: fixed;
  z-index: 30;
  display: grid;
  gap: 2px;
  width: min(220px, calc(100vw - 16px));
  padding: 4px;
  background: var(--h-surface, #fffefa);
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 8px;
  box-shadow: 0 8px 24px #10232714;
}
.insert-menu:not(.is-open) {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
@media (prefers-reduced-motion: reduce) {
  .disclosure svg {
    transition: none;
  }
}
</style>
