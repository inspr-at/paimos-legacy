<script setup lang="ts">
// PAI-326 (base) + PAI-329 (rendering shape extensions).
// Project-settings panel — declarable agents per project.
// List rows show name + description + slash-command + lane-tag chips
// with inline add / edit / delete. The PAI-329 fields (body,
// bootstrap_steps, non_negotiable_rules) are exposed via collapsible
// editor sections inside the inline form. Validation mirrors the
// server's rules for fast feedback; the server is the source of
// truth for uniqueness (409 collision is surfaced as a save error).

import { computed, onMounted, ref, watch } from 'vue'
import { api, errMsg } from '@/api/client'
import type {
  AgentActionCapability,
  AgentBootstrapStep,
  AgentRule,
  ProjectAgent,
  ProjectAgentInput,
} from '@/types'
import {
  createProjectAgent,
  deleteProjectAgent,
  listProjectAgents,
  updateProjectAgent,
  validateAgentName,
} from '@/services/projectAgents'
import LoadingText from '@/components/LoadingText.vue'
import { formatInteger } from '@/composables/useNumberFormat'
import { publicURL } from '@/publicPath'

const props = defineProps<{
  projectId: number
  canWrite: boolean
  reviewer?: boolean
}>()

const emit = defineEmits<{
  count: [n: number]
}>()

interface ProjectRunner {
  user_id: number
  device_id: string
  last_seen: string
  actions?: AgentActionCapability[]
}

interface AgentRun {
  id: number
  issue_id: number
  project_id: number | null
  device_id: string
  action_key: string
  provider_label: string
  agent_name: string
  status: string
  version: string
  deploy_target: string
  tests_summary: string | null
  error: string
  created_at: string
  started_at: string | null
  finished_at: string | null
}

const agents = ref<ProjectAgent[]>([])
const runners = ref<ProjectRunner[]>([])
const runs = ref<AgentRun[]>([])
const loading = ref(true)
const loadError = ref('')
const launchpadLoading = ref(false)
const launchpadError = ref('')

// Inline-editor state. `editingName` is the original name of the row
// being edited (or null = adding a new agent / not editing). The form
// holds the working copy; lane_tags is edited as a comma-separated
// string for typing convenience and split on save.
const editingName = ref<string | null>(null)
const adding = ref(false)
const form = ref<ProjectAgentInput>(emptyForm())
const laneTagsInput = ref('')
const saveError = ref('')
const saving = ref(false)

watch(
  () => agents.value.length,
  (n) => emit('count', n),
  { immediate: true },
)

function emptyStep(): AgentBootstrapStep {
  return { title: '', command: '', rationale: '' }
}

function emptyRule(): AgentRule {
  return { title: '', body: '', memory_ref: '' }
}

function emptyForm(): ProjectAgentInput {
  return {
    name: '',
    description: '',
    slash_command_name: '',
    lane_tags: [],
    metadata: {},
    body: '',
    bootstrap_steps: [],
    non_negotiable_rules: [],
  }
}

const nameError = computed(() => validateAgentName(form.value.name))
const isFormValid = computed(() => nameError.value === '')

const STATUS_LABEL: Record<string, string> = {
  queued: 'Queued',
  running: 'Running',
  completed: 'Implemented',
  tests_passed: 'Tests passed',
  tests_failed: 'Tests failed',
  deployed: 'Deployed',
  failed: 'Failed',
  cancelled: 'Cancelled',
}

const onlineRunnerCount = computed(() => runners.value.length)

const adapterLabels = computed(() => {
  const labels = new Map<string, string>()
  for (const runner of runners.value) {
    for (const action of runner.actions ?? []) {
      const label = action.label?.trim() || action.action_key
      if (label) labels.set(action.action_key, label)
    }
  }
  return [...labels.values()]
})

const runnerStatusLabel = computed(() => {
  const count = onlineRunnerCount.value
  if (count === 0) return 'No runner online'
  return `${formatInteger(count)} runner${count === 1 ? '' : 's'} online`
})

function statusLabel(status: string): string {
  return STATUS_LABEL[status] ?? status
}

function artifactJsonHref(agent: ProjectAgent): string {
  return publicURL(`/api/projects/${props.projectId}/agents/${encodeURIComponent(agent.name)}.json`)
}

function artifactMarkdownHref(agent: ProjectAgent): string {
  return publicURL(`/api/projects/${props.projectId}/agents/${encodeURIComponent(agent.name)}.md`)
}

function issueHref(run: AgentRun): string {
  return publicURL(`/projects/${props.projectId}/issues/${run.issue_id}#ai-workbench`)
}

function artifactStatus(agent: ProjectAgent): string {
  if (agent.body || agent.bootstrap_steps?.length || agent.non_negotiable_rules?.length) {
    return 'Artifact ready'
  }
  return 'Artifact shell'
}

function recentRunsFor(agent: ProjectAgent): AgentRun[] {
  return runs.value
    .filter((run) => run.agent_name === agent.name)
    .slice(-3)
    .reverse()
}

function agentCommands(agent: ProjectAgent): string[] {
  return [
    `paimos skill render ${agent.name}`,
    `eval $(paimos session start --project ${props.projectId} --agent ${agent.name})`,
    `paimos run-agent watch --project ${props.projectId} --repo-root .`,
  ]
}

async function loadLaunchpad() {
  launchpadLoading.value = true
  launchpadError.value = ''
  try {
    const [runnerData, runData] = await Promise.all([
      api.get<{ runners: ProjectRunner[] }>(`/projects/${props.projectId}/runners`),
      api.get<{ runs: AgentRun[] }>(`/projects/${props.projectId}/runs`),
    ])
    runners.value = Array.isArray(runnerData.runners) ? runnerData.runners : []
    runs.value = Array.isArray(runData.runs) ? runData.runs : []
  } catch (e) {
    runners.value = []
    runs.value = []
    launchpadError.value = errMsg(e, 'Failed to load runner and run status.')
  } finally {
    launchpadLoading.value = false
  }
}

async function load() {
  loading.value = true
  loadError.value = ''
  const agentsTask = listProjectAgents(props.projectId)
    .then((items) => {
      agents.value = items
    })
    .catch((e) => {
      loadError.value = errMsg(e, 'Failed to load agents.')
    })
    .finally(() => {
      loading.value = false
    })
  await Promise.all([agentsTask, ...(props.reviewer ? [] : [loadLaunchpad()])])
}

function startAdd() {
  editingName.value = null
  adding.value = true
  form.value = emptyForm()
  laneTagsInput.value = ''
  saveError.value = ''
}

function startEdit(agent: ProjectAgent) {
  editingName.value = agent.name
  adding.value = false
  form.value = {
    name: agent.name,
    description: agent.description,
    slash_command_name: agent.slash_command_name,
    lane_tags: [...agent.lane_tags],
    metadata: { ...agent.metadata },
    body: agent.body ?? '',
    // Defensive copies — without them, dropping a step / rule from
    // the editor would mutate the original object in `agents`.
    bootstrap_steps: (agent.bootstrap_steps ?? []).map((s) => ({ ...s })),
    non_negotiable_rules: (agent.non_negotiable_rules ?? []).map((r) => ({ ...r })),
  }
  laneTagsInput.value = agent.lane_tags.join(', ')
  saveError.value = ''
}

function cancelEdit() {
  editingName.value = null
  adding.value = false
  form.value = emptyForm()
  laneTagsInput.value = ''
  saveError.value = ''
}

function parseLaneTags(raw: string): string[] {
  return raw
    .split(',')
    .map((t) => t.trim())
    .filter((t) => t !== '')
}

function addBootstrapStep() {
  form.value.bootstrap_steps.push(emptyStep())
}
function removeBootstrapStep(i: number) {
  form.value.bootstrap_steps.splice(i, 1)
}
function moveBootstrapStep(i: number, delta: number) {
  const arr = form.value.bootstrap_steps
  const j = i + delta
  if (j < 0 || j >= arr.length) return
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

function addRule() {
  form.value.non_negotiable_rules.push(emptyRule())
}
function removeRule(i: number) {
  form.value.non_negotiable_rules.splice(i, 1)
}
function moveRule(i: number, delta: number) {
  const arr = form.value.non_negotiable_rules
  const j = i + delta
  if (j < 0 || j >= arr.length) return
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

async function save() {
  if (!isFormValid.value) {
    saveError.value = nameError.value
    return
  }
  saving.value = true
  saveError.value = ''
  const payload: ProjectAgentInput = {
    ...form.value,
    name: form.value.name.trim(),
    description: form.value.description.trim(),
    slash_command_name: form.value.slash_command_name.trim(),
    lane_tags: parseLaneTags(laneTagsInput.value),
    body: form.value.body,
    bootstrap_steps: form.value.bootstrap_steps
      .map((s) => ({
        title: s.title.trim(),
        command: s.command.trim(),
        rationale: s.rationale.trim(),
      }))
      .filter((s) => s.title || s.command || s.rationale),
    non_negotiable_rules: form.value.non_negotiable_rules
      .map((r) => ({
        title: r.title.trim(),
        body: r.body.trim(),
        memory_ref: r.memory_ref.trim(),
      }))
      .filter((r) => r.title || r.body || r.memory_ref),
  }
  try {
    if (editingName.value === null) {
      await createProjectAgent(props.projectId, payload)
    } else {
      await updateProjectAgent(props.projectId, editingName.value, payload)
    }
    cancelEdit()
    await load()
  } catch (e) {
    saveError.value = errMsg(e, 'Failed to save agent.')
  } finally {
    saving.value = false
  }
}

async function remove(agent: ProjectAgent) {
  if (!confirm(`Remove agent "${agent.name}"?`)) return
  saveError.value = ''
  try {
    await deleteProjectAgent(props.projectId, agent.name)
    if (editingName.value === agent.name) cancelEdit()
    await load()
  } catch (e) {
    saveError.value = errMsg(e, 'Failed to remove agent.')
  }
}

watch(
  () => props.projectId,
  () => {
    cancelEdit()
    void load()
  },
)

onMounted(load)
</script>

<template>
  <section class="pa-section">
    <div class="pa-header">
      <div>
        <h3 class="pa-title">Agents</h3>
        <p class="pa-desc">
          Declarable agents that work this project (e.g. <code>ops</code>, <code>dev</code>,
          <code>refinement</code>). Names follow <code>[a-z][a-z0-9_-]*</code>, max 32 chars.
        </p>
      </div>
      <button
        v-if="canWrite && !adding && editingName === null"
        type="button"
        class="btn btn-ghost btn-sm"
        @click="startAdd"
      >
        Add agent
      </button>
    </div>

    <div v-if="loadError" class="pa-error">{{ loadError }}</div>
    <div v-if="launchpadError" class="pa-error">{{ launchpadError }}</div>
    <LoadingText v-if="loading" class="pa-empty" label="Loading agents…" />

    <div v-else-if="!agents.length && !adding" class="pa-empty">
      No agents declared yet.<template v-if="!reviewer"> Add one before rendering a skill, starting a session, or running the
      watcher.</template>
    </div>

    <div v-else class="pa-list">
      <div v-for="agent in agents" :key="agent.id" class="pa-row">
        <template v-if="editingName === agent.name">
          <div class="pa-form">
            <div class="pa-field">
              <label>Name</label>
              <input v-model="form.name" type="text" maxlength="32" />
              <span v-if="nameError" class="pa-field-error">{{ nameError }}</span>
            </div>
            <div class="pa-field">
              <label>Description</label>
              <input
                v-model="form.description"
                type="text"
                placeholder="What does this agent own?"
              />
            </div>
            <div class="pa-field">
              <label>Slash command</label>
              <input v-model="form.slash_command_name" type="text" placeholder="e.g. ops" />
            </div>
            <div class="pa-field">
              <label>Lane tags <span class="pa-hint">comma-separated</span></label>
              <input v-model="laneTagsInput" type="text" placeholder="ops, infra" />
            </div>

            <details class="pa-collapsible" open>
              <summary>
                Body <span class="pa-hint">markdown — bulk of the rendered skill</span>
              </summary>
              <textarea
                v-model="form.body"
                class="pa-textarea"
                rows="8"
                placeholder="## What this agent owns&#10;&#10;…"
              />
            </details>

            <details class="pa-collapsible">
              <summary>
                Bootstrap steps
                <span class="pa-hint"
                  >{{ formatInteger(form.bootstrap_steps.length) }} step(s)</span
                >
              </summary>
              <div class="pa-repeater">
                <div
                  v-for="(step, i) in form.bootstrap_steps"
                  :key="`bs-${i}`"
                  class="pa-repeater-row"
                >
                  <div class="pa-repeater-head">
                    <span class="pa-repeater-num">{{ i + 1 }}.</span>
                    <button
                      type="button"
                      class="btn btn-ghost btn-sm"
                      @click="moveBootstrapStep(i, -1)"
                      :disabled="i === 0"
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      class="btn btn-ghost btn-sm"
                      @click="moveBootstrapStep(i, 1)"
                      :disabled="i === form.bootstrap_steps.length - 1"
                    >
                      ↓
                    </button>
                    <button
                      type="button"
                      class="btn btn-ghost btn-sm danger"
                      @click="removeBootstrapStep(i)"
                    >
                      Remove
                    </button>
                  </div>
                  <input
                    v-model="step.title"
                    type="text"
                    placeholder="Title — e.g. Source ops env"
                  />
                  <input
                    v-model="step.command"
                    type="text"
                    placeholder="Command — e.g. source ~/Secrets/ops.env"
                    class="pa-mono"
                  />
                  <input v-model="step.rationale" type="text" placeholder="Rationale (optional)" />
                </div>
                <button type="button" class="btn btn-ghost btn-sm" @click="addBootstrapStep">
                  + Add step
                </button>
              </div>
            </details>

            <details class="pa-collapsible">
              <summary>
                Non-negotiable rules
                <span class="pa-hint"
                  >{{ formatInteger(form.non_negotiable_rules.length) }} rule(s)</span
                >
              </summary>
              <div class="pa-repeater">
                <div
                  v-for="(rule, i) in form.non_negotiable_rules"
                  :key="`nr-${i}`"
                  class="pa-repeater-row"
                >
                  <div class="pa-repeater-head">
                    <span class="pa-repeater-num">{{ i + 1 }}.</span>
                    <button
                      type="button"
                      class="btn btn-ghost btn-sm"
                      @click="moveRule(i, -1)"
                      :disabled="i === 0"
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      class="btn btn-ghost btn-sm"
                      @click="moveRule(i, 1)"
                      :disabled="i === form.non_negotiable_rules.length - 1"
                    >
                      ↓
                    </button>
                    <button
                      type="button"
                      class="btn btn-ghost btn-sm danger"
                      @click="removeRule(i)"
                    >
                      Remove
                    </button>
                  </div>
                  <input
                    v-model="rule.title"
                    type="text"
                    placeholder="Title — e.g. No prod writes without PR"
                  />
                  <textarea v-model="rule.body" rows="2" placeholder="Body (optional)" />
                  <input
                    v-model="rule.memory_ref"
                    type="text"
                    placeholder="memory_ref (optional, resolved at render time)"
                    class="pa-mono"
                  />
                </div>
                <button type="button" class="btn btn-ghost btn-sm" @click="addRule">
                  + Add rule
                </button>
              </div>
            </details>

            <div v-if="saveError" class="pa-error">{{ saveError }}</div>
            <div class="pa-actions">
              <button type="button" class="btn btn-ghost btn-sm" @click="cancelEdit">Cancel</button>
              <button
                type="button"
                class="btn btn-primary btn-sm"
                :disabled="!isFormValid || saving"
                @click="save"
              >
                {{ saving ? 'Saving…' : 'Save' }}
              </button>
            </div>
          </div>
        </template>
        <template v-else>
          <div class="pa-row-main">
            <div class="pa-row-head">
              <span class="pa-name">{{ agent.name }}</span>
              <span v-if="agent.slash_command_name" class="pa-slash"
                >/{{ agent.slash_command_name }}</span
              >
            </div>
            <div v-if="agent.description" class="pa-row-desc">{{ agent.description }}</div>
            <div v-if="agent.lane_tags.length" class="pa-chips">
              <span v-for="tag in agent.lane_tags" :key="tag" class="pa-chip">{{ tag }}</span>
            </div>
            <div
              v-if="
                agent.body || agent.bootstrap_steps?.length || agent.non_negotiable_rules?.length
              "
              class="pa-meta"
            >
              <span v-if="agent.body" class="pa-meta-pill">body</span>
              <span v-if="agent.bootstrap_steps?.length" class="pa-meta-pill"
                >{{ formatInteger(agent.bootstrap_steps.length) }} bootstrap step(s)</span
              >
              <span v-if="agent.non_negotiable_rules?.length" class="pa-meta-pill"
                >{{ formatInteger(agent.non_negotiable_rules.length) }} rule(s)</span
              >
            </div>

            <div v-if="!reviewer" class="pa-launchpad" aria-label="Agent launchpad">
              <div class="pa-readiness">
                <span class="pa-ready pa-ready--ok">Active</span>
                <span class="pa-ready pa-ready--ok">{{ artifactStatus(agent) }}</span>
                <span
                  class="pa-ready"
                  :class="onlineRunnerCount > 0 ? 'pa-ready--ok' : 'pa-ready--warn'"
                >
                  {{ runnerStatusLabel }}
                </span>
              </div>

              <div class="pa-artifacts">
                <a :href="artifactJsonHref(agent)" target="_blank" rel="noopener">Artifact JSON</a>
                <a :href="artifactMarkdownHref(agent)" target="_blank" rel="noopener">Markdown</a>
              </div>

              <div class="pa-adapters">
                <span class="pa-launch-label">Adapters</span>
                <span v-if="adapterLabels.length" class="pa-adapter-list">
                  {{ adapterLabels.join(', ') }}
                </span>
                <span v-else class="pa-muted">No compatible adapter online.</span>
              </div>

              <LoadingText
                v-if="launchpadLoading"
                class="pa-launch-loading"
                label="Checking runners…"
              />
              <div class="pa-commands" aria-label="Agent commands">
                <code v-for="command in agentCommands(agent)" :key="command">{{ command }}</code>
              </div>

              <div class="pa-runs">
                <span class="pa-launch-label">Recent runs</span>
                <div v-if="recentRunsFor(agent).length" class="pa-run-list">
                  <a
                    v-for="run in recentRunsFor(agent)"
                    :key="run.id"
                    class="pa-run"
                    :href="issueHref(run)"
                  >
                    <span class="pa-run-status" :class="`pa-run-status--${run.status}`">
                      {{ statusLabel(run.status) }}
                    </span>
                    <span class="pa-run-meta">#{{ run.id }}</span>
                    <span v-if="run.provider_label" class="pa-run-meta">{{
                      run.provider_label
                    }}</span>
                    <span v-if="run.version" class="pa-run-meta">v{{ run.version }}</span>
                  </a>
                </div>
                <span v-else class="pa-muted">No runs yet for this agent.</span>
              </div>
            </div>
          </div>
          <div v-if="canWrite && editingName === null && !adding" class="pa-row-actions">
            <button type="button" class="btn btn-ghost btn-sm" @click="startEdit(agent)">
              Edit
            </button>
            <button type="button" class="btn btn-ghost btn-sm danger" @click="remove(agent)">
              Remove
            </button>
          </div>
        </template>
      </div>

      <div v-if="adding" class="pa-row pa-row--adding">
        <div class="pa-form">
          <div class="pa-field">
            <label>Name <span class="pa-hint">slug, max 32 chars</span></label>
            <input
              v-model="form.name"
              type="text"
              maxlength="32"
              autofocus
              placeholder="e.g. ops"
            />
            <span v-if="nameError" class="pa-field-error">{{ nameError }}</span>
          </div>
          <div class="pa-field">
            <label>Description</label>
            <input v-model="form.description" type="text" placeholder="What does this agent own?" />
          </div>
          <div class="pa-field">
            <label>Slash command</label>
            <input v-model="form.slash_command_name" type="text" placeholder="e.g. ops" />
          </div>
          <div class="pa-field">
            <label>Lane tags <span class="pa-hint">comma-separated</span></label>
            <input v-model="laneTagsInput" type="text" placeholder="ops, infra" />
          </div>

          <details class="pa-collapsible">
            <summary>Body <span class="pa-hint">markdown</span></summary>
            <textarea
              v-model="form.body"
              class="pa-textarea"
              rows="6"
              placeholder="## What this agent owns…"
            />
          </details>
          <details class="pa-collapsible">
            <summary>
              Bootstrap steps
              <span class="pa-hint">{{ formatInteger(form.bootstrap_steps.length) }} step(s)</span>
            </summary>
            <div class="pa-repeater">
              <div
                v-for="(step, i) in form.bootstrap_steps"
                :key="`bsa-${i}`"
                class="pa-repeater-row"
              >
                <div class="pa-repeater-head">
                  <span class="pa-repeater-num">{{ i + 1 }}.</span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm"
                    @click="moveBootstrapStep(i, -1)"
                    :disabled="i === 0"
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm"
                    @click="moveBootstrapStep(i, 1)"
                    :disabled="i === form.bootstrap_steps.length - 1"
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm danger"
                    @click="removeBootstrapStep(i)"
                  >
                    Remove
                  </button>
                </div>
                <input v-model="step.title" type="text" placeholder="Title" />
                <input v-model="step.command" type="text" placeholder="Command" class="pa-mono" />
                <input v-model="step.rationale" type="text" placeholder="Rationale (optional)" />
              </div>
              <button type="button" class="btn btn-ghost btn-sm" @click="addBootstrapStep">
                + Add step
              </button>
            </div>
          </details>
          <details class="pa-collapsible">
            <summary>
              Non-negotiable rules
              <span class="pa-hint"
                >{{ formatInteger(form.non_negotiable_rules.length) }} rule(s)</span
              >
            </summary>
            <div class="pa-repeater">
              <div
                v-for="(rule, i) in form.non_negotiable_rules"
                :key="`nra-${i}`"
                class="pa-repeater-row"
              >
                <div class="pa-repeater-head">
                  <span class="pa-repeater-num">{{ i + 1 }}.</span>
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm"
                    @click="moveRule(i, -1)"
                    :disabled="i === 0"
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm"
                    @click="moveRule(i, 1)"
                    :disabled="i === form.non_negotiable_rules.length - 1"
                  >
                    ↓
                  </button>
                  <button type="button" class="btn btn-ghost btn-sm danger" @click="removeRule(i)">
                    Remove
                  </button>
                </div>
                <input v-model="rule.title" type="text" placeholder="Title" />
                <textarea v-model="rule.body" rows="2" placeholder="Body (optional)" />
                <input
                  v-model="rule.memory_ref"
                  type="text"
                  placeholder="memory_ref (optional)"
                  class="pa-mono"
                />
              </div>
              <button type="button" class="btn btn-ghost btn-sm" @click="addRule">
                + Add rule
              </button>
            </div>
          </details>

          <div v-if="saveError" class="pa-error">{{ saveError }}</div>
          <div class="pa-actions">
            <button type="button" class="btn btn-ghost btn-sm" @click="cancelEdit">Cancel</button>
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="!isFormValid || saving"
              @click="save"
            >
              {{ saving ? 'Adding…' : 'Add agent' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.pa-section {
  display: flex;
  flex-direction: column;
  gap: 0.8rem;
  margin-top: 1rem;
}
.pa-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}
.pa-title {
  font-size: 14px;
  font-weight: 800;
  color: var(--text);
  margin: 0 0 0.15rem;
}
.pa-desc {
  margin: 0;
  color: var(--text-muted);
  font-size: 12px;
}
.pa-desc code {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 0.2rem;
  font-size: 11px;
}
.pa-empty {
  color: var(--text-muted);
  font-size: 13px;
  padding: 0.5rem 0;
}
.pa-error {
  color: #b42318;
  background: #fef3f2;
  border: 1px solid #fecdca;
  border-radius: 8px;
  padding: 0.5rem 0.65rem;
  font-size: 12px;
}
.pa-list {
  display: flex;
  flex-direction: column;
  gap: 0.55rem;
}
.pa-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.8rem;
  padding: 0.65rem 0.8rem;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
}
.pa-row--adding {
  background: var(--bg-card);
  border-style: dashed;
}
.pa-row-main {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.pa-row-head {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
}
.pa-name {
  font-weight: 700;
  font-size: 13px;
  color: var(--text);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.pa-slash {
  color: var(--text-muted);
  font-size: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.pa-row-desc {
  color: var(--text);
  font-size: 13px;
}
.pa-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
  margin-top: 0.15rem;
}
.pa-chip {
  display: inline-block;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 0.1rem 0.55rem;
  font-size: 11px;
  color: var(--text-muted);
}
.pa-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
  margin-top: 0.15rem;
}
.pa-meta-pill {
  display: inline-block;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.05rem 0.4rem;
  font-size: 10px;
  color: var(--text-muted);
}
.pa-launchpad {
  display: flex;
  flex-direction: column;
  gap: 0.45rem;
  margin-top: 0.55rem;
  padding-top: 0.55rem;
  border-top: 1px solid var(--border);
}
.pa-readiness,
.pa-artifacts,
.pa-adapters,
.pa-runs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.35rem 0.5rem;
  min-width: 0;
}
.pa-ready {
  display: inline-flex;
  align-items: center;
  min-height: 22px;
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.1rem 0.45rem;
  font-size: 11px;
  font-weight: 700;
}
.pa-ready--ok {
  color: #067647;
  background: #ecfdf3;
  border-color: #abefc6;
}
.pa-ready--warn {
  color: #b54708;
  background: #fffaeb;
  border-color: #fedf89;
}
.pa-artifacts a {
  color: #175cd3;
  font-size: 12px;
  font-weight: 700;
  text-decoration: none;
}
.pa-artifacts a:hover {
  text-decoration: underline;
}
.pa-launch-label {
  flex: 0 0 auto;
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 800;
  text-transform: uppercase;
  letter-spacing: 0;
}
.pa-adapter-list {
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
}
.pa-muted {
  color: var(--text-muted);
  font-size: 12px;
}
.pa-launch-loading {
  color: var(--text-muted);
  font-size: 12px;
  padding: 0;
}
.pa-commands {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 0.3rem;
}
.pa-commands code {
  display: block;
  overflow-x: auto;
  white-space: nowrap;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.3rem 0.45rem;
  color: var(--text);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
}
.pa-run-list {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
  min-width: 0;
}
.pa-run {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  min-height: 24px;
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.1rem 0.45rem;
  background: var(--bg-card);
  color: var(--text);
  text-decoration: none;
  font-size: 11px;
}
.pa-run:hover {
  border-color: #84caff;
}
.pa-run-status {
  font-weight: 800;
}
.pa-run-status--queued,
.pa-run-status--running {
  color: #175cd3;
}
.pa-run-status--completed,
.pa-run-status--tests_passed,
.pa-run-status--deployed {
  color: #067647;
}
.pa-run-status--tests_failed,
.pa-run-status--failed {
  color: #b42318;
}
.pa-run-status--cancelled {
  color: var(--text-muted);
}
.pa-run-meta {
  color: var(--text-muted);
}
.pa-row-actions {
  display: flex;
  gap: 0.35rem;
  align-items: flex-start;
}
.pa-form {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 0.55rem;
}
.pa-field {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}
.pa-field label {
  font-size: 12px;
  color: var(--text-muted);
  font-weight: 600;
}
.pa-field input {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
  padding: 0.45rem 0.55rem;
}
.pa-field-error {
  color: #b42318;
  font-size: 11px;
}
.pa-hint {
  color: var(--text-muted);
  font-weight: 400;
  font-size: 11px;
}
.pa-actions {
  display: flex;
  gap: 0.4rem;
  justify-content: flex-end;
}
.pa-collapsible {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.35rem 0.55rem;
  background: var(--bg-card);
}
.pa-collapsible > summary {
  cursor: pointer;
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  display: flex;
  gap: 0.4rem;
  align-items: center;
}
.pa-collapsible > summary::-webkit-details-marker {
  display: none;
}
.pa-textarea {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
  padding: 0.45rem 0.55rem;
  margin-top: 0.35rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.pa-mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.pa-repeater {
  display: flex;
  flex-direction: column;
  gap: 0.55rem;
  margin-top: 0.35rem;
}
.pa-repeater-row {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  padding: 0.45rem 0.55rem;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
}
.pa-repeater-row input,
.pa-repeater-row textarea {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
  padding: 0.35rem 0.5rem;
}
.pa-repeater-head {
  display: flex;
  gap: 0.3rem;
  align-items: center;
}
.pa-repeater-num {
  font-weight: 700;
  color: var(--text-muted);
  font-size: 12px;
  min-width: 1.4em;
}

@media (max-width: 700px) {
  .pa-header,
  .pa-row {
    flex-direction: column;
    align-items: stretch;
  }
  .pa-row-actions {
    justify-content: flex-end;
  }
}
</style>
