// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createApp, nextTick } from "vue";
import { createPinia, setActivePinia } from "pinia";

// PAI-743: identifier-first login. Step 1 must carry no password field
// (that is the whole point — a password manager cannot autofill-and-
// submit past the SSO choice), and step 2 must show only the method(s)
// home realm discovery reports while keeping the username in the DOM
// for password-manager association.

const { apiGet, apiPost, mockRoute } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  mockRoute: { query: {} as Record<string, unknown> },
}));

vi.mock("vue-router", () => ({
  useRoute: () => mockRoute,
  useRouter: () => ({ push: vi.fn() }),
  RouterLink: { template: "<a><slot /></a>" },
}));

// The auth store imports the real router module, which builds a router
// (and registers guards) at import time. Stub the module itself.
vi.mock("@/router", () => ({
  default: { push: vi.fn(), replace: vi.fn(), currentRoute: { value: mockRoute } },
}));

vi.mock("@/api/client", async () => {
  const { ref } = await import("vue");
  const permissionsEpoch = ref<string | null>(null);
  const permissionsEpochGeneration = ref(0);
  const sessionExpired = ref(false);
  return {
    api: { get: apiGet, getWithMeta: vi.fn(), post: apiPost },
    ApiError: class ApiError extends Error {},
    // Consumed by the auth store at setup time.
    permissionsEpoch,
    permissionsEpochGeneration,
    sessionExpired,
    comparePermissionsEpoch: (left: string, right: string) => left.length === right.length
      ? (left === right ? 0 : left < right ? -1 : 1)
      : left.length < right.length ? -1 : 1,
    resetPermissionsEpoch: () => {
      permissionsEpoch.value = null;
      permissionsEpochGeneration.value += 1;
    },
    announceSessionRestored: vi.fn(() => {
      permissionsEpoch.value = null;
      permissionsEpochGeneration.value += 1;
      sessionExpired.value = false;
    }),
  };
});

vi.mock("@/composables/useBranding", () => ({
  useBranding: () => ({
    branding: { logo: "/logo.svg", company: "C", product: "P", tagline: "t" },
  }),
}));

vi.mock("@/composables/useSidebarColors", () => ({
  useSidebarColors: () => ({ bgColor: { value: "#fff" }, patternImage: { value: "none" } }),
}));

vi.mock("@/components/AppIcon.vue", () => ({
  default: { props: ["name"], template: "<span />" },
}));

vi.stubGlobal("__APP_VERSION__", "0.0.0-test");

import LoginView from "@/views/LoginView.vue";

async function mountLogin() {
  const el = document.createElement("div");
  document.body.appendChild(el);
  setActivePinia(createPinia());
  const app = createApp(LoginView);
  app.use(createPinia());
  app.mount(el);
  await nextTick();
  await nextTick();
  return {
    el,
    async unmount() {
      app.unmount();
      el.remove();
    },
  };
}

/** Drive step 1 with an identifier and wait for the routing reply. */
async function submitIdentifier(el: HTMLElement, identifier: string) {
  const input = el.querySelector<HTMLInputElement>("#username")!;
  input.value = identifier;
  input.dispatchEvent(new Event("input"));
  await nextTick();
  el.querySelector("form")!.dispatchEvent(new Event("submit"));
  await nextTick();
  await nextTick();
  await nextTick();
}

describe("LoginView identifier-first flow (PAI-743)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockRoute.query = {};
    window.__PAIMOS_PUBLIC_BASE_PATH__ = "";
    document.body.innerHTML = "";
    apiGet.mockResolvedValue({ enabled: true, label: "Sign in with SSO" });
  });

  it("step 1 asks only for the identifier — no password field to autofill", async () => {
    const m = await mountLogin();
    expect(m.el.querySelector("#username")).not.toBeNull();
    expect(m.el.querySelector("#password")).toBeNull();
    // The SSO button must not be reachable before we know the realm.
    expect(m.el.querySelector(".login-sso-btn")).toBeNull();
    await m.unmount();
  });

  it("prefixes login-card and footer logo URLs under a public mount", async () => {
    window.__PAIMOS_PUBLIC_BASE_PATH__ = "/paimos";
    const m = await mountLogin();
    const logos = [...m.el.querySelectorAll<HTMLImageElement>("img")].map(img => img.getAttribute("src"));
    expect(logos).toEqual(["/paimos/logo.svg", "/paimos/logo.svg"]);
    await m.unmount();
  });

  it("password realm: step 2 shows the password field and keeps the username in the DOM", async () => {
    apiPost.mockResolvedValue({ password: true, sso: true, sso_label: "Sign in with SSO" });
    const m = await mountLogin();
    await submitIdentifier(m.el, "mba");

    expect(apiPost).toHaveBeenCalledWith("/auth/login/methods", { identifier: "mba" });
    expect(m.el.querySelector("#password")).not.toBeNull();
    // Password managers need a rendered username input to associate the
    // credential — clipped, never removed.
    const hidden = m.el.querySelector<HTMLInputElement>("input.visually-hidden");
    expect(hidden).not.toBeNull();
    expect(hidden!.getAttribute("autocomplete")).toBe("username");
    expect(hidden!.value).toBe("mba");
    await m.unmount();
  });

  it("SSO realm: no password field, and the identifier rides along as login_hint", async () => {
    apiPost.mockResolvedValue({ password: false, sso: true, sso_label: "Sign in with SSO" });
    const m = await mountLogin();
    await submitIdentifier(m.el, "mba@agm.ng");

    expect(m.el.querySelector("#password")).toBeNull();
    const sso = m.el.querySelector<HTMLAnchorElement>(".login-sso-btn");
    expect(sso).not.toBeNull();
    expect(sso!.getAttribute("href")).toBe(
      "/api/auth/oidc/login?login_hint=mba%40agm.ng",
    );
    await m.unmount();
  });

  it("?method=password forces the password field on an SSO-routed realm (break-glass)", async () => {
    mockRoute.query = { method: "password" };
    apiPost.mockResolvedValue({ password: true, sso: true });
    const m = await mountLogin();
    await submitIdentifier(m.el, "admin@agm.ng");

    expect(m.el.querySelector("#password")).not.toBeNull();
    expect(apiPost).toHaveBeenCalledWith("/auth/login/methods", { identifier: "admin@agm.ng", method: "password" });
    await m.unmount();
  });

  it.each([{ method: "password" }, { method: ["password"] }])("?method=$method cannot override disabled password login", async ({ method }) => {
    mockRoute.query = { method };
    apiGet.mockResolvedValue({ enabled: true, password_disabled: true });
    apiPost.mockResolvedValue({ password: false, sso: true });
    const m = await mountLogin();
    expect(m.el.querySelector(".login-forgot-link")).toBeNull();
    await submitIdentifier(m.el, "admin@agm.ng");
    expect(m.el.querySelector("#password")).toBeNull();
    expect(m.el.querySelector("button[type=submit]")).toBeNull();
    expect(m.el.querySelector(".login-forgot-link")).toBeNull();
    expect(m.el.querySelector(".login-sso-btn")).not.toBeNull();
    apiPost.mockClear();
    m.el.querySelector("form")!.dispatchEvent(new Event("submit"));
    await nextTick();
    expect(apiPost).not.toHaveBeenCalled();
    await m.unmount();
  });

  it("routing failure cannot reopen password login when instance policy is disabled", async () => {
    mockRoute.query = { method: "password" };
    apiGet.mockResolvedValue({ enabled: true, password_disabled: true });
    apiPost.mockRejectedValue(new Error("network"));
    const m = await mountLogin();
    await submitIdentifier(m.el, "admin");
    expect(m.el.querySelector("#password")).toBeNull();
    expect(m.el.querySelector(".login-sso-btn")).not.toBeNull();
    await m.unmount();
  });

  it("a late instance-policy response closes a routing-failure fallback", async () => {
    let finishStatus!: (value: unknown) => void;
    apiGet.mockReturnValue(new Promise(resolve => { finishStatus = resolve; }));
    apiPost.mockRejectedValue(new Error("network"));
    const m = await mountLogin();
    await submitIdentifier(m.el, "admin");
    finishStatus({ enabled: true, password_disabled: true });
    await nextTick();
    await nextTick();
    expect(m.el.querySelector("#password")).toBeNull();
    expect(m.el.querySelector(".login-forgot-link")).toBeNull();
    expect(m.el.querySelector(".login-sso-btn")).not.toBeNull();
    await m.unmount();
  });

  it("routing failure falls back to both methods instead of stranding the user", async () => {
    apiPost.mockRejectedValue(new Error("network"));
    const m = await mountLogin();
    await submitIdentifier(m.el, "mba");

    expect(m.el.querySelector("#password")).not.toBeNull();
    expect(m.el.querySelector(".login-sso-btn")).not.toBeNull();
    await m.unmount();
  });

  it("Change returns to step 1 and drops any typed password", async () => {
    apiPost.mockResolvedValue({ password: true, sso: false });
    const m = await mountLogin();
    await submitIdentifier(m.el, "mba");

    const pw = m.el.querySelector<HTMLInputElement>("#password")!;
    pw.value = "secret-typed-by-user";
    pw.dispatchEvent(new Event("input"));
    await nextTick();

    m.el.querySelector<HTMLButtonElement>(".login-identity")!.click();
    await nextTick();

    expect(m.el.querySelector("#password")).toBeNull();
    expect(m.el.querySelector<HTMLInputElement>("#username")!.value).toBe("mba");

    // Returning to step 2 must not resurrect the old password value.
    await submitIdentifier(m.el, "mba");
    expect(m.el.querySelector<HTMLInputElement>("#password")!.value).toBe("");
    await m.unmount();
  });
});
