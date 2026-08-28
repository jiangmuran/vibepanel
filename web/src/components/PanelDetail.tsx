import { useEffect } from 'react'
import { ChevronLeft, Maximize2, X } from 'lucide-react'

import type { DetailBlock } from './chrome'
import { t, useLang, type Key } from '../i18n'

/**
 * A block opened out of its compact form.
 *
 * 「token消耗和系统监控点开都能看到详情 都在侧边栏显示 也可以再点一下在全屏显示」
 * — three states, in one component so that all three behave the same way
 * whichever block you are in:
 *
 *   compact    a few figures, sharing the bottom half of the tab
 *   open       the same subject with the whole side panel to itself
 *   full       the same subject with the whole window
 *
 * The rule that makes them predictable is that each state replaces the one
 * below it rather than sitting beside it. An open block is open *instead of*
 * the stack; there is never a second block open behind it, and there is no
 * arrangement in which two of them are half-open. Two things open at once is
 * the four-tab panel's mistake in a smaller box.
 *
 * Escape backs out one level: full to open, open to compact. Not two levels
 * and not to the top, because the level you came from is the one you were
 * reading a moment ago.
 *
 * None of it is persisted. A stored layout is a thing you built; this is a
 * thing you are doing, and coming back tomorrow to a full-screen monitor you
 * opened once is a panel that has remembered the wrong kind of state. The pane
 * layout and the divider positions are remembered precisely because they are
 * the other kind.
 */
export function PanelDetail({
  block,
  title,
  icon: Icon,
  full,
  onBack,
  onFull,
  children,
}: {
  block: DetailBlock
  title: Key
  icon: React.ComponentType<{ size?: number; className?: string }>
  /** Drawn over the window rather than inside the panel. */
  full: boolean
  onBack: () => void
  /** Absent when this block has nothing more to show at full width. */
  onFull?: () => void
  children: React.ReactNode
}) {
  useLang()

  // Escape, from anywhere inside it. A surface you can only leave by finding
  // the one control that closes it is a surface people leave by reloading.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      // Stopped here rather than left to bubble: the pane's own Escape handler
      // cancels a tab drag, and the terminal below takes it as an interrupt.
      e.stopPropagation()
      onBack()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onBack])

  const name = t(title)
  const body = (
    <div
      data-testid={`detail-${block}`}
      data-full={full}
      className="flex h-full min-h-0 flex-col"
    >
      <header className="vp-chrome gap-1 border-b border-hairline px-2">
        <button
          type="button"
          data-testid={`detail-back-${block}`}
          onClick={onBack}
          // Back, not close. It returns to the compact block you pressed,
          // which is one level and not the top, so the chevron points the way
          // the eye came from.
          title={t('detail.back')}
          aria-label={t('detail.back')}
          className="vp-control vp-tap"
        >
          {full ? <X size={14} /> : <ChevronLeft size={14} />}
        </button>
        <h2 className="flex min-w-0 flex-1 items-center gap-1.5 text-vp-md text-ink">
          <Icon size={13} className="shrink-0 text-ink-2" />
          <span className="truncate">{name}</span>
        </h2>
        {!full && onFull && (
          <button
            type="button"
            data-testid={`detail-full-${block}`}
            onClick={onFull}
            title={t('detail.full')}
            aria-label={t('detail.full')}
            className="vp-control vp-tap"
          >
            <Maximize2 size={14} />
          </button>
        )}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
    </div>
  )

  if (!full) return body

  // Over everything, opaque, and its own stacking context. Modelled on the
  // settings and token overlays so there is one way a full-screen surface
  // appears in this product rather than three.
  return (
    <div
      data-testid={`detail-full-view-${block}`}
      className="vp-panel-in fixed inset-0 z-50 flex flex-col vp-solid"
    >
      {body}
    </div>
  )
}
