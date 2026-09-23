/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import { api, ApiError, parsePermissionsEpochHeader } from '@/api/client'
import wireSchema from '../../../backend/contracts/orchestration-v1.schema.json'
import { parseIsoInstant } from './agentModeAggregateSchema'
import type { OrchestrationSnapshotV1 } from './orchestrationTypes'
export type * from './orchestrationTypes'

type Schema = {
  $ref?: string
  type?: string | string[]
  const?: unknown
  enum?: unknown[]
  oneOf?: Schema[]
  properties?: Record<string, Schema>
  required?: string[]
  additionalProperties?: boolean
  items?: Schema
  minimum?: number
  maximum?: number
  minItems?: number
  maxItems?: number
  uniqueItems?: boolean
  minLength?: number
  maxLength?: number
  pattern?: string
  format?: string
}
const definitions: Record<string, Schema> = wireSchema.$defs
const invalid = (): never => {
  throw new Error('invalid orchestration response')
}

// Interpret only the keywords present in this closed wire contract. Sharing the
// canonical schema keeps nested privacy allowlists and fleet bounds from drifting.
function valid(value: unknown, schema: Schema): boolean {
  if (schema.$ref) {
    const target = definitions[schema.$ref.replace('#/$defs/', '')]
    return !!target && valid(value, target)
  }
  if ('const' in schema && value !== schema.const) return false
  if (schema.enum && !schema.enum.includes(value)) return false
  if (schema.oneOf && schema.oneOf.filter((option) => valid(value, option)).length !== 1)
    return false
  const types = Array.isArray(schema.type) ? schema.type : schema.type ? [schema.type] : []
  if (
    types.length &&
    !types.some((type) => {
      switch (type) {
        case 'null':
          return value === null
        case 'object':
          return value !== null && typeof value === 'object' && !Array.isArray(value)
        case 'array':
          return Array.isArray(value)
        case 'integer':
          return Number.isSafeInteger(value)
        case 'number':
          return typeof value === 'number' && Number.isFinite(value)
        default:
          return typeof value === type
      }
    })
  )
    return false
  if (value === null) return true
  if (
    typeof value === 'number' &&
    ((schema.minimum !== undefined && value < schema.minimum) ||
      (schema.maximum !== undefined && value > schema.maximum))
  )
    return false
  if (typeof value === 'string') {
    if (
      (schema.minLength !== undefined && [...value].length < schema.minLength) ||
      (schema.maxLength !== undefined && [...value].length > schema.maxLength) ||
      (schema.pattern && !new RegExp(schema.pattern).test(value)) ||
      (schema.format === 'date-time' && parseIsoInstant(value) === null)
    )
      return false
  }
  if (Array.isArray(value)) {
    if (
      (schema.minItems !== undefined && value.length < schema.minItems) ||
      (schema.maxItems !== undefined && value.length > schema.maxItems) ||
      (schema.uniqueItems &&
        new Set(value.map((item) => JSON.stringify(item))).size !== value.length)
    )
      return false
    return !schema.items || value.every((item) => valid(item, schema.items!))
  }
  if (typeof value === 'object') {
    const object = value as Record<string, unknown>
    if (schema.required?.some((key) => !Object.prototype.hasOwnProperty.call(object, key)))
      return false
    if (
      schema.additionalProperties === false &&
      Object.keys(object).some(
        (key) => !Object.prototype.hasOwnProperty.call(schema.properties ?? {}, key),
      )
    )
      return false
    return Object.entries(schema.properties ?? {}).every(
      ([key, child]) =>
        !Object.prototype.hasOwnProperty.call(object, key) || valid(object[key], child),
    )
  }
  return true
}

export function parseOrchestrationSnapshot(value: unknown): OrchestrationSnapshotV1 {
  if (!valid(value, definitions.OrchestrationSnapshotV1!)) invalid()
  const result = value as OrchestrationSnapshotV1
  const { fleet, instance_root: root, coordination_bounds: bounds } = result
  const workers = new Map(fleet.workers.map((worker) => [worker.harness_session_id, worker]))
  const projects = new Map(result.project_coordination.map((row) => [row.project.id, row]))
  const zoomLimit = fleet.zoom.length < 3 ? Number(fleet.zoom) : 100
  const band = ['detail', 'overview', 'aggregate', 'far'][Math.min(3, fleet.zoom.length - 1)]
  if (
    fleet.sample_limit !== zoomLimit ||
    fleet.band !== band ||
    bounds.sample_limit !== zoomLimit ||
    workers.size !== fleet.workers.length ||
    projects.size !== result.project_coordination.length ||
    fleet.workers.length > zoomLimit ||
    projects.size > zoomLimit ||
    bounds.sampled_projects !== projects.size ||
    bounds.total_projects - projects.size !== bounds.omitted_projects ||
    fleet.totals.sampled_workers !== workers.size ||
    fleet.totals.workers - workers.size !== fleet.totals.omitted_workers ||
    fleet.totals.sampled_projects !== fleet.projects.length ||
    fleet.totals.projects - fleet.projects.length !== fleet.totals.omitted_projects ||
    fleet.sample_truncated !== fleet.totals.omitted_workers > 0 ||
    (fleet.scope.kind === 'project') !== (fleet.scope.project_id !== null)
  )
    invalid()
  for (const worker of fleet.workers) {
    const parent =
      worker.parent_harness_session_id === null
        ? undefined
        : workers.get(worker.parent_harness_session_id)
    if (
      !projects.has(worker.project.id) ||
      (fleet.scope.project_id !== null && worker.project.id !== fleet.scope.project_id) ||
      worker.parent_in_sample !== !!parent ||
      (parent && parent.project.id !== worker.project.id) ||
      worker.liveness.observed_at !== fleet.observed_at ||
      worker.work_contract.shape !== worker.work_shape
    )
      invalid()
    const ancestors = new Set([worker.harness_session_id])
    let ancestor = parent
    while (ancestor) {
      if (ancestors.has(ancestor.harness_session_id)) invalid()
      ancestors.add(ancestor.harness_session_id)
      ancestor =
        ancestor.parent_harness_session_id === null
          ? undefined
          : workers.get(ancestor.parent_harness_session_id)
    }
    if (
      worker.runtime_provenance_trust === 'untrusted' &&
      (worker.machine_id !== null ||
        worker.workspace_provenance !== null ||
        worker.dispatch_profile !== null ||
        worker.account_label !== 'unknown' ||
        worker.account_key)
    )
      invalid()
    if (
      worker.runtime_provenance_trust === 'managed_reporter' &&
      (worker.management_mode !== 'managed' ||
        worker.machine_id === null ||
        worker.workspace_provenance === null)
    )
      invalid()
  }
  for (const row of result.project_coordination) {
    const coordinator = row.coordinator
    const worker = coordinator.session_id === null ? undefined : workers.get(coordinator.session_id)
    if (
      (fleet.scope.project_id !== null && row.project.id !== fleet.scope.project_id) ||
      row.root_binding_revision !==
        (root.configured_identity === null ? null : root.binding_revision) ||
      (coordinator.state === 'resolved') !== (coordinator.session_id !== null) ||
      (coordinator.state === 'resolved' &&
        (!worker ||
          worker.project.id !== row.project.id ||
          worker.role !== 'coordinator' ||
          !['busy', 'idle'].includes(worker.liveness.state)))
    )
      invalid()
  }
  for (const project of fleet.projects) {
    const row = projects.get(project.id)
    const count = fleet.workers.filter((worker) => worker.project.id === project.id).length
    if (
      !row ||
      row.project.key !== project.key ||
      row.project.name !== project.name ||
      row.coordinator.state !== project.orchestrator.state ||
      row.coordinator.reason !== project.orchestrator.reason ||
      row.coordinator.session_id !== project.orchestrator.session_id ||
      project.sampled_workers !== count ||
      project.total_workers - count !== project.omitted_workers
    )
      invalid()
  }
  const active = root.active_generation
  const generation = active.session_id === null ? undefined : workers.get(active.session_id)
  const reviewerScope =
    active.reason === 'reviewer_scope' &&
    active.state === 'unknown' &&
    active.session_id === null &&
    root.configured_identity === null &&
    root.binding_revision === 0 &&
    root.binding_updated_at === null
  if (
    (active.state === 'resolved') !== (active.session_id !== null) ||
    (active.reason === 'reviewer_scope') !== reviewerScope ||
    (root.configured_identity === null &&
      !reviewerScope &&
      (active.state !== 'unset' || active.reason !== 'root_not_configured')) ||
    (active.state === 'resolved' &&
      (!generation ||
        root.configured_identity === null ||
        generation.role !== 'coordinator' ||
        !['busy', 'idle'].includes(generation.liveness.state)))
  )
    invalid()
  return result
}

export async function loadOrchestration(
  options: { projectId?: number; zoom?: string | number; signal?: AbortSignal } = {},
): Promise<OrchestrationSnapshotV1> {
  const { projectId, zoom = '10' } = options
  if (
    (projectId !== undefined && (!Number.isSafeInteger(projectId) || projectId <= 0)) ||
    !/^[1-9][0-9]{0,63}$/.test(String(zoom))
  ) {
    throw new Error('invalid orchestration request')
  }
  const path =
    projectId === undefined
      ? '/agent-mode/orchestration/v1'
      : `/agent-mode/projects/${projectId}/orchestration/v1`
  const response = await api.getWithMeta<unknown>(`${path}?zoom=${zoom}`, {
    signal: options.signal,
  })
  if (
    response.permissionsEpoch == null ||
    parsePermissionsEpochHeader(response.permissionsEpoch) !== response.permissionsEpoch ||
    !Number.isSafeInteger(response.permissionsEpochGeneration) ||
    response.permissionsEpochGeneration < 0
  )
    throw new ApiError(response.status, 'Worker snapshot is missing its authority epoch')
  const result = parseOrchestrationSnapshot(response.data)
  if (result.fleet.scope.project_id !== (projectId ?? null) || result.fleet.zoom !== String(zoom))
    invalid()
  return result
}
