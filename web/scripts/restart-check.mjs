// Does the panel actually survive its own backend restarting?
//
// This is the central promise of the architecture — tmux owns the processes,
// the panel is a thin client that attaches to them — and until now nothing
// verified it end to end. stress-check.mjs drops the WebSocket, which is a
// much weaker test: the ring buffer is still in memory on the other side, so
// replay comes from the hot path and the cold path is never exercised.
//
// Here the server is killed outright. That deletes every ring buffer, so the
// only way content can come back is `capture-pane -S - -E -1`, and the only
// way login can survive is the database-backed session. Both are load-bearing
// and both are invisible until the day they break.
//
//   npm run build && (cd .. && go build -o vibepanel ./cmd/vibepanel)
//   npm run check:restart
import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { assertFreshBuild } from './lib/fresh.mjs'
const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
// Measuring a build that does not contain the change is the one failure that
// looks exactly like a pass. See lib/fresh.mjs.
assertFreshBuild(BIN, new URL('../../', import.meta.url).pathname)
const SHOTS = process.argv[3] ?? join(tmpdir(), 'vprestart-shots')
mkdirSync(SHOTS, { recursive: true })

// The port has to be reused across both runs, so it cannot be kernel-chosen
// per launch the way the other harnesses do it. Probe once and hold it.
const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vprestart-${process.pid}`
const DATA = mkdtempSync(join(tmpdir(), 'vprestart-'))
const FAKE_HOME = mkdtempSync(join(tmpdir(), 'vprestart-home-'))

const findings = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let server = null
let serverLog = ''
function boot() {
  const p = spawn(BIN, ['serve'], {
    env: {
      ...process.env,
      HOME: FAKE_HOME,
      VIBEPANEL_DATA_DIR: DATA,
      VIBEPANEL_TMUX_SOCKET: SOCKET,
      VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
      VIBEPANEL_DOMAIN: 'localhost',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  p.stdout.on('data', (d) => (serverLog += d))
  p.stderr.on('data', (d) => (serverLog += d))
  server = p
  return p
}
async function stop() {
  if (!server) return
  const dead = new Promise((r) => server.once('exit', r))
  server.kill('SIGTERM')
  const exited = await Promise.race([dead.then(() => true), sleep(8000).then(() => false)])
  if (!exited) {
    note('FAIL', 'shutdown', 'the server ignored SIGTERM for 8s; a systemd restart would be a SIGKILL')
    server.kill('SIGKILL')
    await sleep(500)
  }
  server = null
}
async function health(timeoutMs = 20000) {
  const end = Date.now() + timeoutMs
  while (Date.now() < end) {
    try { if ((await fetch(BASE + '/api/health')).ok) return true } catch { /* not up */ }
    await sleep(150)
  }
  return false
}

let browser
let cleanedUp = false
async function cleanup() {
  if (cleanedUp) return
  cleanedUp = true
  try { await browser?.close() } catch { /* already gone */ }
  await stop()
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  try { rmSync(join('/tmp', `tmux-${process.getuid()}`, SOCKET), { force: true }) } catch { /* best effort */ }
  for (const dir of [DATA, FAKE_HOME]) {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ }
  }
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => void cleanup().finally(() => process.exit(130)))
}

const BASE = `http://localhost:${PORT}`
let cookie = ''
// Fetch carrying the session cookie, for seeding through the API.
//
// 404 and 405 throw rather than being returned, because a check that asks for
// a route the server does not have gets a perfectly ordinary Response and goes
// on to draw conclusions from its body. That happened: a probe polled
// `GET /api/sessions`, which exists only for POST, with `.catch(() => [])` on
// the parse — so every refusal became an empty list, an empty list contained
// no session, and "no session" was the success condition. It reported a
// healthy result in three milliseconds and would have done so against a server
// that was switched off.
//
// 405 as well as 404, and that is not a detail: chi answers a known path with
// an unregistered method with 405, so the first version of this guard checked
// only for 404 and did not catch the exact bug it was written for. Injecting
// the bogus call and watching the run stay green is the only reason that was
// noticed.
//
// Nothing here expects either. If something ever does, it can call fetch.
const authed = async (path, init = {}) => {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(cookie ? { Cookie: cookie } : {}),
      ...(init.headers ?? {}),
    },
  })
  if (res.status === 404 || res.status === 405) {
    throw new Error(
      `${init.method ?? 'GET'} ${path} -> ${res.status}; this server has no such ` +
      'route and method, so whatever this check concluded from the answer was meaningless',
    )
  }
  return res
}

const rows = (page) =>
  page.$$eval('.xterm-rows > div', (els) => els.map((el) => el.textContent ?? ''))
const screen = async (page) => (await rows(page)).join('\n')
/** Every pane the panel's tmux server is running, as "session:pid". */
const panes = () => {
  try {
    return execSync(`tmux -L ${SOCKET} list-panes -a -F '#{session_name}:#{pane_pid}'`,
      { encoding: 'utf8' }).trim().split('\n').filter(Boolean).sort()
  } catch {
    return []
  }
}
const waitFor = async (page, needle, timeoutMs = 25000) => {
  const end = Date.now() + timeoutMs
  while (Date.now() < end) {
    if ((await screen(page)).includes(needle)) return true
    await sleep(300)
  }
  return false
}

try {
  boot()
  if (!await health()) throw new Error(`server never came up:\n${serverLog}`)

  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(serverLog)?.[1]
  if (!token) throw new Error(`no setup token:\n${serverLog}`)
  const PASSWORD = 'a sufficiently long password'
  const setupRes = await authed('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token, username: 'restart', password: PASSWORD }),
  })
  cookie = (setupRes.headers.getSetCookie?.() ?? []).map((c) => c.split(';')[0]).join('; ')

  const proj = await (await authed('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: DATA, name: 'restart' }),
  })).json()
  await authed('/api/sessions', {
    method: 'POST',
    body: JSON.stringify({ projectId: proj.id, command: ['bash', '--norc', '--noprofile', '-i'],
      title: 'survivor', cols: 100, rows: 30 }),
  })

  // An agent that rang and is waiting for an answer.
  //
  // The evidence for "waiting" is a bell, which the detector keeps in memory.
  // A restart threw that away and the poller re-derived the session from what
  // is running — which for anything that is not a shell is "working". Measured
  // against the real binary before this was fixed: waiting before the restart,
  // working after it, with the question still on the pane's screen. Every
  // waiting session at once, on the operation this architecture exists to make
  // safe.
  const asking = await (await authed('/api/sessions', {
    method: 'POST',
    body: JSON.stringify({
      projectId: proj.id, title: 'asking', cols: 100, rows: 30,
      command: ['python3', '-u', '-c',
        "import sys,time; sys.stdout.write('Do you want to proceed? (y/n)\\a'); sys.stdout.flush(); time.sleep(600)"],
    }),
  })).json()

  browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 860 } })
  const page = await ctx.newPage()
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(String(e)))

  await page.goto(BASE, { waitUntil: 'networkidle' })
  await page.locator('[data-testid="auth-username"]').fill('restart')
  await page.locator('[data-testid="auth-password"]').fill(PASSWORD)
  await page.locator('[data-testid="auth-submit"]').click()
  await page.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 })
  await page.locator('[data-testid="session-row"]', { hasText: 'survivor' }).first().click()
  await sleep(2500)

  // Something to look for afterwards, and a background process whose pid has
  // to be the same one on the other side. Split so the echoed command line
  // cannot be mistaken for the output — a mistake this harness made once.
  const MARK = 'BEFORE' + '_THE_RESTART'
  await page.locator('.xterm-helper-textarea').fill('')
  await page.keyboard.type(`echo ${MARK.slice(0, 6)}"${MARK.slice(6)}"\n`)
  if (!await waitFor(page, MARK)) {
    throw new Error(`the marker never appeared before the restart:\n${await screen(page)}`)
  }
  const before = panes()
  if (before.length === 0) throw new Error('no panes running before the restart')

  // ── the restart ──────────────────────────────────────────────────────────
  await stop()
  await sleep(1000)

  const survived = panes()
  if (survived.length !== before.length || survived.join() !== before.join()) {
    note('FAIL', 'persistence',
      `killing the backend disturbed the processes it was only supposed to be watching: ` +
      `before=${JSON.stringify(before)} after=${JSON.stringify(survived)}`)
  }

  boot()
  if (!await health()) throw new Error(`server did not come back:\n${serverLog}`)
  // The panel re-attaches on its own timer; give it room before judging.
  await sleep(6000)

  {
    const stateOf = async () => {
      const rows = (await (await authed('/api/state')).json()).sessions ?? []
      return rows.find((x) => x.id === asking.id) ?? {}
    }
    const now = await stateOf()
    if (now.state !== 'waiting') {
      note('FAIL', 'persistence',
        `the session that rang the bell reads as ${JSON.stringify(now.state)} after the restart, ` +
        'not waiting. Its question is still on screen and the panel has stopped saying so.')
    } else {
      note('PASS', 'persistence', `a waiting session is still waiting after the restart (from ${now.stateSource})`)
    }
  }

  const afterBoot = panes()
  if (afterBoot.length !== before.length) {
    note('FAIL', 'persistence',
      `restarting changed the pane count — the server is spawning new sessions rather than ` +
      `re-attaching: before=${JSON.stringify(before)} after=${JSON.stringify(afterBoot)}`)
  }
  if (afterBoot.join() !== before.join()) {
    note('FAIL', 'persistence',
      `the pane pids changed across a restart, so the agents were restarted with them: ` +
      `before=${JSON.stringify(before)} after=${JSON.stringify(afterBoot)}`)
  }

  // ── the page that was left open ──────────────────────────────────────────
  // Nobody reloads before looking. A page open on a second monitor has to
  // heal by itself or the panel looks dead every time it is deployed.
  const healed = await waitFor(page, MARK, 30000)
  await page.screenshot({ path: join(SHOTS, 'after-restart-no-reload.png') })
  if (!healed) {
    note('FAIL', 'reconnect',
      `a page left open across a backend restart never recovered in 30s; ` +
      `screen is:\n${(await screen(page)).slice(0, 400)}`)
  } else {
    // Healing the display is not enough — it has to accept input again.
    const AFTER = 'AFTER' + '_THE_RESTART'
    await page.locator('.xterm-helper-textarea').click()
    await page.keyboard.type(`echo ${AFTER.slice(0, 5)}"${AFTER.slice(5)}"\n`)
    if (!await waitFor(page, AFTER)) {
      note('FAIL', 'reconnect',
        `a healed page renders but swallows input — the write path did not re-attach:\n` +
        `${(await screen(page)).slice(0, 400)}`)
    }
  }

  // ── a page opened after the restart ──────────────────────────────────────
  // The ring buffers died with the old process, so a fresh page is filled by
  // tmux repainting the screen when the panel re-attaches.
  //
  // This said "can only be filled by capture-pane" and called itself the path
  // that had never been covered. It was not covering it either: deleting the
  // capture-pane priming entirely left this check green, the rendered screen
  // byte-identical, and the probe measuring scrollback unchanged. The priming
  // was inert because tmux's attach opens with ESC[?1049h and everything after
  // it is drawn on the alternate screen, which has no scrollback — so the
  // history it fetched was covered a millisecond after it was written.
  //
  // What this check is worth is unchanged and real: a page opened after a
  // restart must show the session's screen without waiting for the session to
  // print something. What it does not show, and cannot, is scrollback; that
  // lives in tmux and is reached with copy-mode.
  const fresh = await ctx.newPage()
  await fresh.goto(BASE, { waitUntil: 'networkidle' })
  if (await fresh.locator('[data-testid="auth-submit"]').isVisible().catch(() => false)) {
    note('FAIL', 'auth',
      'the login session did not survive a backend restart, so every restart logs everyone out')
    await fresh.locator('[data-testid="auth-username"]').fill('restart')
    await fresh.locator('[data-testid="auth-password"]').fill(PASSWORD)
    await fresh.locator('[data-testid="auth-submit"]').click()
  }
  await fresh.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 })
  await fresh.locator('[data-testid="session-row"]', { hasText: 'survivor' }).first().click()
  const replayed = await waitFor(fresh, MARK, 25000)
  await fresh.screenshot({ path: join(SHOTS, 'after-restart-cold-replay.png') })
  if (!replayed) {
    note('FAIL', 'replay',
      `a page opened after the backend restarted shows an empty terminal — the cold ` +
      `capture-pane path did not restore the scrollback:\n${(await screen(fresh)).slice(0, 400)}`)
  }

  // The session must still be the one it was, not a stranger with the same name.
  const list = await (await authed('/api/state')).json()
  const still = (list.sessions ?? []).filter((s) => s.title === 'survivor')
  if (still.length !== 1) {
    note('FAIL', 'persistence',
      `expected exactly one "survivor" session after the restart, found ${still.length}`)
  }

  if (pageErrors.length) {
    note('FAIL', 'console', `the page threw across the restart: ${pageErrors.slice(0, 3).join(' | ')}`)
  }
} catch (err) {
  note('FAIL', 'harness', String(err?.stack ?? err))
} finally {
  await cleanup()
}

for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
// Counting WARN rather than everything-that-is-not-FAIL. The
// subtraction was right only while FAIL and WARN were the only
// severities this file used: the first PASS recorded here was
// reported as a warning, and a summary that invents warnings is one
// people stop reading.
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`=== restart check: ${fails} FAIL, ${findings.filter((f) => f.sev === 'WARN').length} WARN ===`)
console.log(`screenshots: ${SHOTS}`)
process.exit(fails ? 1 : 0)
