import { useRef, useState } from 'react'
import { CornerDownLeft, ImagePlus, Send } from 'lucide-react'
import { t, useLang } from '../../i18n'

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
  onPaste,
  onFiles,
}: {
  sessionId: string
  onPaste: (text: string, submit: boolean) => void
  /**
   * Attach files, which on a phone has no other road.
   *
   * Everywhere else an image reaches the panel by being pasted or dropped, and
   * a phone can do neither: there is no drop, and iOS does not deliver an image
   * on the clipboard to a `paste` handler on a page that is not an editable
   * field. The file panel's own chooser exists but is in the side panel, which
   * a narrow layout does not show. So handing an agent a screenshot -- the
   * single most common thing anybody wants to do from a phone -- was possible
   * on a desktop only. 「ipad和手机端无法上传图片」.
   *
   * A file input is what makes it work: iOS answers one with Photo Library,
   * Take Photo and Files, which is every source a screenshot can come from.
   */
  onFiles: (files: File[]) => void
}) {
  useLang()
  const chooser = useRef<HTMLInputElement | null>(null)
  // The last thing actually sent, so a second commit of it is dropped. Cleared
  // on every change to the box. See isNewSubmission.
  const lastSent = useRef('')
  const [text, setText] = useState('')
  const [newline, setNewline] = useState(true)

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
    onPaste(text, newline)
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
        onChange={(e) => {
          // Any edit makes the next submission a new one, including retyping
          // the same words on purpose.
          lastSent.current = ''
          setText(e.target.value)
        }}
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
          // keyCode 229 as well as isComposing, which is what the rest of the
          // panel checks (ConfirmDialog, DirectoryPicker) and this one did not.
          // 229 is the "the IME is handling this" keycode, and the keyboards
          // that report it without setting isComposing are the transcription
          // and dictation ones -- the same family as the double-send above.
          if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            send()
          }
        }}
        rows={1}
        placeholder={t('compose.placeholder')}
        data-testid="compose-input"
        // The terminal is a monospace grid; what you are about to send should
        // look like what will arrive.
        className="max-h-24 min-h-8 flex-1 resize-none rounded-vp border border-hairline bg-surface px-2 py-1.5 font-mono text-vp-md text-ink outline-none placeholder:font-sans placeholder:text-ink-2 focus:border-accent"
      />
      <input
        ref={chooser}
        type="file"
        multiple
        // Not `capture`: that forces the camera and hides the photo library,
        // and the thing being attached is almost always a screenshot that
        // already exists.
        accept="image/*,application/pdf,text/*"
        data-testid="compose-file"
        className="hidden"
        onChange={(e) => {
          onFiles([...(e.target.files ?? [])])
          // Cleared so the same file can be chosen twice running.
          e.target.value = ''
        }}
      />
      <button
        type="button"
        onClick={() => chooser.current?.click()}
        title={t('compose.attach')}
        data-testid="compose-attach"
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-vp border border-hairline text-ink-2 transition-colors duration-150 ease-vp"
      >
        <ImagePlus size={13} />
      </button>
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
        title={t('compose.send')}
        data-testid="compose-send"
        className="flex h-8 w-9 shrink-0 items-center justify-center rounded-vp transition-opacity duration-150 ease-vp disabled:opacity-40"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        <Send size={13} />
      </button>
    </div>
  )
}
