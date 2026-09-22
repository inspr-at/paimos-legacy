import { defineComponent, h, nextTick, provide, reactive } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { loadOrchestration } from '@/services/orchestration'
import { fetchAgentModeSnapshot, type AgentModeSnapshot } from '@/services/agentMode'
import { PAIMOS6_COMMAND_CONTEXT_KEY, type Paimos6CommandContext } from '@/v6/commandPaletteContext'
import { habitatFixture } from './__fixtures__/orchestration'
const context = vi.hoisted(() => ({
  route: { query: { view: 'workers' } as Record<string, string> },
  replace: vi.fn(),
}))
const voice = vi.hoisted(() => ({
  state: { value: 'idle' },
  level: { value: 0 },
  errorMessage: { value: null },
  isActive: { value: false },
  start: vi.fn(async () => true),
  finish: vi.fn(() => true),
  stop: vi.fn(),
  micSupported: vi.fn(() => true),
}))
vi.mock('vue-router', () => ({
  useRoute: () => context.route,
  useRouter: () => ({ replace: context.replace }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))
const authFixture = vi.hoisted(() => ({
  user: { id: 1, role: 'member', status: 'active' },
  allProjects: true,
  accessibleProjects: new Map(),
  canEdit: () => true,
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authFixture,
}))
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
vi.mock('@/services/agentMode', () => ({ fetchAgentModeSnapshot: vi.fn() }))
vi.mock('@/composables/useMicTranscript', () => ({
  useMicTranscript: () => voice,
}))
vi.mock('@/services/agentModeVoice', () => ({ transcribeAgentModeAudio: vi.fn() }))
vi.mock('@/v6/sessionHomeZoom', () => ({
  loadPaimos6SessionZoom: vi.fn().mockResolvedValue({
    totals: { sessions: 0, exception_messages: 0, action_requests: 0, attention_sessions: 0 },
  }),
}))
import HabitatControlRoom from './HabitatControlRoom.vue'
let commandContext: Paimos6CommandContext | null = null
const CommandContextHost = defineComponent({
  setup() {
    provide(PAIMOS6_COMMAND_CONTEXT_KEY, (next) => {
      commandContext = next
    })
    return () => h(HabitatControlRoom)
  },
})
afterEach(() => {
  authFixture.user.role = 'member'
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  commandContext = null
  document.body.innerHTML = ''
})
describe('Habitat production composition', () => {
  it('routes the command context to voice on the selected real inspector', async () => {
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(CommandContextHost)
    await vi.waitFor(() =>
      expect(mounted.el.querySelector('.habitat-worker-select')).not.toBeNull(),
    )
    mounted.el.querySelector<HTMLButtonElement>('.habitat-worker-select')!.click()
    await vi.waitFor(() =>
      expect(mounted.el.querySelector('[aria-label="Inspector"]')).not.toBeNull(),
    )

    commandContext!.openTalk()
    await vi.waitFor(() => expect(voice.start).toHaveBeenCalledTimes(1))
    expect(context.replace).not.toHaveBeenCalled()
    expect(mounted.el.querySelector('[aria-label="Confirm simple"]')).not.toBeNull()
    await mounted.unmount()
  })

  it('routes command-context talk without a worker to the exact sessions conversation query', async () => {
    context.route = reactive({ query: { view: 'workers', project: '1', zoom: '10' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(CommandContextHost)
    await vi.waitFor(() => expect(commandContext).not.toBeNull())

    commandContext!.openTalk()
    expect(context.replace).toHaveBeenCalledWith({
      query: { view: 'sessions', project: '1', zoom: '10', talk: '1' },
    })
    expect(voice.start).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('makes a compact inspector modal and restores its worker trigger on Escape', async () => {
    vi.stubGlobal(
      'matchMedia',
      vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() })),
    )
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() =>
      expect(mounted.el.querySelector('.habitat-worker-select')).not.toBeNull(),
    )
    const trigger = mounted.el.querySelector<HTMLButtonElement>('.habitat-worker-select')!
    trigger.focus()
    trigger.click()
    await nextTick()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Inspector"]')?.getAttribute('aria-modal')).toBe(
      'true',
    )
    expect(mounted.el.querySelector<HTMLElement>('.habitat-stage')?.inert).toBe(true)
    const close = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Close inspector"]')!
    expect(document.activeElement).toBe(close)
    close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Inspector"]')).toBeNull()
    expect(mounted.el.querySelector<HTMLElement>('.habitat-stage')?.inert).toBe(false)
    expect(document.activeElement).toBe(trigger)
    expect(mounted.el.textContent).not.toContain('no longer in this authorized sample')
    await mounted.unmount()
  })
  it('keeps explicit hierarchy and selected button focus through an ordinary live update', async () => {
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() => expect(mounted.el.querySelector('[data-worker-id]')).not.toBeNull())
    expect(mounted.el.querySelectorAll('[data-worker-id]')).toHaveLength(1)
    mounted.el
      .querySelector<HTMLButtonElement>('[aria-label="Expand coordinator descendants"]')!
      .click()
    await nextTick()
    expect(mounted.el.querySelectorAll('[data-worker-id]')).toHaveLength(2)
    const select = mounted.el.querySelector<HTMLButtonElement>('.habitat-worker-select')!
    select.click()
    select.focus()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Inspector"]')?.textContent).toContain(
      'coordinator',
    )
    const refresh = [...mounted.el.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) => b.textContent?.trim() === 'Refresh',
    )!
    refresh.click()
    await vi.waitFor(() => expect(loadOrchestration).toHaveBeenCalledTimes(2))
    await nextTick()
    expect(document.activeElement).toBe(select)
    await mounted.unmount()
  })
  it('offers recovery for an empty worker tree and never invents an idle presence', async () => {
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('10', null, true))
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() =>
      expect(mounted.el.textContent).toContain('No worker generations in this sample'),
    )
    expect(mounted.el.querySelectorAll('[data-worker-id]')).toHaveLength(0)
    expect(mounted.el.textContent).toContain('Set up a worker')
    expect(mounted.el.textContent).not.toContain('Idle')
    await mounted.unmount()
  })

  it('hides account settings from a reviewer when refresh fails and keeps them for an ordinary user', async () => {
    context.route = reactive({ query: { view: 'home' } })
    vi.mocked(loadOrchestration).mockRejectedValue(new Error('invalid orchestration response'))
    vi.mocked(fetchAgentModeSnapshot).mockRejectedValue(new Error('offline'))
    authFixture.user.role = 'reviewer'
    const reviewer = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() =>
      expect(reviewer.el.textContent).toContain('The control room could not be refreshed.'),
    )
    expect(reviewer.el.textContent).toContain('Retry')
    expect(reviewer.el.textContent).toContain('Open portfolio')
    expect(reviewer.el.textContent).not.toContain('Account settings')
    await reviewer.unmount()

    authFixture.user.role = 'member'
    const member = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() =>
      expect(member.el.textContent).toContain('The control room could not be refreshed.'),
    )
    expect(member.el.textContent).toContain('Retry')
    expect(member.el.textContent).toContain('Open portfolio')
    expect(member.el.textContent).toContain('Account settings')
    await member.unmount()
  })
})
