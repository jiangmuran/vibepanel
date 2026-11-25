import type { ResourcesView, SystemSample } from '../../protocol/wire'

export type MonitorPosture = 'ok' | 'warn' | 'critical'

/** Clamps a percentage to [0, 100]. */
export function clampPct(v: number): number {
  if (!Number.isFinite(v)) return 0
  return Math.max(0, Math.min(100, v))
}

/**
 * Picks the graphical fill token for a pressure percentage.
 *
 * Colour is never the sole carrier (Red Line 4): every meter using this sits
 * beside its exact numeric reading.
 */
export function pressureTone(pct: number | null, warnAt = 70, critAt = 90): string {
  if (pct === null || !Number.isFinite(pct)) return 'var(--vp-state-dead)'
  if (pct >= critAt) return 'var(--mon-crit)'
  if (pct >= warnAt) return 'var(--mon-warn)'
  return 'var(--mon-ok)'
}

/**
 * Overall host health posture from live metrics and the cgroup memory governor.
 * Unreadable metrics (zero totals or null CPU) never trigger a false alarm.
 */
export function overallPosture(
  sample: SystemSample | null,
  resLevel?: 'ok' | 'warn' | 'critical',
): MonitorPosture {
  if (resLevel === 'critical') return 'critical'
  if (!sample) return resLevel === 'warn' ? 'warn' : 'ok'

  const cpu = sample.cpuReadable && sample.cpuPercent !== null ? sample.cpuPercent : 0
  const mem = sample.memTotal > 0 ? ((sample.memTotal - sample.memAvailable) / sample.memTotal) * 100 : 0
  const disk = sample.diskTotal > 0 ? ((sample.diskTotal - sample.diskFree) / sample.diskTotal) * 100 : 0

  if (cpu >= 96 || mem >= 92 || disk >= 95) return 'critical'
  if (resLevel === 'warn' || cpu >= 80 || mem >= 80 || disk >= 88) return 'warn'
  return 'ok'
}

export interface MemorySegment {
  key: 'held' | 'host' | 'used' | 'cache'
  bytes: number
  pct: number
  tone: string
}

export interface MemoryBreakdown {
  valid: boolean
  used: number
  reclaimable: number
  pct: number | null
  segments: MemorySegment[]
}

/**
 * Splits host RAM into non-overlapping segments whose percentages never exceed
 * 100% of MemTotal:
 *   - With cgroup telemetry: Sessions held + Host other + Reclaimable page cache
 *   - Without cgroup telemetry: Active used + Reclaimable page cache
 */
export function memoryParts(sample: SystemSample, res: ResourcesView | null): MemoryBreakdown {
  if (!sample.memTotal || sample.memTotal <= 0) {
    return { valid: false, used: 0, reclaimable: 0, pct: null, segments: [] }
  }
  const total = sample.memTotal
  const avail = Math.min(total, Math.max(0, sample.memAvailable))
  const used = total - avail
  const pct = clampPct((used / total) * 100)

  const rawCached =
    typeof sample.memCached === 'number' && sample.memCached > 0
      ? sample.memCached
      : typeof sample.memFree === 'number' && sample.memFree <= avail
        ? avail - sample.memFree
        : 0
  const reclaimable = Math.max(0, Math.min(avail, rawCached))

  const segs: MemorySegment[] = []
  if (res?.supported && res.pool.held > 0) {
    const held = Math.min(used, res.pool.held)
    const host = Math.max(0, used - held)
    if (held > 0) segs.push({ key: 'held', bytes: held, pct: clampPct((held / total) * 100), tone: 'var(--mon-s1)' })
    if (host > 0) segs.push({ key: 'host', bytes: host, pct: clampPct((host / total) * 100), tone: 'var(--mon-s2)' })
  } else if (used > 0) {
    segs.push({ key: 'used', bytes: used, pct: clampPct((used / total) * 100), tone: 'var(--mon-s1)' })
  }

  if (reclaimable > 0) {
    segs.push({
      key: 'cache',
      bytes: reclaimable,
      pct: clampPct((reclaimable / total) * 100),
      tone: 'var(--mon-cache)',
    })
  }

  return { valid: true, used, reclaimable, pct, segments: segs }
}

/**
 * Scales two throughput series onto one shared [0, 100] axis with a noise floor
 * so idle link chatter stays flat near the baseline rather than filling the plot.
 */
export function scaleDualSeries(
  a: number[],
  b: number[],
  floor = 1024,
): { peak: number; aScaled: number[]; bScaled: number[] } {
  let peak = floor
  for (const v of a) if (v > peak) peak = v
  for (const v of b) if (v > peak) peak = v
  const scale = (arr: number[]) => arr.map((v) => clampPct((Math.max(0, v) / peak) * 100))
  return { peak, aScaled: scale(a), bScaled: scale(b) }
}

/** Human-readable elapsed seconds; sub-minute readings show seconds rather than "0m". */
export function formatUptime(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${s}s`
}
