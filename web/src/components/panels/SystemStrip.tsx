import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { SystemSample } from '../../protocol/wire'
import { formatBytes } from './meter'
import { Trend } from './Trend'
import { t, useLang } from '../../i18n'

/**
 * Three numbers, always on screen.
 *
 * The monitor used to be one of four tabs, which meant giving a whole column to
 * three figures and — worse — that you could not see them while looking at
 * anything else. "Is the machine coping" is not a question you go somewhere to
 * ask; it is a thing you want in the corner of your eye while you read a
 * terminal, and a tab you have to choose is a tab nobody chooses.
 *
 * The full panel keeps the detail: swap, cores, load, the disk path. This is
 * the part you glance at.
 */
const SAMPLE_MS = 4000

/**
 * How many readings the strip remembers.
 *
 * Forty at four seconds is a little over two minutes, which is the span the
 * question "is it climbing" is actually about. Longer and the line flattens
 * into nothing; shorter and a single slow tick is half the chart.
 */
const HISTORY = 40

export function SystemStrip() {
  useLang()
  const [sample, setSample] = useState<SystemSample | null>(null)
  // One series per row, oldest first.
  //
  // A bar says how full something is and nothing about where it is going, and
  // "is the machine coping" is a question about the second one -- 「底下监控的
  // 三个蓝色条都改成迷你走势图」. The percentage stays beside the line: the
  // line carries the trend, the number carries the level, and neither is only
  // a colour (red line 4).
  const [history, setHistory] = useState<Record<string, number[]>>({})

  useEffect(() => {
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.system()
        if (!cancelled) {
          setSample(next)
          setHistory((h) => {
            const memPct = next.memTotal > 0 ? ((next.memTotal - next.memAvailable) / next.memTotal) * 100 : 0
            const diskPct = next.diskTotal > 0 ? ((next.diskTotal - next.diskFree) / next.diskTotal) * 100 : 0
            const push = (series: number[] | undefined, v: number) =>
              [...(series ?? []), v].slice(-HISTORY)
            return {
              cpu: push(h.cpu, next.cpuPercent ?? 0),
              mem: push(h.mem, memPct),
              disk: push(h.disk, diskPct),
              // Raw bytes/sec, not yet a percentage: network has no natural
              // ceiling, so the render below scales this against its own
              // recent peak instead of against a fixed 0-100.
              net: push(h.net, (next.netRxRate ?? 0) + (next.netTxRate ?? 0)),
            }
          })
        }
      } catch {
        // Silent. A strip that turns into an error message is a strip that
        // takes over the corner of the eye it was meant to sit quietly in;
        // the full panel says what went wrong when asked.
      }
      if (!cancelled) timer = window.setTimeout(() => void tick(), SAMPLE_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [])

  if (!sample) return null

  const memUsed = sample.memTotal - sample.memAvailable
  const diskUsed = sample.diskTotal - sample.diskFree
  const netCombined =
    sample.netRxRate !== null && sample.netTxRate !== null ? sample.netRxRate + sample.netTxRate : null
  // Scaled to its own recent peak rather than a fixed 0-100: unlike CPU, memory
  // and disk, throughput has no ceiling a line can be measured against. Floored
  // at 1 KiB/s so a quiet link does not turn its own noise into a mountain
  // range -- the same reasoning Trend's own comment gives for fixing the other
  // three axes instead of scaling them to what has been seen.
  const netMax = Math.max(1024, ...(history.net ?? []))
  const netScaled = (history.net ?? []).map((v) => Math.min(100, (v / netMax) * 100))
  const rows: Array<{ key: string; label: string; pct: number | null; detail: string }> = [
    {
      key: 'cpu',
      label: t('monitor.cpu'),
      pct: sample.cpuPercent,
      detail: t('monitor.cores', { n: sample.cores }),
    },
    {
      key: 'mem',
      label: t('monitor.memory'),
      pct: sample.memTotal > 0 ? (memUsed / sample.memTotal) * 100 : null,
      detail: t('monitor.free', { size: formatBytes(sample.memAvailable) }),
    },
    {
      key: 'disk',
      label: t('monitor.disk'),
      pct: sample.diskTotal > 0 ? (diskUsed / sample.diskTotal) * 100 : null,
      detail: t('monitor.free', { size: formatBytes(sample.diskFree) }),
    },
  ]

  return (
    <div
      data-testid="system-strip"
      className="shrink-0 border-t border-hairline px-3 py-2.5"
      title={t('monitor.strip')}
    >
      {rows.map(({ key, label, pct, detail }) => (
        <div key={key} className="flex items-center gap-2 py-[3px]">
          {/* Wide enough for "Memory", which is the longest of the three in
              either language. At w-8 the English label ran into its own bar. */}
          <span className="w-12 shrink-0 truncate text-vp-xs text-ink-2">{label}</span>
          {/* `.vp-bar`, the same object the monitor's meters are made of. It
              was a hand-written track and fill here and a different
              hand-written pair there, at two heights, which is the drift
              `.vp-control` exists to stop one layer up. */}
          <Trend
            values={history[key] ?? []}
            // Colour follows pressure, and never alone: the number beside it
            // says the same thing, because a line that is merely orange tells
            // a colour-blind reader nothing.
            tone={
              pct === null
                ? 'var(--vp-state-dead)'
                : pct >= 90
                  ? 'var(--vp-state-crashed)'
                  : pct >= 70
                    ? 'var(--vp-state-waiting)'
                    : 'var(--vp-accent)'
            }
          />
          <span className="w-9 shrink-0 text-right tabular text-vp-xs text-ink-2">
            {pct === null ? '—' : `${Math.round(pct)}%`}
          </span>
          {/* Fixed width and no truncation: these three are the same kind of
              fact and a column that sometimes ends in an ellipsis reads as a
              layout that ran out of room rather than as a number.

              nowrap as well, and wide enough for the longest of them. "242.7
              GiB 可用" is two characters longer than "18.6 GiB 可用" and it wrapped
              -- one row of the three silently became two lines tall, which is
              worse than an ellipsis because it moves everything under it. */}
          <span className="w-[88px] shrink-0 text-right tabular whitespace-nowrap text-vp-xs text-ink-2">
            {detail}
          </span>
        </div>
      ))}
      {/* Its own row rather than a fourth entry in `rows`: it has no percentage
          to draw a bar against, and its tone is not "pressure" -- a link
          maxed out is not a problem the way a full disk is, so it never turns
          the warning colours the other three use. */}
      {sample.netReadable && (
        <div className="flex items-center gap-2 py-[3px]" data-testid="system-strip-network">
          <span className="w-12 shrink-0 truncate text-vp-xs text-ink-2">{t('monitor.network')}</span>
          <Trend values={netScaled} tone={netCombined === null ? 'var(--vp-state-dead)' : 'var(--vp-accent)'} />
          <span className="w-9 shrink-0 text-right tabular text-vp-xs text-ink-2">
            {netCombined === null ? '—' : formatRateCompact(netCombined)}
          </span>
          <span className="w-[88px] shrink-0 text-right tabular whitespace-nowrap text-vp-xs text-ink-2">
            {sample.netRxRate === null || sample.netTxRate === null
              ? ''
              : `↓${formatRateCompact(sample.netRxRate)} ↑${formatRateCompact(sample.netTxRate)}`}
          </span>
        </div>
      )}
    </div>
  )
}

/**
 * A throughput at strip width: "1.4M", not "1.4 MiB/s".
 *
 * The strip's detail column is 88px and shared with the down and up figures
 * on the same line -- "↓1.4 MiB/s ↑220 KiB/s" is 22 characters and wraps the
 * row, which is exactly the failure the comment above this row's rendering
 * points at for the other three. The unit and the per-second are both cut:
 * this is the corner-of-the-eye number, and formatRate in the full panel
 * still spells it out.
 */
function formatRateCompact(n: number): string {
  const v = Math.max(0, n)
  if (v < 1024) return `${Math.round(v)}B`
  const units = ['K', 'M', 'G', 'T']
  let x = v / 1024
  let i = 0
  while (x >= 1024 && i < units.length - 1) {
    x /= 1024
    i++
  }
  return `${x < 10 ? x.toFixed(1) : Math.round(x)}${units[i]}`
}
