import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  AlertTriangle,
  Archive,
  CheckCircle2,
  ChevronRight,
  Cpu,
  HardDrive,
  Maximize2,
  MemoryStick,
  Minimize2,
  Network,
  PanelRight,
  PanelRightClose,
  Pause,
  Play,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  SlidersHorizontal,
  TerminalSquare,
  Unlink,
  X,
} from 'lucide-react'

import { api } from '../protocol/api'
import type {
  MachineProc,
  MachineWhere,
  ProcUsage,
  Project,
  ResourcesView,
  Session,
  SessionState,
  SessionUsage,
  SystemSample,
  UsageSample,
} from '../protocol/wire'
import { t, useLang, type Key } from '../i18n'
import { formatBytes, formatRate, meterText, meterWidth } from './panels/meter'
import { historyLength } from './panels/Trend'
import {
  clampPct,
  formatUptime,
  memoryParts,
  overallPosture,
  pressureTone,
  scaleDualSeries,
} from './panels/monitorMath'
import { sessionLabel } from './label'
import { StateDot } from './StateDot'
import { safeText } from './text'
import { useDialogFocus } from './useDialogFocus'
import { useSlidingMarker } from './useSlidingMarker'
import { NARROW_QUERY, useMediaQuery } from '../hooks/useMediaQuery'

const CADENCES = [1000, 2000, 5000] as const
type CadenceMs = (typeof CADENCES)[number]

type SizeMode = 'standard' | 'wide' | 'drawer'
type InspectorTab = 'sessions' | 'projects' | 'host'
type SortKey = 'cpu' | 'mem' | 'procs'

const INSPECTOR_ORDER: InspectorTab[] = ['sessions', 'projects', 'host']
const CULPRIT_CPU = 60
const SIZE_STORAGE_KEY = 'vp.monitor.size'

interface SeriesHistory {
  cpu: number[]
  mem: number[]
  swap: number[]
  netRx: number[]
  netTx: number[]
  diskRead: number[]
  diskWrite: number[]
}

const EMPTY_HISTORY: SeriesHistory = {
  cpu: [],
  mem: [],
  swap: [],
  netRx: [],
  netTx: [],
  diskRead: [],
  diskWrite: [],
}

interface MeasuredRow {
  id: string
  title: string
  projectId: string
  projectName: string
  archived: boolean
  state: SessionState
  exited: boolean
  exitStatus: number
  stateChangedAt: number
  usage: SessionUsage
  held: number
  frozen: boolean
  sessionObj: Session | null
}

function readStoredSize(): SizeMode {
  try {
    const v = localStorage.getItem(SIZE_STORAGE_KEY)
    if (v === 'standard' || v === 'wide' || v === 'drawer') return v
  } catch {
    /* private mode or blocked storage */
  }
  return 'standard'
}

/**
 * Interactive single-series 0–100% area chart with 2px round-joined stroke,
 * ~10% wash fill, recessive 50% hairline, 2px surface-ringed endpoint, and
 * pointer crosshair readout.
 */
function LiveAreaChart({
  values,
  tone,
  cadenceMs,
  height = 52,
  unit = '%',
}: {
  values: number[]
  tone: string
  cadenceMs: number
  height?: number
  unit?: string
}) {
  const [hoverIdx, setHoverIdx] = useState<number | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const w = 240
  const padY = 5

  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    if (values.length < 2 || !svgRef.current) return
    const rect = svgRef.current.getBoundingClientRect()
    if (rect.width <= 0) return
    const ratio = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width))
    setHoverIdx(Math.round(ratio * (values.length - 1)))
  }

  if (values.length < 2) {
    return (
      <div
        className="relative flex w-full items-center justify-center rounded-md bg-bg/60"
        style={{ height }}
        aria-hidden="true"
      >
        <div className="h-px w-full bg-hairline" />
      </div>
    )
  }

  const step = w / (values.length - 1)
  const yOf = (v: number) => padY + (1 - clampPct(v) / 100) * (height - padY * 2)
  const pts = values.map((v, i) => [i * step, yOf(v)] as const)
  const d = pts.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ')
  const area = `${d} L${w},${height} L0,${height} Z`

  const activeIdx = hoverIdx !== null && hoverIdx < values.length ? hoverIdx : values.length - 1
  const [ax, ay] = pts[activeIdx]
  const activeVal = clampPct(values[activeIdx])
  const secAgo = Math.round(((values.length - 1 - activeIdx) * cadenceMs) / 1000)

  return (
    <div className="relative select-none">
      {hoverIdx !== null && (
        <div className="pointer-events-none mb-1 flex items-center justify-between text-vp-xs text-ink-2">
          <span>{secAgo <= 0 ? t('monitor.now') : t('monitor.agoSec', { n: secAgo })}</span>
          <span className="tabular font-medium text-ink">
            {activeVal.toFixed(activeVal < 10 ? 1 : 0)}
            {unit}
          </span>
        </div>
      )}
      <div className="relative">
        <svg
          ref={svgRef}
          className="vp-spark block w-full touch-none overflow-visible"
          style={{ height }}
          viewBox={`0 0 ${w} ${height}`}
          preserveAspectRatio="none"
          onPointerMove={onMove}
          onPointerLeave={() => setHoverIdx(null)}
          aria-hidden="true"
          focusable="false"
        >
          <line
            x1="0"
            y1={(height / 2).toFixed(1)}
            x2={w}
            y2={(height / 2).toFixed(1)}
            stroke="var(--vp-hairline)"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
          <path d={area} fill={tone} opacity="0.11" />
          <path
            d={d}
            fill="none"
            stroke={tone}
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            vectorEffect="non-scaling-stroke"
          />
          {hoverIdx !== null && (
            <line
              x1={ax.toFixed(1)}
              y1="0"
              x2={ax.toFixed(1)}
              y2={height}
              stroke="var(--vp-hairline-strong)"
              strokeWidth="1"
              vectorEffect="non-scaling-stroke"
            />
          )}
        </svg>
        {/* Surface-ringed dot anchored at exact (ax, ay) in unstretched CSS coordinates. */}
        <span
          aria-hidden="true"
          className="pointer-events-none absolute h-2 w-2 -translate-x-1/2 -translate-y-1/2 rounded-full"
          style={{
            left: `${(activeIdx / (values.length - 1)) * 100}%`,
            top: `${(ay / height) * 100}%`,
            background: tone,
            boxShadow: '0 0 0 2px var(--vp-surface)',
          }}
        />
      </div>
    </div>
  )
}

/**
 * Interactive dual-series throughput chart on one shared axis (never dual-axis),
 * with per-point hover crosshair readout in real byte-rate units.
 */
function DualSeriesChart({
  aRaw,
  bRaw,
  aLabel,
  bLabel,
  aTone,
  bTone,
  cadenceMs,
  floor = 1024,
  height = 52,
}: {
  aRaw: number[]
  bRaw: number[]
  aLabel: string
  bLabel: string
  aTone: string
  bTone: string
  cadenceMs: number
  floor?: number
  height?: number
}) {
  const [hoverIdx, setHoverIdx] = useState<number | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const len = Math.min(aRaw.length, bRaw.length)
  const aTrim = aRaw.slice(-len)
  const bTrim = bRaw.slice(-len)
  const { aScaled, bScaled } = scaleDualSeries(aTrim, bTrim, floor)

  const w = 240
  const padY = 5

  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    if (len < 2 || !svgRef.current) return
    const rect = svgRef.current.getBoundingClientRect()
    if (rect.width <= 0) return
    const ratio = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width))
    setHoverIdx(Math.round(ratio * (len - 1)))
  }

  if (len < 2) {
    return (
      <div
        className="relative flex w-full items-center justify-center rounded-md bg-bg/60"
        style={{ height }}
        aria-hidden="true"
      >
        <div className="h-px w-full bg-hairline" />
      </div>
    )
  }

  const step = w / (len - 1)
  const yOf = (v: number) => padY + (1 - clampPct(v) / 100) * (height - padY * 2)
  const pathOf = (arr: number[]) =>
    arr.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${yOf(v).toFixed(1)}`).join(' ')

  const dA = pathOf(aScaled)
  const dB = pathOf(bScaled)
  const activeIdx = hoverIdx !== null && hoverIdx < len ? hoverIdx : null
  const secAgo = activeIdx !== null ? Math.round(((len - 1 - activeIdx) * cadenceMs) / 1000) : 0

  return (
    <div className="relative select-none">
      {activeIdx !== null && (
        <div className="pointer-events-none mb-1 flex flex-wrap items-center justify-between gap-2 text-vp-xs text-ink-2">
          <span>{secAgo <= 0 ? t('monitor.now') : t('monitor.agoSec', { n: secAgo })}</span>
          <span className="tabular font-medium text-ink">
            {aLabel} {formatRate(aTrim[activeIdx])} · {bLabel} {formatRate(bTrim[activeIdx])}
          </span>
        </div>
      )}
      <svg
        ref={svgRef}
        className="vp-spark block w-full touch-none overflow-visible"
        style={{ height }}
        viewBox={`0 0 ${w} ${height}`}
        preserveAspectRatio="none"
        onPointerMove={onMove}
        onPointerLeave={() => setHoverIdx(null)}
        aria-hidden="true"
        focusable="false"
      >
        <line
          x1="0"
          y1={(height / 2).toFixed(1)}
          x2={w}
          y2={(height / 2).toFixed(1)}
          stroke="var(--vp-hairline)"
          strokeWidth="1"
          vectorEffect="non-scaling-stroke"
        />
        <path d={`${dA} L${w},${height} L0,${height} Z`} fill={aTone} opacity="0.10" />
        <path
          d={dB}
          fill="none"
          stroke={bTone}
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          vectorEffect="non-scaling-stroke"
        />
        <path
          d={dA}
          fill="none"
          stroke={aTone}
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          vectorEffect="non-scaling-stroke"
        />
        {activeIdx !== null && (
          <line
            x1={(activeIdx * step).toFixed(1)}
            y1="0"
            x2={(activeIdx * step).toFixed(1)}
            y2={height}
            stroke="var(--vp-hairline-strong)"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>
    </div>
  )
}

function SwatchLegend({
  items,
  cols = 2,
}: {
  items: { key: string; label: string; value: string; tone: string }[]
  cols?: 1 | 2
}) {
  return (
    <div className={`mt-2 grid gap-x-3 gap-y-1 ${cols === 1 ? 'grid-cols-1' : 'grid-cols-2'}`}>
      {items.map((it) => (
        <div key={it.key} className="flex min-w-0 items-center gap-1.5 text-vp-xs">
          <span
            aria-hidden="true"
            className="h-2 w-2 shrink-0 rounded-full"
            style={{ background: it.tone, boxShadow: 'inset 0 0 0 1px var(--vp-hairline)' }}
          />
          <span className="truncate text-ink-2">{it.label}</span>
          <span className="tabular ml-auto shrink-0 text-ink">{it.value}</span>
        </div>
      ))}
    </div>
  )
}

function splitRateParts(bytesPerSec: number): { num: string; unit: string } {
  const formatted = formatRate(bytesPerSec)
  const sp = formatted.indexOf(' ')
  if (sp < 0) return { num: formatted, unit: '' }
  return { num: formatted.slice(0, sp), unit: formatted.slice(sp + 1) }
}

function CoreMicroStrip({ cores }: { cores: number[] }) {
  const [hoverCore, setHoverCore] = useState<number | null>(null)
  if (cores.length === 0) return null
  // Cap visual columns at 32 by pairing adjacent logical threads on large servers
  // so every bar keeps a clean silhouette with whitespace around it.
  const shown =
    cores.length <= 32
      ? cores.map((v, i) => ({ label: String(i), pct: clampPct(v) }))
      : Array.from({ length: Math.ceil(cores.length / 2) }, (_, i) => {
          const a = cores[i * 2] ?? 0
          const b = cores[i * 2 + 1] ?? a
          return { label: `${i * 2}-${Math.min(cores.length - 1, i * 2 + 1)}`, pct: clampPct((a + b) / 2) }
        })

  const active = hoverCore !== null ? shown[hoverCore] : null

  return (
    <div className="mt-2.5 border-t border-hairline pt-2">
      <div className="mb-1 flex items-center justify-between text-vp-xs text-ink-2">
        <span>{t('monitor.coresGrid')}</span>
        {active && (
          <span className="tabular text-ink">
            {t('monitor.coreN', { n: active.label })} · {Math.round(active.pct)}%
          </span>
        )}
      </div>
      <div
        className="flex h-5 items-end gap-1"
        onPointerLeave={() => setHoverCore(null)}
        aria-hidden="true"
      >
        {shown.map((c, idx) => (
          <div
            key={c.label}
            onPointerEnter={() => setHoverCore(idx)}
            className="flex h-full min-w-0 flex-1 items-end overflow-hidden rounded-full bg-surface-2"
          >
            <span
              className="w-full rounded-full transition-[height] duration-300 ease-vp"
              style={{
                height: `${Math.max(10, c.pct)}%`,
                background: pressureTone(c.pct, 75, 92),
              }}
            />
          </div>
        ))}
      </div>
    </div>
  )
}

function ProcRow({ proc }: { proc: ProcUsage }) {
  const what = proc.cmd ? proc.cmd : proc.name
  const pct = clampPct(proc.cpuPercent)
  return (
    <div
      className="flex min-w-0 items-center gap-2 rounded-md bg-surface px-2.5 py-1.5 text-vp-xs border border-hairline"
      data-testid="session-top-proc"
    >
      <span
        className="min-w-0 flex-1 truncate text-ink"
        title={`${safeText(what)} (${t('monitor.pid', { n: proc.pid })})`}
      >
        {proc.detached && (
          <span
            className="mr-1.5 inline-flex items-center gap-0.5 rounded-md bg-surface-2 px-1.5 py-px text-ink-2"
            title={t('monitor.detachedWhy')}
            data-testid="session-top-proc-detached"
          >
            <Unlink size={10} aria-hidden="true" />
            {t('monitor.detached')}
          </span>
        )}
        <span className="font-mono">{safeText(what)}</span>{' '}
        <span className="text-ink-3">{t('monitor.pid', { n: proc.pid })}</span>
      </span>
      <span className="tabular w-12 shrink-0 text-right font-medium text-ink">
        {pct.toFixed(pct < 10 ? 1 : 0)}%
      </span>
      <span className="tabular w-20 shrink-0 text-right text-ink-2">{formatBytes(proc.rss)}</span>
    </div>
  )
}

function SessionUsageRow({
  row,
  now,
  sortBy,
  memCeiling,
  culprit,
  isolated,
  open,
  onToggle,
  onSelectSession,
  onManage,
  onFreezeToggle,
}: {
  row: MeasuredRow
  now: number
  sortBy: SortKey
  memCeiling: number
  culprit: boolean
  isolated: boolean
  open: boolean
  onToggle: () => void
  onSelectSession?: (id: string) => void
  onManage?: (id: string) => void
  onFreezeToggle: (id: string, freeze: boolean) => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const pct = clampPct(row.usage.cpuPercent)
  const memPct = memCeiling > 0 ? clampPct((row.usage.rss / memCeiling) * 100) : 0
  const primaryBarPct = sortBy === 'mem' ? memPct : pct
  const barTone =
    sortBy === 'mem' ? pressureTone(memPct, 50, 80) : pressureTone(pct, 35, 65)
  const dwell = Math.max(0, now - row.stateChangedAt)

  const handleFreeze = async () => {
    setBusy(true)
    try {
      await onFreezeToggle(row.id, !row.frozen)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      className={`rounded-vp border transition-colors duration-150 ${
        open
          ? 'border-hairline-strong bg-bg/55'
          : culprit
            ? 'border-hairline-strong bg-surface'
            : 'border-hairline bg-surface hover:border-hairline-strong'
      }`}
      data-testid="session-usage"
      data-session={row.id}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        data-testid="session-usage-toggle"
        className="vp-press flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left"
      >
        <ChevronRight
          size={13}
          aria-hidden="true"
          className={`shrink-0 text-ink-3 transition-transform duration-150 ease-vp ${open ? 'rotate-90 text-ink' : ''}`}
        />
        <StateDot state={row.state} size={9} exited={row.exited} exitStatus={row.exitStatus} />
        <div className="flex min-w-0 flex-1 items-center gap-1.5">
          <span className="truncate text-vp-sm font-medium text-ink" title={row.title}>
            {row.title}
          </span>
          {row.projectName && (
            <span className="hidden @xl:inline-flex max-w-36 shrink-0 items-center gap-1 truncate rounded-md bg-surface-2 px-1.5 py-px text-vp-xs text-ink-2">
              {row.archived && <Archive size={10} aria-hidden="true" />}
              <span className="truncate">{safeText(row.projectName)}</span>
            </span>
          )}
          {row.frozen && (
            <span className="inline-flex shrink-0 items-center gap-1 rounded-md bg-surface-2 px-1.5 py-px text-vp-xs text-warn-ink">
              <Pause size={10} aria-hidden="true" />
              {t('monitor.frozenBadge')}
            </span>
          )}
          {culprit && (
            <span className="hidden @2xl:inline-flex shrink-0 items-center gap-1 rounded-md bg-surface-2 px-1.5 py-px text-vp-xs text-warn-ink">
              <AlertTriangle size={10} aria-hidden="true" />
              {t('monitor.culprit')}
            </span>
          )}
        </div>

        {row.stateChangedAt > 0 && (
          <span
            className="hidden @2xl:inline-block tabular w-14 shrink-0 text-right text-vp-sm text-ink-2"
            data-testid="session-dwell"
            title={t('monitor.state')}
          >
            {formatUptime(dwell)}
          </span>
        )}
        <span
          className="tabular w-10 shrink-0 text-right text-vp-sm text-ink-2"
          data-testid="session-procs"
          title={row.usage.procs === 1 ? t('monitor.oneProc') : t('monitor.procs', { n: row.usage.procs })}
        >
          ×{row.usage.procs}
        </span>
        <span className="tabular w-12 shrink-0 text-right text-vp-sm font-medium text-ink">
          {pct.toFixed(pct < 10 ? 1 : 0)}%
        </span>
        <span className="tabular w-20 shrink-0 text-right text-vp-sm text-ink-2">
          {formatBytes(row.usage.rss)}
        </span>
      </button>

      <div className="px-3 pb-2">
        <div className="vp-bar h-1.5">
          <span className="vp-bar-fill" style={{ width: `${primaryBarPct}%`, background: barTone }} />
        </div>
      </div>

      <div className="vp-fold" data-open={open}>
        <div className="vp-fold-inner">
          {open && (
            <div className="border-t border-hairline px-3 py-2.5 space-y-2" data-testid="session-top">
              {row.usage.top && row.usage.top.length > 0 ? (
                <div className="space-y-1">
                  {row.usage.top.map((p) => (
                    <ProcRow key={`${p.pid}-${p.start}`} proc={p} />
                  ))}
                </div>
              ) : (
                <p className="text-vp-xs text-ink-2">{t('monitor.sampling')}</p>
              )}

              <div className="flex flex-wrap items-center gap-2 pt-0.5">
                {onSelectSession && !row.archived && row.sessionObj && (
                  <button
                    type="button"
                    data-testid="session-open-terminal"
                    onClick={() => onSelectSession(row.id)}
                    className="vp-outline text-vp-xs"
                  >
                    <TerminalSquare size={12} aria-hidden="true" />
                    {t('monitor.openTerminal')}
                  </button>
                )}
                {isolated && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => void handleFreeze()}
                    className="vp-outline text-vp-xs"
                  >
                    {row.frozen ? <Play size={12} aria-hidden="true" /> : <Pause size={12} aria-hidden="true" />}
                    {t(row.frozen ? 'monitor.thaw' : 'monitor.freeze')}
                  </button>
                )}
                {onManage && (
                  <button
                    type="button"
                    data-testid="session-manage"
                    onClick={() => onManage(row.id)}
                    className="vp-outline text-vp-xs"
                  >
                    <SlidersHorizontal size={12} aria-hidden="true" />
                    {t('monitor.manage')}
                  </button>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function HostWhereBadge({
  where,
  knownSessions,
  onJumpSession,
}: {
  where: MachineWhere
  knownSessions: Set<string>
  onJumpSession: (id: string) => void
}) {
  if (where.kind === 'session' && where.sessionId) {
    const title = safeText(where.sessionTitle ?? '')
    const project = safeText(where.projectName ?? '')
    const text = t('res.where.session', { name: project ? `${title} · ${project}` : title })
    if (knownSessions.has(where.sessionId)) {
      return (
        <button
          type="button"
          onClick={() => onJumpSession(where.sessionId!)}
          className="vp-press truncate text-left text-accent underline decoration-dotted underline-offset-2"
        >
          {text}
        </button>
      )
    }
    return <span className="truncate">{text}</span>
  }
  const label = safeText(where.label ?? '')
  return <span className="truncate">{t(`res.where.${where.kind}` as Key, { label, name: label })}</span>
}

function HostProcessList({
  procs,
  memTotal,
  knownSessions,
  onJumpSession,
}: {
  procs: MachineProc[]
  memTotal: number
  knownSessions: Set<string>
  onJumpSession: (id: string) => void
}) {
  if (procs.length === 0) {
    return <p className="py-6 text-center text-vp-sm text-ink-2">{t('res.machineEmpty')}</p>
  }
  const maxRss = Math.max(1, ...procs.map((p) => p.rss), memTotal > 0 ? memTotal * 0.25 : 1)
  return (
    <div className="space-y-1.5" data-testid="monitor-host-procs">
      {procs.map((p) => {
        const cmd = safeText(p.cmd || p.name)
        const barPct = clampPct((p.rss / maxRss) * 100)
        return (
          <div
            key={`${p.pid}-${p.start}`}
            className="rounded-vp border border-hairline bg-surface px-3 py-2"
          >
            <div className="flex min-w-0 items-baseline justify-between gap-3">
              <span
                className="min-w-0 flex-1 truncate font-mono text-vp-sm text-ink"
                title={`${cmd} (${t('monitor.pid', { n: p.pid })})`}
              >
                {cmd}
              </span>
              <span className="tabular shrink-0 text-vp-sm font-medium text-ink">
                {formatBytes(p.rss)}
              </span>
            </div>
            <div className="mt-1 vp-bar h-1">
              <span className="vp-bar-fill" style={{ width: `${barPct}%`, background: 'var(--mon-s2)' }} />
            </div>
            <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-vp-xs text-ink-2">
              <HostWhereBadge where={p.where} knownSessions={knownSessions} onJumpSession={onJumpSession} />
              <span aria-hidden="true">·</span>
              <span>{safeText(p.user)}</span>
              <span aria-hidden="true">·</span>
              <span>{t('monitor.pid', { n: p.pid })}</span>
              {p.swap > 0 && (
                <>
                  <span aria-hidden="true">·</span>
                  <span className="tabular">{t('res.swapped', { n: formatBytes(p.swap) })}</span>
                </>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}

export function MonitorView({
  sessions,
  projects,
  onClose,
  onManage,
  onSelectSession,
}: {
  sessions: Session[]
  projects: Project[]
  onClose: () => void
  onManage?: (sessionId?: string) => void
  onSelectSession?: (sessionId: string) => void
}) {
  const lang = useLang()
  const narrow = useMediaQuery(NARROW_QUERY)
  const dialogRef = useRef<HTMLDivElement>(null)

  const [sizeMode, setSizeMode] = useState<SizeMode>(readStoredSize)
  const [cadence, setCadence] = useState<CadenceMs>(2000)
  const [paused, setPaused] = useState(false)
  const [tab, setTab] = useState<InspectorTab>('sessions')
  const [slideDir, setSlideDir] = useState<'left' | 'right'>('right')
  const [sortBy, setSortBy] = useState<SortKey>('cpu')

  const [sample, setSample] = useState<SystemSample | null>(null)
  const [usage, setUsage] = useState<UsageSample | null>(null)
  const [resources, setResources] = useState<ResourcesView | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  const [history, setHistory] = useState<SeriesHistory>(EMPTY_HISTORY)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

  const isDrawer = !narrow && sizeMode === 'drawer'
  // Trap focus when modal; in side-drawer companion mode leave focus free so the
  // user can type in the terminal beside the live monitor while Escape still closes.
  useDialogFocus(dialogRef, onClose, { escape: !isDrawer })

  useEffect(() => {
    if (!isDrawer) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented) {
        e.stopPropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isDrawer, onClose])

  const changeSize = (next: SizeMode) => {
    setSizeMode(next)
    try {
      localStorage.setItem(SIZE_STORAGE_KEY, next)
    } catch {
      /* ignore blocked storage */
    }
  }

  const switchTab = (next: InspectorTab) => {
    if (next === tab) return
    setSlideDir(INSPECTOR_ORDER.indexOf(next) >= INSPECTOR_ORDER.indexOf(tab) ? 'right' : 'left')
    setTab(next)
  }

  const fetchOnce = useCallback(async () => {
    try {
      const [nextSys, nextUse, nextRes] = await Promise.all([
        api.system(),
        api.usage(),
        api.resources().catch(() => null),
      ])
      setSample(nextSys)
      setUsage(nextUse)
      if (nextRes) setResources(nextRes)
      setNow(Math.floor(Date.now() / 1000))
      setError(null)
      const cap = historyLength(cadence)
      setHistory((prev) => {
        const push = (arr: number[], v: number) => [...arr, v].slice(-cap)
        const memPct =
          nextSys.memTotal > 0 ? ((nextSys.memTotal - nextSys.memAvailable) / nextSys.memTotal) * 100 : 0
        const swapPct =
          nextSys.swapTotal > 0 ? ((nextSys.swapTotal - nextSys.swapFree) / nextSys.swapTotal) * 100 : 0
        return {
          cpu: nextSys.cpuPercent !== null ? push(prev.cpu, nextSys.cpuPercent) : prev.cpu,
          mem: nextSys.memTotal > 0 ? push(prev.mem, memPct) : prev.mem,
          swap: nextSys.swapTotal > 0 ? push(prev.swap, swapPct) : prev.swap,
          netRx: nextSys.netRxRate !== null ? push(prev.netRx, nextSys.netRxRate) : prev.netRx,
          netTx: nextSys.netTxRate !== null ? push(prev.netTx, nextSys.netTxRate) : prev.netTx,
          diskRead:
            nextSys.diskReadRate !== undefined && nextSys.diskReadRate !== null
              ? push(prev.diskRead, nextSys.diskReadRate)
              : prev.diskRead,
          diskWrite:
            nextSys.diskWriteRate !== undefined && nextSys.diskWriteRate !== null
              ? push(prev.diskWrite, nextSys.diskWriteRate)
              : prev.diskWrite,
        }
      })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [cadence])

  useEffect(() => {
    if (paused) return
    let cancelled = false
    let timer = 0
    const loop = async () => {
      await fetchOnce()
      if (!cancelled) timer = window.setTimeout(() => void loop(), cadence)
    }
    void loop()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [paused, cadence, fetchOnce])

  const cadenceRef = useRef<HTMLDivElement>(null)
  const cadenceMarker = useSlidingMarker(cadenceRef, `${cadence}:${lang}`)
  const tabRef = useRef<HTMLDivElement>(null)
  const hasHostProcs = Boolean(resources?.supported && resources.machine.length > 0)
  const activeTab: InspectorTab = tab === 'host' && !hasHostProcs ? 'sessions' : tab
  const tabMarker = useSlidingMarker(tabRef, `${activeTab}:${hasHostProcs}:${lang}`)
  const sortRef = useRef<HTMLDivElement>(null)
  const sortMarker = useSlidingMarker(sortRef, `${sortBy}:${activeTab}:${lang}`)

  const projectNames = useMemo(() => {
    const m = new Map<string, string>()
    for (const p of projects) m.set(p.id, p.name)
    return m
  }, [projects])

  const resSessionById = useMemo(() => {
    const m = new Map<string, ResourcesView['sessions'][number]>()
    if (resources?.sessions) {
      for (const s of resources.sessions) m.set(s.id, s)
    }
    return m
  }, [resources])

  // Keyed strictly off `/api/usage` so unmeasured / empty usage payloads draw
  // zero session rows, while enriching live rows with cgroup memory and
  // surfacing running sessions inside archived projects when `/api/usage`
  // measured them.
  const measured = useMemo<MeasuredRow[]>(() => {
    if (!usage || !usage.readable) return []
    const seen = new Set<string>()
    const rows: MeasuredRow[] = []

    for (const s of sessions) {
      const u = usage.sessions[s.id]
      if (!u) continue
      seen.add(s.id)
      const rs = resSessionById.get(s.id)
      rows.push({
        id: s.id,
        title: sessionLabel(s),
        projectId: s.projectId,
        projectName: rs?.projectName || projectNames.get(s.projectId) || '',
        archived: false,
        state: s.state,
        exited: s.exited,
        exitStatus: s.exitStatus,
        stateChangedAt: s.stateChangedAt,
        usage: u,
        held: rs?.held ?? 0,
        frozen: rs?.frozen ?? false,
        sessionObj: s,
      })
    }

    if (resources?.sessions) {
      for (const rs of resources.sessions) {
        if (seen.has(rs.id)) continue
        const u = usage.sessions[rs.id]
        if (!u) continue
        rows.push({
          id: rs.id,
          title: safeText(rs.title),
          projectId: rs.projectId,
          projectName: rs.projectName,
          archived: rs.archived,
          state: 'working',
          exited: false,
          exitStatus: 0,
          stateChangedAt: rs.lastUsedAt || rs.createdAt,
          usage: u,
          held: rs.held,
          frozen: rs.frozen,
          sessionObj: null,
        })
      }
    }

    rows.sort((a, b) => {
      if (sortBy === 'mem') return b.usage.rss - a.usage.rss || b.usage.cpuPercent - a.usage.cpuPercent
      if (sortBy === 'procs') return b.usage.procs - a.usage.procs || b.usage.cpuPercent - a.usage.cpuPercent
      return b.usage.cpuPercent - a.usage.cpuPercent || b.usage.rss - a.usage.rss
    })
    return rows
  }, [usage, sessions, resSessionById, resources, projectNames, sortBy])

  const projectGroups = useMemo(() => {
    const map = new Map<
      string,
      {
        key: string
        name: string
        archived: boolean
        sessions: number
        procs: number
        cpu: number
        rss: number
      }
    >()
    for (const r of measured) {
      const key = r.projectId || '__other__'
      const cur = map.get(key) ?? {
        key,
        name: r.projectName || t('res.where.other'),
        archived: r.archived,
        sessions: 0,
        procs: 0,
        cpu: 0,
        rss: 0,
      }
      cur.sessions += 1
      cur.procs += r.usage.procs
      cur.cpu += r.usage.cpuPercent
      cur.rss += r.usage.rss
      map.set(key, cur)
    }
    const list = [...map.values()]
    list.sort((a, b) => {
      if (sortBy === 'mem') return b.rss - a.rss || b.cpu - a.cpu
      if (sortBy === 'procs') return b.procs - a.procs || b.cpu - a.cpu
      return b.cpu - a.cpu || b.rss - a.rss
    })
    return list
  }, [measured, sortBy])

  const totalProcs = measured.reduce((n, r) => n + r.usage.procs, 0)
  const totalSessionCpu = clampPct(measured.reduce((n, r) => n + r.usage.cpuPercent, 0))
  const totalSessionRss = measured.reduce((n, r) => n + r.usage.rss, 0)

  const highestCpuRow = useMemo(() => {
    if (measured.length === 0) return null
    return [...measured].sort((a, b) => b.usage.cpuPercent - a.usage.cpuPercent)[0]
  }, [measured])

  const culpritID =
    sample?.cpuReadable && (sample.cpuPercent ?? 0) >= CULPRIT_CPU && highestCpuRow
      ? highestCpuRow.id
      : null

  const posture = overallPosture(sample, sample && sample.memTotal > 0 ? resources?.level : undefined)
  const PostureIcon =
    posture === 'critical' ? ShieldAlert : posture === 'warn' ? AlertTriangle : CheckCircle2
  const postureText =
    posture === 'critical'
      ? t('monitor.critical')
      : posture === 'warn'
        ? t('monitor.elevated')
        : t('monitor.healthy')
  const postureClass =
    posture === 'critical'
      ? 'text-danger-ink'
      : posture === 'warn'
        ? 'text-warn-ink'
        : 'text-done-ink'

  const handleFreezeToggle = async (sessionId: string, freeze: boolean) => {
    await api.freezeSession(sessionId, freeze)
    await fetchOnce()
  }

  const handleJumpSession = (sessionId: string) => {
    switchTab('sessions')
    setExpanded((prev) => ({ ...prev, [sessionId]: true }))
  }

  const handleOpenTerminal = (sessionId: string) => {
    if (!onSelectSession) return
    onSelectSession(sessionId)
    if (!isDrawer) onClose()
  }

  const knownSessionIds = useMemo(() => new Set(measured.map((r) => r.id)), [measured])

  const renderDashboardBody = () => {
    if (!sample && error) {
      return (
        <div
          className="flex flex-col items-center justify-center gap-3 rounded-vp border border-dashed border-hairline-strong bg-surface px-6 py-12 text-center"
          data-testid="system-monitor"
        >
          <div className="flex items-center gap-2 text-vp-base text-danger-ink">
            <AlertTriangle size={16} className="shrink-0" aria-hidden="true" />
            <span>{safeText(error)}</span>
          </div>
          <button type="button" onClick={() => void fetchOnce()} className="vp-outline text-vp-sm">
            <RefreshCw size={13} aria-hidden="true" />
            {t('monitor.retry')}
          </button>
        </div>
      )
    }

    if (!sample) {
      return (
        <div className="space-y-3" data-testid="system-monitor">
          <p className="sr-only">{t('monitor.reading')}</p>
          <div className="grid grid-cols-1 gap-3 @xl:grid-cols-2 @5xl:grid-cols-4">
            {[0, 1, 2, 3].map((i) => (
              <div key={i} className="rounded-vp border border-hairline bg-surface p-4 space-y-3">
                <div className="vp-skeleton h-4 w-20 rounded-md" />
                <div className="vp-skeleton h-8 w-28 rounded-md" />
                <div className="vp-skeleton h-12 w-full rounded-md" />
              </div>
            ))}
          </div>
          <div className="rounded-vp border border-hairline bg-surface p-4 space-y-2.5">
            <div className="vp-skeleton h-6 w-40 rounded-md" />
            <div className="vp-skeleton h-10 w-full rounded-md" />
            <div className="vp-skeleton h-10 w-full rounded-md" />
          </div>
        </div>
      )
    }

    const coresText = t('monitor.cores', { n: String(sample.cores) })
    const mem = memoryParts(sample, resources)
    const swapUsed = Math.max(0, sample.swapTotal - sample.swapFree)
    const swapPct = sample.swapTotal > 0 ? clampPct((swapUsed / sample.swapTotal) * 100) : null
    const diskUsed = Math.max(0, sample.diskTotal - sample.diskFree)
    const diskPct = sample.diskTotal > 0 ? clampPct((diskUsed / sample.diskTotal) * 100) : null

    const netCombined =
      sample.netRxRate !== null && sample.netTxRate !== null
        ? sample.netRxRate + sample.netTxRate
        : null
    const netPeak = Math.max(1024, ...history.netRx, ...history.netTx)

    const hasCpuSplit =
      sample.cpuReadable &&
      sample.cpuPercent !== null &&
      typeof sample.cpuUser === 'number' &&
      typeof sample.cpuSystem === 'number'
    const cpuUser = clampPct(sample.cpuUser ?? 0)
    const cpuSys = clampPct(sample.cpuSystem ?? 0)
    const cpuWait = clampPct(sample.cpuIowait ?? 0)
    const cpuSteal = clampPct(sample.cpuSteal ?? 0)

    const isolated = resources?.isolation.state === 'isolated'
    const memCeiling = sample.memTotal > 0 ? sample.memTotal : Math.max(1, totalSessionRss)

    return (
      <div className="@container space-y-3" data-testid="system-monitor">
        {error && (
          <div
            role="status"
            className="flex items-center gap-2 rounded-vp border border-hairline-strong bg-surface px-3.5 py-2 text-vp-sm text-warn-ink"
          >
            <AlertTriangle size={14} className="shrink-0" aria-hidden="true" />
            <span className="min-w-0 flex-1 truncate">
              {t('monitor.stale')} · {safeText(error)}
            </span>
            <button type="button" onClick={() => void fetchOnce()} className="vp-outline text-vp-xs">
              <RefreshCw size={12} aria-hidden="true" />
              {t('monitor.retry')}
            </button>
          </div>
        )}

        {/* Top row: Four precision instrument cards */}
        <div className="grid grid-cols-1 gap-3 @xl:grid-cols-2 @5xl:grid-cols-4">
          {/* 1. CPU & Per-core card */}
          <section className="flex min-w-0 flex-col justify-between rounded-vp border border-hairline bg-surface p-3.5 shadow-[var(--depth-raised)]">
            <div>
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-vp-sm font-medium text-ink-2">
                  <Cpu size={13} className="shrink-0 text-ink-3" aria-hidden="true" />
                  {t('monitor.cpu')}
                </span>
                <span className="rounded-md bg-surface-2 px-1.5 py-0.5 text-vp-xs text-ink-2">
                  {coresText}
                </span>
              </div>

              <div className="mt-2 flex items-baseline justify-between gap-2">
                <span className="text-vp-2xl font-semibold tracking-tight text-ink">
                  {meterText(sample.cpuPercent)}
                </span>
                <span className="tabular truncate text-vp-xs text-ink-2">
                  {!sample.cpuReadable
                    ? t('monitor.unavailable')
                    : sample.cpuPercent === null
                      ? t('monitor.sampling')
                      : typeof sample.psiCpu === 'number' && sample.psiCpu >= 1
                        ? t('monitor.psiStall', { n: sample.psiCpu.toFixed(1) })
                        : ''}
                </span>
              </div>

              <div className="mt-2">
                <LiveAreaChart
                  values={history.cpu}
                  tone={pressureTone(sample.cpuPercent)}
                  cadenceMs={cadence}
                />
              </div>
            </div>

            <div className="mt-2.5 space-y-2">
              {hasCpuSplit && (
                <div>
                  <div className="vp-seg-bar h-1.5">
                    {cpuUser > 0 && (
                      <span style={{ width: `${cpuUser}%`, background: 'var(--mon-s1)' }} />
                    )}
                    {cpuSys > 0 && (
                      <span style={{ width: `${cpuSys}%`, background: 'var(--mon-s2)' }} />
                    )}
                    {cpuWait > 0 && (
                      <span style={{ width: `${cpuWait}%`, background: 'var(--mon-s3)' }} />
                    )}
                    {cpuSteal >= 0.5 && (
                      <span style={{ width: `${cpuSteal}%`, background: 'var(--mon-crit)' }} />
                    )}
                  </div>
                  <SwatchLegend
                    items={[
                      {
                        key: 'user',
                        label: t('monitor.user'),
                        value: `${Math.round(cpuUser)}%`,
                        tone: 'var(--mon-s1)',
                      },
                      {
                        key: 'sys',
                        label: t('monitor.kernel'),
                        value: `${Math.round(cpuSys)}%`,
                        tone: 'var(--mon-s2)',
                      },
                      ...(cpuWait >= 0.5
                        ? [
                            {
                              key: 'wait',
                              label: t('monitor.iowait'),
                              value: `${Math.round(cpuWait)}%`,
                              tone: 'var(--mon-s3)',
                            },
                          ]
                        : []),
                      ...(cpuSteal >= 0.5
                        ? [
                            {
                              key: 'steal',
                              label: t('monitor.steal'),
                              value: `${Math.round(cpuSteal)}%`,
                              tone: 'var(--mon-crit)',
                            },
                          ]
                        : []),
                    ]}
                  />
                </div>
              )}

              {sample.corePercents && sample.corePercents.length > 0 && (
                <CoreMicroStrip cores={sample.corePercents} />
              )}

              {(sample.load1 > 0 || sample.load5 > 0 || sample.load15 > 0) && (
                <div
                  className="flex items-baseline justify-between gap-2 border-t border-hairline pt-2 text-vp-xs"
                  title={
                    sample.cores > 0
                      ? t('monitor.perCore', { n: (sample.load1 / sample.cores).toFixed(2) })
                      : undefined
                  }
                >
                  <span className="text-ink-2">{t('monitor.load')}</span>
                  <span className="tabular text-ink">
                    {sample.load1.toFixed(2)} · {sample.load5.toFixed(2)} · {sample.load15.toFixed(2)}
                  </span>
                </div>
              )}
            </div>
          </section>

          {/* 2. Memory & Cgroup Pool card */}
          <section className="flex min-w-0 flex-col justify-between rounded-vp border border-hairline bg-surface p-3.5 shadow-[var(--depth-raised)]">
            <div>
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-vp-sm font-medium text-ink-2">
                  <MemoryStick size={13} className="shrink-0 text-ink-3" aria-hidden="true" />
                  {t('monitor.memory')}
                </span>
                {mem.valid && resources?.supported && (
                  <span
                    className="inline-flex shrink-0 items-center gap-1 rounded-md bg-surface-2 px-1.5 py-0.5 text-vp-xs text-ink-2"
                    title={isolated ? t('res.isolated') : t('res.shared')}
                  >
                    {isolated ? (
                      <ShieldCheck size={11} className="shrink-0 text-done-ink" aria-hidden="true" />
                    ) : (
                      <ShieldAlert size={11} className="shrink-0 text-ink-3" aria-hidden="true" />
                    )}
                    <span>{isolated ? t('monitor.isoShort') : t('monitor.sharedShort')}</span>
                  </span>
                )}
              </div>

              <div className="mt-2 flex items-baseline justify-between gap-2">
                <span className="text-vp-2xl font-semibold tracking-tight text-ink">
                  {meterText(mem.pct)}
                </span>
                <span className="tabular truncate text-vp-xs text-ink-2">
                  {mem.valid
                    ? t('monitor.free', { size: formatBytes(sample.memAvailable) })
                    : t('monitor.unavailable')}
                </span>
              </div>

              <div className="mt-2">
                <LiveAreaChart
                  values={history.mem}
                  tone={pressureTone(mem.pct, 75, 90)}
                  cadenceMs={cadence}
                />
              </div>
            </div>

            {mem.valid && (
              <div className="mt-2.5 space-y-2">
                <div>
                  <div className="vp-seg-bar h-2">
                    {mem.segments.map((seg) => (
                      <span
                        key={seg.key}
                        style={{ width: `${seg.pct}%`, background: seg.tone }}
                      />
                    ))}
                  </div>
                  <SwatchLegend
                    items={[
                      ...mem.segments.map((seg) => ({
                        key: seg.key,
                        label:
                          seg.key === 'held'
                            ? t('monitor.memHeld')
                            : seg.key === 'host'
                              ? t('monitor.memHost')
                              : seg.key === 'cache'
                                ? t('monitor.memCache')
                                : t('monitor.memUsed'),
                        value: formatBytes(seg.bytes),
                        tone: seg.tone,
                      })),
                      {
                        key: 'avail',
                        label: t('monitor.memAvail'),
                        value: formatBytes(Math.max(0, sample.memTotal - mem.used - mem.reclaimable)),
                        tone: 'var(--vp-surface-2)',
                      },
                    ]}
                  />
                </div>

                {(sample.swapTotal > 0 || (resources?.supported && resources.pool.max > 0)) && (
                  <div className="space-y-1 border-t border-hairline pt-2 text-vp-xs">
                    {sample.swapTotal > 0 && swapPct !== null && (
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-ink-2">{t('monitor.swap')}</span>
                        <span className="tabular text-ink">
                          {t('monitor.of', {
                            used: formatBytes(swapUsed),
                            total: formatBytes(sample.swapTotal),
                          })}
                        </span>
                      </div>
                    )}
                    {resources?.supported && resources.pool.max > 0 && (
                      <div className="flex items-center justify-between gap-2">
                        <span className="truncate text-ink-2">
                          {t('monitor.poolCap', {
                            used: formatBytes(resources.pool.current),
                            max: formatBytes(resources.pool.max),
                          })}
                        </span>
                        {typeof sample.psiMem === 'number' && sample.psiMem >= 1 && (
                          <span className="tabular shrink-0 text-warn-ink">
                            {t('monitor.psiStall', { n: sample.psiMem.toFixed(1) })}
                          </span>
                        )}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </section>

          {/* 3. Disk Space & Block I/O card */}
          <section className="flex min-w-0 flex-col justify-between rounded-vp border border-hairline bg-surface p-3.5 shadow-[var(--depth-raised)]">
            <div>
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-vp-sm font-medium text-ink-2">
                  <HardDrive size={13} className="shrink-0 text-ink-3" aria-hidden="true" />
                  {t('monitor.disk')}
                </span>
                {sample.diskPath ? (
                  <span
                    className="max-w-36 truncate rounded-md bg-surface-2 px-1.5 py-0.5 font-mono text-vp-xs text-ink-2"
                    title={`${t('monitor.mount')}: ${safeText(sample.diskPath)}`}
                  >
                    {t('monitor.mount')} {safeText(sample.diskPath)}
                  </span>
                ) : null}
              </div>

              <div className="mt-2 flex items-baseline justify-between gap-2">
                <span className="text-vp-2xl font-semibold tracking-tight text-ink">
                  {meterText(diskPct)}
                </span>
                <span className="tabular truncate text-vp-xs text-ink-2">
                  {sample.diskTotal > 0
                    ? t('monitor.free', { size: formatBytes(sample.diskFree) })
                    : t('monitor.unavailable')}
                </span>
              </div>

              <div className="mt-2.5 vp-bar h-2">
                <span
                  className="vp-bar-fill"
                  style={{
                    width: `${meterWidth(diskPct)}%`,
                    background: pressureTone(diskPct, 80, 92),
                  }}
                />
              </div>
              {sample.diskTotal > 0 && (
                <div className="mt-1 flex items-center justify-between text-vp-xs text-ink-2">
                  <span className="tabular">
                    {t('monitor.of', {
                      used: formatBytes(diskUsed),
                      total: formatBytes(sample.diskTotal),
                    })}
                  </span>
                  {typeof sample.psiIo === 'number' && sample.psiIo >= 1 && (
                    <span className="tabular text-warn-ink">
                      {t('monitor.psiStall', { n: sample.psiIo.toFixed(1) })}
                    </span>
                  )}
                </div>
              )}
            </div>

            {sample.diskIoReadable && (
              <div className="mt-3 border-t border-hairline pt-2.5">
                <div className="mb-1.5 flex items-center justify-between text-vp-xs text-ink-2">
                  <span>{t('monitor.diskIo')}</span>
                  {sample.diskReadRate === null ||
                  sample.diskReadRate === undefined ||
                  sample.diskWriteRate === null ||
                  sample.diskWriteRate === undefined ? (
                    <span>{t('monitor.sampling')}</span>
                  ) : null}
                </div>
                <DualSeriesChart
                  aRaw={history.diskRead}
                  bRaw={history.diskWrite}
                  aLabel={t('monitor.readShort')}
                  bLabel={t('monitor.writeShort')}
                  aTone="var(--mon-s1)"
                  bTone="var(--mon-s3)"
                  cadenceMs={cadence}
                  floor={4096}
                  height={40}
                />
                {sample.diskReadRate !== null &&
                  sample.diskReadRate !== undefined &&
                  sample.diskWriteRate !== null &&
                  sample.diskWriteRate !== undefined && (
                    <SwatchLegend
                      items={[
                        {
                          key: 'read',
                          label: t('monitor.readShort'),
                          value: formatRate(sample.diskReadRate),
                          tone: 'var(--mon-s1)',
                        },
                        {
                          key: 'write',
                          label: t('monitor.writeShort'),
                          value: formatRate(sample.diskWriteRate),
                          tone: 'var(--mon-s3)',
                        },
                      ]}
                    />
                  )}
              </div>
            )}
          </section>

          {/* 4. Network Throughput & Host Uptime card */}
          <section className="flex min-w-0 flex-col justify-between rounded-vp border border-hairline bg-surface p-3.5 shadow-[var(--depth-raised)]">
            <div>
              <div className="flex items-center justify-between gap-2">
                <span className="flex items-center gap-1.5 text-vp-sm font-medium text-ink-2">
                  <Network size={13} className="shrink-0 text-ink-3" aria-hidden="true" />
                  {t('monitor.network')}
                </span>
                {sample.uptime > 0 && (
                  <span className="rounded-md bg-surface-2 px-1.5 py-0.5 text-vp-xs text-ink-2">
                    {t('monitor.up', { d: formatUptime(sample.uptime) })}
                  </span>
                )}
              </div>

              <div className="mt-2 flex items-baseline justify-between gap-2">
                {!sample.netReadable || netCombined === null ? (
                  <span className="text-vp-2xl font-semibold tracking-tight text-ink">—</span>
                ) : (
                  (() => {
                    const parts = splitRateParts(netCombined)
                    return (
                      <span className="flex shrink-0 items-baseline gap-1 whitespace-nowrap">
                        <span className="text-vp-2xl font-semibold tracking-tight text-ink">
                          {parts.num}
                        </span>
                        {parts.unit && (
                          <span className="text-vp-sm font-medium text-ink-2">{parts.unit}</span>
                        )}
                      </span>
                    )
                  })()
                )}
                <span className="tabular truncate text-vp-xs text-ink-2">
                  {!sample.netReadable
                    ? t('monitor.unavailable')
                    : netCombined === null
                      ? t('monitor.sampling')
                      : t('monitor.peak', { v: formatRate(netPeak) })}
                </span>
              </div>

              <div className="mt-2">
                <DualSeriesChart
                  aRaw={history.netRx}
                  bRaw={history.netTx}
                  aLabel={`↓ ${t('monitor.downShort')}`}
                  bLabel={`↑ ${t('monitor.upShort')}`}
                  aTone="var(--mon-s1)"
                  bTone="var(--mon-s2)"
                  cadenceMs={cadence}
                />
              </div>
            </div>

            {sample.netReadable && (
              <div className="mt-2.5 space-y-2">
                <SwatchLegend
                  cols={1}
                  items={[
                    {
                      key: 'rx',
                      label: `↓ ${t('monitor.downShort')}`,
                      value:
                        sample.netRxRate === null ? '—' : formatRate(sample.netRxRate),
                      tone: 'var(--mon-s1)',
                    },
                    {
                      key: 'tx',
                      label: `↑ ${t('monitor.upShort')}`,
                      value:
                        sample.netTxRate === null ? '—' : formatRate(sample.netTxRate),
                      tone: 'var(--mon-s2)',
                    },
                  ]}
                />
                <div className="flex items-baseline justify-between gap-2 border-t border-hairline pt-2 text-vp-xs">
                  <span className="shrink-0 text-ink-2">{t('monitor.netTotal')}</span>
                  <span className="tabular truncate text-ink">
                    {t('monitor.netRate', {
                      down: formatBytes(sample.netRxBytes),
                      up: formatBytes(sample.netTxBytes),
                    })}
                  </span>
                </div>
              </div>
            )}
          </section>
        </div>

        {/* Lower Attribution & Process Inspector Card */}
        <section className="rounded-vp border border-hairline bg-surface shadow-[var(--depth-raised)]">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-hairline px-3.5 py-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <div
                ref={tabRef}
                role="tablist"
                aria-label={t('monitor.perSession')}
                data-marker={tabMarker ? 'on' : 'off'}
                className="vp-segmented w-fit"
              >
                {tabMarker && (
                  <span
                    aria-hidden="true"
                    className="vp-marker-2d"
                    style={{
                      width: tabMarker.width,
                      height: tabMarker.height,
                      transform: `translate3d(${tabMarker.left}px, ${tabMarker.top}px, 0)`,
                    }}
                  />
                )}
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeTab === 'sessions'}
                  data-active={activeTab === 'sessions'}
                  data-testid="monitor-tab-sessions"
                  onClick={() => switchTab('sessions')}
                  className="vp-tab px-2.5 text-vp-sm"
                >
                  <span>{t('monitor.perSession')}</span>
                  {measured.length > 0 && (
                    <span className="tabular rounded-full bg-surface-2 px-1.5 text-vp-xs text-ink-2">
                      {measured.length}
                    </span>
                  )}
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeTab === 'projects'}
                  data-active={activeTab === 'projects'}
                  data-testid="monitor-tab-projects"
                  onClick={() => switchTab('projects')}
                  className="vp-tab px-2.5 text-vp-sm"
                >
                  <span>{t('monitor.byProject')}</span>
                </button>
                {hasHostProcs && (
                  <button
                    type="button"
                    role="tab"
                    aria-selected={activeTab === 'host'}
                    data-active={activeTab === 'host'}
                    data-testid="monitor-tab-host"
                    onClick={() => switchTab('host')}
                    className="vp-tab px-2.5 text-vp-sm"
                  >
                    <span>{t('monitor.hostProcs')}</span>
                  </button>
                )}
              </div>

              {activeTab === 'host' && resources ? (
                <span className="tabular text-vp-xs text-ink-2">
                  {t('monitor.hostSummary', {
                    panel: formatBytes(resources.panel),
                    tmux: formatBytes(resources.tmux),
                  })}
                </span>
              ) : measured.length > 0 ? (
                <span className="tabular text-vp-xs text-ink-2">
                  {totalProcs === 1
                    ? t('monitor.oneProc')
                    : t('monitor.procs', { n: String(totalProcs) })}
                  {' · '}
                  {t('monitor.total')}{' '}
                  {totalSessionCpu.toFixed(totalSessionCpu < 10 ? 1 : 0)}% · {formatBytes(totalSessionRss)}
                </span>
              ) : null}
            </div>

            {activeTab !== 'host' && measured.length > 1 && (
              <div
                ref={sortRef}
                role="group"
                aria-label={t('monitor.sortBy')}
                data-marker={sortMarker ? 'on' : 'off'}
                className="vp-segmented w-fit"
              >
                {sortMarker && (
                  <span
                    aria-hidden="true"
                    className="vp-marker-2d"
                    style={{
                      width: sortMarker.width,
                      height: sortMarker.height,
                      transform: `translate3d(${sortMarker.left}px, ${sortMarker.top}px, 0)`,
                    }}
                  />
                )}
                {(
                  [
                    ['cpu', 'monitor.sortCpu'],
                    ['mem', 'monitor.sortMem'],
                    ['procs', 'monitor.sortProcs'],
                  ] as const
                ).map(([k, labelKey]) => (
                  <button
                    key={k}
                    type="button"
                    aria-pressed={sortBy === k}
                    data-active={sortBy === k}
                    data-testid={`monitor-sort-${k}`}
                    onClick={() => setSortBy(k)}
                    className="vp-tab px-2.5 text-vp-xs"
                  >
                    {t(labelKey)}
                  </button>
                ))}
              </div>
            )}
          </div>

          <div
            key={activeTab}
            data-slide={slideDir}
            className="vp-pane-slide p-3.5"
          >
            {activeTab === 'host' && resources ? (
              <HostProcessList
                procs={resources.machine}
                memTotal={sample.memTotal}
                knownSessions={knownSessionIds}
                onJumpSession={handleJumpSession}
              />
            ) : usage && !usage.readable ? (
              <p className="py-6 text-center text-vp-sm text-ink-2">{t('monitor.noProc')}</p>
            ) : measured.length === 0 ? (
              <p className="py-6 text-center text-vp-sm text-ink-2">
                {usage ? t('monitor.noSessions') : t('monitor.sampling')}
              </p>
            ) : activeTab === 'projects' ? (
              <div className="space-y-2" data-testid="monitor-projects-list">
                {projectGroups.map((g) => {
                  const pCpu = clampPct(g.cpu)
                  const pMemPct = memCeiling > 0 ? clampPct((g.rss / memCeiling) * 100) : 0
                  const barPct = sortBy === 'mem' ? pMemPct : pCpu
                  return (
                    <div
                      key={g.key}
                      className="rounded-vp border border-hairline bg-surface px-3.5 py-2.5"
                    >
                      <div className="flex min-w-0 items-baseline justify-between gap-3">
                        <div className="flex min-w-0 items-center gap-2">
                          <span className="truncate text-vp-sm font-medium text-ink">
                            {safeText(g.name)}
                          </span>
                          {g.archived && (
                            <span className="inline-flex shrink-0 items-center gap-1 rounded-md bg-surface-2 px-1.5 py-px text-vp-xs text-ink-2">
                              <Archive size={10} aria-hidden="true" />
                              {t('res.archivedMark')}
                            </span>
                          )}
                          <span className="shrink-0 text-vp-xs text-ink-2">
                            {g.sessions === 1
                              ? t('monitor.oneSession')
                              : t('monitor.sessionsCount', { n: g.sessions })}
                            {' · ×'}
                            {g.procs}
                          </span>
                        </div>
                        <div className="flex shrink-0 items-baseline gap-3">
                          <span className="tabular text-vp-sm font-medium text-ink">
                            {pCpu.toFixed(pCpu < 10 ? 1 : 0)}%
                          </span>
                          <span className="tabular w-20 text-right text-vp-sm text-ink-2">
                            {formatBytes(g.rss)}
                          </span>
                        </div>
                      </div>
                      <div className="mt-1.5 vp-bar h-1.5">
                        <span
                          className="vp-bar-fill"
                          style={{
                            width: `${barPct}%`,
                            background:
                              sortBy === 'mem'
                                ? pressureTone(pMemPct, 50, 80)
                                : pressureTone(pCpu, 40, 75),
                          }}
                        />
                      </div>
                    </div>
                  )
                })}
              </div>
            ) : (
              <div className="vp-rows space-y-1.5">
                {measured.map((row) => (
                  <SessionUsageRow
                    key={row.id}
                    row={row}
                    now={now}
                    sortBy={sortBy}
                    memCeiling={memCeiling}
                    culprit={row.id === culpritID}
                    isolated={Boolean(isolated)}
                    open={expanded[row.id] ?? row.id === culpritID}
                    onToggle={() =>
                      setExpanded((prev) => ({
                        ...prev,
                        [row.id]: !(prev[row.id] ?? row.id === culpritID),
                      }))
                    }
                    onSelectSession={onSelectSession ? handleOpenTerminal : undefined}
                    onManage={onManage ? (id) => onManage(id) : undefined}
                    onFreezeToggle={handleFreezeToggle}
                  />
                ))}
              </div>
            )}
          </div>
        </section>
      </div>
    )
  }

  const headerBar = (
    <header className="vp-safe-pad-top flex shrink-0 items-center justify-between gap-2 border-b border-hairline bg-surface px-3.5 py-2 @xl:px-4 @xl:py-2.5">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <Activity size={16} className="shrink-0 text-accent" aria-hidden="true" />
        <h2
          id="vp-monitor-title"
          className="truncate text-vp-md font-semibold tracking-tight text-ink"
        >
          {narrow ? t('panel.monitor') : t('monitor.title')}
        </h2>
        <span
          data-testid="monitor-posture"
          data-posture={posture}
          className={`inline-flex shrink-0 items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-vp-xs font-medium ${postureClass}`}
        >
          <PostureIcon size={11} className="shrink-0" aria-hidden="true" />
          <span>{paused ? t('monitor.paused') : postureText}</span>
          {!paused && posture === 'ok' && (
            <span
              aria-hidden="true"
              className="vp-breathe ml-0.5 h-1.5 w-1.5 rounded-full"
              style={{ background: 'var(--ui-done-ink)' }}
            />
          )}
        </span>
      </div>

      <div className="flex shrink-0 items-center gap-1">
        {!narrow && (
          <div
            ref={cadenceRef}
            role="group"
            aria-label={t('monitor.cadence')}
            data-marker={cadenceMarker ? 'on' : 'off'}
            className="vp-segmented w-fit"
          >
            {cadenceMarker && (
              <span
                aria-hidden="true"
                className="vp-marker-2d"
                style={{
                  width: cadenceMarker.width,
                  height: cadenceMarker.height,
                  transform: `translate3d(${cadenceMarker.left}px, ${cadenceMarker.top}px, 0)`,
                }}
              />
            )}
            {CADENCES.map((ms) => (
              <button
                key={ms}
                type="button"
                aria-pressed={cadence === ms}
                data-active={cadence === ms}
                onClick={() => setCadence(ms)}
                className="vp-tab px-2 text-vp-xs"
              >
                {t('monitor.sec', { n: ms / 1000 })}
              </button>
            ))}
          </div>
        )}

        <button
          type="button"
          data-testid="monitor-pause"
          aria-pressed={paused}
          onClick={() => setPaused((p) => !p)}
          title={paused ? t('monitor.resume') : t('monitor.pause')}
          aria-label={paused ? t('monitor.resume') : t('monitor.pause')}
          className="vp-control vp-tap"
        >
          {paused ? <Play size={14} /> : <Pause size={14} />}
        </button>

        {!narrow && (
          <button
            type="button"
            data-testid="monitor-dock-toggle"
            aria-pressed={isDrawer}
            onClick={() => changeSize(isDrawer ? 'standard' : 'drawer')}
            title={isDrawer ? t('monitor.undock') : t('monitor.dockRight')}
            aria-label={isDrawer ? t('monitor.undock') : t('monitor.dockRight')}
            className="vp-control vp-tap"
          >
            {isDrawer ? <PanelRightClose size={14} /> : <PanelRight size={14} />}
          </button>
        )}

        {!narrow && !isDrawer && (
          <button
            type="button"
            data-testid="monitor-size-toggle"
            aria-pressed={sizeMode === 'wide'}
            onClick={() => changeSize(sizeMode === 'wide' ? 'standard' : 'wide')}
            title={sizeMode === 'wide' ? t('monitor.standard') : t('monitor.expand')}
            aria-label={sizeMode === 'wide' ? t('monitor.standard') : t('monitor.expand')}
            className="vp-control vp-tap"
          >
            {sizeMode === 'wide' ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
          </button>
        )}

        {onManage && (
          <button
            type="button"
            data-testid="monitor-open-policy"
            onClick={() => onManage()}
            title={t('monitor.policy')}
            aria-label={t('monitor.policy')}
            className="vp-control vp-tap"
          >
            <SlidersHorizontal size={14} />
          </button>
        )}

        <button
          type="button"
          data-testid="monitor-view-close"
          onClick={onClose}
          title={t('monitor.close')}
          aria-label={t('monitor.close')}
          className="vp-control vp-tap"
        >
          <X size={15} />
        </button>
      </div>
    </header>
  )

  if (isDrawer) {
    return (
      <aside
        ref={dialogRef}
        data-testid="monitor-view"
        data-size="drawer"
        aria-labelledby="vp-monitor-title"
        className="vp-edge-in-right @container fixed inset-y-0 right-0 z-40 flex w-full max-w-md flex-col border-l border-hairline-strong bg-surface shadow-2xl"
      >
        {headerBar}
        <div className="vp-safe-bottom min-h-0 flex-1 overflow-y-auto bg-bg p-3">
          {renderDashboardBody()}
        </div>
      </aside>
    )
  }

  return (
    <div
      className="vp-backdrop @container fixed inset-0 z-40 flex items-center justify-center bg-black/45 p-2 @xl:p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={dialogRef}
        data-testid="monitor-view"
        data-vp-modal="monitor"
        data-size={sizeMode}
        role="dialog"
        aria-modal="true"
        aria-labelledby="vp-monitor-title"
        style={{
          maxWidth: sizeMode === 'wide' ? '92rem' : '72rem',
          maxHeight: sizeMode === 'wide' ? '94dvh' : '90dvh',
        }}
        className="vp-panel-in @container flex w-full flex-col overflow-hidden rounded-vp-lg border border-hairline-strong bg-surface shadow-2xl transition-[max-width,max-height] duration-200 ease-vp"
      >
        {headerBar}
        <div className="vp-safe-bottom min-h-0 flex-1 overflow-y-auto bg-bg px-3 py-3.5 @xl:px-5 @xl:py-4">
          {renderDashboardBody()}
        </div>
      </div>
    </div>
  )
}
