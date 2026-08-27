import { describe, expect, it } from 'vitest'

import type { LaunchProfile } from '../protocol/wire'
import { envCount, looksSecret, profileLabel, profileOf } from './profiles'
import { setLang } from '../i18n'

function profile(p: Partial<LaunchProfile>): LaunchProfile {
  return {
    id: 'p1',
    name: 'a name',
    builtin: false,
    command: [],
    env: [],
    createdAt: 0,
    updatedAt: 0,
    ...p,
  }
}

describe('profileLabel', () => {
  it('translates a built-in, because its name is the server’s', () => {
    setLang('zh')
    expect(profileLabel(profile({ id: 'builtin:shell', name: 'Shell', builtin: true }))).toBe('终端')
    setLang('en')
    expect(profileLabel(profile({ id: 'builtin:shell', name: 'Shell', builtin: true }))).toBe('Shell')
  })

  it('leaves a row’s own name alone in either language', () => {
    setLang('zh')
    expect(profileLabel(profile({ name: 'my gateway' }))).toBe('my gateway')
    setLang('en')
    expect(profileLabel(profile({ name: 'my gateway' }))).toBe('my gateway')
  })

  // A picker reading "builtin:whatever" has put an internal identifier in
  // front of somebody about to press it.
  it('falls back to the server’s name for a built-in this build has not heard of', () => {
    const label = profileLabel(
      profile({ id: 'builtin:something-new', name: 'Something New', builtin: true }),
    )
    expect(label).toBe('Something New')
    expect(label).not.toContain('builtin:')
  })

  // The lookup is keyed on the whole id, so a row can only be translated by
  // having a dictionary entry of its own — which it never has.
  it('does not translate a row whose name is spelled like a built-in', () => {
    setLang('zh')
    expect(profileLabel(profile({ id: 'a1b2c3d4e5f6a7b8', name: 'Shell' }))).toBe('Shell')
    setLang('en')
  })
})

describe('looksSecret', () => {
  it('ticks itself for the names credentials actually have', () => {
    for (const name of [
      'ANTHROPIC_AUTH_TOKEN',
      'OPENAI_API_KEY',
      'MY_SECRET',
      'DB_PASSWORD',
      'aws_credential_file',
    ]) {
      expect(looksSecret(name), name).toBe(true)
    }
  })

  it('leaves an endpoint alone', () => {
    for (const name of ['ANTHROPIC_BASE_URL', 'OPENAI_BASE_URL', 'PATH', 'NO_COLOR']) {
      expect(looksSecret(name), name).toBe(false)
    }
  })
})

describe('envCount', () => {
  // Not env.length. A built-in is a list of variable names with nothing in
  // them, and a row saying "2 variables" next to a profile that sets none is
  // describing the form rather than the session.
  it('counts what will be set, not what is listed', () => {
    const p = profile({
      env: [
        { name: 'A', value: 'x', secret: false, hasValue: true },
        { name: 'B', value: '', secret: false, hasValue: false },
        { name: 'C', value: '', secret: true, hasValue: true },
      ],
    })
    expect(envCount(p)).toBe(2)
  })

  it('is zero for a built-in, whose variables are names waiting to be filled', () => {
    const p = profile({
      builtin: true,
      env: [{ name: 'ANTHROPIC_BASE_URL', value: '', secret: false, hasValue: false }],
    })
    expect(envCount(p)).toBe(0)
  })
})

describe('profileOf', () => {
  const list = [profile({ id: 'p1' }), profile({ id: 'p2' })]

  it('finds one', () => {
    expect(profileOf(list, 'p2')?.id).toBe('p2')
  })

  // Both answers are null, and the caller tells them apart by the id being
  // empty — which is what lets the UI say "the profile is gone" rather than
  // implying the session never had one.
  it('is null for no profile and for a deleted one alike', () => {
    expect(profileOf(list, '')).toBeNull()
    expect(profileOf(list, 'deleted')).toBeNull()
  })
})
