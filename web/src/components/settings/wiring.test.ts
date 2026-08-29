import { readFileSync, readdirSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { SETTINGS_GROUPS, SETTINGS_SECTIONS, groupOf, sectionsIn } from './groups'
import type { SettingsGroup, SettingsSection } from './groups'

/**
 * The map in groups.ts against the components that actually draw the sections.
 *
 * `groupOf` is what every deep link goes through — the sidebar's "states are
 * being guessed" notice asks for `reporting` and is sent to a rail item — so a
 * map that disagrees with the JSX is a notice that opens the settings dialog on
 * the wrong page. Nothing about that is visible: the dialog opens, something is
 * on screen, and it is not what the notice promised. The panel has been here
 * before with the hooks that report state, where every failure produced no
 * error anywhere and a page that looked fine.
 *
 * It resolves the component tree rather than grepping for section ids, and the
 * difference is a whole class of mutant. A first version read each file for
 * `<Section id="…">` and asked which file it was in — which says a section is
 * *written*, not that anything renders it. Deleting `<AuditSection />` from the
 * account group left the activity log declared, unreachable and unreported.
 *
 * Static, because the alternative is jsdom, and vitest here runs in node on
 * purpose (see vitest.config.ts).
 */
const HERE = new URL('./', import.meta.url).pathname
const DIALOG = readFileSync(new URL('../Settings.tsx', import.meta.url), 'utf8')

interface Component {
  sections: SettingsSection[]
  renders: string[]
}

/**
 * Every component declared under settings/, what it draws and what it renders.
 *
 * Chunked on top-level `function` declarations, which is the whole convention
 * these files follow. A component that stopped being one would show up as an
 * empty entry rather than as a silent pass.
 */
function registry(): Map<string, Component> {
  const out = new Map<string, Component>()
  for (const file of readdirSync(HERE).filter((f) => f.endsWith('.tsx'))) {
    const text = readFileSync(HERE + file, 'utf8')
    const heads = [...text.matchAll(/^(?:export )?function (\w+)/gm)]
    heads.forEach((head, i) => {
      const body = text.slice(head.index, heads[i + 1]?.index ?? text.length)
      out.set(head[1], {
        sections: [...body.matchAll(/<Section\s+id="(\w+)"/g)].map((m) => m[1] as SettingsSection),
        renders: [...body.matchAll(/<([A-Z]\w*)/g)].map((m) => m[1]),
      })
    })
  }
  return out
}

const COMPONENTS = registry()

/** Every section a group's component tree puts on screen. */
function sectionsUnder(name: string, seen = new Set<string>()): SettingsSection[] {
  if (seen.has(name)) return []
  seen.add(name)
  const c = COMPONENTS.get(name)
  if (!c) return []
  return [...c.sections, ...c.renders.flatMap((child) => sectionsUnder(child, seen))]
}

/** Which component the dialog renders for each rail item. */
function bodyOf(group: SettingsGroup): string {
  const m = DIALOG.match(new RegExp(`group === '${group}' && <(\\w+)`))
  expect(m, `Settings.tsx renders nothing for the ${group} group`).not.toBeNull()
  return m![1]
}

describe('what each rail item shows', () => {
  it('finds the components at all', () => {
    // A resolver that walks the wrong directory reports nothing, which is
    // indistinguishable from a tree that is wired correctly.
    expect(COMPONENTS.size).toBeGreaterThan(5)
  })

  it('draws exactly the sections groups.ts assigns it', () => {
    for (const group of SETTINGS_GROUPS) {
      const drawn = sectionsUnder(bodyOf(group)).sort()
      expect(drawn, `the ${group} group`).toEqual([...sectionsIn(group)].sort())
    }
  })

  it('draws every section somewhere, exactly once', () => {
    // The failure a restructure actually has: a section quietly left behind in
    // the move. It compiles, nothing complains, and the only symptom is a block
    // of the settings page that no longer exists.
    const drawn = SETTINGS_GROUPS.flatMap((g) => sectionsUnder(bodyOf(g)))
    expect([...drawn].sort()).toEqual([...SETTINGS_SECTIONS].sort())
  })
})

describe('what opens the dialog', () => {
  const APP = readFileSync(new URL('../../App.tsx', import.meta.url), 'utf8')

  it('names a section that exists, never a group', () => {
    const asked = [...APP.matchAll(/setSettingsAt\('([\w]+)'\)/g)].map((m) => m[1])
    expect(asked.length).toBeGreaterThan(0)
    for (const section of asked) {
      expect(SETTINGS_SECTIONS as readonly string[], section).toContain(section)
    }
  })

  it('sends the "states are being guessed" notice to state reporting', () => {
    // The notice exists to lead somewhere, and this is the somewhere. It asks
    // by section, so moving state reporting to another rail item moves the
    // link with it — and the last line says which rail item that is today, so
    // the move is something somebody decides rather than discovers.
    const handler = APP.match(/onOpenSettings=\{\(\) => \{([\s\S]*?)\}\}/)
    expect(handler, 'App.tsx no longer wires the sidebar notice to the settings dialog')
      .not.toBeNull()
    expect(handler![1]).toContain("setSettingsAt('reporting')")
    expect(groupOf('reporting')).toBe('sessions')
  })
})
