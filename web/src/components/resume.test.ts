import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { resumeArgv, resumeIsAmbiguous, resumeProgram, restoreLaunch } from './resume'

/**
 * The rule is written twice, in two languages, and this is what stops the two
 * from drifting.
 *
 * The server decides what a restored session runs and the dialog prints it
 * before anybody presses the button. If the browser's table says `claude` and
 * the server's says `codex`, or one of them learns a third agent, the dialog
 * spends its whole existence lying about the one thing it is for — and nothing
 * would say a word, because both sides type-check and both sides run.
 *
 * Crude on purpose, in the manner of dirpicker.test.ts: it reads the Go file as
 * text. A crude check that runs on every commit is worth more than an exact one
 * that needs a Go toolchain in vitest.
 */
describe('the table on the other side of the wire', () => {
  const go = readFileSync(new URL('../../../internal/session/resume.go', import.meta.url), 'utf8')

  it('finds the Go file at all', () => {
    // Without this the assertions below pass by searching an empty string,
    // which is the failure mode of every checker that reads a file by path.
    expect(go).toContain('func ResumeArgv')
  })

  it('resumes exactly the agents Go resumes', () => {
    // The `case "x":` arms of ResumeArgv's switch, which is the list.
    const arms = [...go.matchAll(/^\tcase "([a-z-]+)":$/gm)].map((m) => m[1])
    expect(arms.length).toBeGreaterThan(0)
    for (const agent of arms) {
      expect(
        resumeArgv([agent]),
        `${agent} resumes in Go and not in the browser`,
      ).not.toBeNull()
    }
    // And nothing this side has invented on its own. opencode is the one that
    // matters: the panel launches it, so it is the obvious thing to add here
    // without measuring it.
    for (const agent of ['opencode', 'aider', 'gemini']) {
      if (arms.includes(agent)) continue
      expect(resumeArgv([agent]), `${agent} resumes in the browser and not in Go`).toBeNull()
    }
  })

  it('spells the flags the same way Go does', () => {
    expect(resumeArgv(['claude'])).toEqual(['claude', '--continue'])
    expect(go).toContain('"--continue"')
    expect(resumeArgv(['codex'])).toEqual(['codex', 'resume', '--last'])
    expect(go).toContain('"resume", "--last"')
  })
})

describe('resumeArgv', () => {
  it('keeps the arguments the session was launched with', () => {
    expect(resumeArgv(['claude', '--model', 'opus'])).toEqual([
      'claude',
      '--continue',
      '--model',
      'opus',
    ])
  })

  it('puts codex’s subcommand first, where codex wants it', () => {
    expect(resumeArgv(['codex', '--model', 'gpt'])).toEqual([
      'codex',
      'resume',
      '--last',
      '--model',
      'gpt',
    ])
  })

  it('reads the program by basename', () => {
    expect(resumeArgv(['/home/x/.local/bin/claude'])).toEqual([
      '/home/x/.local/bin/claude',
      '--continue',
    ])
    expect(resumeProgram(['/home/x/.local/bin/claude'])).toBe('claude')
  })

  it('leaves alone what it cannot bring back', () => {
    expect(resumeArgv([])).toBeNull()
    expect(resumeArgv(['bash', '-l'])).toBeNull()
    expect(resumeArgv(['npm', 'run', 'dev'])).toBeNull()
    expect(resumeArgv(['opencode'])).toBeNull()
  })

  it('does not ask twice for what was asked for by hand', () => {
    expect(resumeArgv(['claude', '--continue'])).toBeNull()
    expect(resumeArgv(['claude', '-c'])).toBeNull()
    expect(resumeArgv(['claude', '--resume', 'abc'])).toBeNull()
    expect(resumeArgv(['codex', 'resume', '--last'])).toBeNull()
  })

  it('does not edit the array it was given', () => {
    const launch = ['claude', '--model', 'opus']
    resumeArgv(launch)
    expect(launch).toEqual(['claude', '--model', 'opus'])
  })
})

describe('two sessions in one directory', () => {
  const rows = [
    { launchCommand: ['claude'], cwd: '/w/api' },
    { launchCommand: ['claude'], cwd: '/w/api' },
    { launchCommand: ['claude'], cwd: '/w/web' },
    { launchCommand: ['codex'], cwd: '/w/api' },
  ]

  it('cannot both resume, because both would take the same conversation', () => {
    expect(resumeIsAmbiguous(rows, 'claude', '/w/api')).toBe(true)
    expect(restoreLaunch(rows[0], rows)).toEqual({ argv: ['claude'], resumed: false })
  })

  it('leaves the one of its kind alone', () => {
    expect(resumeIsAmbiguous(rows, 'claude', '/w/web')).toBe(false)
    expect(restoreLaunch(rows[2], rows)).toEqual({
      argv: ['claude', '--continue'],
      resumed: true,
    })
  })

  it('does not confuse two different agents in one directory', () => {
    // Two transcripts, kept by two different programs in two different places.
    expect(resumeIsAmbiguous(rows, 'codex', '/w/api')).toBe(false)
    expect(restoreLaunch(rows[3], rows)).toEqual({
      argv: ['codex', 'resume', '--last'],
      resumed: true,
    })
  })
})

describe('restoreLaunch', () => {
  it('says nothing at all about a row with no recorded argv', () => {
    // The dialog has its own two sentences for a login shell, and neither of
    // them is about resuming.
    expect(restoreLaunch({ launchCommand: [], cwd: '/w' }, [])).toBeNull()
  })

  it('reports a plain command as itself, started cold', () => {
    const row = { launchCommand: ['npm', 'run', 'dev'], cwd: '/w' }
    expect(restoreLaunch(row, [row])).toEqual({ argv: ['npm', 'run', 'dev'], resumed: false })
  })
})
