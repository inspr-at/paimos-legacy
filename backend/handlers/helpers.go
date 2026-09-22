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

package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/backend/auth"
)

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	writeProblem(w, nil, ProblemDetails{
		Type:   fmt.Sprintf("https://paimos.com/errors/%s", codeForStatus(code)),
		Title:  http.StatusText(code),
		Status: code,
		Detail: msg,
		Code:   codeForStatus(code),
		Error:  msg,
	})
}

// ProblemDetails is the PAIMOS error envelope. It follows RFC 7807's
// core fields and adds stable machine fields for clients/agents.
type ProblemDetails struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Status      int      `json:"status"`
	Detail      string   `json:"detail"`
	Instance    string   `json:"instance,omitempty"`
	Code        string   `json:"code"`
	Field       string   `json:"field,omitempty"`
	ValidValues []string `json:"valid_values,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func problemJSON(w http.ResponseWriter, r *http.Request, p ProblemDetails) {
	writeProblem(w, r, p)
}

func writeProblem(w http.ResponseWriter, r *http.Request, p ProblemDetails) {
	if p.Status == 0 {
		p.Status = http.StatusInternalServerError
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	if p.Code == "" {
		p.Code = codeForStatus(p.Status)
	}
	if p.Type == "" {
		p.Type = fmt.Sprintf("https://paimos.com/errors/%s", p.Code)
	}
	if p.Detail == "" {
		p.Detail = p.Title
	}
	if p.Error == "" {
		p.Error = p.Detail
	}
	if r != nil {
		p.Instance = r.URL.RequestURI()
		if p.RequestID == "" {
			p.RequestID = requestIDFromRequest(r)
		}
	}
	if p.RequestID == "" {
		p.RequestID = strings.TrimSpace(w.Header().Get(RequestIDHeader))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusRequestEntityTooLarge:
		return "request_entity_too_large"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return fmt.Sprintf("http_%d", status)
	}
}

// handleDBError checks for common DB errors and sends appropriate HTTP responses.
// Returns true if an error was handled (caller should return), false if no error.
func handleDBError(w http.ResponseWriter, err error, entity string) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		jsonError(w, entity+" already exists", http.StatusConflict)
		return true
	}
	for _, status := range []string{"frozen", "archived", "deleted"} {
		msg := "project is " + status + "; new issues are disabled"
		if strings.Contains(err.Error(), msg) {
			jsonError(w, msg, http.StatusConflict)
			return true
		}
	}
	if strings.Contains(err.Error(), "project depth 1 forbids parent edges") {
		jsonError(w, "remove all parent edges before setting node_depth to 1", http.StatusConflict)
		return true
	}
	if strings.Contains(err.Error(), "bound harness ticket must be explicitly detached") {
		jsonError(w, "detach active harness sessions before changing this ticket", http.StatusConflict)
		return true
	}
	log.Printf("%s DB error: %v", entity, err)
	jsonError(w, "internal error", http.StatusInternalServerError)
	return true
}

// projectIDFilter returns a SQL fragment and matching args that restrict
// a query to the set of projects the current user can view. The returned
// clause begins with " AND " (so it can be appended to a WHERE clause).
// column is the fully qualified column name of the project ID (e.g.
// "p.id", "i.project_id"). If allowOrphans is true, rows with NULL in
// that column also pass (used for issue lists that include orphan sprints).
//
// For admins, the returned clause is empty — no filtering needed.
// For other users with no accessible projects, the clause evaluates to
// an always-false predicate so the query returns zero rows.
func projectIDFilter(r *http.Request, column string, allowOrphans bool) (string, []any) {
	if user := auth.GetUser(r); user != nil && user.Role == auth.RoleReviewer {
		allowOrphans = false // an unowned row is not an explicit project grant
	}
	ids := auth.AccessibleProjectIDs(r)
	if ids == nil {
		return "", nil // admin — no filter
	}
	if len(ids) == 0 {
		if allowOrphans {
			return " AND " + column + " IS NULL", nil
		}
		return " AND 1=0", nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if allowOrphans {
		return " AND (" + column + " IS NULL OR " + column + " IN (" + placeholders + "))", args
	}
	return " AND " + column + " IN (" + placeholders + ")", args
}
