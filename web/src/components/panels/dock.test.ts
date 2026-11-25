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

  it('gives each block across dock and detail a glyph and key of its own', () => {
    // Two blocks drawn as the same icon is a strip you cannot read at a glance,
    // which is the whole argument for the dock being two named rows.
    const all = [
      ...DOCK_BLOCKS.map((b) => DOCK_META[b]),
      ...DETAIL_BLOCKS.map((b) => DETAIL_META[b]),
    ]
    expect(new Set(all.map((m) => m.icon)).size).toBe(all.length)
    expect(new Set(all.map((m) => m.key)).size).toBe(all.length)
  })
})
