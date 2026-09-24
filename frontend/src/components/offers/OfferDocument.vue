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
import { date, type Offer, type OfferDocument as OfferDoc } from './types'
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
function safePages(source: Page[]): Page[] {
  const blockCount = props.offer.document.blocks.length
  const positionCount = props.offer.document.positions.length
  const next = source.map((page) => ({
    ...page,
    blocks: page.blocks.filter((index) => index >= 0 && index < blockCount),
    positions: page.positions.filter((index) => index >= 0 && index < positionCount),
  }))
  const seenBlocks = new Set(next.flatMap((page) => page.blocks))
  const missingBlocks: number[] = []
  for (let index = 0; index < blockCount; index++) {
    if (!seenBlocks.has(index)) missingBlocks.push(index)
  }
  if (missingBlocks.length) {
    let host = [...next]
      .reverse()
      .find((page) => page.blocks.length > 0 || page.heading === 'terms')
    if (!host) {
      host = { kind: 'terms', heading: 'terms', blocks: [], positions: [], acceptance: false }
      const cover = next.findIndex((page) => page.kind === 'cover')
      next.splice(cover >= 0 ? cover + 1 : 0, 0, host)
    }
    host.blocks.push(...missingBlocks)
    host.blocks.sort((a, b) => a - b)
  }
  const seenPositions = new Set(next.flatMap((page) => page.positions))
  const missingPositions: number[] = []
  for (let index = 0; index < positionCount; index++) {
    if (!seenPositions.has(index)) missingPositions.push(index)
  }
  if (missingPositions.length) {
    let host = [...next]
      .reverse()
      .find((page) => page.positions.length > 0 || page.heading === 'positions' || page.acceptance)
    if (!host) {
      host = {
        kind: 'positions',
        heading: 'positions',
        blocks: [],
        positions: [],
        acceptance: true,
      }
      next.push(host)
    }
    host.positions.push(...missingPositions)
    host.positions.sort((a, b) => a - b)
  }
  return next
}
const renderedPages = computed(() => safePages(pages.value))
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
type DocSnap = {
  data: OfferDoc
  blocks: OfferBlock[]
  positions: OfferPosition[]
}
type DocUndo =
  | { type: 'prose'; block: OfferBlock }
  | { type: 'document'; before: DocSnap; after: DocSnap }
const undoStack: DocUndo[] = []
const redoStack: DocUndo[] = []
const historyVersion = ref(0)
const activeKind = ref<OfferSelection['kind']>('none')
const activePosition = ref<number | null>(null)
const deleteAsk = ref<number | null>(null)
const canUndo = computed(
  () => !!props.editable && historyVersion.value >= 0 && undoStack.length > 0,
)
const canRedo = computed(
  () => !!props.editable && historyVersion.value >= 0 && redoStack.length > 0,
)
function markHistory() {
  historyVersion.value++
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
  applying = true
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
  applying = false
  baseline = capture()
}
function undoDocument() {
  if (!props.editable) return
  commitPending()
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
  } else applySnap(entry.before)
  markHistory()
  emit('change')
}
function redoDocument() {
  if (!props.editable) return
  commitPending()
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
  } else applySnap(entry.after)
  markHistory()
  emit('change')
}
function withBlocks(mutate: () => void) {
  mutate()
}
function withPositions(mutate: () => void) {
  mutate()
}
function jsonClone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}
function capture(): DocSnap {
  const doc = props.offer.document
  return { data: jsonClone(doc), blocks: doc.blocks.slice(), positions: doc.positions.slice() }
}
function withoutProse(doc: OfferDoc): string {
  const clone = jsonClone(doc)
  for (const block of clone.blocks) {
    block.body = ''
    delete block.nodes
  }
  return JSON.stringify(clone)
}
function sameOrder(before: object[], after: object[]): boolean {
  return before.length === after.length && before.every((item, index) => item === after[index])
}
let epoch = 0
let applying = false
let replacing = false
let scheduled = false
let pendingBefore: DocSnap | null = null
let baseline = capture()
function commitPending() {
  if (!pendingBefore || applying || replacing) return
  const before = pendingBefore
  pendingBefore = null
  scheduled = false
  epoch++
  const after = capture()
  if (!props.editable) {
    baseline = after
    return
  }
  const moved =
    !sameOrder(before.blocks, after.blocks) || !sameOrder(before.positions, after.positions)
  if (!moved && JSON.stringify(before.data) === JSON.stringify(after.data)) return
  if (!moved && withoutProse(before.data) === withoutProse(after.data)) {
    baseline = after
    return
  }
  undoStack.push({ type: 'document', before, after })
  redoStack.length = 0
  baseline = after
  markHistory()
}
function noteSoon() {
  if (applying || replacing) return
  if (!pendingBefore) pendingBefore = baseline
  if (scheduled) return
  scheduled = true
  const token = epoch
  queueMicrotask(() => {
    if (token !== epoch) return
    commitPending()
  })
}
function applySnap(snap: DocSnap) {
  applying = true
  const doc = props.offer.document
  const data = snap.data
  doc.title = data.title
  doc.subtitle = data.subtitle
  doc.project_ref = data.project_ref
  doc.offer_date = data.offer_date
  doc.valid_until = data.valid_until
  doc.intro = data.intro
  doc.accept_text = data.accept_text
  doc.vat_note = data.vat_note
  doc.net_total_cents = data.net_total_cents
  doc.sender = jsonClone(data.sender)
  doc.customer = jsonClone(data.customer)
  if (data.footer) doc.footer = jsonClone(data.footer)
  else delete doc.footer
  snap.blocks.forEach((block, index) => {
    const src = data.blocks[index]
    if (!src) return
    block.heading = src.heading
    block.body = src.body
    if (src.nodes) block.nodes = jsonClone(src.nodes)
    else delete block.nodes
  })
  doc.blocks.splice(0, doc.blocks.length, ...snap.blocks)
  snap.positions.forEach((row, index) => {
    const src = data.positions[index]
    if (!src) return
    row.short_text = src.short_text
    row.long_text = src.long_text
    row.quantity = src.quantity
    row.unit = src.unit
    row.unit_price_cents = src.unit_price_cents
    row.total_cents = src.total_cents
  })
  doc.positions.splice(0, doc.positions.length, ...snap.positions)
  applying = false
  baseline = capture()
  if (activeBlock.value != null && !doc.blocks[activeBlock.value]) {
    activeBlock.value = doc.blocks.length
      ? Math.min(activeBlock.value, doc.blocks.length - 1)
      : null
    if (activeBlock.value == null) activeKind.value = 'none'
  }
  if (activePosition.value != null && !doc.positions[activePosition.value]) {
    activePosition.value = null
    if (activeKind.value === 'position') activeKind.value = 'none'
  }
}
function resetHistory() {
  epoch++
  replacing = true
  pendingBefore = null
  scheduled = false
  undoStack.length = 0
  redoStack.length = 0
  baseline = capture()
  markHistory()
  activeKind.value = 'none'
  activeBlock.value = null
  activePosition.value = null
  deleteAsk.value = null
  emit('select', { kind: 'none' })
  replacing = false
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
  const inSection = activeKind.value === 'heading' || activeKind.value === 'text'
  const at =
    !inSection || activeBlock.value == null
      ? blocks.length
      : Math.min(blocks.length, activeBlock.value + 1)
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
function activatedFooter(source?: Event | HTMLElement | null): HTMLElement | null {
  if (!sheetEl.value) return null
  const node =
    source instanceof Event
      ? source.currentTarget instanceof Element
        ? source.currentTarget
        : source.target instanceof Element
          ? source.target
          : null
      : source instanceof Element
        ? source
        : null
  if (!(node instanceof Element) || !sheetEl.value.contains(node)) return null
  return node.closest<HTMLElement>('[aria-label="Fußzeilenlogo auswählen"]')
}
function selectFooter(source?: Event | HTMLElement | null) {
  if (!props.editable) return
  activeKind.value = 'footer'
  const mark =
    activatedFooter(source) ??
    sheetEl.value?.querySelector<HTMLElement>('.ftr [aria-label="Fußzeilenlogo auswählen"]')
  mark?.focus({ preventScroll: true })
}
function onFooterKey(event: KeyboardEvent) {
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  selectFooter(event)
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
      : {
          kind: 'text',
          index: activeBlock.value,
          count: blocks.length,
          heading: blocks[activeBlock.value]!.heading,
        }
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
watch(
  () => props.offer.document,
  (doc, prev) => {
    if (prev && doc !== prev) {
      resetHistory()
      return
    }
    noteSoon()
  },
  { deep: true, flush: 'sync' },
)
defineExpose({
  paginate,
  undo: undoDocument,
  redo: redoDocument,
  canUndo,
  canRedo,
  resetHistory,
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
      <div class="sheet-frame">
        <section
          v-for="(page, index) in renderedPages"
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
              <template v-for="i in page.blocks" :key="sectionKey(offer.document.blocks[i])">
                <div
                  v-if="offer.document.blocks[i]"
                  class="sec"
                  :data-section="i"
                  :data-selected="
                    activeBlock === i && (activeKind === 'heading' || activeKind === 'text')
                      ? activeKind
                      : undefined
                  "
                >
                  <span class="n">{{ i + 1 }}</span
                  ><OfferText
                    v-model="offer.document.blocks[i]!.heading"
                    tag="h3"
                    :editable="editable"
                    :label="`Überschrift Textbaustein ${i + 1}`"
                  /><OfferProse
                    :body="offer.document.blocks[i]!.body"
                    :nodes="offer.document.blocks[i]!.nodes"
                    :memory="sectionMemory(offer.document.blocks[i])"
                    :section-number="i + 1"
                    :editable="editable"
                    :label="`Textbaustein ${i + 1}`"
                    @update="applyBody(i, $event)"
                  />
                </div>
              </template>
            </div>
            <OfferTable
              v-if="page.positions.length"
              @focusin="onPositionFocus"
              :positions="offer.document.positions"
              :indices="page.positions"
              :editable="editable"
              :selected="activeKind === 'position' ? activePosition : null"
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
              :class="{ 'is-selected': activeKind === 'footer' }"
              :layout="offer.document.footer"
              :tabindex="editable ? 0 : undefined"
              :role="editable ? 'button' : undefined"
              :aria-label="editable ? 'Fußzeilenlogo auswählen' : undefined"
              @click="editable && selectFooter($event)"
              @keydown="onFooterKey"
            /><span
              v-else
              class="footmark"
              :class="{ 'is-set': !!offer.document.footer, 'is-selected': activeKind === 'footer' }"
              :style="footerShift"
              :tabindex="editable && !offer.document.footer ? 0 : undefined"
              :role="editable && !offer.document.footer ? 'button' : undefined"
              :aria-label="
                editable && !offer.document.footer ? 'Fußzeilenlogo auswählen' : undefined
              "
              @click="editable && !offer.document.footer && selectFooter($event)"
              @keydown="!offer.document.footer && onFooterKey($event)"
              ><span
                class="lockup"
                :class="{ 'is-selected': activeKind === 'footer' && !!offer.document.footer }"
                :tabindex="editable && offer.document.footer ? 0 : undefined"
                :role="editable && offer.document.footer ? 'button' : undefined"
                :aria-label="
                  editable && offer.document.footer ? 'Fußzeilenlogo auswählen' : undefined
                "
                @click="editable && offer.document.footer && selectFooter($event)"
                @keydown="offer.document.footer && onFooterKey($event)"
                >{{ offer.document.sender.company }}</span
              ></span
            ><span class="right">SEITE {{ index + 1 }} VON {{ renderedPages.length }}</span>
          </div>
        </section>
      </div>
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
