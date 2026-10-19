import { useSyncExternalStore } from 'react'

/**
 * What ⌥/Alt + ← and → do, chosen per browser.
 *
 * Both answers are right for somebody. In a shell, and in Claude Code's own
 * prompt, they move the cursor a word at a time, which is what a terminal
 * user's fingers expect; for somebody who lives in the panel's session list
 * they are the obvious pair to ↑ and ↓. So it is a setting, defaulting to the
 * terminal, and kept in this browser: it follows the keyboard in front of
 * somebody, not the account.
 */
export type SideArrows = 'terminal' | 'panel'

const KEY = 'vibepanel.sideArrows'
const listeners = new Set<() => void>()

export function sideArrows(): SideArrows {
  try {
    return localStorage.getItem(KEY) === 'panel' ? 'panel' : 'terminal'
  } catch {
    return 'terminal'
  }
}

export function setSideArrows(v: SideArrows) {
  try {
    localStorage.setItem(KEY, v)
  } catch {
    /* private mode: the default stays */
  }
  for (const fn of listeners) fn()
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

export function useSideArrows(): SideArrows {
  return useSyncExternalStore(subscribe, sideArrows, sideArrows)
}

/**
 * Whether this is a Mac, for the keys only a Mac can have and for drawing ⌥
 * rather than Alt. ⌥Tab is free on a Mac; Alt+Tab on Windows and most Linux
 * desktops belongs to the window manager and never reaches the page.
 */
export function isMac(): boolean {
  if (typeof navigator === 'undefined') return false
  // Either source: Chrome reports "macOS" through userAgentData, Safari has
  // only navigator.platform ("MacIntel", also on an iPad asking for the
  // desktop site, which is where a hardware keyboard is).
  const ua = navigator as Navigator & { userAgentData?: { platform?: string } }
  return [ua.userAgentData?.platform, navigator.platform].some((p) => /mac|iphone|ipad/i.test(p ?? ''))
}

const shortcutListeners = new Set<() => void>()

/** Opens the list of keys, from wherever a button for it lives. */
export function requestShortcuts() {
  for (const fn of shortcutListeners) fn()
}

export function onShortcutsRequest(fn: () => void): () => void {
  shortcutListeners.add(fn)
  return () => {
    shortcutListeners.delete(fn)
  }
}

/** The options panelShortcut needs, as this browser has them now. */
export function shortcutOptions(): { mac: boolean; sideArrows: SideArrows } {
  return { mac: isMac(), sideArrows: sideArrows() }
}
