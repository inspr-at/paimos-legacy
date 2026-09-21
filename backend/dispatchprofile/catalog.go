// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package dispatchprofile owns the immutable, typed profiles used when the
// execution-options authority dispatches an operator-local agentd child. It
// contains no credentials, paths, process handles, or account identities.
package dispatchprofile

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

const CatalogVersion = "2"

const (
	MachineAuthenticatedReporter = "authenticated_reporter"
	AccountLocalProbe            = "local_probe"
)

var stableValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var modelValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// Profile is an immutable execution choice. Machine and Account are source
// selectors: their runtime values come from the authenticated reporter and a
// fixed-argv local probe respectively, never from a start request.
type Profile struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Harness       string `json:"harness"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	Family        Family `json:"family,omitempty"`
	Tier          Tier   `json:"tier,omitempty"`
	MachineSource string `json:"machine_source"`
	AccountSource string `json:"account_source"`
	WorkspaceMode string `json:"workspace_mode"`
}

// Model is the model/effort authority. Profiles pin one of its allowed efforts.
type Model struct {
	Harness        string   `json:"harness"`
	ID             string   `json:"id"`
	Family         Family   `json:"family"`
	Tier           Tier     `json:"tier"`
	AllowedEfforts []string `json:"allowed_efforts"`
}

var models = []Model{
	{"codex", "gpt-6-luna", OpenAI, Fast, []string{"medium", "high", "xhigh"}},
	{"codex", "gpt-6-terra", OpenAI, Standard, []string{"medium", "high", "xhigh"}},
	{"codex", "gpt-6-sol", OpenAI, Strong, []string{"medium", "high", "xhigh"}},
	{"codex", "gpt-6-astra", OpenAI, Frontier, []string{"medium", "high", "xhigh"}},
	{"claude", "haiku", Anthropic, Fast, []string{"medium", "high"}},
	{"claude", "sonnet", Anthropic, Standard, []string{"high"}},
	{"claude", "opus", Anthropic, Strong, []string{"high", "xhigh"}},
	{"claude", "fable", Anthropic, Frontier, []string{"high", "xhigh"}},
	{"pi", "anthropic/claude-sonnet-5", Anthropic, Standard, []string{"high"}},
	{"pi", "anthropic/claude-opus-5", Anthropic, Strong, []string{"high", "xhigh"}},
	{"cursor", "composer-2.5", Cursor, Standard, []string{"default"}},
	{"cursor", "composer-2.5-fast", Cursor, Standard, []string{"default"}},
	{"cursor", "grok-4.7-low", XAI, Frontier, []string{"low"}},
	{"cursor", "grok-4.7-medium", XAI, Frontier, []string{"medium"}},
	{"cursor", "grok-4.7-high", XAI, Frontier, []string{"high"}},
	{"cursor", "grok-4.7-xhigh", XAI, Frontier, []string{"xhigh"}},
	{"cursor", "grok-4.7-low-fast", XAI, Frontier, []string{"low"}},
	{"cursor", "grok-4.7-medium-fast", XAI, Frontier, []string{"medium"}},
	{"cursor", "grok-4.7-high-fast", XAI, Frontier, []string{"high"}},
	{"cursor", "grok-4.7-xhigh-fast", XAI, Frontier, []string{"xhigh"}},
}

var catalog = buildCatalog()

func buildCatalog() []Profile {
	var out []Profile
	for _, model := range models {
		for _, effort := range model.AllowedEfforts {
			id := model.Harness + "-" + model.ID + "-" + effort
			switch model.Harness {
			case "codex":
				id = "codex-" + strings.TrimPrefix(model.ID, "gpt-6-") + "-" + effort
			case "pi":
				id = "pi-anthropic-" + strings.TrimSuffix(strings.TrimPrefix(model.ID, "anthropic/claude-"), "-5") + "-" + effort
			case "cursor":
				id = "cursor-" + model.ID
				if model.ID == "composer-2.5" {
					id = "cursor-composer"
				}
				if model.ID == "grok-4.7-high" {
					id = "cursor-grok"
				}
				if model.ID == "grok-4.7-xhigh" {
					id = "cursor-grok-xhigh"
				}
			}
			out = append(out, Profile{ID: id, Version: CatalogVersion, Harness: model.Harness, Model: model.ID, Effort: effort, Family: model.Family, Tier: model.Tier,
				MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"})
		}
	}
	return out
}

// List returns a detached, stable-order catalog for the execution-options API.
func List() []Profile {
	out := append([]Profile(nil), catalog...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Resolve requires the exact immutable version and expected harness.
func Resolve(id, version, harness string) (Profile, error) {
	for _, profile := range catalog {
		if profile.ID != id || profile.Version != version {
			continue
		}
		if profile.Harness != harness {
			return Profile{}, errors.New("dispatch profile does not support the requested harness")
		}
		if err := Validate(profile); err != nil {
			return Profile{}, err
		}
		return profile, nil
	}
	return Profile{}, errors.New("dispatch profile id and version are unavailable")
}

// Validate rejects malformed or widened catalog entries before they can reach
// a vendor adapter. Supported pairs are deliberately closed by Resolve.
func Validate(profile Profile) error {
	if err := ValidateSnapshot(profile); err != nil {
		return err
	}
	if profile.WorkspaceMode != "exclusive" {
		return errors.New("dispatch profile workspace mode is unsupported")
	}
	return nil
}

// ValidateSnapshot validates a durable profile without consulting the live
// catalog. Retired profile versions must remain readable after a binary
// upgrade; their immutable model and effort are the historical authority.
func ValidateSnapshot(profile Profile) error {
	for _, value := range []string{profile.ID, profile.Version, profile.Harness, profile.Effort} {
		if len(value) == 0 || len(value) > 128 || !stableValue.MatchString(value) {
			return errors.New("dispatch profile contains an invalid stable value")
		}
	}
	if len(profile.Model) == 0 || len(profile.Model) > 128 || !modelValue.MatchString(profile.Model) {
		return errors.New("dispatch profile contains an invalid model")
	}
	if profile.Harness != "codex" && profile.Harness != "claude" && profile.Harness != "pi" && profile.Harness != "cursor" {
		return errors.New("dispatch profile harness is unsupported")
	}
	if profile.MachineSource != MachineAuthenticatedReporter || profile.AccountSource != AccountLocalProbe {
		return errors.New("dispatch profile provenance source is unsupported")
	}
	if profile.WorkspaceMode != "exclusive" && profile.WorkspaceMode != "shared" {
		return errors.New("dispatch profile workspace mode is unsupported")
	}
	switch profile.Effort {
	case "low", "medium", "high", "xhigh", "max":
	case "default":
		if profile.Harness != "cursor" {
			return errors.New("dispatch profile effort is unsupported")
		}
	default:
		return errors.New("dispatch profile effort is unsupported")
	}
	return nil
}
