/** Percentage steps for the offer page. Fit-width and whole-page stay outside this list. */
export const OFFER_ZOOM_STEPS = [
  25, 50, 75, 100, 125, 150, 175, 200, 250, 300, 400, 500, 600, 700, 800,
] as const

/** Next percentage in `direction`, or null when the current value is already past that end. */
export function nextOfferZoomStep(currentPercent: number, direction: 1 | -1): number | null {
  if (direction > 0) return OFFER_ZOOM_STEPS.find((level) => level > currentPercent + 0.01) ?? null
  return [...OFFER_ZOOM_STEPS].reverse().find((level) => level < currentPercent - 0.01) ?? null
}
