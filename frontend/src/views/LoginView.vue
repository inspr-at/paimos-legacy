<script setup lang="ts">
import { ref, watch, computed, onMounted } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { ApiError, api } from '@/api/client'
import { publicURL } from '@/publicPath'
import { useBranding } from '@/composables/useBranding'
import { useSidebarColors } from '@/composables/useSidebarColors'
import { postLoginRedirectOrFallback } from '@/router/redirects'
import AppIcon from '@/components/AppIcon.vue'
import CalendarVersion from '@/components/CalendarVersion.vue'

const { branding } = useBranding()
const { bgColor, patternImage } = useSidebarColors()
const route = useRoute()

const version = __APP_VERSION__

// PAI-120: SSO probe. The button only appears once /api/auth/oidc/status
// reports enabled=true, so an instance with no IdP configured looks
// identical to today.
const ssoEnabled = ref(false)
const ssoLabel = ref('Sign in with SSO')
const passwordEnabled = ref(true)
onMounted(async () => {
  try {
    const r = await api.get<{ enabled: boolean; label: string; password_disabled?: boolean }>('/auth/oidc/status')
    ssoEnabled.value = r.enabled
    passwordEnabled.value = !r.password_disabled
    if (r.label) ssoLabel.value = r.label
  } catch {
    /* no-op — SSO simply stays hidden */
  }
})

// PAI-743: identifier-first. Step 1 collects the identifier alone so a
// password manager has nothing to autofill-and-submit before the user
// can choose SSO; step 2 shows only the method(s) the server's home
// realm discovery says apply. `?method=password` asks the server to skip
// domain routing, but never overrides the instance's password policy.
const forcePassword = computed(() => {
  const m = route.query.method
  return (Array.isArray(m) ? m[0] : m) === 'password'
})
const identifierSubmitted = ref(false)
const methodPassword = ref(true)
const methodSSO = ref(false)
const showPassword = computed(() => passwordEnabled.value && methodPassword.value)
const showSSO = computed(() => methodSSO.value || (!passwordEnabled.value && ssoEnabled.value))

/** SSO entry point, carrying the identifier so the IdP can skip its own prompt. */
const ssoHref = computed(() => {
  const params = new URLSearchParams()
  const id = username.value.trim()
  if (id) params.set('login_hint', id)
  const redirect = Array.isArray(route.query.redirect) ? route.query.redirect[0] : route.query.redirect
  if (typeof redirect === 'string' && redirect) params.set('redirect', redirect)
  const q = params.toString()
  return publicURL('/api/auth/oidc/login') + (q ? `?${q}` : '')
})

async function submitIdentifier() {
  if (!username.value.trim()) return
  error.value = ''
  loading.value = true
  try {
    const r = await api.post<{ password: boolean; sso: boolean; sso_label?: string }>(
      '/auth/login/methods',
      { identifier: username.value.trim(), ...(forcePassword.value ? { method: 'password' } : {}) },
    )
    methodPassword.value = r.password
    methodSSO.value = r.sso
    if (r.sso_label) ssoLabel.value = r.sso_label
  } catch {
    // Routing is a convenience, never a gate: if the probe fails, fall
    // back to the pre-PAI-743 surface rather than stranding the user.
    methodPassword.value = passwordEnabled.value
    methodSSO.value = ssoEnabled.value
  } finally {
    loading.value = false
    identifierSubmitted.value = true
  }
}

/** Back to step 1 — clears the password so it never rides along. */
function editIdentifier() {
  identifierSubmitted.value = false
  password.value = ''
  error.value = ''
}

const ssoError = computed(() => {
  const e = route.query.sso_error
  if (!e) return ''
  const code = Array.isArray(e) ? e[0] : e
  switch (code) {
    case 'bad_state':
    case 'missing_verifier':
      return 'SSO handshake expired — please try again.'
    case 'email_required':
      return passwordEnabled.value
        ? 'SSO did not return a verified email; sign in with a password instead.'
        : 'SSO did not return a verified email. Ask an admin for help.'
    case 'invite_required':
      return 'No PAIMOS account is linked to this SSO email yet. Ask an admin for access.'
    case 'account_disabled':
      return 'This PAIMOS account is disabled. Ask an admin to restore access.'
    case 'not_configured':
      return 'SSO is not configured on this server.'
    default:
      return 'SSO sign-in failed. Please try again.'
  }
})

const auth = useAuthStore()
const router = useRouter()

const username = ref('')
const password = ref('')
const error    = ref('')
const loading  = ref(false)

// 2FA second step
const totpRequired = ref(false)
const totpToken    = ref('')
const otpCode      = ref('')

const postLoginPath = computed(() => postLoginRedirectOrFallback(route.query.redirect))

function finishLogin() {
  router.push(postLoginPath.value)
}

async function submit() {
  if (!showPassword.value) return
  error.value = ''
  loading.value = true
  try {
    const result = await api.post<any>('/auth/login', {
      username: username.value,
      password: password.value,
    })
    if (result.totp_required) {
      totpToken.value    = result.totp_token
      totpRequired.value = true
    } else {
      // Successful login envelope: { user, access, ...session flags }.
      await auth.completeLogin(result)
      await auth.fetchTOTPStatus()
      finishLogin()
    }
  } catch (e) {
    error.value = e instanceof ApiError ? 'Invalid username or password.' : 'Login failed.'
  } finally {
    loading.value = false
  }
}

async function submitOTP() {
  if (otpCode.value.length !== 6) return
  error.value = ''
  loading.value = true
  try {
    const result = await api.post<any>('/auth/totp/verify', {
      totp_token: totpToken.value,
      code: otpCode.value,
    })
    await auth.completeLogin(result)
    await auth.fetchTOTPStatus()
    finishLogin()
  } catch (e) {
    error.value = 'Invalid code. Please try again.'
    otpCode.value = ''
  } finally {
    loading.value = false
  }
}

// Auto-submit when 6 digits entered
watch(otpCode, v => {
  if (v.length === 6) submitOTP()
})

function backToLogin() {
  totpRequired.value = false
  totpToken.value    = ''
  otpCode.value      = ''
  error.value        = ''
  identifierSubmitted.value = false
  password.value     = ''
}
</script>

<template>
  <div class="login-page" :style="{ background: bgColor }">
    <div class="pattern-bg" :style="{ backgroundImage: patternImage }" aria-hidden="true"></div>

    <div class="login-card">
      <div class="login-header">
        <img :src="publicURL(branding.logo)" :alt="branding.company" class="login-logo" />
        <h1 class="login-title">{{ branding.product }}</h1>
        <p class="login-sub">{{ branding.company }} {{ branding.tagline }}</p>
      </div>

      <!-- Step 1: identifier only (PAI-743). No password field here, so
           a password manager has nothing to autofill-and-submit before
           the user gets to choose SSO. -->
      <form
        v-if="!totpRequired && !identifierSubmitted"
        @submit.prevent="submitIdentifier"
        class="login-form"
      >
        <div class="field">
          <label for="username">Username or email</label>
          <input
            id="username"
            v-model="username"
            type="text"
            autocomplete="username"
            placeholder="you@example.com"
            autofocus
            required
          />
        </div>
        <div v-if="error" class="login-error">{{ error }}</div>
        <div v-else-if="ssoError" class="login-error">{{ ssoError }}</div>
        <button type="submit" class="btn btn-primary login-btn" :disabled="loading">
          {{ loading ? 'Checking…' : 'Continue' }}
        </button>
        <RouterLink v-if="passwordEnabled" to="/forgot" class="login-forgot-link">Forgot password?</RouterLink>
      </form>

      <!-- Step 2: the method(s) that apply to this identifier. -->
      <form
        v-else-if="!totpRequired"
        @submit.prevent="submit"
        class="login-form"
      >
        <!-- The identifier stays in the DOM (hidden, still
             autocomplete="username") so password managers can associate
             the credential with the right account and save it correctly
             — the documented requirement for two-step sign-in forms. -->
        <input
          v-model="username"
          type="text"
          autocomplete="username"
          class="visually-hidden"
          tabindex="-1"
          aria-hidden="true"
          readonly
        />
        <button type="button" class="login-identity" @click="editIdentifier">
          <span class="login-identity-name">{{ username }}</span>
          <span class="login-identity-change">Change</span>
        </button>

        <div v-if="showPassword" class="field">
          <label for="password">Password</label>
          <input
            id="password"
            v-model="password"
            type="password"
            autocomplete="current-password"
            placeholder="••••••••"
            autofocus
            required
          />
        </div>
        <p v-else class="login-sso-hint">
          This address signs in through your identity provider.
        </p>

        <div v-if="error" class="login-error">{{ error }}</div>
        <div v-else-if="ssoError" class="login-error">{{ ssoError }}</div>

        <button
          v-if="showPassword"
          type="submit"
          class="btn btn-primary login-btn"
          :disabled="loading"
        >
          {{ loading ? 'Signing in…' : 'Sign in' }}
        </button>
        <a
          v-if="showSSO"
          :href="ssoHref"
          :class="['btn login-btn login-sso-btn', showPassword ? 'btn-ghost' : 'btn-primary']"
        >
          {{ ssoLabel }}
        </a>
        <RouterLink v-if="passwordEnabled" to="/forgot" class="login-forgot-link">Forgot password?</RouterLink>
      </form>

      <!-- Step 2: OTP code -->
      <div v-else class="login-form">
        <div class="totp-info">
          <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" style="color: var(--brand-blue); margin: 0 auto 0.75rem; display:block">
            <rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 018 0v4"/>
          </svg>
          <p class="totp-label">Two-factor authentication</p>
          <p class="totp-sub">Enter the 6-digit code from your authenticator app.</p>
        </div>
        <div class="field">
          <label for="otp">Authentication code</label>
          <input
            id="otp"
            v-model="otpCode"
            type="text"
            inputmode="numeric"
            pattern="[0-9]*"
            maxlength="6"
            autocomplete="one-time-code"
            placeholder="000000"
            class="otp-input"
            autofocus
          />
        </div>
        <div v-if="error" class="login-error">{{ error }}</div>
        <button class="btn btn-primary login-btn" :disabled="loading || otpCode.length !== 6" @click="submitOTP">
          {{ loading ? 'Verifying…' : 'Verify' }}
        </button>
        <button class="btn btn-ghost login-btn-back" @click="backToLogin">← Back to login</button>
      </div>

      <footer class="login-footer">
        <img :src="publicURL(branding.logo)" alt="" class="footer-logo" aria-hidden="true" />
        <span>{{ branding.company }}</span>
        <span class="footer-sep">·</span>
        <CalendarVersion :version="version" />
        <span class="footer-sep">·</span>
        <a href="https://github.com/PAIMOS/paimos" target="_blank" rel="noopener" class="footer-gh" title="GitHub">
          <AppIcon name="github" :size="12" />
        </a>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  overflow: hidden;
}

.pattern-bg {
  position: absolute;
  inset: 0;
  opacity: 1;
}

.login-card {
  position: relative;
  background: var(--bg-card);
  border-radius: 10px;
  box-shadow: var(--shadow-md), 0 0 0 1px rgba(0,0,0,.08);
  padding: 2.5rem 2.25rem 2rem;
  width: 100%;
  max-width: 360px;
}

.login-header {
  text-align: center;
  margin-bottom: 2rem;
}
.login-logo {
  width: 52px;
  height: 52px;
  object-fit: contain;
  margin-bottom: .75rem;
}
.login-title {
  font-size: 22px;
  font-weight: 700;
  color: var(--text);
  letter-spacing: -.02em;
}
.login-sub {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: .2rem;
}

.login-form { display: flex; flex-direction: column; gap: 1rem; }

.field { display: flex; flex-direction: column; gap: .35rem; }
.field label { font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: .05em; }

.login-error {
  background: #fde8e8;
  color: #c0392b;
  border-radius: var(--radius);
  padding: .5rem .75rem;
  font-size: 13px;
}

/* PAI-743: the step-2 username field must stay RENDERED for password
   managers to associate the credential — clip it, never display:none. */
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}

/* Step-2 identity chip: who you're signing in as, and a way back. */
.login-identity {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: .5rem;
  width: 100%;
  padding: .5rem .75rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg);
  font: inherit;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
  transition: border-color .15s, background .15s;
}
.login-identity:hover,
.login-identity:focus-visible {
  border-color: var(--brand-blue);
  outline: none;
}
.login-identity-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}
.login-identity-change {
  flex: 0 0 auto;
  font-size: 12px;
  font-weight: 600;
  color: var(--brand-blue);
}
.login-sso-hint {
  font-size: 13px;
  color: var(--text-muted);
  line-height: 1.5;
}

.login-btn { width: 100%; justify-content: center; padding: .65rem; font-size: 14px; margin-top: .25rem; }
.login-sso-btn { display: inline-flex; align-items: center; text-decoration: none; }
.login-btn-back { width: 100%; justify-content: center; font-size: 12px; color: var(--text-muted); margin-top: .25rem; }
.login-forgot-link {
  align-self: center;
  font-size: 12px;
  color: var(--text-muted);
  text-decoration: none;
  margin-top: .1rem;
}
.login-forgot-link:hover { color: var(--brand-blue); text-decoration: underline; }

.totp-info { text-align: center; margin-bottom: .5rem; }
.totp-label { font-size: 15px; font-weight: 700; color: var(--text); margin-bottom: .3rem; }
.totp-sub   { font-size: 13px; color: var(--text-muted); line-height: 1.5; }

.otp-input {
  text-align: center;
  font-size: 28px;
  font-weight: 700;
  letter-spacing: .35em;
  font-family: monospace;
  padding: .65rem;
}

.login-footer {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: .4rem;
  margin-top: 1.75rem;
  color: #b0bec8;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: .08em;
  text-transform: uppercase;
}
.footer-logo {
  width: 16px;
  height: 16px;
  object-fit: contain;
  opacity: .35;
}
.footer-sep { opacity: .4; }
.footer-gh { color: #b0bec8; opacity: .6; transition: opacity .15s; display: inline-flex; }
.footer-gh:hover { opacity: 1; }
</style>
