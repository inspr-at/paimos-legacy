/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import type {
  IssuePriority,
  IssueStatus,
  IssueType,
  KnowledgeStatus,
  ProjectStatus,
  RelationType,
} from './types/generated/schema'

export {
  ENUM_FIELDS,
  ISSUE_PRIORITIES,
  ISSUE_STATUSES,
  ISSUE_TYPES,
  KNOWLEDGE_STATUSES,
  KNOWLEDGE_TYPES,
  PROJECT_STATUSES,
  RELATION_TYPES,
  TAG_COLORS,
} from './types/generated/schema'
export type {
  IssuePriority,
  IssueStatus,
  IssueType,
  KnowledgeStatus,
  ProjectStatus,
  KnowledgeType,
  RelationType,
  TagColor,
} from './types/generated/schema'

// PAI-53. Customer record. CRM-agnostic by design — external_* and
// synced_at are all nullable; NULL across all three = a manually-managed
// customer. The provider plugin layer (PAI-101) populates the externals
// when an admin imports from a CRM.
//
// PAI-273: contact_name / contact_email / address / country are kept
// here for one release as a read-compat shim — the canonical contact
// lives in the `contacts` table now (Contact entity, fetched from
// /api/customers/:id/contacts). New metadata fields (website / vat_id /
// employee_count / annual_revenue_cents / description / phone / billing
// & visit address quartets) are first-class.
export interface Customer {
  customer_no?: string | null
  id: number
  name: string
  external_id: string | null
  external_url: string | null
  external_provider: string | null
  synced_at: string | null
  // Legacy (kept for one release; populated from the primary Contact
  // when one exists, otherwise from the legacy column).
  contact_name: string
  contact_email: string
  address: string
  country: string
  industry: string
  // PAI-273 metadata expansion. All optional / empty-default.
  website: string
  domain: string
  vat_id: string
  tax_id: string
  company_register_number: string
  employee_count: number | null
  annual_revenue_cents: number | null
  description: string
  phone: string
  billing_address_street: string
  billing_address_city: string
  billing_address_zip: string
  billing_address_country: string
  visit_address_street: string
  visit_address_zip: string
  rate_hourly: number | null
  rate_lp: number | null
  notes: string
  created_at: string
  updated_at: string
  project_count?: number
}

// PAI-273. One customer holds many Ansprechpartner. Exactly one is
// is_primary at a time. external_* lets HubSpot Contact sync upsert by
// (provider, external_id) idempotently.
export interface Contact {
  id: number
  customer_id: number
  name: string
  email: string
  phone: string
  role: string
  is_primary: boolean
  notes: string
  external_id: string | null
  external_provider: string | null
  external_url: string | null
  synced_at: string | null
  created_at: string
  updated_at: string
}

// PAI-55. Document metadata; the file bytes live in MinIO.
export interface Document {
  id: number
  scope: 'customer' | 'project'
  customer_id: number | null
  project_id: number | null
  filename: string
  mime_type: string
  size_bytes: number
  label: string
  status: 'draft' | 'active' | 'expired'
  valid_from: string | null
  valid_until: string | null
  uploaded_by: number | null
  uploaded_at: string
  updated_at: string
}

// PAI-101. CRM provider metadata, as returned by /api/integrations/crm.
// `useExternalProvider(id)` resolves a provider id (e.g. customer.external_provider)
// to one of these so the UI never hardcodes "HubSpot" anywhere.
export interface ExternalProvider {
  id: string
  name: string
  logo_url: string
  enabled: boolean
  configured: boolean
  schema: ExternalProviderSchema
  /** PAI-259: provider implements the optional ConnectionTester hook. */
  test_supported?: boolean
}

/** PAI-259: response shape of POST /api/integrations/crm/{id}/test */
export interface CRMTestResult {
  ok: boolean
  message: string
  lines?: string[]
}

export interface ExternalProviderSchema {
  fields: ExternalProviderField[]
}

export interface ExternalProviderField {
  key: string
  label: string
  type: 'string' | 'secret' | 'number' | 'select'
  required: boolean
  help?: string
  placeholder?: string
  options?: { value: string; label: string }[]
}

// Per-provider config view returned by /api/integrations/crm/:id/config.
// Secret fields never carry the actual value — only `has_value: true`.
export interface ExternalProviderConfig {
  provider_id: string
  enabled: boolean
  fields: ExternalProviderConfigField[]
}

export interface ExternalProviderConfigField extends ExternalProviderField {
  value?: string
  has_value: boolean
}

// PAI-61. Per-project cooperation metadata. Informational in v1 — no
// behavioural effects elsewhere. Structured fields are nullable; the
// backend's GET endpoint returns zero-value defaults so the UI never
// has to special-case "no row yet".
export interface CooperationMetadata {
  project_id: number
  engagement_type: 'consultancy' | 'project_delivery' | 'managed_service' | 'retainer' | null
  code_ownership: 'client_repo' | 'own_repo' | 'mixed' | null
  env_responsibility: 'dev_staging' | 'dev_staging_prod' | 'full_stack' | null
  has_sla: boolean
  uptime_sla: string
  response_time_sla: string
  backup_responsible: boolean
  oncall: boolean
  sla_details: string
  cooperation_notes: string
  report_contract_basis: string
  report_terms_url: string
  report_objection_period_days: number
  report_customer_responsibilities: string
  report_contractor_responsibilities: string
  created_at?: string
  updated_at?: string
}

export interface ProjectReportPermission {
  id: number
  project_id: number
  person_name: string
  company: string
  role_label: string
  may_approve: boolean
  may_deliver: boolean
  may_accept: boolean
  sort_order: number
}

export interface ProjectReportSnapshot {
  id: number
  project_id: number
  project_key: string
  project_name: string
  code: string
  report_key: string
  report_type: string
  lang: string
  filter_query: string
  issue_ids: number[]
  total_issues: number
  pdf_sha256: string
  status: 'generated' | 'accepted' | 'void'
  signed_document_id: number | null
  signed_at: string | null
  signer_name: string
  signer_company: string
  signer_role: string
  accepted_at: string | null
  accepted_by: number | null
  accept_summary: Record<string, number>
  acceptance_url: string
  created_by: number | null
  created_at: string
  updated_at: string
  eligible_count?: number
  already_final_count?: number
  skipped_count?: number
}

export interface Attachment {
  id: number
  issue_id: number
  object_key: string
  filename: string
  content_type: string
  size_bytes: number
  uploaded_by: number
  uploader: string
  created_at: string
}

export interface Tag {
  id: number
  name: string
  color: string
  description: string
  system?: boolean
  created_at: string
}

export interface Project {
  id: number
  name: string
  key: string
  description: string
  status: ProjectStatus
  product_owner: number | null
  // PAI-54: customer_label is the freeform legacy text; customer_id is
  // the FK into the customers table (PAI-53). Both nullable.
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
  rate_hourly?: number | null
  rate_lp?: number | null
  // PAI-54: effective rates after customer→project cascading; the
  // backend computes these so the UI doesn't have to.
  effective_rate_hourly?: number | null
  effective_rate_lp?: number | null
  rate_inherited?: boolean
  // PAI-356: detail-only aggregate. Returned by GET /api/projects/:id
  // for the footer-bar badges (open issues + knowledge entries).
  // Absent on list responses; treat as null when undefined.
  counts?: ProjectCounts
  ai_defaults?: ProjectAIDefaults
  ai_policy?: ProjectAIPolicy
}

export interface ProjectAIDefaultSet {
  profile_id?: string
  effort?: string
  prompt_preset_ref?: string
  context_pack?: string
  preferred_provider_class?: string
}

export interface ProjectAIDefaults {
  global?: ProjectAIDefaultSet
  actions?: Record<string, ProjectAIDefaultSet>
  runs?: Record<string, ProjectAIDefaultSet>
  agents?: Record<string, ProjectAIDefaultSet>
}

export interface ProjectAIPolicy {
  disable_hosted_draft?: boolean
  disable_local_model_draft?: boolean
}

// PAI-356 — counts surfaced on the project detail response. Both
// fields are non-negative ints; null/undefined means the count
// hasn't been loaded yet (e.g. during initial mount before the
// detail GET completes).
export interface ProjectCounts {
  open_issues: number
  knowledge_entries: number
}

// PAI-326 + PAI-329. ProjectAgent declares an agent that works a
// project (e.g. `ops`, `dev`, `refinement`). `name` is the canonical
// project-scoped key (slug-style, max 32 chars, [a-z][a-z0-9_-]*).
//
// PAI-329 adds the rendering shape:
//   - body: free-text markdown — bulk of the rendered skill body.
//   - bootstrap_steps[]: ordered "do these once at session start"
//     actions — each {title, command, rationale}.
//   - non_negotiable_rules[]: rules the agent must NEVER silently
//     break — each {title, body, memory_ref}. memory_ref is a
//     pass-through string here; resolution happens at render time
//     (PAI-330).
export interface AgentBootstrapStep {
  title: string
  command: string
  rationale: string
}

export interface AgentRule {
  title: string
  body: string
  memory_ref: string
}

export interface ProjectAgent {
  id: number
  project_id: number
  name: string
  description: string
  slash_command_name: string
  lane_tags: string[]
  metadata: Record<string, unknown>
  body: string
  bootstrap_steps: AgentBootstrapStep[]
  non_negotiable_rules: AgentRule[]
  created_at?: string
  updated_at?: string
}

export interface ProjectAgentInput {
  name: string
  description: string
  slash_command_name: string
  lane_tags: string[]
  metadata: Record<string, unknown>
  body: string
  bootstrap_steps: AgentBootstrapStep[]
  non_negotiable_rules: AgentRule[]
}

// PAI-329. ProjectEnvironment / ProjectDeployRecipe are the project-
// level shared inventories every agent inherits at render time.
export interface ProjectEnvironment {
  id: number
  project_id: number
  name: string
  url: string
  host_alias: string
  host_ip: string
  sort_order: number
  created_at?: string
  updated_at?: string
}

export interface ProjectEnvironmentInput {
  name: string
  url: string
  host_alias: string
  host_ip: string
  sort_order?: number
}

export interface ProjectDeployRecipe {
  id: number
  project_id: number
  name: string
  command: string
  summary: string
  sort_order: number
  created_at?: string
  updated_at?: string
}

export interface ProjectDeployRecipeInput {
  name: string
  command: string
  summary: string
  sort_order?: number
}

// PAI-339 / PAI-338. Knowledge plane — memory / runbook /
// external_system / related_project / guideline. Per PAI-346 these
// are issues with a discriminating `type` and a slug; the convenience
// endpoints (`/api/projects/:id/{memory|runbooks|external-systems|...}`)
// hide that. Per-category tail fields live in `metadata`; the editor
// UI surfaces a small set of well-known keys per type but the column
// itself is intentionally schemaless so the front-end can iterate
// without server changes (see PAI-338's "validation is intentionally
// lax" remark).
export type KnowledgeCategory =
  | 'memory'
  | 'runbook'
  | 'external_system'
  | 'related_project'
  | 'guideline'

// URL segment per knowledge category — the kebab-singular form
// the unified /api/projects/{id}/knowledge surface accepts (PAI-394).
// External-facing callers should prefer KNOWLEDGE_URL_SEGMENT over
// the SQL discriminator when building URLs so the wire form stays
// consistent across the codebase.
export const KNOWLEDGE_URL_SEGMENT: Record<KnowledgeCategory, string> = {
  memory: 'memory',
  runbook: 'runbook',
  external_system: 'external-system',
  related_project: 'related-project',
  guideline: 'guideline',
}

export interface KnowledgeEntry {
  id: number
  project_id: number
  type: KnowledgeCategory
  slug: string
  title: string
  body: string
  // Server uses 'backlog' as default-active and 'cancelled' for
  // archived (PAI-346 §"Status values"). The UI labels them as
  // "active" / "archived" but speaks the existing enum on the wire.
  status: string
  metadata: Record<string, unknown>
  created_at: string
  updated_at: string
  // PAI-347 — reference-count tracking for memory entries. Maintained
  // server-side by bundle-include + auto-suggest paths.
  reference_count?: number
  last_referenced_at?: string | null
  // PAI-351 slice 2 — derived "needs re-review" flag (a depends_on parent
  // was revised after this entry's last review). Computed server-side on
  // the knowledge list / graph / dependents paths; never stored.
  needs_review?: boolean
  review_reason?: string
  // PAI-348 — present only on entries that arrived via inheritance
  // through `paimos session start --bundle full`. The convenience
  // CRUD endpoints never set this — own entries on the project's
  // Knowledge tab always have `source` undefined. The Knowledge tab
  // renders a badge + click-through when present.
  source?: KnowledgeEntrySource
}

export interface KnowledgeEntrySource {
  // 'inherited' = entry was pulled from `from_project` on
  // `from_instance`. 'warning' = cross-instance inheritance failed;
  // `message` carries the cause.
  type: 'inherited' | 'warning'
  from_project?: string
  from_instance?: string
  message?: string
}

export interface KnowledgeEntryInput {
  slug: string
  title: string
  body: string
  status?: string
  metadata: Record<string, unknown>
}

export interface ProjectRepo {
  id: number
  project_id: number
  url: string
  default_branch: string
  label: string
  sort_order: number
  created_at: string
  updated_at: string
}

// PAI-358: ProjectManifest type deleted with the manifest table.

export interface IssueAnchor {
  id: number
  project_id: number
  issue_id: number
  repo_id: number
  repo_label: string
  repo_url: string
  default_branch: string
  file_path: string
  line: number
  label: string
  confidence: 'declared' | 'derived' | 'suggested'
  symbol_json: string
  schema_version: string
  repo_revision: string
  generated_at: string
  hidden: boolean
  stale: boolean
  deep_link?: string | null
  updated_at: string
}

export interface IssueRelation {
  source_id: number
  target_id: number
  // Convention: source = container/owner, target = member/child.
  // sprint:     source = sprint, target = member issue.
  // groups:     source = epic/cost_unit/release, target = ticket.
  // depends_on:   source = dependent, target = dependency.
  // impacts:      source = impactor, target = impacted.
  // follows_from: source = spin-off, target = predecessor (PAI-89).
  // blocks:       source = blocker, target = blocked (PAI-89).
  // related:      loose "see also" — direction is cosmetic (PAI-89).
  // applies_to_memory: source = ticket, target = memory entry (PAI-342).
  type: RelationType
  target_key?: string
  target_title?: string
  // "outgoing" when the issue whose /relations endpoint was called is
  // this row's source_id, "incoming" otherwise. Lets the UI render
  // inverse labels without storing a second row. Added in PAI-89.
  direction?: 'outgoing' | 'incoming'
}

// PAI-599: edge-sourced cost_unit/release reference — the container issue's
// id + its title (label). Set by sending a string label on create/update.
export interface LabelRef {
  id: number
  label: string
}

export interface Issue {
  id: number
  project_id: number | null
  issue_number: number
  issue_key: string
  type: IssueType
  parent_id: number | null
  title: string
  description: string
  acceptance_criteria: string
  notes: string
  // PAI-418: customer-facing report-text used by Projektbericht.
  // One field; the audience style (warm customer copy vs technical
  // exec TL;DR) is picked at AI-generation time.
  report_summary: string
  status: IssueStatus
  priority: IssuePriority
  // PAI-599: edge-sourced container reference ({id,label}) or null. Set on
  // create/update by sending a string label; returned as an object.
  cost_unit: LabelRef | null
  release: LabelRef | null
  // v2 group/sprint fields (nullable)
  billing_type: 'time_and_material' | 'fixed_price' | 'mixed' | null
  total_budget: number | null
  rate_hourly: number | null
  rate_lp: number | null
  estimate_hours: number | null
  estimate_lp: number | null
  ar_hours: number | null
  ar_lp: number | null
  time_override: number | null
  start_date: string | null
  end_date: string | null
  group_state: string | null
  sprint_state: string | null
  jira_id: string | null
  jira_version: string | null
  jira_text: string | null
  // epic color — optional visual accent for epic badges
  color: string | null
  // sprint membership — IDs of sprint issues this issue belongs to
  sprint_ids: number[]
  archived: boolean
  assignee_id: number | null
  assignee: { id: number; username: string; role: string } | null
  children?: Issue[]
  tags: Tag[]
  created_at: string
  updated_at: string
  created_by: number | null
  created_by_name: string
  last_changed_by_name: string
  booked_hours: number
  time_logged: number
  time_rollup: number
  time_total: number
  accepted_at: string | null
  accepted_by: number | null
  invoiced_at: string | null
  invoice_number: string
  ai_work_status?: IssueAIWorkStatus | null
}

export interface IssueAIWorkStatus {
  id: number
  status: string
  agent_name: string
  device_id: string
  action_key: string
  provider_kind: string
  provider_id: string
  provider_label: string
  model: string
  run_mode: string
  profile_id: string
  effort: string
  prompt_preset_ref: string
  context_pack: string
  version: string
  deploy_target: string
  tests_summary: string | null
  error: string
  created_at: string
  started_at: string | null
  finished_at: string | null
}

export interface AgentActionCapability {
  action_key: string
  provider_kind: string
  provider_id: string
  label: string
  run_modes?: string[]
  can_test: boolean
  can_deploy: boolean
  available?: boolean
  unavailable_reason?: string
  requires_runner?: boolean
  profile_ids?: string[]
  efforts?: string[]
  models?: string[]
  context_limit_tokens?: number
  endpoint_label?: string
}

export interface IssueListEnvelope<T = Issue> {
  issues: T[]
  total: number
  returned?: number
  offset: number
  limit: number
  has_more?: boolean
  sort?: string
  order?: 'asc' | 'desc' | ''
  query?: string
  revision?: string
  fingerprint?: string
  selection_fingerprint?: string
}

export interface TimeEntry {
  id: number
  issue_id: number // renamed from ticket_id in migration 32
  user_id: number
  username: string
  started_at: string
  stopped_at: string | null
  override: number | null
  material_lp: number | null // PAI-581: material booked (LP / token cost)
  comment: string
  created_at: string
  internal_rate_hourly: number | null
  hours: number | null
  issue_key?: string
  issue_title?: string
  project_id?: number
}

export interface SavedView {
  id: number
  user_id: number
  owner_username: string
  title: string
  description: string
  columns_json: string // JSON array of hidden column keys
  filters_json: string // JSON object matching SavedFilters shape
  is_shared: boolean
  is_admin_default: boolean
  sort_order: number
  hidden: boolean
  pinned: boolean | null // per-user pin state; null = no explicit choice (lazy init)
  created_at: string
  updated_at: string
}

export interface Sprint {
  id: number
  title: string
  start_date: string
  end_date: string
  archived: boolean
  sprint_state: string
  target_ar?: number | null
}

// Canonical User — used everywhere. Local interface stubs are deprecated.
export interface User {
  id: number
  username: string
  role: 'admin' | 'member' | 'reviewer' | 'external' | 'super_admin'
  status: 'active' | 'inactive' | 'deleted'
  nickname: string
  first_name: string
  last_name: string
  email: string
  avatar_path: string
  markdown_default: boolean
  monospace_fields: boolean
  recent_projects_limit: number
  internal_rate_hourly: number | null
  show_alt_unit_table: boolean
  show_alt_unit_detail: boolean
  locale: string
  preview_hover_delay: number
  issue_auto_refresh_enabled: boolean
  issue_auto_refresh_interval_seconds: number
  // PAI-368 / M103: search-scope shortcut (JSON blob; '' = disabled).
  search_scope_shortcut: string
  // PAI-706 / M135: voice-intake auto-switch threshold override (null = instance default)
  intake_confidence_threshold?: number | null
  last_login_at: string | null
  created_at: string
  totp_enabled: boolean
  // PAI-336: compatibility flag. The canonical public role is now
  // `super_admin`; this remains for older clients and cautious UI gates.
  is_super_admin: boolean
}
