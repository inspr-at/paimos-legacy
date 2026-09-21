import { describe, expect, it } from 'vitest'
import { parseOrchestrationSnapshot } from '@/services/orchestration'
import {
  canControlWorker,
  recentCommunication,
  workerNeedsAttention,
  workerRows,
} from './habitatModel'
import { habitatFixture } from './__fixtures__/orchestration'
import { parseDispatchProfiles, parseRootConfig, workerStartCommand } from './habitatSetup'

function stoppedWorker() {
  const worker = habitatFixture().fleet.workers[0]
  worker.phase = 'stopped'
  worker.liveness.state = 'dead'
  worker.liveness.reason = 'stopped'
  worker.liveness.closed_reason = 'stopped'
  worker.delivery_trust.reason = 'ticket_unbound'
  worker.recent_communication = []
  return worker
}

describe('Habitat truth and setup boundaries', () => {
  it('accepts current catalog metadata, Pi selectors and Cursor default effort', () => {
    const base = habitatFixture().fleet.workers[0].dispatch_profile!
    const profiles = [
      { ...base, id: 'codex-astra-xhigh', version: '2', model: 'gpt-6-astra', effort: 'xhigh', family: 'openai', tier: 'frontier' },
      { ...base, id: 'pi-anthropic-sonnet-high', version: '2', harness: 'pi', model: 'anthropic/claude-sonnet-5', effort: 'high', family: 'anthropic', tier: 'standard' },
      { ...base, id: 'cursor-composer', version: '2', harness: 'cursor', model: 'composer-2.5', effort: 'default', family: 'cursor', tier: 'standard' },
      { ...base, id: 'cursor-grok-xhigh', version: '2', harness: 'cursor', model: 'grok-4.7-xhigh', effort: 'xhigh', family: 'xai', tier: 'frontier' },
    ]
    expect(parseDispatchProfiles({ dispatch_profiles: profiles })).toEqual(profiles)
    for (const change of [{ family: 'unknown' }, { tier: 'unknown' }, { effort: 'default' }, { unexpected: true }]) {
      expect(() => parseDispatchProfiles({ dispatch_profiles: [{ ...profiles[0], ...change }] })).toThrow()
    }
  })

  it('keeps an explicitly stopped generation quiet without granting live controls', () => {
    const worker = stoppedWorker()
    expect(workerNeedsAttention(worker)).toBe(false)
    expect(canControlWorker(worker, 'stop', true, true)).toBe(false)
  })
  it.each(['process_failed', 'ownership_lost', 'process_exited', undefined])(
    'retains attention for terminal history with close reason %s',
    (reason) => {
      const worker = stoppedWorker()
      worker.liveness.closed_reason = reason
      expect(workerNeedsAttention(worker)).toBe(true)
    },
  )
  it('retains attention for dead working generations and unknown evidence', () => {
    const worker = stoppedWorker()
    worker.phase = 'working'
    expect(workerNeedsAttention(worker)).toBe(true)
    worker.phase = 'stopped'
    worker.liveness.state = 'unknown'
    expect(workerNeedsAttention(worker)).toBe(true)
  })
  it.each(['waiting_on_human', 'blocked', 'terminal_failed'] as const)(
    'retains independent %s delivery attention after an intentional stop',
    (reason) => {
      const worker = stoppedWorker()
      worker.delivery_trust.reason = reason
      expect(workerNeedsAttention(worker)).toBe(true)
    },
  )
  it.each(['error_code', 'fallback_code'] as const)(
    'retains independent communication %s after an intentional stop',
    (field) => {
      const worker = stoppedWorker()
      worker.recent_communication = [
        {
          message_id: 'test',
          delivery_id: null,
          direction: 'incoming',
          attribution: 'project_agent',
          requested_level: 'simple',
          effective_level: null,
          state: null,
          fallback_code: null,
          error_code: null,
          occurred_at: '2026-09-06T12:00:00Z',
          [field]: 'delivery_unavailable',
        },
      ]
      expect(workerNeedsAttention(worker)).toBe(true)
    },
  )
  it('uses the canonical parser for every fixture zoom and empty shape', () => {
    for (const zoom of ['1', '10', '100', '1000'])
      for (const empty of [false, true])
        expect(() => parseOrchestrationSnapshot(habitatFixture(zoom, null, empty))).not.toThrow()
  })
  it('builds only explicit parent relationships and counts collapsed sampled descendants', () => {
    const workers = habitatFixture().fleet.workers
    const collapsed = workerRows(workers, new Set())
    expect(collapsed).toHaveLength(1)
    expect(collapsed[0].hidden).toBe(1)
    expect(
      workerRows(workers, new Set([workers[0].harness_session_id])).map((row) => row.depth),
    ).toEqual([0, 1])
    workers[1].parent_harness_session_id = null
    expect(workerRows(workers, new Set()).map((row) => row.depth)).toEqual([0, 0])
  })
  it('never promotes unknown, untrusted, stale or forbidden ownership into controls', () => {
    const worker = habitatFixture().fleet.workers[0]
    expect(canControlWorker(worker, 'stop', true, true)).toBe(true)
    expect(canControlWorker(worker, 'stop', false, true)).toBe(false)
    expect(canControlWorker(worker, 'stop', true, false)).toBe(false)
    worker.liveness.source = 'control_plane'
    expect(canControlWorker(worker, 'stop', true, true)).toBe(false)
    worker.liveness.source = 'agentd_reporter'
    worker.liveness.state = 'unknown'
    expect(canControlWorker(worker, 'stop', true, true)).toBe(false)
    expect(workerNeedsAttention(worker)).toBe(true)
    expect(canControlWorker(habitatFixture().fleet.workers[1], 'stop', true, true)).toBe(false)
  })
  it('uses bounded message timestamps, never heartbeat or inferred talking', () => {
    const worker = habitatFixture().fleet.workers[0]
    worker.recent_communication = []
    expect(recentCommunication(worker, Date.now())).toBe(false)
    worker.recent_communication = [
      {
        message_id: 'test',
        delivery_id: null,
        direction: 'incoming',
        attribution: 'project_agent',
        requested_level: 'simple',
        effective_level: null,
        state: null,
        fallback_code: null,
        error_code: null,
        occurred_at: '2026-09-06T12:00:00Z',
      },
    ]
    expect(recentCommunication(worker, Date.parse('2026-09-06T12:00:59Z'))).toBe(true)
    expect(recentCommunication(worker, Date.parse('2026-09-06T12:01:01Z'))).toBe(false)
    expect(recentCommunication(worker, Date.parse('2026-09-06T11:59:59Z'))).toBe(false)
  })
  it('refuses invalid root revisions and ambiguous immutable profile catalogs', () => {
    expect(
      parseRootConfig({ schema_version: 1, revision: 0, orchestrator: null, updated_at: null })
        .revision,
    ).toBe(0)
    expect(() =>
      parseRootConfig({ schema_version: 1, revision: -1, orchestrator: null, updated_at: null }),
    ).toThrow()
    const profile = habitatFixture().fleet.workers[0].dispatch_profile!
    expect(parseDispatchProfiles({ dispatch_profiles: [profile] })).toEqual([profile])
    expect(() => parseDispatchProfiles({ dispatch_profiles: [profile, profile] })).toThrow()
    const input = {
      instance: 'fixture',
      deployment: 'fixture',
      project: 'PAI',
      agent: 'coordinator',
      profile,
      ticket: 'PAI-927',
      shape: 'ship',
    }
    expect(workerStartCommand(input)).toContain("--expect-deployment-instance 'fixture'")
    expect(workerStartCommand(input)).toContain("--project 'PAI'")
    expect(workerStartCommand({ ...input, instance: 'bad;command' })).toBeNull()
    expect(workerStartCommand({ ...input, ticket: 'BAD-927' })).toBeNull()
    expect(workerStartCommand({ ...input, deployment: 'default' })).toBeNull()
  })
})
