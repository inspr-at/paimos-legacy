import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
const fixture = vi.hoisted(() => ({
  role: 'reviewer',
  query: {} as Record<string, string | undefined>,
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: fixture.query }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { role: fixture.role } }) }))
vi.mock('@/components/habitat/HabitatControlRoom.vue', () => ({
  default: { template: '<div>Shared home</div>' },
}))
vi.mock('./Paimos6SessionsView.vue', () => ({
  __esModule: true,
  default: { template: '<div>Product sessions surface</div>' },
}))
import Preview from './Paimos6PreviewView.vue'
afterEach(() => {
  document.body.innerHTML = ''
  fixture.role = 'reviewer'
  fixture.query = {}
})
describe('reviewer home surface', () => {
  it.each([{ view: 'sessions' }, { session: 'retained-session' }])(
    'never mounts sessions for a reviewer with %j',
    async (query) => {
      fixture.query = query
      const mounted = await mountComponent(Preview)
      expect(mounted.el.textContent).toContain('Shared home')
      expect(mounted.el.textContent).not.toContain('Product sessions surface')
      await mounted.unmount()
    },
  )
  it('preserves member product sessions', async () => {
    fixture.role = 'member'
    fixture.query = { view: 'sessions' }
    const mounted = await mountComponent(Preview)
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('Product sessions surface'))
    await mounted.unmount()
  })
})
