<script setup lang="ts">
import HabitatAssignmentHistory from './HabitatAssignmentHistory.vue'
import HabitatWorkerRemoval from './HabitatWorkerRemoval.vue'
import { RouterLink } from 'vue-router'
import { computed, nextTick, onScopeDispose, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useHabitatActions } from '@/composables/habitat/useHabitatActions'
import { useHabitatRemoval } from '@/composables/habitat/useHabitatRemoval'
import { useMicTranscript } from '@/composables/useMicTranscript'
import { transcribeAgentModeAudio } from '@/services/agentModeVoice'
import type { Delivery } from '@/services/agentMode'
import { estimatePresentation } from '@/composables/agent-mode/agentModeTrust'
import { humanize, type HabitatWorker, type HabitatProject } from './habitatModel'
const props = defineProps<{
  worker: HabitatWorker | null
  project: HabitatProject | null
  workers: HabitatWorker[]
  deliveries: Delivery[]
  fresh: boolean
  authority: string
}>()
const emit = defineEmits<{
  refresh: []
  selectWorker: [worker: HabitatWorker]
  assign: [projectId: number, sessionId?: string]
}>()
const auth = useAuthStore()
const isReviewer = computed(() => auth.user?.role === 'reviewer')
const { locale } = useI18n()
const editable = computed(() => (props.worker ? auth.canEdit(props.worker.project.id) : false))
const actions = useHabitatActions({
  worker: toRef(props, 'worker'),
  authority: toRef(props, 'authority'),
  fresh: toRef(props, 'fresh'),
  editable,
})
const removal = useHabitatRemoval({
  worker: toRef(props, 'worker'),
  authority: toRef(props, 'authority'),
  fresh: toRef(props, 'fresh'),
  editable,
  principalId: computed(() => auth.user?.id ?? null),
})
const { action, draft, feedback, busy, control } = actions
const parent = computed(() =>
  props.workers.find(
    (w) =>
      w.harness_session_id === props.worker?.parent_harness_session_id &&
      w.project.id === props.worker?.project.id,
  ),
)
const children = computed(() =>
  props.workers.filter(
    (w) =>
      w.parent_harness_session_id === props.worker?.harness_session_id &&
      w.project.id === props.worker?.project.id,
  ),
)
const projectDeliveries = computed(() =>
  props.deliveries.filter((d) => d.lane.projectId === props.project?.project.id),
)
const confirmButton = ref<HTMLButtonElement | null>(null)
const draftInput = ref<HTMLTextAreaElement | null>(null)
const actionTrigger = ref<HTMLButtonElement | null>(null)
watch(action, (value) => {
  if (value)
    void nextTick(() =>
      value === 'simple' || value === 'steer'
        ? draftInput.value?.focus()
        : confirmButton.value?.focus(),
    )
})
const mic = useMicTranscript()
const voiceFeedback = ref('')
let voiceGeneration = 0
let voiceController: AbortController | null = null
function prepareAction(kind: 'simple' | 'steer' | 'interrupt' | 'stop', event: Event) {
  actionTrigger.value = event.currentTarget as HTMLButtonElement
  actions.prepare(kind)
}
function restoreActionFocus() {
  const trigger = actionTrigger.value
  actionTrigger.value = null
  if (trigger?.isConnected && !trigger.disabled) trigger.focus()
}
function cancelAction() {
  action.value = null
  stopVoice()
  void nextTick(restoreActionFocus)
}
async function submitAction() {
  await actions.submit()
  if (!action.value) void nextTick(restoreActionFocus)
}
function stopVoice() {
  voiceGeneration++
  voiceController?.abort()
  mic.stop()
  voiceFeedback.value = ''
}
watch(
  () =>
    `${props.authority}/${props.worker?.harness_session_id ?? ''}/${props.worker?.revision ?? ''}`,
  stopVoice,
  { flush: 'sync' },
)
async function dictate() {
  if (
    !['inbox', 'interrupt', 'stop'].some((capability) =>
      actions.allowed(capability as 'inbox' | 'interrupt' | 'stop'),
    )
  )
    return
  if (mic.isActive.value) {
    mic.finish?.()
    return
  }
  const version = ++voiceGeneration
  voiceController = new AbortController()
  const signal = voiceController.signal
  actions.prepare('simple')
  voiceFeedback.value = 'Listening for a draft. Nothing is sent until you confirm.'
  const started = await mic.start(async (blob) => {
    try {
      const result = await transcribeAgentModeAudio(
        blob,
        locale.value.startsWith('de') ? 'de' : 'en',
        signal,
      )
      if (version !== voiceGeneration || signal.aborted) return
      const command = result.text
        .trim()
        .toLowerCase()
        .replace(/[.!?]+$/, '')
      const control =
        command === 'stop worker' || command === 'worker stoppen'
          ? 'stop'
          : command === 'interrupt worker' || command === 'worker unterbrechen'
            ? 'interrupt'
            : null
      if (control) {
        stopVoice()
        if (actions.allowed(control)) {
          actions.prepare(control)
          voiceFeedback.value =
            'Voice control recognized. Review the selected generation and confirm before any request is sent.'
        } else
          voiceFeedback.value =
            'This worker does not have the required control capability. No request was sent.'
        return
      }
      if (!actions.allowed('inbox')) {
        stopVoice()
        voiceFeedback.value = 'Messaging is not available for this worker. No message was sent.'
        return
      }
      draft.value = `${draft.value} ${result.text}`.trim()
      voiceFeedback.value =
        'Transcript added to the draft. Review the target and press Send message.'
    } catch {
      if (version === voiceGeneration)
        voiceFeedback.value = 'Transcription unavailable. Type a message or retry the microphone.'
    }
  })
  if (!started && version === voiceGeneration)
    voiceFeedback.value = 'Microphone unavailable. Check browser permission or type your message.'
}
defineExpose({ openVoice: dictate })
onScopeDispose(stopVoice)
</script>
<template>
  <span class="habitat-eyebrow">Inspector</span>
  <template v-if="worker">
    <div class="habitat-inspector-head">
      <span class="habitat-orb" :data-state="worker.liveness.state" aria-hidden="true"></span>
      <div>
        <h2>{{ worker.agent.name }}</h2>
        <span class="habitat-pill"
          >{{ humanize(worker.liveness.state) }} · {{ worker.project.key }}</span
        >
      </div>
    </div>
    <section>
      <h3>Work &amp; assignment</h3>
      <dl class="habitat-facts">
        <dt>Ticket</dt>
        <dd>
          <RouterLink
            v-if="worker.ticket?.details_available"
            :to="`/projects/${worker.project.id}/issues/${worker.ticket.id}`"
            >{{ worker.ticket.key }} · {{ worker.ticket.title }}</RouterLink
          ><template v-else>{{
            worker.ticket ? 'Details unavailable' : 'No ticket binding'
          }}</template>
        </dd>
        <dt>Role / shape</dt>
        <dd>{{ humanize(worker.role) }} · {{ humanize(worker.work_shape) }}</dd>
        <dt>Phase</dt>
        <dd>{{ humanize(worker.phase) }}</dd>
        <dt>Parent</dt>
        <dd>
          <button v-if="parent" type="button" @click="emit('selectWorker', parent)">
            {{ parent.agent.name }}</button
          ><template v-else>{{
            worker.parent_harness_session_id ? 'Outside sample' : 'No parent binding'
          }}</template>
        </dd>
        <dt>Children</dt>
        <dd>
          {{ children.length }} in sample
          <button
            v-for="child in children"
            :key="child.harness_session_id"
            type="button"
            @click="emit('selectWorker', child)"
          >
            {{ child.agent.name }}
          </button>
        </dd>

        <dt>Progress</dt>
        <dd>
          {{
            fresh &&
            worker.delivery_trust.progress_trusted &&
            worker.delivery_trust.progress_percent !== null
              ? `${worker.delivery_trust.progress_percent}%`
              : 'Unknown'
          }}
        </dd>
        <dt>ETA</dt>
        <dd>
          {{ fresh && worker.delivery_trust.eta_trusted ? worker.delivery_trust.eta : 'Unknown' }}
        </dd>
      </dl>
      <p>
        {{ humanize(worker.delivery_trust.reason) }} ·
        {{ worker.delivery_trust.observed_at ?? 'No trusted delivery report' }}
      </p>
      <HabitatAssignmentHistory v-if="!isReviewer" :worker="worker" :workers="workers" :authority="authority" />
      <details>
        <summary>Work contract</summary>
        <p>Output: {{ humanize(worker.work_contract.output_kind) }}</p>
        <ul>
          <li v-for="stage in worker.work_contract.stage_applicability" :key="stage.stage">
            {{ humanize(stage.stage) }} · {{ humanize(stage.applicability) }}
          </li>
        </ul>
        <p>
          Definition of done: {{ worker.work_contract.definition_of_done.map(humanize).join(', ') }}
        </p>
      </details>
    </section>
    <section v-if="!isReviewer">
      <h3>Controls</h3>
      <p v-if="!editable">Project edit permission is required to send or control.</p>
      <p v-else-if="!fresh">Refresh current evidence before using controls.</p>
      <p
        v-else-if="
          worker.runtime_provenance_trust !== 'managed_reporter' ||
          ['dead', 'unknown'].includes(worker.liveness.state)
        "
      >
        Ownership or live activity is not proven. Review recovery before controlling this
        generation.
      </p>
      <p v-else>
        Only advertised capabilities are available. Requests remain pending until runtime evidence
        confirms an outcome.
      </p>
      <div class="habitat-actions">
        <button
          type="button"
          :disabled="!actions.allowed('inbox') || busy"
          @click="prepareAction('simple', $event)"
        >
          Message</button
        ><button
          type="button"
          :disabled="!actions.allowed('steer') || busy"
          @click="prepareAction('steer', $event)"
        >
          Steer</button
        ><button
          type="button"
          :disabled="!actions.allowed('interrupt') || busy"
          @click="prepareAction('interrupt', $event)"
        >
          Interrupt</button
        ><button
          type="button"
          class="habitat-danger"
          :disabled="!actions.allowed('stop') || busy"
          @click="prepareAction('stop', $event)"
        >
          Stop</button
        ><button
          type="button"
          class="habitat-danger"
          :disabled="!removal.allowed.value || busy || removal.busy.value"
          @click="removal.begin()"
        >
          Remove worker</button
        ><button type="button" @click="emit('refresh')">Refresh status</button
        ><button
          type="button"
          @click="emit('assign', worker.project.id, worker.harness_session_id)"
        >
          Restart / repair options
        </button>
      </div>
      <button
        type="button"
        :disabled="
          busy ||
          !mic.micSupported() ||
          !['inbox', 'interrupt', 'stop'].some((capability) =>
            actions.allowed(capability as 'inbox' | 'interrupt' | 'stop'),
          )
        "
        @click="dictate"
      >
        {{ mic.isActive.value ? 'Finish worker voice' : 'Voice to selected worker' }}
      </button>
      <p>
        Say “stop worker” or “interrupt worker” to review a control. Other speech becomes a message
        draft. Every destructive voice request requires confirmation.
      </p>
      <p v-if="!action" role="status">{{ voiceFeedback }}</p>
      <details>
        <summary>Capability evidence</summary>
        <dl class="habitat-facts">
          <template v-for="(value, capability) in worker.capabilities" :key="capability"
            ><dt>{{ humanize(capability) }}</dt>
            <dd>{{ value ? 'Advertised' : 'Not advertised' }}</dd></template
          >
        </dl>
      </details>
      <div
        v-if="action"
        class="habitat-form habitat-card"
        role="group"
        :aria-label="`Confirm ${action}`"
      >
        <strong
          >{{ action === 'simple' ? 'Message' : humanize(action) }} ·
          {{ worker.agent.name }}</strong
        ><small
          >{{ worker.project.key }} · generation {{ worker.harness_session_id }} · revision
          {{ worker.revision }}</small
        >
        <template v-if="action === 'simple' || action === 'steer'"
          ><p>
            Address: {{ worker.harness }}:{{ worker.agent.name }}. Delivery addresses the project
            agent; it is not a process execution receipt.
          </p>
          <label
            >Message draft<textarea
              ref="draftInput"
              v-model="draft"
              maxlength="8000"
              :disabled="busy"
            /></label
          ><button type="button" :disabled="!mic.micSupported() || busy" @click="dictate">
            {{ mic.isActive.value ? 'Finish dictation' : 'Dictate message' }}
          </button>
          <p role="status">{{ voiceFeedback }}</p></template
        >
        <p v-else>
          {{
            action === 'stop'
              ? 'Request this owned generation to stop. Unfinished work may be interrupted. Starting again requires a new generation.'
              : 'Request interruption of the current turn. This does not mark the ticket complete.'
          }}
        </p>
        <div class="habitat-actions">
          <button
            ref="confirmButton"
            type="button"
            :disabled="busy || ((action === 'simple' || action === 'steer') && !draft.trim())"
            :class="action === 'stop' ? 'habitat-danger' : 'habitat-primary'"
            @click="submitAction"
          >
            {{
              busy
                ? 'Checking current evidence…'
                : action === 'simple'
                  ? 'Send message'
                  : `Confirm ${action}`
            }}</button
          ><button type="button" :disabled="busy" @click="cancelAction">Cancel</button>
        </div>
      </div>
      <p class="habitat-status" role="status">{{ feedback }}</p>
      <p v-if="removal.feedback.value" class="habitat-status" role="status">
        {{ removal.feedback.value }}
      </p>
      <div v-if="control" class="habitat-card">
        <strong>{{ humanize(control.kind) }} · {{ humanize(control.state) }}</strong>
        <p>
          {{ control.outcome ? humanize(control.outcome) : 'No terminal outcome confirmed'
          }}<template v-if="control.reason"> · {{ humanize(control.reason) }}</template>
        </p>
        <small>Control {{ control.id }}</small
        ><button type="button" :disabled="busy" @click="actions.checkControl()">
          Check control outcome
        </button>
      </div>
      <div v-if="removal.control.value" class="habitat-card">
        <strong
          >Removal {{ humanize(removal.control.value.kind) }} ·
          {{ humanize(removal.control.value.state) }}</strong
        >
        <p>
          {{
            removal.control.value.outcome
              ? humanize(removal.control.value.outcome)
              : 'No terminal outcome confirmed'
          }}<template v-if="removal.control.value.reason">
            · {{ humanize(removal.control.value.reason) }}</template
          >
        </p>
        <small>Control {{ removal.control.value.id }}</small
        ><button type="button" :disabled="removal.busy.value" @click="removal.checkControl()">
          Check removal outcome
        </button>
      </div>
      <HabitatWorkerRemoval v-if="worker" :worker="worker" :removal="removal" />
    </section>
    <section>
      <h3>Owned generation &amp; runtime</h3>
      <dl class="habitat-facts">
        <dt>Generation</dt>
        <dd>{{ worker.harness_session_id }}</dd>
        <dt>Management</dt>
        <dd>{{ humanize(worker.management_mode) }}</dd>
        <dt>Evidence</dt>
        <dd>{{ humanize(worker.runtime_provenance_trust) }}</dd>
        <dt>Harness</dt>
        <dd>{{ worker.harness }}</dd>
        <dt>Model / effort</dt>
        <dd>
          {{
            worker.dispatch_profile
              ? `${worker.dispatch_profile.model} / ${worker.dispatch_profile.effort}`
              : 'Unknown'
          }}
        </dd>
        <dt>Account</dt>
        <dd>{{ humanize(worker.account_label) }}</dd>
        <dt>Machine</dt>
        <dd>{{ worker.machine_id ?? 'Unknown' }}</dd>
        <dt>Workspace</dt>
        <dd>
          {{
            worker.workspace_provenance
              ? `${humanize(worker.workspace_provenance.kind)} / ${worker.workspace_provenance.mode}`
              : 'Unknown'
          }}
        </dd>
        <dt>Activity source</dt>
        <dd>{{ humanize(worker.liveness.source) }}</dd>
        <dt>Observed</dt>
        <dd>{{ worker.liveness.observed_at }}</dd>
        <dt>Reporter age</dt>
        <dd>
          {{
            worker.liveness.reporter_age_seconds === null
              ? 'Unknown'
              : `${worker.liveness.reporter_age_seconds}s at snapshot`
          }}
        </dd>
      </dl>
      <p>
        {{ humanize(worker.liveness.reason)
        }}<template v-if="worker.liveness.closed_reason">
          · {{ humanize(worker.liveness.closed_reason) }}</template
        >
      </p>
    </section>
    <section>
      <h3>Recent communication</h3>
      <p>
        Up to four metadata events. Message contents remain on authorized ticket and session
        surfaces.
      </p>
      <ul v-if="worker.recent_communication.length">
        <li
          v-for="event in worker.recent_communication"
          :key="`${event.message_id}/${event.delivery_id}`"
        >
          <strong>{{ humanize(event.direction) }} · {{ event.occurred_at }}</strong
          ><br />{{ event.requested_level ?? 'Level unknown' }} →
          {{ event.effective_level ?? 'Not confirmed' }} · {{ humanize(event.state)
          }}<span v-if="event.fallback_code"> · {{ humanize(event.fallback_code) }}</span
          ><span v-if="event.error_code"> · {{ humanize(event.error_code) }}</span>
        </li>
      </ul>
      <p v-else>No recent communication evidence in this sample.</p>
      <p>{{ worker.recent_communication_omitted }} communication events omitted.</p>
    </section>
  </template>
  <template v-else-if="project"
    ><h2>{{ project.project.name }}</h2>
    <p>{{ project.project.key }} · Project</p>
    <section>
      <h3>Coordination</h3>
      <p>{{ humanize(project.coordinator.state) }} · {{ humanize(project.coordinator.reason) }}</p>
      <p>Instance coordination is a project relationship, not a parent-session edge.</p>
      <button
        v-if="workers.find((w) => w.harness_session_id === project?.coordinator.session_id)"
        type="button"
        @click="
          emit(
            'selectWorker',
            workers.find((w) => w.harness_session_id === project?.coordinator.session_id)!,
          )
        "
      >
        Inspect coordinator</button
      ><button v-else-if="!isReviewer" type="button" @click="emit('assign', project.project.id)">
        Review coordinator setup
      </button>
    </section>
    <section>
      <h3>Tickets &amp; delivery evidence</h3>
      <p v-if="!projectDeliveries.length">
        No delivery evidence in this authorized snapshot. Portfolio progress and ETA remain unknown.
      </p>
      <article v-for="delivery in projectDeliveries" :key="delivery.id">
        <RouterLink :to="`/projects/${project.project.id}/issues/${delivery.issueId}`"
          >{{ delivery.issueKey }} · {{ delivery.title }}</RouterLink
        >
        <p>{{ humanize(delivery.stage.key) }} · {{ humanize(delivery.health) }}</p>
        <p>
          Progress:
          {{
            fresh && estimatePresentation(delivery).showPercent
              ? `${estimatePresentation(delivery).percent}%`
              : 'Unknown'
          }}
          · ETA:
          {{
            fresh && estimatePresentation(delivery).showEta
              ? estimatePresentation(delivery).rangeOnly
                ? `${estimatePresentation(delivery).optimisticAt} – ${estimatePresentation(delivery).pessimisticAt}`
                : estimatePresentation(delivery).landingAt
              : 'Unknown'
          }}
        </p>
      </article>
    </section>
    <section>
      <h3>Project workspace</h3>
      <div class="habitat-actions">
        <RouterLink class="habitat-button" :to="`/projects/${project.project.id}?tab=knowledge`"
          >Knowledge</RouterLink
        ><RouterLink
          v-if="!isReviewer"
          class="habitat-button"
          :to="{ path: '/', query: { view: 'sessions', project: String(project.project.id) } }"
          >Product sessions &amp; decisions</RouterLink
        ><RouterLink class="habitat-button" :to="`/projects/${project.project.id}`"
          >Tickets</RouterLink
        >
      </div>
    </section></template
  >
  <template v-else
    ><h2>Keep the work in view.</h2>
    <p>Select a worker or project to inspect its assignment, runtime and recent communication.</p>
    <div class="habitat-card">
      <p>
        Every control follows the selected identity and its advertised capabilities. Unknown
        evidence remains unknown.
      </p>
    </div>
    <p>
      Product sessions are durable work intents. Harness generations are owned process records.
      Selecting one never silently selects the other.
    </p></template
  >
</template>
