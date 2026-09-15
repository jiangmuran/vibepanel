import { ChevronsUpDown, PanelsTopLeft } from 'lucide-react'

import type { SharePageRow } from '../../protocol/wire'
import { t, useLang } from '../../i18n'

/**
 * The share page, as one line above the file list, under the repository's.
 *
 * Shown only when this project's directory is a page's draft. The same gesture
 * as the repository line: press it and the Preview has the side panel; press
 * again and it has the window.
 */
export function PageLine({ page, onOpen }: { page: SharePageRow; onOpen: () => void }) {
  useLang()
  return (
    <button
      type="button"
      data-testid="page-line"
      onClick={onOpen}
      title={t('detail.open', { what: t('panel.page') })}
      aria-label={t('detail.open', { what: t('panel.page') })}
      className="vp-control w-full justify-start gap-1.5 px-2"
    >
      <PanelsTopLeft size={11} className="shrink-0" />
      <span className="min-w-0 truncate text-vp-xs text-ink">{t('panel.page')}</span>
      <span className="tabular shrink-0 text-vp-xs">
        {page.publishedVersion > 0 ? t('page.published', { v: page.publishedVersion }) : t('page.unpublished')}
      </span>
      {page.links > 0 && (
        <span className="tabular shrink-0 text-vp-xs">{t('page.links', { n: page.links })}</span>
      )}
      <ChevronsUpDown size={11} className="ml-auto shrink-0" />
    </button>
  )
}
