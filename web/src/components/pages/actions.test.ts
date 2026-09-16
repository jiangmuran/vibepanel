import { readFileSync, readdirSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * Every action the sharing list offers is still offered, and from the right
 * place.
 *
 * Six of these live only in a menu now — a page's versions, fork, export and
 * delete; a link's lock and revoke — and no browser check presses any of them
 * (the checks drive open, publish, manage, copy, view and edit). A redesign
 * that loses one draws a card that still works and quietly cannot fork a page.
 *
 * What this is **not**: proof that any of it runs. It reads source text, so it
 * sees a control that is written, not one that is reachable. Two things cover
 * the rest, and they are the ones to extend rather than this file:
 * `ask.test.ts` runs the confirm-then-do helper both ways, and `pages-check`
 * opens both menus in a browser and presses inside them.
 *
 * It reads the whole directory rather than two named files, so extracting a
 * row into its own component moves an action without failing this.
 */
const HERE = new URL('./', import.meta.url).pathname

function sources(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const name of readdirSync(HERE)) {
    if (name.endsWith('.tsx')) out[name] = readFileSync(HERE + name, 'utf8')
  }
  return out
}

const FILES = sources()
const ALL = Object.values(FILES).join('\n')

/** What a page's card can do, and where each one lives. */
const PAGE_ACTIONS = {
  'page-open': 'button',
  'page-publish': 'button',
  'page-manage-open': 'button',
  'page-more': 'button',
  'page-rollback': 'button',
  'page-history': 'menu',
  'page-fork': 'menu',
  'page-export': 'menu',
  'page-delete': 'menu',
} as const

/** What a link's row can do. */
const LINK_ACTIONS = {
  'share-new': 'button',
  'share-copy-address': 'button',
  'share-view': 'button',
  'share-edit': 'button',
  'share-more': 'button',
  'share-rotate': 'button',
  'share-lock': 'menu',
  'share-revoke': 'menu',
} as const

/**
 * How many times the list offers this action, whichever way it is written.
 *
 * A control spells it `data-testid="x"`; a menu item spells it `testid: 'x'`,
 * and either may be quoted with ' or ". Counting one spelling passes while the
 * other is the only one there, and keying the menu/not-menu question to a
 * quote style makes a formatting change fail a behaviour test.
 */
function count(text: string, id: string): number {
  return text.match(new RegExp(`testid\\s*[:=]\\s*['"]${id}['"]`, 'g'))?.length ?? 0
}

/**
 * The items of every menu: the entries of each `<Menu … items={[…]}` array.
 *
 * Only the items, not the element. The trigger's own `testid="page-more"` is a
 * prop on `<Menu>` and is a button like any other, and reading the whole tag
 * counted it as something inside the menu.
 */
function menuItems(text: string): string {
  return text
    .split('<Menu')
    .slice(1)
    .map((chunk) => {
      const tag = chunk.slice(0, chunk.indexOf('/>'))
      const at = tag.indexOf('items={')
      return at < 0 ? '' : tag.slice(at)
    })
    .join('\n')
}

const MENUS = Object.values(FILES).map(menuItems).join('\n')

describe('the actions the sharing list offers', () => {
  it('reads the files at all', () => {
    // An empty menu set would make every "not in a menu" assertion pass.
    expect(Object.keys(FILES).length).toBeGreaterThan(3)
    expect(MENUS.length, 'nothing in this directory draws a menu').toBeGreaterThan(100)
  })

  it('offers every one of them, exactly once', () => {
    for (const id of [...Object.keys(PAGE_ACTIONS), ...Object.keys(LINK_ACTIONS)]) {
      expect(count(ALL, id), id).toBe(1)
    }
  })

  it('keeps the rare ones in the menu and the daily ones out of it', () => {
    for (const [id, where] of Object.entries({ ...PAGE_ACTIONS, ...LINK_ACTIONS })) {
      expect(count(MENUS, id) > 0, `${id} should be a ${where}`).toBe(where === 'menu')
    }
  })
})

/**
 * What a JSX element draws inside it: everything after the `>` that closes
 * its opening tag.
 *
 * Not the first `>`: `tone={link.viewers > 0 ? …}` has one inside a prop, and
 * a cut there reads half the attributes as children -- which lets an `icon=`
 * that tests the same condition stand in for words that do not.
 */
function childrenOf(element: string): string {
  let depth = 0
  for (let i = 0; i < element.length; i++) {
    const c = element[i]
    if (c === '{') depth += 1
    else if (c === '}') depth -= 1
    else if (c === '>' && depth === 0) return element.slice(i + 1)
  }
  return ''
}

/**
 * A tone is its own state's colour, and never the only thing saying so.
 *
 * The chips carry the states a reader acts on — an expiry close enough to
 * matter, a link nobody can edit, a page with nothing published — in the
 * panel's state colours, which is what makes them read like a session's dot
 * four inches away. Two ways that rots: the table stops using the state
 * tokens, or a chip changes tone without changing its word, which is red line
 * 4 and is exactly what the expiry chip did first.
 */
describe('what a chip is tinted with', () => {
  const bits = FILES['bits.tsx']

  it('gives each tone its own token', () => {
    for (const [tone, token] of [
      ['accent', '--vp-accent'],
      ['warn', '--vp-state-waiting'],
    ]) {
      const at = bits.indexOf(`${tone}: {`)
      expect(at, tone).toBeGreaterThan(0)
      const entry = bits.slice(at, bits.indexOf('}', at))
      expect(entry, tone).toContain(`wash('${token}'`)
      expect(entry, tone).toContain(`color: 'var(${token})'`)
    }
  })

  it('applies the tone it was given', () => {
    // Without this the table can be perfect while every chip draws grey.
    expect(bits).toContain('style={TONE[tone]}')
  })

  it('never changes only the tone', () => {
    // A tone chosen by a condition needs a word chosen by the same condition:
    // the chip's children have to test what its `tone=` tests. The viewers
    // count and the published status both do; the expiry chip did not, and
    // said "expires 9/22" in the same words whether that was tomorrow or next
    // spring.
    let seen = 0
    for (const [name, text] of Object.entries(FILES)) {
      for (const chunk of text.split('<Chip').slice(1)) {
        const chip = chunk.slice(0, chunk.indexOf('</Chip>'))
        const cond = /tone=\{([^?}]+)\?/.exec(chip)?.[1]?.trim()
        if (!cond) continue
        seen += 1
        expect(childrenOf(chip), `${name}: tone={${cond} ? …} with words that do not depend on it`).toContain(cond)
      }
    }
    // A parser that finds no conditional tone at all passes trivially.
    expect(seen).toBeGreaterThan(1)
  })

  it('says a link is locked, in words', () => {
    // It used to be said only by drawing the pencil at 40%, which a touch
    // screen gives no tooltip for.
    expect(count(ALL, 'share-row-locked')).toBe(1)
    const at = ALL.indexOf("testid=\"share-row-locked\"")
    expect(ALL.slice(Math.max(0, at - 200), at)).toContain('link.locked &&')
  })
})
