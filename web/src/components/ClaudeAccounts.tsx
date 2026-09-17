import { useEffect, useState } from 'react'
import { Check, Copy, Pencil, Plus, RefreshCw, Trash2, UserRound } from 'lucide-react'

import type { ClaudeAccount, ClaudeAccountStatus } from '../protocol/wire'
import { api } from '../protocol/api'
import { copyTextInGesture } from '../clipboard'
import { safeText } from './text'
import { askConfirm } from './ask'
import { InlineName } from './InlineName'
import { t, useLang } from '../i18n'

/**
 * Claude Code logins kept beside ~/.claude.
 *
 * The server does the work (internal/claudeaccount): a directory per account
 * whose habits are links into ~/.claude. This page makes them, names them,
 * says who each is signed in as, and removes them.
 *
 * There is no sign-in button, and the reason is where signing in happens. It
 * is Claude Code's own flow in a terminal -- a URL, a code pasted back -- and
 * the panel already has terminals. A button would have to open one somewhere,
 * in some project, which is a session the person did not ask for; the hint
 * under a signed-out account says how to get the same terminal on purpose.
 */
export function ClaudeAccounts() {
  useLang()
  const [accounts, setAccounts] = useState<ClaudeAccount[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [creating, setCreating] = useState(false)

  const reload = async () => {
    setAccounts(await api.claudeAccounts())
  }

  useEffect(() => {
    let cancelled = false
    api.claudeAccounts().then(
      (list) => {
        if (!cancelled) setAccounts(list)
      },
      (e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  const remove = async (a: ClaudeAccount) => {
    const yes = await askConfirm({
      title: t('acct.removeTitle', { name: a.name }),
      body: t('acct.removeBody'),
      confirm: t('acct.remove'),
      cancel: t('acct.cancel'),
      destructive: true,
    })
    if (!yes) return
    try {
      const r = await api.deleteClaudeAccount(a.id)
      setError('')
      // The directory is gone either way; what a failed logout leaves is a
      // Keychain entry on macOS, and this is the only place that says so.
      setNotice(r.logoutError ? t('acct.logoutFailed', { error: r.logoutError }) : '')
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const rename = async (a: ClaudeAccount, name: string) => {
    try {
      await api.renameClaudeAccount(a.id, name)
      setError('')
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div data-testid="claude-accounts">
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('acct.why')}</p>

      {error && (
        <p
          className="mb-2 text-vp-base"
          data-testid="claude-account-error"
          style={{ color: 'var(--vp-state-crashed)' }}
        >
          {safeText(error)}
        </p>
      )}
      {notice && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
          {safeText(notice)}
        </p>
      )}

      {accounts.map((a) => (
        <AccountRow
          key={a.id}
          account={a}
          onRename={(name) => void rename(a, name)}
          onRemove={() => void remove(a)}
        />
      ))}

      {creating ? (
        <NewAccount
          onCancel={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            setError('')
            void reload()
          }}
          onError={setError}
        />
      ) : (
        <button
          type="button"
          onClick={() => setCreating(true)}
          data-testid="claude-account-new"
          className="vp-press mt-3 flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          <Plus size={13} />
          {t('acct.new')}
        </button>
      )}
    </div>
  )
}

function AccountRow({
  account: a,
  onRename,
  onRemove,
}: {
  account: ClaudeAccount
  onRename: (name: string) => void
  onRemove: () => void
}) {
  useLang()
  // null while a read is in flight. A counter asks for another read.
  const [status, setStatus] = useState<ClaudeAccountStatus | null>(null)
  const [asked, setAsked] = useState(0)
  const checking = status === null
  const [editing, setEditing] = useState(false)
  const [copied, setCopied] = useState(false)

  // Once when the row appears and when asked. Each read runs `claude` for
  // about a second, so this never polls: a login happens in a terminal
  // somewhere else, and "check again" is the honest way to find out.
  useEffect(() => {
    let cancelled = false
    api.claudeAccountStatus(a.id).then(
      (st) => {
        if (!cancelled) setStatus(st)
      },
      (e: unknown) => {
        if (!cancelled) {
          setStatus({ status: null, error: e instanceof Error ? e.message : String(e), links: [] })
        }
      },
    )
    return () => {
      cancelled = true
    }
  }, [a.id, asked])
  const check = () => {
    setStatus(null)
    setAsked((n) => n + 1)
  }

  const blocked = (status?.links ?? []).filter((l) => l.state === 'blocked').map((l) => l.name)

  return (
    <div
      data-testid="claude-account-row"
      data-account={a.id}
      className="border-t border-hairline py-2 text-vp-base first:border-t-0"
    >
      <div className="flex items-center gap-2">
        <UserRound size={13} className="shrink-0 text-ink-2" />
        <span className="min-w-0 flex-1 truncate text-ink">
          <InlineName
            value={a.name}
            onCommit={onRename}
            editing={editing}
            onEditingChange={setEditing}
          />
        </span>
        {a.isolated && (
          <span className="shrink-0 text-vp-sm text-ink-3">{t('acct.isolatedTag')}</span>
        )}
        <button
          type="button"
          onClick={check}
          disabled={checking}
          title={t('acct.refresh')}
          data-testid="claude-account-refresh"
          className="vp-control vp-press disabled:opacity-50"
        >
          <RefreshCw size={13} />
        </button>
        <button
          type="button"
          onClick={() => setEditing(true)}
          title={t('acct.rename')}
          className="vp-control vp-press"
        >
          <Pencil size={13} />
        </button>
        <button
          type="button"
          onClick={onRemove}
          title={t('acct.remove')}
          data-testid="claude-account-remove"
          className="vp-control vp-press"
        >
          <Trash2 size={13} />
        </button>
      </div>

      <div className="mt-1 pl-5 text-vp-sm leading-relaxed" data-testid="claude-account-status">
        <AccountStatusLine status={status} checking={checking} />
        {blocked.length > 0 && (
          <p style={{ color: 'var(--vp-state-waiting)' }}>
            {t('acct.blocked', { names: blocked.join(', ') })}
          </p>
        )}
        {a.profiles.length > 0 && (
          <p className="text-ink-2">{t('acct.usedBy', { names: a.profiles.join(', ') })}</p>
        )}
        {a.running > 0 && <p className="text-ink-2">{t('acct.running', { n: a.running })}</p>}
        <p className="flex min-w-0 items-center gap-1.5 text-ink-3">
          <span className="shrink-0">{t('acct.dir')}</span>
          <span className="min-w-0 truncate font-mono" title={a.dir}>
            {safeText(a.dir)}
          </span>
          <button
            type="button"
            onClick={() => copyTextInGesture(a.dir, setCopied)}
            title={t('acct.copyDir')}
            className="vp-control shrink-0"
          >
            {copied ? <Check size={11} /> : <Copy size={11} />}
          </button>
        </p>
      </div>
    </div>
  )
}

function AccountStatusLine({
  status,
  checking,
}: {
  status: ClaudeAccountStatus | null
  checking: boolean
}) {
  if (checking || !status) return <p className="text-ink-3">{t('acct.checking')}</p>
  if (!status.status) {
    return (
      <p style={{ color: 'var(--vp-state-crashed)' }}>
        {t('acct.statusError', { error: safeText(status.error ?? '') })}
      </p>
    )
  }
  const s = status.status
  if (s.loggedIn && s.authMethod === 'claude.ai') {
    const who = [s.email, s.orgName, s.subscriptionType].filter(Boolean).join(' · ')
    return <p className="text-ink">{t('acct.loggedIn', { who: safeText(who) })}</p>
  }
  if (s.loggedIn) {
    // An API key or a token in the environment, which stands in for the login
    // and bills something other than the account.
    return (
      <p style={{ color: 'var(--vp-state-waiting)' }}>
        {t('acct.otherAuth', { method: safeText(s.authMethod) })}
      </p>
    )
  }
  return (
    <>
      <p className="text-ink">{t('acct.loggedOut')}</p>
      <p className="text-ink-2">{t('acct.howToLogin')}</p>
    </>
  )
}

function NewAccount({
  onCancel,
  onCreated,
  onError,
}: {
  onCancel: () => void
  onCreated: () => void
  onError: (msg: string) => void
}) {
  useLang()
  const [name, setName] = useState('')
  const [isolated, setIsolated] = useState(false)
  const [busy, setBusy] = useState(false)

  const create = async () => {
    setBusy(true)
    try {
      await api.createClaudeAccount({ name, isolated })
      onCreated()
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      data-testid="claude-account-editor"
      className="mt-3 rounded-vp border border-hairline bg-surface-2 p-3"
    >
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={t('acct.name')}
        data-testid="claude-account-name"
        className="mb-2 w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
      />
      <label className="flex items-center gap-1.5 text-vp-base text-ink">
        <input
          type="checkbox"
          checked={isolated}
          data-testid="claude-account-isolated"
          onChange={(e) => setIsolated(e.target.checked)}
        />
        {t('acct.isolated')}
      </label>
      <p className="mt-1 text-vp-sm text-ink-3">{t('acct.isolatedHint')}</p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          disabled={busy || name.trim() === ''}
          onClick={() => void create()}
          data-testid="claude-account-create"
          className="vp-press shrink-0 rounded-vp px-3 py-1.5 text-vp-base disabled:opacity-50"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          {t('acct.create')}
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="vp-press shrink-0 rounded-vp px-3 py-1.5 text-vp-base text-ink-2 hover:text-ink"
        >
          {t('acct.cancel')}
        </button>
      </div>
    </div>
  )
}
