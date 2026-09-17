import { useEffect, useState } from 'react'
import { ChevronDown, ChevronRight, SlidersHorizontal } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ProcUsage, Session, SessionUsage, SystemSample, UsageSample } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { PanelDensity } from '../chrome'
import { meterText, meterWidth, formatBytes, formatRate } from './meter'
import { Trend, historyLength } from './Trend'
import { sessionLabel } from '../label'
import { StateDot } from '../StateDot'
import { safeText } from '../text'

/** How often the monitor refreshes while it is on screen. */
const SAMPLE_MS = 2000

/** Two minutes of readings at this panel's own sampling rate. */
const HISTORY = historyLength(SAMPLE_MS)

/**
 * A session is called out as the likely cause rather than left for someone to
 * spot in a sorted list. Machine CPU past this, and the busiest session's
 * process list opens on its own -- "which one" is the question the moment the
 * meter above it turns orange, not a click later.
 */
const CULPRIT_CPU = 60

function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  // Under a minute reads as "0m", which is the same thing a failed clock reads
  // as. A state that changed twelve seconds ago is the interesting case on
  // this panel, not the uninteresting one.
  return `${Math.max(0, Math.floor(seconds))}s`
}

/**
 * A meter.
 *
 * Shape as well as colour: the bar carries the value, and the number beside it
 * says it exactly. A bar that only changes hue is unreadable to a good number
 * of people and useless in a screenshot.
 *
 * The line under the bar is the same fact over the last two minutes rather
 * than only now. Opening this out of the strip used to lose it -- the strip
 * drew a trend and the detail behind it drew four still numbers, which was
 * information the click removed rather than added.
 */
function Meter({
  label,
  value,
  detail,
  history,
  testid,
}: {
  label: string
  value: number | null
  detail: string
  history: number[]
  testid?: string
}) {
  const pct = meterWidth(value)
  const tone =
    pct >= 90 ? 'var(--vp-state-waiting)' : pct >= 75 ? 'var(--vp-state-working)' : 'var(--vp-accent)'
  return (
    <div className="min-w-0" data-testid={testid}>
      <div className="flex items-baseline justify-between gap-2 text-vp-sm">
        <span className="truncate text-ink-2">{label}</span>
        <span className="tabular shrink-0 text-ink">{meterText(value)}</span>
      </div>
      <div className="vp-bar mt-1 h-1.5">
        <span className="vp-bar-fill" style={{ width: `${pct}%`, background: tone }} />
      </div>
      <div className="mt-1 flex h-3 items-center">
        <Trend values={history} tone={value === null ? 'var(--vp-state-dead)' : tone} />
      </div>
      <div className="tabular truncate text-vp-xs text-ink-2" title={detail}>
        {detail}
      </div>
    </div>
  )
}

/** One process inside a session's tree, shown when its row is opened out. */
function ProcRow({ proc }: { proc: ProcUsage }) {
  return (
    <div className="flex min-w-0 items-center gap-2 text-vp-xs" data-testid="session-top-proc">
      <span className="min-w-0 flex-1 truncate text-ink-2" title={safeText(proc.name)}>
        {safeText(proc.name)} <span className="text-ink-3">pid {proc.pid}</span>
      </span>
      <span className="tabular w-10 shrink-0 text-right text-ink">
        {proc.cpuPercent.toFixed(proc.cpuPercent < 10 ? 1 : 0)}%
      </span>
      <span className="tabular w-16 shrink-0 text-right text-ink-2">{formatBytes(proc.rss)}</span>
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
 *
 * Four figures per row now rather than two. `procs` was already in the payload
 * and was only ever summed at the foot of the list, which answered "is any of
 * this real" but not "which of these is a shell". Dwell — how long this session
 * has been in the state its dot is showing — is the other half of the same
 * question: 4% CPU on something that went to `waiting` two hours ago is a very
 * different fact from 4% on something that started working ten seconds ago, and
 * the dot alone cannot tell you which.
 *
 * Opens to name the process, not only the session. A session reading 80% could
 * be one runaway build or three ordinary ones, and CPUPercent alone cannot
 * tell those apart -- `usage.top` can.
 */
function SessionRow({
  session,
  usage,
  now,
  density,
  open,
  onToggle,
  onManage,
}: {
  session: Session
  usage: SessionUsage
  now: number
  density: PanelDensity
  open: boolean
  onToggle: () => void
  onManage?: (id: string) => void
}) {
  const pct = Math.max(0, Math.min(100, usage.cpuPercent))
  const tone =
    pct >= 50 ? 'var(--vp-state-waiting)' : pct >= 20 ? 'var(--vp-state-working)' : 'var(--vp-accent)'
  // Sessions restored before the panel started have a change time from before
  // this browser was open; a negative dwell is a clock skew, not a fact.
  const dwell = Math.max(0, now - session.stateChangedAt)
  const name = sessionLabel(session)
  const Chevron = open ? ChevronDown : ChevronRight
  return (
    <div className="py-[3px]" data-testid="session-usage" data-session={session.id}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        data-testid="session-usage-toggle"
        className="vp-press flex w-full min-w-0 items-center gap-1.5 text-left"
      >
        <Chevron size={11} className="shrink-0 text-ink-3" aria-hidden="true" />
        <StateDot state={session.state} size={9} exited={session.exited} exitStatus={session.exitStatus} />
        <span className="min-w-0 flex-1 truncate text-vp-sm text-ink" title={name}>
          {name}
        </span>
        {/* Dwell is the first thing dropped when the column narrows: it is the
            slowest-moving of the four and the only one that is about the agent
            rather than about the machine. */}
        {density === 'wide' && session.stateChangedAt > 0 && (
          <span
            className="tabular w-10 shrink-0 text-right text-vp-xs text-ink-2"
            data-testid="session-dwell"
            title={t('monitor.state')}
          >
            {duration(dwell)}
          </span>
        )}
        <span
          className="tabular w-8 shrink-0 text-right text-vp-xs text-ink-2"
          data-testid="session-procs"
          title={usage.procs === 1 ? t('monitor.oneProc') : t('monitor.procs', { n: usage.procs })}
        >
          ×{usage.procs}
        </span>
        <span className="tabular w-9 shrink-0 text-right text-vp-sm text-ink">
          {pct.toFixed(pct < 10 ? 1 : 0)}%
        </span>
        <span className="tabular w-16 shrink-0 text-right text-vp-sm text-ink-2">{formatBytes(usage.rss)}</span>
      </button>
      <div className="ml-[15px] mt-0.5 vp-bar h-1">
        <span className="vp-bar-fill" style={{ width: `${pct}%`, background: tone }} />
      </div>
      {open && (
        <div className="ml-[15px] mt-1.5 space-y-1" data-testid="session-top">
          {usage.top && usage.top.length > 0 ? (
            usage.top.map((p) => <ProcRow key={`${p.pid}-${p.start}`} proc={p} />)
          ) : (
            <p className="text-vp-xs text-ink-3">{t('monitor.sampling')}</p>
          )}
          {onManage && (
            <button
              type="button"
              data-testid="session-manage"
              onClick={() => onManage(session.id)}
              className="vp-outline mt-1 inline-flex items-center gap-1 text-vp-xs"
            >
              <SlidersHorizontal size={11} aria-hidden="true" />
              {t('monitor.manage')}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

/** One label and one figure, for the rows that are not meters. */
function Figure({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-2 py-[1px] text-vp-xs" title={title}>
      <span className="shrink-0 text-ink-2">{label}</span>
      <span className="tabular min-w-0 truncate text-right text-ink-2">{value}</span>
    </div>
  )
}

export function SystemMonitor({
  sessions,
  density = 'narrow',
  onManage,
}: {
  sessions: Session[]
  density?: PanelDensity
  /** Opens Settings on this session's Resources row, to act on what was found here. */
  onManage?: (id: string) => void
}) {
  useLang()
  const [sample, setSample] = useState<SystemSample | null>(null)
  const [usage, setUsage] = useState<UsageSample | null>(null)
  const [error, setError] = useState<string | null>(null)
  // One clock for the whole panel, ticked by the same effect that samples. A
  // Date.now() in the body is a value that changes without the component being
  // told, which React's purity rule refuses; and one clock means fifteen rows
  // cannot disagree about what time it is. Same reason GitPanel keeps one.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  // Oldest first, one series per meter. See Trend for why network is raw
  // bytes/sec rather than a percentage.
  const [history, setHistory] = useState<Record<string, number[]>>({})
  // A session's open/closed state survives its own re-renders because it is
  // keyed by id rather than position; a row a person opened does not close
  // itself when a slower session above it changes rank.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

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
          setNow(Math.floor(Date.now() / 1000))
          setError(null)
          setHistory((h) => {
            const memPct = next.memTotal > 0 ? ((next.memTotal - next.memAvailable) / next.memTotal) * 100 : 0
            const diskPct = next.diskTotal > 0 ? ((next.diskTotal - next.diskFree) / next.diskTotal) * 100 : 0
            const push = (series: number[] | undefined, v: number) =>
              [...(series ?? []), v].slice(-HISTORY)
            return {
              cpu: push(h.cpu, next.cpuPercent ?? 0),
              mem: push(h.mem, memPct),
              disk: push(h.disk, diskPct),
              net: push(h.net, (next.netRxRate ?? 0) + (next.netTxRate ?? 0)),
            }
          })
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
      <p className="px-3 py-4 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
        {safeText(error)}
      </p>
    )
  }
  if (!sample) {
    return <p className="px-3 py-4 text-vp-base text-ink-2">{t('monitor.reading')}</p>
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

  const procs = measured.reduce((n, r) => n + r.usage.procs, 0)
  const sessionCpu = measured.reduce((n, r) => n + r.usage.cpuPercent, 0)
  const sessionRss = measured.reduce((n, r) => n + r.usage.rss, 0)

  // The busiest session opens on its own once the machine is genuinely under
  // pressure, so the answer to "which one" is on screen the moment the CPU
  // meter turns orange rather than a click later. Anyone who closes it stays
  // closed until they reopen it themselves -- `expanded` always wins once set.
  const culpritID =
    sample.cpuReadable && (sample.cpuPercent ?? 0) >= CULPRIT_CPU && measured.length > 0
      ? measured[0].session.id
      : null

  const netCombined =
    sample.netRxRate !== null && sample.netTxRate !== null ? sample.netRxRate + sample.netTxRate : null
  const netMax = Math.max(1024, ...(history.net ?? []))
  const netScaled = (history.net ?? []).map((v) => Math.min(100, (v / netMax) * 100))

  return (
    <div className="@container px-3 py-2" data-testid="system-monitor">
      {/* Two columns above 380px and one below, which is the whole of what the
          extra width buys in the side panel; four once there is a whole window
          to spend on it, which is what the "full" state over-the-window is
          for and the narrow column never reaches. */}
      <div
        className={`vp-rows grid gap-x-4 gap-y-2 ${density === 'wide' ? 'grid-cols-2' : 'grid-cols-1'} @3xl:grid-cols-4`}
      >
        <Meter
          label={t('monitor.cpu')}
          // The first sample has nothing to difference against, so it says so
          // rather than showing a zero that looks like an idle machine.
          value={sample.cpuPercent}
          history={history.cpu ?? []}
          detail={
            !sample.cpuReadable
              ? // "sampling…" promises an answer. On a machine with no
                // /proc/stat — every darwin build this project releases — none
                // is coming, and the promise renews itself every two seconds.
                `${cores} · ${t('monitor.unavailable')}`
              : sample.cpuPercent === null
                ? `${cores} · ${t('monitor.sampling')}`
                : cores
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
          history={history.mem ?? []}
          detail={
            sample.memTotal
              ? t('monitor.of', { used: formatBytes(memUsed), total: formatBytes(sample.memTotal) })
              : t('monitor.unavailable')
          }
        />
        {/* Shown whenever the machine has any, which is not the same as
            whenever any is in use: a box with swap configured and none used is
            a fact about that box, and a meter that appears the moment it starts
            swapping is a meter that appears at the worst moment. */}
        {sample.swapTotal > 0 && (
          <Meter
            label={t('monitor.swap')}
            value={(swapUsed / sample.swapTotal) * 100}
            // Swap has no trend of its own tracked yet; an empty series draws
            // nothing rather than borrowing memory's, which would be a second
            // meter claiming to be a first.
            history={[]}
            detail={t('monitor.of', { used: formatBytes(swapUsed), total: formatBytes(sample.swapTotal) })}
          />
        )}
        <Meter
          label={t('monitor.disk')}
          value={sample.diskTotal ? (diskUsed / sample.diskTotal) * 100 : null}
          history={history.disk ?? []}
          detail={
            sample.diskTotal
              ? t('monitor.free', { size: formatBytes(sample.diskFree) })
              : t('monitor.unavailable')
          }
        />
      </div>

      {/* The three figures the payload has always carried and the panel has
          never shown. `diskPath` is the one that changes an answer: "12% free"
          means one thing about / and another about the volume a project
          happens to be on, and the panel was not saying which it had measured.

          Load is not a percentage and does not get a bar. It is a queue length,
          it goes above the core count, and drawing it against 0–100 would be
          the same lie the per-session bars refuse to tell. */}
      <div className="mt-2 border-t border-hairline pt-1.5">
        {(sample.load1 > 0 || sample.load5 > 0 || sample.load15 > 0) && (
          <Figure
            label={t('monitor.load')}
            value={`${sample.load1.toFixed(2)} ${sample.load5.toFixed(2)} ${sample.load15.toFixed(2)}`}
            title={
              sample.cores > 0
                ? t('monitor.perCore', { n: (sample.load1 / sample.cores).toFixed(2) })
                : undefined
            }
          />
        )}
        {/* Truthiness, not `!== ''`. A payload from an older server, or from a
            proxy that trimmed it, has no `diskPath` at all — and `undefined !==
            ''` is true, so the strict comparison put `undefined` into safeText
            and took the whole panel into its error boundary. The render check
            found it by serving exactly that payload. */}
        {sample.diskPath ? (
          <Figure
            label={t('monitor.mount')}
            value={safeText(sample.diskPath)}
            title={safeText(sample.diskPath)}
          />
        ) : null}
        {/* Hidden rather than shown as "up 0m", which is what an unread
            /proc/uptime renders as and reads as "this machine just booted".
            Same zero-means-unknown as the meters above. */}
        {sample.uptime > 0 && (
          <Figure label={t('monitor.machine')} value={t('monitor.up', { d: duration(sample.uptime) })} />
        )}
        {/* Gated on netReadable rather than the totals being nonzero: a
            freshly-up interface that has carried nothing yet is a real
            reading of 0 B, and only darwin's absent /proc/net/dev is
            "nothing to show here". */}
        {sample.netReadable && (
          <>
            <Figure
              label={t('monitor.network')}
              value={
                sample.netRxRate === null || sample.netTxRate === null
                  ? t('monitor.sampling')
                  : t('monitor.netRate', { down: formatRate(sample.netRxRate), up: formatRate(sample.netTxRate) })
              }
            />
            {/* Auto-scaled to its own recent peak, same reasoning as the
                strip's network row: throughput has no ceiling a fixed 0-100
                could measure it against. */}
            <div className="mt-0.5 flex h-3 items-center">
              <Trend
                values={netScaled}
                tone={netCombined === null ? 'var(--vp-state-dead)' : 'var(--vp-accent)'}
              />
            </div>
            <Figure
              label={t('monitor.netTotal')}
              value={t('monitor.netRate', {
                down: formatBytes(sample.netRxBytes),
                up: formatBytes(sample.netTxBytes),
              })}
            />
          </>
        )}
      </div>

      {/* The reason this panel exists at all, for the person who asked for it:
          the machine meters say the box is busy, and this says which session is
          doing it. */}
      <div className="mt-2 border-t border-hairline pt-1.5">
        <div className="mb-1 flex items-baseline justify-between gap-2">
          <p className="text-vp-xs uppercase tracking-wide text-ink-2">{t('monitor.perSession')}</p>
          {/* Process count decides whether the numbers below mean anything: a
              pane sitting at a shell prompt is one process and reads zero,
              which is true and uninteresting. It was a line under the list,
              which is where nobody looked; beside the heading it is read
              before the rows rather than after them. */}
          {measured.length > 0 && (
            <p className="tabular shrink-0 text-vp-xs text-ink-2">
              {procs === 1 ? t('monitor.oneProc') : t('monitor.procs', { n: String(procs) })}
            </p>
          )}
        </div>
        {usage && !usage.readable ? (
          <p className="text-vp-sm leading-relaxed text-ink-2">{t('monitor.noProc')}</p>
        ) : measured.length === 0 ? (
          <p className="text-vp-sm text-ink-2">
            {usage ? t('monitor.noSessions') : t('monitor.sampling')}
          </p>
        ) : (
          <div className="vp-rows">
            {measured.map(({ session, usage: u }) => (
              <SessionRow
                key={session.id}
                session={session}
                usage={u}
                now={now}
                density={density}
                open={expanded[session.id] ?? session.id === culpritID}
                onToggle={() =>
                  setExpanded((e) => ({
                    ...e,
                    [session.id]: !(e[session.id] ?? session.id === culpritID),
                  }))
                }
                onManage={onManage}
              />
            ))}
          </div>
        )}
        {/* What the agents cost together, against the machine meters at the
            top. Two sessions at 30% each under a machine reading 95% says the
            other 35% is something else on the box, which is a conclusion
            neither half of this panel could offer on its own. */}
        {measured.length > 1 && (
          <div className="mt-1 border-t border-hairline pt-1">
            <Figure
              label={t('monitor.total')}
              value={`${Math.min(100, sessionCpu).toFixed(sessionCpu < 10 ? 1 : 0)}% · ${formatBytes(sessionRss)}`}
            />
          </div>
        )}
      </div>
    </div>
  )
}
