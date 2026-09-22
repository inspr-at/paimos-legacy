import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { habitatFixture } from './__fixtures__/orchestration'
import HabitatHome from './HabitatHome.vue'
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))
vi.mock('./HabitatRuntimeHealth.vue', () => ({ default: { template: '<div data-testid="connections-poller" />' } }))
afterEach(() => {
  document.body.innerHTML = ''
})
describe('Home presents actual next steps', () => {
  it('omits reviewer setup, sessions links and the connections polling component', async () => {
    const mounted = await mountComponent(HabitatHome, {
      snapshot: habitatFixture('10', null, true), deliveries: [], messages: [],
      messageState: 'ready', deliveryState: 'ready', fresh: true,
      authority: 'reviewer:1', reviewer: true,
    })
    expect(mounted.el.querySelector('[data-testid="connections-poller"]')).toBeNull()
    expect(mounted.el.querySelector('.habitat-welcome')).toBeNull()
    expect(mounted.el.querySelector('.habitat-roster-root')).toBeNull()
    expect(mounted.el.textContent).not.toContain('Set up coordinator')
    expect(mounted.el.textContent).not.toContain('Messages')
    await mounted.unmount()
  })
  it('shows intentional stops as history while preserving coordinator start and independent attention', async () => {
    const snapshot = habitatFixture()
    for (const worker of snapshot.fleet.workers) {
      worker.phase = 'stopped'
      worker.liveness.state = 'dead'
      worker.liveness.reason = 'stopped'
      worker.liveness.closed_reason = 'stopped'
      worker.delivery_trust.reason = 'ticket_unbound'
      worker.recent_communication = []
    }
    snapshot.instance_root.active_generation = {
      state: 'unset',
      reason: 'no_active_root_generation',
      session_id: null,
    }
    snapshot.project_coordination[0].coordinator = {
      state: 'unset',
      reason: 'no_active_coordinator',
      session_id: null,
    }
    const onAssign = vi.fn()
    const props = reactive({
      snapshot,
      deliveries: [],
      messages: [],
      messageState: 'ready' as const,
      deliveryState: 'ready' as const,
      fresh: true,
      authority: 'fixture:1',
      onAssign,
    })
    const mounted = await mountComponent(HabitatHome, props)
    expect(mounted.el.querySelector('.habitat-worker-request')).toBeNull()
    expect(
      [...mounted.el.querySelectorAll('.habitat-roster-state')].map((el) => el.textContent),
    ).toEqual(['Stopped', 'Stopped'])
    expect(mounted.el.textContent).not.toContain('Available')
    expect(mounted.el.textContent).not.toContain('Disconnected')
    expect(mounted.el.textContent).toContain('2known workers')
    expect(mounted.el.textContent).toContain('Coordinator needed')
    const start = mounted.el.querySelector<HTMLButtonElement>('.habitat-roster-root button')!
    expect(start.textContent?.trim()).toBe('Review start')
    start.click()
    expect(onAssign).toHaveBeenCalledOnce()

    props.snapshot.fleet.workers[1].delivery_trust.reason = 'terminal_failed'
    await nextTick()
    const requests = mounted.el.querySelectorAll('.habitat-worker-request')
    expect(requests).toHaveLength(1)
    expect(requests[0].textContent).toContain(props.snapshot.fleet.workers[1].agent.name)
    expect(requests[0].textContent).toContain('terminal failed')
    expect(requests[0].textContent).not.toContain('Disconnected')
    expect(mounted.el.querySelectorAll('.habitat-roster-state')[1].textContent).toBe('Stopped')

    props.snapshot.fleet.workers[1].delivery_trust.reason = 'ticket_unbound'
    props.snapshot.fleet.workers[1].recent_communication = [
      {
        message_id: 'test',
        delivery_id: null,
        direction: 'incoming',
        attribution: 'project_agent',
        requested_level: 'simple',
        effective_level: null,
        state: null,
        fallback_code: null,
        error_code: 'delivery_unavailable',
        occurred_at: '2026-09-06T12:00:00Z',
      },
    ]
    await nextTick()
    expect(mounted.el.querySelectorAll('.habitat-worker-request')).toHaveLength(1)
    expect(mounted.el.querySelector('.habitat-worker-request')?.textContent).not.toContain(
      'Disconnected',
    )
    expect(mounted.el.querySelectorAll('.habitat-roster-state')[1].textContent).toBe('Stopped')
    await mounted.unmount()
  })
  it('distinguishes missing identity, missing run, and uncertain coordinator without inventing workers', async () => {
    const props = reactive({
      snapshot: habitatFixture('10', null, true),
      deliveries: [],
      messages: [],
      messageState: 'ready' as const,
      deliveryState: 'ready' as const,
      fresh: true,
      authority: 'fixture:1',
    })
    const mounted = await mountComponent(HabitatHome, props)
    expect(mounted.el.textContent).toContain('Set up coordinator')
    expect(mounted.el.textContent).toContain('No workers yet')
    expect(mounted.el.querySelector('.habitat-roster-row')).toBeNull()
    expect(mounted.el.querySelector('.habitat-welcome')).not.toBeNull()
    props.snapshot.instance_root.configured_identity =
      habitatFixture().instance_root.configured_identity
    props.snapshot.instance_root.active_generation.state = 'unset'
    props.snapshot.instance_root.active_generation.reason = 'no_active_root_generation'
    await nextTick()
    expect(mounted.el.textContent).toContain('Start coordinator')
    expect(mounted.el.textContent).not.toContain('Set up a worker')
    props.snapshot.instance_root.active_generation.state = 'unknown'
    props.snapshot.instance_root.active_generation.reason = 'root_generation_unknown'
    await nextTick()
    expect(mounted.el.textContent).toContain('Review coordinator')
    expect(mounted.el.textContent).not.toContain('connected')
    await mounted.unmount()
  })
  it('labels unavailable attention and retained dead workers without claiming an all-clear or active work', async () => {
    const snapshot = habitatFixture()
    snapshot.fleet.workers[0].liveness.state = 'dead'
    const mounted = await mountComponent(HabitatHome, {
      snapshot,
      deliveries: [],
      messages: [{ projectId: 1, totals: null }],
      messageState: 'ready',
      deliveryState: 'unavailable',
      fresh: true,
      authority: 'fixture:1',
    })
    expect(mounted.el.textContent).toContain('Some attention is unknown')
    expect(mounted.el.textContent).toContain('Your workers')
    expect(mounted.el.textContent).toContain('Disconnected')
    expect(mounted.el.textContent).not.toContain('Nothing needs your attention')
    expect(mounted.el.textContent).not.toContain('At work')
    await mounted.unmount()
  })

  it('does not claim all-clear when the scoped project or worker sample is incomplete', async () => {
    const snapshot = habitatFixture()
    snapshot.coordination_bounds = {
      sample_limit: 10,
      total_projects: 11,
      sampled_projects: 1,
      omitted_projects: 10,
    }
    snapshot.fleet.sample_truncated = true
    snapshot.fleet.totals.omitted_workers = 1
    const mounted = await mountComponent(HabitatHome, {
      snapshot,
      deliveries: [],
      messages: [{ projectId: 1, totals: { ...emptyMessageTotals() } }],
      messageState: 'ready',
      deliveryState: 'ready',
      fresh: true,
      authority: 'fixture:1',
      attentionOnly: true,
    })
    expect(mounted.el.textContent).toContain('Attention coverage is incomplete')
    expect(mounted.el.textContent).not.toContain('Nothing needs your attention')
    await mounted.unmount()
  })
})

function emptyMessageTotals() {
  return {
    sessions: 0,
    unread: 0,
    attention_sessions: 0,
    exception_messages: 0,
    action_requests: 0,
    exception_targets: 0,
    sampled_exception_targets: 0,
  }
}
