<script setup lang="ts">
import { OFFER_CHROME_KEY } from '@/composables/useOfferChrome'
import { LS_HABITAT_THEME } from '@/constants/storage'
import {
  Building2,
  Command,
  LogOut,
  Mic,
  Moon,
  Sun,
  Home,
  Users,
  LayoutGrid,
  Inbox,
  Plus,
  Settings,
  Search,
} from 'lucide-vue-next'
import { computed, nextTick, onScopeDispose, provide, ref, shallowRef } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import { publicURL } from '@/publicPath'
import Paimos6CommandPalette, {
  type Paimos6PaletteActivation,
} from '@/components/v6/Paimos6CommandPalette.vue'
import { usePaimos6CommandPalette } from '@/composables/v6/usePaimos6CommandPalette'
import BrandLogo from '@/components/BrandLogo.vue'
import FlowHost from '@/components/flow/FlowHost.vue'
import AppDevLoginBanner from '@/components/AppDevLoginBanner.vue'
import AppImpersonationBanner from '@/components/AppImpersonationBanner.vue'
import SessionExpiredModal from '@/components/SessionExpiredModal.vue'
import { useBranding } from '@/composables/useBranding'
import { useTotpNag } from '@/composables/useTotpNag'
import { crmEnabled, instanceLabel, instanceHostname, loadInstance } from '@/api/instance'
import '@/components/habitat/habitat.css'
import { useAuthStore } from '@/stores/auth'
import { commandShortcutLabel } from '@/v6/commandPalette'
import { PAIMOS6_COMMAND_CONTEXT_KEY, type Paimos6CommandContext } from '@/v6/commandPaletteContext'

const offerChromeCollapsed = ref(true)
provide(OFFER_CHROME_KEY, offerChromeCollapsed)
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const compactOffer = computed(
  () => /^\/crm\/offers\/[^/]+$/.test(route.path) && offerChromeCollapsed.value,
)
const commandButton = shallowRef<HTMLElement | null>(null)
const commandContext = shallowRef<Paimos6CommandContext | null>(null)
const principalId = computed(() => auth.user?.id ?? null)
const projectId = computed(() => {
  const raw = route.query.project
  if (typeof raw !== 'string' || !/^[1-9]\d*$/.test(raw)) return null
  const parsed = Number(raw)
  return Number.isSafeInteger(parsed) ? parsed : null
})
const flowHostActive = ref(false)
const authorityKey = computed(() =>
  JSON.stringify([
    globalThis.location?.origin ?? 'unknown-origin',
    permissionsEpochGeneration.value,
    permissionsEpoch.value,
    auth.user?.id ?? null,
    auth.user?.role ?? null,
    auth.user?.status ?? null,
    auth.allProjects,
    [...auth.accessibleProjects.entries()].sort(([left], [right]) => left - right),
  ]),
)
const routeKey = computed(() => route.fullPath)
const palette = usePaimos6CommandPalette({ principalId, authorityKey, projectId, routeKey })
const selectedSessionId = computed(() => commandContext.value?.selectedSessionId.value ?? null)
const shortcutLabel = computed(() =>
  palette.effectiveShortcut.value
    ? commandShortcutLabel(palette.effectiveShortcut.value)
    : palette.settingsState.value === 'loading'
      ? 'Loading…'
      : 'Unavailable',
)

provide(PAIMOS6_COMMAND_CONTEXT_KEY, (context) => {
  commandContext.value = context
})

async function activate(item: Paimos6PaletteActivation) {
  if (item.kind === 'node') {
    palette.announcement.value = 'Node detail is not available in the 6.0 web preview.'
    return
  }
  if (item.kind === 'session') {
    const currentProjectId = projectId.value
    if (currentProjectId === null) return
    palette.close()
    await router
      .replace({
        query: {
          ...route.query,
          view: 'sessions',
          project: String(currentProjectId),
          session: item.row.product_session_id,
        },
      })
      .catch(() => {})
    return
  }
  if (item.kind === 'knowledge') {
    const currentProjectId = projectId.value
    if (currentProjectId === null) return
    palette.close()
    await router
      .push({
        path: `/projects/${currentProjectId}`,
        query: { tab: 'knowledge', memory: item.row.type === 'memory' ? item.row.slug : undefined },
      })
      .catch(() => {})
    return
  }
  if (item.action === 'open_talk') {
    palette.close()
    await nextTick()
    commandContext.value?.openTalk()
  } else if (item.action === 'clear_session' && selectedSessionId.value) {
    commandContext.value?.clearSession()
    palette.close()
  } else if (item.action === 'open_settings') {
    palette.close()
    await router.push('/settings?tab=account').catch(() => {})
  } else if (item.action === 'open_crm') {
    palette.close()
    await router.push('/crm').catch(() => {})
  } else if (item.action === 'return_5x') {
    palette.close()
    await router.push('/legacy').catch(() => {})
  }
}

const { branding, brandName } = useBranding()
const { show2FAWarning } = useTotpNag()
void loadInstance()
const displayName = computed(
  () => auth.user?.nickname || auth.user?.first_name || auth.user?.username || 'Account',
)
const initials = computed(() =>
  displayName.value
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part[0])
    .join('')
    .toUpperCase(),
)
const navigation = [
  { view: 'home', label: 'Home', icon: Home },
  { view: 'workers', label: 'Workers', icon: Users },
  { view: 'projects', label: 'Projects', icon: LayoutGrid },
  { view: 'attention', label: 'Needs you', icon: Inbox },
]
const view = computed(() =>
  typeof route.query.session === 'string' ? 'sessions' : route.query.view || 'home',
)
// PAI-980: the CRM door lives on its own path, not on a home `view`.
const onCrm = computed(() => {
  const path = typeof route.path === 'string' ? route.path : ''
  return path === '/crm' || path.startsWith('/crm/')
})
const locationLabel = computed(() => {
  if (onCrm.value) return 'Customers'
  return (
    navigation.find((item) => item.view === view.value)?.label ||
    (view.value === 'assign' ? 'Start work' : 'Product sessions')
  )
})
const theme = ref<'day' | 'night'>(initialTheme())
function initialTheme(): 'day' | 'night' {
  try {
    const saved = localStorage.getItem(LS_HABITAT_THEME)
    if (saved === 'day' || saved === 'night') return saved
  } catch {
    /* Storage may be disabled. */
  }
  return globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'night' : 'day'
}
function toggleTheme() {
  theme.value = theme.value === 'day' ? 'night' : 'day'
  try {
    localStorage.setItem(LS_HABITAT_THEME, theme.value)
  } catch {
    /* Memory-only theme remains usable. */
  }
}
function openTalk() {
  if (auth.user?.role === 'reviewer') return
  if (commandContext.value) commandContext.value.openTalk()
  else void router.replace({ query: { ...route.query, view: 'sessions' } })
}
onScopeDispose(() => {
  commandContext.value = null
})
</script>

<template>
  <div class="p6-shell habitat-shell" data-shell="v6" :data-theme="theme">
    <AppDevLoginBanner />
    <AppImpersonationBanner />
    <SessionExpiredModal />
    <a class="habitat-skip" href="#habitat-main">Skip to content</a>
    <div class="habitat-app-frame">
      <aside class="habitat-rail" aria-label="Workspace navigation">
        <a class="habitat-brand" :href="publicURL('/')" :aria-label="brandName + ' home'">
          <BrandLogo :src="branding.logo" :alt="brandName" />
          <span
            ><strong>{{ brandName }}</strong
            ><small>Agent Intercom</small></span
          >
        </a>
        <div class="habitat-workspace-identity">
          <span class="habitat-workspace-symbol">{{ brandName.charAt(0) }}</span>
          <span
            ><strong>{{ instanceLabel || 'Your workspace' }}</strong
            ><small>{{ instanceHostname || 'Instance identity unavailable' }}</small></span
          >
        </div>
        <nav class="habitat-nav" aria-label="Control room">
          <RouterLink
            v-for="item in navigation"
            :key="item.view"
            :to="{ path: '/', query: { ...route.query, view: item.view, session: undefined } }"
            :aria-current="view === item.view && !onCrm ? 'page' : undefined"
            :aria-label="item.label"
            ><component :is="item.icon" :size="18" aria-hidden="true" /><span>{{
              item.label
            }}</span></RouterLink
          >
          <RouterLink
            v-if="crmEnabled"
            to="/crm"
            :aria-current="onCrm ? 'page' : undefined"
            aria-label="Customers"
            ><Building2 :size="18" aria-hidden="true" /><span>Customers</span></RouterLink
          >
        </nav>
        <div class="habitat-rail-bottom">
          <RouterLink
            class="habitat-rail-start"
            v-if="auth.user?.role !== 'reviewer'"
            aria-label="Start work"
            :to="{ path: '/', query: { ...route.query, view: 'assign', session: undefined } }"
            ><Plus :size="18" aria-hidden="true" /><span>Start work</span></RouterLink
          >
          <RouterLink v-if="auth.user?.role !== 'reviewer'" class="habitat-rail-settings" to="/settings" aria-label="Settings"
            ><Settings :size="17" aria-hidden="true" /><span>Settings</span></RouterLink
          >
          <RouterLink
            v-if="auth.user?.role !== 'reviewer'"
            class="habitat-rail-account"
            to="/settings?tab=account"
            :aria-label="`Signed in as ${displayName}. Open settings`"
            ><span class="habitat-avatar">{{ initials }}</span
            ><span
              ><strong>{{ displayName }}</strong
              ><small>Authenticated account</small></span
            ></RouterLink
          >
        </div>
      </aside>
      <div class="habitat-app-content">
        <FlowHost :project-id="projectId" @active="flowHostActive = $event">
          <template #toolbar>
            <header
              v-show="!compactOffer"
              class="habitat-header"
              :class="{ 'habitat-header--flow-toolbar': flowHostActive }"
            >
              <div class="habitat-location">
                <span>Workspace</span><span>/</span><strong>{{ locationLabel }}</strong>
              </div>
              <div class="habitat-header-tools">
                <button v-if="auth.user?.role !== 'reviewer'" type="button" class="habitat-voice" @click="openTalk">
                  <Mic :size="16" aria-hidden="true" /><span>Voice</span>
                </button>
                <button
                  ref="commandButton"
                  type="button"
                  class="p6-command-mount habitat-search"
                  :aria-label="`Open command palette (${shortcutLabel})`"
                  @click="palette.show"
                >
                  <Search :size="15" aria-hidden="true" /><span>Search anything</span
                  ><kbd><Command :size="11" aria-hidden="true" />{{ shortcutLabel }}</kbd>
                </button>
                <button
                  type="button"
                  :aria-label="theme === 'day' ? 'Switch to dark mode' : 'Switch to bright mode'"
                  @click="toggleTheme"
                >
                  <Moon v-if="theme === 'day'" :size="16" aria-hidden="true" /><Sun
                    v-else
                    :size="16"
                    aria-hidden="true"
                  />
                </button>
                <RouterLink
                  v-if="auth.user?.role !== 'reviewer'"
                  class="habitat-account habitat-mobile-account"
                  to="/settings?tab=account"
                  :aria-label="`Signed in as ${displayName}. Open settings`"
                  >{{ displayName }}</RouterLink
                >
                <button type="button" aria-label="Log out" @click="auth.logout()">
                  <LogOut :size="16" aria-hidden="true" />
                </button>
              </div>
            </header>
            <div v-show="!compactOffer" class="habitat-source">
              <span>{{ instanceHostname || 'Instance identity unavailable' }}</span
              ><span>Authenticated browser session</span>
            </div>
            <div v-if="show2FAWarning" class="habitat-security" role="alert">
              Protect your account with two-factor authentication.
              <RouterLink to="/settings?tab=account#two-factor-authentication"
                >Set up two-factor authentication</RouterLink
              >
            </div>
          </template>
          <div class="p6-shell-content">
            <p v-if="auth.user?.role === 'reviewer'" role="status" class="reviewer-notice">
              {{ displayName }} · Reviewer · Read-only access to shared projects. Administration, customer records and execution are unavailable.
              <span v-if="route.query.access === 'reviewer'">The requested page is outside your shared-project access.</span>
            </p>
            <slot />
          </div>
          <footer class="habitat-footer">
            <span>Agent Intercom · {{ brandName }}</span>
            <RouterLink v-if="auth.user?.role !== 'reviewer'" :to="{ path: '/', query: { ...route.query, view: 'sessions' } }"
              >Product sessions</RouterLink
            >
            <RouterLink v-if="auth.user?.role !== 'reviewer'" to="/legacy">Classic workspace</RouterLink>
            <RouterLink v-if="auth.user?.role !== 'reviewer'" to="/settings">Settings</RouterLink>
          </footer>
        </FlowHost>
      </div>
    </div>
    <Paimos6CommandPalette
      :open="palette.open.value"
      :query="palette.query.value"
      :search="palette.search.value"
      :search-state="palette.searchState.value"
      :settings-state="palette.settingsState.value"
      :shortcut-label="shortcutLabel"
      :shortcut-source="palette.settings.value?.source ?? null"
      :selected-session-id="selectedSessionId"
      :crm-enabled="crmEnabled"
      :read-only="auth.user?.role === 'reviewer'"
      :announcement="palette.announcement.value"
      :return-focus="commandButton"
      @update:query="palette.query.value = $event"
      @close="palette.close"
      @activate="activate"
    />
  </div>
</template>

<style scoped>
.reviewer-notice {
  margin: 0 0 1rem;
  padding: 0.75rem 1rem;
  border-radius: 10px;
  color: var(--h-muted);
  background: var(--h-surface);
  font-size: 0.875rem;
}
</style>
