/**
 * The viewer's two per-browser switches: wrap and line numbers.
 *
 * Per browser, not per account, for the reason the terminal renderer's switch
 * is (renderer.ts): a narrow phone wants wrap on and a wide monitor may not,
 * and the person is the same. Both default on. Stored only when off, so a
 * browser that never touched them has nothing to read back and a default that
 * changes later reaches it.
 */
export const WRAP_KEY = 'vibepanel.viewer.wrap'
export const NUMBERS_KEY = 'vibepanel.viewer.lineNumbers'

function read(key: string): boolean {
  try {
    return localStorage.getItem(key) !== 'off'
  } catch {
    // Private mode, or storage blocked: the default, every time.
    return true
  }
}

function write(key: string, on: boolean) {
  try {
    if (on) localStorage.removeItem(key)
    else localStorage.setItem(key, 'off')
  } catch {
    /* not remembered; the switch still works for this viewer */
  }
}

export const wrapPreference = () => read(WRAP_KEY)
export const setWrapPreference = (on: boolean) => write(WRAP_KEY, on)
export const lineNumbersPreference = () => read(NUMBERS_KEY)
export const setLineNumbersPreference = (on: boolean) => write(NUMBERS_KEY, on)
