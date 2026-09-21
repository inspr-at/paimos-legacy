// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

func TestModelCatalogCacheProvenanceDriftAndExpiry(t *testing.T) {
	resource := NewModelCatalogResource()
	root := t.TempDir()
	until := time.Now().Add(time.Hour).UTC()
	registry := dispatchprofile.NewRegistry([]dispatchprofile.Override{{ProfileID: "codex-astra-xhigh", Version: dispatchprofile.CatalogVersion, State: "unavailable", Reason: "maintenance", Until: until}})
	raw, _ := json.Marshal(registry)
	client := &fakeClient{routes: map[string][]byte{resource.Endpoint(0): raw}}
	if err := resource.Sync(context.Background(), client, 7, "PAI", root, "", nil); err != nil {
		t.Fatal(err)
	}
	cache, err := ReadModelCatalogCache(client, root)
	if err != nil {
		t.Fatal(err)
	}
	request := dispatchprofile.ResolveRequest{Role: "review-gate", AuthorFamily: dispatchprofile.Anthropic}
	before, err := cache.Registry.Resolve(request, until.Add(-time.Second))
	if err != nil || before.Profile.Family != dispatchprofile.XAI {
		t.Fatalf("before %+v %v", before, err)
	}
	after, err := cache.Registry.Resolve(request, until)
	if err != nil || after.Profile.Family != dispatchprofile.OpenAI {
		t.Fatalf("expiry %+v %v", after, err)
	}
	rows, err := resource.Check(context.Background(), client, 7, "PAI", root)
	if err != nil || rows[0].State != "identical" {
		t.Fatalf("check %+v %v", rows, err)
	}
	fresh, _ := json.Marshal(dispatchprofile.NewRegistry(nil))
	client.routes[resource.Endpoint(0)] = fresh
	rows, err = resource.Check(context.Background(), client, 7, "PAI", root)
	if err != nil || rows[0].State != "diff" {
		t.Fatalf("drift %+v %v", rows, err)
	}
	identity, _ := client.CacheIdentity()
	path := filepath.Join(root, resource.LocalPath(identity.Namespace, "catalog"))
	cache.Origin = "https://different.example"
	tampered, _ := json.Marshal(cache)
	if err = os.WriteFile(path, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadModelCatalogCache(client, root); err == nil {
		t.Fatal("origin mismatch accepted")
	}
	cache.Origin = identity.Origin
	cache.Registry.Version = "1"
	tampered, _ = json.Marshal(cache)
	if err = os.WriteFile(path, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadModelCatalogCache(client, root); err == nil {
		t.Fatal("retired cache silently upgraded")
	}
}
