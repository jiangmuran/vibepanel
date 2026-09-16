import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  MAX_TOASTS,
  TOAST_MS,
  clearToasts,
  dismissToast,
  setToastProgress,
  showToast,
  subscribeToasts,
  toastsSnapshot,
} from './toasts'

describe('the toast stack', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    clearToasts()
  })
  afterEach(() => {
    clearToasts()
    vi.useRealTimers()
  })

  it('says one thing and then stops saying it', () => {
    showToast({ kind: 'success', key: 'toast.copied' })
    expect(toastsSnapshot()).toHaveLength(1)
    vi.advanceTimersByTime(TOAST_MS.success - 1)
    expect(toastsSnapshot()).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(toastsSnapshot()).toHaveLength(0)
  })

  it('keeps a failure up for longer than a success', () => {
    // An error is read; a success is glanced at. The socket error banner chose
    // eight seconds for the same reason, and this matches it on purpose.
    expect(TOAST_MS.error).toBeGreaterThan(TOAST_MS.success)
    showToast({ kind: 'error', key: 'toast.uploadFailed' })
    vi.advanceTimersByTime(TOAST_MS.success)
    expect(toastsSnapshot()).toHaveLength(1)
    vi.advanceTimersByTime(TOAST_MS.error - TOAST_MS.success)
    expect(toastsSnapshot()).toHaveLength(0)
  })

  it('counts a repeat instead of stacking it', () => {
    // A write that failed once fails again on the next keystroke. Three
    // identical rows say less than one row saying three.
    showToast({ kind: 'error', key: 'toast.uploadFailed', detail: 'disk full' })
    showToast({ kind: 'error', key: 'toast.uploadFailed', detail: 'disk full' })
    expect(toastsSnapshot()).toHaveLength(1)
    expect(toastsSnapshot()[0].count).toBe(2)
  })

  it('does not count two different failures as one', () => {
    showToast({ kind: 'error', key: 'toast.uploadFailed', detail: 'disk full' })
    showToast({ kind: 'error', key: 'toast.uploadFailed', detail: 'permission denied' })
    expect(toastsSnapshot()).toHaveLength(2)
  })

  it('restarts the clock when the same thing happens again', () => {
    showToast({ kind: 'error', key: 'toast.uploadFailed' })
    vi.advanceTimersByTime(TOAST_MS.error - 100)
    showToast({ kind: 'error', key: 'toast.uploadFailed' })
    vi.advanceTimersByTime(200)
    expect(toastsSnapshot()).toHaveLength(1)
  })

  it('never grows past the cap', () => {
    // Dropping a folder onto the terminal is one gesture and can be thirty
    // failures. A stack that grew to fit them would cover the terminal it is
    // reporting about.
    for (let i = 0; i < MAX_TOASTS + 6; i++) {
      showToast({ kind: 'info', key: 'toast.uploadingMany', params: { n: i } })
    }
    expect(toastsSnapshot()).toHaveLength(MAX_TOASTS)
    // The oldest go, not the newest: the last one is what just happened.
    expect(toastsSnapshot()[MAX_TOASTS - 1].params).toEqual({ n: MAX_TOASTS + 5 })
  })

  it('can be taken back before it expires', () => {
    // Which is how "uploading…" stops sitting above "uploaded".
    const id = showToast({ kind: 'info', key: 'toast.uploadingOne' })
    showToast({ kind: 'success', key: 'toast.uploadedOne' })
    dismissToast(id)
    expect(toastsSnapshot().map((t) => t.key)).toEqual(['toast.uploadedOne'])
  })

  it('hands React a snapshot it can compare by identity', () => {
    // useSyncExternalStore calls this on every render; a fresh array each time
    // is an infinite render loop rather than a slow one.
    const before = toastsSnapshot()
    expect(toastsSnapshot()).toBe(before)
    showToast({ kind: 'info', key: 'toast.copied' })
    expect(toastsSnapshot()).not.toBe(before)
  })

  it('tells its subscribers', () => {
    let calls = 0
    const off = subscribeToasts(() => calls++)
    showToast({ kind: 'info', key: 'toast.copied' })
    expect(calls).toBe(1)
    vi.advanceTimersByTime(TOAST_MS.info)
    expect(calls).toBe(2)
    off()
    showToast({ kind: 'info', key: 'toast.copied' })
    expect(calls).toBe(2)
  })
})

describe('a toast that is reporting something still running', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    clearToasts()
  })
  afterEach(() => {
    clearToasts()
    vi.useRealTimers()
  })

  // Four seconds is the one length of upload that never needed a bar. The
  // 300MB drop this was built for lost its toast four seconds in and said
  // nothing for the rest of the minute.
  it('stays up while the bar is short of the end', () => {
    const id = showToast({ kind: 'info', key: 'toast.uploadingOne', progress: 0 })
    vi.advanceTimersByTime(TOAST_MS.info * 10)
    expect(toastsSnapshot()).toHaveLength(1)
    setToastProgress(id, 0.5)
    vi.advanceTimersByTime(TOAST_MS.info * 10)
    expect(toastsSnapshot()[0]?.progress).toBe(0.5)
  })

  // Moving it is not saying it again: an upload reporting itself every hundred
  // milliseconds must not keep its own toast alive by being noisy, which is
  // what re-raising it would do.
  it('does not count up or re-raise as the bar moves', () => {
    const id = showToast({ kind: 'info', key: 'toast.uploadingOne', progress: 0 })
    setToastProgress(id, 0.3)
    setToastProgress(id, 0.6)
    expect(toastsSnapshot()).toHaveLength(1)
    expect(toastsSnapshot()[0]?.count).toBe(1)
  })

  // A full bar is work that has finished, so it goes back on the ordinary
  // timer. Both callers take their own toast back; this is for the one that
  // forgets, so a finished upload cannot leave a bar on screen for good.
  it('goes away on its own once the bar is full', () => {
    const id = showToast({ kind: 'info', key: 'toast.uploadingOne', progress: 0 })
    setToastProgress(id, 1)
    expect(toastsSnapshot()).toHaveLength(1)
    vi.advanceTimersByTime(TOAST_MS.info)
    expect(toastsSnapshot()).toHaveLength(0)
  })

  // Dedup returns the *same id*, and a progress toast now lives as long as its
  // upload rather than four seconds -- so a second paste during a big upload
  // used to land on the first one's toast: the bar jumped back to the second
  // upload's 1%, and whichever finished first dismissed the toast out from
  // under the other.
  it('does not merge a second upload into the first one’s bar', () => {
    const first = showToast({ kind: 'info', key: 'toast.uploadingOne', progress: 0 })
    setToastProgress(first, 0.6)
    const second = showToast({ kind: 'info', key: 'toast.uploadingOne', progress: 0 })
    expect(second).not.toBe(first)
    expect(toastsSnapshot()).toHaveLength(2)
    dismissToast(first)
    expect(toastsSnapshot().map((t) => t.id)).toEqual([second])
  })
})
