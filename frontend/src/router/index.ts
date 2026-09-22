/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { useAuthStore } from "@/stores/auth";
import { useUndoStore } from "@/stores/undo";
import { safePostLoginRedirect, reviewerRouteAllowed } from "@/router/redirects";
import { mustChangePassword } from "@/api/client";
import { publicURL, routerHistoryBase } from "@/publicPath";
import type { AppShell } from "@/router/shell";

// Route meta shape. `projectIdParam` names the URL param that holds the
// project ID — the beforeEach guard uses it to enforce per-project view
// access before the component mounts.
declare module "vue-router" {
  interface RouteMeta {
    public?: boolean;
    adminOnly?: boolean;
    portal?: boolean;
    acceptance?: boolean;
    projectIdParam?: string;
    // PAI-274 / PAI-361: views that own their internal scroll container
    // (e.g. lists with sticky headers and frozen columns) opt into a
    // flex-bounded `.main-content--self-scroll` so position: sticky has
    // a stable scrolling ancestor. Default 'page' lets the route's
    // root grow with content and .main-content own the scroll —
    // correct for tall, page-scroll views like Settings and
    // IssueDetail. See AppLayout.vue.
    scrollMode?: "page" | "self";
    // PAI-735: focused-tool routes hide the header search field (the
    // capability stays reachable via / and ⌘K).
    headerSearchHidden?: boolean;
    // PAI-805: reduced application shell. `agent` swaps the standard
    // sidebar/header chrome for AgentModeLayout (logo rail, logout,
    // settings, auth/session banners only). Resolved by
    // `resolveLayout()` in ./shell.ts; absent = standard AppLayout.
    shell?: AppShell;
  }
}

// PAI-805: routes are built by a function so the production contract
// (no /dev/* reference routes, agent-mode shell meta) is testable without
// depending on import.meta.env at test time.
export function buildRoutes(includeDev: boolean): RouteRecordRaw[] {
  return [
    {
      path: "/login",
      component: () => import("@/views/LoginView.vue"),
      meta: { public: true },
    },
    {
      path: "/forgot",
      component: () => import("@/views/ForgotPasswordView.vue"),
      meta: { public: true },
    },
    {
      path: "/reset/:token",
      component: () => import("@/views/ResetPasswordView.vue"),
      meta: { public: true },
    },
    {
      path: "/accept/:code",
      component: () => import("@/views/ProjectReportAcceptView.vue"),
      meta: { acceptance: true },
    },
    {
      // PAI-321: forced first-login change-password screen. The guard
      // routes here whenever the API client's 403 interceptor flips the
      // mustChangePassword ref. Marked `public: true` so the guard's
      // "must be logged in" check doesn't bounce the user back to /login
      // — they ARE logged in, they just can't go anywhere else yet.
      path: "/first-login",
      component: () => import("@/views/FirstLoginView.vue"),
      meta: { public: true },
    },
    {
      // PAI-867: the accepted Paimos 6 home owns the production root.
      path: "/",
      component: () => import("@/views/Paimos6PreviewView.vue"),
      meta: { shell: "v6" },
    },
    {
      // PAI-867: keep the exact 5.x dashboard and standard application
      // chrome available as the explicit, stable rollback escape.
      path: "/legacy",
      component: () => import("@/views/DashboardView.vue"),
    },
    { path: "/projects", component: () => import("@/views/ProjectsView.vue") },
    {
      path: "/projects/accruals/print",
      component: () => import("@/views/AccrualsPrintView.vue"),
      meta: { adminOnly: true },
    },
    {
      path: "/projects/:id",
      component: () => import("@/views/ProjectDetailView.vue"),
      meta: { projectIdParam: "id", scrollMode: "self" },
    },
    {
      path: "/projects/:id/issues/:issueId",
      component: () => import("@/views/IssueDetailView.vue"),
      meta: { projectIdParam: "id" },
    },
    {
      // PAI-467: admin Customer Portal Visibility report. Admin-only;
      // the view itself bounces non-admins back to / on mount.
      path: "/admin/projects/:id/portal-visibility",
      component: () => import("@/views/AdminPortalVisibilityView.vue"),
      meta: { projectIdParam: "id", adminOnly: true },
    },
    {
      path: "/customers",
      component: () => import("@/views/CustomersView.vue"),
    },
    {
      // PAI-980: the CRM door in the Paimos 6 shell. Renders the existing
      // customer views inside the v6 layout; list ↔ detail links keep the
      // /crm base so the shell never changes underneath the user.
      path: "/crm",
      component: () => import("@/views/v6/CrmDoorView.vue"),
      meta: { shell: "v6" },
    },
    { path: "/offers/:token", name: "public-offer", component: () => import("@/views/PublicOfferView.vue"), meta: { public: true } },
    { path: "/crm/offers/:id/print", component: () => import("@/views/v6/OfferView.vue"), meta: { shell: "print" } },
    { path: "/crm/offers/:id", component: () => import("@/views/v6/OfferView.vue"), meta: { shell: "v6" } },
    {
      path: "/crm/:id",
      component: () => import("@/views/v6/CrmDoorView.vue"),
      meta: { shell: "v6" },
    },
    {
      path: "/customers/:id",
      component: () => import("@/views/CustomerDetailView.vue"),
    },
    {
      path: "/issues",
      component: () => import("@/views/IssuesView.vue"),
      meta: { scrollMode: "self" },
    },
    {
      path: "/issues/:issueId",
      component: () => import("@/views/IssueDetailView.vue"),
    },
    { path: "/sprints", redirect: "/sprint-board" },
    {
      path: "/sprint-board",
      component: () => import("@/views/SprintBoardView.vue"),
    },
    { path: "/users", redirect: "/settings?tab=users" },
    {
      path: "/integrations",
      component: () => import("@/views/IntegrationsView.vue"),
      meta: { adminOnly: true },
    },
    { path: "/import", redirect: "/integrations" },
    { path: "/search", redirect: "/issues" },
    { path: "/settings", component: () => import("@/views/SettingsView.vue") },
    { path: "/development", redirect: "/settings?tab=development" },
    // Portal routes (external users)
    {
      path: "/portal",
      component: () => import("@/views/portal/PortalDashboard.vue"),
      meta: { portal: true },
    },
    {
      path: "/portal/projects/:id",
      component: () => import("@/views/portal/PortalProjectView.vue"),
      meta: { portal: true },
    },
    {
      path: "/portal/projects/:id/issues/:issueId",
      component: () => import("@/views/portal/PortalIssueView.vue"),
      meta: { portal: true },
    },
    {
      path: "/reporting",
      component: () => import("@/views/ReportingView.vue"),
    },
    {
      path: "/reporting/lieferbericht",
      component: () => import("@/views/LieferberichtView.vue"),
    },
    {
      path: "/reporting/projektbericht",
      component: () => import("@/views/LieferberichtView.vue"),
    },
    {
      path: "/hours/week",
      component: () => import("@/views/HoursWeekView.vue"),
    },
    {
      path: "/hours/project",
      component: () => import("@/views/HoursProjectView.vue"),
    },
    {
      // PAI-703: Voice Intake workbench — first-class route since the epic
      // shipped end-to-end (promoted from the DEV gate in the PAI-709 slice).
      // PAI-735: the workbench toolbar lives in the app header; the search
      // field yields its spot but / and ⌘K still reveal it.
      path: "/intake",
      component: () => import("@/views/VoiceIntakeView.vue"),
      meta: { headerSearchHidden: true },
    },
    {
      // PAI-805: Agent Mode — supervision cockpit (detail 10 ships here;
      // the focused-delivery and portfolio-overview levels render from the
      // same data). Renders in the reduced Agent Mode shell (see
      // ./shell.ts): logo rail, logout, settings, security banners — no
      // ordinary project / issue / timer / undo chrome.
      path: "/agent-mode",
      component: () => import("@/views/AgentModeView.vue"),
      meta: { shell: "agent" },
    },
    ...(includeDev
      ? [
          {
            path: "/dev/ai-ux",
            component: () => import("@/components/ai/AiUxDevReference.vue"),
          },
          {
            // PAI-805: fixture-backed Agent Mode reference (1/10/100
            // deliveries + every load state) for visual QA without the
            // PAI-804 backend. DEV builds only; never reachable in prod.
            path: "/dev/agent-mode",
            component: () => import("@/components/agent-mode/AgentModeDevReference.vue"),
            meta: { shell: "agent" as const },
          },
          {
            // PAI-854 / PAI-861: retain the development alias for focused
            // diagnostics after PAI-867 promotes the same home to `/`.
            path: "/dev/paimos-6",
            component: () => import("@/views/Paimos6PreviewView.vue"),
            meta: { shell: "v6" as const },
          },
          {
            path: "/dev/undo",
            component: () => import("@/components/undo/UndoDevReference.vue"),
          },
        ]
      : []),
    { path: "/:pathMatch(.*)*", redirect: "/" },
  ];
}

const router = createRouter({
  history: createWebHistory(routerHistoryBase()),
  routes: buildRoutes(import.meta.env.DEV),
});

router.beforeEach(async (to) => {
  if (to.name === "public-offer") return; // Independent of internal login/password-change state.
  const auth = useAuthStore();
  const undo = useUndoStore();
  if (!auth.checked) await auth.fetchMe();
  if (!to.meta.public && !auth.user) {
    return { path: "/login", query: { redirect: to.fullPath } };
  }
  // PAI-321: while must_change_password is set, the only authenticated
  // route the user can reach is /first-login. The backend gate is the
  // source of truth (it returns 403 with a marker on every other API),
  // but bouncing them in the router avoids a brief flash of the
  // requested page before the API call fails.
  if (mustChangePassword.value && auth.user && to.path !== "/first-login") {
    return "/first-login";
  }
  if (to.path === "/login" && auth.user) {
    const redirect = safePostLoginRedirect(to.query.redirect);
    return redirect || (auth.user.role === "external" ? "/portal" : "/");
  }
  if (auth.user?.role === "reviewer" && !to.meta.public &&
      !reviewerRouteAllowed(to.path, to.query)) {
    return { path: "/", query: { access: "reviewer" } };
  }
  // PAI-179: legacy /settings?tab=crm deep links redirect to the new
  // location under Integrations. Keep this redirect indefinitely —
  // bookmarks have a long tail.
  if (to.path === "/settings" && to.query.tab === "crm") {
    return { path: "/integrations", query: { tab: "crm" } };
  }
  if (to.path === "/settings" && to.query.tab === "ai") {
    return { path: "/integrations", query: { tab: "ai" } };
  }
  // Admin-only routes
  if (to.meta.adminOnly && !auth.isAdmin) return "/";
  // External users: redirect away from internal routes to portal
  if (
    auth.user?.role === "external" &&
    !to.meta.portal &&
    !to.meta.acceptance &&
    !to.meta.public &&
    to.path !== "/login"
  ) {
    return "/portal";
  }
  // Internal users accessing portal (admins can, members redirect home)
  if (auth.user && ["member", "reviewer"].includes(auth.user.role) && to.meta.portal) {
    return "/";
  }
  // Per-project view access. Routes opt in by setting meta.projectIdParam
  // to the URL parameter that carries the project ID. If the param is
  // missing or not numeric, fall through — the underlying handler will
  // produce the 404 instead.
  const pidParam = to.meta.projectIdParam;
  if (pidParam && auth.user) {
    const raw = to.params[pidParam];
    const pid = Array.isArray(raw) ? Number(raw[0]) : Number(raw);
    if (!Number.isNaN(pid) && pid > 0 && !auth.canView(pid)) {
      return "/";
    }
  }
  if (
    undo.conflict &&
    to.fullPath !==
      window.location.pathname + window.location.search + window.location.hash
  ) {
    return false;
  }
});

// Auto-reload on stale chunks after deploy — dynamic import fails when
// the server-side chunk files have changed but the browser still has
// the old router config referencing old hashed filenames.
router.onError((error, to) => {
  if (
    error.message.includes("Failed to fetch dynamically imported module") ||
    error.message.includes("Importing a module script failed") ||
    error.message.includes("error loading dynamically imported module")
  ) {
    window.location.href = publicURL(to.fullPath);
  }
});

export default router;
