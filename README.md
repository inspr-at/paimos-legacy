<p align="center">
  <img src="docs/brand/favicon.png" alt="Paimos" height="96">
</p>

<h1 align="center">Paimos</h1>

<p align="center">
  <strong>One project picture for people and AI agents.</strong>
</p>

<p align="center">
  Paimos keeps specifications, project work, repository context, operating
  knowledge, execution choices, and run evidence in one self-hosted system.
</p>

<p align="center">
  <code>v260922104613.0.0</code> · <code>AGPL-3.0-only</code> ·
  <code>Go + Vue + SQLite</code>
</p>

<p align="center">
  <a href="https://paimos.inspr.at/">Product site</a> ·
  <a href="https://paimos.inspr.at/de/">Deutsch</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#agent-intercom-quickstart">Agent Intercom</a> ·
  <a href="docs/AGENT_INTERFACE.md">Agent interface</a> ·
  <a href="docs/CONFIGURATION.md">Configuration</a> ·
  <a href="docs/HARDENING.md">Operations</a> ·
  <a href="SECURITY.md">Security</a> ·
  <a href="CONTRIBUTING.md">Contributing</a> ·
  <a href="LICENSE">License</a>
</p>

---

<p align="center">
  <img src="docs/brand/screenshots/ppm-hero.png" alt="Paimos issue list and issue detail workspace" width="100%">
</p>

Paimos is a self-hosted project management and execution-context system for
software teams working with AI agents. People use the web application. Agents
use the CLI, MCP facade, or JSON API. Both work against the same project state,
permissions, knowledge, and history.

Paimos is part of the open [INSPR product family](https://www.inspr.at/) and is
authored and published by [Markus Barta](https://github.com/markus-barta).
Augmentoring's professional services use and integrate Paimos; Augmentoring is
not the product owner.

> [!NOTE]
> Paimos 6 is production-used and actively developed. Releases use INSPR
> calendar v2 tags such as `v260910081500.0.0` (the UTC reservation second as
> a SemVer-shaped coordinate); the preserved 5.x interface remains available at
> `/legacy`. The deployment model is deliberately compact: one Go process and
> one SQLite database. It is not a multi-node high-availability service. See
> [Current maturity and limits](#current-maturity-and-limits) before a production
> rollout.

## Why Paimos exists

An issue tracker tells an agent what a ticket says. It usually does not tell the
agent which repository matters, which operating rule applies, what earlier work
established, what it may execute, or what evidence must come back.

Paimos keeps those facts connected:

```text
project work
  + linked repositories
  + maintained knowledge
  + issue-to-file anchors
  + people and project agents
  + durable agent handoffs
  + bounded execution choices
  + action and run evidence
  = one inspectable project history
```

This is useful without AI as a focused project system. With agents, the same
model becomes the context and accountability layer around their work.

## What ships now

| Area                | Current capability                                                                                                                                                                                           |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Project work        | Hierarchical issues, relations, sprints, releases, priorities, tags, saved views, bulk changes, time tracking, budgets, comments, attachments, full-text search, and active/frozen/archived project lifecycle controls. |
| Voice intake        | A spec workbench that turns continuous speech (or typed input) into a live, editable specification with project detection, impact analysis, ELI5/10/15 understanding checks with spoken playback, per-language (EN/DE) cached artifacts, time-travel history, and one-click issue creation. Voice endpoints are rate-limited, budgeted, and cost-metered. |
| Project context     | Linked repositories, typed knowledge, canonical project-agent artifacts, issue-to-file anchors, entity graph and blast-radius reads, and mixed-context retrieval.                                            |
| Agent interfaces    | A typed `paimos` CLI, `paimos-mcp`, REST, curated OpenAPI, self-describing schema, JSON output, file-first multiline input, dry runs, idempotent transitions, and declarative bulk apply.                    |
| Agent Intercom      | Durable, project-scoped message threads with sender allowlists, simple/steer intent, encrypted receiver targets, truthful fallback, content-free actionable attention wakes, owned Codex/Claude status, steer, interrupt, and stop through `paimos-agentd`, plus explicit recovery actions whenever an orchestrator or canonical agent is missing. |
| Assisted work       | Thirteen in-app AI actions with operator-managed prompts, usage limits, cost records, execution profiles, context packs, and metadata-only audit records.                                                    |
| Implementation runs | Explicit Claude Code and Codex local-runner actions, plus OpenRouter and OpenAI-compatible local-model draft providers. Trusted runners report repository, branch, and before/after commit evidence; draft providers cannot claim repository mutation, tests, shell, or deploy authority. |
| Collaboration       | Internal roles and project grants, an external customer portal, acceptance workflows, customer-facing summaries, and JSON/PDF project reports.                                                               |
| Integrations        | Generic OIDC, Jira, Mite, CSV import/export, HubSpot, and an HMAC-signed HTTP CRM sidecar contract. Optional integrations do not become core runtime dependencies.                                           |
| Operations          | Docker deployment, tracked startup migrations, SQLite WAL, optional S3-compatible attachments and SMTP, health/schema endpoints, configurable branding, retention controls, and backup/restore runbooks.     |
| Supply chain        | Keylessly signed container images, CycloneDX backend and frontend SBOM attestations, checksums, and build provenance in the release workflow.                                                                |

The [claim and evidence matrix](docs/claim-matrix.md) connects public claims to
shipped code and documented verification.

### Recent product changes

- PAI-917 adds a worker-focused Habitat control room with Home,
  Workers, Projects and Needs you, plus worker detail, profile/workspace choices
  and activity history. Runtime
  health distinguishes fresh, stale, offline and missing evidence; assignments
  and controls retain their existing authorization and revision checks.
- Guided orchestrator/worker starts and instance-scoped runtime setup, diagnosis,
  repair and reset now share the same explicit ownership model. Typed browser
  lifecycle requests and generation-fenced inbox/attention consumers connect to
  that local runtime. These workflows require matching PAI-917 server, CLI,
  and daemon builds. See the [release notes](docs/CHANGELOG.md) for the
  implementation scope and remaining verification boundary.

- Paimos 6 empty states became actionable in 26.09.04.20.54: an authorized
  operator can open the existing agent editor, refresh stale choices, retry
  unavailable projections, copy a bounded admin request, or enter the existing
  talk surface. The browser never runs the terminal command or receives a
  credential.
- Paimos 6 promoted the session-first product shell to the authenticated home
  (26.08.31), while preserving the complete 5.x dashboard at `/legacy`.
- Release tags use INSPR calendar v2, `YYMMDDhhmmss.0.0` in UTC (PAI-979); the
  earlier `yy.mm.dd[.hh.mm]` cuts and the 5.x SemVer history stay in the
  changelog as one sequence and are never retagged.
- Voice Intake shipped end-to-end (5.1.0–5.4.0): ElevenLabs Scribe speech
  capture that survives interruptions, a live spec workbench with project
  auto-detection and impact analysis, spoken ELI summaries, and a
  configurable per-session AI budget.
- The paid voice endpoints gained cost gates and real metering (5.5.0):
  per-user concurrency, burst caps, daily audio/character budgets, and
  estimated cost on the AI paper trail.
- Language switching became a cached view switch (5.6.0) — no
  regeneration cost — and the workbench toolbar moved into the app
  header. The orchestrator can no longer drop an end-of-dictation
  update (5.6.1).
- CLI credential hygiene (5.6.2): no API keys via process arguments;
  legacy plaintext keys always migrate into the OS keyring.
- Identifier-first login (5.7.0) discovers the configured home realm before
  starting OIDC, while branded instances render their configured name instead
  of a hard-coded product label (5.7.1).
- Completed agent runs now carry runner-declared repository, branch, and
  `(base, head]` commit evidence beside their outcome (5.8.0). The 5.8.1
  patch keeps the live knowledge freshness gate aligned with the canonical API
  schema and compatible with macOS Bash 3.2. Project lifecycle controls in
  5.8.17 keep active work visible by default and stop frozen or archived
  projects from silently accepting new issues.
- Durable agent messaging shipped across 5.15.0–5.18.0: an attributable,
  issue-visible ledger; security-framed reads and human-gated action requests;
  durable receiver cursors; message-level simple/steer intent; encrypted,
  versioned receiver targets; Grok Bot HTTPS wake; and exact Codex queue/steer
  delivery with deterministic simple fallback.
- From 4.8: generic OIDC with PKCE (Zitadel-validated), shared
  provider/profile/effort/prompt metadata across AI actions and
  implementation runs, and Claude Code / Codex as distinct local
  execution choices.

For release-by-release detail, read the [changelog](docs/CHANGELOG.md).

## Human and agent work share one model

### Web application

The Vue interface covers everyday planning and delivery work: issue hierarchy,
side-panel editing, search, filters, custom views, sprints, time, reports,
administration, and the customer portal. It is keyboard-oriented and backed by
the same JSON routes used by first-party automation.

### CLI, MCP, and API

The official CLI handles issue-key resolution, authentication, multiline
Markdown, JSON responses, safe status transitions, attachments, time tracking,
knowledge, sessions, and bulk workflows.

```bash
paimos issue get PAI-83
paimos issue list --project PAI --status backlog --limit 20
paimos search "session expiry" --project PAI
paimos issue ensure-status PAI-83 done
paimos apply --from-file plan.yaml --dry-run
```

Use the [Agent Interface Guide](docs/AGENT_INTERFACE.md) for the CLI and MCP
surface, [Agent Integration](docs/AGENT_INTEGRATION.md) for workflow contracts,
and [the REST reference](docs/api-minimal.md) when direct HTTP is appropriate.

Two discovery endpoints keep clients from hard-coding local assumptions:

- `GET /api/openapi.json` describes the stable public scriptable contract.
- `GET /api/schema` describes enums, transitions, conventions, scopes, and
  resource shapes.

### Agent Intercom quickstart

Agent Intercom keeps messages and assignments durable in Paimos. An
operator-owned `paimos-agentd` runtime starts and controls local Codex or Claude
children. A canonical project agent is the reusable identity; a running worker
is one owned generation with an explicit workspace, profile and assignment.

First configure a named CLI instance and its reviewed macOS LaunchAgent or
Linux user service as described in the [Agent Intercom runbook](docs/AGENT_INTERCOM.md#local-runtime-setup-doctor-repair-and-reset).
Setup verifies that declaration and reconnects the selected runtime; it does
not install a service, rewrite Home Manager configuration, or choose credentials.

```bash
paimos --instance production runtime doctor --project PAI
paimos --instance production runtime setup --project PAI
```

Use the guided commands to choose authorized agents, immutable dispatch profiles
and parent sessions. Give each exclusive generation its own clean workspace:

```bash
paimos --instance production orchestrator start --guided \
  --project PAI --workspace /path/to/coordinator-workspace

paimos --instance production worker start --guided \
  --project PAI --ticket PAI-123 --work-shape ship \
  --workspace /path/to/worker-workspace
```

`--dry-run` or `--explain` resolves the same plan without starting a child.
Scripts can supply the choices explicitly with `--non-interactive`, `--json`
and a stable `--idempotency-key`. Exact retries preserve the original generation;
an unknown result needs reconciliation before any new start. Matching PAI-917
builds return the public session and matching status, message and
diagnosis commands, plus steer, interrupt and stop when the observed generation
advertises those capabilities. Choose `scout` for investigation work whose
result is evidence rather than an implementation.

`runtime doctor` reports independent readiness layers. `runtime repair` acts
only on the verified instance within a persistent retry budget. `runtime reset`
first produces an exact preview; its confirmation archives eligible owned state
while preserving credentials, workspaces and replay protection. Follow the
runbook's recovery instructions when ownership or a delivery outcome is unknown.

Browser lifecycle controls require an explicitly configured reporter and private
`--lifecycle-config` workspace/profile mappings on the daemon. Local paths stay
in that owner-only configuration; the browser receives opaque workspace handles
and optional operator labels. Runtime discovery, health and typed intent contracts
are documented in the [REST reference](docs/api-minimal.md#browser-to-agentd-lifecycle-intents-pai-924).
Only the daemon executes local effects. Private runtime, worker and consumer
proofs bind authority to exact generations. The per-generation worker lease is
kept out of argv and stored server-side only as a digest; public session UUIDs
and a shared API key are not worker proof.

For encrypted target setup, daemon consumer ownership, supported vendor
capabilities and manual low-level commands, use the
[Agent Intercom runbook](docs/AGENT_INTERCOM.md). Ambiguous issued handoffs are
quarantined rather than retried as if a vendor effect were known to be absent.

### Execution stays local and explicit

Paimos records an implementation request on the server, but it does not open a
remote shell into a developer workstation.

```text
issue action
    -> server-side run record
    -> developer starts `paimos run-agent watch`
    -> repo-scoped local runner claims one job
    -> Claude Code or Codex edits and tests locally
    -> status and value-free provenance return to Paimos
```

The watcher is operator-started, repo-scoped, single-job, and interactive by
default. Deployment is disabled unless the operator enables the runner, supplies
an explicit deploy command, and the run names a deploy target. Hosted and local
model draft providers produce notes without entering this execution path.

See [Implement-this providers](docs/IMPLEMENT_THIS_PROVIDERS.md) and the
[threat model](docs/THREAT_MODEL.md#48--remote-triggered-execution-pai-605) for
the exact boundary.

## Architecture

```text
Browser / CLI / MCP / REST client
               |
               v
        Go service on :8888
        |       |        |
        |       |        +-- optional OIDC, SMTP, and model providers
        |       +----------- optional S3-compatible attachment storage
        +------------------- SQLite in WAL mode
                              ($DATA_DIR is the backup boundary)
```

- One Go process serves the JSON API and the compiled Vue application.
- SQLite is the system of record. Tracked forward migrations run at startup;
  schema rollback requires restoring the matching database backup with the old
  image.
- There is no required external database, Redis, queue, analytics service, or
  Paimos SaaS dependency.
- S3-compatible storage is needed only for attachments. SMTP is needed only for
  password-reset email. OIDC and model providers are operator choices.
- Absent optional services disable their feature. Provider initialization
  failures are reported without preventing the core project system from
  starting.
- Runtime fonts are bundled, and the application ships without analytics,
  externally hosted runtime scripts, or product telemetry.

The complete developer view lives in the
[Developer Guide](docs/DEVELOPER_GUIDE.md). Operator settings and set-once
values live in [Configuration](docs/CONFIGURATION.md).

## Quick start

The following starts a local evaluation instance from source:

```bash
git clone https://github.com/inspr-at/paimos.git
cd paimos

docker build -t paimos:local .
docker run --rm \
  --name paimos \
  -p 8888:8888 \
  -e ADMIN_PASSWORD='replace-this-local-password' \
  -v paimos-data:/app/data \
  paimos:local
```

Open <http://localhost:8888> and sign in as `admin` with the password supplied
above. A fresh database seeds the admin only when `ADMIN_PASSWORD` is present.
Change that password immediately. The variable has no effect after the admin
exists and should not remain in a production environment.

> [!IMPORTANT]
> This command is for local evaluation. Before exposing Paimos to a network,
> put it behind HTTPS, enable secure cookies, move secrets out of shell history,
> protect and back up `$DATA_DIR`, configure off-host backups, and review the
> [Hardening Guide](docs/HARDENING.md).

### Install the CLI

Signed and notarized universal macOS builds, Linux archives, checksums, and
source-build instructions are documented in [Installing the Paimos CLI](docs/INSTALL.md).

```bash
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos

paimos auth login
paimos doctor
```

Authentication details are stored in a mode-`0600` configuration file; API keys
use the operating-system keyring when available. Headless environments can use
explicit environment configuration as documented in the install guide.

## Local development

Requirements:

- Go 1.25+
- Node.js 22+
- npm

Run the backend and frontend in separate terminals:

```bash
# terminal 1
cd backend
ADMIN_PASSWORD='local-development-only' \
  DATA_DIR=../data \
  STATIC_DIR=../frontend/dist \
  go run .

# terminal 2
cd frontend
npm ci
npm run dev
```

The API listens on <http://localhost:8888>. Vite listens on
<http://localhost:5173> and proxies `/api/*` to the backend.

For authenticated UI development, use the build-tagged workflow in
[Local development login](docs/DEV_LOGIN.md). That route is absent from
production binaries and checked by CI.

### Validation commands

```bash
cd backend && go test ./...
cd frontend && npm test -- --run
cd frontend && npm run typecheck && npm run build
```

## Trust and security

Paimos documents its trust boundary instead of treating self-hosting as a
security guarantee.

| Boundary         | Current behavior                                                                                                                                               |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Authentication   | Local password, TOTP, API keys, and one generic OIDC provider. OIDC uses authorization code with PKCE and requires a verified email.                           |
| Authorization    | Canonical roles, project-level view/edit grants, scoped API keys, and explicit super-admin capabilities. Permission changes invalidate affected live sessions. |
| Browser sessions | `HttpOnly`, `SameSite=Lax`, secure-cookie support, CSRF tokens, bounded sliding sessions, and shared rate limits on authentication endpoints.                  |
| Files            | Active browser content is rejected on upload; stored content is re-sniffed and unsafe types are forced to download.                                            |
| Audit            | Session-mutation audit is on by default. AI audit records contain metadata, not prompts, responses, API keys, or local environment values.                     |
| Data rights      | Configurable retention plus administrative per-subject export and erase primitives. Historical project evidence is anonymized rather than silently deleted.    |
| Secrets          | Operator-entered provider secrets use authenticated encryption at rest. Production operators should supply the master key separately from `$DATA_DIR`.         |
| Releases         | CI runs tests and security scanners, publishes checksums and CycloneDX SBOMs, signs images with GitHub OIDC, and attaches provenance attestations.             |

Read the maintained [Threat Model](docs/THREAT_MODEL.md),
[Hardening Guide](docs/HARDENING.md), [Security Review](docs/SECURITY_REVIEW.md),
and [Backup/Restore Guide](docs/BACKUP_RESTORE.md) before production use.

Report vulnerabilities privately to `security@paimos.com`. The supported-version
policy and disclosure process are in [SECURITY.md](SECURITY.md).

## Current maturity and limits

The boundaries below are part of the product description, not fine print:

- Paimos is a compact single-node Go and SQLite system. It does not provide
  multi-node high availability or automatic horizontal scaling.
- No published performance envelope exists yet. Test representative issue,
  attachment, user, and concurrency volumes before a production rollout.
- Security fixes are provided for the latest release only.
- The project has not completed an independent third-party security review.
- The evidence base currently includes one active maintainer-operated production
  deployment and one historical independent second-operator deployment. This is
  meaningful operational use, not broad market validation.
- One generic OIDC provider is supported. SAML is not. The current flow relies on
  the provider's TLS-protected userinfo response rather than locally validating
  the ID token through JWKS.
- Audit history is held locally. A host administrator with direct SQLite write
  access can alter it; remote append-only audit requires an external sink.
- Paimos does not encrypt the entire SQLite database. Operators remain
  responsible for encrypted storage, transport security, secret injection,
  backups, and restore exercises.
- The published container currently has no non-root `USER` declaration. Run it
  with an explicit runtime user and correctly owned storage when your deployment
  requires that boundary.
- Local execution requires an operator-started repository watcher or an
  explicitly configured agentd runtime. Browser lifecycle requests are limited
  to advertised workspaces, profiles and typed actions; they do not grant a
  general remote shell or autonomous deployment authority.
- Habitat and the runtime/lifecycle changes documented here have focused
  automated evidence and operator-owned local Codex and Claude handoff proofs
  with matching PAI-917 builds. Release `v26.09.07` is verified on the
  production server and matching configured Mac CLI/daemon, with full backend
  CI, signed-image supply-chain gates, a fresh stopped-volume backup, and
  scoped post-live CLI and browser-fixture checks. Runtime doctor still reports
  unverified targets, an unconfigured receiver, and an unavailable primary
  inbox; human-session runtime-health, clean-OS onboarding, named cross-machine
  handoffs, accessibility review, and UI draft-refresh checks remain separate
  open checks; the draft-refresh follow-up is tracked in
  [PAI-950](https://pm.barta.cm/projects/6/issues/PAI-950). The deployed static
  asset variant was verified from the running container. The public marketing
  site remains on the prior `26.09.06.21.31` provenance.

Production evidence and open gaps are maintained in
[Reference Deployments](docs/REFERENCE_DEPLOYMENTS.md), the
[Threat Model](docs/THREAT_MODEL.md), and the
[Claim/Evidence Matrix](docs/claim-matrix.md).

## Documentation map

| Need                        | Start here                                                                                                                                               |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operate the web application | [Configuration](docs/CONFIGURATION.md), [Hardening](docs/HARDENING.md), [Deploy/Rollback](docs/DEPLOY.md), [Backup/Restore](docs/BACKUP_RESTORE.md)      |
| Drive Paimos from an agent  | [Agent Intercom](docs/AGENT_INTERCOM.md), [Agent Interface](docs/AGENT_INTERFACE.md), [Agent Integration](docs/AGENT_INTEGRATION.md), [Implement-this Providers](docs/IMPLEMENT_THIS_PROVIDERS.md) |
| Integrate over HTTP         | [Minimal REST reference](docs/api-minimal.md), `GET /api/openapi.json`, `GET /api/schema`                                                                |
| Understand project context  | [Anchors](docs/ANCHORS.md), [Agent Integration](docs/AGENT_INTEGRATION.md#1a-reading-project-context-for-coding-agents)                                  |
| Review security posture     | [Security Policy](SECURITY.md), [Threat Model](docs/THREAT_MODEL.md), [Security Review](docs/SECURITY_REVIEW.md)                                         |
| Contribute code             | [Contributing](CONTRIBUTING.md), [Developer Guide](docs/DEVELOPER_GUIDE.md), [Agent Rules](+agents/rules/AGENTS.md)                                      |
| Follow releases             | [Changelog](docs/CHANGELOG.md), [Install Guide](docs/INSTALL.md)                                                                                         |

## Contributing

Issues and focused pull requests are welcome. Discuss new surface area before
building it, add regression coverage for bug fixes, and keep public API changes
aligned with `backend/handlers/openapi.json`.

Paimos uses the [Developer Certificate of Origin](DCO.md). Every commit must carry
a sign-off created with `git commit -s`. Read [CONTRIBUTING.md](CONTRIBUTING.md)
for the complete workflow.

## License

Copyright © 2026 [Markus Barta](https://github.com/markus-barta).

Paimos is licensed under
[GNU Affero General Public License v3.0 only](LICENSE), SPDX identifier
`AGPL-3.0-only`.

If you modify Paimos and let users interact with that modified version over a
network, AGPL section 13 requires offering those users the Corresponding Source
of the running modified version. Read the project license and the
[official GNU AGPL text](https://www.gnu.org/licenses/agpl-3.0.html) for the
complete terms.
