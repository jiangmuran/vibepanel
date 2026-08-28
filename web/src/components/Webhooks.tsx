import { useEffect, useState } from 'react'
import { Plus, Send, Trash2 } from 'lucide-react'

import { api } from '../protocol/api'
import type { Webhook } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { showToast } from './toasts'
import { safeText } from './text'

/**
 * Where a "waiting for you" goes when the panel is not open.
 *
 * Presets rather than providers: each one fills in the same three fields, so
 * adding a service is a value in this array and not a branch in the server.
 *
 * Each preset carries what its service *requires* and nothing else. It used to
 * add a `?url={url}` to Bark, and Title and Click headers to ntfy -- neither
 * asked for, both then somebody else's to find and delete. 「不要有自带的参数」.
 * And the custom one is empty, because choosing the option that says you will
 * write it yourself is not an invitation to write half of it for you.
 */
const PRESETS: Array<{ key: string; make: () => Omit<Webhook, 'id'> }> = [
  {
    key: 'wh.presetBark',
    make: () => ({
      name: 'Bark',
      method: 'GET',
      url: 'https://api.day.app/YOUR_KEY/{session}/{state}',
      enabled: true,
    }),
  },
  {
    key: 'wh.presetNtfy',
    make: () => ({
      name: 'ntfy',
      method: 'POST',
      url: 'https://ntfy.sh/YOUR_TOPIC',
      body: '{session} {state}',
      enabled: true,
    }),
  },
  {
    key: 'wh.presetServerChan',
    make: () => ({
      name: 'Server酱',
      method: 'POST',
      url: 'https://sctapi.ftqq.com/YOUR_KEY.send',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'title={session}&desp={state}',
      enabled: true,
    }),
  },
  {
    key: 'wh.presetCustom',
    make: () => ({
      name: '',
      method: 'POST',
      url: '',
      enabled: true,
    }),
  },
]

export function Webhooks() {
  useLang()
  const [list, setList] = useState<Webhook[]>([])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.webhooks().then(
      (l) => {
        if (!cancelled) setList(l)
      },
      () => {},
    )
    return () => {
      cancelled = true
    }
  }, [])

  const save = async (next: Webhook[]) => {
    setBusy(true)
    try {
      setList(await api.saveWebhooks(next))
    } catch (e) {
      showToast({ kind: 'error', key: 'wh.saveFailed', detail: msg(e) })
    } finally {
      setBusy(false)
    }
  }

  const test = async (w: Webhook) => {
    setBusy(true)
    try {
      const res = await api.testWebhook(w)
      showToast(
        res.ok
          ? { kind: 'success', key: 'wh.testOk', detail: res.said }
          : { kind: 'error', key: 'wh.testFailed', detail: res.error ?? res.said },
      )
    } catch (e) {
      showToast({ kind: 'error', key: 'wh.testFailed', detail: msg(e) })
    } finally {
      setBusy(false)
    }
  }

  const patch = (i: number, change: Partial<Webhook>) =>
    setList((l) => l.map((w, j) => (j === i ? { ...w, ...change } : w)))

  return (
    <div data-testid="webhooks">
      <p className="mb-2 text-vp-sm leading-relaxed text-ink-2">{t('wh.why')}</p>

      {list.map((w, i) => (
        <div
          key={w.id || i}
          data-testid="webhook-row"
          className="mb-2 rounded-vp border border-hairline bg-surface-2 p-2"
        >
          <div className="mb-1.5 flex items-center gap-2">
            <input
              value={w.name}
              onChange={(e) => patch(i, { name: e.target.value })}
              placeholder={t('wh.name')}
              data-testid="webhook-name"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-base text-ink outline-none focus:border-accent"
            />
            <label className="flex shrink-0 items-center gap-1 text-vp-sm text-ink-2">
              <input
                type="checkbox"
                checked={w.enabled}
                onChange={(e) => patch(i, { enabled: e.target.checked })}
                data-testid="webhook-enabled"
              />
              {t('wh.enabled')}
            </label>
            <button
              type="button"
              onClick={() => void test(w)}
              disabled={busy}
              title={t('wh.test')}
              data-testid="webhook-test"
              className="vp-control disabled:opacity-50"
            >
              <Send size={13} />
            </button>
            <button
              type="button"
              onClick={() => void save(list.filter((_, j) => j !== i))}
              disabled={busy}
              title={t('wh.remove')}
              data-testid="webhook-remove"
              className="vp-control disabled:opacity-50"
            >
              <Trash2 size={13} />
            </button>
          </div>
          <div className="mb-1.5 flex gap-2">
            <select
              value={w.method || 'POST'}
              onChange={(e) => patch(i, { method: e.target.value })}
              data-testid="webhook-method"
              className="shrink-0 rounded-vp border border-hairline bg-surface px-1.5 py-1 text-vp-sm text-ink outline-none"
            >
              <option>GET</option>
              <option>POST</option>
              <option>PUT</option>
            </select>
            <input
              value={w.url}
              onChange={(e) => patch(i, { url: e.target.value })}
              placeholder="https://…"
              data-testid="webhook-url"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
          </div>
          <textarea
            value={w.body ?? ''}
            onChange={(e) => patch(i, { body: e.target.value })}
            placeholder={t('wh.body')}
            rows={2}
            data-testid="webhook-body"
            className="w-full resize-y rounded-vp border border-hairline bg-surface px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
          />
        </div>
      ))}

      <div className="flex flex-wrap items-center gap-2">
        {PRESETS.map((p) => (
          <button
            key={p.key}
            type="button"
            disabled={busy}
            onClick={() => void save([...list, { ...p.make(), id: '' }])}
            data-testid="webhook-add"
            className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink disabled:opacity-50"
          >
            <Plus size={12} />
            {t(p.key as Parameters<typeof t>[0])}
          </button>
        ))}
        {list.length > 0 && (
          <button
            type="button"
            disabled={busy}
            onClick={() => void save(list)}
            data-testid="webhook-save"
            className="vp-press ml-auto rounded-vp px-3 py-1 text-vp-base font-medium disabled:opacity-50"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('wh.save')}
          </button>
        )}
      </div>

      {list.some((w) => w.url.includes('YOUR_')) && (
        <p className="mt-2 text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
          {safeText(t('wh.placeholder'))}
        </p>
      )}
    </div>
  )
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
