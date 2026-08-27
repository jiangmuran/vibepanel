/**
 * The browser half of rendering a page out of a project directory.
 *
 * The whole design is written down in internal/httpapi/preview_render.go, next
 * to the headers that carry most of it. What lives here is the one decision the
 * browser owns — the iframe's `sandbox` attribute — kept in a module of its own
 * so it can be asserted about without a DOM, and so there is exactly one place
 * in the frontend where that string is built.
 *
 * The reason it is worth a file: `sandbox` is a *subtractive* attribute, and
 * the failure mode is silent in the dangerous direction. Removing the attribute
 * gives the frame the panel's origin and its session cookie; adding one token
 * to it does the same. Nothing renders differently, nothing errors, and the
 * preview looks exactly as it did.
 */

import type { Markup } from '../../protocol/api'

/**
 * The token that must never appear here, named so a reader knows what the
 * absence is for.
 *
 * With `allow-same-origin` the framed document is on the panel's origin —
 * `document.cookie` is the session, `window.parent.document` is the console.
 * Together with `allow-scripts` it can also reach up and remove its own sandbox
 * attribute, which is why browsers warn about the pair. The server refuses it
 * too: the effective sandbox is the intersection of this attribute and the
 * response's CSP `sandbox` directive, so neither side can grant it alone.
 */
export const FORBIDDEN_SANDBOX_TOKEN = 'allow-same-origin'

/**
 * The iframe sandbox for a preview.
 *
 * Everything is off. `allow-scripts` is the only token this can ever produce,
 * and only when the reader asked for it on this file, in this dialog.
 *
 * Deliberately absent, each of which looks harmless on its own:
 * `allow-popups` (a preview that opens windows), `allow-modals` (an alert()
 * over the panel from a file an agent wrote), `allow-top-navigation` and its
 * by-user-activation variant (a file that redirects the tab), `allow-downloads`
 * (a preview that starts a save), `allow-forms` (a form that posts somewhere).
 */
export function sandboxFor(scripts: boolean): string {
  return scripts ? 'allow-scripts' : ''
}

/**
 * Whether the panel offers to draw this file at all.
 *
 * The server decides — it sends `X-Preview-Markup` — and this is the check that
 * a value from a newer server is not passed through to an iframe by a build
 * that has no isolation story for it.
 */
export function canRender(markup: string | null): markup is Markup {
  return markup === 'html' || markup === 'svg'
}
