// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/models"
)

func reconcileFriendlyStart(ctx context.Context, c *Client, o friendlyStartOptions, dir string, result friendlyStartResult) friendlyStartResult {
	if result.Plan == nil {
		return result
	}
	daemon, err := friendlyDaemonClient(filepath.Join(dir, "agentd.sock"))
	if err != nil {
		return result
	}
	lookup, ok := daemon.(interface {
		LookupStart(context.Context, string) (agentd.Session, error)
	})
	if !ok {
		return result
	}
	status, err := daemon.Status(ctx)
	if err != nil || status.Instance != o.Deployment || status.DaemonID == "" {
		return result
	}
	session, err := lookup.LookupStart(ctx, c.identity.Namespace+":"+o.Key)
	if errors.Is(err, agentd.ErrStartRejected) {
		result.Outcome = "failed"
		result.Reason = "Original daemon start was rejected before spawn; no start was repeated."
		return result
	}
	if err != nil || uuid.Validate(session.Reporter.PublicSessionID) != nil {
		return result
	}
	p := result.Plan
	if session.Identity != p.Profile.Harness+":"+p.Agent || session.Role != p.Role || session.ParentSessionID != p.Parent || session.DispatchProfile == nil || *session.DispatchProfile != p.Profile || !session.Managed {
		return result
	}
	var public models.HarnessSession
	if friendlyRead(ctx, c, fmt.Sprintf("/api/projects/%d/harness-sessions/%s", session.ProjectID, session.Reporter.PublicSessionID), &public) != nil || public.ID != session.Reporter.PublicSessionID || public.ProjectID != session.ProjectID || public.AgentName != p.Agent || public.Harness != p.Profile.Harness || public.ManagementMode != "managed" || public.Role != p.Role || friendlyParentValue(public.ParentSessionID) != p.Parent || friendlyTicketValue(public.TicketID) != session.TicketID || public.DispatchProfile == nil || *public.DispatchProfile != friendlyProfileSnapshot(p.Profile) {
		return result
	}
	result.Outcome = "started"
	result.State = friendlyGenerationState(session.State)
	result.PublicSessionID = public.ID
	result.PublicPhase = public.Phase
	if friendlyTerminal(session.State) || public.Phase == "stopped" {
		result.Outcome = "failed"
	}
	if result.State == "unknown" {
		result.Outcome = "unknown"
	}
	result.Commands = friendlyStartCommands(c.identity.Name, o, *p, public, session)
	result.Reason = "Original daemon generation reconciled and public registration verified; no spawn was repeated."
	return result
}
