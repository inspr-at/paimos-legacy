# PAIMOS API — Quick Reference

Base URL: `https://paimos.example.com/api`  
Auth: `Authorization: Bearer <key>`  
Format: JSON in, JSON out.

---

## Auth

```
GET    /auth/me
POST   /auth/login                  {username, password}
POST   /auth/password               {current_password, new_password}
                                      — invalidates all *other* sessions; clears must_change_password
POST   /auth/impersonation/start    {user_id} — super_admin only
POST   /auth/impersonation/end      {} — exits active impersonation
POST   /auth/api-keys               {name} → {key} (shown once)
GET    /auth/api-keys
DELETE /auth/api-keys/:id
```

Every authenticated response carries two session-state response
headers (PAI-320 / PAI-322):

- `X-Session-Expires-At` — RFC3339 absolute expiry of the current
  session. Sessions slide on each request up to a 90-day absolute
  cap; the SPA uses this to render the unified expiry modal.
- `X-Permissions-Epoch` — per-user counter bumped on role /
  membership / status change. Clients compare against the value at
  login; a mismatch means cached capability decisions are stale.

Newly created users with `must_change_password=true` (the default)
get `403 {"error":"must_change_password"}` on every endpoint except
`/auth/login`, `/auth/me`, `/auth/logout`, and `/auth/password`
until they POST a new password.

## Projects

```
GET    /projects                    ?status=active|frozen|archived|deleted|all (default active; all excludes deleted)
POST   /projects                    {name, key, description}
GET    /projects/:id
PUT    /projects/:id                partial update; status=active|frozen|archived|deleted
DELETE /projects/:id                admin only
GET    /projects/:id/repos
POST   /projects/:id/repos          {url, default_branch, label, sort_order}
PUT    /projects/:id/repos/:repoId  partial update
DELETE /projects/:id/repos/:repoId
# PAI-358 (v3.0): /projects/:id/manifest endpoints removed; legacy
# taxonomy fully replaced by the PAI-338 knowledge plane.
POST   /projects/:id/anchors        {repo_id, schema_version, repo_revision, generated_at, anchors}
GET    /projects/:id/graph          ?root=issue:42&depth=2
GET    /projects/:id/graph/blast-radius ?issue=PAI-79&depth=3
POST   /projects/:id/retrieve       {q, k}
GET    /projects/:id/baseline-batches/
POST   /projects/:id/baseline-batches/opt-in
POST   /projects/:id/baseline-batches/import
PATCH  /projects/:id/baseline-batches/:draftID
POST   /projects/:id/baseline-batches/:draftID/readiness
POST   /projects/:id/baseline-batches/:draftID/review
POST   /projects/:id/baseline-batches/:draftID/start
GET    /projects/:id/baseline-batches/:draftID/export
GET    /projects/:id/baseline-batches/batches/:batchID
POST   /projects/:id/baseline-batches/batches/:batchID/control
POST   /projects/:id/baseline-batches/batches/:batchID/reconcile
POST   /projects/:id/baseline-batches/batches/:batchID/built-receipt
```

PAI-956 baseline batches are an opt-in INSPR stream on a project: every route
above answers 409 until an authorized human posts `opt-in`, and a project that
never opts in behaves exactly as before. Imported `aithema.handover/0.1` JSON is
a claim: `approved_by` never authenticates a human. Start requires a current
human session, project edit, and an explicit confirmation bound to
digest/seal/revision/scope/mode.

Automatic mode still grants no host-launch authority by itself. A human may,
during deliberate review, opt into one `delegated_launch` selection bound to
that review: one server-listed project-environment `target_ref`, workflow
`deploy-production`, its exact environment symbol, `max_launches:1`, and an
expiry in the next 24 hours. Start revalidates that selection with the same
current, non-impersonated editor session and mints a batch-bound root grant.
Omitting the selection is the default and preserves attended behavior. The
separate `revoke_launch` batch control irreversibly closes an unused grant.

Assisted and automatic modes require a current **owned readiness observation**:
`readiness` submits an `inspr.readiness.v1` probe as a lifecycle intent, the
operator-owned daemon claims it, runs the host checks (`host_kind`,
`activated_generation`, `doctrine_loader`, `workspace_isolation`,
`tool_prerequisites`, `paimos_runtime_doctor`, `paimos_account`) and reports the
observation back through the lifecycle transition. The server binds it to
project, runtime generation, account, dispatch profile, workspace and baseline
digest, keeps the daemon-reported observation time, and clamps freshness to the
runtime registration. Missing, stale, non-ready or unbindable evidence blocks the
start with a typed reason; client `ready:true` is refused outright. Manual mode
does not invent an AI account.

Batch state, stage progress, forecasts and the available controls are derived
from the delivery attempt, the lifecycle intent and the owned harness session on
every read. `completed` follows only from every required delivery stage being
satisfied, never from a worker finishing. Human review of the immutable Aithema
baseline records specification evidence only. Implementation and QA are recorded
by `POST .../built-receipt` (`paimos baseline-batch report-built`): a typed OCI
config digest, release-set digest/coordinate/scheme/channel/sequence/version,
source commit, and scoped QA digest on the current sealed attempt. That receipt
is authenticated reported evidence, not cryptographic proof a test ran, and it
never starts deployment or mints handoff secrets. Automatic receipts may use the
start confirmer's live `agent-controls:write` key bound to the selected worker
account and runtime generation; they are stored as system reporters, not human
evidence. `POST /api/issues/{id}/implement` is refused on any issue that already
has a baseline-batch row, in any `control_state`, so BootstrapRunTx cannot
rewrite the accepted specification. That seal is historical: cancelling or
completing the batch does not reopen Implement-this on the same issue. Later
work is a new Aithema handover, draft, and start, which mints a fresh issue
and delivery. A lookup failure refuses with a server error before any run is
created; it never treats an unknown ownership check as “not a baseline issue”.
Pharos deployment and verification reuse the existing
external-stage `RegisterReporter` / `ActivateOwner` / `SealPrerequisites` /
`CreateHandoff` path. `POST .../reconcile` applies the next currently authorized
step of that frozen plan: manual never auto-creates a handoff, assisted requires
the current editor session, and automatic may use that session or a live scoped
API key with `agent-controls:write`. Missing Pharos registration, an unsealed
prerequisite set, a sealed set that does not cover currently active Janus
dependencies (`prerequisite_review`), incomplete built artifact identity
(`built_artifact_identity`), or a revoked current-generation handoff is
`setup_required` plus `next_action` (`human_review_required` when specification
is unsatisfied; `implementation_evidence` / `qa_evidence` until the typed
receipt is recorded; `v2_report` when a baseline-owned Pharos receipt must use the
owner-v2 artifact tuple). Baseline-owned deliveries require the typed
implementation evidence contract in [`docs/EXTERNAL_STAGE_CONTRACT.md`](EXTERNAL_STAGE_CONTRACT.md)
before handoff or owner completion; generic `external_ref` strings, QA
`test_result` digests, and OCI index/manifest refs are not the release-set
identity named by owner-v2 `release_manifest_digest`. Cancelling a batch does
not drop that binding on a still-live handoff. Conflicting or malformed typed
identity is `setup_required=built_artifact_identity` rather than an internal
error, so pause and cancel remain available. Credentials stay on the owner-only
secret-file mint CLI and never appear in the batch JSON. Pause and cancel write
real PAI-903 harness controls (or release a not-yet-claimed start intent through
the lifecycle revision CAS); an action with no owned effect is reported as
unavailable with its reason instead of being offered.

Project lifecycle is operational, not decorative: only `active` projects
accept new issues (including clones, batch creates, intake filing, and issue
moves into the project). `frozen` preserves reads and edits to existing work;
`archived` is the retired state; `deleted` is the existing trash state.

CLI equivalents:

```
paimos project list                         # active only
paimos project list --all                   # active + frozen + archived
paimos project list --status frozen
paimos project update PAI --status frozen
```

## Issues

```
GET    /projects/:id/issues         ?status= &priority= &type= &assignee_id=
POST   /projects/:id/issues         {title, type, status, priority, description, acceptance_criteria, notes, report_summary}
GET    /projects/:id/issues/tree    epic → ticket → task hierarchy
GET    /issues                      cross-project list; ?q= ranks best match, then recency
GET    /issues?keys=PAI-1,PAI-2     pick list, order preserved, missing → {ref, error} entries
POST   /issues                      orphan (no project) issue
POST   /projects/:key/issues/batch  admin — atomic create-many (parent_ref:"#N" cross-refs)
PATCH  /issues                      admin — atomic update-many [{ref, fields: {...}}, ...]
GET    /issues/recent               dashboard feed
GET    /issues/:id
PUT    /issues/:id                  partial update
DELETE /issues/:id                  admin only — moves to Trash (soft-delete; cascades to child tasks)
POST   /issues/:id/restore          admin only — clears deleted_at
DELETE /issues/:id/purge            admin only — hard delete (must be in Trash first)
GET    /issues/trash                admin only — list soft-deleted issues
PATCH  /issues/:id/archive          {archived: bool} — admin only
POST   /issues/:id/clone            {...field map}
POST   /issues/:id/complete-epic    bulk-transition children
GET    /issues/:id/aggregation      rollup stats
GET    /issues/:id/children
GET    /issues/:id/history          audit trail (who/when/diff)
GET    /issues/:id/comments
POST   /issues/:id/comments         {body, visibility?: 'internal'|'external'}   default internal (PAI-475)
PATCH  /comments/:id                {visibility: 'internal'|'external'}          author or admin
DELETE /comments/:id
GET    /issues/:id/anchors
```

## Issue relations

```
GET    /issues/:id/relations
POST   /issues/:id/relations        {target_id, type}   type: parent|groups|sprint|depends_on|impacts|...
DELETE /issues/:id/relations        {target_id, type} — admin only
GET    /issues/:id/members          list by relation (type=parent for an epic's tickets)
```

`type=parent` is the issue hierarchy (epic⊃ticket, ticket⊃task) — source=parent,
target=child, one parent per child. To put a ticket under an epic:
`POST /issues/{epic}/relations {target_id: ticket, type: parent}`. Legacy
`type=groups` with an epic source is auto-translated to `parent`. A second parent
for a child is rejected (409) — reparent via `parent_id` on issue update instead.
Every parent insertion is also checked at the database boundary: both rows must
belong to the same project, the edge must remain cycle-free, and the project's
`node_depth` must permit it. `node_depth` is exactly `1|nested`; existing
projects default to `nested`, while `1` forbids every parent edge.

## Paimos 6 nodes and product sessions

```
GET|POST /projects/:id/nodes
GET      /projects/:id/nodes/:nodeID
PUT      /projects/:id/nodes/:nodeID/parent
GET|PUT  /node-labels
GET|PUT  /projects/:id/node-labels
GET|POST /projects/:id/product-sessions
GET      /projects/:id/session-home/v1
GET      /projects/:id/session-home/zoom/v1
GET      /projects/:id/product-sessions/:productSessionID
GET      /projects/:id/product-sessions/:productSessionID/events
POST     /projects/:id/product-sessions/:productSessionID/attach-node
POST     /projects/:id/product-sessions/:productSessionID/detach-node
```

The node API is additive over existing `issues` rows. It always returns
`kind: "node"`; `cosmetic_type_label` is presentation text and never rewrites
`issues.type`. Labels use validated global defaults with optional per-project
overrides. The exposed precedence string is exactly
`project_override_then_global_default`; project PUT replaces the override set,
while global PUT is admin-only and requires every supported storage type. New
node clients do not choose epic/ticket/task. Parent writes use `parent_node_id`
and the same `issue_relations(type='parent')` SSOT as legacy issue APIs. Every
delete-before-insert reparent runs in one database transaction, including Jira
imports, so a rejected replacement preserves the old parent edge.

A product session is a separate project resource identified only by
`product_session_id`. It may target Paimos or a registered project agent, may
remain unattached, and has at most one nullable `node_id`; many sessions may
attach to the same node or select the same agent. Attach, reattach, and detach
require `expected_revision`, returning 409 on stale CAS. A product session is
not an attribution header session, harness session, agent run, delivery, issue,
or intake session, and creating one creates none of those resources. Create,
attach, reattach, and detach also append immutable actor, before/after node, and
before/after revision evidence in the same atomic mutation. A failed CAS
appends no event. An attached node must be explicitly detached before it can be
moved to another project or sent to trash.

The parallel session-home endpoint is a strict schema-v1, read-only projection
for one viewed project. Its fixed top-level shape is `schema_version: 1`,
`project_id`, `sessions`, and `totals`; every property is present, empty state
uses `sessions: []`, and rows sort by `updated_at DESC, product_session_id ASC`.
Each row retains its product-session identity and nullable node, so loose
sessions and multiple sessions attached to one node remain distinct.

Project-agent rows compose target-level unread inbox counts and held-message
exception attention with at most one fresh, ownership-correct active M161
harness generation. Management mode and advertised capabilities come only from
that generation. Ambiguous, stale, ownership-mismatched, or cross-project
candidates fail closed with no harness/address or direct controls; unmanaged
rows never advertise interrupt or stop. Direct steer is exposed only when the
generation advertises it, otherwise the closed control state is
`paimos_nudge`. A Paimos-targeted product session returns an explicit `paimos`
target with no invented project agent. The route reauthorizes the current
principal and project ownership inside one read snapshot, and every response
uses `Cache-Control: private, no-store`. The exact nested schema and closed
enums are defined by `GET /openapi.json`.

`totals.unread` de-duplicates shared target-agent inboxes when several product
sessions select the same registered agent; `totals.attention` counts session
rows whose exception-attention block is required.

The additive semantic-zoom endpoint accepts only the optional query keys
`zoom` and `selected_session_id`, each at most once. `zoom` defaults to `10`
and remains a canonical 1–64 digit decimal string on the wire. Its digit count
selects the `detail`, `overview`, `aggregate`, or `far` band; the returned
sample limit is capped at 100. Exact global totals are computed independently
of the deterministic exception-first sample. One newest session represents
each exceptional target before other rows, with UUID tie-breaking. A selected
same-project session is returned separately when it falls outside the sample;
missing, foreign, or inaccessible selections use the same 404 concealment as
an inaccessible project. Authorization, selection, totals, sampling, and one
captured harness-freshness instant share one read transaction, and all
responses are `Cache-Control: private, no-store`.

## Paimos 6 command palette

```
GET|PUT /command-palette/v1/settings
PUT     /command-palette/v1/instance-settings       admin only
GET     /projects/:id/command-palette/v1?q=...&limit=8
```

Every outcome is private with `Cache-Control: private, no-store`. Shortcut
precedence is nullable user override, nullable instance `app_settings`
override, then the safe `Mod+KeyK` default. PUT accepts exactly
`{"shortcut": string|null}` in at most 8 KiB; null resets that layer.
Canonical chord modifiers are ordered `Mod`, `Ctrl`, `Meta`, `Alt`, `Shift`,
followed by one allowlisted `KeyboardEvent.code`. `Mod` cannot be combined
with explicit `Ctrl` or `Meta`, and at least one non-Shift modifier is required.
The reserved browser/OS collision set and complete key-code allowlist are
defined in `GET /openapi.json`; unsafe chords are never stored.

Project search requires current view access and returns the closed
`schema_version`, trimmed `query`, `sessions`, `nodes`, and `knowledge` groups.
Each group is independently capped at 1–20 rows (default 8) and orders exact,
prefix, then substring matches before stable title/key and identity ordering.
The response omits project identity because it is already the authorized URL
scope. It neither searches nor returns node descriptions, knowledge bodies,
metadata, secrets, or hidden content. Knowledge `type` is the route-ready
kebab token; session, node, and knowledge stable identities are explicit.

## Time entries

```
GET    /issues/:id/time-entries
POST   /issues/:id/time-entries     {started_at, stopped_at?, override?, comment?, user_id?}
                                      — super-admin only: user_id = create on behalf of another user
GET    /time-entries/:id
PUT    /time-entries/:id            partial update — super-admin can edit any user's entry
DELETE /time-entries/:id            super-admin can delete any user's entry
GET    /time-entries/running        active timers for current user
GET    /time-entries/recent         recent entries for quick re-entry
GET    /time-entries/today-summary  ?from=<RFC3339>&to=<RFC3339> (both required)
                                      — sum of the current user's stopped entries in [from, to)
                                      → {total_hours, count} (PAI-495)
```

Cross-user writes are stamped in `mutation_log` and emit a
`super_admin_act` audit line (PAI-335).

## Attachments

```
GET    /issues/:id/attachments
POST   /issues/:id/attachments      multipart upload — links immediately
GET    /attachments/:id/meta        metadata only
GET    /attachments/:id             fetch file bytes
DELETE /attachments/:id             admin only
POST   /attachments                 multipart — upload pending (not yet linked)
PATCH  /attachments/link            {issue_id, attachment_ids} — batch link pending
```

## Sprints

```
GET    /sprints                     ?include_archived=true
GET    /sprints/years               distinct years
GET    /sprints/:year               sprints for one year
POST   /sprints/batch               {...template} — admin only
PUT    /sprints/:id                 partial — admin only
POST   /sprints/:id/move-incomplete admin only — bump unfinished to next sprint
PUT    /sprints/:id/reorder         {member_order}
```

## Users

```
GET    /users
POST   /users                       admin only — accepts must_change_password (default true)
PUT    /users/:id                   admin only
POST   /users/:id/disable           admin only
DELETE /users/:id                   admin only
POST   /users/:id/reset-totp        admin only
```

## User memberships (project access)

`reviewer` is an internal human role for read-only review of explicitly shared
projects (PAI-1054). Create it with `POST /users` using `role: "reviewer"`, then
grant `access_level: "viewer"` with the membership endpoint. It starts with no
projects; neither creating a user nor adding a future project seeds access.
Editor grants are rejected by both the API and the database. Converting an
existing user to reviewer requires revoking existing grants first. All other
roles retain their existing defaults. Real OIDC login matches the verified email
of this pre-provisioned user; no application admin role or machine login substitutes for this human browser login.

Reviewer reads are a closed allowlist in `backend/auth/reviewer.go`. They include
the normal internal landing workspace, scoped project/issue lists and details,
search, session-home, command palette, Agent Mode snapshots, and Flow state.
User lists contain only the reviewer, tags contain only tags used by shared
projects/issues, and saved views contain only their own views. Unowned issues,
global CRM/customer/offer data, instance memory and orchestrator configuration,
live event streams, API keys, security enrollment and unknown future endpoints
are unavailable. Instance-root identity/revision is withheld from reviewer
orchestration snapshots. The sole allowed application mutation is sign-out.
The UI marks the account read-only; existing public assets, login endpoints,
health/version and public capability-link routes retain their independent access
contracts and do not gain authority from reviewer login.

Administrators may separately provision a server-only Flow projection credential
with `POST /auth/flow-projection-credentials` and body `{name, reviewer_user_id,
project_id, expires_at}`. It requires an existing active reviewer with explicit
viewer membership on the active project and an RFC3339 expiry within 30 days.
The administrator must hold `flow:credentials:write` (or `*`); impersonation is
refused. The response contains `{id, project_id, expires_at, key}` exactly once
under `Cache-Control: private, no-store`. Capture it directly into protected
server storage, never logs, tickets or browser evidence. No retrieval endpoint
exists. `DELETE /auth/flow-projection-credentials/:id` revokes it permanently.

This separate machine credential permits only exact `GET
/api/projects/:id/baseline-batches/flow-state`, with no query parameters. It has
no general API, authentication, draft or execution authority. M199 stores its
hash outside `api_keys`, so older readers reject it. Every request and the
projection transaction recheck owner, exact project, membership, expiry and
revocation. The human test account still signs in through real OIDC; this
credential only lets the Janus server read its sandbox projection. The sandbox
must opt in via `POST /projects/:id/baseline-batches/opt-in {enabled:true}`.

M198 stores the role as an `is_reviewer` discriminator with both compatibility
role columns set to `external`. This avoids rebuilding the production users/FK
tables and makes an older binary fail closed to external portal access. Never
set the compatibility role to member: member intentionally has default editor
access across projects. No existing account is converted by the migration.

```
GET    /users/:id/memberships                     admin — effective per-project level for every project
PUT    /users/:id/memberships/:projectId          admin — upsert grant {level: "none"|"viewer"|"editor"}
DELETE /users/:id/memberships/:projectId          admin — revert to role default

GET    /users/:id/projects                        admin — legacy portal-grant list (kept for compat)
POST   /users/:id/projects         {project_id}   admin — legacy grant (viewer-equivalent)
DELETE /users/:id/projects/:projectId             admin — legacy revoke

GET    /users/me/recent-projects                  self
POST   /users/me/recent-projects   {project_id}   self — record a visit
```

## Permissions & access audit

```
GET    /permissions/matrix                        any logged-in user — capability × level matrix for UI
GET    /access-audit                              admin — grant/update/revoke trail
```

## Tags

```
GET    /tags
POST   /tags                        admin only — {name, color?, description?}
PUT    /tags/:id                    admin only
DELETE /tags/:id                    admin only
POST   /issues/:id/tags             {tag_id}
DELETE /issues/:id/tags/:tag_id
GET    /projects/:id/tags
POST   /projects/:id/tags           {tag_id}
DELETE /projects/:id/tags/:tag_id
GET    /system-tag-rules
PUT    /system-tag-rules            admin only
```

`color` is constrained to a fixed 12-value palette (paired
background+foreground rendered by the SPA). Hex codes and arbitrary
CSS color names are rejected with `400 invalid color`. The canonical
list, in display order, is:

```
gray, slate, blue, indigo, purple, pink,
red, orange, yellow, green, teal, cyan
```

The same list is discoverable at `GET /api/schema` →
`enums.tag_colors` since schema version `1.2.2` — clients should
prefer the schema over hard-coding the values.

## Views

```
GET    /views
POST   /views                       {name, filters, columns}
PUT    /views/:id                   partial
DELETE /views/:id
PATCH  /views/order                 admin only
POST   /views/:id/pin
DELETE /views/:id/pin
```

## Search

```
GET    /search?q=<term>             min 2 chars; also matches issue keys (prefix)
                                      optional: project=<key-or-id>, type=<issue-type>, limit=N, offset=N
                                      legacy project scope also works: scope=project&project_id=<id>
```

## Agent Context

`/projects/:id/repos`, `/projects/:id/anchors`,
`/projects/:id/graph`, `/projects/:id/retrieve`, and `/issues/:id/anchors`
form the project-context layer for agents.

## Agents & inventories (PAI-326 / PAI-329 / PAI-331)

Each project declares the agents that work it plus shared inventories
(environments, deploy recipes) those agents inherit. Reads are
project-view-gated; writes are admin-only.

```
GET    /projects/:id/agents
POST   /projects/:id/agents            { name, description?, slash_command_name?, lane_tags?, metadata?, body?, bootstrap_steps?, non_negotiable_rules? }
PUT    /projects/:id/agents/:name      partial update
DELETE /projects/:id/agents/:name
GET    /projects/:id/agents/:name.json canonical agent artifact (inlines repos + environments + deploy_recipes)
GET    /projects/:id/agents/:name.md   markdown rendering for CLI / skill render
GET    /projects/:id/agents/:name.rev  plain-text rev hash for cheap-poll fallback
GET    /projects/:id/agents/events     SSE stream — auto-watch sync (PAI-331)
POST   /issues/:id/implement           create a queued local/provider run
GET    /issues/:id/runs                issue run history
GET    /projects/:id/runs              project run history
GET    /projects/:id/runners           live runner capabilities
GET    /runs/:id                       run detail
PATCH  /runs/:id                       lifecycle/report compare-and-set
```

Durable A2A messages use registered names rather than numeric agent IDs:

```
POST /projects/:id/messages            frozen v1 fire-and-forget compatibility send
POST /v2/projects/:id/messages         { to, body, issue_id?, reply_to?, thread_id?, metadata?, is_action_request?, expects_reply?, delivery_level?: "simple"|"steer" }
GET  /projects/:id/messages            frozen v1 compatibility list
GET  /v2/projects/:id/messages         ?to=<address>&thread=<id>&after=<cursor>&limit=<n>
GET  /projects/:id/messages/listen     frozen v1 compatibility inbox
GET  /v2/projects/:id/messages/listen  ?to=<address>&after=<cursor>&limit=<n>
POST /projects/:id/messages/ack        { to, cursor }
POST /projects/:id/messages/:messageId/resolution { outcome: "resolved"|"dismissed" } plus one Idempotency-Key (human session only; API keys forbidden)
GET  /projects/:id/attention/listen    ?to=<address>&after=<cursor>&limit=<n>&delivery=<local-adapter> (super-admin orchestrator portfolio)
POST /projects/:id/attention/ack       { to, cursor, batch_id? } (super-admin orchestrator portfolio)
POST /projects/:id/messages/delivery-complete { to, cursor, delivery_id, effective_level, fallback_reason }
POST /projects/:id/messages/delivery-unavailable { to, cursor, delivery_id, fallback_reason }
POST /projects/:id/message-allowlist   { receiver, sender }
POST /projects/:id/message-targets     { address, adapter, target_kind, target_ref, target_secret?, maximum_level?, role? } (admin; configured orchestrator attention target requires super-admin; target_secret is the write-only routine sender key: required by grok_bot_routine, refused by adapters without a secret header)
GET  /projects/:id/message-targets     ?address=<receiver> (admin; configured orchestrator attention target requires super-admin and is omitted from ordinary-admin list-all; never returns target_ref or target_secret; has_secret only)
POST /projects/:id/message-targets/requeue { address } (admin; configured orchestrator attention target requires super-admin; recovers target_missing message rows and blocked/stale/expired-lease attention batches without changing batch correlation or a live lease)
GET  /projects/:id/message-deliveries  redacted outbox state (admin)
POST /projects/:id/message-deliveries/:deliveryId/requeue (admin)
GET  /projects/:id/message-deliveries/:deliveryId/closed-target-recovery ?expected_closed_session_id=<stopped-public-session>&replacement_session_id=<fresh-public-session> (admin dry-run; project-edit authority rechecked in the read transaction)
POST /projects/:id/message-deliveries/:deliveryId/closed-target-recovery { expected_closed_session_id, expected_target_id, expected_target_version, expected_consumer_fence: 0, replacement_session_id } (admin; exact audited compare-and-swap)
GET  /projects/:id/messages/:messageId frozen v1 compatibility record
GET  /v2/projects/:id/messages/:messageId
GET  /issues/:id/messages              human-visible issue-anchored records (not comments)
GET  /v2/issues/:id/messages           reply-aware human-visible issue records
```

The POST sender is derived from `X-Paimos-Agent-Name`; unknown senders or
addressees fail closed with stable `agent_message_*` problem codes. Addressee
reads return delivered, non-action messages only, capped at 10 per cursor page
and wrapped with the untrusted-message preamble. The unversioned message
send/list/get/listen routes and `backend/contracts/agent-message-v1.schema.json`
are frozen fire-and-forget v1 compatibility surfaces: v1 rejects
`expects_reply` and omits v2-only fields. New clients use `/v2/...` and the
closed `backend/contracts/agent-message-v2.schema.json` contract.
Explicit `is_action_request=true` rows are stored as held for human inspection
and never enter listen; conservative prose detection is a fallback, not a
replacement for the typed marker.
Explicit `expects_reply=true` is separate from action gating and leaves the
ordinary send default unchanged. It creates one durable obligation in the
same transaction as the message. Only a newly committed, accepted counterpart
reply whose exact `reply_to` names that message closes it; held replies, inbox
acknowledgement, delivery handoff, and delivery acknowledgement do not. Due
obligations become eligible for the configured orchestrator's bounded attention
feed at most six times (after 5 minutes, 15 minutes, 1 hour, 4 hours, 12 hours,
and 24 hours). An authorized attention/listen projection poll, not an autonomous
wall-clock scheduler, advances an eligible generation. The obligation then
remains open but quiet and stops being actionable immediately after closure.
The resolution endpoint records an immutable `resolved` or `dismissed` audit
fact for an already-held action request. It requires a human session with
project-edit authority, refuses API-key principals and agent attribution,
stores value-free user/session attribution and
digested idempotency material, and never releases or mutates the held row.
Exact retries return the first record; a changed outcome returns HTTP 409.
The issue detail is the session-authenticated producer and labels the two
decisions explicitly while retaining the held/not-delivered label and a
persistent no-execution/no-delivery disclaimer. `GET /v2/issues/:id/messages`
returns only the content-free `human_resolution_outcome`; the frozen v1 issue
route omits it, and neither projection exposes audit actor/session fields.
Listen and ack additionally require `X-Paimos-Agent-Name` to match the named
addressee. Listen resumes from the greater of the supplied and durable cursor;
acknowledgement is monotonic and rejects cursors that are not delivered rows in
that inbox.

`Idempotency-Key` is optional on message POSTs and accepts one opaque 1–128 byte
value. It is scoped to the current instance, project, and attributed sender;
an exact replay returns the original canonical message, while changed request
content returns HTTP 409 `agent_message_idempotency_conflict`. The raw key is
not stored. Attributed Codex workers request `delivery=codex` on listen; only
that response includes the decrypted snapshotted Codex target and leases the
row. Successful vendor handoff uses `delivery-complete`, which records the
effective level/fallback and advances the receiver cursor atomically.
An unavailable leased `agentd_codex` or `agentd_claude` target uses
`delivery-unavailable`; the
server releases that lease to the currently working steerable M161 generation
whose heartbeat is at most 90 seconds old, or its snapshotted ordinary simple
fallback. A missing route becomes `blocked/target_missing`; the replacement
worker must lease the same durable row and still finish through
`delivery-complete`.

A pending managed delivery whose exact target generation is publicly stopped
may be rebound only through `closed-target-recovery`. The dry-run returns the
original, current effective, and proposed replacement target/session bindings
plus the exact zero-attempt consumer fence. Apply accepts only those reviewed
compare-and-swap fields. It refuses held or action-request rows, any lease,
attempt, consumer claim, handoff, effective level, fallback transition,
capability change, stale/unowned replacement, or cross-project/address binding.
Successful recovery appends one immutable bounded audit record and leaves the
canonical message, frozen v1 envelope, original delivery target columns, and
human `product_session_id` unchanged. V2 envelopes may add
`delivery_effective_target` so clients can distinguish that audited effective
binding from the original snapshot.

PAI-800 runner liveness/progress uses the PAI-799 integration seam directly:
`POST /runs/:id/telemetry`. The supervisor owns stable correlation plus
monotonic sequence and estimate revision, and sends only the documented
heartbeat, phase, activity, needs-input, blocker, progress, and ETA fields.
Provider prompts, source, tool arguments, command output, environment values,
raw errors, and arbitrary provider payloads never cross this seam. Lifecycle
remains on `PATCH /runs/:id`; a successful code run without configured test
execution reports the truthful terminal status `completed`.

Project inventories — small CRUD trios shared by every agent in the project:

```
GET    /projects/:id/environments
POST   /projects/:id/environments      { name, url?, host_alias?, host_ip?, sort_order? }
PUT    /projects/:id/environments/:envId
DELETE /projects/:id/environments/:envId

GET    /projects/:id/deploy-recipes
POST   /projects/:id/deploy-recipes    { name, command?, summary?, sort_order? }
PUT    /projects/:id/deploy-recipes/:recipeId
DELETE /projects/:id/deploy-recipes/:recipeId
```

`/projects/:id/repos` (existing) is the third inventory; all three are
inlined into the canonical agent artifact at render time.

## External delivery stages (PAI-810)

JSON uses `application/vnd.paimos.external-stage.v1+json`. Mint/rotate return
exactly 32 raw bytes as `application/vnd.paimos.external-stage-secret.v1`.
The separate authenticated setup routes use standard `application/json`; their
POSTs require `Idempotency-Key`, current editor authority, and mandatory audit.

```
GET  /agent-mode/deliveries/:deliveryKey/external-reporter-registrations
POST /agent-mode/deliveries/:deliveryKey/external-reporter-registrations
POST /agent-mode/deliveries/:deliveryKey/external-reporter-registrations/:registrationID/revoke
POST /agent-mode/deliveries/:deliveryKey/external-owner-activations
POST /agent-mode/deliveries/:deliveryKey/external-prerequisite-sets

POST /agent-mode/deliveries/:deliveryKey/external-stage-handoffs  internal create; metadata only
POST /agent-mode/external-stage-handoffs/:handoffID/mint          internal one-time raw credential
POST /agent-mode/external-stage-handoffs/:handoffID/rotate        internal rotate + epoch advance
POST /agent-mode/external-stage-handoffs/:handoffID/revoke        internal terminal revoke

GET  /external-stage/handoffs/:handoffID                          external pull
POST /external-stage/handoffs/:handoffID/accept                   external sequence-one accept
POST /external-stage/handoffs/:handoffID/reports                  external exact-next report
POST /external-stage/handoffs/:handoffID/launch-candidates        additive one-shot candidate admission
POST /external-stage/handoffs/:handoffID/launch-admissions/:admissionID/consume
```

The two launch sidecar routes use
`application/vnd.paimos.external-stage-launch-admission.v1+json` and retain the
same registered API key plus independent handoff-secret boundary. They never
run a host command, contact a provider, expose a secret, or make deployment a
completed delivery fact. Candidate admission is possible only for an accepted,
current deployment handoff whose exact Pharos registration includes the still
live delegated `target_ref`; an older registration with no target remains valid
for attended v1/v2 reporting but cannot obtain delegated admission. See the
contract document for the closed bodies and historical replay semantics.

Prerequisite-set requests always carry an explicit array of 0–16
`{dependency_key, reporter_registration_id, requirement}` items, where
`requirement` is exactly `required` or `optional`. An empty array intentionally
seals no dependencies; only required rows gate owner success.

The external routes require the registered Bearer API key and an independent
`X-PAIMOS-Handoff-Secret` request header. The header is inbound-only; no raw or
encoded handoff credential is accepted in path/query/JSON/cookie or returned
later. Missing, unauthorized, invalid, expired, revoked, rotated, and
stale-authority handoffs share canonical 404 concealment. See
[`EXTERNAL_STAGE_CONTRACT.md`](EXTERNAL_STAGE_CONTRACT.md) for the exact DTO,
current-registration discovery, CLI, fixture, digest, and release-pin contract.

## Knowledge

Knowledge entries (memory, runbook, external_system, related_project,
guideline) live as issues with a discriminator on `issues.type` and
are addressed through one unified surface (PAI-394):

```
GET    /projects/:id/knowledge                          list all types
GET    /projects/:id/knowledge?type=<seg>               filtered list
GET    /projects/:id/knowledge/<type>/<slug>            single entry
GET    /projects/:id/knowledge/<type>/<slug>.rev        cheap-poll rev hash
POST   /projects/:id/knowledge                          { type, slug, title, body?, status?, metadata? }
PUT    /projects/:id/knowledge/<type>/<slug>            full replacement (PATCH → 405)
DELETE /projects/:id/knowledge/<type>/<slug>            soft-delete
```

`<type>` (and the `?type=` value) is the kebab-singular URL segment:
`memory`, `runbook`, `guideline`, `external-system`, `related-project`.
Request bodies accept either the URL segment or the SQL discriminator
(`external_system`) in their `type` field.

Memory-specific subroutes — slugs `references`, `stale`, `proposed`
are reserved server-side so they can't shadow a real entry:

```
POST /projects/:id/knowledge/memory/references          { ids: [...] }    bump decay counter
GET  /projects/:id/knowledge/memory/stale[?days=N]      decay candidates
GET  /projects/:id/knowledge/memory/proposed/stale[?days=N]   aged drafts
```

Cross-scope memory (out-of-project ownership) — user-scoped and
instance-scoped memory live alongside project knowledge but on their
own resources:

```
GET    /users/me/memory                   list this user's memory entries
POST   /users/me/memory                   { slug, title, body?, status?, metadata? }
GET    /users/me/memory/:slug
PUT    /users/me/memory/:slug             full replacement
DELETE /users/me/memory/:slug

GET    /instance/memory                   instance-wide memory (read = any user)
GET    /instance/memory/:slug
POST   /memory/:slug/promote              { from: 'project'|'user', to: 'user'|'instance', source_project_id? }
```

Issue-level surfaces that lean on the knowledge plane:

```
GET    /issues/:id/applicable-memories    PAI-342 — memories that match this issue's surface
GET    /issues/:id/lesson-capture-prompt  PAI-343 — prefilled prompt for closing-as-lesson UX
```

The discoverable schema at `GET /api/schema` exposes the registered
type set under `enums.knowledge_types` and the full surface under
the top-level `knowledge` block.

- `repos` declares the mirrored/source repositories a project uses.
- `anchors` ingests machine-generated issue-to-file locations per repo.
- anchors may include derived `symbol` metadata for the nearest enclosing
  function / method / class / type when the repo-side scanner can parse it.
- `graph` exposes typed entity relations (issues, repos, anchors, project).
- `graph/blast-radius` answers "what else is affected if this changes?" in a grouped-by-type shape.
- `retrieve` returns mixed context hits from issue text, anchors, knowledge
  entries, canonical agent/project inventories, derived symbols, and
  graph-neighbor expansion. It uses project-scoped lexical search plus
  deterministic local vector scoring and reciprocal-rank fusion across issue
  and context documents. Response shape includes `hits`, `strategy`, and
  `meta`.

There is no project manifest blob after PAI-358. Agents should compose
context from `repos`, `knowledge`, `anchors`, `graph`, `retrieve`, and
`agents/{name}.json`.

## Auto-watch sync (PAI-331)

Per-(user, device, project) opt-in for the agent-events SSE stream.
Default OFF — a fresh `(device, project)` tuple does not receive
pushes. Toggling OFF invalidates the device's active SSE connection
server-side.

```
GET    /auth/auto-watch                                this user's subscriptions
PUT    /auth/auto-watch/:deviceID/:projectID           { enabled: bool }
DELETE /auth/auto-watch/:deviceID/:projectID           explicit unsubscribe
```

PAI-341 (knowledge-plane sync) reuses the same `(user, device, project)`
table verbatim; one subscription covers all kinds for that triple.

## Adapter registry (PAI-332)

```
GET    /registry/adapters                              all adapters paimos can hand off to
```

Returns the merged in-tree + `$PAIMOS_ADAPTER_PATH`-discovered adapter
list, with `name`, `source` (`builtin` or `PAIMOS_ADAPTER_PATH`),
`harness`, and the rendering capabilities each one exposes.
Env-discovered adapters override in-tree adapters with the same name.

## Schema (self-describing discovery)

```
GET    /schema                      public — enums, transitions, entity shapes
```

Returns `{version, enums, transitions, entities, conventions}`. No auth
required. Cacheable: strong ETag + `Cache-Control: public, max-age=300`.
Version bumps whenever any enum, transition, field, or convention changes.
The CLI and MCP use this endpoint to validate user input before POSTing
so agents catch typos (e.g. `status: "completed"`) client-side.

## Session audit (default on)

```
GET    /sessions/{id}/activity      admin — mutations tagged with X-PAIMOS-Session-Id
                                   ?cursor=<id>&limit=100 (keyset pagination)
```

Enabled by default. Set `PAIMOS_AUDIT_SESSIONS=false` (or `0`) on the
backend to opt out. Every mutation request (POST/PUT/PATCH/DELETE) is
recorded with the caller's `X-PAIMOS-Session-Id` header when present.
Missing header → row with `session_id = null` (non-fatal). The `paimos`
CLI auto-generates a UUIDv7 per invocation; `PAIMOS_SESSION_ID` env var
overrides so multi-step scripts can share a session.

## Reports / audit

```
GET    /projects/:id/acceptance-log              timeline of accept/reject decisions
GET    /projects/:id/acceptance-report           full report
GET    /projects/:id/reports/lieferbericht       JSON delivery report
GET    /projects/:id/reports/lieferbericht/pdf   PDF delivery report  ?text_source=tech|report (PAI-418)
GET    /projects/:id/reports/projektbericht/pdf  alias of lieferbericht/pdf
GET    /projektberichte/:code/pdf                snapshot-by-code PDF (portal default text_source=report)
GET    /reports/accruals                         admin only — per-user time rollup
```

## Agent run telemetry

```
POST   /runs/:id/telemetry          append one allowlisted fact; requester/claimer/admin
GET    /runs/:id/telemetry          append-only history; ?after_sequence=0&limit=100
GET    /runs/:id/telemetry/latest   indexed event/heartbeat/semantic/estimate snapshot
```

Sequence is strictly increasing per run. Exact duplicate replay is idempotent;
conflicting duplicate/out-of-order and post-terminal reports return 409.
Event freshness and supervisor-heartbeat liveness are separate and both use
server receipt time, never the agent clock. Heartbeat-only reports preserve the
latest semantic activity/blocker and estimate facts. Percentage and ETA
are optional evidence-backed declarations and are never derived from elapsed
wall time. Unknown JSON fields are rejected; raw prompts, tool arguments,
command output, environment values, secrets, source contents, and provider
payloads are outside this contract. Obvious secret-bearing values in the two
allowlisted one-line text fields are rejected.

Runs created after schema M144 carry `delivery_instrumentation_version: 1` and
are atomically linked to the issue's internal delivery audit model. Existing
version-0 run responses remain compatible.

## Agent Mode delivery snapshots and events

The worker fleet projection is a separate, worker-rooted Agent Mode read model:

```text
GET /agent-mode/worker-fleet/v1?zoom=10
GET /agent-mode/projects/:projectID/worker-fleet/v1?zoom=10
GET /agent-mode/worker-fleet/v2?zoom=10
GET /agent-mode/projects/:projectID/worker-fleet/v2?zoom=10
```

All four routes use the same projection logic and the Agent Mode authorization
boundary. `zoom` is a canonical positive decimal string; detail, overview,
aggregate, and far bands return at most 100 workers. Responses always report
the exact retained worker/project totals visible in scope, sampled and omitted
counts, and `sample_truncated`. The retained fleet includes every non-terminal
session plus the newest terminal generation per project agent; the explicit
`terminal_generations_per_agent` provenance value pins that history bound. A
parent ID may therefore be present while
`parent_in_sample=false`.

V1 is the frozen PAI-904 wire contract: it reports the current harness-session
hierarchy, ticket binding, revision, effective controls, and liveness. V2 keeps
that contract on separate routes and adds the
closed `work_shape` (`ship`, `scout`, or the projected legacy `unknown`) and its
bounded value-free `work_contract`. Scout means investigation evidence, not a
product delivery. The contract exposes stage applicability, definition-of-done
codes, and non-goals; it neither carries ticket prose nor claims Git
enforcement. Missing historical shape storage remains `unknown` until an
authorized explicit revision-CAS reassignment classifies it, and scout never
silently promotes to ship. Immutable supervision events record semantic
transitions, not every replay-equivalent heartbeat or yield request.
V2 retains `management_mode` and adds closed `runtime_provenance_trust`.
`managed_reporter` means a managed row has complete workspace evidence plus a
valid lease-authenticated reporter heartbeat. Only those rows may expose
`machine_id`, privacy-safe workspace `kind`/`mode`, the exact bounded dispatch
snapshot, and the closed non-secret `account_label`. Trusted rows may also
include an optional opaque `account_key` for named Codex selection; it is
omitted when empty and is never a path, env, executable, or credential.
The dispatch snapshot retains its eight fields; registry `family` and `tier`
metadata is not added to fleet v2. Model IDs preserve Pi's `provider/id`
syntax, accepted by both OpenAPI and the standalone orchestration schema;
historical profile pins and model values remain unchanged.
Unmanaged, pre-heartbeat,
and legacy-unverified rows are `untrusted`: machine/workspace/dispatch are null
and account is unknown even if their registration supplied values; those axes
are deliberately suppressed. This is reporting-channel trust, not
process-binary or machine attestation. Raw paths,
workspace identities, Git branches, message targets, credentials, and quota
never enter either fleet contract.
The focused Agent Mode delivery view consumes the project route only after an
operator selects **Show worker context**. It consumes v2, filters the
authorized bounded sample to the selected ticket, and renders machine,
workspace kind/mode,
dispatch model/effort, account label, management/trust, shape, and output kind. A failed or
revoked lookup retains no earlier worker data; a truncated sample is labelled
as such rather than completed with inferred workers.
Missing, malformed, stale, unmanaged, or otherwise untrusted reporter evidence
is `unknown`, never idle. `dead` requires a terminal projection confirmed by
the agentd reporter or control plane; `liveness.source` distinguishes them.
Fresh managed workers may steer, while interrupt and stop remain available for
a non-terminal stale worker so operators can recover it. Recent communication
is capped at four entries per worker and is
limited to IDs, levels, timestamps, delivery state, and fallback/error codes;
each item says `attribution=project_agent` because messages do not identify a
specific harness generation. These content-free summaries are intentionally
visible to every internal project viewer; message bodies, target references,
and privileged delivery internals never enter this schema. Progress and
ETA remain null unless the existing delivery-trust projection marks each field
trusted. Provenance currently says `authoritative_database`, `cache=none`, and
`remote_cache=false`; a future remote cache must change those fields honestly.

```
GET    /agent-mode/deliveries                         internal snapshot
GET    /agent-mode/projects/:projectID/deliveries     project-audience snapshot
GET    /agent-mode/deliveries/:deliveryKey            detail/selection snapshot
GET    /agent-mode/deliveries/events                  SSE invalidation stream
```

Snapshots are schema-v1 `application/json` with `Cache-Control: private,
no-store`. The fixed top-level shape is `schema_version`, `server_time`, opaque
211-character `cursor`, `rows`, `selected_delivery` (empty string only for
genuinely empty authorized history), optional `selected_outside: {reason,row}`,
and `aggregates`. The sole outside-selection reasons are `filter_excluded`,
`active_fallback`, `terminal_fallback`, and `terminal`. A request may use
allowlisted project/state/attention/health/lane/query filters and a selection;
the project query is a result filter, while the project path is an authorization
audience. More than 1,000 authorized candidate roots is an explicit private
`400`, never a truncated snapshot; an exact detail lookup scopes before that
portfolio ceiling.

The events response is `text/event-stream` with `Cache-Control: private,
no-store, no-transform`. Resume uses `Last-Event-ID` when the header is present,
otherwise `cursor=`. `refetch` carries only currently authorized bounded hints;
`checkpoint` advances across hidden facts without hints. Their SSE `id` is the
replacement cursor. `reset` has no event id and only
`{"schema_version":1,"reason":"resync_required"}`, then closes. Invalid,
expired, revoked, wrong-scope, below-retention, or ahead-of-tail resumes all use
that same reset. A storage invariant found before stream headers is a private
problem+json `500`; after the stream is established it becomes the generic
reset-and-close recovery.

Agent Mode is unavailable to external-role users even if they have an explicit
project grant. Missing and inaccessible project/detail/selection resources are
the same canonical private `404`. The complete schema, enums, filters, response
headers, and SSE envelopes are defined by `GET /openapi.json`.

## Project metadata

```
GET    /projects/suggest-key                     key suggester
GET    /projects/:id/cost-units
GET    /projects/:id/releases
GET    /cost-units                               cross-project distinct values
GET    /releases                                 cross-project distinct values
GET    /projects/:id/export/csv                  admin only
POST   /projects/:id/import/csv/preflight        admin only
POST   /projects/:id/import/csv                  admin only
POST   /import/csv/preflight                     admin only — global
POST   /import/csv                               admin only — global
```

---

## Enums

| Field | Values |
|-------|--------|
| `type` | `epic` `cost_unit` `release` `sprint` `ticket` `task` |
| `status` | `new` `backlog` `in-progress` `qa` `done` `delivered` `accepted` `invoiced` `cancelled` |
| `priority` | `low` `medium` `high` |
| issue-relation `type` | `parent` `groups` `sprint` `depends_on` `impacts` `follows_from` `blocks` `related` |

Hierarchy is the `parent` relation edge — the single source of truth
(source=parent, target=child, at most one parent per child). Set it via
`parent_id` on legacy issue create/update, `parent_node_id` on the node API, or
a `type=parent` relation. Payload parent IDs are projected from the edge;
there is no second hierarchy column. `groups` is
cost_unit/release container membership (M:N, orthogonal axis). Orphan
tickets/tasks allowed. 422 on invalid parent.  
Issue key: `{PROJECT_KEY}-{n}` e.g. `PAI-1` — computed, not stored.  
Project numeric IDs are assigned per-deployment in creation order — always `GET /projects` and match on `key` or `name` before POSTing. Do not hard-code project IDs from examples.

**Issue `{id}` accepts keys too.** Every `/issues/{id}/*` route resolves either
a numeric id (`462`) or an issue key (`PAI-83`, `PMO26-639`). Keys match
case-sensitively against `project.key` + `issue_number`. Malformed
references return 400; key-shaped refs with no matching row return 404.
Soft-deleted issues still resolve so `POST /issues/:id/restore` and
`DELETE /issues/:id/purge` work with keys.

The CLI adds a safety boundary on top of this REST contract: pass issue keys by
default, or `id:462` when intentionally addressing the internal ID. It rejects
bare `462` because that is indistinguishable from a pasted issue-key suffix.
Commands whose argument is explicitly another resource ID, plus
`paimos issue restore <id>`, keep ordinary bare numeric syntax.

## Harness sessions

- `GET|POST /projects/{id}/harness-sessions` — list/register durable,
  non-secret managed or unmanaged harness identities. Registration includes a
  distinct private worker lease for that generation; the server stores only
  its domain-separated digest. Inbox-capable registration creates or reuses
  the encrypted target first, then commits the active session with both digest
  and target FK; an interrupted pre-insert attempt leaves only a reusable
  target, not worker authority. Inbox-capable registration for the configured
  orchestrator attention identity requires super-admin authority because it
  can create or rotate that cross-project receiver target.
- `GET /projects/{id}/harness-sessions/{sessionID}` — status with host and
  public session attribution; the private reference and worker lease are never
  shown. Nullable `parent_harness_session_id` and `ticket_id` are authoritative
  durable bindings, separate from product sessions and mutation-attribution
  session headers.
- `GET /projects/{id}/harness-sessions/orchestrator` — resolves only one active
  coordinator with fresh, known managed activity. Zero candidates is `unset`,
  multiple candidates is `ambiguous`, and unmanaged or stale evidence remains
  `unset`; missing evidence is never interpreted as idle.
- `PATCH /projects/{id}/harness-sessions/{sessionID}/binding` — explicit
  full-state parent/ticket/work-shape assignment with required expected
  revision. It
  rejects stale generations, cross-project or terminal parents, cycles,
  over-depth moved subtrees, and invalid tickets before mutation, then appends
  one immutable event with the before/after bindings. Scout-to-ship promotion
  is possible only through this authorized CAS write; it is never inferred
  from activity, delivery, or Git state.
- `POST .../{sessionID}/heartbeat` · `POST .../{sessionID}/yield` — attributed
  status/yield and typed owned-control claim.
- `POST .../{sessionID}/drain` · `POST .../{sessionID}/complete-delivery` —
  canonical full-FIFO message-bus lease/completion for both simple and steer,
  preserving delivery fields. The legacy steer-named pair is a compatibility
  alias for steer-capable workers and also drains older simple work first.
  Although these harness-session paths are unversioned, their drain response
  was introduced after the frozen message v1 API and intentionally carries the
  current closed v2 envelope; it is not a compatibility projection of
  `/projects/{id}/messages`.
- `POST .../{sessionID}/controls/{interrupt|stop}` ·
  `POST .../{sessionID}/controls/{controlID}/complete` — typed owned-process
  requests; no free-form command or PAI-809 action extension.
- `GET .../{sessionID}/controls/{controlID}` — ProjectView operator read bound
  to the exact project, public session, and control UUID. It returns `id`,
  `project_id`, `harness_session_id`, `correlation_id`, `sequence`, `kind`,
  `state`, optional terminal `outcome` and `reason`, and
  `requested_at`/`claimed_at`/`completed_at`; no worker lease, session
  reference, target reference, body, or requester.
- `POST /projects/{id}/harness-sessions/{sessionID}/messages/v1` — a
  non-impersonated human browser session sends `simple` or `steer` to the exact
  selected managed generation with `expected_revision` and an
  `utt_<32-lowercase-hex>` replay identity. The write transaction reauthorizes
  both the human editor and the current runtime reporter, then checks the
  generation mapping, machine/account provenance, freshness, advertised
  capability, non-stopping phase, and enabled primary target. It creates a durable product
  conversation on first send and records the canonical message as `human`
  from `user:<id>`; it never supplies agent attribution or a fallback target.
  Exact replay returns the original receipt even after that generation stops,
  while a changed tuple or stale selected revision conflicts. Internal
  newlines are byte-preserved; NUL, controls, oversized text, and
  credential-like content fail without body diagnostics.
- `POST .../{sessionID}/stop` — attributed terminal lifecycle transition after
  worker cleanup.

Every worker-side mutation must carry ordinary API authentication, the exact
project path and public harness-session UUID, matching agent attribution, and
exactly one `X-Paimos-Harness-Worker-Lease` proof. A public UUID, a caller-set
agent header, or a ProjectEdit API key is insufficient on its own. Missing,
duplicate, wrong-generation, and cross-project proofs return the same
uniform non-enumerating `403` without mutation. The CLI reads the proof from
`--worker-lease-file`, never argv or a URL, and rejects redirects rather than
forwarding it.

Capability fields are advertised and server-capped. Unmanaged steer is valid
only for a bound Codex target whose plugin and durable target both cap at
`steer`; unmanaged interrupt/stop and OpenClaw/private-socket transports are
rejected.

## Instance orchestrator pin

All orchestrator responses, including early authentication and authorization
failures, carry `Cache-Control: private, no-store`.

- `GET /orchestrator/v1` is available to authenticated internal users and
  returns only `schema_version`, `revision`, nullable
  `orchestrator: {display_label}`, and nullable `updated_at`. It never exposes
  or infers a project, agent ID, or canonical key.
- `GET|PUT /orchestrator/v1/config` is super-admin only. GET includes the full
  stable target (`project_id`, `project_key`, `project_agent_id`, canonical
  `key`, and separate `display_label`). PUT requires `expected_revision` and
  either that exact target input (`project_id`, `key`, `display_label`) or
  `orchestrator: null` to clear. JSON is strict and capped at 16 KiB.
- `GET /orchestrator/v1/events?after_revision=0&limit=50` is the super-admin
  append-only audit feed. Limits are 1–100 and events are ascending by
  consecutive revision.

Revision 0 is pristine unset and has `updated_at: null`. After any mutation,
including clear, revision is positive and `updated_at` is a canonical UTC
timestamp even when `orchestrator` is null. There is no only-agent fallback or
cross-instance inheritance. M165 creates only the unset row; configuring an
instance is a separate authorized operation.

Use the first-class CLI operation shown by the Paimos 6 super-admin empty
state. It contains no credential and names the configured CLI instance,
project, canonical agent, and display label explicitly:

```sh
paimos --instance 'my-local-ppm' orchestrator set --expect-deployment-instance 'ppm' --project 'PAI' --agent 'amy' --display-label 'Amy'
```

`--instance` is the operator's local CLI configuration alias; the browser
cannot discover it. `--expect-deployment-instance` is the server identity
shown by the browser. Before any compare-and-swap write, the CLI reads
`/health` and requires identity enforcement plus matching deployment and agent
bus identities. It then resolves the project and canonical agent, reads the
current revision, and performs one compare-and-swap write. A redirect,
authorization failure, identity mismatch, missing/non-canonical target,
ambiguous project, or concurrent revision change fails closed; rerun the same
command to read fresh state. The equivalent API payload after resolving the
real project ID and reading revision 0 is:

```json
{"expected_revision":0,"orchestrator":{"project_id":42,"key":"amy","display_label":"Amy"}}
```

Neither viewing nor copying the command performs that operation. Credentials
remain in the CLI's configured keyring entry, and no other instance is
implicitly configured.

The Paimos 6 home keeps every empty or unavailable state actionable without
inventing a browser-side mutation:

| Observed state | Truthful next action |
| --- | --- |
| Orchestrator configured, no product sessions | Open the existing talk panel; nothing is sent until the operator submits there. |
| Orchestrator unset, canonical agents available | Choose an explicit agent and copy the visible `paimos orchestrator set` command. |
| Orchestrator unset, no canonical agents | Open the existing Project → Agents editor in a new tab, treat the current catalog as stale, then refresh the choices. |
| Binding or setup projection unavailable | Retry only that read; no orchestrator or command is inferred. |
| Ordinary admin without setup authority | Copy a bounded, secret-free request for a super admin. |
| Session-home projection unavailable | Retry the authorized session-home read after all prior rows have been cleared. |

Opening the agent editor preserves the selected project and binding context in
the original tab. The browser never executes the copied command, reads the
operator's keyring, or receives a secret. Agent creation and registration stay
on the existing Project → Agents surface; there is no hidden setup endpoint.

---

## Create backlog item

```bash
# Resolve project id first — never hard-code from examples.
PID=$(curl -s -H "Authorization: Bearer $KEY" \
  https://paimos.example.com/api/projects \
  | jq '.[] | select(.key=="PAI") | .id')

curl -s -H "Authorization: Bearer $KEY" \
  -X POST "https://paimos.example.com/api/projects/$PID/issues" \
  -H "Content-Type: application/json" \
  -d '{"title":"...","type":"ticket","status":"backlog","priority":"medium",
       "description":"...","acceptance_criteria":"- [ ] ..."}'
```

### Orchestration projection v1 (PAI-925)

`GET /agent-mode/orchestration/v1?zoom=10` and
`GET /agent-mode/projects/:projectID/orchestration/v1?zoom=10` compose the configured
instance root, its active generation, project coordination, and the unchanged
worker-fleet v2 snapshot. The closed contract is
[`orchestration-v1.schema.json`](../backend/contracts/orchestration-v1.schema.json).
Both routes use Agent Mode authorization and `private, no-store`, including
concealed 404 responses. Counts include authorized projects only; the separate
`coordination_bounds` includes empty projects, while fleet totals retain their
existing meaning. Zoom and communication/history bounds are inherited from fleet
v2. Root generation outside the visible sample is explicitly unknown; configured
identity exposes only the existing display label. Project coordination never
creates cross-project parent-session edges.

### Browser-to-agentd lifecycle intents (PAI-924)

`/api/projects/{id}/lifecycle/v1` is an additive typed authority; it does not
change the PAI-925 orchestration projection or execute local processes. The
OpenAPI `Lifecycle*V1` schemas freeze the class-only wire contract. Named
account choices are an optional versioned extension of the same runtime object:
`schema_version: 2` with `accounts[]` of `LifecycleAccountChoiceV2` (`key` plus
operator `label`). Intent `account_key` is optional and is used only with those
versioned named choices. Omitting `accounts` keeps the v1 class-only
advertisement. The authenticated reporter client accepts claimed and
transitioned intents with schema 1 or named-account schema 2 and keeps the
claim envelope at `schema_version: 1`. Unknown intent schemas, mismatched
runtime/project bindings, and schema/account-key mismatches fail closed before
local execution.

- Browser super-admin sessions: `GET /runtimes`, `POST /intents`,
  `GET /intents/{intentID}`, `GET /intents/{intentID}/events`, and
  `POST /intents/{intentID}/cancel` with `expected_revision`.
- API-key super-admin reporters with `agent-controls:runner` (or `*`):
  `POST /runtimes`, `POST /runtimes/{runtimeID}/sessions`,
  `POST /runtimes/{runtimeID}/claim`, and
  `POST /intents/{intentID}/transition`. Private runtime lease proofs use
  `X-Paimos-Runtime-Lease`; session registration also requires the existing
  `X-Paimos-Harness-Worker-Lease`. Proofs never appear in responses.

Start carries canonical agent, exact immutable profile, explicit ticket/work
shape/role and optional same-project parent. Attach/reassign additionally require
an owned session generation and its expected revision; completion applies the
binding CAS and existing assignment event atomically. Restart requires one
owned terminal generation and reserves a new explicit generation. Repair is
bounded to the named `reporter` or `listeners` runtime layer. Starting an
already occupied agent address, exclusive workspace or coordinator is refused.
All these requests retain super-admin authority because an admin daemon must
not grant managed target privileges to a lower-role browser requester.

Runtime discovery advertises opaque workspace handles and identity digests,
immutable profiles, one bounded account class label, optional versioned named
account choices (`accounts[]` with `schema_version: 2`), and proved public
`session_id`/`generation` mappings. Machine provenance uses the existing
**authenticated reporter's Host mapped to MachineID** trust gate; this is not
hardware attestation. A daemon generation may advertise separate project-bound
handles. Every authoritative transition reauthorizes both the exact reporter
credential and the original browser session. Logging out or revoking either
principal closes outstanding authority. Status/history/cancel require the
creating user to remain an authorized browser super-admin.

Workspace advertisements may include an explicit operator-configured `label`
(1–48 trimmed ASCII letters/digits/spaces/dot/underscore/hyphen, with a leading
letter or digit). Secret-like values and paths are refused. The server adds
`workspace_handle` to a discovered session only when its current managed
workspace identity matches exactly one advertised identity. An absent handle
means unknown; clients must never infer one from a path or fingerprint.
Session registration still accepts only `session_id` and `generation`.

`GET /runtime-health` is a separate content-free browser read for current
internal project viewers, using the canonical Agent Mode permission predicate.
It requires a current, non-impersonated human session. API keys, including
owner/admin and reporter keys, receive `403 lifecycle_unavailable`. The endpoint
reauthorizes the exact browser session and every reporter owner, returns the
latest runtime per machine with a maximum of 32 records, and exposes no totals.
Each record has four typed layers (`reporter`, `primary`, `fallback`, `attention`).
Missing reports have `state/status: unknown` and `reason: not_reported`; evidence
older than 60 seconds has `status: stale`; expired runtimes and their layers have
`status: offline`. Reported state/reason/count/time are retained as last observed
evidence and never imply current health when status is stale/offline. Revoked
reporters are omitted. The `RuntimeHealthPageV1` schema freezes the bounded wire.

States are `requested`, `claimed`, `executing`, `completed`, `failed`, `expired`
and `cancelled`, with immutable audit events and terminal outcomes. Request keys
are UUIDs; exact retries return the original record and conflicting reuse fails.
One intent may be claimed/executing per runtime. Cancel uses CAS and is allowed
only before executing commits; it cannot pretend to undo an external effect.
Intent TTL is 30–600 seconds and runtime lease TTL is 120 seconds. Refresh the
same immutable runtime advertisement before lease expiry; an expired daemon
generation cannot be revived. Expiry is materialized on authorized
status/events/claim access. Claimed/executing expiry records `outcome_unknown`;
no retry may reinterpret this as permission to spawn again.

Bodies are limited to 8192 bytes; duplicate/unknown fields and arbitrary argv,
shell, prompts, credentials, paths and private target references are rejected.
Each project admits at most 32 live intents, 32 live runtimes, 10,000 durable
intent replay records and 1,024 durable runtime records. A runtime admits 16
workspaces, 16 profiles and 128 proved session generations. Reaching a durable
record cap refuses new requests; automatic history deletion is not provided.
Responses are private/no-store and inherit authenticated response metadata.
Foreign, missing, revoked and wrong-provenance targets share a bounded refusal;
no private payload or credential identity is echoed.

Daemon integration must implement `lifecycleintents.RuntimeAuthority`, register
its exact principal/generation/proof, and claim/transition through these routes.
Before local effects it must journal the intent ID durably, resolve only an
explicitly configured local workspace handle, reverify provenance/account and
immutable profile, and obtain instructions from authorized canonical agent and
ticket resources. Commit `executing` before invoking an operation; after a crash,
report a proved original outcome or `outcome_unknown`, never automatically
respawn. Start/restart completion requires a newly registered managed session
with the reserved generation and exact authorized specification. Repair
completion records an authenticated reporter's applied result, not independent
server attestation of a local repair. Receipt-only code cannot complete PAI-924;
local execution, durable replay and browser end-to-end integration remain
required before this contract represents an operational lifecycle workflow.

Existing controls remain compatible: harness `pending` maps to `requested`,
`claimed` to `claimed`, `applied` to `completed`, and `rejected` to `failed`.
Their execution stage is unreported. Steer message delivery queued/attempted/
acknowledged maps to requested/executing/completed **delivery evidence only**;
it never proves task completion or a reply. Existing root configuration remains
the super-admin `/api/orchestrator/v1/config` CAS surface.

### Generation-owned inbox and attention consumers (PAI-923)

`/api/projects/{id}/consumers/v1` requires the exact current API-key super-admin
reporter with `agent-controls:runner` (or `*`) and private
`X-Paimos-Runtime-Lease`. `POST /streams` registers a fallback or attention owner
bound to the proved runtime/session generations and selected immutable target
ID/version; `X-Paimos-Consumer-Lease` is a separate daemon-generated private
32-byte base64url proof. Refresh the same registration within its 120-second
lease. Public generation IDs and encrypted-target metadata never authenticate
an owner. The closed `Consumer*V1` OpenAPI schemas define the full wire.

`POST /streams/{streamID}/claim` carries `expected_revision` and UUID
`request_key`, plus a fresh private `X-Paimos-Consumer-Attempt` nonce. Persist
that nonce locally before claiming. Claims reserve the existing FIFO delivery
or attention batch and return content-free attempt metadata only. The
`.../attempts/{attemptID}/execute` call carries `expected_revision` and all three
proofs. It commits `executing` before returning a transient payload once;
retries return only attempt state. Never log or journal execution payloads.

`.../complete` carries the exact revision/nonce, `outcome` (`applied` or
`outcome_unknown`), `effective_level: simple` and a closed `fallback_reason`
(empty for attention). Applied completion advances the existing delivery/batch
and cursor atomically. Exact original completion retries retain their saved
cursor after safe owner replacement. Unexecuted claims expire after 60 seconds
and can release safely. Issued payloads with an ambiguous outcome are quarantined
and never re-leased. Lost execute responses never authorize a vendor retry.

Sticky database fences refuse legacy claims, requeues and acknowledgements even
after stream expiry. An existing legacy lease must finish before registration;
an expired legacy lease still requires safe handoff. The managed harness's
existing exact worker-lease primary path remains supported. There is no second
queue and no claim of exactly-once vendor effects. Streams cap at 256 per
project and retained attempts at 10,000 per stream; reaching either cap refuses
new work without deleting replay evidence. Request bodies cap at 4096 bytes.

`POST /runtime-health` uses only runtime proof and monotonic sequence per
runtime/layer (`reporter`, `primary`, `fallback`, `attention`). Typed healthy or
unhealthy reports carry a closed reason and failure count 0–10. One unhealthy
episode produces an existing-ledger attention item, with a one-minute publish
throttle; recovery resolves that source. The typed health row persists even if
no attention receiver is configured. All responses are `private, no-store`;
wrong, missing, foreign and revoked authority share `consumer_unavailable`.

### Model registry (PAI-1049)

- `GET /api/models/catalog`: authenticated dispatch profiles, allowed model
  efforts, family/tier metadata, typed roles, and this instance's overrides.
- `GET /api/models/resolve?role=<role>[&author_family=<family>][&harness=<harness>]`:
  pinned profile, exact CLI run `command_template`, `command_templates`
  (`run`, `review`, `resume`, `spawn`), ordered ladder and skip reasons.
  Every model candidate carries the same pinned templates; CLI validation
  rejects missing or altered operations, including skipped candidates.
  Review gates require the author's family; owner fallback has no profile.
- `PUT /api/models/overrides` (admin): atomically replace
  `{"overrides":[{"profile_id":"claude-fable-xhigh","version":"2","state":"conserved","reason":"reserve allowance","until":"2026-09-30T00:00:00Z"}]}`.
  States are unavailable, conserved or budget-limited; expiry must be future
  RFC3339. Empty overrides clear policy. Scope is the serving instance database.

See [model roles](AGENT_INTEGRATION.md#model-roles-and-instance-policy-pai-1049)
for CLI/cache behavior. Resolution does not run a model or open a review gate.
