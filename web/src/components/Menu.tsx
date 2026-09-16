import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { MoreHorizontal } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { t } from '../i18n'

/**
 * The rest of the actions, behind one button.
 *
 * A row of seven identical ghost icons is the shape this exists to remove. A
 * page's card had open, publish, manage, export, versions, fork and delete all
 * at the same weight and the same size, so nothing on it said which one you
 * press every day and which one you press twice a year — and on a phone the
 * seven of them wrapped onto a line of their own under the page's name.
 *
 * What stays outside is what somebody does often; what comes in here is the
 * rest, named in words rather than by a glyph you have to hover to read.
 *
 * Through a portal, positioned from the trigger's rectangle, for two reasons
 * found by drawing it the short way first: the card that holds the trigger
 * rounds its corners with `overflow-hidden`, which clips an absolutely
 * positioned child, and the page scrolls under it, so a menu in the flow
 * scrolls with the content while the trigger does not. It closes on a scroll
 * or a resize rather than following, because a menu that chases its button
 * across the screen is worse than one that goes away.
 */

export interface MenuItem {
  /** What it says. Already translated: this component adds no prose. */
  label: string
  icon?: LucideIcon
  testid?: string
  /** Red, for the ones that end something. */
  destructive?: boolean
  disabled?: boolean
  /**
   * A link rather than an action, for the one item that is a download.
   *
   * `<a download>` is what makes the browser save the file instead of
   * navigating to it, and a button cannot say that: driving it from script
   * means building an anchor anyway, in a click handler, where a popup
   * blocker is entitled to refuse it.
   */
  href?: string
  onSelect?: () => void
}

/** Everything the arrows walk: the download is an anchor, not a button. */
const ITEMS = 'button:not(:disabled), a'

interface At {
  top: number
  right: number
}

export function Menu({ items, label, testid }: { items: MenuItem[]; label?: string; testid?: string }) {
  const [at, setAt] = useState<At | null>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLDivElement>(null)

  const close = useCallback(() => {
    setAt(null)
    // Back to the button, so a keyboard is where it started rather than at the
    // top of the document.
    trigger.current?.focus()
  }, [])

  const open = () => {
    const box = trigger.current?.getBoundingClientRect()
    if (!box) return
    setAt({ top: box.bottom + 4, right: Math.max(8, window.innerWidth - box.right) })
  }

  // The first item takes the keyboard the moment it is there, so Enter on the
  // button and then Enter again runs the first action without a hunt.
  useLayoutEffect(() => {
    if (at) list.current?.querySelector<HTMLElement>(ITEMS)?.focus()
  }, [at])

  useEffect(() => {
    if (!at) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        // Not past this: the menu is what Escape means while it is open, and
        // a dialog underneath must not close with it.
        e.stopPropagation()
        close()
        return
      }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
      e.preventDefault()
      const buttons = [...(list.current?.querySelectorAll<HTMLElement>(ITEMS) ?? [])]
      const here = buttons.indexOf(document.activeElement as HTMLElement)
      const next = e.key === 'ArrowDown' ? here + 1 : here - 1
      buttons[(next + buttons.length) % buttons.length]?.focus()
    }
    const onDown = (e: PointerEvent) => {
      const el = e.target as Node
      if (list.current?.contains(el) || trigger.current?.contains(el)) return
      setAt(null)
    }
    const away = () => setAt(null)
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('pointerdown', onDown, true)
    // Capture, so a scroll inside any container counts and not only the window's.
    window.addEventListener('scroll', away, true)
    window.addEventListener('resize', away)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('pointerdown', onDown, true)
      window.removeEventListener('scroll', away, true)
      window.removeEventListener('resize', away)
    }
  }, [at, close])

  return (
    <>
      <button
        ref={trigger}
        type="button"
        aria-haspopup="menu"
        aria-expanded={at !== null}
        title={label ?? t('menu.more')}
        aria-label={label ?? t('menu.more')}
        data-testid={testid}
        onClick={() => (at ? setAt(null) : open())}
        className="vp-control"
      >
        <MoreHorizontal size={14} />
      </button>
      {at !== null &&
        createPortal(
          <div
            ref={list}
            role="menu"
            data-testid={testid ? `${testid}-list` : undefined}
            style={{ top: at.top, right: at.right }}
            className="vp-panel-in fixed z-50 min-w-44 rounded-vp border border-hairline bg-surface p-1 shadow-xl"
          >
            {items.map((item) => {
              const Icon = item.icon
              // Full width and left aligned: a menu is a list of sentences, not
              // a strip of controls. The destructive one is red in its text
              // rather than in its ground, so it does not read as the one to
              // press.
              const look =
                'vp-press flex w-full items-center gap-2 rounded-vp px-2 py-1.5 text-left text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink focus-visible:bg-surface-2 focus-visible:text-ink disabled:opacity-40'
              const tint = item.destructive ? { color: 'var(--vp-state-crashed)' } : undefined
              const inside = (
                <>
                  {Icon && <Icon size={13} className="shrink-0 opacity-80" />}
                  <span className="min-w-0 truncate">{item.label}</span>
                </>
              )
              return item.href !== undefined ? (
                <a
                  key={item.label}
                  role="menuitem"
                  href={item.href}
                  download
                  data-testid={item.testid}
                  onClick={() => setAt(null)}
                  className={look}
                  style={tint}
                >
                  {inside}
                </a>
              ) : (
                <button
                  key={item.label}
                  type="button"
                  role="menuitem"
                  disabled={item.disabled}
                  data-testid={item.testid}
                  onClick={() => {
                    setAt(null)
                    item.onSelect?.()
                  }}
                  className={look}
                  style={tint}
                >
                  {inside}
                </button>
              )
            })}
          </div>,
          document.body,
        )}
    </>
  )
}
