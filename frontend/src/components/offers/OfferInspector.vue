<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Redo2, Undo2 } from 'lucide-vue-next'
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

const widthDraft = ref<string | null>(null)
const offsetDraft = ref<string | null>(null)
const startDraft = ref<string | null>(null)
const glyphDraft = ref<string | null>(null)
const markerDraft = ref<{ axis: 'x' | 'y' | 'text'; raw: string } | null>(null)
const session = useOfferProseSession()
const maxBlocks = 20
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
  if (selection?.kind === 'heading' || selection?.kind === 'text')
    return selection.heading.trim() || 'ohne Überschrift'
  if (selection?.kind === 'position') return 'Leistungsposition'
  return ''
})
const textLabel = computed(() =>
  props.selection?.kind === 'text' ? 'Text in diesem Abschnitt' : '',
)
const shownWidth = computed(() => props.footer?.logo_width_mm ?? OFFER_FOOTER_LOGO.defaultWidthMm)
const shownOffset = computed(() => props.footer?.logo_offset_mm ?? OFFER_FOOTER_LOGO.legacyOffsetMm)
const sectionFull = computed(() => (sectionSelection.value?.count ?? props.blockCount) >= maxBlocks)

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
const showMarkerPlace = computed(() => listState.value.kind !== 'none')

watch(
  () => props.selection?.kind,
  (kind, previous) => {
    if (previous === 'footer' && kind !== 'footer') {
      finishWidth()
      finishOffset()
    }
    if (previous === 'text' && kind !== 'text') finishListStart()
  },
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
function onDocPointer(event: PointerEvent) {
  const target = event.target
  if (target instanceof Node && (event.currentTarget as Node | null) === target) return
  const root = document.getElementById('offer-inspector')
  if (target instanceof Node && root?.contains(target)) return
  finishListStart()
  finishMarkerDrafts()
  finishWidth()
  finishOffset()
}
onMounted(() => window.addEventListener('pointerdown', onDocPointer))
onBeforeUnmount(() => window.removeEventListener('pointerdown', onDocPointer))
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
    <header class="inspector-head">
      <h2 :id="selection?.kind === 'footer' ? 'this-offer-heading' : undefined">
        {{ contextTitle }}
      </h2>
      <p v-if="contextDetail" class="context-detail">{{ contextDetail }}</p>
      <p v-if="textLabel" class="hint">{{ textLabel }}</p>
    </header>
    <div class="undo-row" role="group" aria-label="Änderungen">
      <button type="button" :disabled="!canUndo" aria-label="Rückgängig" @click="undoEdit">
        <Undo2 :size="15" aria-hidden="true" />
        Rückgängig
      </button>
      <button type="button" :disabled="!canRedo" aria-label="Wiederholen" @click="redoEdit">
        <Redo2 :size="15" aria-hidden="true" />
        Wiederholen
      </button>
    </div>
    <p v-if="!selection || selection.kind === 'none'" class="hint">
      Wähle eine Überschrift, einen Text, eine Leistungsposition oder das Fußzeilenlogo. Einfügen
      bleibt hier verfügbar.
    </p>
    <section v-if="sectionSelection" class="group" aria-label="Abschnitt">
      <h3>Abschnitt</h3>
      <div class="action-grid">
        <button type="button" :disabled="sectionFull" @click="emit('section-add')">Danach</button>
        <button
          type="button"
          :disabled="sectionSelection.index === 0"
          aria-label="Abschnitt nach oben"
          @click="emit('section-up')"
        >
          Nach oben
        </button>
        <button
          type="button"
          :disabled="sectionSelection.index >= sectionSelection.count - 1"
          aria-label="Abschnitt nach unten"
          @click="emit('section-down')"
        >
          Nach unten
        </button>
        <button type="button" aria-label="Abschnitt löschen" @click="emit('section-delete')">
          Löschen
        </button>
      </div>
    </section>
    <section v-if="selection?.kind === 'text'" class="group" aria-label="Text">
      <h3>Text</h3>
      <p class="field-label">Listentyp</p>
      <div
        class="segment"
        role="radiogroup"
        aria-label="Listentyp"
        @keydown="onRadioKey($event, 'list')"
      >
        <button
          v-for="item in listKinds"
          :key="item.id"
          type="button"
          role="radio"
          data-group="list-kind"
          :aria-checked="listState.kind === item.id"
          :aria-label="item.label"
          @click="run({ type: 'list', kind: item.id })"
        >
          {{ item.label }}
        </button>
      </div>
      <template v-if="listState.kind !== 'ordered'">
        <p class="field-label">Aufzählungszeichen</p>
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
        <label v-if="listState.kind === 'bullet'" class="start-row">
          Eigenes Zeichen
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
      <p v-if="levelLabel" class="level-readout">{{ levelLabel }}</p>
      <div class="action-grid">
        <span class="level-tip" :data-tip="deeperTip" :title="deeperTip">
          <button
            type="button"
            :disabled="!canIndent"
            :aria-label="indentName"
            @click="run({ type: 'indent' })"
          >
            Einrücken
          </button>
        </span>
        <span class="level-tip" :data-tip="higherTip" :title="higherTip">
          <button
            type="button"
            :disabled="!canOutdent"
            :aria-label="outdentName"
            @click="run({ type: 'outdent' })"
          >
            Ausrücken
          </button>
        </span>
      </div>
      <template v-if="listState.kind !== 'bullet'">
        <p class="field-label">Nummerierung</p>
        <div class="action-grid">
          <button
            type="button"
            :aria-pressed="listState.sectionBound === true"
            @click="run({ type: 'numbering', mode: 'section' })"
          >
            Abschnittsnummer
          </button>
          <button
            type="button"
            :aria-pressed="listState.sectionBound === false"
            @click="run({ type: 'numbering', mode: 'independent' })"
          >
            Unabhängig
          </button>
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
      </template>
      <div v-if="showMarkerPlace" class="marker-place">
        <p class="hint">Gilt für die ausgewählten Listeneinträge.</p>
        <div class="marker-field">
          <span>Zeichen horizontal</span>
          <span class="marker-move">
            <button
              type="button"
              aria-label="Zeichen horizontal nach links"
              @click="nudgeMarker('x', -0.5)"
            >
              −
            </button>
            <input
              type="text"
              inputmode="decimal"
              :value="shownMarker('x')"
              aria-label="Zeichen horizontal"
              @input="onMarker('x', $event)"
              @blur="finishMarkerDrafts"
            />
            <button
              type="button"
              aria-label="Zeichen horizontal nach rechts"
              @click="nudgeMarker('x', 0.5)"
            >
              +
            </button>
            <span>mm</span>
          </span>
        </div>
        <div class="marker-field">
          <span>Zeichen vertikal</span>
          <span class="marker-move">
            <button
              type="button"
              aria-label="Zeichen vertikal nach oben"
              @click="nudgeMarker('y', -0.5)"
            >
              −
            </button>
            <input
              type="text"
              inputmode="decimal"
              :value="shownMarker('y')"
              aria-label="Zeichen vertikal"
              @input="onMarker('y', $event)"
              @blur="finishMarkerDrafts"
            />
            <button
              type="button"
              aria-label="Zeichen vertikal nach unten"
              @click="nudgeMarker('y', 0.5)"
            >
              +
            </button>
            <span>mm</span>
          </span>
        </div>
        <div class="marker-field">
          <span>Textbeginn</span>
          <span class="marker-move">
            <button type="button" aria-label="Textbeginn nach links" @click="nudgeMarker('text', -0.5)">
              −
            </button>
            <input
              type="text"
              inputmode="decimal"
              :value="shownMarker('text')"
              aria-label="Textbeginn"
              @input="onMarker('text', $event)"
              @blur="finishMarkerDrafts"
            />
            <button type="button" aria-label="Textbeginn nach rechts" @click="nudgeMarker('text', 0.5)">
              +
            </button>
            <span>mm</span>
          </span>
        </div>
        <p class="hint">Negativ nach oben oder links, positiv nach unten oder rechts.</p>
        <button type="button" @click="run({ type: 'layout-reset' })">Standard</button>
      </div>
    </section>
    <section v-if="selection?.kind === 'position'" class="group" aria-label="Position">
      <h3>Position</h3>
      <div class="action-grid">
        <button type="button" :disabled="selection.index === 0" @click="emit('position-up')">
          Nach oben
        </button>
        <button
          type="button"
          :disabled="selection.index >= selection.count - 1"
          @click="emit('position-down')"
        >
          Nach unten
        </button>
        <button type="button" @click="emit('position-delete')">Löschen</button>
      </div>
    </section>
    <section
      v-if="selection?.kind === 'footer'"
      class="group this-offer"
      aria-labelledby="this-offer-heading"
    >
      <p class="hint">Logo-Breite und vertikaler Versatz gelten nur für dieses Angebot.</p>
      <label>
        Logo-Breite
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
      <label>
        Vertikaler Versatz
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
      <p class="hint">
        Negativ nach oben, positiv nach unten. Nummer, Linie und Seitenzahl bleiben.
      </p>
    </section>
    <section class="group" aria-label="Einfügen">
      <h3>Einfügen</h3>
      <button
        v-if="!sectionSelection"
        type="button"
        :disabled="blockCount >= maxBlocks"
        @click="emit('insert-section')"
      >
        Abschnitt am Ende
      </button>
      <button type="button" @click="emit('insert-position')">Leistungsposition</button>
    </section>
    <section class="group future-templates" aria-label="Vorlagen für neue Angebote">
      <h3>Vorlagen für neue Angebote</h3>
      <p class="hint">
        Absender und Textvorlagen für künftige Angebote. Dieses Angebot bleibt unverändert.
      </p>
      <button type="button" @click="openTemplates">Vorlagen bearbeiten</button>
    </section>
  </aside>
</template>
<style scoped>
.offer-inspector {
  display: flex;
  flex-direction: column;
  gap: 12px;
  box-sizing: border-box;
  width: 300px;
  padding: 12px;
  color: var(--h-text, #203c3d);
  background: var(--h-surface, #fffefa);
  border-left: 1px solid var(--h-line, #d8e2df);
  font-size: 12px;
}
.offer-inspector[hidden] {
  display: none !important;
}
.inspector-head h2,
.group h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 650;
  line-height: 1.3;
}
.inspector-head h2 {
  color: var(--h-mint, #0e6f6c);
}
.context-detail {
  margin: 2px 0 0;
  color: var(--h-text, #203c3d);
  font-size: 12px;
  line-height: 1.35;
}
.hint,
.field-label {
  margin: 0;
  color: var(--h-muted, #596e70);
  font-size: 11px;
  line-height: 1.35;
}
.field-label {
  margin-bottom: 4px;
}
.group {
  display: grid;
  gap: 6px;
  padding-top: 10px;
  border-top: 1px solid var(--h-line, #d8e2df);
}
.group button,
.undo-row button,
.start-row input,
.mm input {
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
}
.group button,
.undo-row button {
  min-height: 30px;
  padding: 4px 8px;
  cursor: pointer;
  text-align: center;
}
.group button:hover:not(:disabled),
.undo-row button:hover:not(:disabled) {
  background: var(--h-fill, #e0f3f0);
}
.group button:focus-visible,
.undo-row button:focus-visible,
.mm input:focus-visible,
.start-row input:focus-visible {
  outline: 1px solid var(--h-mint, #0e6f6c);
  outline-offset: 2px;
}
.group button:disabled,
.undo-row button:disabled {
  opacity: 0.45;
  cursor: default;
}
.group button[aria-checked='true'],
.group button[aria-pressed='true'] {
  background: var(--h-fill, #e0f3f0);
  color: var(--h-mint, #0e6f6c);
}
.undo-row,
.action-grid,
.segment,
.bullets {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px;
}
.undo-row button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
}
.segment {
  grid-template-columns: 1fr 1fr 1fr;
}
.bullets {
  grid-template-columns: repeat(4, 1fr);
}
.bullets button {
  font-size: 16px;
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
  line-height: 1.35;
}
.marker-place {
  display: grid;
  gap: 6px;
}
.marker-field {
  display: grid;
  gap: 4px;
}
.marker-move {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr) 28px auto;
  gap: 4px;
  align-items: center;
}
.marker-move input {
  width: 100%;
  min-height: 30px;
  padding: 0 6px;
  border: 1px solid var(--h-line, #d8e2df);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
}
.marker-move button {
  min-height: 30px;
  padding: 0;
}
.level-tip:hover::after,
.level-tip:focus-within::after {
  content: attr(data-tip);
  position: absolute;
  z-index: 5;
  left: 0;
  top: calc(100% + 6px);
  width: max-content;
  max-width: 240px;
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--h-text, #203c3d);
  color: var(--h-surface, #fffefa);
  font-size: 11px;
  line-height: 1.35;
  white-space: normal;
  pointer-events: none;
}
.start-row,
.mm {
  display: flex;
  align-items: center;
  gap: 6px;
}
.start-row {
  justify-content: space-between;
}
.start-row input,
.mm input {
  width: 88px;
  min-height: 30px;
  padding: 0 8px;
}
.mm {
  flex: 1;
}
.mm input {
  width: 100%;
}
.group > button {
  width: 100%;
}
</style>
