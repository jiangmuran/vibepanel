import { useCallback, useEffect, useState } from 'react'
import { Maximize, Minimize } from 'lucide-react'

import { t, useLang } from '../../i18n'

/**
 * Put the board on the whole screen.
 *
 * A share link is opened on a television, a wall panel and a tablet propped on
 * a desk, and on every one of those the browser's own furniture — an address
 * bar, a tab strip, a status bar — is the only thing on screen that is not the
 * board. There was no way to get rid of it: 「没有全屏按钮」.
 *
 * Feature-detected rather than assumed, and hidden when the answer is no. iOS
 * Safari on the *phone* has no element fullscreen at all, and offering a
 * control that does nothing is worse than not offering one — the reader cannot
 * tell a missing feature from a broken button. iPadOS and every desktop browser
 * have it, which is most of what a board is opened on.
 *
 * `webkitRequestFullscreen` is still the spelling on Safari, so both are tried.
 * Written as two lookups rather than a `Element.prototype` patch: the patch
 * version reads cleaner and is a global side effect in a component file.
 */

type FsElement = HTMLElement & {
  webkitRequestFullscreen?: () => Promise<void> | void
}
type FsDocument = Document & {
  webkitExitFullscreen?: () => Promise<void> | void
  webkitFullscreenElement?: Element | null
}

/** Whether this browser can put an element on the whole screen. */
export function fullscreenSupported(doc: Document = document): boolean {
  const d = doc as FsDocument
  const el = doc.documentElement as FsElement
  return Boolean(
    (doc.fullscreenEnabled || typeof d.webkitExitFullscreen === 'function') &&
      (typeof el.requestFullscreen === 'function' ||
        typeof el.webkitRequestFullscreen === 'function'),
  )
}

/** Whether something is currently on the whole screen. */
function isFullscreen(doc: Document = document): boolean {
  const d = doc as FsDocument
  return Boolean(doc.fullscreenElement ?? d.webkitFullscreenElement)
}

export function FullscreenButton() {
  useLang()
  const [supported] = useState(() => fullscreenSupported())
  const [on, setOn] = useState(() => isFullscreen())

  useEffect(() => {
    // Both events, and both matter: leaving fullscreen with Escape or the
    // system gesture never goes through this button, and a control still
    // showing "exit" after the user already left is a control that lies.
    const sync = () => setOn(isFullscreen())
    document.addEventListener('fullscreenchange', sync)
    document.addEventListener('webkitfullscreenchange', sync)
    return () => {
      document.removeEventListener('fullscreenchange', sync)
      document.removeEventListener('webkitfullscreenchange', sync)
    }
  }, [])

  const toggle = useCallback(() => {
    const d = document as FsDocument
    const el = document.documentElement as FsElement
    // Rejections are swallowed on purpose. The promise rejects when the request
    // did not come from a user gesture or the browser simply refuses, and
    // neither is something to tell somebody standing in front of a wall
    // display about -- the button not working is the whole message.
    if (isFullscreen()) {
      void Promise.resolve(document.exitFullscreen?.() ?? d.webkitExitFullscreen?.()).catch(
        () => {},
      )
      return
    }
    void Promise.resolve(el.requestFullscreen?.() ?? el.webkitRequestFullscreen?.()).catch(() => {})
  }, [])

  if (!supported) return null

  return (
    <button
      type="button"
      onClick={toggle}
      data-testid="dash-fullscreen"
      aria-pressed={on}
      title={on ? t('dash.exitFullscreen') : t('dash.fullscreen')}
      // `.vp-tap` for the 44px floor under a coarse pointer: this is a control
      // on a tablet propped on a desk as often as one on a laptop.
      className="vp-control vp-tap shrink-0 text-ink-2"
    >
      {on ? <Minimize size={16} /> : <Maximize size={16} />}
      <span className="sr-only">{on ? t('dash.exitFullscreen') : t('dash.fullscreen')}</span>
    </button>
  )
}
