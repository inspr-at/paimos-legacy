/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineComponent, h, nextTick, ref } from 'vue'

import { mountComponent } from '@/components/ai/testMount'
import ProjectFooterBar, { type ProjectPrimaryTab } from './ProjectFooterBar.vue'

describe('ProjectFooterBar (PAI-967 mobile footer)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('keeps tab selection and hides Settings without edit permission', async () => {
    const selected = ref<ProjectPrimaryTab>('overview')
    const Harness = defineComponent({
      setup() {
        return () =>
          h(ProjectFooterBar, {
            modelValue: selected.value,
            openIssues: 7,
            canEditSettings: false,
            'onUpdate:modelValue': (value: ProjectPrimaryTab) => {
              selected.value = value
            },
          })
      },
    })
    const mounted = await mountComponent(Harness)
    const tabs = mounted.el.querySelectorAll('[role="tab"]')
    expect(tabs.length).toBe(7)
    expect(mounted.el.querySelector('.pfb__tab--settings')).toBeNull()

    ;(tabs[0] as HTMLButtonElement).click()
    await nextTick()
    expect(selected.value).toBe('issues')

    await mounted.unmount()
  })

  it('keeps only readable project tabs for a restricted reviewer', async () => {
    const mounted = await mountComponent(ProjectFooterBar, {modelValue: 'issues', reviewer: true})
    expect([...mounted.el.querySelectorAll('[role="tab"]')].map(tab => tab.getAttribute('aria-label')))
      .toEqual(['Issues', 'Overview', 'Knowledge', 'Agents', 'Context'])
    await mounted.unmount()
  })

  it('exposes explicit accessible names for icon-only tabs', async () => {
    const mounted = await mountComponent(ProjectFooterBar, {
      modelValue: 'issues',
      openIssues: 7,
      knowledgeEntries: 0,
      coopPopulated: true,
      canEditSettings: true,
    })
    const issues = mounted.el.querySelector('[role="tab"][aria-label="Issues, 7"]')
    const knowledge = mounted.el.querySelector('[role="tab"][aria-label="Knowledge, 0"]')
    const coop = mounted.el.querySelector('[role="tab"][aria-label="Coop, populated"]')
    const settings = mounted.el.querySelector('[role="tab"][aria-label="Settings"]')

    expect(issues).not.toBeNull()
    expect(knowledge).not.toBeNull()
    expect(coop).not.toBeNull()
    expect(settings).not.toBeNull()

    await mounted.unmount()
  })

  it('uses a horizontally scrollable tab strip', () => {
    const source = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), 'ProjectFooterBar.vue'),
      'utf8',
    )
    expect(source).toMatch(/\.pfb\s*\{[\s\S]*overflow-x:\s*auto/)
    expect(source).toMatch(/\.pfb__tab\s*\{[\s\S]*flex-shrink:\s*0/)
    expect(source).toMatch(/scroll-margin-inline/)
  })

  it('reveals the focused tab inside the horizontal strip', async () => {
    const mounted = await mountComponent(ProjectFooterBar, {
      modelValue: 'knowledge',
      openIssues: 7,
      agentCount: 0,
      canEditSettings: true,
    })
    const nav = mounted.el.querySelector('.pfb') as HTMLElement
    const agents = mounted.el.querySelector('[aria-label="Agents, 0"]') as HTMLButtonElement
    Object.defineProperty(nav, 'clientWidth', { configurable: true, value: 240 })
    Object.defineProperty(nav, 'scrollWidth', { configurable: true, value: 900 })
    nav.scrollLeft = 0
    const reveal = vi.spyOn(agents, 'scrollIntoView')

    agents.focus()
    await nextTick()

    expect(document.activeElement).toBe(agents)
    expect(reveal).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })

    await mounted.unmount()
  })
})
