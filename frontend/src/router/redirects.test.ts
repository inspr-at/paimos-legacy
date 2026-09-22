import { describe, expect, it } from 'vitest'

import router from './index'
import {
  postLoginRedirectOrFallback,
  safePostLoginRedirect,
  reviewerRouteAllowed,
} from './redirects'

describe('post-login redirects', () => {
  it('accepts same-origin app paths including query strings', () => {
    expect(safePostLoginRedirect('/issues/PAI-265')).toBe('/issues/PAI-265')
    expect(safePostLoginRedirect('/projects/6/issues/PAI-265?edit=1')).toBe(
      '/projects/6/issues/PAI-265?edit=1',
    )
  })

  it('rejects external, protocol-relative, and login-loop targets', () => {
    for (const value of [
      'https://example.com/issues/1',
      '//example.com/issues/1',
      'issues/1',
      '/login',
      '/login?redirect=/issues/1',
      '/login/reset',
    ]) {
      expect(safePostLoginRedirect(value), `${value} should be rejected`).toBeNull()
    }
  })

  it('uses the first redirect value and falls back when no safe value exists', () => {
    expect(postLoginRedirectOrFallback(['/issues/1', '/issues/2'])).toBe('/issues/1')
    expect(postLoginRedirectOrFallback('/login', '/portal')).toBe('/portal')
  })

  it('drops principal-bound Agent Mode state from a post-login return path', () => {
    expect(
      postLoginRedirectOrFallback(
        '/agent-mode?delivery=SECRET_A&project=6&lane=project%3A6%2Fepic%3A1&q=PRIVATE&state=active&attention=required&health=blocked&detail=1',
      ),
    ).toBe('/agent-mode')
    expect(postLoginRedirectOrFallback('/agent-mode?unknown=PRIVATE_CANARY#old')).toBe(
      '/agent-mode',
    )
    expect(postLoginRedirectOrFallback('/agent-mode/?delivery=SECRET_SLASH')).toBe('/agent-mode')
    expect(postLoginRedirectOrFallback('/Agent-Mode/?q=PRIVATE_MIXED_CASE')).toBe('/agent-mode')
  })

  it.each(['/agent-mode/?delivery=PRIVATE_SLASH', '/Agent-Mode/?q=PRIVATE_MIXED_CASE#old'])(
    'canonicalizes router-equivalent Agent Mode target %s',
    (target) => {
      expect(router.resolve(target).matched.some((record) => record.path === '/agent-mode')).toBe(
        true,
      )
      expect(safePostLoginRedirect(target)).toBe('/agent-mode')
    },
  )
})

it('restricts human reviewer navigation to readable shared-project screens', () => {
  for (const path of [
    '/',
    '/projects',
    '/issues',
    '/projects/33',
    '/projects/33/issues/7',
    '/issues/7',
  ])
    expect(reviewerRouteAllowed(path), path).toBe(true)
  for (const path of [
    '/settings',
    '/legacy',
    '/reporting',
    '/intake',
    '/agent-mode',
    '/customers',
    '/crm',
    '/unknown',
    '/projects/33/admin',
  ])
    expect(reviewerRouteAllowed(path), path).toBe(false)
})

it('rejects product sessions and action-only home query routes for reviewers', () => {
  for (const view of ['sessions', 'assign', 'talk', 'unknown'])
    expect(reviewerRouteAllowed('/', { view })).toBe(false)
  expect(reviewerRouteAllowed('/', { session: 'retained-session-id' })).toBe(false)
  for (const view of ['home', 'workers', 'projects', 'attention'])
    expect(reviewerRouteAllowed('/', { view, project: '33' })).toBe(true)
})
