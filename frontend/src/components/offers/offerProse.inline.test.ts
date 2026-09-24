import { describe, expect, it } from 'vitest'
import {
  applyInlineStyle,
  clipboardPlain,
  createProseHistory,
  enterProse,
  insertProseText,
  parseProseNodes,
  persistProse,
  proseMarkState,
  rangeAfterStore,
  reconcileProseTexts,
  toggleTypingBits,
} from './offerProse'
import type { OfferTextNode } from './types'

const paragraph = (text: string, marks?: OfferTextNode['marks']): OfferTextNode => ({
  kind: 'paragraph',
  text,
  ...(marks ? { marks } : {}),
})
const item = (text: string, marks?: OfferTextNode['marks']): OfferTextNode => ({
  kind: 'item',
  text,
  marker: 'disc',
  ...(marks ? { marks } : {}),
})
const range = (start: number, end: number, index = 0) => ({
  anchor: { index, offset: start },
  focus: { index, offset: end },
})

describe('inline prose marks', () => {
  it('turns a mixed bold selection on together, then off together', () => {
    const nodes = [
      paragraph('AB', [{ start: 0, end: 2, bold: true }]),
      paragraph('CD'),
    ]
    const across = {
      anchor: { index: 0, offset: 0 },
      focus: { index: 1, offset: 2 },
    }
    const enabled = applyInlineStyle(nodes, across, 'bold')
    expect(enabled.nodes[0]?.marks).toEqual([{ start: 0, end: 2, bold: true }])
    expect(enabled.nodes[1]?.marks).toEqual([{ start: 0, end: 2, bold: true }])
    const cleared = applyInlineStyle(enabled.nodes, across, 'bold')
    expect(cleared.nodes[0]?.marks).toBeUndefined()
    expect(cleared.nodes[1]?.marks).toBeUndefined()
    const italic = applyInlineStyle(
      [
        paragraph('AB', [{ start: 0, end: 2, italic: true }]),
        paragraph('CD', [{ start: 0, end: 1, italic: true }]),
      ],
      across,
      'italic',
    )
    expect(italic.nodes[0]?.marks).toEqual([{ start: 0, end: 2, italic: true }])
    expect(italic.nodes[1]?.marks).toEqual([{ start: 0, end: 2, italic: true }])
  })

  it('keeps a legacy CRLF selection on the same character after the first mark', () => {
    const before = [paragraph('A\r\nBC')]
    const selected = {
      anchor: { index: 0, offset: 3 },
      focus: { index: 0, offset: 4 },
    }
    const styled = applyInlineStyle(before, selected, 'bold')
    const held = rangeAfterStore(before, styled.nodes, selected)
    expect(styled.nodes[0]?.text.slice(held.anchor.offset, held.focus.offset)).toBe('B')
  })

  it('gives an IME insertion the explicitly chosen typing marks', () => {
    const edit = reconcileProseTexts([paragraph('Hi')], ['Hié'], { index: 0, offset: 3 }, 1)
    expect(edit.nodes[0]?.text).toBe('Hié')
    expect(edit.nodes[0]?.marks).toEqual([{ start: 2, end: 3, bold: true }])
  })

  it('styles part of a word and keeps the rest plain', () => {
    const edit = applyInlineStyle([paragraph('Hello')], range(1, 4), 'bold')
    expect(edit.nodes[0]).toEqual(paragraph('Hello', [{ start: 1, end: 4, bold: true }]))
    expect(edit.caret).toEqual({ index: 0, offset: 4 })
  })

  it('toggles bold and italic independently and lets Normal clear both', () => {
    const bold = applyInlineStyle([paragraph('Wort')], range(0, 4), 'bold')
    const both = applyInlineStyle(bold.nodes, range(0, 4), 'italic')
    expect(both.nodes[0]?.marks).toEqual([{ start: 0, end: 4, bold: true, italic: true }])
    const italic = applyInlineStyle(both.nodes, range(0, 4), 'bold')
    expect(italic.nodes[0]?.marks).toEqual([{ start: 0, end: 4, italic: true }])
    const plain = applyInlineStyle(both.nodes, range(0, 4), 'normal')
    expect(plain.nodes[0]?.marks).toBeUndefined()
  })

  it('reports mixed coverage and styles each paragraph in a selection', () => {
    const nodes = [
      paragraph('AB', [{ start: 0, end: 1, bold: true }]),
      paragraph('CD'),
    ]
    const across = {
      anchor: { index: 0, offset: 0 },
      focus: { index: 1, offset: 2 },
    }
    expect(proseMarkState(nodes, across).bold).toBe('mixed')
    const edit = applyInlineStyle(nodes, across, 'italic')
    expect(edit.nodes[0]?.marks).toEqual([
      { start: 0, end: 1, bold: true, italic: true },
      { start: 1, end: 2, italic: true },
    ])
    expect(edit.nodes[1]?.marks).toEqual([{ start: 0, end: 2, italic: true }])
  })

  it('keeps marks on list text and across a split', () => {
    const styled = applyInlineStyle([item('Punkt')], range(0, 2), 'bold')
    expect(styled.nodes[0]?.kind).toBe('item')
    const split = enterProse(styled.nodes, { index: 0, offset: 2 })
    expect(split.nodes[0]?.marks).toEqual([{ start: 0, end: 2, bold: true }])
    expect(split.nodes[1]?.marks).toBeUndefined()
    expect(split.nodes[1]?.kind).toBe('item')
  })

  it('uses UTF-16 offsets and refuses a boundary inside a surrogate pair', () => {
    const text = 'a😀b'
    expect(text.length).toBe(4)
    const edit = applyInlineStyle([paragraph(text)], range(1, 3), 'italic')
    expect(edit.nodes[0]?.marks).toEqual([{ start: 1, end: 3, italic: true }])
    expect(parseProseNodes([{ kind: 'paragraph', text, marks: [{ start: 2, end: 3, bold: true }] }])).toBeNull()
    const stored = persistProse(edit.nodes)
    expect(stored.nodes?.[0]?.marks).toEqual([{ start: 1, end: 3, italic: true }])
    expect(parseProseNodes(stored.nodes)?.[0]?.text).toBe(text)
  })

  it('stores a styled paragraph as nodes and leaves a legacy paragraph as body', () => {
    expect(persistProse([paragraph('Plain')])).toEqual({ body: 'Plain' })
    const styled = persistProse([paragraph('Plain', [{ start: 0, end: 5, bold: true }])])
    expect(styled.nodes).toEqual([paragraph('Plain', [{ start: 0, end: 5, bold: true }])])
    expect(styled.body).toBe('Plain')
  })

  it('undo and redo restore the previous marks and caret', () => {
    const history = createProseHistory()
    const before = { nodes: [paragraph('Hello')], caret: { index: 0, offset: 4 } }
    history.push(before)
    const after = applyInlineStyle(before.nodes, range(0, 4), 'bold')
    const undone = history.undo({ nodes: after.nodes, caret: after.caret })
    expect(undone?.nodes[0]?.marks).toBeUndefined()
    expect(undone?.caret).toEqual(before.caret)
    const redone = history.redo({ nodes: undone!.nodes, caret: undone!.caret })
    expect(redone?.nodes[0]?.marks).toEqual([{ start: 0, end: 4, bold: true }])
  })

  it('types new characters in the chosen style and drops markup from pasted HTML', () => {
    const base = [paragraph('Hi', [{ start: 0, end: 2, bold: true }])]
    const typed = insertProseText(base, { index: 0, offset: 2 }, '!', 0)
    expect(typed.nodes[0]?.text).toBe('Hi!')
    expect(typed.nodes[0]?.marks).toEqual([{ start: 0, end: 2, bold: true }])
    const inherited = insertProseText(base, { index: 0, offset: 2 }, '!')
    expect(inherited.nodes[0]?.marks).toEqual([{ start: 0, end: 3, bold: true }])
    expect(toggleTypingBits(1, 'italic')).toBe(3)
    expect(toggleTypingBits(3, 'normal')).toBe(0)
    expect(clipboardPlain('', '<b>Fett</b> <i>Kursiv</i><script>x</script>')).toBe('Fett Kursiv')
    expect(clipboardPlain('', '<div><span style="color:red">Nur</span> Text</div>')).toBe('Nur Text')
  })
})
