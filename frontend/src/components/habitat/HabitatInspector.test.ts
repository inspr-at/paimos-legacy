import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import { loadOrchestration } from '@/services/orchestration'
import { habitatFixture } from './__fixtures__/orchestration'
import HabitatInspector from './HabitatInspector.vue'
const voice = vi.hoisted(() => ({
  state: { value: 'idle' },
  level: { value: 0 },
  errorMessage: { value: null },
  isActive: { value: false },
  sink: null as null | ((blob: Blob) => Promise<void>),
  start: vi.fn(async (sink: (blob: Blob) => Promise<void>) => {
    voice.sink = sink
    voice.isActive.value = true
    return true
  }),
  finish: vi.fn(() => true),
  stop: vi.fn(() => {
    voice.isActive.value = false
    voice.sink = null
  }),
  micSupported: vi.fn(() => true),
  transcribe: vi.fn(),
}))
vi.mock('./habitatControls', async (original) => ({
  ...(await original<typeof import('./habitatControls')>()),
  loadAssignmentHistory: vi.fn().mockResolvedValue({ events: [], next: null }),
}))
vi.mock('@/composables/useMicTranscript', () => ({
  useMicTranscript: () => voice,
}))
vi.mock('@/services/agentModeVoice', () => ({
  transcribeAgentModeAudio: voice.transcribe,
}))
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
vi.mock('vue-router', () => ({ RouterLink: { props: ['to'], template: '<a><slot /></a>' } }))
const authFixture = vi.hoisted(() => ({user: {role: 'member'}, canEdit: () => true}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authFixture }))
afterEach(() => {
  authFixture.user.role = 'member'
  vi.mocked(api.post).mockRestore?.()
  vi.clearAllMocks()
  voice.sink = null
  voice.isActive.value = false
  voice.transcribe.mockReset()
  voice.start.mockClear()
  voice.finish.mockClear()
  voice.stop.mockClear()
  document.body.innerHTML = ''
})
const button = (el: HTMLElement, text: string) =>
  [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)!
describe('Habitat inspector', () => {
  it('keeps reviewer inspection free of assignment, launch and sessions controls', async () => {
    authFixture.user.role = 'reviewer'
    const fixture = habitatFixture()
    const props = reactive({worker: fixture.fleet.workers[0] as typeof fixture.fleet.workers[0] | null,
      project: null as typeof fixture.project_coordination[0] | null, workers: fixture.fleet.workers,
      deliveries: [], fresh: true, authority: 'reviewer:1'})
    const mounted = await mountComponent(HabitatInspector, props)
    expect(mounted.el.textContent).not.toContain('Restart / repair options')
    expect(mounted.el.textContent).not.toContain('Remove worker')
    props.worker = null
    props.project = fixture.project_coordination[0]
    await nextTick()
    expect(mounted.el.textContent).toContain('Knowledge')
    expect(mounted.el.textContent).not.toContain('Product sessions & decisions')
    expect(mounted.el.textContent).not.toContain('Review coordinator setup')
    await mounted.unmount()
  })
  it('turns a selected-worker transcript into a draft and sends only after explicit confirmation', async () => {
    const fixture = habitatFixture()
    const worker = fixture.fleet.workers[0]
    vi.mocked(loadOrchestration).mockResolvedValue(fixture)
    voice.transcribe.mockResolvedValueOnce({ text: 'Ship the bounded slice' })
    const post = vi.spyOn(api, 'post').mockImplementation(async (_path, body) => {
      const request = body as { utterance_id: string }
      return {
        schema_version: 1,
        utterance_id: request.utterance_id,
        harness_session_id: worker.harness_session_id,
        harness_session_revision: worker.revision,
        message_id: '00000000-0000-4000-8000-000000000091',
        delivery_id: '00000000-0000-4000-8000-000000000092',
        delivery_level: 'simple',
        created_at: new Date().toISOString(),
      }
    })
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })

    button(mounted.el, 'Voice to selected worker').click()
    await vi.waitFor(() => expect(voice.sink).toBeTypeOf('function'))
    expect(post).not.toHaveBeenCalled()
    await voice.sink!(new Blob(['voice']))
    await nextTick()
    expect(mounted.el.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe(
      'Ship the bounded slice',
    )
    expect(mounted.el.querySelector('[aria-label="Confirm simple"]')).not.toBeNull()
    expect(post).not.toHaveBeenCalled()

    button(mounted.el, 'Send message').click()
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.harness_session_id}/messages/v1`,
      expect.objectContaining({
        expected_revision: worker.revision,
        text: 'Ship the bounded slice',
        delivery_level: 'simple',
      }),
      expect.any(Object),
    )
    await mounted.unmount()
  })

  it('turns the stop phrase into a generation-bound review and requests it only after confirm', async () => {
    const fixture = habitatFixture()
    const worker = fixture.fleet.workers[0]
    voice.transcribe.mockResolvedValueOnce({ text: 'Stop worker.' })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      state: 'requested',
      control: {
        sequence: 1,
        requested_at: new Date().toISOString(),
        requested_by_user_id: 1,
        id: '00000000-0000-4000-8000-000000000099',
        harness_session_id: worker.harness_session_id,
        kind: 'stop',
        state: 'pending',
      },
    })
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })

    button(mounted.el, 'Voice to selected worker').click()
    await vi.waitFor(() => expect(voice.sink).toBeTypeOf('function'))
    await voice.sink!(new Blob(['voice']))
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Confirm stop"]')?.textContent).toContain(
      `revision ${worker.revision}`,
    )
    expect(post).not.toHaveBeenCalled()

    button(mounted.el, 'Confirm stop').click()
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.harness_session_id}/controls/v1/stop`,
      { expected_revision: worker.revision, request_key: expect.any(String) },
      expect.any(Object),
    )
    await mounted.unmount()
  })

  it('discards a transcription completed after the selected worker revision changes', async () => {
    const fixture = habitatFixture()
    const props = reactive({
      worker: fixture.fleet.workers[0],
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    let resolve!: (value: { text: string }) => void
    voice.transcribe.mockImplementationOnce(
      () =>
        new Promise((yes) => {
          resolve = yes
        }),
    )
    const post = vi
      .spyOn(api, 'post')
      .mockRejectedValue(new Error('stale transcription must not reach the network'))
    const mounted = await mountComponent(HabitatInspector, props)

    button(mounted.el, 'Voice to selected worker').click()
    await vi.waitFor(() => expect(voice.sink).toBeTypeOf('function'))
    const pending = voice.sink!(new Blob(['voice']))
    await vi.waitFor(() => expect(resolve).toBeTypeOf('function'))
    props.worker = { ...props.worker, revision: props.worker.revision + 1 }
    await nextTick()
    resolve({ text: 'This stale result must not become a draft' })
    await pending
    await nextTick()

    expect(mounted.el.querySelector('textarea')).toBeNull()
    expect(mounted.el.textContent).not.toContain('This stale result must not become a draft')
    expect(post).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('renders source evidence, gates unknown ownership, and focuses explicit destructive confirmation', async () => {
    const fixture = habitatFixture()
    const props = reactive({
      worker: fixture.fleet.workers[0],
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    const mounted = await mountComponent(HabitatInspector, props)
    expect(mounted.el.textContent).toContain('Assignment history')
    button(mounted.el, 'Refresh assignment history').click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('No assignment changes'))
    expect(button(mounted.el, 'Stop').disabled).toBe(false)
    button(mounted.el, 'Stop').click()
    await nextTick()
    await nextTick()
    expect(document.activeElement?.textContent).toBe('Confirm stop')
    expect(mounted.el.querySelector('[aria-label="Confirm stop"]')).not.toBeNull()
    button(mounted.el, 'Cancel').click()
    await nextTick()
    expect(document.activeElement).toBe(button(mounted.el, 'Stop'))
    button(mounted.el, 'Stop').click()
    await nextTick()
    props.worker = fixture.fleet.workers[1]
    await nextTick()
    expect(button(mounted.el, 'Stop').disabled).toBe(true)
    expect(mounted.el.querySelector('[aria-label="Confirm stop"]')).toBeNull()
    expect(mounted.el.textContent).toContain('Ownership or live activity is not proven')
    await mounted.unmount()
  })
  it('does not publish suppressed progress or stale ETA and keeps plain empty evidence', async () => {
    const fixture = habitatFixture()
    fixture.fleet.workers[0].delivery_trust.progress_percent = 77
    fixture.fleet.workers[0].delivery_trust.progress_trusted = false
    const mounted = await mountComponent(HabitatInspector, {
      worker: fixture.fleet.workers[0],
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: false,
      authority: 'fixture:1',
    })
    expect(mounted.el.textContent).not.toContain('77%')
    expect(button(mounted.el, 'Message').disabled).toBe(true)
    expect(button(mounted.el, 'Restart / repair options')).not.toBeNull()
    await mounted.unmount()
  })
})
