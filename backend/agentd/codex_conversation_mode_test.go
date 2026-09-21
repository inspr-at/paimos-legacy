// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCodexConversationModeBuildsFixedConfigurationAndThreadStart(t *testing.T) {
	parent := canonicalTempDir(t)
	scratch := filepath.Join(parent, `scratch "quoted"`)
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	mode, err := buildCodexConversationMode(codexConversationModeInput{
		AccountKey: "conversation-account", DispatchProfileID: "codex-conversation", Model: "gpt-6-sol", Scratch: scratch,
	})
	if err != nil {
		t.Fatal(err)
	}

	args := mode.appServerArgs()
	if !slices.Equal(args[:4], []string{"app-server", "--listen", "stdio://", "--strict-config"}) {
		t.Fatalf("app-server prefix=%q", args[:4])
	}
	if (len(args)-4)%2 != 0 {
		t.Fatalf("malformed config argv length=%d", len(args))
	}
	overrides := map[string]string{}
	for index := 4; index < len(args); index += 2 {
		if args[index] != "-c" {
			t.Fatalf("argv[%d]=%q want -c", index, args[index])
		}
		key, value, ok := strings.Cut(args[index+1], "=")
		if !ok || key == "" || overrides[key] != "" {
			t.Fatalf("invalid or duplicate override %q", args[index+1])
		}
		overrides[key] = value
	}
	wantProfile := `{filesystem={":minimal"="read",` + tomlQuoted(scratch) + `="write"},network={enabled=false}}`
	fixed := map[string]string{
		"analytics.enabled":   "false",
		"default_permissions": `"` + codexConversationPermissionProfile + `"`,
		"mcp_servers":         "{}",
		"permissions." + codexConversationPermissionProfile: wantProfile,
		"project_doc_max_bytes":                             "0",
		"shell_environment_policy":                          `{inherit="none",ignore_default_excludes=false,set={PATH="/usr/bin:/bin"},experimental_use_profile=false}`,
		"web_search":                                        `"disabled"`,
	}
	for key, want := range fixed {
		if overrides[key] != want {
			t.Fatalf("override %s=%q want %q", key, overrides[key], want)
		}
	}
	featurePolicy := codexConversationRequiredFeatures()
	for _, feature := range featurePolicy {
		key := "features." + feature.Name
		if overrides[key] != strconv.FormatBool(feature.Enabled) {
			t.Fatalf("override %s=%q", key, overrides[key])
		}
	}
	if len(overrides) != len(fixed)+len(featurePolicy) {
		t.Fatalf("unexpected override count=%d", len(overrides))
	}
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, mode.accountKey) || strings.Contains(joined, mode.dispatchProfileID) || strings.Contains(joined, mode.model) {
		t.Fatal("account/profile/model provenance leaked into launch argv")
	}

	encoded, err := json.Marshal(mode.threadStartParams())
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"allowProviderModelFallback":false,"approvalPolicy":"never","approvalsReviewer":"user","baseInstructions":` + strconv.Quote(codexConversationBasePolicy) +
		`,"cwd":` + strconv.Quote(scratch) + `,"developerInstructions":` + strconv.Quote(codexConversationDeveloperPolicy) +
		`,"dynamicTools":[],"environments":[],"ephemeral":true,"model":"gpt-6-sol","permissions":"aithema-conversation-v1","runtimeWorkspaceRoots":[` + strconv.Quote(scratch) + `],"selectedCapabilityRoots":[]}`
	if string(encoded) != wantJSON || strings.Contains(string(encoded), `"sandbox"`) {
		t.Fatalf("thread/start=%s", encoded)
	}
	if got := mode.experimentalFeatureListParams(); len(got) != 1 || got["limit"] != uint32(1000) {
		t.Fatalf("feature params=%v", got)
	}
	if got := mode.permissionProfileListParams(); len(got) != 2 || got["cwd"] != scratch || got["limit"] != uint32(1000) {
		t.Fatalf("profile params=%v", got)
	}
}

func TestCodexConversationModeRejectsUnsafeScratch(t *testing.T) {
	input := codexConversationModeInput{AccountKey: "conversation-account", DispatchProfileID: "codex-conversation", Model: "gpt-6-sol"}
	if _, err := buildCodexConversationMode(input); !errors.Is(err, errCodexConversationUnsupported) {
		t.Fatalf("empty scratch err=%v", err)
	}
	input.Scratch = "relative"
	if _, err := buildCodexConversationMode(input); !errors.Is(err, errCodexConversationUnsupported) {
		t.Fatalf("relative scratch err=%v", err)
	}

	parent := canonicalTempDir(t)
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	input.Scratch = link
	if _, err := buildCodexConversationMode(input); !errors.Is(err, errCodexConversationUnsupported) {
		t.Fatalf("symlink scratch err=%v", err)
	}

	input.Scratch = target
	if err := os.WriteFile(filepath.Join(target, "inherited"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildCodexConversationMode(input); !errors.Is(err, errCodexConversationUnsupported) {
		t.Fatalf("non-empty scratch err=%v", err)
	}

	permissive := filepath.Join(parent, "permissive")
	if err := os.Mkdir(permissive, 0755); err != nil {
		t.Fatal(err)
	}
	input.Scratch = permissive
	if _, err := buildCodexConversationMode(input); !errors.Is(err, errCodexConversationUnsupported) {
		t.Fatalf("permissive scratch err=%v", err)
	}
}

func TestCodexConversationModeRequiresControllerResolvedOpaqueInputs(t *testing.T) {
	scratch := canonicalTempDir(t)
	tests := []struct {
		name   string
		mutate func(*codexConversationModeInput)
	}{
		{"account home", func(input *codexConversationModeInput) { input.AccountKey = "/private/account-home" }},
		{"account class", func(input *codexConversationModeInput) { input.AccountKey = "chatgpt" }},
		{"profile path", func(input *codexConversationModeInput) { input.DispatchProfileID = "/profiles/codex" }},
		{"model argument", func(input *codexConversationModeInput) { input.Model = "gpt-safe --endpoint=host" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := codexConversationModeInput{
				AccountKey: "conversation-account", DispatchProfileID: "codex-conversation", Model: "gpt-6-sol", Scratch: scratch,
			}
			test.mutate(&input)
			if _, err := buildCodexConversationMode(input); !errors.Is(err, errCodexConversationUnsupported) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCodexConversationModePinsScratchIdentity(t *testing.T) {
	parent := canonicalTempDir(t)
	scratch := filepath.Join(parent, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	mode := mustCodexConversationMode(t, scratch)
	if err := os.Rename(scratch, filepath.Join(parent, "old-scratch")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	if err := mode.validateScratchIdentity(); !errors.Is(err, errCodexConversationUnsupported) {
		t.Fatalf("replaced scratch err=%v", err)
	}
}

func TestCodexConversationModePreflightAcceptsExactRestrictedState(t *testing.T) {
	mode := mustCodexConversationMode(t, canonicalTempDir(t))
	features, profiles, response := completeCodexConversationPreflight(mode)
	if err := mode.validatePreflight(features, profiles, response); err != nil {
		t.Fatal(err)
	}
}

func TestCodexConversationModePreflightFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*codexConversationExperimentalFeatureListResponse, *codexConversationPermissionProfileListResponse, *codexConversationThreadStartResponse)
	}{
		{"missing feature", func(features *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, _ *codexConversationThreadStartResponse) {
			features.Data = features.Data[1:]
		}},
		{"enabled inherited tool", func(features *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, _ *codexConversationThreadStartResponse) {
			for index := range features.Data {
				if features.Data[index].Name == "plugins" {
					features.Data[index].Enabled = true
				}
			}
		}},
		{"host suppression disabled", func(features *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, _ *codexConversationThreadStartResponse) {
			for index := range features.Data {
				if features.Data[index].Name == "skip_host_skill_discovery" {
					features.Data[index].Enabled = false
				}
			}
		}},
		{"missing profile", func(_ *codexConversationExperimentalFeatureListResponse, profiles *codexConversationPermissionProfileListResponse, _ *codexConversationThreadStartResponse) {
			profiles.Data = nil
		}},
		{"disallowed profile", func(_ *codexConversationExperimentalFeatureListResponse, profiles *codexConversationPermissionProfileListResponse, _ *codexConversationThreadStartResponse) {
			profiles.Data[0].Allowed = false
		}},
		{"missing active profile", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			response.ActivePermissionProfile = nil
		}},
		{"inherited active profile", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			parent := ":workspace"
			response.ActivePermissionProfile.Extends = &parent
		}},
		{"mismatched model", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			response.Model = "different"
		}},
		{"missing roots", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			response.RuntimeWorkspaceRoots = nil
		}},
		{"extra root", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			*response.RuntimeWorkspaceRoots = append(*response.RuntimeWorkspaceRoots, "/unexpected")
		}},
		{"missing sources", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			response.InstructionSources = nil
		}},
		{"unexpected source", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			*response.InstructionSources = append(*response.InstructionSources, "/host/AGENTS.md")
		}},
		{"network enabled", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			enabled := true
			response.Sandbox.NetworkAccess = &enabled
		}},
		{"mismatched writable roots", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			roots := []string{"/unexpected"}
			response.Sandbox.WritableRoots = &roots
		}},
		{"persistent thread", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			ephemeral := false
			response.Thread.Ephemeral = &ephemeral
		}},
		{"selected environment", func(_ *codexConversationExperimentalFeatureListResponse, _ *codexConversationPermissionProfileListResponse, response *codexConversationThreadStartResponse) {
			response.Thread.Environments = json.RawMessage(`[{"environmentId":"unexpected"}]`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode := mustCodexConversationMode(t, canonicalTempDir(t))
			features, profiles, response := completeCodexConversationPreflight(mode)
			test.mutate(&features, &profiles, &response)
			if err := mode.validatePreflight(features, profiles, response); !errors.Is(err, errCodexConversationUnsupported) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// This proof uses Codex's offline sandbox command only: it performs no model
// inference and reads no effective account configuration. Opt-in is required
// because nested macOS sandboxing is unavailable in some test runners.
func TestCodexConversationModeOfflineSandboxDenyFixture(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("PAIMOS_CODEX_SANDBOX_PROOF") != "1" {
		t.Skip("offline Codex sandbox proof not safely available")
	}
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex sandbox executable unavailable")
	}
	parent := canonicalTempDir(t)
	scratch := filepath.Join(parent, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	mode := mustCodexConversationMode(t, scratch)
	run := func(command ...string) error {
		args := make([]string, 0, len(mode.configOverrides())*2+8+len(command))
		for _, override := range mode.configOverrides() {
			args = append(args, "-c", override)
		}
		args = append(args, "sandbox", "-P", codexConversationPermissionProfile, "-C", scratch, "--")
		args = append(args, command...)
		output, runErr := exec.Command(codexPath, args...).CombinedOutput() // #nosec G204 -- opt-in test with fixed executable name and structured argv.
		if runErr != nil && strings.Contains(string(output), "sandbox_apply: Operation not permitted") {
			t.Skip("nested Codex sandbox unavailable")
		}
		return runErr
	}
	inside := filepath.Join(scratch, "inside")
	if err := run("/usr/bin/touch", inside); err != nil {
		t.Fatal("scratch write was denied")
	}
	outsideFixture := filepath.Join(parent, "outside-fixture")
	if err := os.WriteFile(outsideFixture, []byte("harmless fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("/bin/cat", outsideFixture); err == nil {
		t.Fatal("outside read unexpectedly succeeded")
	}
	outsideWrite := filepath.Join(parent, "outside-write")
	if err := run("/usr/bin/touch", outsideWrite); err == nil {
		t.Fatal("outside write unexpectedly succeeded")
	}
	if _, err := os.Stat(outsideWrite); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("outside fixture was modified")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("local TCP deny fixture unavailable")
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := run("/usr/bin/nc", "-z", "127.0.0.1", port); err == nil {
		t.Fatal("local TCP connection unexpectedly succeeded")
	}
}

func mustCodexConversationMode(t *testing.T, scratch string) codexConversationMode {
	t.Helper()
	mode, err := buildCodexConversationMode(codexConversationModeInput{
		AccountKey: "conversation-account", DispatchProfileID: "codex-conversation", Model: "gpt-6-sol", Scratch: scratch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func completeCodexConversationPreflight(mode codexConversationMode) (
	codexConversationExperimentalFeatureListResponse,
	codexConversationPermissionProfileListResponse,
	codexConversationThreadStartResponse,
) {
	features := codexConversationExperimentalFeatureListResponse{}
	for _, required := range codexConversationRequiredFeatures() {
		stage := "stable"
		if required.Name == "skip_host_skill_discovery" {
			stage = "underDevelopment"
		}
		features.Data = append(features.Data, codexConversationFeatureState{Name: required.Name, Enabled: required.Enabled, Stage: stage})
	}
	profiles := codexConversationPermissionProfileListResponse{Data: []codexConversationPermissionProfileState{{ID: codexConversationPermissionProfile, Allowed: true}}}
	response := codexConversationThreadStartResponse{}
	response.ActivePermissionProfile = &struct {
		ID      string  `json:"id"`
		Extends *string `json:"extends"`
	}{ID: codexConversationPermissionProfile}
	response.ApprovalPolicy = json.RawMessage(`"never"`)
	response.ApprovalsReviewer = "user"
	response.CWD = mode.scratch
	sources := []string{}
	response.InstructionSources = &sources
	response.Model = mode.model
	response.ModelProvider = "openai"
	response.MultiAgentMode = json.RawMessage(`"explicitRequestOnly"`)
	roots := []string{}
	response.RuntimeWorkspaceRoots = &roots
	response.Sandbox.Type = "workspaceWrite"
	networkDisabled := false
	response.Sandbox.NetworkAccess = &networkDisabled
	writableRoots := []string{}
	response.Sandbox.WritableRoots = &writableRoots
	response.Thread.ID = "thread-restricted"
	response.Thread.CWD = mode.scratch
	response.Thread.Environments = json.RawMessage(`[]`)
	ephemeral := true
	response.Thread.Ephemeral = &ephemeral
	model := mode.model
	response.Thread.Model = &model
	response.Thread.ModelProvider = "openai"
	return features, profiles, response
}
