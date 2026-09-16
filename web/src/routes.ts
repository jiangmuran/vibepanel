/**
 * What the address bar decides, and nothing else does.
 *
 * The panel is one bundle, and this module says which root it builds. Two of
 * them, and they are not one component with pieces hidden:
 *
 *   - **panel** — the console, behind AuthGate.
 *   - **sharing** — the page share pages and their links are made and edited
 *     from. Behind the same AuthGate as the panel, on the same cookie (its
 *     path is `/`, see internal/auth/session.go), so opening it in a second
 *     tab or from a bookmark asks nothing of somebody already signed in. It
 *     has its own address because the list wants the whole window and because
 *     the person arranging a wall is on a laptop with the panel open somewhere
 *     else; it left the settings dialog for that reason.
 *
 * `/share/<token>` is not here. The server answers it with the page the link
 * draws, or with a static page saying the link no longer works, so a stranger
 * holding a share address never reaches this bundle (red line 8).
 *
 * The paths are exported so the links that point at them import the name. A
 * literal `'/sharing'` written in a component is a link that quietly stops
 * working the day the route moves, and `routes.test.ts` refuses one.
 */

export const PANEL_PATH = '/'

export const SHARING_PATH = '/sharing'

/** The page the chat bridge is set up from: routes.ts is the one place that spells it. */
export const CHAT_PATH = '/chat'

export type Route = { kind: 'panel' } | { kind: 'sharing' } | { kind: 'chat' }

export function routeFor(pathname: string): Route {
  // With or without a trailing slash, because both arrive: a bookmark keeps
  // whatever was typed, and a proxy may add one.
  if (pathname === SHARING_PATH || pathname === `${SHARING_PATH}/`) return { kind: 'sharing' }
  if (pathname === CHAT_PATH || pathname === `${CHAT_PATH}/`) return { kind: 'chat' }
  return { kind: 'panel' }
}

/**
 * The one thing the sharing page asks the panel to do: open a share page as
 * its project, with the Preview beside it and an agent in it.
 *
 * That needs the console — a terminal, the launch picker, the side panel —
 * and the sharing page has none of those on purpose. So it hands over by
 * address: it navigates to the panel with the page named on the query string,
 * and the panel opens it once its first snapshot has arrived, then takes the
 * query back off the address bar so a reload does not open it twice.
 *
 * A query rather than storage, because the address is the only channel both
 * roots already share, and because what it carries is a page id the person
 * is entitled to open anyway: the panel checks that, not the URL.
 */
export const OPEN_PAGE_PARAM = 'page'

export const FRESH_PARAM = 'fresh'

export function panelOpeningPage(pageId: string, fresh: boolean): string {
  const q = new URLSearchParams({ [OPEN_PAGE_PARAM]: pageId })
  if (fresh) q.set(FRESH_PARAM, '1')
  return `${PANEL_PATH}?${q.toString()}`
}

export function pageToOpen(search: string): { id: string; fresh: boolean } | null {
  const q = new URLSearchParams(search)
  const id = q.get(OPEN_PAGE_PARAM)
  // Ids are the panel's own (internal/id): base64url, and never empty. A
  // query somebody typed by hand falls through to a panel that opens nothing
  // rather than to a request the server records as refused.
  if (!id || !/^[A-Za-z0-9_-]+$/.test(id)) return null
  return { id, fresh: q.get(FRESH_PARAM) === '1' }
}

export const OPEN_SESSION_PARAM = 'session'

/**
 * The address a chat card carries: the panel, opened at one session. Built
 * on the server (the bridge's sessionURL) and read here, so the two agree on
 * the parameter's name through this one constant and its test.
 */
export function panelOpeningSession(sessionId: string): string {
  const q = new URLSearchParams({ [OPEN_SESSION_PARAM]: sessionId })
  return `${PANEL_PATH}?${q.toString()}`
}

export function sessionToOpen(search: string): string | null {
  const id = new URLSearchParams(search).get(OPEN_SESSION_PARAM)
  if (!id || !/^[A-Za-z0-9_-]+$/.test(id)) return null
  return id
}
