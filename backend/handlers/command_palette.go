// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	commandPaletteSchemaVersion = 1
	commandPaletteDefault       = "Mod+KeyK"
	commandPaletteSettingKey    = "command_palette_shortcut"
	commandPaletteBodyLimit     = 8 * 1024
	commandPaletteQueryMaxBytes = 128
)

var commandPaletteKeyCodes = func() map[string]bool {
	out := map[string]bool{
		"Space": true, "Slash": true, "Backslash": true, "BracketLeft": true, "BracketRight": true,
		"Semicolon": true, "Quote": true, "Comma": true, "Period": true, "Minus": true, "Equal": true, "Backquote": true,
	}
	for value := 'A'; value <= 'Z'; value++ {
		out["Key"+string(value)] = true
	}
	for value := '0'; value <= '9'; value++ {
		out["Digit"+string(value)] = true
	}
	return out
}()

var commandPaletteReservedCodes = map[string]bool{
	"KeyR": true, "KeyL": true, "KeyW": true, "KeyQ": true, "KeyT": true, "KeyN": true, "KeyP": true, "KeyF": true,
	"Digit1": true, "Digit2": true, "Digit3": true, "Digit4": true, "Digit5": true,
	"Digit6": true, "Digit7": true, "Digit8": true, "Digit9": true,
}

type commandPaletteSettingBody struct {
	Shortcut json.RawMessage `json:"shortcut"`
}

func RegisterCommandPaletteRoutes(r chi.Router) {
	r.Get("/command-palette/v1/settings", GetCommandPaletteSettings)
	r.Put("/command-palette/v1/settings", PutCommandPaletteSettings)
	r.With(auth.RequireAdmin).Put("/command-palette/v1/instance-settings", PutCommandPaletteInstanceSettings)
	r.With(auth.RequireProjectView).Get("/projects/{id}/command-palette/v1", SearchCommandPalette)
	r.Handle("/command-palette/v1/*", http.HandlerFunc(http.NotFound))
	r.With(auth.RequireProjectView).Handle("/projects/{id}/command-palette/v1/*", http.HandlerFunc(http.NotFound))
}

func GetCommandPaletteSettings(w http.ResponseWriter, r *http.Request) {
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeCommandPaletteMiddlewareError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !auth.IsInternalRole(user.Role) {
		writeCommandPaletteMiddlewareError(w, http.StatusForbidden, "forbidden")
		return
	}
	settings, err := loadCommandPaletteSettings(r.Context(), tx, user.ID)
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeCommandPaletteJSON(w, http.StatusOK, settings)
}

func PutCommandPaletteSettings(w http.ResponseWriter, r *http.Request) {
	shortcut, code := decodeCommandPaletteShortcut(w, r)
	if code != "" {
		writeCommandPaletteError(w, http.StatusBadRequest, code)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeCommandPaletteMiddlewareError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !auth.IsInternalRole(user.Role) {
		writeCommandPaletteMiddlewareError(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE users SET command_palette_shortcut=? WHERE id=?`, shortcut, user.ID); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	settings, err := loadCommandPaletteSettings(r.Context(), tx, user.ID)
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeCommandPaletteJSON(w, http.StatusOK, settings)
}

func PutCommandPaletteInstanceSettings(w http.ResponseWriter, r *http.Request) {
	shortcut, code := decodeCommandPaletteShortcut(w, r)
	if code != "" {
		writeCommandPaletteError(w, http.StatusBadRequest, code)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeCommandPaletteMiddlewareError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !auth.IsAdmin(user) {
		writeCommandPaletteMiddlewareError(w, http.StatusForbidden, "forbidden")
		return
	}
	if shortcut == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM app_settings WHERE key=?`, commandPaletteSettingKey)
	} else {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,datetime('now'))
			ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, commandPaletteSettingKey, *shortcut)
	}
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	settings, err := loadCommandPaletteSettings(r.Context(), tx, user.ID)
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeCommandPaletteJSON(w, http.StatusOK, settings)
}

type commandPaletteQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadCommandPaletteSettings(ctx context.Context, queryer commandPaletteQueryer, userID int64) (models.CommandPaletteSettings, error) {
	out := models.CommandPaletteSettings{SchemaVersion: commandPaletteSchemaVersion, DefaultShortcut: commandPaletteDefault}
	var userShortcut sql.NullString
	if err := queryer.QueryRowContext(ctx, `SELECT command_palette_shortcut FROM users WHERE id=?`, userID).Scan(&userShortcut); err != nil {
		return out, err
	}
	var instanceShortcut string
	err := queryer.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key=?`, commandPaletteSettingKey).Scan(&instanceShortcut)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if userShortcut.Valid {
		if valid, _ := classifyCommandPaletteShortcut(userShortcut.String); !valid {
			return out, errors.New("invalid stored user command palette shortcut")
		}
		value := userShortcut.String
		out.UserShortcut = &value
	}
	if err == nil {
		if valid, _ := classifyCommandPaletteShortcut(instanceShortcut); !valid {
			return out, errors.New("invalid stored instance command palette shortcut")
		}
		out.InstanceShortcut = &instanceShortcut
	}
	switch {
	case out.UserShortcut != nil:
		out.EffectiveShortcut, out.Source = *out.UserShortcut, "user"
	case out.InstanceShortcut != nil:
		out.EffectiveShortcut, out.Source = *out.InstanceShortcut, "instance"
	default:
		out.EffectiveShortcut, out.Source = commandPaletteDefault, "default"
	}
	return out, nil
}

func decodeCommandPaletteShortcut(w http.ResponseWriter, r *http.Request) (*string, string) {
	var body commandPaletteSettingBody
	if err := DecodeControlJSON(w, r, commandPaletteBodyLimit, &body); err != nil {
		return nil, "invalid_request"
	}
	if len(body.Shortcut) == 0 {
		return nil, "invalid_request"
	}
	if strings.TrimSpace(string(body.Shortcut)) == "null" {
		return nil, ""
	}
	var value string
	if err := json.Unmarshal(body.Shortcut, &value); err != nil {
		return nil, "invalid_shortcut"
	}
	valid, collision := classifyCommandPaletteShortcut(value)
	if collision {
		return nil, "shortcut_collision"
	}
	if !valid {
		return nil, "invalid_shortcut"
	}
	return &value, ""
}

func classifyCommandPaletteShortcut(value string) (valid, collision bool) {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false, false
	}
	parts := strings.Split(value, "+")
	if len(parts) < 2 || !commandPaletteKeyCodes[parts[len(parts)-1]] {
		return false, false
	}
	modifiers := parts[:len(parts)-1]
	order := map[string]int{"Mod": 0, "Ctrl": 1, "Meta": 2, "Alt": 3, "Shift": 4}
	seen := map[string]bool{}
	previous := -1
	primary := false
	for _, modifier := range modifiers {
		position, ok := order[modifier]
		if !ok || seen[modifier] || position <= previous {
			return false, false
		}
		seen[modifier] = true
		previous = position
		if modifier == "Mod" || modifier == "Ctrl" || modifier == "Meta" || modifier == "Alt" {
			primary = true
		}
	}
	if !primary || (seen["Mod"] && (seen["Ctrl"] || seen["Meta"])) {
		return false, false
	}
	keyCode := parts[len(parts)-1]
	if commandPaletteReservedCodes[keyCode] && (seen["Mod"] || seen["Ctrl"] || seen["Meta"]) {
		return false, true
	}
	return true, false
}

func SearchCommandPalette(w http.ResponseWriter, r *http.Request) {
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	query, limit, ok := parseCommandPaletteQuery(r.URL)
	if !ok {
		writeCommandPaletteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	out := models.CommandPaletteSearchResponse{SchemaVersion: commandPaletteSchemaVersion, Query: query,
		Sessions: []models.CommandPaletteSessionResult{}, Nodes: []models.CommandPaletteNodeResult{}, Knowledge: []models.CommandPaletteKnowledgeResult{}}
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer tx.Rollback()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil {
		writeCommandPaletteMiddlewareError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !commandPaletteCanViewProjectTx(r.Context(), tx, user, projectID) {
		writeCommandPaletteMiddlewareError(w, http.StatusNotFound, "not found")
		return
	}
	if out.Sessions, err = searchCommandPaletteSessions(r.Context(), tx, projectID, query, limit); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if out.Nodes, err = searchCommandPaletteNodes(r.Context(), tx, projectID, query, limit); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if out.Knowledge, err = searchCommandPaletteKnowledge(r.Context(), tx, projectID, query, limit); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeCommandPaletteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeCommandPaletteJSON(w, http.StatusOK, out)
}

func commandPaletteCanViewProjectTx(ctx context.Context, tx *sql.Tx, user *models.User, projectID int64) bool {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=?`, projectID).Scan(&status); err != nil || status == "deleted" {
		return false
	}
	if auth.IsAdmin(user) {
		return true
	}
	if user == nil || user.Status != "active" || (user.Role != auth.RoleMember && user.Role != auth.RoleReviewer) {
		return false
	}
	var level string
	err := tx.QueryRowContext(ctx, `SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`, user.ID, projectID).Scan(&level)
	return (user.Role == auth.RoleMember && errors.Is(err, sql.ErrNoRows)) || (err == nil && (level == "viewer" || level == "editor"))
}

func parseCommandPaletteQuery(uri *url.URL) (string, int, bool) {
	values, err := url.ParseQuery(uri.RawQuery)
	if err != nil {
		return "", 0, false
	}
	for key, entries := range values {
		if (key != "q" && key != "limit") || len(entries) != 1 {
			return "", 0, false
		}
	}
	queries, exists := values["q"]
	if !exists {
		return "", 0, false
	}
	query := strings.TrimSpace(queries[0])
	if query == "" || len([]byte(query)) > commandPaletteQueryMaxBytes || !utf8.ValidString(query) {
		return "", 0, false
	}
	limit := 8
	if entries, exists := values["limit"]; exists {
		parsed, err := strconv.Atoi(entries[0])
		if err != nil || parsed < 1 || parsed > 20 || strconv.Itoa(parsed) != entries[0] {
			return "", 0, false
		}
		limit = parsed
	}
	return query, limit, true
}

func searchCommandPaletteSessions(ctx context.Context, tx *sql.Tx, projectID int64, query string, limit int) ([]models.CommandPaletteSessionResult, error) {
	needle := query
	rows, err := tx.QueryContext(ctx, `SELECT product_session_id,title,summary,updated_at
		FROM product_sessions WHERE project_id=? AND (instr(paimos_casefold(title),paimos_casefold(?))>0 OR instr(paimos_casefold(summary),paimos_casefold(?))>0)
		ORDER BY CASE WHEN paimos_casefold(title)=paimos_casefold(?) OR paimos_casefold(summary)=paimos_casefold(?) THEN 0
		              WHEN substr(paimos_casefold(title),1,length(paimos_casefold(?)))=paimos_casefold(?) OR substr(paimos_casefold(summary),1,length(paimos_casefold(?)))=paimos_casefold(?) THEN 1 ELSE 2 END,
		         paimos_casefold(title),product_session_id LIMIT ?`, projectID, needle, needle, needle, needle, needle, needle, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.CommandPaletteSessionResult{}
	for rows.Next() {
		var item models.CommandPaletteSessionResult
		if err := rows.Scan(&item.ProductSessionID, &item.Title, &item.Summary, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func searchCommandPaletteNodes(ctx context.Context, tx *sql.Tx, projectID int64, query string, limit int) ([]models.CommandPaletteNodeResult, error) {
	needle := query
	rows, err := tx.QueryContext(ctx, `SELECT i.id,p.key||'-'||i.issue_number,i.title,i.type,
		COALESCE(o.label,d.label,'Node'),i.status,i.updated_at
		FROM issues i JOIN projects p ON p.id=i.project_id
		LEFT JOIN node_label_defaults d ON d.issue_type=i.type
		LEFT JOIN project_node_label_overrides o ON o.project_id=i.project_id AND o.issue_type=i.type
		WHERE i.project_id=? AND i.deleted_at IS NULL
		 AND i.type NOT IN ('memory','runbook','external_system','related_project','guideline')
		 AND (instr(paimos_casefold(i.title),paimos_casefold(?))>0 OR instr(paimos_casefold(p.key||'-'||i.issue_number),paimos_casefold(?))>0 OR instr(paimos_casefold(COALESCE(o.label,d.label,'Node')),paimos_casefold(?))>0)
		ORDER BY CASE WHEN paimos_casefold(i.title)=paimos_casefold(?) OR paimos_casefold(p.key||'-'||i.issue_number)=paimos_casefold(?) OR paimos_casefold(COALESCE(o.label,d.label,'Node'))=paimos_casefold(?) THEN 0
		              WHEN substr(paimos_casefold(i.title),1,length(paimos_casefold(?)))=paimos_casefold(?) OR substr(paimos_casefold(p.key||'-'||i.issue_number),1,length(paimos_casefold(?)))=paimos_casefold(?)
		                OR substr(paimos_casefold(COALESCE(o.label,d.label,'Node')),1,length(paimos_casefold(?)))=paimos_casefold(?) THEN 1 ELSE 2 END,
		         paimos_casefold(i.title),paimos_casefold(p.key||'-'||i.issue_number),i.id LIMIT ?`,
		projectID, needle, needle, needle, needle, needle, needle,
		needle, needle, needle, needle, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.CommandPaletteNodeResult{}
	for rows.Next() {
		var item models.CommandPaletteNodeResult
		var storedUpdatedAt string
		if err := rows.Scan(&item.NodeID, &item.NodeKey, &item.Title, &item.Type, &item.TypeLabel, &item.Status, &storedUpdatedAt); err != nil {
			return nil, err
		}
		item.UpdatedAt, err = canonicalCommandPaletteTimestamp(storedUpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func searchCommandPaletteKnowledge(ctx context.Context, tx *sql.Tx, projectID int64, query string, limit int) ([]models.CommandPaletteKnowledgeResult, error) {
	needle := query
	rows, err := tx.QueryContext(ctx, `SELECT i.id,replace(i.type,'_','-'),COALESCE(o.label,d.label,'Knowledge'),i.slug,i.title,i.updated_at
		FROM issues i
		LEFT JOIN node_label_defaults d ON d.issue_type=i.type
		LEFT JOIN project_node_label_overrides o ON o.project_id=i.project_id AND o.issue_type=i.type
		WHERE i.project_id=? AND i.deleted_at IS NULL AND i.slug IS NOT NULL
		 AND i.type IN ('memory','runbook','external_system','related_project','guideline')
		 AND (instr(paimos_casefold(i.title),paimos_casefold(?))>0 OR instr(paimos_casefold(i.slug),paimos_casefold(?))>0 OR instr(paimos_casefold(COALESCE(o.label,d.label,'Knowledge')),paimos_casefold(?))>0)
		ORDER BY CASE WHEN paimos_casefold(i.title)=paimos_casefold(?) OR paimos_casefold(i.slug)=paimos_casefold(?) OR paimos_casefold(COALESCE(o.label,d.label,'Knowledge'))=paimos_casefold(?) THEN 0
		              WHEN substr(paimos_casefold(i.title),1,length(paimos_casefold(?)))=paimos_casefold(?) OR substr(paimos_casefold(i.slug),1,length(paimos_casefold(?)))=paimos_casefold(?)
		                OR substr(paimos_casefold(COALESCE(o.label,d.label,'Knowledge')),1,length(paimos_casefold(?)))=paimos_casefold(?) THEN 1 ELSE 2 END,
		         paimos_casefold(i.title),paimos_casefold(i.slug),i.id LIMIT ?`,
		projectID, needle, needle, needle, needle, needle, needle,
		needle, needle, needle, needle, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.CommandPaletteKnowledgeResult{}
	for rows.Next() {
		var item models.CommandPaletteKnowledgeResult
		var storedUpdatedAt string
		if err := rows.Scan(&item.KnowledgeID, &item.Type, &item.TypeLabel, &item.Slug, &item.Title, &storedUpdatedAt); err != nil {
			return nil, err
		}
		item.UpdatedAt, err = canonicalCommandPaletteTimestamp(storedUpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func canonicalCommandPaletteTimestamp(stored string) (string, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, stored); err == nil {
		return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, stored, time.UTC); err == nil {
			return parsed.Format("2006-01-02T15:04:05.000Z"), nil
		}
	}
	return "", fmt.Errorf("invalid stored command palette timestamp")
}

func writeCommandPaletteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeCommandPaletteError(w http.ResponseWriter, status int, code string) {
	writeCommandPaletteJSON(w, status, map[string]string{"error": code})
}

func writeCommandPaletteMiddlewareError(w http.ResponseWriter, status int, message string) {
	http.Error(w, `{"error":"`+message+`"}`, status)
}
