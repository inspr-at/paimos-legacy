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
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// View represents a saved column+filter set.
type View struct {
	ID             int64  `json:"id"`
	UserID         int64  `json:"user_id"`
	OwnerUsername  string `json:"owner_username"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	ColumnsJSON    string `json:"columns_json"`
	FiltersJSON    string `json:"filters_json"`
	IsShared       bool   `json:"is_shared"`
	IsAdminDefault bool   `json:"is_admin_default"`
	SortOrder      int    `json:"sort_order"`
	Hidden         bool   `json:"hidden"`
	Pinned         *bool  `json:"pinned"` // per-user pin state; nil = no explicit choice (lazy init)
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func scanView(row interface {
	Scan(...any) error
}) (View, error) {
	var v View
	var isShared, isAdminDefault, hidden int
	var pinned *int
	err := row.Scan(
		&v.ID, &v.UserID, &v.OwnerUsername,
		&v.Title, &v.Description,
		&v.ColumnsJSON, &v.FiltersJSON,
		&isShared, &isAdminDefault,
		&v.SortOrder, &hidden, &pinned,
		&v.CreatedAt, &v.UpdatedAt,
	)
	v.IsShared = isShared == 1
	v.IsAdminDefault = isAdminDefault == 1
	v.Hidden = hidden == 1
	if pinned != nil {
		b := *pinned == 1
		v.Pinned = &b
	}
	return v, err
}

// viewSelectSQL includes a LEFT JOIN on user_view_pins for the session user.
// The caller MUST supply the session user ID as the first query argument.
const viewSelectSQL = `
	SELECT v.id, v.user_id, u.username,
	       v.title, v.description, v.columns_json, v.filters_json,
	       v.is_shared, v.is_admin_default,
	       v.sort_order, v.hidden, p.pinned,
	       v.created_at, v.updated_at
	FROM views v
	JOIN users u ON u.id = v.user_id
	LEFT JOIN user_view_pins p ON p.view_id = v.id AND p.user_id = ?`

// GET /api/views
// Returns own views + all shared views (including admin-default).
// Sorted: own views first (by updated_at desc), then shared/admin by title.
func ListViews(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	shared := user.Role != auth.RoleReviewer

	rows, err := db.DB.Query(viewSelectSQL+`
		WHERE v.user_id = ? OR (? AND (v.is_shared = 1 OR v.is_admin_default = 1))
		ORDER BY
			CASE WHEN v.user_id = ? THEN 0 ELSE 1 END,
			v.sort_order ASC,
			v.updated_at DESC,
			v.title ASC
	`, user.ID, user.ID, shared, user.ID)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	views := []View{}
	for rows.Next() {
		v, err := scanView(rows)
		if err == nil {
			views = append(views, v)
		}
	}
	jsonOK(w, views)
}

// POST /api/views  { title, description?, columns_json, filters_json, is_shared?, is_admin_default? }
func CreateView(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	var body struct {
		Title          string `json:"title"`
		Description    string `json:"description"`
		ColumnsJSON    string `json:"columns_json"`
		FiltersJSON    string `json:"filters_json"`
		IsShared       bool   `json:"is_shared"`
		IsAdminDefault bool   `json:"is_admin_default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		jsonError(w, "title required", http.StatusBadRequest)
		return
	}
	// Only admins can set is_admin_default
	if body.IsAdminDefault && !auth.IsAdmin(user) {
		body.IsAdminDefault = false
	}
	if body.ColumnsJSON == "" {
		body.ColumnsJSON = "[]"
	}
	if body.FiltersJSON == "" {
		body.FiltersJSON = "{}"
	}

	res, err := db.DB.Exec(`
		INSERT INTO views(user_id, title, description, columns_json, filters_json, is_shared, is_admin_default)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, user.ID, body.Title, body.Description, body.ColumnsJSON, body.FiltersJSON,
		boolInt(body.IsShared), boolInt(body.IsAdminDefault))
	if err != nil {
		jsonError(w, "insert failed", http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()

	var v View
	row := db.DB.QueryRow(viewSelectSQL+` WHERE v.id = ?`, user.ID, id)
	v, err = scanView(row)
	if err != nil {
		jsonError(w, "fetch after insert failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(v)
}

// PUT /api/views/{id}  — own view only (admin can edit any)
func UpdateView(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Ownership check
	var ownerID int64
	if err := db.DB.QueryRow("SELECT user_id FROM views WHERE id=?", id).Scan(&ownerID); err != nil {
		jsonError(w, "view not found", http.StatusNotFound)
		return
	}
	if ownerID != user.ID && !auth.IsAdmin(user) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	var body struct {
		Title          *string `json:"title"`
		Description    *string `json:"description"`
		ColumnsJSON    *string `json:"columns_json"`
		FiltersJSON    *string `json:"filters_json"`
		IsShared       *bool   `json:"is_shared"`
		IsAdminDefault *bool   `json:"is_admin_default"`
		Hidden         *bool   `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}

	// Only admins can set is_admin_default or hidden
	if body.IsAdminDefault != nil && *body.IsAdminDefault && !auth.IsAdmin(user) {
		body.IsAdminDefault = nil
	}
	if body.Hidden != nil && !auth.IsAdmin(user) {
		body.Hidden = nil
	}

	_, err = db.DB.Exec(`
		UPDATE views SET
			title           = COALESCE(?, title),
			description     = COALESCE(?, description),
			columns_json    = COALESCE(?, columns_json),
			filters_json    = COALESCE(?, filters_json),
			is_shared       = COALESCE(?, is_shared),
			is_admin_default= COALESCE(?, is_admin_default),
			hidden          = COALESCE(?, hidden),
			updated_at      = datetime('now')
		WHERE id = ?
	`, body.Title, body.Description, body.ColumnsJSON, body.FiltersJSON,
		boolIntPtr(body.IsShared), boolIntPtr(body.IsAdminDefault),
		boolIntPtr(body.Hidden), id)
	if err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}

	var v View
	row := db.DB.QueryRow(viewSelectSQL+` WHERE v.id = ?`, user.ID, id)
	v, err = scanView(row)
	if err != nil {
		jsonError(w, "fetch after update failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, v)
}

// DELETE /api/views/{id}  — own view only (admin can delete any)
func DeleteView(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var ownerID int64
	if err := db.DB.QueryRow("SELECT user_id FROM views WHERE id=?", id).Scan(&ownerID); err != nil {
		jsonError(w, "view not found", http.StatusNotFound)
		return
	}
	if ownerID != user.ID && !auth.IsAdmin(user) {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}

	if _, err := db.DB.Exec("DELETE FROM views WHERE id=?", id); err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PATCH /api/views/order — admin bulk reorder (admin-default views)
func ReorderViews(w http.ResponseWriter, r *http.Request) {
	var body []struct {
		ID        int64 `json:"id"`
		SortOrder int   `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	for _, item := range body {
		if _, err := db.DB.Exec("UPDATE views SET sort_order = ? WHERE id = ?", item.SortOrder, item.ID); err != nil {
			log.Printf("ReorderViews: id=%d: %v", item.ID, err)
			continue
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/views/{id}/pin — user pins a view
func PinView(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	_, err = db.DB.Exec(`
		INSERT INTO user_view_pins(user_id, view_id, pinned) VALUES(?,?,1)
		ON CONFLICT(user_id, view_id) DO UPDATE SET pinned=1
	`, user.ID, id)
	if err != nil {
		jsonError(w, "pin failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/views/{id}/pin — user unpins a view
func UnpinView(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	_, err = db.DB.Exec(`
		INSERT INTO user_view_pins(user_id, view_id, pinned) VALUES(?,?,0)
		ON CONFLICT(user_id, view_id) DO UPDATE SET pinned=0
	`, user.ID, id)
	if err != nil {
		jsonError(w, "unpin failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// helpers
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func boolIntPtr(b *bool) *int {
	if b == nil {
		return nil
	}
	v := boolInt(*b)
	return &v
}
