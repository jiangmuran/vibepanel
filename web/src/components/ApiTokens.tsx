import { useEffect, useState } from 'react'
import { KeyRound, Trash2 } from 'lucide-react'

import { api } from '../protocol/api'
import type { ApiToken } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { safeText } from './text'

/**
 * Credentials for programs.
 *
 * The one moment that shapes this component: a token is readable exactly once,
 * in the response that made it, because the database keeps a SHA-256 and a
 * leaked backup must not hand over live credentials. So the reveal is a
 * deliberate step you dismiss rather than a row you can come back to — and it
 * says so before you press the button, not after.
 */
export function ApiTokens() {
  useLang()
  const [tokens, setTokens] = useState<ApiToken[]>([])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    api.listTokens().then(
      (list) => {
        if (!cancelled) setTokens(list)
      },
      (e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  const create = async () => {
    try {
      const made = await api.createToken(name.trim() || 'api')
      setFresh(made.token)
      setCopied(false)
      setName('')
      setTokens(await api.listTokens())
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const revoke = async (tok: ApiToken) => {
    // A confirm, because this is the destructive one and an agent stops working
    // the moment it lands. The name is what the person recognises, so it is
    // what the question is about.
    if (!window.confirm(`${t('tok.revoke')} ${safeText(tok.name)} (${tok.prefix}…)?`)) return
    try {
      await api.deleteToken(tok.id)
      setTokens(await api.listTokens())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div data-testid="api-tokens">
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('tok.why')}</p>

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(error)}
        </p>
      )}

      {fresh ? (
        <div className="mb-3 rounded-vp border border-hairline bg-surface-2 p-3">
          <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('tok.once')}
          </p>
          <div className="flex items-center gap-2">
            <code
              data-testid="token-value"
              className="min-w-0 flex-1 truncate rounded-vp bg-surface px-2 py-1.5 font-mono text-vp-base text-ink"
            >
              {fresh}
            </code>
            <button
              type="button"
              data-testid="token-copy"
              onClick={() => {
                void navigator.clipboard?.writeText(fresh).then(
                  () => setCopied(true),
                  () => setCopied(false),
                )
              }}
              className="shrink-0 rounded-vp border border-hairline px-2 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              {copied ? t('tok.copied') : t('tok.copy')}
            </button>
            <button
              type="button"
              data-testid="token-dismiss"
              onClick={() => setFresh(null)}
              className="shrink-0 rounded-vp px-2.5 py-1.5 text-vp-base"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('tok.done')}
            </button>
          </div>
        </div>
      ) : (
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void create()
            }}
            placeholder={t('tok.name')}
            data-testid="token-name"
            className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
          />
          <button
            type="button"
            onClick={() => void create()}
            data-testid="token-create"
            className="shrink-0 rounded-vp px-3 py-1.5 text-vp-base"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('tok.create')}
          </button>
        </div>
      )}

      {tokens.length === 0 ? (
        <p className="text-vp-base text-ink-3">{t('tok.none')}</p>
      ) : (
        tokens.map((tok) => (
          <div
            key={tok.id}
            data-testid="token-row"
            className="flex items-center gap-2 border-t border-hairline py-2 text-vp-base first:border-t-0"
          >
            <KeyRound size={13} className="shrink-0 text-ink-2" />
            <span className="min-w-0 flex-1 truncate text-ink">{safeText(tok.name)}</span>
            <code className="shrink-0 font-mono text-vp-sm text-ink-2">{tok.prefix}…</code>
            <span className="w-24 shrink-0 text-right text-vp-sm text-ink-2">
              {tok.lastUsedAt === 0
                ? t('tok.neverUsed')
                : new Date(tok.lastUsedAt * 1000).toLocaleDateString()}
            </span>
            <button
              type="button"
              onClick={() => void revoke(tok)}
              title={t('tok.revoke')}
              data-testid="token-revoke"
              className="shrink-0 rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              <Trash2 size={13} />
            </button>
          </div>
        ))
      )}
    </div>
  )
}
