<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { ensureUTC } from '@/composables/useDateFormat'
import { OFFER_CHROME_KEY } from '@/composables/useOfferChrome'
import { publicURL } from '@/publicPath'
import OfferInspector from '@/components/offers/OfferInspector.vue'
import OfferTitleBar from '@/components/offers/OfferTitleBar.vue'
import {
  offerToolbarActions,
  type OfferToolbarActionId,
} from '@/components/offers/offerToolbarActions'
import { OFFER_PROSE_WRITER_VERSION } from '@/components/offers/offerProse'
import {
  beginSave,
  createRemoteWatch,
  finishSave,
  noteOwnRevision,
  observeRemote,
  pollDelayMs,
  reloadChoice,
  remoteIsNewer,
  shouldCheckRemote,
  type RemoteWatch,
} from '@/components/offers/offerExternalChange'
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
const inspectorOpen = ref(true)
const zoomMode = ref('width')
const zoom = ref(1)
function fitZoom() {
  const width = (viewport.value?.clientWidth ?? window.innerWidth) - 32
  const stageHeight = viewport.value?.clientHeight ?? 0
  const height =
    stageHeight > 80
      ? stageHeight - 32
      : window.innerHeight - (toolbarElement()?.getBoundingClientRect().bottom ?? 0) - 32
  zoom.value =
    zoomMode.value === 'width'
      ? Math.max(0.1, width / ((210 * 96) / 25.4))
      : zoomMode.value === 'page'
        ? Math.max(0.1, Math.min(width / ((210 * 96) / 25.4), height / ((297 * 96) / 25.4)))
        : Number(zoomMode.value) / 100
}
watch([zoomMode, collapsed, inspectorOpen], async () => {
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
const remoteWatch = ref<RemoteWatch | null>(null)
const remoteNewer = ref(false)
const remoteConfirm = ref(false)
const remoteDialog = ref<HTMLDialogElement>()
let pollGeneration = 0
let pollTimer: ReturnType<typeof setTimeout> | undefined
let pollInFlight = false
let pollQueued = false
let unchangedPolls = 0
let checkSerial = 0
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
    !finalizing.value &&
    !loading.value,
)
let timer: ReturnType<typeof setTimeout> | undefined
let savedDocument = ''
let pending: Promise<boolean> | undefined
let documentGeneration = 0
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
function stopRemotePoll() {
  pollGeneration += 1
  pollQueued = false
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = undefined
}
function scheduleRemotePoll() {
  if (pollTimer) clearTimeout(pollTimer)
  const generation = pollGeneration
  pollTimer = setTimeout(() => {
    if (generation === pollGeneration) void checkRemoteRevision()
  }, pollDelayMs(unchangedPolls))
}
async function checkRemoteRevision() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = undefined
  const generation = pollGeneration
  const current = offer.value
  if (
    !shouldCheckRemote({
      hidden: document.visibilityState === 'hidden',
      inFlight: pollInFlight,
      loading: loading.value,
      finalizing: finalizing.value,
      offerId: current ? String(current.id) : '',
    })
  ) {
    if (pollInFlight) pollQueued = true
    if (generation === pollGeneration && !pollInFlight) scheduleRemotePoll()
    return
  }
  pollInFlight = true
  const offerId = String(current!.id)
  const checkId = ++checkSerial
  try {
    const fresh = await api.get<Offer>(`/offers/${offerId}`)
    if (generation !== pollGeneration || !offer.value || String(offer.value.id) !== offerId) return
    const watched = remoteWatch.value ?? createRemoteWatch(offerId, offer.value.revision)
    remoteWatch.value = observeRemote(watched, {
      offerId,
      revision: fresh.revision,
      checkId,
    })
    remoteNewer.value = remoteIsNewer(remoteWatch.value)
    unchangedPolls = remoteNewer.value ? 0 : unchangedPolls + 1
  } catch {
    unchangedPolls = Math.min(unchangedPolls + 1, 4)
  } finally {
    pollInFlight = false
    if (generation !== pollGeneration) return
    if (pollQueued) {
      pollQueued = false
      void checkRemoteRevision()
      return
    }
    scheduleRemotePoll()
  }
}
function onRemoteVisibility() {
  if (document.visibilityState === 'hidden') {
    if (pollTimer) clearTimeout(pollTimer)
    pollTimer = undefined
    return
  }
  unchangedPolls = 0
  void checkRemoteRevision()
}
function onRemoteFocus() {
  if (document.visibilityState === 'hidden') return
  unchangedPolls = 0
  void checkRemoteRevision()
}
function requestRemoteReload() {
  const choice = reloadChoice({
    external: remoteNewer.value,
    dirty: dirty.value,
    saving: saving.value,
  })
  if (choice === 'direct') void load()
  else if (choice === 'confirm') remoteConfirm.value = true
}
async function load() {
  const ownedDocument = ++documentGeneration
  const routeId = String(route.params.id ?? '')
  const baseline = offer.value ? JSON.stringify(offer.value.document) : null
  stopRemotePoll()
  const generation = pollGeneration
  if (timer) clearTimeout(timer)
  loading.value = true
  error.value = ''
  const inflight = pending
  try {
    if (inflight) await inflight
    if (ownedDocument !== documentGeneration || String(route.params.id ?? '') !== routeId) return
    await loadInstance()
    const fresh = await api.get<Offer>(`/offers/${routeId}`)
    if (ownedDocument !== documentGeneration || String(route.params.id ?? '') !== routeId) return
    if (baseline != null && offer.value && JSON.stringify(offer.value.document) !== baseline) return
    offer.value = fresh
    savedDocument = JSON.stringify(fresh.document)
    dirty.value = false
    conflict.value = false
    saveFailed.value = false
    document.title = `${fresh.offer_no} · Angebot`
    await nextTick()
    if (ownedDocument !== documentGeneration) return
    renderer.value?.resetHistory()
    remoteWatch.value = createRemoteWatch(String(fresh.id), fresh.revision)
    remoteNewer.value = false
    remoteConfirm.value = false
  } catch (e) {
    if (ownedDocument === documentGeneration) error.value = errMsg(e)
  } finally {
    if (ownedDocument === documentGeneration) loading.value = false
    if (generation === pollGeneration) scheduleRemotePoll()
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
  if (loading.value) return false
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
    const ownedDocument = documentGeneration
    saving.value = true
    saveFailed.value = false
    saveFeedback.value = true
    if (remoteWatch.value) remoteWatch.value = beginSave(remoteWatch.value)
    if (feedbackTimer) clearTimeout(feedbackTimer)
    const started = Date.now()
    error.value = ''
    const current = () => ownedDocument === documentGeneration
    try {
      let forceWrite = force
      while (
        current() &&
        offer.value &&
        (forceWrite || JSON.stringify(offer.value.document) !== savedDocument)
      ) {
        forceWrite = false
        const snapshot = JSON.stringify(offer.value.document)
        const revision = offer.value.revision
        const offerId = offer.value.id
        const result = await api.put<Offer>(`/offers/${offerId}`, {
          revision,
          document: JSON.parse(snapshot),
          prose_writer_version: OFFER_PROSE_WRITER_VERSION,
        })
        if (!current() || !offer.value || offer.value.id !== offerId) return true
        offer.value.revision = result.revision
        offer.value.updated_at = result.updated_at
        savedDocument = snapshot
        if (remoteWatch.value) remoteWatch.value = noteOwnRevision(remoteWatch.value, result.revision)
      }
      if (!current()) return true
      dirty.value = JSON.stringify(offer.value?.document) !== savedDocument
      return !dirty.value
    } catch (e) {
      if (!current()) return true
      saveFailed.value = true
      error.value = errMsg(e)
      if (e instanceof ApiError && e.status === 409) conflict.value = true
      return false
    } finally {
      saving.value = false
      if (remoteWatch.value) {
        remoteWatch.value = finishSave(remoteWatch.value)
        remoteNewer.value = remoteIsNewer(remoteWatch.value)
      }
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
  if (remoteWatch.value) remoteWatch.value = beginSave(remoteWatch.value)
  error.value = ''
  try {
    const result = await api.put<Offer>(`/offers/${offer.value.id}`, {
      revision: offer.value.revision,
      document: offer.value.document,
      finalize: true,
      prose_writer_version: OFFER_PROSE_WRITER_VERSION,
    })
    savedDocument = JSON.stringify(result.document)
    offer.value = result
    dirty.value = false
    if (remoteWatch.value) remoteWatch.value = noteOwnRevision(remoteWatch.value, result.revision)
    finalizeOpen.value = false
    await nextTick()
    renderer.value?.resetHistory()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
    finalizing.value = false
    if (remoteWatch.value) {
      remoteWatch.value = finishSave(remoteWatch.value)
      remoteNewer.value = remoteIsNewer(remoteWatch.value)
    }
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
    loading: loading.value,
    overflow: !!overflow.value,
    copied: copied.value,
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
watch(
  () => String(route.params.id ?? ''),
  (id, previous) => {
    if (!previous || id === previous) return
    remoteConfirm.value = false
    void load()
  },
)
watch(remoteConfirm, async (open) => {
  await nextTick()
  if (open) remoteDialog.value?.showModal()
  else remoteDialog.value?.close()
})
onMounted(() => {
  void load()
  window.addEventListener('beforeunload', beforeUnload)
  window.addEventListener('resize', fitZoom)
  document.addEventListener('visibilitychange', onRemoteVisibility)
  window.addEventListener('focus', onRemoteFocus)
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
  stopRemotePoll()
  window.removeEventListener('beforeunload', beforeUnload)
  window.removeEventListener('resize', fitZoom)
  document.removeEventListener('visibilitychange', onRemoteVisibility)
  window.removeEventListener('focus', onRemoteFocus)
  resizeObserver?.disconnect()
  if (feedbackTimer) clearTimeout(feedbackTimer)
})
onBeforeRouteLeave(async () => !dirty.value || (await save()))
</script>
<template>
  <main class="offer-view" :class="{ 'is-collapsed': collapsed }">
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
        :print-disabled="loading || saving || !!overflow || conflict"
        :show-print="!!offer"
        :actions="toolbarActions"
        :show-inspector="editable"
        :inspector-open="inspectorOpen"
        :header-collapsed="collapsed"
        :external-changed="remoteNewer"
        @save="save(true)"
        @print="printOffer"
        @action="onToolbarAction"
        @toggle-inspector="inspectorOpen = !inspectorOpen"
        @toggle-header="collapsed = !collapsed"
        @reload-remote="requestRemoteReload"
      />
      <p v-if="loading" class="offer-notice">Angebot wird geladen …</p>
      <p v-if="error || overflow" role="alert" class="offer-notice offer-error">
        {{ error || overflow }}
        <button v-if="dirty && !conflict && !loading" class="btn" @click="save()">Erneut speichern</button>
      </p>
      <p v-if="editable && !collapsed" class="offer-notice">
        Klicke in einen Text, um ihn zu bearbeiten. Die Werkzeuge dafür stehen im Inspektor.
        Änderungen werden automatisch gespeichert. Kundenlink und QR-Code werden beim Finalisieren
        erstellt.
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
        <button class="btn" type="button" :disabled="deleting || loading" @click="setDeleted">
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
      <div class="offer-workspace">
        <OfferInspector
          v-if="editable && offer"
          :open="inspectorOpen"
          :footer="offer.document.footer ?? null"
          :can-undo="!!renderer?.canUndo"
          :can-redo="!!renderer?.canRedo"
          :selection="selection"
          :block-count="offer.document.blocks.length"
          @footer="onFooter"
          @undo="renderer?.undo()"
          @redo="renderer?.redo()"
          @action="onToolbarAction"
          @insert-section="renderer?.addSection()"
          @insert-position="addPosition()"
          @section-add="renderer?.addSection()"
          @section-up="renderer?.moveSection(-1)"
          @section-down="renderer?.moveSection(1)"
          @section-delete="renderer?.askDeleteSection()"
          @position-up="selection.kind === 'position' && renderer?.move(selection.index, -1)"
          @position-down="selection.kind === 'position' && renderer?.move(selection.index, 1)"
          @position-delete="selection.kind === 'position' && renderer?.remove(selection.index)"
          @hide="inspectorOpen = false"
        />
        <div ref="viewport" class="offer-stage">
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
        </div>
      </div>
      <dialog
        ref="remoteDialog"
        class="finalize-dialog"
        aria-label="Extern geändert"
        @cancel.prevent="remoteConfirm = false"
      >
        <h2>Extern geändert</h2>
        <p>
          Ein neuerer Stand liegt vor. Aktualisieren ersetzt die lokalen Änderungen. Abbrechen
          behält sie.
        </p>
        <button class="btn btn-primary" type="button" @click="remoteConfirm = false; load()">
          Aktualisieren
        </button>
        <button class="btn" type="button" @click="remoteConfirm = false">Abbrechen</button>
      </dialog>
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
            loading ||
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
@media screen {
  .offer-view {
    display: flex;
    flex-direction: column;
    height: calc(100dvh - 132px);
    min-height: 280px;
  }
  .offer-view.is-collapsed {
    height: calc(100dvh - 16px);
  }
  .offer-workspace {
    flex: 1;
    min-height: 0;
    min-width: 0;
    display: flex;
    align-items: stretch;
  }
  .offer-stage {
    order: 1;
    flex: 1 1 auto;
    min-width: 0;
    min-height: 0;
    overflow: auto;
  }
  .offer-workspace :deep(.offer-inspector) {
    order: 2;
    flex: none;
    align-self: stretch;
    width: 340px;
    max-height: 100%;
    overflow: auto;
  }
}
@media screen and (max-width: 860px) {
  .offer-workspace {
    flex-direction: column;
  }
  .offer-stage,
  .offer-workspace :deep(.offer-inspector) {
    order: 0;
    width: 100%;
    max-height: min(46vh, 420px);
  }
  .offer-stage {
    max-height: none;
    flex: 1 1 auto;
  }
  .offer-workspace :deep(.offer-inspector) {
    border-left: 0;
    border-bottom: 1px solid var(--h-line, #d8e2df);
  }
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
