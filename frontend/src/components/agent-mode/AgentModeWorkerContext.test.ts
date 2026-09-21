/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'

import { mountComponent } from '@/components/ai/testMount'
import i18n from '@/i18n'
import de from '@/i18n/de'
import en from '@/i18n/en'
import type { WorkerFleetTicketContext } from '@/services/workerFleet'
import AgentModeWorkerContext from './AgentModeWorkerContext.vue'

const scoutWorker = {
  harnessSessionId: 'session-907',
  agentName: 'worker-907',
  machineId: 'vienna-builder-1',
  workspace: { kind: 'git_worktree', mode: 'exclusive' },
  dispatch: { model: 'gpt-6-sol', effort: 'xhigh' },
  accountLabel: 'chatgpt',
  managementMode: 'managed',
  runtimeProvenanceTrust: 'managed_reporter',
  shape: 'scout',
  outputKind: 'investigation_evidence',
} as const

function leafKeys(value: object, prefix = ''): string[] {
  return Object.entries(value)
    .flatMap(([key, child]) => {
      const path = prefix ? `${prefix}.${key}` : key
      return typeof child === 'object' && child !== null ? leafKeys(child, path) : [path]
    })
    .sort()
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

async function mountMutable(
  initial: { projectId: number; ticketId: number },
  loader: (projectId: number, ticketId: number) => Promise<WorkerFleetTicketContext>,
) {
  const props = reactive({ ...initial })
  const el = document.createElement('div')
  document.body.appendChild(el)
  const app = createApp({
    render: () =>
      h(AgentModeWorkerContext, {
        projectId: props.projectId,
        ticketId: props.ticketId,
        loader,
      }),
  })
  app.use(i18n)
  app.mount(el)
  await nextTick()
  return {
    el,
    props,
    async unmount() {
      app.unmount()
      el.remove()
      await nextTick()
    },
  }
}

describe('AgentModeWorkerContext', () => {
  afterEach(() => {
    i18n.global.locale.value = 'en'
  })

  it('loads only on request and renders bounded provenance plus scout semantics', async () => {
    const loader = vi.fn().mockResolvedValue({
      sampleTruncated: false,
      workers: [scoutWorker],
    })
    const mounted = await mountComponent(AgentModeWorkerContext, {
      projectId: 6,
      ticketId: 907,
      loader,
    })
    expect(loader).not.toHaveBeenCalled()
    await mounted.el.querySelector<HTMLButtonElement>('button')!.click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('vienna-builder-1'))
    expect(loader).toHaveBeenCalledWith(6, 907)
    const text = mounted.el.textContent ?? ''
    expect(text).toContain('Git worktree · Exclusive')
    expect(text).toContain('gpt-6-sol · Extra high')
    expect(text).toContain('ChatGPT')
    expect(text).toContain('Managed reporter')
    expect(text).toContain('Investigation evidence')
    expect(text).toContain(
      'Scout means investigation evidence. Delivery work requires an explicit reassignment.',
    )
    expect(text).not.toContain('enforces Git')
    await mounted.unmount()
  })

  it('renders the complete worker vocabulary in German without English fallback', async () => {
    i18n.global.locale.value = 'de'
    const loader = vi.fn().mockResolvedValue({ sampleTruncated: false, workers: [scoutWorker] })
    const mounted = await mountComponent(AgentModeWorkerContext, {
      projectId: 6,
      ticketId: 907,
      loader,
    })
    expect(mounted.el.textContent).toContain('Worker-Kontext anzeigen')
    await mounted.el.querySelector<HTMLButtonElement>('button')!.click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('vienna-builder-1'))
    const text = mounted.el.textContent ?? ''
    expect(text).toContain('Maschine')
    expect(text).toContain('Git-Worktree · Exklusiv')
    expect(text).toContain('gpt-6-sol · Sehr hoch')
    expect(text).toContain('Verwalteter Reporter')
    expect(text).toContain('Untersuchungsevidenz')
    expect(text).toContain(
      'Scout bedeutet Untersuchungsevidenz. Lieferarbeit erfordert eine ausdrückliche Neuzuweisung.',
    )
    expect(text).not.toContain('Show worker context')
    await mounted.unmount()
  })

  it('renders unmanaged legacy truth while keeping caller-supplied runtime axes suppressed', async () => {
    const loader = vi.fn().mockResolvedValue({
      sampleTruncated: false,
      workers: [
        {
          harnessSessionId: 'legacy',
          agentName: 'legacy-worker',
          machineId: null,
          workspace: null,
          dispatch: null,
          accountLabel: 'unknown',
          managementMode: 'unmanaged',
          runtimeProvenanceTrust: 'untrusted',
          shape: 'unknown',
          outputKind: 'unclassified',
        },
      ],
    })
    const mounted = await mountComponent(AgentModeWorkerContext, {
      projectId: 6,
      ticketId: 907,
      loader,
    })
    await mounted.el.querySelector<HTMLButtonElement>('button')!.click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('remains unclassified'))
    expect((mounted.el.textContent ?? '').match(/Unknown/g)?.length).toBeGreaterThanOrEqual(4)
    expect(mounted.el.textContent).toContain('Unmanaged')
    expect(mounted.el.textContent).toContain('Untrusted')
    expect(mounted.el.textContent).toContain('Runtime axes are suppressed')
    expect(mounted.el.textContent).not.toContain('legacy-machine')
    await mounted.unmount()
  })

  it('keeps every worker-context catalog leaf in English/German parity', () => {
    expect(leafKeys(en.agentMode.workerContext)).toEqual(leafKeys(de.agentMode.workerContext))
  })

  it('clears successful worker provenance as soon as the selected ticket changes', async () => {
    const loader = vi.fn().mockResolvedValue({ sampleTruncated: false, workers: [scoutWorker] })
    const mounted = await mountMutable({ projectId: 6, ticketId: 907 }, loader)
    await mounted.el.querySelector<HTMLButtonElement>('button')!.click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('vienna-builder-1'))

    mounted.props.ticketId = 908
    await nextTick()
    expect(mounted.el.textContent).not.toContain('vienna-builder-1')
    expect(mounted.el.textContent).toContain('Show worker context')
    expect(loader).toHaveBeenCalledTimes(1)
    await mounted.unmount()
  })

  it('discards an older in-flight lookup after a new ticket lookup succeeds', async () => {
    const oldRequest = deferred<WorkerFleetTicketContext>()
    const newRequest = deferred<WorkerFleetTicketContext>()
    const loader = vi
      .fn()
      .mockImplementationOnce(() => oldRequest.promise)
      .mockImplementationOnce(() => newRequest.promise)
    const mounted = await mountMutable({ projectId: 6, ticketId: 907 }, loader)
    await mounted.el.querySelector<HTMLButtonElement>('button')!.click()

    mounted.props.ticketId = 908
    await nextTick()
    await mounted.el.querySelector<HTMLButtonElement>('button')!.click()
    newRequest.resolve({
      sampleTruncated: false,
      workers: [{ ...scoutWorker, harnessSessionId: 'new-session', machineId: 'new-machine' }],
    })
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('new-machine'))

    oldRequest.resolve({
      sampleTruncated: false,
      workers: [{ ...scoutWorker, harnessSessionId: 'old-session', machineId: 'old-machine' }],
    })
    await Promise.resolve()
    await nextTick()
    expect(mounted.el.textContent).toContain('new-machine')
    expect(mounted.el.textContent).not.toContain('old-machine')
    expect(loader.mock.calls).toEqual([
      [6, 907],
      [6, 908],
    ])
    await mounted.unmount()
  })
})
