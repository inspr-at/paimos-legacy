# Installing the PAIMOS CLI

The `paimos` CLI (and its siblings `paimos-mcp` and `paimos-agentd`) ships as a signed,
notarized universal binary for macOS and unsigned tarballs for Linux.
A new release lands on every `v*` git tag (PAI-99); the
`releases/latest/` URL always points at the most recent one.

---

## macOS — one-liner (recommended)

```bash
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos
```

Or, pinned to a version:

```bash
VER=260921101051.0.0
curl -fL https://github.com/inspr-at/paimos/releases/download/v$VER/paimos_${VER}_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos
```

The binary is **codesigned + notarized** under "Developer ID
Application: Markus Barta (P66J39QV6V)". Gatekeeper accepts it on
first run; no `xattr` dance, no System Settings approval. First run
requires an internet connection so macOS can fetch the notarization
ticket from Apple — after that it works offline.

Verify the signature manually if you want:

```bash
spctl --assess -vv /usr/local/bin/paimos
# → /usr/local/bin/paimos: accepted
# → source=Notarized Developer ID
# → origin=Developer ID Application: Markus Barta (P66J39QV6V)
```

For `paimos-mcp` (the MCP server), substitute `paimos-mcp` everywhere:

```bash
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos-mcp_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos-mcp
```

Install the operator-local worker supervisor separately on machines that run
managed Codex or Claude children:

```bash
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos-agentd_darwin_universal.tar.gz \
  | tar xz -C /usr/local/bin paimos-agentd
```

Run one daemon per PPM instance. The instance value partitions both private
state and the Unix socket; prompts and control text are accepted on stdin, not
argv. `paimos-agentd` owns either a documented Codex app-server stdio child or
a documented Claude Agent SDK streaming Query, inherits the operator's local
CLI login, and never accepts a vendor token:

```bash
PROJECT_ID="$(paimos --json project show PAI | jq -er '.id')"
REPORT_HOST=worker-host
REPORT_URL=https://paimos.example.com
REPORT_API_KEY_FILE=/absolute/path/to/owner-only-api-key
paimos-agentd serve --instance production \
  --report-host "$REPORT_HOST" --report-url "$REPORT_URL" \
  --report-api-key-file "$REPORT_API_KEY_FILE"
printf '%s' 'Implement the assigned ticket.' | paimos-agentd start \
  --instance production --adapter codex --workspace "$PWD" \
  --project-id "$PROJECT_ID" --identity codex:worker \
  --dispatch-profile codex-sol-high --dispatch-profile-version 1
paimos-agentd status --instance production
```

The profile ID and exact version must exist in the authenticated
`/api/ai/execution-options?dispatch_only=1` catalog. Agentd rejects drift before
spawn and records the resolved profile plus canonical exclusive-workspace
provenance on the durable harness session. Legacy starts may omit the profile;
their optional execution axes remain unknown. Shared workspaces require both
`start --workspace-mode shared` and the separately deliberate
`serve --allow-shared-workspaces` authorization.

During a rolling upgrade, recovered pre-profile agentd sessions omit the new
execution fields when reporting to an older server. Upgrade the server before
starting newly profiled workers; their workspace and profile contract is
intentionally fail-closed rather than silently downgraded.

Continue with the [Agent Intercom runbook](AGENT_INTERCOM.md) before registering
targets or starting listeners. It documents the required steer primary plus
simple fallback, redacted diagnostics, per-generation worker-lease boundary,
and restart recovery without publishing local capabilities. When durable
reporting is enabled, keep its shared Paimos API key in a separate protected
credential file; agentd independently stores each generation lease in its
instance-scoped owner-only state and never passes either secret in argv.
The reporting trio is all-or-none; omit all three for local-only status. The
daemon rejects non-loopback HTTP, redirects, unsafe credential custody, and a
failed authenticated preflight. Use an absolute `--paimos-path` only when the
reporting CLI is not discoverable on `PATH`.

Claude owned sessions additionally require Node.js 18+, Claude CLI 2.1.251 or
newer, and the operator-installed Agent SDK at exactly 0.3.251. Install the SDK
deliberately; `paimos-agentd` never downloads it and its release tarball contains
only the `paimos-agentd` binary:

```bash
npm install -g @anthropic-ai/claude-agent-sdk@0.3.251
CLAUDE_SDK_PATH="$(npm root -g)/@anthropic-ai/claude-agent-sdk/sdk.mjs"
shasum -a 256 "$CLAUDE_SDK_PATH"
# expected: 9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93
paimos-agentd serve --instance production \
  --claude-sdk-path "$CLAUDE_SDK_PATH"
```

The daemon also validates the adjacent package version, Node version, Claude
CLI version, documented streaming Query methods, and
`interrupt_receipt_v1`. Missing or incompatible capabilities fail Claude
session startup with an actionable diagnostic; Codex sessions remain
available. Override executable discovery only with absolute `--node-path` and
`--claude-path` values. The default Claude tool boundary is
`Read,Glob,Grep,Edit,Write`; durable messages never grant Bash.

Cursor owned sessions require the pinned operator CLI `2026.09.02-c22c1a3` and
documented `agent acp` stdio. Optional `--cursor-path` pins that executable;
otherwise `cursor-agent` on `PATH` is resolved once. Named starts require
`--cursor-accounts` mapping opaque keys to expected email/userId; the daemon
never copies auth files, never sets `HOME`, never passes `--api-key`, and never
calls `login` / `authenticate` as part of ordinary start. Catalog models are
included Composer (`composer-2.5`) and Grok (`grok-4.6` with acknowledged high)
only.

---

## Linux

Same shape, no signing (Linux has no Gatekeeper-equivalent):

```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fL https://github.com/inspr-at/paimos/releases/latest/download/paimos_linux_${ARCH}.tar.gz \
  | tar xz -C /usr/local/bin paimos
```

Substitute `paimos-agentd` for `paimos` to install the Linux supervisor
artifact on an operator workstation.

---

## Verify checksums

Every release ships a `sha256sums.txt` next to the tarballs:

```bash
VER=260921101051.0.0
curl -fLO https://github.com/inspr-at/paimos/releases/download/v$VER/sha256sums.txt
curl -fLO https://github.com/inspr-at/paimos/releases/download/v$VER/paimos_${VER}_darwin_universal.tar.gz
shasum -a 256 -c sha256sums.txt --ignore-missing
```

---

## Build from source

If you have Go 1.25+ and don't need the signed binary (e.g., on a
Linux server, in CI, or for a contribution):

```bash
go install github.com/inspr-at/paimos/backend/cmd/paimos@latest
go install github.com/inspr-at/paimos/backend/cmd/paimos-mcp@latest
go install github.com/inspr-at/paimos/backend/cmd/paimos-agentd@latest
```

The Nix flake at [`pkgs/paimos-cli`](https://github.com/markus-barta/nixcfg)
also builds from source declaratively.

---

## After install — first-use checklist

```bash
paimos --version    # 260921101051.0.0
```

### 1. Log in

```bash
paimos auth login
# Instance URL [https://pm.barta.cm]: <your PAIMOS host>
# API key (input hidden): <paste a key you generated in Settings → API Keys>
# ✓ logged in as <you> at <url>
#   saved to ~/.paimos/config.yaml as instance "default"
#   api key stored in OS keyring (service "paimos-cli", account "default")
```

The instance URL goes to `~/.paimos/config.yaml` (mode `0600`). The
API key goes to your **OS keyring** — Keychain on macOS, Secret
Service / KWallet on Linux, Credential Manager on Windows — under
service `paimos-cli`, account `<instance-name>`. It is never written
to disk in plaintext.

> **Upgrading from ≤ 5.6:** `paimos auth login --api-key …` was removed
> (a credential in process arguments lands in `ps` output and shell
> history). Workstations use the hidden prompt above; automation sets
> `PAIMOS_URL` and `PAIMOS_API_KEY` together as one runtime-only target — there is deliberately
> no way to pass a credential via argv. Any legacy `api_key:` field
> still in `config.yaml` is migrated into the keyring on the next configured
> CLI run. On keyring-less machines the CLI leaves the only copy in place and
> fails with instructions to use the complete `PAIMOS_URL` + `PAIMOS_API_KEY`
> env-only target and remove the legacy field manually.

### 2. Try a read-only command

```bash
paimos issue list --project PAI --limit 5
paimos issue get PAI-1
```

If those work, the CLI is wired up correctly.

### 3. Common patterns

```bash
# Multi-line markdown — no shell-quoted-JSON foot-gun
paimos issue create --project PAI --type ticket \
  --title "Refactor auth middleware" \
  --description-file /tmp/desc.md \
  --ac-file /tmp/ac.md \
  --tags "backend,auth"

# Idempotent status transitions — safe to re-run
paimos issue ensure-status PAI-83 done

# Update + close-note in one atomic-ish step
paimos issue update PAI-83 --status done \
  --close-note-file /tmp/closing-note.md

# Search, then tag the issue you found
paimos search "flaky session" --project PAI --limit 5
paimos tag list
paimos issue update PAI-83 --add-tag backend

# Track work without leaving the terminal
paimos time start PAI-83 --note "Investigating session expiry"
paimos time stop
paimos time list --issue PAI-83

# Attach a screenshot or generated artefact to the ticket
paimos attach PAI-83 /tmp/screenshot.png
paimos attach list --issue PAI-83

# Machine-readable output for shell pipelines
paimos --json issue list --project PAI --status backlog
```

### Multi-instance / headless

```bash
# Multiple PAIMOS instances
paimos auth login --name ppm       --url https://pm.barta.cm
paimos auth login --name staging   --url https://pm.staging.example.com
paimos --instance ppm issue list   # switch per-command

# CI / containers (no OS keyring available; URL and key are one target)
export PAIMOS_URL="https://pm.barta.cm"
export PAIMOS_API_KEY="paimos_<your-key>"
paimos issue list --project PAI

# Fully bypass config + keyring (temporary agent credentials)
PAIMOS_URL=https://pm.barta.cm \
PAIMOS_API_KEY=paimos_<your-key> \
  paimos issue list --project PAI
```

---

## Deeper guides

The above covers install + auth + the everyday verbs. For the full
agent-driving surface (bulk operations, declarative `apply` YAML, MCP
integration with Claude Desktop, REST fall-back patterns), see:

- [docs/AGENT_INTERFACE.md](AGENT_INTERFACE.md) — the comprehensive CLI guide
- [docs/AGENT_INTEGRATION.md](AGENT_INTEGRATION.md) — REST integration patterns
- [docs/api-minimal.md](api-minimal.md) — REST reference
