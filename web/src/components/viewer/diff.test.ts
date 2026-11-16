import { describe, expect, it } from 'vitest'

import { diffRow, looksLikeDiff } from './diff'

describe('looksLikeDiff', () => {
  it('knows git and unified diffs', () => {
    expect(looksLikeDiff('diff --git a/x b/x\nindex 1..2\n')).toBe(true)
    expect(looksLikeDiff('--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n')).toBe(true)
    expect(looksLikeDiff('Some notes first\n--- old\n+++ new\n')).toBe(true)
  })

  it('does not take a document separator or a rule for one', () => {
    expect(looksLikeDiff('---\nkey: value\n---\nother: 1\n')).toBe(false)
    expect(looksLikeDiff('# Title\n\n---\n\nText\n')).toBe(false)
    expect(looksLikeDiff('--- a\nnot plus\n+++ b\n')).toBe(false)
  })
})

describe('diffRow', () => {
  it('reads each line', () => {
    expect(diffRow('+added')).toBe('add')
    expect(diffRow('-removed')).toBe('del')
    expect(diffRow(' same')).toBe('context')
    expect(diffRow('@@ -1,2 +1,3 @@')).toBe('hunk')
    expect(diffRow('diff --git a/x b/x')).toBe('meta')
    expect(diffRow('index 1111111..2222222 100644')).toBe('meta')
  })

  it('keeps the file header out of the changes', () => {
    expect(diffRow('+++ b/src/app.ts')).toBe('meta')
    expect(diffRow('--- a/src/app.ts')).toBe('meta')
  })
})
