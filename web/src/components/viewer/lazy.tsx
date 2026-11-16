import { lazy } from 'react'
import { createPortal } from 'react-dom'
import { RotateCw, X } from 'lucide-react'

import { t, useLang } from '../../i18n'
import type { FileViewer as Viewer } from './FileViewer'

type Props = Parameters<typeof Viewer>[0]

/**
 * The viewer, fetched the first time a file is opened.
 *
 * Its own chunk so the panel's first paint does not carry it, nor the
 * tokenisers later batches bring with it (docs/file-preview.md,
 * "Dependencies"); bundle.test.ts pins both sizes.
 *
 * A chunk that cannot be fetched is a tab older than the panel it is talking
 * to: an upgrade replaced the bundle, and the chunk this tab knows the name of
 * is gone. That is answered in the viewer's own place with the way out, not
 * thrown at the error boundary, which would take the whole Files tab with it.
 */
export const LazyFileViewer = lazy(() =>
  import('./FileViewer').then(
    (m) => ({ default: m.FileViewer }),
    () => ({ default: Unavailable }),
  ),
)

function Unavailable({ onClose }: Props) {
  useLang()
  return createPortal(
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        role="alertdialog"
        aria-modal="true"
        onClick={(e) => e.stopPropagation()}
        data-testid="viewer-unavailable"
        className="flex max-w-sm flex-col items-center gap-3 rounded-vp border border-hairline bg-surface px-6 py-6 text-center shadow-xl"
      >
        <p className="text-vp-base text-ink">{t('viewer.stale')}</p>
        <div className="flex gap-2">
          <button type="button" onClick={() => window.location.reload()} className="vp-control text-vp-sm">
            <RotateCw size={12} />
            {t('viewer.reload')}
          </button>
          <button type="button" onClick={onClose} className="vp-control text-vp-sm">
            <X size={12} />
            {t('preview.close')}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  )
}
