package dispatchprofile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHarnessOperationTemplates(t *testing.T) {
	for harness, restriction := range map[string]string{"codex": "--sandbox read-only", "claude": "--permission-mode plan", "claude-code": "--permission-mode plan", "grok": "--permission-mode plan", "pi": "--tools read,grep,find,ls", "cursor": "--mode ask"} {
		t.Run(harness, func(t *testing.T) {
			normal, err := HarnessTemplates(harness, false)
			if err != nil {
				t.Fatal(err)
			}
			limited, err := HarnessTemplates(harness, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, cmd := range []string{normal.Run, normal.Review, normal.Resume, normal.Spawn} {
				if !strings.Contains(cmd, "{model}") || !strings.HasSuffix(cmd, "'{prompt}'") {
					t.Fatal(cmd)
				}
				if strings.Contains(cmd, "bypass") || strings.Contains(cmd, "--force") || strings.Contains(cmd, "--yolo") {
					t.Fatal(cmd)
				}
			}
			for _, cmd := range []string{normal.Review, limited.Run, limited.Review, limited.Resume, limited.Spawn} {
				if !strings.Contains(cmd, restriction) {
					t.Fatalf("lost restriction: %s", cmd)
				}
			}
			if !strings.Contains(normal.Resume, "'{session_id}'") || strings.Contains(normal.Spawn, "resume") || !strings.Contains(normal.Spawn, "json") {
				t.Fatalf("lifecycle: %+v", normal)
			}
		})
	}
	if _, err := HarnessTemplates("unknown", false); err == nil {
		t.Fatal("unknown harness accepted")
	}
}
func TestResolvedOperationPinsAndValidation(t *testing.T) {
	request := ResolveRequest{Role: "review-gate", AuthorFamily: Anthropic}
	original, err := NewRegistry(nil).Resolve(request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range original.Ladder {
		if c.Profile == nil {
			continue
		}
		if c.Commands == nil {
			t.Fatal("missing templates")
		}
		for _, cmd := range []string{c.Commands.Run, c.Commands.Review, c.Commands.Resume, c.Commands.Spawn} {
			if strings.Contains(cmd, "{model}") || strings.Contains(cmd, "{effort}") || !strings.Contains(cmd, c.Profile.Model) {
				t.Fatal(cmd)
			}
		}
	}
	raw, _ := json.Marshal(original)
	for _, mutate := range []func(*Resolution){
		func(r *Resolution) { r.Commands = nil },
		func(r *Resolution) { r.Commands.Resume = "writable resume" },
		func(r *Resolution) { r.Ladder[len(r.Ladder)-2].Commands.Spawn = "writable spawn" },
		func(r *Resolution) { r.Ladder[len(r.Ladder)-1].Commands = &CommandTemplates{Run: "owner command"} },
	} {
		var result Resolution
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if err := ValidateResolution(request, result); err != nil {
			t.Fatal(err)
		}
		mutate(&result)
		if ValidateResolution(request, result) == nil {
			t.Fatal("tampered templates accepted")
		}
	}
}
