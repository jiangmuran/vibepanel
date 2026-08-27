import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { FORBIDDEN_SANDBOX_TOKEN, canRender, sandboxFor } from './render'

describe('the sandbox a rendered preview is framed in', () => {
  it('grants nothing at all by default', () => {
    // The default is the whole safety argument for turning HTML previews on:
    // a document that cannot execute is a picture. If this ever returns a
    // non-empty string, every HTML file in every project became executable
    // with no visible change on screen.
    expect(sandboxFor(false)).toBe('')
  })

  it('grants scripts and nothing else when asked', () => {
    expect(sandboxFor(true)).toBe('allow-scripts')
  })

  it('never grants the panel its own origin', () => {
    // allow-same-origin is the one token that turns this from an isolated
    // frame into a page on the origin holding the session cookie. Asserted for
    // both answers rather than for the interesting one, because the way this
    // gets added is "the preview could not load its stylesheet".
    for (const scripts of [false, true]) {
      expect(sandboxFor(scripts)).not.toContain(FORBIDDEN_SANDBOX_TOKEN)
    }
  })

  it('never grants popups, modals, top navigation, downloads or forms', () => {
    const forbidden = [
      'allow-same-origin',
      'allow-popups',
      'allow-popups-to-escape-sandbox',
      'allow-modals',
      'allow-top-navigation',
      'allow-top-navigation-by-user-activation',
      'allow-downloads',
      'allow-forms',
    ]
    for (const scripts of [false, true]) {
      const value = sandboxFor(scripts)
      for (const token of forbidden) {
        expect(value.split(' ')).not.toContain(token)
      }
    }
  })
})

describe('the two sides of the sandbox agree', () => {
  // The effective sandbox is the intersection of this attribute and the CSP
  // `sandbox` directive on the response, so the two are written in two
  // languages and must say the same thing. Neither compiler sees the other.
  const go = readFileSync(
    new URL('../../../../internal/httpapi/preview_render.go', import.meta.url).pathname,
    'utf8',
  )

  it('agrees that scripts are the only token ever granted', () => {
    expect(go).toContain('return "allow-scripts"')
  })

  it('agrees that the server never emits allow-same-origin either', () => {
    // Searched over the whole file including its prose, so a comment
    // mentioning the token keeps this honest only if the code never emits it.
    // The narrower assertion is the one that matters: no quoted CSP or sandbox
    // value contains it.
    const emitted = go.match(/"[^"\n]*allow-[^"\n]*"/g) ?? []
    expect(emitted.length).toBeGreaterThan(0)
    for (const value of emitted) {
      expect(value).not.toContain(FORBIDDEN_SANDBOX_TOKEN)
    }
  })
})

describe('what the panel will render', () => {
  it('takes the answer from the server and only the two it knows', () => {
    expect(canRender('html')).toBe(true)
    expect(canRender('svg')).toBe(true)
  })

  it('refuses a kind this build has no isolation story for', () => {
    // An older tab against a newer server. Passing an unknown value through to
    // an iframe would be this build rendering something it was never designed
    // around; showing it as text is always true.
    expect(canRender('pdf')).toBe(false)
    expect(canRender('xml')).toBe(false)
    expect(canRender('')).toBe(false)
    expect(canRender(null)).toBe(false)
  })
})
