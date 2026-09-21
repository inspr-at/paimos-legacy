// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const (
	grokConversationProfileID = "grok-native-4.7-xhigh"
	grokConversationModel     = "grok-4.7"
	grokConversationEffort    = "xhigh"
	grokConfigSHA256          = "3b9f1cefb4672eed5856debd6173bb82009546ad9b10f28d8372749d1bb8e259"
	grokProfileSHA256         = "9cd1990054adc092e004e649da746e4bb2844ae6debae4a8aa5495df5ecfb231"
)

// GrokBinaryVariant identifies an operator-local executable layout and its
// immutable artifact digest. Variants are deliberately explicit: the
// source-built artifact is not treated as the npm CLI package.
type GrokBinaryVariant string

const (
	GrokBinaryNPM1030         GrokBinaryVariant = "npm-grok-1.0.30"
	GrokBinarySourceBuilt1032 GrokBinaryVariant = "source-xai-grok-pager-1.0.32"
)

type grokVariantSpec struct {
	variant       GrokBinaryVariant
	binaryName    string
	executableSHA string
	sourceCommit  string
}

func grokVariantSpecFor(variant GrokBinaryVariant) (grokVariantSpec, error) {
	switch variant {
	case GrokBinaryNPM1030:
		return grokVariantSpec{
			variant:       variant,
			binaryName:    "grok-native",
			executableSHA: "d53b6e543e482716236748914331db50145c696ac7af91f1ebdedcf5654cfecb",
		}, nil
	case GrokBinarySourceBuilt1032:
		return grokVariantSpec{
			variant:       variant,
			binaryName:    "xai-grok-pager",
			executableSHA: "6294a6bc10304e3d3b5896194277f6d24fca643b0605d650ca39bad5b40a6d14",
			sourceCommit:  "482711333c7195dc16a272777f86086d615e2afb",
		}, nil
	default:
		return grokVariantSpec{}, errors.New("native Grok executable variant unavailable")
	}
}

//go:embed grokassets/config.toml
var grokConversationConfig []byte

//go:embed grokassets/conversation.txt
var grokConversationProfile []byte

// GrokConversationBinding is operator-local, never a public claim. The opaque
// key and expected OIDC subject digest must be enrolled through the existing
// account/attachment authority before a caller may use this adapter.
type GrokConversationBinding struct {
	BinaryVariant   GrokBinaryVariant
	BinaryPath      string
	AuthPath        string
	AccountKey      string
	PrincipalSHA256 string
	ScratchRoot     string
}

// GrokConversationAdapter intentionally does not implement Adapter and is not
// registered with Supervisor or the public profile catalog. PAI-1028's live
// gate must be completed before any capability can be advertised.
type GrokConversationAdapter struct{ binding GrokConversationBinding }

func NewGrokConversationAdapter(binding GrokConversationBinding) (*GrokConversationAdapter, error) {
	if !validAccountKey(binding.AccountKey) || !safeGrokAbsolutePath(binding.BinaryPath) ||
		!safeGrokAbsolutePath(binding.AuthPath) || !safeGrokAbsolutePath(binding.ScratchRoot) ||
		len(binding.PrincipalSHA256) != 64 {
		return nil, errors.New("native Grok conversation binding is unavailable")
	}
	if _, err := grokBinaryRoot(binding.BinaryVariant, binding.BinaryPath, binding.AuthPath, binding.ScratchRoot); err != nil {
		return nil, err
	}
	if _, err := hex.DecodeString(binding.PrincipalSHA256); err != nil ||
		sha256Hex(grokConversationConfig) != grokConfigSHA256 ||
		sha256Hex(grokConversationProfile) != grokProfileSHA256 {
		return nil, errors.New("native Grok conversation assets are unavailable")
	}
	return &GrokConversationAdapter{binding: binding}, nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (a *GrokConversationAdapter) StartConversation(ctx context.Context, request StartRequest, options GrokConversationOptions) (GrokConversationExecution, error) {
	if a == nil || request.KeepAlive || request.Workspace != "" || request.Adapter != AdapterGrok ||
		request.AccountKey != a.binding.AccountKey || request.AttachmentRevision <= 0 ||
		request.ResolvedProfile == nil || request.DispatchProfileID != grokConversationProfileID ||
		request.DispatchProfileID != request.ResolvedProfile.ID || request.DispatchProfileVersion != dispatchprofile.CatalogVersion ||
		request.DispatchProfileVersion != request.ResolvedProfile.Version ||
		request.ResolvedProfile.Harness != AdapterGrok || request.ResolvedProfile.Model != grokConversationModel ||
		request.ResolvedProfile.Effort != grokConversationEffort ||
		request.ResolvedProfile.AccountSource != dispatchprofile.AccountLocalProbe ||
		request.ResolvedProfile.MachineSource != dispatchprofile.MachineAuthenticatedReporter ||
		request.ResolvedProfile.WorkspaceMode != WorkspaceExclusive ||
		request.ProjectID <= 0 || len(request.Prompt) == 0 || len(request.Prompt) > 192<<10 || !utf8.ValidString(request.Prompt) ||
		options.MaxOutputBytes < 1 || options.MaxOutputBytes > maxCodexConversationOutputBytes ||
		options.MaxEvents < 2 || options.MaxEvents > maxCodexConversationEvents || len(options.OutputSchema) != 0 {
		return nil, errors.New("native Grok conversation scope is unavailable")
	}
	return a.startNativeConversation(ctx, request, options)
}

// GrokConversationExecution owns exactly one ACP prompt. Replay never launches
// inference; Stop and WaitConversation reap its process and erase scratch.
type GrokConversationExecution interface {
	ConversationIdentity() (string, string, error)
	ReplayConversation(after uint64) ([]CodexConversationDelta, error)
	WaitConversation(context.Context) (CodexConversationResult, error)
	Stop(context.Context) error
}

func safeGrokAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && value != "/" && !strings.ContainsAny(value, "\x00\r\n\"\\") &&
		!strings.Contains(value, "/../") && !strings.HasSuffix(value, "/..") &&
		!strings.Contains(value, "/./") && !strings.HasSuffix(value, "/.")
}

func grokPackageRoot(binary, authPath, scratchRoot string) (string, error) {
	return grokBinaryRoot(GrokBinaryNPM1030, binary, authPath, scratchRoot)
}

func grokBinaryRoot(variant GrokBinaryVariant, binary, authPath, scratchRoot string) (string, error) {
	spec, err := grokVariantSpecFor(variant)
	if err != nil {
		return "", err
	}
	if variant == GrokBinarySourceBuilt1032 {
		return grokSourceBuildRoot(spec, binary, authPath, scratchRoot)
	}
	return grokNPMPackageRoot(spec, binary, authPath, scratchRoot)
}

func grokNPMPackageRoot(spec grokVariantSpec, binary, authPath, scratchRoot string) (string, error) {
	root := filepath.Dir(filepath.Dir(binary))
	if filepath.Base(binary) != spec.binaryName || filepath.Base(filepath.Dir(binary)) != "bin" ||
		filepath.Base(root) != "grok" || filepath.Base(filepath.Dir(root)) != "@xai-official" ||
		filepath.Base(filepath.Dir(filepath.Dir(root))) != "node_modules" || !safeGrokAbsolutePath(root) {
		return "", errors.New("native Grok package path is unavailable")
	}
	home := os.Getenv("HOME")
	if !safeGrokAbsolutePath(home) || pathInsideGrok(root, home) || pathInsideGrok(root, authPath) ||
		pathInsideGrok(root, scratchRoot) || pathInsideGrok(scratchRoot, root) {
		return "", errors.New("native Grok package read boundary is unsafe")
	}
	return root, nil
}

func grokSourceBuildRoot(spec grokVariantSpec, binary, authPath, scratchRoot string) (string, error) {
	releaseRoot := filepath.Dir(binary)
	if filepath.Base(binary) != spec.binaryName || filepath.Base(releaseRoot) != "release" ||
		filepath.Base(filepath.Dir(releaseRoot)) != "target" || !safeGrokAbsolutePath(releaseRoot) {
		return "", errors.New("native Grok source-built path is unavailable")
	}
	home := os.Getenv("HOME")
	if !safeGrokAbsolutePath(home) || pathInsideGrok(releaseRoot, home) || pathInsideGrok(releaseRoot, authPath) ||
		pathInsideGrok(releaseRoot, scratchRoot) || pathInsideGrok(scratchRoot, releaseRoot) {
		return "", errors.New("native Grok source-built read boundary is unsafe")
	}
	return releaseRoot, nil
}

func pathInsideGrok(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
