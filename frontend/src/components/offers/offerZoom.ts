/** Percentage presets for the offer page. 500 and 700 stay valid typed values, not presets. */
export const OFFER_ZOOM_MIN = 25
export const OFFER_ZOOM_MAX = 800
export const OFFER_ZOOM_STEPS = [
  25, 50, 75, 100, 125, 150, 175, 200, 250, 300, 400, 600, 800,
] as const

/** Integer percent from 25 through 800, including values omitted from the preset list. */
export function parseOfferZoom(raw: string): number | null {
  const text = raw.trim().replace('%', '').trim()
  if (!/^\d+$/.test(text)) return null
  const value = Number(text)
  if (!Number.isInteger(value) || value < OFFER_ZOOM_MIN || value > OFFER_ZOOM_MAX) return null
  return value
}

/** Next preset in `direction`, or null when the current value is already past that end. */
export function nextOfferZoomStep(currentPercent: number, direction: 1 | -1): number | null {
  if (direction > 0) return OFFER_ZOOM_STEPS.find((level) => level > currentPercent + 0.01) ?? null
  return [...OFFER_ZOOM_STEPS].reverse().find((level) => level < currentPercent - 0.01) ?? null
}
