/** Visible-page revision watch. Own saves and late responses are not remote updates. */

export const OFFER_REMOTE_POLL_BASE_MS = 4_000
export const OFFER_REMOTE_POLL_MAX_MS = 20_000

export type RemoteWatch = {
  offerId: string
  localRevision: number
  ownRevisions: number[]
  remoteRevision: number | null
  saveInFlight: boolean
  deferredRemote: number | null
  newestCheck: number
}

export function createRemoteWatch(offerId: string, localRevision: number): RemoteWatch {
  return {
    offerId,
    localRevision,
    ownRevisions: [],
    remoteRevision: null,
    saveInFlight: false,
    deferredRemote: null,
    newestCheck: 0,
  }
}

export function pollDelayMs(unchanged: number): number {
  const steps = Math.max(0, Math.min(unchanged, 3))
  return Math.min(OFFER_REMOTE_POLL_MAX_MS, OFFER_REMOTE_POLL_BASE_MS * 2 ** steps)
}

export function shouldCheckRemote(input: {
  hidden: boolean
  inFlight: boolean
  loading: boolean
  finalizing: boolean
  offerId: string
}): boolean {
  return (
    !input.hidden &&
    !input.inFlight &&
    !input.loading &&
    !input.finalizing &&
    input.offerId !== ''
  )
}

export function beginSave(watch: RemoteWatch): RemoteWatch {
  return { ...watch, saveInFlight: true }
}

function ahead(revision: number | null, localRevision: number, own: number[]): number | null {
  if (revision == null || revision <= localRevision || own.includes(revision)) return null
  return revision
}

export function noteOwnRevision(watch: RemoteWatch, revision: number): RemoteWatch {
  const ownRevisions = watch.ownRevisions.includes(revision)
    ? watch.ownRevisions
    : [...watch.ownRevisions, revision]
  const localRevision = Math.max(watch.localRevision, revision)
  return {
    ...watch,
    ownRevisions,
    localRevision,
    remoteRevision: ahead(watch.remoteRevision, localRevision, ownRevisions),
    deferredRemote: ahead(watch.deferredRemote, localRevision, ownRevisions),
  }
}

/** Release the in-flight save and promote a deferred revision only when it is still newer. */
export function finishSave(watch: RemoteWatch): RemoteWatch {
  const deferred = ahead(watch.deferredRemote, watch.localRevision, watch.ownRevisions)
  return {
    ...watch,
    saveInFlight: false,
    deferredRemote: null,
    remoteRevision: deferred ?? ahead(watch.remoteRevision, watch.localRevision, watch.ownRevisions),
  }
}

/**
 * Apply one GET. A lower check id is stale. Another offer id is ignored.
 * Revisions this session wrote, or that are not ahead of local, are not external.
 * While a save is in flight the newer revision is held until `finishSave`.
 */
export function observeRemote(
  watch: RemoteWatch,
  input: { offerId: string; revision: number; checkId: number },
): RemoteWatch {
  if (input.offerId !== watch.offerId || input.checkId < watch.newestCheck) return watch
  const newestCheck = input.checkId
  if (watch.ownRevisions.includes(input.revision)) {
    const localRevision = Math.max(watch.localRevision, input.revision)
    return {
      ...watch,
      newestCheck,
      localRevision,
      remoteRevision: null,
      deferredRemote: ahead(watch.deferredRemote, localRevision, watch.ownRevisions),
    }
  }
  if (input.revision <= watch.localRevision) {
    return { ...watch, newestCheck, remoteRevision: null }
  }
  if (watch.saveInFlight) {
    return {
      ...watch,
      newestCheck,
      deferredRemote: Math.max(watch.deferredRemote ?? 0, input.revision),
    }
  }
  return { ...watch, newestCheck, remoteRevision: input.revision }
}

export function remoteIsNewer(watch: RemoteWatch | null): boolean {
  return !!watch && watch.remoteRevision != null && watch.remoteRevision > watch.localRevision
}

/** Clean offers reload directly. Dirty or in-flight edits need an explicit confirm. */
export function reloadChoice(input: {
  external: boolean
  dirty: boolean
  saving: boolean
}): 'direct' | 'confirm' | 'idle' {
  if (!input.external) return 'idle'
  if (input.dirty || input.saving) return 'confirm'
  return 'direct'
}
