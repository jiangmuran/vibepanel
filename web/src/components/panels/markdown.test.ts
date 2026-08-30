import { describe, expect, it } from 'vitest'
import { isMarkdown, parseInline, parseMarkdown, safeHref } from './markdown'

describe('what a link is allowed to be', () => {
  // The interesting schemes are the ones nobody thinks of. `javascript:` is
  // the obvious one; `data:` is the one that gets forgotten, and this origin
  // holds a session cookie that is a writable terminal.
  it('refuses every scheme but http, https and mailto', () => {
    for (const bad of [
      'javascript:alert(1)',
      'JavaScript:alert(1)',
      '  javascript:alert(1)  ',
      'data:text/html,<script>x</script>',
      'vbscript:x',
      'file:///etc/passwd',
      'jAvAsCrIpT:x',
      // Protocol-relative: inherits the page's scheme and does not look like
      // a link off the panel.
      '//evil.example/x',
    ]) {
      expect(safeHref(bad), bad).toBeNull()
    }
  })

  it('allows the three that are useful, and relative links', () => {
    for (const ok of [
      'https://example.com/x',
      'http://example.com',
      'mailto:me@example.com',
      './docs/design.md',
      '../README.md',
      '#a-heading',
      'docs/api.md#routes',
    ]) {
      expect(safeHref(ok), ok).toBe(ok.trim())
    }
  })

  // A refused link is shown with its target rather than as bare text: what it
  // pointed at is the thing worth seeing.
  it('renders a refused link as text that still says where it pointed', () => {
    const out = parseInline('[click](javascript:alert(1))')
    expect(out).toEqual([{ t: 'text', v: 'click (javascript:alert(1))' }])
  })
})

describe('inline', () => {
  it('reads code, bold, italic, strikethrough and links', () => {
    expect(parseInline('`a`')).toEqual([{ t: 'code', v: 'a' }])
    expect(parseInline('**a**')).toEqual([{ t: 'strong', kids: [{ t: 'text', v: 'a' }] }])
    expect(parseInline('*a*')).toEqual([{ t: 'em', kids: [{ t: 'text', v: 'a' }] }])
    expect(parseInline('~~a~~')).toEqual([{ t: 'del', kids: [{ t: 'text', v: 'a' }] }])
    expect(parseInline('[a](https://x.test)')).toEqual([
      { t: 'link', href: 'https://x.test', kids: [{ t: 'text', v: 'a' }] },
    ])
  })

  // Backticks win, which is most of what people use them for in a README:
  // showing the markers themselves.
  it('lets code suppress the markers inside it', () => {
    expect(parseInline('`**not bold**`')).toEqual([{ t: 'code', v: '**not bold**' }])
  })

  // Strong is tried before emphasis, or `**a**` reads as an empty emphasis
  // followed by a real one.
  it('does not read bold as two italics', () => {
    expect(parseInline('**a**')).toEqual([{ t: 'strong', kids: [{ t: 'text', v: 'a' }] }])
  })

  it('leaves a lone marker alone', () => {
    expect(parseInline('2 * 3 * 4')).toEqual([{ t: 'text', v: '2 * 3 * 4' }])
  })
})

describe('blocks', () => {
  it('reads headings, paragraphs and rules', () => {
    expect(parseMarkdown('# One\n\ntext\n\n---\n')).toEqual([
      { t: 'h', level: 1, kids: [{ t: 'text', v: 'One' }] },
      { t: 'p', kids: [{ t: 'text', v: 'text' }] },
      { t: 'rule' },
    ])
  })

  // The whole point of a fence: what is inside it is not markdown.
  it('does not read markdown inside a fence', () => {
    const out = parseMarkdown('```go\n# not a heading\n- not a list\n```\n')
    expect(out).toEqual([{ t: 'pre', lang: 'go', code: '# not a heading\n- not a list' }])
  })

  it('keeps a wrapped bullet as one item', () => {
    const out = parseMarkdown('- one\n  continued\n- two\n')
    expect(out).toEqual([
      {
        t: 'list',
        ordered: false,
        items: [
          [{ t: 'p', kids: [{ t: 'text', v: 'one\ncontinued' }] }],
          [{ t: 'p', kids: [{ t: 'text', v: 'two' }] }],
        ],
      },
    ])
  })

  it('tells an ordered list from a bulleted one', () => {
    const [list] = parseMarkdown('1. a\n2. b\n')
    expect(list).toMatchObject({ t: 'list', ordered: true })
  })

  it('reads a pipe table', () => {
    const out = parseMarkdown('| a | b |\n|---|---|\n| 1 | 2 |\n')
    expect(out).toEqual([
      {
        t: 'table',
        head: [[{ t: 'text', v: 'a' }], [{ t: 'text', v: 'b' }]],
        rows: [[[{ t: 'text', v: '1' }], [{ t: 'text', v: '2' }]]],
      },
    ])
  })

  it('reads a quote, and markdown inside it', () => {
    expect(parseMarkdown('> **hi**\n')).toEqual([
      { t: 'quote', kids: [{ t: 'p', kids: [{ t: 'strong', kids: [{ t: 'text', v: 'hi' }] }] }] },
    ])
  })

  // Raw HTML is text. It is never parsed, never rendered, and that is the
  // property the whole file exists for: there is no HTML string anywhere in
  // this pipeline for anything to be injected into.
  it('treats raw HTML as text', () => {
    const out = parseMarkdown('<script>alert(1)</script>\n')
    expect(out).toEqual([{ t: 'p', kids: [{ t: 'text', v: '<script>alert(1)</script>' }] }])
  })

  it('survives an unterminated fence', () => {
    expect(parseMarkdown('```\nstuff\n')).toEqual([{ t: 'pre', lang: '', code: 'stuff' }])
  })

  it('is empty for nothing', () => {
    expect(parseMarkdown('')).toEqual([])
    expect(parseMarkdown('\n\n  \n')).toEqual([])
  })
})

describe('which files are markdown', () => {
  it('takes the extensions people use', () => {
    for (const n of ['a.md', 'A.MD', 'r.markdown', 'x.mkd', 'y.mdown']) {
      expect(isMarkdown(n), n).toBe(true)
    }
    for (const n of ['a.mdx', 'a.txt', 'md', 'a.md.bak']) {
      expect(isMarkdown(n), n).toBe(false)
    }
  })
})
