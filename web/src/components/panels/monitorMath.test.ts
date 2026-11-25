import { describe, expect, it } from 'vitest'

import type { ResourcesView, SystemSample } from '../../protocol/wire'
import {
  clampPct,
  formatUptime,
  memoryParts,
  overallPosture,
  pressureTone,
  scaleDualSeries,
} from './monitorMath'

function baseSample(overrides: Partial<SystemSample> = {}): SystemSample {
  return {
    at: 1000,
    cpuPercent: 25,
    cpuReadable: true,
    cores: 8,
    load1: 1.2,
    load5: 1.0,
    load15: 0.8,
    memTotal: 32 * 1024 ** 3,
    memAvailable: 20 * 1024 ** 3,
    memFree: 8 * 1024 ** 3,
    memCached: 14 * 1024 ** 3,
    swapTotal: 8 * 1024 ** 3,
    swapFree: 8 * 1024 ** 3,
    diskTotal: 500 * 1024 ** 3,
    diskFree: 300 * 1024 ** 3,
    diskPath: '/',
    netReadable: true,
    netRxRate: 2048,
    netTxRate: 1024,
    netRxBytes: 100_000,
    netTxBytes: 50_000,
    uptime: 3665,
    ...overrides,
  }
}

describe('monitorMath', () => {
  it('maps pressure tiers to validated telemetry tokens', () => {
    expect(pressureTone(null)).toBe('var(--vp-state-dead)')
    expect(pressureTone(42)).toBe('var(--mon-ok)')
    expect(pressureTone(75)).toBe('var(--mon-warn)')
    expect(pressureTone(92)).toBe('var(--mon-crit)')
  })

  it('never reports unreadable machines as elevated or critical', () => {
    const unmeasured = baseSample({
      cpuReadable: false,
      cpuPercent: null,
      memTotal: 0,
      memAvailable: 0,
      diskTotal: 0,
      diskFree: 0,
    })
    expect(overallPosture(unmeasured)).toBe('ok')
    expect(memoryParts(unmeasured, null).valid).toBe(false)
    expect(memoryParts(unmeasured, null).pct).toBeNull()
  })

  it('escalates posture on genuine CPU, memory, disk or cgroup pressure', () => {
    expect(overallPosture(baseSample({ cpuPercent: 85 }))).toBe('warn')
    expect(overallPosture(baseSample({ memAvailable: 2 * 1024 ** 3 }))).toBe('critical')
    expect(overallPosture(baseSample(), 'critical')).toBe('critical')
  })

  it('keeps memory composition segments bounded inside 100%', () => {
    const s = baseSample()
    const fakeRes = {
      supported: true,
      pool: { current: 10 * 1024 ** 3, held: 7 * 1024 ** 3, max: 24 * 1024 ** 3, stall: 0 },
    } as unknown as ResourcesView
    const breakdown = memoryParts(s, fakeRes)
    expect(breakdown.valid).toBe(true)
    expect(breakdown.segments.map((x) => x.key)).toEqual(['held', 'host', 'cache'])
    const sum = breakdown.segments.reduce((acc, x) => acc + x.pct, 0)
    expect(sum).toBeLessThanOrEqual(100.001)
  })

  it('scales dual series onto one shared axis with a floor', () => {
    const quiet = scaleDualSeries([100, 200], [50, 80], 1024)
    expect(quiet.peak).toBe(1024)
    expect(Math.max(...quiet.aScaled)).toBeLessThan(25)

    const busy = scaleDualSeries([0, 5000], [2500, 10000], 1024)
    expect(busy.peak).toBe(10000)
    expect(busy.aScaled).toEqual([0, 50])
    expect(busy.bScaled).toEqual([25, 100])
  })

  it('clamps percentages and formats sub-minute uptimes honestly', () => {
    expect(clampPct(-5)).toBe(0)
    expect(clampPct(145)).toBe(100)
    expect(formatUptime(14)).toBe('14s')
    expect(formatUptime(3665)).toBe('1h 1m')
    expect(formatUptime(90000)).toBe('1d 1h')
  })
})
