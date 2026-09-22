# PAIMOS Developer Guide

For operator-facing configuration, see [`CONFIGURATION.md`](CONFIGURATION.md).
For contribution mechanics (DCO, PR flow, review criteria), see
[`CONTRIBUTING.md`](../CONTRIBUTING.md).

**Driving PAIMOS as an agent (Claude Code / scripts / MCP):**
[`AGENT_INTERFACE.md`](AGENT_INTERFACE.md) is the entry point — covers
`paimos` CLI setup, schema discovery, bulk ops, MCP wiring, and
failure-mode guarantees.

**AI control-plane UX**:
[`AI_CONTROL_PLANE_UX.md`](AI_CONTROL_PLANE_UX.md) maps the PAI-646
AI profile, effort, prompt, context, agent, runner, and provenance workflow
to the current Vue and Go surfaces.

**External delivery-stage adapters**:
[`EXTERNAL_STAGE_CONTRACT.md`](EXTERNAL_STAGE_CONTRACT.md) defines the frozen
Pharos/Janus v1 routes and additive scheme-aware v2 evidence, credential
boundary, canonical fixtures, and release pin tuples.

**Project-context tooling**:
- [`ANCHORS.md`](ANCHORS.md) defines `@paimos` source anchors.
- `go run ./backend/cmd/paimos anchors scan --repo-root . --output .paimos/anchors.json`
  generates the deterministic anchor index.
- `go run ./backend/cmd/paimos anchors verify --repo-root . --index .paimos/anchors.json`
  is the local/CI stale-anchor guard.
- `scripts/upload-anchors.sh <project-key-or-id> <repo-id>` is the
  default CI-ready upload wrapper for the committed index.
- `paimos --instance ppm sync pull --project PAI --workspace .`
  refreshes canonical project artifacts into `.paimos/cache/`; use
  `paimos onboard --project PAI` for a one-shot briefing. The legacy
  `paimos manifest pull` flow was removed in PAI-358.

**Adding a CRM sync provider** (HubSpot, Pipedrive, Salesforce, …):
see [`CRM_PROVIDERS.md`](CRM_PROVIDERS.md) for the in-process Go
plugin interface and a worked example.

This guide is for people who want to understand, extend, or debug
PAIMOS's internals.

## 1. Architecture

```
Browser (Vue 3 SPA, Vite)
  ↕ JSON via /api/*
Go server :8888
  ├── chi router
  │    ├── /api/auth/*           session + TOTP + API keys
  │    ├── /api/projects/*       project CRUD + issues + reports
  │    ├── /api/issues/*         cross-project issue ops
  │    ├── /api/portal/*         external-role read-mostly view
  │    ├── /api/integrations/*   Jira + Mite config
  │    ├── /api/import/*         CSV + Jira + Mite async imports
  │    └── /api/(branding|health|instance|…)  meta
  ├── SQLite (WAL, single file at $DATA_DIR/paimos.db)
  ├── optional MinIO client (attachments)
  └── optional net/smtp client (password reset)
```

One process, one port. The Go binary serves both the API and the
built SPA (from `$STATIC_DIR`). No frontend dev server in production.

## 2. Repo layout

```
backend/
  main.go              entrypoint; all routes wired here
  brand/               BRAND_* env parsing (single source of truth)
  auth/                sessions, password hashing, TOTP, API keys, rate limiting
  agentmessage/        durable agent message ledger, delivery bus, adapters, and
                       webhook dispatcher (M151–M156; see AGENT_BUS_ARCHITECTURE.md)
  secretvault/         authenticated encryption for stored secrets and receiver targets
  contracts/           versioned JSON schemas + fixtures (agent-message-v1/v2, external-stage-v1/v2)
  cmd/paimos/          `paimos` CLI (issue, tell, message, listen, serve, run-agent, …)
  cmd/paimos-mcp/      MCP server binary
  db/                  SQLite open + migrations (each migration inline in db.go)
  models/              shared structs (User, Issue, Project, …)
  handlers/            HTTP handlers; one file per domain
    assets/            embedded files (PDF fonts, report logo)
  storage/             MinIO/S3 client (Init + Put/Get/Delete)
frontend/
  src/
    views/             top-level routed pages
    components/        shared UI pieces (MetaSelect, IssueTree, …)
    composables/       reusable Vue 3 composables (useIssueFilter, useBranding, …)
    api/               thin fetch wrappers per domain
    i18n/              vue-i18n message catalogs
  public/              static assets (logo.svg, favicon.svg, app-icon.svg)
docs/
  CONFIGURATION.md     every env var
  CHANGELOG.md         release notes
  DEVELOPER_GUIDE.md   this file
  AI_CONTROL_PLANE_UX.md
                       PAI-646/648 UX map for AI actions, agents, and runs
  DEPLOY.md            live-deploy + backup/rollback runbook (ppm)
  DATA_MODEL.md        current schema (tables, relations, enums)
  archive/
    DATA_MODEL.md      legacy v0.3.5 snapshot — archival only
  AGENT_INTEGRATION.md authenticated API usage for agents
  AGENT_INTERFACE.md   `paimos` CLI, MCP, and durable agent message surface
  AGENT_BUS_ARCHITECTURE.md
                       instant agent bus design: levels, targets, deliveries, adapters
  AGENT_MESSAGE_SECURITY.md
                       untrusted-message framing and native delivery boundary
  api-minimal.md       compact API surface reference
  brand/               visual identity (mark + wordmark + brand guide)
+agents/rules/         rules agents follow when editing this codebase
+pm/                   long-form product framing only (PRD, FIELD_MATRIX).
                       Tickets/backlog live in PAIMOS itself —
                       https://pm.barta.cm, project PAI (id 6).
scripts/               maintenance helpers
```

## 3. Backend conventions

- **Handlers** are thin HTTP→DB adapters. Complex logic lives in
  sibling files in `backend/handlers/` (e.g., `import_engine.go`,
  `imageutil.go`).
- **DB access** is direct `sql.DB` — no ORM. Prepared statements are
  compiled inline via `db.DB.Query(…)`. Makes it easy to read the exact
  query in context.
- **Migrations** live in `backend/db/db.go` inside `migrate()`. Each
  migration is guarded by the `schema_versions` table and runs on one
  pinned SQLite connection. Migrations are transactional by default:
  all steps and the `schema_versions` insert commit together or roll
  back together. The only exception is a table-rebuild migration that
  toggles `PRAGMA foreign_keys`; SQLite ignores that pragma inside an
  open transaction, so those migrations are explicitly connection-
  pinned but non-atomic. Additive-only — never rewrite a past migration.
- **Context propagation**: standard Go `context.Context`; auth loads
  the user into context via middleware in `backend/auth/`.
- **Auth**: session cookies by default, `Authorization: Bearer
  paimos_…` API keys checked first. Both paths resolve to the same
  `*models.User` attached to the request context.
- **Error envelopes**: handlers call `jsonError(w, msg, status)` for
  errors and `jsonOK(w, payload)` for success. No panic recovery
  beyond chi's default `middleware.Recoverer`.

### Adding a harness delivery plugin

Harness delivery adapters live in `backend/agentmessage/harness`. A new
adapter normally needs one Go file: implement the version-1 `Plugin` interface
(`Name`, `Kind`, `MaximumLevel`, `Mode`, `ValidateTarget`, and `Deliver`) and
register it from `init` with `harness.Register`. Core address policy, the
durable ledger, encrypted target storage, leasing, retry, and acknowledgement
remain outside the plugin.

Validation errors and delivery errors must never contain `TargetRef`. Use only
documented vendor primitives, declare `simple` when steer is unavailable, and
return `UNSUPPORTED` instead of guessing an inbound command. Tests can prove
an extension on a fresh `harness.NewRegistry()` without editing the bus or
ledger. The shipped plugins are Codex, Claude resume/channel, and the
server-side `grok_bot_routine` webhook; OpenCode and Pi are not shipped.

A server-side webhook plugin whose vendor authenticates each wake with a
per-target sender secret additionally implements the optional
`SecretHeaderPlugin` capability (`SecretHeader` names the header and value
prefix, `ValidateSecret` checks the raw key without echoing it). Implementing
it makes the secret mandatory at registration (`target_secret`, CLI
`--target-key-file`); plugins without it refuse one. Core encrypts the secret
under its own secretvault domain (`target_secret_cipher`, M157), exposes only
`has_secret`, and the dispatcher renders the header at send time.

## 4. Database

- SQLite only. `WAL` journal mode, 5-second busy timeout, foreign
  keys on.
- Schema migrations are idempotent and run at every startup.
- Default filename is `paimos.db` (override with `BRAND_DB_FILENAME`;
  see caveats in `CONFIGURATION.md`).
- See [`DATA_MODEL.md`](DATA_MODEL.md) for the full schema.

## 4a. Access model (per-project)

PAIMOS uses a three-level per-project access model, stored in the
`project_members` table:

| Level   | Read | Write | Notes                                           |
| ------- | ---- | ----- | ----------------------------------------------- |
| `none`  | no   | no    | Explicit denial; overrides the member default. |
| `viewer`| yes  | no    | Read-only access to the project and its issues.|
| `editor`| yes  | yes   | Full read + write within the project.          |

**Role defaults** (applied when no `project_members` row exists):

- **admin** — always bypasses per-project checks (effectively editor
  everywhere).
- **member** — default `editor` on every non-deleted project.
- **external** — default `none`; must be granted explicitly.

Seeding is automatic:

- `CreateUser` (for admin/member roles) seeds `editor` rows for every
  non-deleted project.
- `CreateProject` seeds `editor` rows for every active admin/member.
- Migration 64 backfills existing portal grants as `viewer` and seeds
  admin/member editors on pre-existing projects.

**API surface**:

- `GET  /api/permissions/matrix` — capabilities × levels matrix for UI.
- `GET  /api/users/{id}/memberships` — per-project effective level.
- `PUT  /api/users/{id}/memberships/{projectId}` — upsert a grant.
- `DELETE /api/users/{id}/memberships/{projectId}` — revert to default.
- `GET  /api/access-audit` — admin-only audit trail.

**Go helpers** (in `backend/auth/access.go`):

- `auth.CanViewProject(r, projectID) bool`
- `auth.CanEditProject(r, projectID) bool`
- `auth.ProjectAccessLevel(r, projectID) AccessLevel`
- `auth.AccessibleProjectIDs(r) []int64` (returns nil for admin)
- `auth.WithAccessCache(ctx)` — attaches a per-request memoization cache
  that batches the lookup; already applied by `auth.Middleware`.

**Middleware** (in `backend/auth/middleware_project.go`):

- `RequireProjectView` / `RequireProjectEdit` — gates a chi route with
  a `{id}` (or `{projectId}`) param on view or edit access.
- `RequireIssueAccess` / `RequireIssueEdit` — resolves the project via
  `issues.project_id`; orphan sprint issues pass through.
- `RequireAttachmentAccess` / `RequireAttachmentEdit`
- `RequireTimeEntryAccess` / `RequireTimeEntryEdit`
- `RequireCommentAccess` / `RequireCommentEdit`

Response convention: 404 on no-view access (no existence oracle), 403
on view-only-when-edit-required.

**Frontend access**:

`/auth/login`, `/auth/me`, and `/auth/totp/verify` return an envelope
`{ user, access, impersonation? }`. `access = { all_projects, levels: { [pid]: "viewer"|"editor" } }`.
When `impersonation.active` is present, `user` is the effective target
and `impersonation.actor` is the real super-admin session owner.
The Pinia store (`stores/auth.ts`) exposes `canView(pid)` and
`canEdit(pid)` plus a hydrated `accessibleProjects` Map. Router routes
opt in to per-project guarding by setting
`meta.projectIdParam: 'id'`.

## 5. Frontend conventions

- **Vue 3 + `<script setup lang="ts">`** for all new components.
- **Strict TypeScript** — `npm run typecheck` must pass.
- **State** — three tiers, pick the smallest that fits:
  1. **Pinia store** (`src/stores/*.ts`) for global, cross-view domain
     state: auth session + permissions, running timer, search query,
     drag-drop intent. Use when multiple unrelated routes need to
     read or mutate the same thing, or when the state outlives any
     single view.
  2. **Module-scope composable singleton** (`src/composables/use*.ts`
     with `const foo = ref(...)` at module level) for UI-level
     preferences that are app-wide but not really domain state:
     sidebar colors, table appearance, timer panel open/closed,
     time unit, issue-display toggles, branding. These persist via
     `localStorage` and share a single reactive cell across every
     consumer.
  3. **Component-local `ref`/`reactive`** for state that belongs to
     one component. No default, no prop drilling.

  No Vuex. No duplicating the same piece of state in two tiers.
- **Routing**: `vue-router`, lazy-loaded views. The list-view URLs
  (internal `/issues`, `/projects/:id`, and customer-portal project
  view) sync the open sidebar selection to `?selected=<ISSUE_KEY>`
  via `replaceState` (PAI-479) so a user can copy the URL bar and
  share a deep-link to a specific issue with its surrounding list
  context preserved. Plumbing lives in `useSidebarSelectionUrl`;
  internal `IssueList` opts in via `:url-sync-selection="true"`.
- **i18n**: `vue-i18n`; English and German catalogs in `src/i18n/`.
- **Locale-aware display formatting**: user-facing numbers, dates,
  times, file sizes, durations, and fixed-currency values must go
  through `src/composables/useNumberFormat.ts` or
  `src/composables/useDateFormat.ts` (the legacy
  `src/utils/formatTime.ts` functions delegate there). The display
  locale resolves from the authenticated user's locale / vue-i18n
  locale first, then the browser locale, then `en-US`. Do not add raw
  `.toLocaleString()`, `.toLocaleDateString()`, `.toLocaleTimeString()`,
  `.toFixed(...)`, or direct `new Intl.*Format(...)` calls in views or
  shared UI. Allowed exemptions are internal numeric state, machine
  output with a documented fixed format, and the formatting helpers
  themselves. Run `scripts/check-frontend-formatting.sh` before review
  when touching UI display formatting.
- **Styling**: scoped `<style>` blocks + a small set of CSS custom
  properties fed from the branding API.
- **localStorage**: every key lives in `src/constants/storage.ts` —
  import from there instead of inlining the string. New keys follow
  `paimos:<domain>:<thing>`; a few pre-dating the convention keep
  their legacy names (documented in the module) to avoid wiping
  existing user preferences.

### Project settings IA (PAI-505 → PAI-508)

The project page (`ProjectDetailView.vue`) is built entirely from
**footer-bar tabs** (`ProjectFooterBar`) — there is no ⋯-menu modal for
project configuration anymore. PAI-508 eliminated the Edit Project
modal:

- **First-class concepts** are left-aligned **content tabs**: Issues,
  Overview, Knowledge, **Agents** (PAI-504), Docs, Coop, Context. These
  are things a user works *with* day to day. Each tab is its own
  component; counts are fed by always-mounted hidden sentinels so the
  footer badges stay live regardless of the active tab. Writes are
  gated on `isAdmin && canEdit(projectId)`; reads are open to project
  viewers — never gate on `isAdmin` alone.
- **Project configuration** lives in the **Settings tab** — a
  **right-aligned, admin-only** footer tab (gear icon + "Settings"
  label), separated from the content tabs by a flex spacer
  (`.pfb__spacer`). The footer renders the button only when
  `canEditSettings` (`isAdmin && canEdit(projectId)`) is true; a
  hand-typed `?tab=settings` deep link falls through to the default
  `issues` tab for non-admins. The tab body is a single scrollable
  surface (no sub-tab bar) split into three plain `<h3>` sections:
  **General** (name, key, description+AI, status, tags, product owner,
  logo), **Billing** (customer, customer label, rates), and **Danger
  zone** (archive / delete / purge). General + Billing share one
  `<form @submit.prevent="saveProject">` with a single Save button; the
  Danger zone sits outside that form with its own inline action buttons.
  The form is kept populated by a `watch` on `project.id` (repopulates
  only when the project identity changes, so in-progress edits and tag
  add/remove are never clobbered).
- **Environments + deploy recipes** (`ProjectInventoriesSection`) moved
  out of settings entirely and now render under the **Context** tab,
  alongside repos (`ProjectContextSection`), so all per-project
  infrastructure config lives on one tab.

**Rule for future additions:** a first-class concept (like Agents or
Knowledge) gets a left-aligned content footer-bar tab. Genuine project
*configuration* goes in the Settings tab — add it to the matching
General / Billing / Danger section; per-project infrastructure
(repos, environments, recipes) belongs on the Context tab. Nothing
hides in a ⋯-menu modal.

## 6. Testing

- **Backend**: `cd backend && go test ./...` — covers handlers,
  auth, imports, sprint logic, reports.
- **Frontend**: `cd frontend && npm test` (Vitest + happy-dom) —
  covers the thin API client and a handful of critical components.
- **Agent bus proofs** (opt-in, real vendor CLIs; skipped when the
  variable is unset): `PAIMOS_AGENT_BUS_E2E_THREAD=<codex thread id>
  go test ./cmd/paimos -run TestAgentBusRealCodexSimpleE2E` and
  `PAIMOS_AGENT_BUS_E2E_CLAUDE_SESSION=<local session UUID>
  go test ./cmd/paimos -run TestAgentBusRealClaudeSimpleE2E` drive
  `paimos tell` through the ledger, a live listener, the real
  `codex queue` / `claude -p --resume` primitive, and the cursor ack,
  logging commit/pickup/handoff timings.
- **Smoke test**: `docker compose up --build`, then visit
  `http://localhost:8888`, create an admin, create a project and
  issue, upload an attachment (MinIO required), log out, reset
  password (dev mode logs the link).

## 7. Running locally

See the "Quick start" and "Local dev" sections of the
[`README`](../README.md). Short version:

```bash
cd backend && DATA_DIR=../data STATIC_DIR=../frontend/dist go run .
cd frontend && npm run dev    # separate terminal
```

With `devenv shell` if you'd rather not manage Go / Node versions
yourself.

## 8. Extending PAIMOS

### Adding an API endpoint

1. Pick the right handlers file (e.g., `handlers/issues.go` for
   issue-centric routes).
2. Write the handler (`func Foo(w, r) { … }`).
3. Wire the route in `backend/main.go` under the right middleware
   group.
4. Add a test in the same handlers package.
5. Consume from the frontend via a thin wrapper in `frontend/src/api/`.

### Adding a migration

1. Append a `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE` block to
   `migrate()` in `backend/db/db.go`.
2. Bump the version counter (see existing pattern).
3. Reflect the schema change in `models/` and `docs/DATA_MODEL.md`.
4. Test on a fresh DB and on a DB with the old schema (migrations are
   one-way).

### One-shot offline PAI-857 pre-M162 repair

`paimos-repair-knowledge-857` is an offline, clone-only repair for the
single verified backup named `ppm-857-20260830-2130`. It has no defaults
or discovery. Its built-in authorization lock requires the exact
`ppm.db`, `ppm.db-wal`, and `ppm.db-shm` sizes and SHA-256 values recorded
for that backup, along with `issues=4881`, knowledge rows `=326`, clean
integrity/foreign keys, and M162 still pending.

Run it from `backend/` with three new, explicit absolute paths:

```sh
go run ./cmd/paimos-repair-knowledge-857 \
  --source-backup-dir /absolute/path/ppm-857-20260830-2130 \
  --clone-dir /absolute/path/ppm-857-repaired-clone \
  --report /absolute/path/pai-857-repair-report.json
```

The source directory is opened only for hashing and byte copying. The
tool creates the clone with restrictive permissions, verifies both
copies of the WAL-safe trio, and opens only the clone through SQLite. It
refuses symlinks, an existing destination/report, schema drift, any
unapproved collision, divergent or referenced losers, and any changed
fingerprint. It never accepts a live database path or an unconditional
delete instruction.

A successful report has `status: "clean"`, enumerates survivor/loser IDs
and every checked reference surface without issue content, and proves
post-repair integrity, foreign keys, counts, and M162-pending state. Its
`report_sha256` is SHA-256 of the compact JSON object with that field set
to the empty string, making verification non-self-referential. Normal
`db.Open` may apply M162 only to the verified repaired copy after this
report exists. A report created from synthetic tests is not evidence
that the actual backup is clean; actual-backup CLEAN requires the
operator-produced report from the exact fingerprinted trio.

### Adding an integration

Look at `handlers/integrations.go`, `handlers/jiraimport.go`, and
`handlers/miteimport.go` for the pattern:

- Store credentials (encrypted if sensitive) in a dedicated table
- Provide `GET` / `PUT` config + `POST /test` endpoints, admin-only
- Long-running imports run as async jobs tracked in a jobs table
- Status endpoints poll the jobs table

### Adding a knowledge-plane writes hook (PAI-353)

Knowledge entries (memory / runbook / external_system / related_project
/ guideline) are issues with extra discipline. When you ship behaviour
that should fire on a knowledge mutation — e.g. reference-count bumps
(PAI-347), propose-flow rate limits (PAI-349), agent re-render
notifications (PAI-331) — wire it as a writes hook in
`backend/handlers/knowledge/`:

1. Register the hook against the canonical `Create` / `Update` /
   `Delete` handlers (they fan out to per-type code paths but share the
   hook chain).
2. Hooks receive the pre-mutation row and the post-mutation row;
   short-circuit by returning an `*errs.Reject` to fail the request
   before the DB write commits.
3. Side effects that must survive failures (audit, push, fan-out) live
   in `defer` blocks gated on the commit succeeding — same pattern as
   `mutation_log` writes elsewhere in handlers.

### Adding a sync `Resource` (PAI-331 / PAI-341)

The sync engine (`paimos sync`, the `/projects/:id/agents/events` SSE
stream, the `.rev` cheap-poll fallback) operates over a kind registry.
Today only `skill` is registered; PAI-341 adds knowledge-plane kinds.
To add one:

1. Implement the `sync.Resource` interface (currentRev, list, fetch,
   write) in `backend/sync/<kind>.go`. Reuse the `.json` / `.rev`
   handler pair so the cheap-poll fallback works out of the box.
2. Register the kind in `backend/sync/registry.go` so the generic verbs
   (`paimos sync init|pull|watch|check`) discover it.
3. Add a CLI alias under `backend/cmd/paimos/cmd_<kind>.go` only if the
   verb deserves friendlier ergonomics than `paimos sync <verb>
   --kind=<kind>`.

### Adding a harness adapter (PAI-332)

Built-in renderers cover Claude Code, Codex, Grok, Pi and Cursor.
Additional external harnesses (e.g. aider, your-tool) plug into
`paimos skill render` via the adapter SDK at `docs/adapter-protocol.md`:

1. A discoverable adapter is a directory on `$PAIMOS_ADAPTER_PATH`
   containing `paimos-adapter.json` plus an executable named
   `paimos-adapter-<name>`.
2. External adapters implement three verbs: `render --input -`,
   `describe`, and `validate --input -`. PAIMOS writes the managed
   header and output path; the adapter owns only the canonical-agent
   JSON to harness-specific content transform.
3. `paimos skill test-adapter <name>` runs the PAI-332 conformance
   suite. Passing conformance is the gate for public registry entries.

### Changing brand defaults

PAIMOS's identity is set via `BRAND_*` env vars (loaded once at
startup into `backend/brand/brand.go`). Adding a new brand-shaped
string:

1. Add a field to the `Brand` struct and populate it in `Load()`
2. Read from `brand.Default.<Field>` in the handler
3. Document the env var in `docs/CONFIGURATION.md`
4. Update `.env.example`

## 9. Code style

- `gofmt` + `goimports` for Go. CI blocks unformatted diffs.
- Prefer explicit over clever. A three-line `for` loop is better than
  a chain of unreadable `strings.Map`s.
- Comments for the *why*, not the *what*. Well-named identifiers make
  the "what" obvious.
- No new dependencies without an issue discussion. PAIMOS is
  deliberately small.

## 10. Common pitfalls

- **Don't hardcode identity strings.** Use `brand.Default` instead.
- **Don't write a migration that can't re-run.** `IF NOT EXISTS`
  everywhere.
- **Don't forget the frontend after a backend schema change.** TS
  types in `frontend/src/api/` need updating too.
- **Don't break the graceful-disable behaviors** (MinIO, SMTP). If the
  env var for an optional service is unset, the feature should
  degrade politely — not panic on startup.
