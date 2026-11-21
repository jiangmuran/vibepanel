import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, HelpCircle } from 'lucide-react'

import { answerAsk, currentAsk, subscribeAsk, type AskRequest } from './ask'
import { useDialogFocus } from './useDialogFocus'
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
  //
  // In a portal on <body>, mounted when the question is asked, so it is the
  // last thing there and over every dialog already open. In the tree it was
  // over the settings, but the file viewer is a portal on <body> at the same
  // z-index, after the app's root, and a question asked from it would have
  // opened underneath it.
  return createPortal(<AskPanel key={pending.id} request={pending.request} />, document.body)
}

function AskPanel({ request }: { request: AskRequest }) {
  const [value, setValue] = useState(request.field?.value ?? '')
  const cancelRef = useRef<HTMLButtonElement | null>(null)
  const fieldRef = useRef<HTMLInputElement | null>(null)
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useDialogFocus(dialogRef, () => answerAsk(null))

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
      // Escape is useDialogFocus's: it answers only for the topmost dialog,
      // so a confirmation raised over Settings does not close both.
      if (e.key !== 'Enter') return
      // A destructive question: Enter presses whichever button has the focus,
      // as it does everywhere else, and the focus starts on Cancel.
      //
      // It used to confirm from anywhere, Cancel included -- preventDefault
      // swallowed the focused button's own click so that "focus starts safe"
      // and "Enter confirms" would not contradict each other. Together they
      // made Enter on a highlighted Cancel kill the session, in all three
      // engines; the audit tabbed to Cancel, pressed Enter, and lost one.
      // Focus starting safe only means something if Enter follows it.
      //
      // The one exception is the field: typing into it and pressing Enter is
      // answering, as in a form.
      if (request.destructive && e.target !== fieldRef.current) return
      // Not destructive (naming a passkey, say): Enter answers from anywhere,
      // which is what window.prompt did.
      e.preventDefault()
      answerAsk(value)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // Re-registered on every keystroke in the field, deliberately. The
    // alternative is a ref holding the value, which is a second copy of one
    // piece of state kept in step by hand -- and the failure it produces is
    // Enter confirming the name as it was one character ago.
  }, [value, request.destructive])

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
        ref={dialogRef}
        role={request.destructive ? 'alertdialog' : 'dialog'}
        aria-modal="true"
        aria-labelledby="vp-confirm-title"
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
            <p id="vp-confirm-title" data-testid="confirm-title" className="text-vp-md font-medium text-ink">
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
            className="vp-outline h-8 flex-[2] text-vp-base"
          >
            {safeText(request.cancel)}
          </button>
          <button
            type="button"
            onClick={() => answerAsk(value)}
            data-testid="confirm-yes"
            className="vp-primary h-8 flex-1 text-vp-base"
            style={
              request.destructive
                ? { background: '#c92a2a', color: '#ffffff' }
                : undefined
            }
          >
            {safeText(request.confirm)}
          </button>
        </div>
      </div>
    </div>
  )
}
