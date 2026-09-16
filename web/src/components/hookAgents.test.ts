import { describe, expect, it } from 'vitest'

import type { HookStatus } from '../protocol/wire'
import { HOOK_AGENTS, hookAgentInstalled, visibleHookAgents } from './hookAgents'

/** A status with nothing installed anywhere and the default rows offered. */
function status(over: Partial<HookStatus> = {}): HookStatus {
  return {
    settingsPath: '/h/.claude/settings.json',
    scriptPath: '/h/.local/share/vibepanel/hooks/vibepanel-report.sh',
    installed: false,
    events: [],
    snippet: '',
    codexPath: '/h/.codex/hooks.json',
    codexInstalled: false,
    codexEvents: [],
    codexSnippet: '',
    codexTrust: '',
    codexLegacyNotify: false,
    codexSessions: 0,
    codexReporting: 0,
    kimiPath: '/h/.kimi-code/config.toml',
    kimiInstalled: false,
    kimiEvents: [],
    kimiSnippet: '',
    zcodePath: '/h/.zcode/cli/config.json',
    zcodeInstalled: false,
    zcodeEvents: [],
    zcodeSnippet: '',
    agentsShown: ['claude', 'codex', 'opencode'],
    opencodePath: '/h/.config/opencode/plugin/vibepanel.js',
    opencodeInstalled: false,
    ...over,
  }
}

describe('which agents the reporting section offers', () => {
  it('draws the ones the owner asked for, in the table order', () => {
    expect(visibleHookAgents(status())).toEqual(['claude', 'codex', 'opencode'])
  })

  it('draws none when the owner has turned them all off', () => {
    expect(visibleHookAgents(status({ agentsShown: [] }))).toEqual([])
  })

  // Hiding a row would hide the Remove button for a block this panel wrote
  // into somebody's config file, and leave nothing on screen saying it is
  // there. A tick decides what is offered, never what is admitted.
  it('draws an installed agent whatever the setting says', () => {
    const st = status({ agentsShown: ['claude'], kimiInstalled: true })
    expect(visibleHookAgents(st)).toEqual(['claude', 'kimi'])
  })

  it('knows where each agent keeps its installed flag', () => {
    for (const { id } of HOOK_AGENTS) {
      expect(hookAgentInstalled(status(), id)).toBe(false)
    }
    expect(hookAgentInstalled(status({ installed: true }), 'claude')).toBe(true)
    expect(hookAgentInstalled(status({ codexInstalled: true }), 'codex')).toBe(true)
    expect(hookAgentInstalled(status({ kimiInstalled: true }), 'kimi')).toBe(true)
    expect(hookAgentInstalled(status({ zcodeInstalled: true }), 'zcode')).toBe(true)
    expect(hookAgentInstalled(status({ opencodeInstalled: true }), 'opencode')).toBe(true)
  })
})
