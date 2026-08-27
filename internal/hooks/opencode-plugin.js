// vibepanel session state reporter for opencode.
//
// Dropped into ~/.config/opencode/plugin/, which opencode auto-discovers:
// every *.js and *.ts in there is loaded with no config entry at all. Verified
// on this machine rather than assumed -- `opencode debug config` reports it as
// `plugin_origins: [{ scope: "global" }]`. That is why installing this touches
// no configuration file, unlike the Claude Code and Codex paths, which have to
// edit JSON and TOML somebody else owns.
//
// Safe to install globally: outside a vibepanel session the three environment
// variables below are absent and the plugin returns no hooks at all, so an
// opencode started from an ordinary terminal is not merely unaffected, it is
// running nothing of ours.
//
// Never fail, never block, never print -- the same rule as report.sh, and for
// the same reason: a hook that makes an agent wait is far more expensive than a
// missed state update. Every send is fire-and-forget with a two-second ceiling,
// and every error is swallowed. The hooks return immediately; they do not await
// the request, because opencode awaits them.

const sessionID = process.env.VIBEPANEL_SESSION_ID
const token = process.env.VIBEPANEL_TOKEN
const url = process.env.VIBEPANEL_URL

function report(state) {
  try {
    fetch(`${url}/api/hook/state`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ sessionId: sessionID, state }),
      signal: AbortSignal.timeout(2000),
      // The destination is loopback, and when the panel serves TLS its
      // certificate is issued for the public hostname, which 127.0.0.1 will
      // never match. Bun honours this; on a runtime that does not, the property
      // is ignored and an https panel simply misses the update.
      tls: { rejectUnauthorized: false },
    }).catch(() => {})
  } catch {
    // Nothing here is worth interrupting an agent for.
  }
}

export const VibepanelState = async () => {
  if (!sessionID || !token || !url) return {}
  return {
    // The three states the panel knows about, and nothing else. They are bare
    // literals here exactly as they are in internal/hooks, because this file
    // does not import the enum and cannot: see red line 3 in AGENTS.md.
    'chat.message': async () => {
      report('working')
    },
    'tool.execute.before': async () => {
      report('working')
    },
    // The agent is asking for permission, which is the one thing that means a
    // person is needed. This is opencode's equivalent of Claude Code's
    // Notification hook.
    'permission.ask': async () => {
      report('waiting')
    },
    event: async ({ event }) => {
      // session.idle is opencode's "the turn is over", which is what Stop is
      // for Claude Code. Every other event is ignored rather than mapped to
      // something plausible -- a wrong state is worse than a guessed one.
      if (event && event.type === 'session.idle') report('done')
    },
  }
}

export default VibepanelState
