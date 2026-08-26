import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { SystemSample } from '../../protocol/wire'
import { meterText, meterWidth, formatBytes } from './meter'

/** How often the monitor refreshes while it is on screen. */
const SAMPLE_MS = 2000

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
function Meter({
  label,
  value,
  detail,
}: {
  label: string
  value: number | null
  detail: string
}) {
  const pct = meterWidth(value)
  const tone =
    pct >= 90 ? 'var(--vp-state-waiting)' : pct >= 75 ? 'var(--vp-state-working)' : 'var(--vp-accent)'
  return (
    <div className="mb-3">
      <div className="mb-1 flex items-baseline justify-between text-[11px]">
        <span className="text-ink-2">{label}</span>
        <span className="tabular text-ink">{meterText(value)}</span>
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
        value={sample.cpuPercent}
        detail={
          !sample.cpuReadable
            ? // "sampling…" promises an answer. On a machine with no
              // /proc/stat — every darwin build this project releases — none
              // is coming, and the promise renews itself every two seconds.
              `${sample.cores} cores · unavailable`
            : sample.cpuPercent === null
              ? `${sample.cores} cores · sampling…`
              : `${sample.cores} cores · load ${sample.load1.toFixed(2)} ${sample.load5.toFixed(2)} ${sample.load15.toFixed(2)}`
        }
      />
      {/* A total of zero means the reading failed, not that the machine has no
          memory: readMem returns zeroes when /proc/meminfo cannot be opened,
          which is every darwin build and any container that masks /proc. This
          rendered "0%" beside "0 B of 0 B" — a measurement nobody made, and
          the measurement it claimed was "nothing is using any memory".

          The CPU meter one line above already knows not to do this, and says
          why in its own comment. */}
      <Meter
        label="Memory"
        value={sample.memTotal ? (memUsed / sample.memTotal) * 100 : null}
        detail={sample.memTotal ? `${formatBytes(memUsed)} of ${formatBytes(sample.memTotal)}` : 'unavailable'}
      />
      {sample.swapTotal > 0 && (
        <Meter
          label="Swap"
          value={(swapUsed / sample.swapTotal) * 100}
          detail={`${formatBytes(swapUsed)} of ${formatBytes(sample.swapTotal)}`}
        />
      )}
      <Meter
        label="Disk"
        value={sample.diskTotal ? (diskUsed / sample.diskTotal) * 100 : null}
        detail={sample.diskTotal ? `${formatBytes(sample.diskFree)} free` : 'unavailable'}
      />
      {/* Hidden rather than shown as "up 0m", which is what an unread
          /proc/uptime renders as and reads as "this machine just booted".
          Same zero-means-unknown as the two meters above; hidden rather than
          "—" because a single line of prose has nothing to draw. */}
      {sample.uptime > 0 && (
        <p className="tabular mt-4 text-[10.5px] text-ink-2">up {duration(sample.uptime)}</p>
      )}
    </div>
  )
}
