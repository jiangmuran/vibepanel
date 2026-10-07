import { describe, expect, it } from 'vitest'

import type { PluginRow, PluginScreen } from '../../protocol/wire'
import { canConfirm, capOf, capsToSend, grouped, initialTicks, listLines, rungsOf, stateOf, textIn } from './screen'

const screen: PluginScreen = {
  confirm: 'grant',
  lines: [
    { kind: 'what', tone: 'plain', text: { en: 'Stand-up 0.3.0', 'zh-CN': '站会 0.3.0' }, code: 'standup' },
    { kind: 'rung', tone: 'plain', text: { en: 'Adds a pane.', 'zh-CN': '加一个 pane。' }, code: 'panel' },
    { kind: 'cap', tone: 'red', text: { en: 'can type', 'zh-CN': '能输入' }, code: 'sessions:input', checkable: true, granted: false },
    { kind: 'cap', tone: 'plain', text: { en: 'can see', 'zh-CN': '能看到' }, code: 'read:panel', checkable: true, granted: true },
    { kind: 'host', tone: 'enforced', text: { en: 'will contact api.example.com (enforced)' }, code: 'api.example.com', checkable: true, granted: true },
    { kind: 'host', tone: 'amber', text: { en: 'declares slack.com' }, code: 'slack.com' },
    { kind: 'keeps', tone: 'plain', text: { en: 'keeps data' }, code: 'digest' },
  ],
}

describe('the install screen', () => {
  it('draws the text in the reader\'s language and falls back to English', () => {
    expect(textIn(screen.lines[0].text, 'zh')).toBe('站会 0.3.0')
    expect(textIn(screen.lines[0].text, 'en')).toBe('Stand-up 0.3.0')
    expect(textIn(screen.lines[4].text, 'zh')).toBe('will contact api.example.com (enforced)')
    expect(textIn(undefined, 'zh')).toBe('')
  })

  it('groups the lines under the headings in the screen\'s order', () => {
    expect(grouped(screen).map((g) => g.heading)).toEqual(['what', 'rung', 'cap', 'host', 'keeps'])
    expect(grouped(screen)[2].lines.map((l) => l.code)).toEqual(['sessions:input', 'read:panel'])
  })

  it('ticks what the server marked granted and sends exactly the ticked boxes', () => {
    const ticks = initialTicks(screen)
    expect([...ticks].sort()).toEqual(['api.example.com', 'read:panel'])
    // A host line's capability is net:<host>; a declared host is not a box.
    expect(capOf(screen.lines[4])).toBe('net:api.example.com')
    expect(capOf(screen.lines[5])).toBeNull()
    expect(capsToSend(screen, ticks)).toEqual(['read:panel', 'net:api.example.com'])
    ticks.add('sessions:input')
    ticks.delete('read:panel')
    expect(capsToSend(screen, ticks)).toEqual(['sessions:input', 'net:api.example.com'])
  })

  it('cannot be confirmed while refused or busy', () => {
    expect(canConfirm(screen, false)).toBe(true)
    expect(canConfirm(screen, true)).toBe(false)
    expect(canConfirm({ ...screen, refused: screen.lines[0] }, false)).toBe(false)
  })
})

describe('a plugin card', () => {
  const row = (over: Partial<PluginRow>): PluginRow => ({
    id: 'x', enabled: false, installedVersion: 0, sourceDir: '', dev: false, createdAt: 0, updatedAt: 0,
    name: { en: 'X' }, version: '1.0.0', rungs: { theme: true, panel: false, service: true, process: false, unsandboxed: false },
    granted: [], wanted: [], secrets: [], latestVersion: 1, problems: [], panels: [],
    ...over,
  })

  it('says what state a plugin is in, with the version that matters', () => {
    expect(stateOf(row({}))).toEqual({ key: 'new', tone: 'warn', v: 1 })
    expect(stateOf(row({ installedVersion: 1, enabled: true }))).toEqual({ key: 'enabled', tone: 'accent', v: 1 })
    expect(stateOf(row({ installedVersion: 1 }))).toEqual({ key: 'disabled', tone: 'plain', v: 1 })
    expect(stateOf(row({ installedVersion: 1, enabled: true, latestVersion: 2 }))).toEqual({ key: 'upgrade', tone: 'warn', v: 2 })
  })

  it('lists the rungs in ladder order', () => {
    expect(rungsOf(row({}))).toEqual(['theme', 'service'])
  })

  it('reads a list setting one item per line', () => {
    expect(listLines(' a \n\nb\n')).toEqual(['a', 'b'])
  })
})
