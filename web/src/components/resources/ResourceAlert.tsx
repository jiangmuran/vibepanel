import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { ResourceAlert as Alert, Session } from '../../protocol/wire'
import { t } from '../../i18n'
import { formatBytes } from '../bytes'
import { sessionLabel } from '../label'
import { safeText } from '../text'
import { LevelIcon } from './level'
import { levelTone } from './tone'

/**
 * The memory question, across the top of the console.
 *
 * A bar rather than a toast or a dialog. A toast goes away on its own, and this
 * is a question that may be followed by the panel acting on it; a dialog blocks
 * the terminal somebody may need to save their work in before answering.
 *
 * The buttons are the answers, and only answers that mean something now are
 * shown. Ending a process from here is one press, because the bar names the
 * process, the session and the size -- except the session's own process, which
 * ends the session and so is left to the resources page, where it asks first.
 * "Later" is only offered for a warning: the governor does not snooze a stall,
 * and a bar that went away while its countdown kept running was a process
 * ended with nothing on screen.
 */
export function ResourceAlertBar({
  alert,
  sessions,
  frozen,
  onDetails,
  onDismiss,
}: {
  alert: Alert
  sessions: Session[]
  frozen: string[]
  onDetails: (sessionId: string | undefined) => void
  onDismiss: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  // A second-by-second clock only while a countdown is showing, read the
  // moment the countdown appears: a bar that had been up since a warning
  // showed the first second of a ten-second countdown as "28s", counted from
  // the clock it read when it first drew.
  useEffect(() => {
    if (!alert.autoAt) return
    const read = () => setNow(Math.floor(Date.now() / 1000))
    const first = window.setTimeout(read, 0)
    const timer = window.setInterval(read, 1000)
    return () => {
      window.clearTimeout(first)
      window.clearInterval(timer)
    }
  }, [alert.autoAt])

  const session = alert.sessionId ? sessions.find((s) => s.id === alert.sessionId) : undefined
  const proc = alert.proc
  const procName = proc ? safeText(proc.name) : ''
  const tone = levelTone(alert.level)
  const left = alert.autoAt ? Math.max(0, alert.autoAt - now) : 0
  const title = t(`res.alert.${alert.reason}`)

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
      setError(null)
      onDismiss()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const numbers =
    alert.reason === 'machine' || !alert.poolMax
      ? t('res.alert.machineNumbers', { free: formatBytes(alert.available), total: formatBytes(alert.total) })
      : t('res.alert.noCulprit', { used: formatBytes(alert.poolCurrent), max: formatBytes(alert.poolMax) })

  return (
    <div
      data-testid="resource-alert"
      data-level={alert.level}
      role="region"
      aria-label={title}
      className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-hairline px-4 py-2 text-vp-base"
      style={{ background: 'var(--vp-surface-2)' }}
    >
      {/* The title is what a screen reader announces, once. The countdown
          below it changes every second and is not in the live region. */}
      <span role="status" className="inline-flex shrink-0 items-center gap-1.5 font-semibold" style={{ color: tone }}>
        <LevelIcon level={alert.level} size={15} />
        {title}
      </span>
      <span className="min-w-[14rem] flex-1 text-ink-2">
        {proc && session ? (
          <>
            {t('res.alert.culprit', { session: sessionLabel(session), proc: procName })}{' '}
            <span className="whitespace-nowrap tabular">{formatBytes(proc.rss)}</span>
          </>
        ) : (
          numbers
        )}
      </span>
      {alert.autoAt ? (
        <span
          data-testid="resource-alert-countdown"
          className="tabular min-w-[7rem] shrink-0 whitespace-nowrap font-medium"
          style={{ color: tone }}
          aria-hidden="true"
        >
          {t('res.alert.auto', { n: left })}
        </span>
      ) : null}
      <span className="flex max-w-full min-w-0 flex-wrap items-center gap-1.5">
        {proc && session && !proc.root && (
          <button
            type="button"
            data-testid="resource-alert-end"
            disabled={busy}
            onClick={() => void run(() => api.killProcess(session.id, proc))}
            className="vp-outline text-vp-sm"
            // Inline, because .vp-outline's own colour is unlayered and beats a
            // utility class; styles.css says destructive buttons do it this way.
            style={{ color: 'var(--vp-state-crashed)' }}
          >
            {t('res.alert.end', { proc: procName })}
          </button>
        )}
        {session && alert.canPause && !frozen.includes(session.id) && (
          <button
            type="button"
            data-testid="resource-alert-pause"
            disabled={busy}
            onClick={() => void run(() => api.freezeSession(session.id, true))}
            className="vp-outline max-w-full text-vp-sm"
          >
            <span className="truncate">{t('res.alert.pause', { session: sessionLabel(session) })}</span>
          </button>
        )}
        {alert.canBoost && (
          <button
            type="button"
            data-testid="resource-alert-boost"
            disabled={busy}
            onClick={() => void run(() => api.boostResources(60))}
            className="vp-outline text-vp-sm"
          >
            {t('res.boost')}
          </button>
        )}
        {alert.level === 'warn' && !alert.autoAt && (
          <button
            type="button"
            data-testid="resource-alert-later"
            disabled={busy}
            onClick={() => void run(() => api.snoozeResourceAlert(alert.id, alert.sessionId ?? '', 15))}
            className="vp-outline text-vp-sm"
          >
            {t('res.alert.later')}
          </button>
        )}
        <button
          type="button"
          data-testid="resource-alert-details"
          onClick={() => onDetails(alert.sessionId)}
          className="vp-outline text-vp-sm"
        >
          {t('res.alert.details')}
        </button>
      </span>
      {error && (
        <p className="w-full text-vp-sm text-state-crashed">{t('res.alert.failed', { why: safeText(error) })}</p>
      )}
    </div>
  )
}
