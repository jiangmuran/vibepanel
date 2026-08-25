import { useState } from 'react'
import { CornerDownLeft, Send } from 'lucide-react'

/**
 * A box you type into, that sends when you are done.
 *
 * Typing straight into a terminal is unusable on a phone with an input method:
 * every composition keystroke reaches the shell, so Chinese, Japanese and
 * Korean input produce garbage and even autocorrect fights the line editor.
 * Composing first and sending once is the only way this works.
 */
export function ComposeInput({
  sessionId,
  onSend,
  onPaste,
}: {
  sessionId: string
  onSend: (text: string) => void
  onPaste: (text: string, submit: boolean) => void
}) {
  const [text, setText] = useState('')
  const [newline, setNewline] = useState(true)

  // What is in the box belongs to the session it was typed for.
  //
  // This component is rendered by position, not keyed, so switching session
  // left the text sitting there while onSend quietly re-pointed at the new
  // one: compose a command for alpha, glance at bravo, tap Send, and it runs
  // in bravo. Measured, not theorised — `echo MEANT_FOR_ALPHA` executed in
  // bravo and never reached alpha. In a panel whose whole purpose is a lot of
  // agents at once, delivering a command to the wrong one is the expensive
  // mistake.
  //
  // Keying by session would fix the misdelivery by throwing the draft away,
  // which is the same thing the notes panel used to do to a half-typed note.
  // Keeping one draft per session fixes both: nothing goes to the wrong
  // terminal and nothing you typed disappears when you look away.
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [shownFor, setShownFor] = useState(sessionId)
  if (shownFor !== sessionId) {
    // Adjusting state during render rather than in an effect, so the box never
    // paints one frame holding the previous session's command. `drafts` is
    // still the pre-update value here, which is what the incoming session's
    // draft has to be read from.
    setDrafts((d) => ({ ...d, [shownFor]: text }))
    setShownFor(sessionId)
    setText(drafts[sessionId] ?? '')
  }

  const send = () => {
    if (!text) return
    // A block with line breaks in it is a paste, not typing.
    //
    // Written into the PTY byte by byte it is indistinguishable from someone
    // pressing Enter after every line: a shell runs each one, and an agent
    // acts on the first sentence of a three-line instruction before it has
    // read the third. Measured against a reader that echoes one submission at
    // a time — three lines in, three separate submissions out — on the one
    // control whose stated premise is "composing first and sending once is
    // the only way this works".
    if (text.includes('\n')) {
      onPaste(text, newline)
    } else {
      onSend(newline ? text + '\r' : text)
    }
    setDrafts((d) => {
      const next = { ...d }
      delete next[sessionId]
      return next
    })
    setText('')
  }

  return (
    <div
      data-testid="compose"
      className="flex shrink-0 items-end gap-1 border-t border-hairline px-2 py-1.5 vp-blur"
    >
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          // Enter sends; Shift-Enter is a newline inside the message, for the
          // rare multi-line paste.
          //
          // isComposing is the whole reason this box exists, applied to the box
          // itself. With an input method, Enter is how a candidate is chosen —
          // Chromium reports that keypress as key "Enter" with isComposing set
          // — so without this guard, picking the first word of a Chinese
          // sentence sends it. The component built to keep an IME away from the
          // terminal was firing on the IME's own confirm key.
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault()
            send()
          }
        }}
        rows={1}
        placeholder="Type a command…"
        data-testid="compose-input"
        // The terminal is a monospace grid; what you are about to send should
        // look like what will arrive.
        className="max-h-24 min-h-8 flex-1 resize-none rounded-vp border border-hairline bg-surface px-2 py-1.5 font-mono text-[13px] text-ink outline-none placeholder:font-sans placeholder:text-ink-2 focus:border-accent"
      />
      <button
        type="button"
        onClick={() => setNewline((v) => !v)}
        title={newline ? 'Sends with Enter' : 'Sends without Enter'}
        data-testid="compose-newline"
        data-on={newline ? 'true' : 'false'}
        className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-vp border border-hairline transition-colors duration-150 ease-vp ${
          newline ? 'text-accent' : 'text-ink-2'
        }`}
      >
        <CornerDownLeft size={13} />
      </button>
      <button
        type="button"
        onClick={send}
        disabled={!text}
        title="Send"
        data-testid="compose-send"
        className="flex h-8 w-9 shrink-0 items-center justify-center rounded-vp transition-opacity duration-150 ease-vp disabled:opacity-40"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        <Send size={13} />
      </button>
    </div>
  )
}
