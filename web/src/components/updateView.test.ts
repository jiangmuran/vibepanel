import { describe, expect, it } from 'vitest'

import type { UpdateJob, UpdateStatus } from '../protocol/wire'
import { canApply, jobActive, jobLine, releaseDate, shouldNotice, statusLine } from './updateView'

const base: UpdateStatus = {
  current: 'v1.9.0',
  platform: 'linux/amd64',
  autoCheck: true,
  checking: false,
  newer: false,
}

describe('what the update section says', () => {
  it('says nothing until something has been asked', () => {
    expect(statusLine(base)).toBeNull()
    expect(statusLine({ ...base, checking: true })).toEqual({ key: 'upd.checking' })
  })

  it('names the kind of unreachable, and keeps the server text as detail', () => {
    const why = 'Get "https://api.github.com/x": dial tcp: lookup api.github.com: no such host'
    expect(statusLine({ ...base, checkedAt: 1, unreachable: why, unreachableKind: 'offline' })).toEqual({
      key: 'upd.offline',
      detail: why,
    })
    expect(statusLine({ ...base, checkedAt: 1, unreachable: why, unreachableKind: 'rateLimited' })?.key).toBe(
      'upd.rateLimited',
    )
    expect(statusLine({ ...base, checkedAt: 1, unreachable: why, unreachableKind: 'timeout' })?.key).toBe('upd.timeout')
    expect(statusLine({ ...base, checkedAt: 1, unreachable: why, unreachableKind: 'http' })?.key).toBe('upd.unreachable')
    expect(statusLine({ ...base, checkedAt: 1, unreachable: why })?.key).toBe('upd.unreachable')
  })

  it('tells up to date from a build that cannot be compared', () => {
    expect(statusLine({ ...base, checkedAt: 1, version: 'v1.9.0' })).toEqual({ key: 'upd.upToDate' })
    expect(statusLine({ ...base, current: 'dev', checkedAt: 1, version: 'v1.9.0' })).toEqual({ key: 'upd.devBuild' })
    expect(statusLine({ ...base, checkedAt: 1 })).toEqual({ key: 'upd.noRelease' })
  })

  it('offers a release only with an archive for this platform', () => {
    const rel = { ...base, checkedAt: 1, version: 'v2.0.0', newer: true }
    expect(statusLine(rel)).toEqual({ key: 'upd.noAsset', params: { v: 'v2.0.0', platform: 'linux/amd64' } })
    expect(canApply(rel)).toBe(false)
    const withAsset = { ...rel, asset: 'https://example/x.tar.gz' }
    expect(statusLine(withAsset)).toEqual({ key: 'upd.available', params: { v: 'v2.0.0' } })
    expect(canApply(withAsset)).toBe(true)
    // A system install is told how, not offered a button that would fail.
    expect(canApply({ ...withAsset, byHand: 'sudo vibepanel service upgrade' })).toBe(false)
    // And not twice.
    expect(canApply({ ...withAsset, job: job('downloading') })).toBe(false)
  })
})

function job(stage: UpdateJob['stage'], extra: Partial<UpdateJob> = {}): UpdateJob {
  return { stage, version: 'v2.0.0', startedAt: 1, done: 0, total: -1, restarting: false, ...extra }
}

describe('the job, stage by stage', () => {
  it('draws a bar only while downloading, and only with a total', () => {
    const half = jobLine(job('downloading', { done: 3.5 * 1024 * 1024, total: 7 * 1024 * 1024 }))
    expect(half.key).toBe('upd.downloading')
    expect(half.params).toEqual({ done: '3.5 MiB', total: '7.0 MiB' })
    expect(half.progress).toBeCloseTo(0.5)
    // A server that did not say how big: the bytes so far, no bar.
    const unsized = jobLine(job('downloading', { done: 2048, total: -1 }))
    expect(unsized.key).toBe('upd.downloadingUnsized')
    expect(unsized.progress).toBeUndefined()
    // Past the total -- a server that lied -- stays at 1.
    expect(jobLine(job('downloading', { done: 20, total: 10 })).progress).toBe(1)
    expect(jobLine(job('installing')).progress).toBeUndefined()
    expect(jobLine(job('restarting')).progress).toBeUndefined()
  })

  it('says the sudo path is the installer, not this process', () => {
    expect(jobLine(job('restarting', { elevated: true })).key).toBe('upd.elevated')
    expect(jobLine(job('restarting'))).toEqual({ key: 'upd.restarting', params: { v: 'v2.0.0' } })
  })

  it('names which step failed, with the server text underneath', () => {
    expect(jobLine(job('failed', { reason: 'checksum', error: 'x' }))).toEqual({ key: 'upd.failedChecksum', detail: 'x' })
    expect(jobLine(job('failed', { reason: 'verify', error: 'x' })).key).toBe('upd.failedVerify')
    expect(jobLine(job('failed', { reason: 'network', error: 'x' })).key).toBe('upd.failedNetwork')
    expect(jobLine(job('failed', { reason: 'install', error: 'x' })).key).toBe('upd.failedInstall')
    expect(jobLine(job('failed', { error: 'x' })).key).toBe('upd.failed')
  })

  it('tells installed-but-not-restarting from restarting', () => {
    const line = jobLine(job('installed', { restartWhy: 'not under systemd' }))
    expect(line.key).toBe('upd.installedNoRestart')
    expect(line.detail).toBe('not under systemd')
    expect(jobActive(job('installed'))).toBe(false)
    expect(jobActive(job('failed'))).toBe(false)
    expect(jobActive(job('downloading'))).toBe(true)
    expect(jobActive(job('installing'))).toBe(true)
    expect(jobActive(job('restarting'))).toBe(true)
    expect(jobActive(undefined)).toBe(false)
  })
})

describe('the notice outside the dialog', () => {
  const rel = { ...base, checkedAt: 1, version: 'v2.0.0', newer: true, asset: 'x' }
  it('shows for a newer release and not for a skipped one', () => {
    expect(shouldNotice(rel, null)).toBe(true)
    expect(shouldNotice(rel, 'v2.0.0')).toBe(false)
    // Skipping one version is not skipping the next.
    expect(shouldNotice(rel, 'v1.9.5')).toBe(true)
    expect(shouldNotice(null, null)).toBe(false)
    expect(shouldNotice({ ...rel, newer: false }, null)).toBe(false)
  })
  it('steps aside while the section is installing it', () => {
    expect(shouldNotice({ ...rel, job: job('downloading') }, null)).toBe(false)
    expect(shouldNotice({ ...rel, job: job('failed') }, null)).toBe(true)
  })
})

describe('the release date', () => {
  it('is a date, in the reader’s language, or nothing', () => {
    expect(releaseDate('2026-09-01T10:00:00Z', 'en')).toMatch(/2026/)
    expect(releaseDate('2026-09-01T10:00:00Z', 'zh')).toMatch(/2026/)
    expect(releaseDate('', 'en')).toBe('')
    expect(releaseDate(undefined, 'en')).toBe('')
    expect(releaseDate('not a date', 'en')).toBe('')
  })
})
