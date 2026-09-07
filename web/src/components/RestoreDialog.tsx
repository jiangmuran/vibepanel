import { useState } from 'react'
import { AlertTriangle, Loader2, RotateCcw } from 'lucide-react'

import type { LaunchProfile, Project, Session } from '../protocol/wire'
import { api } from '../protocol/api'
import { projectLabel, sessionLabel } from './label'
import { profileLabel, profileOf } from './profiles'
import { restoreLaunch } from './resume'
import { safeText } from './text'
import { shellQuote } from '../shell'
import { t, useLang } from '../i18n'

/**
 * What will be run, and where, before anything is pressed.
 *
 * This dialog exists because the alternative is a button labelled "restore"
 * that launches two dozen agents. Every row spells out the argv and the
 * directory, so the thing the panel is about to do to somebody's machine is
 * readable rather than implied.
 *
 * The argv it prints is the one that will run, which since agents began
 * resuming is not always the one that was recorded: `claude` comes back as
 * `claude --continue`, and a row that says otherwise would be this dialog
 * failing at its only job. resume.ts holds that rule and says why it is
 * written twice.
 *
 * It is also where the honest half lives, and the honest half got smaller
 * rather than disappearing. The conversation comes back for the agents that
 * keep one; the process does not, and neither does anything it had not
 * finished. The warning is not dismissible and is not a footnote — a restore
 * that reads as "your work is back" is worse than no restore, because somebody
 * believes it.
 */
export function RestoreDialog({
  sessions,
  allSessions,
  projects,
  profiles,
  labels,
  onClose,
  onDone,
}: {
  sessions: Session[]
  /**
   * Every session the panel has, which is not the same list as `sessions`.
   *
   * Two agents resuming out of one directory would take the same conversation,
   * and a session still *running* there is the one holding it. The claimants
   * are therefore every session, not the restorable ones on show. resume.ts.
   */
  allSessions: Session[]
  projects: Project[]
  profiles: LaunchProfile[]
  labels: Map<string, string>
  onClose: () => void
  onDone: () => void
}) {
  useLang()
  const [chosen, setChosen] = useState<Set<string>>(() => new Set(sessions.map((s) => s.id)))
  const [busy, setBusy] = useState(false)
  const [failures, setFailures] = useState<{ id: string; error?: string }[]>([])

  const projectName = (id: string) => {
    const p = projects.find((x) => x.id === id)
    return p ? projectLabel(p) : ''
  }

  // Not the ticked ones, and not only the ones on show: unticking a row today
  // does not stop it being restored tomorrow, and a session still running in
  // that directory is already holding the conversation. resume.ts.
  const candidates = allSessions.map((s) => ({ launchCommand: s.launchCommand, cwd: s.cwd }))

  const toggle = (id: string) => {
    setChosen((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const run = async () => {
    setBusy(true)
    setFailures([])
    try {
      const res = await api.restoreSessions([...chosen])
      const bad = res.results.filter((r) => !r.ok)
      if (bad.length > 0) {
        // The dialog stays open holding the failures. Closing on a partial
        // success is how somebody finds out tomorrow that three of the twenty
        // four never came back.
        setFailures(bad)
        return
      }
      onDone()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      data-testid="restore-backdrop"
    >
      <div
        className="vp-panel-in flex max-h-[85vh] w-full max-w-2xl flex-col overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl"
        onClick={(e) => e.stopPropagation()}
        data-testid="restore-dialog"
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-4 py-2.5">
          <RotateCcw size={14} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 text-vp-md font-semibold">
            {t('restore.dialogTitle')}
          </span>
          <button
            type="button"
            data-testid="restore-select-all"
            onClick={() =>
              setChosen(
                chosen.size === sessions.length ? new Set() : new Set(sessions.map((s) => s.id)),
              )
            }
            className="vp-press shrink-0 rounded-md px-2 py-1 text-vp-sm text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
          >
            {chosen.size === sessions.length ? t('restore.selectNone') : t('restore.selectAll')}
          </button>
        </div>

        {/* Not a tooltip and not dismissible. */}
        <div
          className="flex shrink-0 items-start gap-2 border-b border-hairline px-4 py-2.5 text-vp-base leading-relaxed"
          style={{ background: 'var(--vp-surface-2)' }}
          data-testid="restore-warning"
        >
          <AlertTriangle
            size={14}
            className="mt-0.5 shrink-0"
            style={{ color: 'var(--vp-state-waiting)' }}
          />
          <span className="min-w-0 text-ink-2">{t('restore.warning')}</span>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {sessions.map((s) => {
            const failure = failures.find((f) => f.id === s.id)
            const plan = restoreLaunch({ launchCommand: s.launchCommand, cwd: s.cwd }, candidates)
            return (
              <div
                key={s.id}
                data-testid="restore-row"
                className="flex items-start gap-3 border-b border-hairline px-4 py-2.5 last:border-b-0"
              >
                <input
                  type="checkbox"
                  checked={chosen.has(s.id)}
                  onChange={() => toggle(s.id)}
                  data-testid="restore-pick"
                  className="mt-1 shrink-0"
                />
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="min-w-0 truncate text-vp-md text-ink">
                      {labels.get(s.id) ?? sessionLabel(s)}
                    </span>
                    <span className="shrink-0 text-vp-sm text-ink-2">
                      {safeText(projectName(s.projectId))}
                    </span>
                  </div>
                  {/* The whole point of the dialog: the argv, quoted the way a
                      shell would read it, and the directory it starts in. */}
                  <div className="mt-1 min-w-0 break-all font-mono text-vp-sm text-ink-2">
                    {plan ? (
                      <>
                        {t('restore.willRun')}{' '}
                        <span className="text-ink">
                          {plan.argv.map((a) => shellQuote(a)).join(' ')}
                        </span>
                      </>
                    ) : s.launchRecorded ? (
                      t('restore.willRunShellKnown')
                    ) : (
                      t('restore.willRunShell')
                    )}
                  </div>
                  {/* Which of the two things the command above does, in words.
                      The flag is readable to somebody who knows the agent and
                      invisible to everybody else, and this is the line the
                      whole restore is judged on. */}
                  {plan && (
                    <div
                      className="mt-0.5 text-vp-sm"
                      data-testid="restore-resume"
                      style={{ color: plan.resumed ? 'var(--vp-state-done)' : undefined }}
                    >
                      <span className={plan.resumed ? '' : 'text-ink-2'}>
                        {plan.resumed ? t('restore.willResume') : t('restore.willStartCold')}
                      </span>
                    </div>
                  )}
                  {/* The environment comes back from the profile rather than
                      from the row, so a profile deleted since is the one thing
                      a restore silently does less of. The session keeps the id,
                      which is what makes "it is gone" sayable at all instead of
                      the row looking like one that never had a profile. */}
                  {s.launchProfileId !== '' && (
                    <div className="mt-0.5 min-w-0 truncate text-vp-sm text-ink-2">
                      {profileOf(profiles, s.launchProfileId)
                        ? safeText(profileLabel(profileOf(profiles, s.launchProfileId)!))
                        : t('profile.gone')}
                    </div>
                  )}
                  <div className="mt-0.5 min-w-0 truncate font-mono text-vp-sm text-ink-2">
                    {safeText(s.cwd)}
                  </div>
                  <div className="mt-0.5 text-vp-sm text-ink-2">
                    {s.scrollbackAt > 0
                      ? t('restore.scrollbackFrom', {
                          when: new Date(s.scrollbackAt * 1000).toLocaleString(),
                        })
                      : t('restore.noScrollback')}
                  </div>
                  <label className="mt-1.5 flex items-center gap-1.5 text-vp-sm text-ink-2">
                    <input
                      type="checkbox"
                      checked={s.restoreOnBoot}
                      data-testid="restore-on-boot"
                      onChange={(e) =>
                        void api.patchSession(s.id, { restoreOnBoot: e.target.checked })
                      }
                    />
                    <span title={t('restore.onBootWhy')}>{t('restore.onBoot')}</span>
                  </label>
                  {failure && (
                    <div
                      data-testid="restore-row-error"
                      className="mt-1 text-vp-sm"
                      style={{ color: 'var(--vp-state-crashed)' }}
                    >
                      {safeText(failure.error ?? '')}
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </div>

        <div className="flex shrink-0 items-center gap-2 border-t border-hairline px-4 py-2.5">
          {failures.length > 0 && (
            <span
              data-testid="restore-failures"
              className="min-w-0 flex-1 text-vp-base"
              style={{ color: 'var(--vp-state-crashed)' }}
            >
              {t('restore.failed', { n: failures.length })}
            </span>
          )}
          <button
            type="button"
            onClick={onClose}
            className="vp-press ml-auto rounded-vp px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
          >
            {t('restore.close')}
          </button>
          <button
            type="button"
            data-testid="restore-go"
            disabled={busy || chosen.size === 0}
            onClick={() => void run()}
            className="vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base disabled:opacity-50"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {busy && <Loader2 size={13} className="animate-spin" />}
            {busy ? t('restore.working') : t('restore.go', { n: chosen.size })}
          </button>
        </div>
      </div>
    </div>
  )
}
