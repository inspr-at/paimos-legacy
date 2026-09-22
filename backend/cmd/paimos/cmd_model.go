// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/cmd/paimos/sync"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/spf13/cobra"
)

func modelCmd() *cobra.Command {
	cmd := commandGroup(&cobra.Command{Use: "model", Short: "Resolve model roles through the instance catalog"})
	var author, harness, workspace string
	resolve := &cobra.Command{Use: "resolve <role>", Short: "Return a pinned profile, decision ladder and command template", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := instanceClient()
		if err != nil {
			return err
		}
		root, err := resolveWorkspaceRoot(workspace)
		if err != nil {
			return err
		}
		result, err := resolveModel(cmd.Context(), client, root, dispatchprofile.ResolveRequest{Role: args[0], AuthorFamily: dispatchprofile.Family(author), Harness: harness})
		if err != nil {
			return err
		}
		if flagJSON {
			if err = emitJSON(result); err != nil {
				return err
			}
		} else {
			if result.Stale {
				fmt.Fprintf(stdout, "STALE cached policy from %s (instance unreachable)\n", result.CachedAt.Format(time.RFC3339))
			}
			if result.Profile != nil {
				fmt.Fprintf(stdout, "%s@%s — %s\n%s\n", result.Profile.ID, result.Profile.Version, result.Profile.Family, result.Command)
			}
			for _, c := range result.Ladder {
				status := "eligible fallback"
				if c.Selected {
					status = "selected"
				}
				if len(c.SkipReasons) > 0 {
					status = strings.Join(c.SkipReasons, "; ")
				}
				fmt.Fprintf(stdout, "%s: %s\n", c.ProfileID, status)
			}
		}
		if result.OwnerRequired {
			return errors.New("owner approval required; review gate remains closed")
		}
		return nil
	}}
	resolve.Flags().StringVar(&author, "author-family", "", "author model family; required for review-gate")
	resolve.Flags().StringVar(&harness, "harness", "", "restrict to codex, claude, pi or cursor")
	resolve.Flags().StringVar(&workspace, "workspace", "", "workspace containing the instance catalog cache (default cwd)")
	cmd.AddCommand(resolve, modelTemplatesCmd())
	return cmd
}

func resolveModel(ctx context.Context, client *Client, root string, request dispatchprofile.ResolveRequest) (dispatchprofile.Resolution, error) {
	var result dispatchprofile.Resolution
	// Validate the combination locally before contacting an instance or reading cache.
	if _, err := dispatchprofile.NewRegistry(nil).Resolve(request, time.Now()); err != nil {
		return result, err
	}
	query := url.Values{"role": {request.Role}, "author_family": {string(request.AuthorFamily)}, "harness": {request.Harness}}
	err := client.getJSON(ctx, "/api/models/resolve?"+query.Encode(), &result)
	if err == nil {
		if result.Source != "instance" || result.Stale || result.CachedAt != nil {
			return result, errors.New("invalid instance model resolution provenance")
		}
		return result, dispatchprofile.ValidateResolution(request, result)
	}
	// An HTTP denial, unsupported server, invalid policy or malformed response is
	// authoritative. Only a transport outage permits stale offline policy.
	var transport net.Error
	if ctx.Err() != nil || !errors.As(err, &transport) {
		return result, err
	}
	cached, cacheErr := sync.ReadModelCatalogCache(&httpSyncClient{client: client}, root)
	if cacheErr != nil {
		return result, fmt.Errorf("instance unreachable; no valid model catalog cache: %w", cacheErr)
	}
	result, err = cached.Registry.Resolve(request, time.Now())
	result.Source = "cache"
	result.Stale = true
	result.CachedAt = &cached.FetchedAt
	return result, err
}

// Syntax discovery works offline and does not resolve or authorize a model.
func modelTemplatesCmd() *cobra.Command {
	var readOnly bool
	cmd := &cobra.Command{Use: "templates <harness>", Short: "Show run, review, resume and spawn syntax without executing it", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		templates, err := dispatchprofile.HarnessTemplates(args[0], readOnly)
		if err != nil {
			return err
		}
		if flagJSON {
			return emitJSON(templates)
		}
		fmt.Fprintf(stdout, "run: %s\nreview: %s\nresume: %s\nspawn: %s\n", templates.Run, templates.Review, templates.Resume, templates.Spawn)
		return nil
	}}
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "retain review restrictions in run, resume and spawn templates")
	return cmd
}
