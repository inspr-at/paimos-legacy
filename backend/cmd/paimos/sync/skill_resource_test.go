package sync

import "testing"

func TestExtractNativeSkillRev(t *testing.T) {
	body := []byte("---\nname: qa\ndescription: qa@example.org\n---\n\n<!-- paimos: rendered from PAI/qa@abc123 harness=codex -->\n\nBody")
	if got := ExtractRevFromHeader(body); got != "abc123" {
		t.Fatalf("rev=%q", got)
	}
	if got := ExtractRevFromHeader([]byte("unmanaged\n<!-- paimos: rendered from PAI/qa@fake harness=codex -->")); got != "" {
		t.Fatalf("body header accepted: %q", got)
	}
}
