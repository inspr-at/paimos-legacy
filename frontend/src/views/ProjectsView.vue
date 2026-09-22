<script setup lang="ts">
import LoadingText from "@/components/LoadingText.vue";
import { ref, onMounted, computed, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'

import AppModal from '@/components/AppModal.vue'
import AppIcon from '@/components/AppIcon.vue'
import ImportCollisionModal from '@/components/ImportCollisionModal.vue'
import type { PreflightResult, CollisionStrategy } from '@/components/ImportCollisionModal.vue'
import { api, csrfHeaders, errMsg } from '@/api/client'
import { publicURL } from '@/publicPath'
import { useAuthStore } from '@/stores/auth'
import TagChip from '@/components/TagChip.vue'
import type { ProjectStatus, Tag } from '@/types'
// PAI-146 expansion: AI optimize on the new-project description.
import AiActionMenu from '@/components/ai/AiActionMenu.vue'
import AiSurfaceFeedback from '@/components/ai/AiSurfaceFeedback.vue'
import {
  ACCRUALS_DEFAULT_STATUSES as ACCRUALS_DEFAULTS,
  ACCRUALS_EXTRA_STATUSES   as ACCRUALS_EXTRAS,
} from '@/constants/status'
import { formatDecimal, formatInteger } from '@/composables/useNumberFormat'
import { fmtRelative } from '@/utils/formatTime'

const router = useRouter()

export interface Project {
  id: number
  name: string
  key: string
  description: string
  status: ProjectStatus
  product_owner: number | null
  customer_label: string
  customer_id: number | null
  customer_name?: string
  created_at: string
  updated_at: string
  issue_count: number
  logo_path: string
  last_activity: string
  open_issue_count: number
  done_issue_count: number
  active_issue_count: number
  tags: Tag[]
}

const auth = useAuthStore()
const isAdmin = computed(() => auth.isAdmin)

const statusFilter = ref<ProjectStatus>('active')
const projects = ref<Project[]>([])
const loading = ref(true)

// PAI-63. Customer filter dropdown. Special values:
//   '' (empty)        — show all
//   '__unassigned__'  — projects with no customer FK
//   '<numeric id>'    — that specific customer
const customerFilter = ref<string>('')
const customers = ref<Array<{ id: number; name: string }>>([])

async function loadCustomers() {
  if (auth.user?.role === 'reviewer') return
  try {
    customers.value = (await api.get<Array<{ id: number; name: string }>>('/customers'))
      .map(c => ({ id: c.id, name: c.name }))
  } catch { /* non-admin or no customers — fine */ }
}

const showCreate = ref(false)
const form = ref({ name: '', key: '', description: '' })
const formError = ref('')
const keyError = ref('')

// PAI-146 expansion: AI optimize on project description.
function onProjectDescriptionAccept(text: string) {
  form.value.description = text
}
async function applyProjectCreateAiResult(info: any) {
  if (info.action === 'ui_generation') {
    const spec = String(info.body?.spec_markdown ?? '')
    form.value.description = info.intent === 'replace-description'
      ? spec
      : [form.value.description, spec].filter(Boolean).join('\n\n')
    return
  }
  if (info.action === 'suggest_enhancement') {
    const lines = (info.selection ?? []).map((idx: number) => info.body?.suggestions?.[idx]).filter(Boolean).map((it: any) => `- ${it.title}: ${it.body}`)
    form.value.description = [form.value.description, lines.join('\n')].filter(Boolean).join('\n\n')
    return
  }
  if (info.action === 'spec_out') {
    const lines = (info.selection ?? []).map((idx: number) => info.body?.items?.[idx]?.text).filter(Boolean).map((text: string) => `- ${text}`)
    form.value.description = [form.value.description, lines.join('\n')].filter(Boolean).join('\n\n')
  }
}
const saving = ref(false)
const keySuggesting = ref(false)

const KEY_RE = /^[A-Z][A-Z0-9]{2,9}$/

function validateKey(key: string): string {
  if (!key) return ''
  if (!/^[A-Z]/.test(key)) return 'Must start with a letter.'
  if (key.length < 3) return 'Min 3 characters.'
  if (key.length > 10) return 'Max 10 characters.'
  if (!KEY_RE.test(key)) return 'Uppercase letters and digits only.'
  return ''
}

function onKeyInput(e: Event) {
  const val = (e.target as HTMLInputElement).value.toUpperCase().replace(/[^A-Z0-9]/g, '')
  form.value.key = val
  keyError.value = validateKey(val)
}

async function load() {
  loading.value = true
  projects.value = await api.get<Project[]>(`/projects?status=${statusFilter.value}`)
  loading.value = false
}

onMounted(() => {
  load()
  loadCustomers()
})

const filteredProjects = computed(() => {
  if (!customerFilter.value) return projects.value
  if (customerFilter.value === '__unassigned__') {
    return projects.value.filter(p => p.customer_id == null)
  }
  const id = Number(customerFilter.value)
  return projects.value.filter(p => p.customer_id === id)
})

// Auto-suggest key when name changes (only if user hasn't typed a key)
watch(() => form.value.name, async (name) => {
  if (!name || form.value.key) return
  keySuggesting.value = true
  try {
    const res = await api.get<{ key: string }>(`/projects/suggest-key?name=${encodeURIComponent(name)}`)
    if (!form.value.key) {
      form.value.key = res.key
      keyError.value = validateKey(res.key)
    }
  } finally {
    keySuggesting.value = false
  }
})

async function createProject() {
  formError.value = ''
  if (!form.value.name.trim()) { formError.value = 'Name required.'; return }
  const ke = validateKey(form.value.key)
  if (ke) { keyError.value = ke; return }
  saving.value = true
  try {
    const p = await api.post<Project>('/projects', form.value)
    projects.value.unshift(p)
    showCreate.value = false
    form.value = { name: '', key: '', description: '' }
  } catch (e: unknown) {
    formError.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

function relativeTime(ts: string): string {
  return ts ? fmtRelative(ts) : ''
}

async function archiveProject(p: Project) {
  const newStatus = p.status === 'active' ? 'archived' : 'active'
  await api.put(`/projects/${p.id}`, { status: newStatus })
  await load()
}

const deleteProjectTarget = ref<Project | null>(null)
const deletingProject = ref(false)

async function confirmDeleteProject() {
  if (!deleteProjectTarget.value) return
  deletingProject.value = true
  try {
    await api.delete(`/projects/${deleteProjectTarget.value.id}`)
    projects.value = projects.value.filter(x => x.id !== deleteProjectTarget.value!.id)
    deleteProjectTarget.value = null
  } catch (e: unknown) { /* swallow */ }
  finally { deletingProject.value = false }
}

// ── Global CSV import ──────────────────────────────────────────────────────────
const globalImportRef      = ref<HTMLInputElement | null>(null)
const globalPreflight      = ref<PreflightResult | null>(null)
const globalImportLoading  = ref(false)
const globalImportError    = ref('')
const globalImportResult   = ref<{ imported: number; updated: number; skipped: number; errors: string[]; project_id: number; project_key: string } | null>(null)
const showCollisionModal   = ref(false)
const pendingImportFile    = ref<File | null>(null)

function triggerGlobalImport() {
  globalImportError.value = ''
  globalImportResult.value = null
  globalImportRef.value?.click()
}

async function onGlobalImportFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  pendingImportFile.value = file
  globalImportLoading.value = true
  globalImportError.value = ''
  try {
    const fd = new FormData()
    fd.append('file', file)
    const resp = await fetch(publicURL('/api/import/csv/preflight'), { method: 'POST', credentials: 'include', headers: csrfHeaders(), body: fd })
    const data = await resp.json()
    if (!resp.ok) { globalImportError.value = data.error ?? 'Preflight failed.'; return }
    globalPreflight.value = data
    showCollisionModal.value = true
  } catch (e: unknown) {
    globalImportError.value = errMsg(e, 'Preflight failed.')
  } finally {
    globalImportLoading.value = false
    if (globalImportRef.value) globalImportRef.value.value = ''
  }
}

async function onImportConfirm(strategy: CollisionStrategy, projectName: string) {
  if (!pendingImportFile.value) return
  showCollisionModal.value = false
  globalImportLoading.value = true
  globalImportError.value = ''
  try {
    const fd = new FormData()
    fd.append('file', pendingImportFile.value)
    fd.append('strategy', strategy)
    if (projectName) fd.append('project_name', projectName)
    const resp = await fetch(publicURL('/api/import/csv'), { method: 'POST', credentials: 'include', headers: csrfHeaders(), body: fd })
    const data = await resp.json()
    if (!resp.ok) { globalImportError.value = data.error ?? 'Import failed.'; return }
    globalImportResult.value = data
    await load()
    // Navigate to the project
    if (data.project_id) router.push(`/projects/${data.project_id}`)
  } catch (e: unknown) {
    globalImportError.value = errMsg(e, 'Import failed.')
  } finally {
    globalImportLoading.value = false
    pendingImportFile.value = null
  }
}

// ── Accruals report ──────────────────────────────────────────────
// Admin-only feature gated by user pref `accruals_stats_enabled`.
// Refined "ledger" aesthetic: tabular numerals, hairline rules, restraint.
const accrualsEnabled = computed(() => isAdmin.value && !!auth.user?.accruals_stats_enabled)

// QRL preset ranges. All inclusive on both ends.
type QrlKey = 'this-month' | 'last-month' | 'this-quarter' | 'last-quarter' | 'ytd' | 'last-year' | 'custom'
function rangeFor(key: QrlKey): { from: string; to: string } {
  const fmt = (d: Date) => `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`
  const now = new Date()
  const y = now.getFullYear()
  const m = now.getMonth()
  switch (key) {
    case 'this-month':   return { from: fmt(new Date(y, m, 1)),         to: fmt(new Date(y, m+1, 0)) }
    case 'last-month':   return { from: fmt(new Date(y, m-1, 1)),       to: fmt(new Date(y, m, 0)) }
    case 'this-quarter': { const qs = Math.floor(m/3)*3; return { from: fmt(new Date(y, qs, 1)), to: fmt(new Date(y, qs+3, 0)) } }
    case 'last-quarter': { const qs = Math.floor(m/3)*3 - 3; return { from: fmt(new Date(y, qs, 1)), to: fmt(new Date(y, qs+3, 0)) } }
    case 'ytd':          return { from: fmt(new Date(y, 0, 1)),         to: fmt(new Date(y, m, 0)) }
    case 'last-year':    return { from: fmt(new Date(y-1, 0, 1)),       to: fmt(new Date(y-1, 11, 31)) }
    case 'custom':       return { from: '', to: '' }
  }
}

const QRL_PRESETS: { key: Exclude<QrlKey, 'custom'>; label: string }[] = [
  { key: 'this-month',   label: 'Dieser Monat' },
  { key: 'last-month',   label: 'Letzter Monat' },
  { key: 'this-quarter', label: 'Dieses Quartal' },
  { key: 'last-quarter', label: 'Letztes Quartal' },
  { key: 'ytd',          label: 'Lfd. Jahr' },
  { key: 'last-year',    label: 'Letztes Jahr' },
]

// German status labels for the accruals zone
const STATUS_DE: Record<string, string> = {
  'new':         'Neu',
  'backlog':     'Backlog',
  'in-progress': 'In Arbeit',
  'qa':          'QA',
  'done':        'Erledigt',
  'delivered':   'Geliefert',
  'accepted':    'Akzeptiert',
  'invoiced':    'Verrechnet',
  'cancelled':   'Storniert',
}
function statusDe(s: string): string { return STATUS_DE[s] ?? s }

const ytdInitial = rangeFor('ytd')
const accrualsFrom = ref(ytdInitial.from)
const accrualsTo   = ref(ytdInitial.to)
const accrualsExtras = ref<string[]>([])
const accrualsRows = ref<Record<number, Record<string, number>>>({})
const accrualsLoading = ref(false)
const accrualsError = ref('')
const accrualsCopied = ref(false)

interface AccrualsApiRow { project_id: number; project_key: string; project_name: string; totals: Record<string, number> }
interface AccrualsApiResp { from: string; to: string; statuses: string[]; rows: AccrualsApiRow[] }

const accrualsColumns = computed<string[]>(() =>
  [...ACCRUALS_DEFAULTS, ...ACCRUALS_EXTRAS.filter(s => accrualsExtras.value.includes(s))]
)

const activeQrl = computed<QrlKey>(() => {
  for (const p of QRL_PRESETS) {
    const r = rangeFor(p.key)
    if (r.from === accrualsFrom.value && r.to === accrualsTo.value) return p.key
  }
  return 'custom'
})

function applyQrl(key: Exclude<QrlKey, 'custom'>) {
  const r = rangeFor(key)
  accrualsFrom.value = r.from
  accrualsTo.value = r.to
}

function initAccrualsExtrasFromUser() {
  const raw = auth.user?.accruals_extra_statuses ?? ''
  accrualsExtras.value = raw.split(',').map(s => s.trim()).filter(s => (ACCRUALS_EXTRAS as readonly string[]).includes(s))
}

async function loadAccruals() {
  if (!accrualsEnabled.value) return
  accrualsLoading.value = true
  accrualsError.value = ''
  try {
    const r = await api.get<AccrualsApiResp>(`/reports/accruals?from=${accrualsFrom.value}&to=${accrualsTo.value}`)
    const m: Record<number, Record<string, number>> = {}
    for (const row of r.rows) m[row.project_id] = row.totals
    accrualsRows.value = m
  } catch (e: unknown) {
    accrualsError.value = errMsg(e, 'Failed to load accruals.')
  } finally {
    accrualsLoading.value = false
  }
}

async function toggleAccrualsExtra(status: string) {
  const idx = accrualsExtras.value.indexOf(status)
  if (idx >= 0) accrualsExtras.value.splice(idx, 1)
  else accrualsExtras.value.push(status)
  try {
    await api.patch('/auth/me', { accruals_extra_statuses: accrualsExtras.value.join(',') })
    if (auth.user) auth.user.accruals_extra_statuses = accrualsExtras.value.join(',')
  } catch { /* best effort */ }
}

function fmtHours(n: number | undefined): string {
  if (n == null || n === 0) return '—'
  return formatDecimal(n, 1)
}

function statusLabel(s: string): string {
  return statusDe(s)
}

// Copy as TSV → clipboard. Paste straight into Google Sheets / Excel.
async function copyAsTsv() {
  const cols = accrualsColumns.value
  const header = ['Kürzel', 'Projekt', ...cols.map(statusLabel), 'Summe'].join('\t')
  const lines: string[] = [
    `Vorratsbuch — Projektvorräte`,
    `Zeitraum\t${accrualsFrom.value} — ${accrualsTo.value}`,
    ``,
    header,
  ]
  let grand = 0
  const tsvDecimal = (value: number) => formatDecimal(value, 1, 'en-US').replace(/,/g, '')
  for (const p of projects.value) {
    const totals = accrualsRows.value[p.id] ?? {}
    let row = `${p.key}\t${p.name}`
    let rowTotal = 0
    for (const c of cols) {
      const v = totals[c] ?? 0
      row += `\t${tsvDecimal(v)}`
      if (c !== 'cancelled') rowTotal += v
    }
    row += `\t${tsvDecimal(rowTotal)}`
    grand += rowTotal
    lines.push(row)
  }
  lines.push(`\tGESAMT${'\t'.repeat(cols.length)}\t${tsvDecimal(grand)}`)
  try {
    await navigator.clipboard.writeText(lines.join('\n'))
    accrualsCopied.value = true
    setTimeout(() => { accrualsCopied.value = false }, 2400)
  } catch {
    accrualsError.value = 'Clipboard write failed.'
  }
}

function openPrintView() {
  const url = `/projects/accruals/print?from=${accrualsFrom.value}&to=${accrualsTo.value}&extras=${accrualsExtras.value.join(',')}`
  window.open(url, '_blank', 'noopener')
}

watch(accrualsEnabled, (v) => {
  if (v) {
    initAccrualsExtrasFromUser()
    loadAccruals()
  }
})
watch([accrualsFrom, accrualsTo], () => {
  if (accrualsEnabled.value) loadAccruals()
})

onMounted(() => {
  if (accrualsEnabled.value) {
    initAccrualsExtrasFromUser()
    loadAccruals()
  }
})
</script>

<template>
    <Teleport defer to="#app-header-left">
      <span class="ah-title">Projects</span>
    </Teleport>
    <Teleport defer to="#app-header-right">
      <select
        v-if="customers.length"
        v-model="customerFilter"
        class="pv-customer-filter"
        title="Filter by customer"
      >
        <option value="">All customers</option>
        <option value="__unassigned__">— Unassigned</option>
        <option v-for="c in customers" :key="c.id" :value="String(c.id)">{{ c.name }}</option>
      </select>
      <div class="segmented">
        <button :class="['seg-btn', { active: statusFilter === 'active' }]" @click="statusFilter='active'; load()">Active</button>
        <button :class="['seg-btn', { active: statusFilter === 'frozen' }]" @click="statusFilter='frozen'; load()">Frozen</button>
        <button :class="['seg-btn', { active: statusFilter === 'archived' }]" @click="statusFilter='archived'; load()">Archived</button>
        <button v-if="isAdmin" :class="['seg-btn', { active: statusFilter === 'deleted' }]" @click="statusFilter='deleted'; load()">Deleted</button>
      </div>
      <template v-if="isAdmin">
        <button class="btn btn-ghost btn-sm" :disabled="globalImportLoading" @click="triggerGlobalImport">
          <AppIcon name="upload" :size="13" />
          {{ globalImportLoading ? 'Importing…' : 'Import project' }}
        </button>
        <input ref="globalImportRef" type="file" accept=".csv" style="display:none" @change="onGlobalImportFile" />
      </template>
      <button v-if="isAdmin" class="btn btn-primary btn-sm" @click="showCreate=true">+ New project</button>
    </Teleport>

    <!-- ── Vorräte / Accruals subheader ────────────────────────── -->
    <div v-if="accrualsEnabled" class="accruals-zone accruals-bar">
      <div class="accruals-bar-inner">
        <div class="accruals-bar-section accruals-bar-section--brand">
          <span class="accruals-eyebrow">Vorräte</span>
          <span class="accruals-title">Vorratsbuch</span>
          <span class="accruals-period">{{ accrualsFrom }} → {{ accrualsTo }}</span>
        </div>

        <div class="accruals-bar-section accruals-bar-section--qrl">
          <button
            v-for="p in QRL_PRESETS" :key="p.key"
            class="qrl-chip"
            :class="{ 'qrl-chip--active': activeQrl === p.key }"
            @click="applyQrl(p.key)"
          >{{ p.label }}</button>
          <span class="qrl-divider">|</span>
          <span class="qrl-custom-wrap">
            <input v-model="accrualsFrom" type="date" class="qrl-date" />
            <span class="qrl-arrow">→</span>
            <input v-model="accrualsTo" type="date" class="qrl-date" />
          </span>
        </div>

        <div class="accruals-bar-section accruals-bar-section--actions">
          <button class="ledger-btn" :class="{ 'ledger-btn--ok': accrualsCopied }" :disabled="accrualsLoading" @click="copyAsTsv">
            <AppIcon :name="accrualsCopied ? 'check' : 'copy'" :size="12" />
            {{ accrualsCopied ? 'Kopiert' : 'TSV kopieren' }}
          </button>
          <button class="ledger-btn ledger-btn--ghost" @click="openPrintView">
            <AppIcon name="printer" :size="12" />
            Drucken
          </button>
        </div>
      </div>

      <!-- Status columns: defaults pinned, extras toggle inline -->
      <div class="accruals-bar-statuses">
        <span class="accruals-status-eyebrow">Spalten</span>
        <span v-for="s in ACCRUALS_DEFAULTS" :key="s" class="status-tag status-tag--pinned">
          <span class="status-tag-dot" :class="`status-dot--${s}`"></span>
          {{ statusLabel(s) }}
        </span>
        <span class="status-tag-divider"></span>
        <button
          v-for="s in ACCRUALS_EXTRAS" :key="s"
          class="status-tag status-tag--toggle"
          :class="{ 'status-tag--on': accrualsExtras.includes(s) }"
          @click="toggleAccrualsExtra(s)"
        >
          <span class="status-tag-dot" :class="`status-dot--${s}`"></span>
          {{ statusLabel(s) }}
        </button>
      </div>
    </div>

    <!-- Import result / error banners -->
    <div v-if="globalImportError" class="import-banner import-banner-error">
      {{ globalImportError }}
      <button class="banner-dismiss" @click="globalImportError=''"><AppIcon name="x" :size="14" /></button>
    </div>

    <LoadingText v-if="loading" class="loading" label="Loading…" />

    <div v-else-if="projects.length === 0" class="empty-state">
      <p>No {{ statusFilter }} projects.</p>
      <button v-if="isAdmin && statusFilter === 'active'" class="btn btn-primary" @click="showCreate=true">Create first project</button>
    </div>

    <div v-else class="project-grid">
      <div v-for="p in filteredProjects" :key="p.id" class="project-card" @click="router.push(`/projects/${p.id}`)">
        <div class="project-card-top">
          <span class="project-key-badge">{{ p.key }}</span>
          <span v-if="p.status !== 'active'" :class="`badge badge-${p.status}`">{{ p.status }}</span>
          <img v-if="p.logo_path" :src="publicURL(p.logo_path)" class="project-card-logo" :alt="p.name" />
        </div>
        <div class="project-card-name">{{ p.name }}</div>
        <RouterLink
          v-if="p.customer_id && p.customer_name"
          :to="`/customers/${p.customer_id}`"
          class="project-card-customer"
          @click.stop
          :title="`Customer: ${p.customer_name}`"
        >
          <AppIcon name="building-2" :size="11" />
          <span>{{ p.customer_name }}</span>
        </RouterLink>
        <p class="project-card-desc">{{ p.description || '' }}</p>
        <div v-if="p.tags?.length" class="project-card-tags">
          <TagChip v-for="t in p.tags" :key="t.id" :tag="t" />
        </div>
        <div v-if="accrualsEnabled" class="accruals-zone ledger-row" @click.stop>
          <div v-for="col in accrualsColumns" :key="col" class="ledger-cell" :class="`ledger-cell--${col}`">
            <span class="ledger-cell-label">{{ statusLabel(col) }}</span>
            <span class="ledger-cell-value">
              <template v-if="(accrualsRows[p.id]?.[col] ?? 0) > 0">
                {{ fmtHours(accrualsRows[p.id]?.[col]) }}<span class="ledger-cell-unit">h</span>
              </template>
              <template v-else>
                <span class="ledger-cell-zero">—</span>
              </template>
            </span>
          </div>
        </div>
        <div v-if="p.active_issue_count > 0" class="project-progress">
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: `${Math.round((p.done_issue_count / p.active_issue_count) * 100)}%` }"></div>
          </div>
          <span class="progress-label">{{ formatInteger(p.done_issue_count) }}/{{ formatInteger(p.active_issue_count) }}</span>
        </div>
        <div class="project-card-footer">
          <span class="project-issues">{{ formatInteger(p.open_issue_count) }} open</span>
          <span v-if="p.last_activity" class="project-activity">{{ relativeTime(p.last_activity) }}</span>
        </div>
      </div>
    </div>

    <!-- Import collision modal -->
    <ImportCollisionModal
      :open="showCollisionModal"
      :preflight="globalPreflight"
      @confirm="onImportConfirm"
      @cancel="showCollisionModal=false; pendingImportFile=null"
    />

    <!-- Delete project modal -->
    <AppModal title="Delete Project" :open="!!deleteProjectTarget" @close="deleteProjectTarget=null" confirm-key="d" @confirm="confirmDeleteProject">
      <p style="font-size:14px;color:var(--text);margin-bottom:1.25rem">
        Soft-delete <strong>{{ deleteProjectTarget?.name }}</strong>? The project and all its issues will be hidden from the UI. All data is preserved and can be restored via database update.
      </p>
      <div style="display:flex;justify-content:flex-end;gap:.5rem">
        <button class="btn btn-ghost" @click="deleteProjectTarget=null"><u>C</u>ancel</button>
        <button class="btn btn-danger" :disabled="deletingProject" @click="confirmDeleteProject"><template v-if="deletingProject">Deleting…</template><template v-else><u>D</u>elete project</template></button>
      </div>
    </AppModal>

    <!-- Create modal -->
    <AppModal title="New Project" :open="showCreate" @close="showCreate=false; form={name:'',key:'',description:''}; keyError=''; formError=''">
      <form @submit.prevent="createProject" class="form">
        <div class="field">
          <label>Name</label>
          <input v-model="form.name" type="text" placeholder="Project name" required autofocus />
        </div>
        <div class="field">
          <label>Key <span class="label-hint">— used in issue IDs, e.g. ACME-3 · 3–10 chars, letters &amp; digits, starts with letter</span></label>
          <input
            :value="form.key"
            @input="onKeyInput"
            type="text"
            placeholder="e.g. BPM26"
            maxlength="10"
            :class="{ 'input-error': keyError }"
            style="text-transform:uppercase; font-family: monospace;"
          />
          <span v-if="keyError" class="field-error">{{ keyError }}</span>
        </div>
        <div class="field">
          <div class="field-label-row">
            <label>Description</label>
            <AiActionMenu surface="issue"
              host-key="projects-create:description"
              field="project_description"
              field-label="Project description"
              :issue-id="0"
              :text="() => form.description"
              :on-accept="onProjectDescriptionAccept"
            />
          </div>
          <textarea v-model="form.description" rows="3" placeholder="Optional description"></textarea>
          <AiSurfaceFeedback host-key="projects-create:description" :apply="applyProjectCreateAiResult" />
        </div>
        <div v-if="formError" class="form-error">{{ formError }}</div>
        <div class="form-actions">
          <button type="button" class="btn btn-ghost" @click="showCreate=false">Cancel</button>
          <button type="submit" class="btn btn-primary" :disabled="saving">
            {{ saving ? 'Creating…' : 'Create project' }}
          </button>
        </div>
      </form>
    </AppModal>

</template>

<style scoped>
/* PAI-146: per-field label row holds the label + the AI optimize button. */
.field-label-row {
  display: flex; align-items: center; justify-content: space-between;
  gap: .5rem;
}
.field-label-row > label { margin-bottom: 0; }

.loading { color: var(--text-muted); padding: 2rem 0; }

.import-banner {
  display: flex; align-items: center; gap: .5rem;
  font-size: 13px; padding: .6rem 1rem; border-radius: var(--radius);
  margin-bottom: .75rem;
}
.import-banner-error { background: #fde8e8; color: #c0392b; }
.banner-dismiss { margin-left: auto; background: none; border: none; font-size: 16px; cursor: pointer; color: inherit; }

.header-actions { display: flex; align-items: center; gap: .75rem; }

.segmented { display: flex; border: 1px solid var(--border); border-radius: var(--radius); overflow: hidden; }
.seg-btn {
  background: none; border: none;
  padding: .4rem .85rem; font-size: 13px; font-weight: 500;
  color: var(--text-muted); cursor: pointer;
  transition: background .12s, color .12s;
}
.seg-btn.active { background: var(--brand-blue); color: #fff; }
.seg-btn:not(.active):hover { background: var(--bg); }

.empty-state {
  text-align: center; padding: 4rem 2rem;
  color: var(--text-muted); display: flex; flex-direction: column; align-items: center; gap: 1rem;
}

.project-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 1rem;
}

.project-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 1.25rem;
  box-shadow: var(--shadow);
  display: flex; flex-direction: column; gap: .55rem;
  cursor: pointer;
  transition: background .18s ease, box-shadow .18s ease, border-color .18s ease, transform .18s ease;
}
.project-card:hover {
  background: #f4f7ff;
  box-shadow: 0 4px 18px rgba(46, 109, 200, .12);
  border-color: #c5d5f5;
  transform: translateY(-1px);
}

.project-card-top {
  display: flex; align-items: center; gap: .45rem;
}
.project-card-logo {
  width: 28px; height: 28px; object-fit: contain; border-radius: 4px; margin-left: auto; flex-shrink: 0;
}
.project-key-badge {
  font-size: 11px; font-weight: 700; letter-spacing: .07em; font-family: monospace;
  background: var(--brand-blue); color: #fff;
  padding: .2rem .55rem; border-radius: 5px;
  flex-shrink: 0;
}
.project-card-name {
  font-size: 15px; font-weight: 600; color: var(--text); line-height: 1.3;
}
.project-card-desc   {
  font-size: 13px; color: var(--text-muted); line-height: 1.5; min-height: 0;
  display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden;
}
.project-card-tags   { display: flex; flex-wrap: wrap; gap: .3rem; }
.project-progress { display: flex; align-items: center; gap: .5rem; }
.progress-bar { flex: 1; height: 4px; background: var(--border); border-radius: 2px; overflow: hidden; }
.progress-fill { height: 100%; background: var(--brand-blue); border-radius: 2px; transition: width .3s ease; }
.progress-label { font-size: 10px; color: var(--text-muted); font-weight: 600; white-space: nowrap; font-variant-numeric: tabular-nums; }
.project-card-footer { display: flex; align-items: center; justify-content: space-between; margin-top: .1rem; }
.project-issues  { font-size: 12px; color: var(--text-muted); }
.project-activity { font-size: 11px; color: var(--text-muted); opacity: .7; }

.btn-sm { padding: .3rem .65rem; font-size: 12px; }

/* Form */
.form { display: flex; flex-direction: column; gap: 1rem; }
.field { display: flex; flex-direction: column; gap: .35rem; }
.field label { font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: .05em; }
.label-hint { font-weight: 400; text-transform: none; letter-spacing: 0; font-size: 11px; }
.form-error   { font-size: 13px; color: #c0392b; background: #fde8e8; padding: .5rem .75rem; border-radius: var(--radius); }
.field-error  { font-size: 11px; color: #c0392b; margin-top: .15rem; }
.input-error  { border-color: #c0392b !important; }
.form-actions { display: flex; justify-content: flex-end; gap: .5rem; margin-top: .25rem; }
textarea { resize: vertical; min-height: 80px; }
.badge-deleted { background: #f8d7da; color: #721c24; }
.danger:hover { color: #c0392b; border-color: #c0392b; }

/* ── Vorräte / Accruals — refined ledger, blue accent ───────── */

/* Scoped fonts: only the .accruals-zone uses these. Rest of PAIMOS untouched. */
.accruals-zone,
.accruals-zone * {
  font-family: 'Bricolage Grotesque', system-ui, -apple-system, sans-serif;
  font-feature-settings: 'ss01' on;
}

/* Local palette: cool neutral paper + var(--accruals-accent) for highlights */
.accruals-bar {
  --acc-paper:   #fafbfc;
  --acc-paper2:  #f3f5f8;
  --acc-ink:     #0f1419;
  --acc-mute:    #6b7480;
  --acc-line:    #e2e6ec;
  --acc-line-2:  #d3d8e0;
}
.ledger-row,
.project-card:has(.ledger-row) {
  --acc-paper:   #fafbfc;
  --acc-ink:     #0f1419;
  --acc-mute:    #6b7480;
  --acc-line:    #e2e6ec;
}

/* ── Subheader bar ─────────────────────────────────────────────────────── */
.accruals-bar {
  position: relative;
  margin: -.25rem -.25rem 1.25rem;
  background: var(--acc-paper);
  border: 1px solid var(--acc-line);
  border-radius: 4px;
  box-shadow: 0 1px 0 rgba(15,20,25,.02);
  overflow: hidden;
}

.accruals-bar-inner {
  display: flex; align-items: center; gap: 1.1rem;
  padding: .55rem .9rem .55rem 1.1rem;
  flex-wrap: wrap;
}

.accruals-bar-section { display: flex; align-items: center; gap: .5rem; }
.accruals-bar-section--qrl     { flex: 1; flex-wrap: wrap; }
.accruals-bar-section--actions { margin-left: auto; }

.accruals-eyebrow {
  font-size: 9px; font-weight: 700; letter-spacing: .14em;
  text-transform: uppercase; color: var(--accruals-accent);
  padding: .12rem .35rem;
  border: 1px solid var(--accruals-accent);
  border-radius: 2px;
  background: #fff;
}
.accruals-title {
  font-size: 14px; font-weight: 700; color: var(--acc-ink);
  letter-spacing: -.012em; line-height: 1;
}
.accruals-period {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px; font-weight: 500; color: var(--acc-mute);
  font-variant-numeric: tabular-nums;
  padding-left: .6rem; border-left: 1px solid var(--acc-line-2);
  margin-left: .1rem;
}

/* QRL preset chips */
.qrl-chip {
  background: transparent; border: none;
  font-family: inherit; font-size: 11px; font-weight: 500;
  color: var(--acc-mute); padding: .3rem .5rem; cursor: pointer;
  border-bottom: 1.5px solid transparent;
  transition: color .15s ease, border-color .15s ease;
  letter-spacing: -.003em;
}
.qrl-chip:hover { color: var(--acc-ink); }
.qrl-chip--active {
  color: var(--accruals-accent); font-weight: 700;
  border-bottom-color: var(--accruals-accent);
}
.qrl-divider {
  color: var(--acc-line-2); font-weight: 300; padding: 0 .15rem; user-select: none;
}
.qrl-custom-wrap {
  display: inline-flex; align-items: center; gap: .2rem;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
}
.qrl-date {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px; font-weight: 500; color: var(--acc-ink);
  font-variant-numeric: tabular-nums;
  padding: .2rem .35rem; background: transparent;
  border: none; border-bottom: 1px dashed var(--acc-line-2);
  border-radius: 0; outline: none; min-width: 90px;
}
.qrl-date:focus { border-bottom-color: var(--accruals-accent); border-bottom-style: solid; }
.qrl-date:hover { border-bottom-color: var(--acc-mute); }
.qrl-arrow { color: var(--acc-mute); font-size: 10px; }

/* Action buttons */
.ledger-btn {
  display: inline-flex; align-items: center; gap: .35rem;
  font-family: inherit; font-size: 11px; font-weight: 600;
  color: #fff; background: var(--accruals-accent);
  padding: .35rem .7rem; border: 1px solid var(--accruals-accent); border-radius: 3px;
  cursor: pointer; letter-spacing: -.003em;
  transition: background .15s ease, transform .12s ease;
}
.ledger-btn:hover { background: var(--accruals-accent-dark); border-color: var(--accruals-accent-dark); }
.ledger-btn:active { transform: translateY(.5px); }
.ledger-btn--ghost {
  background: transparent; color: var(--accruals-accent);
}
.ledger-btn--ghost:hover { background: var(--accruals-accent-soft); color: var(--accruals-accent-dark); }
.ledger-btn--ok {
  background: #1a7a3e; border-color: #1a7a3e;
}
.ledger-btn--ok:hover { background: #155f30; border-color: #155f30; }

/* Status column row */
.accruals-bar-statuses {
  display: flex; align-items: center; gap: .3rem;
  padding: .45rem .9rem .55rem 1.1rem;
  border-top: 1px solid var(--acc-line);
  background: var(--acc-paper2);
  flex-wrap: wrap;
}
.accruals-status-eyebrow {
  font-size: 9px; font-weight: 700; letter-spacing: .14em;
  text-transform: uppercase; color: var(--acc-mute);
  margin-right: .25rem;
}
.status-tag {
  display: inline-flex; align-items: center; gap: .3rem;
  font-family: inherit; font-size: 10px; font-weight: 600;
  padding: .18rem .5rem; border-radius: 2px;
  letter-spacing: -.003em;
  border: 1px solid transparent;
  background: transparent;
  cursor: default;
}
.status-tag-dot {
  width: 5px; height: 5px; border-radius: 50%;
  background: var(--acc-line-2);
}
.status-tag--pinned {
  background: var(--accruals-accent); color: #fff;
}
.status-tag--pinned .status-tag-dot { background: #fff; opacity: .7; }
.status-tag-divider {
  width: 1px; height: 12px; background: var(--acc-line-2); margin: 0 .35rem;
}
.status-tag--toggle {
  cursor: pointer; color: var(--acc-mute);
  border-color: var(--acc-line-2); background: #fff;
  transition: all .15s ease;
}
.status-tag--toggle:hover {
  border-color: var(--accruals-accent); color: var(--accruals-accent);
}
.status-tag--toggle.status-tag--on {
  background: var(--accruals-accent); color: #fff; border-color: var(--accruals-accent);
}
.status-tag--toggle.status-tag--on .status-tag-dot { background: #fff; opacity: .7; }

/* Status dot accents — neutral grays so they don't fight the blue */
.status-dot--done        { background: #6b7480; }
.status-dot--delivered   { background: #6b7480; }
.status-dot--accepted    { background: var(--accruals-accent); opacity: .7; }
.status-dot--invoiced    { background: var(--accruals-accent); }
.status-dot--new         { background: #b8bdc4; }
.status-dot--backlog     { background: #9ca3ac; }
.status-dot--in-progress { background: #6b7480; }
.status-dot--cancelled   { background: #b8bdc4; }

/* ── Card ledger row ──────────────────────────────────────────────────── */
.ledger-row {
  display: flex; align-items: stretch;
  margin-top: .15rem; padding-top: .5rem;
  border-top: 1px solid var(--acc-line);
  gap: 0;
  cursor: default;
}
.ledger-cell {
  flex: 1; min-width: 0;
  display: flex; flex-direction: column;
  padding: .05rem .5rem .05rem 0;
  border-right: 1px solid var(--acc-line);
}
.ledger-cell:last-child { border-right: none; padding-right: 0; }
.ledger-cell-label {
  font-family: 'Bricolage Grotesque', sans-serif;
  font-size: 8px; font-weight: 700; letter-spacing: .09em;
  text-transform: uppercase; color: var(--acc-mute);
  line-height: 1; margin-bottom: .3rem;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.ledger-cell-value {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px; font-weight: 600; color: var(--acc-ink);
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
  display: inline-flex; align-items: baseline; gap: 1px;
}
.ledger-cell-unit {
  font-size: 8.5px; font-weight: 500; color: var(--acc-mute);
  margin-left: 1px;
}
.ledger-cell-zero {
  color: #c8ccd2; font-weight: 400;
}
/* Restraint: only accepted + invoiced get accent ink */
.ledger-cell--accepted  .ledger-cell-value { color: var(--accruals-accent); opacity: .75; }
.ledger-cell--invoiced  .ledger-cell-value { color: var(--accruals-accent); }
.ledger-cell--cancelled .ledger-cell-value { color: #9aa1aa; opacity: .7; font-style: italic; }

/* Card surface tint when ledger present — keep PAIMOS's normal cards intact */
.project-card:has(.ledger-row) {
  background: #fcfdfe;
  border-color: var(--acc-line);
}
.project-card:has(.ledger-row):hover {
  background: var(--accruals-accent-soft);
  border-color: var(--accruals-accent);
  box-shadow: 0 4px 18px rgba(0, 100, 151, .08);
}

/* PAI-63 customer filter + per-card customer pill */
.pv-customer-filter {
  width: auto; min-width: 160px;
  padding: .35rem .65rem;
  font-size: 13px;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
}
.project-card-customer {
  display: inline-flex; align-items: center; gap: .25rem;
  font-size: 11px; font-weight: 600; color: var(--text-muted);
  text-decoration: none;
  align-self: flex-start;
  padding: .1rem .5rem .1rem .35rem;
  background: var(--bg);
  border-radius: 999px;
  transition: color .15s, background .15s;
}
.project-card-customer:hover {
  color: var(--brand-blue-dark);
  background: var(--brand-blue-pale);
}
</style>
