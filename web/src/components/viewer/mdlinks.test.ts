import { describe, expect, it } from 'vitest'

import { isMarkdown, linkKind, projectImage, safeHref, serverImage, slugger } from './mdlinks'

describe('safeHref', () => {
  it('refuses every scheme but http, https and mailto', () => {
    for (const bad of [
      'javascript:alert(1)',
      'JavaScript:alert(1)',
      '  javascript:alert(1)  ',
      'data:text/html,<script>x</script>',
      'vbscript:x',
      'file:///etc/passwd',
      'jAvAsCrIpT:x',
      '//evil.example/x',
      '\\\\evil.example/x',
      '/\\evil.example/x',
    ]) {
      expect(safeHref(bad), bad).toBeNull()
    }
  })

  it('reads the target the way the browser will, whitespace inside it and all', () => {
    // The URL parser deletes tabs and newlines anywhere and drops leading
    // controls: each of these is `javascript:` by the time it is followed.
    for (const bad of ['java\tscript:alert(1)', 'java\nscript:alert(1)', 'jav\r\nascript:x', '\u0001javascript:x', '\u0000 javascript:x']) {
      expect(safeHref(bad), JSON.stringify(bad)).toBeNull()
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
      expect(safeHref(ok), ok).toBe(ok)
    }
  })

  it('says what kind of place a link is', () => {
    expect(linkKind('#x')).toBe('anchor')
    expect(linkKind('https://x')).toBe('external')
    expect(linkKind('mailto:a@b')).toBe('external')
    expect(linkKind('docs/a.md')).toBe('relative')
  })
})

describe('projectImage', () => {
  it('reads a path from the document\'s own directory', () => {
    expect(projectImage('docs/guide.md', 'img/logo.png')).toBe('docs/img/logo.png')
    expect(projectImage('docs/guide.md', './img/logo.png')).toBe('docs/img/logo.png')
    expect(projectImage('docs/guide.md', '../shot.png')).toBe('shot.png')
    expect(projectImage('README.md', 'img/a%20b.png')).toBe('img/a b.png')
    expect(projectImage('README.md', 'img/a.png?raw=true#x')).toBe('img/a.png')
  })

  it('is nothing for what is not a file in the project', () => {
    for (const src of [
      'https://example.com/a.png',
      'http://example.com/a.png',
      '//example.com/a.png',
      'data:image/png;base64,AAAA',
      'file:///etc/passwd',
      '/etc/passwd',
      '\\\\server\\share\\a.png',
      '',
    ]) {
      expect(projectImage('docs/guide.md', src), src).toBeNull()
    }
  })

  it('does not climb out of the project', () => {
    expect(projectImage('docs/guide.md', '../../secret.png')).toBeNull()
    expect(projectImage('README.md', '../outside.png')).toBeNull()
    expect(projectImage('README.md', 'a/../../outside.png')).toBeNull()
    // Encoded, the same climb.
    expect(projectImage('README.md', '%2e%2e/outside.png')).toBeNull()
    expect(projectImage('README.md', '..%2foutside.png')).toBeNull()
  })
})

describe('serverImage', () => {
  it('resolves relative and absolute host paths while refusing remote URLs', () => {
    expect(serverImage('/root/.gemini/brain/id/walkthrough.md', 'shot.png')).toBe(
      '/root/.gemini/brain/id/shot.png',
    )
    expect(serverImage('/root/.gemini/brain/id/walkthrough.md', '/root/.gemini/brain/id/hero.png')).toBe(
      '/root/.gemini/brain/id/hero.png',
    )
    expect(serverImage('/root/.gemini/brain/id/walkthrough.md', 'https://example.com/a.png')).toBeNull()
    expect(serverImage('/root/.gemini/brain/id/walkthrough.md', '//example.com/a.png')).toBeNull()
  })
})

describe('slugger', () => {
  it('makes the ids GitHub makes', () => {
    const slug = slugger()
    expect(slug('Getting Started')).toBe('getting-started')
    expect(slug('What is `fs`?')).toBe('what-is-fs')
    expect(slug('C++ & Rust!')).toBe('c--rust')
    expect(slug('中文 标题')).toBe('中文-标题')
  })

  it('numbers a repeat', () => {
    const slug = slugger()
    expect([slug('Notes'), slug('Notes'), slug('Notes')]).toEqual(['notes', 'notes-1', 'notes-2'])
  })
})

describe('isMarkdown', () => {
  it('knows the extensions', () => {
    for (const n of ['a.md', 'B.MARKDOWN', 'c.mdown', 'd.mkd']) expect(isMarkdown(n), n).toBe(true)
    for (const n of ['a.mdx', 'md', 'a.txt']) expect(isMarkdown(n), n).toBe(false)
  })
})
