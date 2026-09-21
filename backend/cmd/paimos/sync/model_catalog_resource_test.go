// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

// Change the namespace after the identity preflight to exercise containment
// at the actual I/O boundary, independently of identity validation.
type changingModelCacheIdentityClient struct {
	*fakeClient
	identityCalls int
}

func (c *changingModelCacheIdentityClient) CacheIdentity() (CacheIdentity, error) {
	identity, err := c.fakeClient.CacheIdentity()
	c.identityCalls++
	if c.identityCalls > 1 {
		identity.Namespace = "../../../../outside"
	}
	return identity, err
}

func TestModelCatalogCacheRejectsPathEscape(t *testing.T) {
	resource := NewModelCatalogResource()
	raw, err := json.Marshal(dispatchprofile.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"read", "write", "check"} {
		t.Run(operation, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(parent, "outside", "model_catalog.json")
			const sentinel = "outside cache must remain untouched"
			if err := WriteFileAtomic(outside, []byte(sentinel)); err != nil {
				t.Fatal(err)
			}
			client := &changingModelCacheIdentityClient{fakeClient: &fakeClient{routes: map[string][]byte{resource.Endpoint(0): raw}}}
			var err error
			switch operation {
			case "read":
				_, err = ReadModelCatalogCache(client, root)
			case "write":
				err = resource.Sync(context.Background(), client, 0, "", root, "", func(SyncedItem) {
					t.Error("escaped write reported as successful")
				})
			case "check":
				_, err = resource.Check(context.Background(), client, 0, "", root)
			}
			if err == nil || !strings.Contains(err.Error(), "escapes the workspace root") {
				t.Fatalf("path escape was not rejected before I/O: %v", err)
			}
			contents, err := os.ReadFile(outside)
			if err != nil || string(contents) != sentinel {
				t.Fatalf("outside cache changed: %q, %v", contents, err)
			}
		})
	}
}

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
