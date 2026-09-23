import { describe, expect, it } from 'vitest'
import {
  backspaceProse,
  clipboardPlain,
  createProseHistory,
  deleteProseRange,
  enterProse,
  insertProseText,
  insertSoftBreak,
  offerBlockExceedsPage,
  offerBlockOverflowMessage,
  persistProse,
  proseNodes,
} from './offerProse'
import type { OfferTextNode } from './types'

const paragraph = (text: string): OfferTextNode => ({ kind: 'paragraph', text })

describe('offer prose text', () => {
  it('keeps the tail when inserting or pasting into a paragraph', () => {
    const nodes = [paragraph('Hello world')]
    expect(insertProseText(nodes, { index: 0, offset: 5 }, ' TEST').nodes[0]?.text).toBe(
      'Hello TEST world',
    )
    expect(insertProseText(nodes, { index: 0, offset: 5 }, '').nodes).toEqual(nodes)
    expect(
      insertProseText(nodes, { index: 0, offset: 11 }, '\nNext').nodes.map((node) => node.text),
    ).toEqual(['Hello world', 'Next'])
  })

  it('replaces a selection without dropping the unselected suffix', () => {
    const nodes = [paragraph('Hello world')]
    const selected = { anchor: { index: 0, offset: 6 }, focus: { index: 0, offset: 11 } }
    expect(insertProseText(nodes, selected, 'there').nodes[0]?.text).toBe('Hello there')
    expect(backspaceProse(nodes, selected).nodes[0]?.text).toBe('Hello ')
    expect(
      enterProse(nodes, {
        anchor: { index: 0, offset: 2 },
        focus: { index: 0, offset: 4 },
      }).nodes.map((node) => node.text),
    ).toEqual(['He', 'o world'])
  })

  it('deletes across paragraphs and keeps both outer tails', () => {
    const nodes = [paragraph('Hello'), paragraph('world')]
    const edit = deleteProseRange(nodes, {
      anchor: { index: 0, offset: 3 },
      focus: { index: 1, offset: 2 },
    })
    expect(edit.nodes).toEqual([paragraph('Helrld')])
    expect(edit.caret).toEqual({ index: 0, offset: 3 })
  })

  it('refuses an overlong paste instead of truncating it', () => {
    const nodes = [paragraph('Hello world')]
    const edit = insertProseText(nodes, { index: 0, offset: 5 }, 'x'.repeat(2001))
    expect(edit.error).toMatch(/zu lang/)
    expect(edit.nodes).toEqual(nodes)
  })

  it('uses Enter to split and Shift+Enter to keep a soft break in the same node', () => {
    const nodes = [paragraph('Hello world')]
    expect(enterProse(nodes, { index: 0, offset: 5 }).nodes.map((node) => node.text)).toEqual([
      'Hello',
      ' world',
    ])
    const soft = insertSoftBreak(nodes, { index: 0, offset: 5 })
    expect(soft.nodes).toEqual([paragraph('Hello\n world')])
    expect(soft.caret).toEqual({ index: 0, offset: 6 })
  })

  it('exits an empty bullet one level at a time', () => {
    const nested = [{ kind: 'item' as const, text: '', depth: 1 }]
    const once = enterProse(nested, { index: 0, offset: 0 })
    expect(once.nodes).toEqual([{ kind: 'item', text: '' }])
    expect(enterProse(once.nodes, { index: 0, offset: 0 }).nodes).toEqual([paragraph('')])
  })

  it('keeps legacy multiline text literal, including markdown-like lines', () => {
    const legacy = '- Punkt\n- Zweiter'
    expect(proseNodes(legacy, undefined)).toEqual([paragraph(legacy)])
    expect(persistProse([paragraph(legacy)])).toEqual({ body: legacy })
    expect(insertProseText([paragraph(legacy)], { index: 0, offset: 7 }, 'X').nodes[0]?.text).toBe(
      '- PunktX\n- Zweiter',
    )
    expect(
      proseNodes('sichtbar', [{ kind: 'html', text: '<b>x</b>' }] as unknown as OfferTextNode[]),
    ).toEqual([paragraph('sichtbar')])
  })

  it('stores a list as nodes and a plain projection that is not parsed back from hyphens', () => {
    const nodes: OfferTextNode[] = [
      paragraph('Einleitung.'),
      { kind: 'item', text: 'Analyse' },
      { kind: 'item', text: 'Interviews', depth: 1 },
      paragraph('Abschluss.'),
    ]
    const stored = persistProse(nodes)
    expect(stored.body).toBe('Einleitung.\n• Analyse\n  ◦ Interviews\nAbschluss.')
    expect(stored.nodes).toEqual(nodes)
    expect(proseNodes('- nicht\n- lesen', stored.nodes)).toEqual(nodes)
  })

  it('turns pasted HTML into text lines and drops scripts', () => {
    const text = clipboardPlain('', '<ul><li>Eins</li><li><script>alert(1)</script>Zwei</li></ul>')
    expect(text).toBe('Eins\nZwei')
    expect(text).not.toContain('alert')
    const pasted = insertProseText([paragraph('')], { index: 0, offset: 0 }, '- eins\n- zwei')
    expect(pasted.nodes.every((node) => node.kind === 'paragraph')).toBe(true)
    expect(pasted.nodes.map((node) => node.text)).toEqual(['- eins', '- zwei'])
  })

  it('undoes and redoes a replacement without losing the original text', () => {
    const history = createProseHistory()
    const before = { nodes: [paragraph('Hello world')], caret: { index: 0, offset: 6 } }
    history.push(before)
    const after = insertProseText(
      before.nodes,
      { anchor: { index: 0, offset: 6 }, focus: { index: 0, offset: 11 } },
      'there',
    )
    const undone = history.undo({ nodes: after.nodes, caret: after.caret })
    expect(undone?.nodes).toEqual(before.nodes)
    expect(history.redo({ nodes: undone!.nodes, caret: undone!.caret })?.nodes[0]?.text).toBe(
      'Hello there',
    )
  })

  it('reports a whole block that cannot fit on a page without dropping it from the message', () => {
    expect(offerBlockExceedsPage(900, 800, 20, 0)).toBe(true)
    expect(offerBlockExceedsPage(100, 800, 20, 40)).toBe(false)
    expect(offerBlockOverflowMessage(0)).toBe(
      'Textbaustein 1 ist länger als eine Seite. Bitte kürzen oder auf mehrere Bausteine verteilen.',
    )
  })
})
