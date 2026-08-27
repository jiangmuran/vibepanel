import type { ShareWidget } from '../../protocol/wire'

/**
 * What one open dashboard tells the panel about itself, and how a stored board
 * becomes the arrangement this particular screen draws.
 *
 * Both halves are read-only in the direction that matters. There is exactly one
 * board per link, it lives in the link's row, and it is authoritative for every
 * viewer — two screens on the same link show the same thing at the same time,
 * and the way to change either is for the owner to edit the row from a signed-in
 * client. Nothing here writes anything anywhere.
 *
 * A per-viewer local layout was designed and dropped. It would have solved a
 * different problem from the one that exists: the screen this is aimed at is a
 * television with nobody at it, and "let the person at the screen rearrange it"
 * is a feature for a screen with a person at it. Worse, it would have made two
 * walls on one link disagree, which is the opposite of what a link *is*.
 */

/** Where a per-tab id is kept. sessionStorage, so a second tab is a second
 *  screen — which is the question the owner is asking. */
const VIEWER_KEY = 'vibepanel.viewer'

/**
 * This tab's opaque id, for the owner's "how many screens have this open".
 *
 * Hex, sixteen characters, from crypto.getRandomValues. It is not a credential
 * and grants nothing: the server hashes it under the link's own secret and uses
 * it as a map key, so the same tab on two links is two unrelated entries, and
 * an id somebody makes up buys them a viewer that already existed.
 *
 * Every path returns something and none of them throws. A private window, a
 * browser with site data switched off, an embedded webview with storage
 * disabled: all of them still get an id for the life of the page, and the count
 * is right for as long as that page is open, which is as long as it matters.
 */
export function viewerID(): string {
  try {
    const found = sessionStorage.getItem(VIEWER_KEY)
    if (found && /^[0-9a-f]{1,64}$/.test(found)) return found
    const made = randomHex()
    sessionStorage.setItem(VIEWER_KEY, made)
    return made
  } catch {
    return randomHex()
  }
}

function randomHex(): string {
  const bytes = new Uint8Array(8)
  crypto.getRandomValues(bytes)
  return [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('')
}

/**
 * The width below which a widget is given at least half the grid, and the one
 * below which it is given all of it.
 *
 * The same two steps the four-column grid had — four tiles, then two, then one
 * — said as span floors instead of as column counts, because the grid is twelve
 * wide at every size now. A widget the owner made a third of a wall is half a
 * laptop and the whole of a phone, which is what "one stored board opens on
 * both" has always meant.
 */
const HALF_BELOW = 1100
const WHOLE_BELOW = 640

/** How many of the twelve columns this widget actually gets, at this width. */
export function effectiveSpan(span: number, width: number): number {
  const asked = Number.isFinite(span) && span >= 1 ? Math.min(Math.floor(span), 12) : 12
  if (!Number.isFinite(width) || width <= 0) return asked
  if (width < WHOLE_BELOW) return 12
  if (width < HALF_BELOW) return Math.max(asked, 6)
  return asked
}

/**
 * How many rows tall, at this width.
 *
 * A hero is a hero because it is several times the area of the texture around
 * it. On a phone every tile is already the full width, so a tile three rows
 * tall is three screens of one number — the ratio has been paid for by the
 * width and does not need to be paid for again by the height.
 */
export function effectiveHeight(height: number | undefined, width: number): number {
  const asked = Number.isFinite(height) && (height ?? 0) >= 1 ? Math.min(Math.floor(height!), 4) : 1
  if (!Number.isFinite(width) || width <= 0) return asked
  if (width < WHOLE_BELOW) return 1
  if (width < HALF_BELOW) return Math.min(asked, 2)
  return asked
}

/** One widget as this screen draws it. */
export function forViewport(w: ShareWidget, width: number): ShareWidget {
  return {
    ...w,
    span: effectiveSpan(w.span, width),
    height: effectiveHeight(w.height, width),
  }
}
