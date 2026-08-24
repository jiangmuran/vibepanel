import { useEffect, useState } from 'react'
import { Copy } from 'lucide-react'

/**
 * A copy button that appears while text is selected.
 *
 * The selection itself is the browser's: long-press and drag is the gesture
 * people already know, and reimplementing it over a grid of spans would be
 * worse in every way. What the browser does not reliably offer on a page like
 * this is a way to *take* the selection, so that is what this adds.
 */
export function SelectionCopy() {
  const [text, setText] = useState('')
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    const read = () => {
      const selected = window.getSelection()?.toString() ?? ''
      setText(selected.trim() ? selected : '')
      setCopied(false)
    }
    document.addEventListener('selectionchange', read)
    return () => document.removeEventListener('selectionchange', read)
  }, [])

  if (!text) return null

  const chars = text.length

  return (
    <div className="flex shrink-0 items-center gap-2 border-t border-hairline px-3 py-1.5 vp-blur">
      <span className="tabular min-w-0 flex-1 truncate text-[11px] text-ink-2">
        {chars} character{chars === 1 ? '' : 's'} selected
      </span>
      <button
        type="button"
        data-testid="selection-copy"
        onClick={() => {
          void navigator.clipboard
            ?.writeText(text)
            .then(() => setCopied(true))
            .catch(() => setCopied(false))
        }}
        className="flex shrink-0 items-center gap-1 rounded-vp px-3 py-1 text-[12px] font-medium"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        <Copy size={12} />
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}
