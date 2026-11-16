import { describe, expect, it } from 'vitest'

import { crumbs, neighbours } from './nav'

describe('crumbs', () => {
  it('names the root after the project and stops above the file', () => {
    expect(crumbs('src/components/Terminal.tsx', 'vibepanel')).toEqual([
      { name: 'vibepanel', path: '' },
      { name: 'src', path: 'src' },
      { name: 'components', path: 'src/components' },
    ])
  })

  it('is only the root for a file at the root', () => {
    expect(crumbs('README.md', 'p')).toEqual([{ name: 'p', path: '' }])
  })

  it('ignores a leading or doubled slash', () => {
    expect(crumbs('/a//b/c.txt', 'p').map((c) => c.path)).toEqual(['', 'a', 'a/b'])
  })
})

describe('neighbours', () => {
  const files = [{ path: 'a' }, { path: 'b' }, { path: 'c' }]

  it('finds the files either side', () => {
    expect(neighbours(files, 'b')).toEqual({ prev: { path: 'a' }, next: { path: 'c' } })
  })

  it('stops at the ends instead of wrapping', () => {
    expect(neighbours(files, 'a').prev).toBeNull()
    expect(neighbours(files, 'c').next).toBeNull()
  })

  it('has nothing either side of a file that is not listed', () => {
    expect(neighbours(files, 'z')).toEqual({ prev: null, next: null })
  })
})
