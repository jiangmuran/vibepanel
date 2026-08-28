import { Activity, Coins, GitBranch } from 'lucide-react'

import type { DetailBlock, DockBlock } from '../chrome'
import type { Key } from '../../i18n'

/**
 * What each openable block is called and what it is drawn as.
 *
 * Data, not a component, and in its own module for that reason: the dock reads
 * it to label the press target and the panel reads it to title the opened
 * view, and a map that lives inside one of those two files makes the other one
 * import a component to get at a string.
 *
 * The label is a key rather than a string, so a language switch repaints it
 * instead of needing a reload — the same rule the tab strip follows.
 */
export const DOCK_META: Record<DockBlock, { icon: typeof Activity; key: Key }> = {
  tokens: { icon: Coins, key: 'panel.tokens' },
  monitor: { icon: Activity, key: 'panel.monitor' },
}

/**
 * The same, for everything PanelDetail can open.
 *
 * Spread from DOCK_META rather than restated, so a block cannot be named one
 * thing in the dock and another in the header it opens into — which is exactly
 * the kind of drift nobody notices, because the two are never on screen at the
 * same moment.
 */
export const DETAIL_META: Record<DetailBlock, { icon: typeof Activity; key: Key }> = {
  ...DOCK_META,
  repo: { icon: GitBranch, key: 'panel.git' },
}
