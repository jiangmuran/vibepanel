import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { envTemplateFor } from './profiles'
import type { LaunchProfile } from '../protocol/wire'

const p = (id: string, command: string[], env: string[], builtin = true): LaunchProfile => ({
  id,
  name: id,
  builtin,
  command,
  env: env.map((name) => ({ name, value: '', secret: /KEY|TOKEN/.test(name), hasValue: false })),
  createdAt: 0,
  updatedAt: 0,
})

const catalogue = [
  p('builtin:shell', [], []),
  p('builtin:claude', ['claude'], ['ANTHROPIC_BASE_URL', 'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_MODEL']),
  p('builtin:codex', ['codex'], ['OPENAI_BASE_URL', 'OPENAI_API_KEY']),
  p('builtin:opencode', ['opencode'], []),
]

/**
 * The variables an agent reads, for a command somebody typed.
 *
 * Read out of the catalogue the server already sends rather than from a table
 * on this side, which is the whole design: the names live in
 * store.builtinProfiles and a second copy here would be a second thing to keep
 * right. What these check is the matching, which is the part that can be wrong
 * without anybody noticing -- a template that never appears looks exactly like
 * a feature nobody used.
 */
describe('the environment template', () => {
  it('matches on the program, not the whole command line', () => {
    // Both of these are what people type. `claude --model x` is the reason the
    // field exists at all.
    expect(envTemplateFor(['claude'], catalogue).map((v) => v.name)).toEqual([
      'ANTHROPIC_BASE_URL',
      'ANTHROPIC_AUTH_TOKEN',
      'ANTHROPIC_MODEL',
    ])
    expect(envTemplateFor(['claude', '--model', 'x'], catalogue)).toHaveLength(3)
    expect(envTemplateFor(['/usr/local/bin/claude'], catalogue)).toHaveLength(3)
    expect(envTemplateFor(['codex'], catalogue).map((v) => v.name)).toEqual([
      'OPENAI_BASE_URL',
      'OPENAI_API_KEY',
    ])
  })

  it('carries the secret flag and never a value', () => {
    // An empty value is not passed to the process, so a form filled from this
    // runs the agent exactly as a bare terminal would until somebody types.
    //
    // The catalogue here is given values it does not have in practice, on
    // purpose: with empty ones the assertion passes against a build that
    // copies the entry wholesale, and that is what it is here to catch. A
    // future built-in that ships a default, or a server that stops redacting,
    // must not put a value into somebody's new profile without them typing it.
    const withValues = [
      p('builtin:shell', [], []),
      {
        ...p('builtin:claude', ['claude'], ['ANTHROPIC_BASE_URL', 'ANTHROPIC_AUTH_TOKEN']),
        env: [
          { name: 'ANTHROPIC_BASE_URL', value: 'https://left-over', secret: false, hasValue: true },
          { name: 'ANTHROPIC_AUTH_TOKEN', value: 'sk-left-over', secret: true, hasValue: true },
        ],
      },
    ]
    const got = envTemplateFor(['claude'], withValues)
    expect(got.map((v) => v.value)).toEqual(['', ''])
    expect(got.map((v) => v.hasValue)).toEqual([false, false])
    expect(got.find((v) => v.name === 'ANTHROPIC_AUTH_TOKEN')?.secret).toBe(true)
    expect(got.find((v) => v.name === 'ANTHROPIC_BASE_URL')?.secret).toBe(false)
  })

  it('offers nothing it would have to guess', () => {
    // A shell, a build, an agent the catalogue names no variables for, and one
    // it has never heard of. Guessing a name for opencode is what the
    // catalogue's own comment refuses to do.
    for (const cmd of [[], [''], ['sh'], ['make', 'build'], ['opencode'], ['some-new-agent']]) {
      expect(envTemplateFor(cmd, catalogue)).toEqual([])
    }
  })

  it('takes nothing from the owner s own profiles', () => {
    // A row that happens to run `claude` is a configuration, not a catalogue
    // entry, and reading variable names out of one would put somebody else s
    // choices into a new form.
    const mine = [p('abc123', ['claude'], ['MY_OWN_THING'], false)]
    expect(envTemplateFor(['claude'], mine)).toEqual([])
  })

  it('goes away when the agent is removed from the list', () => {
    // A hidden built-in is not in what the server sends, so its template is
    // not offered either -- which is the right way round: somebody who took an
    // agent out of their panel is not the person who wants its variables.
    const without = catalogue.filter((x) => x.id !== 'builtin:codex')
    expect(envTemplateFor(['codex'], without)).toEqual([])
  })
})

describe('and the editor offers it', () => {
  const code = readFileSync(new URL('./LaunchProfiles.tsx', import.meta.url), 'utf8')
    .replace(/^\s*\/\*[\s\S]*?\*\/\s*$/gm, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('recomputes from the command as it is typed', () => {
    expect(code).toMatch(/envTemplateFor\(splitArgv\(draft\.command\), catalogue\)/)
  })

  it('offers rather than applies', () => {
    // Opening the editor and finding three rows nobody added is the panel
    // deciding something. It has to be a button.
    expect(code).toMatch(/data-testid="profile-env-template"/)
    expect(code).toMatch(/env: \[\.\.\.draft\.env, \.\.\.template\]/)
  })

  it('stops offering the names that are already there', () => {
    // Without this the button stays after it has been used and pressing it
    // twice gives six rows, half of them duplicates.
    expect(code).toMatch(/known\.filter\(\(v\) => !draft\.env\.some\(/)
  })
})
