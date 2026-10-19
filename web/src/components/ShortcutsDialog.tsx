import { useRef } from 'react'
import { Keyboard, X } from 'lucide-react'

import { t, useLang } from '../i18n'
import type { Key } from '../i18n'
import { isMac, setSideArrows, useSideArrows } from './keyprefs'
import { useDialogFocus } from './useDialogFocus'

/**
 * The keys, written the way this keyboard labels them: ⌥ and ⌃ on a Mac,
 * Alt and Ctrl everywhere else. A list that says "Alt" to somebody whose key
 * says "option" is a list they have to translate before they can use it.
 */
function keys(mac: boolean) {
  const alt = mac ? '⌥' : 'Alt+'
  const ctrlShift = mac ? '⌃⇧' : 'Ctrl+Shift+'
  return { alt, ctrlShift, shift: mac ? '⇧' : 'Shift+' }
}

interface Line {
  combo: string[]
  what: Key
}

export function shortcutLines(mac: boolean, side: 'terminal' | 'panel'): { title: Key; lines: Line[] }[] {
  const k = keys(mac)
  const sessions: Line[] = [
    { combo: [`${k.alt}↑`, `${k.alt}↓`], what: 'keys.prevNext' },
    { combo: [`${k.alt}1`, '…', `${k.alt}9`], what: 'keys.goto' },
    side === 'panel'
      ? { combo: [`${k.alt}←`, `${k.alt}→`], what: 'keys.projects' }
      : { combo: [`${k.alt}←`, `${k.alt}→`], what: 'keys.wordJump' },
    // ⌃⇧↑ ⌃⇧↓ still work and are left out: one way to do a thing is what a
    // list of keys is for.
  ]
  const move: Line[] = [
    ...(mac ? [{ combo: [`${k.alt}Tab`, `${k.alt}${k.shift}Tab`], what: 'keys.regions' as Key }] : []),
    { combo: [`${k.ctrlShift}←`], what: 'keys.leave' },
  ]
  const list: Line[] = [
    { combo: ['Enter'], what: 'keys.choose' },
    { combo: ['F2'], what: 'keys.rename' },
    { combo: ['↑', '↓'], what: 'keys.moveRow' },
  ]
  const general: Line[] = [
    { combo: [`${k.alt}/`], what: 'keys.help' },
    { combo: ['Esc'], what: 'keys.close' },
  ]
  return [
    { title: 'keys.grpSessions', lines: sessions },
    { title: 'keys.grpMove', lines: move },
    { title: 'keys.grpList', lines: list },
    { title: 'keys.grpGeneral', lines: general },
  ]
}

/** ⌥← ⌥→: the terminal's, or the panel's. Shared by this list and the settings. */
export function SideArrowsChoice() {
  useLang()
  const side = useSideArrows()
  const alt = keys(isMac()).alt
  return (
    <fieldset className="flex flex-col gap-1" data-testid="side-arrows">
      <legend className="mb-1 text-vp-sm text-ink-2">{t('keys.sideLegend', { alt })}</legend>
      {(['terminal', 'panel'] as const).map((v) => (
        <label key={v} className="flex items-center gap-2 text-vp-base text-ink">
          <input
            type="radio"
            name="side-arrows"
            value={v}
            checked={side === v}
            onChange={() => setSideArrows(v)}
            data-testid={`side-arrows-${v}`}
          />
          {t(v === 'terminal' ? 'keys.sideTerminal' : 'keys.sideProjects')}
        </label>
      ))}
    </fieldset>
  )
}

export function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  useLang()
  const ref = useRef<HTMLDivElement>(null)
  useDialogFocus(ref, onClose)
  const mac = isMac()
  const side = useSideArrows()

  return (
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-labelledby="vp-keys-title"
        data-vp-modal="shortcuts"
        data-testid="shortcuts"
        data-platform={mac ? 'mac' : 'other'}
        onClick={(e) => e.stopPropagation()}
        className="vp-panel-in flex max-h-full w-full max-w-lg flex-col overflow-hidden rounded-vp-lg border border-hairline bg-surface shadow-xl"
      >
        <header className="flex items-center gap-2 border-b border-hairline px-5 py-3">
          <Keyboard size={16} aria-hidden className="text-ink-2" />
          <h2 id="vp-keys-title" className="flex-1 text-vp-lg font-semibold tracking-tight text-ink">
            {t('keys.title')}
          </h2>
          <button type="button" onClick={onClose} title={t('keys.close')} data-testid="shortcuts-close" className="vp-control">
            <X size={15} />
          </button>
        </header>
        <div className="overflow-y-auto px-5 py-4">
          {shortcutLines(mac, side).map((g) => (
            <section key={g.title} className="mb-4 last:mb-0">
              <h3 className="mb-1.5 text-vp-sm font-medium text-ink-2">{t(g.title)}</h3>
              <dl className="flex flex-col gap-1.5">
                {g.lines.map((l) => (
                  <div key={l.combo.join('') + l.what} className="flex items-baseline justify-between gap-4">
                    <dt className="text-vp-base text-ink">{t(l.what)}</dt>
                    <dd className="flex shrink-0 items-center gap-1">
                      {l.combo.map((c, i) =>
                        c === '…' ? (
                          <span key={i} className="text-ink-2">…</span>
                        ) : (
                          <kbd
                            key={i}
                            className="rounded-md border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-vp-sm text-ink"
                          >
                            {c}
                          </kbd>
                        ),
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
          <div className="mt-4 border-t border-hairline pt-3">
            <SideArrowsChoice />
          </div>
          <p className="mt-3 text-vp-sm text-ink-2">{t('keys.textFields')}</p>
        </div>
      </div>
    </div>
  )
}
