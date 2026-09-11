import { useState } from 'react'
import { Download, RefreshCw } from 'lucide-react'

import { api } from '../protocol/api'
import type { UpdateCheck } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { askConfirm } from './ask'
import { showToast } from './toasts'
import { safeText } from './text'

/**
 * Updating the panel from the panel.
 *
 * On demand, never on a timer: a self-hosted panel that reaches out on its own
 * schedule is a surprise, and this one has no telemetry at all.
 *
 * The confirmation says the part people actually worry about before it is
 * pressed rather than after — the sessions do not restart with the panel,
 * because they belong to tmux and both units set KillMode=process. That is the
 * whole premise of the project, and it is exactly the thing somebody hesitating
 * over a button labelled "restart" needs told.
 */
export function UpdateSection() {
  useLang()
  const [found, setFound] = useState<UpdateCheck | null>(null)
  const [busy, setBusy] = useState<'' | 'check' | 'apply'>('')
  // Held for as long as the form is open and no longer. Never put anywhere
  // that outlives the page: not localStorage, not a URL, not a log.
  const [secret, setSecret] = useState('')

  const check = async () => {
    setBusy('check')
    try {
      setFound(await api.checkUpdate())
    } catch (e) {
      showToast({ kind: 'error', key: 'upd.failed', params: { why: '' }, detail: msg(e) })
    } finally {
      setBusy('')
    }
  }

  const apply = async (secret?: string) => {
    if (!found?.version) return
    if (!(await askConfirm({
      title: t('upd.confirmTitle', { v: found.version }),
      body: t('upd.confirmBody'),
      confirm: t('upd.apply'),
      cancel: t('ask.cancel'),
    }))) return
    setBusy('apply')
    try {
      const res = await api.applyUpdate(secret)
      // Cleared on the way out and not in `finally`: a wrong one has to stay
      // in the box, or correcting a typo means typing the whole thing again.
      setSecret('')
      showToast({
        kind: 'success',
        // Authorised rather than installed. There is no version to report --
        // the installer is running behind this and ends by restarting the
        // unit -- so claiming one would be the page inventing a number.
        key: res.elevated ? 'upd.elevated' : res.restarting ? 'upd.done' : 'upd.doneNoRestart',
        params: { v: res.installed, why: res.restartWhy },
      })
    } catch (e) {
      showToast({ kind: 'error', key: 'upd.failed', params: { why: '' }, detail: msg(e) })
    } finally {
      setBusy('')
    }
  }

  const status = () => {
    if (!found) return null
    if (found.unreachable) return t('upd.unreachable', { why: found.unreachable })
    if (!found.version) return t('upd.noRelease')
    if (!found.newer) {
      // A development build cannot be compared, and saying "up to date" would
      // be a claim nobody can support.
      return /^v?\d+\.\d+\.\d+/.test(found.current)
        ? t('upd.upToDate', { v: found.current })
        : t('upd.devBuild')
    }
    if (!found.asset) return t('upd.noAsset', { v: found.version })
    return t('upd.available', { v: found.version, cur: found.current })
  }

  // Not offered when the panel cannot replace its own binary. A system
  // install owns it as root, so the button would download seven megabytes and
  // then fail on a temp file -- which is exactly what it did.
  const canApply = Boolean(found?.newer && found.asset && !found.byHand)

  return (
    <div data-testid="update-section">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => void check()}
          disabled={busy !== ''}
          data-testid="update-check"
          className="vp-press flex items-center gap-1.5 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink hover:bg-surface-2 disabled:opacity-50"
        >
          <RefreshCw size={13} className={busy === 'check' ? 'animate-spin' : ''} />
          {busy === 'check' ? t('upd.checking') : t('upd.check')}
        </button>
        {canApply && (
          <button
            type="button"
            onClick={() => void apply()}
            disabled={busy !== ''}
            data-testid="update-apply"
            className="vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            <Download size={13} />
            {busy === 'apply' ? t('upd.applying') : t('upd.apply')}
          </button>
        )}
      </div>

      {/* The panel cannot replace a binary it does not own, and it does not
          hold the credential that could. What it can do is take one, typed
          here, at a moment somebody chose, and spend it on one fixed command.

          That is not the same as a console that can escalate, and the
          difference is worth stating: anybody who can reach this page holds a
          session on a panel whose purpose is running commands as this account,
          and can type the same thing into a terminal in the next tab. What
          this removes is a window switch. */}
      {found?.byHand && found.newer && found.elevate && (
        <form
          data-testid="update-elevate"
          onSubmit={(e) => {
            e.preventDefault()
            void apply(secret)
          }}
          className="mt-2 flex flex-wrap items-center gap-2"
        >
          <input
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            // Named, because "this account" was read as the panel account --
            // the one the person is signed into on this very page -- and sudo
            // wants the machine's. Both are reasonable readings of the same
            // three words, so the field says whose.
            placeholder={t('upd.secretHint', { user: found.elevateAs ?? '' })}
            autoComplete="current-password"
            data-testid="update-secret"
            className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
          />
          <button
            type="submit"
            disabled={busy !== '' || secret === ''}
            data-testid="update-elevate-apply"
            className="vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            <Download size={13} />
            {busy === 'apply' ? t('upd.applying') : t('upd.apply')}
          </button>
        </form>
      )}
      {found?.byHand && found.newer && (
        <p
          className="mt-2 text-vp-sm leading-relaxed break-all text-ink-2"
          data-testid="update-by-hand"
        >
          {/* The server's sentence, which names the command. Kept even when
              the field above is offered: a machine where the credential is
              refused still needs the shell to be a way through, and it is the
              only line here that survives that. Shown as it arrived -- it
              contains a path this side does not know. */}
          {safeText(found.byHand)}
        </p>
      )}
      {found && (
        <p className="mt-2 text-vp-sm leading-relaxed text-ink-2" data-testid="update-status">
          {safeText(status() ?? '')}
        </p>
      )}

      {/* The notes come from a release page and are somebody else's text, so
          they are shown as text and never as markup. */}
      {found?.newer && found.notes && (
        <details className="mt-2">
          <summary className="cursor-pointer text-vp-sm text-ink-2">{t('upd.notes')}</summary>
          <pre className="mt-1 max-h-48 overflow-auto rounded-vp bg-surface-2 p-2 text-vp-xs whitespace-pre-wrap text-ink-2">
            {safeText(found.notes)}
          </pre>
        </details>
      )}
    </div>
  )
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
