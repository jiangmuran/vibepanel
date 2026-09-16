import { describe, expect, it } from 'vitest'

import { inline, parseReleaseNotes, plainText } from './releaseNotes'

// A real tag message from this repository, hard-wrapped the way git wraps
// them, is the shape this has to get right; the Markdown on top of it is the
// shape GitHub's editor produces when somebody writes the notes there instead.
const TAG_MESSAGE = `Selecting a session is faster and brings back four times as much of it, the
built-in agents are a list you can arrange.

The replay buffer went down to 512 KiB in v1.7.1 to fix a session that took two
and a half seconds to open.

Also: on a tablet the right-hand tab strip spans its row.`

describe('release notes from a tag message', () => {
  it('joins hard-wrapped lines into paragraphs', () => {
    const blocks = parseReleaseNotes(TAG_MESSAGE)
    expect(blocks.map((b) => b.kind)).toEqual(['paragraph', 'paragraph', 'paragraph'])
    expect(plainText(blocks).split('\n')[0]).toBe(
      'Selecting a session is faster and brings back four times as much of it, the built-in agents are a list you can arrange.',
    )
  })

  it('draws headings, bullets and fenced code as what they are', () => {
    const blocks = parseReleaseNotes(
      '## What changed\n\n- a thing that is **important**\n- another, with `code`\n  wrapped onto a second line\n\n```\nsudo vibepanel service upgrade\n```\nThe end.',
    )
    expect(blocks[0]).toEqual({ kind: 'heading', level: 2, runs: [{ kind: 'text', text: 'What changed' }] })
    expect(blocks[1]).toEqual({
      kind: 'list',
      items: [
        [
          { kind: 'text', text: 'a thing that is ' },
          { kind: 'bold', text: 'important' },
        ],
        [
          { kind: 'text', text: 'another, with ' },
          { kind: 'code', text: 'code' },
          { kind: 'text', text: ' wrapped onto a second line' },
        ],
      ],
    })
    expect(blocks[2]).toEqual({ kind: 'code', text: 'sudo vibepanel service upgrade' })
    expect(blocks[3]).toEqual({ kind: 'paragraph', runs: [{ kind: 'text', text: 'The end.' }] })
  })

  it('never produces markup: a link keeps its words and loses its address', () => {
    expect(inline('see [the runbook](https://evil.example/x) for <b>details</b>')).toEqual([
      { kind: 'text', text: 'see the runbook for <b>details</b>' },
    ])
    // The angle brackets are text; the component draws runs as text nodes.
    const blocks = parseReleaseNotes('<script>alert(1)</script>')
    expect(plainText(blocks)).toBe('<script>alert(1)</script>')
  })

  it('caps heading depth and tolerates stray markers', () => {
    expect(parseReleaseNotes('###### deep')[0]).toMatchObject({ kind: 'heading', level: 3 })
    expect(inline('a * b ** c ` d')).toEqual([{ kind: 'text', text: 'a * b ** c ` d' }])
    expect(parseReleaseNotes('')).toEqual([])
    expect(parseReleaseNotes('\n\n  \n')).toEqual([])
  })

  it('takes Windows line endings, which is what a browser-written body has', () => {
    const blocks = parseReleaseNotes('one\r\ntwo\r\n\r\nthree')
    expect(blocks).toHaveLength(2)
    expect(plainText(blocks)).toBe('one two\nthree')
  })
})
