# PAIMOS — Adapter Protocol (PAI-332)

> **Who this is for.** Authors of harness adapters that turn the PAIMOS canonical agent artifact into a skill / commands file for a specific harness (Claude Code, Codex, Grok, Pi, Cursor, or external harnesses). Per the INSPR thesis: *"None is the foundation of the platform. Each adapter is welcomed; none is owed."*

This document defines the stable, versioned contract every adapter follows. Implement it once, and your adapter slots into `paimos skill render`, the discovery walk, the public registry, and the conformance suite without touching paimos itself.

The contract is **protocol_version `1`**. Breaking changes will bump the major; additive changes are fair game on the same major.

---

## 1. The shape of the world

```
+---------------+    canonical artifact JSON     +-------------+    rendered file
|  paimos       |  ───────────────────────────>  |  adapter    |  ───────────────>
|  /api/.../    |  via stdin or in-process       |             |  via stdout or
|  agents/...   |                                |             |  in-process
|  .json        |  <───────────────────────────  |             |
+---------------+    suggested path (optional)   +-------------+
```

- **paimos** owns the canonical artifact (PAI-329) and the dispatch layer (`paimos skill render`).
- **The adapter** owns one transform: canonical artifact → harness-specific file.
- The **drift-detection header** on the rendered file is paimos's responsibility — adapters never emit it.

---

## 2. Manifest format

Every adapter ships a v1 manifest. Format: **JSON**.

### Why JSON, not TOML

The ticket left the choice open. We picked JSON because:

- The canonical artifact the adapter consumes is JSON. One syntax across the whole pipeline = one parser, one mental model.
- `paimos-adapter-<name> describe` emits a manifest on stdout. Choosing JSON means in trivial cases the binary can literally `cat paimos-adapter.json`, and external tooling parses one shape (registry, `describe`, on-disk).
- TOML is friendlier for handwritten config but adds a Go dep, two parsers, and a translation layer between disk and wire — net negative for an SDK boundary.

### Canonical shape

```json
{
  "protocol_version": "1",
  "name": "claude-code",
  "version": "1.0.0",
  "supports": ">=1.0.0 <2.0.0",
  "description": "Claude Code skill markdown adapter.",
  "target_path_template": "{workspace}/.claude/commands/{slash_command_name}.md",
  "input_format": "json",
  "output_format": "markdown"
}
```

| Field | Required | Description |
| --- | --- | --- |
| `protocol_version` | yes (defaulted) | Manifest schema version. `"1"` today. Mismatches are hard errors. |
| `name` | yes | Registry key. Matches `--harness <name>`. Lowercase, hyphen-separated. |
| `version` | recommended | Adapter SemVer. Bump on any output-format change. |
| `supports` | recommended | Bosun-style canonical-artifact range, e.g. `">=1.0.0 <2.0.0"`. paimos's dispatcher rejects mismatches with a clear error before calling render. |
| `description` | recommended | One-line CLI help string. |
| `target_path_template` | recommended | Output path with `{token}` substitution (see below). |
| `input_format` | optional | `"json"` (only value supported today). |
| `output_format` | optional | Hint for file extension: `"markdown" \| "json" \| "yaml" \| "text"`. |

### Path-template tokens

| Token | Source |
| --- | --- |
| `{workspace}` | The user's `--workspace` (default cwd). |
| `{slash_command_name}` | `agent.slash_command_name`, falling back to `agent.name`. |
| `{agent_name}` | `agent.name`. |
| `{project_key}` | `project.key`. |

### Versioning + compat (Bosun-style SemVer)

`supports` is a space-separated AND of clauses (`>=1.0.0 <2.0.0`). Each clause is `<op><M.m.p>`; pre-release tags are tolerated and ignored. paimos refuses to dispatch when the canonical artifact's `canonical_schema_version` falls outside the range, with a clear error that names the adapter and the offending version.

---

## 3. Execution contract

External adapters are standalone executables on disk. Naming convention: `paimos-adapter-<name>`. They MUST implement three verbs:

```sh
paimos-adapter-<name> render --input -            # stdin: canonical, stdout: rendered
paimos-adapter-<name> describe                    # stdout: manifest as JSON
paimos-adapter-<name> validate --input -          # exit 0 iff input is consumable
```

Rules:

- **stdin** receives the canonical artifact JSON exactly as paimos would emit. No envelope, no framing.
- **stdout** carries the rendered file body for `render`, the manifest for `describe`. UTF-8, no BOM. No header line — paimos injects the drift-detection header itself.
- **stderr** carries human-readable diagnostics. On non-zero exit, the **first non-empty line** of stderr is folded into paimos's error message — keep it short and actionable.
- **Exit codes**: `0` on success, non-zero on any failure. paimos does not inspect specific codes today; treat any non-zero as "this attempt failed".
- **Timeout**: paimos kills the subprocess after 30s wall clock. Adapters needing longer should rethink the design.
- **No side effects**. The adapter must be a pure transform — no network, no filesystem writes, no mutation of the input. paimos does the writing.

### `render`

- Read all of stdin. Parse the canonical JSON. Produce the rendered body on stdout.
- Adapters never emit the paimos `<!-- paimos: rendered from … -->` header — paimos's dispatch layer prepends it (PAI-331 reads it for drift detection).
- The suggested output path is computed by paimos from `target_path_template`; the adapter does not need to surface a path.

### `describe`

- Emit the v1 manifest as JSON to stdout. The simplest valid implementation is `cat paimos-adapter.json`.
- The conformance suite verifies that `describe` output parses as a v1 manifest and that `name` + `supports` match the on-disk manifest.

### `validate`

- Read stdin. Exit `0` if the input is something this adapter could consume; non-zero with a one-line stderr summary if not.
- This is the cheap pre-check `paimos skill render` runs before delegating the real call. Use it to catch missing fields, wrong canonical schema, etc.

---

## 4. Discovery: `$PAIMOS_ADAPTER_PATH`

paimos finds adapters by walking `$PAIMOS_ADAPTER_PATH` — same shape as `$PATH` (colon-separated on POSIX, semicolon on Windows; ordered, first-match-wins).

Layout for each entry:

```
$PAIMOS_ADAPTER_PATH/<name>/
├── paimos-adapter.json          # the manifest
├── paimos-adapter-<name>        # the executable (chmod +x)
└── expected_output.txt          # optional snapshot fixture for the conformance suite
```

The walk is **shallow** (one directory deep) — paimos doesn't recursively crawl arbitrary trees, so adapter discovery I/O is bounded.

### Discovery layers, in order

1. **Built-in registry** — `claude-code`, `codex`, `grok`, `pi`, and `cursor`. Discovery and per-invocation overrides below may shadow these names.
2. **`$PAIMOS_ADAPTER_PATH` directories** — discovered adapters can shadow built-ins by name (the user installed a custom version on purpose).
3. **`paimos skill render --harness-from-file <path>`** — per-invocation override. Wins over both above.

A bad manifest in one directory doesn't hide its siblings; paimos logs the parse error and moves on.

---

## 5. Conformance suite

```sh
paimos skill test-adapter <name>
```

The suite runs every adapter (in-process or external) through standardised cases. A passing run is the gating criterion for listing in the public registry.

### Cases

| Case | What it asserts |
| --- | --- |
| `manifest_sanity` | Manifest parses as v1, has `name`, MarshalJSON round-trip stable. |
| `supports_boundary_lower_inclusive` | Synthesised canonical at `supports`'s lower bound renders. |
| `supports_boundary_middle` | Canonical mid-range renders. |
| `supports_boundary_upper_exclusive` | Canonical at the exclusive upper bound is **rejected** (no silent truncation). |
| `representative_render` | A fully-populated artifact yields non-empty content. |
| `describe_matches_manifest` | (External adapters only) `describe` JSON parses as v1 and matches the on-disk `name` + `supports`. |
| `snapshot_byte_equality` | (Optional) Render of a probe artifact equals `expected_output.txt` byte-for-byte. |

Exit `0` when every case passes; `1` otherwise. Use `--json` for machine-readable output. Use `--snapshot <path>` to run the byte-equality case explicitly.

### Listing in the public registry

`GET /api/registry/adapters` returns every published adapter with its v1 manifest. The static index lives at `backend/handlers/adapter_registry.json` in this repo — submit a PR adding your entry. Default response hides entries with `"conformance.passes": false`; pass `?include=pending` to see in-flight submissions.

---

## 6. Writing your first adapter

Worked example: `paimos-adapter-claude-code` is the external reference implementation at `https://github.com/inspr-at/paimos-adapter-claude-code`. Paimos still ships a bundled `claude-code` fallback so existing `paimos skill render --harness claude-code` calls continue to work when no external adapter is installed. To build a different external adapter, mirror the bundled fixture at `backend/cmd/paimos/adapters/fixtures/opencode/`.

### Step 1: write the manifest

Create `paimos-adapter.json`:

```json
{
  "protocol_version": "1",
  "name": "myharness",
  "version": "0.1.0",
  "supports": ">=1.0.0 <2.0.0",
  "description": "My-Harness skill renderer.",
  "target_path_template": "{workspace}/.myharness/skills/{slash_command_name}.md",
  "input_format": "json",
  "output_format": "markdown"
}
```

### Step 2: write the executable

Name it `paimos-adapter-myharness`. Any language works — Go, Python, Node, even a POSIX shell script (the bundled opencode fixture is one). Implement the three verbs from §3.

The simplest possible Python sketch:

```python
#!/usr/bin/env python3
import json, sys

def main():
    verb = sys.argv[1]
    if verb == "describe":
        with open("paimos-adapter.json") as f:
            sys.stdout.write(f.read())
        return 0
    if verb == "validate":
        try:
            data = json.load(sys.stdin)
            if not data.get("agent", {}).get("name"):
                print("missing agent.name", file=sys.stderr)
                return 1
        except Exception as e:
            print(f"input parse: {e}", file=sys.stderr)
            return 1
        return 0
    if verb == "render":
        data = json.load(sys.stdin)
        agent = data["agent"]
        sys.stdout.write(f"# {agent['name']}\n\n{agent.get('body', '')}\n")
        return 0
    print(f"unknown verb: {verb}", file=sys.stderr)
    return 64

sys.exit(main())
```

Make it executable (`chmod +x`).

### Step 3: install on `$PAIMOS_ADAPTER_PATH`

```sh
mkdir -p ~/.paimos/adapters/myharness
cp paimos-adapter.json paimos-adapter-myharness ~/.paimos/adapters/myharness/
export PAIMOS_ADAPTER_PATH="$HOME/.paimos/adapters"
```

### Step 4: verify discovery

```sh
paimos skill list-adapters
```

Your adapter shows up next to the built-in adapters.

### Step 5: run conformance

```sh
paimos skill test-adapter myharness
```

When every case passes, you're ready to submit a PR adding your adapter to `backend/handlers/adapter_registry.json` for public listing.

### Step 6: render a real skill

```sh
paimos skill render \
  --project ACME \
  --agent qa \
  --harness myharness
```

The rendered file lands at the path your `target_path_template` resolves to, with the paimos drift-detection header on top. Re-run with `--check` to verify the file is in sync with paimos.

---

## 7. Reference: where the code lives

| Concern | File |
| --- | --- |
| Adapter interface | `backend/cmd/paimos/adapters/adapter.go` |
| Manifest format + validation | `backend/cmd/paimos/adapters/manifest.go` |
| Discovery (`$PAIMOS_ADAPTER_PATH`) | `backend/cmd/paimos/adapters/discovery.go` |
| External-adapter execution | `backend/cmd/paimos/adapters/external.go` |
| Dispatch + header injection | `backend/cmd/paimos/adapters/dispatch.go` |
| Conformance suite | `backend/cmd/paimos/adapters/conformance.go` |
| Reference adapter (claude-code) | `https://github.com/inspr-at/paimos-adapter-claude-code` |
| Bundled fallback (claude-code) | `backend/cmd/paimos/adapters/claudecode/adapter.go` |
| Bundled external stub | `backend/cmd/paimos/adapters/fixtures/opencode/` |
| Public registry endpoint | `backend/handlers/adapter_registry.go` |
| Public registry index | `backend/handlers/adapter_registry.json` |

## 8. Native skills and command templates (PAI-1050)

| Harness | Suggested skill path |
| --- | --- |
| claude-code | `.claude/commands/<slash>.md` (existing format) |
| codex | `.agents/skills/<slash>/SKILL.md` |
| grok | `.grok/skills/<slash>/SKILL.md` |
| pi | `.pi/skills/<slash>/SKILL.md` |
| cursor | `.cursor/skills/<slash>/SKILL.md` |

Native skills begin with YAML `name` and `description` frontmatter. The managed
header follows the closing delimiter; drift checking accepts both this placement
and the existing leading-header format. Canonical instructions, bootstrap steps,
rules, references and deploy recipes use the same body renderer as Claude Code.
No model names or permission grants are inserted. Names use 1–64 lowercase
letters, digits and single hyphens; invalid names fail instead of colliding after
sanitization. The slash alias falls back to the agent name. Empty descriptions
receive an invocation hint.

```bash
paimos skill render --project PAI --agent codex --harness codex
paimos skill render --project PAI --agent codex --harness codex --check
paimos skill test-adapter cursor
paimos model templates grok --json
paimos model templates cursor --read-only --json
```

`model templates <harness>` is offline syntax discovery for `codex`, `claude`
(`claude-code` alias), `grok`, `pi`, and `cursor`. It returns `run`, `review`,
`resume`, and `spawn` templates. Placeholders are documentation, not shell
substitution: construct an argv array and pass each model, effort, prompt and
session identifier as a value. Cursor's model ID already includes its effort.
Syntax discovery grants no execution authority or availability guarantee.

`spawn` starts a fresh foreground subprocess with structured output; the caller
owns its lifetime, working directory, isolation and attribution. It is not a
native subagent or managed agentd launch. `resume` names an explicit vendor
session. `review` selects the vendor's read-only/plan mode (Pi uses its read-only
tool allowlist); `--read-only` retains this restriction in every operation.
Build templates retain vendor permission defaults. Cursor's existing `--trust`
convention is retained: invoke only in a trusted workspace.

Resolved profiles include pinned `command_templates` on the selected profile
and every model ladder entry, alongside the existing `command_template` (run).
Owner fallback has neither. CLI validation checks all four operations, including
skipped candidates. Matching server/CLI builds are required; old servers missing
the templates fail closed. Cached catalog resolution produces the same templates.
Native Grok remains syntax/skill-only until a current native route is verified;
the resolver's xAI route uses Cursor. Skill sync convenience commands keep their
Claude Code default; use `skill render --harness` for native adapters.

Verified against installed CLI help and native skill documentation:
[Codex](https://learn.chatgpt.com/docs/build-skills),
[Grok](https://docs.x.ai/build/features/skills-plugins-marketplaces),
[Pi](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md),
[Cursor](https://cursor.com/docs/skills).

## 9. Related tickets

- **PAI-329** — canonical agent artifact endpoint.
- **PAI-330** — `paimos skill render` verb + claude-code reference adapter.
- **PAI-331** — drift-detection header + `paimos skill check`.
- **PAI-332** — this protocol (manifest, contract, conformance, registry, docs).
- **PAI-333** — claude-code adapter extraction to its own repo (uses this protocol).
