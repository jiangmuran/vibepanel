import { useEffect, useRef } from 'react'
import { Terminal as TerminalIcon } from 'lucide-react'

import type { LaunchProfile } from '../protocol/wire'
import { envCount, profileLabel } from './profiles'
import { joinArgv } from '../shell'
import { safeText } from './text'
import { t, useLang } from '../i18n'

/**
 * Which profile to start a session with.
 *
 * The button this replaces created a login shell in one click, which was the
 * right default for exactly one kind of user; everybody else clicked it and
 * then typed the name of an agent into the pane. So a two-click list is not
 * slower than what it replaces, and the shell is first in it — the old action
 * is still the top item and nothing is hidden behind a submenu.
 *
 * A dialog rather than a menu anchored to the row, because the sidebar's list
 * scrolls inside `overflow-y-auto` and an anchored popover is clipped by it.
 * The centred panel is also the one shape that works unchanged on a phone,
 * which is where this panel is read.
 *
 * Nothing here is editable. Making a profile is a settings action; the most
 * common control in the panel is not the place to grow a form.
 */
export function LaunchPicker({
  profiles,
  onPick,
  onClose,
}: {
  profiles: LaunchProfile[]
  onPick: (profileId: string) => void
  onClose: () => void
}) {
  useLang()
  const first = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    first.current?.focus()
  }, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      data-testid="launch-backdrop"
    >
      <div
        className="vp-panel-in flex max-h-[85vh] w-full max-w-md flex-col overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl"
        onClick={(e) => e.stopPropagation()}
        data-testid="launch-picker"
        data-vp-modal="launch"
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-4 py-2.5">
          <TerminalIcon size={14} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 text-vp-md font-semibold">{t('profile.pick')}</span>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-2">
          {profiles.map((p, i) => {
            const n = envCount(p)
            return (
              <button
                key={p.id}
                ref={i === 0 ? first : undefined}
                type="button"
                data-testid="launch-option"
                data-profile={p.id}
                onClick={() => onPick(p.id)}
                className="vp-press vp-tap flex w-full items-center gap-2 rounded-vp px-3 py-2 text-left transition-colors duration-150 ease-vp hover:bg-surface-2 focus:bg-surface-2 focus:outline-none"
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-vp-md text-ink">
                    {safeText(profileLabel(p))}
                  </span>
                  {p.command.length > 0 && (
                    <span className="block truncate font-mono text-vp-sm text-ink-2">
                      {safeText(joinArgv(p.command))}
                    </span>
                  )}
                </span>
                {n > 0 && (
                  <span className="tabular shrink-0 text-vp-sm text-ink-3">
                    {n === 1 ? t('profile.envSetOne') : t('profile.envSet', { n })}
                  </span>
                )}
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
