// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package dispatchprofile

import (
	"strings"
	"testing"
	"time"
)

func TestReviewGateLadderAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	r := NewRegistry(nil)
	result, err := r.Resolve(ResolveRequest{Role: "review-gate", AuthorFamily: Anthropic}, now)
	if err != nil || result.Profile.ID != "codex-astra-xhigh" || result.Command != "codex exec -m gpt-6-astra -c model_reasoning_effort=xhigh --sandbox read-only '{prompt}'" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Ladder) != 5 || result.Ladder[1].SkipReasons[0] != "author family" || result.Ladder[2].SkipReasons[0] != "author family" {
		t.Fatalf("ladder=%+v", result.Ladder)
	}
	r.Overrides = []Override{{ProfileID: "claude-fable-xhigh", Version: CatalogVersion, State: "conserved", Reason: "reserve remaining allowance", Until: now.Add(time.Hour)}}
	result, err = r.Resolve(ResolveRequest{Role: "review-gate", AuthorFamily: OpenAI}, now)
	if err != nil || result.Profile.ID != "claude-opus-xhigh" || !strings.Contains(result.Ladder[1].SkipReasons[0], "conserved") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = r.Resolve(ResolveRequest{Role: "review-gate", AuthorFamily: OpenAI}, now.Add(time.Hour))
	if err != nil || result.Profile.ID != "claude-fable-xhigh" {
		t.Fatalf("expiry result=%+v err=%v", result, err)
	}
	// A prior catalog version's override never suppresses a new route.
	r.Overrides[0].Version = "1"
	result, err = r.Resolve(ResolveRequest{Role: "review-gate", AuthorFamily: OpenAI}, now)
	if err != nil || result.Profile.ID != "claude-fable-xhigh" {
		t.Fatal("retired override affected current pin")
	}
}

func TestReviewGateOwnerAndXAI(t *testing.T) {
	now := time.Now()
	r := NewRegistry(nil)
	for _, id := range []string{"claude-fable-xhigh", "claude-opus-xhigh"} {
		r.Overrides = append(r.Overrides, Override{ProfileID: id, Version: CatalogVersion, State: "budget-limited", Reason: "allowance exhausted", Until: now.Add(time.Hour)})
	}
	result, err := r.Resolve(ResolveRequest{Role: "review-gate", AuthorFamily: OpenAI}, now)
	if err != nil || result.Profile.Family != XAI || result.Profile.Harness != "cursor" || result.Profile.Effort != "xhigh" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	r.Overrides = append(r.Overrides, Override{ProfileID: "cursor-grok-xhigh", Version: CatalogVersion, State: "unavailable", Reason: "CLI route not acknowledged", Until: now.Add(time.Hour)})
	result, err = r.Resolve(ResolveRequest{Role: "review-gate", AuthorFamily: OpenAI}, now)
	if err != nil || !result.OwnerRequired || result.Profile != nil || result.Command != "" {
		t.Fatalf("owner result=%+v err=%v", result, err)
	}
}

func TestResolveRoleFailsClosed(t *testing.T) {
	for _, request := range []ResolveRequest{
		{Role: "unknown"}, {Role: "review-gate"}, {Role: "review-gate", AuthorFamily: "unknown"},
		{Role: "build", Harness: "unknown"}, {Role: "scout", Harness: "cursor"},
	} {
		if _, err := NewRegistry(nil).Resolve(request, time.Now()); err == nil {
			t.Fatalf("accepted %+v", request)
		}
	}
	r := NewRegistry(nil)
	r.Profiles[0].Model = "unverified"
	if _, err := r.Resolve(ResolveRequest{Role: "build"}, time.Now()); err == nil {
		t.Fatal("altered catalog accepted")
	}
	if _, err := Resolve("codex-sol-high", "1", "codex"); err == nil {
		t.Fatal("retired pin silently upgraded")
	}
}

func TestRolesAndCatalogMetadata(t *testing.T) {
	r := NewRegistry(nil)
	for _, test := range []struct {
		model string
		tier  Tier
	}{{"gpt-6-luna", Fast}, {"gpt-6-terra", Standard}, {"gpt-6-sol", Strong}, {"gpt-6-astra", Frontier}} {
		found := false
		for _, model := range r.Models {
			if model.Harness == "codex" && model.ID == test.model && model.Tier == test.tier {
				found = true
			}
		}
		if !found {
			t.Fatalf("Codex model %s must have tier %s", test.model, test.tier)
		}
	}
	for _, test := range []struct{ role, id string }{{"scout", "codex-luna-medium"}, {"mechanical", "codex-luna-high"}, {"build", "codex-terra-high"}, {"build-hard", "codex-sol-xhigh"}} {
		got, err := r.Resolve(ResolveRequest{Role: test.role}, time.Now())
		if err != nil || got.Profile.ID != test.id {
			t.Fatalf("role %s: %+v %v", test.role, got, err)
		}
	}
	for _, p := range r.Profiles {
		found := false
		for _, m := range r.Models {
			if m.Harness == p.Harness && m.ID == p.Model && m.Family == p.Family && m.Tier == p.Tier {
				for _, e := range m.AllowedEfforts {
					if e == p.Effort {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("profile outside model authority: %+v", p)
		}
	}
	r.Models[0].AllowedEfforts[0] = "tampered"
	if NewRegistry(nil).Models[0].AllowedEfforts[0] == "tampered" {
		t.Fatal("mutable model storage leaked")
	}
}

func TestCommandTemplatesHonorReadOnlyRoleFlag(t *testing.T) {
	for _, test := range []struct {
		id, harness, writable, readOnly string
	}{
		{"codex-astra-xhigh", "codex",
			"codex exec -m gpt-6-astra -c model_reasoning_effort=xhigh '{prompt}'",
			"codex exec -m gpt-6-astra -c model_reasoning_effort=xhigh --sandbox read-only '{prompt}'"},
		{"claude-fable-xhigh", "claude",
			"claude -p --model fable --effort xhigh '{prompt}'",
			"claude -p --model fable --effort xhigh --permission-mode plan '{prompt}'"},
		{"cursor-grok-xhigh", "cursor",
			"cursor-agent --trust --model grok-4.7-xhigh -p '{prompt}'",
			"cursor-agent --trust --mode ask --model grok-4.7-xhigh -p '{prompt}'"},
		{"pi-anthropic-opus-xhigh", "pi",
			"pi --model anthropic/claude-opus-5:xhigh -p '{prompt}'",
			"pi --model anthropic/claude-opus-5:xhigh --tools read,grep,find,ls -p '{prompt}'"},
	} {
		t.Run(test.harness, func(t *testing.T) {
			profile, err := Resolve(test.id, CatalogVersion, test.harness)
			if err != nil {
				t.Fatal(err)
			}
			// The flag, not the role name, controls the permission boundary.
			if got := commandTemplate(profile, Role{Name: "another-role", ReadOnly: true}); got != test.readOnly {
				t.Fatalf("read-only command = %q, want %q", got, test.readOnly)
			}
			if got := commandTemplate(profile, Role{Name: "review-gate"}); got != test.writable {
				t.Fatalf("writable command = %q, want %q", got, test.writable)
			}
		})
	}
}

func TestReviewGateRejectsWritableCommandsAnywhereInLadder(t *testing.T) {
	request := ResolveRequest{Role: "review-gate", AuthorFamily: Anthropic}
	for index := 0; index < 4; index++ {
		result, err := NewRegistry(nil).Resolve(request, time.Now())
		if err != nil || !result.Role.ReadOnly {
			t.Fatalf("review role: %+v, %v", result.Role, err)
		}
		candidate := &result.Ladder[index]
		candidate.Command = commandTemplate(*candidate.Profile, Role{})
		if candidate.Selected {
			result.Command = candidate.Command
		}
		if ValidateResolution(request, result) == nil {
			t.Fatalf("writable command accepted for %s", candidate.ProfileID)
		}
	}
	result, err := NewRegistry(nil).Resolve(ResolveRequest{Role: "build"}, time.Now())
	if err != nil || result.Role.ReadOnly || result.Command != "codex exec -m gpt-6-terra -c model_reasoning_effort=high '{prompt}'" {
		t.Fatalf("build command changed permission mode: %+v, %v", result, err)
	}
}

func TestValidateResolutionRejectsMalformedOrWidenedResponse(t *testing.T) {
	request := ResolveRequest{Role: "review-gate", AuthorFamily: Anthropic}
	for _, change := range []func(*Resolution){
		func(r *Resolution) { r.CatalogVersion = "1" },
		func(r *Resolution) { r.Command = "unapproved command" },
		func(r *Resolution) { r.Role.ReadOnly = false },
		func(r *Resolution) { r.Profile.Model = "unverified" },
		func(r *Resolution) { r.Ladder[1].SkipReasons = nil },
		func(r *Resolution) { r.Ladder[1].Selected = true },
		func(r *Resolution) { r.Profile = nil },
	} {
		result, err := NewRegistry(nil).Resolve(request, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateResolution(request, result); err != nil {
			t.Fatal(err)
		}
		change(&result)
		if ValidateResolution(request, result) == nil {
			t.Fatal("malformed resolution accepted")
		}
	}
}
