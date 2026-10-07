import { useEffect, useState } from 'react'
import { X } from 'lucide-react'

import { t, useLang } from '../../i18n'
import type { PluginDetail, PluginLine } from '../../protocol/wire'
import { safeText } from '../text'
import { canConfirm, capOf, capsToSend, grouped, initialTicks, textIn } from './screen'
import type { Heading } from './screen'

/**
 * The install screen: docs/plugins.md §6, drawn from what the server said.
 *
 * Every line here is one `plugins.Describe` produced; this component adds
 * headings and boxes and nothing else. The confirm button's words come with
 * the screen, because the verb is what a person reads at the moment of
 * deciding, and "Run this as you" is a different decision from "Install".
 *
 * A box the owner unticks is a capability the plugin does not get. The
 * plugin sees what was granted (`vp.caps`) and degrades; it does not ask.
 */
const HEADING_KEY: Record<Heading, 'plg.screen.what' | 'plg.screen.rungs' | 'plg.screen.may' | 'plg.screen.reach' | 'plg.screen.runs' | 'plg.screen.keeps' | null> = {
  what: 'plg.screen.what',
  rung: 'plg.screen.rungs',
  cap: 'plg.screen.may',
  host: 'plg.screen.reach',
  runs: 'plg.screen.runs',
  keeps: 'plg.screen.keeps',
  danger: null,
}

const TONE_COLOR: Record<PluginLine['tone'], string> = {
  plain: 'var(--vp-ink)',
  amber: 'var(--vp-state-waiting)',
  red: 'var(--vp-state-crashed)',
  enforced: 'var(--vp-state-done)',
}

export function InstallScreen({
  plugin,
  onConfirm,
  onClose,
}: {
  plugin: PluginDetail
  /** The boxes as ticked. Resolves when the server has answered. */
  onConfirm: (caps: string[]) => Promise<void>
  onClose: () => void
}) {
  const lang = useLang()
  const screen = plugin.screen
  const [ticks, setTicks] = useState<Set<string>>(() => initialTicks(screen))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const pressConfirm = async () => {
    setBusy(true)
    setError('')
    try {
      await onConfirm(capsToSend(screen, ticks))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  const name = textIn(plugin.name, lang)
  const confirmKey = `plg.confirm.${screen.confirm}` as const
  const danger = screen.confirm === 'run'

  return (
    <div className="vp-backdrop absolute inset-0 z-30 flex items-start justify-center overflow-y-auto bg-black/40 px-4 py-8">
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t('plg.screen.title', { name, version: plugin.version })}
        data-testid="plugin-install-screen"
        data-vp-modal="plugin-install"
        className="vp-panel-in flex max-h-full w-full max-w-xl flex-col rounded-vp-lg border border-hairline bg-surface shadow-xl"
      >
        <div className="flex items-center gap-2 border-b border-hairline px-5 py-3">
          <h2 className="mr-auto min-w-0 truncate text-vp-lg font-semibold tracking-tight text-ink">
            {t('plg.screen.title', { name: safeText(name), version: plugin.version })}
          </h2>
          <button type="button" onClick={onClose} title={t('plg.close')} data-testid="plugin-screen-close" className="vp-control">
            <X size={15} />
          </button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
          {screen.refused && (
            <p className="rounded-vp border px-3 py-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)', borderColor: 'var(--vp-state-crashed)' }} data-testid="plugin-screen-refused">
              {safeText(textIn(screen.refused.text, lang))}
            </p>
          )}

          {grouped(screen).map(({ heading, lines }) => (
            <section key={heading} data-testid={`plugin-screen-${heading}`}>
              {HEADING_KEY[heading] && (
                <h3 className="mb-1.5 text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">{t(HEADING_KEY[heading])}</h3>
              )}
              {heading === 'cap' && <p className="mb-1.5 text-vp-sm text-ink-3">{t('plg.screen.untick')}</p>}
              <ul className="grid gap-1.5">
                {lines.map((l, i) => {
                  const cap = capOf(l)
                  const text = safeText(textIn(l.text, lang))
                  if (heading === 'danger') {
                    return (
                      <li key={i} className="rounded-vp border-l-4 px-3 py-2 text-vp-base leading-relaxed" style={{ borderColor: 'var(--vp-state-crashed)', background: 'color-mix(in srgb, var(--vp-state-crashed) 10%, transparent)' }} data-testid="plugin-screen-danger-text">
                        {text}
                      </li>
                    )
                  }
                  if (heading === 'runs') {
                    return (
                      <li key={i} className="text-vp-base">
                        <p className="text-ink-2">{text}</p>
                        <pre className="mt-1 overflow-x-auto rounded-vp bg-surface-2 px-2 py-1.5 font-mono text-vp-sm text-ink">{safeText(l.code ?? '')}</pre>
                      </li>
                    )
                  }
                  return (
                    <li key={i} className="flex items-start gap-2 text-vp-base" data-code={l.code}>
                      {cap && l.code ? (
                        <input
                          type="checkbox"
                          id={`plugin-cap-${l.code}`}
                          data-testid={`plugin-cap-${cap}`}
                          checked={ticks.has(l.code)}
                          onChange={(e) => {
                            const next = new Set(ticks)
                            if (e.target.checked) next.add(l.code!)
                            else next.delete(l.code!)
                            setTicks(next)
                          }}
                          className="mt-1 shrink-0 accent-[var(--vp-accent)]"
                        />
                      ) : (
                        <span className="mt-2 h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: TONE_COLOR[l.tone] }} aria-hidden="true" />
                      )}
                      <label htmlFor={cap && l.code ? `plugin-cap-${l.code}` : undefined} className="min-w-0 leading-relaxed" style={{ color: TONE_COLOR[l.tone], fontWeight: l.tone === 'red' ? 600 : 400 }}>
                        {text}
                        {l.code && heading !== 'what' && (
                          <code className="ml-1.5 rounded-md bg-surface-2 px-1 font-mono text-vp-xs text-ink-3">{safeText(l.code)}</code>
                        )}
                      </label>
                    </li>
                  )
                })}
              </ul>
            </section>
          ))}

          {error && (
            <p className="text-vp-base" style={{ color: 'var(--vp-state-crashed)' }} data-testid="plugin-screen-error">
              {safeText(error)}
            </p>
          )}
        </div>

        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-hairline px-5 py-3">
          <button type="button" onClick={onClose} className="vp-outline text-vp-base" data-testid="plugin-screen-cancel">
            {t('ask.cancel')}
          </button>
          <button
            type="button"
            disabled={!canConfirm(screen, busy)}
            onClick={() => void pressConfirm()}
            data-testid="plugin-screen-confirm"
            className="vp-press rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-40"
            style={{ background: danger ? 'var(--vp-state-crashed)' : 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t(confirmKey)}
          </button>
        </div>
      </div>
    </div>
  )
}
