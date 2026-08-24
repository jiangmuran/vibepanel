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
export function ComposeInput({ onSend }: { onSend: (text: string) => void }) {
  const [text, setText] = useState('')
  const [newline, setNewline] = useState(true)

  const send = () => {
    if (!text) return
    onSend(newline ? text + '\r' : text)
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
          if (e.key === 'Enter' && !e.shiftKey) {
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
