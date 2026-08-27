import { useEffect, useState } from 'react'
import { Copy } from 'lucide-react'

/**
 * A copy button that appears while text is selected.
 *
 * Two sources, because there are two kinds of selection on this page. Over the
 * terminal it is xterm's own, driven by the press-and-hold gesture in
 * touchSelect.ts and handed down through `selection`. Everywhere else — a
 * note, a file path, the audit log — it is the browser's, which works with a
 * finger because those are ordinary text.
 *
 * What neither offers on a phone is a reliable way to *take* what is selected:
 * the platform copy bubble does not appear over a page like this. That is what
 * this adds.
 */
export function SelectionCopy({ selection = '' }: { selection?: string }) {
  const [domText, setDomText] = useState('')
  // What was last copied rather than a boolean: the confirmation has to fall
  // away by itself when the selection moves on, and deriving it from the text
  // avoids an effect that resets a flag on every change.
  const [copiedText, setCopiedText] = useState('')

  useEffect(() => {
    const read = () => {
      const selected = window.getSelection()?.toString() ?? ''
      setDomText(selected.trim() ? selected : '')
    }
    document.addEventListener('selectionchange', read)
    return () => document.removeEventListener('selectionchange', read)
  }, [])

  // The terminal wins when both exist: on a phone the terminal is what fills
  // the screen, so a stale DOM selection behind it must not shadow the thing
  // the user just dragged over.
  const text = selection.trim() ? selection : domText
  const copied = text !== '' && text === copiedText

  if (!text) return null

  const chars = text.length

  return (
    <div
      data-testid="selection-bar"
      className="flex shrink-0 items-center gap-2 border-t border-hairline px-3 py-1.5 vp-blur"
    >
      <span className="tabular min-w-0 flex-1 truncate text-vp-sm text-ink-2">
        {chars} character{chars === 1 ? '' : 's'} selected
      </span>
      <button
        type="button"
        data-testid="selection-copy"
        onClick={() => {
          void navigator.clipboard
            ?.writeText(text)
            .then(() => setCopiedText(text))
            .catch(() => setCopiedText(''))
        }}
        className="flex shrink-0 items-center gap-1 rounded-vp px-3 py-1 text-vp-base font-medium"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        <Copy size={12} />
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}
