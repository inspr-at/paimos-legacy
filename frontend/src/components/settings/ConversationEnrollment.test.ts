// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const { apiGet, apiPost, authState, loadRuntimes } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  authState: { isAdmin: true },
  loadRuntimes: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  api: { get: apiGet, post: apiPost },
  errMsg: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get isAdmin() { return authState.isAdmin },
  }),
}))

vi.mock('@/components/habitat/habitatLifecycle', async importOriginal => ({
  ...(await importOriginal<typeof import('@/components/habitat/habitatLifecycle')>()),
  loadHabitatRuntimes: loadRuntimes,
}))

import ConversationEnrollment from './ConversationEnrollment.vue'

const runtime = (overrides: Record<string, unknown> = {}) => ({
  id: '11111111-1111-4111-8111-111111111111',
  project_id: 17,
  generation: '22222222-2222-4222-8222-222222222222',
  machine_id: 'owned-mac',
  schema_version: 4 as const,
  workspaces: [],
  account_scopes: [{
    account_label: 'chatgpt',
    accounts: [{ key: 'account-a', label: 'Codex Team' }],
    profiles: [{ id: 'codex-sol-high', version: '2' }],
    attachment_revision: 7,
    account_availability: 'available' as const,
  }],
  conversation: {
    schema_version: 1 as const,
    account_key: 'account-a',
    attachment_revision: 7,
    dispatch_profile_id: 'codex-sol-high',
    dispatch_profile_version: '2',
    execution_policy_id: 'aithema-conversation-v1' as const,
    max_output_bytes: 262144,
    max_events: 512,
  },
  expires_at: '2027-01-01T00:00:00Z',
  sessions: [],
  ...overrides,
})

const codexProfile = {
  id: 'codex-sol-high',
  version: '2',
  harness: 'codex',
  model: 'gpt-6-sol',
  family: 'openai',
  tier: 'strong',
  effort: 'high',
  machine_source: 'authenticated_reporter',
  account_source: 'local_probe',
  workspace_mode: 'exclusive',
}

async function settle(turns = 10) {
  await Promise.resolve()
  for (let index = 0; index < turns; index++) await nextTick()
}

async function mountEnrollment() {
  const host = document.createElement('div')
  document.body.appendChild(host)
  createApp(ConversationEnrollment).mount(host)
  await settle()
  return host
}

function setValue(host: HTMLElement, selector: string, value: string, event = 'input') {
  const element = host.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(selector)!
  element.value = value
  element.dispatchEvent(new Event(event, { bubbles: true }))
}

async function fillForm(host: HTMLElement) {
  setValue(host, '#conversation-name', 'Aithema attended')
  setValue(host, '#conversation-project', '17', 'change')
  await settle()
  setValue(host, '#conversation-runtime', runtime().id, 'change')
  await nextTick()
  setValue(host, '#conversation-account', 'chatgpt\0account-a', 'change')
  await nextTick()
  setValue(host, '#conversation-profile', 'codex-sol-high@2', 'change')
  setValue(host, '#conversation-expiry', new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString())
  setValue(host, '#conversation-project-ref', 'aithema-project-explicit')
  setValue(host, '#conversation-issuer-0', 'https://aithema.example')
  setValue(host, '#conversation-subject-0', 'actor-explicit-42')
  setValue(host, '#conversation-user-0', '9', 'change')
  setValue(host, '#conversation-age-recipients', 'age1publicrecipient')
  await nextTick()
  const verified = host.querySelector<HTMLInputElement>('.verification-row input')!
  verified.checked = true
  verified.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
}

function responseFor(host: HTMLElement, overrides: Record<string, unknown> = {}) {
  const expiry = host.querySelector<HTMLInputElement>('#conversation-expiry')!.value
  return {
    schema_version: 1,
    id: 81,
    name: 'Aithema attended',
    key_prefix: 'paimos_abcd',
    expires_at: expiry,
    binding: {
      binding_id: '33333333-3333-4333-8333-333333333333',
      revision: 1,
      project_id: 17,
      host_id: 'owned-mac',
      project_ref: 'aithema-project-explicit',
      runtime_id: runtime().id,
      runtime_generation: runtime().generation,
      account_key: 'account-a',
      attachment_revision: 7,
      dispatch_profile_id: 'codex-sol-high',
      dispatch_profile_version: '2',
      execution_policy_id: 'aithema-conversation-v1',
      limits: {
        max_input_bytes: 131072,
        max_messages: 128,
        max_output_bytes: 262144,
        max_event_bytes: 8192,
        max_events: 512,
        max_timeout_ms: 180000,
      },
    },
    credential_delivery: 'age',
    key_age_base64: window.btoa('age-encryption.org/v1\nencrypted conversation credential'),
    ...overrides,
  }
}

describe('ConversationEnrollment (PAI-1029)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authState.isAdmin = true
    document.body.innerHTML = ''
    loadRuntimes.mockResolvedValue([runtime()])
    apiGet.mockImplementation(async (path: string) => {
      if (path === '/projects') return [{ id: 17, key: 'PAI', name: 'Paimos', status: 'active' }]
      if (path === '/users') return [{ id: 9, username: 'mapped-user', status: 'active' }]
      if (path === '/ai/execution-options?project_id=17') {
        return {
          dispatch_profiles: [
            codexProfile,
            { ...codexProfile, id: 'cursor-grok', harness: 'cursor', model: 'grok-4.7-high' },
          ],
        }
      }
      throw new Error(`unexpected GET ${path}`)
    })
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, writable: true, value: vi.fn(() => 'blob:conversation-age') })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, writable: true, value: vi.fn() })
  })

  it('loads only server-listed Codex choices and never offers Grok', async () => {
    const host = await mountEnrollment()
    setValue(host, '#conversation-project', '17', 'change')
    await settle()
    setValue(host, '#conversation-runtime', runtime().id, 'change')
    await nextTick()
    setValue(host, '#conversation-account', 'chatgpt\0account-a', 'change')
    await nextTick()

    expect(host.querySelector('#conversation-account')?.textContent).toContain('Codex Team')
    expect(host.querySelector('#conversation-profile')?.textContent).toContain('codex-sol-high@2')
    expect(host.textContent).not.toContain('Grok')
  })

  it('shows setup needed and offers no ordinary coding runtime without readiness', async () => {
    const codingOnly = runtime({ conversation: undefined })
    loadRuntimes.mockResolvedValueOnce([codingOnly])
    const host = await mountEnrollment()
    setValue(host, '#conversation-project', '17', 'change')
    await settle()

    expect(host.textContent).toContain('Conversation setup is needed')
    expect(host.querySelectorAll('#conversation-runtime option')).toHaveLength(1)
    expect(host.textContent).not.toContain(codingOnly.id)
  })

  it('refreshes authority, submits the exact verified binding, and downloads only ciphertext', async () => {
    vi.useFakeTimers()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const host = await mountEnrollment()
    await fillForm(host)
    apiPost.mockResolvedValueOnce(responseFor(host))

    host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle()

    expect(loadRuntimes).toHaveBeenCalledTimes(2)
    expect(apiPost).toHaveBeenCalledWith('/auth/conversation-services', expect.objectContaining({
      schema_version: 1,
      project_id: 17,
      host_id: 'owned-mac',
      project_ref: 'aithema-project-explicit',
      runtime_id: runtime().id,
      runtime_generation: runtime().generation,
      account_key: 'account-a',
      attachment_revision: 7,
      dispatch_profile_id: 'codex-sol-high',
      dispatch_profile_version: '2',
      actors: [{ issuer: 'https://aithema.example', subject: 'actor-explicit-42', user_id: 9 }],
      age_recipients: ['age1publicrecipient'],
    }))
    const request = apiPost.mock.calls[0]![1]
    expect(request).not.toHaveProperty('account_label')
    expect(request).not.toHaveProperty('key')
    expect(request).not.toHaveProperty('password')
    expect(URL.createObjectURL).toHaveBeenCalledOnce()
    const blob = vi.mocked(URL.createObjectURL).mock.calls[0]![0] as Blob
    expect(await blob.text()).toBe('age-encryption.org/v1\nencrypted conversation credential')
    expect(click).toHaveBeenCalledOnce()
    expect(host.textContent).toContain('Binding 33333333-3333-4333-8333-333333333333 revision 1 is ready.')
    expect(host.textContent).not.toContain(responseFor(host).key_age_base64)
    expect(host.querySelector('input[value*="paimos_"]')).toBeNull()

    const retry = [...host.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.includes('Download encrypted credential again'))!
    retry.click()
    await nextTick()
    expect(apiPost).toHaveBeenCalledTimes(1)
    expect(URL.createObjectURL).toHaveBeenCalledTimes(2)
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
  })

  it('invalidates review when the runtime generation changes during refresh', async () => {
    loadRuntimes
      .mockResolvedValueOnce([runtime()])
      .mockResolvedValueOnce([runtime({ generation: '44444444-4444-4444-8444-444444444444' })])
    const host = await mountEnrollment()
    await fillForm(host)

    host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle()

    expect(apiPost).not.toHaveBeenCalled()
    expect(host.textContent).toContain('runtime, account attachment, or profile changed')
    expect(host.querySelector<HTMLInputElement>('.verification-row input')!.checked).toBe(false)
  })

  it('invalidates review when the account attachment revision changes during refresh', async () => {
    loadRuntimes
      .mockResolvedValueOnce([runtime()])
      .mockResolvedValueOnce([runtime({
        account_scopes: [{
          ...runtime().account_scopes[0],
          attachment_revision: 8,
        }],
      })])
    const host = await mountEnrollment()
    await fillForm(host)

    host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle()

    expect(apiPost).not.toHaveBeenCalled()
    expect(host.textContent).toContain('runtime, account attachment, or profile changed')
  })

  it('invalidates review when the initialized consumer caps change during refresh', async () => {
    loadRuntimes
      .mockResolvedValueOnce([runtime()])
      .mockResolvedValueOnce([runtime({
        conversation: { ...runtime().conversation, max_events: 256 },
      })])
    const host = await mountEnrollment()
    await fillForm(host)

    host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle()

    expect(apiPost).not.toHaveBeenCalled()
    expect(host.textContent).toContain('runtime, account attachment, or profile changed')
    expect(host.querySelector<HTMLInputElement>('#conversation-max_events')!.value).toBe('256')
  })

  it('rejects plaintext, mismatched binding, and malformed ciphertext responses', async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const host = await mountEnrollment()
    await fillForm(host)
    apiPost
      .mockResolvedValueOnce({ ...responseFor(host), key: 'plaintext-must-not-render' })
      .mockResolvedValueOnce(responseFor(host, { binding: { ...responseFor(host).binding, attachment_revision: 9 } }))
      .mockResolvedValueOnce(responseFor(host, { key_age_base64: window.btoa('not age ciphertext') }))

    const submit = host.querySelector<HTMLButtonElement>('button[type="submit"]')!
    for (let index = 0; index < 3; index++) {
      submit.click()
      await settle()
      expect(host.textContent).toContain('Failed to create a valid encrypted conversation credential.')
      expect(host.textContent).not.toContain('plaintext-must-not-render')
    }
    expect(URL.createObjectURL).not.toHaveBeenCalled()
    expect(click).not.toHaveBeenCalled()
  })

  it('surfaces metadata and enrollment failures without exposing a download', async () => {
    apiGet.mockRejectedValueOnce(new Error('offline'))
    let host = await mountEnrollment()
    expect(host.textContent).toContain('Enrollment choices are unavailable.')

    document.body.innerHTML = ''
    apiGet.mockImplementation(async (path: string) => {
      if (path === '/projects') return [{ id: 17, key: 'PAI', name: 'Paimos' }]
      if (path === '/users') return [{ id: 9, username: 'mapped-user', status: 'active' }]
      if (path.includes('/ai/execution-options')) return { dispatch_profiles: [codexProfile] }
      return []
    })
    host = await mountEnrollment()
    await fillForm(host)
    apiPost.mockRejectedValueOnce(new Error('refused'))
    host.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle()
    expect(host.textContent).toContain('Failed to create a valid encrypted conversation credential.')
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })
})
