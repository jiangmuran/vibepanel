import { liveTerminals } from './terminals'

/**
 * Whatever is holding the keyboard at the moment the question is asked.
 *
 * A structure rather than a reach into `document`, so the rule below can be
 * tested without a browser: vitest runs these files in node, where there is no
 * document at all and a rule that can only be exercised through one is a rule
 * nothing checks.
 */
export interface KeyboardHolder {
  /** document.activeElement, or a stand-in for it. */
  active?: FocusTarget | null
  /** A modal is on screen: the picker, settings, or a confirmation. */
  modalOpen?: boolean
}

/** The parts of an element this file looks at, and nothing else. */
export interface FocusTarget {
  tagName?: string
  type?: string
  isContentEditable?: boolean
  classList?: { contains(name: string): boolean }
}

/**
 * Types that live in an `<input>` and are not text: pressing a key in one of
 * them does not put a character anywhere, so nothing is interrupted by moving
 * the focus off it.
 */
const NOT_TEXT = new Set([
  'button',
  'checkbox',
  'color',
  'file',
  'hidden',
  'image',
  'radio',
  'range',
  'reset',
  'submit',
])

/**
 * Is this element somewhere a person puts words?
 *
 * The exception in the middle is the whole reason this is a function rather
 * than a tag check. xterm types through a hidden `<textarea>` -- that is how a
 * terminal receives keystrokes at all -- so a naive "is the focus in a
 * textarea" answers yes for a focused terminal, and the feature this exists for
 * is moving focus *between* terminals: click a tab in the strip while the main
 * terminal has the keyboard and the answer would be "somebody is typing, leave
 * them alone", forever. The class is xterm's own and has been stable across
 * major versions; if it ever changes, the symptom is that clicking a terminal
 * tab stops moving the keyboard, which is what focus.test.ts pins.
 */
export function takesTyping(el: FocusTarget | null | undefined): boolean {
  if (!el) return false
  if (el.isContentEditable) return true
  const tag = (el.tagName ?? '').toLowerCase()
  if (tag === 'textarea') return !el.classList?.contains('xterm-helper-textarea')
  if (tag === 'select') return true
  if (tag !== 'input') return false
  return !NOT_TEXT.has((el.type ?? 'text').toLowerCase())
}

/** Is the keyboard already doing something that must not be interrupted? */
export function keyboardIsSpokenFor(holder: KeyboardHolder): boolean {
  // A modal is its own argument: the picker's list, the confirmation's cancel
  // button and the settings dialog are all things the keyboard was handed to
  // on purpose, and none of them is a text field, so the check above would let
  // the focus be pulled out from under them.
  if (holder.modalOpen) return true
  return takesTyping(holder.active)
}

function domHolder(): KeyboardHolder {
  if (typeof document === 'undefined') return {}
  return {
    active: document.activeElement,
    modalOpen: document.querySelector('[data-vp-modal]') !== null,
  }
}

/** How often to look for a terminal that has not mounted yet, and for how long. */
const RETRY_MS = 16
const GIVE_UP_MS = 600

/**
 * Aim the keyboard at a terminal, unless somebody is already typing.
 *
 * The rule, in one sentence: focus moves only when a person asked for a
 * terminal by choosing one -- a session in the sidebar, a tab in the strip, a
 * tab in the side panel -- and only if, at the instant that terminal is ready
 * to take it, nothing else is holding the keyboard.
 *
 * Both halves are load-bearing, and each was arrived at by asking what breaks.
 *
 * The first is why this is called from click handlers and never from an effect
 * on the selected id. An effect fires for every reason the selection changes,
 * and the selection also changes without anybody touching it: `applyState`
 * reselects the first session in the list whenever the current one stops
 * existing, which happens when an agent's process exits somewhere else. Focus
 * jumping out of the notes textarea because a build finished in another project
 * is a worse bug than the one this fixes, and it would be blamed on the
 * keyboard rather than on the panel.
 *
 * The second is why the check happens here rather than in the click handler.
 * The terminal for a session that was just selected does not exist yet: the
 * view is keyed by session id, so choosing another unmounts one xterm and a new
 * one registers itself a frame or two later. That gap is long enough to click
 * into the compose box -- and the activeElement at the moment of the click is
 * the tab that was clicked, not the field the person was typing in a moment
 * before. Asked at the click, the question has the wrong answer twice over.
 *
 * A read-only terminal is never focused. That is the phone: the terminal there
 * is a display, typing arrives through the compose box, and focusing xterm's
 * hidden textarea raises the software keyboard over the thing being read --
 * which is the exact reason `readOnly` exists on the narrow layout.
 */
export function focusTerminal(sessionId: string, holder: () => KeyboardHolder = domHolder) {
  let waited = 0
  const attempt = () => {
    if (keyboardIsSpokenFor(holder())) return
    const term = liveTerminals.get(sessionId)
    if (term) {
      if (!term.options.disableStdin) term.focus()
      return
    }
    waited += RETRY_MS
    if (waited >= GIVE_UP_MS) return
    setTimeout(attempt, RETRY_MS)
  }
  // Never synchronously: the caller is a click handler, and the render that
  // mounts the terminal it is asking for has not happened yet.
  setTimeout(attempt, 0)
}
