import { readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import {
  absOf,
  classifyInput,
  crumbs,
  filterEntries,
  insideRoot,
  matchSpan,
  resolveKey,
  type KeyState,
} from './dirpicker'

const HOME = '/home/u'

/**
 * The two ends of this control that a unit test cannot render.
 *
 * The picker is driven from web/scripts by `data-testid`, and those scripts
 * start a browser: a renamed testid is a check that fails twenty minutes into
 * a run somebody started for another reason, and reads as the feature being
 * broken rather than as the handle having moved. The same for the classes the
 * motion is in -- they live in styles.css by house rule, so the component and
 * the stylesheet are two files that have to agree with nothing between them.
 *
 * Crude on purpose, in the manner of scale.test.ts: it reads both sides as
 * text. A crude check that runs on every commit is worth more here than an
 * exact one that needs a browser.
 */
describe('the handles the browser checks hold on to', () => {
  const source = readFileSync(new URL('./DirectoryPicker.tsx', import.meta.url), 'utf8')
  const scriptDir = new URL('../../scripts/', import.meta.url)
  const scripts = readdirSync(scriptDir)
    .filter((f) => f.endsWith('.mjs'))
    .map((f) => readFileSync(new URL(f, scriptDir), 'utf8'))
    .join('\n')

  it('keeps every dir- testid the scripts reach for', () => {
    const wanted = new Set([...scripts.matchAll(/data-testid="(dir-[a-z-]+)"/g)].map((m) => m[1]))
    // Without this the loop below passes by iterating over nothing, which is
    // the failure mode of every checker that scans a directory.
    expect(wanted.size).toBeGreaterThan(2)
    for (const id of wanted) {
      expect(
        source.includes(`"${id}"`) || source.includes(`'${id}'`),
        `${id} is used by a browser check and is not in the picker any more`,
      ).toBe(true)
    }
  })

  it('uses no motion that styles.css does not define', () => {
    const css = readFileSync(new URL('../styles.css', import.meta.url), 'utf8')
    for (const cls of ['vp-enter-right', 'vp-enter-left', 'vp-crumbs', 'vp-crumbs-away', 'vp-skeleton']) {
      expect(source, `${cls} is not used by the picker any more`).toContain(cls)
      expect(css, `${cls} has no rule in styles.css`).toContain(`.${cls}`)
    }
  })
})

describe('crumbs', () => {
  it('starts at the root even when there is nothing below it', () => {
    expect(crumbs('')).toEqual([{ label: '~', path: '' }])
  })

  it('gives every level somewhere to click back to', () => {
    expect(crumbs('projects/vibepanel/web')).toEqual([
      { label: '~', path: '' },
      { label: 'projects', path: 'projects' },
      { label: 'vibepanel', path: 'projects/vibepanel' },
      { label: 'web', path: 'projects/vibepanel/web' },
    ])
  })

  it('is not confused by a trailing or doubled separator', () => {
    expect(crumbs('a//b/')).toEqual([
      { label: '~', path: '' },
      { label: 'a', path: 'a' },
      { label: 'b', path: 'a/b' },
    ])
  })
})

describe('absOf', () => {
  it('names the root itself', () => {
    expect(absOf(HOME, '')).toBe(HOME)
  })

  it('joins without doubling the separator', () => {
    expect(absOf('/home/u/', 'projects')).toBe('/home/u/projects')
    expect(absOf('/', 'srv')).toBe('/srv')
  })
})

describe('insideRoot', () => {
  it('answers the root itself with the empty path, which is not null', () => {
    expect(insideRoot(HOME, HOME)).toBe('')
  })

  /**
   * The prefix trap, and the reason this is a function rather than a
   * `startsWith` at the call site. `/home/u` is a string prefix of
   * `/home/user2`, so the cheap check says another account's home is inside
   * yours -- and the picker would then try to list it, get a 403 out of the
   * server's containment, and report a refusal for a path it should simply
   * have offered to use as it is.
   */
  it('refuses a sibling whose name merely starts the same way', () => {
    expect(insideRoot('/home/user2/x', HOME)).toBeNull()
  })

  it('lets everything in when the root is the filesystem root', () => {
    expect(insideRoot('/srv/thing', '/')).toBe('srv/thing')
    expect(insideRoot('/', '/')).toBe('')
  })
})

describe('classifyInput', () => {
  it('reads ordinary text as a filter', () => {
    expect(classifyInput('  vibe ', HOME)).toEqual({ kind: 'filter', query: 'vibe' })
  })

  it('reads an empty box as a filter that matches everything', () => {
    expect(classifyInput('', HOME)).toEqual({ kind: 'filter', query: '' })
  })

  it('reads a leading slash as a place, outside the root', () => {
    expect(classifyInput('/srv/thing', HOME)).toEqual({
      kind: 'path',
      abs: '/srv/thing',
      inside: null,
    })
  })

  it('expands ~ to the root the server actually gave us', () => {
    expect(classifyInput('~', HOME)).toEqual({ kind: 'path', abs: HOME, inside: '' })
    expect(classifyInput('~/projects/x', HOME)).toEqual({
      kind: 'path',
      abs: '/home/u/projects/x',
      inside: 'projects/x',
    })
  })

  it('leaves another account’s home for the server to refuse', () => {
    // `~someone` is a shell's notation for a home the browser has no way to
    // look up. Guessing would be worse than being told no.
    expect(classifyInput('~someone/x', HOME)).toEqual({
      kind: 'path',
      abs: '~someone/x',
      inside: null,
    })
  })

  it('does not expand ~ before the server has said where home is', () => {
    // The root arrives with the first listing. Expanding against an empty one
    // silently turns `~/projects` into `/projects`, which is a real directory
    // somewhere else.
    expect(classifyInput('~/projects', '')).toEqual({
      kind: 'path',
      abs: '~/projects',
      inside: null,
    })
  })

  it('collapses . and .. before deciding where the path is', () => {
    // Without this, `~/projects/../../etc` is "inside the root" by string
    // prefix, and the picker offers to walk into somewhere it cannot list.
    expect(classifyInput('~/projects/../../etc', HOME)).toEqual({
      kind: 'path',
      abs: '/home/etc',
      inside: null,
    })
    expect(classifyInput('/home/u/./a//b/', HOME)).toEqual({
      kind: 'path',
      abs: '/home/u/a/b',
      inside: 'a/b',
    })
  })
})

describe('matchSpan', () => {
  it('finds the match regardless of case', () => {
    expect(matchSpan('VibePanel', 'panel')).toEqual([4, 9])
  })

  it('has nothing to mark for an empty query or a miss', () => {
    expect(matchSpan('web', '')).toBeNull()
    expect(matchSpan('web', 'zzz')).toBeNull()
  })
})

describe('filterEntries', () => {
  const dirs = [
    { name: 'my-web-archive' },
    { name: 'services' },
    { name: 'Web' },
    { name: 'web-tools' },
  ]

  it('keeps the server ordering when nothing is typed', () => {
    expect(filterEntries(dirs, '  ').map((d) => d.name)).toEqual([
      'my-web-archive',
      'services',
      'Web',
      'web-tools',
    ])
  })

  it('puts what starts with the query above what merely contains it', () => {
    // Typing `web` where `web-tools` and `my-web-archive` both live should not
    // make anybody read both rows to find the one they meant.
    expect(filterEntries(dirs, 'web').map((d) => d.name)).toEqual([
      'Web',
      'web-tools',
      'my-web-archive',
    ])
  })

  it('drops what does not match at all', () => {
    expect(filterEntries(dirs, 'zzz')).toEqual([])
  })
})

/**
 * One box and one list share one focus, so every key has to belong to exactly
 * one of them. `text` is the answer that means "not ours -- let the input have
 * it", and getting that wrong is how Backspace deletes a character *and* walks
 * up a directory.
 */
describe('resolveKey', () => {
  const base: KeyState = {
    key: '',
    kind: 'filter',
    navigable: false,
    hasText: false,
    count: 5,
    active: 0,
    hasParent: true,
  }
  const at = (over: Partial<KeyState>) => resolveKey({ ...base, ...over })

  it('walks the list and stops at both ends', () => {
    expect(at({ key: 'ArrowDown', active: 0 })).toEqual({ do: 'move', to: 1 })
    expect(at({ key: 'ArrowDown', active: 4 })).toEqual({ do: 'move', to: 4 })
    expect(at({ key: 'ArrowUp', active: 3 })).toEqual({ do: 'move', to: 2 })
    expect(at({ key: 'ArrowUp', active: 0 })).toEqual({ do: 'move', to: 0 })
  })

  it('jumps to the ends, because a filter is too short to need a caret', () => {
    expect(at({ key: 'Home', active: 3, hasText: true })).toEqual({ do: 'move', to: 0 })
    expect(at({ key: 'End', active: 0, hasText: true })).toEqual({ do: 'move', to: 4 })
  })

  it('has nowhere to move in an empty directory', () => {
    expect(at({ key: 'ArrowDown', count: 0, active: -1 })).toEqual({ do: 'move', to: -1 })
    expect(at({ key: 'End', count: 0, active: -1 })).toEqual({ do: 'move', to: -1 })
  })

  it('opens the highlighted row on Enter', () => {
    expect(at({ key: 'Enter' })).toEqual({ do: 'open' })
  })

  it('offers to create what was typed when nothing matched it', () => {
    expect(at({ key: 'Enter', hasText: true, count: 0, active: -1 })).toEqual({
      do: 'createNamed',
    })
  })

  it('takes the directory when there is nothing in it to open', () => {
    expect(at({ key: 'Enter', hasText: false, count: 0, active: -1 })).toEqual({ do: 'use' })
  })

  it('gives the caret keys back the moment there is text to edit', () => {
    for (const key of ['Backspace', 'ArrowLeft', 'ArrowRight']) {
      expect(at({ key, hasText: true }), key).toEqual({ do: 'text' })
    }
  })

  it('walks the hierarchy with those same keys while the box is empty', () => {
    expect(at({ key: 'Backspace' })).toEqual({ do: 'up' })
    expect(at({ key: 'ArrowLeft' })).toEqual({ do: 'up' })
    expect(at({ key: 'ArrowRight' })).toEqual({ do: 'open' })
  })

  it('does not pretend to go up from the root', () => {
    expect(at({ key: 'Backspace', hasParent: false })).toEqual({ do: 'text' })
  })

  it('clears the box before it closes the dialog', () => {
    expect(at({ key: 'Escape', hasText: true })).toEqual({ do: 'clear' })
    expect(at({ key: 'Escape', hasText: false })).toEqual({ do: 'close' })
  })

  it('leaves ordinary typing alone', () => {
    expect(at({ key: 'a' })).toEqual({ do: 'text' })
    expect(at({ key: 'PageDown' })).toEqual({ do: 'text' })
  })

  describe('addressing a path', () => {
    const path: Partial<KeyState> = { kind: 'path', hasText: true }

    it('goes there when the picker can list it', () => {
      expect(at({ ...path, key: 'Enter', navigable: true })).toEqual({ do: 'go' })
    })

    it('takes it as it is when it is outside the root', () => {
      // A project under /srv or /opt is ordinary. The server roots the listing
      // at home, so there is nothing to show -- which is a reason to accept the
      // path, not a reason to refuse it.
      expect(at({ ...path, key: 'Enter', navigable: false })).toEqual({ do: 'use' })
    })

    it('hands every editing key to the field, ends included', () => {
      for (const key of ['Home', 'End', 'ArrowLeft', 'ArrowRight', 'Backspace', 'ArrowDown']) {
        expect(at({ ...path, key }), key).toEqual({ do: 'text' })
      }
    })

    it('gets out of path mode without closing the dialog', () => {
      expect(at({ ...path, key: 'Escape' })).toEqual({ do: 'clear' })
    })
  })
})
