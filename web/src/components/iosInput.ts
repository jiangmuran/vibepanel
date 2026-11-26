/**
 * Whether this browser runs on iOS/iPadOS WebKit (including Chrome on iOS/iPadOS
 * and iPadOS Safari in desktop mode, which reports MacIntel with touch points).
 *
 * The keyCode 229 workaround below exists specifically for iOS/iPadOS virtual
 * keyboards and hardware keyboards emitting direct punctuation without a
 * composition session. Running it on desktop Windows/macOS Chrome breaks CJK
 * IMEs, which also emit keyCode 229 with isComposing === false on composition
 * start and on non-text keys (Enter, Backspace, Escape, arrows) that never
 * produce a matching direct input event.
 */
export function isIOSWebKit(
  nav: Pick<Navigator, 'userAgent' | 'platform' | 'maxTouchPoints'> | undefined =
    typeof navigator !== 'undefined' ? navigator : undefined,
): boolean {
  if (!nav) return false
  if (/iPad|iPhone|iPod/.test(nav.userAgent)) return true
  return nav.platform === 'MacIntel' && (nav.maxTouchPoints ?? 0) > 1
}

/**
 * iOS Chrome and Safari use keyCode 229 for direct IME text input such as
 * punctuation and spaces, even though no composition is active. xterm treats
 * that keydown as the start of composition and can then discard the following
 * composed input event. Let the textarea's input event handle it instead.
 */
export function shouldBypassXtermKeydown(event: Pick<KeyboardEvent, 'keyCode' | 'isComposing'>): boolean {
  return event.keyCode === 229 && !event.isComposing
}

/** Bytes for the editing input events iOS can emit after a 229 keydown. */
export function iosInputText(event: Pick<InputEvent, 'inputType' | 'data'>): string | null {
  if (event.inputType === 'insertText' && event.data) return event.data
  if (event.inputType === 'deleteContentBackward') return '\x7f'
  if (event.inputType === 'deleteContentForward') return '\x1b[3~'
  if (event.inputType === 'insertLineBreak') return '\r'
  return null
}
