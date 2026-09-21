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

// Command paimos is the agent-facing CLI for PAIMOS instances. It wraps
// the HTTP API so Claude Code / agent scripts / humans can work with
// issues without wrestling with shell-quoted JSON.
//
// Bootstrap surface (PAI-90):
//
//	paimos auth login              — write ~/.paimos/config.yaml
//	paimos auth whoami
//	paimos project list
//	paimos issue get <ref>
//	paimos issue list --project PAI --status backlog
//	paimos issue children <ref>
//
// Global flags: --instance <name>, --json, --config <path>.
//
// Exit codes: 0 ok, 1 API error, 2 usage/config error, 3 no inbox messages,
// 4 selected native message adapter unavailable.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

// Version is stamped by goreleaser/-ldflags for releases; "dev" for
// local builds. Surfaced via `paimos --version`.
var Version = "dev"

// Global flag values, populated by Cobra's PersistentFlags.
var (
	flagInstance   string
	flagJSON       bool
	flagConfigPath string
	// PAI-325: optional ad-hoc overrides for agent-attribution.
	// Empty (default) → resolveAgentAttribution falls back to env vars
	// (PAIMOS_AGENT_NAME / PAIMOS_SESSION_ID), then to nothing for
	// agent-name and the auto-generated per-invocation UUID for
	// session-id. Flags take precedence so one-off scripts can
	// attribute writes without re-exporting env.
	flagAgentName string
	flagSessionID string
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := rootCmd().ExecuteContext(ctx); err != nil {
		// Usage errors: our own error type → print + exit 2 (convention).
		// apiError has already been printed in the caller's chosen
		// format (pretty or --json), don't double up.
		// doctorExitCode carries its own status code (1 warn / 2 fail).
		// Everything else (config I/O, marshaling, etc.) → print + exit 1.
		if ue, ok := err.(*usageError); ok {
			fmt.Fprintln(os.Stderr, "paimos: "+ue.Error())
			os.Exit(2)
		}
		if de, ok := err.(*doctorExitCode); ok {
			os.Exit(de.code)
		}
		// PAI-330: skill render --check uses a typed exit code so 0/1/2
		// can flow through without a generic "Error:" prefix (the
		// drift-detection caller has already written the relevant
		// message to stderr in its chosen format).
		if ce, ok := err.(*checkExitCode); ok {
			os.Exit(ce.code)
		}
		if le, ok := err.(*listenExitCode); ok {
			if le.err != nil {
				fmt.Fprintln(os.Stderr, "paimos:", le.err)
			}
			os.Exit(le.code)
		}
		if _, ok := err.(*apiError); !ok {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "paimos",
		Short: "Agent-facing CLI for PAIMOS (Professional & Personal AI Project OS)",
		Long: `paimos — thin CLI wrapper over the PAIMOS HTTP API.

CLI issue references use keys (PAI-83) by default. To intentionally address
an internal database ID, use the explicit form id:462; bare numbers are
rejected because they are indistinguishable from a pasted issue-key suffix.
Every mutation accepts file inputs for multiline fields (--description-file,
--ac-file) so there's no shell-quoted-JSON foot-gun. Pass --json on any
command to emit machine-readable output.

Get started:
  paimos auth login           # interactive; writes ~/.paimos/config.yaml
  paimos issue list --help`,
		Version:       Version,
		SilenceUsage:  true, // don't dump usage on every API error
		SilenceErrors: true, // we print errors ourselves in main()
	}

	cmd.PersistentFlags().StringVar(&flagInstance, "instance", "", "instance name from config (defaults to default_instance)")
	cmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "emit JSON output suitable for piping")
	cmd.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config file (default ~/.paimos/config.yaml)")
	// PAI-325: ad-hoc agent-attribution overrides. Env vars
	// PAIMOS_AGENT_NAME / PAIMOS_SESSION_ID are the recommended path;
	// these flags exist for one-off shell scripts that don't want to
	// re-export env. Both are forwarded only on writes.
	cmd.PersistentFlags().StringVar(&flagAgentName, "agent-name", "", "agent attribution forwarded on writes (overrides PAIMOS_AGENT_NAME)")
	cmd.PersistentFlags().StringVar(&flagSessionID, "session-id", "", "session id forwarded on writes (overrides PAIMOS_SESSION_ID)")

	cmd.AddCommand(authCmd())
	cmd.AddCommand(projectCmd())
	cmd.AddCommand(tagCmd())
	cmd.AddCommand(attachCmd())
	cmd.AddCommand(timeCmd())
	cmd.AddCommand(searchCmd())
	cmd.AddCommand(curlCmd())
	cmd.AddCommand(anchorsCmd())
	// PAI-358: manifestCmd removed with the project_manifests table.
	cmd.AddCommand(issueCmd())
	cmd.AddCommand(relationCmd())
	cmd.AddCommand(schemaCmd())
	cmd.AddCommand(doctorCmd())
	cmd.AddCommand(applyCmd())
	// PAI-327: agent-attribution session lifecycle.
	cmd.AddCommand(sessionCmd())
	// PAI-848: process-control noun, deliberately distinct from attribution sessions.
	cmd.AddCommand(harnessCmd())
	// PAI-349: memory propose verb.
	cmd.AddCommand(memoryCmd())
	cmd.AddCommand(knowledgeCmd())
	// PAI-506: first-class project-agent CRUD.
	cmd.AddCommand(agentCmd())
	cmd.AddCommand(orchestratorCmd())
	cmd.AddCommand(workerCmd())
	cmd.AddCommand(runtimeCmd())
	// PAI-330: skill render with adapter dispatch.
	cmd.AddCommand(skillCmd())
	// PAI-352: paimos onboard — human-readable briefing renderer.
	cmd.AddCommand(onboardCmd())
	// PAI-590 / PAI-591: local read-only context broker for coding agents.
	cmd.AddCommand(serveCmd())
	// PAI-331: generic sync verbs (init/pull/watch/check) over the
	// resource registry. PAI-341 will register additional kinds.
	cmd.AddCommand(syncCmd())
	cmd.AddCommand(modelCmd())
	cmd.AddCommand(runCmd())      // PAI-799: append-only progress reporting seam
	cmd.AddCommand(runAgentCmd()) // PAI-608: local "Implement this" runner
	cmd.AddCommand(tellCmd())     // PAI-815: durable name-addressed A2A messages
	cmd.AddCommand(messageCmd())  // PAI-815: ledger read/listen primitives
	cmd.AddCommand(listenCmd())   // PAI-816: durable receiver cursor and native adapters
	// PAI-810: pinned external delivery-stage handoff protocol.
	cmd.AddCommand(externalStageCmd())
	cmd.AddCommand(baselineBatchCmd())
	// PAI-358: migrateCmd removed; the only verb (manifest-to-knowledge)
	// is meaningless now that the project_manifests table is gone.

	// If no subcommand is given, Cobra's default is to print help with
	// exit 0. The AC wants exit 2 for that case (standard convention).
	cmd.Run = func(c *cobra.Command, args []string) {
		_ = c.Help()
		os.Exit(2)
	}

	return cmd
}

// commandGroup makes noun-only commands fail closed when a caller omits or
// misspells the verb. Cobra otherwise treats a non-runnable parent as a help
// request and returns success, while explicit --help remains successful.
func commandGroup(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return &usageError{msg: c.CommandPath() + " requires a subcommand; use --help to list available commands"}
	}
	return cmd
}

// usageError is returned for bad CLI invocations so main() can map to
// exit code 2. Use fmt.Errorf for actual runtime failures (→ exit 1).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// apiError wraps an HTTP API failure so main() can suppress Cobra's
// default "Error: ..." prefix (we've already printed it via the client
// in the caller's chosen format — pretty or JSON).
type apiError struct{ inner error }

func (e *apiError) Error() string { return e.inner.Error() }
