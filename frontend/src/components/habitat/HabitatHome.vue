<script setup lang="ts">
import { computed } from 'vue'
import HabitatRuntimeHealth from './HabitatRuntimeHealth.vue'
import { RouterLink, useRoute } from 'vue-router'
import { ArrowRight, ChevronRight, MessageSquare, ShieldCheck, Info } from 'lucide-vue-next'
import type { OrchestrationSnapshotV1 } from '@/services/orchestrationTypes'
import type { Delivery } from '@/services/agentMode'
import type { Paimos6SessionZoomTotals } from '@/v6/sessionHomeZoom'
import {
  humanize,
  workerIntentionallyStopped,
  workerNeedsAttention,
  type HabitatWorker,
} from './habitatModel'
const props = defineProps<{
  snapshot: OrchestrationSnapshotV1
  deliveries: Delivery[]
  messages: { projectId: number; totals: Paimos6SessionZoomTotals | null }[]
  messageState: 'loading' | 'ready'
  deliveryState: 'loading' | 'ready' | 'unavailable'
  fresh: boolean
  attentionOnly?: boolean
  authority: string
  reviewer?: boolean
}>()
const emit = defineEmits<{
  assign: [projectId?: number]
  selectWorker: [worker: HabitatWorker]
  selectProject: [projectId: number]
  workers: []
  projects: []
  refresh: []
}>()
const route = useRoute()
const noWorkers = computed(() => props.snapshot.fleet.totals.workers === 0)
const root = computed(() => props.snapshot.instance_root)
const nextStep = computed(() => {
  if (!root.value.configured_identity)
    return {
      title: 'Start with your coordinator.',
      description:
        'Your projects are here. Set up the workspace coordinator, then choose a project and start your first worker.',
      action: 'Set up coordinator',
    }
  if (['ambiguous', 'unknown'].includes(root.value.active_generation.state))
    return {
      title: 'Let’s check your coordinator.',
      description:
        'The coordinator’s current run is not confirmed. Review its state before starting more work.',
      action: 'Review coordinator',
    }
  if (root.value.active_generation.state !== 'resolved')
    return {
      title: 'Bring your coordinator online.',
      description:
        'Your coordinator is configured. Choose a runtime and start its first run to bring your project work together.',
      action: 'Start coordinator',
    }
  return {
    title: 'Bring your first worker online.',
    description:
      'Your coordinator is connected. Choose a runtime, a project, and a task to start a worker.',
    action: 'Set up a worker',
  }
})
const projects = computed(() => props.snapshot.project_coordination)
const attentionWorkers = computed(() => props.snapshot.fleet.workers.filter(workerNeedsAttention))
const attentionDeliveries = computed(() =>
  props.deliveries.filter(
    (d) => d.attention.level >= 2 || ['blocked', 'stale', 'attention'].includes(d.health),
  ),
)
const attentionMessages = computed(() =>
  props.messages.filter((row) => row.totals && row.totals.exception_messages > 0),
)
const attentionCount = computed(
  () =>
    attentionWorkers.value.length +
    attentionDeliveries.value.length +
    attentionMessages.value.length,
)
const attentionCoverageIncomplete = computed(() => {
  const bounds = props.snapshot.coordination_bounds
  const fleet = props.snapshot.fleet
  const projectIds = new Set(projects.value.map((row) => row.project.id))
  const messageProjectIds = new Set(props.messages.map((row) => row.projectId))
  return (
    !props.fresh ||
    bounds.sampled_projects !== bounds.total_projects ||
    bounds.sampled_projects !== projects.value.length ||
    bounds.omitted_projects > 0 ||
    messageProjectIds.size !== projects.value.length ||
    [...projectIds].some((projectId) => !messageProjectIds.has(projectId)) ||
    fleet.sample_truncated ||
    fleet.totals.sampled_workers !== fleet.totals.workers ||
    fleet.totals.omitted_workers > 0 ||
    fleet.totals.sampled_projects !== fleet.totals.projects ||
    fleet.totals.omitted_projects > 0
  )
})
const attentionUnknown = computed(
  () =>
    props.messageState !== 'ready' ||
    props.deliveryState !== 'ready' ||
    props.messages.some((row) => row.totals === null) ||
    attentionCoverageIncomplete.value,
)
const attentionUnknownCopy = computed(() => {
  if (props.messageState === 'loading' || props.deliveryState === 'loading')
    return 'Checking messages and delivery decisions…'
  if (attentionCoverageIncomplete.value)
    return 'Attention coverage is incomplete. Some projects or workers are outside this snapshot.'
  return 'Some attention is unknown. Refresh to check messages and delivery decisions.'
})
const atWork = computed(() => props.snapshot.fleet.workers.slice(0, 4))
const rootWorker = computed(() =>
  props.snapshot.fleet.workers.find(
    (worker) => worker.harness_session_id === root.value.active_generation.session_id,
  ),
)
const activity = computed(() =>
  props.snapshot.fleet.workers
    .flatMap((worker) => worker.recent_communication.map((event) => ({ worker, event })))
    .sort((a, b) => Date.parse(b.event.occurred_at) - Date.parse(a.event.occurred_at))
    .slice(0, 3),
)
function workerLabel(worker: HabitatWorker) {
  if (!props.fresh) return 'Last snapshot'
  if (workerIntentionallyStopped(worker)) return 'Stopped'
  if (workerNeedsAttention(worker))
    return worker.liveness.state === 'dead'
      ? 'Disconnected'
      : worker.liveness.state === 'unknown'
        ? 'Unknown'
        : 'Needs you'
  return worker.liveness.state === 'busy' ? 'Working' : 'Available'
}
function projectCount(id: number) {
  return (
    props.snapshot.fleet.projects.find((project) => project.id === id)?.total_workers ??
    (noWorkers.value ? 0 : null)
  )
}
function projectStatus(id: number) {
  const project = projects.value.find((row) => row.project.id === id)
  if (!props.fresh) return 'Last snapshot'
  if (project?.coordinator.state === 'ambiguous') return 'Check coordinator'
  if (project?.coordinator.state === 'resolved') return 'Coordinated'
  return projectCount(id) === 0 ? 'No workers yet' : 'Coordinator needed'
}
</script>

<template>
  <div class="habitat-home">
    <div class="habitat-overview" aria-label="Workspace summary">
      <button type="button" @click="emit('workers')">
        <strong>{{ snapshot.fleet.totals.workers }}</strong
        ><span>{{ snapshot.fleet.totals.workers === 1 ? 'known worker' : 'known workers' }}</span>
      </button>
      <button type="button" @click="emit('projects')">
        <strong>{{ snapshot.coordination_bounds.total_projects }}</strong
        ><span>{{
          snapshot.coordination_bounds.total_projects === 1 ? 'project' : 'projects'
        }}</span>
      </button>
      <span class="habitat-overview-attention"
        ><strong>{{ attentionUnknown ? '—' : attentionCount }}</strong
        ><span>{{
          attentionUnknown
            ? 'attention checking'
            : attentionCount === 1
              ? 'item needs you'
              : 'items need you'
        }}</span></span
      >
      <span class="habitat-overview-source">{{
        fresh ? 'Current snapshot' : 'Last snapshot'
      }}</span>
    </div>

    <section v-if="!reviewer && noWorkers && !attentionOnly" class="habitat-welcome">
      <div>
        <h2>{{ nextStep.title }}</h2>
        <p>{{ nextStep.description }}</p>
        <div class="habitat-actions">
          <button class="habitat-primary" type="button" @click="emit('assign')">
            {{ nextStep.action }}<ArrowRight :size="15" aria-hidden="true" />
          </button>
        </div>
      </div>
    </section>

    <div
      class="habitat-home-columns"
      :class="{ 'is-empty': noWorkers, 'attention-only': attentionOnly }"
    >
      <div class="habitat-home-primary">
        <section
          v-if="attentionOnly || !noWorkers || attentionCount || attentionUnknown"
          class="habitat-home-attention"
          aria-labelledby="habitat-needs-heading"
        >
          <div class="habitat-section-head">
            <h2 id="habitat-needs-heading">
              Needs you
              <span v-if="!attentionUnknown" class="habitat-count">{{ attentionCount }}</span>
            </h2>
            <RouterLink v-if="!reviewer" :to="{ query: { ...route.query, view: 'sessions' } }"
              >Messages <ArrowRight :size="13"
            /></RouterLink>
          </div>
          <div class="habitat-attention-list">
            <article
              v-for="row in attentionMessages"
              :key="`message-${row.projectId}`"
              class="habitat-home-request"
            >
              <span class="habitat-request-label"
                ><MessageSquare :size="13" aria-hidden="true" />Messages &amp; decisions</span
              >
              <h3>
                {{ projects.find((project) => project.project.id === row.projectId)?.project.name }}
              </h3>
              <p>
                {{ row.totals?.action_requests }} held action requests ·
                {{ row.totals?.exception_messages }} exceptional messages.
              </p>
              <RouterLink
                v-if="!reviewer"
                class="habitat-button habitat-primary"
                :to="{
                  query: { ...route.query, project: String(row.projectId), view: 'sessions' },
                }"
                >Review messages &amp; decisions <ArrowRight :size="14" aria-hidden="true"
              /></RouterLink>
            </article>
            <article
              v-for="delivery in attentionDeliveries"
              :key="delivery.id"
              class="habitat-home-request"
            >
              <span class="habitat-request-label"
                ><ShieldCheck :size="13" aria-hidden="true" />{{
                  humanize(delivery.attention.reason ?? delivery.health)
                }}</span
              >
              <h3>{{ delivery.title }}</h3>
              <p>
                {{ delivery.issueKey }} · {{ humanize(delivery.stage.key) }} ·
                {{ humanize(delivery.freshness.state) }} evidence
              </p>
              <RouterLink
                class="habitat-button habitat-primary"
                :to="`/projects/${delivery.lane.projectId}/issues/${delivery.issueId}`"
                >Review decision <ArrowRight :size="14" aria-hidden="true"
              /></RouterLink>
            </article>
            <article
              v-for="worker in attentionWorkers"
              :key="worker.harness_session_id"
              class="habitat-home-request habitat-worker-request"
            >
              <span
                class="habitat-orb"
                :data-state="worker.liveness.state"
                aria-hidden="true"
              ></span>
              <div>
                <span class="habitat-request-label">{{ workerLabel(worker) }}</span>
                <h3>{{ worker.agent.name }}</h3>
                <p>
                  {{ worker.project.name }} · {{ humanize(worker.liveness.reason) }} ·
                  {{ humanize(worker.delivery_trust.reason) }}
                </p>
                <button type="button" @click="emit('selectWorker', worker)">
                  Inspect &amp; recover <ArrowRight :size="14" aria-hidden="true" />
                </button>
              </div>
            </article>
            <div v-if="!attentionCount && !attentionUnknown" class="habitat-home-clear">
              <span class="habitat-clear-mark">✓</span>
              <h3>Nothing needs your attention here.</h3>
              <p>Questions and decisions from this view will appear here.</p>
            </div>
            <div v-if="attentionUnknown" class="habitat-home-unknown">
              <Info :size="15" aria-hidden="true" />
              <div>
                <p>{{ attentionUnknownCopy }}</p>
                <button
                  v-if="messageState === 'ready' && deliveryState !== 'loading'"
                  type="button"
                  @click="emit('refresh')"
                >
                  Refresh attention
                </button>
              </div>
            </div>
          </div>
        </section>

        <section v-if="!attentionOnly" class="habitat-home-projects" aria-label="Your projects">
          <div class="habitat-section-head">
            <h2>
              Your projects <span class="habitat-count">{{ projects.length }}</span>
            </h2>
            <button type="button" class="habitat-text-action" @click="emit('projects')">
              All projects <ArrowRight :size="13" aria-hidden="true" />
            </button>
          </div>
          <div v-if="projects.length" class="habitat-project-table-wrap">
            <table class="habitat-project-table">
              <thead>
                <tr>
                  <th scope="col">Project</th>
                  <th scope="col">Status</th>
                  <th scope="col" class="habitat-project-workers-col">Workers</th>
                  <th scope="col"><span class="habitat-sr-only">Details</span></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(project, index) in projects" :key="project.project.id">
                  <td>
                    <button
                      type="button"
                      class="habitat-project-open"
                      @click="emit('selectProject', project.project.id)"
                    >
                      <span class="habitat-project-initial" :data-color="index % 4">{{
                        project.project.key.charAt(0)
                      }}</span
                      ><span
                        ><strong>{{ project.project.name }}</strong
                        ><small>{{ project.project.key }}</small></span
                      >
                    </button>
                  </td>
                  <td>
                    <span class="habitat-project-status">{{
                      projectStatus(project.project.id)
                    }}</span>
                  </td>
                  <td class="habitat-project-workers-col">
                    {{ projectCount(project.project.id) ?? 'Unknown' }}
                  </td>
                  <td>
                    <button
                      type="button"
                      class="habitat-project-chevron"
                      :aria-label="`Inspect ${project.project.name}`"
                      @click="emit('selectProject', project.project.id)"
                    >
                      <ChevronRight :size="14" aria-hidden="true" />
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="habitat-home-clear">
            <h3>Your first project starts here.</h3>
            <p>No projects are available in this view.</p>
            <RouterLink class="habitat-button" to="/projects">Open project workspace</RouterLink>
          </div>
        </section>
      </div>

      <aside v-if="!attentionOnly" class="habitat-home-secondary">
        <section v-if="noWorkers" class="habitat-setup-guide">
          <h2>Three steps to your first run</h2>
          <div class="habitat-setup-step">
            <span>1</span>
            <div>
              <h3>Set the coordinator</h3>
              <p>Choose the agent that will bring the work together.</p>
            </div>
          </div>
          <div class="habitat-setup-step">
            <span>2</span>
            <div>
              <h3>Connect a runtime</h3>
              <p>Choose where your worker will run.</p>
            </div>
          </div>
          <div class="habitat-setup-step">
            <span>3</span>
            <div>
              <h3>Give it a project and task</h3>
              <p>Review the setup, then start the work.</p>
            </div>
          </div>
        </section>
        <template v-else>
          <section>
            <div class="habitat-section-head">
              <h2>Your workers</h2>
              <button type="button" class="habitat-text-action" @click="emit('workers')">
                All workers <ArrowRight :size="13" aria-hidden="true" />
              </button>
            </div>
            <div class="habitat-home-roster">
              <button
                v-for="worker in atWork"
                :key="worker.harness_session_id"
                type="button"
                class="habitat-roster-row"
                @click="emit('selectWorker', worker)"
              >
                <span
                  class="habitat-orb"
                  :data-state="worker.liveness.state"
                  aria-hidden="true"
                ></span
                ><span class="habitat-roster-person"
                  ><strong>{{ worker.agent.name }}</strong
                  ><small>{{ worker.project.name }}</small></span
                ><span
                  class="habitat-roster-state"
                  :data-attention="workerNeedsAttention(worker)"
                  >{{ workerLabel(worker) }}</span
                >
              </button>
              <p v-if="!atWork.length" class="habitat-roster-empty">
                No workers are included at this level of detail.
              </p>
              <div v-if="!reviewer" class="habitat-roster-root">
                <span>{{
                  root.configured_identity?.display_label || 'Coordinator not configured'
                }}</span
                ><button v-if="rootWorker" type="button" @click="emit('selectWorker', rootWorker)">
                  Inspect</button
                ><button v-else type="button" @click="emit('assign')">
                  {{ root.configured_identity ? 'Review start' : 'Set up' }}
                </button>
              </div>
            </div>
          </section>
          <section v-if="activity.length" class="habitat-home-activity">
            <div class="habitat-section-head"><h2>Recent communication</h2></div>
            <button
              v-for="row in activity"
              :key="`${row.worker.harness_session_id}-${row.event.message_id}-${row.event.direction}`"
              type="button"
              class="habitat-activity-row"
              @click="emit('selectWorker', row.worker)"
            >
              <span class="habitat-activity-mark"
                ><MessageSquare :size="12" aria-hidden="true" /></span
              ><span
                ><strong>{{ row.worker.agent.name }}</strong
                ><span> · {{ humanize(row.event.state) }} message</span
                ><small
                  >{{ row.worker.project.name }} ·
                  {{
                    new Date(row.event.occurred_at).toLocaleTimeString([], {
                      hour: '2-digit',
                      minute: '2-digit',
                    })
                  }}</small
                ></span
              >
            </button>
          </section>
        </template>
        <HabitatRuntimeHealth
          v-if="!reviewer"
          :project-ids="projects.map((row) => row.project.id)"
          :authority="authority"
          :fresh="fresh"
        />
      </aside>
    </div>
  </div>
</template>
