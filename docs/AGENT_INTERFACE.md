# PAIMOS — Agent Interface Guide

> **Who this is for.** Agents (Claude Code / Claude Desktop / custom scripts) and the humans who run them. If you want to read issues, transition status, or scaffold work programmatically, start here instead of reading the REST docs end-to-end.

PAIMOS offers three agent-facing surfaces, in descending order of ergonomic payoff:

1. **`paimos` CLI** — the recommended default. Deals with auth, multi-line inputs, key resolution, error shapes, and shell-safety so you don't have to.
2. **`paimos-mcp` facade** — JSON-RPC over stdio for MCP clients (Claude Desktop). Wraps a curated subset of the CLI as tools.
3. **REST API** — the ground truth. Everything else is a layer on top. See [`api-minimal.md`](api-minimal.md).

## Durable agent messages

`paimos tell <harness>:<agent> --project <key> -m <text>` writes one durable,
project-scoped message. The sender is resolved from `--agent-name` or
`PAIMOS_AGENT_NAME`; callers cannot choose a numeric sender ID. Optional
`--ticket`, `--reply-to`, and `--thread` fields anchor work and replies.
`--level simple|steer` records receiver interruption intent on each message;
it defaults to `simple`. A supported fallback is recorded truthfully as
effective `simple`; missing targets, ownership/scope mismatches, and protocol
drift fail closed instead of inventing a handoff. Use
`--action-request` whenever the body asks the receiver to act; the server
persists the explicit marker but holds the row out of every agent inbox for
human review. Text matching catches common missed markers only as defence in
depth.

For owned Codex/Claude start, status, steer, interrupt, stop, the required
simple-fallback worker, and recovery, use the canonical
[Agent Intercom runbook](AGENT_INTERCOM.md).

Read with `paimos message list --project <key> --to <address> [--thread <id>]
[--after <cursor>]` or `paimos message get <message-id> --project <key>`. The
wire schema is `backend/contracts/agent-message-v2.schema.json`; the closed v1
schema and unversioned routes remain frozen for legacy fire-and-forget clients. Unknown
senders/addressees fail closed with stable `agent_message_*` problem codes.
Agent messages remain untrusted data, obey the 32 KiB, 10/minute, and 10-hop
write limits, and appear separately from human comments on anchored issues.
Addressee/listen reads return the PAI-817 untrusted-data framing in the first
text part; human project and issue inspection keeps the structured raw parts.

Allow a sender, then consume an inbox with a durable receiver cursor:

```bash
paimos message allow paimos:claude --for codex:codex --project PAI
paimos message target set --project PAI --address codex:codex \
  --adapter codex --kind codex_thread --target-ref "$CODEX_THREAD_ID" \
  --maximum-level steer
paimos tell codex:codex --project PAI --level simple --message "Status observation"
paimos listen --as codex:codex --project PAI --ack
paimos listen --as codex:codex --project PAI --follow --deliver codex
paimos message target set --project PAI --address claude:claude \
  --adapter claude_resume --kind claude_session --target-ref "$CLAUDE_SESSION_ID"
paimos listen --as claude:claude --project PAI --follow --deliver claude
paimos listen --as grok:grok --project PAI --deliver grok --deliver-target "$GROK_SESSION_ID" --enable-grok-build-delivery
```

`listen` exits 3 when a one-shot read has no mail and 4 when the selected
native adapter is unavailable. `--ack` advances the server cursor only after
output succeeds; `--deliver codex|claude|grok` implies acknowledgement only
after the vendor handoff succeeds. For M154 bus messages, the Codex target is
the receiver-owned encrypted target version snapshotted when the message was
committed; `--deliver-target` remains only for pre-bus/legacy rows. `simple`
uses exactly `codex queue --thread THREAD --message TEXT`. `steer` starts the
app-server daemon and connects only through `codex app-server proxy`. That
proxied stream is a WebSocket byte pipe to the daemon's control socket, so the
worker performs the HTTP Upgrade handshake and sends one JSON-RPC message per
text frame (no `jsonrpc` header on the wire): `initialize` with
`capabilities.experimentalApi=true`, `initialized`, exactly one
`thread/read {threadId,includeTurns:true}`, and `turn/steer` with the required
`expectedTurnId` only for the latest nonempty `inProgress` turn. Idle,
not-loaded, policy-capped, raced, and `activeTurnNotSteerable` cases use the
exact queue fallback. Daemon, proxy, initialize, thread-read, and steer
transport failures also use that queue with `fallback_reason=transport_error`;
the steer budget still bounds a silent proxy and kills its whole process
group. A completed JSON-RPC rejection during initialize or thread-read is a
hard delivery error, as is any undocumented steer rejection, malformed frame,
or malformed/mismatched successful response. Only `idle` and `notLoaded` are
clean inactive statuses; system-error/unknown status, missing history, a mismatched thread,
an unknown turn status, or a missing/mismatched returned turn ID indicate
protocol drift. An inactive status paired with an `inProgress` turn is also
rejected as contradictory. These cases are never relabeled as transport fallback.
Fallback diagnostics contain only the controlled phase and reason;
vendor error text, target references, message bodies, and secret-like values
are never copied to listener output. The queue primitive's stdout and stderr
are also discarded; a nonzero exit remains a controlled delivery failure. Any
other `turn/steer` rejection (unknown method, request-shape drift, sub-agent
ownership, internal error) fails the delivery instead of queueing.
Claude delivery
is simple-only and uses two documented print-mode primitives: a local session
UUID target runs `claude -p --resume <session_id>` as a new turn, and a
`session_…`/`cse_…` cloud target runs `claude -p --cloud <session_id>` as a
queued follow-up. The framed body is piped over stdin, a zero exit is the
handoff, and the vendor response is discarded. The Claude session is the
receiver-owned `claude_resume` target (`--kind claude_session`: an encrypted
local session UUID or `session_…`/`cse_…` cloud id); the listener leases that
work with `?delivery=claude_resume` and completes it through
`delivery-complete`, while `--deliver-target` applies only to pre-bus
envelopes. There is no Claude messaging socket or send-to-session command;
a `steer` request (durable `--level steer`, or legacy `--deliver-mode steer`)
falls back to the same simple primitive and records
`fallback_reason=unsupported`. A session started with `--channels` can instead
receive pushes from `paimos serve --mcp-stdio --channel-as`; with a
`claude_channel` target registered, the channel leases and completes each
push's delivery row and leaves rows owned by another adapter alone.
The experimental Grok Build adapter is off unless
`--enable-grok-build-delivery` is present. It resumes only a canonical session
UUID through the first-party `grok` CLI, sends one verbatim turn with tools,
subagents, planning, and web access disabled, and discards the vendor response.
No adapter stores or prints vendor credentials.

### Grok Bot routine wake

Grok Bot has no inbound CLI or mid-turn steer primitive. A routine created
with the "When a webhook fires" trigger shows a POST URL and a sender key on
its trigger card (Grok Bot desktop app). Keep each in its own one-line file
that you own with mode `0600` — the CLI opens both without following symlinks
and refuses group- or world-readable files, symlinks, hard links, directories,
and files owned by another user before reading a byte — and register them as
one encrypted receiver target:

```bash
paimos message target set --project PHAROS --address grok_bot:amy \
  --adapter grok_bot_routine --kind https_webhook \
  --target-ref-file /path/to/routine-url.txt \
  --target-key-file /path/to/routine-sender-key.txt \
  --maximum-level simple
paimos tell grok_bot:amy --project PHAROS --level steer --message "Observation"
```

The exact secret configuration fields are the target registration request's
`target_ref` (the URL; CLI `--target-ref-file`) and `target_secret` (the raw
sender key; CLI `--target-key-file`). Both flags accept `-` for stdin, but
only one per invocation, and neither value is ever accepted as an argument.
The server encrypts them in `agent_message_targets.target_ref_cipher` and
`target_secret_cipher` under separate secretvault domains, never returns
either, and reports only `has_secret`. Every wake POST carries
`Authorization: Bearer <sender key>` — the header the trigger card issues —
next to `Idempotency-Key`. A `grok_bot_routine` target without a sender key
is refused, and a version registered before the key existed blocks with
`target_secret_missing` instead of calling the endpoint. The live server must
also set `PAIMOS_ENV=production`, matching `PAIMOS_DEPLOYMENT_INSTANCE=ppm` and
`PAIMOS_AGENT_BUS_INSTANCE=ppm`, and include the routine hostname in
`PAIMOS_AGENT_BUS_WEBHOOK_HOSTS`. A steer request to this adapter is recorded
as `effective_level=simple` with `fallback_reason=unsupported`; PAIMOS does not
run `grok send`, `grok queue`, or `grok steer`.

Operators can inspect only redacted state with `paimos message target list`
and `paimos message deliveries`. A message committed before a target existed
stays `blocked/target_missing` until the explicit
`paimos message target requeue --address ...` operation; allowing a sender or
registering a target never releases a historical held row as a side effect.

## Which surface should I use?

**TL;DR — one-line decision rule:**

> Pick the one with the **fewest layers between intent and PAIMOS** that still gives you typed errors and multi-line inputs. For most agents, that's the CLI.

### Decision tree

- **You are a human chatting with Claude Desktop / Cursor / similar interactive MCP client** → **MCP**. Lowest friction; Claude discovers verbs automatically and types arguments for you. Token cost doesn't matter when you're talking.
- **You are an agent doing work in a shell** (Claude Code session, a bash script, a CI job) → **CLI**. Lower latency per call than MCP (no LLM round-trip to encode/decode the tool call), cleaner error shapes than raw HTTP, file-based markdown inputs avoid shell-quote foot-guns, and you can compose with `jq` / `xargs` / pipes.
- **You need an endpoint the CLI doesn't wrap, or you're debugging "what does the wire actually look like"** → **REST**. Otherwise, file a ticket against PAI to add the CLI verb — the whole point of PAI-85 was that ergonomics belong in the CLI.
- **You are writing bulk operations** (create-many, batch-update, declarative state across many issues) → **CLI** with `apply` / `batch-update`. MCP exposes one-at-a-time verbs by design (context budget); REST works but is verbose.

### Comparison matrix

| Dimension | MCP (Claude → tool) | `paimos` CLI (via shell) | `curl` / REST |
|---|---|---|---|
| **Latency per call** | slowest — LLM tool encode/decode + HTTP | fast — process spawn + HTTP | fastest — HTTP only |
| **Token cost in chat context** | highest — tool schemas in context, JSON returns through LLM | low — Bash output is plain text | low |
| **Discoverability** | best — typed tool schemas the LLM reads automatically | good — `paimos --help` per verb | poor — must read OpenAPI |
| **Multi-line markdown input** | works (JSON-encoded) | best — `--description-file`, `--ac-file`, etc. | painful — shell-quote hell |
| **Bulk ops** | slow — one call per item by design | best — `apply` YAML, `batch-update` | medium — scriptable but verbose |
| **Error messages** | typed, good | best — paimos CLI normalises shapes | rawest — HTTP status + JSON |
| **Shareable in scripts / CI** | no — only inside an MCP client | yes | yes |
| **Best when** | interactive chat in Claude Desktop / Cursor | agent in a shell, CI jobs, bulk work | one-off debug, endpoint without CLI cover |

### Rules for LLMs reading this doc

1. **In a chat client (Claude Desktop, Cursor)** — call the MCP tools. They show up as `paimos-<instance>.*` namespaces (e.g. `paimos-ppm.*`); use the namespace that matches the instance the user wants.
2. **In a shell session (Claude Code, bash agent, CI)** — shell out to `paimos`. Same machine, same OS-keyring auth, lower token cost, and you can pipe `--json` output into `jq` for follow-up work.
3. **Reach for `curl` only** when (a) the CLI lacks the verb, or (b) you're explicitly debugging HTTP-level behavior. If you find yourself reaching for `curl` more than rarely in normal work, file a CLI gap as a child of [PAI-373](https://pm.barta.cm/issues/PAI-373).
4. **Never mix surfaces in a single task** — if you start with the CLI, finish with the CLI. Swapping mid-flow means duplicated auth setup, inconsistent error handling, and harder reproduction when something fails.

### Caveat — current CLI coverage gaps

The CLI today covers issue-CRUD, cross-project issue move (`issue move`), free-text search, batch issue create via `apply`, time entries, attachments, relations (add only), tag management + assignment, anchors, sync, skills, sessions, project-context, and the unified knowledge plane (`paimos knowledge list|get|create|update|delete|promote` + `paimos knowledge memory bump-refs|stale|proposed-stale`, PAI-394). **It does NOT yet cover** sprints, issue forensics (history/activity), comment delete, or the full issue lifecycle (clone/archive/restore/purge). For those, fall back to REST — full list and tracking under [PAI-373](https://pm.barta.cm/issues/PAI-373) and section 8 below.

> **Extending PAIMOS with a CRM sync provider** (HubSpot, Pipedrive, …)?
> See [`CRM_PROVIDERS.md`](CRM_PROVIDERS.md) for the in-process Go
> plugin interface — `crm.Provider` + the registry — and a worked example.

---

## 1. Get set up

### Install

**macOS (recommended) — signed + notarized universal binary, no Gatekeeper dance:**

```sh
# paimos CLI
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos

# paimos-mcp (MCP server for Claude Desktop etc.)
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos-mcp_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos-mcp
```

The binary is codesigned under "Developer ID Application: Markus Barta (P66J39QV6V)" and notarized by Apple — first run with internet pulls the notarization ticket; no `xattr -dr com.apple.quarantine`, no System Settings approval. See [docs/INSTALL.md](INSTALL.md) for signature/checksum verification.

**Linux:**

```sh
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos_linux_${ARCH}.tar.gz \
  | tar xz -C /usr/local/bin paimos
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos-mcp_linux_${ARCH}.tar.gz \
  | tar xz -C /usr/local/bin paimos-mcp
```

**Build from source (Go 1.25+ required, no signed binary needed):**

```sh
go install github.com/inspr-at/paimos/backend/cmd/paimos@latest
go install github.com/inspr-at/paimos/backend/cmd/paimos-mcp@latest
```

Both binaries end up in `$GOBIN` (default `$HOME/go/bin`). Add it to `PATH` if you haven't.

### Log in

```sh
paimos auth login
# Instance URL [https://pm.barta.cm]: <enter or paste>
# API key (input hidden): <paste>
# ✓ logged in as mba at https://pm.barta.cm
#   saved to /Users/you/.paimos/config.yaml as instance "default"
#   api key stored in OS keyring (service "paimos-cli", account "default")
#   default_instance = "default"
```

The instance URL goes to `~/.paimos/config.yaml` (mode `0600`). The API key goes to the **OS keyring** — Keychain on macOS, Secret Service / KWallet on Linux, Credential Manager on Windows — under service `paimos-cli`, account `<instance-name>`. It is never written to disk in plaintext.

Multi-instance is supported:

```sh
paimos auth login --name ppm       --url https://pm.barta.cm
paimos auth login --name staging   --url https://pm.staging.example.com
```

Use `--instance <name>` on any command to switch, or rely on `default_instance`.

#### Headless / CI

If there's no session keyring available (CI runners, containers, headless Linux without `gnome-keyring` / `kwalletd`), set `PAIMOS_URL` and `PAIMOS_API_KEY` together. They form one env-only target and bypass configured-instance and keyring resolution for the lifetime of the process:

```sh
export PAIMOS_URL="https://pm.barta.cm"
export PAIMOS_API_KEY="paimos_…"
paimos issue list --project PAI
```

For temporary agent credentials, set both URL and key to bypass `~/.paimos/config.yaml` and the keyring completely:

```sh
export PAIMOS_URL="https://pm.barta.cm"
export PAIMOS_API_KEY="paimos_…"
paimos doctor
```

The personal PPM aliases are also supported when sourcing local secret files:

```sh
export PPM_URL="https://pm.barta.cm"
export PPMAPIKEY="paimos_…"
paimos issue list --project PAI
```

Target selection is fail-closed rather than fall-through precedence. Without
`--instance`, exactly one complete env pair (`PAIMOS_URL` + `PAIMOS_API_KEY` or
`PPM_URL` + `PPMAPIKEY`) selects an env-only target; two URL pairs or an
incomplete pair are errors. An explicit `--instance` selects only the named
configured instance and therefore requires both env URL/key pairs to be unset.
A generic `PAIMOS_API_KEY` without `PAIMOS_URL` never applies to a named
configured instance; use `paimos auth login --name <instance>` so credentials
stay scoped to that instance.

#### Log out

```sh
paimos auth logout                       # remove keyring entry for the resolved instance
paimos auth logout --name ppm            # explicit
paimos auth logout --remove-instance     # also drop the URL from config.yaml
```

Idempotent: a missing entry is not an error.

#### Migration from pre-keyring CLIs

If you upgrade from an older `paimos` that stored `api_key:` inline in `config.yaml`, the first invocation moves the key into the OS keyring, rewrites the config without the field, and prints a one-line notice. No manual steps required.

### Verify with `doctor`

```sh
paimos doctor
#   ✓ config       ok — ppm (https://pm.barta.cm) [url=config:ppm, credential=keyring:paimos-cli/ppm]
#   ✓ health       ok
#   ✓ auth         ok — user=mba
#   ✓ schema       ok — version=2.3.0
#   ✓ attribution  ok — agent=(unset) [unset], session=01a04231… [auto]
```

CI-safe; exit 0 all green, 1 warnings only, 2 any failure.

### Enable shell completions

```sh
# fish
paimos completion fish > ~/.config/fish/completions/paimos.fish

# zsh
paimos completion zsh > "${fpath[1]}/_paimos"

# bash
paimos completion bash > /etc/bash_completion.d/paimos
```

After `paimos schema` runs once per instance, flag values like `--status done` get tab-completion from the cached enum list.

---

## 2. Core patterns

### CLI issue refs use keys or explicit `id:<n>`

Use full issue keys in CLI commands: they are stable across instance re-imports
and human-readable in logs. When an internal database ID is genuinely needed,
make that intent explicit with `id:<n>`. Bare numbers are rejected because a
human who means the issue-key suffix `76` and the database row with ID `76`
otherwise produce the same command. The REST and MCP contracts remain
unchanged and continue to accept bare numeric IDs/references (PAI-86).

```sh
paimos issue get PAI-85          # preferred key
paimos issue get id:465          # intentional internal database ID
# paimos issue get 465           # usage error: ambiguous bare number
```

### Multi-line fields are file-first

Inline `--foo "…"` is single-line only **by design**. For markdown (description, acceptance-criteria, notes, close-notes, comments): use `--foo-file path` or `--foo-file -` (stdin).

```sh
# Good
paimos issue create --project PAI --title "Fix paginator" \
  --type ticket --priority high \
  --description-file /tmp/desc.md \
  --ac-file /tmp/ac.md

# Also good — editor-driven
$EDITOR /tmp/desc.md
paimos issue create ... --description-file /tmp/desc.md

# Agent-driven with no temp files — pipe via stdin
cat <<EOF | paimos issue create --project PAI --title "Refactor X" --description-file -
# What
Long multi-line markdown here.
EOF
```

Mixing `--description` + `--description-file` on the same command is a hard error (exit 2). No silent preference.

### Link need-a-host work to Pharos by opaque request ID

Paimos remains the work record. When Pharos owns the corresponding host action,
store only its request ID on the issue:

```sh
paimos issue update PAI-812 \
  --pharos-request-id pharos-create-csb1-1787912345000-1

# Explicitly remove the link.
paimos issue update PAI-812 --pharos-request-id ''
```

The value is an opaque 8–128 character identifier containing only ASCII
letters, digits, `-`, and `_`. Do not pass a URL, credential, host inventory,
or free text. Paimos validates and stores the reference but never checks Pharos,
creates a host, or turns a ticket into a provisioning request. REST and MCP use
the field name `pharos_request_id` with the same semantics.

### Knowledge metadata updates are explicit

`paimos knowledge update <type> <slug>` preserves existing `metadata`
when you update only `--title`, `--body`, `--body-file`, `--status`, or
`--slug`. To replace metadata intentionally, pass a JSON object through
`--metadata` or `--metadata-file`.

```sh
# Body-only edit: existing metadata is preserved.
paimos knowledge update guideline deploy-notes --project PAI --body-file notes.md

# Metadata repair/replacement: explicit JSON object.
paimos knowledge update guideline deploy-notes --project PAI \
  --metadata-file metadata.json
```

### Always pass `--dry-run` first when scripting

Every mutation supports `--dry-run`: the CLI prints the resolved request payload (method, path, body) as JSON and exits 0 without calling the API.

```sh
paimos issue update PAI-83 --status done --close-note-file /tmp/close.md --dry-run
# {
#   "method": "PUT", "path": "/api/issues/PAI-83",
#   "body": {"status": "done"},
#   "close_note_will_comment": "contents of close.md…"
# }
```

### Prefer `--json` for anything that pipes into another tool

```sh
paimos --json issue list --project PAI --status backlog --limit 5 \
  | jq '.issues[] | {key: .issue_key, title}'
```

### Move issues between projects

`paimos issue move <ref>… --to <key|id>` re-homes one or more issues to
another project without close-and-recreate, preserving comments, time
entries, history, tags, and cross-project dependencies. The issue keeps its
id but is re-keyed to the target project's prefix + next number (e.g.
`PAI-690 → OPS-12`); its former key is aliased so existing references still
resolve. Project-scoped structural links (parent, sprint, cost unit, release,
group) that would become cross-project are detached and reported — re-link
them in the target project afterward.

```sh
paimos issue move PAI-690 --to OPS            # single
paimos issue move NIX-12 NIX-15 NIX-19 --to OPS   # bulk reorg campaign
paimos issue update PAI-690 --project OPS     # equivalent single-issue form
```

`--project` on `issue update` cannot be combined with other field edits —
move first, then update. Requires edit access on **both** the source and
target project. Use `--dry-run` to preview the payload.

### Search issues

Use `paimos search` for free-text issue lookup instead of raw
`/api/search` calls. The query can be quoted or passed as trailing words;
`--project` accepts either a project key or a numeric project id.

```sh
paimos search "undo history" --project PAI --type ticket --limit 20
paimos search undo history --project 6

paimos --json search "undo history" --project PAI \
  | jq '.issues[] | {key: .issue_key, type, status, title}'
```

Pretty output is an issue table. `--json` returns the server's full search
payload, including projects, users, tags, and `has_more`.

### Track time

Use `paimos time ...` for ticket-level time tracking. Issue arguments use
keys or explicit `id:<n>` references. Time-entry IDs remain bare numbers
because `time stop|set|get <id>` names that resource unambiguously.

```sh
paimos time start PAI-83 --note "debugging failing deploy"
paimos time stop                         # idempotent no-op when nothing is running
paimos time stop 123                     # explicit timer id

paimos time list --running
paimos time list --recent --limit 5
paimos time list --issue PAI-83

paimos time set 123 --duration 90m --note "corrected"
paimos time get 123 --json
```

`--duration` uses Go duration syntax (`90m`, `1h30m`, `2h`) and stores a
manual hours override. Use `--started-at` / `--stopped-at` only when you
need an explicit timestamp correction.

### Attach files

Use `paimos attach <issue> <file>` for the common screenshot/log/artifact
case. The command uploads the file as pending, links it to the issue, and
rolls the pending attachment back if linking fails.

```sh
paimos attach PAI-83 /tmp/screenshot.png
paimos attach list --issue PAI-83
paimos attach get 42
paimos attach get 42 --download /tmp/screenshot.png
paimos attach rm 42
```

`--json` is supported on every attachment verb. `attach get --download -`
writes raw bytes to stdout, so do not combine that form with `--json`.

### Project metadata

Use `paimos project ...` for project-scoped workspace facts instead of
hand-rolled API calls. Every command accepts a project key or numeric id.

```sh
paimos project show PAI
paimos project repos PAI
paimos project releases PAI
paimos project anchors PAI
paimos project tags PAI

paimos --json project repos PAI | jq '.[] | {label, url, default_branch}'
```

`project show` returns the full project detail payload. The narrower
subcommands expose the common agent lookup surfaces directly: linked repos,
release labels, code anchors, and project taxonomy tags.

### Tag workflows

There are two separate tag flows:

- **Manage the catalog** with `paimos tag ...`
- **Assign existing tags** during `issue create` / `issue update`, or one at a
  time with `paimos issue tag ...`

```sh
# Global catalog
paimos tag list
paimos tag create --name blocked --color red
paimos tag update 42 --name blocked-by-release --color orange
paimos tag delete 42 --yes

# Project taxonomy bootstrap; --project accepts key or numeric id.
paimos tag list --project PAI
paimos tag create --project PAI --name qa --color teal

# Initial assignment; --tags is comma-separated or repeatable.
paimos issue create --project PAI --title "Investigate hsb9" \
  --tags "host:hsb9,home-assistant"

# Non-destructive update; these may be combined with ordinary field flags.
paimos issue update PAI-123 --add-tag "host:hsb9,home-assistant"
paimos issue update PAI-123 --remove-tag home-assistant

# Equivalent single-tag verbs; resolve --tag against /api/tags.
paimos issue tag add PAI-123 --tag qa
paimos issue tag rm  PAI-123 --tag qa
```

Tag names are exact and case-sensitive. These commands assign only tags that
already exist in the global catalog; they never create catalog entries.
`issue update` deliberately has no replace-all flag: `--add-tag` and
`--remove-tag` call the server's idempotent association endpoints, so existing
and concurrently managed tags are not clobbered. The CLI trims, deduplicates,
and resolves the complete requested set with one `/api/tags` read before its
first issue write, and rejects the same name appearing in both operations.

Issue fields and tag associations remain separate REST resources. A combined
create/update can therefore complete its issue write before a later tag request
fails. The error names that boundary and the failed tag; retrying the tag flags
is safe. `--dry-run` performs no catalog lookup and reports names as an
unresolved `tag_changes` plan.

Valid colors are: `gray`, `slate`, `blue`, `indigo`, `purple`,
`pink`, `red`, `orange`, `yellow`, `green`, `teal`, `cyan`.
`paimos tag delete` removes the catalog tag and therefore its existing
issue/project assignments; non-interactive scripts must pass `--yes`.

### Errors exit non-zero and never dump HTML

- Exit **0** success
- Exit **1** API/runtime error — in `--json`, payload is `{"error": "…", "code": 4xx}` (a WAF-returned HTML body is mapped to `"non-JSON response (proxy/WAF?)"`, never echoed)
- Exit **2** usage error (bad flags, missing config, mutually-exclusive options)

---

## 3. End-to-end transcript — a realistic session

This is a lightly edited transcript of a real `paimos` session ticking off one child of this epic. Shell commands are exactly what ran; only output was trimmed for brevity.

```sh
# 1. Start from the ticket.
$ paimos issue get PAI-89
PAI-89  D. Relations: add follows_from / blocks / related
  type:     ticket
  status:   backlog
  priority: low
  …

# 2. Branch + make changes (normal git flow).
$ git checkout -b feat/pai-89-new-relation-types
$ $EDITOR backend/db/db.go  # add migration M67
$ $EDITOR backend/handlers/issues.go  # allowlist update
$ go test ./...
# ... all green

# 3. Commit + open PR (gh, not paimos — complementary tools).
$ git commit -m "feat(relations): follows_from, blocks, related (PAI-89)"
$ gh pr create --title "..." --body "..."
# https://github.com/inspr-at/paimos/pull/15

# 4. CI runs; PR merges; docker image publishes; deploy.
$ ssh user@deploy-host "cd ~/docker && just deploy"

# 5. Smoke check the new behaviour before closing.
$ paimos --json issue get PAI-85 \
  | jq '.description' | head -c 80
# "Sibling to **PAI-29** (context-in) and **PAI-30** …"

# 6. Close the ticket with a close-note — single command.
$ cat > /tmp/pai89-close.md <<'EOF'
Shipped in v1.2.8 — merge 0f0235f.

Three new directional relation types live: follows_from, blocks, related.
Inverse display without a second row via new `direction` field on
GET /api/issues/{id}/relations. Tested live: PAI-84 follows_from PAI-40
— both sides render correctly.
EOF

$ paimos issue update PAI-89 --status done --close-note-file /tmp/pai89-close.md
✓ updated PAI-89 with close note
```

**No temp JSON files. No shell-quoted markdown. No retry-on-404 phantom tickets.** That's the whole point of the epic.

---

## 4. Bulk work

### Bulk status transitions: `batch-update`

Drop a JSONL file, run once. Handles 100 items per transaction; larger files chunk automatically.

```sh
$ cat > /tmp/ops.jsonl <<'EOF'
{"ref": "PAI-101", "fields": {"status": "accepted"}}
{"ref": "PAI-102", "fields": {"status": "accepted"}}
{"ref": "PAI-103", "fields": {"status": "accepted"}}
EOF

$ paimos issue batch-update --from-file /tmp/ops.jsonl
chunk 1: 3 items updated
3 items updated across 1 chunk(s); 0 failed
```

### Batch issue create and scaffolding: `apply`

Declarative YAML plan — create many issues, then optionally update or
relate in one command. For batch issue creation, `apply` is the
canonical CLI path: its `create:` block calls
`POST /api/projects/{key}/issues/batch`, so the server applies the
create rows atomically and supports same-batch `parent_ref` links.
Named refs let children reference a same-plan parent.

```yaml
# plan.yaml
project: PAI
create:
  - name: epic
    type: epic
    title: Q2 refactor
  - name: extract-auth
    type: ticket
    title: Extract auth module
    parent: epic       # → server-side parent_ref: "#0"
  - name: migrate-sessions
    type: ticket
    title: Migrate session store
    parent: epic
relations:
  - source: epic
    type: related
    target: PAI-40
```

```sh
$ paimos apply --from-file plan.yaml
✓ created 3 issues
✓ added 1 relations
```

For “file 50 tickets at once,” generate the same YAML shape from your
script and keep each batch at 100 `create:` rows or fewer. Put large
multi-line markdown in a literal block instead of shell-quoted JSON:

```yaml
project: PAI
create:
  - type: ticket
    title: Import retry should back off
    description: |
      ## Problem

      Remote 429s currently retry too aggressively.
    acceptance_criteria: |
      - Backoff is exponential
      - Test covers 429 retry timing
  - type: ticket
    title: Import retry emits structured log
```

Use `paimos apply --from-file plan.yaml --dry-run` before sending.
Dry-run parses and echoes the plan without touching the API.

**Not idempotent in v1** — running twice duplicates. After scaffolding, use `ensure-status` / `batch-update` for subsequent changes:

```sh
$ paimos issue ensure-status PAI-101 done
✓ PAI-101 already done       # no-op, exit 0
```

---

## 4a. Agent sessions, skills, sync, onboard (PAI-325 → PAI-340)

The v2.8.x cycle introduced five sibling verbs that wire `paimos` into
an agent's lifecycle: `session`, `skill`, `sync`, `onboard`, and
`memory propose`. Run `paimos <verb> --help` for the full flag set —
this section captures the integration shape, not the per-flag docs.

### Friendly owned starts (PAI-921)

A **canonical agent** is a project-owned persona and its instructions. A
**worker** is one running, daemon-owned generation of that agent, with an
explicit public session ID, project, role, parent, ticket and work shape.
`session start` remains attribution-only; existing `harness`, `orchestrator set`,
and `paimos-agentd` commands keep their low-level behavior.

Start the instance coordinator with project/agent keys and an immutable profile:

```sh
paimos --instance my-instance orchestrator start --project ACME26 --agent ops \
  --profile codex-sol-high@2 --workspace /path/to/clean/coordinator-worktree \
  --idempotency-key coordinator-first-start

paimos --instance my-instance worker start --project ACME26 --agent builder \
  --ticket ACME26-42 --work-shape ship --parent codex:ops \
  --profile codex-sol-high@2 --workspace /path/to/clean/worker-worktree \
  --idempotency-key worker-acme26-42
```

Workers may also use `--role build` (or `scout`, `mechanical`, `build-hard`,
`review-gate`) to resolve a model from instance policy; `review-gate` requires
`--author-family`. See [model roles and offline cache](AGENT_INTEGRATION.md#model-roles-and-instance-policy-pai-1049).

The named instance must match the enforced deployment identity, or supply
`--expect-deployment-instance`. The coordinator command reads the instance
binding and writes with revision CAS; `--expected-revision` adds an explicit
revision check. The instance root has no fabricated cross-project parent.
Workers accept a public parent UUID or a unique `harness:agent` handle in the
same project. Missing, ambiguous, stopping or terminal parents fail before spawn.

Use `--guided` to fill missing inputs, or `--non-interactive` to forbid prompts.
`--dry-run` and `--explain` run the same resolver and daemon/workspace checks
without writes or spawn. Preview does not reserve a workspace or guarantee
that later execution succeeds. `--json` emits the sanitized plan/outcome.
Canonical instructions enter the child prompt in memory; optional task text
comes from `--prompt-file`. Neither is printed or retained in retry records.

Instead of `--profile ID@version`, select an exact compatible combination of
`--harness`, `--model` and `--effort`. Zero or multiple matches list supported
profile choices and require more specific input. Explicit profiles and selectors
must agree. `--account local_probe` and `--machine authenticated_reporter`
select the supported provenance sources. `--account chatgpt` (or another closed
class label) still pins only that class. Any other safe `--account` value is an
opaque operator registry key; it never carries a path, env, executable, or
credential. Pinning a class label, named-account key, or machine ID requires
matching daemon pre-spawn constraint support; older runtime contracts reject
these constraints explicitly instead of guessing their values.

The workspace must be clean and available for exclusive ownership. The command
never cleans, creates or switches a repository worktree. The daemon remains the
final authority for reservation and spawn. `--state-root` locates a fixture or
non-default private daemon root; its default matches `paimos-agentd`.

Always reuse the same `--idempotency-key` for an exact retry. Private durable
records bind it to the selected instance origin and input digest. Completed
retries return the original outcome, even if the parent has since stopped.
Changed input with the same key fails closed. An interrupted CLI or lost daemon
response leaves an **unknown** outcome and never automatically respawns. Inspect
`paimos runtime doctor` and `paimos harness list --project <key>` before issuing a
new key; retain retry records until that uncertainty is reconciled.

Success requires the reporter's public session ID to match the server's managed
session and assignment. Output includes the observed generation state and
copyable status/message/steer/interrupt/stop commands for supported capabilities.
Message and steer commands submit canonical message deliveries through the separate
runtime-owned receiver target/listener. Typed browser lifecycle intents remain a
distinct control path; public registration alone does not prove delivery readiness.
Sender registration and the exact receiver allowlist are separate readiness layers
from generation start and from optional fallback; start does not infer them from a
parent or PID, and it does not grant a sender.
A local ID or vendor session reference never substitutes for a public session ID.
`--wait` bounds registration waiting (default 10s, maximum 30s); missing evidence
returns `unknown` and a nonzero exit, and a terminal generation is not reported
as a successful start. A start is not evidence of ticket completion.

### `paimos session start` (PAI-325)

Mints a session UUID and emits env-var assignments that subsequent
`paimos` calls in the same shell consume — every write request stamps
`X-Paimos-Agent-Name` and `X-Paimos-Session-Id` headers so the
mutation log can answer "which agent did this in which session?":

```sh
eval $(paimos session start --project ACME26 --agent ops)
# PAIMOS_AGENT_NAME / PAIMOS_SESSION_ID now exported
paimos session show                    # echo the current values
eval $(paimos session end)             # clear the env
```

`--bundle full` resolves the project's canonical context bundle
(PAI-340) and prints it alongside the env exports — agents that open
with this verb get the project briefing for free.

### `paimos skill render` + `paimos sync` (PAI-329 / PAI-331 / PAI-332)

The canonical agent artifact at `GET /projects/:id/agents/:name.json`
is harness-agnostic. `paimos skill render` runs that artifact through
a registered adapter (built-in `claude-code` plus anything on
`$PAIMOS_ADAPTER_PATH`) and produces the file your harness consumes
(e.g. `.claude/commands/<name>.md`). Rendered files carry a
paimos-managed header so drift can be detected later.

```sh
paimos skill list-adapters             # what harnesses are wired up
paimos skill render ops                # one-shot
paimos sync watch --kind=skill         # subscribe (SSE) and re-render on change
paimos sync check                      # CI-friendly drift report (exit 1 = drift)
```

`sync` is the generic engine; `skill` aliases provide a friendlier
verb shape today. PAI-341 will register more `--kind` values (memory,
runbook, …) so the same verbs cover the knowledge plane.

### `paimos onboard` (PAI-340)

Single readable briefing for a project (or a specific agent within it)
— overview + related projects + external systems + top guidelines +
recent context, plus the agent's `body` / `bootstrap_steps` /
`non_negotiable_rules` when `--agent <name>` is passed. Markdown by
default; `--format html` for self-contained HTML.

The output is prefixed with a drift-detection header. `--check`
compares an on-disk briefing against the current canonical bundle and
exits non-zero on drift — drop it in CI to fail the build when
onboarding docs go stale.

### `paimos memory propose` (PAI-349)

Drafts a memory entry in `proposed` status pending operator review.
The draft surfaces in the Knowledge tab's "Proposed" inbox; accept /
edit / reject from there.

The propose verb honours two server-side gates (see
`PAIMOS_PROPOSE_LIMIT_PER_SESSION` and `PAIMOS_PROPOSE_DISABLED` in
`CONFIGURATION.md`). 429 means the per-session cap tripped; 503 means
the operator turned the verb off instance-wide.

## 5. Schema discovery

The server publishes a single source of truth at `GET /api/schema`. The CLI caches it (`~/.paimos/schema-<instance>.json`) and uses it for tab-completions.

```sh
$ paimos schema
instance: ppm (https://pm.barta.cm)
version:  2.1.0
enum priority: low, medium, high
enum project_status: active, frozen, archived, deleted
enum relation: parent, cost_unit, release, groups, sprint, depends_on, impacts, follows_from, blocks, related, applies_to_memory
enum status:   new, backlog, in-progress, qa, done, delivered, accepted, invoiced, cancelled
enum type:     epic, cost_unit, release, sprint, ticket, task

$ paimos schema --refresh       # re-download + report if version moved
```

The schema includes **recommended** status transitions — enforced client-side as hints, the backend still accepts any→any so you can fix mistakes without ceremony.

### Project creation + api-key scopes (PAI-379)

`/api/schema` also exposes a `scopes` block — the catalog of named
api-key scopes that narrow what a key can do. Today there's one:

```json
{
  "name": "projects:write",
  "required_role": "admin",
  "endpoints": ["POST /api/projects"],
  "description": "Create new projects. Combined with the existing admin-role gate on the endpoint."
}
```

**Policy.** Scopes only **narrow**. They never let a caller do
something their role couldn't already do. `POST /api/projects` still
requires admin role; for api-key auth, the key must additionally carry
`projects:write`. A member cannot conjure a `projects:write` key —
the catalog says it requires admin role at issue-time. Session-cookie
auth (the browser SPA) is never narrowed.

**Bootstrap a project as an agent.**

```sh
# Admin logs in to the web UI, goes to Settings → API Keys, ticks
# "projects:write" on a new key, gives it to the agent.
export PAIMOS_API_KEY="paimos_..."
export PAIMOS_URL="https://pm.barta.cm"

paimos project create --name "My new project" --key MYP
# or, from Claude Desktop:
#   paimos_project_create(name="My new project", key="MYP")
```

The narrowed key cannot do anything else — it can't create issues, list
private boards, etc. It is exactly "create projects, and only that".

### Project lifecycle (PAI-754)

Project selection defaults to live work: `GET /api/projects` and
`paimos project list` return `active` projects only. Explicit project
references still resolve frozen and archived projects so existing work remains
reachable.

```sh
paimos project list --all
paimos project update GSC26 --status archived
paimos project update AGM --status frozen
```

`active` accepts new issues. `frozen` keeps existing issues editable but
rejects direct, batch, clone, intake, and move-in creation with `409` and a
message naming the state. `archived` is retired and rejects new issues;
`deleted` remains the soft-delete trash state. Agents must not work around the
gate by writing directly to storage or choosing another project.

---

## 6. MCP integration

### External delivery-stage handoffs (PAI-810)

Registered Pharos and Janus adapters use the first-class
`paimos external-stage` command instead of hand-rolled HTTP. The command covers
internal create/mint/rotate/revoke and external pull/accept/report operations
against the frozen v1 vendor media types.

Provisioning is an authenticated, audited Agent Mode admin plane outside the
frozen adapter route family. `external-stage registrations list` discovers only
current non-revoked safe registration IDs; `registrations create|revoke` and
`prerequisites seal` perform the supported mutations with CLI-generated
canonical idempotency keys. Use the returned exact `registration_id` for
handoff creation.
Never guess IDs or provision with direct SQL.

Prerequisite sealing is explicit: repeat
`--prerequisite required:dependency=registration-id` or
`--prerequisite optional:dependency=registration-id` up to 16 times. There is
no implicit mode. Omitting the flag seals an intentional empty set; only
required rows gate owner success, while optional rows remain advisory
dependency facts.

The independent 32-byte handoff credential never enters argv, an environment
variable, JSON, a URL, or CLI output. Mint/rotate require
`--secret-output <new-path>`, create it as `0600` without overwriting, and
durably stream exactly 32 raw response bytes. Pull/accept/report require exactly
one of `--secret-file <owner-only-path>` or `--secret-stdin`; the CLI sends its
unpadded base64url form only as `X-PAIMOS-Handoff-Secret`.

```sh
paimos --json external-stage registrations list issue:4664
paimos --json external-stage registrations create issue:4664 \
  --api-key-id "$PHAROS_API_KEY_ID" --class pharos --role owner \
  --workflow deploy-production --environment production-eu1

paimos external-stage mint "$HANDOFF_ID" \
  --expected-credential-epoch 0 --secret-output /run/credentials/handoff.bin
paimos external-stage pull "$HANDOFF_ID" \
  --secret-file /run/credentials/handoff.bin
paimos external-stage report "$HANDOFF_ID" \
  --secret-file /run/credentials/handoff.bin --report-file report.json
```

A lost or ambiguously captured mint/rotate response is unrecoverable: rotate
before use. There is no raw-secret stdout mode. Strict report decoding rejects
unknown fields and invalid closed evidence locally. See
[`EXTERNAL_STAGE_CONTRACT.md`](EXTERNAL_STAGE_CONTRACT.md) for the fixture
digest, contract pins, exact cross-stage verification rule, and safe examples.

### Claude Desktop

Add `paimos-mcp` to your MCP servers config:

```json
{
  "mcpServers": {
    "paimos": {
      "command": "/Users/you/go/bin/paimos-mcp",
      "env": { "PAIMOS_INSTANCE": "ppm" }
    }
  }
}
```

Tools available in the current allowlist:

| Tool                  | Notes |
|----------------------|-------|
| `paimos_schema`       | Call before choosing enum values |
| `paimos_retrieve`     | mixed project-context retrieval with fusion metadata |
| `paimos_graph`        | typed entity-graph traversal |
| `paimos_blast_radius` | grouped impact traversal for one issue |
| `paimos_search`       | global PAIMOS search endpoint |
| `paimos_issue_get`    | by key or id |
| `paimos_issue_list`   | `project_key`, `status`, etc. |
| `paimos_issue_create` | title + project_key required |
| `paimos_issue_update` | partial by ref |
| `paimos_relation_add` | all 7 types |
| `paimos_project_list` | list projects on the active instance |
| `paimos_project_create` | name + key required; api-key needs `projects:write` scope on an admin account |
| `paimos_report_progress` | append allowlisted facts about one existing run; requester/claimer/admin |

**Deliberately not exposed**: `batch-update`, `apply`. MCP context grows fast; agents that need bulk should shell out to the `paimos` CLI instead.

For shell agents, `paimos run report [run-id]` is the equivalent first-class
CLI verb. It accepts stable correlation/provider/adapter identity, monotonic
sequence, heartbeat, phase, bounded activity, needs-input/blocker state, and
optional evidence-backed progress/ETA. See [Agent integration](AGENT_INTEGRATION.md#single-run-telemetry-pai-799)
for the exact contract and privacy boundary.

### Local repo broker

For coding agents inside a checkout, `paimos serve` can expose local repo
context alongside authenticated PAIMOS retrieval:

```bash
paimos serve --project PAI --repo-root . --mcp-stdio
```

MCP tools exposed by this stdio server:

| Tool | Notes |
|------|-------|
| `paimos_repo_state` | branch, HEAD, dirty counts, `AGENTS.md`, anchor index |
| `paimos_local_search` | bounded fixed-string repo search |
| `paimos_local_read` | bounded line-range file read; blocks traversal/symlink escape and secret-prone files |
| `paimos_symbol_search` | regex fallback declaration search; does not claim LSP |
| `paimos_local_retrieve` | remote `/retrieve` plus local search/symbol hits |
| `paimos_pack_context` | bounded issue/query context bundle for an agent prompt |

HTTP mode is also available for custom clients:

```bash
paimos serve --project PAI --repo-root . --addr 127.0.0.1:8765
```

The broker is read-only and loopback-only by default. Repo-derived
content is marked `untrusted_data` because an agent must treat local code
and docs as prompt input, not instructions.

Claude Code can opt into the experimental channel capability while launching
the stdio broker:

```bash
paimos serve --project PAI --repo-root . --mcp-stdio --channel-as claude:claude
```

This adds `capabilities.experimental["claude/channel"]` and emits
`notifications/claude/channel` after initialization. The option changes only
message read/cursor state; it does not grant repo or PAIMOS mutation tools.
See Anthropic's [channels reference](https://code.claude.com/docs/en/channels-reference)
and [channels guide](https://code.claude.com/docs/en/channels).

---

## 7. Failure modes (explicit)

PAIMOS deliberately makes the "what happens when X breaks" contract explicit so agents can trust it.

- **Soft-deleted issues**: status/trash-aware endpoints return 404. Key resolution (`ResolveIssueRef`) still finds them so `restore` / `purge` can target trashed tickets by key.
- **Missing issue**: 404 `{"error": "not found"}`. Never a misleading 200 with `null` body.
- **Malformed ref**: 400 `{"error": "invalid id"}`. Distinct from 404 so agents can distinguish "garbage input" from "nothing matched".
- **Server-side validation failure in a batch**: the whole batch rolls back, response is 400 with `{"errors": [{index, ref?, error}], "rolled_back": true}`. No half-commits.
- **Session audit**: on by default. Set `PAIMOS_AUDIT_SESSIONS=false` (or `0`) on the backend to opt out. Every mutation is recorded with the CLI's auto-generated UUIDv7 `X-PAIMOS-Session-Id` when present, so you can replay "what did my agent do?" via `GET /api/sessions/:id/activity`. Calls without the header remain valid and are recorded with a null session id.
- **Schema version drift**: the server bumps `SchemaVersion` on any enum / transition / field change. `paimos schema --refresh` reports whether the server moved vs. your local cache.

---

## 8. When to drop down to REST

Most of the time you won't need to. Reach for the REST API directly when:

- You're writing code in a language where Go's `paimos` CLI isn't convenient (even then, shelling out is often faster than pulling in an HTTP client).
- You need an endpoint the CLI doesn't wrap yet (see "Known CLI gaps" below).

If a pattern feels awkward via CLI but natural via REST — **file a ticket** under [PAI-373](https://pm.barta.cm/issues/PAI-373) (the agent-CLI surface-gap epic) or as a sibling of PAI-85. The whole point of those epics is that agent-facing ergonomics belong in the CLI.

### Known CLI gaps (current v3.2.x audit, tracked under PAI-373)

The tier-1 PAI-373 gaps are now covered:

| Domain | Status | Tracked under |
|---|---|---|
| **Search** — free-text issue lookup | ✅ `paimos search` | [PAI-376](https://pm.barta.cm/issues/PAI-376) |
| **Tag management** — list/create/update/delete | ✅ `paimos tag ...` plus `paimos issue tag ...` | [PAI-377](https://pm.barta.cm/issues/PAI-377) |
| **Batch issue create** | ✅ `paimos apply` create block | [PAI-378](https://pm.barta.cm/issues/PAI-378) |
| **Time entries** — start/stop/list/edit | ✅ `paimos time ...` | [PAI-374](https://pm.barta.cm/issues/PAI-374) |
| **Attachments** — upload + link | ✅ `paimos attach ...` | [PAI-375](https://pm.barta.cm/issues/PAI-375) |

Remaining REST fallbacks are tier-2/admin surfaces:

| Domain | Status | Tracked under |
|---|---|---|
| **Issue forensics** — history/activity/AI activity | REST only | PAI-373 follow-up |
| **Sprints** — full lifecycle | REST/UI only | PAI-373 follow-up |
| **Relations** — list/remove | partial CLI (add only) | PAI-373 follow-up |
| **Knowledge plane** — full CRUD + memory subroutes | shipped via `paimos knowledge` (PAI-394) | — |
| **Issue lifecycle** — clone/archive/restore/purge | REST/UI only | PAI-373 follow-up |

If you find yourself reaching for `curl` outside this list, that's a new gap — file it as a child of PAI-373.

---

## See also

- [`api-minimal.md`](api-minimal.md) — REST reference, canonical.
- [`DEVELOPER_GUIDE.md`](DEVELOPER_GUIDE.md) — PAIMOS dev setup.
- [`DATA_MODEL.md`](DATA_MODEL.md) — schema + entity relationships.
- [`CHANGELOG.md`](CHANGELOG.md) — recent-features / version history.
