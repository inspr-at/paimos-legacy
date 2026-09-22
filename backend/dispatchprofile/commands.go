// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package dispatchprofile

import (
	"fmt"
	"strings"
)

// CommandTemplates are documentation, never shell input. Callers must substitute
// placeholders as individual argv values, not interpolate text into a shell.
// Spawn starts a fresh foreground subprocess with structured output; the caller
// owns its lifecycle. Resume requires an explicit vendor session, never --last.
type CommandTemplates struct {
	Run    string `json:"run"`
	Review string `json:"review"`
	Resume string `json:"resume"`
	Spawn  string `json:"spawn"`
}

// HarnessTemplates exposes model-independent syntax, including native Grok
// whose model availability is not yet verified in the dispatch catalog.
func HarnessTemplates(harness string, readOnly bool) (CommandTemplates, error) {
	var run, review, resume, spawn string
	switch harness {
	case "codex":
		run = "codex exec -m {model} -c model_reasoning_effort={effort}"
		review = run + " --sandbox read-only"
		resume = "codex exec -m {model} -c model_reasoning_effort={effort}"
		if readOnly {
			resume += " --sandbox read-only"
		}
		resume += " resume '{session_id}'"
		if readOnly {
			run = review
		}
		spawn = run + " --json"
	case "claude", "claude-code":
		run = "claude -p --model {model} --effort {effort}"
		review = run + " --permission-mode plan"
		if readOnly {
			run = review
		}
		resume = run + " --resume '{session_id}'"
		spawn = run + " --output-format json"
	case "grok":
		run = "grok --model {model} --reasoning-effort {effort}"
		review = run + " --permission-mode plan"
		if readOnly {
			run = review
		}
		resume = run + " --resume '{session_id}' -p"
		spawn = run + " --output-format json -p"
		run += " -p"
		review += " -p"
	case "pi":
		run = "pi --model {model}:{effort}"
		review = run + " --tools read,grep,find,ls"
		if readOnly {
			run = review
		}
		resume = run + " --session '{session_id}' -p"
		spawn = run + " --mode json -p"
		run += " -p"
		review += " -p"
	case "cursor":
		run = "cursor-agent --trust --model {model}"
		review = "cursor-agent --trust --mode ask --model {model}"
		if readOnly {
			run = review
		}
		resume = run + " --resume '{session_id}' -p"
		spawn = run + " --output-format json -p"
		run += " -p"
		review += " -p"
	default:
		return CommandTemplates{}, fmt.Errorf("unsupported command harness %q", harness)
	}
	return CommandTemplates{run + " '{prompt}'", review + " '{prompt}'", resume + " '{prompt}'", spawn + " '{prompt}'"}, nil
}

func profileTemplates(p Profile, role Role) (CommandTemplates, error) {
	if err := Validate(p); err != nil {
		return CommandTemplates{}, err
	}
	templates, err := HarnessTemplates(p.Harness, role.ReadOnly)
	if err != nil {
		return CommandTemplates{}, err
	}
	replace := strings.NewReplacer("{model}", p.Model, "{effort}", p.Effort)
	return CommandTemplates{replace.Replace(templates.Run), replace.Replace(templates.Review), replace.Replace(templates.Resume), replace.Replace(templates.Spawn)}, nil
}

func commandTemplate(p Profile, role Role) string {
	templates, _ := profileTemplates(p, role)
	return templates.Run
}
