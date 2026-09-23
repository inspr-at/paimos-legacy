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
  type SectionEditorMemory,
} from './offerProse'
import type { OfferTextNode } from './types'
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
const emit = defineEmits<{ overflow: [message: string]; change: [] }>()
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
  props.offer.document.positions.splice(i, 1)
  emit('change')
}
function move(i: number, direction: number) {
  const j = i + direction
  if (j < 0 || j >= props.offer.document.positions.length) return
  const p = props.offer.document.positions.splice(i, 1)[0]!
  props.offer.document.positions.splice(j, 0, p)
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
function sectionMemory(block: object | undefined): SectionEditorMemory | null {
  if (!block) return null
  const known = sectionMemories.get(block)
  if (known) return known
  const memory = { history: createProseHistory(), caret: null }
  sectionMemories.set(block, memory)
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
  blocks.splice(at, 0, { heading: '', body: '' })
  activeBlock.value = at
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
  const [block] = blocks.splice(index, 1)
  if (!block) return
  blocks.splice(next, 0, block)
  activeBlock.value = next
  emit('change')
  focusSection(next, activeField.value)
}
const footerShift = computed(() => {
  const footer = props.offer.document.footer
  if (!footer) return undefined
  const scale = footer.logo_width_mm / OFFER_FOOTER_LOGO.defaultWidthMm
  return {
    '--mark-offset': `${footer.logo_offset_mm}mm`,
    '--mark-font': `${7.5 * scale}pt`,
  }
})
defineExpose({ paginate })
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
          <OfferProse :body="block.body" :nodes="block.nodes" />
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
      <div v-if="editable" class="section-tools" data-offer-chrome>
        <button
          type="button"
          :disabled="!canAdd"
          aria-label="Abschnitt hinzufügen"
          @mousedown.prevent
          @click="addSection"
        >
          Abschnitt hinzufügen
        </button>
        <button
          type="button"
          :disabled="!canUp"
          aria-label="Abschnitt nach oben"
          @mousedown.prevent
          @click="moveSection(-1)"
        >
          Nach oben
        </button>
        <button
          type="button"
          :disabled="!canDown"
          aria-label="Abschnitt nach unten"
          @mousedown.prevent
          @click="moveSection(1)"
        >
          Nach unten
        </button>
      </div>
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
              /><OfferProse
                :body="offer.document.blocks[i]!.body"
                :nodes="offer.document.blocks[i]!.nodes"
                :memory="sectionMemory(offer.document.blocks[i])"
                :editable="editable"
                :label="`Textbaustein ${i + 1}`"
                @update="applyBody(i, $event)"
              />
            </div>
          </div>
          <OfferTable
            v-if="page.positions.length"
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
          /><span
            v-else
            class="footmark"
            :class="{ 'is-set': !!offer.document.footer }"
            :style="footerShift"
            ><span class="lockup">{{ offer.document.sender.company }}</span></span
          ><span class="right">SEITE {{ index + 1 }} VON {{ pages.length }}</span>
        </div>
      </section>
    </div>
  </div>
</template>
<style src="./offer-document.css"></style>
