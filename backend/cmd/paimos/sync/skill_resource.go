// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// PAI-331 — `skill` Resource implementation. Wraps the adapter dispatch
// from PAI-330 so the generic sync engine can pull, render, and drift-
// check skill files for a project. PAI-341 will register additional
// Resource implementations for the knowledge plane (memory, runbook,
// external_system, related_project, guideline) without modifying this
// file.

package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters"
)

// SkillResource implements Resource for harness-rendered skill files.
// It uses an adapters.Dispatch to convert canonical agent artifacts
// into local files. The harness is configurable (defaults to
// claude-code) so a single workspace with multiple harnesses can layer
// multiple Resource registrations on the same Registry — PAI-341 will
// likely follow this pattern when knowledge entries grow harness-
// specific renderings.
type SkillResource struct {
	// Dispatch is the adapter dispatch used to render canonical
	// artifacts. Must be configured with a registered harness adapter.
	Dispatch *adapters.Dispatch

	// HarnessName picks the adapter from Dispatch.Registry. Required —
	// passing an empty string is a programming error.
	HarnessName string
}

// NewSkillResource returns a SkillResource bound to the given dispatch
// + harness. The constructor centralises the validation so callers get
// a clear error (rather than a surprise on the first Sync call).
func NewSkillResource(disp *adapters.Dispatch, harnessName string) (*SkillResource, error) {
	if disp == nil || disp.Registry == nil {
		return nil, fmt.Errorf("skill resource: nil adapter dispatch")
	}
	if strings.TrimSpace(harnessName) == "" {
		return nil, fmt.Errorf("skill resource: harness name required")
	}
	if _, err := disp.Registry.Get(harnessName); err != nil {
		return nil, fmt.Errorf("skill resource: %w", err)
	}
	return &SkillResource{Dispatch: disp, HarnessName: harnessName}, nil
}

// Kind returns the registry key. PAI-330's CLI verb is named `skill`;
// the kind matches.
func (s *SkillResource) Kind() string { return "skill" }

// Endpoint returns the agent-list endpoint. The skill resource pulls
// every agent declared on the project on `init` (no individual list
// endpoint is required since `Sync` iterates the agents listing first).
func (s *SkillResource) Endpoint(projectID int64) string {
	return fmt.Sprintf("/api/projects/%d/agents", projectID)
}

// LocalPath delegates to the configured adapter's Render so the path
// matches what `paimos skill render` would have written. Falls back to
// `.paimos/skills/<harness>/<name>.md` when the adapter has no opinion
// — this keeps the directory layout predictable in fork scenarios.
func (s *SkillResource) LocalPath(projectKey, name string) string {
	// The adapter knows the harness convention better than we do, but
	// we don't have a canonical artifact to feed it here. Use the
	// stable default; Sync will write to the adapter's preferred path.
	return filepath.ToSlash(filepath.Join(".paimos", "skills", s.HarnessName, name+".md"))
}

// HeaderRev compares an in-memory rendered body against the file on
// disk and reports whether they refer to the same artifact rev. The
// rendered body already carries the canonical paimos header
// (adapters.BuildHeader), so equality of the full body implies equality
// of rev. Treat byte-equal bodies as in-sync.
func (s *SkillResource) HeaderRev(rendered, existing []byte) bool {
	return string(rendered) == string(existing)
}

// agentListItem mirrors models.ProjectAgent's relevant fields for the
// /api/projects/:id/agents listing. We unmarshal only what we need.
type agentListItem struct {
	Name string `json:"name"`
}

// Sync fetches every agent (or a single one when selectName is set),
// renders it through the configured adapter, and writes the output to
// the adapter's suggested path under workspaceRoot. Reports each
// artifact through onWritten with action "wrote" or "unchanged".
func (s *SkillResource) Sync(
	ctx context.Context,
	c SyncClient,
	projectID int64,
	projectKey, workspaceRoot, selectName string,
	onWritten func(SyncedItem),
) error {
	if c == nil {
		return fmt.Errorf("skill sync: nil client")
	}
	if onWritten == nil {
		onWritten = func(SyncedItem) {}
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return fmt.Errorf("skill sync: workspace root required")
	}

	names, err := s.targetAgentNames(c, projectID, selectName)
	if err != nil {
		return err
	}

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, err := s.syncOne(c, projectID, projectKey, workspaceRoot, name)
		if err != nil {
			return fmt.Errorf("sync skill %q: %w", name, err)
		}
		onWritten(item)
	}
	return nil
}

// targetAgentNames returns the list of agent names to sync. When
// selectName is set, returns just that name (existence is verified by
// the canonical-artifact GET in syncOne). Otherwise lists everything.
func (s *SkillResource) targetAgentNames(c SyncClient, projectID int64, selectName string) ([]string, error) {
	if name := strings.TrimSpace(selectName); name != "" {
		return []string{name}, nil
	}
	body, err := c.Get(fmt.Sprintf("/api/projects/%d/agents", projectID))
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	var items []agentListItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode agents list: %w", err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if n := strings.TrimSpace(it.Name); n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// syncOne fetches one agent's canonical artifact, renders, and writes
// (or skips when unchanged). Centralises the per-artifact work so Check
// can reuse the render half without the write half.
func (s *SkillResource) syncOne(c SyncClient, projectID int64, projectKey, workspaceRoot, name string) (SyncedItem, error) {
	rendered, suggested, err := s.renderOne(c, projectID, projectKey, name)
	if err != nil {
		return SyncedItem{}, err
	}
	target, err := joinWorkspacePath(workspaceRoot, suggested)
	if err != nil {
		return SyncedItem{}, err
	}
	action := "wrote"
	// #nosec G304 -- target is contained in workspaceRoot by joinWorkspacePath.
	if existing, readErr := os.ReadFile(target); readErr == nil && s.HeaderRev(rendered, existing) {
		action = "unchanged"
	}
	if action == "wrote" {
		if err := WriteFileAtomic(target, rendered); err != nil {
			return SyncedItem{}, err
		}
	}
	return SyncedItem{
		Kind:   s.Kind(),
		Name:   name,
		Path:   target,
		Rev:    ExtractRevFromHeader(rendered),
		Action: action,
	}, nil
}

// renderOne fetches the canonical artifact and dispatches it through
// the configured adapter. Returns the rendered body + the adapter's
// suggested path. The CLI never sees this layer — Sync wraps it.
func (s *SkillResource) renderOne(c SyncClient, projectID int64, projectKey, name string) ([]byte, string, error) {
	path := fmt.Sprintf("/api/projects/%d/agents/%s.json", projectID, url.PathEscape(name))
	canonical, err := c.Get(path)
	if err != nil {
		return nil, "", fmt.Errorf("fetch canonical: %w", err)
	}
	out, err := s.Dispatch.Render(adapters.RenderRequest{
		Canonical:   canonical,
		HarnessName: s.HarnessName,
		ProjectKey:  projectKey,
		AgentName:   name,
	})
	if err != nil {
		return nil, "", err
	}
	suggested := strings.TrimSpace(out.SuggestedPath)
	if suggested == "" {
		suggested = s.LocalPath(projectKey, name)
	}
	return []byte(out.Body), suggested, nil
}

// Check enumerates agents on the server, renders each one, and compares
// against the local copy. Mirrors the structure of Sync but never
// writes — emits a CheckRecord per artifact.
func (s *SkillResource) Check(
	ctx context.Context,
	c SyncClient,
	projectID int64,
	projectKey, workspaceRoot string,
) ([]CheckRecord, error) {
	if c == nil {
		return nil, fmt.Errorf("skill check: nil client")
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("skill check: workspace root required")
	}
	names, err := s.targetAgentNames(c, projectID, "")
	if err != nil {
		return nil, err
	}
	out := make([]CheckRecord, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		rendered, suggested, err := s.renderOne(c, projectID, projectKey, name)
		if err != nil {
			return out, fmt.Errorf("check skill %q: %w", name, err)
		}
		target, err := joinWorkspacePath(workspaceRoot, suggested)
		if err != nil {
			return out, fmt.Errorf("check skill %q: %w", name, err)
		}
		rec := CheckRecord{
			Kind: s.Kind(),
			Name: name,
			Path: target,
			Rev:  ExtractRevFromHeader(rendered),
		}
		// #nosec G304 -- target is contained in workspaceRoot by joinWorkspacePath.
		existing, readErr := os.ReadFile(target)
		switch {
		case readErr != nil && os.IsNotExist(readErr):
			rec.State = "missing_local"
		case readErr != nil:
			return out, fmt.Errorf("read %s: %w", target, readErr)
		case adapters.Compare(string(rendered), string(existing)) == adapters.CheckIdentical:
			rec.State = "identical"
		case adapters.Compare(string(rendered), string(existing)) == adapters.CheckHeaderMissing:
			rec.State = "header_missing"
		default:
			rec.State = "diff"
		}
		out = append(out, rec)
	}
	return out, nil
}

// ExtractRevFromHeader pulls the rev string out of a rendered body's
// canonical header line. Returns "" if the header is malformed (we
// don't fail the operation — the rev is purely diagnostic in
// SyncedItem / CheckRecord).
//
// PAI-341 promoted this from package-private to exported so the
// knowledge-plane Resources (memory_resource.go, runbook_resource.go, …)
// can reuse the same parsing logic.
func ExtractRevFromHeader(body []byte) string {
	s := adapters.ManagedHeader(string(body))
	if s == "" {
		return ""
	}
	const marker = "@"
	at := strings.Index(s, marker)
	if at < 0 {
		return ""
	}
	tail := s[at+1:]
	end := strings.IndexAny(tail, " \t\r\n")
	if end < 0 {
		return ""
	}
	return tail[:end]
}

// WriteFileAtomic creates parent dirs and writes to a tmpfile then
// renames into place. Mirrors cmd_skill.go's writeRendered so behaviour
// is consistent between the two surfaces.
//
// PAI-341 promoted this from package-private to exported so the
// knowledge-plane Resources (memory_resource.go, runbook_resource.go, …)
// can reuse the same atomic-write helper without duplication.
func WriteFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// joinWorkspacePath joins a server/adapter-suggested relative path under
// workspaceRoot, rejecting absolute paths and ".." traversal so a
// hostile artifact (suggested path, agent name, or slug) cannot steer
// reads or writes outside the workspace.
func joinWorkspacePath(workspaceRoot, suggested string) (string, error) {
	rel := filepath.FromSlash(strings.TrimSpace(suggested))
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("suggested path %q escapes the workspace root", suggested)
	}
	return filepath.Join(workspaceRoot, rel), nil
}
