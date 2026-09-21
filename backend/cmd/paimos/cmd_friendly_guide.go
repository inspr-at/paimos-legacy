// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/models"
)

type friendlyChoice struct {
	key, label string
	value      string
}

// Guidance only fills selectors from the principal's read-only catalogs. The
// same authoritative resolver used by non-interactive/JSON rechecks every choice.
func guideFriendlyStart(ctx context.Context, in io.Reader, out io.Writer, o *friendlyStartOptions) error {
	client, err := instanceClient()
	if err != nil {
		return errors.New("guided choices require a configured authenticated instance")
	}
	reader := bufio.NewReader(io.LimitReader(in, 16<<10))
	choose := func(name string, dst *string, choices []friendlyChoice) error {
		if *dst != "" {
			return nil
		}
		if len(choices) == 0 && name == "Parent" {
			return fmt.Errorf("no active parent is available; bootstrap one with: %s orchestrator start --project %s --guided", friendlyCLIBase(client), o.Project)
		}
		if len(choices) == 0 {
			return fmt.Errorf("no authorized %s choices are available; inspect the project or runtime configuration", name)
		}
		fmt.Fprintf(out, "%s choices:\n", name)
		for i, c := range choices {
			fmt.Fprintf(out, "  %d. %s\n", i+1, c.label)
		}
		fmt.Fprint(out, "Select a row number or exact displayed key: ")
		line, e := reader.ReadString('\n')
		if e != nil && e != io.EOF {
			return errors.New("guided input could not be read")
		}
		selected := strings.TrimSpace(line)
		if n, e := strconv.Atoi(selected); e == nil && n > 0 && n <= len(choices) {
			*dst = choices[n-1].key
			if choices[n-1].value != "" {
				*dst = choices[n-1].value
			}
			return nil
		}
		matches := 0
		for _, c := range choices {
			if c.key == selected {
				matches++
				*dst = c.key
				if c.value != "" {
					*dst = c.value
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("select one displayed %s choice; rerun with --guided or supply the required flags", name)
		}
		return nil
	}
	if o.Project == "" {
		var projects []orchestratorProject
		if err := friendlyRead(ctx, client, "/api/projects?status=all", &projects); err != nil {
			return err
		}
		var choices []friendlyChoice
		for _, p := range projects {
			if p.ID > 0 && orchestratorProjectKeyPattern.MatchString(p.Key) {
				choices = append(choices, friendlyChoice{key: p.Key, label: p.Key})
			}
		}
		if err := choose("Project", &o.Project, choices); err != nil {
			return err
		}
	}
	project, err := resolveExactOrchestratorProject(client, o.Project)
	if err != nil {
		return err
	}
	if o.Agent == "" {
		var agents []agentSummary
		if err := friendlyRead(ctx, client, fmt.Sprintf("/api/projects/%d/agents", project.ID), &agents); err != nil {
			return err
		}
		var choices []friendlyChoice
		for _, a := range agents {
			if orchestratorAgentKeyPattern.MatchString(a.Name) && a.Name != "web-ui" {
				choices = append(choices, friendlyChoice{key: a.Name, label: a.Name})
			}
		}
		if err := choose("Agent", &o.Agent, choices); err != nil {
			return err
		}
	}
	if !o.Coordinator {
		if o.Ticket == "" {
			fmt.Fprint(out, "Ticket key (for example "+o.Project+"-921): ")
			line, e := reader.ReadString('\n')
			if e != nil && e != io.EOF {
				return errors.New("guided input could not be read")
			}
			o.Ticket = strings.TrimSpace(line)
			if o.Ticket == "" {
				return errors.New("ticket input is required; supply --ticket or rerun --guided")
			}
		}
		if err := choose("Work shape", &o.Shape, []friendlyChoice{{key: "ship", label: "ship"}, {key: "scout", label: "scout"}}); err != nil {
			return err
		}
		if o.Parent == "" {
			var sessions []models.HarnessSession
			if err := friendlyRead(ctx, client, fmt.Sprintf("/api/projects/%d/harness-sessions", project.ID), &sessions); err != nil {
				return err
			}
			var choices []friendlyChoice
			counts := map[string]int{}
			for _, s := range sessions {
				if _, e := resolveFriendlyParent([]models.HarnessSession{s}, project.ID, s.ID); e == nil {
					counts[s.Harness+":"+s.AgentName]++
				}
			}
			for _, s := range sessions {
				if _, e := resolveFriendlyParent([]models.HarnessSession{s}, project.ID, s.ID); e == nil {
					key := s.Harness + ":" + s.AgentName
					if counts[key] != 1 {
						key = s.ID
					}
					choices = append(choices, friendlyChoice{key: key, label: fmt.Sprintf("%s (%s; %s; %s)", key, s.Role, s.Phase, s.ID), value: s.ID})
				}
			}
			if err := choose("Parent", &o.Parent, choices); err != nil {
				return err
			}
		}
	}
	if o.Profile == "" && o.ModelRole == "" {
		var options struct {
			Profiles []dispatchprofile.Profile `json:"dispatch_profiles"`
		}
		if err := friendlyRead(ctx, client, "/api/ai/execution-options?dispatch_only=1", &options); err != nil {
			return err
		}
		var choices []friendlyChoice
		for _, p := range options.Profiles {
			selected := *o
			selected.Profile = p.ID + "@" + p.Version
			if _, err := resolveFriendlyProfile([]dispatchprofile.Profile{p}, selected); err == nil {
				key := p.ID + "@" + p.Version
				choices = append(choices, friendlyChoice{key: key, label: fmt.Sprintf("%s — %s / %s / %s", key, p.Harness, p.Model, p.Effort)})
			}
		}
		if err := choose("Profile", &o.Profile, choices); err != nil {
			return err
		}
	}
	if o.Coordinator && o.Label == "" {
		o.Label = o.Agent
	}
	if o.Key == "" && !o.DryRun && !o.Explain {
		o.Key = uuid.NewString()
		fmt.Fprintf(out, "Retry key: %s\n", o.Key)
	}
	return nil
}
