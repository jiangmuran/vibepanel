import { useEffect, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'

import { api } from '../protocol/api'
import type { VncTarget } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { showToast } from './toasts'

/**
 * The displays the panel may open, and the credentials for them.
 *
 * Here rather than in the panel tab because this is where an address and a
 * password are decided, and the tab is 240 pixels wide. The tab picks among
 * what this made.
 */
export function VncDisplays() {
  useLang()
  const [list, setList] = useState<VncTarget[]>([])
  const [busy, setBusy] = useState(false)
  // The password is write-only: it is never sent back, so the field cannot be
  // populated from the row and is held here per id until it is saved.
  const [secrets, setSecrets] = useState<Record<string, string>>({})
  const [draft, setDraft] = useState<null | {
    name: string
    host: string
    port: string
    viewOnly: boolean
    password: string
  }>(null)

  useEffect(() => {
    let cancelled = false
    api.vncTargets().then(
      (l) => {
        if (!cancelled) setList(l)
      },
      () => {},
    )
    return () => {
      cancelled = true
    }
  }, [])

  const save = async (row: VncTarget) => {
    setBusy(true)
    try {
      const password = secrets[row.id]
      const saved = await api.saveVncTarget(row.id, {
        name: row.name,
        host: row.host,
        port: row.port,
        viewOnly: row.viewOnly,
        // Omitted unless something was typed. Sending '' would clear the
        // stored password on every save of a name.
        ...(password === undefined ? {} : { password }),
      })
      setList((l) => l.map((x) => (x.id === saved.id ? saved : x)))
      setSecrets((s) => {
        const next = { ...s }
        delete next[row.id]
        return next
      })
    } catch (e) {
      showToast({ kind: 'error', key: 'vnc.saveFailed', detail: msg(e) })
    } finally {
      setBusy(false)
    }
  }

  const add = async () => {
    if (!draft) return
    setBusy(true)
    try {
      const made = await api.saveVncTarget(null, {
        name: draft.name,
        host: draft.host.trim(),
        port: Number(draft.port),
        viewOnly: draft.viewOnly,
        password: draft.password,
      })
      setList((l) => [...l, made])
      setDraft(null)
    } catch (e) {
      showToast({ kind: 'error', key: 'vnc.saveFailed', detail: msg(e) })
    } finally {
      setBusy(false)
    }
  }

  const remove = async (row: VncTarget) => {
    setBusy(true)
    try {
      await api.deleteVncTarget(row.id)
      setList((l) => l.filter((x) => x.id !== row.id))
    } catch (e) {
      showToast({ kind: 'error', key: 'vnc.saveFailed', detail: msg(e) })
    } finally {
      setBusy(false)
    }
  }

  const patch = (id: string, change: Partial<VncTarget>) =>
    setList((l) => l.map((x) => (x.id === id ? { ...x, ...change } : x)))

  return (
    <div data-testid="vnc-displays">
      <p className="mb-2 text-vp-sm leading-relaxed text-ink-2">{t('vnc.why')}</p>

      {list.map((row) => (
        <div
          key={row.id}
          data-testid="vnc-row"
          className="mb-2 rounded-vp border border-hairline bg-surface-2 p-2"
        >
          <div className="mb-1.5 flex items-center gap-2">
            <input
              value={row.name}
              onChange={(e) => patch(row.id, { name: e.target.value })}
              placeholder={t('vnc.name')}
              data-testid="vnc-name"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-base text-ink outline-none focus:border-accent"
            />
            <label className="flex shrink-0 items-center gap-1 text-vp-sm text-ink-2">
              <input
                type="checkbox"
                checked={row.viewOnly}
                onChange={(e) => patch(row.id, { viewOnly: e.target.checked })}
                data-testid="vnc-viewonly"
              />
              {t('vnc.viewOnly')}
            </label>
            <button
              type="button"
              onClick={() => void remove(row)}
              disabled={busy}
              title={t('vnc.remove')}
              data-testid="vnc-remove"
              className="vp-press shrink-0 rounded-md p-1 text-ink-2 hover:text-ink disabled:opacity-50"
            >
              <Trash2 size={13} />
            </button>
          </div>
          <div className="flex flex-wrap gap-2">
            <input
              value={row.host}
              onChange={(e) => patch(row.id, { host: e.target.value })}
              placeholder={t('vnc.host')}
              data-testid="vnc-host"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
            <input
              value={row.port}
              inputMode="numeric"
              onChange={(e) => patch(row.id, { port: Number(e.target.value) || 0 })}
              data-testid="vnc-port"
              className="w-20 shrink-0 rounded-vp border border-hairline bg-surface px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
            <input
              type="password"
              value={secrets[row.id] ?? ''}
              onChange={(e) => setSecrets((s) => ({ ...s, [row.id]: e.target.value }))}
              placeholder={row.hasPassword ? t('vnc.passwordSet') : t('vnc.password')}
              data-testid="vnc-password"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent"
            />
            <button
              type="button"
              onClick={() => void save(row)}
              disabled={busy}
              data-testid="vnc-save"
              className="vp-press shrink-0 rounded-vp px-3 py-1 text-vp-base font-medium disabled:opacity-50"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('vnc.save')}
            </button>
          </div>
        </div>
      ))}

      {draft !== null && (
        <div data-testid="vnc-draft" className="mb-2 rounded-vp border border-hairline bg-surface-2 p-2">
          <div className="mb-1.5 flex items-center gap-2">
            <input
              autoFocus
              value={draft.name}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              placeholder={t('vnc.name')}
              data-testid="vnc-draft-name"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-base text-ink outline-none focus:border-accent"
            />
            <label className="flex shrink-0 items-center gap-1 text-vp-sm text-ink-2">
              <input
                type="checkbox"
                checked={draft.viewOnly}
                onChange={(e) => setDraft({ ...draft, viewOnly: e.target.checked })}
                data-testid="vnc-draft-viewonly"
              />
              {t('vnc.viewOnly')}
            </label>
          </div>
          <div className="flex flex-wrap gap-2">
            <input
              value={draft.host}
              onChange={(e) => setDraft({ ...draft, host: e.target.value })}
              placeholder={t('vnc.host')}
              data-testid="vnc-draft-host"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
            <input
              value={draft.port}
              inputMode="numeric"
              onChange={(e) => setDraft({ ...draft, port: e.target.value })}
              data-testid="vnc-draft-port"
              className="w-20 shrink-0 rounded-vp border border-hairline bg-surface px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
            <input
              type="password"
              value={draft.password}
              onChange={(e) => setDraft({ ...draft, password: e.target.value })}
              placeholder={t('vnc.password')}
              data-testid="vnc-draft-password"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent"
            />
            <button
              type="button"
              onClick={() => void add()}
              disabled={busy || draft.host.trim() === ''}
              data-testid="vnc-draft-save"
              className="vp-press shrink-0 rounded-vp px-3 py-1 text-vp-base font-medium disabled:opacity-50"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('vnc.add')}
            </button>
          </div>
        </div>
      )}

      <div className="flex items-center gap-2">
        <button
          type="button"
          disabled={busy}
          onClick={() => setDraft({ name: '', host: '127.0.0.1', port: '5900', viewOnly: false, password: '' })}
          data-testid="vnc-add"
          className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink disabled:opacity-50"
        >
          <Plus size={12} />
          {t('vnc.add')}
        </button>
      </div>

      {/* One sentence, and it is here rather than in the tab because this is
          where somebody types the password. Say the thing and stop. */}
      <p className="mt-2 text-vp-sm text-ink-2">{t('vnc.passwordNote')}</p>
    </div>
  )
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
