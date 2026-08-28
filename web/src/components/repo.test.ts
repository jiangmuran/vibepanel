import { describe, expect, it } from 'vitest'

import { githubURL } from './repo'
import type { GitRemote } from '../protocol/wire'

const at = (over: Partial<GitRemote>): GitRemote => ({
  url: 'git@github.com:acme/payroll.git',
  host: 'github.com',
  owner: 'acme',
  name: 'payroll',
  ...over,
})

describe('the repository link', () => {
  it('is built from the owner and the name', () => {
    expect(githubURL(at({}))).toBe('https://github.com/acme/payroll')
  })

  it('is never built from the remote string itself', () => {
    // The one property worth stating outright: whatever is in `url`, the href
    // is the two parsed halves. A config full of nonsense produces either a
    // github.com URL or nothing.
    const evil = at({ url: 'data:text/html,<script>fetch(1)</script>' })
    expect(githubURL(evil)).toBe('https://github.com/acme/payroll')
  })

  it('refuses a host this panel does not link to', () => {
    // Not because other forges are unwelcome — because `github.com/x/y` is the
    // only path shape this function knows, and guessing an Enterprise install's
    // web host from a git remote is how a panel that promises not to phone home
    // resolves a hostname somebody's DNS points anywhere.
    for (const host of ['gitlab.com', 'github.example.com', 'evil.com', '']) {
      expect(githubURL(at({ host })), host).toBeNull()
    }
  })

  it('refuses a half that is missing', () => {
    expect(githubURL(at({ owner: '' }))).toBeNull()
    expect(githubURL(at({ name: '' }))).toBeNull()
    expect(githubURL(null)).toBeNull()
    expect(githubURL(undefined)).toBeNull()
  })

  it('refuses anything that would make the path mean something else', () => {
    // Every one of these is a string the server's parser already rejects. The
    // wall is here as well because this is the side that writes the href, and
    // "the parser is correct" is a different claim from "the link is safe".
    const bad = [
      '..',
      'a/b',
      'a?b',
      'a#b',
      'a b',
      'a\\b',
      'a%2Fb',
      '@evil.com',
      'a:b',
      'a\nb',
    ]
    for (const v of bad) {
      expect(githubURL(at({ owner: v })), `owner ${JSON.stringify(v)}`).toBeNull()
      expect(githubURL(at({ name: v })), `name ${JSON.stringify(v)}`).toBeNull()
    }
  })

  it('allows what a real repository is called', () => {
    for (const v of ['dot.name', 'with-dash', 'under_score', 'v2', 'A1']) {
      expect(githubURL(at({ name: v })), v).toBe(`https://github.com/acme/${v}`)
    }
  })

  it('only ever produces an https github.com URL', () => {
    // The property, rather than the cases. Whatever comes out, a reader clicking
    // it lands on github.com over TLS or nowhere at all.
    for (const owner of ['acme', 'a.b', 'x', '..', 'a/b', '']) {
      for (const name of ['payroll', 'a-b', '', 'a?b']) {
        const url = githubURL(at({ owner, name }))
        if (url === null) continue
        expect(url.startsWith('https://github.com/'), `${owner}/${name} -> ${url}`).toBe(true)
        expect(new URL(url).host).toBe('github.com')
        expect(new URL(url).pathname.split('/').filter(Boolean)).toHaveLength(2)
      }
    }
  })
})
