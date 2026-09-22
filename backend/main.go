// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/devseed"
	"github.com/inspr-at/paimos/backend/handlers"
	"github.com/inspr-at/paimos/backend/handlers/crm"
	"github.com/inspr-at/paimos/backend/handlers/knowledge"
	"github.com/inspr-at/paimos/backend/mailer"
	"github.com/inspr-at/paimos/backend/publicbase"
	"github.com/inspr-at/paimos/backend/releaseacceptance"
	"github.com/inspr-at/paimos/backend/secretinput"
	"github.com/inspr-at/paimos/backend/storage"

	// CRM provider plugins. Blank-import each provider so its init()
	// registers it with the crm package's registry. Adding a new
	// provider = one line here + one new subpackage under crm/.
	_ "github.com/inspr-at/paimos/backend/handlers/crm/http"
	_ "github.com/inspr-at/paimos/backend/handlers/crm/hubspot"
)

func main() {
	// PAI-267: dev-seed subcommand. Recognised only when the dev_login
	// build tag is active (otherwise auth.DevLoginEnabled() is false
	// and we ignore the arg). Re-using the same binary keeps the
	// fixtures co-located with the runtime that uses them and ensures
	// the seed code is build-tag-gated alongside the dev-login handler.
	if len(os.Args) > 1 && os.Args[1] == "dev-seed" {
		if !auth.DevLoginEnabled() {
			log.Fatalf("dev-seed: this binary was not built with -tags dev_login")
		}
		if err := db.Open(); err != nil {
			log.Fatalf("dev-seed: db.Open: %v", err)
		}
		if err := devseed.Run(); err != nil {
			log.Fatalf("dev-seed: %v", err)
		}
		log.Printf("dev-seed: ok")
		return
	}

	// PAI-261: secrets subcommand (rotate, ...). Operator-side; runs
	// on the host where the SQLite file lives. The expected workflow
	// is `service stop → paimos secrets rotate ... → update env →
	// service start` and the subcommand prints that guidance.
	if len(os.Args) > 1 && os.Args[1] == "secrets" {
		if err := db.Open(); err != nil {
			log.Fatalf("secrets: db.Open: %v", err)
		}
		runSecretsSubcommand(os.Args[2:])
		return
	}

	production := productionEnvironment()
	if err := agentmessage.ValidateInstanceIdentity(os.Getenv("PAIMOS_DEPLOYMENT_INSTANCE"), production); err != nil {
		log.Fatalf("configuration: agent bus identity: %v", err)
	}

	if err := secretinput.Validate(
		"ADMIN_PASSWORD",
		"PAIMOS_SECRET_KEY",
		"SMTP_PASS",
		"OIDC_CLIENT_SECRET",
	); err != nil {
		log.Fatalf("configuration: %v", err)
	}

	publicBasePath, err := publicbase.LoadFromEnv()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	publicbase.SetCurrent(publicBasePath)
	if err := auth.ValidatePasswordLoginConfig(); err != nil {
		log.Fatalf("configuration: %v", err)
	}

	// PAI-267: validate dev-login config at boot. No-op on production
	// builds (prod stub returns immediately). On dev builds, panics if
	// PAIMOS_ENV=production OR if PAIMOS_DEV_LOGIN_TOKEN is set but
	// invalid.
	auth.ValidateDevLoginConfig()

	if err := db.Open(); err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := seedAdmin(); err != nil {
		log.Fatalf("seed: %v", err)
	}
	if err := auth.ValidatePasswordLoginAccess(); err != nil {
		log.Fatalf("configuration: %v", err)
	}
	handlers.EnsureAtRiskTag()

	if err := storage.Init(); err != nil {
		log.Printf("WARN: MinIO init failed (attachments disabled): %v", err)
	} else if storage.Enabled() {
		log.Printf("MinIO connected: bucket=%s", storage.Bucket)
	}

	// PAI-117: GDPR retention sweeper. Idempotent — first sweep runs
	// after a 30-second warm-up so a cold start stays quiet.
	handlers.StartRetentionSweeper()
	// PAI-801: reconcile queued and running agent runs from durable,
	// server-received supervisor heartbeat evidence. This is process-owned and
	// does not depend on an issue page or another Implement click.
	handlers.StartAgentRunReconciler()
	handlers.StartHarnessActivityReconciler()
	// PAI-826: instance-local durable webhook outbox for Grok Bot routine
	// wake targets. Local Codex targets remain owned by paimos listen.
	agentmessage.StartWebhookDispatcher(db.DB)
	releaseacceptance.StartMailDispatcher(db.DB, mailer.FromEnv())
	handlers.StartOfferConfirmationDispatcher(mailer.FromEnv())

	r := chi.NewRouter()
	// PAI-809: first and unconditional for every structurally classified
	// control request, including auth/parser failures, 404/405, and panic
	// recovery. Near-miss routes retain their ordinary cache semantics.
	r.Use(handlers.ClassifiedControlCachePolicyMiddleware)
	// PAI-809: chi's Logger for ordinary traffic, a five-field private
	// line for supervisory control. Outermost, so no earlier middleware
	// can record a control request under the ordinary format.
	r.Use(handlers.ControlAwareRequestLogger)
	r.Use(handlers.ControlAwareRecoverer)
	// Native prefix: reject off-mount requests and strip the configured
	// path so chi routes stay /api and /*. Classification already ran on
	// the original URL via publicbase.Current().
	r.Use(publicbase.RejectOutsideAndStrip(publicBasePath))
	r.Use(middleware.Compress(5))
	// PAI-114: baseline security headers on every response. Non-breaking
	// (X-Frame-Options=SAMEORIGIN keeps the in-app PDF preview iframes
	// working, CSP runs in Report-Only mode until PAI-118 lands).
	r.Use(handlers.SecurityHeaders)
	r.Use(handlers.OfferPrivacyMiddleware)
	// PAI-97 / PAI-116: session-scoped mutation audit. On by default; set
	// PAIMOS_AUDIT_SESSIONS=false to disable.
	r.Use(handlers.SessionAuditMiddleware)
	r.Use(handlers.RequestIDMiddleware)

	r.Route("/api", mountAPI)

	// Project logos + avatars are served from DATA_DIR subdirs, which
	// are volume-mounted and survive container rebuilds. The handlers
	// themselves are defined as package-level funcs (serveLogoHandler,
	// serveAvatarHandler below) so they can be wired inside the
	// authenticated route group via the chi r.Get() above.

	// Public branding assets — the login page needs the logo/favicon
	// before any session exists. Path is /brand/<filename>, served from
	// $DATA_DIR/branding-assets/ with a strict filename whitelist. Must
	// be registered BEFORE the SPA catchall ("/*") below, otherwise
	// chi's route matcher falls through to index.html.
	r.Get("/brand/{filename}", handlers.ServeBrandingAsset)

	// Serve static frontend files (SPA fallback)
	staticDir := getStaticDir()
	if _, err := os.Stat(staticDir); err == nil {
		fileServer := http.FileServer(http.Dir(staticDir))
		indexPath := filepath.Join(staticDir, "index.html")
		r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Anchor-clean the request path so ".." segments cannot climb
			// out of staticDir before the Stat below. (The FileServer that
			// actually serves files has its own containment via http.Dir.)
			path := filepath.Join(staticDir, filepath.Clean("/"+r.URL.Path))
			// #nosec G703 -- path is contained: anchored filepath.Clean above strips traversal before the join.
			info, err := os.Stat(path)
			if os.IsNotExist(err) || (info != nil && info.IsDir()) {
				// SPA fallback (or root /): serve index.html with no-cache so
				// browsers always fetch the latest version after deploys.
				if w.Header().Get("Cache-Control") != "no-store" {
					w.Header().Set("Cache-Control", "no-cache")
				}
				serveSPAIndex(w, indexPath)
				return
			}
			// Cache strategy under /assets/:
			//   - Vite-hashed bundle files (e.g. `index-D6h5dtXR.js`)
			//     have a content hash in the filename, so they're safe
			//     to cache immutably — content can't change for a given
			//     URL.
			//   - Stable-name files dropped in via `public/` (e.g.
			//     `crm/hubspot.svg`) keep the same URL across versions,
			//     so caching them immutably is wrong: a future re-skin
			//     under the same name would never reach the user, and
			//     a transient 404 (e.g. asset added in v1.6.1 after a
			//     v1.6.0 visit) would also be locked in.
			// Hash check: Vite emits `<name>-<8+ char base64ish>.<ext>`.
			if len(r.URL.Path) > 8 && r.URL.Path[:8] == "/assets/" {
				if isViteHashed(r.URL.Path) {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					// 1 hour cache for stable-name assets — long enough
					// to be CDN-friendly, short enough to recover from
					// a typo/404 within a coffee break.
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
			}
			fileServer.ServeHTTP(w, r)
		}))
	}

	port := getPort()
	fmt.Printf("%s server starting on :%s\n", brand.Default.ProductName, port)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
	log.Fatal(srv.ListenAndServe())
}

// Server timeouts are intentionally conservative for a reverse-proxied
// single-binary app: reject slow header/body clients, but leave enough
// write time for AI calls, PDF generation, imports, and bounded downloads.
const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 2 * time.Minute
	serverIdleTimeout       = 2 * time.Minute
)

// appVersion is stamped by Dockerfile/-ldflags for releases; "dev" for
// local builds. Surfaced via /api/health so operators can confirm the
// running image without SSHing to the host.
var appVersion = "dev"

type healthResponse struct {
	Status                   string `json:"status"`
	Service                  string `json:"service"`
	Version                  string `json:"version"`
	AgentBusInstance         string `json:"agent_bus_instance"`
	DeploymentInstance       string `json:"deployment_instance"`
	AgentBusIdentityEnforced bool   `json:"agent_bus_identity_enforced"`
}

func productionEnvironment() bool {
	value := strings.TrimSpace(os.Getenv("PAIMOS_ENV"))
	return strings.EqualFold(value, "production") || strings.EqualFold(value, "prod")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Status:                   "ok",
		Service:                  brand.Default.HealthServiceName,
		Version:                  appVersion,
		AgentBusInstance:         agentmessage.InstanceName(),
		DeploymentInstance:       strings.TrimSpace(os.Getenv("PAIMOS_DEPLOYMENT_INSTANCE")),
		AgentBusIdentityEnforced: productionEnvironment(),
	})
}

func liveUpdatesConfigured() bool {
	v := strings.TrimSpace(os.Getenv("PAIMOS_LIVE_UPDATES_ENABLED"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func instanceHandler(w http.ResponseWriter, r *http.Request) {
	reviewer := auth.GetUser(r) != nil && auth.GetUser(r).Role == auth.RoleReviewer
	label := os.Getenv("INSTANCE_LABEL")
	hostname, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	// `attachments_enabled` lets the SPA hide drop zones on instances that
	// don't have MinIO wired up — otherwise the first upload surfaces a
	// 503 with a stuck 0% progress bar. See ACME-1.
	// `crm_enabled` (PAI-980) is the instance-level CRM module switch; the
	// shells use it to show or hide the Customers entry points.
	json.NewEncoder(w).Encode(map[string]any{
		"label":                label,
		"hostname":             hostname,
		"attachments_enabled":  storage.Enabled(),
		"live_updates_enabled": liveUpdatesConfigured() && !reviewer,
		"crm_enabled":          handlers.CRMModuleEnabled(r.Context()) && !reviewer,
	})
}

// serveLogoHandler + serveAvatarHandler serve image files from
// DATA_DIR/logos/ and DATA_DIR/avatars/ respectively. Both are
// registered inside the auth.Middleware group by main() so any
// unauthenticated request returns 401 before reaching the file system.
// Filename whitelist is kept intentionally tight: digits, lowercase,
// dot only — this is defence-in-depth against path traversal even
// though chi.URLParam already decodes away slashes.
func serveLogoHandler(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	for _, c := range filename {
		if !((c >= '0' && c <= '9') || c == '.' || (c >= 'a' && c <= 'z')) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	// #nosec G703 -- filename is whitelisted above to a single [0-9a-z.] segment, and ServeFile rejects ".." path elements.
	http.ServeFile(w, r, filepath.Join(getDataDir(), "logos", filename))
}

func serveAvatarHandler(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	for _, c := range filename {
		if !((c >= '0' && c <= '9') || c == '.' || (c >= 'a' && c <= 'z')) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	// #nosec G703 -- filename is whitelisted above to a single [0-9a-z.] segment, and ServeFile rejects ".." path elements.
	http.ServeFile(w, r, filepath.Join(getDataDir(), "avatars", filename))
}

func seedAdmin() error {
	var count int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		log.Printf("seed: failed to query user count: %v", err)
		return nil
	}
	if count > 0 {
		return nil
	}
	password, err := secretinput.Optional("ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	if password == "" {
		log.Println("seed: ADMIN_PASSWORD missing; refusing to create default admin user")
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Printf("seed: hash error: %v", err)
		return nil
	}
	// PAI-739: the bootstrap user must be super_admin — granting
	// super_admin requires being one, so an admin-only seed made the
	// role unreachable on fresh installs. Column triple mirrors what
	// the users handler writes for a super_admin (legacy role shim +
	// canonical role_key + compat flag).
	_, err = db.DB.Exec(
		"INSERT INTO users(username,password,role,role_key,is_super_admin) VALUES(?,?,?,?,1)",
		"admin", hash, auth.LegacyRoleForPublicRole(auth.RoleSuperAdmin), auth.RoleSuperAdmin,
	)
	if err != nil {
		log.Printf("seed: insert error: %v", err)
		return nil
	}
	log.Println("seed: created super-admin bootstrap user (username: admin)")
	return nil
}

func getPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8888"
}

func getStaticDir() string {
	if dir := os.Getenv("STATIC_DIR"); dir != "" {
		return dir
	}
	return "/app/static"
}

func serveSPAIndex(w http.ResponseWriter, indexPath string) {
	// #nosec G304 -- indexPath is STATIC_DIR/index.html from operator config.
	body, err := os.ReadFile(indexPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body = publicbase.InjectIndexBootstrap(body, publicbase.Current())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func getDataDir() string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}
	return "/app/data"
}

// viteHashedRe matches Vite's bundled-asset filename pattern:
// `<name>-<8+ char base62 hash>.<ext>`. Hashed files are immutable —
// the URL changes when content does, so the cache can never go stale.
// Stable-name files dropped via `public/` (e.g. `crm/hubspot.svg`)
// don't match and get a short cache so they can be re-skinned.
var viteHashedRe = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

func isViteHashed(urlPath string) bool {
	return viteHashedRe.MatchString(urlPath)
}

// mountAPI registers every /api/* route. Extracted from main() (PAI-294)
// so the OpenAPI-coverage guard test can walk the route tree without
// booting the server, which keeps the published public surface auditable.
func mountAPI(r chi.Router) {
	// Classified control families retain their fixed privacy envelope even
	// when chi rejects a missing route or wrong method before a handler runs.
	// Ordinary API paths keep chi's existing behavior.
	r.NotFound(handlers.ControlAwareNotFound)
	r.MethodNotAllowed(handlers.ControlAwareMethodNotAllowed)

	// ── Strictly public endpoints ────────────────────────────────
	// Everything here is accessible without a session. Keep this
	// list short and auditable — the only valid reasons for a
	// route to live outside the auth group are:
	//   (a) health checks for Docker / CI / monitoring, or
	//   (b) the login page needs it before any session can exist, or
	//   (c) agent-discovery endpoints the CLI / MCP fetch before
	//       any API key is issued (e.g. /api/schema), or
	//   (d) finalized offer capabilities, with dedicated expiry and rate gates.
	// Audited 2026-04-21.
	r.Get("/health", healthHandler) // (a) Docker + CI
	handlers.RegisterPublicOfferRoutes(r)
	r.Get("/branding", handlers.GetBranding) // (b) login page logo + colors
	r.Get("/schema", handlers.GetAPISchema)  // (c) CLI / MCP discovery (PAI-87)
	// PAI-332: public adapter registry. CLI / external tooling
	// browses the published adapter list without an account so
	// adapter authors can self-link from their own README.
	r.Get("/registry/adapters", handlers.GetAdapterRegistry)
	// PAI-119: published OpenAPI 3.1 contract. Public so external
	// clients can discover the API without an account.
	r.Get("/openapi.json", handlers.GetOpenAPI)
	// PAI-114: CSP violation reports. Browser-driven, unauthenticated.
	r.Post("/csp-report", handlers.CSPReport)

	// Auth (public — no session possible yet)
	r.Post("/auth/login", auth.LoginHandler)
	// PAI-743: identifier-first home realm discovery. Config-derived
	// only — see INV-AUTH-HRD in auth/login_methods.go for why this
	// must never look the identifier up.
	r.Post("/auth/login/methods", auth.LoginMethods)
	r.Post("/auth/totp/verify", auth.TOTPVerify)
	// PAI-267: dev-login route — only mounted when the dev_login
	// build tag is active. Production binaries do not contain
	// the handler symbol and the route returns chi's stock 404.
	if auth.DevLoginEnabled() {
		r.Post("/auth/dev-login", auth.DevLoginHandler)
	}

	// PAI-120: OpenID Connect SSO. Status is always reachable so the
	// SPA can decide whether to render the SSO button; login starts
	// the flow and callback completes it. All three are public — no
	// session can exist before the IdP redirects the user back.
	r.Get("/auth/oidc/status", auth.OIDCStatus)
	r.Get("/auth/oidc/login", auth.OIDCLogin)
	r.Get("/auth/oidc/callback", auth.OIDCCallback)

	// Forgot / reset password — all three must stay public since a
	// user who has forgotten their password has no session cookie.
	r.Post("/auth/forgot", handlers.ForgotPassword)
	r.Get("/auth/reset/validate", handlers.ValidateResetToken)
	r.Post("/auth/reset", handlers.ResetPassword)

	// Auth (authenticated but open to all roles, including external)
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware) // PAI-113
		// PAI-321: must_change_password gate. Allowlist (logout,
		// /auth/me, /auth/password) is built into the gate so the
		// user can complete the change-password flow from this
		// group without bypass.
		r.Use(auth.MustChangePasswordGate)

		r.Post("/auth/logout", auth.LogoutHandler)
		r.Get("/auth/me", auth.MeHandler)
		r.Post("/auth/password", auth.ChangePassword)
		r.Post("/auth/impersonation/start", handlers.StartImpersonation)
		r.Post("/auth/impersonation/end", handlers.EndImpersonation)
		r.Patch("/auth/me", handlers.UpdateProfile)
		r.Post("/auth/avatar", handlers.UploadAvatar)
		r.Delete("/auth/avatar", handlers.DeleteAvatar)

		// TOTP 2FA (authenticated)
		r.Get("/auth/totp/status", auth.TOTPStatus)
		r.Get("/auth/totp/setup", auth.TOTPSetup)
		r.Post("/auth/totp/enable", auth.TOTPEnable)
		r.Post("/auth/totp/disable", auth.TOTPDisable)

		// API keys
		r.With(auth.RequireAdmin, auth.RequireScope(auth.ScopeFlowCredentialsWrite)).Post("/auth/flow-projection-credentials", handlers.CreateFlowProjectionCredential)
		r.With(auth.RequireAdmin, auth.RequireScope(auth.ScopeFlowCredentialsWrite)).Delete("/auth/flow-projection-credentials/{id}", handlers.RevokeFlowProjectionCredential)
		r.Get("/auth/api-keys", handlers.ListAPIKeys)
		r.Post("/auth/api-keys", handlers.CreateAPIKey)
		r.Delete("/auth/api-keys/{id}", handlers.DeleteAPIKey)
		r.With(auth.RequireAdmin).Post("/auth/machine-notifiers", handlers.CreateMachineNotifier)
		r.Post("/machine-notifier/messages", handlers.SendMachineNotifierMessage)
		r.Get("/machine-notifier/messages/{messageID}/receipt", handlers.GetMachineNotifierReceipt)

		// PAI-331 — auto-watch sync subscriptions (per-user,
		// per-(device, project) toggle). The browser UI hits these;
		// the CLI manages its row implicitly via the SSE handshake.
		// {deviceID} is opaque text; {projectID} is numeric. Routes
		// stay under /auth/* because the resource is "this user's
		// preferences", same as the api-keys neighborhood.
		r.Get("/auth/auto-watch", handlers.ListAutoWatch)
		r.Put("/auth/auto-watch/{deviceID}/{projectID}", handlers.UpsertAutoWatch)
		r.Delete("/auth/auto-watch/{deviceID}/{projectID}", handlers.DeleteAutoWatch)

		// moved inside the auth group. Any authed role
		// (including external/portal users) may fetch these — they
		// are non-sensitive but there's no good reason for them to
		// be reachable pre-authentication.
		r.Get("/instance", instanceHandler)
		r.Get("/brandings", handlers.ListBrandings)
		r.Get("/logos/{filename}", serveLogoHandler)
		r.Get("/avatars/{filename}", serveAvatarHandler)
		r.Get("/projektberichte/accept/{code}", handlers.GetProjectReportAcceptance)
		r.Post("/projektberichte/accept/{code}", handlers.AcceptProjectReport)
		r.Put("/projektberichte/accept/{code}/signed", handlers.LinkProjectReportSignedArtifact)
		r.Get("/projektberichte/{code}/pdf", handlers.GetProjectReportPDF)

		// PAI-799 telemetry performs its own requester/claimer/admin and
		// run-visibility checks so unauthorized run ids return the same 404 as
		// missing ids. Keep it outside BlockExternal, which would turn that
		// existence-hiding response into an early 403.
		r.Post("/runs/{id}/telemetry", handlers.IngestAgentRunTelemetry)
		r.Get("/runs/{id}/telemetry", handlers.ListAgentRunTelemetry)
		r.Get("/runs/{id}/telemetry/latest", handlers.GetLatestAgentRunTelemetry)
	})

	// Agent Mode has an existence-hiding guard of its own: external users
	// receive the same 404 as an inaccessible resource. Keep this separate
	// from BlockExternal, whose 403 contract is correct for the older internal
	// API but would disclose that this supervision surface exists.
	r.Route("/agent-mode", func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore)
		r.Use(auth.Middleware)
		r.Use(auth.RequireAgentModeInternal)
		r.Use(auth.CSRFMiddleware)
		r.Use(auth.MustChangePasswordGate)
		r.NotFound(handlers.AgentModeControlAwareNotFound)
		r.MethodNotAllowed(handlers.AgentModeControlAwareMethodNotAllowed)

		// Literal events must precede the delivery-key wildcard.
		r.Get("/deliveries/events", handlers.AgentModeEvents)
		r.Get("/deliveries", handlers.AgentModeDeliveries)
		r.Get("/projects/{projectID}/deliveries", handlers.AgentModeProjectDeliveries)
		r.Get("/deliveries/{deliveryKey}", handlers.AgentModeDelivery)
		handlers.RegisterOrchestrationProjectionRoutes(r)
		r.Get("/worker-fleet/v1", handlers.AgentModeWorkerFleet)
		r.Get("/projects/{projectID}/worker-fleet/v1", handlers.AgentModeProjectWorkerFleet)
		r.Get("/worker-fleet/v2", handlers.AgentModeWorkerFleetV2)
		r.Get("/projects/{projectID}/worker-fleet/v2", handlers.AgentModeProjectWorkerFleetV2)
		r.Post("/voice/transcribe", handlers.TranscribeAgentModeVoice)
		r.Post("/voice/speak", handlers.SpeakAgentModeVoice)
		handlers.MountAgentModeControlRoutes(r)
		handlers.MountInternalExternalStageContractRoutes(r)
	})

	// Runner control is API-key only and intentionally lives outside the
	// legacy BlockExternal group. Exact scope and credential/device/run
	// bindings are enforced again by the transport and supervision service.
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware)
		r.Use(auth.RequireAgentModeInternal)
		r.Use(auth.CSRFMiddleware)
		r.Use(auth.MustChangePasswordGate)
		handlers.MountRunnerControlRoutes(r)
	})

	// PAI-810 contract freeze: external stage adapters are API-key callers.
	// The transport conceals failed authentication, and the service rechecks the
	// exact key/reporter/project/handoff binding in every transaction.
	r.Group(func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore)
		r.Use(handlers.ExternalStageAPIKeyAuth)
		handlers.MountExternalStageContractRoutes(r)
	})

	// PAI-861: session home is a private projection. Keep its no-store
	// middleware outside every early authentication/role gate so 401, 403,
	// concealed 404, and handler responses all carry the same cache policy.
	// The remaining gates intentionally match the ordinary internal group.
	// PAI-865: instance orchestrator identity is private on every outcome.
	// Keep no-store outside authentication and every later refusal gate.
	r.Group(func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore)
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware)
		r.Use(auth.MustChangePasswordGate)
		r.Use(auth.BlockExternal)
		handlers.RegisterSessionHomeRoutes(r)
		handlers.RegisterOrchestratorRoutes(r)
		handlers.RegisterCommandPaletteRoutes(r)
	})

	// PAI-924: private lifecycle authority, including early auth/CSRF refusals.
	r.Group(func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore)
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware)
		r.Use(auth.MustChangePasswordGate)
		handlers.RegisterLifecycleIntentRoutes(r)
		handlers.RegisterConsumerRoutes(r)
		handlers.RegisterHarnessBrowserRoutes(r)
	})

	// PAI-1027: dedicated conversation-service and exact-runtime runner lanes.
	// The credential kind has a strict method/path allowlist in auth middleware;
	// every domain transition reauthorizes its immutable binding again.
	r.Group(func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore)
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware)
		r.Use(auth.MustChangePasswordGate)
		r.With(auth.RequireAdmin).Post("/auth/conversation-services", handlers.CreateConversationService)
		handlers.RegisterConversationRoutes(r)
	})

	// PAI-863: the structured-knowledge surface is private on every outcome.
	// No-store must wrap authentication and every refusal gate so even early
	// 401/403/concealed-404 responses cannot be cached. Keep this dedicated
	// group as the single production registration for the complete surface,
	// including the instance/kernel/vision promotion endpoint.
	r.Group(func(r chi.Router) {
		r.Use(auth.AgentModePrivateNoStore)
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware)
		r.Use(auth.MustChangePasswordGate)
		r.Use(auth.BlockExternal)
		handlers.RegisterStructuredKnowledgeRoutes(r)
	})

	// Portal (external + admin)
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware)         // PAI-113
		r.Use(auth.MustChangePasswordGate) // PAI-321
		r.Use(auth.RequirePortalAccess)

		r.Get("/portal/overview", handlers.PortalOverview)
		r.Get("/portal/projects", handlers.PortalListProjects)
		r.Get("/portal/projects/{id}", handlers.PortalGetProject)
		r.Get("/portal/projects/{id}/issues", handlers.PortalListIssues)
		r.Get("/portal/projects/{id}/issues/{issueId}", handlers.PortalGetIssue)
		r.Post("/portal/projects/{id}/requests", handlers.PortalSubmitRequest)
		r.Get("/portal/issues/{id}/comments", handlers.PortalListIssueComments)
		r.Post("/portal/issues/{id}/accept", handlers.PortalAcceptIssue)
		r.Post("/portal/issues/{id}/reject", handlers.PortalRejectIssue)
		r.Post("/portal/issues/{id}/undo-accept", handlers.PortalUndoAccept)
		r.Post("/portal/issues/{id}/undo-reject", handlers.PortalUndoReject)
		r.Get("/portal/projects/{id}/summary", handlers.PortalProjectSummary)
		r.Get("/portal/projects/{id}/projektberichte", handlers.ListProjectReports)
		r.Get("/portal/projects/{id}/acceptance-report", handlers.AcceptanceReport)
		handlers.RegisterPortalReleaseAcceptanceRoutes(r)
	})

	// Internal (admin + member; blocked for external)
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware)
		r.Use(auth.CSRFMiddleware)         // PAI-113
		r.Use(auth.MustChangePasswordGate) // PAI-321
		r.Use(auth.BlockExternal)

		r.Get("/changes", handlers.ChangesStream)

		// Projects
		r.Get("/projects", handlers.ListProjects)
		// PAI-379: scope-narrowable project creation. The admin
		// role is still required, AND when the caller authenticates
		// via api-key the key must carry the projects:write scope.
		// Session-cookie auth implicitly carries ScopeAll, so the
		// admin web UI is unaffected.
		r.With(auth.RequireAdmin, auth.RequireScope(auth.ScopeProjectsWrite)).Post("/projects", handlers.CreateProject)
		r.With(auth.RequireProjectView).Get("/projects/{id}", handlers.GetProject)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}", handlers.UpdateProject)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}", handlers.DeleteProject)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/logo", handlers.UploadProjectLogo)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/logo", handlers.DeleteProjectLogo)
		r.With(auth.RequireProjectView).Get("/projects/{id}/repos", handlers.ListProjectRepos)
		r.With(auth.RequireProjectEdit).Post("/projects/{id}/repos", handlers.CreateProjectRepo)
		r.With(auth.RequireProjectEdit).Put("/projects/{id}/repos/{repoId}", handlers.UpdateProjectRepo)
		r.With(auth.RequireProjectEdit).Delete("/projects/{id}/repos/{repoId}", handlers.DeleteProjectRepo)
		r.With(auth.RequireProjectView).Get("/projects/{id}/anchors", handlers.ListProjectAnchors)
		// PAI-326 — declarable agents. Read = view; writes = admin
		// (project-settings territory; matches CreateProject auth).
		r.With(auth.RequireProjectView).Get("/projects/{id}/agents", handlers.ListProjectAgents)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/agents", handlers.CreateProjectAgent)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/agents/{name}", handlers.UpdateProjectAgent)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/agents/{name}", handlers.DeleteProjectAgent)
		// PAI-329 — canonical agent artifact + markdown debug
		// rendering. Read-only, view-gated.
		r.With(auth.RequireProjectView).Get("/projects/{id}/agents/{name}.json", handlers.GetProjectAgentArtifact)
		r.With(auth.RequireProjectView).Get("/projects/{id}/agents/{name}.md", handlers.GetProjectAgentArtifactMarkdown)
		// PAI-331 — auto-watch sync: SSE event stream + cheap-poll
		// .rev fallback. The events route is per-project and the
		// route param `id` is the project id (chi's URL parser
		// matches the same pattern as the .json/.md siblings). The
		// .rev path mirrors the .json shape so polling clients
		// reuse their cached agent_name; the response is plain
		// text (the 12-char rev hash) so a curl loop can compare
		// without a JSON parser.
		r.With(auth.RequireProjectView).Get("/projects/{id}/agents/events", handlers.AgentsEventsStream)
		r.With(auth.RequireProjectView).Get("/projects/{id}/agents/{name}.rev", handlers.AgentRevHandler)
		// PAI-817 — agent-to-agent messaging security contract.
		// Prevents prompt injection via framing, allowlists, hop
		// limits, rate limits. Action-request messages are NEVER
		// delivered as executable; they surface to humans only.
		handlers.RegisterAgentMessageRoutes(r)
		// PAI-848 — durable harness-session identity and daemon-owned controls.
		handlers.RegisterHarnessSessionRoutes(r)
		// PAI-859 / PAI-860 — first-class product sessions and the additive
		// one-node compatibility API. These resources do not alias the harness,
		// run, delivery, attribution, issue, or intake-session identities.
		handlers.RegisterProductSessionRoutes(r)
		handlers.RegisterNodeRoutes(r)
		// PAI-607: online implement-capable runners for the device picker.
		r.With(auth.RequireProjectView).Get("/projects/{id}/runners", handlers.ListProjectRunners)
		r.With(auth.RequireProjectView).Get("/projects/{id}/runs", handlers.ListProjectRuns)
		// PAI-329 — project-level inventories (environments,
		// deploy recipes). repos already exists at /repos.
		r.With(auth.RequireProjectView).Get("/projects/{id}/environments", handlers.ListProjectEnvironments)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/environments", handlers.CreateProjectEnvironment)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/environments/{envId}", handlers.UpdateProjectEnvironment)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/environments/{envId}", handlers.DeleteProjectEnvironment)
		r.With(auth.RequireProjectView).Get("/projects/{id}/deploy-recipes", handlers.ListProjectDeployRecipes)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/deploy-recipes", handlers.CreateProjectDeployRecipe)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/deploy-recipes/{recipeId}", handlers.UpdateProjectDeployRecipe)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/deploy-recipes/{recipeId}", handlers.DeleteProjectDeployRecipe)

		// PAI-338 (gated by PAI-346) — knowledge plane. PAI-394
		// collapsed the original five-alias surface
		// (/memory, /runbooks, /external-systems, /related-
		// projects, /guidelines) into one resource. The type
		// discriminator lives in the URL path segment for
		// reads, and in the request body for POST. Auth
		// mirrors agents: read = project view, writes = admin
		// (knowledge entries shape an agent's view of the
		// project, so they're project-settings adjacent).
		//
		// Memory-specific subroutes (PAI-347 decay tracking +
		// PAI-349 admin review) live under /knowledge/memory/
		// and are guarded against shadowing by the reserved-
		// slug check in knowledge.IsReservedSlug.
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge", knowledge.ListAllHandler)
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/graph", knowledge.GraphHandler)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/knowledge", knowledge.CreateHandler)
		r.With(auth.RequireProjectView).Post("/projects/{id}/knowledge/memory/references", handlers.BumpMemoryReferences)
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/stale", handlers.ListStaleMemory)
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/proposed/stale", handlers.ListStaleProposedMemory)
		// PAI-351 slice 2 — needs-review triage queue. Literal route MUST
		// precede the {slug} wildcard (and "needs-review" is a reserved slug).
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/needs-review", knowledge.NeedsReviewHandler)
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/{slug}/dependents", knowledge.MemoryDependentsHandler)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/knowledge/memory/{slug}/reviewed", handlers.MarkMemoryReviewed)
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/{type}/{slug}", knowledge.GetHandler)
		r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/{type}/{slug}.rev", handlers.KnowledgeRevHandler)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/knowledge/{type}/{slug}", knowledge.UpdateHandler)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/knowledge/{type}/{slug}", knowledge.DeleteHandler)

		// PAI-345 — cross-scope memory: user-scope and instance-scope
		// CRUD parallels the project-scope endpoints above. The
		// discriminator is the WHERE clause inside the handler;
		// see handlers/knowledge_user.go and knowledge_instance.go.
		// User memory: any auth'd user can manage their own.
		r.Get("/users/me/memory", handlers.ListUserMemory)
		r.Post("/users/me/memory", handlers.CreateUserMemory)
		r.Get("/users/me/memory/{slug}", handlers.GetUserMemory)
		r.Put("/users/me/memory/{slug}", handlers.UpdateUserMemory)
		r.Delete("/users/me/memory/{slug}", handlers.DeleteUserMemory)
		// Instance memory: read open to all authenticated users,
		// writes admin-only. Defence-in-depth check inside each
		// handler keeps the gate even if the middleware is wrong.
		r.Get("/instance/memory", handlers.ListInstanceMemory)
		r.With(auth.RequireAdmin).Post("/instance/memory", handlers.CreateInstanceMemory)
		r.Get("/instance/memory/{slug}", handlers.GetInstanceMemory)
		r.With(auth.RequireAdmin).Put("/instance/memory/{slug}", handlers.UpdateInstanceMemory)
		r.With(auth.RequireAdmin).Delete("/instance/memory/{slug}", handlers.DeleteInstanceMemory)
		// Promotion endpoint — crosses scopes by design, so no
		// /projects/:id prefix. Auth is per-scope inside the
		// handler (admin gate fires when promoting to instance).
		r.Post("/memory/{slug}/promote", handlers.PromoteMemory)

		// PAI-394 moved PAI-347 + PAI-349 memory subroutes
		// under /projects/{id}/knowledge/memory/... — see
		// the unified knowledge mount block above.
		// PAI-358: GET /projects/{id}/manifest, PUT
		// /projects/{id}/manifest, and POST /projects/{id}/migrate-
		// manifest-to-knowledge are gone — the legacy taxonomy is
		// fully replaced by the PAI-338 knowledge plane.
		r.With(auth.RequireProjectEdit).Post("/projects/{id}/anchors", handlers.IngestProjectAnchors)
		r.With(auth.RequireProjectView).Get("/projects/{id}/graph", handlers.ListProjectEntityRelations)
		r.With(auth.RequireProjectView).Get("/projects/{id}/graph/blast-radius", handlers.BlastRadius)
		r.With(auth.RequireProjectView).Post("/projects/{id}/retrieve", handlers.RetrieveProjectContext)
		handlers.RegisterBaselineBatchRoutes(r)
		handlers.RegisterReleaseAcceptanceRoutes(r)

		// Project key suggestion
		r.Get("/projects/suggest-key", handlers.SuggestProjectKey)

		// Issues nested under project
		r.With(auth.RequireProjectView).Get("/projects/{id}/issues", handlers.ListIssues)
		r.With(auth.RequireProjectEdit, handlers.IdempotencyMiddleware).Post("/projects/{id}/issues", handlers.CreateIssue)
		r.With(auth.RequireProjectView).Get("/projects/{id}/issues/tree", handlers.GetIssueTree)
		r.With(auth.RequireProjectView).Get("/projects/{id}/cost-units", handlers.ListCostUnits)
		r.With(auth.RequireProjectView).Get("/projects/{id}/releases", handlers.ListReleases)
		r.With(auth.RequireProjectView).Get("/projects/{id}/export/csv", handlers.ExportCSV)
		r.With(auth.RequireProjectView).Get("/projects/{id}/acceptance-log", handlers.AcceptanceLog)
		r.With(auth.RequireProjectView).Get("/projects/{id}/acceptance-report", handlers.AcceptanceReport)
		r.With(auth.RequireProjectView).Get("/projects/{id}/reports/lieferbericht", handlers.GetLieferbericht)
		r.With(auth.RequireProjectView).Get("/projects/{id}/reports/lieferbericht/pdf", handlers.GetLieferberichtPDF)
		r.With(auth.RequireProjectView).Get("/projects/{id}/reports/projektbericht", handlers.GetLieferbericht)
		r.With(auth.RequireProjectView).Get("/projects/{id}/reports/projektbericht/pdf", handlers.GetLieferberichtPDF)
		// PAI-579: booked-hours report (user × project × window). Hours/material only.
		r.With(auth.RequireProjectView).Get("/projects/{id}/time-report", handlers.GetProjectTimeReport)
		r.With(auth.RequireProjectView).Get("/projects/{id}/projektberichte", handlers.ListProjectReports)
		r.With(auth.RequireProjectView).Get("/projects/{id}/report-permissions", handlers.ListProjectReportPermissions)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/report-permissions", handlers.PutProjectReportPermissions)
		// Project accruals (Vorräte) — admin only
		r.With(auth.RequireAdmin).Get("/reports/accruals", handlers.GetAccruals)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/import/csv/preflight", handlers.ImportCSVPreflight)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/import/csv", handlers.ImportCSV)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/projects/{id}/time-entries/users", handlers.PurgeUsers)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/time-entries/purge-preview", handlers.PurgePreview)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/time-entries/purge", handlers.PurgeTimeEntries)
		r.With(auth.RequireAdmin).Post("/import/csv/preflight", handlers.ImportCSVGlobalPreflight)
		r.With(auth.RequireAdmin).Post("/import/csv", handlers.ImportCSVGlobal)

		// Dashboard recent activity (must be before /issues/{id})
		r.Get("/issues/recent", handlers.RecentIssues)
		// Trash (soft-deleted issues) — admin-only (must be before /issues/{id})
		r.With(auth.RequireAdmin).Get("/issues/trash", handlers.ListTrashIssues)
		// Cross-project issue list + orphan sprint creation (must be before /issues/{id})
		// ListOrLookupIssues dispatches to the key-pick handler when ?keys= is
		// present (PAI-88) and falls through to ListAllIssues otherwise.
		r.Get("/issues", handlers.ListOrLookupIssues)
		r.Post("/issues", handlers.CreateOrphanIssue)

		// Bulk issue ops (PAI-88). Admin-only for v1 — simpler to reason
		// about than cross-project per-item access checks, and the primary
		// caller is the paimos CLI run under an admin key. Single-issue
		// endpoints still serve non-admins.
		r.With(auth.RequireAdmin).Patch("/issues", handlers.UpdateIssuesBatch)
		r.With(auth.RequireAdmin, handlers.IdempotencyMiddleware).Post("/projects/{key}/issues/batch", handlers.CreateIssuesBatch)

		// Sprints
		r.Get("/sprints", handlers.ListSprints)
		r.Get("/sprints/years", handlers.ListSprintYears)
		r.Get("/sprints/{year}", handlers.ListSprintsByYear)
		r.With(auth.RequireAdmin).Post("/sprints/batch", handlers.CreateSprintsBatch)
		r.With(auth.RequireAdmin).Put("/sprints/{id}", handlers.UpdateSprint)
		r.With(auth.RequireAdmin).Post("/sprints/{id}/move-incomplete", handlers.MoveIncompleteToNextSprint)
		r.With(auth.RequireAdmin).Put("/sprints/{id}/reorder", handlers.ReorderSprintMembers)
		// Cross-project distinct values
		r.Get("/cost-units", handlers.ListAllCostUnits)
		r.Get("/releases", handlers.ListAllReleases)

		// Issues by ID
		r.With(auth.RequireIssueAccess).Get("/issues/{id}", handlers.GetIssue)
		r.With(auth.RequireIssueEdit).Put("/issues/{id}", handlers.UpdateIssue)
		r.With(auth.RequireIssueEdit).Patch("/issues/{id}", handlers.UpdateIssue)
		// PAI-690 — move an issue to another project (re-key + old-key alias).
		// RequireIssueEdit gates the source project; the handler additionally
		// verifies edit access on the target. Bulk move authorizes per-issue.
		r.With(auth.RequireIssueEdit).Post("/issues/{id}/move", handlers.MoveIssue)
		r.Post("/issues/move", handlers.MoveIssuesBulk)
		r.With(auth.RequireAdmin, auth.RequireIssueAccess).Delete("/issues/{id}", handlers.DeleteIssue)
		r.With(auth.RequireAdmin, auth.RequireIssueAccess).Post("/issues/{id}/restore", handlers.RestoreIssue)
		r.With(auth.RequireAdmin, auth.RequireIssueAccess).Delete("/issues/{id}/purge", handlers.PurgeIssue)
		r.With(auth.RequireAdmin, auth.RequireIssueAccess).Patch("/issues/{id}/archive", handlers.ArchiveIssue)
		// Attachments
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/attachments", handlers.ListAttachments)
		r.With(auth.RequireIssueEdit).Post("/issues/{id}/attachments", handlers.UploadAttachment)
		r.With(auth.RequireAttachmentAccess).Get("/attachments/{id}/meta", handlers.GetAttachmentMeta)
		r.With(auth.RequireAttachmentAccess).Get("/attachments/{id}", handlers.GetAttachmentFile)
		r.With(auth.RequireAttachmentEdit).Delete("/attachments/{id}", handlers.DeleteAttachment)
		r.Post("/attachments", handlers.UploadPendingAttachment)
		r.Patch("/attachments/link", handlers.LinkAttachments)

		r.With(auth.RequireIssueEdit).Post("/issues/{id}/clone", handlers.CloneIssue)
		// PAI-606 (epic PAI-605): "Implement this" run lifecycle.
		r.With(auth.RequireIssueEdit).Post("/issues/{id}/implement", handlers.ImplementIssue)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/runs", handlers.ListIssueRuns)
		r.Get("/runs/{id}", handlers.GetAgentRun)
		r.Patch("/runs/{id}", handlers.PatchAgentRun)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/aggregation", handlers.GetIssueAggregation)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/children", handlers.GetIssueChildren)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/history", handlers.GetIssueHistory)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/anchors", handlers.ListIssueAnchors)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/ai-activity", handlers.AIListIssueActivity)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/activity", handlers.ListIssueMutationActivity)
		r.Get("/undo/activity", handlers.ListMyMutationActivity)
		r.Post("/undo/{id}", handlers.UndoMutation)
		r.Post("/undo/{id}/resolve", handlers.ResolveUndoMutation)
		r.Post("/undo/request/{requestID}", handlers.UndoMutationByRequestID)
		r.Post("/redo/{id}", handlers.RedoMutation)
		r.Post("/redo/{id}/resolve", handlers.ResolveRedoMutation)
		r.Post("/redo/request/{requestID}", handlers.RedoMutationByRequestID)
		r.With(auth.RequireIssueEdit).Post("/issues/{id}/complete-epic", handlers.CompleteEpic)

		// PAI-704 (epic PAI-703): voice-intake workbench sessions. UI-internal
		// surface, intentionally not in /api/openapi.json. Ownership is
		// enforced inside the handlers (owner-or-admin, non-owner → 404,
		// INV-INTAKE-01) because sessions are project-less until detection.
		r.Post("/intake/sessions", handlers.CreateIntakeSession)
		r.Get("/intake/sessions", handlers.ListIntakeSessions)
		r.Get("/intake/sessions/{id}", handlers.GetIntakeSession)
		r.Patch("/intake/sessions/{id}", handlers.PatchIntakeSession)
		r.Delete("/intake/sessions/{id}", handlers.AbandonIntakeSession)
		r.Post("/intake/sessions/{id}/transcript", handlers.IngestIntakeTranscript)
		r.Post("/intake/sessions/{id}/checkpoints", handlers.CreateIntakeCheckpoint)
		r.Get("/intake/sessions/{id}/events", handlers.ListIntakeEvents)
		r.Get("/intake/sessions/{id}/state", handlers.GetIntakeState)
		r.Post("/intake/sessions/{id}/restore", handlers.RestoreIntakeSession)
		r.Post("/intake/sessions/{id}/refresh", handlers.RefreshIntakeSession)
		r.Post("/intake/sessions/{id}/audio", handlers.TranscribeIntakeAudio)
		r.Post("/intake/sessions/{id}/tts", handlers.SpeakIntakeSummary)
		r.With(auth.RequireProjectEdit, handlers.IdempotencyMiddleware).Post("/projects/{id}/intake-sessions/{sessionID}/issue", handlers.CreateIntakeIssue)
		r.Get("/intake/sessions/{id}/stream", handlers.IntakeSessionStream)

		// Issue relations (v2)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/relations", handlers.ListIssueRelations)
		r.With(auth.RequireIssueEdit, handlers.IdempotencyMiddleware).Post("/issues/{id}/relations", handlers.CreateIssueRelation)
		r.With(auth.RequireAdmin, auth.RequireIssueAccess).Delete("/issues/{id}/relations", handlers.DeleteIssueRelation)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/members", handlers.ListIssuesByRelation)

		// PAI-342: applicable memories. Read-only convenience over
		// issue_relations(type='applies_to_memory') with an optional
		// `?suggest=1` mode that scores up to 3 candidates the
		// ticket isn't yet linked to. Mutations reuse the relations
		// endpoints above with the same type discriminator.
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/applicable-memories", handlers.ListApplicableMemories)

		// PAI-343: lesson-capture trigger detection. Asked by the UI
		// (and the `paimos issue update --draft-memory` CLI flag)
		// when a ticket transitions to a terminal status, so the
		// trigger logic stays in one place. Read-only.
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/lesson-capture-prompt", handlers.LessonCapturePrompt)

		// Time entries (v2)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/time-entries", handlers.ListTimeEntries)
		r.With(auth.RequireIssueEdit).Post("/issues/{id}/time-entries", handlers.CreateTimeEntry)
		r.Get("/time-entries/running", handlers.GetRunningTimers)
		r.Get("/time-entries/recent", handlers.GetRecentTimers)
		r.Get("/time-entries/today-summary", handlers.GetTimeEntriesTodaySummary)
		r.Get("/time-entries/week", handlers.GetWeekTimeEntries)
		r.With(auth.RequireTimeEntryAccess).Get("/time-entries/{id}", handlers.GetTimeEntry)
		r.With(auth.RequireTimeEntryEdit).Put("/time-entries/{id}", handlers.UpdateTimeEntry)
		r.With(auth.RequireTimeEntryEdit).Delete("/time-entries/{id}", handlers.DeleteTimeEntry)

		// System tag rules (admin only)
		r.Get("/system-tag-rules", handlers.ListSystemTagRules)
		r.With(auth.RequireAdmin).Put("/system-tag-rules", handlers.UpdateSystemTagRule)

		// Recent projects (current user)
		r.Get("/users/me/recent-projects", handlers.GetRecentProjects)
		r.Post("/users/me/recent-projects", handlers.UpsertRecentProject)

		// Users (admin only for create/update/delete)
		r.Get("/users", handlers.ListUsers)
		r.With(auth.RequireAdmin).Post("/users", handlers.CreateUser)
		r.With(auth.RequireAdmin).Put("/users/{id}", handlers.UpdateUser)
		r.With(auth.RequireAdmin).Post("/users/{id}/disable", handlers.DisableUser)
		r.With(auth.RequireAdmin).Delete("/users/{id}", handlers.DeleteUser)
		r.With(auth.RequireAdmin).Post("/users/{id}/reset-totp", handlers.ResetUserTOTP)

		// PAI-117: per-subject GDPR ops. Export streams every row that
		// references the user; erase replaces PII with a placeholder
		// instead of cascade-deleting historical project data.
		r.With(auth.RequireAdmin).Get("/users/{id}/gdpr-export", handlers.ExportSubject)
		r.With(auth.RequireAdmin).Post("/users/{id}/gdpr-erase", handlers.EraseSubject)
		r.With(auth.RequireAdmin).Get("/gdpr/retention", handlers.GetRetentionPolicy)
		r.With(auth.RequireAdmin).Get("/system/settings", handlers.GetSystemSettings)
		r.With(auth.RequireAdmin).Put("/system/settings", handlers.PutSystemSettings)

		// User project access (admin only). The legacy /users/{id}/projects
		// endpoints drive the external-portal grants page; the new
		// /users/{id}/memberships endpoints drive the full matrix editor
		// (with viewer/editor/none levels). Both write the same table.
		r.With(auth.RequireAdmin).Get("/users/{id}/projects", handlers.ListUserProjects)
		r.With(auth.RequireAdmin).Post("/users/{id}/projects", handlers.AddUserProject)
		r.With(auth.RequireAdmin).Delete("/users/{id}/projects/{projectId}", handlers.RemoveUserProject)
		r.With(auth.RequireAdmin).Get("/users/{id}/memberships", handlers.ListUserMemberships)
		r.With(auth.RequireAdmin).Put("/users/{id}/memberships/{projectId}", handlers.UpsertUserMembership)
		r.With(auth.RequireAdmin).Delete("/users/{id}/memberships/{projectId}", handlers.DeleteUserMembership)

		// Permissions matrix (read-only; any authenticated internal user
		// may view it, but the settings page is admin-gated on the UI side).
		r.Get("/permissions/matrix", handlers.GetPermissionsMatrix)

		// Access-change audit log (admin only).
		r.With(auth.RequireAdmin).Get("/access-audit", handlers.ListAccessAudit)
		r.With(auth.RequireAdmin, auth.RequireCapability(auth.CapabilitySuperAdminAuditRead)).Get("/super-admin-activity", handlers.ListSuperAdminActivity)

		// Incident log (PAI-116). Admin-only CRUD + JSON/CSV export
		// for SIEM ingestion. Export route is registered before
		// /incidents/{id} so the literal path wins the chi match.
		r.With(auth.RequireAdmin).Get("/incidents/export", handlers.ExportIncidents)
		r.With(auth.RequireAdmin).Get("/incidents", handlers.ListIncidents)
		r.With(auth.RequireAdmin).Post("/incidents", handlers.CreateIncident)
		r.With(auth.RequireAdmin).Get("/incidents/{id}", handlers.GetIncident)
		r.With(auth.RequireAdmin).Patch("/incidents/{id}", handlers.UpdateIncident)
		r.With(auth.RequireAdmin).Delete("/incidents/{id}", handlers.DeleteIncident)

		// Session-scoped mutation audit (PAI-97). Admin only. Returns
		// the mutations recorded under a session, keyset-paginated by
		// `id > cursor`. Empty array for unknown sessions.
		r.With(auth.RequireAdmin).Get("/sessions/{id}/activity", handlers.GetSessionActivity)

		// Tags (admin only for write)
		r.Get("/tags", handlers.ListTags)
		r.With(auth.RequireAdmin).Post("/tags", handlers.CreateTag)
		r.With(auth.RequireAdmin).Put("/tags/{id}", handlers.UpdateTag)
		r.With(auth.RequireAdmin).Delete("/tags/{id}", handlers.DeleteTag)

		// Tag associations
		r.With(auth.RequireProjectView).Get("/projects/{id}/tags", handlers.ListProjectTags)
		r.With(auth.RequireIssueEdit).Post("/issues/{id}/tags", handlers.AddTagToIssue)
		r.With(auth.RequireIssueEdit).Delete("/issues/{id}/tags/{tag_id}", handlers.RemoveTagFromIssue)
		// PAI-463: compact endpoint backing the IssueDetailView visibility
		// toggle's audit line. Read-only — any user with issue access.
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/portal-visibility", handlers.GetIssuePortalVisibility)
		// PAI-465: bulk attach/detach a single tag across N issues.
		// Per-issue permission check happens inside the handler so a
		// mixed-permission selection fails cleanly with 403.
		r.Post("/issues/batch/tags", handlers.BatchTagIssues)
		// PAI-467: admin Customer Portal Visibility report. Both
		// JSON and CSV variants are admin + project-view gated.
		r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/admin/projects/{id}/portal-visibility", handlers.GetAdminProjectPortalVisibility)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/admin/projects/{id}/portal-visibility.csv", handlers.GetAdminProjectPortalVisibilityCSV)
		r.With(auth.RequireProjectEdit).Post("/projects/{id}/tags", handlers.AddTagToProject)
		r.With(auth.RequireProjectEdit).Delete("/projects/{id}/tags/{tag_id}", handlers.RemoveTagFromProject)

		// Comments
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/comments", handlers.ListComments)
		r.With(auth.RequireIssueEdit, handlers.CommentIdempotencyMiddleware).Post("/issues/{id}/comments", handlers.CreateComment)
		r.With(auth.RequireCommentEdit).Patch("/comments/{id}", handlers.UpdateCommentVisibility)
		r.With(auth.RequireCommentAccess).Delete("/comments/{id}", handlers.DeleteComment)

		// Branding write endpoints (admin only). GET /api/branding stays
		// public (see the public group above) because the login page needs
		// it pre-auth. Writes are admin-gated; assets are served by
		// /brand/{filename} at the root, below.
		r.With(auth.RequireAdmin).Put("/branding", handlers.PutBranding)
		r.With(auth.RequireAdmin).Post("/branding/logo", handlers.UploadBrandingLogo)
		r.With(auth.RequireAdmin).Post("/branding/favicon", handlers.UploadBrandingFavicon)

		// AI text optimization (PAI-146). Admin-only settings + write
		// surface; the per-call optimize endpoint is mounted in the
		// authenticated group below so any logged-in user with edit
		// rights on the target field can call it. AIStatus stays in
		// the authenticated (non-admin) group as well — the SPA polls
		// it on every render that shows an AI button.
		r.With(auth.RequireAdmin).Get("/ai/settings", handlers.GetAISettings)
		r.With(auth.RequireAdmin).Put("/ai/settings", handlers.PutAISettings)
		r.With(auth.RequireAdmin).Get("/ai/voice-settings", handlers.GetVoiceSettings)
		r.With(auth.RequireAdmin).Put("/ai/voice-settings", handlers.PutVoiceSettings)
		// PAI-159: admin-only test-connection ping. Mounted in the
		// admin group; CSRF middleware (PAI-113) covers it.
		r.With(auth.RequireAdmin).Post("/ai/test", handlers.AITestConnection)
		// PAI-160: live OpenRouter model picker, server-cached 1h.
		// Admin-only because the response shapes admin decisions
		// (which model to enable for the whole instance).
		r.With(auth.RequireAdmin).Get("/ai/models", handlers.AIListModels)
		// PAI-161: per-user usage summary for the admin dashboard.
		r.With(auth.RequireAdmin).Get("/ai/usage", handlers.AIUsage)
		r.With(auth.RequireAdmin).Get("/ai/calls", handlers.AIListCalls)
		r.With(auth.RequireAdmin).Get("/ai/calls/export.csv", handlers.AIExportCallsCSV)
		r.With(auth.RequireAdmin).Get("/ai/calls/{id}", handlers.AIGetCall)
		// PAI-175 / PAI-176 / PAI-177: prompt CRUD + dry-run.
		// Admin-only. The list endpoint lazily seeds built-in
		// rows so a fresh install never sees an empty list.
		r.With(auth.RequireAdmin).Get("/ai/prompts", handlers.AIListPrompts)
		r.With(auth.RequireAdmin).Post("/ai/prompts", handlers.AICreatePrompt)
		r.With(auth.RequireAdmin).Put("/ai/prompts/{id}", handlers.AIUpdatePrompt)
		r.With(auth.RequireAdmin).Delete("/ai/prompts/{id}", handlers.AIDeletePrompt)
		r.With(auth.RequireAdmin).Post("/ai/prompts/{id}/reset", handlers.AIResetPrompt)
		r.With(auth.RequireAdmin).Post("/ai/prompts/{id}/dry-run", handlers.AIDryRunPrompt)
		r.Get("/ai/actions", handlers.AIListActions)
		r.Get("/ai/execution-options", handlers.AIExecutionOptions)
		r.Get("/models/catalog", handlers.ModelCatalog)
		r.Get("/models/resolve", handlers.ModelResolve)
		r.With(auth.RequireAdmin).Put("/models/overrides", handlers.PutModelOverrides)
		r.Get("/ai/status", handlers.AIStatus)
		r.Get("/ai/calls/me", handlers.AIListMyCalls)
		r.Get("/ai/calls/me/export.csv", handlers.AIExportMyCallsCSV)
		// PAI-164: legacy /api/ai/optimize endpoint retired —
		// the optimize behaviour now lives behind /api/ai/action
		// with action="optimize". The frontend useAiOptimize
		// composable was updated to call the dispatcher.
		r.Post("/ai/action", handlers.AIAction)
		// PAI-449: pre-run cost estimate for bulk AI flows.
		r.Get("/ai/bulk-cost-estimate", handlers.AIBulkCostEstimate)
		r.With(auth.RequireIssueAccess).Get("/issues/{id}/ai-calls", handlers.AIListIssueCalls)

		// Integrations (admin only for write)
		r.Get("/integrations/jira", handlers.GetJiraIntegration)
		r.With(auth.RequireAdmin).Put("/integrations/jira", handlers.PutJiraIntegration)
		r.With(auth.RequireAdmin).Post("/integrations/jira/test", handlers.TestJiraIntegration)

		// Mite integration
		r.Get("/integrations/mite", handlers.GetMiteIntegration)
		r.With(auth.RequireAdmin).Put("/integrations/mite", handlers.PutMiteIntegration)
		r.With(auth.RequireAdmin).Post("/integrations/mite/test", handlers.TestMiteIntegration)

		// CRM provider plugin layer (PAI-101). All admin-only.
		// The plugin layer does not assume any specific provider —
		// `crm.List()` only returns whatever was blank-imported in
		// this binary.
		r.With(auth.RequireAdmin).Get("/integrations/crm", crm.ListProviders)
		// PAI-980: instance-level CRM module switch (read for everyone via
		// GET /instance; admin read/write here).
		r.With(auth.RequireAdmin).Get("/integrations/crm/module", handlers.GetCRMModule)
		r.With(auth.RequireAdmin).Put("/integrations/crm/module", handlers.PutCRMModule)
		r.With(auth.RequireAdmin).Get("/integrations/crm/offers", handlers.GetOfferSettings)
		r.With(auth.RequireAdmin).Put("/integrations/crm/offers", handlers.PutOfferSettings)
		r.Get("/customers/{id}/offers", handlers.ListCustomerOffers)
		r.Get("/offers/acceptances", handlers.ListOfferAcceptances)
		r.Get("/offers/summary", handlers.ListOfferSummary)
		r.With(auth.RequireAdmin).Put("/offers/{id}/deleted", handlers.SetOfferDeleted)
		r.With(auth.RequireAdmin).Get("/offers/readiness", handlers.GetOfferDeliveryReadiness)
		r.With(auth.RequireAdmin).Post("/offers/{id}/confirmation/retry", handlers.RetryOfferConfirmation)
		r.Get("/offers/{id}/pdf", handlers.GetOfferPDF)
		r.With(auth.RequireAdmin).Post("/offers/{id}/link", handlers.CreateOfferLink)
		r.Get("/offers/{id}", handlers.GetOffer)
		r.With(auth.RequireAdmin).Post("/offers", handlers.CreateOffer)
		r.With(auth.RequireAdmin).Put("/offers/{id}", handlers.PutOffer)

		r.With(auth.RequireAdmin).Get("/integrations/crm/{id}/config", crm.GetProviderConfig)
		r.With(auth.RequireAdmin).Put("/integrations/crm/{id}/config", crm.PutProviderConfig)
		r.With(auth.RequireAdmin).Put("/integrations/crm/{id}/enabled", crm.PutProviderEnabled)
		r.With(auth.RequireAdmin).Post("/integrations/crm/{id}/test", crm.TestProviderConnection)
		// PAI-266: name-based company search across enabled providers.
		r.With(auth.RequireAdmin).Get("/integrations/crm/search", crm.SearchProviders)

		// Customers (PAI-53). CRM-agnostic CRUD; manual customers
		// fully supported (no provider required).
		r.Get("/customers", handlers.ListCustomers)
		r.Get("/customers/{id}", handlers.GetCustomer)
		r.With(auth.RequireAdmin).Post("/customers", handlers.CreateCustomer)
		r.With(auth.RequireAdmin).Put("/customers/{id}", handlers.UpdateCustomer)
		r.With(auth.RequireAdmin).Post("/customers/{id}/number/reformat", handlers.ReformatCustomerNumber)
		r.With(auth.RequireAdmin).Delete("/customers/{id}", handlers.DeleteCustomer)
		// Provider-driven import / sync (PAI-103).
		r.With(auth.RequireAdmin).Post("/customers/import", crm.ImportCustomer)
		r.With(auth.RequireAdmin).Post("/customers/{id}/sync", crm.SyncCustomer)

		// Contacts (PAI-273). One customer holds many Ansprechpartner;
		// list is open to anyone with customer-read, mutations are
		// admin-only. promote-primary is the dedicated atomic flip.
		r.Get("/customers/{id}/contacts", handlers.ListCustomerContacts)
		r.With(auth.RequireAdmin).Post("/customers/{id}/contacts", handlers.CreateCustomerContact)
		r.Get("/contacts/{id}", handlers.GetContact)
		r.With(auth.RequireAdmin).Put("/contacts/{id}", handlers.UpdateContact)
		r.With(auth.RequireAdmin).Delete("/contacts/{id}", handlers.DeleteContact)
		r.With(auth.RequireAdmin).Post("/contacts/{id}/promote-primary", handlers.PromoteContactPrimary)

		// Documents (PAI-55). Scoped under customer / project for
		// list + upload; unscoped paths for read / mutate / delete
		// since documents are uniquely id-addressable.
		r.Get("/customers/{id}/documents", handlers.ListCustomerDocuments)
		r.With(auth.RequireAdmin).Post("/customers/{id}/documents", handlers.UploadCustomerDocument)
		r.With(auth.RequireProjectView).Get("/projects/{id}/documents", handlers.ListProjectDocuments)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/documents", handlers.UploadProjectDocument)
		r.Get("/documents/{id}/download", handlers.DownloadDocument)
		r.With(auth.RequireAdmin).Put("/documents/{id}", handlers.UpdateDocument)
		r.With(auth.RequireAdmin).Delete("/documents/{id}", handlers.DeleteDocument)

		// Cooperation metadata (PAI-61). Per-project engagement
		// profile; informational only in v1.
		r.With(auth.RequireProjectView).Get("/projects/{id}/cooperation", handlers.GetCooperation)
		r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/cooperation", handlers.PutCooperation)

		// Mite import
		r.With(auth.RequireAdmin).Post("/import/mite", handlers.ImportFromMite)
		r.With(auth.RequireAdmin).Get("/import/mite/jobs/{id}", handlers.GetMiteImportJobStatus)
		r.With(auth.RequireAdmin).Post("/import/mite/jobs/{id}/cancel", handlers.CancelMiteImportJob)
		r.With(auth.RequireAdmin).Get("/import/mite/resume-date", handlers.GetMiteResumeDate)
		r.With(auth.RequireAdmin).Delete("/import/mite/entries", handlers.DeleteMiteEntries)

		// Jira import
		r.With(auth.RequireAdmin).Get("/import/jira/projects", handlers.ListJiraProjects)
		r.With(auth.RequireAdmin).Get("/import/jira/debug", handlers.DebugJiraFetch)
		r.With(auth.RequireAdmin).Post("/import/jira", handlers.ImportFromJira)
		r.With(auth.RequireAdmin).Get("/import/jira/jobs/{id}", handlers.GetImportJobStatus)

		// Views (saved column+filter sets)
		r.Get("/views", handlers.ListViews)
		r.Post("/views", handlers.CreateView)
		r.Put("/views/{id}", handlers.UpdateView)
		r.Delete("/views/{id}", handlers.DeleteView)
		r.With(auth.RequireAdmin).Patch("/views/order", handlers.ReorderViews)
		r.Post("/views/{id}/pin", handlers.PinView)
		r.Delete("/views/{id}/pin", handlers.UnpinView)

		// Search
		r.Get("/search", handlers.Search)

		// Dev panel (admin only)
		r.With(auth.RequireAdmin).Post("/dev/test-reports", handlers.UploadTestReport)
		r.With(auth.RequireAdmin).Get("/dev/test-reports", handlers.ListTestReports)
		r.With(auth.RequireAdmin).Get("/dev/test-reports/summary", handlers.GetTestReportSummary)
		r.With(auth.RequireAdmin).Get("/dev/test-reports/{filename}", handlers.GetTestReport)
	})
}
