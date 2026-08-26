import { describe, expect, it } from 'vitest'

import { disambiguatedLabels, passkeyLabel, projectLabel, sessionLabel, terminalLabel, exitReason } from './label'
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

describe('projectLabel', () => {
  it('sanitises a name that came from a directory', () => {
    // A project's name defaults to the basename of its directory, and an agent
    // creates directories. The same override that reverses a filename reverses
    // this, in a sidebar row and in the confirm that kills every session in it.
    expect(projectLabel({ name: 'billing\u202Egnp.hs' })).not.toContain('\u202E')
    expect(projectLabel({ name: 'billing' })).toBe('billing')
  })

  it('never renders a project row with no text in it', () => {
    // A row with no label cannot be identified, and cannot be clicked back
    // into to fix the name that made it. InlineName and the CLI both guard
    // their own entrance; `--name "   "` walks past both.
    expect(projectLabel({ name: '   ', path: '/home/you/projects/billing' })).toBe('billing')
    expect(projectLabel({ name: '', path: '/home/you/projects/billing/' })).toBe('billing')
    expect(projectLabel({ name: '', path: '' })).toBe('project')
    expect(projectLabel({ name: '\u200B', path: '' })).not.toBe('')
  })
})

describe('terminalLabel', () => {
  const term = (title: string) => ({ title }) as Session

  it('sanitises a title that came from pane_title', () => {
    expect(terminalLabel(term('build\u202Egnp.hs'), 0)).not.toContain('\u202E')
  })

  it('numbers a terminal the server chose not to name', () => {
    // Not a fallback to the command: every shell is called "bash", and a strip
    // of tabs all reading "bash" says nothing. The empty title is the server's
    // judgement, not a gap to fill.
    expect(terminalLabel(term(''), 0)).toBe('term 1')
    expect(terminalLabel(term(''), 2)).toBe('term 3')
  })
})

describe('passkeyLabel', () => {
  it('sanitises a name before it reaches the confirm that deletes the key', () => {
    // The fourth name-rendering site and the only one that was not funnelled.
    // Lower stakes than the others -- this name is typed rather than taken from
    // a directory or a pane title -- but the dialog is the last thing between a
    // credential and being deleted, and a name carrying an override can make it
    // ask about a different key than the one it removes.
    expect(passkeyLabel({ name: 'phone\u202Epotpal' })).not.toContain('\u202E')
  })

  it('falls back rather than rendering an empty row', () => {
    expect(passkeyLabel({ name: '' })).toBe('Passkey')
    expect(passkeyLabel({ name: '   ' })).toBe('Passkey')
    expect(passkeyLabel({})).toBe('Passkey')
  })

  it('shows a name made only of control characters rather than hiding it', () => {
    // This was written the other way round first, expecting the fallback, and
    // the test was wrong rather than the code: safeText *replaces* with U+FFFD
    // rather than stripping, deliberately, so that a name which contained
    // something deceptive looks wrong instead of looking short. Falling back to
    // "Passkey" here would hide exactly the thing the sanitising exists to
    // show, and in the dialog that deletes the credential.
    const label = passkeyLabel({ name: '\u202E\u200B' })
    expect(label).not.toBe('Passkey')
    expect(label).not.toContain('\u202E')
    expect(label).toBe('\uFFFD\uFFFD')
  })

  it('keeps an ordinary name intact', () => {
    expect(passkeyLabel({ name: 'work laptop' })).toBe('work laptop')
  })
})

describe('sessionLabel sanitising', () => {
  it('strips an override out of a title a pane set', () => {
    // Removing safeText from sessionLabel used to pass every test in the
    // project. The function is the funnel three call sites go through, and
    // nothing asserted the one thing the funnel exists for -- its own tests
    // were all about the fallback chain, which is the other half.
    //
    // `title` is whatever `pane_title` held, and any program running in a pane
    // sets that with a two-byte escape sequence, so this is the least
    // trustworthy name in the panel rather than the most.
    const s = session({ id: 'a', title: 'deploy\u202Egnp.sh', command: 'bash' })
    expect(sessionLabel(s)).not.toContain('\u202E')
  })

  it('strips the invisibles that make two rows the same pixels', () => {
    // Not a bidi override: a zero-width space between two rows that otherwise
    // read identically. The sidebar exists to answer "which of these needs
    // me", and two rows that cannot be told apart is the failure it was built
    // to prevent -- you click one to find out, which is typing into an agent
    // you did not choose.
    const s = session({ id: 'a', title: 'dep\u200Bloy', command: 'bash' })
    expect(sessionLabel(s)).not.toContain('\u200B')
  })

  it('sanitises the command it falls back to', () => {
    // The fallback is not safer than the title. `command` is
    // #{pane_current_command}, which is a process name, and a process can be
    // named anything a filesystem allows.
    const s = session({ id: 'a', title: '', command: 'node\u202Ehs.yolp' })
    expect(sessionLabel(s)).not.toContain('\u202E')
  })
})
