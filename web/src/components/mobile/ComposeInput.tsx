import { useRef, useState } from 'react'
import { CornerDownLeft, ImagePlus, Keyboard, Send, WifiOff } from 'lucide-react'
import { t, useLang } from '../../i18n'
import { isEnter } from '../enter'

/**
 * Whether this submission is a new one.
 *
 * `send` reads `text` out of state and clears it with `setText('')`, which does
 * not take effect until React re-renders — so two commits arriving before that
 * both read the same string and both send it. 「语音输入法好像有的时候会粘贴两
 * 遍，尤其是那种可能带转写、自动总结或者转发的」: a keyboard that transcribes
 * and then commits a summary produces exactly that shape, two submissions of
 * the same text with nothing typed in between.
 *
 * Compared by content rather than latched on a timer, because the two arrive
 * some hundreds of milliseconds apart and any timer short enough to be safe is
 * too short to catch them. `last` is cleared whenever the box changes, so
 * sending the same words twice on purpose still works: it requires typing them
 * again, which is a change.
 *
 * A function of two strings so it can be tested. The alternative is a
 * condition inside a component that nothing renders in this suite.
 */
export function isNewSubmission(text: string, last: string): boolean {
  return text !== '' && text !== last
}

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
  disconnected = false,
  onPaste,
  onFiles,
  keysShown,
  onToggleKeys,
}: {
  sessionId: string
  /** Send is off while the panel is not connected; typing a draft is not. */
  disconnected?: boolean
  /** False when nothing was sent, and then the box keeps what was typed. */
  onPaste: (text: string, submit: boolean) => boolean
  onFiles: (files: File[]) => void
  keysShown?: boolean
  onToggleKeys?: () => void
}) {
  useLang()
  const chooser = useRef<HTMLInputElement | null>(null)
  // The last thing actually sent, so a second commit of it is dropped. Cleared
  // on every change to the box. See isNewSubmission.
  const lastSent = useRef('')
  const [text, setText] = useState('')
  const [newline, setNewline] = useState(true)
  // Set when a send did not leave: the words stay in the box and this says
  // why. Cleared by the next edit or the next send that does leave.
  const [unsent, setUnsent] = useState(false)

  // What is in the box belongs to the session it was typed for.
  //
  // This component is rendered by position, not keyed, so switching session
  // left the text sitting there while the send target quietly re-pointed at the new
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
    if (!isNewSubmission(text, lastSent.current)) return
    lastSent.current = text
    // A block with line breaks in it is a paste, not typing.
    //
    // Written into the PTY byte by byte it is indistinguishable from someone
    // pressing Enter after every line: a shell runs each one, and an agent
    // acts on the first sentence of a three-line instruction before it has
    // read the third. Measured against a reader that echoes one submission at
    // a time — three lines in, three separate submissions out — on the one
    // control whose stated premise is "composing first and sending once is
    // the only way this works".
    // Driven by render-check's mobile section now, against a pane that asks
    // for bracketed paste and renders the markers with `cat -v`. Removing this
    // branch fails it with "open=false close=false".
    //
    // It needed its own session, and the first attempt at that check is why:
    // pasting into the scratchpad pane, which runs `sh`, proved nothing.
    // dash never asks for bracketed paste, tmux correctly does not bracket for
    // a pane that never asked, and the promise below is "better rather than
    // airtight" for exactly that reason. What the check pins is the half this
    // file is responsible for -- routing a block with newlines down the paste
    // road instead of typing it.
    // Everything goes down the paste road, not only what has a newline in it.
    //
    // The two roads differ in a way the sender cannot see and the agent can:
    // the paste road writes the text, and then writes the carriage return as a
    // *separate* write, while this branch used to append it and send one.
    // Claude Code and Codex are both Ink applications, and Ink reads a burst
    // that ends in CR as a paste -- so the return became a newline inside the
    // prompt and the message sat there unsent. 「点击发送按钮不会发送消息 只会
    // 换行」, on both agents.
    //
    // So there is one road now. It was two because a single line looked like
    // it did not need the paste machinery, which is true of the bracketing and
    // false of the part that matters.
    if (!onPaste(text, newline)) {
      // Not connected. The box used to clear regardless, and the text was in
      // no pane after the reconnect. Kept, and not queued: whether it still
      // makes sense to send once the link is back is the person's call.
      lastSent.current = ''
      setUnsent(true)
      return
    }
    setUnsent(false)
    setDrafts((d) => {
      const next = { ...d }
      delete next[sessionId]
      return next
    })
    setText('')
  }

  return (
    <div data-testid="compose" className="shrink-0 border-t border-hairline px-2 py-1.5 vp-blur">
      {unsent && (
        <p role="alert" data-testid="compose-unsent" className="mb-1 flex items-center gap-1.5 px-1 text-vp-sm text-ink">
          <WifiOff size={14} aria-hidden className="shrink-0 text-danger-ink" />
          {t('compose.unsent')}
        </p>
      )}
      <div className="flex items-end gap-1 rounded-vp-lg border border-hairline-strong bg-surface px-1.5 py-1">
        <input
          ref={chooser}
          type="file"
          multiple
          accept="image/*,application/pdf,text/*"
          data-testid="compose-file"
          className="hidden"
          onChange={(e) => {
            onFiles([...(e.target.files ?? [])])
            e.target.value = ''
          }}
        />
        <button
          type="button"
          onClick={() => chooser.current?.click()}
          title={t('compose.attach')}
          data-testid="compose-attach"
          className="vp-control shrink-0"
        >
          <ImagePlus size={15} />
        </button>
        <textarea
          value={text}
          onChange={(e) => {
            lastSent.current = ''
            setUnsent(false)
            setText(e.target.value)
          }}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return
            if (isEnter(e) && !e.shiftKey) {
              e.preventDefault()
              send()
            }
          }}
          rows={1}
          placeholder={t('compose.placeholder')}
          data-testid="compose-input"
          className="max-h-24 min-h-8 flex-1 resize-none border-0 bg-transparent px-1.5 py-1 font-mono text-vp-md text-ink outline-none placeholder:font-sans placeholder:text-ink-3"
        />
        <button
          type="button"
          onClick={() => setNewline((v) => !v)}
          title={newline ? t('compose.withEnter') : t('compose.withoutEnter')}
          data-testid="compose-newline"
          data-on={newline ? 'true' : 'false'}
          aria-pressed={newline}
          className="vp-control shrink-0"
        >
          <CornerDownLeft size={14} />
        </button>
        {onToggleKeys && (
          <button
            type="button"
            onClick={onToggleKeys}
            title={t('app.toggleKeys')}
            data-testid="compose-keys-toggle"
            aria-pressed={keysShown}
            className="vp-control shrink-0"
          >
            <Keyboard size={14} />
          </button>
        )}
        <button
          type="button"
          onClick={send}
          disabled={!text || disconnected}
          title={t('compose.send')}
          data-testid="compose-send"
          className="vp-primary w-9 shrink-0 px-0"
        >
          <Send size={14} />
        </button>
      </div>
    </div>
  )
}
