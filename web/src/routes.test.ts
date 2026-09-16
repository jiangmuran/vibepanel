import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import {
  CHAT_PATH,
  PANEL_PATH,
  SHARING_PATH,
  pageToOpen,
  panelOpeningPage,
  panelOpeningSession,
  routeFor,
  sessionToOpen,
} from './routes'

/**
 * Which root the address bar builds, and how the two hand over to each other.
 *
 * The quiet failure here is a link that is a string: the sharing page is
 * reached by links, and a link keeps compiling after the route is renamed.
 * What breaks is a button that lands on the panel with no error anywhere. So
 * the string is pinned to the route in one place, and the hand-over from the
 * sharing page back to the panel is a round trip through two functions rather
 * than a query somebody spells at each end.
 */
describe('what the address bar decides', () => {
  it('builds the panel for the root and for anything it does not know', () => {
    expect(routeFor(PANEL_PATH)).toEqual({ kind: 'panel' })
    expect(routeFor('/sessions')).toEqual({ kind: 'panel' })
    expect(routeFor('/share')).toEqual({ kind: 'panel' })
    expect(routeFor('/share/abcdefghijklmnopqrstuvwx')).toEqual({ kind: 'panel' })
  })

  it('builds the sharing page for its path, with or without a slash', () => {
    expect(routeFor(SHARING_PATH)).toEqual({ kind: 'sharing' })
    expect(routeFor(`${SHARING_PATH}/`)).toEqual({ kind: 'sharing' })
  })

  it('builds the chat page for its path and not by prefix', () => {
    expect(routeFor(CHAT_PATH)).toEqual({ kind: 'chat' })
    expect(routeFor(`${CHAT_PATH}/`)).toEqual({ kind: 'chat' })
    expect(routeFor(`${CHAT_PATH}x`)).toEqual({ kind: 'panel' })
    expect(routeFor(`${CHAT_PATH}/abc`)).toEqual({ kind: 'panel' })
  })

  it('does not match the sharing page by prefix', () => {
    // `/sharing` and `/share/<token>` share five letters, and `/sharingx` is
    // nothing at all; a prefix match would build the wrong root for both.
    expect(routeFor(`${SHARING_PATH}/abcdefghijklmnopqrstuvwx`)).toEqual({ kind: 'panel' })
    expect(routeFor(`${SHARING_PATH}x`)).toEqual({ kind: 'panel' })
  })
})

describe('a chat card opening a session', () => {
  it('round-trips the id and refuses what is not one', () => {
    const to = panelOpeningSession('abc123')
    expect(to.startsWith(PANEL_PATH)).toBe(true)
    expect(sessionToOpen(new URL(to, 'http://x').search)).toBe('abc123')
    expect(sessionToOpen('')).toBeNull()
    expect(sessionToOpen('?session=')).toBeNull()
    expect(sessionToOpen('?session=../x')).toBeNull()
  })
})

describe('handing a page over to the panel', () => {
  it('round-trips the id and whether an agent should start', () => {
    const to = panelOpeningPage('pg_abc-DEF_123', true)
    expect(to.startsWith(PANEL_PATH)).toBe(true)
    expect(pageToOpen(new URL(to, 'http://x').search)).toEqual({ id: 'pg_abc-DEF_123', fresh: true })
    expect(pageToOpen(new URL(panelOpeningPage('pg_1', false), 'http://x').search)).toEqual({
      id: 'pg_1',
      fresh: false,
    })
  })

  it('opens nothing for a plain visit, an empty id or one that is not an id', () => {
    expect(pageToOpen('')).toBeNull()
    expect(pageToOpen('?page=')).toBeNull()
    expect(pageToOpen('?page=../x')).toBeNull()
    expect(pageToOpen('?fresh=1')).toBeNull()
  })
})

/**
 * Every link to the sharing page imports the path.
 *
 * The rail item in settings and the way back from the page are both links,
 * and a link written as `'/sharing'` keeps compiling after the route is
 * renamed; what breaks is a button that lands on the panel with no error
 * anywhere. So the literal may appear in exactly one file.
 */
describe('who spells the sharing path', () => {
  const SRC = new URL('./', import.meta.url).pathname

  function sources(dir: string): string[] {
    const out: string[] = []
    for (const name of readdirSync(dir)) {
      const p = join(dir, name)
      if (statSync(p).isDirectory()) out.push(...sources(p))
      else if (/\.tsx?$/.test(name) && !name.endsWith('.test.ts')) out.push(p)
    }
    return out
  }

  it('is routes.ts and nobody else', () => {
    const spelt = sources(SRC).filter((p) => {
      const text = readFileSync(p, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
      return text.includes(`'${SHARING_PATH}'`) || text.includes(`"${SHARING_PATH}"`)
    })
    expect(spelt.map((p) => p.slice(SRC.length))).toEqual(['routes.ts'])
  })

  it('is pointed at from somewhere', () => {
    // A route nothing links to is a page only a bookmark can reach.
    const linking = sources(SRC).filter((p) => readFileSync(p, 'utf8').includes('href={SHARING_PATH}'))
    expect(linking.length).toBeGreaterThan(0)
  })

  it('is what the panel is handed a page through', () => {
    // Both ends of the hand-over go through routes.ts: the page navigates
    // with panelOpeningPage and the panel reads with pageToOpen. One end
    // spelling the query by hand is the drift this exists to refuse.
    const text = (f: string) => readFileSync(join(SRC, f), 'utf8')
    expect(text('components/SharingPage.tsx')).toContain('panelOpeningPage(')
    expect(text('App.tsx')).toContain('pageToOpen(')
  })
})
