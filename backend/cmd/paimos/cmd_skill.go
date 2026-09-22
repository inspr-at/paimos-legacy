// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-330 — `paimos skill render` + `paimos skill list-adapters`.
//
// `render` is the central verb: fetch the canonical agent artifact,
// dispatch through the registered harness adapter, and write (or
// drift-check via --check) the resulting file.
//
// `list-adapters` enumerates registered adapters so users can confirm
// what's available before invoking `render --harness ...`.
//
// Adapter resolution order:
//
//   1. Built-in registry (Claude Code plus native Codex/Grok/Pi/Cursor skills).
//   2. --harness-from-file <path> — manifest-based adapter loaded for
//      this invocation. Wins over a built-in of the same name (escape
//      hatch for forks / experiments before PAI-332's SDK lands).

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters"
	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters/claudecode"
	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters/native"
)

// builtInAdaptersFn is the package-level hook tests use to swap in a
// minimal registry. Production wiring registers the built-in skill adapters
// and any adapters added via --harness-from-file.
var builtInAdaptersFn = registerBuiltIns

func registerBuiltIns(reg *adapters.Registry) {
	reg.Register(claudecode.New())
	for _, adapter := range native.All() {
		reg.Register(adapter)
	}
}

// adapterDiscoveryFn is the package-level hook for $PAIMOS_ADAPTER_PATH
// discovery. Tests can substitute a no-op or fixture-driven discovery
// without touching real env state. Production calls
// adapters.DiscoverAdapters("", logger).
var adapterDiscoveryFn = func() ([]adapters.DiscoveredAdapter, error) {
	return adapters.DiscoverAdapters("", func(format string, a ...any) {
		fmt.Fprintf(stderr, "paimos: "+format+"\n", a...)
	})
}

// registerDiscoveredAdapters wraps each $PAIMOS_ADAPTER_PATH-found
// manifest as an ExternalAdapter and registers it. External entries
// shadow built-ins of the same name (the user installed a custom
// version on purpose); --harness-from-file shadows discovery
// (per-invocation override). Discovery errors are logged but not
// fatal — a single bad adapter must not break `skill render`.
func registerDiscoveredAdapters(reg *adapters.Registry) {
	if adapterDiscoveryFn == nil {
		return
	}
	found, err := adapterDiscoveryFn()
	if err != nil {
		fmt.Fprintf(stderr, "paimos: adapter discovery: %v\n", err)
		return
	}
	for _, d := range found {
		ext, err := adapters.NewExternalAdapter(d)
		if err != nil {
			fmt.Fprintf(stderr, "paimos: skip discovered adapter %q: %v\n", d.Manifest.Name, err)
			continue
		}
		reg.Register(ext)
	}
}

func skillCmd() *cobra.Command {
	c := commandGroup(&cobra.Command{
		Use:   "skill",
		Short: "Render canonical agent artifacts into harness-specific skill files",
		Long: `skill — turn paimos canonical agent artifacts into harness-specific
skill files via registered adapters.

The canonical artifact lives at GET /api/projects/<id>/agents/<name>.json
and is consumed by an adapter (e.g. claude-code) that produces the file
your harness expects (e.g. .claude/commands/<name>.md). Rendered files
carry a paimos-managed header line so PAI-331 can detect drift.`,
	})
	c.AddCommand(skillRenderCmd())
	c.AddCommand(skillListAdaptersCmd())
	c.AddCommand(skillTestAdapterCmd())
	// PAI-331: thin convenience wrappers over `paimos sync`. Both verb
	// namespaces work: `paimos sync init --kind=skill` is the canonical
	// form, `paimos skill init` is the muscle-memory shortcut for users
	// already in the skill verb namespace.
	c.AddCommand(skillInitCmd())
	c.AddCommand(skillPullCmd())
	c.AddCommand(skillWatchCmd())
	c.AddCommand(skillCheckCmd())
	return c
}

// skillInitCmd is `paimos skill init` — convenience wrapper for
// `paimos sync init --kind=skill`.
func skillInitCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
	)
	c := &cobra.Command{
		Use:   "init",
		Short: "Pull every agent's rendered skill file once (alias for `paimos sync init --kind=skill`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncMulti(projectRef, workspaceRoot, "skill", "", false)
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	return c
}

// skillPullCmd is `paimos skill pull`.
func skillPullCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
		name          string
	)
	c := &cobra.Command{
		Use:   "pull",
		Short: "Refresh skill files (alias for `paimos sync pull --kind=skill`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncMulti(projectRef, workspaceRoot, "skill", strings.TrimSpace(name), false)
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	c.Flags().StringVar(&name, "agent", "", "single agent name (omit for all)")
	return c
}

// skillWatchCmd is `paimos skill watch`.
func skillWatchCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
	)
	c := &cobra.Command{
		Use:   "watch",
		Short: "Subscribe to agent changes, re-render on event (alias for `paimos sync watch --kind=skill`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWatch(projectRef, workspaceRoot, "skill")
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	return c
}

// skillCheckCmd is `paimos skill check`.
func skillCheckCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
	)
	c := &cobra.Command{
		Use:   "check",
		Short: "Compare local skill files against canonical (alias for `paimos sync check --kind=skill`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncCheck(projectRef, workspaceRoot, "skill")
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	return c
}

func skillRenderCmd() *cobra.Command {
	var (
		projectRef    string
		agentName     string
		harnessName   string
		outPath       string
		checkOnly     bool
		harnessFile   string
		workspaceRoot string
	)
	c := &cobra.Command{
		Use:   "render",
		Short: "Render an agent's canonical artifact through a harness adapter",
		Long: `render fetches the canonical agent artifact (PAI-329) and dispatches
it through a registered harness adapter (PAI-330). The result is written
to --out, or (when --out is absent) to the adapter's suggested path
under --workspace (default: cwd).

Use --check to compare an existing rendered file against what would be
generated. Exit codes:

  0  identical (or, in the absence of --check, write succeeded)
  1  diff (or any runtime/API error)
  2  --check found the file but it has no paimos-managed header

The rendered file is prefixed with a paimos drift-detection header
(PAI-331 reads this).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(projectRef) == "" {
				return &usageError{msg: "--project is required"}
			}
			if strings.TrimSpace(agentName) == "" {
				return &usageError{msg: "--agent is required"}
			}
			if strings.TrimSpace(harnessName) == "" && strings.TrimSpace(harnessFile) == "" {
				return &usageError{msg: "--harness or --harness-from-file is required"}
			}

			client, err := instanceClient()
			if err != nil {
				return err
			}
			projectID, err := resolveProjectID(client, projectRef)
			if err != nil {
				return err
			}

			// Fetch the canonical artifact. We also pull the project key
			// up front so the header line can carry it (the artifact
			// includes it but using the API-resolved key avoids a re-
			// decode in the common path).
			path := fmt.Sprintf("/api/projects/%d/agents/%s.json",
				projectID, url.PathEscape(strings.TrimSpace(agentName)))
			canonical, err := client.do("GET", path, nil)
			if err != nil {
				return reportError(err)
			}
			projectKey, agentNameFromArtifact := extractProjectAndAgentKey(canonical)
			if agentNameFromArtifact == "" {
				agentNameFromArtifact = strings.TrimSpace(agentName)
			}

			// Build the registry: built-ins first, then any --harness-from-file
			// override (which wins by virtue of map last-write).
			reg := adapters.NewRegistry()
			builtInAdaptersFn(reg)
			registerDiscoveredAdapters(reg)
			if strings.TrimSpace(harnessFile) != "" {
				m, err := adapters.LoadManifestAdapter(harnessFile)
				if err != nil {
					return err
				}
				reg.Register(m)
				if strings.TrimSpace(harnessName) == "" {
					harnessName = m.Name()
				}
			}

			disp := &adapters.Dispatch{Registry: reg}
			out, err := disp.Render(adapters.RenderRequest{
				Canonical:   canonical,
				HarnessName: harnessName,
				ProjectKey:  projectKey,
				AgentName:   agentNameFromArtifact,
			})
			if err != nil {
				return err
			}

			// Resolve the target path: --out wins; otherwise resolve the
			// adapter's SuggestedPath under --workspace (default cwd).
			target, err := resolveTargetPath(outPath, workspaceRoot, out.SuggestedPath)
			if err != nil {
				return err
			}

			if checkOnly {
				return runCheck(target, out.Body)
			}

			if err := writeRendered(target, out.Body); err != nil {
				return err
			}

			if flagJSON {
				payload := map[string]any{
					"path":    target,
					"rev":     out.Rev,
					"harness": harnessName,
					"bytes":   len(out.Body),
				}
				b, _ := json.Marshal(payload)
				fmt.Fprintln(stdout, string(b))
			} else {
				fmt.Fprintf(stdout, "wrote %s (%d bytes, rev=%s)\n", target, len(out.Body), out.Rev)
			}
			return nil
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&agentName, "agent", "", "agent name (slug)")
	c.Flags().StringVar(&harnessName, "harness", "", "registered adapter name (e.g. claude-code)")
	c.Flags().StringVar(&outPath, "out", "", "output file path (overrides adapter suggested path)")
	c.Flags().BoolVar(&checkOnly, "check", false, "do not write; compare existing file and exit non-zero on drift")
	c.Flags().StringVar(&harnessFile, "harness-from-file", "", "load an ad-hoc adapter from a manifest file (escape hatch)")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root for the adapter's suggested path (defaults to cwd)")
	return c
}

func skillListAdaptersCmd() *cobra.Command {
	var harnessFile string
	c := &cobra.Command{
		Use:   "list-adapters",
		Short: "List registered harness adapters",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg := adapters.NewRegistry()
			builtInAdaptersFn(reg)
			registerDiscoveredAdapters(reg)
			if strings.TrimSpace(harnessFile) != "" {
				m, err := adapters.LoadManifestAdapter(harnessFile)
				if err != nil {
					return err
				}
				reg.Register(m)
			}
			items := reg.List()
			if flagJSON {
				// Emit the formal v1 manifest shape (PAI-332). Older
				// `describe` / `supports` keys remain present in the
				// canonical manifest; consumers should prefer the
				// PAI-332 canonical names.
				out := make([]adapters.Manifest, 0, len(items))
				for _, a := range items {
					out = append(out, adapters.ManifestOf(a))
				}
				b, _ := json.Marshal(out)
				fmt.Fprintln(stdout, string(b))
				return nil
			}
			if len(items) == 0 {
				fmt.Fprintln(stdout, "(no adapters registered)")
				return nil
			}
			fmt.Fprintln(stdout, "NAME            VERSION   SUPPORTS              DESCRIBE")
			for _, a := range items {
				fmt.Fprintf(stdout, "%-15s %-9s %-21s %s\n",
					a.Name(), a.Version(), a.Supports(), a.Describe())
			}
			return nil
		},
	}
	c.Flags().StringVar(&harnessFile, "harness-from-file", "", "also include an ad-hoc adapter loaded from a manifest")
	return c
}

// skillTestAdapterCmd is `paimos skill test-adapter <name>` — runs the
// PAI-332 conformance suite against a registered adapter. Exit 0 when
// every case passes; exit 1 otherwise. Used as the gating check for
// listing in the public registry endpoint.
func skillTestAdapterCmd() *cobra.Command {
	var (
		harnessFile string
		snapshot    string
	)
	c := &cobra.Command{
		Use:   "test-adapter <name>",
		Short: "Run the PAI-332 conformance suite against an adapter",
		Long: `test-adapter runs an adapter through the PAI-332 conformance suite:
manifest sanity, supports-range boundary probes, representative-render
non-empty, and (for external adapters) describe-matches-manifest.

A passing run is the criterion for an adapter to be listed in the
public registry endpoint (GET /api/registry/adapters).

When --snapshot is supplied (or an expected_output.txt fixture sits
next to a discovered manifest) the suite also asserts byte-equality
against the rendered output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return &usageError{msg: "<name> is required"}
			}
			reg := adapters.NewRegistry()
			builtInAdaptersFn(reg)
			discovered := []adapters.DiscoveredAdapter{}
			if adapterDiscoveryFn != nil {
				if got, err := adapterDiscoveryFn(); err == nil {
					discovered = got
					for _, d := range got {
						ext, err := adapters.NewExternalAdapter(d)
						if err == nil {
							reg.Register(ext)
						}
					}
				}
			}
			if strings.TrimSpace(harnessFile) != "" {
				m, err := adapters.LoadManifestAdapter(harnessFile)
				if err != nil {
					return err
				}
				reg.Register(m)
			}

			a, err := reg.Get(name)
			if err != nil {
				return err
			}
			opts := adapters.ConformanceOptions{
				SnapshotOverride: strings.TrimSpace(snapshot),
			}
			for _, d := range discovered {
				if d.Manifest.Name == name {
					opts.ManifestPath = d.ManifestPath
					break
				}
			}
			report := adapters.RunConformance(a, opts)

			if flagJSON {
				b, _ := json.MarshalIndent(report, "", "  ")
				fmt.Fprintln(stdout, string(b))
			} else {
				fmt.Fprintf(stdout, "conformance report for adapter %q:\n", report.Adapter)
				for _, c := range report.Cases {
					status := "PASS"
					if !c.Pass {
						status = "FAIL"
					}
					fmt.Fprintf(stdout, "  [%s] %s", status, c.Name)
					if c.Message != "" {
						fmt.Fprintf(stdout, " — %s", c.Message)
					}
					fmt.Fprintln(stdout)
				}
				if report.AllPassed() {
					fmt.Fprintln(stdout, "all cases passed")
				} else {
					fmt.Fprintf(stdout, "%d case(s) failed\n", report.FailureCount())
				}
			}
			if !report.AllPassed() {
				return &checkExitCode{code: 1}
			}
			return nil
		},
	}
	c.Flags().StringVar(&harnessFile, "harness-from-file", "", "also include an ad-hoc adapter loaded from a manifest")
	c.Flags().StringVar(&snapshot, "snapshot", "", "path to expected-output fixture for byte-equality check")
	return c
}

// resolveTargetPath picks the on-disk path the rendered output goes to.
// --out wins. Otherwise the adapter's suggested path is joined under
// --workspace (defaulting to cwd). An empty suggested path with no
// --out is a usage error — the adapter must have given us something.
func resolveTargetPath(outPath, workspace, suggested string) (string, error) {
	outPath = strings.TrimSpace(outPath)
	if outPath != "" {
		return filepath.Clean(outPath), nil
	}
	suggested = strings.TrimSpace(suggested)
	if suggested == "" {
		return "", fmt.Errorf("adapter did not provide a suggested path; pass --out to choose one")
	}
	root := strings.TrimSpace(workspace)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		root = cwd
	}
	if filepath.IsAbs(suggested) {
		return filepath.Clean(suggested), nil
	}
	return filepath.Clean(filepath.Join(root, suggested)), nil
}

// writeRendered creates parent directories then writes atomically via
// rename so a concurrent reader never sees a half-written file.
func writeRendered(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// runCheck implements --check semantics: exit 0 (identical), 1 (diff),
// or 2 (header missing). The function returns a *checkExitCode for
// non-zero cases so main() can map directly without printing a generic
// "Error:" prefix.
func runCheck(path, rendered string) error {
	existing, err := os.ReadFile(path) // #nosec G304 -- path is the render target the CLI user chose via --out/--workspace.
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "paimos: %s does not exist (would be created on render)\n", path)
			return &checkExitCode{code: 1}
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	result := adapters.Compare(rendered, string(existing))
	switch result {
	case adapters.CheckIdentical:
		if flagJSON {
			b, _ := json.Marshal(map[string]any{"check": "identical", "path": path})
			fmt.Fprintln(stdout, string(b))
		} else {
			fmt.Fprintf(stdout, "%s: identical\n", path)
		}
		return nil
	case adapters.CheckHeaderMissing:
		fmt.Fprintf(stderr, "paimos: %s has no paimos-managed header — out of management surface\n", path)
		return &checkExitCode{code: 2}
	case adapters.CheckDiff:
		fmt.Fprintf(stderr, "paimos: %s differs from canonical render (run without --check to update)\n", path)
		// Surface a tiny diff summary so users have something actionable.
		summariseDiff(string(existing), rendered)
		return &checkExitCode{code: 1}
	}
	return &checkExitCode{code: 1}
}

// summariseDiff emits an inexpensive line-count delta to stderr — not a
// full diff, just enough to confirm "yes, things changed". Users who
// want the full diff run without --check, then `git diff`.
func summariseDiff(existing, rendered string) {
	exLines := strings.Split(strings.TrimRight(existing, "\n"), "\n")
	reLines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	fmt.Fprintf(stderr, "  current: %d lines, canonical: %d lines\n", len(exLines), len(reLines))
}

// checkExitCode is the typed error main() routes to a specific exit
// code (1 or 2). Mirrors the doctorExitCode pattern already in the CLI.
type checkExitCode struct{ code int }

func (e *checkExitCode) Error() string {
	return fmt.Sprintf("check failed (exit %d)", e.code)
}

// extractProjectAndAgentKey pulls the project key + agent name out of
// the canonical artifact for the header line. Both are best-effort —
// callers fall back to flag values when the artifact is malformed.
func extractProjectAndAgentKey(canonical []byte) (projectKey, agentName string) {
	var probe struct {
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
		Agent struct {
			Name string `json:"name"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(canonical, &probe); err != nil {
		return "", ""
	}
	return strings.TrimSpace(probe.Project.Key), strings.TrimSpace(probe.Agent.Name)
}
