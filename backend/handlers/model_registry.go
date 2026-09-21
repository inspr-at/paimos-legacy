// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const modelOverridesKey = "model_registry_overrides_v1"

func loadModelRegistry() (dispatchprofile.Registry, error) {
	var raw string
	err := db.DB.QueryRow(`SELECT value FROM app_settings WHERE key=?`, modelOverridesKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatchprofile.NewRegistry(nil), nil
	}
	if err != nil {
		return dispatchprofile.Registry{}, err
	}
	var overrides []dispatchprofile.Override
	if err = json.Unmarshal([]byte(raw), &overrides); err != nil {
		return dispatchprofile.Registry{}, err
	}
	if err = dispatchprofile.ValidateOverrides(overrides); err != nil {
		return dispatchprofile.Registry{}, err
	}
	return dispatchprofile.NewRegistry(overrides), nil
}

func ModelCatalog(w http.ResponseWriter, r *http.Request) {
	registry, err := loadModelRegistry()
	if err != nil {
		jsonError(w, "model policy unavailable", http.StatusServiceUnavailable)
		return
	}
	jsonOK(w, registry)
}

func ModelResolve(w http.ResponseWriter, r *http.Request) {
	registry, err := loadModelRegistry()
	if err != nil {
		jsonError(w, "model policy unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	result, err := registry.Resolve(dispatchprofile.ResolveRequest{Role: q.Get("role"), AuthorFamily: dispatchprofile.Family(q.Get("author_family")), Harness: q.Get("harness")}, time.Now())
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, result)
}

// Admin-only at the router. One atomic instance-local settings write keeps PPM
// and PMA policies separate, with no migration or historical profile mutation.
func PutModelOverrides(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Overrides []dispatchprofile.Override `json:"overrides"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		jsonError(w, "invalid model overrides", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		jsonError(w, "invalid model overrides", http.StatusBadRequest)
		return
	}
	if body.Overrides == nil {
		jsonError(w, "overrides must be an explicit array", http.StatusBadRequest)
		return
	}
	if err := dispatchprofile.ValidateOverrides(body.Overrides); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, override := range body.Overrides {
		found := false
		for _, p := range dispatchprofile.List() {
			if p.ID == override.ProfileID && p.Version == override.Version {
				found = true
				break
			}
		}
		if !found || !override.Until.After(time.Now()) {
			jsonError(w, "override requires a current profile pin and future expiry", http.StatusBadRequest)
			return
		}
	}
	raw, err := json.Marshal(body.Overrides)
	if err != nil {
		jsonError(w, "invalid model overrides", http.StatusBadRequest)
		return
	}
	if _, err = db.DB.Exec(`INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,datetime('now')) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=datetime('now')`, modelOverridesKey, string(raw)); err != nil {
		jsonError(w, "model policy unavailable", http.StatusInternalServerError)
		return
	}
	jsonOK(w, dispatchprofile.NewRegistry(body.Overrides))
}
