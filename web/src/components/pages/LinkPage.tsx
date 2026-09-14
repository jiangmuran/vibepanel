import { useEffect, useRef, useState } from 'react'

import { api } from '../../protocol/api'
import type { ShareLink, SharePageRow, ShareParamValue } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { ParamsForm } from './ParamsForm'
import { manifestFor, useNow, usePageDetail } from './usePages'

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** How long a parameter edit sits still before it reaches the wall. The board's number. */
const SAVE_AFTER_MS = 700

/**
 * What an existing link draws: its board, or a page at a version with its
 * settings. The page half of the link editor.
 *
 * Changing the page or the pin is saved at once; parameters are saved a moment
 * after they stop changing, the way the board is, because the wall is the
 * preview and a save button on a thing you are watching change is the one that
 * gets forgotten.
 */
export function LinkPage({
  link,
  pages,
  onSaved,
  onError,
}: {
  link: ShareLink
  pages: SharePageRow[]
  onSaved: () => void
  onError: (message: string) => void
}) {
  useLang()
  const [params, setParams] = useState<Record<string, ShareParamValue>>(link.params)
  const detail = usePageDetail(link.pageId)
  const manifest = manifestFor(detail, link.pinVersion)
  const timer = useRef(0)

  const save = async (pageId: string, pinVersion: number, next: Record<string, ShareParamValue>) => {
    try {
      await api.setSharePage(link.id, { pageId, pinVersion, params: pageId === '' ? {} : next })
      onSaved()
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    }
  }

  useEffect(() => () => clearTimeout(timer.current), [])

  const now = useNow()
  const trial = link.pinUntil > now

  return (
    <div data-testid="link-page" className="mb-3 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2">
      <div className="min-w-0">
        <label htmlFor={`draws-${link.id}`} className="mb-1 block text-vp-sm text-ink-3">
          {t('page.draws')}
        </label>
        <select
          id={`draws-${link.id}`}
          data-testid="link-draws"
          value={link.pageId}
          disabled={trial}
          onChange={(e) => {
            setParams({})
            void save(e.target.value, 0, {})
          }}
          className={INPUT}
        >
          <option value="">{t('page.drawsBoard')}</option>
          {pages.map((p) => (
            <option key={p.id} value={p.id}>
              {safeText(p.name)}
            </option>
          ))}
        </select>
      </div>
      {link.pageId !== '' && detail && (
        <div className="min-w-0">
          <label htmlFor={`pin-${link.id}`} className="mb-1 block text-vp-sm text-ink-3">
            {t('page.version')}
          </label>
          <select
            id={`pin-${link.id}`}
            data-testid="link-pin"
            value={trial ? -1 : link.pinVersion}
            disabled={trial}
            onChange={(e) => void save(link.pageId, Number(e.target.value), params)}
            className={INPUT}
          >
            {trial && <option value={-1}>{t('page.trialShort', { v: link.pinVersion })}</option>}
            <option value={0}>{t('page.followPublished', { v: detail.page.publishedVersion })}</option>
            {detail.versions
              .filter((v) => !v.candidate)
              .map((v) => (
                <option key={v.version} value={v.version}>
                  {t('page.pinned', { v: v.version })}
                </option>
              ))}
          </select>
        </div>
      )}
      {link.pageId !== '' && manifest && (
        <div className="min-w-0 @md:col-span-2">
          <ParamsForm
            specs={manifest.params ?? []}
            values={params}
            idPrefix={`param-${link.id}`}
            onChange={(next) => {
              setParams(next)
              clearTimeout(timer.current)
              timer.current = window.setTimeout(() => void save(link.pageId, link.pinVersion, next), SAVE_AFTER_MS)
            }}
          />
        </div>
      )}
    </div>
  )
}
