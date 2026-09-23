import type { OfferFooterLayout } from './types'

/** A4 box and footer lockup from offer-document.css.
 * Content width 168mm. Side labels keep 36mm. Bottom padding 16mm with 6mm clearance.
 * The wordmark is 1em at 7.5pt beside 7.2mm dots and a 2.2mm gap (about 43.3mm).
 */
export const OFFER_FOOTER_LOGO = {
  minWidthMm: 18,
  maxWidthMm: 96,
  /** Negative lifts the mark; positive lowers it. -6 stays inside the 7mm gap above the rule. */
  minOffsetMm: -6,
  maxOffsetMm: 10,
  defaultWidthMm: 43.3,
  defaultOffsetMm: 2,
  legacyOffsetMm: 0,
} as const

const WORDMARK_HEIGHT_MM = (7.5 * 25.4) / 72
const WORDMARK_WIDTH_MM = WORDMARK_HEIGHT_MM * (1998 / 156)
const DOTS_W = 7.2
const DOTS_H = 1.6
const GAP = 2.2
const LEGACY_WIDTH = DOTS_W + GAP + WORDMARK_WIDTH_MM

export function roundFooterMm(value: number): number {
  return Math.round(value * 10) / 10
}

export function footerLogoBox(widthMm: number) {
  const width = roundFooterMm(widthMm)
  const scale = width / LEGACY_WIDTH
  return {
    dotsWidthMm: DOTS_W * scale,
    dotsHeightMm: DOTS_H * scale,
    gapMm: GAP * scale,
    wordmarkWidthMm: WORDMARK_WIDTH_MM * scale,
    wordmarkHeightMm: WORDMARK_HEIGHT_MM * scale,
  }
}

export function explicitFooter(
  current: OfferFooterLayout | null | undefined,
  patch: Partial<OfferFooterLayout>,
): OfferFooterLayout | null {
  const width = roundFooterMm(
    patch.logo_width_mm ?? current?.logo_width_mm ?? OFFER_FOOTER_LOGO.defaultWidthMm,
  )
  const offset = roundFooterMm(
    patch.logo_offset_mm ?? current?.logo_offset_mm ?? OFFER_FOOTER_LOGO.legacyOffsetMm,
  )
  if (
    width < OFFER_FOOTER_LOGO.minWidthMm ||
    width > OFFER_FOOTER_LOGO.maxWidthMm ||
    offset < OFFER_FOOTER_LOGO.minOffsetMm ||
    offset > OFFER_FOOTER_LOGO.maxOffsetMm
  )
    return null
  return { logo_width_mm: width, logo_offset_mm: offset }
}
