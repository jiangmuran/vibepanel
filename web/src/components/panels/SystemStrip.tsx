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

export function SystemStrip() {
  useLang()
  const [sample, setSample] = useState<SystemSample | null>(null)

  useEffect(() => {
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.system()
        if (!cancelled) setSample(next)
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
          <span className="h-1 min-w-0 flex-1 overflow-hidden rounded-full bg-surface-2">
            <span
              className="block h-full rounded-full transition-[width] duration-500 ease-vp"
              style={{
                width: `${Math.min(100, Math.max(0, pct ?? 0))}%`,
                // Colour follows pressure, and never alone: the number beside
                // it says the same thing, because a bar that is merely orange
                // tells a colour-blind reader nothing.
                background:
                  pct === null
                    ? 'var(--vp-state-dead)'
                    : pct >= 90
                      ? 'var(--vp-state-crashed)'
                      : pct >= 70
                        ? 'var(--vp-state-waiting)'
                        : 'var(--vp-accent)',
              }}
            />
          </span>
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
