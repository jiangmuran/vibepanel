import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import Markdown from './Markdown'

/**
 * The document renderer, drawn to a string in node. A string is the right
 * thing to inspect here: it is exactly what a browser would be handed, so
 * "no <script> in it" is the claim itself rather than a proxy for it.
 */
const html = (text: string, path = 'docs/guide.md') =>
  renderToStaticMarkup(createElement(Markdown, { text, projectId: 'p1', path }))

describe('a document cannot put markup into the page', () => {
  const hostile = [
    '<script>alert(1)</script>',
    '<img src=x onerror=alert(2)>',
    '<iframe src="https://evil.example"></iframe>',
    '[click](javascript:alert(3))',
    '[tab](java\tscript:alert(4))',
    '[data](data:text/html,<script>alert(5)</script>)',
    '![x](javascript:alert(6))',
    '<a href="javascript:alert(7)">raw</a>',
    '[ref][r]\n\n[r]: javascript:alert(8)',
  ].join('\n\n')
  const out = html(hostile)

  it('draws raw HTML as its text', () => {
    expect(out).not.toMatch(/<script/i)
    expect(out).not.toMatch(/<iframe/i)
    // An event handler as an attribute of a real tag; as escaped text it is
    // the document saying what it said.
    expect(out).not.toMatch(/<[a-z][^>]*\son[a-z]+=/i)
    expect(out).toContain('&lt;script&gt;alert(1)&lt;/script&gt;')
  })

  it('follows no link to a script, however it is written', () => {
    expect(out).not.toMatch(/href="\s*(java|data|vb)/i)
    expect(out).not.toMatch(/src="\s*(java|data)/i)
    // And says where each one pointed.
    expect(out).toContain('(javascript:alert(3))')
  })
})

describe('images', () => {
  it('loads one from the project through the download route', () => {
    const out = html('![logo](img/logo.png)')
    expect(out).toMatch(/<img[^>]+src="\/api\/projects\/p1\/download\?path=docs%2Fimg%2Flogo\.png"/)
    expect(out).toContain('alt="logo"')
  })

  it('never requests one from anywhere else, and shows where it was', () => {
    const out = html('![tracker](https://example.com/pixel.png?u=1)')
    expect(out).not.toMatch(/<img/)
    expect(out).toContain('md-remote-image')
    expect(out).toContain('https://example.com/pixel.png?u=1')
  })

  it('does not climb out of the project for one', () => {
    const out = html('![x](../../../etc/hosts.png)')
    expect(out).not.toMatch(/<img/)
  })
})

describe('GitHub\'s extensions', () => {
  it('draws a table with its alignment', () => {
    const out = html('| a | b | c |\n|:--|:-:|--:|\n| 1 | 2 | 3 |')
    expect(out).toMatch(/<th class="[^"]*text-left[^"]*"[^>]*>a<\/th>/)
    expect(out).toMatch(/<td class="[^"]*text-center[^"]*"[^>]*>2<\/td>/)
    expect(out).toMatch(/<td class="[^"]*text-right[^"]*"[^>]*>3<\/td>/)
  })

  it('draws a task as a checkbox, done or not', () => {
    const out = html('- [x] done\n- [ ] not yet')
    expect(out).toMatch(/data-task="done"[^]*?<input type="checkbox"[^>]*checked=""/)
    expect(out).toMatch(/data-task="open"/)
    expect(out).not.toContain('[x]')
  })

  it('keeps a nested list nested', () => {
    const out = html('- one\n  - nested\n    - deeper\n- two')
    expect((out.match(/<ul/g) ?? []).length).toBe(3)
  })

  it('strikes through, links bare addresses, and numbers footnotes', () => {
    const out = html('~~gone~~ https://example.com and a claim.[^1]\n\n[^1]: Its source.')
    expect(out).toContain('<del>gone</del>')
    expect(out).toMatch(/<a href="https:\/\/example\.com"[^>]*target="_blank"[^>]*rel="noopener noreferrer"/)
    expect(out).toMatch(/<sup id="md-fnref-1"><a href="#md-fn-1"/)
    expect(out).toMatch(/<li id="md-fn-1">/)
  })
})

describe('headings', () => {
  it('get the ids a GitHub link uses, prefixed, and repeats numbered', () => {
    const out = html('# Getting Started\n\n## Notes\n\n## Notes')
    expect(out).toContain('id="md-getting-started"')
    expect(out).toContain('id="md-notes"')
    expect(out).toContain('id="md-notes-1"')
  })
})

describe('a link to another file in the repository', () => {
  it('says where it points and goes nowhere', () => {
    // Followed as a URL it leaves the panel for a path the panel does not serve.
    const out = html('[the api](api.md#routes)')
    expect(out).toContain('md-relative-link')
    expect(out).not.toMatch(/href="api\.md/)
  })
})
