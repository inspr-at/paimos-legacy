// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import "testing"

func TestValidateEffectiveStateRejectsMismatch(t *testing.T) {
	state := StateData{
		Model:         &StateModel{Provider: "openai", ID: "gpt-5"},
		ThinkingLevel: "low",
	}
	expected := ExpectedState{Provider: "anthropic", ModelID: "claude-sonnet-5", ThinkingLevel: "high"}
	if err := ValidateEffectiveState(state, expected); err == nil {
		t.Fatal("expected provider/model/thinking mismatch")
	}
}

func TestValidateEffectiveStateAcceptsReviewedIntent(t *testing.T) {
	state := StateData{
		Model:         &StateModel{Provider: "anthropic", ID: "claude-sonnet-5"},
		ThinkingLevel: "high",
	}
	expected := ExpectedFromLaunch(LaunchConfig{
		Provider: "anthropic", Model: "claude-sonnet-5", ThinkingLevel: "high",
	})
	if err := ValidateEffectiveState(state, expected); err != nil {
		t.Fatal(err)
	}
}
