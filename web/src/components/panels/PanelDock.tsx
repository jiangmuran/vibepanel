import { ChevronsUpDown } from 'lucide-react'

import { DOCK_BLOCKS, type DockBlock, type PanelDensity } from '../chrome'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { DOCK_META } from './dock'
import { SystemStrip } from './SystemStrip'
import { TokenBlock } from './TokenBlock'
import { SPEND_SPAN, type Spend } from './useSpend'

/**
 * The bottom half of both tabs.
 *
 * The same two blocks in the same order whichever tab you are on, which is the
 * point: 「token消耗和系统监控 … 都在侧边栏显示」. Neither is somewhere you
 * navigate to — both are things you want in the corner of your eye while
 * reading a file list or writing a note, and a tab you have to choose is a tab
 * nobody chooses. The monitor strip was already built on that argument; giving
 * the monitor a tab as well contradicted it, and this is that contradiction
 * settled the other way.
 *
 * This replaces the strip that used to hang below the panel. Two places
 * showing the machine's three meters — one permanent, one a tab — was the same
 * fact twice.
 */
export function PanelDock({
  spend,
  density,
  projectId,
  projectName,
  onOpen,
}: {
  spend: Spend
  density: PanelDensity
  projectId: string | null
  projectName: string | null
  /** Opens one block into the whole side panel. See PanelDetail. */
  onOpen: (block: DockBlock) => void
}) {
  useLang()
  return (
    <div data-testid="panel-dock" className="flex h-full min-h-0 flex-col overflow-y-auto">
      {DOCK_BLOCKS.map((block) => (
        <section
          key={block}
          data-testid={`dock-${block}`}
          className="shrink-0 border-t border-hairline first:border-t-0"
        >
          <DockHeader block={block} onOpen={() => onOpen(block)} />
          {block === 'tokens' ? (
            spend.error !== null ? (
              <p className="px-3 py-2 text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
                {safeText(spend.error)}
              </p>
            ) : spend.data === null ? (
              <p className="px-3 py-2 text-vp-sm text-ink-2">{t('spend.scanning')}</p>
            ) : (
              <TokenBlock
                data={spend.data}
                projectId={projectId}
                projectName={projectName}
                span={SPEND_SPAN}
                density={density}
                now={spend.now}
              />
            )
          ) : (
            <SystemStrip />
          )}
        </section>
      ))}
    </div>
  )
}

/**
 * The press target, and the only one in the block.
 *
 * A block that is both a button and a container full of controls is how
 * somebody expands a panel while trying to click a number, so the affordance is
 * one wide row across the top and nothing below it is pressable. It is visible
 * rather than revealed on hover: a control that appears when the pointer
 * arrives is a control a finger never finds, and this is the only way into the
 * detail.
 *
 * `.vp-control` rather than a class list of its own — it is the same object as
 * every other button in this chrome, stretched. What is added to it is
 * orthogonal: a width, an alignment, and a padding that suits a row rather than
 * a square.
 */
function DockHeader({ block, onOpen }: { block: DockBlock; onOpen: () => void }) {
  const { icon: Icon, key } = DOCK_META[block]
  const name = t(key)
  return (
    <button
      type="button"
      data-testid={`dock-open-${block}`}
      onClick={onOpen}
      title={t('detail.open', { what: name })}
      aria-label={t('detail.open', { what: name })}
      className="vp-control w-full justify-between px-3"
    >
      <span className="flex min-w-0 items-center gap-1.5">
        <Icon size={12} className="shrink-0" />
        <span className="truncate text-vp-xs uppercase tracking-wide">{name}</span>
      </span>
      {/* Shape, not only a hover colour: the chevron is what says the row is a
          way in rather than a heading. */}
      <ChevronsUpDown size={12} className="shrink-0" />
    </button>
  )
}
