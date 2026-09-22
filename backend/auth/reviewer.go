// PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only.
package auth

import (
	"net/http"
	"strconv"
	"strings"
)

// reviewerReadOnly is deliberately an allowlist. Internal endpoints historically
// include instance-wide CRM, directories and operator state. New routes must be
// reviewed before this role may reach them, even when they are GET requests.
// Authentication and all existing per-resource checks still run normally.
func reviewerReadOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r)
		if user == nil || user.Role != RoleReviewer {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		if !reviewerRouteAllowed(r) {
			http.Error(w, `{"error":"reviewer access is limited to read-only shared projects","code":"reviewer_scope"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func reviewerRouteAllowed(r *http.Request) bool {
	path := r.URL.Path
	if r.Method == http.MethodPost && path == "/api/auth/logout" {
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch path {
	case "/api/auth/me", "/api/auth/totp/status", "/api/instance", "/api/brandings",
		"/api/permissions/matrix", "/api/command-palette/v1/settings",
		"/api/projects", "/api/issues", "/api/issues/recent", "/api/search",
		"/api/agent-mode/orchestration/v1", "/api/agent-mode/deliveries",
		"/api/agent-mode/worker-fleet/v1", "/api/agent-mode/worker-fleet/v2",
		"/api/users", "/api/tags", "/api/views", "/api/users/me/recent-projects":
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/"), "/")
	if !strings.HasPrefix(path, "/api/") || len(parts) < 2 {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err == nil && id > 0 && strconv.FormatInt(id, 10) == parts[1] {
		suffix := strings.Join(parts[2:], "/")
		switch parts[0] {
		case "projects":
			if !CanViewProject(r, id) {
				return false
			}
			switch suffix {
			case "", "issues", "repos", "agents", "environments", "deploy-recipes", "tags", "releases",
				"knowledge", "command-palette/v1", "session-home/v1", "session-home/zoom/v1",
				"sessions", "nodes", "baseline-batches", "baseline-batches/", "baseline-batches/flow-state":
				return true
			}
		case "issues":
			project, found, orphan := ProjectIDForIssue(id)
			if !found || orphan || !CanViewProject(r, project) {
				return false
			}
			switch suffix {
			case "", "comments", "history", "tags", "attachments":
				return true
			}
		}
	}
	// Worker snapshots use the independently tested Agent Mode authorization
	// CTE. Require an explicit project at this outer boundary as well.
	if len(parts) >= 4 && parts[0] == "agent-mode" && parts[1] == "projects" {
		project, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || project <= 0 || !CanViewProject(r, project) {
			return false
		}
		switch strings.Join(parts[3:], "/") {
		case "worker-fleet/v1", "worker-fleet/v2", "orchestration/v1", "deliveries":
			return true
		}
	}
	return false
}
