<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useBranding } from '@/composables/useBranding'
import AppIcon from '@/components/AppIcon.vue'
import AppChangelogModal from '@/components/AppChangelogModal.vue'
import CalendarVersion from '@/components/CalendarVersion.vue'
import { userInitials } from '@/utils/userDisplay'
import { publicURL } from '@/publicPath'

defineProps<{
  isExpanded: boolean
  isAdmin: boolean
  completeFailures: number
}>()

const route = useRoute()
const auth = useAuthStore()
const { branding } = useBranding()
const rawVersion = __APP_VERSION__
const gitHash   = __GIT_HASH__
const showChangelog = ref(false)

function isActive(path: string) {
  return route.path.startsWith(path)
}
</script>

<template>
  <RouterLink v-if="auth.user?.role !== 'reviewer'" to="/settings" :class="['nav-item', { active: isActive('/settings') }]" :title="isExpanded ? '' : 'Settings'">
    <AppIcon name="settings" /><span class="sl">Settings</span>
    <span v-if="isAdmin && completeFailures > 0" class="dev-badge">{{ completeFailures }}</span>
  </RouterLink>

  <div class="user-row">
    <RouterLink v-if="auth.user?.role !== 'reviewer'" to="/settings?tab=account" class="user-profile-link" :title="isExpanded ? 'Profile settings' : (auth.user?.nickname || auth.user?.first_name || auth.user?.username || '')">
      <div class="user-avatar">
        <img v-if="auth.user?.avatar_path" :src="publicURL(auth.user.avatar_path)" class="user-avatar-img" :alt="auth.user.username" />
        <span v-else>{{ userInitials(auth.user) }}</span>
      </div>
      <div class="user-info sl">
        <span class="user-name">{{ auth.user?.nickname || auth.user?.first_name || auth.user?.username }}</span>
        <span class="user-role">{{ auth.user?.role }}</span>
      </div>
    </RouterLink>
    <span v-if="auth.user?.role === 'reviewer'" class="user-name">{{ auth.user?.username }} · Reviewer</span>
    <button class="logout-btn" @click="auth.logout" title="Log out">
      <span class="logout-label sl">Logout</span>
      <AppIcon name="log-out" :size="16" />
    </button>
  </div>

  <!-- Footer — always visible -->
  <div class="sidebar-footer">
    <template v-if="isExpanded">
      <a :href="branding.website" target="_blank" rel="noopener" class="footer-link">&copy; {{ branding.website.replace(/^https?:\/\//, '') }}</a>
      <span class="footer-version-group">
        <CalendarVersion :version="rawVersion" />
        <button class="footer-version" type="button" @click="showChangelog = true" title="What's new" aria-label="What's new">
          <AppIcon name="history" :size="13" />
        </button>
      </span>
    </template>
  </div>
  <div class="sidebar-meta-row">
    <a href="https://www.gnu.org/licenses/agpl-3.0.html" target="_blank" rel="noopener" class="meta-badge" title="Licensed under AGPL-3.0-only">
      <AppIcon name="shield" :size="9" />
      <span class="sl">AGPL-3.0-only</span>
    </a>
    <!-- PAI-280: upstream attribution. Was AppFooter's only unique
         payload; merged here so we don't lose the outbound link when
         AppFooter is dropped. Stays paimos.com regardless of brand. -->
    <a href="https://paimos.com" target="_blank" rel="noopener" class="meta-badge" title="paimos.com">
      <AppIcon name="globe" :size="9" />
      <span class="sl">paimos.com</span>
    </a>
    <a v-if="gitHash" href="https://github.com/PAIMOS/paimos" target="_blank" rel="noopener" class="meta-badge" :title="`Build ${gitHash}`">
      <AppIcon name="github" :size="9" />
      <span class="sl">{{ gitHash }}</span>
    </a>
  </div>

  <AppChangelogModal :open="showChangelog" @close="showChangelog = false" />
</template>

<style scoped>
/* ── Nav item (Settings link) ────────────────────────────────────────────── */
.nav-item {
  display: flex; align-items: center; gap: .6rem;
  padding: .5rem .65rem; border-radius: var(--radius);
  color: #8faabf; font-size: 13px; font-weight: 500;
  transition: background .15s, color .15s; text-decoration: none; overflow: hidden;
}
.nav-item svg { width: 16px; height: 16px; flex-shrink: 0; }
.nav-item:hover { background: rgba(255,255,255,.06); color: #fff; }
.nav-item.active { background: color-mix(in srgb, var(--brand-blue) 30%, transparent); color: #fff; }
.dev-badge {
  margin-left: auto; background: #ef4444; color: #fff;
  font-size: 10px; font-weight: 700; border-radius: 99px;
  padding: .05rem .4rem; line-height: 1.6; flex-shrink: 0;
}

/* ── User row ────────────────────────────────────────────────────────────── */
.user-row {
  display: flex; align-items: center; gap: .55rem; overflow: hidden;
  padding: .5rem .4rem;
  border-top: 1px solid rgba(255,255,255,.07);
  border-bottom: 1px solid rgba(255,255,255,.07);
}
.user-profile-link {
  display: flex; align-items: center; gap: .55rem;
  flex: 1; min-width: 0; overflow: hidden; text-decoration: none;
  border-radius: var(--radius);
  transition: background .15s;
}
.user-profile-link:hover { background: rgba(255,255,255,.06); }
.user-profile-link:hover .user-avatar { box-shadow: 0 0 0 2px rgba(255,255,255,.2); }
.user-avatar {
  width: 26px; height: 26px; background: var(--brand-blue); color: #fff;
  border-radius: 50%; display: flex; align-items: center; justify-content: center;
  font-size: 11px; font-weight: 700; flex-shrink: 0; overflow: hidden;
  box-shadow: 0 0 0 1px rgba(0,0,0,.35);
}
.user-avatar-img { width: 100%; height: 100%; object-fit: cover; display: block; }
.user-info  { flex: 1; min-width: 0; }
.user-name  { display: block; font-size: 12px; font-weight: 500; color: #fff; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.user-role  { display: block; font-size: 10px; color: #637a8f; text-transform: uppercase; letter-spacing: .05em; }
.logout-btn {
  background: transparent; border: none; color: #637a8f;
  padding: .2rem .35rem; border-radius: var(--radius);
  display: flex; align-items: center; gap: .35rem; cursor: pointer; font-family: inherit; flex-shrink: 0;
}
.logout-btn svg { width: 14px; height: 14px; }
.logout-label { font-size: 11px; font-weight: 500; letter-spacing: .02em; }
.logout-btn:hover { color: var(--sidebar-text, #e4e4e7); background: rgba(255,255,255,.06); }

/* ── Footer ──────────────────────────────────────────────────────────────── */
.sidebar-footer {
  display: flex; align-items: center; justify-content: space-between;
  gap: .5rem; padding-top: .35rem;
}
.footer-link {
  font-size: 10px; color: #6b8ea8; text-decoration: none;
  letter-spacing: .02em; transition: color .15s;
}
.footer-link:hover { color: #9ab8ce; }
.footer-version-group {
  display: inline-flex; align-items: center; gap: .4rem;
  font-size: 10px; color: #5a7a96; font-weight: 600;
}
.footer-version {
  font-size: 10px; color: #5a7a96; font-weight: 600; letter-spacing: .04em;
  flex-shrink: 0; background: none; border: none; padding: 0; cursor: pointer;
  font-family: inherit; transition: color .15s;
}
.footer-version:hover { color: #9ab8ce; text-decoration: underline; }

/* ── Sidebar meta row (license + git hash) ───────────────────────────────── */
.sidebar-meta-row {
  display: flex; align-items: center; justify-content: center; gap: .65rem;
  padding: .1rem .4rem;
}
.meta-badge {
  display: inline-flex; align-items: center; gap: .2rem;
  font-size: 9px; font-weight: 600; letter-spacing: .03em;
  color: #5a7a96; text-decoration: none;
  opacity: .45; transition: opacity .15s;
}
.meta-badge:hover { opacity: .8; }
</style>
