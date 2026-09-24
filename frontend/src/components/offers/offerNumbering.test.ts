import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  applyStructure,
  canonMarkerMm,
  enterProse,
  indentItem,
  insertProseText,
  numberingCommandForIndex,
  OFFER_PROSE_MAX_DEPTH,
  outlineMarkerColumns,
  outdentItem,
  parseProseNodes,
  persistProse,
  projectProse,
  proseLevelMoves,
  proseMarkerLabels,
  setDecimalControl,
  type NumberingMode,
} from './offerProse'
import type { OfferTextNode } from './types'

const paragraph = (text: string): OfferTextNode => ({ kind: 'paragraph', text })

function numberingOp(mode: NumberingMode, start?: number) {
  let position = 0
  return (list: OfferTextNode[], index: number) => {
    const command = numberingCommandForIndex(mode, position)
    position += 1
    return setDecimalControl(list, index, command, start)
  }
}
const outline = (
  text: string,
  depth = 0,
  anchor: Pick<OfferTextNode, 'list_start' | 'list_continue'> = {},
): OfferTextNode => ({
  kind: 'item',
  text,
  ...(depth > 0 ? { depth } : {}),
  marker: 'decimal',
  numbering: 'outline',
  ...anchor,
})

describe('offer outline numbering', () => {
  it('renders multilevel labels, restart, and continue across a paragraph', () => {
    const nodes = [
      outline('A', 0, { list_start: 3 }),
      outline('B', 1),
      outline('C', 2),
      paragraph(''),
      outline('D', 2, { list_continue: true }),
      outline('E', 0, { list_start: 1 }),
      outline('F'),
    ]
    expect(proseMarkerLabels(nodes)).toEqual(['3', '3.1', '3.1.1', '', '3.1.2', '1', '2'])
    expect(projectProse(nodes)).toBe('3 A\n  3.1 B\n    3.1.1 C\n\n    3.1.2 D\n1 E\n2 F')
    const independent = [outline('A', 0, { list_start: 3 }), paragraph(''), outline('B')]
    expect(proseMarkerLabels(independent)).toEqual(['3', '', '1'])
    const plain: OfferTextNode[] = [
      { kind: 'item', text: 'A', marker: 'decimal' },
      { kind: 'item', text: 'B', depth: 1, marker: 'decimal' },
      paragraph(''),
      { kind: 'item', text: 'C', marker: 'decimal' },
    ]
    expect(proseMarkerLabels(plain)).toEqual(['1.', '1.', '', '1.'])
    const continued: OfferTextNode[] = [
      { kind: 'item', text: 'A', marker: 'decimal' },
      paragraph('dazwischen'),
      { kind: 'item', text: 'B', marker: 'decimal', list_continue: true },
    ]
    expect(proseMarkerLabels(continued)).toEqual(['1.', '', '2.'])
  })

  it('keeps a custom start only on the marked item', () => {
    const nodes = [outline('A', 0, { list_start: 3 }), paragraph('p'), outline('B')]
    expect(proseMarkerLabels(setDecimalControl(nodes, 2, 'continue'))[2]).toBe('4')
    expect(proseMarkerLabels(setDecimalControl(nodes, 2, 'restart'))[2]).toBe('1')
    expect(setDecimalControl(nodes, 2, 'start', 8)[2]?.list_start).toBe(8)
    expect(proseMarkerLabels(setDecimalControl(nodes, 2, 'start', 8))[2]).toBe('8')
    expect(setDecimalControl(nodes, 2, 'start', 0)).toEqual(nodes)
    const selection = applyStructure(
      [outline('A'), outline('B', 0, { list_start: 9 })],
      { anchor: { index: 0, offset: 0 }, focus: { index: 1, offset: 1 } },
      (() => {
        let first = true
        return (list: OfferTextNode[], index: number) => {
          const mode = first ? 'start' : 'follow'
          first = false
          return setDecimalControl(list, index, mode, 4)
        }
      })(),
    )
    expect(selection.nodes[0]).toMatchObject({ list_start: 4, numbering: 'outline' })
    expect(selection.nodes[1]?.list_start).toBeUndefined()
    expect(selection.nodes[1]?.list_continue).toBeUndefined()
    expect(proseMarkerLabels(selection.nodes)).toEqual(['4', '5'])
  })

  it('drops start metadata when a line is split, pasted, or indented', () => {
    const started = [outline('AB', 0, { list_start: 3 })]
    const split = enterProse(started, { index: 0, offset: 1 })
    expect(split.nodes[0]).toMatchObject({ text: 'A', list_start: 3, numbering: 'outline' })
    expect(split.nodes[1]).toEqual(outline('B'))
    expect(proseMarkerLabels(split.nodes)).toEqual(['3', '4'])
    const pasted = insertProseText(started, { index: 0, offset: 1 }, '\n')
    expect(pasted.nodes[0]?.list_start).toBe(3)
    expect(pasted.nodes[1]?.list_start).toBeUndefined()
    expect(pasted.nodes[1]?.numbering).toBe('outline')
    const pair = [outline('A', 0, { list_start: 3 }), outline('B', 0, { list_start: 9 })]
    const indented = indentItem(pair, 1)
    expect(indented[0]?.list_start).toBe(3)
    expect(indented[1]?.depth).toBe(1)
    expect(indented[1]?.list_start).toBeUndefined()
    expect(indented[1]?.numbering).toBe('outline')
    expect(proseMarkerLabels(indented)).toEqual(['3', '3.1'])
  })

  it('indents the first item without inventing a parent number and keeps a continued level', () => {
    const started = [outline('A', 0, { list_start: 3 })]
    const indentedFirst = indentItem(started, 0)
    expect(indentedFirst[0]).toMatchObject({ depth: 1, numbering: 'outline' })
    expect(indentedFirst[0]?.list_start).toBeUndefined()
    expect(proseMarkerLabels(indentedFirst)).toEqual(['1'])
    expect(proseMarkerLabels(indentedFirst).join('')).not.toContain('.0')
    expect(parseProseNodes(persistProse(indentedFirst).nodes)).toEqual(indentedFirst)
    expect(outdentItem(indentedFirst, 0)[0]?.depth).toBeUndefined()
    const continued = [
      outline('A', 0, { list_start: 3 }),
      outline('B', 1),
      paragraph(''),
      outline('C', 1, { list_continue: true }),
    ]
    expect(proseMarkerLabels(continued)[3]).toBe('3.2')
    const held = indentItem(continued, 3)
    expect(held[3]).toMatchObject({ depth: 2, list_continue: true, numbering: 'outline' })
    expect(proseMarkerLabels(held)[3]).not.toContain('.0')
    expect(proseMarkerLabels(outdentItem(held, 3))[3]).toBe('3.2')
    const outdented = outdentItem(continued, 3)
    expect(outdented[3]).toMatchObject({ list_continue: true, numbering: 'outline' })
    expect(outdented[3]?.depth).toBeUndefined()
    expect(proseMarkerLabels(outdented)[3]).toBe('4')
    const nested = [
      outline('A', 0, { list_start: 3 }),
      outline('B', 1),
      outline('C', 2),
      paragraph(''),
      outline('D', 2, { list_continue: true }),
    ]
    expect(proseMarkerLabels(nested)).toEqual(['3', '3.1', '3.1.1', '', '3.1.2'])
    const once = outdentItem(nested, 4)
    expect(once[4]).toMatchObject({ depth: 1, list_continue: true, numbering: 'outline' })
    expect(proseMarkerLabels(once)[4]).toBe('3.2')
    expect(parseProseNodes(once)).toEqual(once)
  })

  it('uses the section number as the outline prefix without writing it into the text', () => {
    const bound = (text: string, depth = 0, start?: number): OfferTextNode => ({
      ...outline(text, depth, start ? { list_start: start } : {}),
      section_bound: true,
    })
    const sameLevel = [bound('A'), bound('B')]
    expect(proseMarkerLabels(sameLevel, 2)).toEqual(['2.1', '2.2'])
    expect(sameLevel[0]?.text).toBe('A')
    const nested = [bound('A'), bound('Kind', 1), bound('B')]
    expect(proseMarkerLabels(nested, 2)).toEqual(['2.1', '2.1.1', '2.2'])
    expect(proseMarkerLabels(nested, 3)).toEqual(['3.1', '3.1.1', '3.2'])
    expect(proseMarkerLabels([bound('A', 0, 5)], 2)).toEqual(['2.5'])
    expect(proseMarkerLabels(sameLevel)).toEqual(['1', '2'])
    expect(projectProse(sameLevel, 2)).toBe('2.1 A\n2.2 B')
    expect(parseProseNodes(sameLevel)).toEqual(sameLevel)
  })

  it('keeps each start when numbering ownership changes across a selection', () => {
    const nodes = [outline('A', 0, { list_start: 5 }), outline('B', 0, { list_start: 9 })]
    const range = { anchor: { index: 0, offset: 0 }, focus: { index: 1, offset: 1 } }
    const sectioned = applyStructure(nodes, range, numberingOp('section'))
    expect(sectioned.nodes[0]).toMatchObject({ list_start: 5, section_bound: true })
    expect(sectioned.nodes[1]).toMatchObject({ list_start: 9, section_bound: true })
    expect(sectioned.nodes[1]?.list_continue).toBeUndefined()
    expect(proseMarkerLabels(sectioned.nodes, 2)).toEqual(['2.5', '2.9'])
    const continued = [outline('A', 0, { list_start: 5 }), outline('B', 0, { list_continue: true })]
    const kept = applyStructure(continued, range, numberingOp('section'))
    expect(kept.nodes[1]).toMatchObject({ list_continue: true, section_bound: true })
    expect(kept.nodes[1]?.list_start).toBeUndefined()
    const independent = applyStructure(sectioned.nodes, range, numberingOp('independent'))
    expect(independent.nodes[0]?.list_start).toBe(5)
    expect(independent.nodes[1]?.list_start).toBe(9)
    expect(independent.nodes[0]?.section_bound).toBeUndefined()
    expect(independent.nodes[1]?.section_bound).toBeUndefined()
    expect(proseMarkerLabels(independent.nodes, 2)).toEqual(['5', '9'])
    const restarted = applyStructure(nodes, range, numberingOp('restart'))
    expect(proseMarkerLabels(restarted.nodes)).toEqual(['1', '2'])
    expect(restarted.nodes[1]?.list_start).toBeUndefined()
    const started = applyStructure(
      [outline('A'), outline('B', 0, { list_start: 9 })],
      range,
      numberingOp('start', 4),
    )
    expect(started.nodes[0]?.list_start).toBe(4)
    expect(started.nodes[1]?.list_start).toBeUndefined()
    expect(proseMarkerLabels(started.nodes)).toEqual(['4', '5'])
  })

  it('disables level moves only when the structural command would change nothing', () => {
    const caret = (index: number) => ({ index, offset: 0 })
    const all = { anchor: { index: 0, offset: 0 }, focus: { index: 1, offset: 1 } }
    expect(proseLevelMoves([outline('A')], caret(0))).toMatchObject({
      indent: true,
      outdent: true,
      indentLimit: null,
      outdentLimit: null,
    })
    const siblings = [outline('A'), outline('B')]
    expect(proseLevelMoves(siblings, caret(1)).indent).toBe(true)
    expect(proseLevelMoves(siblings, all).indent).toBe(true)
    const capped = [outline('A'), outline('B', 1)]
    expect(proseLevelMoves(capped, caret(1))).toMatchObject({
      indent: true,
      outdent: true,
      indentLimit: null,
    })
    expect(proseLevelMoves(capped, all)).toMatchObject({
      indent: true,
      outdent: true,
    })
    const chain = Array.from({ length: OFFER_PROSE_MAX_DEPTH + 1 }, (_, depth) =>
      outline(String.fromCharCode(65 + depth), depth),
    )
    expect(proseLevelMoves(chain, caret(OFFER_PROSE_MAX_DEPTH))).toMatchObject({
      indent: false,
      outdent: true,
      indentLimit: 'max-depth',
    })
    const text = [paragraph('A'), paragraph('B')]
    expect(proseLevelMoves(text, all)).toMatchObject({
      indent: true,
      outdent: false,
      indentLimit: null,
      outdentLimit: 'not-list',
    })
    const afterParagraph = [paragraph('p'), outline('A')]
    expect(proseLevelMoves(afterParagraph, all).indent).toBe(true)
    expect(proseLevelMoves(afterParagraph, caret(1))).toMatchObject({
      indent: true,
      indentLimit: null,
    })
  })

  it('indents a bullet under a shallower number and keeps the section labels', () => {
    const bullet = (
      text: string,
      depth: number,
      marker: OfferTextNode['marker'],
    ): OfferTextNode => ({
      kind: 'item',
      text,
      depth,
      marker,
    })
    const nodes = [
      { ...outline('Planungsrahmen', 0, { list_start: 4 }), section_bound: true as const },
      bullet('Projektstart', 1, 'circle'),
      bullet('Monat', 2, 'square'),
      { ...outline('Weiter'), section_bound: true as const },
    ]
    expect(proseMarkerLabels(nodes, 5)).toEqual(['5.4', '◦', '▪', '5.5'])
    expect(proseLevelMoves(nodes, { index: 1, offset: 0 }).indent).toBe(true)
    const indented = indentItem(nodes, 1)
    expect(indented[1]).toMatchObject({ depth: 2, marker: 'circle', text: 'Projektstart' })
    expect(indented[0]?.list_start).toBe(4)
    expect(indented[0]?.depth).toBeUndefined()
    expect(indented[3]?.marker).toBe('decimal')
    expect(proseMarkerLabels(indented, 5)[0]).toBe('5.4')
    expect(proseMarkerLabels(indented, 5)[1]).toBe('◦')
    expect(proseMarkerLabels(outdentItem(indented, 1), 5)[1]).toBe('◦')
    expect(outdentItem(indented, 1)[1]?.depth).toBe(1)
    const stored = persistProse(indented, 5)
    expect(stored.body).not.toContain('.0')
    expect(parseProseNodes(stored.nodes)).toEqual(indented)
    const nested = [
      { ...outline('Planungsrahmen', 0, { list_start: 4 }), section_bound: true as const },
      { ...outline('Kind', 1), section_bound: true as const },
    ]
    expect(proseMarkerLabels(nested, 5)).toEqual(['5.4', '5.4.1'])
    const columns = outlineMarkerColumns(proseMarkerLabels(nested, 5), nested)
    expect(columns[0]).toEqual({ col: 3, prefix: 0, indent: 'calc(0ch + 0 * 0.4em)' })
    expect(columns[1]).toEqual({ col: 5, prefix: 3, indent: 'calc(3ch + 1 * 0.4em)' })
    const short = [outline('A'), outline('B')]
    expect(outlineMarkerColumns(proseMarkerLabels(short), short)).toEqual([
      { col: 1, prefix: 0, indent: 'calc(0ch + 0 * 0.4em)' },
      { col: 1, prefix: 0, indent: 'calc(0ch + 0 * 0.4em)' },
    ])
    const css = readFileSync('src/components/offers/offer-document.css', 'utf8')
    expect(css).not.toContain('4.8em')
    expect(css).toContain('minmax(var(--outline-col, max-content), max-content)')
    expect(css).toContain('margin-left: var(--outline-indent, calc(var(--depth, 0) * 1.55em))')
    const mixed = [bullet('A', 0, 'disc'), bullet('B', 1, 'disc'), outline('C', 2)]
    expect(outlineMarkerColumns(proseMarkerLabels(mixed), mixed)[2]).toMatchObject({
      indent: 'calc(2.85em)',
    })
    const underBullet = [bullet('A', 0, 'disc'), outline('B', 1), outline('C', 2)]
    const underColumns = outlineMarkerColumns(proseMarkerLabels(underBullet), underBullet)
    expect(underColumns[1]?.indent).toBe('calc(1.5em)')
    expect(underColumns[2]?.indent).toBe('calc(1ch + 1.9em)')
    const skipped = [bullet('A', 0, 'disc'), outline('C', 2)]
    expect(outlineMarkerColumns(proseMarkerLabels(skipped), skipped)[1]?.indent).toBe('calc(1.5em)')
    const plainParent = [
      { kind: 'item' as const, text: 'A', marker: 'decimal' as const },
      outline('B', 1),
    ]
    expect(outlineMarkerColumns(proseMarkerLabels(plainParent), plainParent)[1]?.indent).toBe(
      'calc(2.3em)',
    )
    expect(css).toContain('margin-left: calc(var(--depth, 0) * 1.35em)')
    expect(css).toContain('grid-template-columns: 1.15em minmax(0, 1fr)')
    expect(css).toContain('column-gap: 0.35em')
    expect(css).toContain('grid-template-columns: 1.9em minmax(0, 1fr)')
    expect(css).toContain('margin-left: calc(var(--depth, 0) * 1.55em)')
    const pair = [
      { ...outline('A'), section_bound: true as const },
      { ...outline('B', 1), section_bound: true as const },
    ]
    expect(proseMarkerLabels(pair, 2)).toEqual(['2.1', '2.1.1'])
    const out = outdentItem(pair, 1)
    expect(proseMarkerLabels(out, 2)).toEqual(['2.1', '2.2'])
  })

  it('stores a plain bullet symbol and marker offsets and rejects html', () => {
    const nodes: OfferTextNode[] = [
      {
        kind: 'item',
        text: 'Projektstart',
        marker: 'circle',
        depth: 1,
        glyph: '✓',
        marker_x_mm: -1.5,
        marker_y_mm: 0.5,
        text_start_mm: 2,
      },
    ]
    const stored = persistProse(nodes)
    expect(stored.body).toContain('✓ Projektstart')
    expect(parseProseNodes(stored.nodes)).toEqual(nodes)
    expect(
      parseProseNodes([{ kind: 'item', text: 'A', marker: 'disc', glyph: '<b>x</b>' }]),
    ).toBeNull()
    expect(
      parseProseNodes([
        { kind: 'item', text: 'A', marker: 'decimal', numbering: 'outline', glyph: '✓' },
      ]),
    ).toBeNull()
    expect(parseProseNodes([{ kind: 'item', text: 'A', marker: 'disc', marker_x_mm: 80 }])).toBeNull()
    expect(canonMarkerMm(-1.25, -30, 30)).toBe(-1.3)
    expect(canonMarkerMm(1.25, -30, 30)).toBe(1.3)
    expect(canonMarkerMm(-1.24, -30, 30)).toBe(-1.2)
    expect(canonMarkerMm(-1.26, -30, 30)).toBe(-1.3)
    expect(canonMarkerMm(-30.05, -30, 30)).toBeNull()
    expect(canonMarkerMm(-30.04, -30, 30)).toBe(-30)
    expect(canonMarkerMm(30.05, -30, 30)).toBeNull()
    expect(canonMarkerMm(30.04, -30, 30)).toBe(30)
    expect(canonMarkerMm(-30, -30, 30)).toBe(-30)
    expect(canonMarkerMm(30, -30, 30)).toBe(30)
    expect(canonMarkerMm(-0.05, -30, 30)).toBe(-0.1)
    expect(canonMarkerMm(0.05, -30, 30)).toBe(0.1)
    expect(canonMarkerMm(-29.95, -30, 30)).toBe(-30)
    expect(canonMarkerMm(29.95, -30, 30)).toBe(30)
    expect(canonMarkerMm(-20.05, -20, 20)).toBeNull()
    expect(
      parseProseNodes([{ kind: 'item', text: 'A', marker: 'disc', marker_x_mm: -1.25 }]),
    ).toEqual([{ kind: 'item', text: 'A', marker: 'disc', marker_x_mm: -1.3 }])
    expect(
      parseProseNodes([{ kind: 'item', text: 'A', marker: 'disc', marker_x_mm: -30.05 }]),
    ).toBeNull()
    expect(
      parseProseNodes([{ kind: 'item', text: 'A', marker: 'disc', marker_y_mm: Number.NaN }]),
    ).toBeNull()
  })

  it('round-trips outline metadata and rejects a start stored with continue', () => {
    const stored = persistProse([outline('A', 0, { list_start: 3 })])
    expect(stored.nodes?.[0]).toMatchObject({ numbering: 'outline', list_start: 3 })
    expect(parseProseNodes(stored.nodes)).toEqual(stored.nodes)
    expect(
      parseProseNodes([
        {
          kind: 'item',
          text: 'A',
          marker: 'decimal',
          numbering: 'outline',
          list_start: 3,
          list_continue: true,
        },
      ]),
    ).toBeNull()
    expect(
      parseProseNodes([{ kind: 'item', text: 'A', marker: 'disc', numbering: 'outline' }]),
    ).toBeNull()
  })
})
