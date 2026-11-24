import { useEffect, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'

import { t } from '../../../i18n'
import { CONFIRM_WINDOW_MS, ConfirmPress as Press } from './pressRule'
import type { PressPhase } from './pressRule'

/**
 * A button that ends something on the second press.
 *
 * `act` is the whole thing: the button disarms and re-enables itself when it
 * settles, and showing what went wrong is the caller's, in the row the button
 * is in.
 *
 * Focus stays on the button when it arms, so Enter or Space twice does the
 * same as two clicks. Escape disarms and says so to the dialog it is in
 * (preventDefault), which would otherwise close.
 */
export function ConfirmPress({
  label,
  armedLabel,
  armedShort,
  ariaLabel,
  act,
  disabled = false,
  testid,
  className = '',
}: {
  label: string
  /** Said while armed, in words: the colour is not the message. */
  armedLabel: string
  /** The same for a container too narrow for the long one. */
  armedShort: string
  ariaLabel: string
  act: () => Promise<unknown>
  disabled?: boolean
  testid: string
  className?: string
}) {
  const [phase, setPhase] = useState<PressPhase>('idle')
  const ref = useRef<HTMLButtonElement>(null)
  // The latest closures without recreating the controller, which owns a timer.
  const latest = useRef(act)
  useEffect(() => {
    latest.current = act
  })
  const press = useRef<Press | null>(null)
  useEffect(() => {
    const p = new Press(() => latest.current(), setPhase)
    press.current = p
    return () => {
      p.dispose()
      press.current = null
    }
  }, [])

  // A press anywhere else disarms. Safari does not focus a button on click, so
  // the blur below is not enough there.
  useEffect(() => {
    if (phase !== 'armed') return
    const away = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) press.current?.cancel()
    }
    document.addEventListener('pointerdown', away, true)
    return () => document.removeEventListener('pointerdown', away, true)
  }, [phase])

  const armed = phase === 'armed'
  const running = phase === 'running'
  return (
    <>
      <button
        ref={ref}
        type="button"
        data-testid={testid}
        data-phase={phase}
        disabled={disabled || running}
        aria-label={armed ? `${armedLabel}: ${ariaLabel}` : ariaLabel}
        onClick={() => press.current?.press()}
        onBlur={() => press.current?.cancel()}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && armed) {
            // The dialog behind this closes on Escape; this one only takes the
            // arming back.
            e.preventDefault()
            press.current?.cancel()
          }
        }}
        style={{ ['--vp-confirm-window' as string]: `${CONFIRM_WINDOW_MS}ms` }}
        className={`vp-outline vp-confirm shrink-0 text-vp-sm ${className}`}
      >
        {running ? (
          <>
            <Loader2 size={12} className="animate-spin" aria-hidden="true" />
            {t('res.ending')}
          </>
        ) : armed ? (
          <>
            <span className="@md:hidden">{armedShort}</span>
            <span className="hidden @md:inline">{armedLabel}</span>
          </>
        ) : (
          label
        )}
      </button>
      {/* Heard, not seen: the button's own words change too. */}
      <span role="status" aria-live="polite" className="sr-only">
        {armed ? t('res.confirm.announce') : ''}
      </span>
    </>
  )
}
