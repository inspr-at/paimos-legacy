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

// PAI-331 — `paimos sync` verb namespace.
//
//   paimos sync init    --project <key>            — pull every kind once.
//   paimos sync pull    --project <key> [--kind ..]— refresh on demand.
//   paimos sync watch   --project <key>            — long-running SSE.
//   paimos sync check   --project <key>            — drift summary.
//
// All four verbs operate over the sync.Registry; PAI-341 will register
// additional Resource implementations (memory, runbook, …) and they
// pick up these verbs automatically. The convenience wrappers under
// `paimos skill init|pull|watch|check` (cmd_skill.go) call into these
// with --kind=skill.

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters"
	"github.com/inspr-at/paimos/backend/cmd/paimos/sync"
)

// httpSyncClient adapts *Client to sync.SyncClient. Splitting the
// adapter out (rather than implementing on *Client directly) keeps the
// sync package free of paimos-specific HTTP concerns and makes the
// fake-client used in tests trivial.
type httpSyncClient struct {
	client *Client
}

func (h *httpSyncClient) Get(path string) ([]byte, error) {
	return h.client.do("GET", path, nil)
}

func (h *httpSyncClient) CacheIdentity() (sync.CacheIdentity, error) {
	i := h.client.identity
	if i.Name == "" || i.Origin == "" || i.Namespace == "" {
		return sync.CacheIdentity{}, fmt.Errorf("knowledge cache requires a resolved instance/origin identity")
	}
	return sync.CacheIdentity{Instance: i.Name, Origin: i.Origin, Namespace: i.Namespace}, nil
}

// Stream opens an SSE connection. Handles parsing the `data: <json>`
// frames and dispatches each Event to onEvent. Returns when the
// context is cancelled or the server closes the stream.
func (h *httpSyncClient) Stream(ctx context.Context, path string, onEvent func(sync.Event)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", h.client.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build SSE request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if h.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.client.apiKey)
	}
	if agent, session := resolveAgentAttribution(); session != "" {
		req.Header.Set(sessionAttrHeader, session)
		_ = agent // not relevant for reads, but resolved together
	}

	// SSE connections must NOT use the default 30s read timeout — they
	// stay open indefinitely. Build a dedicated client that mirrors the
	// auth/transport but lifts the timeout.
	streamHTTP := &http.Client{Timeout: 0, Transport: http.DefaultTransport}
	resp, err := streamHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE status %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("SSE read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, ":") {
			// Blank line = end-of-event. Comment lines (": …") are
			// keep-alives — both safe to skip here because we only
			// surface a single-line `data:` per event.
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var ev sync.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// Skip malformed events rather than failing the watch loop.
			continue
		}
		onEvent(ev)
	}
}

// syncRegistryFn is a test seam — production wiring registers the
// skill resource (claude-code adapter); tests can override to inject
// a fake.
var syncRegistryFn = buildDefaultSyncRegistry

// buildDefaultSyncRegistry constructs the default Registry: the skill
// resource bound to the claude-code adapter, plus the five PAI-341
// knowledge-plane Resources (memory, runbook, external_system,
// related_project, guideline). The registry order doesn't matter —
// `paimos sync init` iterates Registry.List() which sorts by Kind().
func buildDefaultSyncRegistry() (*sync.Registry, error) {
	reg := adapters.NewRegistry()
	builtInAdaptersFn(reg)
	disp := &adapters.Dispatch{Registry: reg}
	skillRes, err := sync.NewSkillResource(disp, "claude-code")
	if err != nil {
		return nil, err
	}
	r := sync.NewRegistry()
	r.Register(skillRes)
	// PAI-341 — knowledge plane. Each kind plugs into the same verbs
	// (init/pull/watch/check) and SSE event types via the shared
	// knowledgeResource implementation.
	r.Register(sync.NewMemoryResource())
	r.Register(sync.NewRunbookResource())
	r.Register(sync.NewExternalSystemResource())
	r.Register(sync.NewRelatedProjectResource())
	r.Register(sync.NewGuidelineResource())
	r.Register(sync.NewModelCatalogResource())
	return r, nil
}

func syncCmd() *cobra.Command {
	c := commandGroup(&cobra.Command{
		Use:   "sync",
		Short: "Pull canonical artifacts from the paimos instance into a local cache",
		Long: `sync — generic init / pull / watch / check engine.

Operates over a kind registry. Today the only registered kind is
"skill" (the claude-code-adapter-rendered agent files); PAI-341 will
extend with knowledge-plane kinds (memory, runbook, external_system,
related_project, guideline) using the same verbs.`,
	})
	c.AddCommand(syncInitCmd())
	c.AddCommand(syncPullCmd())
	c.AddCommand(syncWatchCmd())
	c.AddCommand(syncCheckCmd())
	return c
}

// syncInitCmd is `paimos sync init`. Pulls everything once.
func syncInitCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
	)
	c := &cobra.Command{
		Use:   "init",
		Short: "Pull canonical artifacts for every registered kind once",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncMulti(projectRef, workspaceRoot, "", "", false /* checkOnly */)
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root for adapter-suggested paths (default cwd)")
	return c
}

// syncPullCmd is `paimos sync pull`. Refresh on demand. --kind narrows
// to a single kind; --name narrows further to a single artifact.
func syncPullCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
		kind          string
		name          string
	)
	c := &cobra.Command{
		Use:   "pull",
		Short: "Refresh canonical artifacts on demand",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncMulti(projectRef, workspaceRoot, strings.TrimSpace(kind), strings.TrimSpace(name), false)
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	c.Flags().StringVar(&kind, "kind", "", "restrict to a single kind (e.g. skill)")
	c.Flags().StringVar(&name, "name", "", "restrict to a single artifact name (requires --kind)")
	return c
}

// syncCheckCmd is `paimos sync check`. Drift summary across all kinds.
func syncCheckCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
		kind          string
	)
	c := &cobra.Command{
		Use:   "check",
		Short: "Compare local cache against canonical, report drift",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncCheck(projectRef, workspaceRoot, strings.TrimSpace(kind))
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	c.Flags().StringVar(&kind, "kind", "", "restrict to a single kind")
	return c
}

// syncWatchCmd is `paimos sync watch`. Long-running SSE subscriber.
func syncWatchCmd() *cobra.Command {
	var (
		projectRef    string
		workspaceRoot string
		kind          string
	)
	c := &cobra.Command{
		Use:   "watch",
		Short: "Subscribe to canonical-state changes, re-render on event",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncWatch(projectRef, workspaceRoot, strings.TrimSpace(kind))
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id")
	c.Flags().StringVar(&workspaceRoot, "workspace", "", "workspace root (default cwd)")
	c.Flags().StringVar(&kind, "kind", "", "filter SSE stream + re-render to a single kind")
	return c
}

// runSyncMulti is the shared sync init/pull body. The two verbs differ
// only in defaults (init has no --kind / --name; pull supports both).
func runSyncMulti(projectRef, workspaceRoot, kind, name string, checkOnly bool) error {
	if strings.TrimSpace(projectRef) == "" {
		return &usageError{msg: "--project is required"}
	}
	if strings.TrimSpace(name) != "" && strings.TrimSpace(kind) == "" {
		return &usageError{msg: "--name requires --kind"}
	}

	client, err := instanceClient()
	if err != nil {
		return err
	}
	projectID, err := resolveProjectID(client, projectRef)
	if err != nil {
		return err
	}
	projectKey, err := resolveProjectKey(client, projectID)
	if err != nil {
		return err
	}

	registry, err := syncRegistryFn()
	if err != nil {
		return err
	}
	work, err := resolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}

	resources, err := selectResources(registry, kind)
	if err != nil {
		return err
	}

	syncer := &httpSyncClient{client: client}
	ctx, cancel := signalContext()
	defer cancel()

	allWritten := []sync.SyncedItem{}
	for _, res := range resources {
		err := res.Sync(ctx, syncer, projectID, projectKey, work, name, func(it sync.SyncedItem) {
			allWritten = append(allWritten, it)
			if !flagJSON {
				fmt.Fprintf(stdout, "%-7s %s/%s -> %s (rev=%s)\n",
					it.Action, it.Kind, it.Name, it.Path, it.Rev)
			}
		})
		if err != nil {
			return err
		}
	}

	if flagJSON {
		out := map[string]any{
			"project_id":  projectID,
			"project_key": projectKey,
			"items":       allWritten,
		}
		b, _ := json.Marshal(out)
		fmt.Fprintln(stdout, string(b))
	} else if len(allWritten) == 0 {
		fmt.Fprintln(stdout, "(nothing to sync)")
	}
	return nil
}

// runSyncCheck runs Resource.Check across all selected kinds, prints a
// summary, and exits non-zero if any artifact is in drift.
func runSyncCheck(projectRef, workspaceRoot, kind string) error {
	if strings.TrimSpace(projectRef) == "" {
		return &usageError{msg: "--project is required"}
	}
	client, err := instanceClient()
	if err != nil {
		return err
	}
	projectID, err := resolveProjectID(client, projectRef)
	if err != nil {
		return err
	}
	projectKey, err := resolveProjectKey(client, projectID)
	if err != nil {
		return err
	}
	registry, err := syncRegistryFn()
	if err != nil {
		return err
	}
	work, err := resolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	resources, err := selectResources(registry, kind)
	if err != nil {
		return err
	}

	syncer := &httpSyncClient{client: client}
	ctx, cancel := signalContext()
	defer cancel()

	all := []sync.CheckRecord{}
	driftCount := 0
	for _, res := range resources {
		records, err := res.Check(ctx, syncer, projectID, projectKey, work)
		if err != nil {
			return err
		}
		all = append(all, records...)
	}
	for _, rec := range all {
		if rec.State != "identical" {
			driftCount++
		}
	}

	if flagJSON {
		out := map[string]any{
			"project_id":  projectID,
			"project_key": projectKey,
			"records":     all,
			"drift_count": driftCount,
		}
		b, _ := json.Marshal(out)
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, rec := range all {
			fmt.Fprintf(stdout, "%-15s %s/%s -> %s\n", rec.State, rec.Kind, rec.Name, rec.Path)
		}
		if driftCount == 0 {
			fmt.Fprintln(stdout, "(no drift)")
		} else {
			fmt.Fprintf(stdout, "%d artifact(s) in drift\n", driftCount)
		}
	}
	if driftCount > 0 {
		return &checkExitCode{code: 1}
	}
	return nil
}

// runSyncWatch is the long-running watch loop. Subscribes to the SSE
// stream, then re-renders affected artifacts on each event. Falls
// through cleanly on signal (Ctrl-C) and on server-side disconnect
// (auto-watch toggled OFF in the UI).
func runSyncWatch(projectRef, workspaceRoot, kind string) error {
	if strings.TrimSpace(projectRef) == "" {
		return &usageError{msg: "--project is required"}
	}
	client, err := instanceClient()
	if err != nil {
		return err
	}
	projectID, err := resolveProjectID(client, projectRef)
	if err != nil {
		return err
	}
	projectKey, err := resolveProjectKey(client, projectID)
	if err != nil {
		return err
	}
	registry, err := syncRegistryFn()
	if err != nil {
		return err
	}
	work, err := resolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	resources, err := selectResources(registry, kind)
	if err != nil {
		return err
	}
	deviceID, err := resolveDeviceID()
	if err != nil {
		return err
	}

	syncer := &httpSyncClient{client: client}
	ctx, cancel := signalContext()
	defer cancel()

	fmt.Fprintf(stdout, "watching project %s (device=%s)\n", projectKey, deviceID)
	ssePath := sync.EventEndpoint(projectID, deviceID, kind, false)
	err = syncer.Stream(ctx, ssePath, func(ev sync.Event) {
		evKind := sync.EventKind(ev.Type)
		if evKind == "" {
			return
		}
		// Find matching resource(s) — there will only be one in the
		// default registry, but the loop is the future-proof shape.
		for _, res := range resources {
			if res.Kind() != evKind {
				continue
			}
			if err := res.Sync(ctx, syncer, projectID, projectKey, work, ev.Name, func(it sync.SyncedItem) {
				fmt.Fprintf(stdout, "%s %s/%s -> %s (rev=%s)\n",
					time.Now().Format(time.RFC3339), it.Action, it.Kind+"/"+it.Name, it.Path, it.Rev)
			}); err != nil {
				fmt.Fprintf(stderr, "sync %s/%s: %v\n", evKind, ev.Name, err)
			}
		}
	})
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// selectResources narrows the registered kinds to the slice the
// command will operate over. Empty kind means all.
func selectResources(reg *sync.Registry, kind string) ([]sync.Resource, error) {
	if strings.TrimSpace(kind) == "" {
		return reg.List(), nil
	}
	r, err := reg.Lookup(kind)
	if err != nil {
		return nil, err
	}
	return []sync.Resource{r}, nil
}

// resolveProjectKey loads the project key for a numeric ID. The CLI
// already has resolveProjectID for the inverse direction; this small
// helper rounds out the pair.
func resolveProjectKey(client *Client, projectID int64) (string, error) {
	body, err := client.do("GET", "/api/projects?status=all", nil)
	if err != nil {
		return "", err
	}
	var projects []projectListItem
	if err := json.Unmarshal(body, &projects); err != nil {
		return "", fmt.Errorf("decode projects: %w", err)
	}
	for _, p := range projects {
		if p.ID == projectID {
			return p.Key, nil
		}
	}
	return "", fmt.Errorf("project %d not found", projectID)
}

// resolveWorkspaceRoot picks the workspace root (--workspace or cwd).
func resolveWorkspaceRoot(flag string) (string, error) {
	flag = strings.TrimSpace(flag)
	if flag != "" {
		abs, err := filepath.Abs(flag)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	return os.Getwd()
}

// resolveDeviceID returns the persistent per-device identifier the SSE
// handshake sends to the server. Cached at ~/.paimos/device-id; created
// on first call. Honors PAIMOS_DEVICE_ID when set so containerised /
// CI invocations can pin a stable id.
func resolveDeviceID() (string, error) {
	if v := strings.TrimSpace(os.Getenv("PAIMOS_DEVICE_ID")); v != "" {
		if len(v) > 64 {
			v = v[:64]
		}
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	dir := filepath.Join(home, ".paimos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "device-id")
	// #nosec G304 -- fixed cache path under the user's own home directory.
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}
	id, err := newDeviceID()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return id, nil
}

// newDeviceID returns a fresh 32-hex-char identifier. We avoid the
// uuid dependency for this single use — 16 bytes of crypto/rand
// rendered as hex is a stable, opaque shape.
func newDeviceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "dev-" + hex.EncodeToString(b), nil
}

// signalContext wraps context.WithCancel with SIGINT/SIGTERM handling
// so the watch loop exits cleanly on Ctrl-C without leaving the SSE
// connection in an awkward half-closed state.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}
