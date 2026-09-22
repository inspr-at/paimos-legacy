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

import { clearHabitatIntentRecovery } from '@/constants/storage'
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import {
  ApiError,
  api,
  announceSessionRestored,
  comparePermissionsEpoch,
  permissionsEpoch,
  permissionsEpochGeneration,
  resetPermissionsEpoch,
  sessionExpired,
} from '@/api/client'
import router from '@/router'
import i18n from '@/i18n'
import { setDisplayTimezone } from '@/utils/formatTime'
import { setDisplayLocale } from '@/utils/displayLocale'
import { useSearchStore } from '@/stores/search'

export interface User {
  id: number
  username: string
  role: 'admin' | 'member' | 'reviewer' | 'external' | 'super_admin'
  status: 'active' | 'inactive' | 'deleted'
  created_at: string
  // Profile fields (migration 25)
  nickname:    string
  first_name:  string
  last_name:   string
  email:       string
  avatar_path: string  // relative path e.g. /avatars/1.jpg — served by Go
  // Editor preferences (migration 29)
  markdown_default: boolean
  monospace_fields: boolean
  // Recent projects limit (migration 38)
  recent_projects_limit: number
  // Internal hourly rate (migration 39)
  internal_rate_hourly: number | null
  // Alt-unit display preferences (migration 44)
  show_alt_unit_table: boolean
  show_alt_unit_detail: boolean
  // Locale (migration 47)
  locale: string
  // Recent timers limit (migration 49)
  recent_timers_limit: number
  // Display timezone (migration 50) — 'auto' = browser local
  timezone: string
  // Preview hover delay in ms (migration 53)
  preview_hover_delay: number
  // Issue list auto-refresh preferences (migration 88)
  issue_auto_refresh_enabled: boolean
  issue_auto_refresh_interval_seconds: number
  // PAI-368 / M103: search-scope shortcut (JSON or '' = disabled).
  // See useSearchScopeShortcut for parse + matcher.
  search_scope_shortcut: string
  // PAI-706 / M135: voice-intake auto-switch threshold override (null = instance default)
  intake_confidence_threshold?: number | null
  // Last login timestamp (migration 54)
  last_login_at: string | null
  // Accruals report preferences (migration 62) — admin-only feature
  accruals_stats_enabled: boolean
  accruals_extra_statuses: string
  // PAI-336: compatibility flag. The canonical public role is now
  // `super_admin`; this remains for older clients and cautious UI gates.
  is_super_admin: boolean
}

// AccessLevel mirrors backend auth.AccessLevel.
export type AccessLevel = 'viewer' | 'editor'

// AccessResponse is the `access` field on the login / totp / me responses.
// AllProjects=true is the admin shortcut; otherwise `levels` lists every
// project the user has at least viewer access on.
export interface AccessResponse {
  all_projects: boolean
  levels: Record<string, AccessLevel>
}

// MeResponse is the envelope returned by /auth/login, /auth/totp/verify,
// and /auth/me. Parsed once to hydrate the access Map.
//
// PAI-267: via_dev_login is true iff the current request authenticated
// via the dev-login route. Only set in development builds with the
// dev_login backend tag — production /auth/me always omits it.
export interface MeResponse {
  user: User
  access: AccessResponse
  via_dev_login?: boolean
  suppress_security_nags?: boolean
  impersonation?: ImpersonationState | null
}

export interface ImpersonationState {
  active: boolean
  actor: User
  target: User
}

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const checked = ref(false)
  const totpEnabled = ref(false)
  const totpChecked = ref(false)
  // PAI-742: true when the current session was minted by the OIDC
  // callback — the local-2FA nag stays quiet (MFA is the IdP's policy).
  const ssoSession = ref(false)

  // accessibleProjects maps project ID (number) → access level. Admins
  // get an empty map and `allProjects=true` — the helpers below handle
  // both branches.
  const accessibleProjects = ref<Map<number, AccessLevel>>(new Map())
  const allProjects = ref(false)

  // PAI-267: true iff the current session was created via the
  // dev-login route. Drives the non-dismissable AppDevLoginBanner in
  // AppLayout. Always false in production frontend builds talking to a
  // production backend — the backend simply never sets the field.
  const viaDevLogin = ref(false)
  const suppressSecurityNags = ref(false)
  const impersonation = ref<ImpersonationState | null>(null)
  let lastSyncedEpoch: string | null = null
  let lastSyncedEpochGeneration: number | null = null
  let authorityExpected = false
  let suppressGenerationRefresh = false
  let observedPermissionsEpochGeneration = permissionsEpochGeneration.value
  let queuedAuthorityTarget: { generation: number; epoch: string | null } | null = null
  let currentAuthorityTarget: { generation: number; epoch: string | null } | null = null
  let authorityHydrationController: AbortController | null = null
  let authorityHydrationPromise: Promise<void> | null = null
  let authorityHydrationRunGeneration = 0
  let queuedAuthorityForce = false
  let authorityRetryTimer: ReturnType<typeof setTimeout> | null = null
  let authorityRetryAttempt = 0

  const AUTHORITY_HYDRATION_TIMEOUT_MS = 30_000

  function hydrateSession(
    resp: MeResponse,
    responseEpoch: string | null = null,
    responseEpochGeneration: number | null = null,
  ) {
    user.value = resp.user
    hydrateAccess(resp.access)
    viaDevLogin.value = !!resp.via_dev_login
    suppressSecurityNags.value = !!resp.suppress_security_nags
    impersonation.value = resp.impersonation?.active ? resp.impersonation : null
    // Header observation precedes body hydration. Binding the baseline here
    // means the first real post-hydration bump cannot be mistaken for the
    // pristine session's first observation.
    authorityExpected = true
    if (responseEpoch != null && responseEpochGeneration != null) {
      lastSyncedEpoch = responseEpoch
      lastSyncedEpochGeneration = responseEpochGeneration
    }
    setDisplayLocale(user.value?.locale)
    if (user.value?.locale) i18n.global.locale.value = user.value.locale as 'en' | 'de'
    setDisplayTimezone(user.value?.timezone)
  }

  async function completeLogin(_resp: MeResponse) {
    // PAI-322: broadcast session-restored so sibling tabs dismiss their
    // session-expired modals. The login envelope itself is intentionally not
    // installed as authorization: only a response-local current /auth/me proof
    // may expose the new principal's role and project map.
    authorityExpected = true
    announceSessionRestored()
    checked.value = false
    try {
      await queueAuthorityHydration({
        generation: permissionsEpochGeneration.value,
        epoch: permissionsEpoch.value,
      }, true)
      if (!user.value) throw new Error('login authority could not be verified')
    } finally {
      checked.value = true
    }
  }

  function hydrateAccess(access: AccessResponse | undefined | null) {
    const m = new Map<number, AccessLevel>()
    allProjects.value = !!access?.all_projects
    if (access?.levels) {
      for (const [k, v] of Object.entries(access.levels)) {
        const id = Number(k)
        if (!Number.isNaN(id)) m.set(id, v)
      }
    }
    accessibleProjects.value = m
  }

  function canView(projectId: number | null | undefined): boolean {
    if (!user.value) return false
    if (user.value.role === 'reviewer' && projectId == null) return false
    if (projectId == null) return true // orphan / no project — show
    if (allProjects.value) return true
    return accessibleProjects.value.has(projectId)
  }

  function canEdit(projectId: number | null | undefined): boolean {
    if (!user.value) return false
    if (user.value.role === 'reviewer') return false
    if (projectId == null) return true
    if (allProjects.value) return true
    return accessibleProjects.value.get(projectId) === 'editor'
  }

  const canEditAnyProject = computed(() =>
    allProjects.value || [...accessibleProjects.value.values()].some((level) => level === 'editor'),
  )
  const isAdmin = computed(() => user.value?.role === 'admin' || user.value?.role === 'super_admin')
  const isSuperAdmin = computed(() => user.value?.role === 'super_admin' || !!user.value?.is_super_admin)

  function clearPrincipalProjection() {
    clearHabitatIntentRecovery()
    user.value = null
    allProjects.value = false
    accessibleProjects.value = new Map()
    viaDevLogin.value = false
    suppressSecurityNags.value = false
    impersonation.value = null
    totpEnabled.value = false
    totpChecked.value = false
    ssoSession.value = false
    setDisplayLocale(undefined)
    setDisplayTimezone(undefined)
  }

  function targetKey(target: { generation: number; epoch: string | null }): string {
    return `${target.generation}:${target.epoch ?? 'baseline'}`
  }

  function targetIsHydrated(target: { generation: number; epoch: string | null }): boolean {
    if (lastSyncedEpochGeneration !== target.generation || lastSyncedEpoch == null) return false
    return target.epoch == null || lastSyncedEpoch === target.epoch
  }

  async function hydrateMeWithAuthority(signal?: AbortSignal): Promise<void> {
    const response = await api.getWithMeta<MeResponse>('/auth/me', { signal })
    if (
      response.permissionsEpoch == null
      || response.permissionsEpochGeneration !== permissionsEpochGeneration.value
      || response.permissionsEpoch !== permissionsEpoch.value
    ) throw new Error('authentication authority changed during hydration')
    // Check and commit share one synchronous continuation: no unrelated
    // response can advance ambient authority between them.
    hydrateSession(response.data, response.permissionsEpoch, response.permissionsEpochGeneration)
  }

  function invalidateAuthorityHydration() {
    authorityHydrationRunGeneration += 1
    authorityHydrationController?.abort()
    authorityHydrationController = null
    currentAuthorityTarget = null
    queuedAuthorityTarget = null
    queuedAuthorityForce = false
    if (authorityRetryTimer !== null) clearTimeout(authorityRetryTimer)
    authorityRetryTimer = null
    authorityRetryAttempt = 0
    // Detach from abort-ignoring work owned by an older auth identity. Its
    // response-local generation check prevents commit if it ever resolves.
    authorityHydrationPromise = null
  }

  function scheduleAuthorityRetry() {
    if (
      authorityRetryTimer !== null
      || !authorityExpected
      || sessionExpired.value
      || !queuedAuthorityTarget
    ) return
    const wait = Math.min(30_000, 2_000 * 2 ** authorityRetryAttempt)
    authorityRetryAttempt += 1
    authorityRetryTimer = setTimeout(() => {
      authorityRetryTimer = null
      const target = queuedAuthorityTarget
      if (target) void queueAuthorityHydration(target)
    }, wait)
  }

  function queueAuthorityHydration(
    target: { generation: number; epoch: string | null },
    force = false,
  ): Promise<void> {
    if (target.generation !== permissionsEpochGeneration.value) return Promise.resolve()
    if (targetIsHydrated(target) && !force) return authorityHydrationPromise ?? Promise.resolve()
    if (!queuedAuthorityTarget || targetKey(queuedAuthorityTarget) !== targetKey(target)) {
      authorityRetryAttempt = 0
    }
    queuedAuthorityTarget = target
    queuedAuthorityForce = queuedAuthorityForce || force
    if (authorityRetryTimer !== null) clearTimeout(authorityRetryTimer)
    authorityRetryTimer = null

    if (currentAuthorityTarget) {
      const generationChanged = currentAuthorityTarget.generation !== target.generation
      const newerEpoch = currentAuthorityTarget.epoch != null
        && target.epoch != null
        && comparePermissionsEpoch(target.epoch, currentAuthorityTarget.epoch) > 0
      if (generationChanged || newerEpoch) authorityHydrationController?.abort()
    }
    if (authorityHydrationPromise) return authorityHydrationPromise

    const runGeneration = authorityHydrationRunGeneration
    // Start after the promise sentinel is assigned. A fast response publishes
    // its epoch through a flush-sync watcher before body commit; without this
    // microtask boundary that watcher could re-enter and launch a second run.
    const promise = Promise.resolve().then(async () => {
      let lastAttemptedKey = ''
      let attemptsForTarget = 0
      while (runGeneration === authorityHydrationRunGeneration && queuedAuthorityTarget) {
        const next = queuedAuthorityTarget
        if (next.generation !== permissionsEpochGeneration.value) {
          queuedAuthorityTarget = null
          return
        }
        const forceCurrentTarget = queuedAuthorityForce
        queuedAuthorityForce = false
        if (targetIsHydrated(next) && !forceCurrentTarget) {
          queuedAuthorityTarget = null
          return
        }

        const key = targetKey(next)
        if (key !== lastAttemptedKey) {
          lastAttemptedKey = key
          attemptsForTarget = 0
        }
        attemptsForTarget += 1
        currentAuthorityTarget = next
        const controller = new AbortController()
        authorityHydrationController = controller
        const timeout = setTimeout(() => controller.abort(), AUTHORITY_HYDRATION_TIMEOUT_MS)
        let terminalUnauthenticated = false
        try {
          await hydrateMeWithAuthority(controller.signal)
        } catch (cause) {
          terminalUnauthenticated = cause instanceof ApiError && cause.status === 401
          // Keep authorization invalid. A newer queued target is drained below;
          // the current target gets one bounded retry and never a hot loop.
        } finally {
          clearTimeout(timeout)
          if (authorityHydrationController === controller) authorityHydrationController = null
          if (currentAuthorityTarget === next) currentAuthorityTarget = null
        }

        if (runGeneration !== authorityHydrationRunGeneration) return
        if (terminalUnauthenticated) {
          authorityExpected = false
          queuedAuthorityTarget = null
          clearPrincipalProjection()
          return
        }

        if (targetIsHydrated(next)) {
          authorityRetryAttempt = 0
          if (queuedAuthorityTarget && targetKey(queuedAuthorityTarget) === key) {
            queuedAuthorityTarget = null
          }
          continue
        }
        const latest = queuedAuthorityTarget
        if (latest && targetKey(latest) !== key) continue
        if (attemptsForTarget < 2) continue
        scheduleAuthorityRetry()
        return
      }
    }).finally(() => {
      if (runGeneration !== authorityHydrationRunGeneration || authorityHydrationPromise !== promise) return
      authorityHydrationPromise = null
      currentAuthorityTarget = null
      authorityHydrationController = null
    })
    authorityHydrationPromise = promise
    return promise
  }

  async function fetchMe() {
    try {
      authorityExpected = true
      await queueAuthorityHydration({
        generation: permissionsEpochGeneration.value,
        epoch: permissionsEpoch.value,
      }, true)
      if (!user.value) throw new Error('authentication authority is unavailable')
      await fetchTOTPStatus()
    } catch {
      authorityExpected = false
      invalidateAuthorityHydration()
      clearPrincipalProjection()
    } finally {
      checked.value = true
    }
  }

  async function login(username: string, password: string) {
    const resp = await api.post<MeResponse>('/auth/login', { username, password })
    await completeLogin(resp)
    await fetchTOTPStatus()
  }

  // PAI-83: installing a fresh authenticated user proves the session is
  // valid again; clear the banner flag here so both password and TOTP
  // login paths (which converge on setUser) recover from a stale 401.
  // PAI-322: also broadcast across tabs.
  async function setUser(_u: User) {
    authorityExpected = true
    announceSessionRestored()
    checked.value = false
    try {
      await queueAuthorityHydration({
        generation: permissionsEpochGeneration.value,
        epoch: permissionsEpoch.value,
      }, true)
    } finally {
      checked.value = true
    }
  }

  async function fetchTOTPStatus(force = false) {
    if (!user.value) {
      totpEnabled.value = false
      totpChecked.value = false
      ssoSession.value = false
      return false
    }
    if (totpChecked.value && !force) return totpEnabled.value
    try {
      const status = await api.get<{ enabled: boolean; sso_session?: boolean }>(
        '/auth/totp/status',
      )
      totpEnabled.value = status.enabled
      ssoSession.value = status.sso_session === true
      totpChecked.value = true
    } catch {
      totpEnabled.value = false
      totpChecked.value = false
      ssoSession.value = false
    }
    return totpEnabled.value
  }

  function setTOTPEnabled(enabled: boolean) {
    totpEnabled.value = enabled
    totpChecked.value = true
  }

  async function logout() {
    try { await api.post('/auth/logout', {}) } catch { /* ignore */ }
    authorityExpected = false
    clearPrincipalProjection()
    checked.value = true
    resetEpochBaseline() // PAI-320
    suppressGenerationRefresh = true
    resetPermissionsEpoch()
    suppressGenerationRefresh = false
    // PAI-242: search query persists across users via localStorage; reset
    // it on logout so the next login doesn't pre-fill the prior session's
    // sidebar search input.
    useSearchStore().clear()
    router.push('/login')
  }

  // Re-fetch /auth/me and update user + access state — used after
  // profile/avatar changes and after permission edits that should apply
  // immediately without a page reload.
  async function refreshMe() {
    try {
      await queueAuthorityHydration({
        generation: permissionsEpochGeneration.value,
        epoch: permissionsEpoch.value,
      }, true)
    } catch { /* ignore */ }
  }

  async function startImpersonation(userId: number) {
    await api.post('/auth/impersonation/start', { user_id: userId })
    resetEpochBaseline()
    suppressGenerationRefresh = true
    resetPermissionsEpoch()
    suppressGenerationRefresh = false
    clearPrincipalProjection()
    authorityExpected = true
    await queueAuthorityHydration({
      generation: permissionsEpochGeneration.value,
      epoch: permissionsEpoch.value,
    }, true)
    if (!user.value) throw new Error('impersonation authority could not be verified')
    await fetchTOTPStatus(true)
  }

  async function stopImpersonation() {
    await api.post('/auth/impersonation/end', {})
    resetEpochBaseline()
    suppressGenerationRefresh = true
    resetPermissionsEpoch()
    suppressGenerationRefresh = false
    clearPrincipalProjection()
    authorityExpected = true
    await queueAuthorityHydration({
      generation: permissionsEpochGeneration.value,
      epoch: permissionsEpoch.value,
    }, true)
    if (!user.value) throw new Error('impersonation authority could not be verified')
    await fetchTOTPStatus(true)
  }

  // PAI-320: server-pushed permissions invalidation. The middleware
  // emits X-Permissions-Epoch on every authed response, captured into
  // the module-level `permissionsEpoch` ref by client.ts. We track the
  // last epoch we hydrated from /auth/me; when the next observed value
  // differs (admin promoted/demoted/membership change), refetch /auth/me
  // and re-hydrate access. Soft refresh, no re-login.
  //
  // We compare against `lastSyncedEpoch` rather than the previous
  // ref value because the very first observation just sets the
  // baseline — there's no "previous" hydration to invalidate yet.
  watch(permissionsEpoch, (n) => {
    if (n == null) return // sentinel: not yet observed
    if (!authorityExpected && !user.value) return
    if (
      lastSyncedEpochGeneration === permissionsEpochGeneration.value
      && lastSyncedEpoch === n
    ) return
    // A strict same-principal bump may revoke the old role, status, or project
    // map. Remove every principal-bound gate before awaiting /auth/me.
    authorityExpected = true
    clearPrincipalProjection()
    void queueAuthorityHydration({ generation: permissionsEpochGeneration.value, epoch: n })
  }, { flush: 'sync' })

  watch(permissionsEpochGeneration, (generation) => {
    if (generation === observedPermissionsEpochGeneration) return
    observedPermissionsEpochGeneration = generation
    // A restored sibling tab may have a valid new shared cookie even when this
    // tab was already scrubbed to user=null. Explicit logout/impersonation own
    // their reset with suppression; every other live-session generation must
    // prove the current principal through /auth/me.
    const shouldRehydrate = !suppressGenerationRefresh
    invalidateAuthorityHydration()
    lastSyncedEpoch = null
    lastSyncedEpochGeneration = null
    clearPrincipalProjection()
    if (!shouldRehydrate) return
    void Promise.resolve().then(() => {
      if (
        generation !== permissionsEpochGeneration.value
        || sessionExpired.value
        || suppressGenerationRefresh
      ) return
      void queueAuthorityHydration({ generation, epoch: permissionsEpoch.value })
    })
  }, { flush: 'sync' })

  // Reset the response-bound baseline and detach all older auth work.
  function resetEpochBaseline() {
    lastSyncedEpoch = null
    lastSyncedEpochGeneration = null
    invalidateAuthorityHydration()
  }

  onScopeDispose(() => invalidateAuthorityHydration())

  return {
    user,
    checked,
    totpEnabled,
    totpChecked,
    ssoSession,
    accessibleProjects,
    allProjects,
    viaDevLogin,
    suppressSecurityNags,
    impersonation,
    fetchMe,
    login,
    completeLogin,
    setUser,
    hydrateAccess,
    canView,
    canEdit,
    canEditAnyProject,
    isAdmin,
    isSuperAdmin,
    fetchTOTPStatus,
    setTOTPEnabled,
    logout,
    refreshMe,
    startImpersonation,
    stopImpersonation,
  }
})
