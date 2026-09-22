// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultCanonicalSchemaVersion is the version assumed when the
// canonical artifact does not carry a `canonical_schema_version` field.
// PAI-329 shipped the shape we currently consume; bumping this requires
// a coordinated server + adapter change (PAI-332 will formalise).
const DefaultCanonicalSchemaVersion = "1.0.0"

// HeaderPrefix is the literal prefix the dispatcher injects at the top
// of every rendered file. PAI-331 uses this for drift detection: the
// presence of the prefix means "this file is paimos-managed", and the
// trailing fields identify the source artifact + harness.
const HeaderPrefix = "<!-- paimos: rendered from "

// Dispatch carries the dependencies the CLI passes through to the
// dispatch helpers. Splitting it out keeps the cmd_skill.go wiring
// thin and the dispatch logic unit-testable without a Cobra harness.
type Dispatch struct {
	Registry *Registry
}

// RenderRequest captures the inputs to a single render call.
type RenderRequest struct {
	// Canonical is the raw JSON returned by
	// /api/projects/:id/agents/:name.json.
	Canonical []byte

	// HarnessName picks the adapter from Registry.
	HarnessName string

	// ProjectKey + AgentName are pulled from the artifact for the
	// header line; passed in explicitly so the CLI can also use the
	// values it resolved to call the API.
	ProjectKey string
	AgentName  string
}

// RenderOutput is the dispatch result the CLI writes (or compares in
// --check mode).
type RenderOutput struct {
	// Body is the final file contents, including the injected
	// drift-detection header. Write this verbatim.
	Body string

	// SuggestedPath is the adapter's conventional target. CLI uses it
	// when --out is absent.
	SuggestedPath string

	// Rev is the short artifact hash that ended up in the header line.
	// Surfaced for diagnostics.
	Rev string
}

// Render orchestrates: lookup adapter → version-compat check → adapter
// renders → header injection. Returns the final byte body.
func (d *Dispatch) Render(req RenderRequest) (*RenderOutput, error) {
	if d.Registry == nil {
		return nil, fmt.Errorf("dispatch: registry not configured")
	}
	adapter, err := d.Registry.Get(req.HarnessName)
	if err != nil {
		return nil, err
	}
	canonicalVersion := extractCanonicalSchemaVersion(req.Canonical)
	if err := CheckSupports(adapter.Supports(), canonicalVersion); err != nil {
		return nil, fmt.Errorf("adapter %q (supports %q) cannot render canonical schema %s: %w",
			adapter.Name(), adapter.Supports(), canonicalVersion, err)
	}
	result, err := adapter.Render(req.Canonical)
	if err != nil {
		return nil, fmt.Errorf("adapter %q render: %w", adapter.Name(), err)
	}
	rev := canonicalRev(req.Canonical)
	header := BuildHeader(req.ProjectKey, req.AgentName, rev, adapter.Name())
	body := injectHeader(header, result.Content)
	return &RenderOutput{
		Body:          body,
		SuggestedPath: result.SuggestedPath,
		Rev:           rev,
	}, nil
}

// BuildHeader returns the canonical header line PAI-331 will read for
// drift detection. Format frozen here — changing it forces a paired
// PAI-331 update.
func BuildHeader(projectKey, agentName, rev, harness string) string {
	if rev == "" {
		rev = "unknown"
	}
	return fmt.Sprintf("%s%s/%s@%s harness=%s -->", HeaderPrefix, projectKey, agentName, rev, harness)
}

// HasHeader reports whether `body` starts with the paimos drift-detection
// header (PAI-331 entry point). A missing header is the exit-code-2 case
// in --check mode.
func HasHeader(body string) bool {
	return ManagedHeader(body) != ""
}

// injectHeader prepends the header line + a blank line. If the adapter
// already started its output with a header line (which it shouldn't,
// but be defensive), we rewrite to use the dispatcher's canonical one.
func injectHeader(header, content string) string {
	frontmatter, trimmed := splitFrontmatter(stripBOM(content))
	if strings.HasPrefix(trimmed, HeaderPrefix) {
		// Drop existing header line — replace with ours so rev stays
		// authoritative.
		if i := strings.Index(trimmed, "\n"); i >= 0 {
			trimmed = trimmed[i+1:]
		} else {
			trimmed = ""
		}
		trimmed = strings.TrimLeft(trimmed, "\n")
	}
	if trimmed == "" {
		return frontmatter + header + "\n"
	}
	return frontmatter + header + "\n\n" + trimmed
}

// ManagedHeader returns only the leading managed header, allowing native skill
// frontmatter before it. Header-like text elsewhere in the body is not trusted.
func ManagedHeader(body string) string {
	_, rest := splitFrontmatter(strings.TrimLeft(stripBOM(body), " \t\r\n"))
	rest = strings.TrimLeft(rest, " \t\r\n")
	line, _, _ := strings.Cut(rest, "\n")
	if strings.HasPrefix(line, HeaderPrefix) {
		return line
	}
	return ""
}

func splitFrontmatter(content string) (string, string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	if end := strings.Index(content[4:], "\n---\n"); end >= 0 {
		end += 9
		return content[:end] + "\n", strings.TrimLeft(content[end:], "\n")
	}
	return "", content
}

// stripBOM removes the UTF-8 BOM (U+FEFF) prefix if present. We never
// emit one ourselves, but defensively tolerate it on read so a stray
// editor-saved BOM doesn't cause a header-presence false-negative.
// (Go forbids a literal BOM mid-source, so we express it via byte
// sequence rather than a literal rune.)
func stripBOM(s string) string {
	const bom = "\xef\xbb\xbf"
	return strings.TrimPrefix(s, bom)
}

// canonicalRev returns a short hash of the canonical JSON bytes. We
// hash the raw bytes (after a re-encode pass to normalise whitespace)
// so the rev is stable across re-fetches when nothing changed and
// changes the moment any field flips.
func canonicalRev(canonical []byte) string {
	var doc any
	if err := json.Unmarshal(canonical, &doc); err != nil {
		// Fall back to raw bytes — better an unstable rev than no rev.
		sum := sha256.Sum256(canonical)
		return hex.EncodeToString(sum[:])[:12]
	}
	normalised, err := json.Marshal(doc)
	if err != nil {
		sum := sha256.Sum256(canonical)
		return hex.EncodeToString(sum[:])[:12]
	}
	sum := sha256.Sum256(normalised)
	return hex.EncodeToString(sum[:])[:12]
}

// extractCanonicalSchemaVersion pulls the optional top-level
// `canonical_schema_version` field out of the artifact, defaulting to
// DefaultCanonicalSchemaVersion when absent.
func extractCanonicalSchemaVersion(canonical []byte) string {
	var probe struct {
		Version string `json:"canonical_schema_version"`
	}
	if err := json.Unmarshal(canonical, &probe); err == nil && strings.TrimSpace(probe.Version) != "" {
		return strings.TrimSpace(probe.Version)
	}
	return DefaultCanonicalSchemaVersion
}

// CheckResult enumerates --check-mode outcomes.
type CheckResult int

const (
	// CheckIdentical: file on disk matches the rendered output byte-
	// for-byte. Exit 0.
	CheckIdentical CheckResult = 0
	// CheckDiff: file exists, has a header, but content differs. Exit 1.
	CheckDiff CheckResult = 1
	// CheckHeaderMissing: file exists but has no paimos header — out of
	// the management surface. Exit 2.
	CheckHeaderMissing CheckResult = 2
)

// Compare classifies the relationship between an existing file body
// and the rendered body. The CLI maps the result to an exit code.
//
// File-not-exists is handled by the CLI before calling Compare (it's
// a "diff" relative to the to-be-rendered body — exit 1 with a clear
// "file does not exist yet" message).
func Compare(rendered, existing string) CheckResult {
	if rendered == existing {
		return CheckIdentical
	}
	if !HasHeader(existing) {
		return CheckHeaderMissing
	}
	return CheckDiff
}
