import { useState } from 'react'
import { Archive, ChevronDown, ChevronRight, ChevronsDown, ChevronsUp, Pause, Play } from 'lucide-react'

import { api } from '../../../protocol/api'
import type { ResourceProc, ResourceSession, Session } from '../../../protocol/wire'
import { t } from '../../../i18n'
import { formatBytes } from '../../bytes'
import { sessionLabel } from '../../label'
import { HeldBar } from '../../resources/level'
import { StateDot } from '../../StateDot'
import { safeText } from '../../text'
import { ConfirmPress } from './ConfirmPress'
import type { ProjectGroup } from './group'
import { afterCollapse, exactTime, shortAgo } from './time'

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/**
 * One project: a header that says what it holds and ends all of it, and its
 * sessions under it.
 *
 * The header is where the page answers "which project", and its bar is drawn
 * on the same scale as every session's, so a project of three small sessions
 * and one of a single large one can be told apart at a glance.
 */
export function ProjectBlock({
  group,
  sessions,
  scale,
  isolated,
  now,
  open,
  onToggle,
  onChanged,
}: {
  group: ProjectGroup
  sessions: Map<string, Session>
  scale: number
  isolated: boolean
  /** The server's clock, unix seconds. */
  now: number
  open: string | null
  onToggle: (id: string) => void
  onChanged: () => Promise<void>
}) {
  const [leaving, setLeaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const name = safeText(group.name) || t('res.where.other')
  const n = group.rows.length

  const endAll = async () => {
    setError(null)
    try {
      await api.endProjectSessions(group.id)
    } catch (e) {
      setError(message(e))
      // Some of them may have been ended; the list says which.
      await onChanged()
      return
    }
    setLeaving(true)
    await afterCollapse()
    await onChanged()
  }

  return (
    <div
      className="vp-collapse"
      data-leaving={leaving}
      inert={leaving}
      data-testid="resources-project"
      data-project={group.key}
    >
      <div>
        <div className="py-2">
          <div className="flex min-w-0 items-center gap-2">
            <h4 className="min-w-0 flex-1 truncate text-vp-base font-medium text-ink" title={name}>
              {name}
            </h4>
            {group.archived && (
              <span
                data-testid="resources-project-archived"
                className="inline-flex shrink-0 items-center gap-1 rounded-vp border border-hairline px-1.5 py-px text-vp-xs text-ink-2"
              >
                <Archive size={11} aria-hidden="true" />
                {t('res.archivedMark')}
              </span>
            )}
            <span className="tabular w-20 shrink-0 text-right text-vp-sm text-ink">{formatBytes(group.memory)}</span>
            {group.id && (
              <ConfirmPress
                testid="resources-end-project"
                label={t('res.endAll')}
                armedLabel={t('res.confirm.againAll')}
                armedShort={t('res.confirm.againShort')}
                ariaLabel={t('res.endAllAria', { name, n })}
                act={endAll}
              />
            )}
          </div>
          <div className="mt-1 flex items-center gap-2">
            <span className="tabular shrink-0 text-vp-xs text-ink-3">
              {t(n === 1 ? 'res.sessionCount.one' : 'res.sessionCount.other', { n })}
            </span>
            <div className="min-w-0 flex-1">
              <HeldBar held={group.held} current={group.memory} scale={scale} tone="var(--vp-accent)" height="h-1" />
            </div>
          </div>
          {error && (
            <p className="mt-1 text-vp-sm text-state-crashed" data-testid="resources-project-error">
              {safeText(error)}
            </p>
          )}
        </div>
        <div className="ml-3 divide-y divide-hairline border-l border-hairline pl-2">
          {group.rows.map((r) => (
            <SessionRow
              key={r.id}
              row={r}
              session={sessions.get(r.id)}
              scale={scale}
              isolated={isolated}
              now={now}
              open={open === r.id}
              onToggle={() => onToggle(r.id)}
              onChanged={onChanged}
            />
          ))}
        </div>
      </div>
    </div>
  )
}

function SessionRow({
  row,
  session,
  scale,
  isolated,
  now,
  open,
  onToggle,
  onChanged,
}: {
  row: ResourceSession
  /** Absent for a session the snapshot does not carry: an archived project's. */
  session: Session | undefined
  scale: number
  isolated: boolean
  now: number
  open: boolean
  onToggle: () => void
  onChanged: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [leaving, setLeaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const name = session ? sessionLabel(session) : safeText(row.title)

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
      setError(null)
      await onChanged()
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }

  // The sidebar's end, not a second one: the same route, so what is ended and
  // what is left running is one definition.
  const endSession = async () => {
    setError(null)
    try {
      await api.deleteSession(row.id)
    } catch (e) {
      setError(message(e))
      return
    }
    setLeaving(true)
    await afterCollapse()
    await onChanged()
  }

  const endProc = (p: ResourceProc) => async () => {
    setError(null)
    try {
      await api.killProcess(row.id, p)
      await onChanged()
    } catch (e) {
      setError(message(e))
    }
  }

  const Chevron = open ? ChevronDown : ChevronRight
  // A row holding next to nothing draws no bar: a column of empty tracks is
  // noise between the rows that matter.
  const drawBar = row.memory >= scale / 200
  const created = shortAgo(row.createdAt, now)
  const used = shortAgo(row.lastUsedAt, now)
  return (
    // Stays collapsed, not removed, until the server's next reading no longer
    // lists it: that reading can be a tick old, and a row that came back for
    // two seconds after it was ended read as the end not having worked.
    <div className="vp-collapse" data-leaving={leaving} inert={leaving}>
      <div>
        <div className="py-1.5" data-testid="resources-session" data-session={row.id}>
          <div className="flex min-w-0 items-center gap-1.5">
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={open}
              className="vp-press flex min-w-0 flex-1 flex-col items-stretch gap-0.5 text-left"
            >
              <span className="flex min-w-0 items-center gap-1.5">
                <Chevron size={13} className="shrink-0 text-ink-3" aria-hidden="true" />
                {session ? (
                  <StateDot state={session.state} size={9} exited={session.exited} exitStatus={session.exitStatus} />
                ) : (
                  <span
                    title={t('res.inArchived')}
                    className="inline-flex shrink-0 text-ink-3"
                    data-testid="resources-session-archived"
                  >
                    <Archive size={10} aria-hidden="true" />
                    <span className="sr-only">{t('res.inArchived')}</span>
                  </span>
                )}
                <span className="min-w-0 flex-1 truncate text-vp-base text-ink" title={name}>
                  {name}
                </span>
                {row.frozen && (
                  <span
                    className="inline-flex shrink-0 items-center gap-0.5 text-vp-xs"
                    style={{ color: 'var(--vp-state-waiting)' }}
                  >
                    <Pause size={11} aria-hidden="true" />
                    {t('res.frozen')}
                  </span>
                )}
                {row.priority === 'low' && (
                  <span className="inline-flex shrink-0 items-center gap-0.5 text-vp-xs text-ink-2" title={t('res.loweredTitle')}>
                    <ChevronsDown size={11} aria-hidden="true" />
                    <span className="hidden @md:inline">{t('res.lowered')}</span>
                  </span>
                )}
                {row.priority === 'high' && (
                  <span className="inline-flex shrink-0 items-center gap-0.5 text-vp-xs text-accent" title={t('res.priorityTitle')}>
                    <ChevronsUp size={11} aria-hidden="true" />
                    <span className="hidden @md:inline">{t('res.priority')}</span>
                  </span>
                )}
                <span className="tabular hidden w-20 shrink-0 text-right text-vp-sm text-ink-2 @md:inline" title={t('res.cpuTitle')}>
                  CPU {row.cpuPercent.toFixed(row.cpuPercent < 10 ? 1 : 0)}%
                </span>
                <span
                  className="tabular w-20 shrink-0 text-right text-vp-sm text-ink"
                  title={`${t('res.held', { n: formatBytes(row.held) })} · ${t('res.cache', { n: formatBytes(Math.max(0, row.memory - row.held)) })}`}
                >
                  {formatBytes(row.memory)}
                </span>
              </span>
              <span className="ml-5 flex min-w-0 flex-wrap gap-x-3 text-vp-xs text-ink-3" data-testid="resources-session-times">
                {created && (
                  <span title={exactTime(row.createdAt)}>{t('res.createdAgo', { ago: created })}</span>
                )}
                {used && <span title={exactTime(row.lastUsedAt)}>{t('res.usedAgo', { ago: used })}</span>}
              </span>
            </button>
            <ConfirmPress
              testid="resources-end-session"
              label={t('res.end')}
              armedLabel={t('res.confirm.again')}
              armedShort={t('res.confirm.againShort')}
              ariaLabel={t('res.endSessionAria', { name })}
              act={endSession}
              disabled={busy}
            />
          </div>
          {drawBar && (
            <div className="mt-1 ml-5">
              <HeldBar held={row.held} current={row.memory} scale={scale} tone="var(--vp-accent)" height="h-1" />
            </div>
          )}
          {error && (
            <p className="mt-1 ml-5 text-vp-sm text-state-crashed" data-testid="resources-session-error">
              {safeText(error)}
            </p>
          )}

          {open && (
            <div className="mt-2 ml-5 space-y-1" data-testid="resources-session-detail">
              {(row.top ?? []).map((p) => (
                <div key={`${p.pid}-${p.start}`} className="flex min-w-0 items-center gap-2 text-vp-sm">
                  <span className="min-w-0 flex-1 truncate text-ink" title={safeText(p.cmd || p.name)}>
                    {/* What it runs: comm is "bash" for every command an agent
                        starts, and this is where somebody decides which to end. */}
                    <span className="font-mono">{safeText(p.cmd || p.name)}</span>
                    <span className="ml-1.5 text-vp-xs text-ink-3">{p.root ? t('res.rootProc') : `pid ${p.pid}`}</span>
                  </span>
                  <span className="tabular shrink-0 text-ink-2">{formatBytes(p.rss)}</span>
                  <ConfirmPress
                    testid="resources-end"
                    label={t('res.end')}
                    armedLabel={t('res.confirm.again')}
                    armedShort={t('res.confirm.againShort')}
                    ariaLabel={t('res.endAria', { name: safeText(p.name), pid: p.pid })}
                    act={endProc(p)}
                    disabled={busy}
                  />
                </div>
              ))}
              {row.procs > (row.top ?? []).length && (
                <p className="text-vp-xs text-ink-3">{t('res.moreProcs', { n: row.procs - (row.top ?? []).length })}</p>
              )}
              {isolated && (
                // No question: pausing is undone by the button beside it, and
                // the sidebar marks a paused session.
                <button
                  type="button"
                  data-testid="resources-freeze"
                  disabled={busy}
                  onClick={() => void run(() => api.freezeSession(row.id, !row.frozen))}
                  className="vp-outline text-vp-sm"
                >
                  {row.frozen ? <Play size={13} aria-hidden="true" /> : <Pause size={13} aria-hidden="true" />}
                  {row.frozen ? t('res.resume') : t('res.pause')}
                </button>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
