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
