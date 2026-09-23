import { afterEach, describe, expect, it } from 'vitest'
import { createApp, nextTick } from 'vue'
import OfferProse from './OfferProse.vue'
import type { OfferTextNode } from './types'

type Update = { body: string; nodes?: OfferTextNode[] }

async function mount(props: { body: string; nodes?: OfferTextNode[] }) {
  const updates: Update[] = []
  const el = document.createElement('div')
  document.body.appendChild(el)
  const app = createApp(OfferProse, {
    ...props,
    editable: true,
    label: 'Textbaustein 1',
    onUpdate: (value: Update) => updates.push(value),
  })
  app.mount(el)
  await nextTick()
  return {
    el,
    updates,
    root: el.querySelector<HTMLElement>('.offer-prose')!,
    unmount() {
      app.unmount()
      el.remove()
    },
  }
}

function place(root: ParentNode, index: number, start: number, end = start) {
  const el = root.querySelector<HTMLElement>(`[data-text][data-index="${index}"]`)
  if (!el) throw new Error(`missing text ${index}`)
  const text = el.firstChild
  const range = document.createRange()
  const target = text && text.nodeType === Node.TEXT_NODE ? text : el
  const startOffset = target === el ? 0 : start
  const endOffset = target === el ? 0 : end
  range.setStart(target, startOffset)
  range.setEnd(target, endOffset)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
  document.dispatchEvent(new Event('selectionchange'))
}

function key(root: HTMLElement, init: KeyboardEventInit) {
  root.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
}

function lastUpdate(updates: Update[]) {
  return updates[updates.length - 1]
}

describe('OfferProse editor', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    window.getSelection()?.removeAllRanges()
  })

  it('edits a legacy multiline paragraph without reverting the newline', async () => {
    const mounted = await mount({ body: 'Eins\nZwei' })
    expect(mounted.root.querySelector('[data-text]')?.textContent).toBe('Eins\nZwei')
    expect(mounted.root.querySelector('script')).toBeNull()
    place(mounted.root, 0, 4)
    mounted.root.dispatchEvent(
      new InputEvent('beforeinput', {
        bubbles: true,
        cancelable: true,
        inputType: 'insertText',
        data: 'X',
      }),
    )
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('EinsX\nZwei')
    expect(lastUpdate(mounted.updates)?.nodes).toBeUndefined()
    expect(mounted.root.querySelectorAll('[data-node]')).toHaveLength(1)
    mounted.unmount()
  })

  it('pins the toolbar to the paragraph under the caret', async () => {
    const mounted = await mount({
      body: '',
      nodes: [
        { kind: 'paragraph', text: 'Erster' },
        { kind: 'paragraph', text: 'Zweiter' },
      ],
    })
    place(mounted.root, 1, 2)
    const button = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Liste"]')!
    button.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
    button.click()
    await nextTick()
    const saved = lastUpdate(mounted.updates)
    expect(saved?.nodes?.[0]?.kind).toBe('paragraph')
    expect(saved?.nodes?.[1]).toMatchObject({ kind: 'item', text: 'Zweiter' })
    expect(mounted.root.querySelector('[data-node="1"]')?.getAttribute('data-bullet')).toBe('•')
    mounted.unmount()
  })

  it('selects the whole block and deletes only that text', async () => {
    const mounted = await mount({
      body: '',
      nodes: [
        { kind: 'paragraph', text: 'Hello' },
        { kind: 'item', text: 'world' },
      ],
    })
    mounted.root.focus()
    key(mounted.root, { key: 'a', metaKey: true })
    const selected = window.getSelection()?.toString() ?? ''
    expect(selected).toContain('Hello')
    expect(selected).toContain('world')
    key(mounted.root, { key: 'Backspace' })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('')
    expect(mounted.root.textContent).not.toContain('Hello')
    expect(mounted.el.querySelector('script')).toBeNull()
    mounted.unmount()
  })

  it('replaces a selection on paste and undoes it', async () => {
    const mounted = await mount({ body: 'Hello world' })
    place(mounted.root, 0, 6, 11)
    const transfer = new DataTransfer()
    transfer.setData('text/plain', 'there')
    transfer.setData('text/html', '<b>there</b><script>alert(1)</script>')
    const paste = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(paste, 'clipboardData', { value: transfer })
    mounted.root.dispatchEvent(paste)
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('Hello there')
    expect(mounted.root.querySelector('script, b')).toBeNull()
    key(mounted.root, { key: 'z', metaKey: true })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('Hello world')
    mounted.unmount()
  })

  it('splits on Enter and keeps Shift+Enter inside the same item', async () => {
    const mounted = await mount({ body: '', nodes: [{ kind: 'item', text: 'Analyse Punkt' }] })
    place(mounted.root, 0, 7)
    key(mounted.root, { key: 'Enter', shiftKey: true })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.nodes).toEqual([{ kind: 'item', text: 'Analyse\n Punkt' }])
    place(mounted.root, 0, 7)
    key(mounted.root, { key: 'Enter' })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.nodes?.map((node) => node.text)).toEqual(['Analyse', '\n Punkt'])
    expect(lastUpdate(mounted.updates)?.nodes?.every((node) => node.kind === 'item')).toBe(true)
    mounted.unmount()
  })
})
