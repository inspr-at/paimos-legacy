// PAIMOS — Copyright (C) 2026 Markus Barta
// Wire types for backend/contracts/orchestration-v1.schema.json.

export type WorkerFleetSnapshotV2 = {
  schema_version: 2
  observed_at: string
  scope: {
    kind: 'portfolio' | 'project'
    project_id: number | null
  }
  zoom: string
  band: 'detail' | 'overview' | 'aggregate' | 'far'
  sample_limit: number
  sample_truncated: boolean
  totals: {
    projects: number
    sampled_projects: number
    omitted_projects: number
    workers: number
    sampled_workers: number
    omitted_workers: number
  }
  projects: {
    id: number
    key: string
    name: string
    total_workers: number
    sampled_workers: number
    omitted_workers: number
    orchestrator: {
      state: 'resolved' | 'unset' | 'ambiguous'
      reason:
        | 'single_active_coordinator'
        | 'no_active_coordinator'
        | 'multiple_active_coordinators'
        | 'coordinator_unknown'
      session_id: string | null
    }
  }[]
  workers: {
    harness_session_id: string
    parent_harness_session_id: string | null
    parent_in_sample: boolean
    project: {
      id: number
      key: string
      name: string
    }
    agent: {
      id: number
      name: string
    }
    role: 'coordinator' | 'worker'
    harness: string
    machine_id: string | null
    workspace_provenance: WorkerWorkspaceProvenance | null
    dispatch_profile: HarnessDispatchProfile | null
    account_label:
      | 'unknown'
      | 'chatgpt'
      | 'api_key'
      | 'claude_ai_max'
      | 'claude_ai_pro'
      | 'claude_ai_team'
      | 'claude_ai_enterprise'
      | 'console'
      | 'pi_context'
      | 'cursor_context'
    account_key?: string
    management_mode: 'managed' | 'unmanaged'
    runtime_provenance_trust: 'managed_reporter' | 'untrusted'
    ticket: {
      id: number
      details_available: boolean
      key: string | null
      title: string | null
    } | null
    work_shape: 'unknown' | 'ship' | 'scout'
    work_contract: WorkShapeContract
    phase: 'starting' | 'working' | 'yielded' | 'stopping' | 'stopped'
    revision: number
    liveness: {
      state: 'busy' | 'idle' | 'unknown' | 'dead'
      reason: string
      observed_at: string
      source: 'agentd_reporter' | 'control_plane' | 'unmanaged' | 'unknown'
      reporter_age_seconds: number | null
      closed_reason?: string
    }
    capabilities: {
      inbox: boolean
      status: boolean
      steer: boolean
      interrupt: boolean
      stop: boolean
    }
    recent_communication: {
      message_id: string
      delivery_id: string | null
      direction: 'incoming' | 'outgoing'
      attribution: 'project_agent'
      requested_level: 'simple' | 'steer' | null
      effective_level: 'simple' | 'steer' | null
      state: string | null
      fallback_code: string | null
      error_code: string | null
      occurred_at: string
    }[]
    recent_communication_omitted: number
    delivery_trust: {
      progress_trusted: boolean
      eta_trusted: boolean
      reason:
        | 'trusted'
        | 'ticket_unbound'
        | 'trust_unavailable'
        | 'trust_malformed'
        | 'no_estimate'
        | 'terminal_complete'
        | 'cancelled'
        | 'terminal_failed'
        | 'waiting_on_human'
        | 'blocked'
        | 'stale'
        | 'unknown_reporter'
        | 'no_signal'
        | 'estimate_expired'
        | 'outlier_heavy'
        | 'insufficient_basis'
        | 'missing_contributor'
      trust_revision: string | null
      observed_at: string | null
      progress_percent: number | null
      eta: string | null
    }
  }[]
  provenance: {
    source: 'authoritative_database'
    cache: 'none'
    remote_cache: false
    projection_version: 2
    terminal_generations_per_agent: 1
  }
}

export type WorkerWorkspaceProvenance = {
  kind: 'directory' | 'git_primary' | 'git_worktree'
  mode: 'exclusive' | 'shared'
}

export type HarnessDispatchProfile = {
  family?: 'openai' | 'anthropic' | 'xai' | 'cursor'
  tier?: 'fast' | 'standard' | 'strong' | 'frontier'
  id: string
  version: string
  harness: 'codex' | 'claude' | 'pi' | 'cursor'
  model: string
  effort: 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'default'
  machine_source: 'authenticated_reporter'
  account_source: 'local_probe'
  workspace_mode: 'exclusive' | 'shared'
}

export type WorkShapeContract = {
  shape: 'unknown' | 'ship' | 'scout'
  output_kind: 'unclassified' | 'delivery' | 'investigation_evidence'
  stage_applicability: {
    stage: 'specification' | 'implementation' | 'qa' | 'deployment' | 'verification'
    applicability: 'required' | 'not_applicable' | 'unknown'
  }[]
  definition_of_done: (
    | 'implementation_complete'
    | 'required_stages_succeeded'
    | 'delivery_verified'
    | 'investigation_question_answered'
    | 'evidence_recorded'
    | 'uncertainty_reported'
    | 'explicit_classification_required'
  )[]
  non_goals: (
    | 'product_delivery'
    | 'git_enforcement'
    | 'silent_promotion'
    | 'delivery_claim'
    | 'shape_inference'
  )[]
}

export type OrchestrationCoordinatorV1 = {
  state: 'resolved' | 'unset' | 'ambiguous'
  reason:
    | 'single_active_coordinator'
    | 'no_active_coordinator'
    | 'multiple_active_coordinators'
    | 'coordinator_unknown'
  session_id: string | null
}

export type OrchestrationRootV1 = {
  configured_identity: {
    display_label: string
  } | null
  binding_revision: number
  binding_updated_at: string | null
  active_generation: {
    state: 'resolved' | 'unset' | 'ambiguous' | 'unknown'
    reason:
      | 'root_not_configured'
      | 'generation_unavailable'
      | 'no_active_root_generation'
      | 'multiple_active_root_generations'
      | 'generation_not_in_sample'
      | 'root_generation_unknown'
      | 'single_active_root_generation'
      | 'reviewer_scope'
    session_id: string | null
  }
}

export type ProjectCoordinationV1 = {
  project: {
    id: number
    key: string
    name: string
  }
  coordinator: OrchestrationCoordinatorV1
  relationship: 'instance_root_coordination'
  root_binding_revision: number | null
}

export type OrchestrationSnapshotV1 = {
  schema_version: 1
  instance_root: OrchestrationRootV1
  fleet: WorkerFleetSnapshotV2
  project_coordination: ProjectCoordinationV1[]
  coordination_bounds: {
    sample_limit: number
    total_projects: number
    sampled_projects: number
    omitted_projects: number
  }
}
