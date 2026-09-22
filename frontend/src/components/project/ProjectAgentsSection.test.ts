import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'

import { api } from '@/api/client'
import type { ProjectAgent } from '@/types'
import ProjectAgentsSection from './ProjectAgentsSection.vue'
import { listProjectAgents } from '@/services/projectAgents'

vi.mock('@/api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
  errMsg: (_e: unknown, fallback: string) => fallback,
}))

vi.mock('@/services/projectAgents', () => ({
  listProjectAgents: vi.fn(),
  createProjectAgent: vi.fn(),
  updateProjectAgent: vi.fn(),
  deleteProjectAgent: vi.fn(),
  validateAgentName: (name: string) => (name.trim() ? '' : 'Name is required.'),
}))

async function settle() {
  for (let i = 0; i < 5; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function agent(overrides: Partial<ProjectAgent> = {}): ProjectAgent {
  return {
    id: 1,
    project_id: 9,
    name: 'codex',
    description: 'Implementation agent',
    slash_command_name: 'codex',
    lane_tags: ['dev'],
    metadata: {},
    body: 'Own implementation work.',
    bootstrap_steps: [
      { title: 'Read doctrine', command: 'paimos onboard --project PAI', rationale: '' },
    ],
    non_negotiable_rules: [{ title: 'No secrets', body: '', memory_ref: '' }],
    ...overrides,
  }
}

function run(overrides: Record<string, unknown> = {}) {
  return {
    id: 42,
    issue_id: 656,
    project_id: 9,
    device_id: 'laptop',
    action_key: 'codex_cli.implement',
    provider_label: 'Codex CLI',
    agent_name: 'codex',
    status: 'failed',
    version: '0.2.0',
    deploy_target: '',
    tests_summary: null,
    error: 'failed',
    created_at: '2026-07-07 09:00:00',
    started_at: null,
    finished_at: null,
    ...overrides,
  }
}

function mountSection(canWrite = false, reviewer = false) {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const Host = defineComponent({
    render() {
      return h(ProjectAgentsSection, { projectId: 9, canWrite, reviewer })
    },
  })
  const app = createApp(Host)
  app.mount(el)
  return {
    el,
    unmount() {
      app.unmount()
      el.remove()
    },
  }
}

describe('ProjectAgentsSection launchpad', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset()
    vi.mocked(listProjectAgents).mockReset()
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
  })

  it('shows reviewer agent definitions without mounting or fetching the launchpad', async () => {
    vi.mocked(listProjectAgents).mockResolvedValue([agent()])
    const {el, unmount} = mountSection(false, true)
    await settle()
    expect(el.textContent).toContain('codex')
    expect(el.querySelector('[aria-label="Agent launchpad"]')).toBeNull()
    expect(el.querySelector('.pa-error')).toBeNull()
    expect(api.get).not.toHaveBeenCalled()
    unmount()
  })

  it('shows artifact links, runner adapters, commands, and recent runs for an agent', async () => {
    vi.mocked(listProjectAgents).mockResolvedValue([agent()])
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/projects/9/runners') {
        return {
          runners: [
            {
              user_id: 1,
              device_id: 'laptop',
              last_seen: '',
              actions: [
                {
                  action_key: 'codex_cli.implement',
                  provider_kind: 'local_cli',
                  provider_id: 'codex_cli',
                  label: 'Codex CLI',
                  run_modes: ['edit'],
                  can_test: true,
                  can_deploy: false,
                },
              ],
            },
          ],
        }
      }
      if (path === '/projects/9/runs') return { runs: [run()] }
      return {}
    })

    const { el, unmount } = mountSection()
    await settle()

    expect(el.textContent).toContain('codex')
    expect(el.textContent).toContain('Artifact ready')
    expect(el.textContent).toContain('1 runner online')
    expect(el.textContent).toContain('Codex CLI')
    expect(el.textContent).toContain('paimos skill render codex')
    expect(el.textContent).toContain('paimos session start --project 9 --agent codex')
    expect(el.textContent).toContain('paimos run-agent watch --project 9 --repo-root .')
    expect(el.textContent).toContain('Failed')
    expect(el.textContent).toContain('#42')

    const hrefs = [...el.querySelectorAll<HTMLAnchorElement>('a')].map((a) =>
      a.getAttribute('href'),
    )
    expect(hrefs).toContain('/api/projects/9/agents/codex.json')
    expect(hrefs).toContain('/api/projects/9/agents/codex.md')
    expect(hrefs).toContain('/projects/9/issues/656#ai-workbench')
    unmount()
  })

  it('explains missing runner capability and missing runs without hiding declared agents', async () => {
    vi.mocked(listProjectAgents).mockResolvedValue([
      agent({ body: '', bootstrap_steps: [], non_negotiable_rules: [] }),
    ])
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/projects/9/runners') return { runners: [] }
      if (path === '/projects/9/runs') return { runs: [] }
      return {}
    })

    const { el, unmount } = mountSection()
    await settle()

    expect(el.textContent).toContain('Artifact shell')
    expect(el.textContent).toContain('No runner online')
    expect(el.textContent).toContain('No compatible adapter online.')
    expect(el.textContent).toContain('No runs yet for this agent.')
    unmount()
  })

  it('labels a completed implementation distinctly from verified tests', async () => {
    vi.mocked(listProjectAgents).mockResolvedValue([agent()])
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/projects/9/runners') return { runners: [] }
      if (path === '/projects/9/runs') return { runs: [run({ status: 'completed', error: '' })] }
      return {}
    })

    const { el, unmount } = mountSection()
    await settle()
    expect(el.textContent).toContain('Implemented')
    expect(el.textContent).not.toContain('Tests passed')
    unmount()
  })

  it('uses the empty state to tell users what is missing before launch commands work', async () => {
    vi.mocked(listProjectAgents).mockResolvedValue([])
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/projects/9/runners') return { runners: [] }
      if (path === '/projects/9/runs') return { runs: [] }
      return {}
    })

    const { el, unmount } = mountSection()
    await settle()

    expect(el.textContent).toContain('No agents declared yet.')
    expect(el.textContent).toContain('Add one before rendering a skill')
    unmount()
  })
})
