import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { MoreHorizontal } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { t } from '../i18n'
import { safeText } from './text'

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
 * Through a portal, because the page scrolls under the trigger: a menu in the
 * flow scrolls away from the button it belongs to. That means it has to be
 * placed by hand, and the placement is the part that was wrong first — see
 * `place` and `follow` below.
 */

interface Common {
  /** What it says. Already translated: this component adds no prose. */
  label: string
  icon?: LucideIcon
  testid?: string
  /** Red, for the ones that end something. */
  destructive?: boolean
}

/**
 * An item is an action or a link, never both.
 *
 * A union rather than optional fields, because the first version allowed
 * `{href, disabled}` and silently ignored the `disabled`: the anchor branch
 * had nowhere to put it. A shape that cannot say that is a shape nobody has
 * to remember.
 */
export type MenuItem =
  | (Common & {
      onSelect: () => void
      disabled?: boolean
      /** On for a toggle, so a reader is told which way it is set. */
      checked?: boolean
      href?: never
    })
  | (Common & {
      /**
       * A path on this panel. Relative on purpose: everything a menu links to
       * is served by the panel itself, and a checked shape is what keeps the
       * next caller from passing something a page supplied.
       */
      href: `/${string}`
      /** Save it rather than open it. */
      download?: boolean
      onSelect?: never
    })

/** Everything the arrows walk: the download is an anchor, not a button. */
const ITEMS = 'button:not(:disabled), a'

/** Where the list sits, as the two edges it is pinned to. */
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
    // top of the document. Selecting an item goes through here too: the item
    // is unmounted with the portal, and focus would otherwise land on <body>.
    trigger.current?.focus()
  }, [])

  /**
   * The list's corner, from the trigger's rectangle and the room around it.
   *
   * Flipped above the button when there is not enough room below it. Without
   * this, the menu of the last card in a long list is drawn past the bottom of
   * the window — and because it is `fixed`, there is nothing to scroll to
   * reach it. Every action that lives only in a menu (fork, export, delete,
   * lock, revoke) was unreachable there.
   *
   * `height` is what the list actually measured, or an estimate before it has
   * been drawn once. The estimate is deliberately generous: guessing too tall
   * flips a menu that would have fitted, which is survivable, and guessing too
   * short puts it off the screen, which is the bug.
   */
  const place = useCallback((height: number): At | null => {
    const box = trigger.current?.getBoundingClientRect()
    if (!box) return null
    const below = window.innerHeight - box.bottom - 8
    const top = height <= below ? box.bottom + 4 : Math.max(8, box.top - height - 4)
    return { top, right: Math.max(8, window.innerWidth - box.right) }
  }, [])

  const open = () => setAt(place(items.length * 30 + 8))

  // Measured once it is on screen, and again on every frame while it is open.
  //
  // The list around this one re-reads itself every five seconds, so a card can
  // grow a row or lose one while the menu is up; the trigger moves and a
  // position read once at open time points at a different row. Following costs
  // one rectangle per frame and only while something is open, and it is also
  // what lets a scroll keep the menu attached instead of dismissing it.
  useLayoutEffect(() => {
    if (!at) return
    list.current?.querySelector<HTMLElement>(ITEMS)?.focus()
    let frame = 0
    const step = () => {
      const next = place(list.current?.offsetHeight ?? 0)
      if (next) setAt((now) => (now && now.top === next.top && now.right === next.right ? now : next))
      frame = requestAnimationFrame(step)
    }
    frame = requestAnimationFrame(step)
    return () => cancelAnimationFrame(frame)
    // `at` is what this follows; depending on it would restart the loop on
    // every frame it moves. It runs while the menu is open, which is what
    // `at !== null` says.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [at !== null, place])

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
      const buttons = [...(list.current?.querySelectorAll<HTMLElement>(ITEMS) ?? [])]
      const here = buttons.indexOf(document.activeElement as HTMLElement)
      // Only while the keyboard is in the menu. It used to take both arrows
      // from the whole document in capture, so tabbing out of an open menu
      // left a page that would not scroll.
      if (here < 0) return
      e.preventDefault()
      const next = e.key === 'ArrowDown' ? here + 1 : here - 1
      buttons[(next + buttons.length) % buttons.length]?.focus()
    }
    const onDown = (e: PointerEvent) => {
      const el = e.target as Node
      if (list.current?.contains(el) || trigger.current?.contains(el)) return
      setAt(null)
    }
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('pointerdown', onDown, true)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('pointerdown', onDown, true)
    }
  }, [at, close])

  const name = label ?? t('menu.more')
  return (
    <>
      <button
        ref={trigger}
        type="button"
        aria-haspopup="menu"
        aria-expanded={at !== null}
        title={name}
        aria-label={name}
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
            aria-label={name}
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
              //
              // `focus:` and not only `focus-visible:`: the first item is
              // focused the moment the menu opens, and Chrome does not match
              // focus-visible after a pointer opened it — which left a live
              // Enter target with nothing drawn on it.
              const look =
                'vp-press flex w-full items-center gap-2 rounded-vp px-2 py-1.5 text-left text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink focus:bg-surface-2 focus:text-ink focus:outline-none disabled:opacity-40'
              const tint = item.destructive ? { color: 'var(--vp-state-crashed)' } : undefined
              const inside = (
                <>
                  {Icon && <Icon size={13} className="shrink-0 opacity-80" />}
                  <span className="min-w-0 truncate">{safeText(item.label)}</span>
                </>
              )
              // Keyed by what identifies the action rather than by what it
              // says: two items can end up with the same words in some
              // language, and the lock's label changes as it is toggled.
              const key = item.testid ?? item.label
              return item.href !== undefined ? (
                <a
                  key={key}
                  role="menuitem"
                  href={item.href}
                  download={item.download}
                  data-testid={item.testid}
                  onClick={close}
                  className={look}
                  style={tint}
                >
                  {inside}
                </a>
              ) : (
                <button
                  key={key}
                  type="button"
                  // A toggle says which way it is set. The versions block is
                  // one, and as a plain menuitem nothing exposed that it was
                  // already open.
                  role={item.checked === undefined ? 'menuitem' : 'menuitemcheckbox'}
                  aria-checked={item.checked}
                  disabled={item.disabled}
                  data-testid={item.testid}
                  onClick={() => {
                    close()
                    item.onSelect()
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
