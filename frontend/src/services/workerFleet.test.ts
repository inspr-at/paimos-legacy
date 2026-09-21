/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import { loadWorkerFleetTicket } from './workerFleet'

vi.mock('@/api/client', () => ({ api: { get: vi.fn() } }))

function worker(overrides: Record<string, unknown> = {}) {
  return {
    harness_session_id: '00000000-0000-4000-8000-000000000907',
    agent: { id: 7, name: 'scout-worker' },
    machine_id: 'vienna-builder-1',
    workspace_provenance: { kind: 'git_worktree', mode: 'exclusive' },
    dispatch_profile: { model: 'gpt-6-sol', effort: 'xhigh' },
    account_label: 'chatgpt',
    management_mode: 'managed',
    runtime_provenance_trust: 'managed_reporter',
    ticket: { id: 907 },
    work_shape: 'scout',
    work_contract: { shape: 'scout', output_kind: 'investigation_evidence' },
    ...overrides,
  }
}

describe('worker fleet ticket companion', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads the authorized project endpoint and keeps only the requested ticket axes', async () => {
    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [worker(), worker({ ticket: { id: 908 } })],
    })
    const result = await loadWorkerFleetTicket(6, 907)
    expect(api.get).toHaveBeenCalledWith('/agent-mode/projects/6/worker-fleet/v2?zoom=100')
    expect(result.workers).toEqual([
      expect.objectContaining({
        machineId: 'vienna-builder-1',
        workspace: { kind: 'git_worktree', mode: 'exclusive' },
        dispatch: { model: 'gpt-6-sol', effort: 'xhigh' },
        accountLabel: 'chatgpt',
        managementMode: 'managed',
        runtimeProvenanceTrust: 'managed_reporter',
        shape: 'scout',
        outputKind: 'investigation_evidence',
      }),
    ])
  })

  it('preserves legacy unknown provenance and rejects shape inference or malformed bounds', async () => {
    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [
        worker({
          machine_id: null,
          workspace_provenance: null,
          dispatch_profile: null,
          account_label: 'unknown',
          runtime_provenance_trust: 'untrusted',
          work_shape: 'unknown',
          work_contract: { shape: 'unknown', output_kind: 'unclassified' },
        }),
      ],
    })
    expect((await loadWorkerFleetTicket(6, 907)).workers[0]).toMatchObject({
      workspace: null,
      dispatch: null,
      shape: 'unknown',
    })

    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [worker({ work_shape: 'ship' })],
    })
    await expect(loadWorkerFleetTicket(6, 907)).rejects.toThrow('invalid worker fleet response')

    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: Array.from({ length: 101 }, () => worker()),
    })
    await expect(loadWorkerFleetTicket(6, 907)).rejects.toThrow('invalid worker fleet response')
  })

  it('requires untrusted workers to suppress every caller-supplied runtime axis', async () => {
    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [
        worker({
          machine_id: null,
          workspace_provenance: null,
          dispatch_profile: null,
          account_label: 'unknown',
          management_mode: 'unmanaged',
          runtime_provenance_trust: 'untrusted',
        }),
      ],
    })
    expect((await loadWorkerFleetTicket(6, 907)).workers[0]).toMatchObject({
      machineId: null,
      workspace: null,
      dispatch: null,
      accountLabel: 'unknown',
      managementMode: 'unmanaged',
      runtimeProvenanceTrust: 'untrusted',
    })

    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [worker({ runtime_provenance_trust: 'untrusted' })],
    })
    await expect(loadWorkerFleetTicket(6, 907)).rejects.toThrow('invalid worker fleet response')
  })

  it('loads a valid pi_context worker and still rejects unknown account labels', async () => {
    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [worker({ account_label: 'pi_context' })],
    })
    expect((await loadWorkerFleetTicket(6, 907)).workers[0]).toMatchObject({
      accountLabel: 'pi_context',
    })

    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [worker({ account_label: 'cursor_context' })],
    })
    expect((await loadWorkerFleetTicket(6, 907)).workers[0]).toMatchObject({
      accountLabel: 'cursor_context',
    })

    vi.mocked(api.get).mockResolvedValue({
      schema_version: 2,
      sample_truncated: false,
      workers: [worker({ account_label: 'not-a-label' })],
    })
    await expect(loadWorkerFleetTicket(6, 907)).rejects.toThrow('invalid worker fleet response')
  })

  it('rejects frozen v1 responses on the explicit v2 companion', async () => {
    vi.mocked(api.get).mockResolvedValue({
      schema_version: 1,
      sample_truncated: false,
      workers: [],
    })
    await expect(loadWorkerFleetTicket(6, 907)).rejects.toThrow('invalid worker fleet response')
  })
})
