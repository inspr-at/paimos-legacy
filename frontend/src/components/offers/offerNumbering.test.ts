import { describe, expect, it } from 'vitest'
import {
  applyStructure,
  enterProse,
  indentItem,
  insertProseText,
  outdentItem,
  parseProseNodes,
  persistProse,
  projectProse,
  proseMarkerLabels,
  setDecimalControl,
} from './offerProse'
import type { OfferTextNode } from './types'

const paragraph = (text: string): OfferTextNode => ({ kind: 'paragraph', text })
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

  it('keeps a start when indent cannot change depth and keeps a continued level', () => {
    const started = [outline('A', 0, { list_start: 3 })]
    expect(indentItem(started, 0)[0]).toMatchObject({ list_start: 3, numbering: 'outline' })
    expect(indentItem(started, 0)[0]?.depth).toBeUndefined()
    expect(proseMarkerLabels(indentItem(started, 0))).toEqual(['3'])
    const continued = [
      outline('A', 0, { list_start: 3 }),
      outline('B', 1),
      paragraph(''),
      outline('C', 1, { list_continue: true }),
    ]
    expect(proseMarkerLabels(continued)[3]).toBe('3.2')
    const held = indentItem(continued, 3)
    expect(held[3]).toMatchObject({ depth: 1, list_continue: true, numbering: 'outline' })
    expect(proseMarkerLabels(held)[3]).toBe('3.2')
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
