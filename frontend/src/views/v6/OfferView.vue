<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { ensureUTC } from '@/composables/useDateFormat'
import { OFFER_CHROME_KEY } from '@/composables/useOfferChrome'
import { publicURL } from '@/publicPath'
import OfferTitleBar from '@/components/offers/OfferTitleBar.vue'
import {
  offerToolbarActions,
  type OfferToolbarActionId,
} from '@/components/offers/offerToolbarActions'
import { provideOfferProseSession } from '@/components/offers/offerProseSession'
import {
  offerStatus,
  receiptTime,
  validOfferEmail,
  type OfferFooterLayout,
  type OfferSelection,
} from '@/components/offers/types'
import { api, errMsg, ApiError } from '@/api/client'
import { crmEnabled, instanceHostname, loadInstance } from '@/api/instance'
import { useAuthStore } from '@/stores/auth'
import OfferConfirmationStatus from '@/components/offers/OfferConfirmationStatus.vue'
import OfferDocument from '@/components/offers/OfferDocument.vue'
import OfferSettingsDialog from '@/components/offers/OfferSettingsDialog.vue'
import type { Offer } from '@/components/offers/types'
const route = useRoute(),
  router = useRouter(),
  auth = useAuthStore()
const offer = ref<Offer>()
const collapsed = inject(OFFER_CHROME_KEY, ref(true))
provideOfferProseSession()
const toolbar = ref<{ root?: HTMLElement } | null>(null)
function toolbarElement(): HTMLElement | undefined {
  return toolbar.value?.root
}
const viewport = ref<HTMLElement>()
const zoomMode = ref('width')
const zoom = ref(1)
function fitZoom() {
  const width = (viewport.value?.clientWidth ?? window.innerWidth) - 32
  const height = window.innerHeight - (toolbarElement()?.getBoundingClientRect().bottom ?? 0) - 32
  zoom.value =
    zoomMode.value === 'width'
      ? Math.max(0.1, width / ((210 * 96) / 25.4))
      : zoomMode.value === 'page'
        ? Math.max(0.1, Math.min(width / ((210 * 96) / 25.4), height / ((297 * 96) / 25.4)))
        : Number(zoomMode.value) / 100
}
watch([zoomMode, collapsed], async () => {
  await nextTick()
  fitZoom()
})
let resizeObserver: ResizeObserver | undefined
const saveFeedback = ref(false)
const saveFailed = ref(false)
let feedbackTimer: ReturnType<typeof setTimeout> | undefined
const busy = computed(() => saving.value || saveFeedback.value)
const savedAt = computed(() => {
  const raw = offer.value?.updated_at
  if (!raw) return ''
  const date = new Date(ensureUTC(raw))
  if (!Number.isFinite(date.getTime())) return ''
  const today = date.toLocaleDateString('de-AT') === new Date().toLocaleDateString('de-AT')
  return `${today ? 'Heute' : date.toLocaleDateString('de-AT')}, ${date.toLocaleTimeString('de-AT')}`
})
const error = ref(''),
  overflow = ref(''),
  saving = ref(false),
  dirty = ref(false),
  conflict = ref(false),
  settingsOpen = ref(false),
  finalizeOpen = ref(false)
const loading = ref(true),
  finalizing = ref(false)
const renderer = ref<InstanceType<typeof OfferDocument>>()
const finalizeDialog = ref<HTMLDialogElement>()
const deleteDialog = ref<HTMLDialogElement>()
const selection = ref<OfferSelection>({ kind: 'none' })
let deleteTrigger: HTMLElement | null = null
watch(loading, async () => {
  await nextTick()
  if (toolbarElement()) resizeObserver?.observe(toolbarElement()!)
  fitZoom()
})
watch(finalizeOpen, async (open) => {
  await nextTick()
  if (open) finalizeDialog.value?.showModal()
  else finalizeDialog.value?.close()
})
const publicUrl = computed(() =>
  offer.value?.public_token
    ? new URL(publicURL(`/offers/${offer.value.public_token}`), window.location.origin).href
    : '',
)
const copied = ref(false)
async function copyLink() {
  if (!offer.value) return
  try {
    if (!publicUrl.value) offer.value = await api.post<Offer>(`/offers/${offer.value.id}/link`, {})
    await navigator.clipboard.writeText(publicUrl.value)
    copied.value = true
  } catch (e) {
    error.value = errMsg(e)
  }
}
const printMode = computed(() => route.path.endsWith('/print'))
const editable = computed(
  () =>
    !!offer.value &&
    offer.value.status === 'draft' &&
    auth.isAdmin &&
    !printMode.value &&
    !conflict.value &&
    !finalizing.value,
)
let timer: ReturnType<typeof setTimeout> | undefined
let savedDocument = ''
let pending: Promise<boolean> | undefined
const state = computed(() =>
  loading.value
    ? 'Lädt …'
    : !offer.value
      ? 'Nicht geladen'
      : conflict.value
        ? 'Speicherkonflikt'
        : busy.value
          ? 'Speichert …'
          : saveFailed.value
            ? 'Nicht gespeichert'
            : dirty.value
              ? 'Ungespeichert'
              : offer.value?.status !== 'draft' && offer.value
                ? offerStatus(offer.value.status)
                : 'Gespeichert',
)
async function load() {
  loading.value = true
  error.value = ''
  try {
    await loadInstance()
    offer.value = await api.get<Offer>(`/offers/${route.params.id}`)
    savedDocument = JSON.stringify(offer.value.document)
    dirty.value = false
    conflict.value = false
    saveFailed.value = false
    document.title = `${offer.value.offer_no} · Angebot`
    await nextTick()
    renderer.value?.resetHistory()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}
watch(
  () => offer.value?.document,
  () => {
    if (!editable.value || !offer.value) return
    dirty.value = JSON.stringify(offer.value.document) !== savedDocument
    if (timer) clearTimeout(timer)
    if (dirty.value) timer = setTimeout(() => void save(), 700)
  },
  { deep: true },
)
async function save(force = false): Promise<boolean> {
  if (document.querySelector('.offer-document .sheet input:invalid')) {
    saveFailed.value = true
    error.value = 'Bitte ungültige Zahlen korrigieren.'
    return false
  }
  if (pending) {
    const ok = await pending
    return force && ok ? save(true) : ok
  }
  if (!offer.value || (!dirty.value && !force)) return true
  if (force && !editable.value) return false
  if (conflict.value) return false
  if (timer) clearTimeout(timer)
  pending = (async () => {
    saving.value = true
    saveFailed.value = false
    saveFeedback.value = true
    if (feedbackTimer) clearTimeout(feedbackTimer)
    const started = Date.now()
    error.value = ''
    try {
      let forceWrite = force
      while (
        offer.value &&
        (forceWrite || JSON.stringify(offer.value.document) !== savedDocument)
      ) {
        forceWrite = false
        const snapshot = JSON.stringify(offer.value.document)
        const result = await api.put<Offer>(`/offers/${offer.value.id}`, {
          revision: offer.value.revision,
          document: JSON.parse(snapshot),
        })
        offer.value.revision = result.revision
        offer.value.updated_at = result.updated_at
        savedDocument = snapshot
      }
      dirty.value = false
      return true
    } catch (e) {
      saveFailed.value = true
      error.value = errMsg(e)
      if (e instanceof ApiError && e.status === 409) conflict.value = true
      return false
    } finally {
      saving.value = false
      pending = undefined
      feedbackTimer = setTimeout(
        () => {
          saveFeedback.value = false
        },
        Math.max(0, 1000 - (Date.now() - started)),
      )
    }
  })()
  return pending
}
const deleteOpen = ref(false),
  deleting = ref(false)
watch(deleteOpen, async (open) => {
  await nextTick()
  if (open) deleteDialog.value?.showModal()
  else {
    deleteDialog.value?.close()
    const trigger = deleteTrigger
    deleteTrigger = null
    if (trigger?.isConnected) trigger.focus()
    else document.querySelector<HTMLElement>('[aria-label="Weitere Aktionen"]')?.focus()
  }
})
async function setDeleted() {
  if (!offer.value || deleting.value || !(await save())) return
  deleting.value = true
  try {
    const result = await api.put<{ deleted: boolean }>(`/offers/${offer.value.id}/deleted`, {
      deleted: !offer.value.deleted,
    })
    offer.value.deleted = result.deleted
    deleteOpen.value = false
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    deleting.value = false
  }
}
async function refreshConfirmation() {
  if (offer.value?.status !== 'accepted') return
  try {
    const updated = await api.get<Offer>(`/offers/${offer.value.id}`)
    offer.value.confirmation = updated.confirmation
    offer.value.document_sha256 = updated.document_sha256
  } catch {
    /* Keep receipt visible. */
  }
}
async function finalize() {
  if (!offer.value || !(await save())) return
  await renderer.value?.paginate()
  if (overflow.value) return
  saving.value = true
  finalizing.value = true
  error.value = ''
  try {
    const result = await api.put<Offer>(`/offers/${offer.value.id}`, {
      revision: offer.value.revision,
      document: offer.value.document,
      finalize: true,
    })
    savedDocument = JSON.stringify(result.document)
    offer.value = result
    dirty.value = false
    finalizeOpen.value = false
    await nextTick()
    renderer.value?.resetHistory()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
    finalizing.value = false
  }
}
async function printOffer() {
  if (!(await save())) return
  await document.fonts.ready
  await renderer.value?.paginate()
  await nextTick()
  if (overflow.value || document.querySelector('.offer-document .sheet input:invalid')) {
    error.value = overflow.value || 'Bitte ungültige Zahlen korrigieren.'
    return
  }
  if (printMode.value) window.print()
  else await router.push(`/crm/offers/${offer.value!.id}/print`)
}
async function duplicate() {
  if (!offer.value || !(await save())) return
  saving.value = true
  error.value = ''
  try {
    const copy = await api.post<Offer>('/offers', {
      customer_id: offer.value.customer_id,
      duplicate_id: offer.value.id,
    })
    await router.push(`/crm/offers/${copy.id}`)
    await load()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}
function downloadDraft() {
  if (!offer.value) return
  const blob = new Blob([JSON.stringify(offer.value.document, null, 2)], {
    type: 'application/json',
  })
  const url = URL.createObjectURL(blob),
    link = document.createElement('a')
  link.href = url
  link.download = `${offer.value.offer_no}-entwurf.json`
  link.click()
  URL.revokeObjectURL(url)
}
const toolbarActions = computed(() =>
  offerToolbarActions({
    hasOffer: !!offer.value,
    status: offer.value?.status ?? '',
    printMode: printMode.value,
    isAdmin: auth.isAdmin,
    editable: editable.value,
    saving: saving.value,
    overflow: !!overflow.value,
    copied: copied.value,
    collapsed: collapsed.value,
    linkAvailable: !!publicUrl.value || auth.isAdmin,
    deleted: !!offer.value?.deleted,
    deleting: deleting.value,
  }),
)
function onToolbarAction(id: OfferToolbarActionId) {
  if (id === 'link') void copyLink()
  else if (id === 'settings') settingsOpen.value = true
  else if (id === 'position') addPosition()
  else if (id === 'duplicate') void duplicate()
  else if (id === 'finalize') finalizeOpen.value = true
  else if (id === 'chrome') collapsed.value = !collapsed.value
  else if (id === 'delete') {
    if (offer.value?.deleted) void setDeleted()
    else {
      deleteTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
      deleteOpen.value = true
    }
  } else if (id === 'layout') renderer.value?.selectFooter()
}
function onFooter(value: OfferFooterLayout) {
  if (!editable.value || !offer.value) return
  offer.value.document.footer = value
}
function addPosition() {
  offer.value?.document.positions.push({
    short_text: '',
    long_text: '',
    quantity: 1,
    unit: 'Std.',
    unit_price_cents: 0,
    total_cents: 0,
  })
}
function beforeUnload(e: BeforeUnloadEvent) {
  if (dirty.value || saving.value) {
    e.preventDefault()
    e.returnValue = ''
  }
}
onMounted(() => {
  void load()
  window.addEventListener('beforeunload', beforeUnload)
  window.addEventListener('resize', fitZoom)
  resizeObserver = new ResizeObserver(fitZoom)
  if (viewport.value) resizeObserver.observe(viewport.value)
  if (toolbarElement()) resizeObserver.observe(toolbarElement()!)
  fitZoom()
})
const confirmationTimer = setInterval(() => {
  if (
    offer.value?.status === 'accepted' &&
    ['pending', 'rendering', 'sending'].includes(offer.value.confirmation?.state || '')
  )
    void refreshConfirmation()
}, 5000)
onBeforeUnmount(() => {
  clearInterval(confirmationTimer)
  if (timer) clearTimeout(timer)
  window.removeEventListener('beforeunload', beforeUnload)
  window.removeEventListener('resize', fitZoom)
  resizeObserver?.disconnect()
  if (feedbackTimer) clearTimeout(feedbackTimer)
})
onBeforeRouteLeave(async () => !dirty.value || (await save()))
</script>
<template>
  <main ref="viewport" class="offer-view" :class="{ 'is-collapsed': collapsed }">
    <template v-if="crmEnabled">
      <OfferTitleBar
        ref="toolbar"
        :back-to="
          printMode
            ? `/crm/offers/${route.params.id}`
            : offer
              ? `/crm/${offer.customer_id}`
              : '/crm'
        "
        :back-label="printMode ? 'Zum Angebot' : 'Zum Kunden'"
        :offer-no="offer?.offer_no || 'Angebot'"
        :hostname="instanceHostname"
        :editable="editable"
        :busy="busy"
        :loading="loading"
        :dirty="dirty"
        :save-failed="saveFailed"
        :state="state"
        :saved-at="savedAt"
        :saved-at-iso="offer?.updated_at || ''"
        v-model:zoom-mode="zoomMode"
        :zoom="zoom"
        :print-disabled="saving || !!overflow || conflict"
        :show-print="!!offer"
        :actions="toolbarActions"
        :footer="offer?.document.footer ?? null"
        :can-undo="!!renderer?.canUndo"
        :can-redo="!!renderer?.canRedo"
        :selection="selection"
        @save="save(true)"
        @print="printOffer"
        @action="onToolbarAction"
        @footer="onFooter"
        @undo="renderer?.undo()"
        @redo="renderer?.redo()"
        @insert-section="renderer?.addSection()"
        @insert-position="addPosition()"
        @select-footer="renderer?.selectFooter()"
        @section-add="renderer?.addSection()"
        @section-up="renderer?.moveSection(-1)"
        @section-down="renderer?.moveSection(1)"
        @section-delete="renderer?.askDeleteSection()"
        @position-up="selection.kind === 'position' && renderer?.move(selection.index, -1)"
        @position-down="selection.kind === 'position' && renderer?.move(selection.index, 1)"
        @position-delete="selection.kind === 'position' && renderer?.remove(selection.index)"
      />
      <p v-if="loading" class="offer-notice">Angebot wird geladen …</p>
      <p v-if="error || overflow" role="alert" class="offer-notice offer-error">
        {{ error || overflow }}
        <button v-if="dirty && !conflict" class="btn" @click="save()">Erneut speichern</button>
      </p>
      <p v-if="editable && !collapsed" class="offer-notice">
        Klicke in einen Text, um ihn zu bearbeiten. Änderungen werden automatisch gespeichert.
        Kundenlink und QR-Code werden beim Finalisieren erstellt.
      </p>
      <p v-else-if="offer?.status === 'sent' && !printMode" class="offer-notice">
        Finalisiert am {{ offer.sent_at?.slice(0, 10) }}. Zum Ändern ein neues Angebot duplizieren.
      </p>
      <p v-if="offer?.status === 'accepted'" class="offer-notice" role="status">
        Angenommen von {{ offer.accepted_name }} · {{ offer.accepted_company }} ·
        {{ receiptTime(offer.accepted_at) }}<br v-if="offer.accepted_note" />{{
          offer.accepted_note
        }}
      </p>
      <p v-if="offer?.status === 'expired'" class="offer-notice">
        Die Bindefrist ist abgelaufen. Der Kundenlink zeigt das Angebot ohne Annahmeformular.
      </p>
      <div v-if="conflict" class="offer-notice" role="alert">
        Deine Änderungen sind noch in diesem Fenster. Du kannst sie sichern oder den aktuellen
        Serverstand laden.
        <button type="button" class="btn" @click="downloadDraft">
          Lokalen Entwurf herunterladen
        </button>
        <button type="button" class="btn" @click="load">
          Serverstand laden und lokale Änderungen verwerfen
        </button>
      </div>
      <dialog
        ref="deleteDialog"
        class="finalize-dialog"
        aria-label="Angebot ausblenden"
        @cancel.prevent="deleteOpen = false"
      >
        <h2>{{ offer?.offer_no }}</h2>
        <p>
          {{ offer?.offer_no }} aus den Übersichten ausblenden? Inhalte, Nachweise und Kundenlinks
          bleiben erhalten.
          {{
            offer?.status === 'draft'
              ? 'Das Angebot wird als gelöscht markiert.'
              : 'Das Angebot wird archiviert.'
          }}
        </p>
        <button class="btn" type="button" :disabled="deleting" @click="setDeleted">
          {{ offer?.status === 'draft' ? 'Als gelöscht markieren' : 'Archivieren' }}
        </button>
        <button class="btn" type="button" @click="deleteOpen = false">Abbrechen</button>
      </dialog>
      <OfferConfirmationStatus
        v-if="offer?.status === 'accepted'"
        :offer-id="offer.id"
        :confirmation="offer.confirmation"
        :admin="auth.isAdmin"
        @refresh="refreshConfirmation"
      />
      <OfferDocument
        v-if="offer"
        ref="renderer"
        :key="`${offer.id}-${printMode}`"
        :offer="offer"
        :zoom="zoom"
        :public-url="publicUrl"
        :qr-preview="offer.status === 'draft'"
        :editable="editable"
        @overflow="overflow = $event"
        @select="selection = $event"
      />
      <OfferSettingsDialog :open="settingsOpen" @close="settingsOpen = false" />
      <dialog
        ref="finalizeDialog"
        class="finalize-dialog"
        aria-label="Angebot finalisieren"
        @cancel.prevent="finalizeOpen = false"
      >
        <h2>Angebot finalisieren</h2>
        <p>
          Absender, Kundenanschrift, Texte und Preise werden festgeschrieben. Es wird keine E-Mail
          gesendet. Danach kannst du das Angebot per Kundenlink, QR-Code oder PDF weitergeben. Der
          QR-Code erscheint dann auch im Dokument und in der PDF. Wer den Kundenlink besitzt, kann
          das Angebot ansehen und bis zum Ablaufdatum annehmen. Eine E-Mail wird dabei nicht
          verschickt.
        </p>
        <p>
          Kundenkontakt:
          {{
            offer?.document.customer.email ||
            'E-Mail fehlt – bitte im Kundenkontakt ergänzen und neu laden.'
          }}<br />Absender: {{ offer?.document.sender.email || 'E-Mail fehlt' }}
        </p>
        <p>Nach der Annahme erhalten beide Adressen eine gemeinsame Bestätigung mit PDF.</p>
        <p v-if="error" role="alert">{{ error }}</p>
        <button
          class="btn btn-primary"
          :disabled="
            saving ||
            !validOfferEmail(offer?.document.customer.email) ||
            !validOfferEmail(offer?.document.sender.email)
          "
          @click="finalize"
        >
          Jetzt finalisieren</button
        ><button type="button" class="btn" @click="finalizeOpen = false">Abbrechen</button>
      </dialog>
    </template>
    <p v-else class="offer-notice">CRM ist auf dieser Instanz deaktiviert.</p>
  </main>
</template>
<style scoped>
.offer-visibility {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding: 4px 12px;
  font-size: 12px;
}
.offer-visibility button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
@media print {
  .offer-visibility {
    display: none;
  }
}
.finalize-dialog {
  color: #203c3d;
  background: #fffefa;
  border: 1px solid #dfe6e5;
  border-radius: 12px;
  padding: 24px;
  width: min(500px, calc(100vw - 32px));
}
.finalize-dialog::backdrop {
  background: #10232788;
}
.finalize-dialog h2 {
  font-size: 18px;
  margin: 0 0 12px;
}
.finalize-dialog p {
  line-height: 1.5;
}

.offer-view {
  min-width: 0;
}
.offer-notice {
  padding: 8px 20px;
  margin: 0;
  font-size: 13px;
}
.offer-error {
  color: var(--danger, #b42318);
}
</style>
