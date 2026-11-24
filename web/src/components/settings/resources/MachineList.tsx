import type { MachineProc, MachineWhere } from '../../../protocol/wire'
import { t } from '../../../i18n'
import type { Key } from '../../../i18n'
import { formatBytes } from '../../bytes'
import { safeText } from '../../text'
import { exactTime, shortAgo } from './time'

/** Where a process comes from, in words. A session's is a button. */
function Whence({
  where,
  known,
  onReveal,
}: {
  where: MachineWhere
  /** Whether the page lists that session, so there is a row to go to. */
  known: boolean
  onReveal: (id: string) => void
}) {
  if (where.kind === 'session' && where.sessionId) {
    const title = safeText(where.sessionTitle ?? '')
    const project = safeText(where.projectName ?? '')
    const text = t('res.where.session', { name: project ? `${title} · ${project}` : title })
    if (!known) return <span className="min-w-0 truncate">{text}</span>
    return (
      <button
        type="button"
        data-testid="resources-machine-session"
        onClick={() => onReveal(where.sessionId!)}
        className="vp-press min-w-0 truncate text-left text-accent underline decoration-dotted underline-offset-2"
      >
        {text}
      </button>
    )
  }
  const label = safeText(where.label ?? '')
  return <span className="min-w-0 truncate">{t(`res.where.${where.kind}` as Key, { label, name: label })}</span>
}

/**
 * The processes holding the most memory on the whole machine, wherever they
 * came from. Read-only: the panel ends what it can prove is a session's, and
 * shows the rest.
 */
export function MachineList({
  procs,
  known,
  now,
  onReveal,
}: {
  procs: MachineProc[]
  known: Set<string>
  /** The server's clock, unix seconds. */
  now: number
  onReveal: (id: string) => void
}) {
  if (procs.length === 0) return <p className="text-vp-base text-ink-2">{t('res.machineEmpty')}</p>
  return (
    <ul className="divide-y divide-hairline" data-testid="resources-machine">
      {procs.map((p) => {
        const started = shortAgo(p.startedAt, now)
        const cmd = safeText(p.cmd || p.name)
        return (
          <li
            key={`${p.pid}-${p.start}`}
            className="py-1.5"
            data-testid="resources-machine-proc"
            data-kind={p.where.kind}
          >
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="min-w-0 flex-1 truncate font-mono text-vp-sm text-ink" title={`${cmd} (pid ${p.pid})`}>
                {cmd}
              </span>
              <span className="tabular shrink-0 text-vp-sm text-ink">{formatBytes(p.rss)}</span>
            </div>
            <div className="mt-0.5 flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0 text-vp-xs text-ink-3">
              <Whence where={p.where} known={!!p.where.sessionId && known.has(p.where.sessionId)} onReveal={onReveal} />
              <span aria-hidden="true">·</span>
              <span>{safeText(p.user)}</span>
              {started && (
                <>
                  <span aria-hidden="true">·</span>
                  <span title={exactTime(p.startedAt)}>{t('res.startedAgo', { ago: started })}</span>
                </>
              )}
              {p.swap > 0 && (
                <>
                  <span aria-hidden="true">·</span>
                  <span className="tabular" data-testid="resources-machine-swap">
                    {t('res.swapped', { n: formatBytes(p.swap) })}
                  </span>
                </>
              )}
            </div>
          </li>
        )
      })}
    </ul>
  )
}
