<!--
  Doctrine loader.

  This project vendors the PUBLIC INSPR doctrine only — the identity-free
  safety baseline. It deliberately does not carry the operator-specific
  half: no fleet, no tracker layout, no personal preferences. Nothing here
  assumes anyone's infrastructure.

  Source: github.com/inspr-at/inspr-modules, vendored as ./doctrine.
  Bump with: git submodule update --remote doctrine
  Verify:    ./doctrine/scripts/doctrine-check.sh
-->

@./doctrine/docs/AGENTS-KERNEL.md
@./AGENTS.md

## Commands wired in this repo

Authoritative for what is wired here; `doctrine-check.sh` diffs this against
`.claude/commands/`.

| Command | Loads |
| --- | --- |
| `/dev` | Code, tests, git workflow |
| `/nix` | Nix, flakes, Home Manager |
| `/inspr` | Doctrine map |
| `/inspr-versioning` | INSPR Calendar Versioning adoption |
| `/push` | Single-repo commit + push |
