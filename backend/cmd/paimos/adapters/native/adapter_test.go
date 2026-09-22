package native

import (
	"encoding/json"
	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters"
	"gopkg.in/yaml.v3"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeSkills(t *testing.T) {
	for _, adapter := range All() {
		t.Run(adapter.Name(), func(t *testing.T) {
			report := adapters.RunConformance(adapter, adapters.ConformanceOptions{})
			if !report.AllPassed() {
				t.Fatalf("%+v", report)
			}
			registry := adapters.NewRegistry()
			registry.Register(adapter)
			raw, _ := json.Marshal(map[string]any{"project": map[string]string{"key": "PAI"}, "agent": map[string]any{"name": "qa", "slash_command_name": "review-work", "description": "Use for: \"reviews\"\nwith @signs", "body": "Canonical instructions.", "bootstrap_steps": []map[string]string{{"title": "Setup", "command": "go test ./..."}}, "non_negotiable_rules": []map[string]string{{"title": "Safety", "body": "Preserve data.", "memory_ref": "rules"}}}, "deploy_recipes": []map[string]string{{"name": "Deploy", "command": "just deploy"}}})
			out, err := (&adapters.Dispatch{Registry: registry}).Render(adapters.RenderRequest{Canonical: raw, HarnessName: adapter.Name(), ProjectKey: "PAI", AgentName: "qa"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.Body, "---\n") || !adapters.HasHeader(out.Body) {
				t.Fatal(out.Body)
			}
			fields := map[string]string{}
			if err := yaml.Unmarshal([]byte(strings.SplitN(out.Body, "---\n", 3)[1]), &fields); err != nil {
				t.Fatal(err)
			}
			if fields["name"] != "review-work" || fields["description"] != "Use for: \"reviews\" with @signs" {
				t.Fatal(fields)
			}
			for _, text := range []string{"Canonical instructions.", "go test ./...", "Preserve data.", "rules", "just deploy"} {
				if !strings.Contains(out.Body, text) {
					t.Fatal(text)
				}
			}
			if filepath.Base(out.SuggestedPath) != "SKILL.md" || filepath.Base(filepath.Dir(out.SuggestedPath)) != "review-work" {
				t.Fatal(out.SuggestedPath)
			}
			if adapters.Compare(out.Body, out.Body) != adapters.CheckIdentical || adapters.Compare(out.Body, out.Body+"drift") != adapters.CheckDiff {
				t.Fatal("drift")
			}
		})
	}
}
func TestInvalidNamesAndSparseArtifact(t *testing.T) {
	for _, name := range []string{"", "../escape", "a/b", `a\b`, "-flag", "Upper", "double--hyphen", strings.Repeat("a", 65)} {
		raw, _ := json.Marshal(map[string]any{"agent": map[string]string{"name": name}})
		for _, adapter := range All() {
			if _, err := adapter.Render(raw); err == nil {
				t.Fatalf("%s accepted %q", adapter.Name(), name)
			}
		}
	}
	for _, adapter := range All() {
		out, err := adapter.Render([]byte(`{"agent":{"name":"qa"}}`))
		if err != nil || !strings.Contains(out.Content, "Use when operating as the qa Paimos agent.") {
			t.Fatalf("%+v %v", out, err)
		}
	}
}
