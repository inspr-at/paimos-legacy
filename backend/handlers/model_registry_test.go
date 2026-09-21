// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/models"
)

func TestModelRegistryPolicyAPI(t *testing.T) {
	openChangesTestDB(t)
	call := func(handler http.Handler, method, path, body, role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), auth.UserKey, &models.User{ID: 1, Role: role}))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	override := dispatchprofile.Override{ProfileID: "codex-astra-xhigh", Version: dispatchprofile.CatalogVersion, State: "budget-limited", Reason: "reserve capacity", Until: time.Now().Add(time.Hour).UTC()}
	raw, _ := json.Marshal(map[string]any{"overrides": []dispatchprofile.Override{override}})
	put := auth.RequireAdmin(http.HandlerFunc(PutModelOverrides))
	if w := call(put, "PUT", "/api/models/overrides", string(raw), "member"); w.Code != 403 {
		t.Fatalf("member status %d", w.Code)
	}
	if w := call(put, "PUT", "/api/models/overrides", string(raw), "admin"); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	w := call(http.HandlerFunc(ModelResolve), "GET", "/api/models/resolve?role=review-gate&author_family=anthropic", "", "member")
	var result dispatchprofile.Resolution
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Profile.ID != "cursor-grok-xhigh" || len(result.Ladder[0].SkipReasons) != 1 {
		t.Fatalf("resolve %d %s", w.Code, w.Body)
	}
	// Reload from durable settings; no process-local policy state.
	r, err := loadModelRegistry()
	if err != nil || len(r.Overrides) != 1 {
		t.Fatalf("persisted %+v %v", r, err)
	}
	for _, body := range []string{`{}`, `{"overrides":null}`, `{"overrides":[{}]}`, `{"unknown":1}`, string(raw) + `{}`} {
		if w = call(put, "PUT", "/api/models/overrides", body, "admin"); w.Code != 400 {
			t.Fatalf("invalid accepted %s", body)
		}
	}
	w = call(http.HandlerFunc(ModelResolve), "GET", "/api/models/resolve?role=unknown", "", "member")
	if w.Code != 400 {
		t.Fatalf("unknown status %d", w.Code)
	}
	if _, err = db.DB.Exec(`UPDATE app_settings SET value='invalid' WHERE key=?`, modelOverridesKey); err != nil {
		t.Fatal(err)
	}
	w = call(http.HandlerFunc(ModelResolve), "GET", "/api/models/resolve?role=build", "", "member")
	if w.Code != 503 {
		t.Fatal("corrupt policy did not fail closed")
	}
}
