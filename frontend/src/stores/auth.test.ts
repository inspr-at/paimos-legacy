/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import {
  ApiError,
  api,
  capturePermissionsEpochGeneration,
  permissionsEpoch,
  resetPermissionsEpoch,
  sessionExpired,
  type ApiMetaResponse,
} from '@/api/client'
import { useAuthStore, type MeResponse, type User } from './auth'

function fakeUser(overrides: Partial<User> = {}): User {
  return {
    id: 1,
    username: 'mba',
    role: 'admin',
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    nickname: '',
    first_name: '',
    last_name: '',
    email: '',
    avatar_path: '',
    markdown_default: true,
    monospace_fields: false,
    recent_projects_limit: 10,
    internal_rate_hourly: null,
    show_alt_unit_table: false,
    show_alt_unit_detail: false,
    locale: 'en',
    recent_timers_limit: 10,
    timezone: 'auto',
    preview_hover_delay: 300,
    issue_auto_refresh_enabled: true,
    issue_auto_refresh_interval_seconds: 60,
    search_scope_shortcut: '',
    last_login_at: null,
    accruals_stats_enabled: false,
    accruals_extra_statuses: '',
    is_super_admin: false,
    ...overrides,
  }
}

function me(user: User, allProjects: boolean, levels: Record<string, 'viewer' | 'editor'> = {}): MeResponse {
  return { user, access: { all_projects: allProjects, levels } }
}

function meta(data: MeResponse, epoch: string, generation = capturePermissionsEpochGeneration()): ApiMetaResponse<MeResponse> {
  return {
    data,
    etag: null,
    lastModified: null,
    permissionsEpoch: epoch,
    permissionsEpochGeneration: generation,
    status: 200,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, resolve, reject }
}

async function flush() {
  for (let index = 0; index < 10; index += 1) await Promise.resolve()
}

describe('auth store response-local permissions authority', () => {
  let auth: ReturnType<typeof useAuthStore> | null = null

  it('keeps an internal reviewer limited to explicitly shared viewer projects', async () => {
    vi.spyOn(api, 'getWithMeta').mockImplementation(async () => {
      permissionsEpoch.value = '1'
      return meta(me(fakeUser({ role: 'reviewer' }), false, { '7': 'viewer' }), '1')
    })
    auth = useAuthStore()
    await auth.fetchMe()
    expect(auth.user?.role).toBe('reviewer')
    expect(auth.isAdmin).toBe(false)
    expect(auth.allProjects).toBe(false)
    expect(auth.canView(7)).toBe(true)
    expect(auth.canEdit(7)).toBe(false)
    expect(auth.canView(8)).toBe(false)
    expect(auth.canView(null)).toBe(false)
  })

  beforeEach(() => {
    resetPermissionsEpoch()
    sessionExpired.value = false
    setActivePinia(createPinia())
    vi.spyOn(api, 'get').mockResolvedValue({ enabled: false } as never)
    vi.spyOn(api, 'post').mockResolvedValue({} as never)
  })

  afterEach(() => {
    auth?.$dispose()
    auth = null
    vi.restoreAllMocks()
    sessionExpired.value = false
  })

  it('binds pristine /me to its response epoch and scrubs every old gate synchronously on the next bump', async () => {
    const admin = me(fakeUser({ username: 'admin', role: 'admin' }), true)
    const member = me(fakeUser({ id: 2, username: 'member', role: 'member' }), false, { '7': 'viewer' })
    vi.spyOn(api, 'getWithMeta')
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(admin, '10')
      })
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '11'
        return meta(member, '11')
      })

    auth = useAuthStore()
    await auth.fetchMe()
    expect(auth.user?.username).toBe('admin')
    expect(auth.isAdmin).toBe(true)
    expect(auth.allProjects).toBe(true)

    permissionsEpoch.value = '11'
    expect(auth.user).toBeNull()
    expect(auth.isAdmin).toBe(false)
    expect(auth.allProjects).toBe(false)
    expect(auth.accessibleProjects.size).toBe(0)
    expect(auth.canView(null)).toBe(false)
    expect(auth.canEdit(null)).toBe(false)

    await flush()
    expect(api.getWithMeta).toHaveBeenCalledTimes(2)
    expect(auth.user?.username).toBe('member')
    expect(auth.isAdmin).toBe(false)
    expect(auth.canView(7)).toBe(true)
    expect(auth.canEdit(7)).toBe(false)
  })

  it('aborts epoch 11, drains epoch 12, and succeeds without waiting for an epoch 13 event', async () => {
    const pendingEleven = deferred<ApiMetaResponse<MeResponse>>()
    let elevenSignal: AbortSignal | undefined
    const admin = me(fakeUser({ username: 'admin' }), true)
    const revoked = me(fakeUser({ id: 2, username: 'revoked', role: 'member' }), false)
    vi.spyOn(api, 'getWithMeta')
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(admin, '10')
      })
      .mockImplementationOnce(async (_path, options) => {
        elevenSignal = options?.signal
        options?.signal?.addEventListener('abort', () => {
          pendingEleven.reject(new DOMException('superseded', 'AbortError'))
        }, { once: true })
        return await pendingEleven.promise
      })
      .mockRejectedValueOnce(new Error('transient epoch-12 failure'))
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '12'
        return meta(revoked, '12')
      })

    auth = useAuthStore()
    await auth.fetchMe()
    permissionsEpoch.value = '11'
    expect(auth.user).toBeNull()
    await Promise.resolve()
    expect(api.getWithMeta).toHaveBeenCalledTimes(2)

    permissionsEpoch.value = '12'
    expect(elevenSignal?.aborted).toBe(true)
    await flush()

    expect(api.getWithMeta).toHaveBeenCalledTimes(4)
    expect(auth.user?.username).toBe('revoked')
    expect(auth.allProjects).toBe(false)
  })

  it('never exposes a headerless login envelope before current /me proves the new principal', async () => {
    const loginEnvelope = me(fakeUser({ username: 'LOGIN_BODY_ADMIN' }), true)
    const proven = me(fakeUser({ id: 2, username: 'proven-member', role: 'member' }), false, { '9': 'editor' })
    const proof = deferred<ApiMetaResponse<MeResponse>>()
    vi.spyOn(api, 'getWithMeta').mockImplementation(async () => await proof.promise)
    auth = useAuthStore()
    sessionExpired.value = true

    const completing = auth.completeLogin(loginEnvelope)
    expect(auth.user).toBeNull()
    expect(auth.isAdmin).toBe(false)
    expect(auth.canView(null)).toBe(false)
    await Promise.resolve()
    expect(api.getWithMeta).toHaveBeenCalledOnce()

    permissionsEpoch.value = '7'
    proof.resolve(meta(proven, '7'))
    await completing
    expect(auth.user?.username).toBe('proven-member')
    expect(auth.canEdit(9)).toBe(true)
    expect(JSON.stringify(auth.user)).not.toContain('LOGIN_BODY_ADMIN')
    expect(sessionExpired.value).toBe(false)
  })

  it('restores checked but leaves every gate empty when login authority proof fails', async () => {
    vi.spyOn(api, 'getWithMeta').mockRejectedValue(new Error('offline'))
    auth = useAuthStore()

    await expect(auth.completeLogin(me(fakeUser({ username: 'UNPROVEN_ADMIN' }), true)))
      .rejects.toThrow('login authority could not be verified')
    expect(auth.checked).toBe(true)
    expect(auth.user).toBeNull()
    expect(auth.isAdmin).toBe(false)
    expect(auth.allProjects).toBe(false)
    expect(auth.accessibleProjects.size).toBe(0)
  })

  it('clears an already-hydrated principal on a terminal /me 401', async () => {
    const admin = me(fakeUser({ username: 'ADMIN' }), true)
    vi.spyOn(api, 'getWithMeta')
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(admin, '10')
      })
      .mockRejectedValueOnce(new ApiError(401, 'unauthorized'))

    auth = useAuthStore()
    await auth.fetchMe()
    expect(auth.user?.username).toBe('ADMIN')
    await auth.refreshMe()

    expect(auth.user).toBeNull()
    expect(auth.isAdmin).toBe(false)
    expect(auth.allProjects).toBe(false)
    expect(auth.canView(null)).toBe(false)
  })

  it('probes /me on a live cross-tab generation restore even when the local user was already null', async () => {
    const bob = me(fakeUser({ id: 2, username: 'BOB', role: 'member' }), false, { '5': 'viewer' })
    vi.spyOn(api, 'getWithMeta').mockImplementationOnce(async () => {
      permissionsEpoch.value = '5'
      return meta(bob, '5')
    })
    auth = useAuthStore()
    expect(auth.user).toBeNull()

    resetPermissionsEpoch()
    await flush()

    expect(api.getWithMeta).toHaveBeenCalledOnce()
    expect(auth.user?.username).toBe('BOB')
    expect(auth.canView(5)).toBe(true)
  })

  it('clears Alice on a cross-tab generation reset and accepts Bob at the same numeric epoch', async () => {
    const alice = me(fakeUser({ username: 'ALICE_ADMIN' }), true)
    const bob = me(fakeUser({ id: 2, username: 'BOB_MEMBER', role: 'member' }), false, { '5': 'viewer' })
    vi.spyOn(api, 'getWithMeta')
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(alice, '10')
      })
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(bob, '10')
      })

    auth = useAuthStore()
    await auth.fetchMe()
    expect(auth.user?.username).toBe('ALICE_ADMIN')

    resetPermissionsEpoch()
    expect(auth.user).toBeNull()
    expect(auth.allProjects).toBe(false)
    await flush()

    expect(api.getWithMeta).toHaveBeenCalledTimes(2)
    expect(auth.user?.username).toBe('BOB_MEMBER')
    expect(auth.canView(5)).toBe(true)
    expect(JSON.stringify(auth.user)).not.toContain('ALICE_ADMIN')
  })

  it('keeps an old-generation /me body inert after the new equal-epoch principal commits', async () => {
    const oldBody = deferred<ApiMetaResponse<MeResponse>>()
    const alice = me(fakeUser({ username: 'ALICE' }), true)
    const staleAlice = me(fakeUser({ username: 'STALE_ALICE' }), true)
    const bob = me(fakeUser({ id: 2, username: 'BOB', role: 'member' }), false)
    let oldGeneration = -1
    vi.spyOn(api, 'getWithMeta')
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(alice, '10')
      })
      .mockImplementationOnce(async () => {
        oldGeneration = capturePermissionsEpochGeneration()
        return await oldBody.promise
      })
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '10'
        return meta(bob, '10')
      })

    auth = useAuthStore()
    await auth.fetchMe()
    const staleRefresh = auth.refreshMe()
    await Promise.resolve()
    resetPermissionsEpoch()
    await flush()
    expect(auth.user?.username).toBe('BOB')

    oldBody.resolve(meta(staleAlice, '10', oldGeneration))
    await staleRefresh
    await flush()
    expect(auth.user?.username).toBe('BOB')
    expect(auth.isAdmin).toBe(false)
  })

  it('accepts lower epochs across impersonation generations and never retains the prior principal on failure', async () => {
    const admin = me(fakeUser({ username: 'ADMIN' }), true)
    const target = me(fakeUser({ id: 2, username: 'TARGET', role: 'member' }), false, { '4': 'viewer' })
    vi.spyOn(api, 'getWithMeta')
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '100'
        return meta(admin, '100')
      })
      .mockImplementationOnce(async () => {
        permissionsEpoch.value = '5'
        return meta(target, '5')
      })
      .mockRejectedValue(new Error('failed to restore admin'))

    auth = useAuthStore()
    await auth.fetchMe()
    await auth.startImpersonation(2)
    expect(auth.user?.username).toBe('TARGET')
    expect(permissionsEpoch.value).toBe('5')
    expect(auth.canView(4)).toBe(true)

    await expect(auth.stopImpersonation()).rejects.toThrow('impersonation authority could not be verified')
    expect(auth.user).toBeNull()
    expect(auth.allProjects).toBe(false)
    expect(auth.accessibleProjects.size).toBe(0)
  })
})
