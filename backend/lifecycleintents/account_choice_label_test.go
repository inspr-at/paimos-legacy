package lifecycleintents

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAccountChoiceLabelCauseRejectsWithoutEchoingSecretLike(t *testing.T) {
	secret := "sk-live-abcdefghijk"
	if cause := AccountChoiceLabelCause(secret); cause != "secret-like value" {
		t.Fatalf("secret-like cause=%q", cause)
	}
	if strings.Contains(AccountChoiceLabelCause(secret), secret) {
		t.Fatal("label cause echoed secret-like input")
	}
	cases := []struct {
		value, cause string
	}{
		{"Work: Main", "colon not allowed"},
		{strings.Repeat("a", 49), "exceeds 48 characters"},
		{" Coordinator", "surrounding whitespace"},
		{"Coordinator ", "surrounding whitespace"},
		{"chatgpt", "reserved account class"},
	}
	for _, tc := range cases {
		if got := AccountChoiceLabelCause(tc.value); got != tc.cause {
			t.Fatalf("value cause=%q want %q", got, tc.cause)
		}
		if !ValidAccountChoiceLabel("Coordinator") || !ValidAccountChoiceLabel("Personal") {
			t.Fatal("operator labels rejected")
		}
	}
}

func TestDisplayLabelForAccountKeyPreservesOrdinaryAndDerivesUnsafe(t *testing.T) {
	if got := DisplayLabelForAccountKey("coordinator", nil); got != "coordinator" {
		t.Fatalf("ordinary key label=%q", got)
	}
	if got := DisplayLabelForAccountKey("team:alpha", nil); got != "team-alpha" || !ValidAccountChoiceLabel(got) {
		t.Fatalf("colon key label=%q", got)
	}
	long := strings.Repeat("n", 49)
	got := DisplayLabelForAccountKey(long, nil)
	if got != strings.Repeat("n", 48) || !ValidAccountChoiceLabel(got) {
		t.Fatalf("long key label=%q", got)
	}
	secret := "sk-live-abcdefghijk"
	safe := DisplayLabelForAccountKey(secret, nil)
	if safe == "" || safe == secret || !ValidAccountChoiceLabel(safe) || strings.Contains(safe, "sk-live") {
		t.Fatalf("secret-like key used as label=%q", safe)
	}
	if again := DisplayLabelForAccountKey(secret, nil); again != safe {
		t.Fatal("derived label was not stable")
	}
	blocked := DisplayLabelForAccountKey("team:alpha", []string{"team-alpha"})
	if blocked == "team-alpha" || blocked == "team:alpha" || !ValidAccountChoiceLabel(blocked) {
		t.Fatalf("reserved derived label=%q", blocked)
	}
}

func TestValidateAdvertisedAccountsAcceptsDerivedLegacyLabels(t *testing.T) {
	generation, host := uuid.NewString(), "fixture-machine"
	profiles := []Profile{{ID: "codex-sol-high", Version: "2"}}
	long := strings.Repeat("n", 49)
	cases := []AccountChoice{
		{Key: "coordinator", Label: DisplayLabelForAccountKey("coordinator", nil)},
		{Key: "team:alpha", Label: DisplayLabelForAccountKey("team:alpha", nil)},
		{Key: long, Label: DisplayLabelForAccountKey(long, nil)},
	}
	for _, choice := range cases {
		in := Registration{
			Generation: generation, Host: host, AccountLabel: "chatgpt",
			Accounts: []AccountChoice{choice}, SchemaVersion: AccountChoiceSchemaV2, Profiles: profiles,
		}
		if err := ValidateAdvertisedAccounts(in); err != nil {
			t.Fatalf("choice %+v rejected: %v", choice, err)
		}
	}
	if err := ValidateAdvertisedAccounts(Registration{
		Generation: generation, Host: host, AccountLabel: "chatgpt",
		Accounts: []AccountChoice{{Key: "team:alpha", Label: "team:alpha"}}, SchemaVersion: AccountChoiceSchemaV2, Profiles: profiles,
	}); err != ErrInvalid {
		t.Fatalf("colon label accepted: %v", err)
	}
	if err := ValidateAdvertisedAccounts(Registration{
		Generation: generation, Host: host, AccountLabel: "chatgpt",
		Accounts: []AccountChoice{
			{Key: "coordinator", Label: "Coordinator"},
			{Key: "personal", Label: "coordinator"},
		},
		SchemaVersion: AccountChoiceSchemaV2, Profiles: profiles,
	}); err != ErrInvalid {
		t.Fatalf("key/label ambiguity accepted: %v", err)
	}
	if err := ValidateAdvertisedAccounts(Registration{
		Generation: generation, Host: host, AccountLabel: "chatgpt",
		Accounts:      []AccountChoice{{Key: "coordinator", Label: "codex-sol-high"}},
		SchemaVersion: AccountChoiceSchemaV2, Profiles: profiles,
	}); err != ErrInvalid {
		t.Fatalf("profile label collision accepted: %v", err)
	}
}
