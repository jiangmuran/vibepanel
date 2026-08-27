import { useEffect, useState } from 'react'
import { ExternalLink, MonitorSmartphone, Trash2 } from 'lucide-react'

import { api } from '../protocol/api'
import type { ShareDetail, ShareLink } from '../protocol/wire'
import type { Key } from '../i18n'
import { t, useLang } from '../i18n'
import { safeText } from './text'

/**
 * Read-only share links.
 *
 * The same shape as ApiTokens and for the same reason: the token is readable
 * exactly once, in the response that made it, because the database keeps a
 * SHA-256 and a leaked backup must not hand over live links.
 *
 * What is different is that the thing you copy is a URL rather than a
 * credential to paste into a header, so that is what the reveal shows. Anybody
 * holding it can watch, which is the whole point and also the whole risk — the
 * copy above the button says so before the link exists rather than after.
 */

/** Expiry choices, in seconds. 0 is a link that does not expire. */
const EXPIRIES: { seconds: number; label: Key }[] = [
  { seconds: 0, label: 'share.expiryNever' },
  { seconds: 86400, label: 'share.expiryDay' },
  { seconds: 604800, label: 'share.expiryWeek' },
  { seconds: 2592000, label: 'share.expiryMonth' },
]

function shareURL(token: string): string {
  return `${location.origin}/share/${token}`
}

function detailLabel(detail: string): string {
  return detail === 'names' ? t('share.detailNames') : t('share.detailCounts')
}

export function ShareLinks() {
  useLang()
  const [links, setLinks] = useState<ShareLink[]>([])
  const [name, setName] = useState('')
  const [detail, setDetail] = useState<ShareDetail>('counts')
  const [expiresIn, setExpiresIn] = useState(0)
  const [fresh, setFresh] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  // Which row is one more click from being revoked. An inline second step
  // rather than a browser dialog: this panel is used on a phone, where a
  // native confirm covers the screen and reads as a page change.
  const [confirming, setConfirming] = useState<string | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    api.listShares().then(
      (list) => {
        if (!cancelled) setLinks(list)
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
      const made = await api.createShare(name.trim(), detail, expiresIn)
      setFresh(shareURL(made.token))
      setCopied(false)
      setName('')
      setLinks(await api.listShares())
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const revoke = async (link: ShareLink) => {
    try {
      await api.deleteShare(link.id)
      setLinks(await api.listShares())
      setConfirming(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div data-testid="share-links">
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('share.why')}</p>

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(error)}
        </p>
      )}

      {fresh ? (
        <div className="mb-3 rounded-vp border border-hairline bg-surface-2 p-3">
          <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('share.once')}
          </p>
          <div className="flex items-center gap-2">
            <code
              data-testid="share-url"
              className="min-w-0 flex-1 truncate rounded-vp bg-surface px-2 py-1.5 font-mono text-vp-base text-ink"
            >
              {fresh}
            </code>
            <button
              type="button"
              data-testid="share-copy"
              onClick={() => {
                void navigator.clipboard?.writeText(fresh).then(
                  () => setCopied(true),
                  () => setCopied(false),
                )
              }}
              className="vp-press shrink-0 rounded-vp border border-hairline px-2 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              {copied ? t('tok.copied') : t('tok.copy')}
            </button>
            <a
              href={fresh}
              target="_blank"
              rel="noreferrer"
              title={t('share.open')}
              data-testid="share-open"
              className="vp-press shrink-0 rounded-vp border border-hairline p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              <ExternalLink size={13} />
            </a>
            <button
              type="button"
              data-testid="share-dismiss"
              onClick={() => setFresh(null)}
              className="shrink-0 rounded-vp px-2.5 py-1.5 text-vp-base"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('tok.done')}
            </button>
          </div>
        </div>
      ) : (
        <div className="mb-3">
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void create()
              }}
              placeholder={t('share.name')}
              data-testid="share-name"
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
            />
            <select
              value={detail}
              onChange={(e) => setDetail(e.target.value as ShareDetail)}
              title={t('share.shows')}
              data-testid="share-detail"
              className="shrink-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
            >
              <option value="counts">{t('share.detailCounts')}</option>
              <option value="names">{t('share.detailNames')}</option>
            </select>
            <select
              value={expiresIn}
              onChange={(e) => setExpiresIn(Number(e.target.value))}
              title={t('share.expiry')}
              data-testid="share-expiry"
              className="shrink-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
            >
              {EXPIRIES.map((choice) => (
                <option key={choice.seconds} value={choice.seconds}>
                  {t(choice.label)}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => void create()}
              data-testid="share-create"
              className="shrink-0 rounded-vp px-3 py-1.5 text-vp-base"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('share.create')}
            </button>
          </div>
          <p className="text-vp-sm leading-relaxed text-ink-3">{t('share.detailWhy')}</p>
        </div>
      )}

      {links.length === 0 ? (
        <p className="text-vp-base text-ink-3">{t('share.none')}</p>
      ) : (
        links.map((link) => (
          <div
            key={link.id}
            data-testid="share-row"
            className="flex items-center gap-2 border-t border-hairline py-2 text-vp-base first:border-t-0"
          >
            <MonitorSmartphone size={13} className="shrink-0 text-ink-2" />
            <span className="min-w-0 flex-1 truncate text-ink">{safeText(link.name)}</span>
            <code className="shrink-0 font-mono text-vp-sm text-ink-2">{link.prefix}…</code>
            <span className="shrink-0 text-vp-sm text-ink-2">{detailLabel(link.detail)}</span>
            <span className="w-28 shrink-0 text-right text-vp-sm text-ink-2">
              {link.expiresAt === 0
                ? t('share.noExpiry')
                : t('share.expiresOn', {
                    date: new Date(link.expiresAt * 1000).toLocaleDateString(),
                  })}
            </span>
            <span className="w-24 shrink-0 text-right text-vp-sm text-ink-2">
              {link.lastUsedAt === 0
                ? t('tok.neverUsed')
                : new Date(link.lastUsedAt * 1000).toLocaleDateString()}
            </span>
            {confirming === link.id ? (
              <span className="flex shrink-0 items-center gap-1">
                <button
                  type="button"
                  onClick={() => void revoke(link)}
                  data-testid="share-revoke-confirm"
                  className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-sm"
                  style={{ background: 'var(--vp-state-crashed)', color: 'var(--vp-accent-ink)' }}
                >
                  {t('share.revokeSure')}
                </button>
                <button
                  type="button"
                  onClick={() => setConfirming(null)}
                  className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-sm text-ink-2"
                >
                  {t('share.keep')}
                </button>
              </span>
            ) : (
              <button
                type="button"
                onClick={() => setConfirming(link.id)}
                title={t('share.revoke')}
                data-testid="share-revoke"
                className="vp-press shrink-0 rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
              >
                <Trash2 size={13} />
              </button>
            )}
          </div>
        ))
      )}
    </div>
  )
}
