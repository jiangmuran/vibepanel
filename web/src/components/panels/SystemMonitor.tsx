import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { Session, SessionUsage, SystemSample, UsageSample } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { meterText, meterWidth, formatBytes } from './meter'
import { StateDot } from '../StateDot'
import { safeText } from '../text'

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

/**
 * One session's share of the machine.
 *
 * Sorted by CPU, because the question this answers is "which one has run
 * away". The bar is drawn against the same 0–100 as the machine meter above it
 * rather than against the busiest session, so a panel full of quiet sessions
 * looks quiet — a bar normalised to the maximum makes the least idle session
 * look pegged.
 */
function SessionRow({ session, usage }: { session: Session; usage: SessionUsage }) {
  const pct = Math.max(0, Math.min(100, usage.cpuPercent))
  const tone =
    pct >= 50 ? 'var(--vp-state-waiting)' : pct >= 20 ? 'var(--vp-state-working)' : 'var(--vp-accent)'
  return (
    <div className="mb-2" data-testid="session-usage">
      <div className="flex items-baseline gap-1.5 text-[11.5px]">
        <StateDot state={session.state} size={9} exited={session.exited} exitStatus={session.exitStatus} />
        <span className="min-w-0 flex-1 truncate text-ink" title={safeText(session.title)}>
          {safeText(session.title)}
        </span>
        <span className="tabular shrink-0 text-ink">{pct.toFixed(pct < 10 ? 1 : 0)}%</span>
        <span className="tabular w-16 shrink-0 text-right text-ink-2">{formatBytes(usage.rss)}</span>
      </div>
      <div className="mt-1 h-1 overflow-hidden rounded-full" style={{ background: 'var(--vp-surface-2)' }}>
        <div
          className="h-full rounded-full transition-[width] duration-500 ease-vp"
          style={{ width: `${pct}%`, background: tone }}
        />
      </div>
    </div>
  )
}

export function SystemMonitor({ sessions }: { sessions: Session[] }) {
  useLang()
  const [sample, setSample] = useState<SystemSample | null>(null)
  const [usage, setUsage] = useState<UsageSample | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    // Self-scheduling, and only while this panel is mounted: a monitor nobody
    // is looking at should cost nothing.
    //
    // Both readings are fetched together and the next tick is scheduled after
    // both have landed, so a slow one cannot make the other pile up requests.
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const [next, use] = await Promise.all([api.system(), api.usage()])
        if (!cancelled) {
          setSample(next)
          setUsage(use)
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
    return (
      <p className="px-3 py-4 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
        {safeText(error)}
      </p>
    )
  }
  if (!sample) {
    return <p className="px-3 py-4 text-[12px] text-ink-2">{t('monitor.reading')}</p>
  }

  const memUsed = sample.memTotal - sample.memAvailable
  const swapUsed = sample.swapTotal - sample.swapFree
  const diskUsed = sample.diskTotal - sample.diskFree
  const cores = t('monitor.cores', { n: String(sample.cores) })

  // Absent from the payload means the pane has gone, which is not the same as
  // idle — those sessions are left out rather than drawn at zero.
  const measured = sessions
    .map((s) => ({ session: s, usage: usage?.sessions[s.id] }))
    .filter((row): row is { session: Session; usage: SessionUsage } => row.usage !== undefined)
    .sort((a, b) => b.usage.cpuPercent - a.usage.cpuPercent || b.usage.rss - a.usage.rss)

  return (
    <div className="px-3 py-3" data-testid="system-monitor">
      <Meter
        label={t('monitor.cpu')}
        // The first sample has nothing to difference against, so it says so
        // rather than showing a zero that looks like an idle machine.
        value={sample.cpuPercent}
        detail={
          !sample.cpuReadable
            ? // "sampling…" promises an answer. On a machine with no
              // /proc/stat — every darwin build this project releases — none
              // is coming, and the promise renews itself every two seconds.
              `${cores} · ${t('monitor.unavailable')}`
            : sample.cpuPercent === null
              ? `${cores} · ${t('monitor.sampling')}`
              : `${cores} · load ${sample.load1.toFixed(2)} ${sample.load5.toFixed(2)} ${sample.load15.toFixed(2)}`
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
        label={t('monitor.memory')}
        value={sample.memTotal ? (memUsed / sample.memTotal) * 100 : null}
        detail={
          sample.memTotal
            ? t('monitor.of', { used: formatBytes(memUsed), total: formatBytes(sample.memTotal) })
            : t('monitor.unavailable')
        }
      />
      {sample.swapTotal > 0 && (
        <Meter
          label={t('monitor.swap')}
          value={(swapUsed / sample.swapTotal) * 100}
          detail={t('monitor.of', { used: formatBytes(swapUsed), total: formatBytes(sample.swapTotal) })}
        />
      )}
      <Meter
        label={t('monitor.disk')}
        value={sample.diskTotal ? (diskUsed / sample.diskTotal) * 100 : null}
        detail={
          sample.diskTotal ? t('monitor.free', { size: formatBytes(sample.diskFree) }) : t('monitor.unavailable')
        }
      />

      {/* The reason this panel exists at all, for the person who asked for it:
          the machine meters say the box is busy, and this says which session is
          doing it. */}
      <div className="mt-4 border-t border-hairline pt-3">
        <p className="mb-2 text-[11px] uppercase tracking-wide text-ink-2">{t('monitor.perSession')}</p>
        {usage && !usage.readable ? (
          <p className="text-[11px] leading-relaxed text-ink-2">{t('monitor.noProc')}</p>
        ) : measured.length === 0 ? (
          <p className="text-[11px] text-ink-2">
            {usage ? t('monitor.noSessions') : t('monitor.sampling')}
          </p>
        ) : (
          measured.map(({ session, usage: u }) => (
            <SessionRow key={session.id} session={session} usage={u} />
          ))
        )}
        {/* Process count decides whether the numbers above mean anything: a
            pane sitting at a shell prompt is one process and reads zero, which
            is true and uninteresting. */}
        {measured.length > 0 && (
          <p className="tabular mt-1 text-[10.5px] text-ink-2">
            {measured.reduce((n, r) => n + r.usage.procs, 0) === 1
              ? t('monitor.oneProc')
              : t('monitor.procs', { n: String(measured.reduce((n, r) => n + r.usage.procs, 0)) })}
          </p>
        )}
      </div>

      {/* Hidden rather than shown as "up 0m", which is what an unread
          /proc/uptime renders as and reads as "this machine just booted".
          Same zero-means-unknown as the two meters above; hidden rather than
          "—" because a single line of prose has nothing to draw. */}
      {sample.uptime > 0 && (
        <p className="tabular mt-4 text-[10.5px] text-ink-2">
          {t('monitor.up', { d: duration(sample.uptime) })}
        </p>
      )}
    </div>
  )
}
