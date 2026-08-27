import { useSyncExternalStore } from 'react'
import { AlertTriangle, CheckCircle2, Info, X } from 'lucide-react'

import { t, useLang } from '../i18n'
import { safeText } from './text'
import { dismissToast, subscribeToasts, toastsSnapshot, type ToastKind } from './toasts'

/**
 * Shape first, colour second.
 *
 * Red line 4: colour is never the only carrier of meaning. A round tick, a
 * triangle and a circled i are three different silhouettes at a glance and in a
 * greyscale screenshot, and they are the same three shapes the state dot uses
 * for the same reason.
 */
const ICON: Record<ToastKind, typeof Info> = {
  info: Info,
  success: CheckCircle2,
  error: AlertTriangle,
}

const TINT: Record<ToastKind, string> = {
  info: 'var(--vp-accent)',
  success: 'var(--vp-state-done)',
  error: 'var(--vp-state-crashed)',
}

/**
 * Where the panel says what just happened.
 *
 * Two placements, and the phone one is not the desktop one moved.
 *
 * On a wide screen: the bottom-right corner, floating, stacked with the newest
 * nearest the corner because that is where the eye already is.
 *
 * On a phone: a zero-height anchor sitting in the layout immediately above the
 * compose box, with the stack growing upward out of it. The bottom of a phone
 * screen is the compose box, the key bar and -- whenever anybody is typing --
 * the software keyboard, so a fixed offset from the bottom of the window is
 * either on top of the controls or underneath the keyboard, and which one it is
 * depends on hardware the page is not reliably told about. Anchoring to the top
 * edge of that chrome is the same answer without measuring anything: the
 * keyboard pushes the chrome, the chrome carries the anchor, and the toast
 * cannot cover either.
 *
 * Nothing here holds the pointer. The stack is `pointer-events-none` and only
 * the dismiss buttons take it back, so a toast over the terminal never eats the
 * click aimed at what is underneath it.
 */
export function Toasts({ narrow }: { narrow: boolean }) {
  useLang()
  const toasts = useSyncExternalStore(subscribeToasts, toastsSnapshot, toastsSnapshot)
  if (toasts.length === 0) return null

  const stack = (
    <div
      data-testid="toasts"
      className={`pointer-events-none flex flex-col gap-2 ${
        narrow ? 'absolute right-2 bottom-1 left-2' : 'fixed right-4 bottom-4 z-[60] w-80 max-w-[calc(100vw-2rem)]'
      }`}
    >
      {toasts.map((toast) => {
        const Icon = ICON[toast.kind]
        return (
          <div
            key={toast.id}
            data-testid="toast"
            data-toast-kind={toast.kind}
            // role=status is announced when the reader gets to it; an error is
            // the one worth interrupting for.
            role={toast.kind === 'error' ? 'alert' : 'status'}
            className="vp-toast-in pointer-events-auto flex items-start gap-2 rounded-vp border border-hairline vp-solid px-3 py-2 shadow-lg"
          >
            <Icon size={14} className="mt-px shrink-0" style={{ color: TINT[toast.kind] }} />
            <span className="min-w-0 flex-1 text-vp-base leading-relaxed text-ink">
              {/* safeText, not because the dictionary needs it but because the
                  parameters do: a toast about an upload carries a filename, and
                  one about a failure carries whatever the server said. Both are
                  channels a directional override travels down. */}
              {safeText(t(toast.key, toast.params))}
              {toast.detail && (
                <span className="block text-ink-2">{safeText(toast.detail)}</span>
              )}
            </span>
            {toast.count > 1 && (
              <span
                data-testid="toast-count"
                className="tabular shrink-0 rounded-full bg-surface-2 px-1.5 text-vp-xs text-ink-2"
              >
                ×{toast.count}
              </span>
            )}
            <button
              type="button"
              data-testid="toast-dismiss"
              onClick={() => dismissToast(toast.id)}
              aria-label={t('toast.dismiss')}
              title={t('toast.dismiss')}
              className="vp-press -mr-1 shrink-0 rounded-md p-0.5 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              <X size={12} />
            </button>
          </div>
        )
      })}
    </div>
  )

  if (!narrow) return stack
  // h-0, so the stack costs the layout nothing. A toast that took space in the
  // column would resize the terminal under it -- and resizing the terminal
  // means reflowing the grid for every viewer of that session, twice, for a
  // sentence about an upload.
  return (
    <div className="pointer-events-none relative z-[60] h-0 shrink-0">{stack}</div>
  )
}
