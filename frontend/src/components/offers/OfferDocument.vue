<script setup lang="ts">
import '@fontsource/anta/latin-400.css'
import '@fontsource/manrope/latin-400.css'
import '@fontsource/manrope/latin-500.css'
import '@fontsource/manrope/latin-600.css'
import '@fontsource/manrope/latin-700.css'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import OfferCover from './OfferCover.vue'
import OfferText from './OfferText.vue'
import OfferProse from './OfferProse.vue'
import {
  createProseHistory,
  offerBlockExceedsPage,
  offerBlockOverflowMessage,
  persistProse,
  proseNodes,
  type SectionEditorMemory,
} from './offerProse'
import type { OfferBlock, OfferPosition, OfferSelection, OfferTextNode } from './types'
import { date, type Offer } from './types'
import { OFFER_FOOTER_LOGO } from './offerLayout'
import OfferTable from './OfferTable.vue'
import OfferAcceptance from './OfferAcceptance.vue'
import OfferFootmark from './OfferFootmark.vue'
import OfferBrandDots from './OfferBrandDots.vue'
const props = defineProps<{
  offer: Pick<Offer, 'offer_no' | 'document'> & Partial<Offer>
  zoom?: number
  editable?: boolean
  publicUrl?: string
  qrPreview?: boolean
}>()
const emit = defineEmits<{
  overflow: [message: string]
  change: []
  select: [value: OfferSelection]
}>()
type Page = {
  kind: 'cover' | 'terms' | 'positions'
  heading?: 'terms' | 'positions'
  blocks: number[]
  positions: number[]
  acceptance: boolean
}
const pages = ref<Page[]>([
  { kind: 'cover', blocks: [], positions: [], acceptance: false },
  { kind: 'positions', blocks: [], positions: [], acceptance: true },
])
const measure = ref<HTMLElement>()
const printBlocked = ref('')
const probe = ref<HTMLElement>()
let queued = false
async function paginate() {
  await nextTick()
  const root = measure.value
  const available = (probe.value?.getBoundingClientRect().height ?? 0) - 12
  if (!root || available <= 0) return
  const height = (s: string) => root.querySelector(s)?.getBoundingClientRect().height ?? 0
  const termsHeadingHeight = height('[data-section-heading="terms"]')
  const positionsHeadingHeight = height('[data-section-heading="positions"]')
  const tableHeaderHeight = height('.offer-table thead') + 12
  const continuationInset = height('[data-page-inset]')
  const next: Page[] = [{ kind: 'cover', blocks: [], positions: [], acceptance: false }]
  let remaining = available - height('.offer-cover')
  let error =
    remaining < 0
      ? 'Der Angebotskopf ist zu lang. Bitte Titel, Anschrift oder Einleitung kürzen.'
      : ''
  for (const [i] of props.offer.document.blocks.entries()) {
    const h = height(`[data-block="${i}"]`) + 17
    const headingHeight = i === 0 ? termsHeadingHeight : 0
    if (offerBlockExceedsPage(h, available, continuationInset, headingHeight))
      error = offerBlockOverflowMessage(i)
    if (h + headingHeight > remaining) {
      next.push({ kind: 'terms', blocks: [], positions: [], acceptance: false })
      remaining = available - (headingHeight ? 0 : continuationInset)
    }
    if (i === 0) next[next.length - 1]!.heading = 'terms'
    next[next.length - 1]!.blocks.push(i)
    remaining -= h + headingHeight
  }
  const positionPage = (): Page => ({
    kind: 'positions',
    blocks: [],
    positions: [],
    acceptance: false,
  })
  next.push(positionPage())
  next[next.length - 1]!.heading = 'positions'
  remaining = available - positionsHeadingHeight - tableHeaderHeight
  for (const [i] of props.offer.document.positions.entries()) {
    const h = height(`[data-position="${i}"]`) + 2
    if (h > available - tableHeaderHeight - (i === 0 ? positionsHeadingHeight : continuationInset))
      error = `Position ${i + 1} ist länger als eine Seite. Bitte die Beschreibung auf mehrere Positionen verteilen.`
    if (h > remaining && i > 0) {
      next.push(positionPage())
      remaining = available - continuationInset - tableHeaderHeight
    }
    next[next.length - 1]!.positions.push(i)
    remaining -= h
  }
  const acceptanceHeight = height('.offer-acceptance') + 28
  if (acceptanceHeight > available - continuationInset)
    error = 'Der Annahmetext ist zu lang. Bitte kürzen.'
  if (acceptanceHeight > remaining) next.push(positionPage())
  next[next.length - 1]!.acceptance = true
  if (JSON.stringify(next) !== JSON.stringify(pages.value)) pages.value = next
  printBlocked.value = error
  emit('overflow', error)
}
function schedule() {
  if (queued) return
  queued = true
  void nextTick().then(async () => {
    queued = false
    await paginate()
  })
}
watch(() => [props.offer, props.publicUrl, props.qrPreview], schedule, { deep: true })
onMounted(async () => {
  await document.fonts.ready
  await paginate()
})
function applyBody(index: number, next: { body: string; nodes?: OfferTextNode[] }) {
  const block = props.offer.document.blocks[index]
  if (!block) return
  block.body = next.body
  if (next.nodes) block.nodes = next.nodes
  else delete block.nodes
}
function remove(i: number) {
  withPositions(() => {
    props.offer.document.positions.splice(i, 1)
  })
  if (activePosition.value === i) activePosition.value = null
  emit('change')
}
function move(i: number, direction: number) {
  const j = i + direction
  if (j < 0 || j >= props.offer.document.positions.length) return
  withPositions(() => {
    const p = props.offer.document.positions.splice(i, 1)[0]!
    props.offer.document.positions.splice(j, 0, p)
  })
  activePosition.value = j
  emit('change')
}
const OFFER_MAX_BLOCKS = 20
const sectionIds = new WeakMap<object, number>()
const sectionMemories = new WeakMap<object, SectionEditorMemory>()
let nextSectionId = 1
function sectionKey(block: object | undefined): number {
  if (!block) return nextSectionId++
  const known = sectionIds.get(block)
  if (known != null) return known
  const id = nextSectionId++
  sectionIds.set(block, id)
  return id
}
type DocUndo =
  | { type: 'prose'; block: OfferBlock }
  | { type: 'blocks'; before: OfferBlock[]; after: OfferBlock[] }
  | { type: 'positions'; before: OfferPosition[]; after: OfferPosition[] }
const undoStack: DocUndo[] = []
const redoStack: DocUndo[] = []
const canUndo = ref(false)
const canRedo = ref(false)
const activeKind = ref<OfferSelection['kind']>('none')
const activePosition = ref<number | null>(null)
const deleteAsk = ref<number | null>(null)
function markHistory() {
  canUndo.value = undoStack.length > 0
  canRedo.value = redoStack.length > 0
}
function recordProse(block: OfferBlock) {
  undoStack.push({ type: 'prose', block })
  redoStack.length = 0
  markHistory()
}
function applyProseUndo(
  block: OfferBlock,
  nodes: OfferTextNode[],
  caret: { index: number; offset: number },
) {
  const index = props.offer.document.blocks.indexOf(block)
  const stored = persistProse(nodes, index >= 0 ? index + 1 : 0)
  block.body = stored.body
  if (stored.nodes) block.nodes = stored.nodes
  else delete block.nodes
  const memory = sectionMemories.get(block)
  if (memory) memory.caret = caret
  if (index >= 0) {
    activeBlock.value = index
    activeKind.value = 'text'
    activeField.value = 'body'
    focusSection(index, 'body')
  }
}
function undoDocument() {
  const entry = undoStack.pop()
  if (!entry) return
  redoStack.push(entry)
  if (entry.type === 'prose') {
    const memory = sectionMemories.get(entry.block)
    const current = proseNodes(entry.block.body, entry.block.nodes)
    const prev = memory?.history.undo({
      nodes: current,
      caret: memory.caret ?? { index: 0, offset: 0 },
    })
    if (prev) applyProseUndo(entry.block, prev.nodes, prev.caret)
  } else if (entry.type === 'blocks') {
    props.offer.document.blocks.splice(0, props.offer.document.blocks.length, ...entry.before)
  } else {
    props.offer.document.positions.splice(0, props.offer.document.positions.length, ...entry.before)
  }
  markHistory()
  emit('change')
}
function redoDocument() {
  const entry = redoStack.pop()
  if (!entry) return
  undoStack.push(entry)
  if (entry.type === 'prose') {
    const memory = sectionMemories.get(entry.block)
    const current = proseNodes(entry.block.body, entry.block.nodes)
    const next = memory?.history.redo({
      nodes: current,
      caret: memory.caret ?? { index: 0, offset: 0 },
    })
    if (next) applyProseUndo(entry.block, next.nodes, next.caret)
  } else if (entry.type === 'blocks') {
    props.offer.document.blocks.splice(0, props.offer.document.blocks.length, ...entry.after)
  } else {
    props.offer.document.positions.splice(0, props.offer.document.positions.length, ...entry.after)
  }
  markHistory()
  emit('change')
}
function withBlocks(mutate: () => void) {
  const before = props.offer.document.blocks.slice()
  mutate()
  const after = props.offer.document.blocks.slice()
  if (before.length === after.length && before.every((block, index) => block === after[index]))
    return
  undoStack.push({ type: 'blocks', before, after })
  redoStack.length = 0
  markHistory()
}
function withPositions(mutate: () => void) {
  const before = props.offer.document.positions.slice()
  mutate()
  const after = props.offer.document.positions.slice()
  if (before.length === after.length && before.every((row, index) => row === after[index])) return
  undoStack.push({ type: 'positions', before, after })
  redoStack.length = 0
  markHistory()
}
function sectionMemory(block: object | undefined): SectionEditorMemory | null {
  if (!block) return null
  let memory = sectionMemories.get(block)
  if (!memory) {
    memory = { history: createProseHistory(), caret: null }
    sectionMemories.set(block, memory)
  }
  const owned = block as OfferBlock
  memory.record = () => recordProse(owned)
  memory.requestUndo = () => undoDocument()
  memory.requestRedo = () => redoDocument()
  return memory
}
const sheetEl = ref<HTMLElement>()
const activeBlock = ref<number | null>(null)
const activeField = ref<'heading' | 'body'>('heading')
const canAdd = computed(
  () => !!props.editable && props.offer.document.blocks.length < OFFER_MAX_BLOCKS,
)
const canUp = computed(() => {
  const index = activeBlock.value
  return !!props.editable && index != null && index > 0
})
const canDown = computed(() => {
  const index = activeBlock.value
  return !!props.editable && index != null && index < props.offer.document.blocks.length - 1
})
function onSheetFocusIn(event: FocusEvent) {
  const target = event.target
  if (!(target instanceof Element)) return
  const sec = target.closest('[data-section]')
  if (!sec) return
  const index = Number(sec.getAttribute('data-section'))
  if (!Number.isInteger(index)) return
  activeBlock.value = index
  activeField.value = target.closest('.offer-prose') ? 'body' : 'heading'
  activeKind.value = activeField.value === 'body' ? 'text' : 'heading'
}
function focusSection(index: number, field: 'heading' | 'body') {
  const tryFocus = (left: number) => {
    const sec = sheetEl.value?.querySelector(`[data-section="${index}"]`)
    const el =
      field === 'body'
        ? sec?.querySelector<HTMLElement>('.offer-prose')
        : sec?.querySelector<HTMLElement>('h3')
    if (el) {
      el.focus()
      return
    }
    if (left <= 0) return
    void nextTick(() => tryFocus(left - 1))
  }
  void nextTick(() => tryFocus(4))
}
function addSection() {
  if (!canAdd.value) return
  const blocks = props.offer.document.blocks
  const at =
    activeBlock.value == null ? blocks.length : Math.min(blocks.length, activeBlock.value + 1)
  withBlocks(() => {
    blocks.splice(at, 0, { heading: '', body: '' })
  })
  activeBlock.value = at
  activeKind.value = 'heading'
  activeField.value = 'heading'
  emit('change')
  focusSection(at, 'heading')
}
function moveSection(direction: -1 | 1) {
  const index = activeBlock.value
  if (index == null) return
  const blocks = props.offer.document.blocks
  const next = index + direction
  if (next < 0 || next >= blocks.length) return
  withBlocks(() => {
    const [block] = blocks.splice(index, 1)
    if (block) blocks.splice(next, 0, block)
  })
  activeBlock.value = next
  emit('change')
  focusSection(next, activeField.value)
}
function askDeleteSection(index = activeBlock.value) {
  if (index == null || !props.offer.document.blocks[index]) return
  deleteAsk.value = index
}
function confirmDeleteSection() {
  const index = deleteAsk.value
  if (index == null) return
  const blocks = props.offer.document.blocks
  withBlocks(() => {
    blocks.splice(index, 1)
  })
  deleteAsk.value = null
  activeBlock.value = blocks.length ? Math.min(index, blocks.length - 1) : null
  activeKind.value = activeBlock.value == null ? 'none' : 'heading'
  emit('change')
  if (activeBlock.value != null) focusSection(activeBlock.value, 'heading')
}
function selectFooter() {
  if (!props.editable) return
  activeKind.value = 'footer'
  sheetEl.value?.querySelector<HTMLElement>('.ftr .footmark')?.focus()
}
function onPositionFocus(event: FocusEvent) {
  const target = event.target
  if (!(target instanceof Element)) return
  const row = target.closest('[data-position]')
  if (!row) return
  const index = Number(row.getAttribute('data-position'))
  if (!Number.isInteger(index)) return
  activeKind.value = 'position'
  activePosition.value = index
}
function currentSelection(): OfferSelection {
  const blocks = props.offer.document.blocks
  if (activeKind.value === 'footer') return { kind: 'footer' }
  if (activeKind.value === 'position' && activePosition.value != null)
    return {
      kind: 'position',
      index: activePosition.value,
      count: props.offer.document.positions.length,
    }
  if (
    (activeKind.value === 'heading' || activeKind.value === 'text') &&
    activeBlock.value != null &&
    blocks[activeBlock.value]
  ) {
    return activeKind.value === 'heading'
      ? {
          kind: 'heading',
          index: activeBlock.value,
          count: blocks.length,
          heading: blocks[activeBlock.value]!.heading,
        }
      : { kind: 'text', index: activeBlock.value, count: blocks.length }
  }
  return { kind: 'none' }
}
watch(
  () => [
    activeKind.value,
    activeBlock.value,
    activePosition.value,
    props.offer.document.blocks.length,
    props.offer.document.positions.length,
    activeBlock.value == null ? '' : props.offer.document.blocks[activeBlock.value]?.heading,
  ],
  () => emit('select', currentSelection()),
)
const footerShift = computed(() => {
  const footer = props.offer.document.footer
  if (!footer) return undefined
  const scale = footer.logo_width_mm / OFFER_FOOTER_LOGO.defaultWidthMm
  return {
    '--mark-offset': `${footer.logo_offset_mm}mm`,
    '--mark-font': `${7.5 * scale}pt`,
  }
})
defineExpose({
  paginate,
  undo: undoDocument,
  redo: redoDocument,
  canUndo,
  canRedo,
  addSection,
  moveSection,
  askDeleteSection,
  selectFooter,
  move,
  remove,
})
</script>
<template>
  <div class="offer-document" :data-print-blocked="printBlocked || undefined">
    <div class="offer-measure" aria-hidden="true" inert>
      <section class="page">
        <div class="hdr">ANGEBOT</div>
        <div ref="probe" class="page-content" />
        <div class="ftr">SEITE</div>
      </section>
      <div ref="measure" class="offer-measure-content">
        <OfferCover :offer="offer" />
        <div class="page-continuation" data-page-inset />
        <h2 class="section-heading" data-section-heading="terms">
          <span>I. BEDINGUNGEN</span><OfferBrandDots />
        </h2>
        <h2 class="section-heading" data-section-heading="positions">
          <span>{{ offer.document.blocks.length ? 'II.' : 'I.' }} LEISTUNGSAUFSTELLUNG</span>
          <OfferBrandDots />
        </h2>
        <div
          v-for="(block, i) in offer.document.blocks"
          :key="sectionKey(block)"
          class="sec"
          :data-block="i"
        >
          <span class="n">{{ i + 1 }}</span>
          <h3>{{ block.heading }}</h3>
          <OfferProse :body="block.body" :nodes="block.nodes" :section-number="i + 1" />
        </div>
        <OfferTable
          :positions="offer.document.positions"
          :indices="offer.document.positions.map((_, i) => i)"
        /><OfferAcceptance
          :document="offer.document"
          :receipt="offer"
          :public-url="publicUrl"
          :qr-preview="qrPreview"
        />
      </div>
    </div>
    <div
      ref="sheetEl"
      class="sheet"
      :style="{ '--offer-zoom': zoom ?? 1 }"
      @focusin="onSheetFocusIn"
    >
      <section
        v-for="(page, index) in pages"
        :key="index"
        :class="['page', page.kind === 'cover' ? 'p1' : 'p2']"
        :aria-label="`Seite ${index + 1}`"
      >
        <div class="hdr">
          <span>ANGEBOT {{ offer.offer_no }}</span>
          <span class="right">{{ date(offer.document.offer_date) }}</span>
        </div>
        <div class="page-content">
          <div v-if="index > 0 && !page.heading" class="page-continuation" />
          <OfferCover v-if="page.kind === 'cover'" :offer="offer" :editable="editable" />
          <h2 v-if="page.heading" class="section-heading">
            <span>{{
              page.heading === 'terms'
                ? 'I. BEDINGUNGEN'
                : `${offer.document.blocks.length ? 'II.' : 'I.'} LEISTUNGSAUFSTELLUNG`
            }}</span>
            <OfferBrandDots />
          </h2>
          <div v-if="page.blocks.length" class="sections">
            <div
              v-for="i in page.blocks"
              :key="sectionKey(offer.document.blocks[i])"
              class="sec"
              :data-section="i"
            >
              <span class="n">{{ i + 1 }}</span
              ><OfferText
                v-model="offer.document.blocks[i]!.heading"
                tag="h3"
                :editable="editable"
                :label="`Überschrift Textbaustein ${i + 1}`"
              /><span
                v-if="editable"
                class="sec-actions"
                :aria-label="`Aktionen für Abschnitt ${i + 1}`"
              >
                <button
                  type="button"
                  title="Abschnitt danach hinzufügen"
                  :aria-label="`Abschnitt ${i + 1} danach hinzufügen`"
                  :disabled="!canAdd"
                  @mousedown.prevent
                  @click="
                    () => {
                      activeBlock = i
                      addSection()
                    }
                  "
                >
                  +
                </button>
                <button
                  type="button"
                  title="Abschnitt nach oben verschieben"
                  aria-label="Abschnitt nach oben"
                  :disabled="i === 0"
                  @mousedown.prevent
                  @click="
                    () => {
                      activeBlock = i
                      moveSection(-1)
                    }
                  "
                >
                  ↑
                </button>
                <button
                  type="button"
                  title="Abschnitt nach unten verschieben"
                  aria-label="Abschnitt nach unten"
                  :disabled="i === offer.document.blocks.length - 1"
                  @mousedown.prevent
                  @click="
                    () => {
                      activeBlock = i
                      moveSection(1)
                    }
                  "
                >
                  ↓
                </button>
                <button
                  type="button"
                  title="Diesen Abschnitt löschen"
                  :aria-label="`Abschnitt ${i + 1} löschen`"
                  @mousedown.prevent
                  @click="askDeleteSection(i)"
                >
                  ×
                </button> </span
              ><OfferProse
                :body="offer.document.blocks[i]!.body"
                :nodes="offer.document.blocks[i]!.nodes"
                :memory="sectionMemory(offer.document.blocks[i])"
                :section-number="i + 1"
                :editable="editable"
                :label="`Textbaustein ${i + 1}`"
                @update="applyBody(i, $event)"
              />
            </div>
          </div>
          <OfferTable
            v-if="page.positions.length"
            @focusin="onPositionFocus"
            :positions="offer.document.positions"
            :indices="page.positions"
            :editable="editable"
            @remove="remove"
            @move="move"
            @change="emit('change')"
          />
          <OfferAcceptance
            v-if="page.acceptance"
            :document="offer.document"
            :receipt="offer"
            :public-url="publicUrl"
            :qr-preview="qrPreview"
            :editable="editable"
          />
        </div>
        <div class="ftr">
          <span>{{ offer.offer_no }}</span
          ><OfferFootmark
            v-if="offer.document.sender.company.trim().toLowerCase() === 'augmentoring gmbh'"
            :layout="offer.document.footer"
            :tabindex="editable ? 0 : undefined"
            role="button"
            aria-label="Fußzeilenlogo auswählen"
            @click="selectFooter"
            @keydown.enter.prevent="selectFooter"
          /><span
            v-else
            class="footmark"
            :class="{ 'is-set': !!offer.document.footer, 'is-selected': activeKind === 'footer' }"
            :style="footerShift"
            :tabindex="editable ? 0 : undefined"
            :role="editable ? 'button' : undefined"
            aria-label="Fußzeilenlogo auswählen"
            @click="selectFooter"
            @keydown.enter.prevent="selectFooter"
            ><span class="lockup">{{ offer.document.sender.company }}</span></span
          ><span class="right">SEITE {{ index + 1 }} VON {{ pages.length }}</span>
        </div>
      </section>
    </div>
    <dialog
      v-if="deleteAsk != null"
      open
      class="section-delete"
      :aria-label="`Abschnitt ${deleteAsk + 1} löschen`"
    >
      <p>
        Abschnitt {{ deleteAsk + 1 }}
        <strong>{{ offer.document.blocks[deleteAsk]?.heading || 'ohne Überschrift' }}</strong>
        löschen? Der Text dieses Abschnitts wird entfernt. Rückgängig stellt ihn wieder her.
      </p>
      <button type="button" @click="confirmDeleteSection">Löschen</button>
      <button type="button" @click="deleteAsk = null">Abbrechen</button>
    </dialog>
  </div>
</template>
<style src="./offer-document.css"></style>
