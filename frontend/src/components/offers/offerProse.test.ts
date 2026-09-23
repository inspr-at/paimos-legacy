import { describe, expect, it } from 'vitest'
import {
  applyStructure,
  backspaceProse,
  clipboardPlain,
  createProseHistory,
  deleteForwardProse,
  deleteProseRange,
  enterProse,
  insertProseText,
  insertSoftBreak,
  offerBlockExceedsPage,
  offerBlockOverflowMessage,
  parseProseNodes,
  persistProse,
  projectProse,
  proseNodes,
  reconcileProseTexts,
  setBulletMarker,
  setListKind,
  toggleItem,
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

  it('numbers nested lists and keeps an absent marker on the old glyphs', () => {
    const legacy = persistProse([
      paragraph('Einleitung.'),
      { kind: 'item', text: 'Analyse' },
      { kind: 'item', text: 'Interviews', depth: 1 },
    ])
    expect(legacy.body).toBe('Einleitung.\n• Analyse\n  ◦ Interviews')
    expect(legacy.nodes?.[0]).toEqual(paragraph('Einleitung.'))
    const numbered = [
      paragraph('Einleitung.'),
      { kind: 'item' as const, text: 'Analyse', marker: 'decimal' as const },
      { kind: 'item' as const, text: 'Interviews', depth: 1, marker: 'decimal' as const },
      { kind: 'item' as const, text: 'Punkt', marker: 'disc' as const },
      { kind: 'item' as const, text: 'Weiter', marker: 'decimal' as const },
    ]
    expect(projectProse(numbered)).toBe(
      'Einleitung.\n1. Analyse\n  1. Interviews\n• Punkt\n1. Weiter',
    )
    expect(parseProseNodes([{ kind: 'item', text: 'x', marker: 'image' }])).toBeNull()
    const squared = applyStructure(numbered, { index: 1, offset: 0 }, (nodes, index) =>
      setBulletMarker(nodes, index, 'square'),
    )
    expect(squared.nodes[1]?.marker).toBe('square')
    const cleared = applyStructure([squared.nodes[1]!], { index: 0, offset: 0 }, (nodes, index) =>
      setListKind(nodes, index, 'none'),
    )
    expect(cleared.nodes[0]).toEqual(paragraph('Analyse'))
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

  it('shrinks a legacy body that is already past the node cap', () => {
    const legacy = 'A'.repeat(2002)
    const nodes = proseNodes(legacy, null)
    const edit = backspaceProse(nodes, { index: 0, offset: legacy.length })
    expect(edit.error).toBeUndefined()
    expect(edit.nodes[0]?.text).toBe('A'.repeat(2001))
    expect(persistProse(edit.nodes)).toEqual({ body: 'A'.repeat(2001) })
    const converted = applyStructure(nodes, { index: 0, offset: 0 }, toggleItem)
    expect(converted.error).toMatch(/zu lang/)
    expect(converted.nodes).toEqual(nodes)
    expect(persistProse(converted.nodes)).toEqual({ body: legacy })
    const split = enterProse(nodes, { index: 0, offset: 1 })
    expect(split.error).toMatch(/zu lang/)
    expect(split.nodes).toEqual(nodes)
  })

  it('deletes one user-perceived character, including an emoji', () => {
    const text = 'A😀B'
    const back = backspaceProse([paragraph(text)], { index: 0, offset: text.length - 1 })
    expect(back.nodes[0]?.text).toBe('AB')
    expect(back.nodes[0]?.text).not.toMatch(/\uD83D|\uDE00/)
    const forward = deleteForwardProse([paragraph(text)], { index: 0, offset: 1 })
    expect(forward.nodes[0]?.text).toBe('AB')
    const cluster = `A${'e\u0301'}B`
    expect(
      backspaceProse([paragraph(cluster)], { index: 0, offset: cluster.length - 1 }).nodes[0]?.text,
    ).toBe('AB')
    expect(backspaceProse([paragraph('AB')], { index: 0, offset: 2 }).nodes[0]?.text).toBe('A')
  })

  it('keeps carriage returns in a legacy paragraph and strips them when converting to a list', () => {
    const legacy = 'Alpha\r\nBeta'
    expect(proseNodes(legacy, null)).toEqual([paragraph(legacy)])
    const shrunk = backspaceProse(proseNodes(legacy, null), { index: 0, offset: legacy.length })
    expect(shrunk.nodes[0]?.text).toBe('Alpha\r\nBet')
    expect(persistProse(shrunk.nodes)).toEqual({ body: 'Alpha\r\nBet' })
    const converted = applyStructure(
      [paragraph(legacy)],
      { index: 0, offset: legacy.length },
      toggleItem,
    )
    expect(converted.error).toBeUndefined()
    expect(converted.nodes).toEqual([{ kind: 'item', text: 'Alpha\nBeta' }])
    expect(converted.caret.offset).toBe('Alpha\nBeta'.length)
    expect(parseProseNodes(converted.nodes)).toEqual(converted.nodes)
    expect(parseProseNodes([{ kind: 'item', text: legacy }])).toBeNull()
  })

  it('keeps parent text that wraps a nested list when HTML has no plain-text alternative', () => {
    const nested = clipboardPlain(
      '',
      '<ul><li><span>Parent</span><ul><li>Child</li></ul></li></ul>',
    )
    expect(nested).toBe('Parent\nChild')
    const around = clipboardPlain('', '<ul><li>Before<ul><li>Child</li></ul>After</li></ul>')
    expect(around).toBe('Before\nChild\nAfter')
  })

  it('adopts a spelling replacement from visible text and drops injected markup', () => {
    const edit = reconcileProseTexts([paragraph('helo world')], ['hello world'], {
      index: 0,
      offset: 5,
    })
    expect(edit.error).toBeUndefined()
    expect(edit.nodes).toEqual([paragraph('hello world')])
    const tagged = reconcileProseTexts([paragraph('helo')], ['hello'], { index: 0, offset: 5 })
    expect(tagged.nodes[0]?.text).toBe('hello')
    expect(tagged.nodes[0]?.text).not.toContain('<')
  })

  it('reports a whole block that cannot fit on a page without dropping it from the message', () => {
    expect(offerBlockExceedsPage(900, 800, 20, 0)).toBe(true)
    expect(offerBlockExceedsPage(100, 800, 20, 40)).toBe(false)
    expect(offerBlockOverflowMessage(0)).toBe(
      'Textbaustein 1 ist länger als eine Seite. Bitte kürzen oder auf mehrere Bausteine verteilen.',
    )
  })
})
