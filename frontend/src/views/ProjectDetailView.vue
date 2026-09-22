<script setup lang="ts">
import LoadingText from '@/components/LoadingText.vue'
import { ref, onMounted, onUnmounted, computed, nextTick, watch } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'

import AppModal from '@/components/AppModal.vue'
import IssueList from '@/components/IssueList.vue'
import TagChip from '@/components/TagChip.vue'
import TagSelector from '@/components/TagSelector.vue'
import MetaSelect from '@/components/MetaSelect.vue'
import type { MetaOption } from '@/components/MetaSelect.vue'
import ImportCollisionModal from '@/components/ImportCollisionModal.vue'
import type { PreflightResult, CollisionStrategy } from '@/components/ImportCollisionModal.vue'
import { errMsg } from '@/api/client'
import { publicURL } from '@/publicPath'
import { MAX_IMAGE_SIZE } from '@/utils/constants'
import { useAuthStore } from '@/stores/auth'
import { useSearchStore } from '@/stores/search'
import { useIssueRefreshPromptStore } from '@/stores/issueRefreshPrompt'
import AppIcon from '@/components/AppIcon.vue'
import { useConfirm } from '@/composables/useConfirm'
import { provideIssueContext } from '@/composables/useIssueContext'
import { useFreshness } from '@/composables/useFreshness'
import { useIssueQuery } from '@/composables/useIssueQuery'
import { createInternalFetcher, controllerFreshnessPath } from '@/composables/issueQueryFetchers'
import {
  buildProjectUpdatePayload,
  emptyProjectEditForm,
  inheritedProjectRateHint,
  projectToEditForm,
} from '@/config/projectDetailEdit'
import { buildProjectPurgePayload, emptyProjectPurgeForm } from '@/config/projectPurge'
import type { Tag, Issue, Project, User, Sprint, Customer } from '@/types'
import type { IssueListEnvelope } from '@/types'
import {
  addProjectTag as addProjectTagRequest,
  buildProjectCsvExportUrl,
  deleteProjectDetail,
  deleteProjectLogo,
  executeProjectTimeEntryPurge,
  loadProjectDetailData,
  loadProjectPurgeUsers,
  preflightProjectCsvImport,
  previewProjectTimeEntryPurge,
  removeProjectTag as removeProjectTagRequest,
  runProjectCsvImport,
  saveProjectDetail,
  setProjectStatus,
  uploadProjectLogo,
} from '@/services/projectDetail'
import DocumentsSection from '@/components/customer/DocumentsSection.vue'
import CooperationSection from '@/components/customer/CooperationSection.vue'
import ProjectContextSection from '@/components/project/ProjectContextSection.vue'
import ProjectAgentsTab from '@/components/project/ProjectAgentsTab.vue'
import ProjectInventoriesSection from '@/components/project/ProjectInventoriesSection.vue'
// PAI-339: project page primary tab structure (Overview / Issues /
// Knowledge). PAI-508 — Settings is now a right-aligned, admin-only
// footer tab (replacing the old "Edit project" modal); environments +
// deploy recipes moved to the Context tab.
// PAI-504 — Agents is now a first-class primary tab (peer of
// Knowledge), no longer mounted inside Edit Project; it wraps the
// existing ProjectAgentsSection via ProjectAgentsTab.
// PAI-360 — unified Knowledge tab (single list + type filter chips)
// supersedes the PAI-339 ProjectKnowledgeTab + KnowledgeCategoryPanel
// pill-row pattern.
import ProjectKnowledgeUnified from '@/components/project/knowledge/ProjectKnowledgeUnified.vue'
import KnowledgeGraph from '@/components/project/knowledge/KnowledgeGraph.vue'
import ProjectOverviewTab from '@/components/project/knowledge/ProjectOverviewTab.vue'
// PAI-356 — bottom-anchored footer bar replaces the top tab strip
// (Issues / Overview / Knowledge). Same identities, quieter chrome.
import ProjectFooterBar from '@/components/project/ProjectFooterBar.vue'
// PAI-146 expansion: AI optimize on the project description.
// project_description is its own field name (not aliased to
// "description") so the prompt reminder fits a stakeholder audience.
import AiActionMenu from '@/components/ai/AiActionMenu.vue'
import AiSurfaceFeedback from '@/components/ai/AiSurfaceFeedback.vue'
import { formatDurationHours, formatInteger } from '@/composables/useNumberFormat'
import { fmtRelative } from '@/utils/formatTime'
// PAI-359: side-panel exclusion machinery dropped with the
// workspace dock; primaryTab is mutually exclusive by construction.

const { confirm } = useConfirm()
const PROJECT_STATUS_OPTIONS: MetaOption[] = [
  { value: 'active', label: 'Active' },
  { value: 'frozen', label: 'Frozen — existing work only' },
  { value: 'archived', label: 'Archived' },
  { value: 'deleted', label: 'Deleted' },
]

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const search = useSearchStore()
const issueRefreshPrompt = useIssueRefreshPromptStore()
const isAdmin = computed(() => auth.isAdmin)
const isReviewer = computed(() => auth.user?.role === 'reviewer')
const projectId = computed(() => Number(route.params.id))
// Whether the current user can edit inside this project — admins and
// project editors pass, viewers fall through to read-only rendering.
const canEditProject = computed(() => auth.canEdit(projectId.value))
const project = ref<Project | null>(null)
const issues = ref<Issue[]>([])
const users = ref<User[]>([])
const allTags = ref<Tag[]>([])
const costUnits = ref<string[]>([])
const releases = ref<string[]>([])
const sprints = ref<Sprint[]>([])
// PAI-58/59. Loaded once on mount; powers the customer assignment
// dropdown in the edit modal and the inherited-rate hints.
const customers = ref<Customer[]>([])

// PAI-145. Documents and Cooperation panes don't belong inline in
// the project body — they were always-visible and shouted empty-state
// at the user. They now live in a slide-from-right aux panel
// (ProjectAuxPanel) toggled from buttons in the IssueList toolbar
// (next to Tree/Flat). Independent toggles, not tabs — but at most one
// aux panel is shown at a time because they share the right-edge slot
// with IssueSidePanel.
// PAI-178: 'context' joins docs/cooperation as a third aux panel.
// Project context (repos + manifest) used to render full-width
// above the issue tabs which crowded the page even on projects
// that don't use anchors. Now it's behind a toggle that lives in
// the same toolbar cluster as Docs / Coop.
// PAI-359: workspacePanel ref + ProjectWorkspace type deleted with
// the dock. Counts below feed the footer-bar badges (always-mounted
// sentinels emit @count / @populated regardless of which tab is
// active — see template).
const docCount = ref(0)
// PAI-504: agent count feeds the footer-bar badge. Kept fresh by an
// always-mounted sentinel ProjectAgentsTab (mirrors docCount /
// contextSummary) so the badge is live regardless of the active tab.
const agentCount = ref(0)
const cooperationPopulated = ref(false)
const contextSummary = ref({ repoCount: 0, hasManifest: false, populated: false })

provideIssueContext({ users, allTags, costUnits, releases, projects: ref([]), sprints })

const loading = ref(true)
const PROJECT_ISSUE_PAGE = 100
const ISSUE_LIST_CHANGE_SUBJECTS = new Set(['issue', 'issue_tag', 'comment', 'time_entry'])
const issueListRef = ref<InstanceType<typeof IssueList> | null>(null)
const exporting = ref(false)
const projectIssueQuery = computed(() => (search.scope === 'project' ? search.query : ''))
const projectServerFilterQuery = ref('')
const projectIssueTotal = ref(0)
const projectIssueHasMore = ref(false)
const projectIssueFingerprint = ref('')
const projectIssueSelectionFingerprint = ref('')
const projectIssuesLoadingMore = ref(false)
const projectIssueWindowMode = ref<'page' | 'all'>('page')
const projectIssueSortKey = ref('')
const projectIssueSortDir = ref<'asc' | 'desc'>('asc')
const trimmedProjectIssueQuery = computed(() => projectIssueQuery.value.trim())
let projectLoadRequestSeq = 0

// PAI-570: the IssueList controller owns fetch + cache + orchestration for the
// project list; a mirror watcher syncs its outputs into the existing refs so the
// many `issues` readers and the template stay unchanged.
const projectCtrl = useIssueQuery<Issue>({
  initial: { mode: 'internal-project', projectId: projectId.value },
  fetcher: createInternalFetcher(),
})
watch(
  () => [
    projectCtrl.issues.value,
    projectCtrl.total.value,
    projectCtrl.hasMore.value,
    projectCtrl.loading.value,
    projectCtrl.serverFingerprint.value,
    projectCtrl.selectionFingerprint.value,
  ],
  () => {
    issues.value = projectCtrl.issues.value
    projectIssueTotal.value = projectCtrl.total.value
    projectIssueHasMore.value = projectCtrl.hasMore.value
    projectIssuesLoadingMore.value = projectCtrl.loading.value
    projectIssueFingerprint.value = projectCtrl.serverFingerprint.value
    projectIssueSelectionFingerprint.value = projectCtrl.selectionFingerprint.value
  },
)

// PAI-246: header ⋯ menu (Export / Import). PAI-508 — the Edit project
// item moved out to the admin-only Settings footer tab.
// PAI-265: panel is teleported to <body> with position:fixed because the
// teleport-target #app-header-right has `overflow:hidden` (load-bearing for
// header truncation), which clips an absolute-positioned panel inside it.
const overflowOpen = ref(false)
const overflowTriggerRef = ref<HTMLElement | null>(null)
const overflowPanelRef = ref<HTMLElement | null>(null)
const overflowPanelStyle = ref<{ top: string; right: string }>({ top: '0px', right: '0px' })
function recomputeOverflowPosition() {
  const el = overflowTriggerRef.value
  if (!el) return
  const r = el.getBoundingClientRect()
  overflowPanelStyle.value = {
    top: `${r.bottom + 6}px`,
    right: `${window.innerWidth - r.right}px`,
  }
}
function closeOverflow() {
  overflowOpen.value = false
}
function toggleOverflow() {
  overflowOpen.value = !overflowOpen.value
  if (overflowOpen.value) void nextTick(recomputeOverflowPosition)
}
function onOverflowOutsideClick(e: MouseEvent) {
  if (!overflowOpen.value) return
  const target = e.target as Node
  const inTrigger = overflowTriggerRef.value?.contains(target) ?? false
  const inPanel = overflowPanelRef.value?.contains(target) ?? false
  if (!inTrigger && !inPanel) closeOverflow()
}
function onOverflowKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && overflowOpen.value) closeOverflow()
}
function onMenuExport() {
  closeOverflow()
  void exportCSV()
}
function onMenuLieferbericht() {
  closeOverflow()
  primaryTab.value = 'issues'
  void nextTick(() => issueListRef.value?.openLieferberichtExport())
}
function onMenuImport() {
  closeOverflow()
  triggerImport()
}

// ── Primary tabs (PAI-339) ───────────────────────────────────────────────
// Issues / Overview / Knowledge / Agents. PAI-508 — Settings is now a
// right-aligned, admin-only footer tab (replacing the old "Edit project"
// modal). The default landing tab is 'issues' to preserve the existing
// UX — daily users hit /projects/:id and expect the issue list, not a
// marketing card.
// PAI-359 — mutually-exclusive primary tabs. Docs / Coop / Context
// were workspace-dock toggles in PAI-279; that pattern is gone —
// they're peer tabs now.
// PAI-504 — Agents joins as a first-class peer tab (was previously
// buried in the Edit Project modal); reads allowed for project
// viewers, writes gated on (isAdmin && canEditProject) like Knowledge.
// PAI-508 — 'settings' joins as a right-aligned, admin-only footer tab
// (replaces the Edit Project modal). It carries the project *config*
// surface (General / Billing / Danger sections); environments + deploy
// recipes moved to the Context tab.
type ProjectPrimaryTab =
  | 'issues'
  | 'overview'
  | 'knowledge'
  | 'agents'
  | 'docs'
  | 'coop'
  | 'context'
  | 'settings'
const PROJECT_PRIMARY_TABS: ProjectPrimaryTab[] = [
  'issues',
  'overview',
  'knowledge',
  'agents',
  'docs',
  'coop',
  'context',
  'settings',
]
// Honor `?tab=knowledge` query param so deep-links from elsewhere
// (e.g. the IssueDetail "Applicable Memories" panel — PAI-342)
// land on the right tab without requiring a manual click.
// PAI-508: `?tab=settings` is admin-gated — the footer hides the button
// for non-admins, but a hand-typed deep link must also fall through to
// the default tab rather than render the settings surface read-only.
const initialTab: ProjectPrimaryTab = (() => {
  const t = route.query.tab
  if (typeof t === 'string' && (PROJECT_PRIMARY_TABS as readonly string[]).includes(t)) {
    if (isReviewer.value && ['docs', 'coop'].includes(t)) return 'issues'
    if (t === 'settings' && !(isAdmin.value && canEditProject.value)) return 'issues'
    return t as ProjectPrimaryTab
  }
  return 'issues'
})()
const primaryTab = ref<ProjectPrimaryTab>(initialTab)
// PAI-342 — deep-link target for the Knowledge tab. When present
// (e.g. ?tab=knowledge&memory=feedback_lock_signature), the
// Knowledge panel auto-selects the memory category and opens the
// matching slug in edit mode. ProjectKnowledgeTab reads this prop
// and clears it on first activation so subsequent tab switches
// don't re-open the editor.
const initialKnowledgeSlug = computed<string>(() => {
  const m = route.query.memory
  return typeof m === 'string' ? m : ''
})

// PAI-350 — knowledge tab view mode (list ↔ graph), deep-linked via ?kview=graph.
const knowledgeView = ref<'list' | 'graph'>(!isReviewer.value && route.query.kview === 'graph' ? 'graph' : 'list')
watch(knowledgeView, (v) => {
  const q = { ...route.query }
  if (v === 'graph') q.kview = 'graph'
  else delete q.kview
  void router.replace({ query: q })
})

// PAI-359 — workspace-dock state (`workspacePanel`, `toggleWorkspace`,
// `workspaceSummary`, aux-panel exclusion plumbing) deleted with the
// dock pattern. Docs / Coop / Context are first-class primaryTab
// values now; mount their components directly.

function resetWorkspaceState() {
  docCount.value = 0
  agentCount.value = 0
  cooperationPopulated.value = false
  contextSummary.value = { repoCount: 0, hasManifest: false, populated: false }
}

onMounted(() => {
  document.addEventListener('mousedown', onOverflowOutsideClick)
  document.addEventListener('keydown', onOverflowKey)
  // PAI-265: keep teleported panel anchored to trigger across viewport changes.
  // Capture phase catches scrolls inside nested scrollers (.main-content) too.
  window.addEventListener('resize', recomputeOverflowPosition)
  window.addEventListener('scroll', recomputeOverflowPosition, true)
})
onUnmounted(() => {
  document.removeEventListener('mousedown', onOverflowOutsideClick)
  document.removeEventListener('keydown', onOverflowKey)
  window.removeEventListener('resize', recomputeOverflowPosition)
  window.removeEventListener('scroll', recomputeOverflowPosition, true)
  issueRefreshPrompt.clear(refreshProjectIssueListFromHeader)
})

function updateContextSummary(payload: {
  repoCount: number
  hasManifest: boolean
  populated: boolean
}) {
  contextSummary.value = payload
}

// Project settings (PAI-508 — formerly the Edit Project modal). The
// form fields + handlers below are unchanged; only their host moved
// from a ⋯-menu modal to the admin-only Settings footer tab.
const editForm = ref(emptyProjectEditForm())
const editError = ref('')
const saving = ref(false)

// PAI-146 expansion: AI optimize composable + onAccept handler for
// the project description. The Settings tab is admin-gated, so the
// button only appears for users who already have edit rights here.
function onProjectDescriptionAccept(text: string) {
  editForm.value.description = text
}
async function applyProjectAiResult(info: any) {
  if (info.action === 'ui_generation') {
    const spec = String(info.body?.spec_markdown ?? '')
    editForm.value.description =
      info.intent === 'replace-description'
        ? spec
        : [editForm.value.description, spec].filter(Boolean).join('\n\n')
    return
  }
  if (info.action === 'suggest_enhancement') {
    const lines = (info.selection ?? [])
      .map((idx: number) => info.body?.suggestions?.[idx])
      .filter(Boolean)
      .map((it: any) => `- ${it.title}: ${it.body}`)
    editForm.value.description = [editForm.value.description, lines.join('\n')]
      .filter(Boolean)
      .join('\n\n')
    return
  }
  if (info.action === 'spec_out') {
    const lines = (info.selection ?? [])
      .map((idx: number) => info.body?.items?.[idx]?.text)
      .filter(Boolean)
      .map((text: string) => `- ${text}`)
    editForm.value.description = [editForm.value.description, lines.join('\n')]
      .filter(Boolean)
      .join('\n\n')
  }
}

// Logo upload
const logoInputRef = ref<HTMLInputElement | null>(null)
const logoUploading = ref(false)
const logoError = ref('')

async function uploadLogo(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  if (file.size > MAX_IMAGE_SIZE) {
    logoError.value = 'Image must be smaller than 3 MB.'
    return
  }
  logoError.value = ''
  logoUploading.value = true
  try {
    project.value = await uploadProjectLogo(projectId.value, file)
  } catch (ex: unknown) {
    logoError.value = errMsg(ex, 'Upload failed.')
  } finally {
    logoUploading.value = false
    if (logoInputRef.value) logoInputRef.value.value = ''
  }
}

async function deleteLogo() {
  logoError.value = ''
  try {
    project.value = await deleteProjectLogo(projectId.value)
  } catch (ex: unknown) {
    logoError.value = errMsg(ex, 'Failed to remove logo.')
  }
}

const projectTagIds = computed(() => project.value?.tags.map((t) => t.id) ?? [])

// PAI-508 — the Settings form has no explicit "open" step anymore (no
// modal). Keep editForm in sync with the loaded project, but only when
// the project *identity* changes — repopulating on every project
// mutation would clobber in-progress edits (e.g. a half-typed name)
// whenever a tag add/remove or AI-accept reassigns project.value.
// saveProject reloads the project (id unchanged → no repopulate), so the
// form keeps the just-saved values; tag add/remove mutate project.tags
// only, which flows to the TagSelector via projectTagIds independently.
watch(
  () => project.value?.id,
  (id) => {
    if (id != null && project.value) {
      editForm.value = projectToEditForm(project.value)
      editError.value = ''
    }
  },
  { immediate: true },
)

async function saveProject() {
  editError.value = ''
  if (!editForm.value.name.trim()) {
    editError.value = 'Name required.'
    return
  }
  saving.value = true
  try {
    const payload = buildProjectUpdatePayload(editForm.value, project.value?.customer_id ?? null)
    project.value = await saveProjectDetail(projectId.value, payload)
  } catch (e: unknown) {
    editError.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

// PAI-59. Effective rate display in the edit modal: when the project
// rate is null and a customer is selected with a rate, show the
// inherited value as a hint under the input.
function inheritedRateLabel(kind: 'hourly' | 'lp'): string {
  return inheritedProjectRateHint(editForm.value, customers.value, kind)
}

// ── Archive / Delete ──────────────────────────────────────────────────────────
const archiving = ref(false)
const showDeleteConfirm = ref(false)
const deletingProject = ref(false)

async function toggleArchive() {
  if (!project.value) return
  archiving.value = true
  try {
    const newStatus = project.value.status === 'active' ? 'archived' : 'active'
    project.value = await setProjectStatus(projectId.value, newStatus)
    editForm.value.status = project.value.status
  } finally {
    archiving.value = false
  }
}

async function deleteProject() {
  deletingProject.value = true
  try {
    await deleteProjectDetail(projectId.value)
    showDeleteConfirm.value = false
    router.push('/projects')
  } catch (e: unknown) {
    editError.value = errMsg(e, 'Delete failed.')
  } finally {
    deletingProject.value = false
  }
}

// ── Purge time entries ───────────────────────────────────────────────────────
const showPurge = ref(false)
const purgeLoading = ref(false)
const purgeBusy = ref(false)
const purgeConfirmKey = ref('')
const purgeError = ref('')
const purgeForm = ref(emptyProjectPurgeForm())
const purgePreview = ref<{ count: number; total_hours: number } | null>(null)
const purgeSuccess = ref<{ count: number; total_hours: number } | null>(null)
const purgeUsers = ref<{ id: number; username: string }[]>([])

async function openPurge() {
  purgeForm.value = emptyProjectPurgeForm()
  purgePreview.value = null
  purgeSuccess.value = null
  purgeConfirmKey.value = ''
  purgeError.value = ''
  showPurge.value = true
  try {
    purgeUsers.value = await loadProjectPurgeUsers(projectId.value)
  } catch {
    /* ignore */
  }
}

function closePurge() {
  showPurge.value = false
}

async function previewPurge() {
  purgeLoading.value = true
  purgePreview.value = null
  purgeSuccess.value = null
  purgeError.value = ''
  purgeConfirmKey.value = ''
  try {
    purgePreview.value = await previewProjectTimeEntryPurge(
      projectId.value,
      buildProjectPurgePayload(purgeForm.value),
    )
  } catch (e: unknown) {
    purgeError.value = errMsg(e, 'Preview failed')
  } finally {
    purgeLoading.value = false
  }
}

async function executePurge() {
  purgeBusy.value = true
  purgeError.value = ''
  try {
    const payload = {
      ...buildProjectPurgePayload(purgeForm.value),
      confirmation_key: purgeConfirmKey.value,
    }
    purgeSuccess.value = await executeProjectTimeEntryPurge(projectId.value, payload)
    purgePreview.value = null
    purgeConfirmKey.value = ''
  } catch (e: unknown) {
    purgeError.value = errMsg(e, 'Purge failed')
  } finally {
    purgeBusy.value = false
  }
}

async function addProjectTag(tagId: number) {
  await addProjectTagRequest(projectId.value, tagId)
  const tag = allTags.value.find((t) => t.id === tagId)
  if (tag && project.value) project.value.tags = [...project.value.tags, tag]
}

async function removeProjectTag(tagId: number) {
  if (!(await confirm({ message: 'Remove this tag from the project?', confirmLabel: 'Remove' })))
    return
  await removeProjectTagRequest(projectId.value, tagId)
  if (project.value) project.value.tags = project.value.tags.filter((t) => t.id !== tagId)
}

// ── CSV Export ────────────────────────────────────────────────────────────────
const exportError = ref('')

async function exportCSV() {
  if (exporting.value) return // guard against double-click
  exporting.value = true
  exportError.value = ''
  try {
    const il = issueListRef.value
    const selectedIds = il?.selectionMode && il.selectedIds.size > 0 ? [...il.selectedIds] : []
    const url = buildProjectCsvExportUrl(projectId.value, selectedIds)
    const resp = await fetch(url, { credentials: 'include' })
    if (resp.status === 401) {
      exportError.value = 'Session expired — please reload and log in again.'
      return
    }
    if (!resp.ok) {
      exportError.value = `Export failed (${resp.status}).`
      return
    }
    const blob = await resp.blob()
    if (blob.size === 0) {
      exportError.value = 'Export returned an empty file.'
      return
    }
    const cd = resp.headers.get('Content-Disposition') ?? ''
    const match = cd.match(/filename="([^"]+)"/)
    const filename = match ? match[1] : `${project.value?.key ?? 'export'}.csv`
    const objUrl = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = objUrl
    a.download = filename
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(objUrl)
  } catch (e: unknown) {
    exportError.value = `Export failed: ${errMsg(e, 'network error')}`
  } finally {
    exporting.value = false
  }
}

// ── CSV Import (with preflight + collision modal) ─────────────────────────────
const importResult = ref<{
  imported: number
  updated: number
  skipped: number
  errors: string[]
} | null>(null)
const importError = ref('')
const importing = ref(false)
const importInputRef = ref<HTMLInputElement | null>(null)
const importPreflight = ref<PreflightResult | null>(null)
const showImportModal = ref(false)
const pendingImportFile = ref<File | null>(null)

function triggerImport() {
  importResult.value = null
  importError.value = ''
  importInputRef.value?.click()
}

async function onImportFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  pendingImportFile.value = file
  importing.value = true
  importError.value = ''
  importResult.value = null
  try {
    importPreflight.value = await preflightProjectCsvImport(projectId.value, file)
    // If no collisions, import directly without showing modal
    if (importPreflight.value.collision_count === 0) {
      await doImport('insert', '')
    } else {
      showImportModal.value = true
    }
  } catch (ex: unknown) {
    importError.value = errMsg(ex, 'Preflight failed.')
  } finally {
    importing.value = false
    if (importInputRef.value) importInputRef.value.value = ''
  }
}

async function onImportConfirm(strategy: CollisionStrategy, _projectName: string) {
  showImportModal.value = false
  await doImport(strategy, '')
}

function onImportCancel() {
  showImportModal.value = false
  pendingImportFile.value = null
}

async function doImport(strategy: CollisionStrategy, _projectName: string) {
  if (!pendingImportFile.value) return
  importing.value = true
  try {
    importResult.value = await runProjectCsvImport(
      projectId.value,
      pendingImportFile.value,
      strategy,
    )
    await load()
  } catch (ex: unknown) {
    importError.value = errMsg(ex, 'Import failed.')
  } finally {
    importing.value = false
    pendingImportFile.value = null
  }
}

async function load() {
  const request = ++projectLoadRequestSeq
  loading.value = true
  resetWorkspaceState()
  projectIssueWindowMode.value = 'page'
  const loadQuery = projectIssueQuery.value
  const loadFilters = projectServerFilterQuery.value
  const loadSortKey = projectIssueSortKey.value
  const loadSortDir = projectIssueSortDir.value
  const data = await loadProjectDetailData(projectId.value, loadQuery, loadFilters, {
    envelope: true,
    limit: PROJECT_ISSUE_PAGE,
    offset: 0,
    sort: loadSortKey || undefined,
    order: loadSortKey ? loadSortDir : undefined,
  }, { includeCustomers: !isReviewer.value })
  if (request !== projectLoadRequestSeq) return
  project.value = data.project
  projectCtrl.query.projectId = projectId.value
  await projectCtrl.start()
  await issueFreshness.prime().catch(() => {}) // baseline the poll; never block load
  users.value = data.users
  costUnits.value = data.costUnits
  releases.value = data.releases
  allTags.value = data.allTags
  customers.value = data.customers
  loading.value = false
  void nextTick(applyLieferberichtHandoffFromRoute)
}

onMounted(() => {
  load()
})

// Re-fetch when navigating project→project (Vue Router reuses the component)
watch(
  () => route.params.id,
  (newId, oldId) => {
    if (newId && newId !== oldId) {
      resetWorkspaceState()
      load()
    }
  },
)

async function loadAllProjectIssues() {
  await projectCtrl.setWindow('all')
}

// Re-fetch issues when search query changes (search-as-filter overlay).
watch(trimmedProjectIssueQuery, (q) => {
  projectCtrl.setSearch(q)
})

function onServerFilterChange(query: string) {
  void projectCtrl.setRawFilter(query)
}

function onServerSortChange(key: string, dir: 'asc' | 'desc') {
  void projectCtrl.setSort(key, dir)
}

function applyLieferberichtHandoffFromRoute() {
  if (route.query.report !== 'lieferbericht') return
  primaryTab.value = 'issues'
  if (!issueListRef.value) {
    void nextTick(applyLieferberichtHandoffFromRoute)
    return
  }
  const dateField =
    typeof route.query.date_field === 'string' ? route.query.date_field : 'completed'
  const dateFrom = typeof route.query.date_from === 'string' ? route.query.date_from : ''
  const dateTo = typeof route.query.date_to === 'string' ? route.query.date_to : ''
  const filters = {
    type: ['ticket', 'task'],
    status: ['done', 'delivered', 'accepted', 'invoiced'],
    dateField,
    dateFrom,
    dateTo,
    treeView: false,
  }
  issueListRef.value.applyView(
    {
      id: -700,
      user_id: 0,
      owner_username: 'system',
      title: 'Projektbericht',
      description: 'Project report filter preset.',
      columns_json: '[]',
      filters_json: JSON.stringify(filters),
      is_shared: true,
      is_admin_default: false,
      sort_order: 0,
      hidden: false,
      pinned: null,
      created_at: '',
      updated_at: '',
    },
    false,
  )
  issueListRef.value.showToast(
    'Projektbericht uses the issue list filters. Adjust filters, then use ⋯ → Export Projektbericht PDF.',
  )
}

watch(
  () => route.query.report,
  () => nextTick(applyLieferberichtHandoffFromRoute),
)

function onCreated(issue: Issue) {
  void projectCtrl.refresh()
  const cuLabel = issue.cost_unit?.label
  if (cuLabel && !costUnits.value.includes(cuLabel))
    costUnits.value = [...costUnits.value, cuLabel].sort()
  const releaseLabel = issue.type === 'release' ? issue.title : issue.release?.label
  if (releaseLabel && !releases.value.includes(releaseLabel))
    releases.value = [...releases.value, releaseLabel].sort()
}

function onUpdated(issue: Issue) {
  projectCtrl.confirmMutation(issue.id, issue)
}

function onDeleted() {
  void projectCtrl.refresh()
}

// ── Last change metadata ───────────────────────────────────────────────────────
// Derived from loaded issues — most recently updated, zero extra API calls.
const lastChanged = computed(() => {
  if (!issues.value.length) return null
  const latest = [...issues.value].sort((a, b) => b.updated_at.localeCompare(a.updated_at))[0]
  const when = fmtRelative(latest.updated_at)
  return { id: latest.id, key: latest.issue_key, when, title: latest.title }
})

const projectFreshnessPath = computed(() =>
  controllerFreshnessPath(projectCtrl.query, projectCtrl.loaded.value, PROJECT_ISSUE_PAGE),
)
const issueFreshness = useFreshness<IssueListEnvelope<Issue>>(projectFreshnessPath, {
  apply: (env) => projectCtrl.reconcile(env.issues ?? [], env.total, env.revision),
  count: (payload) => payload.total,
  changes: (event) =>
    event.project_id === projectId.value && ISSUE_LIST_CHANGE_SUBJECTS.has(event.subject_type),
})
const issueFreshnessStale = computed(() => issueFreshness.stale.value)
const issueFreshnessCount = computed(() => issueFreshness.newCount.value)

function refreshProjectIssueListFromHeader() {
  issueFreshness.refresh()
}

watch(
  [issueFreshnessStale, issueFreshnessCount],
  ([stale, count]) => {
    if (stale) issueRefreshPrompt.show(count, refreshProjectIssueListFromHeader)
    else issueRefreshPrompt.clear(refreshProjectIssueListFromHeader)
  },
  { immediate: true },
)
</script>

<template>
  <div class="pd-page">
    <LoadingText v-if="loading" class="loading" label="Loading…" />
    <template v-else-if="project">
      <Teleport defer to="#app-header-left">
        <RouterLink to="/projects" class="ah-back">
          <AppIcon name="arrow-left" :size="13" />
        </RouterLink>
        <img
          v-if="project.logo_path"
          :src="publicURL(project.logo_path)"
          class="ah-project-logo"
          :alt="project.name"
        />
        <span class="ah-key-badge">{{ project.key }}</span>
        <span class="ah-title">{{ project.name }}</span>
        <span v-if="project.description" class="ah-subtitle">{{ project.description }}</span>
      </Teleport>
      <Teleport defer to="#app-header-right">
        <RouterLink
          v-if="project.customer_id"
          :to="`/customers/${project.customer_id}`"
          class="pd-customer-pill"
          :title="`Customer: ${project.customer_name ?? ''}`"
        >
          <AppIcon name="building-2" :size="12" />
          <span>{{ project.customer_name }}</span>
        </RouterLink>
        <span v-if="lastChanged" class="ah-meta-text">
          <span class="ah-meta-prefix">updated {{ lastChanged.when }}</span>
          <RouterLink
            :to="`/projects/${projectId}/issues/${lastChanged.id}`"
            class="ah-meta-link"
            >{{ lastChanged.key }}</RouterLink
          >
        </span>
        <span :class="`badge badge-${project.status}`">{{ project.status }}</span>
        <TagChip v-for="t in project.tags" :key="t.id" :tag="t" />
        <input
          ref="importInputRef"
          type="file"
          accept=".csv"
          style="display: none"
          @change="onImportFile"
        />
        <!-- PAI-246: Export / Import folded into a single ⋯ menu so
             the right cluster doesn't outweigh the global Undo / Edit
             controls. Icon convention here is data-flow oriented:
             Export = data leaves (up arrow), Import = data enters (down).
             PAI-508 — Edit project left this menu for the admin-only
             Settings footer tab. -->
        <button
          ref="overflowTriggerRef"
          class="btn btn-ghost btn-sm icon-only pd-overflow-trigger"
          :class="{ active: overflowOpen }"
          :title="overflowOpen ? 'Close menu' : 'More project actions'"
          @click="toggleOverflow"
        >
          <AppIcon name="more-horizontal" :size="14" />
        </button>
      </Teleport>

      <!-- PAI-265: panel teleported to <body> with position:fixed so it
           escapes #app-header-right's overflow:hidden clip. -->
      <Teleport to="body">
        <div
          v-if="overflowOpen"
          ref="overflowPanelRef"
          class="pd-overflow-menu"
          role="menu"
          :style="overflowPanelStyle"
        >
          <button class="pd-overflow-item" :disabled="exporting" @click="onMenuExport">
            <AppIcon v-if="!exporting" name="upload" :size="14" />
            <AppIcon v-else name="loader" :size="14" class="spin" />
            <span>{{ exporting ? 'Preparing download…' : 'Export CSV' }}</span>
          </button>
          <button class="pd-overflow-item" @click="onMenuLieferbericht">
            <AppIcon name="file-down" :size="14" />
            <span>Export Projektbericht PDF</span>
          </button>
          <button
            v-if="isAdmin && canEditProject"
            class="pd-overflow-item"
            :disabled="importing"
            @click="onMenuImport"
          >
            <AppIcon name="download" :size="14" />
            <span>Import CSV</span>
          </button>
          <!-- PAI-467: admin nav entry into the Customer Portal Visibility
               report. Lives in the same overflow menu where project-scoped
               admin tools (Import) live, so admins find it where they
               expect them. -->
          <RouterLink
            v-if="isAdmin"
            :to="`/admin/projects/${projectId}/portal-visibility`"
            class="pd-overflow-item pd-overflow-item--link"
          >
            <AppIcon name="eye" :size="14" />
            <span>{{ $t('visibility.filterTitle') }}</span>
          </RouterLink>
        </div>
      </Teleport>

      <!-- Export error banner -->
      <div v-if="exportError" class="import-error">
        {{ exportError }}
        <button class="import-dismiss" @click="exportError = ''">
          <AppIcon name="x" :size="14" />
        </button>
      </div>

      <!-- Import result banner -->
      <div v-if="importResult" class="import-result">
        <span class="import-ok"
          ><AppIcon name="check" :size="13" />
          {{ formatInteger(importResult.imported) }} imported</span
        >
        <span v-if="importResult.updated" class="import-ok">
          · {{ formatInteger(importResult.updated) }} updated</span
        >
        <span v-if="importResult.skipped" class="import-skip">
          · {{ formatInteger(importResult.skipped) }} skipped</span
        >
        <span v-if="importResult.errors?.length" class="import-errs">
          · {{ formatInteger(importResult.errors.length) }} error(s):
          {{ importResult.errors.join('; ') }}</span
        >
        <button class="import-dismiss" @click="importResult = null">
          <AppIcon name="x" :size="14" />
        </button>
      </div>
      <div v-if="importError" class="import-error">
        {{ importError }}
        <button class="import-dismiss" @click="importError = ''">
          <AppIcon name="x" :size="14" />
        </button>
      </div>

      <!-- PAI-178: ProjectContextSection moved into the aux panel
           cluster below. It used to render full-width here which
           wasted space on projects that don't use anchors yet.
           The Context toggle in the IssueList toolbar opens it. -->

      <!-- PAI-356 supersedes PAI-339's top tab strip. The three-item
           switcher (Issues / Overview / Knowledge) now lives in
           <ProjectFooterBar> at the bottom of the page, freeing the
           top of the content area for the work itself. -->

      <!-- Issues tab (existing UX preserved verbatim) ───────────────── -->
      <template v-if="primaryTab === 'issues'">
        <!-- Single IssueList for project issues. Project-level workspaces now live
             in the footer rail instead of competing with issue-list controls. -->
        <IssueList
          ref="issueListRef"
          :project-id="projectId"
          :issues="issues"
          :search-query-override="projectIssueQuery"
          :result-total="projectIssueTotal"
          :result-has-more="projectIssueHasMore"
          :result-fingerprint="projectIssueFingerprint"
          :selection-fingerprint="projectIssueSelectionFingerprint"
          :loading-more="projectIssuesLoadingMore"
          :url-sync-selection="true"
          :can-edit="canEditProject"
          @load-all="loadAllProjectIssues"
          @created="onCreated"
          @updated="onUpdated"
          @deleted="onDeleted"
          @server-filter-change="onServerFilterChange"
          @server-sort-change="onServerSortChange"
        />
      </template>

      <!-- PAI-509 — non-Issues tabs share ONE bounded scroll viewport.
           Issues is exempt: IssueList owns its own internal overflow
           scroller (sticky thead). Every other tab renders plain content
           into .pd-page's bounded-flex height and would otherwise just
           clip (Settings, Knowledge, …). This is the single scrollbar:
           none of these components carry an in-flow vertical scroller of
           their own, and the Knowledge side-panel is position:fixed — so
           there is no nesting / double scroll. -->
      <div v-else class="pd-tab-scroll">
        <!-- Overview tab — README-like + state callouts ───────────────── -->
        <ProjectOverviewTab v-if="primaryTab === 'overview'" :project="project" :issues="issues" :can-write="canEditProject" />

        <!-- Knowledge tab — PAI-360 unified list + PAI-350 graph view. -->
        <template v-else-if="primaryTab === 'knowledge'">
          <div v-if="!isReviewer" class="knowledge-view-toggle">
            <button
              type="button"
              :class="{ active: knowledgeView === 'list' }"
              @click="knowledgeView = 'list'"
            >
              List
            </button>
            <button
              type="button"
              :class="{ active: knowledgeView === 'graph' }"
              @click="knowledgeView = 'graph'"
            >
              Graph
            </button>
          </div>
          <KnowledgeGraph v-if="!isReviewer && knowledgeView === 'graph'" :project-id="projectId" />
          <ProjectKnowledgeUnified
            v-else
            :project-id="projectId"
            :can-write="isAdmin && canEditProject"
            :initial-memory-slug="initialKnowledgeSlug"
          />
        </template>

        <!-- Agents tab — PAI-504 first-class peer of Knowledge. Wraps
           ProjectAgentsSection (the same inline editor that used to
           live in the Edit Project modal). Reads allowed for project
           viewers; writes gated on (isAdmin && canEditProject). -->
        <ProjectAgentsTab
          v-else-if="primaryTab === 'agents'"
          :project-id="projectId"
          :reviewer="isReviewer"
          :can-write="isAdmin && canEditProject"
          @count="(n: number) => (agentCount = n)"
        />

        <!-- PAI-359 — Docs / Coop / Context as first-class primary
           tabs. Each renders full-page when its tab is active; the
           legacy aux-panel dock + workspace rail (PAI-279) is gone.
           The components emit @count / @populated either way, which
           keeps the footer-bar badges live whether or not the user
           is currently on that tab. -->
        <DocumentsSection
          v-else-if="!isReviewer && primaryTab === 'docs'"
          scope="project"
          :scope-id="projectId"
          :can-write="isAdmin && canEditProject"
          @count="(n: number) => (docCount = n)"
        />
        <CooperationSection
          v-else-if="!isReviewer && primaryTab === 'coop'"
          :project-id="projectId"
          :can-write="isAdmin && canEditProject"
          @populated="(v: boolean) => (cooperationPopulated = v)"
        />
        <!-- Context tab — repos (ProjectContextSection) + shared
           inventories (ProjectInventoriesSection: environments + deploy
           recipes). PAI-508 relocated inventories here from the Edit
           Project modal so all per-project infrastructure config lives
           on one tab. -->
        <div v-else-if="primaryTab === 'context'" class="pst-root">
          <ProjectContextSection
            :project-id="projectId"
            :can-write="isAdmin && canEditProject"
            :show-header="false"
            @populated="(v: boolean) => (contextSummary.populated = v)"
            @summary="updateContextSummary"
          />
          <ProjectInventoriesSection
            :project-id="projectId"
            :can-write="isAdmin && canEditProject"
          />
        </div>

        <!-- Settings tab — PAI-508. Admin-only project *configuration*
           (formerly the Edit Project modal). Three plain headed
           sections, no sub-tab bar: General + Billing share one form
           (Save persists them via saveProject); Danger zone keeps its
           own inline action buttons. Gated on (isAdmin && canEditProject)
           — the footer hides the button for everyone else, and the
           ?tab=settings deep link falls through to 'issues'. -->
        <section
          v-else-if="primaryTab === 'settings' && isAdmin && canEditProject"
          class="pst-root"
        >
          <form @submit.prevent="saveProject" class="form pst-form">
            <!-- ── General ──────────────────────────────────────────── -->
            <div class="pst-section">
              <h3 class="pst-heading">General</h3>
              <div class="field">
                <label>Name</label>
                <input v-model="editForm.name" type="text" required />
              </div>
              <div class="field">
                <label>Key <span class="label-hint">— used in issue IDs, e.g. WEB-1</span></label>
                <input
                  v-model="editForm.key"
                  type="text"
                  maxlength="6"
                  style="text-transform: uppercase"
                />
              </div>
              <div class="field">
                <div class="field-label-row">
                  <label>Description</label>
                  <AiActionMenu
                    surface="issue"
                    host-key="project-detail:description"
                    field="project_description"
                    field-label="Project description"
                    :issue-id="0"
                    :text="() => editForm.description"
                    :on-accept="onProjectDescriptionAccept"
                  />
                </div>
                <textarea v-model="editForm.description" rows="3"></textarea>
                <AiSurfaceFeedback
                  host-key="project-detail:description"
                  :apply="applyProjectAiResult"
                />
              </div>
              <div class="field">
                <label>Status</label>
                <MetaSelect v-model="editForm.status" :options="PROJECT_STATUS_OPTIONS" />
              </div>
              <div class="field">
                <label>Tags</label>
                <TagSelector
                  :all-tags="allTags"
                  :selected-ids="projectTagIds"
                  @add="addProjectTag"
                  @remove="removeProjectTag"
                />
              </div>
              <div class="field">
                <label>Product Owner</label>
                <select v-model="editForm.product_owner">
                  <option :value="null">— none —</option>
                  <option v-for="u in users" :key="u.id" :value="u.id">{{ u.username }}</option>
                </select>
              </div>
              <div class="field">
                <label
                  >Logo
                  <span class="label-hint">— square image, max 200×200px · JPG/PNG</span></label
                >
                <div class="logo-upload-row">
                  <img
                    v-if="project?.logo_path"
                    :src="publicURL(project.logo_path)"
                    class="logo-preview"
                    alt="Current logo"
                  />
                  <div v-else class="logo-placeholder">No logo</div>
                  <input
                    ref="logoInputRef"
                    type="file"
                    accept="image/jpeg,image/png"
                    style="display: none"
                    @change="uploadLogo"
                  />
                  <button
                    type="button"
                    class="btn btn-ghost btn-sm"
                    :disabled="logoUploading"
                    @click="logoInputRef?.click()"
                  >
                    {{ logoUploading ? 'Uploading…' : project?.logo_path ? 'Replace' : 'Upload' }}
                  </button>
                  <button
                    v-if="project?.logo_path"
                    type="button"
                    class="btn btn-ghost btn-sm"
                    @click="deleteLogo"
                  >
                    Remove
                  </button>
                </div>
                <div v-if="logoError" class="field-error">{{ logoError }}</div>
              </div>
            </div>

            <!-- ── AI Defaults ─────────────────────────────────────── -->
            <div class="pst-section">
              <h3 class="pst-heading">AI defaults</h3>
              <div class="pd-ai-grid">
                <div class="field">
                  <label>Profile</label>
                  <select v-model="editForm.ai_default_profile_id">
                    <option value="">Inherited</option>
                    <option value="default">Default</option>
                    <option value="fast">Fast</option>
                    <option value="balanced">Balanced</option>
                    <option value="deep">Deep</option>
                  </select>
                </div>
                <div class="field">
                  <label>Effort</label>
                  <select v-model="editForm.ai_default_effort">
                    <option value="">Inherited</option>
                    <option value="low">Low</option>
                    <option value="standard">Standard</option>
                    <option value="deep">Deep</option>
                  </select>
                </div>
                <div class="field">
                  <label>Prompt</label>
                  <input
                    v-model="editForm.ai_default_prompt_preset_ref"
                    type="text"
                    placeholder="default or kb:type:slug"
                  />
                </div>
                <div class="field">
                  <label>Context</label>
                  <select v-model="editForm.ai_default_context_pack">
                    <option value="">Inherited</option>
                    <option value="issue">Issue only</option>
                    <option value="knowledge">Project knowledge</option>
                    <option value="retrieve">Retrieved context</option>
                    <option value="repo">Repo-aware bundle</option>
                  </select>
                </div>
                <div class="field">
                  <label>Provider class</label>
                  <select v-model="editForm.ai_preferred_provider_class">
                    <option value="">Inherited</option>
                    <option value="local_cli">Trusted runner</option>
                    <option value="hosted_model">Hosted draft</option>
                    <option value="local_model">Local model draft</option>
                  </select>
                </div>
              </div>
              <div class="pd-ai-policy">
                <label
                  ><input v-model="editForm.ai_disable_hosted_draft" type="checkbox" /> Disable
                  hosted draft providers</label
                >
                <label
                  ><input v-model="editForm.ai_disable_local_model_draft" type="checkbox" /> Disable
                  local model draft providers</label
                >
              </div>
              <div class="field">
                <label>Scoped overrides JSON</label>
                <textarea
                  v-model="editForm.ai_scoped_defaults_json"
                  rows="5"
                  spellcheck="false"
                  placeholder='{"actions":{"spec_out":{"profile_id":"deep"}},"runs":{"openrouter_draft.implement":{"context_pack":"knowledge"}},"agents":{"codex":{"effort":"deep"}}}'
                ></textarea>
              </div>
            </div>

            <!-- ── Billing ──────────────────────────────────────────── -->
            <div class="pst-section">
              <h3 class="pst-heading">Billing</h3>
              <div class="field">
                <label
                  >Customer
                  <span class="label-hint"
                    >— links rates + documents to a Customer record</span
                  ></label
                >
                <select v-model="editForm.customer_id">
                  <option :value="null">— Unassigned —</option>
                  <option v-for="c in customers" :key="c.id" :value="c.id">{{ c.name }}</option>
                </select>
              </div>
              <div class="field">
                <label
                  >Customer label
                  <span class="label-hint">— legacy freeform reference (PMO26 era)</span></label
                >
                <input v-model="editForm.customer_label" type="text" placeholder="e.g. CUST-123" />
              </div>
              <div style="display: flex; gap: 0.75rem">
                <div class="field" style="flex: 1">
                  <label>Rate (€/h)</label>
                  <input
                    v-model.number="editForm.rate_hourly"
                    type="number"
                    step="0.01"
                    placeholder="e.g. 120"
                  />
                  <span v-if="inheritedRateLabel('hourly')" class="pd-inherit-hint">
                    <AppIcon name="link" :size="11" /> {{ inheritedRateLabel('hourly') }}
                  </span>
                </div>
                <div class="field" style="flex: 1">
                  <label>Rate (€/LP)</label>
                  <input
                    v-model.number="editForm.rate_lp"
                    type="number"
                    step="0.01"
                    placeholder="e.g. 1200"
                  />
                  <span v-if="inheritedRateLabel('lp')" class="pd-inherit-hint">
                    <AppIcon name="link" :size="11" /> {{ inheritedRateLabel('lp') }}
                  </span>
                </div>
              </div>
            </div>

            <!-- Save persists General + Billing (saveProject), exactly as
               the modal footer did. -->
            <div v-if="editError" class="form-error">{{ editError }}</div>
            <div class="form-actions">
              <button type="submit" class="btn btn-primary" :disabled="saving">
                {{ saving ? 'Saving…' : 'Save changes' }}
              </button>
            </div>
          </form>

          <!-- ── Danger zone ──────────────────────────────────────────
             Outside the form so its actions never trip the submit
             handler; same markup the modal's Danger sub-tab carried. -->
          <div class="pst-section">
            <h3 class="pst-heading">Danger zone</h3>
            <div class="danger-zone-actions">
              <button type="button" class="btn btn-ghost btn-sm" @click="openPurge">
                Purge time entries
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-sm"
                :disabled="archiving"
                @click="toggleArchive"
              >
                {{
                  archiving
                    ? 'Saving…'
                    : project?.status === 'active'
                      ? 'Archive project'
                      : 'Restore to active'
                }}
              </button>
              <button type="button" class="btn btn-danger btn-sm" @click="showDeleteConfirm = true">
                Delete project
              </button>
            </div>
          </div>
        </section>
      </div>
      <!-- /.pd-tab-scroll -->

      <!-- Always-mounted, visually-hidden sentinels feed the
           footer-bar count badges for tabs the user has not yet
           clicked. They mirror the @count / @populated emits but
           render with display:none. Skipped for the active tab to
           avoid double-mount + double-fetch. -->
      <div v-if="!isReviewer" class="pd-sentinels" aria-hidden="true">
        <!-- PAI-504: agent-count sentinel. can-write=false so the
             hidden mount never exposes write affordances; skipped when
             Agents is the active tab to avoid double-mount + double
             GET (mirrors the Docs / Context sentinels below). -->
        <ProjectAgentsTab
          v-if="primaryTab !== 'agents'"
          :project-id="projectId"
          :can-write="false"
          @count="(n: number) => (agentCount = n)"
        />
        <DocumentsSection
          v-if="primaryTab !== 'docs'"
          scope="project"
          :scope-id="projectId"
          :can-write="false"
          @count="(n: number) => (docCount = n)"
        />
        <CooperationSection
          v-if="primaryTab !== 'coop'"
          :project-id="projectId"
          :can-write="false"
          @populated="(v: boolean) => (cooperationPopulated = v)"
        />
        <ProjectContextSection
          v-if="primaryTab !== 'context'"
          :project-id="projectId"
          :can-write="false"
          :show-header="false"
          @populated="(v: boolean) => (contextSummary.populated = v)"
          @summary="updateContextSummary"
        />
      </div>

      <!-- PAI-359 / PAI-361 — Teleport the footer bar into the
           bottom-chrome slot under <main> (peer of AppHeader).
           Spans the full main width by default; mounted only while
           ProjectDetailView is active; clean teardown on route change. -->
      <Teleport to="#project-footer-slot">
        <ProjectFooterBar
          v-model="primaryTab"
          :reviewer="isReviewer"
          :open-issues="issues.length"
          :knowledge-entries="project.counts?.knowledge_entries ?? null"
          :agent-count="agentCount"
          :docs-count="docCount"
          :context-repos="contextSummary.repoCount"
          :coop-populated="cooperationPopulated"
          :can-edit-settings="isAdmin && canEditProject"
        />
      </Teleport>
    </template>
  </div>

  <!-- Delete confirm modal -->
  <AppModal
    title="Delete Project"
    :open="showDeleteConfirm"
    @close="showDeleteConfirm = false"
    confirm-key="d"
    @confirm="deleteProject"
  >
    <p style="font-size: 14px; color: var(--text); margin-bottom: 1.25rem">
      Soft-delete <strong>{{ project?.name }}</strong
      >? The project and all its issues will be hidden from the UI. All data is preserved and can be
      restored via database update.
    </p>
    <div style="display: flex; justify-content: flex-end; gap: 0.5rem">
      <button class="btn btn-ghost" @click="showDeleteConfirm = false"><u>C</u>ancel</button>
      <button class="btn btn-danger" :disabled="deletingProject" @click="deleteProject">
        <template v-if="deletingProject">Deleting…</template
        ><template v-else><u>D</u>elete project</template>
      </button>
    </div>
  </AppModal>

  <!-- Import collision modal -->
  <ImportCollisionModal
    :open="showImportModal"
    :preflight="importPreflight"
    @confirm="onImportConfirm"
    @cancel="onImportCancel"
  />

  <!-- Purge time entries modal (admin only) -->
  <AppModal
    v-if="isAdmin"
    title="Purge Time Entries"
    :open="showPurge"
    @close="closePurge"
    max-width="540px"
  >
    <div class="purge-body">
      <div class="purge-filters">
        <div class="field">
          <label>Source</label>
          <select v-model="purgeForm.source">
            <option value="all">All entries</option>
            <option value="mite">Mite-imported only</option>
            <option value="manual">Manual only</option>
          </select>
        </div>
        <div style="display: flex; gap: 0.75rem">
          <div class="field" style="flex: 1">
            <label>From date</label>
            <input v-model="purgeForm.from_date" type="date" />
          </div>
          <div class="field" style="flex: 1">
            <label>To date</label>
            <input v-model="purgeForm.to_date" type="date" />
          </div>
        </div>
        <div class="field">
          <label>User</label>
          <select v-model="purgeForm.user_id">
            <option :value="null">All users</option>
            <option v-for="u in purgeUsers" :key="u.id" :value="u.id">{{ u.username }}</option>
          </select>
        </div>
        <button
          class="btn btn-ghost"
          :disabled="purgeLoading"
          @click="previewPurge"
          style="align-self: flex-start"
        >
          {{ purgeLoading ? 'Loading...' : 'Preview' }}
        </button>
      </div>

      <div v-if="purgePreview" class="purge-preview">
        <p class="purge-warning">
          This will permanently delete <strong>{{ formatInteger(purgePreview.count) }}</strong> time
          {{ purgePreview.count === 1 ? 'entry' : 'entries' }}
          ({{ formatDurationHours(purgePreview.total_hours) }}). This cannot be undone.
        </p>
        <div class="field">
          <label
            >Type <strong>{{ project?.key }}</strong> to confirm</label
          >
          <input
            v-model="purgeConfirmKey"
            type="text"
            :placeholder="project?.key"
            autocomplete="off"
          />
        </div>
        <button
          class="btn btn-danger"
          :disabled="purgeBusy || purgeConfirmKey.toUpperCase() !== project?.key?.toUpperCase()"
          @click="executePurge"
        >
          {{ purgeBusy ? 'Purging...' : `Purge ${formatInteger(purgePreview.count)} entries` }}
        </button>
      </div>

      <div v-if="purgeSuccess" class="purge-success">
        Deleted {{ formatInteger(purgeSuccess.count) }}
        {{ purgeSuccess.count === 1 ? 'entry' : 'entries' }} ({{
          formatDurationHours(purgeSuccess.total_hours)
        }}).
      </div>
      <div v-if="purgeError" class="form-error">{{ purgeError }}</div>
    </div>
  </AppModal>
</template>

<style scoped>
/* PAI-350 — knowledge List/Graph view toggle. */
.knowledge-view-toggle {
  display: inline-flex;
  gap: 0;
  margin: 0 0 12px;
  border: 1px solid var(--border, #d1d5db);
  border-radius: 6px;
  overflow: hidden;
}
.knowledge-view-toggle button {
  padding: 5px 14px;
  font-size: 13px;
  background: var(--surface, #fff);
  border: none;
  border-left: 1px solid var(--border, #e5e7eb);
  cursor: pointer;
  color: var(--text-muted, #6b7280);
}
.knowledge-view-toggle button:first-child {
  border-left: none;
}
.knowledge-view-toggle button.active {
  background: var(--accent, #3b82f6);
  color: #fff;
}

/* PAI-146: per-field label row holds the label + the AI optimize
   button on the right. */
.field-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}
.field-label-row > label {
  margin-bottom: 0;
}

.loading {
  color: var(--text-muted);
  padding: 2rem 0;
}

/* ── Project logo ─────────────────────────────────────────────────────────── */
.ah-project-logo {
  width: 24px;
  height: 24px;
  object-fit: contain;
  border-radius: 4px;
  flex-shrink: 0;
}
.logo-upload-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.logo-preview {
  width: 40px;
  height: 40px;
  object-fit: contain;
  border-radius: 6px;
  border: 1px solid var(--border);
}
.logo-placeholder {
  width: 40px;
  height: 40px;
  border-radius: 6px;
  border: 1px dashed var(--border);
  background: var(--bg);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  color: var(--text-muted);
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
.spin {
  animation: spin 0.8s linear infinite;
}
.icon-only {
  padding: 0.3rem 0.45rem;
}
.header-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
}

/* ── Project header ─────────────────────────────────────────────────────────── */
.project-header {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  margin-bottom: 1.75rem;
}

/* Row 1 — big key */
.ph-key {
  font-size: 28px;
  font-weight: 800;
  color: var(--text);
  letter-spacing: -0.03em;
  line-height: 1;
}

/* Row 2 — name · desc  ←spacer→  meta · badge  |  actions */
.ph-row2 {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex-wrap: wrap;
}
.ph-identity {
  display: flex;
  align-items: baseline;
  gap: 0.55rem;
  flex-wrap: wrap;
}
.ph-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--text);
}
.ph-desc {
  font-size: 13px;
  color: var(--text-muted);
}

/* Spacer pushes meta + badge + actions to the right */
.ph-meta {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 0.65rem;
}
.ph-last {
  font-size: 12px;
  color: var(--text-muted);
  display: flex;
  align-items: center;
  gap: 0.3rem;
}
.ph-last-link {
  font-size: 12px;
  font-weight: 600;
  font-family: monospace;
  color: var(--brand-blue);
}
.ph-last-link:hover {
  text-decoration: underline;
}
.ph-badge {
  flex-shrink: 0;
}

.ph-actions {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-shrink: 0;
}

.import-result,
.import-error {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
  font-size: 13px;
  padding: 0.6rem 1rem;
  border-radius: var(--radius);
  margin-bottom: 0.75rem;
}
.import-result {
  background: #d4edda;
  color: #155724;
}
.import-error {
  background: #fde8e8;
  color: #c0392b;
}
.import-ok {
  font-weight: 600;
}
.import-skip {
  color: #856404;
}
.import-errs {
  color: #721c24;
}
.import-dismiss {
  margin-left: auto;
  background: none;
  border: none;
  font-size: 16px;
  cursor: pointer;
  color: inherit;
  line-height: 1;
  padding: 0 0.25rem;
}

.form {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
}

/* PAI-508 — Settings primary tab. Mirrors pku-root / pat-root: a
   vertical flex column with the same content gutters the other primary
   tabs use, so the page scrolls in the main-content viewport. Plain
   headed sections (no sub-tab bar) separate General / Billing / Danger. */
.pst-root {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
  padding: 0.25rem 0;
  min-width: 0;
}
.pst-form {
  gap: 1.5rem;
}
.pst-section {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.pst-heading {
  margin: 0;
  font-size: 13px;
  font-weight: 700;
  color: var(--text);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding-bottom: 0.5rem;
  border-bottom: 1px solid var(--border);
}
.field label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.label-hint {
  font-weight: 400;
  text-transform: none;
  letter-spacing: 0;
  font-size: 11px;
}
.pd-ai-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
  gap: 0.75rem;
}
.pd-ai-policy {
  display: flex;
  align-items: center;
  gap: 0.6rem 1rem;
  flex-wrap: wrap;
  color: var(--text);
  font-size: 12px;
}
.pd-ai-policy label {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}
.form-error {
  font-size: 13px;
  color: #c0392b;
  background: #fde8e8;
  padding: 0.5rem 0.75rem;
  border-radius: var(--radius);
}
.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  margin-top: 0.25rem;
}
textarea {
  resize: vertical;
  min-height: 80px;
}

/* PAI-508 — Danger zone now sits in its own .pst-section under an <h3>
   heading; the standalone border/label that framed it inside the modal
   are no longer needed. */
.danger-zone-actions {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}
.btn-sm {
  padding: 0.3rem 0.65rem;
  font-size: 12px;
}

/* ── Purge modal ──────────────────────────────────────────────────────────── */
.purge-body {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.purge-filters {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.purge-preview {
  border: 1px solid #e8c4c4;
  background: #fdf2f2;
  border-radius: var(--radius);
  padding: 0.75rem 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.purge-warning {
  font-size: 13px;
  color: #7a1a1a;
  margin: 0;
  line-height: 1.5;
}
.purge-success {
  font-size: 13px;
  color: #1a6b3a;
  background: #edf7ed;
  border: 1px solid #c4e0c4;
  border-radius: var(--radius);
  padding: 0.6rem 0.75rem;
}

/* ── Group list ─────────────────────────────────────────────────────────────── */
.group-empty {
  color: var(--text-muted);
  font-size: 13px;
  padding: 1.5rem 0;
}
.group-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.group-table th {
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.4rem 0.75rem;
  border-bottom: 1px solid var(--border);
}
.group-row {
  border-bottom: 1px solid var(--border-subtle, var(--border));
}
.group-row:hover {
  background: var(--surface-2);
}
.group-row td {
  padding: 0.55rem 0.75rem;
  vertical-align: middle;
}
.group-key {
  font-family: monospace;
  font-size: 12px;
  white-space: nowrap;
}
.group-title {
  max-width: 340px;
}
.group-meta {
  color: var(--text-muted);
  white-space: nowrap;
}
.group-link {
  color: var(--text);
  text-decoration: none;
}
.group-link:hover {
  color: var(--brand-blue);
  text-decoration: underline;
}

/* ── PAI-58/59 customer link + rate inheritance ───────────────────── */
.pd-customer-pill {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.15rem 0.55rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  text-decoration: none;
  background: var(--bg-card);
  transition:
    border-color 0.15s,
    color 0.15s,
    background 0.15s;
  white-space: nowrap;
}

/* PAI-246: ⋯ overflow menu in the right cluster (Export / Import / Edit). */
.pd-overflow-trigger.active {
  background: var(--bg);
  color: var(--text);
}
/* PAI-265: position:fixed + inline top/right (computed from trigger rect)
   so the panel escapes #app-header-right's overflow:hidden clip. */
.pd-overflow-menu {
  position: fixed;
  z-index: 60;
  min-width: 180px;
  padding: 0.25rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 6px 20px rgba(15, 23, 42, 0.08);
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.pd-overflow-item {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  padding: 0.45rem 0.6rem;
  font-size: 12.5px;
  color: var(--text);
  background: transparent;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  text-align: left;
  font-family: inherit;
  white-space: nowrap;
}
.pd-overflow-item:hover:not(:disabled) {
  background: var(--bg);
}
.pd-overflow-item:disabled {
  color: var(--text-muted);
  cursor: not-allowed;
}
.pd-overflow-item :deep(svg) {
  color: var(--text-muted);
  flex-shrink: 0;
}
/* PAI-467: RouterLink form inherits the same chrome as the buttons. */
.pd-overflow-item--link {
  text-decoration: none;
  color: var(--text);
}
.pd-overflow-item--link:hover {
  background: var(--bg);
}
.pd-customer-pill:hover {
  border-color: var(--brand-blue);
  color: var(--brand-blue-dark);
  background: var(--brand-blue-pale);
}

.pd-inherit-hint {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  font-size: 11px;
  color: var(--brand-blue);
  margin-top: 0.15rem;
}

/* PAI-509 — the shared scroll viewport for every non-Issues primary
   tab. A bounded flex child of .pd-page (flex:1 + min-height:0) so it
   fills the space under the header and scrolls *internally* rather than
   letting tall content (Settings, Knowledge, …) clip. Block flow inside
   so the tab roots size to content; the Issues tab is NOT wrapped (its
   IssueList keeps its own sticky-thead scroller), so there is never a
   second scrollbar. padding-bottom gives the last row breathing room
   above the footer bar. */
.pd-tab-scroll {
  flex: 1;
  min-height: 0;
  min-width: 0;
  overflow-y: auto;
  padding-bottom: 1.5rem;
}

.pd-page {
  display: flex;
  flex-direction: column;
  /* PAI-274 / PAI-361: participate in AppLayout's `.main-content--self-scroll` flex chain
     so IssueList's table-wrap (flex:1; min-height:0; overflow:auto) actually
     has a bounded scrolling viewport — restoring sticky thead + frozen
     columns inside this view. `min-height:100%` was the old page-scroll
     contract; `flex:1; min-height:0` is the bounded-flex contract. */
  flex: 1;
  min-height: 0;
  min-width: 0;
}

/* PAI-359: pd-workspace-* + pd-context-overview-* + pd-workspace-rail-*
   styles deleted with the dock surface. The Docs / Coop / Context
   tabs now render full-page like the other primary tabs; counts
   feed the footer-bar badges via the always-mounted sentinel
   components below. */

/* Sentinel components keep reactivity but render no UI. */
.pd-sentinels {
  display: none;
}
</style>
