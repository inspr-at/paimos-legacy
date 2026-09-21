# Agent Intercom: Architecture and Operator Runbook

Agent Intercom is Paimos's durable, project-scoped path for agent messages and
owned local controls. The Paimos ledger is the source of truth; local delivery
workers and `paimos-agentd` apply a message to a receiver only after leasing
that durable work.

The base owned-session commands first appeared in 5.21.0. Historical scoped
controls, generation worker leases, durable reporting, control-outcome reads,
and the M168 database guards require 26.09.01 or later; they are not available
as documented here in 5.21.0 or 26.08.31.

The guided start, runtime-management, and Habitat lifecycle workflows in this
guide are available in 26.09.07 and require matching Paimos server, CLI, and
daemon builds. Named account choices (`accounts` / `schema_version: 2`, and
optional intent `account_key`) are an unreleased source extension in this tree;
they are not part of the released 26.09.07 daemon, server, or CLI. A 26.09.07
daemon decoder uses `DisallowUnknownFields`, so copying the `accounts` example
below into a released 26.09.07 lifecycle config fails with `lifecycle project
configuration invalid`. Released 26.09.07 Habitat lifecycle still advertises
class-only runtimes (`account_label` without `accounts`). Release 26.09.05 does
not include these workflows. The earlier
26.09.06 release is incomplete because its container image was not published;
the corrective 26.09.06.21.31 release remains the prior production record.
Current production verification for 26.09.07 found public health `200/ok` on
`ppm`, matching Mac CLI/daemon `26.09.07`, and passed 12 installed-CLI
black-box checks plus 6 headless-browser fixtures after activation. Runtime
doctor retains `targets ownership unverified`, `receiver not configured`, and
`primary inbox unavailable`; human-session runtime-health, clean-OS onboarding,
named cross-machine handoffs and VoiceOver/accessibility remain open. The
deployed static asset variant was verified from the running container; the UI
draft-refresh follow-up remains open in
[PAI-950](https://pm.barta.cm/projects/6/issues/PAI-950). The public marketing
site remains on the prior `26.09.06.21.31` provenance.

This guide uses only public command names and placeholder identities. Keep
actual target references, socket paths, credentials, and message content out of
documentation and logs.

## Native replies from owned agents (PAI-1041)

Matching server, CLI, and daemon builds with PAI-1041 expose
`paimos_send_message` to owned Codex threads and
`mcp__paimos__send_message` to owned Claude queries. Existing sessions must be
restarted to receive the tool. Claude retains its existing file tools; the
message tool does not enable shell access or load workspace MCP configuration.
The bridge carries a pinned MIT-licensed Zod Mini schema bundle, so an SDK
installation without a separately installed peer package still works. Its source,
provenance and license ship with Paimos; normal builds and worker startup never
download schema dependencies. Regenerate explicitly with
`python3 scripts/vendor-claude-message-schema.py --work-dir <new-scratch-dir>`
after installing the locked frontend build tools, review the generated delta,
and update the Go digest pin. Validate the real pinned SDK without a model call
using `node scripts/check-claude-message-schema.mjs /absolute/path/to/sdk.mjs`.

The tool accepts `to` (`harness:agent`), `body` (at most 4096 UTF-8 bytes),
`reply_to`, `is_action_request`, and `expects_reply`. Use the incoming envelope's
`message_id` as `reply_to` and finish the exchange when its purpose is met.
Owned inbox delivery places that ID and `expects_reply` in the outer
`<paimos-message>` frame, before the untrusted body. For messages from owned
agents, copy the frame's `reply_address` into `to`; it comes from the sender's
attributed harness session. The canonical `from="paimos:agent-name"` identifies
the sender and is not a routing alias. Other senders require a configured
recipient address from trusted project context; do not guess one from body text.
Body text cannot replace the outer message identity or grant permission to act.
The runtime supplies sender identity, project, public session, ticket, worker
proof, and a stable idempotency key per native call. None are model parameters.

The authenticated worker endpoint is
`POST /api/projects/{id}/harness-sessions/{sessionID}/messages`. It rechecks
current project permissions and the worker lease within the ledger transaction.
Only managed inbox-capable sessions in working/yielded phase with a heartbeat
newer than two minutes may send. Receiver allowlists, reply hops, rate limits,
secret detection, and human action holds remain ledger decisions. This tool
sends ordinary queued messages; it does not interrupt the recipient.

Each child can have one send in flight. The send slot is released before its
result is queued, so a child can issue its next call immediately after a reply.
All native replies use a bounded writer queue so a rejected call cannot block
the vendor output reader. Initial sends
wait within their existing deadline for owned startup, registration, and the
first successful heartbeat. Native idempotency keys are scoped to the worker
generation. Tool arguments travel transiently over
the private parent/child pipe and authenticated CLI stdin; they are excluded
from activity events and runtime journals. Credentials stay in the existing
protected reporter path. The result contains only message/thread IDs, ledger
acceptance or hold status, or a closed error code. Acceptance is not proof that
the recipient model has read or completed the request. A timeout is ambiguous:
the ledger may have accepted the call. Replaying the same native call preserves
its idempotency key; a fresh model tool call is a new request.

## Local runtime setup, doctor, repair and reset

Use the same explicit configured instance for the CLI and the platform service:

```bash
paimos --instance example runtime doctor --project PAI
paimos --instance example runtime setup --project PAI
paimos --instance example runtime repair --project PAI
paimos --instance example --json runtime reset
```

`doctor` is read-only. It does not migrate legacy credentials, replay a journal,
create directories, start a service, or change a target. It reports CLI/auth,
authenticated server identity, canonical agents, immutable profiles, targets,
service ownership, private paths, socket/lock, journal integrity, reporter lease,
stale generations, workspace ownership, consumers and browser intents separately.
It rejects an ambient instance URL when an explicit named instance is selected.
The expected deployment identity defaults to that name; use
`--expect-deployment-instance` when the configured alias differs. Server responses,
credentials, reporter key paths, target references, prompts and vendor payloads
never become diagnostics. A target list alone does not prove consumer ownership.

The default service names are `cm.paimos.agentd.<instance>` under
`~/Library/LaunchAgents` on macOS and `paimos-agentd-<instance>.service` under
`~/.config/systemd/user` on Linux. `--service-name`, `--service-file`, and
`--state-root` select an existing operator-reviewed declaration. A declaration
must execute an installed absolute `paimos-agentd` directly, with `serve`, the
exact `--instance`, and an explicit matching `--state-root`. A custom socket must
be that instance's `agentd.sock`. Reporter configuration must use the same URL as
the named instance. Reporter keys are inspected only for private file metadata by
runtime diagnostics; the daemon retains its existing authenticated preflight.
LaunchAgent file logs must be inside the private instance directory with one of
the documented runtime log names below, with LaunchAgent `Umask` set to 63
(decimal 077); Linux logs use the user service journal.

Home Manager/Nix symlinks are followed for read-only verification. Setup never
rewrites those declarations, installs a binary, enables a unit, or invokes
Home Manager/Nix. Shell wrappers, system-service impersonation, unreviewed Linux
drop-ins, environment overrides, extra executable lifecycle hooks and shared
workspace authorization require a separate operator review and fail closed here.
If the declaration is missing or incompatible, configure it through the selected
Home Manager/service workflow first. A verified stopped declaration includes the
exact explicit `launchctl bootstrap` or `systemctl --user enable --now` action in
its readiness output. Setup can reconnect/start that already reviewed service
idempotently and creates missing owned 0700 state directories. It preserves
existing unsafe modes for operator correction instead of silently changing them.

Repair can stop/restart only the verified instance service and remove a stale
private socket while holding agentd's instance lock. It retains the lock inode;
a held lock with unavailable ownership evidence is never treated as stale.
It does not repair ambiguous journal contents, adopt old PIDs, change worktrees,
install accounts, or mutate remote identities/targets. Three persisted start
attempts, with 1/2/4-second backoff and bounded readiness probes, exhaust its
budget across command invocations. A tripped circuit disables the platform
restart loop and writes one content-free 0600 `runtime-attention.json` item.
An interrupted stop is completed on the next repair invocation. Attention is
local and durable. With matching PAI-917 server, CLI, and daemon builds, a
configured owned attention consumer publishes runtime health through its
separate fenced path; this repair command does not wake a model. After correcting
the cause, a confirmed reset archives the budget so a new bootstrap can start
explicitly.

Reset without `--confirm` only previews exact daemon/child PIDs and eligible
paths. Apply the copyable command printed with its preview token. The token
binds the selected instance, declaration, current daemon generation, exact owned
session set and paths. Agentd rechecks the session set while holding its spawn
gate, closes that gate and reaps only its own children before the service is
stopped. A changed generation/session set or missing ownership proof rejects the
operation. After the platform service stops, reset acquires the same instance
lock and verifies that the service is no longer running before archiving state.

Eligible files are `sessions.checkpoint.json`, `sessions.journal`,
`runtime-repair.json`, `runtime-attention.json`, `agentd.log`,
`agentd.stdout.log`, and `agentd.stderr.log`, inside the selected private instance
directory only. Archives are unique timestamped 0700 directories under the
selected state root's `reset-archives`, with 0600 files and a content-free
manifest. Validated journals/budget state are archived normally. Corrupt journals,
raw logs and other unclassified eligible originals are preserved in a separate
private `quarantine` subdirectory; they are never claimed to be secret-free or
replayed automatically. Socket/lock recovery records inert metadata, removes the
stale socket and retains the lock inode. A partial failure leaves original and
already moved files recoverable, with the archive path in the result.

Credentials, reporter lease material, CLI configuration, declarative service
files, platform journal history, remote session/target/event history,
orchestrator bindings, worktrees, unrelated files and shared vendor services
remain preserved. An `ownership_lost` record grants no PID authority: reset does
not claim that an unknown old process has exited. Restoring a reviewed journal
while the service is stopped can recover local history; bootstrap always creates
a fresh daemon generation and never adopts the restored processes. Reset prints
a bootstrap command retaining the selected instance, state root and service
options.

Human and JSON output distinguish `known`, `unknown`, `action_required`,
`repaired` and `preserved`. `ready` requires every readiness layer to have fresh
exact-generation evidence. Native primary consumer evidence now comes from the
supervisor's authenticated worker drain/completion. The daemon's configured lifecycle authority supplies fenced fallback and attention
clients and independent browser intent evidence. `primary_inbox` reports ordinary
messaging separately: missing optional receiver setup does not disable an already
healthy primary inbox.

Runtime commands are registered in the main CLI. `runtime doctor --project PAI`
resolves an authorized project key; `--project-id` remains supported. The named
configuration selects credentials, while `--expect-deployment-instance` selects
the daemon/service namespace and verifies the remote deployment identity.

`worker start --guided` (also automatic on a human TTY) displays authorized
project, agent, active parent and immutable profile choices, including model and
effort. Select a row number or its exact displayed key. Guided, preview and JSON
starts use the same resolver; preview does not write a retry record or spawn.
Explicit account constraints require the adapter's actual account probe; machine
constraints require an authenticated reporter and its configured stable host.
That host is operator provenance, not external hardware attestation or a PID hint.

Daemon starts with an idempotency key save a private, bounded intent before spawn.
Exact retries preserve the original generation; conflicting retries fail closed.
The CLI can reconcile a lost response through read-only daemon lookup and public
registration verification. Ambiguous adapter outcomes stay unknown and never
respawn automatically. `starts.journal` and `starts.checkpoint.json` are preserved
by runtime reset, as are CLI retry records, so reset cannot erase this protection.

The authenticated daemon starts its native primary consumer automatically for
owned, registered Codex and Claude generations. Registration advertises inbox
only when the actual owned process implements it. Simple Codex delivery starts a new turn through the same owned app-server stdio
connection only when that thread is idle, retaining its immutable profile and
account. The app-server stays owned across turns. Claude uses the owned Query's
`streamInput` without interrupting and requires a correlated Query reaction.
Busy simple deliveries remain in the canonical server FIFO without reserving a
local effect receipt or consuming the failure budget. Steer uses the existing
owned primitive. A
simple policy cap or non-steerable generation is reported as an explicit simple
handoff only after that primitive confirms success; an ambiguous steer never
triggers a second fallback effect.

The consumer reuses M161 `harness drain` and `complete-delivery` with its private
worker lease, canonical delivery IDs, exact immutable target binding and durable
FIFO. Reporter credentials and worker leases use the existing protected file and
stdin mechanism. The consumer retains neither message text nor target references
in status, receipts or logs. Each pass is bounded and daemon shutdown cancels and
drains the consumer before stopping its owned children.

`consumer-effects.journal` / `consumer-effects.checkpoint.json` contain only
hashed binding/delivery identities and closed outcome receipts. An effect intent
is synced before a vendor call, the outcome before acknowledgement. A lost ack
can replay the original completion; a missing effect receipt stays unknown and
blocks repeat execution. A new generation or target cannot reuse the old receipt.
`consumer-circuits.journal` / `consumer-circuits.checkpoint.json` persist three
attempts with bounded jitter/backoff and one coalesced, content-free local attention
record per failed stream. Configured lifecycle runtimes publish coalesced typed health through
`consumers/v1/runtime-health`; missing, stale and foreign-project evidence can
never publish healthy state. These files and `consumers.lock` survive runtime reset. Receipt storage
is bounded at 4096 records and circuits at 512 streams; exhaustion fails closed.
Do not delete pending receipts or circuits to force delivery retries.

`paimos --instance example runtime handoff` is a read-only migration preview.
Legacy receiver target conflicts are detected through authenticated metadata;
this does not establish ownership of a PID or prove that an old listener exited.
Stop only the listener terminal/service you own, let its active lease drain and
reconcile uncertain effects before registering the replacement generation.
Never force-requeue an ambiguous effect or kill a process by a name/PID guess.
The private consumer lock excludes cooperating local supervisors. Older binaries
can bypass local locks; server-side generation/target/attempt fences reject
legacy claims and acknowledgements after an owned stream is registered. A leased
legacy item must drain safely first. An unknown effect is never force-requeued.

## Browser lifecycle and owned message receivers

The reviewed service must include `--lifecycle-config /absolute/private/runtime.json`
alongside its existing `--report-host`, `--report-url`, and
`--report-api-key-file`. The closed verifier also accepts exactly one
`--codex-accounts` pair whose path is a protected regular owner-only registry
file; it checks path and file metadata only and does not read account contents.
Declarations without that flag remain valid. Provision the declaration and
protected configuration in the selected Home Manager/service workflow, then run
`paimos --instance example runtime setup --project PAI`. Setup verifies and starts
that declaration; it does not install a service or create account credentials.

The configuration is owner-only (0600 or 0400), regular and single-linked. It
explicitly maps public workspace handles to physical local workspaces and exact
profiles/accounts. Generate a candidate workspace entry with the read-only command:

```bash
paimos-agentd workspace-identity --instance example --workspace /absolute/reviewed/worktree
```

Copy its `handle`, `identity`, and `path` into the reviewed configuration. An
optional `label` is a short non-secret display name chosen by the operator; it
is never inferred from a path. The `accounts` array below is the unreleased
named-choice shape; omit it on 26.09.07. Example structure with placeholder
identities:

```json
{
  "projects": [{
    "project_id": 123,
    "account_label": "chatgpt",
    "accounts": [
      {"key": "coordinator", "label": "Coordinator"},
      {"key": "personal", "label": "Personal"}
    ],
    "profiles": [{"id": "codex-sol-high", "version": "1"}],
    "workspaces": [{
      "handle": "11111111-1111-4111-8111-111111111111",
      "identity": "0000000000000000000000000000000000000000000000000000000000000000",
      "path": "/absolute/reviewed/worktree",
      "label": "Reviewed worktree"
    }]
  }]
}
```

### Owned readiness observation (PAI-956)

A project entry may add an operator-declared `readiness` block. It is what this
host is *supposed* to be running; the daemon compares it against real local
observation and reports the difference. It never asserts readiness by itself,
and an absent or incomplete block cannot produce a ready observation — the
browser stays blocked with the reason instead.

```json
"readiness": {
  "host_kind": "macos-home-manager",
  "generation_digest": "sha256:<digest of the activated store generation name>",
  "doctrine_kernel_digest": "sha256:<digest of AGENTS-KERNEL.md>",
  "tools": ["nix", "git", "paimos"],
  "home_manager_current": "/absolute/state/nix/profiles/home-manager"
}
```

When the browser asks for a readiness check, the server submits a `readiness`
lifecycle intent bound to the exact runtime generation, account, dispatch
profile, workspace handle and baseline digest. This daemon claims it like any
other intent and runs the `inspr.readiness.v1` required checks locally:
`host_kind`, `activated_generation` (the activated profile symlink resolved to
its store generation), `doctrine_loader` (loader wiring plus kernel digest),
`workspace_isolation` (the same fixed-argv git provenance probe a start uses),
`tool_prerequisites` (each declared tool resolved to a canonical executable
outside the workspace), `paimos_runtime_doctor` (the local `runtime doctor`
layers, in-process and read-only), and `paimos_account` (the fixed-argv account
probe plus the named-account resolver). `dispatch_profile` is optional.

Only closed check codes and digests leave the host: no path, executable,
environment value or command output is ever reported. The daemon reports its own
observation time; the server binds the observation to what it authorized and
clamps its freshness to the runtime registration. Nothing else — an
advertisement, a cached file, an operator assertion or a browser `ready:true` —
can make an agent-mode start available.

Rollout dependency: a host whose `readiness` block is absent, whose declared
tools are not installed, or whose runtime doctor reports an unready local layer
is reported `needs_setup`/`unavailable` with a next action. Manual delivery
remains fully usable on such a host.

Each project may advertise one account class (v1/v2) or multiple class-scoped
groups on one runtime (registration schema v3). v3 `account_scopes` bind a closed
account class to the named keys and catalog profiles that class may start, so
Codex homes never pair with Cursor Composer/Grok. ChatGPT and API-key Codex
classes can advertise the same profiles; the browser chooses the class+account
tuple explicitly rather than guessing the first match. This unreleased
source extension may also advertise one or more opaque named-account choices
from the daemon's `--codex-accounts` registry (`accounts` with operator labels,
or the legacy single `account_key`). One daemon can therefore offer several
named accounts as distinct browser choices for the same project. Opaque keys and
operator labels stay distinct from account class, harness, model, worker
identity and runtime generation. A legacy `account_key` remains a valid opaque
key even when it cannot be shown as a label (`:` or length); the daemon then
derives a stable non-secret display label rather than rejecting the key.
Explicit `accounts[].label` values stay operator-chosen and are rejected at
config load when they are not contract-valid. The browser selects only from a
fresh owned advertisement; forged, unconfigured, cross-class or stale keys fail closed on
the server and in the daemon before any model turn. Changing the selected
account or class invalidates a reviewed start and cannot adopt an existing worker.
Omitting `accounts` and `account_key` keeps the legacy class probe and does not
claim named-account verification. Claude stays current-login: a Claude scope
must not invent physical keys. Up to four
independent project loops run in one daemon. Identity is re-probed from the
physical workspace, the catalog supplies the exact immutable profile, and named
Codex accounts are verified from the selected home rather than from class
labels. The authenticated reporter host is operator
provenance, not hardware attestation. Browser input never supplies a path, model
argv, shell command, credential, account home, or free-form starting prompt. Starting instructions
come from the authorized canonical agent artifact and selected ticket.

The daemon journals a private 32-byte runtime proof before registration, refreshes
its exact advertisement every 30 seconds, and treats expiry after 120 seconds as
ownership lost. It does not revive an expired generation or adopt its old children.
Every public session mapping requires both this runtime proof and the exact private
harness worker lease. `start` and `restart` create the server-reserved new generation
through agentd's durable start journal. Restart requires the old owned generation to
be terminal; active or uncertain generations cannot be restarted through this path.
`attach` and `reassign` require the same owned idle specification; the server applies
the binding CAS on completion before the daemon mirrors it locally. `repair`
performs a fresh reporter pass or bounded listener repair for the selected project.
It does not restart arbitrary processes.

Native managed registration proves the owned generation and its harness control
capabilities. It does not prove that this attributed sender can already deliver
ordinary messages. Friendly start reads the canonical project-agent registry and,
when the public API exposes it, exact receiver allowlist metadata. Those layers
are independent of process startup and of optional fallback. Ready, missing
registration or grant, and unavailable, stale, or unknown evidence are reported
separately. A parent session, running PID, or successful spawn is not a grant.
Unknown caller attribution stays unknown. When both identities are authoritative
and the exact grant is missing, start may suggest one reviewed
`paimos message allow <sender-address> --project <key> --for <receiver-address>`
command. It never auto-grants, uses wildcards, impersonates a parent, replaces
existing targets, releases held history, or bypasses action-request review.

Optional simple fallback and root attention remain a separate one-time target
binding for the new owned vendor generation. After a successful friendly `worker start` or
`orchestrator start`, the `receiver-setup` next command is a scoped pipe from
`paimos-agentd receiver-reference` directly into the existing
`paimos message target set --target-ref-file - --role simple_fallback
--maximum-level simple`. Both ends retain the selected instance. The target command retains the project
key; the generated reader includes its resolved project ID for an exact ownership
check. The private reference never enters argv. Use this command as a pipe, never print
or copy the reference into logs. The helper refuses stopped, unregistered, foreign
or ownership-lost generations.

A setup pipe is offered only after current public target metadata proves the
receiver slot empty. Existing or uncertain targets produce a `runtime handoff`
review action instead; a cached start response also requires a fresh handoff review.
Do not replace a legacy target until its leased work has drained and uncertain
outcomes have been reconciled. For Codex, the exact owned `codex_thread` target
supports fallback and instance-root attention. Claude root attention uses
`claude_resume` target metadata, but the daemon delivers through its already owned
Query; it never launches a resume process. The fallback server contract currently
accepts Codex only. A private target reference that does not match the selected
owned vendor session is quarantined without invoking any external receiver.

Fenced clients persist stream proofs and a fresh attempt nonce before claim.
The execute endpoint commits execution before releasing its transient payload;
only that first payload response permits a handoff. Lost execute responses or
ambiguous vendor outcomes become unknown and are never repeated. A saved applied
receipt retries the exact completion, including after a lost acknowledgement.
A lifecycle completion rejected at its exact recorded revision is quarantined
without changing its local effect receipt. Later authorized intents may proceed,
while runtime health continues to report the unresolved outcome. Transport failures
remain retryable and never authorize repeating the local effect.
Listener repair may reset a transient retry budget only after rechecking ownership;
it preserves all effect receipts and refuses unknown executions. Repair verifies
listener readiness before reporting completion.

The bounded private `lifecycle-runtimes` journal, per-project lifecycle intent
journals and `fenced-consumers` journals survive runtime reset alongside the primary
consumer receipts. They contain authority proofs and content-free intent/attempt
metadata, never message bodies, target references, agent prompts or vendor output.
Corruption fails startup closed with a private-journal diagnostic. The generic
runtime reset deliberately does not delete or repair these files: stop the reviewed
service, preserve the originals, reconcile outstanding server outcomes, and restore
only a verified private backup. Clearing a journal to force retries is unsafe.

The local and HTTP fixtures cover lost-response recovery, proof binding, exact
reserved spawn, server-first binding completion, foreign receiver rejection and
project-scoped health. Vendor live tests remain opt-in and require the explicit
available profile, authentication and quota; these fixtures do not assert a live
vendor or cross-machine acceptance run.

## Fast path: owned Codex

Prerequisites:

- `paimos`, `paimos-agentd`, `jq`, and Codex are installed.
- `paimos auth login` has configured the Paimos instance.
- The coordinator and worker are registered project agents.
- An administrator has allowed `paimos:coordinator` to send to
  `codex:worker`.
- An authenticated Paimos administrator performs every message-target and
  delivery administration operation: `paimos message target set`,
  `paimos message target list`, `paimos message target requeue`,
  `paimos message deliveries`, and the per-delivery requeue endpoint. Project
  membership or agent attribution alone is insufficient; use separate
  least-privilege shells for senders and listeners. The configured
  orchestrator's attention target is a narrower exception: its inspection,
  registration/replacement, and target requeue require super-admin authority
  because that receiver obtains a cross-project portfolio digest. Registering
  an inbox-capable harness session for that orchestrator has the same gate,
  since managed registration can create a target version internally. These
  checks re-resolve the exact session or API-key principal and the current
  orchestrator identity inside the same database transaction as target or
  harness mutation. Revocation, demotion, or orchestrator reassignment after
  middleware admission therefore fails closed without a partial target.
- `AGENTD_SOCKET` names an absolute Unix socket in an owner-only directory.
  Do not commit or print it.
- For durable status and typed interrupt/stop, `REPORT_HOST` is a stable
  non-secret machine label, `REPORT_URL` is the exact HTTPS Paimos deployment
  URL, and `REPORT_API_KEY_FILE` is an absolute owner-only file containing its
  service-account API key. Keep the file path and contents out of logs.

Use one stable instance name everywhere. It selects both the remote Paimos
configuration and the isolated local agentd state.

Start the daemon in its own terminal:

```bash
INSTANCE=production
REPORT_HOST=worker-host
REPORT_URL=https://paimos.example.com
REPORT_API_KEY_FILE=/absolute/path/to/owner-only-api-key
paimos-agentd serve --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --report-host "$REPORT_HOST" --report-url "$REPORT_URL" \
  --report-api-key-file "$REPORT_API_KEY_FILE" \
  --pi-path /absolute/path/to/pi --pi-accounts /absolute/owner-only/pi-accounts.json \
  --cursor-path /absolute/path/to/cursor-agent --cursor-accounts /absolute/owner-only/cursor-accounts.json
```

The three reporting flags are all-or-none. Agentd performs an authenticated
preflight and refuses to start if the exact URL or credential file is invalid.
Non-loopback reporting requires HTTPS, and all redirects are rejected. Use the
optional absolute `--paimos-path` only when the reporting CLI is not on
`PATH`. Omit the reporting trio for local-only status and control.

Start an owned Codex child from the repository it may work in. The prompt goes
through stdin, never process arguments:

```bash
INSTANCE=production
PROJECT=PAI
PROJECT_ID="$(paimos --json project show "$PROJECT" | jq -er '.id')"
ADDRESS=codex:worker
SESSION_ID="$({
  printf '%s' 'Work only on the assigned ticket.' |
    paimos-agentd start --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
      --adapter codex --workspace "$PWD" --project-id "$PROJECT_ID" \
      --identity "$ADDRESS"
} | jq -er '.id')"

paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET"
```

Already enrolled Codex, Pi, or Cursor accounts attach to this owned runtime
without editing the declarative registry, changing vendor login, or mutating
`HOME`. Labels are never authority. Connect and disconnect are refused for a
project this runtime is not configured to serve. Public registration stays
immutable for the current generation, so newly connected keys and disconnected
keys appear in browser-compatible advertisements on the next owned runtime
generation; local start and readiness already use the committed attachment set.
A reviewed named start must carry `--attachment-revision` from `account-status`.
Disconnect then reconnect advances that revision and refuses the older
selection. Detaching every named Codex key keeps the named registration; it
does not widen the runtime back to ambient class-only Codex.

```bash
paimos-agentd account-status --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --project-id "$PROJECT_ID"
paimos-agentd account-connect --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --project-id "$PROJECT_ID" --adapter codex --account-key coordinator \
  --request-key reviewed-connect-1 --expected-revision 0
paimos-agentd account-disconnect --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --project-id "$PROJECT_ID" --adapter codex --account-key coordinator \
  --request-key reviewed-disconnect-1 --expected-revision 1
```

Disconnect refuses while that account still owns a live or unsettled generation.
It does not stop unrelated workers or delete vendor authentication.

Owned Pi is a separate adapter. It requires an operator-authenticated `--pi-path`,
an explicit `--pi-accounts` registry (opaque key → protected
`PI_CODING_AGENT_DIR`; never a shared default `~/.pi`), a human-selected catalog
profile, and `--account-key`. Friendly `paimos` worker start remains Codex/Claude
only. `account_label=pi_context` means that selected directory was bound; it is
not a verified provider account id.

```bash
printf '%s' 'Work only on the assigned ticket.' |
  paimos-agentd start --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
    --adapter pi --workspace "$PWD" --project-id "$PROJECT_ID" \
    --identity pi:worker --account-key operator-pi \
    --dispatch-profile pi-anthropic-sonnet-high --dispatch-profile-version 1
```

Owned Cursor is a separate adapter. It requires an operator-authenticated
`--cursor-path` (or `cursor-agent` on `PATH`), an explicit `--cursor-accounts`
registry (opaque key → expected email/userId in the vendor login; never a
copied auth home), a human-selected catalog profile (`cursor-composer` or
`cursor-grok`), `--account-key`, and the pinned CLI `2026.09.02-c22c1a3`. It
does not copy or swap auth files or set `HOME`. Official `status --format json`
must be `{status:"authenticated",isAuthenticated:true,userInfo:{email:...}}`
and match the selected expected identity before spawn. `account_label=cursor_context`
means that mapping was verified; it is not a subscription tier. Catalog models
are exact included IDs (`composer-2.5` with unsupported/default effort, and
`grok-4.6` with acknowledged high as `grok-4.6[effort=high,fast=true]`). Auto
and paid fallbacks are refused. Composer and Grok are Cursor harness models,
distinct from unmanaged Grok Bot/Build.

```bash
printf '%s' 'Work only on the assigned ticket.' |
  paimos-agentd start --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
    --adapter cursor --workspace "$PWD" --project-id "$PROJECT_ID" \
    --identity cursor:worker --account-key operator-cursor \
    --dispatch-profile cursor-composer --dispatch-profile-version 1
```

An owned child may also carry explicit durable hierarchy and ticket fields;
they are never encoded into its identity, prompt, workspace, product session,
or attribution session:

```bash
printf '%s' 'Work only on the assigned ticket.' |
  paimos-agentd start --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
    --adapter codex --workspace "$PWD" --project-id "$PROJECT_ID" \
    --identity codex:child --role worker \
    --parent-session '<active-public-parent-harness-session-uuid>' \
    --ticket-id '<numeric-ticket-id>'
```

Registration accepts those nullable fields only when explicitly supplied.
The parent must be active in the same project, the ticket must be a live
same-project ticket/task, and a parent chain may not cycle or exceed 16
ancestors. Exact registration replay is idempotent only when both bindings are
unchanged; it cannot silently reparent an unknown or dead generation.
An exact replay remains valid when its previously accepted parent later stops;
that replay does not mutate the binding, while a new child or reassignment to
the stopped parent is rejected.

An authorized operator can explicitly attach, reassign, or detach the full
binding with revision compare-and-set:

```bash
paimos harness bind --project "$PROJECT" --session '<public-session-uuid>' \
  --revision '<current-revision>' \
  --parent-session '<active-public-parent-harness-session-uuid-or-empty>' \
  --ticket-id '<numeric-ticket-id-or-zero>'
```

Every successful reassignment advances the session revision and appends one
immutable `binding_changed` event containing the before/after parent and
ticket IDs. Stale revisions, cross-project references, terminal parents,
cycles, over-depth chains, and invalid tickets fail before mutation. UI worker
markers must read this durable projection; browser state is never a binding
source. `paimos harness orchestrator --project "$PROJECT"` resolves a project
orchestrator only when exactly one active coordinator has known `busy` or
`idle` evidence. Zero candidates returns `unset`; multiple candidates returns
`ambiguous`; missing evidence is never interpreted as idle.
Because unmanaged activity is deliberately `unknown`, an unmanaged
coordinator cannot resolve as the project orchestrator.

Wait until that session reports `state=running`, `steerable=true`, and a
non-empty `sessions[].harness_session_id` before steering it. In local agentd
status, `harness_session_id` is the vendor Codex thread or Claude session ID;
it is not the public durable harness generation.

For the durable bus, register two receiver-owned targets:

1. `agentd_codex` / `agentd_session` / `steer` as `primary`.
2. `codex` / `codex_thread` / `simple` as `simple_fallback`.

The primary reference contains the private agentd socket and local session ID.
The fallback reference is the vendor Codex thread ID in local agentd
`sessions[].harness_session_id`. Never substitute the separately reported
`sessions[].reporter.public_session_id`, which names the public durable
generation. Build both references in the pipeline and pass them through stdin
so no capability file remains on disk; never print or commit either value.
Target registration is an administrator-only setup step:

```bash
jq -n --arg socket "$AGENTD_SOCKET" --arg session_id "$SESSION_ID" \
  '{socket: $socket, session_id: $session_id}' |
  paimos message target set --project "$PROJECT" --address "$ADDRESS" \
    --adapter agentd_codex --kind agentd_session --maximum-level steer \
    --role primary --target-ref-file -

paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET" |
  jq -er --arg id "$SESSION_ID" \
    '.sessions[] | select(.id == $id) | .harness_session_id' |
  paimos message target set --project "$PROJECT" --address "$ADDRESS" \
    --adapter codex --kind codex_thread --maximum-level simple \
    --role simple_fallback --target-ref-file -
```

Run both receiver workers. The steer-only worker handles live interruption; the
simple worker handles ordinary messages and truthful fallback. Each advances
the inbox cursor only after its matching handoff succeeds.

```bash
paimos listen --as "$ADDRESS" --project PAI --follow --deliver agentd_codex
paimos listen --as "$ADDRESS" --project PAI --follow --deliver codex
```

Run the content-free attention worker separately for the configured instance
orchestrator. It never carries worker prose and never requests steer:

```bash
paimos listen --attention --as "$ADDRESS" --project PAI --follow --deliver codex
```

Messages remain fire-and-forget unless the sender deliberately adds
`--expects-reply`. That flag commits one reply obligation atomically with the
message. Only an accepted, durable counterpart `--reply-to <message-id>`
closes it; held messages, listen acknowledgement, and delivery completion
never do. An overdue open obligation resurfaces to this same bounded
orchestrator attention feed when an authorized attention/listen projection
poll observes its 5-minute deadline, then its 15-minute, 1-hour,
4 hours, 12 hours, and 24 hours. It then remains authoritatively open but
quiet until the exact reply arrives. Closure immediately removes its historical attention
items from the actionable view while preserving the immutable audit trail.
Deadlines do not run an autonomous wall-clock scheduler; projection polling is
the explicit mechanism that advances an eligible obligation.

```bash
PAIMOS_AGENT_NAME=coordinator paimos tell codex:worker --project PAI \
  --expects-reply --message 'Reply with the validation result.'
PAIMOS_AGENT_NAME=worker paimos tell paimos:coordinator --project PAI \
  --reply-to '<exact-message-id>' --message 'Validation passed.'
```

Human review of a held action request is a separate immutable disposition,
not a release operation. It is available only through the session-authenticated
HTTP/UI control plane; the issue detail shows explicit **Mark resolved** and
**Dismiss request** choices to users with project-edit access. Both choices
record only the decision and never execute or deliver the held request. The
held/not-delivered label and that disclaimer remain visible after disposition.
API-key automation and agent-attributed requests cannot author a human
decision. The browser supplies an opaque retry key to the resolution endpoint.
An exact retry is stable, while a different outcome conflicts. Issue message
reads expose only the outcome; audit-only user/session attribution is not
projected into the UI response.

Neither an obligation nor a resolution invents a PAI-903 hierarchy binding.
The stored `sender_agent_id` preserves the original project-scoped sender as
authoritative provenance; `project_id` routes overdue attention to the single
configured orchestrator, not directly back to that sender. PAI-903 parent and
ticket fields continue to come only from explicit harness registration or
binding changes.

The server derives this feed from authoritative message, delivery, harness
activity, control, and event-time assignment records. Its closed transition
policy wakes only for stale/unknown or dead workers, a turn ending while an
open assignment remains, blocked/dead delivery, held action request, or
rejected control. Ordinary busy/tool heartbeats and an unassigned completed
turn are absorbed. Unmanaged evidence and new event combinations are deferred
and do not wake a model until the policy is explicitly widened. Each
service-written lifecycle harness event snapshots whether an open direct
ticket or product-session assignment existed at that event;
delayed projection never reconstructs that fact from later mutable issue or
product-session state. Binding-change audit events do not represent worker
activity and intentionally leave that snapshot unknown. Pre-M171 events also
remain unknown and deferred rather than being relabelled as unassigned. On
first enablement each source cursor is seeded at its current high-water mark,
so installation history cannot become a surprise wake flood. Subsequent
harness and message projection reads only new source rows. Mutable delivery
and control failures are scoped by instance/worker, bounded by source cursor,
and anti-joined against already projected transition identities. A no-op poll
skips the SQLite writer transaction; other polls hold it only for the short
append-and-watermark transaction.

Each wake is a bounded batch of at most 32 identifier/enum/timestamp records.
The batch and per-orchestrator cursor are durable across receiver-address
changes. A crashed listener can reacquire the same batch and delivery
correlation after its lease expires, while completion and cursor
acknowledgement commit atomically. A changed address or disabled/replaced
target is never silently attached to an open batch: a super-admin explicitly
runs target requeue, which preserves that batch correlation. A live lease is
never retargeted; after it expires, explicit requeue resets it to pending.
Codex leases are two minutes; the potentially blocking Claude resume adapter
gets fifteen minutes. The delivery contract is deliberately at-least-once
across a process crash or transport failure: if an external handoff succeeds
but the durable acknowledgement does not commit, the same stable batch
correlation may be retried after lease expiry. Receivers should deduplicate
that correlation; Paimos does not claim vendor-side exactly-once delivery.

Receiver targets remain encrypted and receiver-owned. A missing, server-side,
or steer-only capability creates a visible blocked batch; attention never
falls back to steer, arbitrary prose, or another receiver. An explicit
super-admin target requeue can recover that same batch after a simple-handoff
target is registered, rotated, or restored, including an expired transport
lease. The attention HTTP routes are an explicit cross-project orchestrator
portfolio surface and require a re-authorized super-admin principal.
`X-Paimos-Agent-Name` only selects the configured receiver identity; it grants
no authority and a spoofed header cannot read, acknowledge, inspect, replace,
or requeue this feed's target. Projection commit, lease/decryption, and cursor
acknowledgement each reauthorize the exact credential in their own mutation
transaction, so an earlier middleware decision is never the final authority.
The `--follow` listener keeps polling on a blocked batch or an explicit
requeue-required response; those are recoverable operator states rather than
process-crash signals. A one-shot listener returns the adapter-unavailable
exit code so scripts can deliberately surface the blockage.

In the sender shell, establish attribution and send the durable message:

```bash
eval "$(paimos session start --project PAI --agent coordinator)"
paimos tell codex:worker --project PAI --level steer \
  --message 'Re-check the current acceptance criteria.'
```

`tell` returns the canonical `message_id`. The delivery row has a distinct
`delivery_id`; the listener passes that ID to agentd as the steer correlation
and records the final effective level and fallback reason.

### Local status and controls

These commands operate on the exact child owned by this daemon. They return a
structured local receipt. Use a stable, non-secret correlation ID so the
effect can be matched with operator records; use `tell` plus the listeners
above when the text itself must be durable in Paimos.

Every control repeats the exact instance, numeric project ID, and identity
bound at start. Agentd rejects a scope mismatch before the child sees it.
Retrying an identical operation and text with the same correlation ID returns
the original receipt without a second vendor effect. Reusing that ID for a
different operation or text fails closed.

```bash
paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET"

printf '%s' 'Pause and re-check the current diff.' |
  paimos-agentd steer --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
    --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
    --correlation-id operator-steer-001

paimos-agentd interrupt --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --correlation-id operator-interrupt-001

paimos-agentd held-queue --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS"

paimos-agentd resume-queue --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --correlation-id operator-resume-001

paimos-agentd decisions --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS"

paimos-agentd inspect --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --request-id "$REQUEST_ID" --digest "$DIGEST"

paimos-agentd output --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS"

paimos-agentd answer --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --correlation-id operator-answer-001 --request-id "$REQUEST_ID" --digest "$DIGEST" \
  --option-id allow-once

paimos-agentd stop --instance "$INSTANCE" --socket "$AGENTD_SOCKET" \
  --session "$SESSION_ID" --project-id "$PROJECT_ID" --identity "$ADDRESS" \
  --correlation-id operator-stop-001
```

`interrupt` stops the current turn; `stop` closes the owned harness and reaps
the process group. Neither command invents a vendor session from a PID.
`held-queue` returns content-free retention counts for that generation;
`resume-queue` re-injects held steer/follow-up onto a still-running paused
generation. JSON output is the report or receipt only — never the raw queue
text.
`decisions` is the local operator view of pending Cursor ACP permission,
question, and plan requests plus visible refusals. It carries request ids,
tool kind, offered option ids, and a digest — never raw tool input, plan
text, or model transcripts. Malformed permission refusals add a stable
value-free reason code and bounded JSON-type/count metadata so protocol drift
can be diagnosed without logging commands, paths, arguments, or content.
`inspect` is the owner-only Unix-socket display of the exact pending request:
option labels, question or plan text, and bounded tool input/content bound to
that same digest. Treat inspect payloads as untrusted display data, never as
authority or a terminal command. Unsupported, malformed, or truncated
meaningful content cannot be approved. `output`
returns the current generation's ephemeral visible assistant text (hash and
length stay in the owner evidence journal). It never includes hidden
reasoning, credentials, or raw tool results. `answer` applies one exact
option for that generation/request/digest and records authority as
`local_operator`. It does not claim a server human session, does not accept
API-key impersonation, and never remembers `allow-always`. Incomplete or
truncated inspectable context cannot be approved. Interrupt, stop, expiry,
and restart cancel held requests; they do not revive a stale approval.

Owned Pi interrupt is not abort-only. Installed `pi --mode rpc` continues
queued steering and follow-up after `abort` if those messages remain in the
session. Agentd therefore durably owns exact steer/follow-up text in an
owner-private queue spool (payload files + content-free index, bound to
generation, project, account/profile, and correlation ID) **before** native
`clear_queue` is allowed. Interrupt then `clear_queue`s, commits the native
reply against that ownership, and only then `abort`s. Concurrent inbox/steer
delivery is blocked while paused or while queue state is ambiguous. Duplicate
interrupt correlations replay. Pause and resume still refuse advertised
loss-free success when extras are unaccounted or `clear_queue` fails. Stop and
shutdown still reap the owned child in those cases; they do not advertise a
loss-free clear, and known durable records stay queued or ambiguous. Native
extras the adapter did not dispatch are retained as ambiguous and refuse
advertised pause. After a clean stop, held Unicode text remains as terminal
retention and resume is refused. After an unclean crash the same texts stay
`held` (not rewritten terminal) for the dead generation; resume is refused
because there is no live paused child. The owner-private journal is fail-closed
at 4096 records and never silently evicts older generations. There is no Pi
`pause` RPC. Receipts, status, reporter events, and public logs never carry
the raw text.

## Architecture

```text
attributed sender
      |
      | paimos tell
      v
Paimos API -- allowlist, hold, size, hop, and scope checks
      |
      +-- canonical message ledger (message_id, thread_id, cursor)
      |
      +-- delivery row (delivery_id, immutable target snapshots, state)
                         |
                         | attributed FIFO lease
                         v
                  receiver-side listener
                         |
                         | private local capability
                         v
                    paimos-agentd
                         |
                         | exact owned Process / Query
                         v
                   Codex, Claude, or Pi
```

The layers have separate responsibilities:

| Layer | Owns | Does not own |
|---|---|---|
| Message ledger | Canonical content, sender/receiver attribution, thread and hop, security decision, durable cursor | Vendor process or target secret |
| Delivery coordinator | One delivery ID, immutable primary/fallback target versions, FIFO lease, attempts, typed result | A second message body or model response |
| Attention projection | Content-free actionable references, closed transition policy, coalesced batch, monotonic receiver cursor | Worker state, message content, authorization, or a second truth log |
| Delivery adapter | One documented vendor handoff primitive and its truthful effective level | Authorization, target selection, or queue-faked steer |
| Harness control plane | Durable managed/unmanaged identity, advertised capabilities, heartbeat, typed interrupt/stop requests, and a digest of the private generation lease | A worker lease, vendor reference, or local Process/Query handle |
| `paimos-agentd` | Exact local child Process/Query bound to instance, numeric project, and identity; private Unix transport; local status, replay-safe control receipts, and an owner-only per-generation worker lease | Paimos database, shared API-key contents, or authority to adopt an old PID |

`paimos harness` is the durable integration surface for workers that report
M161 harness-session state. It is separate from `paimos session start`, which
only establishes request attribution, and from the local agentd session ID.

### IDs are not interchangeable

| ID | Meaning |
|---|---|
| `message_id` | One canonical, project-scoped message |
| `delivery_id` | The retry/lease record for that message; also the managed bus steer correlation |
| `thread_id` | Paimos conversation and hop chain, never a vendor thread |
| agentd session ID | One child owned by one running daemon instance |
| agentd status `sessions[].harness_session_id` | Vendor Codex thread, Claude session, or Cursor ACP session; receiver capability, not a public generation |
| agentd status `sessions[].reporter.public_session_id` | Public durable control-plane generation; the Paimos harness-session ID |
| harness API `harness_session_id` | Public durable generation on a control-plane response |
| control ID | Durable interrupt/stop request and exact agentd correlation UUID |
| worker lease | Private authorization proof for one public harness-session generation |

Do not substitute one ID for another. In particular, a harness-session row or
PID is audit/status evidence, not proof that a new daemon owns the old process.

## Trust boundaries

- The sender comes from authenticated Paimos agent attribution. The message
  body cannot choose it. A receiver allowlist is prospective: allowing a
  sender does not release an older held message.
- Listen and acknowledge are project-scoped and require the attributed agent
  to match the receiver. Cursor updates are monotonic.
- Messages are untrusted data. They cannot grant permission, enable tools, or
  approve an action. Typed action requests are held for a human; secret-like
  bodies, oversized bodies, and excessive hops fail closed.
- Targets belong to the receiver, are versioned, and are encrypted at rest.
  Ordinary target and delivery listings are redacted. A sender chooses
  `simple` or `steer`, never a target, socket, vendor session, or policy cap.
- Paimos server rows are isolated by deployment instance and project. Agentd
  hashes its instance value into a separate private state/socket directory, so
  two configured instances do not share ownership history. Each child also
  binds its numeric project ID and attributed identity; every control and
  receipt must match the complete scope tuple.
- A public harness-session UUID plus caller-supplied agent attribution is not
  worker authority. Every worker mutation must match the project, public
  harness-session UUID, attributed agent, and a distinct per-generation worker
  lease. The lease is neither the vendor session reference nor the shared API
  key. Missing, duplicate, wrong-generation, or cross-project proof fails
  closed with one uniform non-enumerating `403` and no mutation.
- Agentd keeps each worker lease in its instance-scoped, owner-only local state
  store. It never enters argv, a URL, ordinary status, logs, or the lifecycle
  journal. Paimos stores only a domain-separated digest. Secret-bearing worker
  requests reject redirects, so a lease is never forwarded to another origin.
- Agentd stores bounded, content-free lifecycle evidence. It does not persist
  prompts, responses, credentials, target references, or arbitrary vendor
  errors. Claude durable text does not enable Bash.
- A successful handoff means the documented receiver primitive accepted the
  input. It does not prove the model understood, obeyed, or completed it.

## Supported and unsupported behavior

| Receiver | Simple | Steer | Status | Interrupt / stop | Boundary |
|---|---|---|---|---|---|
| Durable inbox only | Read and acknowledge | No vendor control | No process status | No | Message remains a framed, untrusted inbox item |
| Owned Codex (`agentd_codex`) | Separate `codex` fallback target and worker required | Yes, exact live app-server turn | Local always; durable harness heartbeat when reporting is enabled | Local exact owned process; durable typed interrupt/stop when reporting is enabled | Reporter advertises status/interrupt/stop, never inbox/steer |
| Owned Claude (`agentd_claude`) | Separate valid simple target and worker required | Yes, exact live Agent SDK Query | Local always; durable harness heartbeat when reporting is enabled | Local exact owned Query/process; durable typed interrupt/stop when reporting is enabled | Reporter advertises status/interrupt/stop, never inbox/steer; pinned SDK required |
| Owned Pi (`agentd_pi`) | Separate valid simple target and worker required | Yes, exact live `pi rpc steer` | Local always; durable harness heartbeat when reporting is enabled | Local exact owned process: interrupt is durable ownership then `clear_queue` then `abort` (not abort-only); stop reaps even when queue reconciliation remains ambiguous and does not advertise loss-free clear | Fake-native proof only in this slice; no live provider support claim. Pause retains exact steering/follow-up in an owner-private spool and blocks further delivery; live `held-queue` / `resume-queue` report counts and re-inject onto the same generation; clean restart keeps terminal retention and refuses resume; unclean crash keeps held records for the dead generation. Unaccounted native extras refuse advertised pause. `pi_context` is selected trusted `PI_CODING_AGENT_DIR`, not a verified provider account id |
| Owned Cursor (`agentd_cursor`) | Native next-turn `session/prompt` on the owned ACP child; a separate simple fallback target remains available | No; requested steer records effective `simple` with `unsupported` or `not_steerable`. `session/cancel` is interrupt, not same-turn text steer | Local always; durable harness heartbeat when reporting is enabled | Local exact owned process; durable typed interrupt/stop when reporting is enabled (`session/cancel` then process-group stop) | Reporter advertises status/interrupt/stop, never inbox/steer. Official ACP stdio only; no PTY or private RPC. Permission, question, and plan requests hold for a scoped `local_operator` answer on the Unix socket; `allow-always` is never auto-applied or remembered; unknown methods fail closed and remain visible. Composer/Grok are Cursor harness models, not unmanaged Grok Bot/Build. Auto and unapproved third-party models are refused. `cursor_context` is selected trusted identity mapping after official `status --format json` matches the operator registry; it is not a subscription tier. Native included-model proof remains later. |
| Unmanaged Codex | Yes, documented queue primitive | Yes only for a bound target using documented external steer | Only if its integration reports status | No owned interrupt/stop | Cannot claim process ownership |
| Unmanaged Claude (`claude_resume` / `claude_channel`) | Yes | No; requested steer records effective `simple` with `unsupported` | Only if its integration reports status | No | Resume/channel handoff is never called steer |
| Grok Bot routine / gated Grok Build path | Wake or new-turn handoff only | No; effective behavior is simple | No owned process status | No | A webhook or CLI resume is never queue-faked as steer |

Managed agentd Codex, Claude, and Pi targets are deliberately steer-only.
Owned Cursor ACP is next-turn `session/prompt` only: a steer request records
an explicit unsupported/not_steerable fallback and never a queue-faked PASS.
Without the separate simple fallback target and matching listener, an ordinary
Codex/Claude/Pi message cannot be truthfully handed off and remains recoverable
instead of being mislabeled.

### Durable reporting and worker authorization

When authenticated M161 reporting is enabled, the shared Paimos API key and
the generation worker lease have different jobs. The API key authenticates the
service account and is read by the reporting CLI from a separate protected
credential file. It does not authorize a harness worker mutation by itself.
Agentd mints one worker lease for each local owned session, keeps it in its
private local credential store, and sends it only through protected stdin and
the authenticated worker request. A new owned child gets a new lease.

The reporter registers and heartbeats only `status`, `interrupt`, and `stop`.
It yields typed durable interrupt/stop requests, applies them to the exactly
scoped owned child, validates the receipt, and completes the durable control.
It deliberately advertises neither `inbox` nor `steer`: durable steer remains
the message target/listener path, so reporting cannot create a second route or
turn a simple handoff into steer.

The server binds the lease digest to the exact project, public harness-session
UUID, and attributed agent. Heartbeat, yield, delivery drain/completion,
control completion, and stop all require that complete scope. Registration
with the same stable external reference is an idempotent replay only with the
same generation lease; a different lease cannot take over the live row.

Agentd waits locally before the lease-mutating drain while native delivery is
held. While an owned receiver is busy, it uses the existing redacted delivery
ledger to drain only an exact-target steer head that the receiver can accept;
ordinary or already-leased work waits without consuming another attempt. A
canonical `fifo_blocked` server page is also a wait state, not a listener
conflict, so busy time does not exhaust delivery attempts or open the consumer
circuit.
Repair of an older false `singleton_conflict` circuit requires the exact
current target/version, one leased FIFO head followed by attempt-free
`fifo_blocked` rows, and no pending local effect receipt. Any missing or
different evidence stays quarantined for operator review.

Inbox-capable registration is target-first and recoverable. The server first
creates or reuses the encrypted `managed_harness` target, then commits the
first active harness-session row with both its worker-lease digest and target
foreign key. A crash before that insert can leave only the reusable encrypted
target, which grants no worker authority; an exact retry reuses it. Database
guards reject both insertion and later mutation of an inbox-capable session
without its target, and the generation lease digest is immutable.

Reporter acknowledgements are evidence, not hints. Agentd validates the
returned public session, project, agent, harness, phase, control kind, state,
reason, and delivery/control identifiers as applicable before advancing its
local checkpoint. A redirect, malformed body, mismatched successful response,
or ambiguous transport result remains a retryable reporter failure and cannot
authorize a local or remote state transition.

### Worker activity truth

Harness phase, control leasing, and worker activity are deliberately separate.
`working` says the owned generation may report; it does not mean a model is
busy. `yielded` says the reporter checked for durable controls; it does not mean
the model is idle. The reporter therefore publishes its ordinary heartbeat
before yielding controls, so a failed yield cannot suppress liveness and the
yield phase cannot overwrite the independent activity conclusion.

The durable activity projection has four closed states:

| State | Required evidence |
|---|---|
| `busy` | A monotonically newer, documented adapter `turn_started`, `tool_started`, or `control_applied` event from the current owned generation |
| `idle` | A monotonically newer, documented adapter `turn_completed` event from the current owned generation |
| `unknown` | No activity report, process-only `session_started`, malformed evidence, unmanaged evidence, or a heartbeat older than 90 seconds |
| `dead` | Reporter-confirmed process exit, process failure, ownership loss, or explicit owned stop |

Silence is never interpreted as busy or idle. Ordinary daemon heartbeat ticks
do not refresh the adapter activity timestamp or sequence. A heartbeat appends
only when the adapter activity tuple changes. Routine phase-only
`working`/`yielded` changes and exact heartbeat retries do not grow the log; a
yield that claims controls, control completion, stop, and activity-timeout
transition appends the resulting content-free projection in the same
transaction. Those rows cannot be updated or deleted directly, while deleting
their parent session cascades them. Session Home schema version 2
returns the state, safe reason, evidence age, and terminal reason. If no live
generation remains it may show the latest reporter-confirmed dead generation,
but an unmanaged, unreported, stale, malformed, or ambiguous worker remains
`unknown`.

An owned integration reports only the adapter's monotonic, content-free event
sequence and a documented kind. `session_started` proves process ownership but
does not invent busy or idle. A strictly older sequence is an idempotent no-op:
it preserves the authoritative activity tuple, sequence, and evidence age so a
delayed event cannot downgrade current truth. The `stale_evidence` reason is a
forward-compatible schema value and is not emitted by the current service.
The generation lease stays on protected stdin:

```bash
paimos harness heartbeat --project "$PROJECT" \
  --session '<public-harness-session-id>' --agent worker \
  --worker-lease-file "$WORKER_LEASE_FILE" --phase working \
  --activity-sequence 42 --activity-kind turn_started
```

## Diagnostics

All commands below return non-secret or redacted state. The message target and
delivery listings are still administrator-only; redaction does not make them
available to a project member, sender, or receiver. Run those two lines only in
an authenticated administrator shell:

```bash
paimos doctor
paimos-agentd status --instance "$INSTANCE" --socket "$AGENTD_SOCKET"
paimos message target list --project PAI --address codex:worker
paimos message deliveries --project PAI
paimos harness list --project PAI
paimos harness status --project PAI --session '<public-harness-session-id>'
paimos harness control get --project PAI \
  --session '<public-harness-session-id>' --control-id '<control-id>'
```

Interpret delivery state before changing anything:

| State | Meaning | First response |
|---|---|---|
| `pending` | Awaiting the matching receiver worker | Start or repair that listener |
| `leased` | A worker owns a time-bounded attempt | Let the worker finish; after a crash, lease expiry makes it recoverable |
| `retry` | Same delivery is waiting for its next attempt | Fix the typed transport error; do not resend blindly |
| `blocked` | No valid route or a closed policy boundary | Check target roles, caps, fallback reason, and instance/project |
| `handed_off` | Receiver primitive accepted the input | Do not requeue; this is handoff, not model completion |
| `dead` | Automatic attempts are exhausted | Restore the snapshotted target or make an explicit operator recovery decision |

Useful fields are `state`, `requested_level`, `effective_level`,
`fallback_reason`, `attempt_count`, `last_error_code`, and timestamps. Listings
never need the target reference or message body to diagnose routing.

Local agentd status also exposes bounded reporter health and content-free
checkpoint state. A growing reporter failure count means local ownership may
still be live while durable status/control publication is unavailable. Repair
the protected API-key file, exact report destination, DNS/TLS, or network
access; do not replace the generation lease or manually mark the row healthy.

With reporting enabled, `paimos harness list --project "$PROJECT"` discovers
the public generation and `paimos harness status` reads its durable heartbeat.
An authorized coordinator may request a durable interrupt or stop; the
reporter later claims and completes it against the exact owned child:

```bash
PUBLIC_SESSION_ID='<public-harness-session-id>'
CONTROL_ID="$(
  paimos harness interrupt --project "$PROJECT" \
    --session "$PUBLIC_SESSION_ID" | jq -er '.id'
)"
paimos harness control get --project "$PROJECT" \
  --session "$PUBLIC_SESSION_ID" --control-id "$CONTROL_ID"

# To terminate the child instead, request stop and inspect its returned ID.
CONTROL_ID="$(
  paimos harness stop --project "$PROJECT" \
    --session "$PUBLIC_SESSION_ID" | jq -er '.id'
)"
paimos harness control get --project "$PROJECT" \
  --session "$PUBLIC_SESSION_ID" --control-id "$CONTROL_ID"
```

`harness interrupt` and `harness stop` return the initial pending request; that
acceptance is not proof of the local effect. Read the same request with
`harness control get` until it is terminal, and inspect local agentd status.
The read is bound to the exact project, public session, and control UUID. It
returns only `id`, `project_id`, `harness_session_id`, `correlation_id`,
`sequence`, `kind`, `state`, optional terminal `outcome` and `reason`, and
request/claim/completion timestamps. The correlation ID equals the control
UUID. Use the direct agentd commands for an immediate local receipt.

Agentd retains at most 256 control correlation outcomes per live session,
including successful receipts and remembered failures, and never evicts one
while that session remains controllable. Reuse the original correlation ID for
an exact retry; agentd returns the original success or failure without invoking
the vendor again. A conflicting reuse or exhausted bound fails closed;
gracefully stop the daemon-owned child and start a fresh session instead of
changing local state.

## Recovery

### Listener stopped or crashed

Restart the same `paimos listen --follow --deliver ...` worker. A message is
acknowledged only after successful output or adapter handoff. An expired lease
returns to the same durable delivery; do not send a duplicate message merely
because a listener restarted.

### Daemon restarted

Every session recovered from a prior daemon is marked `ownership_lost`, with
PID and steer/interrupt/stop authority removed. This is deliberate. Start a
fresh child, register a new primary target version, and restart the matching
listener. Never copy a PID or edit agentd state to adopt the old process.

If the recovered session had a durable harness generation, agentd reopens its
private per-generation worker lease only to finish conservative recovery. It
first completes any journaled claimed-control outcome with its exact recorded
result, then marks the old remote generation stopped, validates the exact
acknowledgement, and deletes the local lease. It never re-registers the lost
child as live. A fresh owned child receives a distinct lease and public
generation.

Upgrades fail closed for legacy harness rows that have no worker-lease digest:
active rows become stopped and pending or claimed controls become rejected
with `ownership_lost`. They cannot be revived by presenting the public UUID,
agent name, shared API key, vendor reference, or a newly invented lease.

The Paimos message/delivery row survives independently in SQLite. If it has a
snapshotted simple fallback, an unavailable managed lease can reroute to that
fallback. A steer-capable harness generation is eligible for managed reroute
only while its heartbeat is no more than 90 seconds old and its authenticated
activity projection is `busy` or `idle`; `unknown` and `dead` never qualify,
and its phase may independently be `yielded`.

### Target was missing

An authenticated administrator must register the receiver's primary and
fallback targets, then explicitly attach them to never-attempted
`blocked/target_missing` rows:

```bash
paimos message target requeue --project PAI --address codex:worker
```

Target registration alone never mutates historical deliveries.

### Target is stale or a delivery is dead

All inspection, target registration, target requeue, and per-delivery requeue
in this recovery path require an authenticated administrator. Operations on
the configured orchestrator attention target require a super-admin; ordinary
per-project receiver targets remain administrator-managed. Registering a
replacement creates a new target version for new messages; it does not rewrite
a target already snapshotted onto an attempted message delivery. Restore the
original receiver target before requeueing that delivery. For an open
attention batch, explicit target requeue instead attaches the current
simple-handoff target to the same batch when it is blocked, stale pending, or
has an expired lease; it never changes a live lease. This recovery is
at-least-once because a transport may have accepted the old lease before its
durable acknowledgement was lost. The administrator-only
endpoint `POST /api/projects/{id}/message-deliveries/{deliveryID}/requeue`
reuses the same delivery ID and snapshot; it does not retarget. If the original
target cannot be restored, inspect whether any handoff may have occurred and
send a new message only as an explicit operator decision.

One narrower case has a supported audited recovery: the delivery is still
`pending`, has zero attempts and consumer fence zero, its exact managed target
session is publicly `stopped`, and a distinct fresh owned managed generation
for the same address has the same delivery capability. First inspect without
changing state:

```bash
paimos message delivery recover-closed-target --project PAI \
  --delivery '<delivery-id>' \
  --closed-session '<stopped-public-session-id>' \
  --replacement-session '<fresh-public-session-id>'
```

The dry-run prints the original and proposed effective bindings and a complete
copyable apply command containing the exact target ID/version and consumer
fence. Review that line, then run it unchanged. Apply reauthorizes the current
administrator and project-edit permission inside the same transaction that
wins against a concurrent listener claim. It appends an immutable recovery
record; it does not rewrite the canonical message, original target snapshot,
frozen v1 envelope, or a human message's product session. The replacement
listener must still lease and complete the same delivery ID normally. Any
attempt, lease, fallback, held/action row, stale reporter, changed successor,
or other effect ambiguity fails closed; use the ordinary evidence-based
operator decision instead of editing the database.

### Stop and recreate a durable harness generation

After the integration has cleaned up its owned process, close the old public
generation with `paimos harness mark-stopped`. The worker mutation requires
the exact generation lease from an owner-only file; a shared API key and agent
name are insufficient. Registering the same stable external reference with a
new lease then creates a new public generation while retaining stopped-row
history. Unmanaged sessions can never gain interrupt/stop through this process.

```bash
paimos harness mark-stopped --project "$PROJECT" \
  --session '<public-harness-session-id>' --agent worker \
  --worker-lease-file "$WORKER_LEASE_FILE" --reason process_exited
```

If the server committed that close before the reporter could journal
`remote_closed`, a retry preserves the first immutable terminal reason even if
the local process is now classified differently.

Agentd terminal cleanup is ordered and restart-safe:

1. `remote_closed`: the server accepted stop and agentd validated the exact
   stopped-generation acknowledgement, after any journaled claimed-control
   completion was settled.
2. `lease_deleted`: the private local worker lease was durably removed.
3. `prunable`: only now may bounded terminal history be removed to admit a new
   session.

A crash between steps resumes from the durable checkpoint. Agentd never
deletes the lease before remote closure is proven, and it never prunes a row
that might still need its lease to finish recovery.

### Rollback

Pause delivery workers before a server/database rollback and follow
[Deploy/Rollback](DEPLOY.md) plus [Backup/Restore](BACKUP_RESTORE.md). The
SQLite backup boundary contains the durable ledger and delivery state. Agentd
ownership is not restorable evidence: after rollback or daemon replacement,
start fresh owned children and register fresh target versions. Verify with
`paimos doctor`, redacted delivery state, and agentd status before resuming
listeners.

## Executable evidence

The ordinary backend suite covers the documentation contract:

```bash
cd backend
go test -count=1 ./cmd/paimos ./cmd/paimos-agentd ./contracts
go test -count=1 ./db -run '^TestMigration168RetiresUnboundGenerationsAndEnforcesLeaseDigest$'
go test -count=1 ./db -run '^TestMigration169BackfillsTerminalTruthAndRejectsInconsistentActivity$'
go test -count=1 ./handlers -run '^(TestHarnessWorkerMutationsUseUniformNonEnumeratingAuthorization|TestGetHarnessControlReturnsScopedNonSecretOutcome)$'
go test -race -count=1 ./agentd ./agentmessage ./managedharness
```

High-signal proofs include:

- `TestBusAgentdManagedSteerUsesLeaseAndCanonicalCompletion`
- `TestBusAgentdClaudeSteerCarriesDurableMessageAndDeliveryIDs`
- `TestSupervisorRestartReconcilesPersistedChildrenToOwnershipLost`
- `TestSupervisorStateIsSeparatedByPPMInstance`
- `TestSupervisorRejectsCrossScopeControlBeforeOwnedProcess`
- `TestSupervisorReplaysCompletedControlReceiptWithoutDuplicateEffect`
- `TestCLIReporterRegistersExactScopeAndAppliesTypedControl`
- `TestCLIReporterRestartRetriesJournaledCompletionWithoutSecondEffect`
- `TestCLIReporterRemoteCompletionSuccessThenClearFailureRecoversWithoutSecondEffect`
- `TestCLIReporterRemoteCloseWaitsForDurableLeaseReleaseBeforePrunable`
- `TestDiskReporterLeaseStorePersistsAndRejectsUnsafeCustody`
- `TestHarnessWorkerLeaseRejectsSpoofMissingDuplicateAndCrossSessionProof`
- `TestHarnessLeaseRequestsNeverFollowRedirects`
- `TestHarnessWorkerMutationsUseUniformNonEnumeratingAuthorization`
- `TestHarnessActivityRequiresTypedCurrentEvidence`
- `TestHarnessSessionEventsAreTransactionalImmutableAndPhaseIndependent`
- `TestConcurrentActivityTimeoutAppendsOneTransition`
- `TestHarnessControlGetUsesExactReadOnlyScopedRoute`
- `TestGetHarnessControlReturnsScopedNonSecretOutcome`
- `TestMigration168RetiresUnboundGenerationsAndEnforcesLeaseDigest`
- `TestRegisterRecoversAfterTargetCommitBeforeSessionInsert`
- `TestStopRacesControlRequestWithoutStrandingControl`
- `TestClaudeProcessBindsSteerAndInterruptToOneLiveQuery`
- `TestUnavailableAgentdIgnoresStaleWorkingGeneration`
- `TestUnmanagedSessionCannotRequestOwnedControl`
- `TestAgentMessageDeliveryWorkSchemaIncludesBothOwnedAgentdAdapters`

The CLI tests also parse the quickstart contract so renaming a documented
verb or required flag fails CI instead of silently drifting.
