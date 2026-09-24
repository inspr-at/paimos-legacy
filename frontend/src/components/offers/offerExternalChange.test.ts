import { describe, expect, it } from 'vitest'
import {
  beginSave,
  createRemoteWatch,
  finishSave,
  noteOwnRevision,
  observeRemote,
  pollDelayMs,
  reloadChoice,
  remoteIsNewer,
  shouldCheckRemote,
} from './offerExternalChange'

describe('offer external revision watch', () => {
  it('flags another session and ignores our own save and a late stale response', () => {
    let watch = createRemoteWatch('41', 4)
    watch = observeRemote(watch, { offerId: '41', revision: 5, checkId: 1 })
    expect(remoteIsNewer(watch)).toBe(true)
    expect(watch.remoteRevision).toBe(5)

    watch = observeRemote(watch, { offerId: '99', revision: 8, checkId: 2 })
    expect(watch.remoteRevision).toBe(5)

    watch = beginSave(watch)
    watch = observeRemote(watch, { offerId: '41', revision: 6, checkId: 3 })
    expect(remoteIsNewer(watch)).toBe(true)
    expect(watch.deferredRemote).toBe(6)
    watch = noteOwnRevision(watch, 6)
    watch = finishSave(watch)
    expect(watch.localRevision).toBe(6)
    expect(remoteIsNewer(watch)).toBe(false)

    watch = observeRemote(watch, { offerId: '41', revision: 7, checkId: 4 })
    expect(watch.remoteRevision).toBe(7)
    const late = observeRemote(watch, { offerId: '41', revision: 6, checkId: 2 })
    expect(late.remoteRevision).toBe(7)
    const current = observeRemote(watch, { offerId: '41', revision: 6, checkId: 5 })
    expect(remoteIsNewer(current)).toBe(false)
  })

  it('does not treat the revision we just wrote as a remote update', () => {
    let watch = createRemoteWatch('41', 5)
    watch = beginSave(watch)
    watch = observeRemote(watch, { offerId: '41', revision: 6, checkId: 1 })
    watch = noteOwnRevision(watch, 6)
    watch = finishSave(watch)
    expect(remoteIsNewer(watch)).toBe(false)
    watch = observeRemote(watch, { offerId: '41', revision: 6, checkId: 2 })
    expect(remoteIsNewer(watch)).toBe(false)
  })

  it('keeps a newer revision seen during our save when it is not the one we wrote', () => {
    let watch = createRemoteWatch('41', 5)
    watch = beginSave(watch)
    watch = observeRemote(watch, { offerId: '41', revision: 8, checkId: 1 })
    watch = noteOwnRevision(watch, 6)
    watch = finishSave(watch)
    expect(watch.localRevision).toBe(6)
    expect(watch.remoteRevision).toBe(8)
    expect(remoteIsNewer(watch)).toBe(true)
  })

  it('reloads a clean offer directly and asks before replacing dirty or in-flight edits', () => {
    expect(reloadChoice({ external: true, dirty: false, saving: false })).toBe('direct')
    expect(reloadChoice({ external: true, dirty: true, saving: false })).toBe('confirm')
    expect(reloadChoice({ external: true, dirty: false, saving: true })).toBe('confirm')
    expect(reloadChoice({ external: false, dirty: true, saving: false })).toBe('idle')
  })

  it('backs off and skips hidden, overlapping, loading, and finalizing checks', () => {
    expect(pollDelayMs(0)).toBe(4000)
    expect(pollDelayMs(1)).toBe(8000)
    expect(pollDelayMs(3)).toBe(20000)
    expect(pollDelayMs(9)).toBe(20000)
    expect(
      shouldCheckRemote({
        hidden: false,
        inFlight: false,
        loading: false,
        finalizing: false,
        offerId: '41',
      }),
    ).toBe(true)
    expect(
      shouldCheckRemote({
        hidden: true,
        inFlight: false,
        loading: false,
        finalizing: false,
        offerId: '41',
      }),
    ).toBe(false)
    expect(
      shouldCheckRemote({
        hidden: false,
        inFlight: true,
        loading: false,
        finalizing: false,
        offerId: '41',
      }),
    ).toBe(false)
    expect(
      shouldCheckRemote({
        hidden: false,
        inFlight: false,
        loading: true,
        finalizing: false,
        offerId: '41',
      }),
    ).toBe(false)
    expect(
      shouldCheckRemote({
        hidden: false,
        inFlight: false,
        loading: false,
        finalizing: true,
        offerId: '41',
      }),
    ).toBe(false)
  })
})
