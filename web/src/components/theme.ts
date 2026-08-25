import type { ITheme } from '@xterm/xterm'

/**
 * Builds the xterm palette from the CSS tokens.
 *
 * Reading the computed values rather than hard-coding a second palette is what
 * keeps the terminal and the interface the same colour system. A duplicated
 * palette drifts, and the terminal is the largest surface on screen — it is the
 * first place the drift shows.
 */
export function terminalTheme(): ITheme {
  const s = getComputedStyle(document.documentElement)
  const v = (name: string) => s.getPropertyValue(name).trim()
  return {
    background: v('--vp-terminal-bg'),
    foreground: v('--vp-term-fg'),
    cursor: v('--vp-term-cursor'),
    cursorAccent: v('--vp-terminal-bg'),
    selectionBackground: v('--vp-selection'),
    black: v('--vp-term-black'),
    red: v('--vp-term-red'),
    green: v('--vp-term-green'),
    yellow: v('--vp-term-yellow'),
    blue: v('--vp-term-blue'),
    magenta: v('--vp-term-magenta'),
    cyan: v('--vp-term-cyan'),
    white: v('--vp-term-white'),
    brightBlack: v('--vp-term-bright-black'),
    brightRed: v('--vp-term-bright-red'),
    brightGreen: v('--vp-term-bright-green'),
    brightYellow: v('--vp-term-bright-yellow'),
    brightBlue: v('--vp-term-bright-blue'),
    brightMagenta: v('--vp-term-bright-magenta'),
    brightCyan: v('--vp-term-bright-cyan'),
    brightWhite: v('--vp-term-bright-white'),
  }
}

export type ThemeChoice = 'system' | 'light' | 'dark'

/**
 * Where the theme choice is remembered.
 *
 * Exported because index.html has to read the same key from an inline script
 * before first paint, and there is otherwise nothing tying the two spellings
 * together. Drifting apart does not merely bring back the flash of the wrong
 * palette: the pre-paint script would find nothing, and nothing else applies
 * the stored choice — `applyTheme` runs only when somebody uses the toggle —
 * so the whole session would follow the system preference while the toggle
 * showed the choice it was ignoring.
 */
export const STORAGE_KEY = 'vibepanel.theme'

export function loadTheme(): ThemeChoice {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    if (v === 'light' || v === 'dark' || v === 'system') return v
  } catch {
    /* private mode */
  }
  return 'system'
}

export function applyTheme(choice: ThemeChoice) {
  const root = document.documentElement
  if (choice === 'system') {
    delete root.dataset.theme
  } else {
    root.dataset.theme = choice
  }
  try {
    localStorage.setItem(STORAGE_KEY, choice)
  } catch {
    /* private mode: the choice simply does not persist */
  }
}
