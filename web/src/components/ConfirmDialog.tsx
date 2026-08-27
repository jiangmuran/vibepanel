import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { AlertTriangle, HelpCircle } from 'lucide-react'

import { answerAsk, currentAsk, subscribeAsk, type AskRequest } from './ask'
import { safeText } from './text'

/**
 * The panel's own confirmation, in the panel's own modal language.
 *
 * Same furniture as the directory picker on purpose -- `.vp-backdrop`,
 * `.vp-panel-in`, `.vp-press`, the same radius and hairline -- because two
 * dialogs in one product that arrive differently read as two products.
 */
export function ConfirmDialog() {
  const pending = useSyncExternalStore(subscribeAsk, currentAsk, currentAsk)
  if (!pending) return null
  // Keyed by the request, so the field starts from the value that question
  // asked for rather than from whatever the previous one was left holding.
  return <AskPanel key={pending.id} request={pending.request} />
}

function AskPanel({ request }: { request: AskRequest }) {
  const [value, setValue] = useState(request.field?.value ?? '')
  const cancelRef = useRef<HTMLButtonElement | null>(null)
  const fieldRef = useRef<HTMLInputElement | null>(null)

  /**
   * The keyboard starts on the answer that changes nothing.
   *
   * A dialog that opens with the destructive button focused turns the Enter
   * somebody was already pressing -- to send a command, to accept the last
   * thing -- into the confirmation of a kill. The safe choice is where the
   * focus lands, and the destructive one has to be aimed at: it is also the
   * narrower of the two buttons, for the same reason.
   *
   * A question with a field is not that shape. Nothing is destroyed by naming a
   * passkey, and the field is the only reason the dialog is on screen, so the
   * focus goes there with the suggested name selected -- typing replaces it,
   * which is what `window.prompt` did and the one thing about it worth keeping.
   */
  useEffect(() => {
    if (fieldRef.current) {
      fieldRef.current.focus()
      fieldRef.current.select()
      return
    }
    cancelRef.current?.focus()
  }, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Mid-composition Enter belongs to the input method: it is choosing a
      // candidate, not answering a question. Confirming there would submit a
      // half-typed name and, worse, teach people that Enter is unsafe here.
      if (e.isComposing || e.keyCode === 229) return
      if (e.key === 'Escape') {
        e.preventDefault()
        answerAsk(null)
        return
      }
      if (e.key !== 'Enter') return
      // preventDefault is what lets Enter mean "confirm" while the focus sits
      // on cancel. Enter on a focused button generates a click as its default
      // action; cancelling the default cancels that click, so the two rules --
      // focus starts safe, Enter confirms -- stop contradicting each other.
      e.preventDefault()
      answerAsk(value)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // Re-registered on every keystroke in the field, deliberately. The
    // alternative is a ref holding the value, which is a second copy of one
    // piece of state kept in step by hand -- and the failure it produces is
    // Enter confirming the name as it was one character ago.
  }, [value])

  const Icon = request.destructive ? AlertTriangle : HelpCircle
  const tint = request.destructive ? 'var(--vp-state-crashed)' : 'var(--vp-accent)'

  return (
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={() => answerAsk(null)}
      data-testid="confirm-backdrop"
    >
      <div
        // Read by focusTerminal: while this is on screen the keyboard belongs
        // to it, and a terminal tab is not allowed to take it away.
        data-vp-modal="confirm"
        data-testid="confirm-dialog"
        data-destructive={request.destructive === true}
        onClick={(e) => e.stopPropagation()}
        className="vp-panel-in w-full max-w-sm overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl"
      >
        <div className="flex items-start gap-2.5 p-4">
          {/* The shape says which kind of question this is before the colour
              does. Red line 4 again: at 2am in a dark room the hue is the part
              that does not arrive. */}
          <Icon size={16} className="mt-0.5 shrink-0" style={{ color: tint }} />
          <div className="min-w-0 flex-1">
            <p data-testid="confirm-title" className="text-vp-md font-medium text-ink">
              {safeText(request.title)}
            </p>
            {request.body && (
              <p
                data-testid="confirm-body"
                className="mt-1.5 text-vp-base leading-relaxed text-ink-2"
              >
                {safeText(request.body)}
              </p>
            )}
            {request.field && (
              <label className="mt-3 block">
                <span className="mb-1 block text-vp-sm text-ink-2">
                  {safeText(request.field.label)}
                </span>
                <input
                  ref={fieldRef}
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                  data-testid="confirm-field"
                  className="w-full rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
                />
              </label>
            )}
          </div>
        </div>

        {/* Cancel is the wide one. The button that destroys something is
            deliberately the smaller target and the one further from where the
            thumb rests, because the cost of the two mistakes is not
            symmetrical: cancelling by accident costs a second click. */}
        <div className="flex gap-2 border-t border-hairline p-3">
          <button
            ref={cancelRef}
            type="button"
            onClick={() => answerAsk(null)}
            data-testid="confirm-no"
            className="vp-press flex-[2] rounded-vp border border-hairline px-3 py-2 text-vp-md text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
          >
            {safeText(request.cancel)}
          </button>
          <button
            type="button"
            onClick={() => answerAsk(value)}
            data-testid="confirm-yes"
            className="vp-press flex-1 rounded-vp px-3 py-2 text-vp-md"
            style={
              request.destructive
                ? { background: 'var(--vp-state-crashed)', color: '#fff' }
                : { background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }
            }
          >
            {safeText(request.confirm)}
          </button>
        </div>
      </div>
    </div>
  )
}
