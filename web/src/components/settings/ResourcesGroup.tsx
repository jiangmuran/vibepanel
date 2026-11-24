import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronsUp, ShieldAlert, ShieldCheck } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ResourceAction, ResourceMode, ResourcePolicy, ResourcesView, Session } from '../../protocol/wire'
import { t, tKey, useLang } from '../../i18n'
import type { Lang } from '../../i18n'
import { formatBytes } from '../bytes'
import { sessionLabel } from '../label'
import { HeldBar, LevelIcon } from '../resources/level'
import { levelTone } from '../resources/tone'
import { policyOnly, pollStillCurrent, wholeNumber } from './resourcesPolicy'
import { safeText } from '../text'
import { Section } from './parts'
import { MachineList } from './resources/MachineList'
import { ProjectBlock } from './resources/SessionRows'
import { groupSessions } from './resources/group'
import { reducedMotion } from './resources/time'

/** How often the page reads the governor while it is open. Its own tick is two seconds. */
const POLL_MS = 2000

const MODES: ResourceMode[] = ['conservative', 'balanced', 'performance', 'custom']

const MODE_LABEL = {
  conservative: 'res.mode.conservative',
  balanced: 'res.mode.balanced',
  performance: 'res.mode.performance',
  custom: 'res.mode.custom',
} as const

/**
 * How much of the machine the sessions get, and what the panel does when that
 * runs out.
 *
 * Five blocks, in the order somebody arriving from the memory question reads
 * them: is anything wrong and how wrong (memory), which of the sessions
 * holds it (by project, ended from here), what else on the machine does
 * (read-only), what the panel is allowed to do about it (allocation), and what
 * has already been done (recent).
 *
 * Built on a container query, because this is the settings body: a third of a
 * wide window and the whole of a phone.
 */
export function ResourcesGroup({ sessions, focus }: { sessions: Session[]; focus?: string }) {
  const lang = useLang()
  const [view, setView] = useState<ResourcesView | null>(null)
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)
  // The session the memory question named is open from the start.
  const [open, setOpen] = useState<string | null>(focus ?? null)
  const root = useRef<HTMLDivElement>(null)

  // A session named in the machine list: open it and bring its row into view
  // once it has been drawn open. A counter, so choosing the same session twice
  // scrolls twice.
  const [revealed, setRevealed] = useState<{ id: string; n: number } | null>(null)
  const reveal = useCallback((id: string) => {
    setOpen(id)
    setRevealed((r) => ({ id, n: (r?.n ?? 0) + 1 }))
  }, [])
  useEffect(() => {
    if (!revealed) return
    const row = [...(root.current?.querySelectorAll<HTMLElement>('[data-session]') ?? [])].find(
      (el) => el.dataset.session === revealed.id,
    )
    row?.scrollIntoView({ block: 'center', behavior: reducedMotion() ? 'auto' : 'smooth' })
  }, [revealed])

  const load = useCallback(async () => {
    const asked = generation.current
    try {
      const next = await api.resources()
      if (!pollStillCurrent(asked, generation.current)) return
      setView(next)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const changed = useCallback((next: ResourcesView) => {
    generation.current++
    setView(next)
  }, [])

  useEffect(() => {
    let cancelled = false
    let timer = 0
    const tick = async () => {
      await load()
      if (!cancelled) timer = window.setTimeout(() => void tick(), POLL_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [load])

  if (error && !view) {
    return <p className="text-vp-base text-state-crashed">{safeText(error)}</p>
  }
  if (!view) return null

  return (
    <div ref={root} className="@container" data-testid="resources">
      <MemorySection view={view} />
      {view.supported && (
        <>
          <UsageSection view={view} sessions={sessions} open={open} setOpen={setOpen} onChanged={load} />
          <MachineSection view={view} onReveal={reveal} />
          <AllocationSection view={view} lang={lang} onView={changed} />
          {view.actions.length > 0 && <RecentSection actions={view.actions} sessions={sessions} lang={lang} />}
        </>
      )}
    </div>
  )
}

function MemorySection({ view }: { view: ResourcesView }) {
  return (
    <Section id="memory" title={t('res.memory')}>
      {view.supported ? <MemoryBody view={view} /> : <p className="text-vp-base text-ink-2">{t('res.unsupported')}</p>}
    </Section>
  )
}

function MemoryBody({ view }: { view: ResourcesView }) {
  const { pool } = view
  const hasPool = pool.max > 0
  // Without a pool the bar is drawn against the whole machine: the sessions
  // have no ceiling of their own, and the machine's is the one they will hit.
  const ceiling = hasPool ? pool.max : view.total
  return (
    <>
      <IsolationLine view={view} />
      <div className="mt-3" data-testid="resources-pool">
        <div className="flex items-baseline justify-between gap-3 text-vp-sm">
          <span className="text-ink-2">{t('res.pool')}</span>
          <span className="tabular text-ink">
            {hasPool ? `${formatBytes(pool.current)} / ${formatBytes(pool.max)}` : t('res.noPool')}
          </span>
        </div>
        <div className="mt-1" title={t('res.heldTitle')}>
          <HeldBar held={pool.held} current={pool.current} scale={ceiling} tone={levelTone(view.level)}>
            {hasPool && (
              <span
                data-testid="resources-ask-marker"
                className="absolute inset-y-0 w-0.5 bg-ink"
                style={{ left: `calc(${view.params.askPercent}% - 1px)` }}
              />
            )}
          </HeldBar>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-vp-xs text-ink-2">
          <span
            data-testid="resources-level"
            data-level={view.level}
            className="inline-flex items-center gap-1"
            style={{ color: levelTone(view.level) }}
          >
            <LevelIcon level={view.level} size={12} />
            {t(`res.level.${view.level}`)}
          </span>
          <span className="tabular">{t('res.held', { n: formatBytes(pool.held) })}</span>
          <span className="tabular">{t('res.cache', { n: formatBytes(Math.max(0, pool.current - pool.held)) })}</span>
          {hasPool && <span className="tabular">{t('res.stall', { n: pool.stall.toFixed(pool.stall < 10 ? 1 : 0) })}</span>}
          {hasPool && <span>▏{t('res.askLegend')}</span>}
        </div>
        <div className="mt-0.5 flex flex-wrap gap-x-3 text-vp-xs text-ink-3">
          <span className="tabular">{t('res.machineFree', { n: formatBytes(view.available) })}</span>
          <span className="tabular">{t('res.panelMem', { n: formatBytes(view.panel) })}</span>
          {view.tmux > 0 && <span className="tabular">{t('res.tmuxMem', { n: formatBytes(view.tmux) })}</span>}
        </div>
      </div>
    </>
  )
}

/** Whether the sessions are apart from the panel, and if not, what fixes it. */
function IsolationLine({ view }: { view: ResourcesView }) {
  const iso = view.isolation
  if (iso.state === 'isolated') {
    return (
      <div data-testid="resources-isolation" data-state="isolated" className="flex items-center gap-2 text-vp-base text-ink">
        <ShieldCheck size={15} className="shrink-0" style={{ color: 'var(--vp-state-done)' }} aria-hidden="true" />
        <span>{t('res.isolated')}</span>
      </div>
    )
  }
  const reason = iso.reason ?? 'failed'
  // Not being isolated before the first session exists is the ordinary state
  // of a fresh install, not a problem, and it is drawn without the warning.
  const quiet = reason === 'no-server'
  const why =
    reason === 'failed' ? t('res.why.failed', { detail: safeText(iso.detail ?? '') }) : (tKey(`res.why.${reason}`) ?? reason)
  return (
    <div data-testid="resources-isolation" data-state="none" data-reason={reason} className="text-vp-base">
      <div className="flex items-center gap-2 text-ink">
        <ShieldAlert
          size={15}
          className="shrink-0"
          style={{ color: quiet ? 'var(--vp-ink-3)' : 'var(--vp-state-waiting)' }}
          aria-hidden="true"
        />
        <span>{t('res.shared')}</span>
      </div>
      <p className="mt-0.5 pl-6 text-vp-sm text-ink-2">{why}</p>
      {reason === 'unit-outdated' && (
        <code className="mt-1 ml-6 block font-mono text-vp-sm text-ink select-all">sudo vibepanel service upgrade</code>
      )}
    </div>
  )
}

function AllocationSection({
  view,
  lang,
  onView,
}: {
  view: ResourcesView
  lang: Lang
  onView: (v: ResourcesView) => void
}) {
  const [draft, setDraft] = useState<ResourcePolicy | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // The mode shown selected is the one being edited, if any, so choosing
  // Custom opens its fields before anything is saved.
  const selected = draft?.mode ?? view.policy.mode

  const [saved, setSaved] = useState(0)
  useEffect(() => {
    if (!saved) return
    const timer = window.setTimeout(() => setSaved(0), 2000)
    return () => window.clearTimeout(timer)
  }, [saved])

  const run = async (fn: () => Promise<ResourcesView>) => {
    setBusy(true)
    try {
      onView(await fn())
      setDraft(null)
      setError(null)
      setSaved((n) => n + 1)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const choose = (mode: ResourceMode) => {
    setError(null)
    if (mode === 'custom') {
      // Starting from the numbers in force, so Custom opens on what the
      // current mode does.
      const from = view.policy.mode === 'custom' ? view.policy : view.params
      setDraft({
        mode: 'custom',
        poolPercent: from.poolPercent,
        askPercent: from.askPercent,
        autoAct: from.autoAct,
        graceSeconds: from.graceSeconds >= view.bounds.minGrace ? from.graceSeconds : view.policy.graceSeconds,
      })
      return
    }
    void run(() => api.setResourcePolicy({ ...policyOnly(view.policy), mode }))
  }

  // While a boost runs, what is in force is Performance whatever is chosen,
  // and the lines under the modes say what is in force.
  const shown = view.params.boosted
    ? view.params
    : selected === 'custom'
      ? (draft ?? view.policy)
      : (view.presets.find((p) => p.mode === selected) ?? view.params)

  return (
    <Section id="allocation" title={t('res.allocation')}>
      <div className="vp-segmented w-full" data-testid="resources-mode">
        {MODES.map((m) => (
          <button
            key={m}
            type="button"
            data-testid={`resources-mode-${m}`}
            aria-pressed={selected === m}
            data-active={selected === m}
            disabled={busy}
            onClick={() => choose(m)}
            className="vp-tab"
          >
            <span className="truncate text-vp-sm">{t(MODE_LABEL[m])}</span>
          </button>
        ))}
      </div>

      {saved > 0 && (
        <p role="status" className="mt-1 text-vp-xs" style={{ color: 'var(--vp-state-done)' }}>
          {t('res.saved')}
        </p>
      )}
      <ul className="mt-2 space-y-0.5 text-vp-sm text-ink-2" data-testid="resources-summary">
        <li>{t('res.summary.pool', { n: shown.poolPercent })}</li>
        <li>{t('res.summary.ask', { n: shown.askPercent })}</li>
        <li>{shown.autoAct ? t('res.summary.auto', { n: shown.graceSeconds }) : t('res.summary.noAuto')}</li>
        <li>{t('res.summary.dynamic')}</li>
      </ul>

      {draft && (
        <CustomFields
          draft={draft}
          bounds={view.bounds}
          busy={busy}
          onChange={setDraft}
          onSave={() => void run(() => api.setResourcePolicy(policyOnly(draft)))}
          onCancel={() => {
            setDraft(null)
            setError(null)
          }}
        />
      )}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        {view.params.boosted && view.policy.boostUntil ? (
          <>
            <span className="text-vp-sm text-ink" data-testid="resources-boosted">
              {t('res.boosted', { time: clock(view.policy.boostUntil, lang) })}
            </span>
            <button type="button" className="vp-outline text-vp-sm" disabled={busy} onClick={() => void run(() => api.boostResources(0))}>
              {t('res.boostEnd')}
            </button>
          </>
        ) : (
          view.policy.mode !== 'performance' && (
            <button
              type="button"
              data-testid="resources-boost"
              className="vp-outline text-vp-sm"
              disabled={busy}
              onClick={() => void run(() => api.boostResources(60))}
            >
              <ChevronsUp size={13} aria-hidden="true" />
              {t('res.boost')}
            </button>
          )
        )}
      </div>
      {error && <p className="mt-2 text-vp-sm text-state-crashed">{safeText(error)}</p>}
    </Section>
  )
}

const FIELD =
  'w-20 shrink-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-right font-mono text-vp-sm text-ink outline-none focus:border-accent disabled:opacity-50 aria-[invalid=true]:border-state-crashed'
const FIELD_ROW = 'flex items-center justify-between gap-3 text-vp-sm text-ink-2'

type NumberKey = 'poolPercent' | 'askPercent' | 'graceSeconds'

function CustomFields({
  draft,
  bounds,
  busy,
  onChange,
  onSave,
  onCancel,
}: {
  draft: ResourcePolicy
  bounds: ResourcesView['bounds']
  busy: boolean
  onChange: (p: ResourcePolicy) => void
  onSave: () => void
  onCancel: () => void
}) {
  const [text, setText] = useState<Record<NumberKey, string>>({
    poolPercent: String(draft.poolPercent),
    askPercent: String(draft.askPercent),
    graceSeconds: String(draft.graceSeconds),
  })
  const range: Record<NumberKey, [number, number]> = {
    poolPercent: [bounds.minPool, bounds.maxPool],
    askPercent: [bounds.minAsk, bounds.maxAsk],
    graceSeconds: [bounds.minGrace, bounds.maxGrace],
  }
  const bad = (key: NumberKey) => {
    const n = wholeNumber(text[key])
    return n === null || n < range[key][0] || n > range[key][1]
  }
  const edit = (key: NumberKey) => (e: React.ChangeEvent<HTMLInputElement>) => {
    const next = e.target.value
    setText({ ...text, [key]: next })
    const n = wholeNumber(next)
    if (n !== null && n >= range[key][0] && n <= range[key][1]) onChange({ ...draft, [key]: n })
  }
  const invalid = bad('poolPercent') || bad('askPercent') || (draft.autoAct && bad('graceSeconds'))

  const field = (key: NumberKey, label: string, testid: string, disabled = false) => (
    <div>
      <label className={FIELD_ROW}>
        {label}
        <input
          type="text"
          inputMode="numeric"
          value={text[key]}
          onChange={edit(key)}
          disabled={disabled}
          aria-invalid={!disabled && bad(key)}
          data-testid={testid}
          className={FIELD}
        />
      </label>
      {!disabled && bad(key) && (
        <p className="mt-0.5 text-right text-vp-xs text-state-crashed">
          {t('res.custom.range', { min: range[key][0], max: range[key][1] })}
        </p>
      )}
    </div>
  )

  return (
    // One column: the auto switch and the wait it governs are one sentence,
    // and a grid put the switch flush against the next field's label.
    <div data-testid="resources-custom" className="mt-3 flex flex-col gap-2 rounded-vp border border-hairline p-3">
      {field('poolPercent', t('res.custom.pool'), 'resources-custom-pool')}
      {field('askPercent', t('res.custom.ask'), 'resources-custom-ask')}
      <label className={FIELD_ROW}>
        {t('res.custom.auto')}
        <input
          type="checkbox"
          checked={draft.autoAct}
          onChange={() => onChange({ ...draft, autoAct: !draft.autoAct })}
          data-testid="resources-custom-auto"
        />
      </label>
      <div className="pl-4">{field('graceSeconds', t('res.custom.grace'), 'resources-custom-grace', !draft.autoAct)}</div>
      <div className="mt-1 flex gap-2">
        <button
          type="button"
          disabled={busy || invalid}
          onClick={onSave}
          data-testid="resources-custom-save"
          className="vp-press rounded-vp border border-accent bg-accent/10 px-3 py-1 text-vp-sm text-accent transition-colors duration-200 ease-vp hover:bg-accent/20 disabled:opacity-50"
        >
          {t('res.save')}
        </button>
        <button type="button" className="vp-outline text-vp-sm" disabled={busy} onClick={onCancel}>
          {t('res.cancel')}
        </button>
      </div>
    </div>
  )
}

function UsageSection({
  view,
  sessions,
  open,
  setOpen,
  onChanged,
}: {
  view: ResourcesView
  sessions: Session[]
  open: string | null
  setOpen: (id: string | null) => void
  onChanged: () => Promise<void>
}) {
  const byId = new Map(sessions.map((s) => [s.id, s]))
  // The scale every row is drawn against: the pool when there is one, so the
  // bars add up to the pool's bar above them; the machine otherwise.
  const scale = view.pool.max > 0 ? view.pool.max : view.total
  // Every session the server measured, including those of archived projects:
  // hiding a project frees nothing, and a page about memory that stopped
  // listing a session the moment its project was archived would be showing a
  // machine with less in use than it has.
  const groups = groupSessions(view.sessions)
  return (
    <Section id="usage" title={t('res.sessions')}>
      {groups.length === 0 && <p className="text-vp-base text-ink-2">{t('res.none')}</p>}
      <div className="divide-y divide-hairline" data-testid="resources-sessions">
        {groups.map((g) => (
          <ProjectBlock
            key={g.key}
            group={g}
            sessions={byId}
            scale={scale}
            isolated={view.isolation.state === 'isolated'}
            now={view.at}
            open={open}
            onToggle={(id) => setOpen(open === id ? null : id)}
            onChanged={onChanged}
          />
        ))}
      </div>
    </Section>
  )
}

function MachineSection({ view, onReveal }: { view: ResourcesView; onReveal: (id: string) => void }) {
  const known = new Set(view.sessions.map((s) => s.id))
  return (
    <Section id="machine" title={t('res.machine')}>
      <MachineList procs={view.machine ?? []} known={known} now={view.at} onReveal={onReveal} />
    </Section>
  )
}

function RecentSection({ actions, sessions, lang }: { actions: ResourceAction[]; sessions: Session[]; lang: Lang }) {
  const byId = new Map(sessions.map((s) => [s.id, s]))
  return (
    <Section id="recent" title={t('res.recent')}>
      <ul className="space-y-1 text-vp-sm" data-testid="resources-recent">
        {actions.map((a, i) => {
          const s = a.sessionId ? byId.get(a.sessionId) : undefined
          const what =
            a.kind === 'kill'
              ? t('res.act.kill', { name: safeText(a.name ?? ''), rss: formatBytes(a.rss ?? 0) })
              : t(`res.act.${a.kind}`)
          return (
            <li key={`${a.at}-${i}`} className="flex min-w-0 items-baseline gap-2">
              <span className="tabular shrink-0 text-ink-3">{clock(a.at, lang)}</span>
              <span className="min-w-0 flex-1 truncate text-ink">
                {what}
                {(s || a.session) && (
                  <span className="text-ink-2"> · {s ? sessionLabel(s) : safeText(a.session ?? '')}</span>
                )}
              </span>
              {a.auto && <span className="shrink-0 text-vp-xs text-ink-3">{t('res.act.auto')}</span>}
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

/** A time of day in the page's language, not the browser's. */
function clock(unix: number, lang: Lang): string {
  return new Date(unix * 1000).toLocaleTimeString(lang === 'zh' ? 'zh-CN' : 'en-GB', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

