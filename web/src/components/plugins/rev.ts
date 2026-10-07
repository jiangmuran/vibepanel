/**
 * The plugins revision (docs/plugins.md §5a): one integer on every state
 * snapshot that rises when a plugin is installed, enabled, removed or put in
 * dev mode. The list itself is not in the snapshot, because it changes
 * rarely and every viewer receives every snapshot; this is what lets the
 * page notice a change the moment it happens instead of on a timer.
 *
 * Listeners are the plugin list and the theme list; the theme stylesheet is
 * re-requested here, because a theme installed while a tab is open used to
 * arrive only on the next load.
 */

const listeners = new Set<() => void>()
let last: number | null = null

/** Called when a plugin-related cache should re-read: the list, the themes. */
export function onPluginsRev(fn: () => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

/** The one thing of the document this needs, so a test can hand it a stand-in. */
export interface SheetHolder {
  querySelector(selector: string): { setAttribute(name: string, value: string): void } | null
}

/** App hands every snapshot's revision here. The first is a baseline; a change fires the listeners. */
export function notePluginsRev(rev: number, doc: SheetHolder | null = typeof document === 'undefined' ? null : document) {
  if (typeof rev !== 'number' || !Number.isFinite(rev)) return
  const changed = last !== null && rev !== last
  last = rev
  if (!changed) return
  for (const fn of listeners) {
    try {
      fn()
    } catch (e) {
      console.warn('plugins revision listener', e)
    }
  }
  swapThemeSheet(rev, doc)
}

/** Re-request the plugin theme sheet: a new query string is a new request, with the same link in place. */
function swapThemeSheet(rev: number, doc: SheetHolder | null) {
  if (!doc) return
  const link = doc.querySelector('link[rel="stylesheet"][href^="/plugin-themes.css"]')
  if (link) link.setAttribute('href', `/plugin-themes.css?v=${rev}`)
}

/** For tests. */
export function resetPluginsRev() {
  last = null
  listeners.clear()
}
