import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { SystemSample } from '../../protocol/wire'

/** How often the monitor refreshes while it is on screen. */
const SAMPLE_MS = 2000

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/**
 * A meter.
 *
 * Shape as well as colour: the bar carries the value, and the number beside it
 * says it exactly. A bar that only changes hue is unreadable to a good number
 * of people and useless in a screenshot.
 */
function Meter({ label, value, detail }: { label: string; value: number; detail: string }) {
  const pct = Math.max(0, Math.min(100, value))
  const tone =
    pct >= 90 ? 'var(--vp-state-waiting)' : pct >= 75 ? 'var(--vp-state-working)' : 'var(--vp-accent)'
  return (
    <div className="mb-3">
      <div className="mb-1 flex items-baseline justify-between text-[11px]">
        <span className="text-ink-2">{label}</span>
        <span className="tabular text-ink">{pct.toFixed(0)}%</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full" style={{ background: 'var(--vp-surface-2)' }}>
        <div
          className="h-full rounded-full transition-[width] duration-500 ease-vp"
          style={{ width: `${pct}%`, background: tone }}
        />
      </div>
      <div className="mt-1 tabular text-[10.5px] text-ink-2">{detail}</div>
    </div>
  )
}

export function SystemMonitor() {
  const [sample, setSample] = useState<SystemSample | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    // Self-scheduling, and only while this panel is mounted: a monitor nobody
    // is looking at should cost nothing.
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.system()
        if (!cancelled) {
          setSample(next)
          setError(null)
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      }
      if (!cancelled) timer = window.setTimeout(() => void tick(), SAMPLE_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [])

  if (error) {
    return <p className="px-3 py-4 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>{error}</p>
  }
  if (!sample) {
    return <p className="px-3 py-4 text-[12px] text-ink-2">Reading…</p>
  }

  const memUsed = sample.memTotal - sample.memAvailable
  const swapUsed = sample.swapTotal - sample.swapFree
  const diskUsed = sample.diskTotal - sample.diskFree

  return (
    <div className="px-3 py-3" data-testid="system-monitor">
      <Meter
        label="CPU"
        // The first sample has nothing to difference against, so it says so
        // rather than showing a zero that looks like an idle machine.
        value={sample.cpuPercent ?? 0}
        detail={
          sample.cpuPercent === null
            ? `${sample.cores} cores · sampling…`
            : `${sample.cores} cores · load ${sample.load1.toFixed(2)} ${sample.load5.toFixed(2)} ${sample.load15.toFixed(2)}`
        }
      />
      <Meter
        label="Memory"
        value={sample.memTotal ? (memUsed / sample.memTotal) * 100 : 0}
        detail={`${bytes(memUsed)} of ${bytes(sample.memTotal)}`}
      />
      {sample.swapTotal > 0 && (
        <Meter
          label="Swap"
          value={(swapUsed / sample.swapTotal) * 100}
          detail={`${bytes(swapUsed)} of ${bytes(sample.swapTotal)}`}
        />
      )}
      <Meter
        label="Disk"
        value={sample.diskTotal ? (diskUsed / sample.diskTotal) * 100 : 0}
        detail={`${bytes(sample.diskFree)} free`}
      />
      <p className="tabular mt-4 text-[10.5px] text-ink-2">up {duration(sample.uptime)}</p>
    </div>
  )
}
