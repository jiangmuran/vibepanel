import { describe, expect, it } from 'vitest'

import type { ArchiveEntry } from '../../protocol/listings'
import { archiveSummary as summary } from '../../protocol/listings'
import { archiveMatches, buildTree, initialOpen, stamp, visibleRows } from './archive'

const file = (path: string, size = 1, more: Partial<ArchiveEntry> = {}): ArchiveEntry => ({ path, size, modified: 0, wall: false, isDir: false, unsafe: false, ...more })
const dir = (path: string): ArchiveEntry => ({ path, size: 0, modified: 0, wall: false, isDir: true, unsafe: false })
const names = (rows: ReturnType<typeof visibleRows>) => rows.map((r) => `${'  '.repeat(r.depth)}${r.node.name}${r.node.dir ? '/' : ''}`)

describe('buildTree', () => {
  it('makes the directories a zip only names files inside, directories first', () => {
    const root = buildTree([file('b.txt'), file('src/z.ts'), file('src/a.ts'), file('a.txt')])
    const open = new Set(['src/'])
    expect(names(visibleRows(root, open))).toEqual(['src/', '  a.ts', '  z.ts', 'a.txt', 'b.txt'])
    expect(root.children[0].count).toBe(2)
  })

  it('folds every directory until it is opened', () => {
    const root = buildTree([file('x/a'), file('y/b')])
    expect(names(visibleRows(root, new Set()))).toEqual(['x/', 'y/'])
  })

  it('takes a listed directory and a file inside it as one directory', () => {
    const root = buildTree([dir('d/'), file('d/f')])
    expect(root.children).toHaveLength(1)
    expect(root.children[0].count).toBe(1)
  })

  it('shows a name that climbs out whole, at the top, and never inside the tree', () => {
    const root = buildTree([file('ok/a'), file('../../etc/passwd', 1, { unsafe: true }), file('/abs', 1, { unsafe: true })])
    const rows = visibleRows(root, new Set(['ok/']))
    expect(names(rows)).toEqual(['../../etc/passwd', '/abs', 'ok/', '  a'])
    expect(rows[0].node.unsafe).toBe(true)
    // Not a directory called `..` with `etc` inside it.
    expect(root.children.some((c) => c.name === '..')).toBe(false)
  })

  it('keeps a file listed twice twice', () => {
    expect(buildTree([file('a'), file('a')]).children).toHaveLength(2)
  })
})

describe('initialOpen', () => {
  it('opens the directories an archive is wrapped in, down to the first with more than one thing', () => {
    const root = buildTree([file('pkg-1.0/src/a.ts'), file('pkg-1.0/src/b.ts'), file('pkg-1.0/src/lib/c.ts')])
    expect([...initialOpen(root)]).toEqual(['pkg-1.0/', 'pkg-1.0/src/'])
  })

  it('opens nothing when the top holds more than one thing, or one file', () => {
    expect(initialOpen(buildTree([file('a/x'), file('b')])).size).toBe(0)
    expect(initialOpen(buildTree([file('only')])).size).toBe(0)
  })
})

describe('archiveMatches', () => {
  it('finds names in the order the tree shows them, with the directories to open', () => {
    const root = buildTree([file('src/readme.md'), file('README'), file('docs/x')])
    const path = (id: string) => id.replace(/#\d+$/, '')
    expect(archiveMatches(root, 'readme').map((m) => [path(m.id), m.parents])).toEqual([
      ['src/readme.md', ['src/']],
      ['README', []],
    ])
    // Smart case: a capital asks for that case.
    expect(archiveMatches(root, 'README').map((m) => path(m.id))).toEqual(['README'])
    expect(archiveMatches(root, '')).toEqual([])
  })
})

describe('summary', () => {
  it('counts files and directories, implied ones included', () => {
    expect(summary([file('a/b/c', 3), file('a/d', 4), dir('e/')])).toEqual({ files: 2, dirs: 3, bytes: 7, sized: true })
  })

  it('has no total when a size is not known', () => {
    expect(summary([file('log', -1)]).sized).toBe(false)
  })
})

describe('stamp', () => {
  it('is local date and minutes, and nothing for no time', () => {
    const d = new Date(2023, 10, 14, 9, 5)
    expect(stamp(d.getTime() / 1000)).toBe('2023-11-14 09:05')
    expect(stamp(0)).toBeNull()
  })

  it('is a wall-clock reading as it was written, whatever the zone', () => {
    // 2024-03-01 12:30 read as UTC, as the server reads a zip's DOS time.
    expect(stamp(Date.UTC(2024, 2, 1, 12, 30) / 1000, true)).toBe('2024-03-01 12:30')
  })

  it('carries the wall mark from the entry to the row', () => {
    const root = buildTree([file('d/a', 1, { modified: 1, wall: true }), file('b', 1, { modified: 1 })])
    expect(root.children.map((c) => (c.dir ? c.children[0].wall : c.wall))).toEqual([true, false])
    // And from a directory the archive lists, with its own time.
    const listed = buildTree([{ ...dir('d/'), modified: 1, wall: true }, file('d/a')])
    expect(listed.children[0].wall).toBe(true)
  })
})
