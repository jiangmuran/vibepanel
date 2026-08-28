/**
 * Putting text on the device clipboard, from wherever the panel is served.
 *
 * `navigator.clipboard` does not exist on a non-secure origin, and a
 * self-hosted panel on a LAN address over plain http is exactly that. This was
 * written three times in three files with three different answers, and the copy
 * path that mattered most had the wrong one:
 *
 *     navigator.clipboard?.writeText(text).catch(fallback)
 *
 * Optional chaining short-circuits the *whole* chain, so on plain http that
 * expression is `undefined` and `.catch` is never reached. Selecting text put
 * nothing on the clipboard and offered nothing either — silently, which is the
 * reported 「我明明复制上了 但是没有出现在设备剪切板里面」.
 *
 * `execCommand('copy')` is the fallback rather than a preference: it is
 * deprecated, it needs a user gesture, and it is the only thing that works on
 * an insecure origin. Callers inside a gesture (a pointerup ending a
 * selection, a click) can use it; callers outside one — text arriving from
 * OSC 52 while nobody is touching the page — cannot, and get `false` so they
 * can offer a button whose click supplies the gesture.
 */

/** Writes without needing a gesture. Resolves false when it could not. */
export async function copyText(text: string): Promise<boolean> {
  // No `if (!navigator.clipboard)` above this. It was there, and mutation
  // testing showed it was doing nothing: reaching through an undefined
  // clipboard throws, the throw lands here, and the answer is the same false.
  // A branch whose removal changes no behaviour is a branch that reads as a
  // guard without being one.
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

/**
 * The same, from inside a user gesture, where the deprecated path is allowed.
 *
 * Synchronous on purpose: `execCommand` must run in the same task as the
 * gesture that authorised it, and awaiting `navigator.clipboard` first would
 * spend that authorisation before the fallback could use it. So the modern API
 * is started and not waited for, and the legacy one runs when it is absent.
 */
export function copyTextInGesture(text: string, onResult?: (ok: boolean) => void): void {
  const clip = navigator.clipboard
  if (clip) {
    void clip.writeText(text).then(
      () => onResult?.(true),
      () => onResult?.(legacyCopy(text)),
    )
    return
  }
  onResult?.(legacyCopy(text))
}

function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  // Off-screen rather than hidden: `display:none` and `visibility:hidden` are
  // both unselectable, and an unselectable textarea copies an empty string.
  ta.style.position = 'fixed'
  ta.style.top = '-1000px'
  ta.style.opacity = '0'
  ta.setAttribute('readonly', '')
  document.body.appendChild(ta)
  try {
    ta.select()
    ta.setSelectionRange(0, text.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    ta.remove()
  }
}
