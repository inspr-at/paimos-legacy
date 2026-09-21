// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package dispatchprofile

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type Family string
type Tier string

const (
	OpenAI    Family = "openai"
	Anthropic Family = "anthropic"
	XAI       Family = "xai"
	Cursor    Family = "cursor"
	Fast      Tier   = "fast"
	Standard  Tier   = "standard"
	Strong    Tier   = "strong"
	Frontier  Tier   = "frontier"
)

type Role struct {
	Name        string `json:"name"`
	Tier        Tier   `json:"tier"`
	Effort      string `json:"effort"`
	CrossFamily bool   `json:"cross_family"`
	ReadOnly    bool   `json:"read_only"`
	// Gate profiles have explicit order, including the same-rung fallback.
	Ladder []string `json:"ladder,omitempty"`
}

func Roles() []Role {
	return []Role{
		{Name: "scout", Tier: Fast, Effort: "medium"},
		{Name: "mechanical", Tier: Fast, Effort: "high"},
		{Name: "build", Tier: Standard, Effort: "high"},
		{Name: "build-hard", Tier: Strong, Effort: "xhigh"},
		{Name: "review-gate", Tier: Frontier, Effort: "xhigh", CrossFamily: true, ReadOnly: true,
			Ladder: []string{"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "cursor-grok-xhigh", "owner"}},
	}
}

type Override struct {
	ProfileID string    `json:"profile_id"`
	Version   string    `json:"version"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	Until     time.Time `json:"until"`
}

// A policy belongs to the serving instance's database, never to a project or
// a caller-supplied instance name. Cache provenance is checked separately.
type Registry struct {
	Version   string     `json:"catalog_version"`
	Profiles  []Profile  `json:"dispatch_profiles"`
	Models    []Model    `json:"models"`
	Roles     []Role     `json:"roles"`
	Overrides []Override `json:"overrides"`
}

func NewRegistry(overrides []Override) Registry {
	detachedModels := make([]Model, len(models))
	for i, m := range models {
		detachedModels[i] = m
		detachedModels[i].AllowedEfforts = append([]string(nil), m.AllowedEfforts...)
	}
	return Registry{Version: CatalogVersion, Profiles: List(), Models: detachedModels, Roles: Roles(), Overrides: append([]Override{}, overrides...)}
}

func ValidateOverrides(overrides []Override) error {
	if len(overrides) > 128 {
		return errors.New("too many model overrides")
	}
	seen := map[string]bool{}
	for _, o := range overrides {
		key := o.ProfileID + "@" + o.Version
		// Retired pins may remain in durable policy; they never match a new version.
		if !stableValue.MatchString(o.ProfileID) || !stableValue.MatchString(o.Version) || len(key) > 257 || seen[key] {
			return errors.New("invalid or duplicate model override pin")
		}
		seen[key] = true
		if o.State != "unavailable" && o.State != "conserved" && o.State != "budget-limited" {
			return errors.New("unsupported model override state")
		}
		if strings.TrimSpace(o.Reason) == "" || len(o.Reason) > 512 || strings.ContainsAny(o.Reason, "\x00\r\n") || o.Until.IsZero() {
			return errors.New("model override requires a bounded reason and expiry")
		}
	}
	return nil
}

func (r Registry) Validate() error {
	expected := NewRegistry(nil)
	if r.Version != CatalogVersion || !reflect.DeepEqual(r.Profiles, expected.Profiles) || !reflect.DeepEqual(r.Models, expected.Models) || !reflect.DeepEqual(r.Roles, expected.Roles) {
		return errors.New("unsupported or altered model catalog; update matching server and CLI")
	}
	return ValidateOverrides(r.Overrides)
}

type ResolveRequest struct {
	Role         string `json:"role"`
	AuthorFamily Family `json:"author_family,omitempty"`
	Harness      string `json:"harness,omitempty"`
}

type Candidate struct {
	ProfileID   string   `json:"profile_id"`
	Profile     *Profile `json:"profile,omitempty"`
	Command     string   `json:"command_template,omitempty"`
	SkipReasons []string `json:"skip_reasons"`
	Selected    bool     `json:"selected"`
}

type Resolution struct {
	CatalogVersion string      `json:"catalog_version"`
	Role           Role        `json:"role"`
	Profile        *Profile    `json:"profile,omitempty"`
	Command        string      `json:"command_template,omitempty"`
	Ladder         []Candidate `json:"ladder"`
	OwnerRequired  bool        `json:"owner_required"`
	Source         string      `json:"source"`
	Stale          bool        `json:"stale"`
	CachedAt       *time.Time  `json:"cached_at,omitempty"`
}

func ValidFamily(f Family) bool { return f == OpenAI || f == Anthropic || f == XAI || f == Cursor }

func (r Registry) Resolve(request ResolveRequest, now time.Time) (Resolution, error) {
	var result Resolution
	if err := r.Validate(); err != nil {
		return result, err
	}
	var role *Role
	for i := range r.Roles {
		if r.Roles[i].Name == request.Role {
			role = &r.Roles[i]
			break
		}
	}
	if role == nil {
		return result, errors.New("unknown model role")
	}
	if request.AuthorFamily != "" && !ValidFamily(request.AuthorFamily) {
		return result, errors.New("unknown author family")
	}
	if role.CrossFamily && request.AuthorFamily == "" {
		return result, errors.New("review-gate requires author-family")
	}
	switch request.Harness {
	case "", "codex", "claude", "pi", "cursor":
	default:
		return result, errors.New("unsupported model harness")
	}
	result = Resolution{CatalogVersion: r.Version, Role: *role, Source: "instance", Ladder: []Candidate{}}
	ids := append([]string(nil), role.Ladder...)
	if !role.CrossFamily {
		// Harness preference is explicit; catalog/list order is not routing policy.
		for _, harness := range []string{"codex", "claude", "pi", "cursor"} {
			for _, p := range r.Profiles {
				if p.Harness == harness && p.Tier == role.Tier && p.Effort == role.Effort {
					ids = append(ids, p.ID)
				}
			}
		}
	}
	matchingHarness := request.Harness == ""
	for _, id := range ids {
		c := Candidate{ProfileID: id, SkipReasons: []string{}}
		if id == "owner" {
			c.Selected = result.Profile == nil
			result.OwnerRequired = c.Selected
			result.Ladder = append(result.Ladder, c)
			continue
		}
		var profile *Profile
		for i := range r.Profiles {
			if r.Profiles[i].ID == id {
				p := r.Profiles[i]
				profile = &p
				break
			}
		}
		if profile == nil {
			return Resolution{}, errors.New("role references an unavailable profile")
		}
		c.Profile = profile
		c.Command = commandTemplate(*profile, *role)
		if profile.Harness == request.Harness {
			matchingHarness = true
		}
		if role.CrossFamily && profile.Family == request.AuthorFamily {
			c.SkipReasons = append(c.SkipReasons, "author family")
		}
		if request.Harness != "" && profile.Harness != request.Harness {
			c.SkipReasons = append(c.SkipReasons, "harness filter")
		}
		for _, o := range r.Overrides {
			if o.ProfileID == id && o.Version == profile.Version && now.Before(o.Until) {
				c.SkipReasons = append(c.SkipReasons, fmt.Sprintf("%s until %s: %s", o.State, o.Until.UTC().Format(time.RFC3339), o.Reason))
			}
		}
		if len(c.SkipReasons) == 0 && result.Profile == nil {
			c.Selected = true
			result.Profile = profile
			result.Command = c.Command
		}
		result.Ladder = append(result.Ladder, c)
	}
	if !matchingHarness {
		return Resolution{}, errors.New("role and harness combination is unsupported")
	}
	if result.Profile == nil && !result.OwnerRequired {
		return result, errors.New("no eligible model route")
	}
	return result, nil
}

// Only validated catalog values reach these templates. {prompt} is a literal
// placeholder, not interpolated user input; resolving never runs the command.
func commandTemplate(p Profile, role Role) string {
	switch p.Harness {
	case "codex":
		if role.ReadOnly {
			return fmt.Sprintf("codex exec -m %s -c model_reasoning_effort=%s --sandbox read-only '{prompt}'", p.Model, p.Effort)
		}
		return fmt.Sprintf("codex exec -m %s -c model_reasoning_effort=%s '{prompt}'", p.Model, p.Effort)
	case "claude":
		if role.ReadOnly {
			return fmt.Sprintf("claude -p --model %s --effort %s --permission-mode plan '{prompt}'", p.Model, p.Effort)
		}
		return fmt.Sprintf("claude -p --model %s --effort %s '{prompt}'", p.Model, p.Effort)
	case "pi":
		if role.ReadOnly {
			return fmt.Sprintf("pi --model %s:%s --tools read,grep,find,ls -p '{prompt}'", p.Model, p.Effort)
		}
		return fmt.Sprintf("pi --model %s:%s -p '{prompt}'", p.Model, p.Effort)
	case "cursor":
		if role.ReadOnly {
			return fmt.Sprintf("cursor-agent --trust --mode ask --model %s -p '{prompt}'", p.Model)
		}
		return fmt.Sprintf("cursor-agent --trust --model %s -p '{prompt}'", p.Model)
	default:
		return ""
	}
}

// ValidateResolution prevents an older/malformed server response from becoming
// an executable choice. Instance policy can add skips, never remove family or
// harness restrictions, substitute a pin, or change its command template.
func ValidateResolution(request ResolveRequest, result Resolution) error {
	expected, err := NewRegistry(nil).Resolve(request, time.Now())
	if err != nil {
		return err
	}
	invalid := errors.New("invalid or unsupported model resolution")
	if result.CatalogVersion != CatalogVersion || !reflect.DeepEqual(result.Role, expected.Role) || len(result.Ladder) != len(expected.Ladder) {
		return invalid
	}
	selected := false
	for i, c := range result.Ladder {
		baseline := expected.Ladder[i]
		if c.ProfileID != baseline.ProfileID || !reflect.DeepEqual(c.Profile, baseline.Profile) || c.Command != baseline.Command {
			return invalid
		}
		for _, required := range baseline.SkipReasons {
			found := false
			for _, reason := range c.SkipReasons {
				if reason == required {
					found = true
				}
			}
			if !found {
				return invalid
			}
		}
		shouldSelect := !selected && len(c.SkipReasons) == 0
		if c.Selected != shouldSelect {
			return invalid
		}
		if c.Selected {
			selected = true
			if c.ProfileID == "owner" {
				if !result.OwnerRequired || result.Profile != nil || result.Command != "" {
					return invalid
				}
			} else if result.OwnerRequired || result.Profile == nil || *result.Profile != *c.Profile || result.Command != c.Command {
				return invalid
			}
		}
	}
	if !selected {
		return invalid
	}
	return nil
}
