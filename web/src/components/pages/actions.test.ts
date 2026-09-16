import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * Every action the sharing list offers is still offered.
 *
 * Four of them moved into a menu in the redesign — a page's versions, fork,
 * export and delete; a link's lock and revoke — and that move is exactly the
 * kind that loses one on the way. Nothing would say so: the card still draws,
 * the other actions still work, and the only symptom is that the way to fork a
 * page is gone. The browser checks do not press these (they drive open,
 * publish, manage, copy, view and edit), so this is the only thing that counts
 * them.
 *
 * It reads the files rather than rendering them, for the reason every other
 * static check here does: vitest runs in node, and what is being caught is a
 * missing line, not a computed style.
 */
const HERE = new URL('./', import.meta.url).pathname

function source(file: string): string {
  return readFileSync(HERE + file, 'utf8')
}

/** What a page's card can do, and where each one lives. */
const PAGE_ACTIONS = {
  'page-open': 'button',
  'page-publish': 'button',
  'page-manage-open': 'button',
  'page-more': 'button',
  'page-history': 'menu',
  'page-fork': 'menu',
  'page-export': 'menu',
  'page-delete': 'menu',
} as const

/** What a link's row can do. */
const LINK_ACTIONS = {
  'share-copy-address': 'button',
  'share-view': 'button',
  'share-edit': 'button',
  'share-more': 'button',
  'share-lock': 'menu',
  'share-revoke': 'menu',
  'share-new': 'button',
} as const

/**
 * How many times a file offers this action, whichever way it is written.
 *
 * A control spells it `data-testid="x"`; a menu item spells it `testid: 'x'`,
 * because the menu puts the attribute on the element it draws. Counting one
 * spelling passes while the other is the only one there.
 */
function count(text: string, id: string): number {
  return text.match(new RegExp(`testid[:=]\\s*['"]${id}['"]`, 'g'))?.length ?? 0
}

/** Everything between `<Menu` and the line that closes its items list. */
function menuBlocks(text: string): string {
  return text
    .split('<Menu')
    .slice(1)
    .map((chunk) => chunk.slice(0, chunk.indexOf('/>')))
    .join('\n')
}

describe('the actions on a page card', () => {
  const text = source('Sharing.tsx')

  it('offers every one of them, exactly once', () => {
    for (const id of Object.keys(PAGE_ACTIONS)) {
      expect(count(text, id), id).toBe(1)
    }
  })

  it('keeps the rare ones in the menu and the daily ones out of it', () => {
    const menus = menuBlocks(text)
    expect(menus.length, 'Sharing.tsx draws no menu at all').toBeGreaterThan(100)
    for (const [id, where] of Object.entries(PAGE_ACTIONS)) {
      expect(menus.includes(`'${id}'`), `${id} should be ${where}`).toBe(where === 'menu')
    }
  })
})

describe('the actions on a link row', () => {
  const text = source('PageLinks.tsx')

  it('offers every one of them, exactly once', () => {
    for (const id of Object.keys(LINK_ACTIONS)) {
      expect(count(text, id), id).toBe(1)
    }
  })

  it('keeps the rare ones in the menu and the daily ones out of it', () => {
    const menus = menuBlocks(text)
    expect(menus.length, 'PageLinks.tsx draws no menu at all').toBeGreaterThan(100)
    for (const [id, where] of Object.entries(LINK_ACTIONS)) {
      expect(menus.includes(`'${id}'`), `${id} should be ${where}`).toBe(where === 'menu')
    }
  })

  it('asks before revoking', () => {
    // The two-step inline confirm went with the row's redesign; what replaced
    // it is the panel's own dialog. A revoke that just happens is the one
    // mistake on this page nobody can undo.
    expect(text).toContain('askConfirm')
    expect(text).toContain("t('share.revokeTitle'")
  })
})

/**
 * A tone is its own state's colour, or it is decoration.
 *
 * The chips carry the two states a reader acts on -- an expiry close enough to
 * matter, a page that is not published -- and they carry them in the panel's
 * state colours, which is what makes them read the same as a session's dot
 * four inches away. Swapping one for the ink colour leaves a chip that still
 * says the right word and no longer looks like anything, and nothing else here
 * would notice: the word is what red line 4 requires, and this is the half
 * that makes it findable.
 */
describe('what a chip is tinted with', () => {
  const text = source('bits.tsx')

  it('gives each tone its own token', () => {
    for (const [tone, token] of [
      ['accent', '--vp-accent'],
      ['warn', '--vp-state-waiting'],
      ['danger', '--vp-state-crashed'],
    ]) {
      const line = text.split('\n').find((l) => l.trimStart().startsWith(`${tone}:`))
      expect(line, tone).toBeDefined()
      expect(line, tone).toContain(`wash('${token}'`)
      expect(line, tone).toContain(`color: 'var(${token})'`)
    }
  })
})
