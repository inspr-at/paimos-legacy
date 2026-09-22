// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package native renders the shared canonical instructions as native Agent
// Skills. Model choice stays in the resolver, never in rendered skills.
package native

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters"
	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters/claudecode"
)

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type Adapter struct{ name, root string }

func All() []adapters.Adapter {
	return []adapters.Adapter{
		&Adapter{"codex", ".agents"}, &Adapter{"grok", ".grok"},
		&Adapter{"pi", ".pi"}, &Adapter{"cursor", ".cursor"},
	}
}
func (a *Adapter) Name() string     { return a.name }
func (a *Adapter) Version() string  { return "1.0.0" }
func (a *Adapter) Supports() string { return claudecode.SupportsRange }
func (a *Adapter) Describe() string {
	return a.name + " Agent Skill — writes to " + a.root + "/skills/<slash>/SKILL.md"
}
func (a *Adapter) Manifest() adapters.Manifest {
	return adapters.Manifest{ProtocolVersion: adapters.ProtocolVersion, Name: a.Name(), Version: a.Version(), Supports: a.Supports(), Description: a.Describe(), TargetPathTemplate: "{workspace}/" + a.root + "/skills/{slash_command_name}/SKILL.md", InputFormat: "json", OutputFormat: "markdown"}
}
func (a *Adapter) Render(canonical []byte) (adapters.RenderResult, error) {
	var artifact struct {
		Agent struct {
			Name, Description string
			Slash             string `json:"slash_command_name"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(canonical, &artifact); err != nil {
		return adapters.RenderResult{}, fmt.Errorf("decode canonical artifact: %w", err)
	}
	slug := strings.TrimSpace(artifact.Agent.Slash)
	if slug == "" {
		slug = strings.TrimSpace(artifact.Agent.Name)
	}
	// Reject aliases that could traverse directories or collide after sanitizing.
	if len(slug) > 64 || !skillName.MatchString(slug) {
		return adapters.RenderResult{}, fmt.Errorf("skill name must be 1–64 lowercase letters, digits or single hyphens")
	}
	rendered, err := claudecode.New().Render(canonical)
	if err != nil {
		return adapters.RenderResult{}, err
	}
	description := strings.Join(strings.Fields(artifact.Agent.Description), " ")
	if description == "" {
		description = "Use when operating as the " + artifact.Agent.Name + " Paimos agent."
	}
	if utf8.RuneCountInString(description) > 1024 {
		description = string([]rune(description)[:1024])
	}
	// JSON string encoding is valid YAML and safely quotes punctuation/newlines.
	nameJSON, _ := json.Marshal(slug)
	descriptionJSON, _ := json.Marshal(description)
	rendered.Content = fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", nameJSON, descriptionJSON, rendered.Content)
	rendered.SuggestedPath = filepath.Join(a.root, "skills", slug, "SKILL.md")
	return rendered, nil
}
