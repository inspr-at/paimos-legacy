<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onScopeDispose, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowUpRight, ChevronDown, ChevronRight, RefreshCw } from 'lucide-vue-next'
import { permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useHabitat } from '@/composables/habitat/useHabitat'
import { PAIMOS6_COMMAND_CONTEXT_KEY } from '@/v6/commandPaletteContext'
import {
  humanize,
  workerRows,
  workerNeedsAttention,
  recentCommunication,
  type HabitatWorker,
} from './habitatModel'
import HabitatInspector from './HabitatInspector.vue'
import HabitatHome from './HabitatHome.vue'
import HabitatSetup from './HabitatSetup.vue'
import HabitatDeliveryEvidence from './HabitatDeliveryEvidence.vue'

const auth = useAuthStore()
const isReviewer = computed(() => auth.user?.role === 'reviewer')
const route = useRoute()
const router = useRouter()
const { t } = useI18n({
  useScope: 'local',
  messages: {
    en: {
      home: 'Home',
      workers: 'Workers',
      projects: 'Projects',
      assign: 'Start work',
      attention: 'Needs you',
    },
    de: {
      home: 'Start',
      workers: 'Worker',
      projects: 'Projekte',
      attention: 'Braucht dich',
      assign: 'Arbeit starten',
    },
  },
})
const view = computed(() =>
  ['workers', 'projects', 'assign', 'attention'].includes(String(route.query.view))
    ? String(route.query.view)
    : 'home',
)
const projectId = computed(() => {
  const value = route.query.project
  return typeof value === 'string' &&
    /^[1-9]\d*$/.test(value) &&
    Number.isSafeInteger(Number(value))
    ? Number(value)
    : null
})
const zoom = computed(() =>
  typeof route.query.zoom === 'string' && /^[1-9]\d{0,63}$/.test(route.query.zoom)
    ? route.query.zoom
    : '10',
)
const zoomInput = ref(zoom.value)
const principal = computed(() => auth.user?.id ?? null)
const authority = computed(() =>
  JSON.stringify([
    permissionsEpochGeneration.value,
    permissionsEpoch.value,
    auth.user?.id,
    auth.user?.role,
    auth.user?.status,
    auth.allProjects,
    [...auth.accessibleProjects.entries()].sort(([a], [b]) => a - b),
  ]),
)
const habitat = useHabitat({ authority, principal, project: projectId, zoom })
const {
  snapshot,
  messageAttention,
  messageState,
  deliveries,
  state,
  deliveryState,
  refreshing,
  selectedId,
  selectedWorker,
  stale,
  now,
} = habitat
const expanded = ref(new Set<string>())
const projectSelection = ref<number | null>(null)
const inspector = ref<HTMLElement | null>(null)
const inspectorActions = ref<InstanceType<typeof HabitatInspector> | null>(null)
const announcement = ref('')
const viewOptions = ref(false)
const pageTitle = computed(() => t(view.value))
const inspectorTrigger = ref<HTMLElement | null>(null)
const compactMedia = globalThis.matchMedia?.('(max-width: 760px)')
const compactViewport = ref(compactMedia?.matches ?? false)
const compactShell = ref(false)
const compactInspector = computed(() => compactViewport.value || compactShell.value)
let shellResize: ResizeObserver | null = null
onMounted(() => {
  const shell = document.querySelector('#habitat-main')?.closest<HTMLElement>('.habitat-shell')
  if (shell && typeof ResizeObserver !== 'undefined') {
    compactShell.value = shell.clientWidth <= 760
    shellResize = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width
      if (width !== undefined) compactShell.value = width <= 760
    })
    shellResize.observe(shell)
  }
})
function updateCompactInspector() {
  compactViewport.value = compactMedia?.matches ?? false
}
compactMedia?.addEventListener('change', updateCompactInspector)
const inertBackground = new Map<HTMLElement, boolean>()
let priorBodyOverflow: string | null = null
let modalGeneration = 0
function restoreInspectorBackground() {
  for (const [element, prior] of inertBackground) element.inert = prior
  inertBackground.clear()
  if (priorBodyOverflow !== null) {
    document.body.style.overflow = priorBodyOverflow
    priorBodyOverflow = null
  }
}
function restoreInspectorFocus() {
  const trigger = inspectorTrigger.value
  if (trigger?.isConnected) trigger.focus()
  else document.querySelector<HTMLElement>('#habitat-main')?.focus()
}
function closeInspector() {
  selectedId.value = null
  projectSelection.value = null
  restoreInspectorBackground()
  void nextTick(restoreInspectorFocus)
}
const selectedProject = computed(
  () =>
    snapshot.value?.project_coordination.find((p) => p.project.id === projectSelection.value) ??
    null,
)
const inspectorOpen = computed(() => !!(selectedWorker.value || selectedProject.value))
watch([compactInspector, inspectorOpen], async ([compact, open]) => {
  const version = ++modalGeneration
  const focusedInside = inspector.value?.contains(document.activeElement)
  restoreInspectorBackground()
  if (!open) {
    if (focusedInside) void nextTick(restoreInspectorFocus)
    return
  }
  if (!compact) return
  await nextTick()
  if (version !== modalGeneration || !inspector.value) return
  const shell = inspector.value.closest('.habitat-shell') ?? document
  for (const element of shell.querySelectorAll<HTMLElement>(
    '.habitat-rail, .habitat-header, .habitat-source, .habitat-security, .habitat-footer, .habitat-hero, .habitat-context, .habitat-stage, .habitat-snapshot-line',
  )) {
    inertBackground.set(element, element.inert)
    element.inert = true
  }
  priorBodyOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  inspector.value.querySelector<HTMLButtonElement>('.habitat-inspector-close')?.focus()
})
function inspectorKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    closeInspector()
    return
  }
  if (
    !compactInspector.value ||
    event.key !== 'Tab' ||
    event.ctrlKey ||
    event.metaKey ||
    event.altKey
  )
    return
  const focusable = [
    ...(inspector.value?.querySelectorAll<HTMLElement>(
      'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])',
    ) ?? []),
  ].filter((element) => element.getClientRects().length > 0 && !element.closest('[inert]'))
  const first = focusable[0],
    last = focusable[focusable.length - 1]
  if (!first || !last) {
    event.preventDefault()
    inspector.value?.focus()
    return
  }
  if (
    event.shiftKey &&
    (document.activeElement === first || document.activeElement === inspector.value)
  ) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
function rememberInspectorTrigger() {
  if (
    document.activeElement instanceof HTMLElement &&
    !inspector.value?.contains(document.activeElement)
  )
    inspectorTrigger.value = document.activeElement
}
onScopeDispose(() => {
  modalGeneration++
  compactMedia?.removeEventListener('change', updateCompactInspector)
  shellResize?.disconnect()
  restoreInspectorBackground()
})
const projects = computed(() => snapshot.value?.project_coordination ?? [])
const projectWorkers = (id: number) =>
  snapshot.value?.fleet.workers.filter((w) => w.project.id === id) ?? []
const projectDeliveryRows = (id: number) =>
  deliveries.value?.deliveries.filter((d) => d.lane.projectId === id) ?? []
const projectAggregate = (id: number) =>
  deliveries.value?.aggregates?.projects.find((p) => p.projectId === id)
const projectTotals = (id: number) => snapshot.value?.fleet.projects.find((p) => p.id === id)
const visibleRows = (id: number) => workerRows(projectWorkers(id), expanded.value)
const root = computed(() => snapshot.value?.instance_root)
const rootWorker = computed(() =>
  snapshot.value?.fleet.workers.find(
    (w) => w.harness_session_id === root.value?.active_generation.session_id,
  ),
)
const detail = computed(() => snapshot.value?.fleet.band === 'detail')
const consolidated = computed(() => ['aggregate', 'far'].includes(snapshot.value?.fleet.band ?? ''))
const bandCopy = computed(
  () =>
    ({
      detail: '1 · individual work and dispatch',
      overview: '10 · worker trees',
      aggregate: '100 · project clusters',
      far: '1000+ · portfolio and omissions',
    })[snapshot.value?.fleet.band ?? 'overview'],
)

watch(zoom, (value) => {
  zoomInput.value = value
})
watch(
  [authority, projectId, zoom],
  () => {
    projectSelection.value = null
    announcement.value = ''
    expanded.value = new Set()
  },
  { flush: 'sync' },
)
watch(
  () => snapshot.value?.fleet.workers,
  (workers) => {
    const focused =
      document.activeElement?.closest<HTMLElement>('[data-worker-id]')?.dataset.workerId
    if (focused && !workers?.some((w) => w.harness_session_id === focused))
      void nextTick(() => document.querySelector<HTMLElement>('#habitat-main')?.focus())
    if (detail.value && workers) expanded.value = new Set(workers.map((w) => w.harness_session_id))
  },
)
watch(selectedWorker, (worker, prior) => {
  if (
    !worker &&
    prior &&
    !snapshot.value?.fleet.workers.some(
      (row) => row.harness_session_id === prior.harness_session_id,
    )
  ) {
    announcement.value =
      'The selected worker is no longer in this authorized sample. Refresh or change detail to locate it.'
    if (inspector.value?.contains(document.activeElement)) void nextTick(restoreInspectorFocus)
  }
})
function selectWorker(worker: HabitatWorker) {
  rememberInspectorTrigger()
  selectedId.value = worker.harness_session_id
  projectSelection.value = null
  announcement.value = `Inspecting ${worker.agent.name}, ${worker.project.name}.`
}
function selectProject(id: number) {
  rememberInspectorTrigger()
  selectedId.value = null
  projectSelection.value = id
  announcement.value = 'Project selected. Details are in the inspector.'
}
function toggleWorker(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}
function setZoom(value: string) {
  if (!/^[1-9]\d{0,63}$/.test(value)) {
    announcement.value = 'Enter a positive whole number, up to 64 digits.'
    return
  }
  void router.replace({ query: { ...route.query, zoom: value } })
}
function assign(project?: number, sessionId?: string) {
  if (isReviewer.value) return
  if (inspectorOpen.value) closeInspector()
  void router.replace({
    query: {
      ...route.query,
      view: 'assign',
      worker: sessionId,
      project: project === undefined ? route.query.project : String(project),
    },
  })
}
function showWorkers(id?: number) {
  void router.replace({
    query: {
      ...route.query,
      view: 'workers',
      project: id === undefined ? route.query.project : String(id),
      zoom: '10',
    },
  })
}
const registerContext = inject(PAIMOS6_COMMAND_CONTEXT_KEY, null)
const noProductSession = computed(() => null)
registerContext?.({
  selectedSessionId: noProductSession,
  openTalk: () => {
    if (isReviewer.value) return
    if (selectedWorker.value) {
      void inspectorActions.value?.openVoice()
      return
    }
    announcement.value =
      'Voice opens the product-session conversation. Worker controls remain in the inspector.'
    void router.replace({
      query: {
        ...route.query,
        view: 'sessions',
        project: route.query.project,
        talk: '1',
      },
    })
  },
  clearSession: () => {
    selectedId.value = null
  },
})
onScopeDispose(() => registerContext?.(null))
</script>

<template>
  <main id="habitat-main" class="habitat-main" tabindex="-1">
    <div v-show="viewOptions || !['home', 'attention'].includes(view)" class="habitat-context">
      <span
        >Habitat / {{ view === 'home' ? 'Needs you' : humanize(view)
        }}<template v-if="projectId"> / Project {{ projectId }}</template></span
      >
      <RouterLink v-if="projectId" :to="{ query: { ...route.query, project: undefined } }"
        >All authorized projects</RouterLink
      >
      <div class="habitat-zoom" aria-label="Semantic zoom">
        <span>Detail</span>
        <button
          v-for="value in ['1', '10', '100', '1000']"
          :key="value"
          type="button"
          :aria-pressed="zoom === value"
          @click="setZoom(value)"
        >
          {{ value === '1000' ? '1000+' : value }}
        </button>
        <label class="habitat-sr-only" for="habitat-zoom-value">Custom semantic zoom</label>
        <input
          id="habitat-zoom-value"
          v-model="zoomInput"
          inputmode="numeric"
          maxlength="64"
          @change="setZoom(zoomInput)"
          @keydown.enter="setZoom(zoomInput)"
        />
      </div>
      <button type="button" :disabled="refreshing" @click="habitat.refresh()">
        <RefreshCw :size="14" aria-hidden="true" />{{ refreshing ? 'Refreshing' : 'Refresh' }}
      </button>
    </div>
    <header class="habitat-hero">
      <div>
        <h1>{{ pageTitle }}</h1>
      </div>
      <button
        v-if="!isReviewer && !['assign', 'home', 'attention'].includes(view)"
        type="button"
        class="habitat-primary"
        @click="assign()"
      >
        Start work <ArrowUpRight :size="16" aria-hidden="true" />
      </button>
      <button
        v-if="['home', 'attention'].includes(view)"
        class="habitat-view-options"
        type="button"
        :aria-expanded="viewOptions"
        @click="viewOptions = !viewOptions"
      >
        View options <ChevronDown :size="13" aria-hidden="true" />
      </button>
    </header>
    <p
      class="habitat-status"
      :class="{ 'habitat-sr-only': !announcement }"
      role="status"
      aria-live="polite"
    >
      {{ announcement }}
    </p>
    <div v-if="state === 'loading'" class="habitat-loading" role="status">
      <span class="habitat-eyebrow">Connecting to your work</span>
      <h2>Loading your workers…</h2>
      <p>Worker state will appear when the server provides its evidence.</p>
    </div>
    <div v-else-if="state !== 'ready'" class="habitat-card habitat-error" role="alert">
      <h2>
        {{
          state === 'unauthorized'
            ? 'This scope is not available to your account.'
            : 'The control room could not be refreshed.'
        }}
      </h2>
      <p>
        {{
          state === 'unauthorized'
            ? 'No worker data is retained. Check your project access or return to your authorized portfolio.'
            : 'Runtime health and current work are unknown. Check the connection and retry this read.'
        }}
      </p>
      <div class="habitat-actions">
        <button type="button" @click="habitat.refresh()">Retry</button
        ><RouterLink class="habitat-button" to="/">Open portfolio</RouterLink
        ><RouterLink class="habitat-button" to="/settings?tab=account">Account settings</RouterLink>
      </div>
    </div>
    <template v-else-if="snapshot">
      <div v-if="stale" class="habitat-error" role="status">
        This snapshot is stale. Controls are paused until a successful refresh confirms current
        ownership. <button type="button" @click="habitat.refresh()">Refresh evidence</button>
      </div>
      <div
        v-if="!['home', 'attention'].includes(view)"
        class="habitat-section-head habitat-muted habitat-snapshot-line"
      >
        <span>{{ bandCopy }}</span
        ><span
          >Server snapshot · {{ new Date(snapshot.fleet.observed_at).toLocaleString() }} · refreshes
          every 15s</span
        >
      </div>
      <div
        class="habitat-workspace"
        :class="{ 'has-inspector': selectedWorker || selectedProject }"
      >
        <div class="habitat-stage habitat-stack">
          <HabitatSetup
            v-if="view === 'assign'"
            :projects="projects"
            :project-id="projectId"
            :authority="authority"
            :root="snapshot.instance_root"
            :workers="snapshot.fleet.workers"
            :fresh="!stale && state === 'ready'"
            @refresh="habitat.refresh()"
          />
          <template v-else>
            <section v-if="!isReviewer && !['home', 'attention'].includes(view)" class="habitat-card habitat-root">
              <span
                class="habitat-orb"
                :data-state="rootWorker?.liveness.state ?? 'unknown'"
                aria-hidden="true"
              ></span>
              <div>
                <span class="habitat-eyebrow">Instance orchestrator</span>
                <h2>
                  {{
                    root?.configured_identity?.display_label ?? 'A root identity is not configured.'
                  }}
                </h2>
                <span class="habitat-pill"
                  >{{ humanize(root?.active_generation.state) }} generation</span
                >
                <p>
                  {{ humanize(root?.active_generation.reason) }}. Binding revision
                  {{ root?.binding_revision }}.
                </p>
                <button v-if="rootWorker" type="button" @click="selectWorker(rootWorker)">
                  Inspect generation
                </button>
                <button v-else type="button" @click="assign()">
                  {{
                    root?.configured_identity ? 'Review recovery / start' : 'Set up orchestrator'
                  }}
                </button>
              </div>
            </section>
            <HabitatHome
              v-if="view === 'home' || view === 'attention'"
              :reviewer="isReviewer"
              :snapshot="snapshot"
              :deliveries="deliveries?.deliveries ?? []"
              :messages="messageAttention"
              :message-state="messageState"
              :delivery-state="deliveryState"
              :fresh="!stale && state === 'ready'"
              :attention-only="view === 'attention'"
              :authority="authority"
              @assign="assign"
              @select-worker="selectWorker"
              @select-project="selectProject"
              @workers="showWorkers()"
              @projects="router.replace({ query: { ...route.query, view: 'projects' } })"
              @refresh="habitat.refresh()"
            />
            <section
              v-else-if="view === 'workers'"
              class="habitat-tree"
              aria-label="Worker hierarchy"
            >
              <p class="habitat-muted">
                Instance coordination → project trees. Indentation represents explicit same-project
                parent bindings.
              </p>
              <section
                v-for="project in projects"
                :key="project.project.id"
                class="habitat-project-tree"
              >
                <div class="habitat-section-head">
                  <h2>
                    {{ project.project.name }}
                    <small class="habitat-muted">{{ project.project.key }}</small>
                  </h2>
                  <button type="button" @click="selectProject(project.project.id)">
                    Inspect project
                  </button>
                </div>
                <p class="habitat-muted">
                  Coordinator: {{ humanize(project.coordinator.state) }} ·
                  {{
                    projectTotals(project.project.id)?.total_workers ??
                    (snapshot.fleet.totals.workers === 0 ? 0 : 'Unknown count of')
                  }}
                  retained workers<template
                    v-if="projectTotals(project.project.id)?.omitted_workers"
                  >
                    · {{ projectTotals(project.project.id)?.omitted_workers }} omitted</template
                  >
                </p>
                <template v-if="consolidated"
                  ><button type="button" @click="showWorkers(project.project.id)">
                    Expand {{ project.project.key }} workers
                  </button></template
                >
                <ol
                  v-else
                  class="habitat-worker-list"
                  :aria-label="`${project.project.name} worker tree`"
                >
                  <li
                    v-for="row in visibleRows(project.project.id)"
                    :key="row.worker.harness_session_id"
                    class="habitat-worker-row"
                    :style="{ '--depth': row.depth }"
                    :data-selected="selectedId === row.worker.harness_session_id"
                    :data-worker-id="row.worker.harness_session_id"
                  >
                    <button
                      v-if="row.children"
                      type="button"
                      class="habitat-expand"
                      :aria-expanded="expanded.has(row.worker.harness_session_id)"
                      :aria-label="`${expanded.has(row.worker.harness_session_id) ? 'Collapse' : 'Expand'} ${row.worker.agent.name} descendants`"
                      @click="toggleWorker(row.worker.harness_session_id)"
                    >
                      <ChevronDown
                        v-if="expanded.has(row.worker.harness_session_id)"
                        :size="16"
                      /><ChevronRight v-else :size="16" />
                    </button>
                    <button
                      type="button"
                      class="habitat-worker-select"
                      :aria-pressed="selectedId === row.worker.harness_session_id"
                      @click="selectWorker(row.worker)"
                    >
                      <span
                        class="habitat-orb"
                        :data-state="row.worker.liveness.state"
                        :data-recent="!stale && recentCommunication(row.worker, now)"
                        aria-hidden="true"
                      ></span>
                      <span
                        ><strong>{{ row.worker.agent.name }}</strong
                        ><small
                          >{{
                            row.worker.role === 'coordinator'
                              ? 'Project coordinator'
                              : row.worker.parent_harness_session_id
                                ? 'Subagent · explicit parent'
                                : 'Agent · no parent binding'
                          }}
                          · {{ humanize(row.worker.liveness.state) }}</small
                        ><small
                          >{{
                            row.worker.ticket?.key ??
                            (row.worker.ticket ? 'Ticket details unavailable' : 'No ticket binding')
                          }}
                          · {{ humanize(row.worker.work_shape) }}</small
                        ><small v-if="detail"
                          >{{ row.worker.harness }} ·
                          {{ row.worker.dispatch_profile?.model ?? 'Model unknown' }} ·
                          {{ row.worker.account_label }}</small
                        ><small v-if="row.hidden"
                          >{{ row.hidden }} sampled descendants collapsed</small
                        ><small
                          v-if="
                            row.worker.parent_harness_session_id && !row.worker.parent_in_sample
                          "
                          >Parent outside this sample</small
                        ><small v-if="recentCommunication(row.worker, now)"
                          >Message evidence in the last 60 seconds</small
                        ></span
                      >
                    </button>
                  </li>
                </ol>
                <div v-if="!projectWorkers(project.project.id).length">
                  <p class="habitat-muted">
                    No worker generations in this sample. A canonical agent and a running process
                    are separate resources.
                  </p>
                  <button v-if="!isReviewer" type="button" @click="assign(project.project.id)">Set up a worker</button>
                </div>
              </section>
            </section>
            <section v-else class="habitat-stack" aria-label="Project portfolio">
              <div v-if="snapshot.fleet.band === 'far'" class="habitat-card">
                <span class="habitat-eyebrow">Portfolio · 1000+</span>
                <h2>{{ snapshot.coordination_bounds.total_projects }} authorized projects</h2>
                <p>
                  {{ snapshot.fleet.totals.workers }} retained workers.
                  {{ deliveries?.aggregates?.root.activeTotal ?? 'Unknown' }} active deliveries.
                </p>
                <p v-if="deliveries?.aggregates">
                  Trusted landing groups: {{ deliveries.aggregates.root.landing.within_4h }} within
                  4h · {{ deliveries.aggregates.root.landing.within_24h }} within 24h ·
                  {{ deliveries.aggregates.root.landing.within_3d }} within 3d ·
                  {{ deliveries.aggregates.root.landing.later }} later ·
                  {{ deliveries.aggregates.root.landing.range_only }} range only ·
                  {{ deliveries.aggregates.root.landing.suppressed_or_unknown }} unknown/suppressed.
                </p>
              </div>
              <div class="habitat-grid">
                <article v-for="project in projects" :key="project.project.id" class="habitat-card">
                  <span class="habitat-eyebrow">{{ project.project.key }} · project</span>
                  <h2>{{ project.project.name }}</h2>
                  <p>
                    {{
                      projectTotals(project.project.id)?.total_workers ??
                      (snapshot.fleet.totals.workers === 0 ? 0 : 'Unknown count of')
                    }}
                    retained workers · Coordinator {{ project.coordinator.state }}
                  </p>
                  <p v-if="!consolidated" class="habitat-muted">
                    {{ projectWorkers(project.project.id).filter(workerNeedsAttention).length }}
                    workers need evidence review in this sample.
                  </p>
                  <template v-if="projectAggregate(project.project.id)"
                    ><p>
                      {{ projectAggregate(project.project.id)!.counts.activeTotal }} active
                      deliveries ·
                      {{ projectAggregate(project.project.id)!.counts.flags.attention }} need
                      attention.
                    </p>
                    <p>
                      Landing evidence:
                      {{ projectAggregate(project.project.id)!.counts.landing.within_24h }} within
                      24h ·
                      {{
                        projectAggregate(project.project.id)!.counts.landing.suppressed_or_unknown
                      }}
                      unknown/suppressed.
                    </p>
                    <details v-if="consolidated">
                      <summary>Delivery stage counts</summary>
                      <p
                        v-for="(count, stage) in projectAggregate(project.project.id)!.counts
                          .currentStage"
                        :key="stage"
                      >
                        {{ humanize(stage) }} · {{ count }}
                      </p>
                    </details></template
                  >
                  <p v-else>
                    Portfolio progress / ETA: unknown. No trusted project aggregate is available.
                  </p>
                  <template v-if="!consolidated"
                    ><HabitatDeliveryEvidence
                      v-for="delivery in projectDeliveryRows(project.project.id).slice(
                        0,
                        detail ? 10 : 2,
                      )"
                      :key="delivery.id"
                      :delivery="delivery"
                      :fresh="!stale"
                      :detail="detail"
                    />
                    <p v-if="projectDeliveryRows(project.project.id).length > (detail ? 10 : 2)">
                      {{ projectDeliveryRows(project.project.id).length - (detail ? 10 : 2) }}
                      delivery rows collapsed. Inspect the project for more.
                    </p></template
                  >
                  <div class="habitat-actions">
                    <button type="button" @click="selectProject(project.project.id)">
                      Inspect project</button
                    ><button type="button" @click="showWorkers(project.project.id)">
                      Worker tree
                    </button>
                  </div>
                </article>
              </div>
            </section>
            <section v-if="!projects.length" class="habitat-card">
              <span class="habitat-eyebrow">Your next starting point</span>
              <h2>No authorized projects in this snapshot.</h2>
              <p>
                There is no work to visualize yet. Open the project workspace to create a project or
                check access, then refresh here.
              </p>
              <RouterLink class="habitat-button" to="/projects">Open project workspace</RouterLink>
            </section>
          </template>
          <details class="habitat-view-evidence habitat-muted">
            <summary>About this view</summary>
            <p>
              {{ snapshot.coordination_bounds.sampled_projects }} of
              {{ snapshot.coordination_bounds.total_projects }} authorized projects ·
              {{ snapshot.coordination_bounds.omitted_projects }} projects omitted.
              {{ snapshot.fleet.totals.sampled_workers }} of
              {{ snapshot.fleet.totals.workers }} retained workers ·
              {{ snapshot.fleet.totals.omitted_workers }} workers omitted.
            </p>
            <small
              >Authoritative database · no remote cache · one terminal generation retained per
              agent. Recent communication is bounded metadata; it is not proof of a reply or active
              conversation.</small
            >
          </details>
        </div>
        <div
          v-if="compactInspector && inspectorOpen"
          class="habitat-inspector-backdrop"
          aria-hidden="true"
          @click="closeInspector"
        ></div>
        <aside
          v-if="selectedWorker || selectedProject"
          ref="inspector"
          class="habitat-inspector"
          aria-label="Inspector"
          :role="compactInspector ? 'dialog' : undefined"
          :aria-modal="compactInspector ? 'true' : undefined"
          tabindex="-1"
          @keydown="inspectorKeydown"
        >
          <button
            type="button"
            class="habitat-inspector-close"
            aria-label="Close inspector"
            @click="closeInspector"
          >
            Close <span aria-hidden="true">×</span>
          </button>
          <HabitatInspector
            ref="inspectorActions"
            :worker="selectedWorker"
            :project="selectedProject"
            :workers="snapshot.fleet.workers"
            :deliveries="deliveries?.deliveries ?? []"
            :fresh="!stale && state === 'ready'"
            :authority="authority"
            @refresh="habitat.refresh()"
            @select-worker="selectWorker"
            @assign="assign"
          />
        </aside>
      </div>
    </template>
  </main>
</template>
