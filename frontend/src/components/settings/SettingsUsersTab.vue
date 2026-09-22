<script setup lang="ts">
import LoadingText from "@/components/LoadingText.vue";
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { api, errMsg } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useSort } from '@/composables/useSort'
import AppModal from '@/components/AppModal.vue'
import AppIcon from '@/components/AppIcon.vue'
import MetaSelect from '@/components/MetaSelect.vue'
import type { MetaOption } from '@/components/MetaSelect.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import type { User } from '@/types'

const auth = useAuthStore()
const router = useRouter()

const ROLE_OPTIONS: MetaOption[] = [
  { value: 'member', label: 'Member' },
  { value: 'reviewer', label: 'Reviewer (shared projects only)' },
  { value: 'admin', label: 'Admin' },
  { value: 'super_admin', label: 'Super admin' },
  { value: 'external', label: 'External' },
]

const users          = ref<User[]>([])
const usersLoaded    = ref(false)

// ── Unified create / edit form ─────────────────────────────────────────────
// One form drives both "New user" and "Edit user" so the two flows can never
// drift apart (same fields, same validation). `mode` selects POST vs PUT.
// PAI-321: must_change_password defaults to TRUE so a new (or admin-reset)
// account is forced through the change-password screen; it's sent explicitly
// so the backend honors `false` rather than its server-side default of true.
type UserFormMode = 'create' | 'edit'
const blankUserForm = () => ({
  username: '', role: 'member', password: '', must_change_password: true,
  nickname: '', email: '', internal_rate_hourly: null as number | null, locale: 'en',
})
const userFormOpen   = ref(false)
const userFormMode   = ref<UserFormMode>('create')
const userFormTarget = ref<User | null>(null)
const userForm       = ref(blankUserForm())
const userFormError  = ref('')
const savingUser     = ref(false)
const isEditing      = computed(() => userFormMode.value === 'edit')

const resetTotpConfirm = ref(false)
const resetTotpLoading = ref(false)
const disableUserTarget = ref<User | null>(null)
const disablingUser     = ref(false)
const deleteUserTarget  = ref<User | null>(null)
const deletingUser      = ref(false)
const impersonatingUserId = ref<number | null>(null)
const impersonationError = ref('')
const isSelf = (u: User) => u.id === auth.user?.id
const roleText = (role: User['role']) => role.replace('_', ' ').toUpperCase()
const canImpersonate = (u: User) => auth.isSuperAdmin && !auth.impersonation && !isSelf(u) && u.status === 'active'

// ── Lifecycle status filter (active / inactive / deleted) ──────────────────
// The whole user lifecycle lives on this one screen: create → edit → disable
// → delete → restore, switched via this filter instead of a separate Trash.
type StatusFilter = 'active' | 'inactive' | 'deleted'
const STATUS_FILTERS: { value: StatusFilter; label: string }[] = [
  { value: 'active',   label: 'Active'   },
  { value: 'inactive', label: 'Inactive' },
  { value: 'deleted',  label: 'Deleted'  },
]
const statusFilter = ref<StatusFilter>('active')
const statusCounts = computed(() => ({
  active:   users.value.filter(u => u.status === 'active').length,
  inactive: users.value.filter(u => u.status === 'inactive').length,
  deleted:  users.value.filter(u => u.status === 'deleted').length,
}))
const visibleUsers = computed(() => users.value.filter(u => u.status === statusFilter.value))

// Inline nickname editing
const nickEditId  = ref<number | null>(null)
const nickEditVal = ref('')
function startNickEdit(u: User) {
  nickEditId.value  = u.id
  nickEditVal.value = u.nickname || ''
}
function cancelNickEdit() { nickEditId.value = null }
async function saveNick(u: User) {
  const val = nickEditVal.value.trim().slice(0, 3)
  nickEditId.value = null
  if (val === (u.nickname || '')) return
  try {
    const updated = await api.put<User>(`/users/${u.id}`, { nickname: val })
    const idx = users.value.findIndex(x => x.id === u.id)
    if (idx !== -1) users.value[idx] = updated
  } catch { /* ignore */ }
}

function relativeTime(ts: string): string {
  const diff = Date.now() - new Date(ts).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return `${Math.floor(days / 30)}mo ago`
}

const { sorted: sortedUsers, sortIndicator: userSortInd, thProps: userThProps } = useSort(visibleUsers, {
  username:   { value: u => u.username,   type: 'string' },
  role:       { value: u => u.role,       type: { order: ['super_admin','admin','member','reviewer','external'] } },
  rate:       { value: u => u.internal_rate_hourly ?? 0, type: 'number' },
  created_at: { value: u => u.created_at, type: 'date' },
})

// ── Per-user project access (unified matrix; replaces legacy Projects flow) ─

type AccessLevel = 'none' | 'viewer' | 'editor'
type FilterMode = 'explicit' | 'all'

interface MembershipRow {
  project_id: number
  project_key: string
  project_name: string
  access_level: AccessLevel
}

const ACCESS_LEVELS: { value: AccessLevel; label: string }[] = [
  { value: 'none',   label: 'None'   },
  { value: 'viewer', label: 'Viewer' },
  { value: 'editor', label: 'Editor' },
]

const membershipsTarget   = ref<User | null>(null)
const memberships         = ref<MembershipRow[]>([])
const membershipsLoading  = ref(false)
const filter              = ref<FilterMode>('all')
const addProjectId        = ref<number | null>(null)
const addLevel            = ref<AccessLevel>('viewer')

const explicitAccessOnly = computed(() => ['external', 'reviewer'].includes(membershipsTarget.value?.role ?? ''))
// Reviewers and externals have no default project access.
const roleDefault = computed<AccessLevel>(() => explicitAccessOnly.value ? 'none' : 'editor')
const roleDefaultLabel = computed(() => roleDefault.value)
const roleHint = computed(() => {
  if (!membershipsTarget.value) return ''
  return explicitAccessOnly.value
    ? 'This role starts with no access. Grant read-only access per project below.'
    : 'Members and admins default to editor. Use this to override per project.'
})
// Reviewers and externals are granted read-only access in this editor.
const visibleLevels = computed(() =>
  explicitAccessOnly.value ? ACCESS_LEVELS.filter(l => l.value !== 'editor') : ACCESS_LEVELS
)
const explicitRows = computed(() =>
  memberships.value
    .filter(r => r.access_level !== roleDefault.value)
    .slice()
    .sort((a, b) => a.project_key.localeCompare(b.project_key))
)
const defaultRows = computed(() =>
  memberships.value
    .filter(r => r.access_level === roleDefault.value)
    .slice()
    .sort((a, b) => a.project_key.localeCompare(b.project_key))
)
// Pickable in the Add bar = projects without an explicit grant yet. Includes
// defaulted rows: picking one promotes it to the chosen level.
const addableProjects = computed(() => {
  const explicitIds = new Set(explicitRows.value.map(r => r.project_id))
  return memberships.value
    .filter(r => !explicitIds.has(r.project_id))
    .slice()
    .sort((a, b) => a.project_key.localeCompare(b.project_key))
})

async function openMemberships(u: User) {
  membershipsTarget.value = u
  membershipsLoading.value = true
  memberships.value = []
  // External rows are sparse (no seeded grants) — explicit-only is the
  // useful default. Staff defaults to editor everywhere — show all so
  // overrides are visible.
  filter.value = ['external', 'reviewer'].includes(u.role) ? 'explicit' : 'all'
  addProjectId.value = null
  addLevel.value = 'viewer'
  try {
    memberships.value = await api.get<MembershipRow[]>(`/users/${u.id}/memberships`)
  } catch { /* ignore */ }
  membershipsLoading.value = false
}

async function setMembership(row: MembershipRow, lvl: AccessLevel) {
  if (!membershipsTarget.value) return
  const uid = membershipsTarget.value.id
  const prev = row.access_level
  row.access_level = lvl // optimistic
  try {
    await api.put(`/users/${uid}/memberships/${row.project_id}`, { access_level: lvl })
  } catch {
    row.access_level = prev
  }
}

async function resetMembership(row: MembershipRow) {
  if (!membershipsTarget.value) return
  const uid = membershipsTarget.value.id
  const prev = row.access_level
  try {
    await api.delete(`/users/${uid}/memberships/${row.project_id}`)
    // After deleting the explicit row the backend falls back to the role
    // default — re-read memberships so the row reflects that default.
    memberships.value = await api.get<MembershipRow[]>(`/users/${uid}/memberships`)
  } catch {
    row.access_level = prev
  }
}

async function addRow() {
  if (!membershipsTarget.value || !addProjectId.value) return
  const uid = membershipsTarget.value.id
  const pid = addProjectId.value
  const lvl: AccessLevel = explicitAccessOnly.value ? 'viewer' : addLevel.value
  const row = memberships.value.find(r => r.project_id === pid)
  const prev = row?.access_level
  if (row) row.access_level = lvl // optimistic
  try {
    await api.put(`/users/${uid}/memberships/${pid}`, { access_level: lvl })
    addProjectId.value = null
  } catch {
    if (row && prev !== undefined) row.access_level = prev
  }
}

async function loadUsers() {
  if (usersLoaded.value) return
  // Active + inactive come from the default list; deleted are fetched
  // alongside so the entire lifecycle is filterable on this one screen.
  const [live, deleted] = await Promise.all([
    api.get<User[]>('/users'),
    api.get<User[]>('/users?status=deleted'),
  ])
  users.value = [...live, ...deleted]
  usersLoaded.value = true
}

function openCreateUser() {
  userFormMode.value = 'create'
  userFormTarget.value = null
  userForm.value = blankUserForm()
  userFormError.value = ''
  resetTotpConfirm.value = false
  userFormOpen.value = true
}
function openEditUser(u: User) {
  userFormMode.value = 'edit'
  userFormTarget.value = u
  // Default the force-change gate ON when resetting someone else's
  // password, OFF when editing your own (a deliberate self-set).
  userForm.value = {
    username: u.username, role: u.role, password: '',
    must_change_password: !isSelf(u),
    nickname: u.nickname || '', email: u.email || '',
    internal_rate_hourly: u.internal_rate_hourly ?? null, locale: u.locale || 'en',
  }
  userFormError.value = ''
  resetTotpConfirm.value = false
  userFormOpen.value = true
}
function closeUserForm() {
  userFormOpen.value = false
  userFormTarget.value = null
}

async function saveUser() {
  userFormError.value = ''
  const f = userForm.value
  if (!f.username) { userFormError.value = 'Username is required.'; return }
  if (!isEditing.value && !f.password) { userFormError.value = 'Username and password required.'; return }
  savingUser.value = true
  try {
    if (isEditing.value) {
      const t = userFormTarget.value!
      // Send only what changed so an edit never clobbers untouched fields.
      const payload: Record<string, string | number | boolean | null> = {}
      if (f.username !== t.username)                          payload.username = f.username
      if (f.role !== t.role)                                 payload.role = f.role
      if (f.nickname !== (t.nickname || ''))                 payload.nickname = f.nickname
      if (f.email !== (t.email || ''))                       payload.email = f.email
      if (f.internal_rate_hourly !== t.internal_rate_hourly) payload.internal_rate_hourly = f.internal_rate_hourly
      if (f.locale !== (t.locale || 'en'))                   payload.locale = f.locale
      if (f.password) {
        payload.password = f.password
        // Carry the force-change choice only when actually setting a password.
        payload.must_change_password = f.must_change_password
      }
      const u = await api.put<User>(`/users/${t.id}`, payload)
      const idx = users.value.findIndex(x => x.id === u.id)
      if (idx >= 0) users.value[idx] = u
    } else {
      const u = await api.post<User>('/users', {
        username: f.username, role: f.role, password: f.password,
        must_change_password: f.must_change_password,
        nickname: f.nickname, email: f.email,
        internal_rate_hourly: f.internal_rate_hourly, locale: f.locale,
      })
      users.value.push(u)
    }
    closeUserForm()
  } catch (e: unknown) { userFormError.value = errMsg(e) }
  finally { savingUser.value = false }
}
async function resetUserTOTP() {
  if (!userFormTarget.value) return
  resetTotpLoading.value = true
  try {
    await api.post(`/users/${userFormTarget.value.id}/reset-totp`, {})
    const idx = users.value.findIndex(x => x.id === userFormTarget.value!.id)
    if (idx >= 0) users.value[idx] = { ...users.value[idx], totp_enabled: false }
    userFormTarget.value = { ...userFormTarget.value, totp_enabled: false }
    resetTotpConfirm.value = false
  } catch (e: unknown) { userFormError.value = errMsg(e, 'Failed to reset 2FA.') }
  finally { resetTotpLoading.value = false }
}
async function enableUser(u: User) {
  await api.put<User>(`/users/${u.id}`, { status: 'active' })
  const idx = users.value.findIndex(x => x.id === u.id)
  if (idx >= 0) users.value[idx] = { ...users.value[idx], status: 'active' }
}
async function restoreUser(u: User) {
  // Deleted → inactive: bring the account back disabled, then it can be
  // re-enabled from the Inactive filter. Mirrors the old Trash behavior.
  await api.put<User>(`/users/${u.id}`, { status: 'inactive' })
  const idx = users.value.findIndex(x => x.id === u.id)
  if (idx >= 0) users.value[idx] = { ...users.value[idx], status: 'inactive' }
}
async function confirmDisableUser() {
  if (!disableUserTarget.value) return
  disablingUser.value = true
  try {
    await api.post(`/users/${disableUserTarget.value.id}/disable`, {})
    const idx = users.value.findIndex(x => x.id === disableUserTarget.value!.id)
    if (idx >= 0) users.value[idx] = { ...users.value[idx], status: 'inactive' }
    disableUserTarget.value = null
  } catch (e: unknown) { /* swallow */ }
  finally { disablingUser.value = false }
}
async function confirmDeleteUser() {
  if (!deleteUserTarget.value) return
  deletingUser.value = true
  try {
    await api.delete(`/users/${deleteUserTarget.value.id}`)
    // Soft-delete: keep the row so it shows under the Deleted filter and
    // can be restored, rather than vanishing from the screen.
    const idx = users.value.findIndex(x => x.id === deleteUserTarget.value!.id)
    if (idx >= 0) users.value[idx] = { ...users.value[idx], status: 'deleted' }
    deleteUserTarget.value = null
  } catch (e: unknown) { /* swallow */ }
  finally { deletingUser.value = false }
}

async function impersonateUser(u: User) {
  if (!canImpersonate(u)) return
  impersonatingUserId.value = u.id
  impersonationError.value = ''
  try {
    await auth.startImpersonation(u.id)
    await router.push(auth.user?.role === 'external' ? '/portal' : '/')
  } catch (e: unknown) {
    impersonationError.value = errMsg(e, 'Failed to start impersonation.')
  } finally {
    impersonatingUserId.value = null
  }
}

// Init
loadUsers()
</script>

<template>
  <div class="section">
    <div class="section-header-row">
      <div>
        <h2 class="section-title">Users</h2>
        <p class="section-desc">Manage team members and their roles.</p>
      </div>
      <button class="btn btn-primary btn-sm" @click="openCreateUser">+ New user</button>
    </div>
    <div v-if="impersonationError" class="form-error impersonation-error">{{ impersonationError }}</div>

    <div class="user-status-filter" role="tablist" aria-label="User status">
      <button
        v-for="s in STATUS_FILTERS" :key="s.value"
        type="button" role="tab"
        class="user-status-filter__opt"
        :class="{ 'is-active': statusFilter === s.value }"
        :aria-selected="statusFilter === s.value"
        @click="statusFilter = s.value"
      >{{ s.label }} <span class="user-status-filter__count">{{ statusCounts[s.value] }}</span></button>
    </div>

    <div class="card" style="padding:0;overflow:hidden">
      <table class="settings-table">
        <thead>
          <tr>
            <th v-bind="userThProps('username')">Username <span class="sort-ind"><AppIcon :name="userSortInd('username')" :size="11" /></span></th>
            <th>Nickname</th>
            <th v-bind="userThProps('role')">Role <span class="sort-ind"><AppIcon :name="userSortInd('role')" :size="11" /></span></th>
            <th>Status</th>
            <th v-bind="userThProps('rate')">Rate <span class="sort-ind"><AppIcon :name="userSortInd('rate')" :size="11" /></span></th>
            <th v-bind="userThProps('created_at')">Created <span class="sort-ind"><AppIcon :name="userSortInd('created_at')" :size="11" /></span></th>
            <th>Last Login</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in sortedUsers" :key="u.id" :class="{ 'row-muted': u.status !== 'active' }">
            <td>
              <div class="user-avatar-row">
                <UserAvatar :user="u" size="sm" :class="{ 'ua--inactive': u.status !== 'active' }" />
                <span class="fw500">{{ u.username }}</span>
                <span v-if="isSelf(u)" class="you-badge">you</span>
              </div>
            </td>
            <td class="nick-cell">
              <template v-if="nickEditId === u.id">
                <input
                  class="nick-input"
                  v-model="nickEditVal"
                  maxlength="3"
                  placeholder="abc"
                  @keydown.enter.prevent="saveNick(u)"
                  @keydown.escape.prevent="cancelNickEdit"
                  @blur="saveNick(u)"
                  autofocus
                />
              </template>
              <span v-else class="nick-display" @click="startNickEdit(u)" :title="'Click to edit nickname'">
                {{ u.nickname || '—' }}
              </span>
            </td>
            <td><span class="role-label" :class="`role-${u.role}`">{{ roleText(u.role) }}</span></td>
            <td>
              <span class="badge" :class="{
                'badge-active': u.status === 'active',
                'badge-archived': u.status === 'inactive',
                'badge-deleted': u.status === 'deleted',
              }">{{ u.status }}</span>
            </td>
            <td class="muted">{{ u.internal_rate_hourly != null ? `€${u.internal_rate_hourly}` : '—' }}</td>
            <td class="muted">{{ u.created_at.slice(0,10) }}</td>
            <td class="muted" :title="u.last_login_at ?? ''">{{ u.last_login_at ? relativeTime(u.last_login_at) : 'Never' }}</td>
            <td class="actions-cell">
              <template v-if="u.status === 'deleted'">
                <button class="btn btn-ghost btn-sm" @click="restoreUser(u)" title="Restore to disabled">Restore</button>
              </template>
              <template v-else>
                <button
                  v-if="canImpersonate(u)"
                  class="btn btn-ghost btn-sm"
                  :disabled="impersonatingUserId === u.id"
                  @click="impersonateUser(u)"
                  title="Act as this user in the current session"
                >{{ impersonatingUserId === u.id ? 'Starting…' : 'Act as' }}</button>
                <button class="btn btn-ghost btn-sm" @click="openEditUser(u)">Edit</button>
                <button v-if="!isSelf(u)" class="btn btn-ghost btn-sm" @click="openMemberships(u)" title="Per-project access levels">Access</button>
                <template v-if="!isSelf(u)">
                  <button v-if="u.status === 'active'" class="btn btn-ghost btn-sm" @click="disableUserTarget=u" title="Disable account">Disable</button>
                  <button v-if="u.status === 'inactive'" class="btn btn-ghost btn-sm" @click="enableUser(u)" title="Re-enable account">Enable</button>
                  <button class="btn btn-ghost btn-sm danger" @click="deleteUserTarget=u" title="Soft-delete account">Delete</button>
                </template>
              </template>
            </td>
          </tr>
          <tr v-if="usersLoaded && sortedUsers.length === 0">
            <td colspan="8" class="users-empty">No {{ statusFilter }} users.</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>

  <!-- ── Modals ──────────────────────────────────────────────────────────── -->
  <AppModal title="Disable Account" :open="!!disableUserTarget" @close="disableUserTarget=null" confirm-key="d" @confirm="confirmDisableUser">
    <p style="font-size:14px;color:var(--text);margin-bottom:1.25rem">
      Disable <strong>{{ disableUserTarget?.username }}</strong>? They won't be able to log in. All their data is preserved and the account can be re-enabled.
    </p>
    <div class="form-actions">
      <button class="btn btn-ghost" @click="disableUserTarget=null"><u>C</u>ancel</button>
      <button class="btn btn-danger" :disabled="disablingUser" @click="confirmDisableUser"><template v-if="disablingUser">Disabling…</template><template v-else><u>D</u>isable account</template></button>
    </div>
  </AppModal>

  <AppModal title="Delete Account" :open="!!deleteUserTarget" @close="deleteUserTarget=null" confirm-key="d" @confirm="confirmDeleteUser">
    <p style="font-size:14px;color:var(--text);margin-bottom:1.25rem">
      Soft-delete <strong>{{ deleteUserTarget?.username }}</strong>? The account will be hidden from the UI and login will be blocked. All their issues, comments and history are preserved. Restore is possible via database update.
    </p>
    <div class="form-actions">
      <button class="btn btn-ghost" @click="deleteUserTarget=null"><u>C</u>ancel</button>
      <button class="btn btn-danger" :disabled="deletingUser" @click="confirmDeleteUser"><template v-if="deletingUser">Deleting…</template><template v-else><u>D</u>elete account</template></button>
    </div>
  </AppModal>

  <!-- Unified create / edit user form — identical fields for both flows. -->
  <AppModal :title="isEditing ? `Edit ${userFormTarget?.username}` : 'New user'" :open="userFormOpen" @close="closeUserForm">
    <form @submit.prevent="saveUser" class="form">
      <div class="edit-user-grid">
        <div class="field"><label>Username</label><input v-model="userForm.username" type="text" required /></div>
        <div class="field"><label>Nickname <span class="label-hint">— up to 3 chars</span></label>
          <input v-model="userForm.nickname" type="text" maxlength="3" placeholder="abc" style="width:80px" />
        </div>
        <div class="field"><label>Email</label><input v-model="userForm.email" type="email" placeholder="user@example.com" /></div>
        <div class="field"><label>Role</label><MetaSelect v-model="userForm.role" :options="ROLE_OPTIONS" /></div>
        <div class="field">
          <label>{{ isEditing ? 'New password' : 'Password' }}<span v-if="isEditing" class="label-hint"> — leave blank to keep</span></label>
          <input v-model="userForm.password" type="password" autocomplete="new-password" :placeholder="isEditing ? '••••••••' : 'At least 8 characters'" :required="!isEditing" />
          <!-- Setting a password drops the user's other sessions and (by
               default) forces them to choose their own on next login. -->
          <label v-if="userForm.password" class="check-row check-inline">
            <input v-model="userForm.must_change_password" type="checkbox" />
            <span>Force password change on {{ isEditing ? 'next' : 'first' }} login</span>
          </label>
        </div>
        <div class="field"><label>Internal rate <span class="label-hint">— €/h</span></label>
          <input v-model.number="userForm.internal_rate_hourly" type="number" step="0.01" min="0" placeholder="e.g. 95.00" style="width:120px" />
        </div>
        <div class="field"><label>Locale</label>
          <select v-model="userForm.locale" style="width:120px">
            <option value="en">English</option>
            <option value="de">Deutsch</option>
          </select>
        </div>
      </div>

      <div v-if="isEditing && userFormTarget?.totp_enabled" class="edit-user-totp">
        <div class="edit-user-totp-info">
          <AppIcon name="shield-check" :size="14" class="totp-icon" />
          <span>2FA is <strong>active</strong> for this user.</span>
        </div>
        <template v-if="!resetTotpConfirm">
          <button type="button" class="btn btn-ghost btn-sm danger" @click="resetTotpConfirm=true">Reset 2FA…</button>
        </template>
        <template v-else>
          <span class="totp-confirm-text">This will disable 2FA for {{ userFormTarget?.username }}. Continue?</span>
          <button type="button" class="btn btn-sm btn-danger" :disabled="resetTotpLoading" @click="resetUserTOTP">{{ resetTotpLoading ? 'Resetting…' : 'Yes, disable 2FA' }}</button>
          <button type="button" class="btn btn-ghost btn-sm" @click="resetTotpConfirm=false">Cancel</button>
        </template>
      </div>

      <div v-if="userFormError" class="form-error">{{ userFormError }}</div>
      <div class="form-actions">
        <button type="button" class="btn btn-ghost" @click="closeUserForm">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="savingUser">{{ savingUser ? 'Saving…' : (isEditing ? 'Save changes' : 'Create user') }}</button>
      </div>
    </form>
  </AppModal>

  <!-- Unified per-user project access (matrix + role-aware filter + add picker) -->
  <AppModal :title="`Access: ${membershipsTarget?.username}`" :open="!!membershipsTarget" max-width="680px" @close="membershipsTarget=null">
    <LoadingText v-if="membershipsLoading" class="memb-loading" label="Loading…" />

    <div v-else class="memb-body">
      <p class="empty-hint memb-hint">{{ roleHint }}</p>

      <div class="memb-toolbar">
        <div class="memb-segmented" role="tablist" aria-label="View">
          <button
            type="button" role="tab"
            class="memb-segmented__opt"
            :class="{ 'is-active': filter === 'explicit' }"
            :aria-selected="filter === 'explicit'"
            @click="filter = 'explicit'"
          >Explicit grants</button>
          <button
            type="button" role="tab"
            class="memb-segmented__opt"
            :class="{ 'is-active': filter === 'all' }"
            :aria-selected="filter === 'all'"
            @click="filter = 'all'"
          >All projects</button>
        </div>

        <div class="memb-add" :class="{ 'is-disabled': addableProjects.length === 0 }">
          <select v-model="addProjectId" class="memb-add__field memb-add__pick" :disabled="addableProjects.length === 0">
            <option :value="null" disabled>{{ addableProjects.length === 0 ? 'No projects to add' : 'Pick a project…' }}</option>
            <option v-for="p in addableProjects" :key="p.project_id" :value="p.project_id">
              {{ p.project_key }} — {{ p.project_name }}
            </option>
          </select>
          <select v-if="!explicitAccessOnly" v-model="addLevel" class="memb-add__field memb-add__lvl">
            <option value="viewer">Viewer</option>
            <option value="editor">Editor</option>
          </select>
          <span v-else class="memb-add__lvl-locked" title="Externals can only be granted Viewer access">Viewer</span>
          <button
            type="button"
            class="memb-add__btn"
            :disabled="!addProjectId"
            @click="addRow"
          >Add</button>
        </div>
      </div>

      <div v-if="memberships.length === 0" class="memb-empty">
        <span class="memb-empty__icon" aria-hidden="true">⌀</span>
        No projects to manage yet.
      </div>
      <div v-else-if="filter === 'explicit' && explicitRows.length === 0" class="memb-empty">
        <span class="memb-empty__icon" aria-hidden="true">∅</span>
        <span>No explicit grants yet.<br />Use the picker above to assign access.</span>
      </div>

      <div v-else class="memb-table-wrap">
        <table class="settings-table memb-table">
          <thead>
            <tr>
              <th>Project</th>
              <th class="th-access">Access</th>
              <th class="th-reset" aria-label="Reset to default"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in explicitRows" :key="`e-${row.project_id}`">
              <td>
                <span class="project-access-key">{{ row.project_key }}</span>
                <span class="project-access-name">{{ row.project_name }}</span>
              </td>
              <td>
                <div class="memb-lvl-group">
                  <button
                    v-for="lvl in visibleLevels" :key="lvl.value"
                    type="button"
                    class="btn btn-sm"
                    :class="row.access_level === lvl.value ? 'btn-primary' : 'btn-ghost'"
                    @click="setMembership(row, lvl.value)"
                  >{{ lvl.label }}</button>
                </div>
              </td>
              <td>
                <button class="btn btn-ghost btn-sm memb-reset" @click="resetMembership(row)" title="Revert to role default">Reset</button>
              </td>
            </tr>

            <tr
              v-if="filter === 'all' && explicitRows.length > 0 && defaultRows.length > 0"
              class="memb-divider-row"
              aria-hidden="true"
            >
              <td colspan="3">
                <div class="memb-divider"><span>Defaults — {{ roleDefaultLabel }}</span></div>
              </td>
            </tr>

            <tr v-for="row in defaultRows" :key="`d-${row.project_id}`">
              <td>
                <span class="project-access-key">{{ row.project_key }}</span>
                <span class="project-access-name">{{ row.project_name }}</span>
              </td>
              <td>
                <div class="memb-lvl-group">
                  <button
                    v-for="lvl in visibleLevels" :key="lvl.value"
                    type="button"
                    class="btn btn-sm"
                    :class="row.access_level === lvl.value ? 'btn-primary' : 'btn-ghost'"
                    @click="setMembership(row, lvl.value)"
                  >{{ lvl.label }}</button>
                </div>
              </td>
              <td>
                <button class="btn btn-ghost btn-sm memb-reset" disabled title="Already at role default">Reset</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="form-actions">
        <button type="button" class="btn btn-ghost" @click="membershipsTarget=null">Close</button>
      </div>
    </div>
  </AppModal>
</template>

<style src="./settings-shared.css"></style>
<style scoped>
.user-avatar-row { display: flex; align-items: center; gap: .6rem; }
.impersonation-error { margin: 0 0 .75rem; }

/* Lifecycle status filter — segmented pill above the table. */
.user-status-filter {
  display: inline-flex; align-items: center;
  border: 1px solid var(--border); border-radius: 999px;
  background: var(--bg-card); padding: 2px; margin-bottom: .9rem;
}
.user-status-filter__opt {
  border: none; background: transparent; color: var(--text-muted);
  font-size: 12px; font-weight: 600; letter-spacing: .02em;
  padding: .35rem .9rem; border-radius: 999px; cursor: pointer;
  white-space: nowrap; transition: background .12s ease, color .12s ease;
}
.user-status-filter__opt:hover:not(.is-active) { color: var(--text); }
.user-status-filter__opt.is-active { background: var(--brand-blue-pale); color: var(--brand-blue-dark); }
.user-status-filter__count {
  font-size: 11px; font-weight: 700; opacity: .65; margin-left: .15rem;
}
.users-empty { text-align: center; color: var(--text-muted); font-size: 13px; padding: 1.5rem; }
.you-badge { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; color: var(--text-muted); background: var(--bg); border: 1px solid var(--border); border-radius: 20px; padding: .1rem .45rem; }
.role-label { font-size: 11px; font-weight: 700; letter-spacing: .05em; }
.role-admin    { color: #d97706; }
.role-super_admin { color: #7c3aed; }
.role-member   { color: var(--brand-blue); }
.role-external { color: #dc2626; }
.field-check { display: flex; align-items: flex-start; }
.check-row {
  display: flex; align-items: flex-start; gap: .55rem;
  font-size: 13px; color: var(--text); cursor: pointer;
  user-select: none;
}
.check-row input[type="checkbox"] {
  width: auto; margin: 2px 0 0;
  accent-color: var(--brand-blue);
}
.check-hint { color: var(--text-muted); font-size: 12px; }
.check-inline { margin-top: .5rem; font-size: 12px; }
.edit-user-grid { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem 1.25rem; }
.edit-user-totp {
  display: flex; align-items: center; gap: .6rem; flex-wrap: wrap;
  padding: .6rem .75rem; background: var(--bg); border: 1px solid var(--border);
  border-radius: var(--radius); margin-top: .25rem; font-size: 13px;
}
.edit-user-totp-info { display: flex; align-items: center; gap: .35rem; flex: 1; }
.totp-icon { color: #16a34a; flex-shrink: 0; }
.totp-confirm-text { font-size: 12px; color: var(--text-muted); }
.ua--inactive { background: var(--text-muted) !important; }
.nick-cell { min-width: 80px; }
.nick-display {
  cursor: pointer; color: var(--text-muted); font-size: 12px; font-weight: 600;
  padding: .15rem .35rem; border-radius: 4px; display: inline-block;
  border: 1px solid transparent;
}
.nick-display:hover { background: var(--bg); border-color: var(--border); color: var(--text); }
.nick-input {
  width: 56px !important; padding: .2rem .35rem !important;
  font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: .05em;
  border: 1px solid var(--brand-blue); border-radius: 4px; outline: none;
  background: #fff;
}
.badge-deleted { background: #f8d7da; color: #721c24; }
.row-muted td { opacity: .65; }
.row-muted:hover td { opacity: .85; }
.badge-active   { background: #d4edda; color: #155724; }
/* ── Per-user access modal: chips, table, toolbar ───────────────────── */
.project-access-key {
  font-size: 11px; font-weight: 700; letter-spacing: .03em;
  padding: .1rem .4rem; border-radius: 3px;
  background: var(--brand-blue-pale); color: var(--brand-blue-dark);
  margin-right: .5rem;
}
.project-access-name { font-weight: 500; font-size: 13px; }

.memb-body { display: flex; flex-direction: column; }
.memb-loading { padding: 1rem; color: var(--text-muted); font-size: 13px; }
.memb-hint    { margin: 0 0 .9rem; padding: 0; }

.memb-toolbar {
  display: flex;
  align-items: stretch;
  gap: .65rem;
  margin-bottom: .9rem;
}

/* Filter — a "view setting" pill, distinct from action buttons */
.memb-segmented {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-card);
  padding: 2px;
}
.memb-segmented__opt {
  border: none;
  background: transparent;
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: .02em;
  padding: .35rem .8rem;
  border-radius: 999px;
  cursor: pointer;
  white-space: nowrap;
  transition: background .12s ease, color .12s ease;
}
.memb-segmented__opt:hover:not(.is-active) { color: var(--text); }
.memb-segmented__opt.is-active {
  background: var(--brand-blue-pale);
  color: var(--brand-blue-dark);
}

/* Add bar — one composed control with hairline-divided segments */
.memb-add {
  display: flex;
  align-items: stretch;
  flex: 1 1 auto;
  min-width: 0;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
  transition: border-color .12s ease, box-shadow .12s ease;
}
.memb-add:focus-within {
  border-color: var(--brand-blue);
  box-shadow: 0 0 0 3px var(--brand-blue-pale);
}
.memb-add.is-disabled { opacity: .55; }

/* Native selects stripped of their chrome so the bar reads as one shell;
   custom caret SVG keeps the affordance visible. */
.memb-add__field {
  appearance: none;
  -webkit-appearance: none;
  border: none;
  background: transparent;
  color: var(--text);
  font-size: 12px;
  font-family: inherit;
  outline: none;
  cursor: pointer;
  padding: 0 1.6rem 0 .75rem;
  background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='8' height='5' viewBox='0 0 8 5'><path d='M0 0l4 5 4-5z' fill='%237a8597'/></svg>");
  background-repeat: no-repeat;
  background-position: right .6rem center;
}
.memb-add__field:disabled { cursor: not-allowed; }
.memb-add__pick {
  flex: 1 1 auto;
  min-width: 0;
  border-right: 1px solid var(--border);
  text-overflow: ellipsis;
}
.memb-add__lvl {
  flex: 0 0 92px;
  border-right: 1px solid var(--border);
}
.memb-add__lvl-locked {
  display: inline-flex; align-items: center;
  flex: 0 0 92px;
  padding: 0 .75rem;
  border-right: 1px solid var(--border);
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  letter-spacing: .01em;
}
/* Custom-styled instead of .btn-primary so it sits flush in the bar
   (no nested border, no rounded-corner mismatch). */
.memb-add__btn {
  border: none;
  background: var(--brand-blue);
  color: #fff;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: .01em;
  padding: 0 1rem;
  cursor: pointer;
  transition: background .12s ease;
  white-space: nowrap;
}
.memb-add__btn:hover:not(:disabled) { background: var(--brand-blue-dark); }
.memb-add__btn:disabled {
  background: var(--bg);
  color: var(--text-muted);
  cursor: not-allowed;
}

/* Empty states */
.memb-empty {
  display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  gap: .55rem;
  min-height: 120px;
  padding: 1.5rem;
  margin-bottom: .9rem;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
  background: var(--bg);
  border: 1px dashed var(--border);
  border-radius: var(--radius);
  line-height: 1.45;
}
.memb-empty__icon {
  font-family: 'DM Mono','Fira Code',monospace;
  font-size: 22px;
  color: var(--border);
  line-height: 1;
}

/* Table */
.memb-table-wrap {
  max-height: 56vh;
  overflow-y: auto;
  margin-bottom: .9rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.memb-table { margin: 0; }
.memb-table thead th { position: sticky; top: 0; z-index: 1; }
.memb-table td { padding: .5rem .6rem; }
.memb-table .th-access { width: 220px; }
.memb-table .th-reset  { width: 76px; }
.memb-lvl-group { display: inline-flex; gap: .25rem; }

/* Reset is neutral, not destructive — keep its hover off the danger palette. */
.memb-reset:hover:not(:disabled) { background: var(--bg); color: var(--text); }
.memb-reset:disabled {
  opacity: .35;
  cursor: not-allowed;
  pointer-events: none;
}

/* Divider row — hairline + small label, Linear-style "below the fold". */
.memb-divider-row td {
  padding: 1rem .6rem .35rem;
  background: transparent;
  border-bottom: none;
}
.memb-divider-row:hover td { background: transparent; }
.memb-divider {
  display: flex; align-items: center; gap: .75rem;
}
.memb-divider::before {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--border);
}
.memb-divider span {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: .08em;
  text-transform: uppercase;
  color: var(--text-muted);
}
</style>
