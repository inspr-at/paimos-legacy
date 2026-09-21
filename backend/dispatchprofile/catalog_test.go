// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package dispatchprofile

import "testing"

func TestCatalogIsStableClosedAndDetached(t *testing.T) {
	profiles := List()
	if len(profiles) != 32 {
		t.Fatalf("profile count = %d", len(profiles))
	}
	var sawPi, sawCursor bool
	for index, profile := range profiles {
		if err := Validate(profile); err != nil {
			t.Fatalf("profile %q: %v", profile.ID, err)
		}
		if profile.ID == "pi-anthropic-sonnet-high" {
			sawPi = true
			if profile.Harness != "pi" {
				t.Fatalf("pi profile harness=%q", profile.Harness)
			}
		}
		if profile.ID == "cursor-composer" {
			sawCursor = true
			if profile.Harness != "cursor" || profile.Model != "composer-2.5" || profile.Effort != "default" {
				t.Fatalf("cursor profile=%#v", profile)
			}
		}
		if profile.ID == "cursor-grok" {
			if profile.Harness != "cursor" || profile.Model != "grok-4.7-high" || profile.Effort != "high" {
				t.Fatalf("cursor grok profile=%#v", profile)
			}
		}
		if index > 0 && profiles[index-1].ID >= profile.ID {
			t.Fatal("catalog is not in stable id order")
		}
		resolved, err := Resolve(profile.ID, profile.Version, profile.Harness)
		if err != nil || resolved != profile {
			t.Fatalf("resolve %q = %#v, %v", profile.ID, resolved, err)
		}
	}
	if !sawPi {
		t.Fatal("catalog lost the human-selected Pi profile")
	}
	if !sawCursor {
		t.Fatal("catalog lost the human-selected Cursor Composer profile")
	}
	profiles[0].Model = "tampered"
	if fresh := List(); fresh[0].Model == "tampered" {
		t.Fatal("List leaked mutable catalog storage")
	}
}

func TestResolveRejectsEveryUnpinnedAxis(t *testing.T) {
	profile := List()[0]
	for _, test := range []struct{ id, version, harness string }{
		{"missing", profile.Version, profile.Harness},
		{profile.ID, "latest", profile.Harness},
		{profile.ID, profile.Version, "other"},
	} {
		if _, err := Resolve(test.id, test.version, test.harness); err == nil {
			t.Fatalf("Resolve(%q,%q,%q) succeeded", test.id, test.version, test.harness)
		}
	}
}

func TestValidateSnapshotDoesNotRequireLiveCatalogMembership(t *testing.T) {
	profile := Profile{ID: "retired-profile", Version: "2026-08", Harness: "codex", Model: "retired-model", Effort: "high",
		MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"}
	if err := ValidateSnapshot(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(profile.ID, profile.Version, profile.Harness); err == nil {
		t.Fatal("retired snapshot unexpectedly became a live catalog entry")
	}
	if err := ValidateSnapshot(Profile{ID: "cursor-default", Version: "1", Harness: "codex", Model: "gpt-6-sol", Effort: "default",
		MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"}); err == nil {
		t.Fatal("default effort leaked onto a non-Cursor harness")
	}
}
