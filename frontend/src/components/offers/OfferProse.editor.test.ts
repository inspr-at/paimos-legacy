import { afterEach, describe, expect, it } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'
import OfferProse from './OfferProse.vue'
import {
  provideOfferProseSession,
  type OfferProseSession,
  type ProseCommand,
} from './offerProseSession'
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
  const vm = app.mount(el) as unknown as { format: (command: ProseCommand) => void }
  await nextTick()
  return {
    el,
    updates,
    vm,
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
    mounted.vm.format({ type: 'list', kind: 'bullet' })
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
    expect(lastUpdate(mounted.updates)?.nodes?.map((node) => node.text)).toEqual([
      'Analyse',
      '\n Punkt',
    ])
    expect(lastUpdate(mounted.updates)?.nodes?.every((node) => node.kind === 'item')).toBe(true)
    mounted.unmount()
  })

  it('keeps a composed character when the selection renders again', async () => {
    const mounted = await mount({ body: 'A' })
    const text = mounted.root.querySelector<HTMLElement>('[data-text]')!
    mounted.root.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    text.textContent = 'Aü'
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    expect(text.textContent).toBe('Aü')
    mounted.root.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: 'ü' }))
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('Aü')
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    expect(mounted.root.querySelector('[data-text]')?.textContent).toBe('Aü')
    mounted.unmount()
  })

  it('applies a spelling replacement and keeps it after the next render', async () => {
    const mounted = await mount({ body: 'helo' })
    place(mounted.root, 0, 0, 4)
    const before = new InputEvent('beforeinput', {
      bubbles: true,
      cancelable: true,
      inputType: 'insertReplacementText',
      data: 'hello',
    })
    mounted.root.dispatchEvent(before)
    mounted.root.dispatchEvent(
      new InputEvent('input', { bubbles: true, inputType: 'insertReplacementText', data: 'hello' }),
    )
    await nextTick()
    expect(before.defaultPrevented).toBe(true)
    expect(lastUpdate(mounted.updates)?.body).toBe('hello')
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    expect(mounted.root.querySelector('[data-text]')?.textContent).toBe('hello')
    mounted.unmount()
  })

  it('reconciles a native spelling change without keeping injected markup', async () => {
    const mounted = await mount({ body: 'helo world' })
    const text = mounted.root.querySelector<HTMLElement>('[data-text]')!
    text.innerHTML = '<b>hello</b> world'
    mounted.root.dispatchEvent(
      new InputEvent('input', { bubbles: true, inputType: 'insertReplacementText' }),
    )
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('hello world')
    expect(mounted.root.querySelector('b')).toBeNull()
    expect(mounted.root.querySelector('[data-text]')?.textContent).toBe('hello world')
    mounted.unmount()
  })

  it('shrinks an overlong legacy body and does not emit it as a list', async () => {
    const legacy = 'A'.repeat(2002)
    const mounted = await mount({ body: legacy })
    place(mounted.root, 0, legacy.length)
    mounted.vm.format({ type: 'list', kind: 'bullet' })
    await nextTick()
    expect(mounted.updates).toEqual([])
    expect(mounted.el.querySelector('[role="alert"]')?.textContent).toMatch(/zu lang/)
    place(mounted.root, 0, legacy.length)
    key(mounted.root, { key: 'Backspace' })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('A'.repeat(2001))
    expect(lastUpdate(mounted.updates)?.nodes).toBeUndefined()
    mounted.unmount()
  })

  it('converts a CRLF legacy body into a list without a carriage return', async () => {
    const mounted = await mount({ body: 'Alpha\r\nBeta' })
    place(mounted.root, 0, 'Alpha\r\nBeta'.length)
    mounted.vm.format({ type: 'list', kind: 'bullet' })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.nodes).toEqual([{ kind: 'item', text: 'Alpha\nBeta' }])
    expect(mounted.root.textContent).not.toContain('\r')
    mounted.unmount()
  })

  it('deletes a whole emoji instead of one code unit', async () => {
    const mounted = await mount({ body: 'A😀' })
    place(mounted.root, 0, 'A😀'.length)
    key(mounted.root, { key: 'Backspace' })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('A')
    const forward = await mount({ body: 'A😀' })
    place(forward.root, 0, 1)
    key(forward.root, { key: 'Delete' })
    await nextTick()
    expect(lastUpdate(forward.updates)?.body).toBe('A')
    mounted.unmount()
    forward.unmount()
  })

  it('pastes nested html without dropping the parent text', async () => {
    const mounted = await mount({ body: '' })
    place(mounted.root, 0, 0)
    const transfer = new DataTransfer()
    transfer.setData('text/html', '<ul><li><span>Parent</span><ul><li>Child</li></ul></li></ul>')
    const paste = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(paste, 'clipboardData', { value: transfer })
    mounted.root.dispatchEvent(paste)
    await nextTick()
    expect(lastUpdate(mounted.updates)?.body).toBe('Parent\nChild')
    expect(mounted.root.querySelector('span span, ul')).toBeNull()
    mounted.unmount()
  })

  it('types at the same character after formatting a CRLF paragraph', async () => {
    const mounted = await mount({ body: 'A\r\nBC' })
    place(mounted.root, 0, 3)
    mounted.vm.format({ type: 'list', kind: 'bullet' })
    await nextTick()
    expect(lastUpdate(mounted.updates)?.nodes?.[0]?.text).toBe('A\nBC')
    mounted.root.dispatchEvent(
      new InputEvent('beforeinput', {
        bubbles: true,
        cancelable: true,
        inputType: 'insertText',
        data: 'X',
      }),
    )
    await nextTick()
    expect(lastUpdate(mounted.updates)?.nodes?.[0]?.text).toBe('A\nXBC')
    mounted.unmount()
  })

  it('drops the list target when a nonfocusable heading is clicked and keeps it for the title bar', async () => {
    const updates: Update[] = []
    let session!: OfferProseSession
    let format!: (command: ProseCommand) => void
    const el = document.createElement('div')
    document.body.appendChild(el)
    const Host = defineComponent({
      setup() {
        session = provideOfferProseSession()
        return () =>
          h('div', { class: 'offer-document' }, [
            h('div', { 'data-offer-chrome': 'true' }, [
              h('button', { type: 'button', class: 'list-button' }, 'Listen & Punkte'),
            ]),
            h('h3', { class: 'plain-heading' }, 'Leistung'),
            h(OfferProse, {
              body: 'Alpha',
              editable: true,
              label: 'Textbaustein 1',
              onUpdate: (value: Update) => updates.push(value),
              onVnodeMounted: (vnode) => {
                format = (vnode.component?.exposed as { format: (command: ProseCommand) => void })
                  .format
              },
            }),
          ])
      },
    })
    const app = createApp(Host)
    app.mount(el)
    await nextTick()
    const root = el.querySelector<HTMLElement>('.offer-prose')!
    root.focus()
    place(root, 0, 1)
    document.dispatchEvent(new Event('selectionchange'))
    await nextTick()
    expect(session.active.value).not.toBeNull()
    el.querySelector('.list-button')!.dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, cancelable: true }),
    )
    expect(session.active.value).not.toBeNull()
    format({ type: 'list', kind: 'bullet' })
    await nextTick()
    expect(lastUpdate(updates)?.nodes?.[0]).toMatchObject({ kind: 'item', text: 'Alpha' })
    el.querySelector('h3')!.dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, cancelable: true }),
    )
    expect(session.active.value).toBeNull()
    const before = updates.length
    format({ type: 'list', kind: 'none' })
    await nextTick()
    expect(updates).toHaveLength(before)
    app.unmount()
    el.remove()
  })
})
