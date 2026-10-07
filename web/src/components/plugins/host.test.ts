import { describe, expect, it } from 'vitest'

import type { PluginRow } from '../../protocol/wire'
import { allowed, capFor, entriesAt, extTabs, pageEntry, parseFrameMessage, runs, settled } from './host'

const row = (over: Partial<PluginRow>): PluginRow => ({
  id: 'standup', enabled: true, installedVersion: 1, sourceDir: '', dev: false, createdAt: 0, updatedAt: 0,
  name: { en: 'Stand-up' }, version: '1.0.0', rungs: { theme: false, panel: true, service: false, process: false, unsandboxed: false },
  granted: ['read:panel'], wanted: ['read:panel', 'ui:open'], secrets: [], latestVersion: 1, problems: [],
  panels: [
    { slot: 'sidepanel.pane', entry: 'pane.html', title: { en: 'Stand-up' }, icon: 'list-checks' },
    { slot: 'settings.section', entry: 's.html', title: { en: 'Stand-up' }, group: 'notify' },
    { slot: 'page', entry: 'index.html', title: { en: 'Stand-up' }, path: 'standup' },
  ],
  ...over,
})

describe('which plugins run, and where', () => {
  it('runs an enabled installed plugin, or a draft in dev mode, and nothing else', () => {
    expect(runs(row({}))).toBe(true)
    expect(runs(row({ enabled: false }))).toBe(false)
    expect(runs(row({ installedVersion: 0 }))).toBe(false)
    expect(runs(row({ enabled: false, installedVersion: 0, dev: true, sourceDir: '/d' }))).toBe(true)
    expect(runs(row({ enabled: false, dev: true, sourceDir: '' }))).toBe(false)
  })

  it('places each frame at its slot, in install order, by its index in the manifest', () => {
    const list = [row({}), row({ id: 'other', panels: [{ slot: 'sidepanel.pane', entry: 'p.html', title: { en: 'O' } }] })]
    expect(extTabs(list).map((x) => x.id)).toEqual(['ext:standup:0', 'ext:other:0'])
    expect(entriesAt(list, 'settings.section').map((e) => e.spec.group)).toEqual(['notify'])
    expect(pageEntry(list, 'standup')?.plugin.id).toBe('standup')
    expect(pageEntry(list, 'nope')).toBeNull()
    expect(extTabs([row({ enabled: false })])).toEqual([])
  })
})

describe('what a frame may say', () => {
  it('rebuilds a message from its checked fields and refuses the rest', () => {
    expect(parseFrameMessage({ type: 'height', px: 123.6 })).toEqual({ type: 'height', px: 124 })
    expect(parseFrameMessage({ type: 'height', px: 1e9 })).toEqual({ type: 'height', px: 4000 })
    expect(parseFrameMessage({ type: 'open', id: 1, session: '0123456789abcdef', extra: 'x' })).toEqual({
      type: 'open', id: 1, session: '0123456789abcdef',
    })
    // A real id is not a handle; a handle is sixteen hex characters.
    expect(parseFrameMessage({ type: 'open', id: 1, session: 'real-id-here' })).toBeNull()
    expect(parseFrameMessage({ type: 'open', id: 0, session: '0123456789abcdef' })).toBeNull()
    expect(parseFrameMessage({ type: 'notify', id: 2, text: 'x'.repeat(900), kind: 'danger' })).toEqual({
      type: 'notify', id: 2, text: 'x'.repeat(500), kind: 'info',
    })
    expect(parseFrameMessage({ type: 'confirm', id: 3, title: 'Sure?', destructive: 'yes' })).toEqual({
      type: 'confirm', id: 3, title: 'Sure?', body: '', confirm: '', cancel: '', destructive: false,
    })
    expect(parseFrameMessage({ type: 'menu', id: 4, items: [{ label: 'A' }, { id: 'b', label: 'B', destructive: true }, 'junk'] })).toEqual({
      type: 'menu', id: 4, items: [{ id: 'A', label: 'A', destructive: false }, { id: 'b', label: 'B', destructive: true }],
    })
    expect(parseFrameMessage({ type: 'eval', code: 'x' })).toBeNull()
    expect(parseFrameMessage('ready')).toBeNull()
    expect(parseFrameMessage(null)).toBeNull()
  })

  it('needs ui:open to open and ui:notify to notify, and nothing for the rest', () => {
    const p = row({ granted: ['read:panel', 'ui:notify'] })
    const open = parseFrameMessage({ type: 'open', id: 1, project: '0123456789abcdef' })!
    const notify = parseFrameMessage({ type: 'notify', id: 2, text: 'hi' })!
    expect(capFor(open)).toBe('ui:open')
    expect(capFor(notify)).toBe('ui:notify')
    expect(capFor({ type: 'height', px: 1 })).toBeNull()
    expect(allowed(p, open)).toBe(false)
    expect(allowed(p, notify)).toBe(true)
    expect(allowed(p, { type: 'ready' })).toBe(true)
  })
})

describe('dev mode', () => {
  it('reloads when two readings agree on something new, not on the first change', () => {
    expect(settled(['a'], 'a')).toBe(false)
    expect(settled(['a', 'b'], 'a')).toBe(false)
    expect(settled(['b', 'b'], 'a')).toBe(true)
    expect(settled(['b', 'b'], 'b')).toBe(false)
  })
})
