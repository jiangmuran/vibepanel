import { describe, expect, it } from 'vitest'

import { disambiguatedLabels, sessionLabel, exitReason } from './label'
import type { Session } from '../protocol/wire'

/**
 * A shell is named after the directory it sits in, so a project containing
 * `services/web` and `admin/web` showed two rows both reading "web". The
 * sidebar exists to answer "which of these needs me", and two identical rows
 * in the same group cannot: you click one to find out, which is the thing this
 * panel was built to stop.
 */
function session(over: Partial<Session>): Session {
  return {
    id: 'x',
    projectId: 'p1',
    tmuxName: 'vp_x',
    title: '',
    titleSource: 'auto',
    state: 'done',
    stateSource: 'heuristic',
    pinned: false,
    sortIndex: null,
    cwd: '/home/u/p',
    command: 'bash',
    cols: 80,
    rows: 24,
    exited: false,
    exitStatus: 0,
    parentSessionId: null,
    lastOutputAt: 0,
    stateChangedAt: 0,
    createdAt: 0,
    ...over,
  } as Session
}

describe('disambiguatedLabels', () => {
  it('leaves distinct names alone', () => {
    const list = [
      session({ id: 'a', title: 'web' }),
      session({ id: 'b', title: 'api' }),
    ]
    const labels = disambiguatedLabels(list)
    expect(labels.get('a')).toBe('web')
    expect(labels.get('b')).toBe('api')
  })

  it('qualifies duplicates with the directory above', () => {
    const list = [
      session({ id: 'a', title: 'web', cwd: '/home/u/proj/services/web' }),
      session({ id: 'b', title: 'web', cwd: '/home/u/proj/admin/web' }),
    ]
    const labels = disambiguatedLabels(list)
    expect(labels.get('a')).toBe('services/web')
    expect(labels.get('b')).toBe('admin/web')
  })

  it('does not qualify across projects', () => {
    // The sidebar prints the project name above each group, so the same name
    // under two projects is already distinguished by where it is. Qualifying
    // it would add a prefix to rows that never needed one.
    const list = [
      session({ id: 'a', title: 'web', projectId: 'p1', cwd: '/home/u/one/web' }),
      session({ id: 'b', title: 'web', projectId: 'p2', cwd: '/home/u/two/web' }),
    ]
    const labels = disambiguatedLabels(list)
    expect(labels.get('a')).toBe('web')
    expect(labels.get('b')).toBe('web')
  })

  it('leaves two sessions in the same directory as they are', () => {
    // Nothing the machine knows tells them apart. That is what renaming is
    // for, and inventing a suffix would only look like information.
    const list = [
      session({ id: 'a', title: 'web', cwd: '/home/u/proj/web' }),
      session({ id: 'b', title: 'web', cwd: '/home/u/proj/web' }),
    ]
    const labels = disambiguatedLabels(list)
    expect(labels.get('a')).toBe('proj/web')
    expect(labels.get('b')).toBe('proj/web')
  })

  it('falls back the way sessionLabel does', () => {
    const s = session({ id: 'a', title: '', command: 'claude' })
    expect(sessionLabel(s)).toBe('claude')
    expect(disambiguatedLabels([s]).get('a')).toBe('claude')
  })
})

describe('exitReason', () => {
  it('names the signals worth recognising', () => {
    // 137 is what a machine running a couple of dozen agents produces when it
    // runs out of memory. It must not read like success.
    expect(exitReason(137)).toBe('killed (SIGKILL)')
    expect(exitReason(139)).toBe('killed (SIGSEGV)')
    expect(exitReason(143)).toBe('killed (SIGTERM)')
  })

  it('falls back to the number for a signal it does not know', () => {
    expect(exitReason(128 + 31)).toBe('killed by signal 31')
  })

  it('leaves ordinary exits alone', () => {
    expect(exitReason(0)).toBe('exited')
    expect(exitReason(1)).toBe('exited with status 1')
    expect(exitReason(3)).toBe('exited with status 3')
    // 128 itself is not a signal death, and neither is a status past the range.
    expect(exitReason(128)).toBe('exited with status 128')
    expect(exitReason(255)).toBe('exited with status 255')
  })
})
