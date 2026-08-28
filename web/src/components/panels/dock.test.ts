import { describe, expect, it } from 'vitest'

import { DETAIL_BLOCKS, DOCK_BLOCKS } from '../chrome'
import { DETAIL_META, DOCK_META } from './dock'

/**
 * Every block has a name and a glyph, and they are the same ones in both maps.
 *
 * The `Record<DockBlock, ...>` type already says the first half, and it says it
 * at compile time — which is exactly why this is here. Mutation testing removed
 * the `tokens` entry and every unit test stayed green, because `tsc` is not
 * what `vitest` runs. A block with no meta renders a header with no name and no
 * way in, and the type that was supposed to prevent it is only consulted by a
 * command somebody might not run.
 */
describe('what each openable block is called', () => {
  it('names every block in the dock', () => {
    for (const block of DOCK_BLOCKS) {
      expect(DOCK_META[block], block).toBeDefined()
      expect(DOCK_META[block].key, block).toBeTruthy()
      // A lucide icon is a forwardRef object rather than a plain function.
      expect(DOCK_META[block].icon, block).toBeTruthy()
    }
    expect(Object.keys(DOCK_META).sort()).toEqual([...DOCK_BLOCKS].sort())
  })

  it('names every block that can be opened', () => {
    for (const block of DETAIL_BLOCKS) {
      expect(DETAIL_META[block], block).toBeDefined()
      expect(DETAIL_META[block].key, block).toBeTruthy()
    }
    expect(Object.keys(DETAIL_META).sort()).toEqual([...DETAIL_BLOCKS].sort())
  })

  it('calls a block the same thing in the dock and in the header it opens', () => {
    // Drift nobody would notice, because the two are never on screen at the
    // same moment: the compact block is replaced by the opened one.
    for (const block of DOCK_BLOCKS) {
      expect(DETAIL_META[block].key, block).toBe(DOCK_META[block].key)
      expect(DETAIL_META[block].icon, block).toBe(DOCK_META[block].icon)
    }
  })

  it('gives each block a glyph of its own', () => {
    // Two blocks drawn as the same icon is a strip you cannot read at a glance,
    // which is the whole argument for the dock being two named rows.
    const icons = DETAIL_BLOCKS.map((b) => DETAIL_META[b].icon)
    expect(new Set(icons).size).toBe(icons.length)
    const keys = DETAIL_BLOCKS.map((b) => DETAIL_META[b].key)
    expect(new Set(keys).size).toBe(keys.length)
  })
})
