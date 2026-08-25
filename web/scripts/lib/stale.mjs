import { execSync } from 'node:child_process'
import { readdirSync, readFileSync, unlinkSync } from 'node:fs'
import { join } from 'node:path'

/**
 * Kills tmux servers left behind by harness runs that were killed outright.
 *
 * Every check registers cleanup on SIGINT, SIGTERM and SIGHUP, which covers a
 * `timeout` and a Ctrl-C. It cannot cover SIGKILL, and it does not have to be
 * often: one interrupted run leaves a tmux server holding eight sessions, and
 * it stays there. One was found six hours old, still running an htop.
 *
 * That matters more here than in most projects. The entire premise is that
 * this panel does not disturb what is already running on the machine, and a
 * test suite that quietly accumulates processes is the same failure wearing a
 * different hat.
 *
 * Safety rests on the socket name, which every check builds as
 * `<prefix>-<its own pid>`:
 *
 *   - the name must match a harness prefix and end in digits, so a socket
 *     somebody else made is never a candidate — the panel's own default is
 *     `vibepanel`, with nothing after it;
 *   - the pid in the name must not be running. If a pid has been reused by an
 *     unrelated process the socket is skipped, which is the safe direction:
 *     the failure mode is leaving debris, never killing something live.
 */
const HARNESS_SOCKET = /^vp(render|stress|restart|scale|tls|clip|probe|check|release)-(\d+)$/

export function sweepStaleSockets(log = () => {}) {
  const dir = join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid()}`)
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return 0 // no tmux has ever run here
  }

  let killed = 0
  for (const name of entries) {
    const m = HARNESS_SOCKET.exec(name)
    if (!m) continue
    const pid = Number(m[2])
    if (pid === process.pid) continue
    try {
      // Signal 0 tests for existence without delivering anything.
      process.kill(pid, 0)
      continue // still running: not ours to clean up
    } catch (err) {
      // EPERM means it exists and belongs to somebody else. Only ESRCH — no
      // such process — says the owner is gone.
      if (err.code !== 'ESRCH') continue
    }
    try {
      execSync(`tmux -L ${name} kill-server`, { stdio: 'ignore' })
      killed++
      log(`swept stale tmux socket ${name} (owner ${pid} is gone)`)
    } catch {
      // Already dead, or a socket file with no server behind it.
    }
    killOrphanedPanel(name, log)
    // tmux removes its own socket when it shuts down cleanly; one killed any
    // other way leaves the file behind forever. Forty-eight of them had
    // accumulated in /tmp before anybody looked. Safe here for the same reason
    // the kill above is: the name says whose it was, and that process is gone.
    try {
      unlinkSync(join(dir, name))
    } catch {
      // Already removed by the kill-server above, which is the usual case.
    }
  }
  return killed
}

/**
 * Kills a panel process left over from the same dead run.
 *
 * Sweeping the tmux server is only half of it: the harness starts a vibepanel
 * of its own, and a SIGKILLed run leaves that holding a port and a data
 * directory too. It cannot be found by its command line — the panel is started
 * as a bare `vibepanel serve` with everything in the environment, so matching
 * on the name would put a user's real panel in range.
 *
 * The environment is what makes it identifiable, and precisely: the process to
 * kill is the one told to use *this* socket, the one whose owner has just been
 * established as gone. A real panel uses the default socket name, which never
 * matches a harness prefix, so it is never a candidate.
 */
function killOrphanedPanel(socket, log) {
  const want = `VIBEPANEL_TMUX_SOCKET=${socket}`
  let pids
  try {
    pids = readdirSync('/proc').filter((n) => /^\d+$/.test(n))
  } catch {
    return // not Linux, or no procfs
  }
  for (const pid of pids) {
    let env
    try {
      env = readFileSync(`/proc/${pid}/environ`, 'utf8')
    } catch {
      continue // gone, or somebody else's
    }
    if (!env.split('\0').includes(want)) continue
    try {
      process.kill(Number(pid), 'SIGTERM')
      log(`swept orphaned panel ${pid} (it was serving ${socket})`)
    } catch {
      // Already gone.
    }
  }
}
