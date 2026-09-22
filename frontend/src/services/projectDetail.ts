import { api, csrfHeaders } from '@/api/client'
import { publicURL } from '@/publicPath'
import type { CollisionStrategy, PreflightResult } from '@/components/ImportCollisionModal.vue'
import type { Customer, Issue, IssueListEnvelope, Project, SavedView, Tag, User } from '@/types'

export interface ProjectDetailData {
  project: Project
  issues: Issue[]
  issueTotal: number
  issueEnvelope?: IssueListEnvelope<Issue>
  users: User[]
  allTags: Tag[]
  costUnits: string[]
  releases: string[]
  customers: Customer[]
}

export interface ProjectPurgeStats {
  count: number
  total_hours: number
}

export interface ProjectImportResult {
  imported: number
  updated: number
  skipped: number
  errors: string[]
}

export interface ProjectPurgeUser {
  id: number
  username: string
}

export interface ProjectIssuesRequestOptions {
  envelope?: boolean
  limit?: number
  offset?: number
  sort?: string
  order?: 'asc' | 'desc'
}

export function buildProjectIssuesUrl(
  projectId: number,
  query: string,
  filters = '',
  opts: ProjectIssuesRequestOptions = {},
): string {
  const q = query.trim()
  const params = new URLSearchParams(filters)
  params.set('fields', 'list')
  if (opts.envelope) params.set('envelope', '1')
  if (opts.limit !== undefined) params.set('limit', String(opts.limit))
  if (opts.offset !== undefined) params.set('offset', String(opts.offset))
  if (opts.sort) params.set('sort', opts.sort)
  if (opts.order) params.set('order', opts.order)
  if (q.length >= 2) params.set('q', q)
  return `/projects/${projectId}/issues?${params.toString().replace(/\+/g, '%20')}`
}

export function buildProjectCsvExportUrl(projectId: number, selectedIds: number[]): string {
  let url = publicURL(`/api/projects/${projectId}/export/csv`)
  if (selectedIds.length > 0) {
    url += `?ids=${selectedIds.join(',')}`
  }
  return url
}

export async function loadProjectDetailData(
  projectId: number,
  query: string,
  filters = '',
  issueOpts: ProjectIssuesRequestOptions = {},
  access: { includeCustomers?: boolean } = {},
): Promise<ProjectDetailData> {
  const [project, issuePayload, users, costUnits, releases, allTags, customers] = await Promise.all([
    api.get<Project>(`/projects/${projectId}`),
    issueOpts.envelope
      ? loadProjectIssuesEnvelope(projectId, query, filters, issueOpts)
      : api.get<Issue[]>(buildProjectIssuesUrl(projectId, query, filters)),
    api.get<User[]>('/users'),
    api.get<string[]>(`/projects/${projectId}/cost-units`).catch(() => []),
    api.get<string[]>(`/projects/${projectId}/releases`).catch(() => []),
    api.get<Tag[]>('/tags'),
    access.includeCustomers === false ? Promise.resolve([] as Customer[]) : api.get<Customer[]>('/customers').catch(() => [] as Customer[]),
  ])

  const issues = Array.isArray(issuePayload) ? issuePayload : issuePayload.issues
  const issueTotal = Array.isArray(issuePayload) ? issuePayload.length : issuePayload.total
  const issueEnvelope = Array.isArray(issuePayload) ? undefined : issuePayload

  return {
    project,
    issues,
    issueTotal,
    issueEnvelope,
    users,
    allTags,
    costUnits,
    releases,
    customers,
  }
}

export function loadProjectIssues(projectId: number, query: string, filters = ''): Promise<Issue[]> {
  return api.get<Issue[]>(buildProjectIssuesUrl(projectId, query, filters))
}

export function loadProjectIssuesEnvelope(
  projectId: number,
  query: string,
  filters = '',
  opts: ProjectIssuesRequestOptions = {},
): Promise<IssueListEnvelope<Issue>> {
  return api.get<IssueListEnvelope<Issue>>(buildProjectIssuesUrl(projectId, query, filters, { ...opts, envelope: true }))
}

export async function uploadProjectLogo(projectId: number, file: File): Promise<Project> {
  const fd = new FormData()
  fd.append('logo', file)
  const resp = await fetch(publicURL(`/api/projects/${projectId}/logo`), {
    method: 'POST',
    body: fd,
    credentials: 'same-origin',
    headers: csrfHeaders(),
  })
  const data = await resp.json()
  if (!resp.ok) {
    throw new Error(data.error ?? 'Upload failed.')
  }
  return data as Project
}

export function deleteProjectLogo(projectId: number): Promise<Project> {
  return api.delete<Project>(`/projects/${projectId}/logo`)
}

export function loadProjectPurgeUsers(projectId: number): Promise<ProjectPurgeUser[]> {
  return api.get<ProjectPurgeUser[]>(`/projects/${projectId}/time-entries/users`)
}

export function previewProjectTimeEntryPurge(projectId: number, payload: Record<string, unknown>): Promise<ProjectPurgeStats> {
  return api.post<ProjectPurgeStats>(`/projects/${projectId}/time-entries/purge-preview`, payload)
}

export function executeProjectTimeEntryPurge(projectId: number, payload: Record<string, unknown>): Promise<ProjectPurgeStats> {
  return api.post<ProjectPurgeStats>(`/projects/${projectId}/time-entries/purge`, payload)
}

export async function preflightProjectCsvImport(projectId: number, file: File): Promise<PreflightResult> {
  const fd = new FormData()
  fd.append('file', file)
  const resp = await fetch(publicURL(`/api/projects/${projectId}/import/csv/preflight`), {
    method: 'POST',
    credentials: 'include',
    headers: csrfHeaders(),
    body: fd,
  })
  const data = await resp.json()
  if (!resp.ok) {
    throw new Error(data.error ?? 'Preflight failed.')
  }
  return data as PreflightResult
}

export async function runProjectCsvImport(
  projectId: number,
  file: File,
  strategy: CollisionStrategy,
): Promise<ProjectImportResult> {
  const fd = new FormData()
  fd.append('file', file)
  fd.append('strategy', strategy)
  const resp = await fetch(publicURL(`/api/projects/${projectId}/import/csv`), {
    method: 'POST',
    credentials: 'include',
    headers: csrfHeaders(),
    body: fd,
  })
  const data = await resp.json()
  if (!resp.ok) {
    throw new Error(data.error ?? 'Import failed.')
  }
  return data as ProjectImportResult
}

export function refreshProjectViews(): Promise<SavedView[]> {
  return api.get<SavedView[]>('/views')
}

export function saveProjectDetail(projectId: number, payload: Record<string, unknown>): Promise<Project> {
  return api.put<Project>(`/projects/${projectId}`, payload)
}

export function setProjectStatus(projectId: number, status: 'active' | 'frozen' | 'archived'): Promise<Project> {
  return api.put<Project>(`/projects/${projectId}`, { status })
}

export function deleteProjectDetail(projectId: number): Promise<void> {
  return api.delete(`/projects/${projectId}`)
}

export function addProjectTag(projectId: number, tagId: number): Promise<void> {
  return api.post(`/projects/${projectId}/tags`, { tag_id: tagId })
}

export function removeProjectTag(projectId: number, tagId: number): Promise<void> {
  return api.delete(`/projects/${projectId}/tags/${tagId}`)
}
