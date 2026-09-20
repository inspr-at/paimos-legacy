# PAIMOS — Configuration Reference

Every environment variable PAIMOS reads, grouped by concern. Defaults
shown in parentheses. Unless noted, all vars are optional.

## Runtime secret files

The secret-bearing variables `ADMIN_PASSWORD`, `PAIMOS_SECRET_KEY`,
`SMTP_PASS`, and `OIDC_CLIENT_SECRET` each accept a `_FILE` companion. For
example, setting `SMTP_PASS_FILE=/run/secrets/smtp-pass` makes PAIMOS read the
password from that file instead of `SMTP_PASS`. `_FILE` wins when both forms
are set, and the direct variable remains supported for compatibility.

PAIMOS removes exactly one trailing LF or CRLF line ending from file input and
preserves every other byte. A configured `_FILE` that is empty or unreadable
fails startup with an error naming the configuration variable; PAIMOS does not
fall back to the direct variable and does not include the path or secret in the
error. Mount runtime files read-only with the narrowest ownership and mode your
container or service manager supports. This keeps the secret value out of the
process environment and composes directly with Docker secrets, systemd
credentials, and secret-manager runtime files.

## Core server

| Var | Default | Notes |
|---|---|---|
| `PORT` | `8888` | Listen port |
| `STATIC_DIR` | `/app/static` | Path to the built Vue SPA |
| `DATA_DIR` | `/app/data` | Path for SQLite DB, branding JSON, logos, avatars |
| `ADMIN_PASSWORD` | *(empty)* | **First-run only.** Seeds the `admin` user on a fresh DB. No effect once `admin` exists. Supports `ADMIN_PASSWORD_FILE`. |
| `COOKIE_SECURE` | *(unset)* | Set to `true` on HTTPS deployments to add `Secure` to session cookies |
| `INSTANCE_LABEL` | *(empty)* | Shows a banner in the sidebar (e.g. `STAGING`) — useful on non-prod instances |
| `PAIMOS_PUBLIC_BASE_PATH` | *(empty)* | Optional native public URL prefix for a shared-origin reverse-proxy mount (e.g. `/paimos` or `/apps/paimos`). Empty keeps today's standalone origin-root. Canonical ASCII absolute path; segments `[A-Za-z0-9_-]+`; no trailing slash, dots, percent encoding, query, or fragment. Invalid values fail closed at startup. Same origin is a shared trust domain — cookie names avoid accidental collisions but are not app isolation. Cookie `Path=/` is not a security boundary. Production path choice is operator-owned. |

## Secret encryption

Paimos encrypts operator-entered provider secrets with domain-separated keys
derived from one 32-byte root key. Production deployments should supply that
root key from a secret manager rather than store it beside the database.

| Var | Default | Notes |
|---|---|---|
| `PAIMOS_SECRET_KEY` | auto-generated as `$DATA_DIR/.secret-key` when first needed | Base64 encoding of exactly 32 bytes. `PAIMOS_SECRET_KEY_FILE` takes precedence over the direct value and the generated `$DATA_DIR/.secret-key`. Do not replace an existing key directly; use `paimos secrets rotate` so stored ciphertext is re-encrypted atomically. |

## Branding

All are optional; defaults produce "PAIMOS" out of the box.

| Var | Default | Used for |
|---|---|---|
| `BRAND_PRODUCT_NAME` | `PAIMOS` | Startup log, email subject/body, default page title, TOTP issuer (unless overridden) |
| `BRAND_COMPANY_NAME` | *(empty)* | Appended to page title (`PAIMOS — ACME Corp`) and email footer when set |
| `BRAND_WEBSITE_URL` | `https://paimos.com` | Default `branding.json` website, email footer, password-reset URL fallback |
| `BRAND_PUBLIC_URL` | *(empty)* | Required for password-reset magic links. Falls back to `BRAND_WEBSITE_URL` if unset. No trailing slash. |
| `BRAND_EMAIL_FROM` | *(empty)* | `From:` header on outgoing emails. Falls back to `noreply@<host-of-BRAND_WEBSITE_URL>` when SMTP is configured but this is unset. |
| `BRAND_TOTP_ISSUER` | `BRAND_PRODUCT_NAME` | Shown by authenticator apps on TOTP enrollment |
| `BRAND_HEALTH_SERVICE_NAME` | lowercase `BRAND_PRODUCT_NAME` | `GET /api/health` service label. The response also reports the build version and non-secret remote deployment/Agent Intercom identity evidence used by `paimos doctor`. |
| `BRAND_PAGE_TITLE` | `BRAND_PRODUCT_NAME` [+ ` — ` + `BRAND_COMPANY_NAME`] | Shipped as the `pageTitle` in default branding |

## Live updates

PAIMOS can stream mutation metadata to the SPA over Server-Sent Events
so issue lists mark themselves stale without waiting for the polling
fallback. The stream only emits subject ids, project ids, mutation
types, user ids, and timestamps; it does not send before/after payloads
or undo snapshots.

| Var | Default | Notes |
|---|---|---|
| `PAIMOS_LIVE_UPDATES_ENABLED` | `false` | Set to `true` to enable `GET /api/changes?since=<seq>`. When disabled, the endpoint returns 404 and clients keep using conditional polling. |
| `PAIMOS_LIVE_UPDATES_MAX_CONNECTIONS` | `100` | Process-local cap for concurrent SSE clients. |

## Agent run reconciliation

The server reconciles active Implement-this runs at boot and on a timer. All
durations use Go duration syntax (`45s`, `15m`, `2h`). Invalid or non-positive
values fall back to the documented default.

| Var | Default | Notes |
|---|---|---|
| `PAIMOS_RUN_RECONCILE_INTERVAL` | `30s` | Active-run watchdog cadence. |
| `PAIMOS_RUN_QUEUED_TIMEOUT` | `15m` | Fail a run that no runner claimed. |
| `PAIMOS_RUN_FIRST_HEARTBEAT_TIMEOUT` | `1m` | Fail a newly supervised claim whose first server-received heartbeat never arrives. |
| `PAIMOS_RUN_HEARTBEAT_TIMEOUT` | `90s` | Fail a supervised run after its latest server-received heartbeat becomes stale. Semantic events do not reset this clock. |
| `PAIMOS_RUN_LEGACY_TIMEOUT` | `2h` | Longer fallback for old/uninstrumented running rows without the durable supervised-claim marker. |

## Instant agent bus

The message ledger and delivery coordinator belong to exactly one PAIMOS
instance. Set a distinct instance name on each deployment; never point `ppm`
and `pma` at the same target registry or dispatcher configuration.

| Var | Default | Notes |
|---|---|---|
| `PAIMOS_ENV` | *(empty / development)* | Set exactly `production` on every live deployment. This activates fail-closed deployment checks, including the agent-bus identity firewall. |
| `PAIMOS_DEPLOYMENT_INSTANCE` | *(empty)* | Server deployment ID used to prove that the bus identity matches the configured domain. Required and non-default when `PAIMOS_ENV=production`. This is separate from the CLI/MCP `PAIMOS_INSTANCE` configured-instance selector. |
| `PAIMOS_AGENT_BUS_INSTANCE` | `default` (development only) | Stable non-secret bus identity written into target, delivery, idempotency, and webhook records. Production rejects an empty/`default` ID and any mismatch with `PAIMOS_DEPLOYMENT_INSTANCE`; `ppm` must set `ppm` and `pma` must independently set `pma`. |
| `PAIMOS_AGENT_BUS_WEBHOOK_HOSTS` | *(empty / all denied)* | Comma-separated exact hostnames approved for operator-registered `https_webhook` targets. Amy's Grok Bot routine hostname must appear here before its target can be registered. |
| `PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS` | `false` | When `true`, permits loopback/private/link-local webhook addresses. Intended only for isolated local capture tests; keep false in production. HTTPS and the hostname allowlist still apply. |

For the later, separate `ppm` rollout, pin these exact values before registering
Amy's target (replace only the hostname placeholder):

```text
PAIMOS_ENV=production
PAIMOS_AGENT_BUS_INSTANCE=ppm
PAIMOS_DEPLOYMENT_INSTANCE=ppm
PAIMOS_AGENT_BUS_WEBHOOK_HOSTS=<Amy routine hostname>
PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS=false
```

The webhook capability and the routine sender key are not environment
variables. Register them via the closed admin API fields `target_ref` (the
POST URL) and `target_secret` (the raw sender key), or equivalently
`paimos message target set ... --target-ref-file <url-file> --target-key-file <key-file>`
(each flag also accepts `-` for stdin, but only one of them per invocation;
neither value is accepted as an argument, and a file must be a regular,
owner-only `0600` file owned by the caller — symlinks, hard links, and group-
or world-readable files are refused). Both come from the routine's "When
a webhook fires" trigger card in the Grok Bot desktop app. PAIMOS encrypts the
URL under the `agent-message-targets` secretvault domain and the key under the
separate `agent-message-target-secrets` domain using `PAIMOS_SECRET_KEY`;
list/status responses return neither, only `has_secret`. Dispatch uses
bounded DNS/connect/response timeouts, revalidates resolved addresses, disables
proxies and redirects, sends the stable `delivery_id` as `Idempotency-Key`, and
authenticates every wake with `Authorization: Bearer <sender key>`.

## Local agent runner flags

`paimos run-agent watch` is configured with CLI flags rather than server
environment variables. The safety-relevant defaults are:

| Flag | Default | Notes |
|---|---|---|
| `--execution-timeout` | `2h` | Hard lifetime for the owned provider process tree. |
| `--heartbeat-timeout` | `5m` | Maximum child stdout/stderr silence before termination. |
| `--heartbeat-interval` | `15s` | Supervisor-owned liveness cadence; independent of provider callbacks. |
| `--exec` | `claude` | Built-in Claude structured-stream mode with validated direct argv, `--safe-mode`, and repository read/edit tools only. Any custom value is an explicit raw shell-command fallback with neutral `paimos/custom-runner` telemetry identity. |
| `--claude-permission-mode` | `dontAsk` | One of the installed modes `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, or `plan`; stale/unknown values and shell controls are rejected. |
| `--claude-allowed-tools` | `Read,Glob,Grep,Edit,Write` | Passed to `--tools` as the hard built-in availability boundary and to `--allowedTools` for approval. Bash, browser, MCP, and inherited customizations are absent by default. |
| `--unsafe-allow-bypass-permissions` | `false` | Separate unsafe opt-in required for `bypassPermissions`; only the dual opt-in generates Claude's dangerous-skip acknowledgement argument. |
| `--test-exec` | *(empty)* | The only runner-owned test evidence source. Empty means successful implementation reports `completed`, not `tests_passed`. |
| `--attach-logs` | `false` | Opt-in bounded raw log capture; output can contain secrets. |
| `--allow-deploy` | `false` | Still requires `--deploy-exec`, a run `deploy_target`, and separate consent unless `--yes-deploy`. |

Provider, test, and enabled deploy commands all run through the same
process-group supervisor, execution/silence watchdogs, and heartbeat cadence.

The built-in Claude action does not enable Bash, MCP, browser tools, or
permission bypass. A custom `--exec` can broaden permissions, but that is an
operator-authored arbitrary shell command and its stream is not parsed into
telemetry.

### Set-once (changing after data exists has consequences)

| Var | Default | Caveat |
|---|---|---|
| `BRAND_API_KEY_PREFIX` | `paimos_` | Changing after keys are issued orphans the old keys — the prefix is stored verbatim and matched on auth |
| `BRAND_DB_FILENAME` | `paimos.db` | Change only on an empty `DATA_DIR`. No auto-migration. |
| `BRAND_MINIO_BUCKET` | `paimos-attachments` | Change before uploads begin. Existing objects won't follow. |

## Email (SMTP — optional)

PAIMOS only sends password-reset emails when `SMTP_HOST` is set. With
SMTP unconfigured the reset endpoint refuses to send and logs a
misconfiguration warning — your users will see "If an account with
that email exists, a reset link has been sent" but no link will reach
them. To run a true local-dev flow without an SMTP server, also set
`PAIMOS_DEV_MODE=true`; this prints the reset link to stdout so a
developer can paste it into the browser. Never set `PAIMOS_DEV_MODE`
in shared staging or production — the link is a one-shot password
reset and anyone with log access can use it (PAI-115).

| Var | Default | Notes |
|---|---|---|
| `SMTP_HOST` | *(unset)* | Unset = no email sent. Set to enable real sending. |
| `SMTP_PORT` | `587` | STARTTLS submission port |
| `SMTP_USER` | *(empty)* | Leave blank for unauthenticated relay |
| `SMTP_PASS` | *(empty)* | Pair with `SMTP_USER`. Supports `SMTP_PASS_FILE`. |
| `PAIMOS_DEV_MODE` | *(unset)* | When `true` AND `SMTP_HOST` unset, log reset links to stdout. Local dev only. |

## Single Sign-On (OpenID Connect — PAI-120 / PAI-680)

PAIMOS supports a single OIDC provider end-to-end with authorization code +
PKCE. The flow is hidden from the login page until all three required vars
are set; once configured, the SPA renders an "SSO" button alongside the
password form unless password login is disabled below. SSO answers identity
only; PAIMOS roles and per-project permissions remain local authorization.

### Instance password-login policy (PAI-1044)

`AUTH_PASSWORD_LOGIN=enabled|disabled` defaults to `enabled` when unset or
empty. Values are case-insensitive and trimmed; any other value refuses
startup. The default preserves username + password + TOTP, password recovery,
and the `?method=password` fallback on SSO-routed domains.

Set `AUTH_PASSWORD_LOGIN=disabled` to require OIDC for new interactive logins.
This rejects password login for **every account**, including `admin` and
`super_admin`, rejects already-issued password TOTP challenges, and disables
forgot-password token issuance, token validation and redemption. The login
page hides password recovery and cannot override the policy with
`?method=password`; direct API requests are independently rejected by the
handlers. Production binaries still contain no dev-login route, and even a
development-tagged handler refuses login under this policy.
Authenticated change-password and TOTP-disable requests are also refused before
checking a local password. Successful OIDC sign-in clears any local
`must_change_password` requirement, including for previously invited users;
new SSO users are created without that requirement.

Disabled password login requires valid OIDC configuration at startup:
`OIDC_ISSUER_URL`, `OIDC_CLIENT_ID` and `OIDC_REDIRECT_URL` must be set, with
the usual redirect and provisioning validation. The issuer must use HTTPS,
except for loopback hosts (`localhost` or a loopback IP address).
After database migration and admin seeding, startup also refuses to continue
unless there is an active user with a usable email for OIDC matching or an
active general API key belonging to an active user without a pending password
rotation. Expired, disabled, and dedicated service keys do not count. An
email-less seeded admin alone is insufficient, even with auto-create enabled:
provision a matching user or recovery key with password login enabled before
cutover. These checks only inspect configuration and local database state;
they do not contact the IdP or prove the provider will accept a login, so an
IdP outage does not prevent startup when these prerequisites are present.

**The API key is the break-glass credential.** Before disabling password login,
verify an appropriately authorized API key held by the `paimos` CLI's keyring.
API-key authentication is independent of both this switch and OIDC. During an
IdP outage there is intentionally **no new web login**; recovery access is
CLI-only. The operator gives up the local-admin browser fallback. Existing
sessions are not revoked by this switch; use the existing session-revocation
controls when a cutover also requires ending previously authenticated access.
Re-enabling the setting and restarting restores the local login feature.

### OIDC provider settings

| Var | Default | Notes |
|---|---|---|
| `OIDC_ISSUER_URL` | *(unset)* | Required. e.g. `https://login.example.com` (no trailing slash). The discovery doc must be reachable at `${OIDC_ISSUER_URL}/.well-known/openid-configuration`. |
| `OIDC_CLIENT_ID` | *(unset)* | Required. |
| `OIDC_CLIENT_SECRET` | *(unset)* | Optional for public clients (PKCE-only); required for confidential clients. Supports `OIDC_CLIENT_SECRET_FILE`. |
| `OIDC_REDIRECT_URL` | *(unset)* | Required. Must exactly match the IdP-registered redirect **including** `PAIMOS_PUBLIC_BASE_PATH` when set (standalone: `https://paimos.example.com/api/auth/oidc/callback`; prefix `/paimos`: `https://paimos.example.com/paimos/api/auth/oidc/callback`). |
| `OIDC_SCOPES` | `openid email profile` | Space-separated. |
| `OIDC_PROMPT` | *(unset)* | Optional space-separated OIDC prompt values forwarded to the authorization endpoint. Unset preserves the IdP's normal session-reuse behavior; `select_account` asks compatible providers to show an account chooser. Provider extensions are allowed. |
| `OIDC_BUTTON_LABEL` | `Sign in with SSO` | Shown on the login page. |
| `OIDC_POST_LOGIN_REDIRECT` | `/` | SPA path to land on after a successful SSO login. |
| `OIDC_SSO_DOMAINS` | *(unset)* | PAI-743 home realm discovery: comma-separated email domains served by this IdP (`agm.ng, example.com`; a leading `@` is tolerated). On the identifier-first login, an address in one of these domains is offered SSO **only** — the password field is hidden. Unset means no domain routing: every identifier is offered password + SSO when password login is enabled. |
| `OIDC_PROVISION_MODE` | `invite-only` | `invite-only` matches only existing active users by verified email. `auto-create` creates missing users. |
| `OIDC_AUTO_CREATE_ROLE` | `member` | Used only when `OIDC_PROVISION_MODE=auto-create`. Allowed: `member`, `external`. |

Provisioning rules:
- A returning user is matched by case-insensitive email.
- By default, an unknown email is refused and the user lands on
  `/login?sso_error=invite_required`. Create the PAIMOS user first, with
  the same email, to invite them.
- With `OIDC_PROVISION_MODE=auto-create`, a new user is created with
  status `active`, no local password, the configured default role, and a
  username derived from `preferred_username` or the email local-part.
- An OIDC user with no `email_verified: true` claim is refused. Operators
  who run IdPs that omit `email_verified` should set the claim to `true`
  on the IdP side or the redirect lands on
  `/login?sso_error=email_required`.
- For Zitadel, configure an authorization-code + PKCE application, add
  the exact `OIDC_REDIRECT_URL` as an allowed redirect URI, and make sure
  the `email` and `email_verified` claims are present in userinfo. PAIMOS
  works well with a public PKCE client (`OIDC_CLIENT_SECRET` unset);
  confidential clients are supported when the IdP accepts
  `client_secret_post` at the token endpoint.

The id_token signature is not verified locally; trust comes from the
TLS-protected userinfo round trip back to the issuer. This trade-off
keeps the dependency surface small. JWKS-based id_token verification
is a follow-on if a future deployment requires it.

## Sessions (PAI-322 / PAI-321 / PAI-320)

Session lifetime is **not** env-configurable today — the values live as
constants in `backend/auth/auth.go`. They're called out here so
operators know where to look if they need to fork the defaults:

| Constant                  | Value | Meaning                                                                                       |
| ------------------------- | ----- | --------------------------------------------------------------------------------------------- |
| `sessionDuration`         | 30d   | Sliding window. Every authenticated request that's at least half-expired pushes `expires_at`. |
| `sessionAbsoluteLifetime` | 90d   | Hard ceiling measured from `sessions.created_at` (M89). Forces re-login regardless of slide.  |
| `sessionRenewThreshold`   | 15d   | "Don't `UPDATE` on every request" floor — only renew when below this remaining-time mark.     |

Cookie `Expires` is set to `sessionAbsoluteLifetime` so browser
state doesn't outlive what the server will accept.

Browser cookie names are `paimos_session`, `paimos_csrf_token`, and
`paimos_oidc_state` / `paimos_oidc_pkce` / `paimos_oidc_nonce` /
`paimos_oidc_return`. Standalone (empty `PAIMOS_PUBLIC_BASE_PATH`)
also dual-writes and dual-reads the legacy names `session`,
`csrf_token`, and `oidc_*` so existing browsers keep working. A
shared-origin mount writes and reads **only** the `paimos_*` names,
including on logout, so PAIMOS does not clobber another app's generic
cookies. `__Host-` cookies, if used later, stay `Secure`, `Path=/`,
and without `Domain`.

Two response headers expose session state to clients (PAI-320 /
PAI-322):

- `X-Session-Expires-At` — RFC3339; the SPA renders an expiry modal
  before this value passes.
- `X-Permissions-Epoch` — bumped on role / membership / status
  change; mismatch invalidates capability decisions.

`POST /auth/password` invalidates all *other* sessions for the user
and clears the `users.must_change_password` (M91) flag on success.

## Audit & retention (PAI-116 / PAI-117)

The session-mutation audit is on by default for NIS2 readiness. Set
`PAIMOS_AUDIT_SESSIONS=false` (or `0`) to opt out — primarily useful in
sandbox or local-dev runs where the noise is unwanted. The retention
sweeper runs every 24 hours and trims rows older than the configured
window for each class. Tune any variable below; defaults are the
"careful operator" baseline, not regulator maxima.

| Var | Default | Notes |
|---|---|---|
| `PAIMOS_AUDIT_SESSIONS` | `true` | Set `false`/`0` to disable the session-mutation audit middleware. |
| `PAIMOS_RETENTION_DAYS_SESSIONS` | `30` | Sessions are also auto-expired by their own `expires_at`; this is the cleanup floor. |
| `PAIMOS_RETENTION_DAYS_RESET_TOKENS` | `7` | Password-reset tokens are single-use; this caps the audit trail. |
| `PAIMOS_RETENTION_DAYS_ACCESS_AUDIT` | `365` | Project membership-change audit log. |
| `PAIMOS_RETENTION_DAYS_SESSION_ACTIVITY` | `90` | Per-mutation session activity rows. |
| `PAIMOS_RETENTION_DAYS_INCIDENT_CLOSED` | `730` | Closed incidents only — open/investigating/resolved are kept until closed. |
| `PAIMOS_RETENTION_DAYS_AI_CALLS` | `365` | AI paper-trail metadata rows (`ai_calls`). |
| `PAIMOS_RETENTION_DAYS_MUTATION_LOG` | `90` | Undo / redo activity log rows (`mutation_log`). |
| `PAIMOS_RETENTION_DAYS_TOTP_PENDING_MIN` | `60` | Pending TOTP tokens; minutes, not days. |

Per-subject GDPR endpoints (admin only):

- `GET  /api/users/{id}/gdpr-export` — JSON dump of every row referencing the user.
- `POST /api/users/{id}/gdpr-erase`  — replaces PII with placeholders, drops sessions/keys, sets `status='deleted'`.
- `GET  /api/gdpr/retention`         — current retention policy (introspection).

## Undo (PAI-209)

Undo uses two separate controls:

- `undo_stack_depth` in the database
  - edited at runtime under `Settings -> Admin -> System`
  - bounds `1..20`
  - default `3`
  - controls how many recent actions remain actively undoable per user
- `PAIMOS_RETENTION_DAYS_MUTATION_LOG`
  - env var, default `90`
  - controls how long `mutation_log` audit rows remain on disk

These are intentionally different knobs:

- stack depth affects the active undo/redo working set
- retention affects long-lived audit visibility

GDPR erase extends to the undo audit:

- `mutation_log.user_id` is nulled for the erased user
- `mutation_log.session_id` is cleared
- known display-name fields inside stored snapshots are scrubbed

See [`UNDO_SPEC.md`](UNDO_SPEC.md) for the conflict-resolution contract and UX flow.

## AI assist (PAI-146 / PAI-159 → PAI-183)

The AI assist feature exposes a multi-action menu next to multiline
text fields and on issue-level surfaces (issue header, side panel).
**Off by default.** Configuration is in the database — admins set it
under **Settings → AI** and **Settings → AI prompts**, not via env
vars — so this section is reference, not tuning.

### Provider + model

`ai_settings` (M74, singleton row) holds:

- `enabled`, `provider` (`openrouter` or `local_model`), `model`
  (provider model slug — e.g. `anthropic/claude-sonnet-4.5`),
  `base_url` for OpenAI-compatible local endpoints, `api_key` when the
  provider needs one, and `optimize_instruction` (admin-editable
  preface to the Optimize action's wrapper).

Set them from **Settings → AI**:
- **Test connection** runs a fixed-prompt smoke test against the
  unsaved form values, falling back to the saved key when the field
  is blank — admins don't have to re-paste the key just to verify.
  Audited under a separate `audit: ai_test ...` line.
- The **model picker** is fed live by `GET /api/ai/models`
  (server-cached 1h) showing top 4 models in six categories: Frontier,
  Value, Fastest, Cheapest, Open-weights, Free. Frontier picks are
  vendor-diverse (one model from each of Anthropic / OpenAI / xAI /
  Google). Manual model-id input stays always-visible.

### Execution controls

The AI control-plane path resolves user choices before a request starts:

- **Profile/model**: Fast, Balanced, Deep, or an admin/project default,
  resolved to the configured provider/model.
- **Effort**: low, standard, or deep where the provider/action supports it.
- **Prompt preset**: Default or a project knowledge entry explicitly marked as
  an AI prompt preset. Prompt bodies are not returned in options or activity
  payloads.
- **Context pack**: issue-only, project knowledge, retrieved context, or
  repo-aware context where project anchors exist. Responses include safe source
  metadata and truncation flags, not raw context bodies.

The same metadata is stored for AI action audit rows and Implement-this runs so
activity views can explain what ran without exposing prompts, API keys, model
responses, or local environment values.

Project settings add a project-level AI defaults section. A project can define
global defaults for profile, effort, prompt preset, context pack, and preferred
provider class; advanced JSON scopes can override those defaults per action,
run action, or project agent. The backend accepts IDs and safe refs only, for
example `default` or `kb:runbook:release_checklist`, and rejects values that
look like secrets.

Project AI policy can disable hosted draft providers and/or local-model draft
providers for that project. Disabled providers remain visible in the catalog as
unavailable with a policy reason, so users can see why Draft is blocked.

`GET /api/ai/execution-options` is the shared catalog for these controls. It
returns profiles, effort choices, prompt presets, safe PPM knowledge
suggestions, context packs, draft providers, and `selector_defaults` for action
menus, issue rows, and the issue AI Workbench. Selector defaults, project
policy, and knowledge suggestions are IDs/labels/status/revision only; user
selector changes stay local unless saved as project defaults.

### Actions

Each action is registered in code and surfaced via the
`POST /api/ai/action` dispatcher.

Built-in actions (13):
- `optimize`, `optimize_customer` — rewrite the field
- `suggest_enhancement` (sub-actions: security, performance, ux, dx,
  flow, risks)
- `spec_out` — description → AC checklist
- `find_parent` — top-3 plausible parents from the project tree
- `translate` (sub-actions: de_en, en_de)
- `generate_subtasks` — propose 3–7 child issues
- `estimate_effort` — hours + LP + reasoning
- `detect_duplicates` — top-5 similar issues in the project
- `ui_generation` — markdown UI spec
- `tone_check` — de-sales rewrite (customer surface)
- `customer_rewrite` (PAI-418, sub-actions: release_note, feature, fix,
  stability, security_hardening) — warm Apple-Notes-Stil German
  release-note copy for the customer-facing `report_summary` field
- `exec_summary` (PAI-418) — technical TL;DR for executive readers,
  same `report_summary` target field

### Placement (PAI-181)

Each action carries a `placement` field — `text`, `issue`, or
`both`:
- **text** — inline next to text fields (textareas)
- **issue** — in issue-level menus only (issue header, side-panel
  header, edit-mode toolbar)
- **both** — everywhere

Defaults: `optimize`, `suggest_enhancement`, `spec_out`, `translate`,
`ui_generation`, `tone_check`, `optimize_customer`, `customer_rewrite`,
`exec_summary` → text.
`find_parent`, `generate_subtasks`, `estimate_effort`,
`detect_duplicates` → issue.

Admins override per-row in **Settings → AI prompts → Edit**.

### Prompt CRUD (PAI-175 → PAI-177)

`ai_prompts` (M78, with `placement` added in M79) is the admin-edited
prompt store. Built-in rows are seeded lazily from the action
registry on first list call, so admins see the actual default in the
editor (not an empty textarea). Action handlers read the live row at
request time via `resolveActionPrompt(key)` with constant-default
fallback — admin edits actually take effect.

Endpoints (admin-only, CSRF-protected):
- `GET /api/ai/prompts` — list
- `POST /api/ai/prompts` — create custom
- `PUT /api/ai/prompts/{id}` — update (built-in: prompt + enabled +
  placement; custom: all editable fields)
- `DELETE /api/ai/prompts/{id}` — delete (custom only)
- `POST /api/ai/prompts/{id}/reset` — reset built-in to current
  code default
- `POST /api/ai/prompts/{id}/dry-run` — render the template against
  a real issue and call the LLM; returns rendered prompt + response
  side-by-side. NO state changes.

Templates use Go `text/template` syntax. Surface-specific variables:
- Issue: `Title`, `Description`, `AcceptanceCriteria`, `Notes`,
  `Type`, `Status`, `IssueKey`, `ProjectName`, `ParentEpic`
- Customer: `CustomerName`, `Industry`, `Notes`, `CooperationType`,
  `SLADetails`, `CooperationNotes`

### Usage cap (PAI-161)

`ai_usage` (M77) tracks per-user per-day token spend. Default cap is
**100 000 tokens / user / day**, configurable via the env var
`PAIMOS_AI_DAILY_CAP_TOKENS`. Per-user override goes to
`users.ai_cap_override_tokens` (nullable INT; null = use default,
0 = disabled, positive = raised cap). Admins are exempt from the
soft block but get an `X-AI-Over-Cap: true` response header for UI
warning. Settings → AI surfaces the org-wide totals + per-user
table.

### Voice provider + session budget (PAI-703 … PAI-808)

Voice input/output for the intake workbench and Agent Mode is configured at
Settings → Integrations → AI (admin; `PUT /api/ai/voice-settings`):
`provider` (`""` = off, `elevenlabs`), `api_key` (write-only — reads
report `has_api_key` only), `base_url`, `stt_model`, `tts_model`, and
`tts_voice_id`. The base URL may be blank (standard endpoint) or the HTTPS
root of `api.elevenlabs.io` / `api.eu.residency.elevenlabs.io`; credentials,
ports, paths, queries, fragments, and other hosts are rejected before the
key is stored or loaded. Intake audio is transcribed and dropped; Agent Mode
audio and transcripts are wholly ephemeral. TTS returns bytes with
`Cache-Control: no-store`, and Agent Mode TTS accepts server-owned templates
only—never arbitrary caller text.

Each intake session also has an LLM token budget so a runaway dictation
can't spend unbounded: `app_settings` key
`intake_session_token_budget` (Settings → System; default `60000`,
bounds 1 000–500 000). When exhausted, capture keeps working and the
spec freezes with an explanatory stage message.

### Voice cost gates (PAI-724)

The Intake voice endpoints (`.../audio` STT, `.../tts`) and Agent Mode voice
endpoints (`.../voice/transcribe`, `.../voice/speak`) call paid provider APIs
and run shared modality gates before every provider call:
per-user concurrency (2 in flight), per-minute burst caps (20 STT /
10 TTS), the PAI-161 usage cap above, and per-user daily unit
budgets, all answering `429` + `Retry-After` when exceeded. The daily
budgets are soft caps (admins pass) summed from today's `ai_calls` rows across
both surfaces—`intake_stt` + `agent_mode_stt` in estimated audio seconds and
`intake_tts` + `agent_mode_tts` in characters. Concurrent admitted calls hold
pending-unit reservations until their metadata row is recorded, so alternating
or racing the two surfaces cannot double-spend the same allowance:

| Env var | Default | Meaning |
| --- | --- | --- |
| `PAIMOS_VOICE_STT_DAILY_SECONDS` | `7200` | Per-user daily speech-to-text budget in estimated audio seconds (2 audio-hours). Non-positive / non-numeric values fall back to the default. |
| `PAIMOS_VOICE_TTS_DAILY_CHARS` | `60000` | Per-user daily text-to-speech budget in characters (~30 full-length spoken summaries). Same fallback rule. |

Successful voice calls record their units in `prompt_tokens` and an
estimated `cost_micro_usd` on the paper trail (Scribe ≈ $0.40/audio-
hour, multilingual TTS ≈ $0.10/1k chars), so voice spend shows up in
the cost totals instead of as zero. Provider attempts are recorded with a
short cancellation-independent context after the provider returns; request
disconnects cannot erase billed metadata. No audio, transcript, template
text, note body, or candidate identity is copied into `ai_calls`.

### Paper trail (`PAI-207` / `PAI-208`)

`ai_calls` (M81) stores one metadata row per AI attempt:

- `request_id`, `user_id`
- `action_key`, `sub_action`, `surface`
- optional subject ids (`issue_id`, `project_id`, `customer_id`, `cooperation_id`)
- provider / model
- prompt, completion, and total tokens
- `cost_micro_usd`
- outcome / error class
- latency

Endpoints:

- `GET /api/ai/calls` — admin paper trail
- `GET /api/ai/calls/{id}` — admin single-row detail
- `GET /api/ai/calls/export.csv` — admin CSV export
- `GET /api/ai/calls/me` — self-scope activity
- `GET /api/ai/calls/me/export.csv` — self-scope CSV export
- `GET /api/issues/{id}/ai-calls` — raw issue-scoped call feed
- `GET /api/issues/{id}/ai-activity` — issue-sidebar AI activity trail

Retention and GDPR:

- `PAIMOS_RETENTION_DAYS_AI_CALLS` controls pruning, default `365`
- GDPR erase nulls `user_id` on `ai_calls` rows, preserving operational cost history without retaining identity
- prompt and response bodies are not stored in `ai_calls`

### Audit shape

One stdout audit line per call:

```
audit: ai_action request_id=018fd... action=optimize sub_action= user_id=42
       field=description issue_id=123
       model="anthropic/claude-sonnet-4.5" outcome=ok
       latency_ms=850 prompt_tokens=100 completion_tokens=50
```

Test-connection pings emit a separate `audit: ai_test ...` line
(fewer fields). The audit prefix moved from `ai_optimize` to
`ai_action action=<key>` in v1.10.0 — operators with grep patterns
on `ai_optimize` need a one-line update.

Outcome is a closed enum (one bucket per exit path):

- `ok` — provider returned a result (token counts populated)
- `fail_timeout` — handler-imposed deadline fired before the provider
  responded (raise the cap or pick a faster model)
- `fail_upstream` — provider replied with 4xx / 5xx or a structurally
  invalid body (transient: retry, or check provider status)
- `denied` — caller cannot view the target issue
- `unconfigured` — feature toggle off or settings incomplete
- `bad_request` — body decode failed, action not registered, field
  not in the allow-list, text empty / too large, or daily cap hit
- `provider_missing` — configured provider name not registered
- `cfg_load_fail` — settings row failed to load (DB error)
- `ctx_fail` — issue-context lookup failed (DB error, not access)
- `unauth` — unauthenticated (defensive; the route is auth-gated so
  this is unreachable in practice)

Every exit path emits exactly one line, so the line count equals
the attempt count regardless of outcome. Test-connection has its
own outcomes (`test_ok`, `test_fail`).

### What is NOT logged

**Prompt and response bodies are NEVER logged.** The audit line
carries metadata only. PAI-146 / PAI-153 explicitly forbid body
logging; a regression test in
`backend/handlers/ai_optimize_audit_test.go` (renamed to cover
auditAction) enforces this and will fail CI if a future refactor
reintroduces body text into the line.

Provider-rejection responses (e.g. "model not found", "rate
limited") are logged separately at the call site, also without
bodies. Admins see the upstream message in the SPA banner;
operators see the full chain in `docker compose logs paimos`.

### Operational guidance

- Provider API keys are encrypted at rest in SQLite when saved through
  Settings. Legacy plaintext rows are still readable as a migration
  fallback until the next save clears them. Keep the data volume on
  encrypted storage if your threat model requires defense in depth.
- `local_model` uses `base_url` for an OpenAI-compatible endpoint
  such as an Ollama, LM Studio, llama.cpp, or internal gateway URL.
  The UI and draft-provider capability payload strip userinfo, query
  strings, and fragments from displayed endpoint labels.
- Token cost is on the operator's OpenRouter account. The optimize
  endpoint caps input at 32 KiB and output at ~3000 tokens per call;
  per-user spend is bounded by `PAIMOS_AI_DAILY_CAP_TOKENS`.
- "Test connection" calls don't go through the per-user cap (admin
  smoke tests should always work) but they do count against
  OpenRouter billing.
- Frontier picks pull from an undocumented OpenRouter frontend
  endpoint (`/api/frontend/models/find?order=top-weekly`); it can
  break. The picker has a static-fallback list for cold-start
  resilience and serves the last-known-good snapshot when the
  upstream call fails (with a `stale: true` flag in the response).

## Knowledge plane (PAI-326 → PAI-354)

The knowledge plane (project agents + memory / runbooks / external-systems
/ related-projects / guidelines + propose verb + adapter discovery) is on
by default. The knobs below tune the propose verb's rate limits and the
external-adapter discovery path; nothing here gates the read-side or the
inline-knowledge UI.

| Var | Default | Notes |
|---|---|---|
| `PAIMOS_PROPOSE_LIMIT_PER_SESSION` | `5` | Per-`(agent, session)` cap on `paimos memory propose` calls before the verb returns 429. Non-positive / non-numeric values fall back to the default (a `0` here would be a foot-gun). |
| `PAIMOS_PROPOSE_DISABLED` | *(unset)* | Operator opt-out. Set to `1`, `true`, `yes`, or `on` to make the propose path return 503 instance-wide. Useful when an agent goes rogue or for read-only release windows. |
| `PAIMOS_PROPOSE_STALE_DAYS` | `30` | Threshold for `GET /api/projects/:id/memory/proposed/stale` — proposed entries untouched this long surface in the admin "stale proposed" view. Per-request `?days=N` wins over the env value. |
| `PAIMOS_ADAPTER_PATH` | *(unset)* | Colon-separated list of directories `paimos skill render` walks to discover external adapters (PAI-332). Mirrors `$PATH` semantics — empty entries are skipped; unreadable directories log a warning but don't fail discovery. In-tree adapters always register first; env-discovered adapters can override them. |

## Attachments (MinIO / S3 — optional)

When `MINIO_ENDPOINT` is unset, the attachments feature is disabled:
upload UI is hidden, and download endpoints return 503. This is safe
for installations that don't need file uploads.

| Var | Default | Notes |
|---|---|---|
| `MINIO_ENDPOINT` | *(unset)* | Hostname:port, e.g. `minio.internal:9000` |
| `MINIO_ACCESS_KEY` | *(empty)* | Required when endpoint is set |
| `MINIO_SECRET_KEY` | *(empty)* | Required when endpoint is set |
| `MINIO_USE_SSL` | `false` | Set `true` for HTTPS endpoints |
| `MINIO_BUCKET` | `paimos-attachments` (from `BRAND_MINIO_BUCKET`) | Bucket name; created on first boot if missing |

## Example minimal `.env` (prod)

The `/run/secrets/...` examples below assume the deployment mounts those files
at the same paths inside the PAIMOS container. The repository Compose file
forwards the four supported `_FILE` variables; production deployments must
also provide the read-only mounts.

```env
# Core
PORT=8888
DATA_DIR=/app/data
COOKIE_SECURE=true

# Secret encryption — mount from a secret manager
PAIMOS_SECRET_KEY_FILE=/run/secrets/paimos-secret-key

# Branding
BRAND_PRODUCT_NAME=ACME PM
BRAND_COMPANY_NAME=ACME Corp
BRAND_WEBSITE_URL=https://pm.acme.example
BRAND_PUBLIC_URL=https://pm.acme.example
BRAND_EMAIL_FROM=noreply@acme.example

# Email
SMTP_HOST=smtp.postmarkapp.com
SMTP_PORT=587
SMTP_USER=<postmark-token>
SMTP_PASS_FILE=/run/secrets/smtp-pass

# Attachments
MINIO_ENDPOINT=minio.internal:9000
MINIO_ACCESS_KEY=<key>
MINIO_SECRET_KEY=<secret>
MINIO_USE_SSL=false
```

Bootstrap on first run:

```bash
ADMIN_PASSWORD_FILE=/run/secrets/paimos-admin-password docker compose up -d
```

Rotate that temp password via the UI, remove `ADMIN_PASSWORD_FILE` (or
`ADMIN_PASSWORD` when using the compatibility form), and restart.

## Runtime branding

The **preferred** way to brand a PAIMOS install is the admin UI at
**Settings → Visual → Workspace Branding** — edit product name, tagline, URLs, page title,
the full colour palette, and upload custom logo + favicon without a
restart or redeploy. Changes apply live the moment you hit Save.

Behind the UI, branding lives in `$DATA_DIR/branding.json`; uploaded
assets live in `$DATA_DIR/branding-assets/` and are served from
`/brand/<filename>` (public — the login page needs the logo pre-auth).
The JSON file is human-readable; ops who prefer git-versioned branding
can edit it directly and it will be picked up on next request.

The `BRAND_*` env vars (`BRAND_PRODUCT_NAME`, `BRAND_WEBSITE_URL`, …)
remain as the **pre-UI fallback**: they generate a default
`branding.json` on first boot and still drive server-side identity
that the UI can't edit (email `From:` header, API-key prefix, TOTP
issuer, health-check service name).

Additional `branding-<name>.json` files in `$DATA_DIR/` can be selected
at runtime via `?file=branding-<name>.json` on `GET /api/branding`
— useful for multi-tenant white-labeling. The admin UI writes to
whichever file is currently selected in the viewer's localStorage, so
edit under the brand you want to change.

### `branding.json` shape

```json
{
  "name": "PAIMOS",
  "company": "",
  "product": "PAIMOS",
  "tagline": "Your Professional & Personal AI Project OS",
  "website": "https://paimos.com",
  "logo": "/logo.svg",
  "favicon": "/favicon.svg",
  "backgroundPattern": "triangle",
  "colors": {
    "primary": "#52525b",
    "primaryDark": "#3f3f46",
    "primaryLight": "#a1a1aa",
    "primaryPale": "#f4f4f5",
    "accent": "#16a34a",
    "sidebarBg": "#18181b",
    "sidebarText": "#e4e4e7",
    "loginBg": "#18181b",
    "loginPattern": "#27272a"
  },
  "pageTitle": "PAIMOS",
  "contractor": [
    "Acme GmbH",
    "Musterweg 1, 8010 Graz, Austria",
    "UID: ATU00000000, FN: 000000x",
    "office@acme.example"
  ]
}
```

`backgroundPattern` controls the shared login and sidebar texture. Supported
values are `triangle` (the default), `square`, `hex`, `lines`, and `none`.
Admins can preview and change it under **Settings → Branding**.

`contractor` (PAI-686) is the legal-identity block printed as the
"Auftragnehmer" party on report PDFs, one line per array entry (max 10).
When unset, reports fall back to the branding `company`/`name` (then the
`BRAND_*` defaults) — there is no baked-in operator identity. Note that
`GET /api/branding` is public, so keep this to imprint-grade data.

### Asset endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /api/branding` | public | Current branding JSON (login page reads this pre-auth) |
| `PUT /api/branding` | admin | Write `branding.json` (accepts `?file=branding-<slug>.json`) |
| `POST /api/branding/logo` | admin | Multipart `file` field, SVG/PNG/JPEG, ≤ 1 MB |
| `POST /api/branding/favicon` | admin | Multipart `file` field, SVG/PNG/ICO, ≤ 256 KB |
| `GET /brand/<filename>` | public | Serves uploaded assets with SVG-safe CSP |

Default `/logo.svg` and `/favicon.svg` resolve against the bundled
static assets — they're only used when no uploaded branding asset has
taken over the `logo` / `favicon` JSON fields.
