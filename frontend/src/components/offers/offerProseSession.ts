import { inject, provide, ref, type InjectionKey, type Ref } from 'vue'
import type { OfferBulletMarker, OfferTextNode } from './types'
import type { ProseListKind, ProseListState } from './offerProse'

export type ProseCommand =
  | { type: 'list'; kind: ProseListKind }
  | { type: 'marker'; marker: OfferBulletMarker }
  | { type: 'indent' }
  | { type: 'outdent' }
  | { type: 'numbering'; mode: 'restart' | 'continue' | 'start'; start?: number }

export type ProseTarget = {
  id: number
  state: () => ProseListState
  remember: () => void
  apply: (command: ProseCommand) => void
}

export type OfferProseSession = {
  active: Ref<ProseTarget | null>
  revision: Ref<number>
  claim: (target: ProseTarget) => void
  release: (id: number) => void
  touch: () => void
}

export const OFFER_PROSE_SESSION: InjectionKey<OfferProseSession> = Symbol('offer-prose-session')

let nextProseId = 1
export function takeOfferProseId(): number {
  return nextProseId++
}

export function provideOfferProseSession(): OfferProseSession {
  const active = ref<ProseTarget | null>(null)
  const revision = ref(0)
  const session: OfferProseSession = {
    active,
    revision,
    claim(target) {
      active.value = target
      revision.value++
    },
    release(id) {
      if (active.value?.id === id) active.value = null
    },
    touch() {
      revision.value++
    },
  }
  provide(OFFER_PROSE_SESSION, session)
  return session
}

export function useOfferProseSession(): OfferProseSession | null {
  return inject(OFFER_PROSE_SESSION, null)
}

export function isOfferChrome(node: EventTarget | null): boolean {
  const element = node instanceof Element ? node : node instanceof Node ? node.parentElement : null
  return !!element?.closest('[data-offer-chrome]')
}

export type { OfferTextNode }
