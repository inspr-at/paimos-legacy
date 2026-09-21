// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type friendlyStartOptions struct {
	ModelRole, AuthorFamily                                  string
	Project, Agent, Ticket, Shape, Parent, Role              string
	Profile, Harness, Model, Effort, Account, Machine        string
	Workspace, PromptFile, Key, Deployment, Label, StateRoot string
	ExpectedRevision                                         int64
	Coordinator                                              bool
	DryRun, Explain, Guided, NonInteractive                  bool
	Wait                                                     time.Duration
}

type friendlyStartPlan struct {
	ModelResolution *dispatchprofile.Resolution `json:"model_resolution,omitempty"`
	Project         string                      `json:"project"`
	Agent           string                      `json:"agent"`
	Ticket          string                      `json:"ticket,omitempty"`
	Role            string                      `json:"role"`
	Shape           string                      `json:"work_shape,omitempty"`
	Parent          string                      `json:"parent_session_id,omitempty"`
	Profile         dispatchprofile.Profile     `json:"dispatch_profile"`
	BindingRevision *int64                      `json:"binding_revision,omitempty"`
	BindingChange   bool                        `json:"binding_change"`
	request         agentd.StartRequest
	binding         orchestratorConfig
}

type friendlyStartResult struct {
	Outcome         string                      `json:"outcome"`
	State           string                      `json:"generation_state"`
	PublicSessionID string                      `json:"public_session_id,omitempty"`
	PublicPhase     string                      `json:"public_phase,omitempty"`
	Replayed        bool                        `json:"replayed,omitempty"`
	Plan            *friendlyStartPlan          `json:"plan,omitempty"`
	Commands        map[string]string           `json:"commands,omitempty"`
	Reason          string                      `json:"reason,omitempty"`
	Key             string                      `json:"idempotency_key,omitempty"`
	Readiness       *friendlyMessagingReadiness `json:"readiness,omitempty"`
}

type friendlyDaemon interface {
	Status(context.Context) (agentd.Status, error)
	Start(context.Context, agentd.StartRequest) (agentd.Session, error)
}

var friendlyDaemonClient = func(socket string) (friendlyDaemon, error) { return agentd.NewClient(socket) }
var friendlySafeValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var friendlyModelValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var friendlyTicketKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,9}-[1-9][0-9]*$`)

func workerCmd() *cobra.Command {
	cmd := commandGroup(&cobra.Command{Use: "worker", Short: "Start an owned generation of a canonical project agent"})
	cmd.AddCommand(friendlyStartCmd(false))
	return cmd
}

func friendlyStartCmd(coordinator bool) *cobra.Command {
	o := friendlyStartOptions{Coordinator: coordinator}
	role := "worker"
	if coordinator {
		role = "coordinator"
	}
	cmd := &cobra.Command{Use: "start", Short: "Resolve an agent and immutable profile, then start an owned " + role, Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		normalizeFriendlyModelRole(&o)
		if o.Guided && o.NonInteractive {
			return friendlyStartError("--guided and --non-interactive cannot be combined")
		}
		if o.Guided || (!o.NonInteractive && !flagJSON && term.IsTerminal(int(os.Stdin.Fd()))) {
			if err := guideFriendlyStart(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), &o); err != nil {
				return friendlyStartError(err.Error())
			}
		}
		result, err := runFriendlyStart(cmd.Context(), o)
		if err != nil {
			return friendlyStartError(err.Error())
		}
		if flagJSON {
			if err := emitJSON(result); err != nil {
				return err
			}
		} else {
			if result.Replayed {
				fmt.Fprint(stdout, "replayed original outcome: ")
			}
			fmt.Fprintf(stdout, "%s: generation %s", result.Outcome, result.State)
			if result.PublicSessionID != "" {
				fmt.Fprintf(stdout, "; public session %s", result.PublicSessionID)
			}
			if result.PublicPhase != "" {
				fmt.Fprintf(stdout, "; public phase %s", result.PublicPhase)
			}
			fmt.Fprintln(stdout)
			if result.Plan != nil {
				fmt.Fprintf(stdout, "Project %s; agent %s; role %s; profile %s@%s\n", result.Plan.Project, result.Plan.Agent, result.Plan.Role, result.Plan.Profile.ID, result.Plan.Profile.Version)
			}
			if result.Key != "" {
				fmt.Fprintf(stdout, "Idempotency key: %s\n", result.Key)
			}
			if result.Reason != "" {
				fmt.Fprintln(stdout, result.Reason)
			}
			for _, name := range []string{"status", "message", "allow", "steer", "interrupt", "stop", "doctor"} {
				if line := result.Commands[name]; line != "" {
					fmt.Fprintf(stdout, "%s: %s\n", name, line)
				}
			}
		}
		if result.Outcome == "unknown" || result.Outcome == "failed" {
			return &apiError{inner: errors.New("start did not establish a ready public generation")}
		}
		return nil
	}
	f := cmd.Flags()
	f.StringVar(&o.Project, "project", "", "canonical project key")
	f.StringVar(&o.Agent, "agent", "", "canonical agent key (persona, not a running session)")
	f.StringVar(&o.Ticket, "ticket", "", "ticket key, e.g. PAI-921")
	f.StringVar(&o.Shape, "work-shape", "", "ship or scout; required with a ticket")
	f.StringVar(&o.Parent, "parent", "", "same-project public session UUID or unambiguous harness:agent handle")
	f.StringVar(&o.Role, "role", role, "worker, coordinator, or a model role (scout, mechanical, build, build-hard, review-gate)")
	f.StringVar(&o.AuthorFamily, "author-family", "", "author model family for review-gate")
	f.StringVar(&o.Profile, "profile", "", "immutable profile ID@version; selectors must agree")
	f.StringVar(&o.Harness, "harness", "", "codex, claude, pi or cursor")
	f.StringVar(&o.Model, "model", "", "exact supported model selector")
	f.StringVar(&o.Effort, "effort", "", "exact supported effort selector")
	f.StringVar(&o.Account, "account", "", "opaque named-account key from the operator registry, or a closed class label; local_probe selects the class source without named-account verification")
	f.StringVar(&o.Machine, "machine", "", "authenticated machine ID constraint (requires daemon support); authenticated_reporter selects its source")
	f.StringVar(&o.Workspace, "workspace", ".", "clean, exclusively available workspace")
	f.StringVar(&o.PromptFile, "prompt-file", "", "task instructions from file; never included in output or retry records")
	f.StringVar(&o.Key, "idempotency-key", "", "stable key for exact retries (required outside guided mode)")
	f.StringVar(&o.Deployment, "expect-deployment-instance", "", "expected deployment identity; defaults to the named configured instance")
	f.StringVar(&o.Label, "display-label", "", "instance orchestrator label; defaults to canonical agent key")
	f.Int64Var(&o.ExpectedRevision, "expected-revision", -1, "explicit instance binding CAS revision, otherwise read the current revision")
	f.StringVar(&o.StateRoot, "state-root", "", "private agentd state root override")
	f.BoolVar(&o.DryRun, "dry-run", false, "resolve and validate without writes or spawn")
	f.BoolVar(&o.Explain, "explain", false, "show the validated plan without writes or spawn")
	f.BoolVar(&o.Guided, "guided", false, "prompt for missing required inputs")
	f.BoolVar(&o.NonInteractive, "non-interactive", false, "never prompt")
	f.DurationVar(&o.Wait, "wait", 10*time.Second, "bounded wait for a reporter-backed public session (0 to 30s)")
	return cmd
}

func friendlyStartError(reason string) error {
	if flagJSON {
		_ = emitJSON(friendlyStartResult{Outcome: "failed", State: "unknown", Reason: reason})
		return &apiError{inner: errors.New(reason)}
	}
	return errors.New(reason)
}

// Model roles choose execution; the existing harness hierarchy remains worker/coordinator.
func normalizeFriendlyModelRole(o *friendlyStartOptions) {
	if o.Coordinator {
		return
	}
	for _, role := range dispatchprofile.Roles() {
		if o.Role == role.Name {
			o.ModelRole = o.Role
			o.Role = "worker"
			return
		}
	}
}

func validateFriendlyStart(o friendlyStartOptions) error {
	if !orchestratorProjectKeyPattern.MatchString(o.Project) {
		return errors.New("--project must be an exact canonical project key; use paimos project list")
	}
	if !orchestratorAgentKeyPattern.MatchString(o.Agent) || o.Agent == "web-ui" {
		return errors.New("--agent must be a canonical agent key; use paimos agent list --project <key>")
	}
	if o.Role != "worker" && o.Role != "coordinator" || o.Coordinator && o.Role != "coordinator" {
		return errors.New("--role must be worker, coordinator, or a model role (scout, mechanical, build, build-hard, review-gate)")
	}
	if !o.Coordinator && (o.Ticket == "" || o.Parent == "") {
		return errors.New("worker start requires --ticket <key>, --work-shape ship|scout and --parent <public-session-id|harness:agent>")
	}
	if o.Coordinator && o.Parent != "" {
		return errors.New("instance orchestrator cannot have a project parent; omit --parent")
	}
	if o.Ticket != "" && (!friendlyTicketKey.MatchString(o.Ticket) || !strings.HasPrefix(o.Ticket, o.Project+"-")) {
		return errors.New("--ticket must be a ticket key in the selected project")
	}
	if o.Ticket != "" && o.Shape != "ship" && o.Shape != "scout" || o.Ticket == "" && o.Shape != "" {
		return errors.New("--work-shape must be ship or scout with a ticket, and omitted without one")
	}
	if !friendlySafeValue.MatchString(o.Key) && !(o.Key == "" && (o.DryRun || o.Explain)) {
		return errors.New("supply --idempotency-key with 1–128 safe label characters; reuse it only for the same request")
	}
	if o.Harness != "" && o.Harness != "codex" && o.Harness != "claude" && o.Harness != "pi" && o.Harness != "cursor" {
		return errors.New("unsupported harness capability; choose --harness codex, claude, pi or cursor and a catalog profile")
	}
	if o.Model != "" && !friendlyModelValue.MatchString(o.Model) {
		return errors.New("model selector must be an exact catalog model")
	}
	for _, value := range []string{o.Effort, o.Account, o.Machine} {
		if value != "" && !friendlySafeValue.MatchString(value) {
			return errors.New("selectors must be non-secret safe labels")
		}
	}
	if o.Wait < 0 || o.Wait > 30*time.Second {
		return errors.New("--wait must be between 0s and 30s")
	}
	if !o.Coordinator && o.ExpectedRevision != -1 {
		return errors.New("--expected-revision applies only to orchestrator start")
	}
	if o.ExpectedRevision < -1 {
		return errors.New("--expected-revision must be non-negative")
	}
	if o.Coordinator && validateOrchestratorDisplayLabel(o.Label) != "" {
		return errors.New("--display-label must be 1–64 UTF-8 bytes without control characters")
	}
	return nil
}

func friendlyRead(ctx context.Context, client *Client, path string, out any) error {
	if err := client.getJSON(ctx, path, out); err != nil {
		if errors.Is(err, errMalformedJSON) {
			return errors.New("authority returned malformed data; no start was attempted")
		}
		return fmt.Errorf("required authority read failed for %s; verify instance authentication and project access", strings.Split(path, "?")[0])
	}
	return nil
}

func resolveFriendlyProfile(profiles []dispatchprofile.Profile, o friendlyStartOptions) (dispatchprofile.Profile, error) {
	var matches []dispatchprofile.Profile
	for _, p := range profiles {
		if o.Profile != "" && o.Profile != p.ID+"@"+p.Version {
			continue
		}
		if o.Harness != "" && o.Harness != p.Harness || o.Model != "" && o.Model != p.Model || o.Effort != "" && o.Effort != p.Effort {
			continue
		}
		expected, err := dispatchprofile.Resolve(p.ID, p.Version, p.Harness)
		if err != nil || expected != p {
			return dispatchprofile.Profile{}, errors.New("unsupported immutable profile capability; update matching CLI/daemon or select a supported catalog profile")
		}
		matches = append(matches, p)
	}
	if len(matches) != 1 {
		choices := make([]string, 0, len(profiles))
		for _, p := range profiles {
			if dispatchprofile.Validate(p) == nil {
				choices = append(choices, p.ID+"@"+p.Version)
			}
		}
		return dispatchprofile.Profile{}, fmt.Errorf("%d compatible profiles; set --profile ID@version or refine --harness/--model/--effort. Available: %s", len(matches), strings.Join(choices, ", "))
	}
	return matches[0], nil
}

func resolveFriendlyParent(sessions []models.HarnessSession, projectID int64, handle string) (string, error) {
	var matches []models.HarnessSession
	for _, s := range sessions {
		if s.ID == handle || s.Harness+":"+s.AgentName == handle {
			matches = append(matches, s)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("%d parent matches; run paimos harness list --project <key> and pass an exact public --parent UUID", len(matches))
	}
	s := matches[0]
	if s.ProjectID != projectID {
		return "", errors.New("cross-project parent is forbidden; select a public session from this project")
	}
	if s.Phase != "starting" && s.Phase != "working" && s.Phase != "yielded" {
		return "", errors.New("parent is terminal, stopping or unknown; select an active same-project public session")
	}
	if uuid.Validate(s.ID) != nil {
		return "", errors.New("parent has no valid public session ID")
	}
	return s.ID, nil
}

func resolveFriendlyStart(ctx context.Context, client *Client, o friendlyStartOptions, prompt string) (friendlyStartPlan, error) {
	plan := friendlyStartPlan{Project: o.Project, Agent: o.Agent, Ticket: o.Ticket, Role: o.Role, Shape: o.Shape}
	project, err := resolveExactOrchestratorProject(client, o.Project)
	if err != nil {
		return plan, errors.New("project key did not resolve uniquely; use paimos project list on the selected instance")
	}
	if err := resolveExactOrchestratorAgent(client, project.ID, o.Agent); err != nil {
		return plan, errors.New("canonical agent did not resolve uniquely; use paimos agent list --project <key>")
	}
	var catalog struct {
		Profiles []dispatchprofile.Profile `json:"dispatch_profiles"`
	}
	if err := friendlyRead(ctx, client, "/api/ai/execution-options?dispatch_only=1", &catalog); err != nil {
		return plan, err
	}
	if o.ModelRole != "" {
		resolution, resolveErr := resolveModel(ctx, client, o.Workspace, dispatchprofile.ResolveRequest{Role: o.ModelRole, AuthorFamily: dispatchprofile.Family(o.AuthorFamily), Harness: o.Harness})
		if resolveErr != nil {
			return plan, resolveErr
		}
		if resolution.OwnerRequired || resolution.Profile == nil {
			return plan, errors.New("owner approval required; no worker model selected")
		}
		pin := resolution.Profile.ID + "@" + resolution.Profile.Version
		if o.Profile != "" && o.Profile != pin {
			return plan, errors.New("--profile conflicts with model role resolution")
		}
		o.Profile = pin
		plan.ModelResolution = &resolution
	}
	plan.Profile, err = resolveFriendlyProfile(catalog.Profiles, o)
	if err != nil {
		return plan, err
	}
	request := agentd.StartRequest{Adapter: plan.Profile.Harness, Workspace: o.Workspace, WorkspaceMode: plan.Profile.WorkspaceMode, Identity: plan.Profile.Harness + ":" + o.Agent, ProjectID: project.ID, Role: o.Role, WorkShape: o.Shape, DispatchProfileID: plan.Profile.ID, DispatchProfileVersion: plan.Profile.Version}
	if o.Ticket != "" {
		var ticket struct {
			ID        int64 `json:"id"`
			ProjectID int64 `json:"project_id"`
		}
		if err := friendlyRead(ctx, client, "/api/issues/"+url.PathEscape(o.Ticket), &ticket); err != nil {
			return plan, err
		}
		if ticket.ID <= 0 || ticket.ProjectID != project.ID {
			return plan, errors.New("ticket authority does not match the selected project")
		}
		request.TicketID = ticket.ID
	}
	var sessions []models.HarnessSession
	if err := friendlyRead(ctx, client, fmt.Sprintf("/api/projects/%d/harness-sessions", project.ID), &sessions); err != nil {
		return plan, err
	}
	if o.Parent != "" {
		plan.Parent, err = resolveFriendlyParent(sessions, project.ID, o.Parent)
		if err != nil {
			return plan, err
		}
		request.ParentSessionID = plan.Parent
	}
	if o.Role == "coordinator" {
		for _, s := range sessions {
			if s.ProjectID == project.ID && s.Role == "coordinator" && s.Phase != "stopped" {
				return plan, errors.New("project already has a non-terminal coordinator; inspect paimos harness orchestrator --project <key> and stop or resolve it explicitly")
			}
		}
	}
	if o.Coordinator {
		plan.binding, err = readOrchestratorConfig(client)
		if err != nil {
			return plan, errors.New("instance binding could not be read; verify super-admin authority")
		}
		if o.ExpectedRevision >= 0 && o.ExpectedRevision != plan.binding.Revision {
			return plan, errors.New("instance binding revision changed; inspect the binding and rerun with its explicit --expected-revision")
		}
		revision := plan.binding.Revision
		plan.BindingRevision = &revision
		target := plan.binding.Orchestrator
		plan.BindingChange = target == nil || target.ProjectID != project.ID || target.Key != o.Agent || target.DisplayLabel != o.Label
	}
	var artifact struct {
		Project orchestratorProject `json:"project"`
		Agent   struct {
			Name           string                      `json:"name"`
			ProjectID      int64                       `json:"project_id"`
			Body           string                      `json:"body"`
			BootstrapSteps []models.AgentBootstrapStep `json:"bootstrap_steps"`
			Rules          []models.AgentRule          `json:"non_negotiable_rules"`
		} `json:"agent"`
	}
	if err := friendlyRead(ctx, client, fmt.Sprintf("/api/projects/%d/agents/%s.json", project.ID, o.Agent), &artifact); err != nil {
		return plan, err
	}
	if artifact.Project.ID != project.ID || artifact.Project.Key != o.Project || artifact.Agent.ProjectID != project.ID || artifact.Agent.Name != o.Agent {
		return plan, errors.New("canonical agent artifact does not match the selected project and agent")
	}
	instructions, err := json.Marshal(artifact.Agent)
	if err != nil {
		return plan, errors.New("canonical instructions are unavailable")
	}
	request.Prompt = fmt.Sprintf("Act as canonical project agent %s in project %s. Your owned session role is %s. Follow these canonical instructions:\n%s", o.Agent, o.Project, o.Role, instructions)
	if o.Ticket != "" {
		request.Prompt += fmt.Sprintf(" Your explicit ticket is %s and work shape is %s. Read it with paimos issue get %s.", o.Ticket, o.Shape, o.Ticket)
	}
	if prompt != "" {
		request.Prompt += "\n\n" + prompt
	}
	if len(request.Prompt) > 256<<10 {
		return plan, errors.New("task instructions exceed the start request limit")
	}
	request, err = friendlyConstrainedRequest(request, o)
	if err != nil {
		return plan, err
	}
	plan.request = request
	return plan, nil
}

// Round-trip the shared request so older binaries cannot silently discard a
// requested constraint. The runtime owns validation against actual provenance.
func friendlyConstrainedRequest(request agentd.StartRequest, o friendlyStartOptions) (agentd.StartRequest, error) {
	raw, _ := json.Marshal(request)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	constraints := map[string]string{}
	if o.Account != "" && o.Account != dispatchprofile.AccountLocalProbe {
		if agentd.IsClosedAccountLabel(o.Account) {
			constraints["expected_account_label"] = o.Account
		} else {
			constraints["account_key"] = o.Account
		}
	}
	if o.Machine != "" && o.Machine != dispatchprofile.MachineAuthenticatedReporter {
		constraints["expected_machine_id"] = o.Machine
	}
	for key, value := range constraints {
		fields[key], _ = json.Marshal(value)
	}
	raw, _ = json.Marshal(fields)
	_ = json.Unmarshal(raw, &request)
	raw, _ = json.Marshal(request)
	var supported map[string]json.RawMessage
	_ = json.Unmarshal(raw, &supported)
	for key, value := range constraints {
		var actual string
		if json.Unmarshal(supported[key], &actual) != nil || actual != value {
			return request, errors.New("account/machine label constraint is unsupported by this runtime contract; install matching CLI/daemon support, or explicitly select --account local_probe --machine authenticated_reporter without pinning runtime values")
		}
	}
	return request, nil
}

func runFriendlyStart(ctx context.Context, o friendlyStartOptions) (friendlyStartResult, error) {
	normalizeFriendlyModelRole(&o)
	if o.Label == "" {
		o.Label = o.Agent
	}
	if err := validateFriendlyStart(o); err != nil {
		return friendlyStartResult{}, err
	}
	client, err := instanceClient()
	if err != nil {
		return friendlyStartResult{}, errors.New("instance configuration is unavailable; run paimos auth login for the selected instance")
	}
	if o.Deployment == "" && client.identity.Name != "env" {
		o.Deployment = client.identity.Name
	}
	if !orchestratorInstanceNamePattern.MatchString(o.Deployment) || o.Deployment == "default" {
		return friendlyStartResult{}, errors.New("supply --expect-deployment-instance with the non-default deployment identity")
	}
	workspace, err := filepath.Abs(o.Workspace)
	if err != nil {
		return friendlyStartResult{}, errors.New("workspace is unavailable")
	}
	o.Workspace = workspace
	prompt, err := friendlyPrompt(o.PromptFile)
	if err != nil {
		return friendlyStartResult{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return friendlyStartResult{}, errors.New("private cache directory is unavailable")
	}
	if o.StateRoot == "" {
		o.StateRoot = filepath.Join(cache, "paimos", "agentd")
	}
	if !filepath.IsAbs(o.StateRoot) {
		return friendlyStartResult{}, errors.New("--state-root must be absolute")
	}
	runtimeDir, err := agentd.InstanceStateDir(o.StateRoot, o.Deployment)
	if err != nil {
		return friendlyStartResult{}, errors.New("runtime instance directory is unavailable")
	}
	// Replay precedes mutable authority/workspace checks: a completed generation
	// remains the original outcome even after its parent exits or its files change.
	ledgerDir := filepath.Join(o.StateRoot, "friendly-start", client.identity.Namespace)
	fingerprint := friendlyStartFingerprint(o, prompt)
	if !o.DryRun && !o.Explain {
		if result, found, err := readFriendlyStartRecord(ledgerDir, o.Key, fingerprint); err != nil || found {
			if err == nil && result.Outcome == "unknown" {
				result = reconcileFriendlyStart(ctx, client, o, runtimeDir, result)
				if result.Outcome != "unknown" {
					if e := completeFriendlyStartRecord(ledgerDir, o.Key, fingerprint, result); e != nil {
						return friendlyStartResult{}, errors.New("reconciled start could not be checkpointed")
					}
				}
			}
			return friendlyReplayCommands(ctx, result, client, o), err
		}
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(o.Workspace)
	if err != nil {
		return friendlyStartResult{}, errors.New("workspace is unavailable; select an existing clean directory")
	}
	if err := verifyOrchestratorDeployment(client, o.Deployment); err != nil {
		return friendlyStartResult{}, errors.New("deployment identity is unverified or mismatched; check --instance and --expect-deployment-instance")
	}
	plan, err := resolveFriendlyStart(ctx, client, o, prompt)
	if err != nil {
		return friendlyStartResult{}, err
	}
	plan.request.Workspace = resolvedWorkspace
	daemon, err := friendlyDaemonClient(filepath.Join(runtimeDir, "agentd.sock"))
	if err != nil {
		return friendlyStartResult{}, fmt.Errorf("local daemon transport is unavailable; run %s runtime doctor", friendlyCLIBase(client))
	}
	status, err := daemon.Status(ctx)
	if err != nil {
		return friendlyStartResult{}, fmt.Errorf("local daemon is unavailable; run %s runtime setup, then %s runtime doctor", friendlyCLIBase(client), friendlyCLIBase(client))
	}
	if status.Instance != o.Deployment || status.DaemonID == "" {
		return friendlyStartResult{}, errors.New("local daemon identity does not match the verified deployment; inspect paimos runtime doctor")
	}
	if err := validateFriendlyWorkspace(ctx, resolvedWorkspace, status.Sessions); err != nil {
		return friendlyStartResult{}, err
	}
	if o.DryRun || o.Explain {
		return friendlyStartResult{Outcome: "validated", State: "not_started", Plan: &plan, Reason: "Read-only plan. Spawn, profile, ownership, parent and reporter authority can still change before execution."}, nil
	}
	result := friendlyStartResult{Outcome: "unknown", State: "unknown", Plan: &plan, Key: o.Key, Reason: "Start outcome is uncertain. Inspect runtime doctor and harness list; do not issue a fresh start key until reconciled."}
	if err := reserveFriendlyStartRecord(ledgerDir, o.Key, fingerprint, result); err != nil {
		if previous, found, readErr := readFriendlyStartRecord(ledgerDir, o.Key, fingerprint); readErr == nil && found {
			return friendlyReplayCommands(ctx, previous, client, o), nil
		}
		return friendlyStartResult{}, err
	}
	finish := func(result friendlyStartResult) (friendlyStartResult, error) {
		if err := completeFriendlyStartRecord(ledgerDir, o.Key, fingerprint, result); err != nil {
			return friendlyStartResult{}, errors.New("start outcome could not be checkpointed; inspect runtime doctor and harness list before any new start")
		}
		return result, nil
	}
	if o.Coordinator {
		// Even unchanged pins are read again immediately before spawn. A changed pin
		// must never be silently accepted by a retry or by a long-running prompt.
		current, readErr := readOrchestratorConfig(client)
		if readErr != nil || current.Revision != plan.binding.Revision {
			result.Outcome, result.Reason = "failed", "Instance binding changed before start; inspect it and make a new explicit request."
			return finish(result)
		}
		if plan.BindingChange {
			body := map[string]any{"expected_revision": plan.binding.Revision, "orchestrator": map[string]any{"project_id": plan.request.ProjectID, "key": o.Agent, "display_label": o.Label}}
			raw, writeErr := client.doForAgentContext(ctx, http.MethodPut, "/api/orchestrator/v1/config", body, "")
			var configured orchestratorConfig
			if writeErr != nil || json.Unmarshal(raw, &configured) != nil || validateOrchestratorConfig(configured) != nil || configured.Revision != plan.binding.Revision+1 || configured.Orchestrator == nil || configured.Orchestrator.ProjectID != plan.request.ProjectID || configured.Orchestrator.Key != o.Agent || configured.Orchestrator.DisplayLabel != o.Label {
				result.Reason = "Binding CAS was rejected or its result is unconfirmed; no spawn was attempted. Inspect the binding before a new request."
				return finish(result)
			}
			plan.BindingRevision = &configured.Revision
		}
	}
	// Namespace the daemon key by authenticated origin as well as selected instance.
	plan.request.IdempotencyKey = client.identity.Namespace + ":" + o.Key
	session, startErr := daemon.Start(ctx, plan.request)
	if startErr != nil {
		result.Reason = "Daemon start failed or its response was lost; inspect runtime doctor and harness list before any new start."
		return finish(result)
	}
	deadline := time.Now().Add(o.Wait)
	waitCtx, cancelWait := context.WithDeadline(ctx, deadline)
	defer cancelWait()
	for session.Reporter.PublicSessionID == "" && !friendlyTerminal(session.State) && time.Now().Before(deadline) {
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return finish(result)
		case <-timer.C:
		}
		snapshot, statusErr := daemon.Status(waitCtx)
		if statusErr != nil || snapshot.DaemonID != status.DaemonID || snapshot.Instance != status.Instance {
			break
		}
		for _, candidate := range snapshot.Sessions {
			if candidate.ID == session.ID {
				session = candidate
				break
			}
		}
	}
	if session.ProjectID != plan.request.ProjectID || session.Identity != plan.request.Identity || session.Adapter != plan.request.Adapter || session.Role != plan.request.Role || session.ParentSessionID != plan.request.ParentSessionID || session.TicketID != plan.request.TicketID || session.WorkShape != plan.request.WorkShape || session.AccountKey != plan.request.AccountKey || !session.Managed {
		result.Reason = "Daemon returned an unexpected generation; inspect runtime doctor."
		return finish(result)
	}
	result.State = friendlyGenerationState(session.State)
	if uuid.Validate(session.Reporter.PublicSessionID) != nil {
		result.Reason = "Local generation observed; public registration remains unknown. Inspect runtime doctor and harness list."
		return finish(result)
	}
	var public models.HarnessSession
	if err := friendlyRead(ctx, client, fmt.Sprintf("/api/projects/%d/harness-sessions/%s", plan.request.ProjectID, session.Reporter.PublicSessionID), &public); err != nil || public.ID != session.Reporter.PublicSessionID || public.ProjectID != plan.request.ProjectID || public.AgentName != o.Agent || public.Harness != plan.Profile.Harness || public.ManagementMode != "managed" || public.Role != plan.Role || (public.WorkShape != plan.Shape && !(plan.Shape == "" && public.WorkShape == "unknown")) || friendlyParentValue(public.ParentSessionID) != plan.Parent || friendlyTicketValue(public.TicketID) != plan.request.TicketID || public.DispatchProfile == nil || *public.DispatchProfile != (friendlyProfileSnapshot(plan.Profile)) || public.AccountKey != plan.request.AccountKey {
		result.Reason = "Reporter registration could not be verified against the public authority; inspect runtime doctor and harness list."
		return finish(result)
	}
	result.PublicSessionID = public.ID
	result.PublicPhase = public.Phase
	freeReceiverSlot, _ := friendlyReceiverSlot(ctx, client, public.ProjectID, session.Identity)
	result.Commands = friendlyStartCommands(client.identity.Name, o, plan, public, session, freeReceiverSlot)
	result.Outcome = "started"
	if friendlyTerminal(session.State) || public.Phase == "stopped" {
		result.Outcome = "failed"
	}
	if result.State == "unknown" {
		result.Outcome = "unknown"
	}
	result = friendlyApplyMessaging(ctx, client, o, result, public, session, false)
	if result.State == "unknown" || public.ActivityState == "unknown" {
		result.Reason += " Activity may be unknown until fresh evidence arrives."
	}
	return finish(result)
}

// A cached start outcome is immutable, while a receiver slot can change later.
// Never replay an old mutating setup suggestion without a new target review.
func friendlyReplayCommands(ctx context.Context, result friendlyStartResult, client *Client, o friendlyStartOptions) friendlyStartResult {
	if result.Commands != nil {
		result.Commands["receiver-setup"] = friendlyCLIBase(client) + " runtime handoff --project " + shellQuote(o.Project)
	}
	return friendlyApplyMessaging(ctx, client, o, result, models.HarnessSession{ID: result.PublicSessionID, ProjectID: 0}, agentd.Session{}, true)
}

func friendlyTerminal(state agentd.SessionState) bool {
	return state == agentd.StateStopped || state == agentd.StateExited || state == agentd.StateFailed || state == agentd.StateOwnershipLost
}
func friendlyGenerationState(state agentd.SessionState) string {
	switch state {
	case agentd.StateStarting, agentd.StateRunning, agentd.StateStopping, agentd.StateStopped, agentd.StateExited, agentd.StateFailed, agentd.StateOwnershipLost:
		return string(state)
	}
	return "unknown"
}
func friendlyStartCommands(instance string, o friendlyStartOptions, p friendlyStartPlan, s models.HarnessSession, local agentd.Session, freeReceiverSlot ...bool) map[string]string {
	base := "paimos"
	if flagConfigPath != "" {
		base += " --config " + shellQuote(flagConfigPath)
	}
	if instance != "env" {
		base += " --instance " + shellQuote(instance)
	}
	scope := " --project " + shellQuote(o.Project)
	sessionScope := scope + " --session " + shellQuote(s.ID)
	commands := map[string]string{"status": base + " harness status" + sessionScope, "doctor": base + " runtime doctor"}
	commands["receiver-setup"] = base + " runtime handoff" + scope
	if len(freeReceiverSlot) > 0 && freeReceiverSlot[0] && s.ProjectID > 0 && uuid.Validate(local.ID) == nil && p.Profile.Harness == "codex" {
		reader := "paimos-agentd receiver-reference --instance " + shellQuote(o.Deployment) + " --session " + shellQuote(local.ID) + " --identity " + shellQuote(local.Identity) + fmt.Sprintf(" --project-id %d", s.ProjectID)
		if o.StateRoot != "" {
			reader += " --state-root " + shellQuote(o.StateRoot)
		}
		commands["receiver-setup"] = reader + " | " + base + " message target set" + scope + " --address " + shellQuote(local.Identity) + " --adapter codex --kind codex_thread --role simple_fallback --maximum-level simple --target-ref-file -"
	}
	// Native registration supplies the owned primary inbox; public harness
	// control capabilities determine the additional generation-scoped actions.
	commands["message"] = base + " tell " + shellQuote(p.Profile.Harness+":"+o.Agent) + scope + " --level simple -m 'YOUR MESSAGE'"
	steer := false
	for _, capability := range local.Capabilities {
		if capability == agentd.CapabilitySteer {
			steer = true
		}
	}
	if steer {
		commands["steer"] = base + " tell " + shellQuote(p.Profile.Harness+":"+o.Agent) + scope + " --level steer -m 'YOUR STEERING MESSAGE'"
	}
	if s.Capabilities.Interrupt {
		commands["interrupt"] = base + " harness interrupt" + sessionScope
	}
	if s.Capabilities.Stop {
		commands["stop"] = base + " harness stop" + sessionScope
	}
	return commands
}
func friendlyPrompt(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "-" {
		return "", errors.New("--prompt-file must name a file; stdin is reserved for guided input")
	}
	file, err := os.Open(path) // #nosec G304 -- explicit operator --prompt-file input; bounded to 256 KiB below and never derived from remote input.
	if err != nil {
		return "", errors.New("task instruction file is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 || strings.ContainsRune(string(raw), 0) {
		return "", errors.New("task instruction file is invalid or exceeds 256 KiB")
	}
	return string(raw), nil
}

func friendlyParentValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func friendlyTicketValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func friendlyCLIBase(client *Client) string {
	base := "paimos"
	if client.identity.Name != "" && client.identity.Name != "env" {
		base += " --instance " + shellQuote(client.identity.Name)
	} else if flagInstance != "" {
		base += " --instance " + shellQuote(flagInstance)
	}
	if flagConfigPath != "" {
		base += " --config " + shellQuote(flagConfigPath)
	}
	return base
}

func friendlyProfileSnapshot(p dispatchprofile.Profile) models.HarnessDispatchProfile {
	return models.HarnessDispatchProfile{ID: p.ID, Version: p.Version, Harness: p.Harness, Model: p.Model, Effort: p.Effort, MachineSource: p.MachineSource, AccountSource: p.AccountSource, WorkspaceMode: p.WorkspaceMode}
}
