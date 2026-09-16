import type { HookAgent, HookStatus } from '../protocol/wire'
import { t } from '../i18n'

type DictKey = Parameters<typeof t>[0]

/**
 * The agents the panel can report state for, in the order they are offered.
 *
 * One table, read by the settings section and by the tour. They had a list
 * each, and the two disagreed about the order the day a fourth agent was added
 * to one of them -- which is the cheap version of the failure red line 3 is
 * about: two places describing one fact, with nothing to notice when they stop
 * agreeing. A Go test compares the ids here against the server's own list.
 */
export const HOOK_AGENTS: { id: HookAgent; nameKey: DictKey }[] = [
  { id: 'claude', nameKey: 'set.claudeCode' },
  { id: 'codex', nameKey: 'set.codex' },
  { id: 'kimi', nameKey: 'set.kimiCode' },
  { id: 'zcode', nameKey: 'set.zcode' },
  { id: 'opencode', nameKey: 'set.opencode' },
]

export function hookAgentName(id: HookAgent): string {
  const key = HOOK_AGENTS.find((a) => a.id === id)?.nameKey
  // The id is not a name, but it is the agent's own name in every case here,
  // and a row labelled "zcode" reads better than one labelled nothing.
  return key ? t(key) : id
}

/** Whether this panel has written that agent's hooks into its config. */
export function hookAgentInstalled(st: HookStatus, id: HookAgent): boolean {
  switch (id) {
    case 'claude':
      return st.installed
    case 'codex':
      return st.codexInstalled
    case 'kimi':
      return st.kimiInstalled
    case 'zcode':
      return st.zcodeInstalled
    case 'opencode':
      return st.opencodeInstalled
  }
}

/**
 * Which agents get a row.
 *
 * The setting, plus every agent whose hooks are actually installed. The second
 * half is not a convenience: hiding a row would hide the Remove button for a
 * block this panel wrote into somebody's config file, and leave no way to find
 * out it is there. A tick controls what is offered, never what is admitted.
 */
export function visibleHookAgents(st: HookStatus): HookAgent[] {
  return HOOK_AGENTS.filter(
    (a) => st.agentsShown.includes(a.id) || hookAgentInstalled(st, a.id),
  ).map((a) => a.id)
}
