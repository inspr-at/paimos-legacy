import { formatDecimal, formatDecimalFlex } from '@/composables/useNumberFormat'
import { formatDateWithLocale, formatDateTimeWithLocale } from '@/composables/useDateFormat'
export const OFFER_BULLET_MARKERS = ['disc', 'circle', 'square', 'dash'] as const
export type OfferBulletMarker = (typeof OFFER_BULLET_MARKERS)[number]
export type OfferMarker = OfferBulletMarker | 'decimal'
export interface OfferTextNode {
  kind: 'paragraph' | 'item'
  text: string
  depth?: number
  marker?: OfferMarker
  /** Multilevel 3 / 3.1 / 3.1.1. Absent decimal items stay plain per-level numbers. */
  numbering?: 'outline'
  /** Positive start for this item. Not copied onto a node created by splitting. */
  list_start?: number
  /** Continue the preceding matching numbering, including across a paragraph. */
  list_continue?: boolean
  /** Prefix the outline with the owning section number. The digits are not stored in the text. */
  section_bound?: boolean
  /** Plain bullet symbol. Decimal items keep their generated number. */
  glyph?: string
  /** Signed millimetres from the marker's default position. */
  marker_x_mm?: number
  marker_y_mm?: number
  /** Signed millimetres added before the item text. Wrapped lines use the same start. */
  text_start_mm?: number
}
export interface OfferFooterLayout {
  logo_width_mm: number
  logo_offset_mm: number
}
export type OfferSelection =
  | { kind: 'none' }
  | { kind: 'heading'; index: number; count: number; heading: string }
  | { kind: 'text'; index: number; count: number; heading: string }
  | { kind: 'position'; index: number; count: number }
  | { kind: 'footer' }

export interface OfferBlock {
  heading: string
  body: string
  nodes?: OfferTextNode[]
}
export interface OfferSender {
  company: string
  street: string
  postal_code: string
  city: string
  country: string
  register_no: string
  register_court: string
  email: string
  phone: string
  website: string
  uid: string
  bank_name: string
  iban: string
  bic: string
  contact_person: string
}
export interface OfferDefaults {
  intro: string
  blocks: OfferBlock[]
  accept_text: string
  vat_note: string
}
export interface OfferSettings {
  sender: OfferSender
  defaults: OfferDefaults
}
export interface OfferPosition {
  short_text: string
  long_text: string
  quantity: number
  unit: string
  unit_price_cents: number
  total_cents: number
}
export interface OfferDocument extends OfferDefaults {
  title: string
  subtitle: string
  project_ref: string
  offer_date: string
  valid_until: string
  sender: OfferSender
  customer: {
    name: string
    address: string
    contact: string
    country: string
    customer_no: string
    email?: string
  }
  positions: OfferPosition[]
  net_total_cents: number
  footer?: OfferFooterLayout
}
export interface OfferConfirmation {
  state: string
  sent_at?: string
  error_class?: string
  pdf_ready: boolean
}
export interface Offer {
  deleted?: boolean
  document_sha256?: string
  confirmation?: OfferConfirmation
  id: number
  offer_no: string
  customer_id: number
  status: string
  revision: number
  document: OfferDocument
  created_at: string
  updated_at: string
  public_token?: string
  accepted_at?: string
  accepted_name?: string
  accepted_company?: string
  accepted_note?: string
  sent_at: string | null
}
export const money = (cents: number) => `€ ${formatDecimal(cents / 100, 2, 'de-AT')}`
export const quantity = (n: number) => formatDecimalFlex(n, 2, 'de-AT')
export const date = (s: string) =>
  formatDateWithLocale(s, 'de-AT', {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    timeZone: 'UTC',
  })
export const total = (p: OfferPosition) =>
  Math.floor((Math.round(p.quantity * 100) * p.unit_price_cents + 50) / 100)
export const net = (d: OfferDocument) => d.positions.reduce((n, p) => n + total(p), 0)
export function parseAmount(s: string): number | null {
  const stripped = s.replace(/[€\s]/g, '')
  const clean = stripped.includes(',')
    ? stripped.replace(/\./g, '').replace(',', '.')
    : /^\d{1,3}(\.\d{3})+$/.test(stripped)
      ? stripped.replace(/\./g, '')
      : stripped
  if (!/^\d+(\.\d{1,2})?$/.test(clean)) return null
  const n = Number(clean)
  return Number.isFinite(n) ? n : null
}

export type PublicOffer = Pick<
  Offer,
  | 'offer_no'
  | 'status'
  | 'revision'
  | 'document'
  | 'accepted_at'
  | 'accepted_name'
  | 'accepted_company'
  | 'accepted_note'
  | 'document_sha256'
  | 'confirmation'
>
export const offerStatus = (status: string) =>
  ({
    draft: 'In Bearbeitung',
    sent: 'Finalisiert',
    accepted: 'Angenommen',
    declined: 'Abgelehnt',
    expired: 'Abgelaufen',
  })[status] || status

export const receiptTime = (value?: string) =>
  formatDateTimeWithLocale(value || '', 'de-AT', {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    timeZone: 'Europe/Vienna',
    timeZoneName: 'short',
  })

export const validOfferEmail = (email?: string) =>
  !!email && /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(email)
