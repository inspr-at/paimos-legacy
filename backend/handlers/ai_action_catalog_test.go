package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAIActionCatalogIsMounted(t *testing.T) {
	ts := newTestServer(t)

	resp := ts.get(t, "/api/ai/actions", ts.memberCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var body struct {
		Actions []struct {
			Key              string `json:"key"`
			Surface          string `json:"surface"`
			Placement        string `json:"placement"`
			DefaultProfileID string `json:"default_profile_id"`
			DefaultEffort    string `json:"default_effort"`
		} `json:"actions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(body.Actions) == 0 {
		t.Fatal("expected at least one AI action")
	}

	foundOptimize := false
	for _, a := range body.Actions {
		if a.Key != "optimize" {
			continue
		}
		foundOptimize = true
		if a.Surface != "issue" {
			t.Fatalf("optimize surface=%q want issue", a.Surface)
		}
		if a.Placement == "" {
			t.Fatal("optimize placement should not be empty")
		}
		if a.DefaultProfileID != "fast" || a.DefaultEffort != "low" {
			t.Fatalf("optimize defaults=(%q,%q), want (fast,low)", a.DefaultProfileID, a.DefaultEffort)
		}
	}
	if !foundOptimize {
		t.Fatal("expected optimize action in catalog")
	}
}

func TestAIExecutionOptionsIsMounted(t *testing.T) {
	ts := newTestServer(t)

	resp := ts.get(t, "/api/ai/execution-options", ts.memberCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var body struct {
		Profiles []struct {
			ID         string `json:"id"`
			Label      string `json:"label"`
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Effort     string `json:"effort"`
			SpeedLabel string `json:"speed_label"`
			CostLabel  string `json:"cost_label"`
		} `json:"profiles"`
		Efforts        []string `json:"efforts"`
		ActionDefaults map[string]struct {
			ProfileID string `json:"profile_id"`
			Effort    string `json:"effort"`
		} `json:"action_defaults"`
		SelectorDefaults struct {
			Actions map[string]struct {
				ActionKey       string `json:"action_key"`
				ProfileID       string `json:"profile_id"`
				ProfileLabel    string `json:"profile_label"`
				Effort          string `json:"effort"`
				PromptPresetRef string `json:"prompt_preset_ref"`
				ContextPack     string `json:"context_pack"`
				ProviderID      string `json:"provider_id"`
				Source          string `json:"source"`
			} `json:"actions"`
			Runs map[string]struct {
				ActionKey       string `json:"action_key"`
				ProviderID      string `json:"provider_id"`
				ProviderLabel   string `json:"provider_label"`
				ProfileID       string `json:"profile_id"`
				Effort          string `json:"effort"`
				PromptPresetRef string `json:"prompt_preset_ref"`
				ContextPack     string `json:"context_pack"`
				Source          string `json:"source"`
			} `json:"runs"`
			RowLaunch struct {
				ActionKey       string `json:"action_key"`
				ProviderID      string `json:"provider_id"`
				ProviderLabel   string `json:"provider_label"`
				PromptPresetRef string `json:"prompt_preset_ref"`
				ContextPack     string `json:"context_pack"`
				Source          string `json:"source"`
			} `json:"row_launch"`
		} `json:"selector_defaults"`
		ContextPacks []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"context_packs"`
		DispatchProfiles []struct {
			ID            string `json:"id"`
			Version       string `json:"version"`
			Harness       string `json:"harness"`
			MachineSource string `json:"machine_source"`
			AccountSource string `json:"account_source"`
		} `json:"dispatch_profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode execution options: %v", err)
	}
	if len(body.Profiles) < 4 {
		t.Fatalf("profiles len=%d, want at least default/fast/balanced/deep", len(body.Profiles))
	}
	if len(body.DispatchProfiles) < 2 {
		t.Fatalf("dispatch profiles len=%d", len(body.DispatchProfiles))
	}
	for _, profile := range body.DispatchProfiles {
		if profile.ID == "" || profile.Version != "2" || profile.Harness == "" || profile.MachineSource != "authenticated_reporter" || profile.AccountSource != "local_probe" {
			t.Fatalf("unsafe dispatch profile: %#v", profile)
		}
	}
	foundDeep := false
	for _, p := range body.Profiles {
		if p.ID == "deep" {
			foundDeep = true
			if p.Effort != "deep" || p.SpeedLabel == "" || p.CostLabel == "" {
				t.Fatalf("deep profile incomplete: %#v", p)
			}
		}
	}
	if !foundDeep {
		t.Fatal("expected deep execution profile")
	}
	if got := body.ActionDefaults["detect_duplicates"]; got.ProfileID != "deep" || got.Effort != "deep" {
		t.Fatalf("detect_duplicates defaults=%#v, want deep/deep", got)
	}
	if got := body.SelectorDefaults.Actions["optimize"]; got.ActionKey != "optimize" ||
		got.ProfileID != "fast" || got.ProfileLabel == "" || got.Effort != "low" ||
		got.PromptPresetRef != "default" || got.ContextPack != "issue" || got.Source != "global" {
		t.Fatalf("selector_defaults.actions.optimize=%#v, want safe optimize defaults", got)
	}
	if got := body.SelectorDefaults.Runs["openrouter_draft.implement"]; got.ActionKey != "openrouter_draft.implement" ||
		got.ProviderID != "openrouter" || got.ProviderLabel != "OpenRouter Draft" ||
		got.ProfileID != "balanced" || got.Effort != "standard" ||
		got.PromptPresetRef != "default" || got.ContextPack != "issue" {
		t.Fatalf("selector_defaults.runs.openrouter=%#v, want draft defaults", got)
	}
	if body.SelectorDefaults.RowLaunch.ActionKey != "claude_cli.implement" || body.SelectorDefaults.RowLaunch.ProviderID != "claude_cli" {
		t.Fatalf("row_launch default=%#v, want Claude local runner", body.SelectorDefaults.RowLaunch)
	}
	if len(body.ContextPacks) != 1 || body.ContextPacks[0].ID != "issue" {
		t.Fatalf("global context packs=%#v, want issue only", body.ContextPacks)
	}
}

func TestAIExecutionOptionsDispatchOnlyIsBoundedAndExcludesPromptMaterial(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.get(t, "/api/ai/execution-options?dispatch_only=1", ts.memberCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || len(raw["dispatch_profiles"]) == 0 {
		t.Fatalf("dispatch-only shape=%v", raw)
	}
	for _, forbidden := range []string{"prompt_presets", "knowledge_suggestions", "context_packs", "action_defaults"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("dispatch-only response exposed %s", forbidden)
		}
	}
}

func TestAIExecutionOptionsSelectorDefaultsUseProjectAgentWhenUnambiguous(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "AI Defaults", "AIDF")
	seedProjectAgent(t, projectID, "codex")

	resp := ts.get(t, fmt.Sprintf("/api/ai/execution-options?project_id=%d", projectID), ts.adminCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body struct {
		SelectorDefaults struct {
			RowLaunch struct {
				ActionKey string `json:"action_key"`
				AgentName string `json:"agent_name"`
				Source    string `json:"source"`
			} `json:"row_launch"`
			Workbench struct {
				ActionKey string `json:"action_key"`
				AgentName string `json:"agent_name"`
				Source    string `json:"source"`
			} `json:"workbench"`
			Runs map[string]struct {
				AgentName string `json:"agent_name"`
				Source    string `json:"source"`
			} `json:"runs"`
		} `json:"selector_defaults"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode execution options: %v", err)
	}
	if body.SelectorDefaults.RowLaunch.ActionKey != "claude_cli.implement" ||
		body.SelectorDefaults.RowLaunch.AgentName != "codex" ||
		body.SelectorDefaults.RowLaunch.Source != "project" {
		t.Fatalf("row_launch defaults=%#v, want project agent codex", body.SelectorDefaults.RowLaunch)
	}
	if body.SelectorDefaults.Workbench.AgentName != "codex" || body.SelectorDefaults.Workbench.Source != "project" {
		t.Fatalf("workbench defaults=%#v, want project agent codex", body.SelectorDefaults.Workbench)
	}
	if got := body.SelectorDefaults.Runs["codex_cli.implement"]; got.AgentName != "codex" || got.Source != "project" {
		t.Fatalf("codex run defaults=%#v, want project agent codex", got)
	}
}

func TestAIExecutionOptionsUseProjectAIDefaultsAndPolicy(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "AI Project Defaults", "AIPD")

	update := ts.put(t, fmt.Sprintf("/api/projects/%d", projectID), ts.adminCookie, map[string]any{
		"ai_defaults": map[string]any{
			"global": map[string]any{
				"profile_id":               "deep",
				"effort":                   "deep",
				"context_pack":             "knowledge",
				"preferred_provider_class": "hosted_model",
			},
			"actions": map[string]any{
				"spec_out": map[string]any{
					"profile_id": "balanced",
					"effort":     "standard",
				},
			},
		},
		"ai_policy": map[string]any{
			"disable_hosted_draft": true,
		},
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d", update.StatusCode)
	}
	update.Body.Close()

	resp := ts.get(t, fmt.Sprintf("/api/ai/execution-options?project_id=%d", projectID), ts.adminCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body struct {
		SelectorDefaults struct {
			Actions map[string]struct {
				ProfileID   string `json:"profile_id"`
				Effort      string `json:"effort"`
				ContextPack string `json:"context_pack"`
				Source      string `json:"source"`
			} `json:"actions"`
			RowLaunch struct {
				ActionKey   string `json:"action_key"`
				ProfileID   string `json:"profile_id"`
				Effort      string `json:"effort"`
				ContextPack string `json:"context_pack"`
				Source      string `json:"source"`
			} `json:"row_launch"`
		} `json:"selector_defaults"`
		RunProviders []struct {
			ActionKey         string `json:"action_key"`
			Available         bool   `json:"available"`
			UnavailableReason string `json:"unavailable_reason"`
		} `json:"run_providers"`
		ProjectPolicy struct {
			DisableHostedDraft bool `json:"disable_hosted_draft"`
		} `json:"project_policy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode execution options: %v", err)
	}
	if got := body.SelectorDefaults.Actions["optimize"]; got.ProfileID != "deep" ||
		got.Effort != "deep" || got.ContextPack != "knowledge" || got.Source != "project" {
		t.Fatalf("optimize selector default=%#v, want project global deep/knowledge", got)
	}
	if got := body.SelectorDefaults.Actions["spec_out"]; got.ProfileID != "balanced" ||
		got.Effort != "standard" || got.ContextPack != "knowledge" || got.Source != "project" {
		t.Fatalf("spec_out selector default=%#v, want action override plus global context", got)
	}
	if got := body.SelectorDefaults.RowLaunch; got.ActionKey != "claude_cli.implement" ||
		got.ProfileID != "deep" || got.Effort != "deep" || got.ContextPack != "knowledge" || got.Source != "project" {
		t.Fatalf("row_launch default=%#v, want policy fallback to trusted runner with project defaults", got)
	}
	if !body.ProjectPolicy.DisableHostedDraft {
		t.Fatalf("project_policy=%#v, want hosted draft disabled", body.ProjectPolicy)
	}
	var foundOpenRouter bool
	for _, provider := range body.RunProviders {
		if provider.ActionKey == "openrouter_draft.implement" {
			foundOpenRouter = true
			if provider.Available || provider.UnavailableReason != "Disabled by project AI policy." {
				t.Fatalf("openrouter provider=%#v, want policy-disabled", provider)
			}
		}
	}
	if !foundOpenRouter {
		t.Fatalf("run_providers=%#v, missing openrouter draft provider", body.RunProviders)
	}
}

func TestAIExecutionOptionsListsProjectKnowledgePromptPresetsSafely(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "AI Presets", "AIPR")

	create := ts.post(t, knowledgeBaseURL(projectID), ts.adminCookie, map[string]any{
		"type":   "memory",
		"slug":   "spec_writer",
		"title":  "Spec Writer",
		"body":   "DO NOT RETURN THIS PROMPT BODY",
		"status": "backlog",
		"metadata": map[string]any{
			"ai_prompt_preset": map[string]any{
				"enabled": true,
				"label":   "Spec Writer",
				"status":  "active",
				"actions": []string{"spec_out"},
			},
		},
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create preset status=%d", create.StatusCode)
	}
	create.Body.Close()
	plain := ts.post(t, knowledgeBaseURL(projectID), ts.adminCookie, map[string]any{
		"type":   "guideline",
		"slug":   "review_scope",
		"title":  "Review Scope",
		"body":   "DO NOT RETURN THIS CONTEXT BODY",
		"status": "backlog",
		"metadata": map[string]any{
			"category": "review",
		},
	})
	if plain.StatusCode != http.StatusCreated {
		t.Fatalf("create guideline status=%d", plain.StatusCode)
	}
	plain.Body.Close()

	resp := ts.get(t, fmt.Sprintf("/api/ai/execution-options?project_id=%d", projectID), ts.memberCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(raw), "DO NOT RETURN") {
		t.Fatalf("execution options leaked prompt body: %s", string(raw))
	}

	var body struct {
		PromptPresets []struct {
			Ref      string   `json:"ref"`
			Label    string   `json:"label"`
			Type     string   `json:"type"`
			Slug     string   `json:"slug"`
			Status   string   `json:"status"`
			Revision string   `json:"revision"`
			Actions  []string `json:"actions"`
		} `json:"prompt_presets"`
		KnowledgeSuggestions []struct {
			Ref                string   `json:"ref"`
			Type               string   `json:"type"`
			Slug               string   `json:"slug"`
			Title              string   `json:"title"`
			Revision           string   `json:"revision"`
			SuggestedUse       string   `json:"suggested_use"`
			PromptPreset       bool     `json:"prompt_preset"`
			PromptPresetRef    string   `json:"prompt_preset_ref"`
			PromptPresetLabel  string   `json:"prompt_preset_label"`
			PromptPresetStatus string   `json:"prompt_preset_status"`
			Actions            []string `json:"actions"`
		} `json:"knowledge_suggestions"`
		ContextPacks []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"context_packs"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode execution options: %v", err)
	}
	if len(body.PromptPresets) != 1 {
		t.Fatalf("prompt_presets len=%d, want 1", len(body.PromptPresets))
	}
	got := body.PromptPresets[0]
	if got.Ref != "kb:memory:spec_writer" || got.Label != "Spec Writer" || got.Type != "memory" || got.Slug != "spec_writer" {
		t.Fatalf("preset identity = %#v", got)
	}
	if got.Status != "active" || got.Revision == "" || len(got.Actions) != 1 || got.Actions[0] != "spec_out" {
		t.Fatalf("preset metadata = %#v", got)
	}
	if len(body.KnowledgeSuggestions) != 2 {
		t.Fatalf("knowledge_suggestions len=%d, want 2: %#v", len(body.KnowledgeSuggestions), body.KnowledgeSuggestions)
	}
	var foundPrompt, foundContext bool
	for _, suggestion := range body.KnowledgeSuggestions {
		switch suggestion.Ref {
		case "kb:memory:spec_writer":
			foundPrompt = true
			if !suggestion.PromptPreset || suggestion.SuggestedUse != "prompt" ||
				suggestion.PromptPresetRef != "kb:memory:spec_writer" ||
				suggestion.PromptPresetLabel != "Spec Writer" ||
				suggestion.PromptPresetStatus != "active" ||
				len(suggestion.Actions) != 1 || suggestion.Actions[0] != "spec_out" ||
				suggestion.Revision == "" {
				t.Fatalf("prompt suggestion = %#v", suggestion)
			}
		case "kb:guideline:review_scope":
			foundContext = true
			if suggestion.PromptPreset || suggestion.SuggestedUse != "context" ||
				suggestion.Title != "Review Scope" ||
				suggestion.Revision == "" {
				t.Fatalf("context suggestion = %#v", suggestion)
			}
		}
	}
	if !foundPrompt || !foundContext {
		t.Fatalf("knowledge_suggestions = %#v, want prompt and context suggestions", body.KnowledgeSuggestions)
	}
	contextIDs := map[string]bool{}
	for _, pack := range body.ContextPacks {
		contextIDs[pack.ID] = true
	}
	for _, want := range []string{"issue", "knowledge", "retrieve"} {
		if !contextIDs[want] {
			t.Fatalf("context_packs missing %q: %#v", want, body.ContextPacks)
		}
	}
}

func TestAIActionDispatcherIsMounted(t *testing.T) {
	ts := newTestServer(t)

	req, err := http.NewRequest(http.MethodPost, ts.srv.URL+"/api/ai/action", bytes.NewBufferString(`{"action":"optimize","field":"description","text":"hello"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", ts.memberCookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post dispatcher: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("expected /api/ai/action to be mounted, got 404")
	}
}
