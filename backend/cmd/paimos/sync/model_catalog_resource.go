// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

type ModelCatalogCache struct {
	Schema    int                      `json:"schema"`
	Instance  string                   `json:"instance"`
	Origin    string                   `json:"origin"`
	FetchedAt time.Time                `json:"fetched_at"`
	Registry  dispatchprofile.Registry `json:"registry"`
}

type ModelCatalogResource struct{}

func NewModelCatalogResource() *ModelCatalogResource { return &ModelCatalogResource{} }
func (*ModelCatalogResource) Kind() string           { return "model_catalog" }
func (*ModelCatalogResource) Endpoint(int64) string  { return "/api/models/catalog" }
func (*ModelCatalogResource) LocalPath(namespace, name string) string {
	return filepath.Join(".paimos", "cache", "instances", namespace, "model_catalog.json")
}
func (*ModelCatalogResource) HeaderRev(a, b []byte) bool {
	var x, y ModelCatalogCache
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && x.Schema == y.Schema && x.Instance == y.Instance && x.Origin == y.Origin && registriesEqual(x.Registry, y.Registry)
}
func registriesEqual(a, b dispatchprofile.Registry) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func modelCacheIdentity(c SyncClient) (CacheIdentity, error) {
	if _, err := knowledgeCacheProjectKey(c, "catalog"); err != nil {
		return CacheIdentity{}, err
	}
	return c.CacheIdentity()
}
func (m *ModelCatalogResource) Sync(ctx context.Context, c SyncClient, _ int64, _ string, root, name string, written func(SyncedItem)) error {
	if name != "" && name != "catalog" {
		return errors.New("model_catalog has only the catalog artifact")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := modelCacheIdentity(c)
	if err != nil {
		return err
	}
	raw, err := c.Get(m.Endpoint(0))
	if err != nil {
		return err
	}
	var registry dispatchprofile.Registry
	if err = json.Unmarshal(raw, &registry); err != nil {
		return err
	}
	if err = registry.Validate(); err != nil {
		return err
	}
	cache := ModelCatalogCache{Schema: 1, Instance: identity.Instance, Origin: identity.Origin, FetchedAt: time.Now().UTC(), Registry: registry}
	body, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	path, err := joinWorkspacePath(root, m.LocalPath(identity.Namespace, "catalog"))
	if err != nil {
		return err
	}
	// Refresh the timestamp even when the immutable catalog/policy is unchanged.
	if err = WriteFileAtomic(path, body); err != nil {
		return err
	}
	if written != nil {
		written(SyncedItem{Kind: m.Kind(), Name: "catalog", Path: path, Rev: registry.Version, Action: "wrote"})
	}
	return nil
}
func ReadModelCatalogCache(c SyncClient, root string) (ModelCatalogCache, error) {
	var cache ModelCatalogCache
	identity, err := modelCacheIdentity(c)
	if err != nil {
		return cache, err
	}
	path, err := joinWorkspacePath(root, NewModelCatalogResource().LocalPath(identity.Namespace, "catalog"))
	if err != nil {
		return cache, err
	}
	// #nosec G304 -- target is contained in workspaceRoot by joinWorkspacePath.
	raw, err := os.ReadFile(path)
	if err != nil {
		return cache, err
	}
	if err = json.Unmarshal(raw, &cache); err != nil {
		return cache, err
	}
	if cache.Schema != 1 || cache.Instance != identity.Instance || cache.Origin != identity.Origin || cache.FetchedAt.IsZero() || cache.FetchedAt.After(time.Now().Add(time.Minute)) {
		return cache, errors.New("model catalog cache provenance is invalid")
	}
	return cache, cache.Registry.Validate()
}
func (m *ModelCatalogResource) Check(ctx context.Context, c SyncClient, _ int64, _ string, root string) ([]CheckRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := modelCacheIdentity(c)
	if err != nil {
		return nil, err
	}
	raw, err := c.Get(m.Endpoint(0))
	if err != nil {
		return nil, err
	}
	var registry dispatchprofile.Registry
	if err = json.Unmarshal(raw, &registry); err != nil {
		return nil, err
	}
	if err = registry.Validate(); err != nil {
		return nil, err
	}
	path, err := joinWorkspacePath(root, m.LocalPath(identity.Namespace, "catalog"))
	if err != nil {
		return nil, err
	}
	row := CheckRecord{Kind: m.Kind(), Name: "catalog", Path: path, Rev: registry.Version, State: "diff"}
	cached, err := ReadModelCatalogCache(c, root)
	if os.IsNotExist(err) {
		row.State = "missing_local"
	} else if err != nil {
		return nil, err
	} else if registriesEqual(registry, cached.Registry) {
		row.State = "identical"
	}
	return []CheckRecord{row}, nil
}
