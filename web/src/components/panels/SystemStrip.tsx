import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { SystemSample } from '../../protocol/wire'
import { formatBytes } from './meter'
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
    </div>
  )
}

/**
 * Two minutes of one number, as a line.
 *
 * Drawn as an SVG path rather than through a chart library, because it is
 * forty points in a box twelve pixels tall and a dependency for that is a
 * dependency to keep updated forever.
 *
 * Fixed to 0-100 rather than scaled to what has been seen. A CPU that has sat
 * between 3% and 5% would otherwise draw a dramatic mountain range, which is a
 * chart that lies about the thing it is for -- the question is whether the
 * machine is under pressure, and a flat line near the floor is the correct
 * answer to it.
 *
 * One reading is a dot and no reading is nothing: a single point makes no line,
 * and drawing a flat one across the whole width would claim two minutes of
 * history that does not exist yet.
 */
function Trend({ values, tone }: { values: number[]; tone: string }) {
  const W = 56
  const H = 16
  if (values.length < 2) {
    return <span className="h-4 min-w-0 flex-1" aria-hidden />
  }
  // Stretched to the width rather than plotted against a fixed two-minute
  // axis. Both were drawn and looked at: growing in from the left is more
  // honest about how much history there is and, for the first two minutes
  // after every page load, it is three short marks in the corner of the eye --
  // which is worse at the only job this has. Nobody reads the x axis of a
  // 56-pixel line; they read whether it is climbing.
  const step = W / (values.length - 1)
  const y = (v: number) => H - (Math.min(100, Math.max(0, v)) / 100) * (H - 1) - 0.5
  const line = values.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${y(v).toFixed(1)}`).join(' ')
  return (
    <svg
      className="h-4 min-w-0 flex-1"
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      aria-hidden
      focusable="false"
    >
      {/* The area under it, faint, so a low line still reads as a line rather
          than as a stray rule across an empty box. */}
      <path d={`${line} L${W},${H} L0,${H} Z`} fill={tone} opacity="0.15" />
      <path d={line} fill="none" stroke={tone} strokeWidth="1" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}
