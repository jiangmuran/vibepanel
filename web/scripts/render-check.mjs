// Headless render check for vibepanel.
//
// Boots the real binary against a throwaway tmux socket and data dir, drives it
// with a real browser, and reports anything that looks wrong: console errors,
// failed requests, unreadable colour pairs, a terminal that never connects, a
// palette that does not follow the theme.
//
//   npm run build && (cd .. && go build -o vibepanel ./cmd/vibepanel)
//   npm run check:render
//
// It exists because the bugs that matter most here are not the ones a unit
// test sees. Every finding it has produced so far — a replay injecting terminal
// responses into the shell, a viewer claiming the grid by loading the page, the
// terminal palette lagging one theme change behind — needed a real browser
// talking to a real tmux to show up at all.
//
// Exits non-zero on any FAIL. Screenshots land in the directory given as the
// second argument.
import { chromium } from 'playwright'
import { rows as screenRows } from './lib/screen.mjs'
import { spawn, execSync } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdtempSync, rmSync, mkdirSync, readFileSync, writeFileSync, existsSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sweepStaleSockets } from './lib/stale.mjs'
import { findUnreachable } from './lib/overflow.mjs'
import { findFadedControls } from './lib/faded.mjs'
import { findCoveredControls } from './lib/covered.mjs'
import { findSmallTargets } from './lib/tap.mjs'
import { findInvisibleFocus } from './lib/focus.mjs'
import { findUnnamedControls } from './lib/names.mjs'
import { assertFreshBuild } from './lib/fresh.mjs'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
// Measuring a build that does not contain the change is the one failure that
// looks exactly like a pass. See lib/fresh.mjs.
assertFreshBuild(BIN, new URL('../../', import.meta.url).pathname)
// A free port picked by the kernel, not a guess. A hard-coded one silently
// connects to whatever is already listening — including an orphaned server
// from a previous run, which then serves a stale build and produces an hour of
// confusing results.
const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vprender-${process.pid}`

// Before anything else: a run killed with SIGKILL cannot clean up after
// itself, and what it leaves behind is a tmux server holding live sessions.
sweepStaleSockets((msg) => console.log(`==> ${msg}`))
const DATA = mkdtempSync(join(tmpdir(), 'vprender-'))
const SHOTS = process.argv[3] ?? join(DATA, 'shots')
mkdirSync(SHOTS, { recursive: true })

const findings = []

// Collected at module scope and reported in the finally below.
//
// These lived inside the try and were reported near the end of it, so any
// earlier failure — a click that timed out because the page was blank — threw
// past the one piece of evidence that explained the blank page. One of them
// collected these and never reported them at all. An uncaught error in the
// page is the most informative thing a run can produce; it has to survive the
// run failing.
const pageErrors = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/**
 * Move the settings dialog to one of its groups, and say so if it did not go.
 *
 * The dialog is a rail and one group at a time now, so every block below has
 * to be asked for. Reading back `data-group` rather than trusting the click is
 * the point: a rail item that selects a different group than the one it is
 * labelled with is invisible in a screenshot -- something is on screen, and it
 * is not what was asked for.
 */
async function settingsGroup(page, id) {
  const tab = page.locator(`[data-testid="settings-group-${id}"]`)
  await tab.scrollIntoViewIfNeeded().catch(() => {})
  await tab.click()
  await sleep(500)
  const at = await page.locator('[data-testid="settings-body"]').getAttribute('data-group')
    .catch(() => null)
  if (at !== id) {
    note('FAIL', 'settings', `the settings rail was asked for ${id} and showed ${at}`)
  }
}

/** note()-reporting wrapper around the name scan. See lib/names.mjs. */
async function scanNames(target, where) {
  const unnamed = await findUnnamedControls(target)
  if (unnamed.length > 0) {
    note('FAIL', 'a11y',
      `in ${where}, controls have no name a screen reader can announce: ${unnamed.join(', ')}`)
  }
}

/** note()-reporting wrapper around the focus scan. See lib/focus.mjs. */
async function scanFocus(target, where) {
  const invisible = await findInvisibleFocus(target)
  if (invisible.length > 0) {
    note('FAIL', 'a11y',
      `in ${where}, these look the same focused as unfocused, so keyboard navigation is ` +
      `invisible: ${invisible.join(', ')}`)
  }
}

/** note()-reporting wrapper around the opacity scan. See lib/faded.mjs. */
async function scanFaded(target, where) {
  const faded = await findFadedControls(target)
  if (faded.length > 0) {
    note('FAIL', 'ui',
      `in ${where}, controls are on screen as far as a script can tell and invisible to a ` +
      `person: ${faded.join(', ')}`)
  }
}

/** note()-reporting wrapper around the occlusion scan. See lib/covered.mjs. */
async function scanCovered(target, where) {
  const covered = await findCoveredControls(target)
  if (covered.length > 0) {
    note('FAIL', 'ui',
      `in ${where}, controls are on screen with something else on top of them: ${covered.join(', ')}`)
  }
}

/** note()-reporting wrapper around the tap scan. See lib/tap.mjs. */
//
// KNOWN GAP, not fixed here: both of these report only when they find
// something, and neither asserts that it looked at anything.
//
// findSmallTargets walks `button, a[href], [role="button"]`. Replace a button
// with a `<div onClick>` — an ordinary refactor — and it returns [] and the
// check passes. An empty result and a clean page are the same value.
// findUnreachable has the same shape.
//
// The other way to go blind, calling this before the view has rendered, is
// covered by accident: this file clicks dozens of specific testids, and a page
// that had not rendered would fail those first and loudly. It is the refactor
// case that has nothing standing in front of it — Playwright clicks a div as
// happily as a button, so every other assertion keeps passing while the rule
// stops being measured.
//
// This is the failure this project has already been bitten by twice: a CSS
// import that resolved to an empty stub so every theme assertion passed against
// nothing, and a `go list` run from the wrong directory so the module guard
// measured itself. Both were found by accident. The two checks added most
// recently — the harness socket sweep and the route walk — assert a floor on
// what they saw for exactly this reason; these two predate that and never got
// it.
//
// The fix is a floor, not a threshold: have the scans report how many
// candidates they examined and fail at zero. Zero is unambiguous — a rendered
// panel has buttons — so it cannot false-positive, which matters for a check
// that takes six minutes and gates a release.
//
// Established by reading, in a stretch where nothing could be run. Not written
// as code for that reason: a browser check nobody can execute is a change that
// fails closed on whoever runs it next.
async function scanTapTargets(target, where, minExpected = 1) {
  const { examined, small } = await findSmallTargets(target)
  // A scan reports "nothing wrong" and a scan that matched nothing report the
  // same thing, which is silence. findSmallTargets walks `button, a[href],
  // [role="button"]`; a control refactored into a `<div onClick>` leaves it,
  // and if enough of them did, this would go on passing while looking at an
  // empty set. The floor cannot catch one control going missing -- that needs
  // an expected count, which is brittle -- but it does catch the scan going
  // blind, and printing the number makes a drop visible between runs.
  if (examined < minExpected) {
    note('FAIL', 'mobile',
      `in ${where}, the tap-target scan looked at ${examined} controls, expected at least ` +
      `${minExpected}. Either the page had not rendered or the selector no longer matches ` +
      'what this panel builds controls out of; either way the check below saw nothing.')
  } else {
    note('PASS', 'mobile', `${examined} tap targets measured in ${where}`)
  }
  if (small.length > 0) {
    note('FAIL', 'mobile',
      `in ${where}, controls are too small for a thumb: ${small.join('; ')}`)
  }
}

/** note()-reporting wrapper around the shared scan. See lib/overflow.mjs. */
async function scanUnreachable(target, where, minExpected = 20) {
  const { examined, found } = await findUnreachable(target, sleep)
  // Same reasoning as scanTapTargets. This one walks every element, so a
  // handful means the page was not there yet rather than that the layout is
  // clean -- which is the reading a silent pass invites.
  if (examined < minExpected) {
    note('FAIL', 'ui',
      `in ${where}, the overflow scan measured ${examined} elements, expected at least ` +
      `${minExpected}; it was looking at a page that had not rendered`)
  }
  if (found.length > 0) {
    note('FAIL', 'ui',
      `in ${where}, content is painted outside its container with no way to scroll to it: ` +
      found.join('; '))
  }
}


/**
 * What the terminal is showing, with any non-breaking spaces normalised.
 *
 * xterm emits one <span> per run of styling, and the text between two spans can
 * come back as U+00A0 rather than U+0020 -- so how a row reads back depends on
 * how many escape sequences produced it, and every `includes(...)` against it
 * stops matching without saying why.
 *
 * Measured in stress-check, which reads `textContent`: making a flood emit
 * eight sequences a line instead of two broke an assertion on a passing tree,
 * and it printed the very text that satisfies it. The codepoints read
 * `6c 69 6e 65 a0 31 39 39 39 39` -- "line", U+00A0, "19999".
 *
 * This file reads `innerText`, and removing the replace below does *not* fail
 * the styled-runs check further down -- also measured. So this is belt and
 * braces here rather than a fix: what it buys is that the decision lives in one
 * place instead of at ten call sites, one of which could reasonably be written
 * with `textContent` tomorrow.
 *
 * Only the terminal. The other innerText reads in this file are React elements,
 * which have no per-run spans.
 */
const screenText = async (target, sel = '.xterm-screen', opts) => {
  // The buffer first, the DOM second.
  //
  // `.xterm-screen` has text only under xterm's DOM renderer. The GPU renderer
  // draws to a canvas and leaves it empty however full the terminal is, so this
  // returned "" for every terminal in the panel the moment the renderer was
  // loaded -- thirteen assertions red at once, each of them describing an empty
  // terminal that was not empty.
  //
  // `sel` is still honoured for the callers that read something other than the
  // terminal; only the default path goes through the buffer.
  //
  // `opts` names which terminal, for the callers that mean a particular one
  // rather than the focused one -- see the bottom-terminal check, where "the
  // focused one" was the whole of a flake.
  if (sel === '.xterm-screen' && typeof target.evaluate === 'function') {
    const rows = await screenRows(target, opts).catch(() => null)
    if (rows && rows.length) return rows.join('\n').replace(/\u00a0/g, ' ')
  }
  return (await target.locator(sel).first().innerText().catch(() => '')).replace(/\u00a0/g, ' ')
}

function parseColor(c) {
  const m = c.match(/rgba?\(([^)]+)\)/)
  if (!m) return null
  const parts = m[1].split(/[\s,/]+/).filter(Boolean).map(Number)
  return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 }
}
function lum({ r, g, b }) {
  const f = (v) => {
    v /= 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}
function contrast(fg, bg) {
  // Flatten any alpha onto the background before comparing; ignoring it makes
  // translucent secondary text look far more readable than it is.
  const a = fg.a ?? 1
  const flat = { r: fg.r * a + bg.r * (1 - a), g: fg.g * a + bg.g * (1 - a), b: fg.b * a + bg.b * (1 - a) }
  const l1 = lum(flat), l2 = lum(bg)
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

let cleanedUp = false
// A throwaway HOME for the server.
//
// The settings page can install agent hooks, which edits ~/.claude/settings.json
// — and the first run of that check did exactly that to the real one. A test
// that reaches outside its own directories is not a test, it is an incident
// waiting for the right moment.
const FAKE_HOME = mkdtempSync(join(tmpdir(), 'vprender-home-'))
mkdirSync(join(FAKE_HOME, '.claude'), { recursive: true })

// One transcript, so the spend block has something to be about.
//
// Without it every assertion below about that block runs against a panel that
// has scanned nothing: `scannedAt` stays 0, the branch that dates a reading is
// unreachable, and a check written to say "do not date a fresh reading" passes
// by never reaching the code. Measured — the assertion was added, the guard it
// was written for was removed, and nothing went red.
//
// The shape is what internal/usage reads: one JSON object per line, `usage` on
// an assistant message. Two lines, so a per-request average is not a division
// by one.
{
  const day = new Date().toISOString().slice(0, 10)
  const dir = join(FAKE_HOME, '.claude', 'projects', 'render-check')
  mkdirSync(dir, { recursive: true })
  // id and requestId matter: the reader deduplicates on the pair, because one
  // API response is written as one line per content block carrying the same
  // usage object. Two lines without them are one record, which is how the
  // first version of this seed produced a block with nothing in it.
  const line = (n, input, output) => JSON.stringify({
    type: 'assistant',
    timestamp: `${day}T12:00:00.000Z`,
    sessionId: 'render-check',
    cwd: FAKE_HOME,
    requestId: `req-${n}`,
    message: {
      id: `msg-${n}`,
      model: 'claude-opus-5',
      usage: {
        input_tokens: input,
        output_tokens: output,
        cache_read_input_tokens: input * 40,
        cache_creation_input_tokens: input * 3,
      },
    },
  })
  writeFileSync(join(dir, 'render-check.jsonl'), line(1, 120, 4400) + '\n' + line(2, 80, 2100) + '\n')
}

const server = spawn(BIN, ['serve'], {
  env: {
    ...process.env,
    HOME: FAKE_HOME,
    VIBEPANEL_DATA_DIR: DATA,
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
    // localhost is a valid Relying Party ID and a secure context even over
    // plain HTTP, which is what lets passkeys be exercised here at all.
    VIBEPANEL_DOMAIN: 'localhost',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})
let serverLog = ''
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

const BASE = `http://localhost:${PORT}`
let browser

async function cleanup() {
  if (cleanedUp) return
  cleanedUp = true
  try { await browser?.close() } catch { /* already gone */ }
  server.kill('SIGTERM')
  await sleep(400)
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  // kill-server leaves the socket file behind; a few hundred of those pile up
  // fast when this runs in a loop.
  try { rmSync(join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid()}`, SOCKET), { force: true }) } catch { /* best effort */ }
  try { rmSync(DATA, { recursive: true, force: true }) } catch { /* best effort */ }
  try { rmSync(FAKE_HOME, { recursive: true, force: true }) } catch { /* best effort */ }
}

// The server is a child process, and a child does not die because its parent
// was killed. Without these handlers, every `timeout`-terminated run leaves a
// panel listening on a port and a tmux server holding sessions.
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    void cleanup().finally(() => process.exit(130))
  })
}

/**
 * Waits for the panel to answer its health probe.
 *
 * Restored after being deleted by accident: extracting the overflow scan into
 * a shared module cut from the scan to the next top-level const, and this sat
 * in between. The sweep caught it a minute later as
 * `ReferenceError: waitHealth is not defined` — which is the argument for
 * running everything after a refactor, not only the thing that was refactored.
 */
async function waitHealth(base, timeoutMs = 20000) {
  const end = Date.now() + timeoutMs
  while (Date.now() < end) {
    try {
      if ((await fetch(base + '/api/health')).ok) return true
    } catch {
      /* not up yet */
    }
    await sleep(150)
  }
  return false
}

const USERNAME = 'render-check'
const PASSWORD = 'a sufficiently long password'
const NEW_PASSWORD = 'a different sufficiently long password'
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

try {
  if (!(await waitHealth(BASE))) {
    note('FAIL', 'server', `did not become healthy on ${BASE}\n${serverLog}`)
    throw new Error('server not healthy')
  }

  // The panel now guards everything behind a session, so the check has to go
  // through the real setup flow rather than reaching past it.
  const tokenMatch = serverLog.match(/one-time setup token:\s*\n\s*\n\s*(\S+)/)
  if (!tokenMatch) {
    note('FAIL', 'auth', `no setup token in the server output:\n${serverLog}`)
    throw new Error('no setup token')
  }
  const setupRes = await authed('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token: tokenMatch[1], username: USERNAME, password: PASSWORD }),
  })
  if (setupRes.status !== 201) {
    note('FAIL', 'auth', `setup failed: ${setupRes.status} ${await setupRes.text()}`)
    throw new Error('setup failed')
  }
  cookie = (setupRes.headers.getSetCookie?.() ?? [])
    .map((c) => c.split(';')[0])
    .join('; ')
  if (!cookie) {
    note('FAIL', 'auth', 'setup returned no session cookie')
    throw new Error('no cookie')
  }

  // Seed content through the API, the same way the UI would.
  const proj = await (await authed('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: process.cwd(), name: 'render-check' }),
  })).json()

  const mkSession = (cmd, title) =>
    authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId: proj.id, command: cmd, title }),
    }).then((r) => r.json())

  // Named, not left to the automatic title.
  //
  // Everything below finds the shell by its name. Automatically, a shell is
  // named after the directory it sits in — which is this project's path, which
  // is wherever the harness happens to be run from. The name it looked for was
  // fixed while the directory was not, so the shell was never found, every
  // later step typed into whichever session was selected instead, and the
  // failures pointed anywhere but here.
  await mkSession(['sh', '-c', 'echo RENDER_CHECK_MARKER; exec sh'], 'scratchpad')
  await mkSession(['htop'])

  // A session that rings the terminal bell: without hooks this is the only
  // signal that an agent has stopped and wants a person, and surfacing it is
  // the panel's whole reason to exist.
  await mkSession(['sh', '-c', "sleep 1; printf 'needs you\\a'; exec sleep 300"])

  // A second project, so ordering can be exercised.
  await authed('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: DATA, name: 'zzz-second' }),
  })
  await sleep(2500) // let the poller derive titles

  browser = await chromium.launch({ headless: true })
  // clipboard-write so the OSC 52 path can be checked on its happy path; the
  // refused case gets its own context below, without it.
  const ctx = await browser.newContext({
    // Service workers blocked, and this is load-bearing rather than tidy.
    //
    // The panel registers one for installability and notifications, and it
    // passes every request through with respondWith(fetch(...)). Playwright's
    // page.route does not see requests a service worker makes -- so the moment
    // that worker shipped, every stubbed endpoint in this file silently started
    // testing the real machine instead of the payload under test. Found by a
    // counter on the route handler reading zero while the assertion under it
    // failed against real numbers.
    //
    // The worker itself is covered separately, below.
    serviceWorkers: 'block',
    viewport: { width: 1440, height: 900 },
    permissions: ['clipboard-read', 'clipboard-write'],
  })
  const page = await ctx.newPage()

  const consoleErrors = []
  // Warnings are not failures and are not noise either: the panel uses them
  // for things it cannot show on screen, such as a note flushed from a
  // component that has already unmounted. Collected so a check that fails can
  // quote the panel's own account of why.
  const consoleWarnings = []
  const failedReqs = []
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text())
    if (m.type() === 'warning') consoleWarnings.push(m.text())
  })
  page.on('pageerror', (e) => pageErrors.push(String(e)))
  page.on('requestfailed', (r) => failedReqs.push(`${r.url()} — ${r.failure()?.errorText}`))

  // Tracks the bell session across the run, so a state that gets clobbered
  // says which step did it rather than only that it happened.
  const bellTrace = []
  const traceBell = async (phase) => {
    try {
      const st = await (await authed('/api/state')).json()
      const row = st.sessions.find((x) => (x.title || x.command) === 'sleep')
      bellTrace.push(`${phase}=${row ? row.state : 'gone'}`)
    } catch {
      bellTrace.push(`${phase}=?`)
    }
  }
  await traceBell('seeded')

  await page.goto(BASE, { waitUntil: 'networkidle' })
  await sleep(1200)

  // A stranger must land on a sign-in form, not on somebody's terminals.
  const loginForm = page.locator('[data-testid="login-form"]')
  await page.screenshot({ path: join(SHOTS, 'login.png') })
  if (!(await loginForm.isVisible().catch(() => false))) {
    note('FAIL', 'auth', 'the browser reached the panel without signing in')
  } else {
    // Wrong credentials must be refused and say so.
    await page.locator('[data-testid="auth-username"]').fill(USERNAME)
    await page.locator('[data-testid="auth-password"]').fill('definitely not it')
    await page.locator('[data-testid="auth-submit"]').click()
    await sleep(1200)
    if (!(await page.locator('[data-testid="auth-error"]').isVisible().catch(() => false))) {
      note('FAIL', 'auth', 'a wrong password produced no visible error')
    }
    if (!(await loginForm.isVisible().catch(() => false))) {
      note('FAIL', 'auth', 'a wrong password let the browser in')
    }

    // The throttle now stands in the way, which is the point. Wait it out.
    await sleep(1500)
    await page.locator('[data-testid="auth-password"]').fill(PASSWORD)
    await page.locator('[data-testid="auth-submit"]').click()
    await page.waitForSelector('[data-testid="sidebar"], [data-testid="sidebar-rail"]', { timeout: 15000 })
      .catch(() => note('FAIL', 'auth', 'signing in did not reach the panel'))
    await sleep(1500)
  }

  await traceBell('signed-in')

  // The sign-in step deliberately submits a wrong password, so its 401 is
  // expected noise. Start collecting once past it.
  consoleErrors.length = 0
  failedReqs.length = 0
  await sleep(500)

  // ── structure ────────────────────────────────────────────────────────────
  const sidebarText = await page.locator('[data-testid="sidebar"]').innerText().catch(() => '')
  if (!sidebarText.toLowerCase().includes('render-check')) {
    note('FAIL', 'ui', `sidebar does not list the project; saw: ${JSON.stringify(sidebarText)}`)
  }
  const sessionRows = await page.locator('[data-testid="session-row"]').count()
  if (sessionRows < 2) note('FAIL', 'ui', `expected 2 session rows, found ${sessionRows}`)

  // The row and the title bar must agree on the name.
  //
  // They are computed from one function for exactly this reason, and the
  // labels are now qualified when two sessions in a project would otherwise
  // read the same — which means the qualification has to reach both. Two
  // places disagreeing about the name of the session you are looking at reads
  // as a rendering glitch rather than as two code paths.
  const headerName = await page
    .locator('[data-testid="session-title"]')
    .innerText()
    .catch(() => '')
  if (headerName) {
    // Exactly, not as a substring. The first version asked whether any row
    // *contained* the header's text, which "xscratchpad" does — so prefixing
    // every row label passed the check. A test for two things agreeing has to
    // compare them, not check that one is somewhere inside the other.
    const rowTexts = await page.$$eval(
      '[data-testid="session-row"] [data-testid="inline-name"]',
      (els) => els.map((el) => el.textContent?.trim() ?? ''),
    )
    if (!rowTexts.includes(headerName)) {
      note('FAIL', 'ui',
        `the title bar calls the session ${JSON.stringify(headerName)} and no row in the sidebar ` +
        `does: ${JSON.stringify(rowTexts)}`)
    }
  }

  await scanUnreachable(page, 'the desktop layout')
  await scanFocus(page, 'the desktop layout')
  await scanNames(page, 'the desktop layout')
  await scanFaded(page, 'the desktop layout')
  await scanCovered(page, 'the desktop layout')

  // ── websocket status ─────────────────────────────────────────────────────
  const status = await page
    .locator('[data-testid="connection"]')
    .getAttribute('data-status')
    .catch(() => null)
  if (status !== 'open') note('FAIL', 'ws', `socket status is ${JSON.stringify(status)}, want "open"`)

  // ── terminal actually painted something ──────────────────────────────────
  await page.waitForSelector('.xterm-screen', { timeout: 8000 }).catch(() =>
    note('FAIL', 'term', 'xterm never mounted'),
  )
  await sleep(1500)
  const termText = await screenText(page)
  if (!termText.trim()) note('FAIL', 'term', 'terminal rendered but is empty')

  // ── typing round trip ────────────────────────────────────────────────────
  // Pick the shell explicitly. Sessions sort by urgency, so the one that rang
  // the bell is at the top and gets auto-selected — typing into it would be
  // typing into a sleep.
  const shellRow = page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first()
  if (await shellRow.isVisible().catch(() => false)) {
    await shellRow.click()
    await sleep(1200)
  } else {
    // Not a warning. Everything after this types into whatever session is
    // selected instead, so a run that cannot find the shell is not a run with
    // one thing missing — it is a run whose remaining results mean nothing.
    note('FAIL', 'ui', 'could not find the shell session row to select')
  }
  await page.locator('.xterm-screen').click()
  await page.keyboard.type('echo BROWSER_TYPED_OK')
  await page.keyboard.press('Enter')
  // A line built out of several styling runs, read back as text.
  //
  // Several assertions in this file grep terminal output for a phrase with
  // spaces in it, and a line split into many styling runs is where that stops
  // being obviously safe: stress-check reads rows with `textContent` and a
  // heavily-coloured line came back with U+00A0 where its spaces were. This
  // file reads `innerText` and does not have that problem -- measured, by
  // removing the normalisation and watching this still pass -- so what this
  // pins is the weaker and still useful thing: a line built from eight
  // sequences reads back as its own text.
  //
  // Eight sequences, so the marker is split across runs on both sides of a
  // space rather than sitting inside one.
  //
  // Normal intensity, not bold. `\033[1m` maps to the bright palette, and the
  // light theme's bright row is 2.2:1 to 4.1:1 on white -- every one of it
  // below AA -- so a bold-red fixture leaves a standing contrast WARN behind.
  // A permanent warning is how warnings stop being read, which is the thing
  // `make verify` was just changed to count. The palette itself is 49.
  await page.keyboard.type(
    "printf '\\033[31mSTYLED\\033[0m \\033[4m\\033[7mRUNS\\033[0m\\033[27m\\033[24m\\033[36m OK\\033[0m\\n'")
  await page.keyboard.press('Enter')
  let styled = false
  for (let i = 0; i < 30; i++) {
    if ((await screenText(page)).includes('STYLED RUNS OK')) { styled = true; break }
    await sleep(300)
  }
  if (!styled) {
    const raw = await page.locator('.xterm-screen').first().innerText().catch(() => '')
    const near = raw.split('\n').find((r) => r.includes('STYLED')) ?? ''
    note('FAIL', 'term',
      'a line made of several styling runs does not read back as its own text. ' +
      `The row is ${JSON.stringify(near)} and its codepoints are ` +
      JSON.stringify([...near].map((ch) => ch.codePointAt(0).toString(16)).join(' ')))
  }

  let typed = false
  for (let i = 0; i < 40; i++) {
    const t = await screenText(page)
    if ((t.match(/BROWSER_TYPED_OK/g) ?? []).length >= 2) { typed = true; break }
    await sleep(250)
  }
  if (!typed) note('FAIL', 'term', 'typing into the terminal produced no output')

  await traceBell('typed')

  // ── contrast, both themes ────────────────────────────────────────────────
  const probeContrast = async (label) => {
    const results = await page.evaluate(() => {
      const out = []
      const seen = new Set()
      const SKIP = new Set(['STYLE', 'SCRIPT', 'TEMPLATE', 'NOSCRIPT', 'TITLE'])
      const walk = (el) => {
        if (SKIP.has(el.tagName)) return
        const cs = getComputedStyle(el)
        // xterm keeps offscreen measurement nodes in the DOM; they are not
        // shown to anyone and their colours mean nothing.
        if (cs.visibility === 'hidden' || cs.display === 'none' || cs.opacity === '0') return
        const box = el.getBoundingClientRect()
        if (box.width < 1 || box.height < 1) return
        if (box.bottom < 0 || box.right < 0) return
        const text = (el.textContent ?? '').trim()
        const own = Array.from(el.childNodes).some(
          (n) => n.nodeType === 3 && n.textContent.trim(),
        )
        if (own && text) {
          // Walk up for the nearest painted background.
          let bg = 'rgba(0, 0, 0, 0)'
          let p = el
          while (p) {
            const c = getComputedStyle(p).backgroundColor
            if (c && !/rgba\(0, 0, 0, 0\)|transparent/.test(c)) { bg = c; break }
            p = p.parentElement
          }
          const key = `${cs.color}|${bg}|${cs.fontSize}`
          if (!seen.has(key)) {
            seen.add(key)
            out.push({
              color: cs.color, bg, fontSize: parseFloat(cs.fontSize),
              weight: cs.fontWeight, sample: text.slice(0, 40),
            })
          }
        }
        for (const c of el.children) walk(c)
      }
      walk(document.body)
      return out
    })
    for (const r of results) {
      const fg = parseColor(r.color), bg = parseColor(r.bg)
      if (!fg || !bg) continue
      const ratio = contrast(fg, bg)
      const large = r.fontSize >= 18.66 || (r.fontSize >= 14 && Number(r.weight) >= 700)
      const min = large ? 3 : 4.5
      if (ratio < min) {
        note(ratio < 2 ? 'FAIL' : 'WARN', `contrast/${label}`,
          `${ratio.toFixed(2)}:1 (need ${min}) — "${r.sample}" ${r.color} on ${r.bg}`)
      }
    }
  }

  // Drive the real control rather than setting the attribute: the attribute
  // alone changes the CSS but not React's state, so xterm keeps its old
  // palette and the check silently measures a combination no user can produce.
  const themeButton = page.locator('[data-testid="theme-toggle"]')
  const setTheme = async (want) => {
    for (let i = 0; i < 4; i++) {
      const cur = await page.evaluate(() => document.documentElement.dataset.theme ?? 'system')
      if (cur === want) return true
      await themeButton.click()
      await sleep(300)
    }
    return false
  }
  if (!(await setTheme('light'))) note('WARN', 'theme', 'could not reach the light theme via the toggle')
  await sleep(500)
  await probeContrast('light')
  await page.screenshot({ path: join(SHOTS, 'light.png'), fullPage: false })

  if (!(await setTheme('dark'))) note('WARN', 'theme', 'could not reach the dark theme via the toggle')
  await sleep(500)
  await probeContrast('dark')
  await page.screenshot({ path: join(SHOTS, 'dark.png'), fullPage: false })

  // The terminal is the largest surface on the page. xterm holds its own
  // palette, so if it is not rebuilt when the theme changes it stays bright
  // white in dark mode — and React runs child effects before parent ones,
  // which is exactly how that regressed once already.
  // Checks the viewport specifically, not just the nearest painted ancestor of
  // the rows: the viewport is a *sibling* of .xterm-screen, so walking up from
  // the rows skips it entirely — which is how a hard-coded black viewport
  // survived an earlier theme check unnoticed.
  const termLuminance = async () => page.evaluate(() => {
    const el = document.querySelector('.xterm-viewport') ?? document.querySelector('.xterm')
    if (!el) return null
    let p = el
    while (p) {
      const c = getComputedStyle(p).backgroundColor
      if (c && !/rgba\(0, 0, 0, 0\)|transparent/.test(c)) {
        const m = c.match(/rgba?\(([^)]+)\)/)
        if (!m) return null
        const [r, g, b] = m[1].split(/[\s,/]+/).filter(Boolean).map(Number)
        const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }
        return { css: c, l: 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b) }
      }
      p = p.parentElement
    }
    return null
  })
  const darkTerm = await termLuminance()
  if (!darkTerm || darkTerm.l > 0.2) {
    note('FAIL', 'theme',
      `terminal background is ${darkTerm?.css ?? 'unknown'} in dark mode (luminance ${darkTerm?.l.toFixed(3)}); xterm did not follow the theme`)
  }
  if (!(await setTheme('light'))) note('WARN', 'theme', 'could not return to the light theme')
  await sleep(600)
  const lightTerm = await termLuminance()
  if (!lightTerm || lightTerm.l < 0.6) {
    note('FAIL', 'theme',
      `terminal background is ${lightTerm?.css ?? 'unknown'} in light mode (luminance ${lightTerm?.l.toFixed(3)})`)
  }
  if (!(await setTheme('dark'))) note('WARN', 'theme', 'could not return to the dark theme')
  await sleep(400)

  await traceBell('themes')

  // ── two viewers: sync and size arbitration ───────────────────────────────
  const page2 = await ctx.newPage()
  const p2Errors = []
  page2.on('pageerror', (e) => p2Errors.push(String(e)))
  await page2.setViewportSize({ width: 520, height: 700 })
  await page2.goto(BASE, { waitUntil: 'networkidle' })
  await sleep(2500)
  for (const e of p2Errors) note('FAIL', 'js/viewer2', `uncaught: ${e}`)

  // Wait rather than sampling: the affordance appears only once the subscribe
  // round trip has told this viewer it is passive.
  const takeControlVisible = await page2
    .locator('[data-testid="take-control"]')
    .waitFor({ state: 'visible', timeout: 8000 })
    .then(() => true)
    .catch(() => false)
  if (!takeControlVisible) {
    // Not a warning either. Either the affordance is missing — leaving a phone
    // permanently scaled with no way out — or the second viewer took the grid
    // by arriving, which is the reflow-under-somebody-else's-hands that the
    // whole arbitration design exists to prevent. Both are failures.
    note('FAIL', 'arbitration',
      'the small second viewer shows no "take control" affordance; it may have silently taken the grid')
  }
  // The affordance existing is not the same as somebody being able to see it,
  // and the check above cannot tell those apart: an element at opacity 0 is
  // visible to Playwright. This is the one screen where that distinction is
  // the whole product — the pill is the only way out of a scaled grid.
  await scanFaded(page2, 'the passive viewer')
  await scanCovered(page2, 'the passive viewer')

  await page.bringToFront()
  await page.locator('.xterm-screen').click()
  await page.keyboard.type('echo SYNC_TO_SECOND_VIEWER')
  await page.keyboard.press('Enter')
  let synced = false
  for (let i = 0; i < 40; i++) {
    const t = await screenText(page2)
    if (t.includes('SYNC_TO_SECOND_VIEWER')) { synced = true; break }
    await sleep(250)
  }
  if (!synced) note('FAIL', 'sync', 'the second viewer never saw output typed in the first')

  // A passive viewer scales the owner's grid down to fit. It must not scale it
  // down past the point where there is text on the screen.
  //
  // Measured on a phone watching a session a 1920 desktop owned: scale 0.29, a
  // 13px font rendered at under four pixels, the whole grid squeezed into the
  // top one per cent of the display with a thousand empty pixels underneath —
  // because width was the only binding constraint and nothing used the height.
  // That is not "displayed smaller", and the panel's reason to exist is being
  // able to read an agent's question from a phone.
  // Measured at phone width, which is where the floor actually bites and where
  // somebody actually reads this. page2 is already the passive viewer; shrink
  // it for the measurement and put it back.
  const viewer2Size = page2.viewportSize()
  await page2.setViewportSize({ width: 390, height: 844 })
  await sleep(1500)
  const legibility = await page2.evaluate(() => {
    const screen = document.querySelector('.xterm-screen')
    if (!screen) return null
    let el = screen
    let scale = 1
    while (el && el !== document.body) {
      const t = getComputedStyle(el).transform
      if (t && t !== 'none') { scale = Number(/matrix\(([^,]+)/.exec(t)?.[1] ?? 1); break }
      el = el.parentElement
    }
    const rows = document.querySelector('.xterm-rows')
    const font = Number(getComputedStyle(rows ?? screen).fontSize.replace('px', ''))
    return { scale: Number(scale.toFixed(3)), font, effective: Number((font * scale).toFixed(2)) }
  })
  if (!legibility) {
    note('FAIL', 'arbitration', 'the passive viewer has no terminal to measure')
  } else if (legibility.effective < 8) {
    note('FAIL', 'arbitration',
      `the passive viewer renders text at ${legibility.effective}px ` +
      `(${legibility.font}px scaled by ${legibility.scale}); below about 8px there are no glyphs left`)
  }

  await page2.screenshot({ path: join(SHOTS, 'viewer2-phone.png') })
  await page2.setViewportSize(viewer2Size)
  await sleep(1000)

  await page2.screenshot({ path: join(SHOTS, 'viewer2-narrow.png') })

  // The converse. A viewer the same size as the owner sees an identical
  // picture, so offering to take the grid would be a permanent button over
  // the terminal that changes nothing when pressed — and invites two windows
  // to trade ownership back and forth.
  const page3 = await ctx.newPage()
  await page3.setViewportSize(page.viewportSize())
  await page3.goto(BASE, { waitUntil: 'networkidle' })
  await sleep(3000)
  if (await page3.locator('[data-testid="take-control"]').isVisible().catch(() => false)) {
    const label = await page3.locator('[data-testid="take-control"]').innerText()
    note('FAIL', 'arbitration',
      `a viewer the same size as the grid owner is still offered "take control" (${label.trim()}); ` +
      'pressing it would change nothing on screen')
  }
  await page3.close()

  await traceBell('two-viewers')

  // A passive viewer resizing its own window must not move the shared grid.
  const gridBefore = await (await authed('/api/state')).json()
  await page2.setViewportSize({ width: 380, height: 600 })
  await sleep(1800)
  const gridAfter = await (await authed('/api/state')).json()
  const before = gridBefore.sessions.map((s) => `${s.id}:${s.cols}x${s.rows}`).join(',')
  const after = gridAfter.sessions.map((s) => `${s.id}:${s.cols}x${s.rows}`).join(',')
  if (before !== after) {
    note('FAIL', 'arbitration',
      `a passive viewer resizing its window moved the shared grid: ${before} -> ${after}`)
  }

  // ── reload keeps content ─────────────────────────────────────────────────
  // A background tab is throttled and stops painting, so xterm's DOM renderer
  // has nothing to read. Bring it forward before asserting on what it shows.
  await page.bringToFront()
  await sleep(500)
  const titleBefore = await page.locator('[data-testid="session-title"]').innerText().catch(() => '')
  await page.reload({ waitUntil: 'networkidle' })
  await sleep(3000)
  const titleAfter = await page.locator('[data-testid="session-title"]').innerText().catch(() => '')
  if (titleBefore && titleAfter && titleBefore !== titleAfter) {
    note('FAIL', 'selection',
      `a reload changed the selected session: ${JSON.stringify(titleBefore)} -> ${JSON.stringify(titleAfter)}`)
  }
  const afterReload = await screenText(page)
  if (!afterReload.includes('BROWSER_TYPED_OK')) {
    note('FAIL', 'replay',
      `scrollback did not come back after a reload; terminal shows ${JSON.stringify(
        afterReload.replace(/\s+/g, ' ').trim().slice(0, 200),
      )}`)
  }
  // The replay must not make the fresh terminal re-answer capability queries
  // it finds in the buffer; those answers land at the shell prompt.
  if (/\[\?\d+;\d+c|\[>\d+;\d+;\d+c/.test(afterReload)) {
    note('FAIL', 'replay',
      `terminal responses were injected into the session by the replay: ${JSON.stringify(
        afterReload.replace(/\s+/g, ' ').trim().slice(-120),
      )}`)
  }

  await traceBell('reloaded')

  // ── right panel ──────────────────────────────────────────────────────────
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(500)
  const showRight = page.locator('[data-testid="right-show"]')
  const rightPanel = page.locator('[data-testid="right-panel"]')
  // Guarded on the panel, not on the button. `right-show` used to exist only
  // while the panel was hidden, so its visibility answered "is the panel
  // closed" and clicking it was safe. It is a toggle now — always in the
  // header, reporting aria-pressed — which means clicking it while the panel
  // is open closes it, and everything below this line then fails for a reason
  // that has nothing to do with what it is checking.
  if (!(await rightPanel.isVisible().catch(() => false))) {
    await showRight.click().catch(() => {})
    await sleep(600)
  }
  if (!(await rightPanel.isVisible().catch(() => false))) {
    note('FAIL', 'panel', 'the side panel never appeared')
  } else {
    // Every control in the header has to stay reachable. Labelling all four
    // tabs once pushed the collapse button off the edge of a 280px column.
    // The strip draws no words at all now, which takes that risk away.
    for (const id of ['files', 'notes']) {
      await page.locator(`[data-testid="panel-tab-${id}"]`).click()
      await sleep(300)
      const header = await page.locator('[data-testid="panel-header"]').boundingBox()
      const collapse = await page.locator('[data-testid="panel-collapse"]').boundingBox()
      if (!header || !collapse) {
        note('FAIL', 'panel', `header controls missing on the ${id} tab`)
      } else if (collapse.x + collapse.width > header.x + header.width + 1) {
        note('FAIL', 'panel',
          `the collapse button overflows the header on the ${id} tab`)
      }
    }

    // Files
    await page.locator('[data-testid="panel-tab-files"]').click()
    await sleep(900)
    const fileCount = await page.locator('[data-testid="file-entry"]').count()
    if (fileCount === 0) note('FAIL', 'panel/files', 'the file browser listed nothing')

    // The machine, opened out of the dock. It is not a tab any more — it is the
    // bottom half of both tabs, and pressing its header gives it the column.
    const openBlock = async (block) => {
      // Idempotent: the dock header only exists while the block is compact, so
      // a second call while it is already open would time out.
      if ((await page.locator(`[data-testid="detail-${block}"]`).count()) === 0) {
        await page.locator(`[data-testid="dock-open-${block}"]`).click()
      }
      await sleep(400)
    }
    const closeBlock = async () => {
      await page.keyboard.press('Escape')
      await sleep(400)
    }
    await openBlock('monitor')
    await sleep(3000)
    const monitorText = await page.locator('[data-testid="system-monitor"]').innerText().catch(() => '')
    for (const want of ['CPU', 'Memory', 'Disk']) {
      if (!monitorText.includes(want)) {
        note('FAIL', 'panel/monitor', `the monitor is missing ${want}: ${JSON.stringify(monitorText)}`)
      }
    }
    if (/\bNaN\b|undefined/.test(monitorText)) {
      note('FAIL', 'panel/monitor', `the monitor rendered a broken value: ${JSON.stringify(monitorText)}`)
    }

    // The figures the payload has always carried and the panel never showed.
    // `diskPath` is the one that changes an answer -- "12% free" means one
    // thing about / and another about the volume a project sits on, and the
    // panel was not saying which it had measured. Load is the other: it is a
    // queue length, it is in every sample, and it was folded into the CPU
    // meter's detail line where it only appeared if the CPU was readable.
    //
    // Both come out of /proc, so both are asserted only where /proc is.
    if (process.platform === 'linux') {
      for (const want of ['Mount', 'Load']) {
        if (!monitorText.includes(want)) {
          note('FAIL', 'panel/monitor',
            `the monitor is missing ${want}: ${JSON.stringify(monitorText.replace(/\s+/g, ' ').trim())}`)
        }
      }
      // Per-session process counts, per row rather than only summed at the
      // foot. A pane at a shell prompt is one process reading zero, which is
      // true and uninteresting, and the row could not say which it was.
      const procCols = await page.locator('[data-testid="session-procs"]').count()
      if (procCols === 0) {
        note('FAIL', 'panel/monitor', 'no session row says how many processes it is measuring')
      }
    }

    // Per-session usage: the machine meters say the box is busy, and this says
    // which session is doing it. End to end against the real /proc rather than
    // a stub, because the part worth checking is the attribution -- a pane pid
    // walked to its whole tree and matched back to a session id -- and a stub
    // asserts nothing about any of that.
    // Case-insensitive: the heading is uppercased in CSS and innerText returns
    // what is rendered, not what the source says.
    if (!/per session/i.test(monitorText)) {
      note('FAIL', 'panel/monitor',
        `the monitor has no per-session section: ${JSON.stringify(monitorText)}`)
    }
    const usageRows = await page.locator('[data-testid="session-usage"]').count()
    if (usageRows === 0) {
      note('FAIL', 'panel/monitor',
        'no session was measured, so the panel cannot answer which one is running away: ' +
        JSON.stringify(monitorText.replace(/\s+/g, ' ').trim()))
    }
    // A percentage of the whole machine, never top's convention -- the machine
    // meter is an inch above it, and a session reading 310% beside a machine
    // reading 31% invites exactly one wrong conclusion.
    for (const m of monitorText.matchAll(/(\d+(?:\.\d+)?)%/g)) {
      if (Number(m[1]) > 100) {
        note('FAIL', 'panel/monitor', `a meter read ${m[1]}%, which is not a share of anything`)
      }
    }

    // No /proc is not the same as every session idle. A list of zeroes is a
    // measurement nobody made -- the same mistake the machine meters below
    // already have a check for.
    let usageRouteHits = 0
    await page.route('**/api/usage', async (route) => {
      usageRouteHits++
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ readable: false, cores: 8, sessions: {} }),
      })
    })
    await closeBlock()
    await openBlock('monitor')
    await sleep(3000)
    const noProc = await page.locator('[data-testid="system-monitor"]').innerText().catch(() => '')
    if (usageRouteHits === 0) {
      note('FAIL', 'panel/monitor', 'the fake /api/usage was never requested')
    } else if (!/No \/proc/.test(noProc)) {
      note('FAIL', 'panel/monitor',
        'a machine with no /proc gets no per-session explanation, just an empty list: ' +
        JSON.stringify(noProc.replace(/\s+/g, ' ').trim()))
    }
    if (await page.locator('[data-testid="session-usage"]').count() > 0) {
      note('FAIL', 'panel/monitor', 'sessions were drawn from a payload that says it measured nothing')
    }
    await page.unroute('**/api/usage')

    // A machine the panel cannot measure must not be described as an idle one.
    //
    // readMem returns zeroes when /proc/meminfo cannot be opened — every
    // darwin build, and any container that masks /proc — and the disk read
    // does the same when statfs fails. That rendered "Memory 0% · 0 B of 0 B"
    // and "Disk 0% · 0 B free": a measurement nobody made, claiming nothing is
    // in use. The CPU meter beside it already knew better.
    //
    // Served rather than provoked: this machine has a readable /proc, so the
    // only honest way to see that payload is to send it.
    let systemRouteHits = 0
    // Empty rather than absent: the assertions below match on "0%" across the
    // whole panel, and a real per-session row reading 0.0% would trip them
    // while saying nothing about the machine meters under test.
    await page.route('**/api/usage', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ readable: true, cores: 8, sessions: {} }),
      })
    })
    await page.route('**/api/system', async (route) => {
      systemRouteHits++
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          at: Date.now(), cpuPercent: null, cpuReadable: false, cores: 8,
          load1: 1.2, load5: 0.9, load15: 0.7,
          memTotal: 0, memAvailable: 0, swapTotal: 0, swapFree: 0,
          diskTotal: 0, diskFree: 0, uptime: 0,
        }),
      })
    })
    await closeBlock()
    await openBlock('monitor')
    await sleep(3500)
    const unmeasured = await page.locator('[data-testid="system-monitor"]').innerText().catch(() => '')
    if (systemRouteHits === 0) {
      note('FAIL', 'panel/monitor',
        'the fake /api/system was never requested, so every assertion below it is about the ' +
        'real machine rather than the payload under test')
    }
    if (/0 B of 0 B|\b0%/.test(unmeasured)) {
      note('FAIL', 'panel/monitor',
        `the monitor reports a machine it cannot measure as an idle one: ` +
        `${JSON.stringify(unmeasured.replace(/\s+/g, ' ').trim())}`)
    }
    if (/\bup 0m\b/.test(unmeasured)) {
      note('FAIL', 'panel/monitor',
        `an unread /proc/uptime is shown as "up 0m", which reads as a machine that just ` +
        `booted: ${JSON.stringify(unmeasured.replace(/\s+/g, ' ').trim())}`)
    }
    if (/sampling…/.test(unmeasured)) {
      note('FAIL', 'panel/monitor',
        `the CPU meter promises a sample on a machine that has no counters to sample; ` +
        `"sampling…" renews itself every two seconds forever: ` +
        `${JSON.stringify(unmeasured.replace(/\s+/g, ' ').trim())}`)
    }
    if (!unmeasured.includes('—')) {
      note('FAIL', 'panel/monitor',
        `an unreadable meter should read "—" like the CPU one does before its first ` +
        `sample: ${JSON.stringify(unmeasured.replace(/\s+/g, ' ').trim())}`)
    }
    await page.unroute('**/api/system')
    await page.unroute('**/api/usage')
    await closeBlock()

    // The six figures, and the fact that they are six ranks rather than six
    // cards. 「几个数字 有布局 … 好看一点」 — the layout is the request, so
    // this measures it: the hero has to be visibly larger than the pair, and
    // the pair has to be the same size as each other.
    await page.locator('[data-testid="panel-tab-files"]').click()
    await sleep(2500)
    const block = page.locator('[data-testid="token-block"]')
    if ((await block.count()) !== 1) {
      note('FAIL', 'panel/spend', 'the token block is not in the dock')
    } else {
      const ranks = await block.evaluate((el) =>
        [...el.querySelectorAll('[data-testid="spend-figure"]')].map((v) => ({
          label: v.getAttribute('data-rank'),
          size: parseFloat(getComputedStyle(v).fontSize),
        })))
      if (ranks.length < 3) {
        note('FAIL', 'panel/spend', `only ${ranks.length} figures in the block`)
      } else {
        const [hero, ...pair] = ranks
        if (!(hero.size > pair[0].size)) {
          note('FAIL', 'panel/spend',
            `today is not the largest figure: ${JSON.stringify(ranks)}`)
        }
        if (pair.length >= 2 && pair[0].size !== pair[1].size) {
          note('FAIL', 'panel/spend',
            `the two context figures are different sizes: ${JSON.stringify(ranks)}`)
        }
        // Three ranks and no more. Four sizes in a block this small is the
        // "nine font sizes" complaint the scale exists for, one layer up.
        const sizes = new Set(ranks.map((r) => r.size))
        if (sizes.size > 2) {
          note('FAIL', 'panel/spend',
            `${sizes.size} figure sizes in one block: ${JSON.stringify(ranks)}`)
        }
      }
      const footer = await page.locator('[data-testid="token-block-footer"]').innerText()
        .catch(() => '')
      // 时间 and 字数, as qualifiers rather than as two more cards: what was
      // produced, over what period, as of when.
      if (!footer.trim()) {
        note('FAIL', 'panel/spend', 'the block does not say what period it covers')
      }
      // Printed rather than asserted, and that is the honest outcome.
      //
      // The rule is that a reading is dated only once it is behind: inside
      // usage.MinInterval the panel calls it current, so saying "11 seconds
      // ago" is the panel narrating its own scan rather than describing the
      // figures -- the reported `输出 51.5M · 近 30 天 · 11秒钟前读的`.
      //
      // But `scannedAt` is 0 here. The pass that sets it does not finish inside
      // this check's lifetime, so the gate and no gate render identically and
      // no assertion could tell them apart. One was written, the guard it was
      // written for was removed, and nothing went red -- which is the only way
      // to find out that a check is decorative. The logic is covered by
      // ago.test.ts instead; making this bite needs a stale reading, and that
      // means a clock this check does not control.
      note('PASS', 'panel/spend', `footer: ${JSON.stringify(footer)}`)
      // And nothing that implies the panel counted characters or money.
      const blockText = await block.innerText()
      for (const lie of ['$', '¥', '€']) {
        if (blockText.includes(lie)) {
          note('FAIL', 'panel/spend',
            `the block shows ${lie}; pricing is per model and per tier and the panel ` +
            `does not know it: ${JSON.stringify(blockText)}`)
        }
      }
    }

    // A panel you resized and then closed has to come back the size you left
    // it, and the size and the closed-ness have to be two stored things.
    //
    // They were one: zero width meant collapsed, so closing the panel wrote 0
    // over the only record of the width, and reopening fell back to the
    // built-in default. Drag the notes panel out to read something, glance at
    // the terminal, open it again, and it is narrow — every time. The comment
    // above the state said the opposite of what the encoding allowed.
    const panelWidth = async () => {
      const box = await rightPanel.boundingBox().catch(() => null)
      return box ? Math.round(box.width) : 0
    }
    const startWidth = await panelWidth()
    const divider = page.locator('[data-testid="panel-resize"]')
    const grip = await divider.boundingBox()
    if (!grip) {
      note('FAIL', 'panel', 'the resize divider is not there to drag')
    } else {
      await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
      await page.mouse.down()
      await page.mouse.move(grip.x - 140, grip.y + grip.height / 2, { steps: 8 })
      await page.mouse.up()
      await sleep(700)
      const chosen = await panelWidth()
      if (chosen <= startWidth + 40) {
        note('WARN', 'panel', `dragging the divider moved the panel from ${startWidth} to ${chosen}; ` +
          'the restore check below cannot tell a remembered width from the default')
      } else {
        await page.locator('[data-testid="panel-collapse"]').click()
        await sleep(700)
        await page.locator('[data-testid="right-show"]').click()
        await sleep(900)
        const reopened = await panelWidth()
        if (Math.abs(reopened - chosen) > 4) {
          note('FAIL', 'panel',
            `a panel dragged to ${chosen}px reopened at ${reopened}px; closing it threw the width away`)
        }
      }
    }

    // Notes: typing must reach the server without a save button, and the
    // status has to say so — "did that save?" is otherwise unanswerable.
    await page.locator('[data-testid="panel-tab-notes"]').click()
    await sleep(700)
    await page.locator('[data-testid="notes"] textarea').fill('remember: NOTE_PERSIST_OK')
    let savedOk = false
    for (let i = 0; i < 25; i++) {
      const st = await page.locator('[data-testid="notes-status"]').getAttribute('data-status')
      if (st === 'saved') { savedOk = true; break }
      await sleep(400)
    }
    if (!savedOk) note('FAIL', 'panel/notes', 'the note never reported itself saved')

    // The same note, but leaving the panel before the debounce has elapsed.
    //
    // The check above waits for 'saved' before doing anything else, so it only
    // ever exercised the patient case. The impatient one lost the text: the
    // panel renders one tab at a time, so clicking Files unmounted Notes, and
    // the unmount cancelled the pending save. Typing and immediately switching
    // tab — one click, entirely ordinary — silently discarded up to 800ms of
    // it. Same for switching project, and for closing the page.
    //
    // Asserted against the server rather than the textarea: the question is
    // whether the write actually landed, and a remounted panel showing the old
    // text would answer a different one.
    await page.locator('[data-testid="notes"] textarea')
      .fill('remember: NOTE_PERSIST_OK\nNOTE_FLUSH_OK')
    await page.locator('[data-testid="panel-tab-files"]').click()
    let flushed = ''
    for (let i = 0; i < 20; i++) {
      await sleep(300)
      const n = await (await authed(`/api/projects/${proj.id}/notes`)).json().catch(() => ({}))
      flushed = n.content ?? ''
      if (flushed.includes('NOTE_FLUSH_OK')) break
    }
    if (!flushed.includes('NOTE_FLUSH_OK')) {
      // Report what the panel thought was happening, not only what the server
      // ended up with. This failed once, intermittently, and the message said
      // nothing about why — which is the difference between a bug and a
      // rumour.
      const status = await page.locator('[data-testid="notes-status"]').getAttribute('data-status')
        .catch(() => '?')
      const complaint = consoleWarnings.filter((w) => w.includes('note could not be saved')).slice(-1)
      note('FAIL', 'panel/notes',
        `leaving the tab mid-edit threw the edit away; the server still has ` +
        `${JSON.stringify(flushed)}, the panel last reported status ${JSON.stringify(status)}` +
        (complaint.length ? `, and it said: ${JSON.stringify(complaint[0])}` : ', and it said nothing'))
    }
    await page.locator('[data-testid="panel-tab-notes"]').click()
    await sleep(600)

    // The other way out: closing the page. That save has to go out with
    // keepalive, because a browser cancels an ordinary fetch from a document
    // that is going away — so this half of the fix is a different mechanism
    // from the one above and fails on its own. Done on a throwaway page, so
    // the check cannot disturb the rest of the run.
    const leaving = await ctx.newPage()
    await leaving.setViewportSize({ width: 1440, height: 900 })
    await leaving.goto(BASE, { waitUntil: 'networkidle' })
    await sleep(2000)
    // On the panel, not on the button — see the guard at the top of this
    // section. `right-show` is a toggle, so asking whether it is visible
    // answers "am I on a wide screen", and clicking it here would close the
    // panel this page was opened to type into.
    if (!(await leaving.locator('[data-testid="right-panel"]').isVisible().catch(() => false))) {
      await leaving.locator('[data-testid="right-show"]').click().catch(() => {})
      await sleep(600)
    }
    await leaving.locator('[data-testid="panel-tab-notes"]').click().catch(() => {})
    await sleep(900)
    await leaving.locator('[data-testid="notes"] textarea')
      .fill('remember: NOTE_PERSIST_OK\nNOTE_FLUSH_OK\nNOTE_CLOSE_OK')
    await leaving.close({ runBeforeUnload: true })
    let onClose = ''
    for (let i = 0; i < 20; i++) {
      await sleep(300)
      const n = await (await authed(`/api/projects/${proj.id}/notes`)).json().catch(() => ({}))
      onClose = n.content ?? ''
      if (onClose.includes('NOTE_CLOSE_OK')) break
    }
    if (!onClose.includes('NOTE_CLOSE_OK')) {
      note('FAIL', 'panel/notes',
        `closing the page threw away the edit; the server has ${JSON.stringify(onClose)}`)
    }
    // Let the still-open viewer adopt the new revision before anything else
    // touches the note, so a later save is not refused as a conflict.
    await sleep(1500)

    // The checklist is gone from the panel entirely — 「也不要留下 todo」 — and
    // "gone" has to mean gone rather than hidden. The routes are still there
    // for the wall boards and for an API client; nothing in the panel offers
    // them, and this is what says so.
    await page.locator('[data-testid="panel-tab-notes"]').click()
    await sleep(700)
    for (const id of ['todos', 'todo-input', 'todo-item', 'panel-tab-todos']) {
      if ((await page.locator(`[data-testid="${id}"]`).count()) > 0) {
        note('FAIL', 'panel/notes', `the checklist is still in the panel: [${id}] is on screen`)
      }
    }

    // Two tabs, and the same dock under both. Whichever one you are on, the two
    // things you glance at are in the same place at the same size.
    for (const [tab, top] of [['notes', 'notes'], ['files', 'file-tree']]) {
      await page.locator(`[data-testid="panel-tab-${tab}"]`).click()
      await sleep(900)
      const divider = page.locator(`[data-testid="stack-${tab}-divider"]`)
      if ((await divider.count()) !== 1) {
        note('FAIL', 'panel', `the ${tab} tab is not two panels with a divider`)
        continue
      }
      const topBox = await page.locator(`[data-testid="${top}"]`).boundingBox().catch(() => null)
      const dockBox = await page.locator('[data-testid="panel-dock"]').boundingBox()
        .catch(() => null)
      if (!topBox || !dockBox) {
        note('FAIL', 'panel',
          `the ${tab} tab is missing a half: top=${!!topBox} dock=${!!dockBox}`)
      } else if (dockBox.y <= topBox.y) {
        note('FAIL', 'panel', `the ${tab} tab draws the dock above its own content`)
      }
      for (const b of ['tokens', 'monitor']) {
        if ((await page.locator(`[data-testid="dock-${b}"]`).count()) !== 1) {
          note('FAIL', 'panel', `the ${b} block is not in the dock on the ${tab} tab`)
        }
      }
      // And the divider drags. It is the gesture the whole restructure rests
      // on -- 「可以上下拖动」.
      const before = await page.locator(`[data-testid="stack-${tab}-top"]`).boundingBox()
      const grip = await divider.boundingBox()
      await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
      await page.mouse.down()
      await page.mouse.move(grip.x + grip.width / 2, grip.y - 120, { steps: 10 })
      await sleep(200)
      await page.mouse.up()
      await sleep(400)
      const after = await page.locator(`[data-testid="stack-${tab}-top"]`).boundingBox()
      if (!(before.height - after.height > 60)) {
        note('FAIL', 'panel',
          `the ${tab} divider does not drag (${Math.round(before.height)} -> ${Math.round(after.height)})`)
      }
    }

    // The repository, as a line above the file list rather than as a panel or
    // a tab. The check runs in this repository, so there is one to find.
    await page.locator('[data-testid="panel-tab-files"]').click()
    await sleep(1200)
    const repoLine = page.locator('[data-testid="repo-line"]')
    if ((await repoLine.count()) !== 1) {
      note('FAIL', 'panel/repo', 'the file tree has no repository line above it')
    } else {
      const lineBox = await repoLine.boundingBox()
      const entry = await page.locator('[data-testid="file-entry"]').first().boundingBox()
        .catch(() => null)
      if (entry && lineBox.y >= entry.y) {
        note('FAIL', 'panel/repo', 'the repository line is below the listing it describes')
      }
      // And it opens the whole panel, same gesture as the dock's two blocks.
      await repoLine.click()
      await sleep(600)
      if ((await page.locator('[data-testid="detail-repo"]').count()) !== 1) {
        note('FAIL', 'panel/repo', 'pressing the repository line opens nothing')
      }
      if ((await page.locator('[data-testid="git-panel"]').count()) !== 1) {
        note('FAIL', 'panel/repo', 'the opened repository shows no repository panel')
      }
      await page.keyboard.press('Escape')
      await sleep(500)
      if ((await page.locator('[data-testid="detail-repo"]').count()) !== 0) {
        note('FAIL', 'panel/repo', 'Escape did not close the repository panel')
      }
    }

    // The project's name and its repository, at the foot of the sidebar.
    // 「面板左下角等等地方 都加上GitHub链接和项目名」.
    const mark = page.locator('[data-testid="sidebar-project"]')
    if ((await mark.count()) !== 1) {
      note('FAIL', 'panel/project', 'the sidebar does not say which project you are in')
    } else {
      const link = page.locator('[data-testid="sidebar-project-link"]')
      if ((await link.count()) === 1) {
        const href = await link.getAttribute('href')
        if (!/^https:\/\/github\.com\/[\w.-]+\/[\w.-]+$/.test(href ?? '')) {
          note('FAIL', 'panel/project',
            `the repository link is not a github.com project URL: ${JSON.stringify(href)}`)
        }
      }
    }

    await page.locator('[data-testid="panel-tab-notes"]').click()
    await sleep(700)

    // The note must survive a reload; it is the panel's only durable prose.
    await page.reload({ waitUntil: 'networkidle' })
    await sleep(2500)
    const kept = await page.locator('[data-testid="notes"] textarea').inputValue().catch(() => '')
    if (!kept.includes('NOTE_PERSIST_OK')) {
      note('FAIL', 'panel/notes', `the note did not survive a reload: ${JSON.stringify(kept)}`)
    }
    await page.screenshot({ path: join(SHOTS, 'right-panel.png') })
  }

  await traceBell('right-panel')

  // ── bottom terminals ─────────────────────────────────────────────────────
  // They belong to the session above them and follow it. A terminal opened
  // while working on one thing must not still be sitting there, pointing at
  // the wrong directory, after switching to another.
  const showBottom = page.locator('[data-testid="bottom-show"]')
  if (await showBottom.isVisible().catch(() => false)) {
    await showBottom.click()
    await sleep(600)
  }
  const bottom = page.locator('[data-testid="bottom-terminals"]')
  if (!(await bottom.isVisible().catch(() => false))) {
    note('FAIL', 'bottom', 'the bottom terminal panel never appeared')
  } else {
    await page.locator('[data-testid="bottom-new"]').click()
    let gotTab = false
    for (let i = 0; i < 30; i++) {
      if ((await page.locator('[data-testid="bottom-tab"]').count()) > 0) { gotTab = true; break }
      await sleep(400)
    }
    if (!gotTab) {
      note('FAIL', 'bottom', 'creating a bottom terminal produced no tab')
    } else {
      // It must be a working terminal, not just a tab.
      const bottomScreen = bottom.locator('.xterm-screen')
      await bottomScreen.click()

      await page.keyboard.type('echo BOTTOM_TERMINAL_OK')
      await page.keyboard.press('Enter')

      // Read this terminal by name, not "whichever has focus".
      //
      // Asking for the focused one made this the flakiest assertion in the
      // file -- red on roughly one run in three. `vibepanelScreen` used to
      // fall back to the first terminal in the map when focus was on none of
      // them, and with a main terminal above and a scratch one below, the
      // frame where focus is between the two answered about the wrong screen.
      // The fallback is gone now, but naming the terminal is what makes this
      // check independent of focus at all: what it is asserting is that a
      // bottom terminal works, not where the caret happens to be.
      //
      // Through the buffer, not the DOM: `.xterm-rows` is empty under the GPU
      // renderer however full the terminal is.
      const bottomID = await page.locator('[data-testid="bottom-tab"]').first()
        .getAttribute('data-session-id')
      if (!bottomID) {
        note('FAIL', 'bottom', 'a bottom tab carries no session id, so its screen cannot be read')
      }
      // Rows joined before matching, because a terminal wraps.
      //
      // Two occurrences are wanted -- the shell's echo of the line, then the
      // command's output -- and the buffer is a grid, so a line longer than the
      // pane is two rows with the marker split across them:
      //
      //     …/agent-adc49cc79424c01b2/web$ echo BO
      //     TTOM_TERMINAL_OK
      //     BOTTOM_TERMINAL_OK
      //
      // One match instead of two, and a check that reported "produced no
      // output" about a terminal that had echoed perfectly. Whether it wrapped
      // depended on how long the *prompt* was, which is how deep the checkout
      // is on whoever's disk -- so this passed in a clone at the top of a home
      // directory and failed in a worktree, for two months, looking like a
      // race. Joining the rows is what the terminal is showing anyway: the
      // wrap is a rendering artefact of the pane's width, not something in the
      // output.
      let echoed = false
      for (let i = 0; i < 40; i++) {
        const txt = (await screenText(page, undefined, { id: bottomID })).replace(/\n/g, '')
        if ((txt.match(/BOTTOM_TERMINAL_OK/g) ?? []).length >= 2) { echoed = true; break }
        await sleep(300)
      }
      if (!echoed) note('FAIL', 'bottom', 'typing into a bottom terminal produced no output')

      // A second terminal, to check the tabs are told apart. They all live in
      // the same directory as the session above them, so a naming rule based
      // on the directory would produce a row of identical tabs.
      await page.locator('[data-testid="bottom-new"]').click()
      await sleep(2500)
      const labels = await page.$$eval('[data-testid="bottom-tab"]', (els) =>
        els.map((el) => el.textContent?.trim() ?? ''),
      )
      if (labels.length < 2) {
        note('WARN', 'bottom', `expected two terminal tabs, saw ${JSON.stringify(labels)}`)
      } else if (new Set(labels).size !== labels.length) {
        note('FAIL', 'bottom', `terminal tabs are indistinguishable: ${JSON.stringify(labels)}`)
      }

      // Tabs must not move when a terminal prints.
      //
      // The strip is a filter over the session list, which is ordered by
      // urgency and recent output — right for the sidebar, wrong for tabs. A
      // terminal that printed jumped to the front, and since the automatic
      // label is positional the tab was renamed as well as moved: the one you
      // had been using was neither where you left it nor called what you had
      // been calling it.
      // By id, not by label. The automatic label is positional — "term 1" is
      // whatever is first — so a reorder leaves the names exactly where they
      // were and swaps the terminals underneath them. Comparing the text
      // cannot see that, which is worse than not checking: the damage is that
      // the tab called "term 2" is now a different terminal.
      const tabIds = () =>
        page.$$eval('[data-testid="bottom-tab"]', (els) =>
          els.map((el) => el.getAttribute('data-session-id') ?? ''),
        )
      const orderBefore = await tabIds()
      // The last tab, deliberately. Printing in the first one cannot move it
      // when the order is by recency — it is already first — so this passed
      // against the unsorted version and proved nothing.
      await page.locator('[data-testid="bottom-tab"]').last().click()
      await sleep(500)
      await bottom.locator('.xterm-screen').last().click()
      await page.keyboard.type('echo SHUFFLE')
      await page.keyboard.press('Enter')
      await sleep(1500)
      // Reload rather than poking the API to force a fresh list.
      //
      // Output alone does not push a new session list — the browser keeps the
      // order it was last sent, so the shuffle appears the next time anything
      // changes state, which is exactly when the user is least expecting the
      // tabs to move. The first version of this forced that by patching a
      // session's state, and the session it happened to pick was the one a
      // later check needed left alone: it turned the bell session to done and
      // broke a check three sections away. A reload asks for the same fresh
      // list and changes nothing.
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.waitForSelector('[data-testid="bottom-tab"]', { timeout: 20000 }).catch(() => {})
      await sleep(2500)
      const orderAfter = await tabIds()
      if (JSON.stringify(orderBefore) !== JSON.stringify(orderAfter)) {
        note('FAIL', 'bottom',
          `printing in a terminal reordered the tabs: ${JSON.stringify(orderBefore)} became ` +
          `${JSON.stringify(orderAfter)}`)
      }

      // A bottom terminal whose process is gone has to say so on its tab.
      //
      // The strip showed a name and a close button and nothing else, so a build
      // that died down there looked exactly like a build still running — and
      // the bottom strip is where builds and tests live, which makes "did it
      // finish" the only question anybody asks of it.
      const lastTab = page.locator('[data-testid="bottom-tab"]').last()
      await lastTab.click()
      await sleep(600)
      await bottom.locator('.xterm-screen').last().click()
      await page.keyboard.type('exit')
      await page.keyboard.press('Enter')
      let marked = false
      for (let i = 0; i < 40; i++) {
        const labelled = await lastTab.locator('svg[role="img"]').count().catch(() => 0)
        if (labelled > 0) { marked = true; break }
        await sleep(400)
      }
      if (!marked) {
        const st = await (await authed('/api/state')).json()
        const kids = (st.sessions ?? [])
          .filter((x) => x.parentSessionId)
          .map((x) => ({ title: x.title, exited: x.exited, status: x.exitStatus, state: x.state }))
        const html = await lastTab.innerHTML().catch(() => '?')
        note('FAIL', 'bottom',
          'a bottom terminal whose shell exited shows nothing on its tab; it is indistinguishable ' +
          `from one still running. The API says ${JSON.stringify(kids)} and the tab is ` +
          `${JSON.stringify(html.slice(0, 200))}`)
      }

      // Switching the main session must swap the strip, not carry it across.
      const otherRow = page.locator('[data-testid="session-row"]', { hasText: 'htop' }).first()
      if (await otherRow.isVisible().catch(() => false)) {
        await otherRow.click()
        await sleep(1500)
        const carried = await page.locator('[data-testid="bottom-tab"]').count()
        if (carried !== 0) {
          note('FAIL', 'bottom',
            `switching sessions carried ${carried} terminal tab(s) from the previous one`)
        }
        // And switching back brings it back.
        await page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first().click()
        await sleep(1500)
        if ((await page.locator('[data-testid="bottom-tab"]').count()) === 0) {
          note('FAIL', 'bottom', 'the terminal did not come back when switching back')
        }
      }
    }
    await page.screenshot({ path: join(SHOTS, 'bottom.png') })
  }

  await traceBell('bottom')

  // ── session state ────────────────────────────────────────────────────────
  // The bell has to reach the panel through tmux, the PTY and the detector.
  // tmux's bell-action swallowed it entirely once, silently, so this is worth
  // asserting end to end rather than trusting the unit tests.
  let sawWaiting = false
  for (let i = 0; i < 40; i++) {
    const states = await page.$$eval('[data-testid="state-dot"]', (els) =>
      els.map((el) => el.getAttribute('data-state')),
    )
    if (states.includes('waiting')) { sawWaiting = true; break }
    await sleep(500)
  }
  if (!sawWaiting) {
    const truth = await (await authed('/api/state')).json()
    const summary = truth.sessions
      .map((x) => `${x.title || x.command}=${x.state}/${x.stateSource}`)
      .join(' ')
    const dots = await page.$$eval('[data-testid="state-dot"]', (els) =>
      els.map((el) => el.getAttribute('data-state')),
    )
    note('FAIL', 'state',
      `a session that rang the bell never showed as waiting\n         trace: ${bellTrace.join(' -> ')}\n         server: [${summary}]\n         UI dots: ${JSON.stringify(dots)}`)
  } else {
    // And it must be visible from another tab, which is where the user
    // actually is.
    const title = await page.title()
    if (!/^\(\d+\)/.test(title)) {
      note('FAIL', 'state', `tab title is ${JSON.stringify(title)}; a waiting session should show a count`)
    }

    // Red line 4: shape carries the meaning, not only hue. Two states that
    // differ by colour alone are the same state to a colour-blind reader, and
    // to anyone reading this on a phone in a dark room — which is the moment
    // the panel was built for. Compared here rather than in a unit test
    // because what matters is what the browser actually drew.
    const glyphFor = async (state) => {
      const el = page.locator(`[data-testid="state-dot"][data-state="${state}"] svg`).first()
      if ((await el.count()) === 0) return null
      return el.innerHTML()
    }
    const shapes = []
    for (const st of ['waiting', 'working', 'done']) {
      shapes.push([st, await glyphFor(st)])
    }
    const absent = shapes.filter(([, v]) => v === null).map(([k]) => k)
    if (absent.length) {
      // A setup gap, not a product failure — but said out loud, because a
      // comparison that did not happen reads exactly like one that passed.
      note('WARN', 'a11y',
        `no session was in ${absent.join(', ')} at this moment, so those shapes were not compared`)
    }
    const drawn = shapes.filter(([, v]) => v !== null)
    for (let i = 0; i < drawn.length; i++) {
      for (let j = i + 1; j < drawn.length; j++) {
        if (drawn[i][1] === drawn[j][1]) {
          note('FAIL', 'a11y',
            `"${drawn[i][0]}" and "${drawn[j][0]}" draw an identical shape; only colour ` +
            'tells them apart')
        }
      }
    }

    // Clicking the indicator is how you say "I have dealt with this".
    const waitingDot = page.locator('[data-testid="state-dot"][data-state="waiting"]').first()
    await waitingDot.click()
    let cleared = false
    for (let i = 0; i < 20; i++) {
      const still = await page.locator('[data-testid="state-dot"][data-state="waiting"]').count()
      if (still === 0) { cleared = true; break }
      await sleep(400)
    }
    if (!cleared) {
      note('FAIL', 'state', 'clicking the indicator did not clear the waiting state')
    } else {
      // And the override must survive the poller, or the control is useless.
      await sleep(5000)
      const back = await page.locator('[data-testid="state-dot"][data-state="waiting"]').count()
      if (back > 0) note('FAIL', 'state', 'the manual override was undone by the poller')
    }
  }

  // ── dragging a project reorders it ───────────────────────────────────────
  // Built on Pointer Events precisely so it works on touch as well as mouse,
  // which HTML5 drag-and-drop does not, so it is worth proving it works at all.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(600)
  const projectNames = async () =>
    page.$$eval('[data-testid="project-group"]', (els) =>
      els.map((el) => el.querySelector('span')?.textContent?.trim() ?? ''),
    )
  const beforeDrag = await projectNames()
  if (beforeDrag.length < 2) {
    note('WARN', 'reorder', `expected two projects to drag, saw ${JSON.stringify(beforeDrag)}`)
  } else {
    const groups = page.locator('[data-testid="project-group"]')
    const second = groups.nth(1)
    await second.hover()
    const grip = second.locator('[data-testid="project-grip"]')
    const gripBox = await grip.boundingBox()
    const firstBox = await groups.nth(0).boundingBox()
    if (!gripBox || !firstBox) {
      note('WARN', 'reorder', 'could not locate the drag handle')
    } else {
      await page.mouse.move(gripBox.x + gripBox.width / 2, gripBox.y + gripBox.height / 2)
      await page.mouse.down()
      // Above the first project's midpoint, which is where the gap moves.
      await page.mouse.move(firstBox.x + 40, firstBox.y + firstBox.height / 2 - 8, { steps: 12 })
      await page.mouse.up()
      await sleep(1200)
      const afterDrag = await projectNames()
      if (afterDrag[0] !== beforeDrag[1]) {
        note('FAIL', 'reorder',
          `dragging the second project to the top did nothing: ${JSON.stringify(beforeDrag)} -> ${JSON.stringify(afterDrag)}`)
      }
      // And the panel should now offer a way back to activity ordering.
      const toAuto = page.locator('[data-testid="order-auto"]')
      if (!(await toAuto.isVisible().catch(() => false))) {
        note('FAIL', 'reorder',
          'after a manual reorder there is no control to return to automatic ordering')
      } else {
        // Switching ordering must not throw the arrangement away.
        //
        // It used to: the control ran `UPDATE projects SET sort_index = NULL`,
        // so one click on a clock icon with no confirmation destroyed an
        // arrangement somebody had sat down and made — and then removed
        // itself, because it only renders in manual mode, leaving nothing to
        // click and no way back. Measured before the fix: four projects
        // arranged `delta bravo alpha charlie`, one click, `alpha bravo
        // charlie delta`, unrecoverable.
        const arranged = await projectNames()
        await toAuto.click()
        await sleep(1200)
        const automatic = await projectNames()

        // Asserted against the server, because whether the arrangement still
        // exists does not depend on the two orderings looking different — and
        // in this fixture they often do not, since the project that was
        // dragged to the top is also the one being clicked into.
        const st = await (await authed('/api/state')).json()
        if (st.hasProjectOrder !== true) {
          note('FAIL', 'reorder',
            'switching to activity ordering discarded the arrangement; there is nothing ' +
            'to go back to and the control that did it removes itself')
        }
        const toManual = page.locator('[data-testid="order-manual"]')
        if (!(await toManual.isVisible().catch(() => false))) {
          note('FAIL', 'reorder', 'no control is offered to return to the arrangement')
        } else if (automatic.join(' ') === arranged.join(' ')) {
          note('INFO', 'reorder',
            'the two orderings agree in this fixture; the round trip is covered by the ' +
            'stored-arrangement check above rather than by comparing what is on screen')
        } else {
          await toManual.click()
          await sleep(1200)
          const restored = await projectNames()
          if (restored.join(' ') !== arranged.join(' ')) {
            note('FAIL', 'reorder',
              `going back gave ${JSON.stringify(restored)}, want the arrangement ` +
              `${JSON.stringify(arranged)}; switching ordering discarded it`)
          }
        }
      }
    }
  }

  // ── the narrow-screen drawer must be opaque ──────────────────────────────
  // It floats over the terminal. A translucent one leaves session output
  // showing through the project list, and backdrop-filter cannot be relied on
  // to hide it — headless browsers and prefers-reduced-transparency both skip
  // it entirely.
  await page.setViewportSize({ width: 390, height: 844 })
  await sleep(700)
  // By testid, and loudly. This was `header button[title="Projects"]`, and the
  // title is not "Projects" when something is waiting -- it is "Projects — 2
  // waiting for you" -- so the selector missed and the whole drawer check
  // below it was skipped by the isVisible guard. A check that stops checking
  // looks exactly like a check that passes. It is also now translated, which
  // would have broken the old selector a second way.
  const menu = page.locator('[data-testid="menu-button"]')
  if (!(await menu.isVisible().catch(() => false))) {
    note('FAIL', 'mobile', 'no menu button at 390 wide, so the drawer cannot be opened or checked')
  } else {
    await menu.click()
    await sleep(600)
    const drawer = await page.evaluate(() => {
      const el = document.querySelector('[data-testid="sidebar"][data-overlay="true"]')
      if (!el) return null
      const cs = getComputedStyle(el)
      const m = cs.backgroundColor.match(/rgba?\(([^)]+)\)/)
      const parts = m ? m[1].split(/[\s,/]+/).filter(Boolean).map(Number) : []
      return { bg: cs.backgroundColor, alpha: parts.length > 3 ? parts[3] : 1 }
    })
    if (!drawer) {
      note('WARN', 'layout/drawer', 'the narrow-screen drawer did not open')
    } else if (drawer.alpha < 0.98) {
      note('FAIL', 'layout/drawer',
        `the drawer is translucent (${drawer.bg}); terminal output shows through the project list`)
    }
    // Close through the drawer's own control; the full-screen backdrop sits
    // underneath it and cannot be clicked where the drawer covers it.
    await page
      .locator('[data-testid="sidebar"][data-overlay="true"] header button')
      .first()
      .click({ timeout: 3000 })
      .catch(() => {})
    await sleep(400)
  }

  for (const e of consoleErrors) note('WARN', 'console', e)
  for (const f of failedReqs) note('FAIL', 'net', f)

  // ── horizontal overflow, desktop and phone ───────────────────────────────
  for (const [label, vp] of [['desktop', { width: 1440, height: 900 }], ['phone', { width: 390, height: 844 }]]) {
    await page.setViewportSize(vp)
    await sleep(600)
    const over = await page.evaluate(() =>
      document.documentElement.scrollWidth - document.documentElement.clientWidth)
    // The scale check already treats this as a failure, and it is the same
    // defect in both places: a panel that scrolls sideways has lost a column
    // off the edge of somebody's screen.
    if (over > 1) note('FAIL', `layout/${label}`, `page scrolls horizontally by ${over}px`)
    await page.screenshot({ path: join(SHOTS, `${label}.png`) })
  }
  // ── the phone layout ─────────────────────────────────────────────────────
  // Not a squeezed desktop: typing into a raw terminal is unusable with an
  // input method, so input arrives through a compose box and a bar of the keys
  // a phone does not have.
  await page.setViewportSize({ width: 390, height: 844 })
  await sleep(1200)

  // Make sure a shell is selected, not the sleeping session that sorts first.
  const phoneMenu = page.locator('header button[title^="Projects"]')
  if (await phoneMenu.isVisible().catch(() => false)) {
    await phoneMenu.click()
    await sleep(600)
    const shell = page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first()
    if (await shell.isVisible().catch(() => false)) {
      await shell.click()
      await sleep(1500)
    }
  }

  const compose = page.locator('[data-testid="compose-input"]')
  const keyBar = page.locator('[data-testid="key-bar"]')
  if (!(await compose.isVisible().catch(() => false))) {
    note('FAIL', 'mobile', 'no compose box at phone width')
  } else if (!(await keyBar.isVisible().catch(() => false))) {
    note('FAIL', 'mobile', 'no key bar at phone width')
  } else {
    // Tapping the terminal must not raise the software keyboard over the thing
    // being read, so xterm does not take input at this width.
    const takesInput = await page.evaluate(() => {
      const ta = document.querySelector('.xterm-helper-textarea')
      return ta ? !ta.hasAttribute('disabled') && ta.getAttribute('readonly') === null : null
    })
    if (takesInput === true) {
      // Not a warning. This is the guard that keeps an IME away from the raw
      // PTY: with it gone, every composition keystroke reaches the shell and
      // Chinese, Japanese and Korean input produce garbage — the failure the
      // compose box exists to prevent.
      note('FAIL', 'mobile', 'the terminal still accepts direct keystrokes at phone width')
    }

    await compose.fill('echo MOBILE_COMPOSE_OK')
    await page.locator('[data-testid="compose-send"]').click()
    let sent = false
    for (let i = 0; i < 40; i++) {
      const txt = await screenText(page)
      if ((txt.match(/MOBILE_COMPOSE_OK/g) ?? []).length >= 2) { sent = true; break }
      await sleep(300)
    }
    if (!sent) note('FAIL', 'mobile', 'the compose box did not reach the terminal')

    // The box must clear, or the next command is appended to the last one.
    if ((await compose.inputValue()) !== '') {
      note('FAIL', 'mobile', 'the compose box kept its text after sending')
    }

    // What is in the box belongs to the session it was typed for.
    //
    // The box is rendered by position rather than keyed, so switching session
    // used to leave the text sitting there while the send handler re-pointed
    // at the new session: compose for one agent, glance at another, tap Send,
    // and it ran in the wrong one. Measured before it was fixed — an echo
    // composed for alpha ran in bravo and never reached alpha. Sending a
    // command to the wrong agent is the expensive mistake in a panel built to
    // run a lot of them at once.
    //
    // The second half matters just as much: switching away must not throw the
    // draft away either, or the fix is just a different way to lose what you
    // typed.
    const openPhoneMenu = async () => {
      await page.locator('header button[title^="Projects"]').click()
      await sleep(700)
    }
    await compose.fill('echo DRAFT_FOR_SCRATCHPAD')
    await sleep(200)
    await openPhoneMenu()
    // By position, not by name: this only needs *a different* session.
    const other = page.locator('[data-testid="session-row"]')
      .filter({ hasNotText: 'scratchpad' }).first()
    if (!(await other.isVisible().catch(() => false))) {
      note('WARN', 'mobile', 'only one session at phone width; draft isolation not exercised')
      await openPhoneMenu()
    } else {
      await other.click()
      await sleep(1800)
      const leaked = await compose.inputValue().catch(() => '')
      if (leaked !== '') {
        note('FAIL', 'mobile',
          `a draft composed for another session is sitting in this one's box: ${JSON.stringify(leaked)}`)
      }
      await compose.fill('echo DRAFT_FOR_OTHER')
      await sleep(200)
      await openPhoneMenu()
      await page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first().click()
      await sleep(1800)
      const back = await compose.inputValue().catch(() => '')
      if (back !== 'echo DRAFT_FOR_SCRATCHPAD') {
        note('FAIL', 'mobile',
          `looking at another session threw this one's draft away; the box holds ${JSON.stringify(back)}`)
      }
      await compose.fill('')
    }

    // A key from the bar has to arrive as the byte a terminal expects.
    await compose.fill('printf KEYBAR')
    const newlineBtn = page.locator('[data-testid="compose-newline"]')
    const newlineWas = await newlineBtn.getAttribute('data-on')
    await newlineBtn.click() // send without Enter
    await page.locator('[data-testid="compose-send"]').click()
    await sleep(800)
    await page.locator('[data-testid="key-enter"]').click()
    let keyed = false
    for (let i = 0; i < 40; i++) {
      const txt = await screenText(page)
      if ((txt.match(/KEYBAR/g) ?? []).length >= 2) { keyed = true; break }
      await sleep(300)
    }
    if (!keyed) note('FAIL', 'mobile', 'the Enter key from the bar did not reach the terminal')

    // A block with line breaks in it is a paste, not typing.
    //
    // Written into the PTY byte by byte it is indistinguishable from somebody
    // pressing Enter after every line: a shell runs each one, and an agent acts
    // on the first sentence of a three-line instruction before it has read the
    // third. That was measured, and fixed, and then nothing exercised the fix:
    // this section filled the box with single-line commands, so onPaste never
    // fired, and the chain behind it -- pasteText, MsgPaste, Manager.Paste --
    // was 0.0% in a -coverpkg run and named by no Go test either.
    //
    // It needs its own session, and the first attempt at this check is why.
    // Pasting into the scratchpad pane and asserting the lines did not run
    // failed, and the failure was the fixture: that pane runs `sh`, dash never
    // asks for bracketed paste, and tmux's `paste-buffer -p` correctly does not
    // bracket for a pane that never asked. The product did exactly what its own
    // comment promises -- "better rather than airtight" -- and the check was
    // asserting a guarantee that does not exist for that shell.
    //
    // So: a pane that does ask, and `cat -v` so the markers are text rather
    // than sequences the terminal swallows. Same fixture internal/tmux uses,
    // for the same reason. This asserts what the client is actually
    // responsible for -- routing a multi-line block down the paste road
    // instead of typing it -- and leaves what the receiving program does about
    // it to the receiving program.
    const pasteSess = await mkSession(
      ['sh', '-c', "stty -echo -icanon min 1 time 0; printf '\\033[?2004h'; exec cat -v"],
      'paste-target',
    )
    await sleep(1200)
    await openPhoneMenu()
    await page.locator('[data-testid="session-row"]', { hasText: 'paste-target' }).first().click()
    await sleep(1600)

    const pasted = ['ONE', 'TWO', 'THREE'].map((n) => `echo ${n}`).join('\n')
    await compose.fill(pasted)
    // What the box is holding, before anything is concluded from what arrived.
    // The routing turns on this value containing a newline, so a fill that did
    // not deliver them would make the terminal's behaviour correct and the
    // fixture wrong -- which is the mistake this check already made once.
    const filledHasNewline = (await compose.inputValue()).includes(String.fromCharCode(10))
    await page.locator('[data-testid="compose-send"]').click()
    await sleep(1800)
    const pasteTxt = await screenText(page)
    const opened = pasteTxt.includes('^[[200~')
    const closed = pasteTxt.includes('^[[201~')
    if (!filledHasNewline) {
      note('WARN', 'mobile', 'the compose box did not hold the newlines; nothing was measured')
    } else if (!pasteTxt.includes('echo ONE')) {
      note('WARN', 'mobile', 'the block never reached the pane; nothing was measured')
    } else if (!opened || !closed) {
      note('FAIL', 'mobile',
        `a three-line block from the compose box arrived without bracketing ` +
        `(open=${opened} close=${closed}), so it was typed rather than pasted: every ` +
        'newline in it is an Enter, and an agent acts on the first line before it has read ' +
        `the third. Screen tail: ${JSON.stringify(pasteTxt.slice(-200))}`)
    } else {
      note('PASS', 'mobile', 'a multi-line block went down the paste road, bracketed')
    }

    await authed(`/api/sessions/${pasteSess.id}`, { method: 'DELETE' }).catch(() => {})
    await sleep(600)
    await openPhoneMenu()
    await page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first().click()
    await sleep(1200)

    // Put the toggle back. It is sticky, not a one-shot, and leaving it off
    // meant every send after this point in the run arrived without an Enter —
    // so commands piled up unexecuted on the input line while the checks that
    // depended on them reported that the feature under test had failed. The
    // cost of a harness leaving a mode behind is paid by whatever is added
    // after it, which is the worst way to distribute it.
    await newlineBtn.click()
    if ((await newlineBtn.getAttribute('data-on')) !== newlineWas) {
      note('FAIL', 'mobile', 'could not restore the compose box Enter toggle')
    }

    // The keys that matter must be on screen without scrolling. A single
    // scrolling row put y, n and Escape off the left edge, which is exactly
    // the set a phone is there for.
    const primary = await page.locator('[data-testid="key-row-primary"]').boundingBox()
    if (!primary) {
      note('FAIL', 'mobile', 'no primary key row')
    } else {
      for (const label of ['^C', 'y', 'n', 'esc', 'tab', 'ctrl', 'alt', 'enter']) {
        const box = await page.locator(`[data-testid="key-${label}"]`).boundingBox()
        if (!box) {
          note('FAIL', 'mobile', `key ${label} is missing`)
        } else if (box.x < 0 || box.x + box.width > primary.x + primary.width + 1) {
          note('FAIL', 'mobile', `key ${label} is off screen without scrolling`)
        }
      }
    }

    // The second row is allowed to scroll — "losing sight of ~ costs far less
    // than losing sight of Escape" — but allowed to scroll and actually
    // scrollable are different things, and the difference is what the row
    // above already got wrong once: eight keys overflowed a 320px phone, the
    // page did not scroll, and ctrl and alt were simply unreachable.
    //
    // So: the arrows must be there without scrolling, because they are what
    // this row is mostly for, and the last key must be reachable with it.
    const secondary = page.locator('[data-testid="key-row-secondary"]')
    const secBox = await secondary.boundingBox()
    if (!secBox) {
      note('FAIL', 'mobile', 'no secondary key row')
    } else {
      for (const label of ['up', 'down', 'left', 'right']) {
        const box = await page.locator(`[data-testid="key-${label}"]`).boundingBox()
        if (!box) {
          note('FAIL', 'mobile', `key ${label} is missing`)
        } else if (box.x < secBox.x - 1 || box.x + box.width > secBox.x + secBox.width + 1) {
          note('FAIL', 'mobile', `arrow key ${label} needs scrolling to reach`)
        }
      }

      const overflow = await secondary.evaluate((el) => ({
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
        overflowX: getComputedStyle(el).overflowX,
      }))
      if (overflow.scrollWidth > overflow.clientWidth + 1) {
        if (overflow.overflowX !== 'auto' && overflow.overflowX !== 'scroll') {
          note('FAIL', 'mobile',
            `the secondary key row overflows by ${overflow.scrollWidth - overflow.clientWidth}px ` +
            `with overflow-x: ${overflow.overflowX} — the keys past the edge cannot be reached`)
        }
        // Scroll it and check the last key really arrives.
        await secondary.evaluate((el) => { el.scrollLeft = el.scrollWidth })
        await sleep(250)
        const last = await page.locator('[data-testid="key-~"]').boundingBox()
        const after = await secondary.boundingBox()
        if (!last || !after) {
          note('FAIL', 'mobile', 'the last key of the secondary row disappeared when scrolled')
        } else if (last.x + last.width > after.x + after.width + 1 || last.x < after.x - 1) {
          note('FAIL', 'mobile',
            'the secondary key row scrolled to its end and the last key is still not in it')
        }
        await secondary.evaluate((el) => { el.scrollLeft = 0 })
        await sleep(250)
      }
    }

    // Sticky modifiers: tap, then tap what they apply to.
    await page.locator('[data-testid="key-ctrl"]').click()
    const ctrlOn = await page.locator('[data-testid="key-ctrl"]').getAttribute('data-active')
    if (ctrlOn !== 'true') note('FAIL', 'mobile', 'ctrl did not latch')
    await page.locator('[data-testid="key-1"]').click()
    await sleep(400)
    if ((await page.locator('[data-testid="key-ctrl"]').getAttribute('data-active')) !== 'false') {
      note('FAIL', 'mobile', 'ctrl stayed latched after the key it applied to')
    }

    // Arming ctrl and then pressing a key it cannot apply to must not consume
    // it. It used to: every raw sequence cleared both modifiers, so arming
    // ctrl and tapping y sent a plain "y" — a yes to whatever the agent was
    // asking — while the user believed they had just interrupted it.
    await page.locator('[data-testid="key-ctrl"]').click()
    await page.locator('[data-testid="key-esc"]').click()
    await sleep(300)
    if ((await page.locator('[data-testid="key-ctrl"]').getAttribute('data-active')) !== 'true') {
      note('FAIL', 'mobile',
        'ctrl was consumed by a key it cannot modify; the modifier silently did nothing')
    }
    await page.locator('[data-testid="key-ctrl"]').click() // put it back

    // What the above does not prove, said out loud every run rather than left
    // to be rediscovered.
    //
    // Ctrl+C, end to end, from the bar.
    //
    // This used to be a WARN explaining why it could not be tested: ctrl folds
    // the top three bits, so it only changes bytes in 0x40-0x5f, and every key
    // the bar could send through the modifier path — 1 2 3 / - | ~ — falls
    // outside that range. The modifier latched, un-latched, and could not
    // alter a single byte. The one thing anybody wants from a phone when a
    // command has run away had no key to apply to, and saying so every run was
    // not the same as fixing it.
    // Start from a clean prompt. The checks above deliberately leave things
    // on the input line — a stray "1" from the modifier test, an Escape from
    // the one before it — and readline treats Escape as a meta prefix, so the
    // command that follows is swallowed rather than run. The first attempt at
    // this test spent its whole budget waiting for a `sleep` that had never
    // been submitted, and reported it as the interrupt failing.
    await page.locator('[data-testid="key-enter"]').click()
    await sleep(600)

    await compose.fill('sleep 120')
    await page.locator('[data-testid="compose-send"]').click()
    await sleep(1200)
    await page.locator('[data-testid="key-^C"]').click()
    await sleep(600)
    // If the interrupt did not land, the shell is still inside sleep and this
    // sits in its input buffer unexecuted until the sleep ends — well past the
    // window below. So the marker appearing is proof the shell came back.
    const backMark = 'AFTER' + '_INTERRUPT'
    await compose.fill(`echo ${backMark.slice(0, 5)}"${backMark.slice(5)}"`)
    await page.locator('[data-testid="compose-send"]').click()
    let interrupted = false
    for (let i = 0; i < 30; i++) {
      const txt = await screenText(page)
      // Once, not twice. The marker is split across a quote in the command so
      // that the shell's echo of the typed line cannot contain it — which is
      // the whole point of writing it that way, and asking for two occurrences
      // made an assertion that could not pass however well the key worked.
      if ((txt.match(new RegExp(backMark, 'g')) ?? []).length >= 1) { interrupted = true; break }
      await sleep(400)
    }
    if (!interrupted) {
      const shown = (await screenRows(page)).filter((t) => t.trim()).slice(-6)
      note('FAIL', 'mobile',
        'the ^C key did not interrupt a running command; the shell never came back. ' +
        `The terminal's last lines are ${JSON.stringify(shown)}`)
      await page.screenshot({ path: join(SHOTS, 'ctrl-c-failed.png') })
    }

    // A session that wants a human has to be visible from the one screen a
    // phone shows, which is not the list. Marked through the API rather than
    // depending on one left waiting by an earlier step.
    const all = await (await authed('/api/state')).json()
    const victim = all.sessions.find((x) => !x.parentSessionId && x.state !== 'waiting')
    if (!victim) {
      note('WARN', 'mobile', 'no session available to mark as waiting')
    } else {
      await authed(`/api/sessions/${victim.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ state: 'waiting' }),
      })
      let badge = false
      for (let i = 0; i < 25; i++) {
        if (await page.locator('[data-testid="waiting-badge"]').isVisible().catch(() => false)) {
          badge = true
          break
        }
        await sleep(400)
      }
      if (!badge) {
        note('FAIL', 'mobile',
          'no waiting badge on the menu button; on a phone nothing else on screen can say a session needs you')
      }
    }

    await page.screenshot({ path: join(SHOTS, 'mobile.png') })

    // Everything above ran in a narrow window with a mouse attached, which is
    // not a phone. Chromium only reports `(hover: none)` and `(pointer:
    // coarse)` when touch is actually emulated, so any bug that hinges on
    // hover not existing is invisible to a viewport resize. This is a separate
    // context because touch emulation is a property of the context.
    const touchCtx = await browser.newContext({
      serviceWorkers: 'block',
      viewport: { width: 390, height: 844 },
      hasTouch: true,
      isMobile: true,
      deviceScaleFactor: 3,
      permissions: ['clipboard-read', 'clipboard-write'],
    })
    const touch = await touchCtx.newPage()
    await touch.goto(BASE, { waitUntil: 'networkidle' })
    await touch.locator('[data-testid="auth-username"]').fill(USERNAME)
    await touch.locator('[data-testid="auth-password"]').fill(PASSWORD)
    await touch.locator('[data-testid="auth-submit"]').click()
    await touch.waitForSelector('[data-testid="sidebar"], header', { timeout: 15000 })
    await sleep(1500)

    const pointer = await touch.evaluate(() => ({
      hover: matchMedia('(hover: hover)').matches,
      fine: matchMedia('(pointer: fine)').matches,
    }))
    if (pointer.hover || pointer.fine) {
      note('WARN', 'mobile',
        'touch emulation did not take; the reachability check below is measuring a mouse')
    }

    await touch.locator('header button[title^="Projects"]').click()
    await sleep(800)
    const touchRow = touch
      .locator('[data-testid="sidebar"][data-overlay="true"] [data-testid="session-row"]')
      .first()
    // Pin and kill are revealed on hover, and a phone never hovers. "Pin this
    // to the top of the project" is a feature of the panel, not a nicety, and
    // it was a control you had to know the pixel position of.
    for (const control of ['pin-session', 'kill-session']) {
      const btn = touchRow.locator(`[data-testid="${control}"]`)
      if ((await btn.count()) === 0) {
        note('FAIL', 'mobile', `no ${control} control in the session row at all`)
        continue
      }
      const opacity = await btn.evaluate((el) => getComputedStyle(el).opacity)
      if (Number(opacity) < 0.9) {
        note('FAIL', 'mobile',
          `${control} renders at opacity ${opacity} on a touch screen; it is only revealed by ` +
          'a hover that will never happen')
      }

      // Visible is not the same as hittable. These were 16x16 css pixels
      // twenty-four apart — a 12px icon with two pixels of padding — and one of
      // them kills a running agent. A thumb is about nine millimetres across,
      // which is where the 44px floor comes from; missing kill and hitting pin
      // is harmless, missing pin and hitting kill is not.
      const box = await btn.boundingBox()
      if (!box) {
        note('FAIL', 'mobile', `${control} has no box on a touch screen`)
      } else if (box.width < 40 || box.height < 40) {
        note('FAIL', 'mobile',
          `${control} is ${Math.round(box.width)}x${Math.round(box.height)} css px on a touch ` +
          'screen; a thumb needs 44')
      }
    }
    // The project header carries controls too, and they were missed the first
    // time this was measured because the loop above only looks inside a session
    // row. The grip is the worst of them: reordering is a press-and-hold drag,
    // and it was a sixteen-pixel target.
    for (const control of ['project-grip', 'project-new-shell']) {
      const btn = touch.locator(`[data-testid="sidebar"][data-overlay="true"] [data-testid="${control}"]`).first()
      if ((await btn.count()) === 0) {
        note('FAIL', 'mobile', `no ${control} control in the project header`)
        continue
      }
      const box = await btn.boundingBox()
      if (!box) {
        note('FAIL', 'mobile', `${control} has no box on a touch screen`)
      } else if (box.width < 40 || box.height < 40) {
        note('FAIL', 'mobile',
          `${control} is ${Math.round(box.width)}x${Math.round(box.height)} css px on a touch ` +
          'screen; a thumb needs 44')
      }
    }

    // A session whose title is whatever the agent printed.
    //
    // The title comes from OSC 0/2, so its length is decided by the pane. It
    // is bounded now — 200,000 characters used to reach the database and take
    // the state snapshot from 705 bytes to 200,710, broadcast to every viewer
    // — but the bound is 256, and 256 characters in a 260px sidebar is still a
    // layout question that only a browser can answer.
    {
      const second = (await (await authed('/api/state')).json()).projects
        .find((x) => x.name === 'zzz-second')
      if (second) {
        await authed('/api/sessions', {
          method: 'POST',
          body: JSON.stringify({
            projectId: second.id,
            command: ['sh', '-c',
              'printf "\\033]2;"; head -c 5000 /dev/zero | tr "\\0" A; printf "\\007"; exec sleep 300'],
          }),
        })
        // The poller derives titles on its own schedule; this is two ticks.
        await sleep(5000)
        await page.setViewportSize({ width: 1440, height: 900 })
        await sleep(600)

        const overflowing = await page.evaluate(() => {
          const doc = document.documentElement
          const rows = [...document.querySelectorAll('[data-testid="session-row"]')]
          const wide = rows
            .filter((el) => el.scrollWidth > el.clientWidth + 1)
            .map((el) => ({ text: (el.textContent ?? '').slice(0, 30), scroll: el.scrollWidth, client: el.clientWidth }))
          return { page: doc.scrollWidth - doc.clientWidth, wide }
        })
        if (overflowing.page > 0) {
          note('FAIL', 'title', `a long session title pushed the page ${overflowing.page}px wide`)
        } else {
          note('PASS', 'title', 'a title the pane chose does not widen the page')
        }
        if (overflowing.wide.length > 0) {
          note('FAIL', 'title',
            `${overflowing.wide.length} sidebar rows overflow: ${JSON.stringify(overflowing.wide[0])}`)
        }
        // Bounded in the data as well, not only clipped by CSS: what CSS hides
        // is still in every snapshot pushed to every browser.
        const stored = ((await (await authed('/api/state')).json()).sessions ?? [])
          .filter((x) => x.projectId === second.id)
          .map((x) => (x.title ?? '').length)
        const longest = Math.max(0, ...stored)
        if (longest > 300) {
          note('FAIL', 'title', `a title of ${longest} characters reached the snapshot`)
        } else {
          note('PASS', 'title', `the longest stored title is ${longest} characters`)
        }
      }
    }

    // Removing a project from the panel.
    //
    // You add a project by typing a path into a prompt, and a path that is
    // wrong but happens to exist gives you a project. The endpoint, the CLI
    // and the client method were all there; nothing in the panel called any of
    // them, so the sidebar had no way back out.
    //
    // Driven rather than asserted from the API, because the button only exists
    // on hover — a control that is present but unreachable is the shape of
    // defect this harness exists for.
    {
      const listState = async () => (await (await authed('/api/state')).json())
      const listProjects = async () => (await listState()).projects ?? []
      const doomedProject = (await listProjects()).find((x) => x.name === 'zzz-second')
      if (!doomedProject) {
        note('FAIL', 'projects', 'the second project is missing before the remove is tried')
      } else {
        // Give it sessions, including a scratch terminal under one of them.
        // The confirmation counts what will be killed, and a count nobody has
        // ever checked is a person agreeing to something other than what
        // happens. Empty projects were all this had exercised.
        const doomedMain = await (await authed('/api/sessions', {
          method: 'POST',
          body: JSON.stringify({ projectId: doomedProject.id, command: ['sleep', '300'], title: 'doomed-one' }),
        })).json()
        await authed('/api/sessions', {
          method: 'POST',
          body: JSON.stringify({ projectId: doomedProject.id, command: ['sleep', '300'], title: 'doomed-two' }),
        })
        await authed('/api/sessions', {
          method: 'POST',
          body: JSON.stringify({
            projectId: doomedProject.id, parentSessionId: doomedMain.id,
            command: ['sleep', '300'], title: 'doomed-scratch',
          }),
        })
        await sleep(2000)
        const doomedSessions = ((await listState()).sessions ?? [])
          .filter((x) => x.projectId === doomedProject.id)

        // Earlier sections leave the page wherever they finished; the sidebar
        // only lists projects at a desktop width.
        await page.setViewportSize({ width: 1440, height: 900 })
        await sleep(600)
        const onScreen = await page.$$eval('[data-testid="project-group"]', (els) =>
          els.map((el) => el.textContent?.trim() ?? ''))
        const group = page.locator('[data-testid="project-group"]', { hasText: 'zzz-second' }).first()
        if ((await group.count()) === 0) {
          note('FAIL', 'projects', `no sidebar row for the project to remove; saw ${JSON.stringify(onScreen)}`)
          throw new Error('project row missing')
        }
        await group.hover()
        const remove = group.locator('[data-testid="project-remove"]')
        const reachable = await remove.isVisible().catch(() => false)
        if (!reachable) {
          note('FAIL', 'projects', 'no way to remove a project from the sidebar')
        } else {
          // The confirmation counts what goes and promises what stays. Read it
          // rather than blindly accepting: a count that is wrong here is a
          // person agreeing to something other than what happens.
          //
          // The panel's own dialog, not the browser's. This drove
          // `page.once('dialog')` until window.confirm was replaced, and that
          // listener does not fail when there is no longer a dialog to hear --
          // it simply never fires, the click sits on an unanswered modal, and
          // the assertions below read a page that never changed. The same trap
          // the first-run check fell into over the directory picker.
          await remove.click()
          const asked = await page
            .waitForSelector('[data-testid="confirm-dialog"]', { timeout: 5000 })
            .then(() => true)
            .catch(() => false)
          if (!asked) {
            note('FAIL', 'projects', 'removing a project asked nothing before killing its sessions')
            throw new Error('no confirmation')
          }
          const prompt = [
            await page.locator('[data-testid="confirm-title"]').innerText(),
            await page.locator('[data-testid="confirm-body"]').innerText(),
          ].join('\n')
          // The destructive answer is marked as destructive, and it is not the
          // one the keyboard starts on: a dialog that opens with "kill" focused
          // turns the Enter somebody was already pressing into a confirmation.
          if ((await page.getAttribute('[data-testid="confirm-dialog"]', 'data-destructive')) !== 'true') {
            note('FAIL', 'projects', 'the confirmation does not mark itself destructive')
          }
          const focusedOnSafe = await page.evaluate(() =>
            document.activeElement?.getAttribute('data-testid') === 'confirm-no')
          if (!focusedOnSafe) {
            note('FAIL', 'projects', 'the confirmation opens with the focus on the destructive button')
          }
          await page.screenshot({ path: join(SHOTS, 'confirm.png') })
          await page.locator('[data-testid="confirm-yes"]').click()
          await sleep(1500)

          const after = await listProjects()
          if (after.some((x) => x.id === doomedProject.id)) {
            note('FAIL', 'projects', 'the project survived being removed')
          } else {
            note('PASS', 'projects', `removed from the sidebar; confirmation said ${JSON.stringify(prompt.split('\n')[0])}`)
          }

          // The number in the confirmation against the number that actually
          // died. Both are worth failing on: a low count understates what the
          // click destroys, a high one is a panel crying wolf about work it is
          // not going to do.
          const claimed = Number((prompt.match(/Its (\d+) session/) ?? [])[1] ?? 0)
          const gone = (await listState()).sessions ?? []
          const survivors = gone.filter((x) => x.projectId === doomedProject.id)
          if (claimed !== doomedSessions.length) {
            note('FAIL', 'projects',
              `the confirmation offered ${claimed} sessions, the project had ${doomedSessions.length}: ` +
              JSON.stringify(doomedSessions.map((x) => x.title)))
          } else {
            note('PASS', 'projects', `the confirmation counted all ${claimed} sessions, scratch terminals included`)
          }
          if (survivors.length > 0) {
            note('FAIL', 'projects',
              `${survivors.length} sessions outlived the project they belonged to`)
          }
          const liveTmux = execSync(`tmux -L ${SOCKET} ls 2>/dev/null || true`, { encoding: 'utf8' })
          for (const x of doomedSessions) {
            if (liveTmux.includes(x.tmuxName)) {
              note('FAIL', 'projects', `tmux session ${x.tmuxName} outlived the project`)
            }
          }
          const stillListed = await page
            .locator('[data-testid="project-group"]', { hasText: 'zzz-second' })
            .count()
          if (stillListed > 0) {
            note('FAIL', 'projects', 'the sidebar still shows a project that is gone')
          }
          // The confirmation promises the directory is left alone. A panel that
          // deleted somebody's working tree because they tidied their sidebar
          // is the worst thing in this file.
          if (!existsSync(DATA)) {
            note('FAIL', 'projects', 'removing a project deleted the directory it pointed at')
          } else {
            note('PASS', 'projects', 'the directory it pointed at is untouched')
          }
        }
      }
    }

    // A session that stops being valid must take its terminals with it.
    //
    // Authorisation happens once, at the handshake, and the socket then lives
    // for hours. Measured before this was fixed: with the session row deleted,
    // the panel still showed everything, the connection dot still said open,
    // and typing still reached the shell. Signing out, an expiry, and the
    // password change two sections below all had that hole — the last one
    // especially, since deleting other browsers' sessions is the entire point
    // of it.
    //
    // Its own context, so the rest of this run keeps its session.
    const doomedCtx = await browser.newContext({ serviceWorkers: 'block', viewport: { width: 1024, height: 768 } })
    const doomed = await doomedCtx.newPage()
    await doomed.goto(BASE, { waitUntil: 'domcontentloaded' })
    await doomed.locator('[data-testid="auth-username"]').fill(USERNAME)
    await doomed.locator('[data-testid="auth-password"]').fill(PASSWORD)
    await doomed.locator('[data-testid="auth-submit"]').click()
    const doomedIn = await doomed
      .waitForSelector('[data-layout]', { timeout: 20000 })
      .then(() => true)
      .catch(() => false)
    if (!doomedIn) {
      note('FAIL', 'auth', 'could not sign a second browser in to test session revocation')
    } else {
      await sleep(2000)
      // Invalidate it without the page being told, which is what an expiry
      // looks like from the page's point of view.
      await doomed.evaluate(() => fetch('/api/auth/logout', { method: 'POST' }))
      let cut = false
      for (let i = 0; i < 40; i++) {
        await sleep(500)
        const state = await doomed.evaluate(() => ({
          login: !!document.querySelector('[data-testid="auth-password"]'),
          conn: document.querySelector('[data-testid="connection"]')?.getAttribute('data-status'),
        }))
        if (state.login) { cut = true; break }
      }
      if (!cut) {
        const shown = await doomed.evaluate(() => ({
          conn: document.querySelector('[data-testid="connection"]')?.getAttribute('data-status'),
          rows: document.querySelectorAll('[data-testid="session-row"]').length,
        }))
        note('FAIL', 'auth',
          'twenty seconds after its session was destroyed the browser is still in the panel: ' +
          JSON.stringify(shown))
      }
      await doomedCtx.close()
    }

    // Every field a finger can focus must be at least 16px.
    //
    // iOS Safari zooms the page when a focused input's text is smaller than
    // that, and has ignored `user-scalable=no` since iOS 10 — so the meta tag
    // in index.html does not prevent it, and nothing zooms back afterwards.
    // Every field here was 12 or 13px, the compose box included, which is the
    // main way to type on a phone.
    await touch.locator('[data-testid="compose-input"]').click().catch(() => {})
    const smallFields = await touch.evaluate(() =>
      [...document.querySelectorAll('input, textarea')]
        .filter((el) => el.offsetParent !== null)
        .map((el) => ({
          id: el.getAttribute('data-testid') ?? el.tagName.toLowerCase(),
          px: Math.round(parseFloat(getComputedStyle(el).fontSize)),
        }))
        .filter((f) => f.px < 16),
    )
    if (smallFields.length > 0) {
      note('FAIL', 'mobile',
        `fields a finger will focus are under 16px, so iOS magnifies the page when they are ` +
        `tapped and does not put it back: ${JSON.stringify(smallFields)}`)
    }

    // Renaming, with a finger.
    //
    // "Double click to rename" is what the label said, and on a narrow screen
    // the list is an overlay that closes when you choose a session — so the
    // first tap of a double tap dismisses the thing being tapped and the
    // second never arrives. Renaming from a phone was not awkward, it was
    // impossible, and the tooltip promising otherwise is only visible with a
    // mouse.
    //
    // Driven through CDP because this is our own gesture: Playwright's tap()
    // helper cannot express "hold still for six hundred milliseconds".
    const nameEl = touch
      .locator('[data-testid="sidebar"][data-overlay="true"] [data-testid="session-row"] [data-testid="inline-name"]')
      .first()
    const nameBox = await nameEl.boundingBox()
    if (!nameBox) {
      note('FAIL', 'mobile', 'no session name to press and hold')
    } else {
      const cdp = await touchCtx.newCDPSession(touch)
      const cx = nameBox.x + nameBox.width / 2
      const cy = nameBox.y + nameBox.height / 2
      const point = { touchPoints: [{ x: cx, y: cy, radiusX: 8, radiusY: 8, force: 1, id: 1 }] }
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', ...point })
      await sleep(800)
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
      await sleep(600)
      const editing = await touch
        .locator('[data-testid="sidebar"][data-overlay="true"] input')
        .count()
      if (editing === 0) {
        note('FAIL', 'mobile', 'pressing and holding a session name does not start a rename')
      }
      // And the press must not double as a selection: choosing a session
      // closes the drawer, which would take the input away in the same
      // gesture that opened it.
      const stillOpen = await touch
        .locator('[data-testid="sidebar"][data-overlay="true"]')
        .count()
      if (stillOpen === 0) {
        note('FAIL', 'mobile',
          'the long press also selected the session, so the drawer closed and took the rename ' +
          'input with it')
      }
      await touch.keyboard.press('Escape')
      await sleep(300)
    }

    await scanUnreachable(touch, 'the phone drawer')
    await scanTapTargets(touch, 'the phone drawer', 8)
    await scanFaded(touch, 'the phone drawer')
    await scanNames(touch, 'the phone drawer')
    await touch.screenshot({ path: join(SHOTS, 'mobile-drawer.png') })

    // The settings dialog on a phone.
    //
    // It is reachable here, and the rule the panel holds itself to is that the
    // set of controls does not change with the viewport (components/chrome.ts,
    // and the complaint that produced it). The rail may lie down and scroll
    // sideways at 390px; it may not drop a group, and it may not fold back
    // into one long page, which is the thing this restructure exists to end.
    //
    // The drawer is open from the long-press check above and covers the header
    // the gear is in, so it is closed through its own control first — and
    // opened again at the end, because what follows this expects it open.
    {
      await touch
        .locator('[data-testid="sidebar"][data-overlay="true"] header button')
        .first()
        .click({ timeout: 3000 })
        .catch(() => {})
      await sleep(400)
      await touch.locator('[data-testid="settings-open"]').click()
      await sleep(900)
      if (!(await touch.locator('[data-testid="settings"]').isVisible().catch(() => false))) {
        note('FAIL', 'mobile', 'the settings dialog does not open on a phone')
      } else {
        const tabs = touch.locator('[data-testid="settings-rail"] [role="tab"]')
        const count = await tabs.count()
        if (count !== 5) {
          note('FAIL', 'mobile',
            `the settings rail has ${count} groups on a phone and 5 on a laptop; a control set ` +
            'that changes with the viewport is the complaint chrome.ts exists for')
        }
        const view = touch.viewportSize()
        const ids = []
        for (let i = 0; i < count; i++) {
          const tab = tabs.nth(i)
          ids.push(await tab.getAttribute('data-testid'))
          await tab.scrollIntoViewIfNeeded().catch(() => {})
          const box = await tab.boundingBox()
          const inside =
            box && box.x >= -1 && box.x + box.width <= view.width + 1 &&
            box.y >= -1 && box.y + box.height <= view.height + 1
          if (!inside) {
            note('FAIL', 'mobile',
              `${ids[i]} cannot be brought on screen at ${view.width}px: ${JSON.stringify(box)}`)
          }
        }
        // And pressing one moves the dialog, rather than scrolling a page that
        // has everything on it.
        const last = ids[ids.length - 1]?.replace('settings-group-', '')
        if (last) await settingsGroup(touch, last)
        await touch.screenshot({ path: join(SHOTS, 'mobile-settings.png') })
        await touch.locator('[data-testid="settings-close"]').click()
        await sleep(300)
      }
      await touch.locator('[data-testid="menu-button"]').click()
      await sleep(600)
    }

    // The two phone shapes nothing had ever been measured at.
    //
    // 390x844 is one phone. The checks all ran at that one size, and both of
    // these were broken at sizes just as common.
    for (const shape of [
      // The narrowest phone still in use. Eight keys at the 44px a thumb needs
      // come to 380px, which does not fit here — the row overflowed by 56 and
      // the page does not scroll, so `ctrl` and `alt` could not be pressed at
      // all. Widening the touch targets is what broke it, which is the sort of
      // thing a fix does when it is only ever measured at one width.
      { name: '320 wide', w: 320, h: 568 },
      // A phone held sideways. The layout switch was on width alone, and 844
      // is wide — so rotating the phone produced the *desktop* layout: a 260px
      // sidebar, the right panel, the bottom strip, and a six-line terminal,
      // with no compose box and no key bar. Turning a phone sideways is
      // something people do to see more of a terminal.
      { name: 'landscape', w: 844, h: 390 },
    ]) {
      const ctx2 = await browser.newContext({
      serviceWorkers: 'block',
        viewport: { width: shape.w, height: shape.h },
        hasTouch: true,
        isMobile: true,
      })
      const p2 = await ctx2.newPage()
      await p2.goto(BASE, { waitUntil: 'domcontentloaded' })
      await p2.locator('[data-testid="auth-username"]').fill(USERNAME)
      // PASSWORD, not NEW_PASSWORD: the section that changes it runs later in
      // this file. If that ever moves above here, the assertions below start
      // reporting "the null layout", which is this check saying it never got
      // past the sign-in page rather than anything about the layout.
      await p2.locator('[data-testid="auth-password"]').fill(PASSWORD)
      await p2.locator('[data-testid="auth-submit"]').click()
      const signedIn = await p2
        .waitForSelector('[data-layout]', { timeout: 20000 })
        .then(() => true)
        .catch(() => false)
      if (!signedIn) {
        note('FAIL', 'mobile', `could not sign in at ${shape.name} to measure the layout`)
        await ctx2.close()
        continue
      }
      await sleep(2500)

      const m = await p2.evaluate(() => {
        const row = document.querySelector('[data-testid="key-row-primary"]')
        return {
          layout: document.querySelector('[data-layout]')?.getAttribute('data-layout') ?? null,
          hidden: row ? row.scrollWidth - row.clientWidth : null,
          compose: !!document.querySelector('[data-testid="compose-input"]'),
          bar: !!document.querySelector('[data-testid="key-bar"]'),
        }
      })
      if (m.layout !== 'narrow') {
        note('FAIL', 'mobile',
          `at ${shape.name} the panel uses the ${m.layout} layout; a touch screen this size has ` +
          'no keyboard and no room for a sidebar and two panels')
      }
      if (!m.compose || !m.bar) {
        note('FAIL', 'mobile',
          `at ${shape.name} there is ${m.compose ? '' : 'no compose box'}${!m.compose && !m.bar ? ' and ' : ''}${m.bar ? '' : 'no key bar'}`)
      }
      if (m.hidden === null) {
        note('FAIL', 'mobile', `at ${shape.name} there is no primary key row`)
      } else if (m.hidden > 0) {
        note('FAIL', 'mobile',
          `at ${shape.name} the primary key row hides ${m.hidden}px of keys, and the page does ` +
          'not scroll, so they cannot be pressed')
      }
      await scanUnreachable(p2, `a ${shape.name} phone`)
      await scanTapTargets(p2, `a ${shape.name} phone`)
      await scanFaded(p2, `a ${shape.name} phone`)
      await p2.screenshot({ path: join(SHOTS, `phone-${shape.w}x${shape.h}.png`) })
      await ctx2.close()
    }

    // A fresh context has no remembered session and lands on whichever is
    // first — which was htop, so the compose box was typing into a TUI.
    // Choose the shell deliberately; selecting also closes the drawer.
    await touch
      .locator('[data-testid="sidebar"][data-overlay="true"] [data-testid="session-row"]',
        { hasText: 'scratchpad' })
      .first()
      .click()
    await sleep(2500)

    // ── copying terminal text with a finger ────────────────────────────────
    // This was asked for by name and had no test at all. It cannot be checked
    // through the browser's own selection: headless Chromium performs no
    // native touch text selection, and a probe using it reports failure over
    // ordinary page text too — so it would have "found" a bug in any
    // implementation, correct or not. The gesture is ours, so it can be driven
    // directly.
    await touch.evaluate(() => {
      const el = document.querySelector('.xterm-helper-textarea')
      if (el instanceof HTMLTextAreaElement) el.blur()
    })
    const marker = 'FINGER' + '_COPY_TARGET'
    // A throwaway line first. The key bar check above leaves a stray "1" at
    // the prompt, and appending to it turned the marker command into
    // "1echo ..." — the sort of thing that reads as a broken feature when it
    // is a dirty prompt.
    await touch.locator('[data-testid="compose-input"]').fill('true')
    await touch.locator('[data-testid="compose-send"]').click()
    await sleep(700)
    await touch.locator('[data-testid="compose-input"]').fill(`echo ${marker.slice(0, 6)}"${marker.slice(6)}"`)
    await touch.locator('[data-testid="compose-send"]').click()
    let markerBox = null
    for (let i = 0; i < 30; i++) {
      // Where the marker is, computed rather than looked up.
      //
      // This used to find the row element and ask it. Under the renderer that
      // ships there is no row element -- the screen is a canvas -- and pinning
      // this one context to the DOM renderer to keep the lookup working meant
      // the phone paths were only ever checked against a renderer nobody runs.
      // That hid a real defect: the touch layer measured `.xterm-rows` too, so
      // dragging scrolled zero rows on every real phone.
      //
      // The screen's box and the row's index in the viewport are enough.
      markerBox = await touch.evaluate((needle) => {
        const rows = window.vibepanelScreen?.() ?? []
        const i = rows.findIndex((r) => r.includes(needle))
        if (i < 0) return null
        const screen = document.querySelector('.xterm-screen')
        if (!screen) return null
        const r = screen.getBoundingClientRect()
        const h = r.height / rows.length
        return { x: r.x, y: r.y + i * h, w: r.width, h }
      }, marker)
      if (markerBox) break
      await sleep(400)
    }
    if (!markerBox) {
      const shown = (await screenRows(touch)).filter((t) => t.trim()).slice(-6)
      note('FAIL', 'mobile',
        `the phone could not send a command through the compose box; the terminal's last ` +
        `lines are ${JSON.stringify(shown)}`)
    } else {
      const cdp = await touchCtx.newCDPSession(touch)
      const y = markerBox.y + markerBox.h / 2
      const at = (x) => ({ touchPoints: [{ x, y, radiusX: 8, radiusY: 8, force: 1, id: 1 }] })
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', ...at(markerBox.x + 2) })
      // Longer than the hold threshold, or this is a scroll.
      await sleep(700)
      // Past the end of the text: dragging off the edge is how anyone selects
      // to the end of a line, and it must clamp rather than throw.
      for (let x = markerBox.x + 2; x < markerBox.x + markerBox.w + 40; x += 12) {
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', ...at(x) })
        await sleep(25)
      }
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
      await sleep(500)

      const bar = await touch.locator('[data-testid="selection-bar"]').isVisible().catch(() => false)
      if (!bar) {
        note('FAIL', 'mobile',
          'pressing and holding on terminal text selected nothing; there is no way to copy ' +
          'from a phone')
      } else {
        const selected = await touch.evaluate(() => {
          const el = [...document.querySelectorAll('[data-testid="selection-bar"] span')][0]
          return el?.textContent ?? ''
        })
        if (!/\d+ characters selected/.test(selected)) {
          note('FAIL', 'mobile', `the selection bar reads ${JSON.stringify(selected)}`)
        }
        await touch.screenshot({ path: join(SHOTS, 'touch-selection.png') })
        await touch.locator('[data-testid="selection-copy"]').click()
        await sleep(400)
        const clip = await touch.evaluate(() => navigator.clipboard.readText().catch(() => ''))
        if (!clip.includes(marker)) {
          note('FAIL', 'mobile',
            `the copy button put ${JSON.stringify(clip.slice(0, 60))} on the clipboard, not the ` +
            'selected line')
        }
        // Tapping away has to dismiss it, or the bar sits over the key bar
        // forever.
        await cdp.send('Input.dispatchTouchEvent', {
          type: 'touchStart',
          ...at(markerBox.x + 2),
        })
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
        await sleep(500)
        if (await touch.locator('[data-testid="selection-bar"]').isVisible().catch(() => false)) {
          note('FAIL', 'mobile', 'a tap elsewhere did not dismiss the selection bar')
        }
      }
    }
    // Scrolling back with a finger.
    //
    // tmux keeps 20,000 lines per session. Until the alternate screen was
    // taken out of the embedded config there was no scrollback to reach at
    // all; now there is, and on a phone nothing could still reach it. xterm's
    // scrollable element listens for wheel events, which a touchscreen never
    // sends, and the panel's own pgup key sends ESC[5~, which a shell ignores.
    // Measured with 269 lines behind the screen: wheel 269 -> 268, drag
    // 268 -> 268, pgup 268 -> 268.
    {
      const shown = await touch.locator('[data-testid="session-title"]').innerText().catch(() => '')
      const sess = ((await (await authed('/api/state')).json()).sessions ?? [])
        .find((x) => (x.title ?? '') === shown.trim())
      if (!sess) {
        note('WARN', 'mobile', `could not tell which session the phone is showing (${JSON.stringify(shown)})`)
      } else {
        execSync(
          `tmux -L ${SOCKET} send-keys -t '=${sess.tmuxName}:' ` +
          `'i=1; while [ $i -le 400 ]; do echo TOUCH_$i; i=$((i+1)); done' Enter`)
        const rowsOf = async () => (await screenRows(touch)).map((r) => r.trim())
        // Wait for the burst to have arrived. Measuring a picture that is still
        // moving is what made the first version of the desktop check pass one
        // run in three.
        let arrived = false
        for (let i = 0; i < 40; i++) {
          if ((await rowsOf()).some((r) => r.includes('TOUCH_400'))) { arrived = true; break }
          await sleep(500)
        }
        if (!arrived) {
          note('WARN', 'mobile', 'the burst never finished arriving; not measuring a moving picture')
        } else {
          await sleep(1200)
          const lineNo = (rows) => {
            const first = rows.find((r) => r)
            return Number((/TOUCH_(\d+)/.exec(first ?? '') ?? [])[1] ?? NaN)
          }
          const before = lineNo(await rowsOf())
          const tbox = await touch.locator('.xterm-screen').boundingBox()
          if (!tbox || !Number.isFinite(before)) {
            note('WARN', 'mobile', 'no terminal box to drag on')
          } else {
            const cdp2 = await touchCtx.newCDPSession(touch)
            const x = tbox.x + tbox.width / 2
            const y0 = tbox.y + 60
            const pt = (y) => ({ touchPoints: [{ x, y, radiusX: 8, radiusY: 8, force: 1, id: 1 }] })
            await cdp2.send('Input.dispatchTouchEvent', { type: 'touchStart', ...pt(y0) })
            // Faster than the hold threshold, or this becomes a selection.
            for (let i = 1; i <= 24; i++) {
              await cdp2.send('Input.dispatchTouchEvent', { type: 'touchMove', ...pt(y0 + i * 20) })
              await sleep(8)
            }
            await cdp2.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
            // Poll rather than read once after a fixed sleep.
            //
            // This failed twice in nine runs on trees where the change under
            // test was nowhere near it, both times reporting the top line as
            // "unreadable" -- lineNo returning NaN because the row it read was
            // mid-repaint, not because the scroll had not happened. A check
            // that fails one run in five teaches people to run it again
            // instead of looking, and the whole argument for these browser
            // checks is that a FAIL here means something.
            //
            // Keeping the last readable value separately matters for the same
            // reason: when this does fail for real, "now TOUCH_368" says the
            // scroll did not move, and "never readable" says something else
            // is wrong. The old message could not tell those apart.
            let after = NaN
            let lastReadable = NaN
            for (let i = 0; i < 20; i++) {
              after = lineNo(await rowsOf())
              if (Number.isFinite(after)) lastReadable = after
              if (Number.isFinite(after) && after < before) break
              await sleep(250)
            }
            if (!Number.isFinite(after) || after >= before) {
              note('FAIL', 'mobile',
                `dragging down did not scroll back within five seconds: top line was ` +
                `TOUCH_${before}, ` +
                `${Number.isFinite(lastReadable) ? `last readable TOUCH_${lastReadable}` : 'never readable'}. ` +
                'tmux keeps the history and a phone cannot reach it.')
            } else {
              note('PASS', 'mobile', `a finger scrolled back from TOUCH_${before} to TOUCH_${after}`)
            }
            await touch.screenshot({ path: join(SHOTS, 'touch-scrollback.png') })
          }
        }
      }
    }

    await touchCtx.close()
  }

  // ── what a first-time visitor actually sees ──────────────────────────────
  // Everything above runs in a context that has been clicking panels open for
  // several minutes. A new browser has no localStorage, and that is the state
  // every real user starts in — the one state the harness had never measured.
  const freshCtx = await browser.newContext({ serviceWorkers: 'block', viewport: { width: 1440, height: 900 } })
  const first = await freshCtx.newPage()
  await first.goto(BASE, { waitUntil: 'networkidle' })
  await first.locator('[data-testid="auth-username"]').fill(USERNAME)
  await first.locator('[data-testid="auth-password"]').fill(PASSWORD)
  await first.locator('[data-testid="auth-submit"]').click()
  await first.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 })
  await sleep(2500)
  for (const [id, what] of [
    ['right-panel', 'the files and notes tabs, and the dock under both'],
    ['bottom-terminals', 'the terminal strip'],
  ]) {
    if (!(await first.locator(`[data-testid="${id}"]`).isVisible().catch(() => false))) {
      note('FAIL', 'first-run',
        `${what} is not on screen for a visitor with no stored preferences; ` +
        'a remembered size of zero and never having chosen one are being treated alike')
    }
  }
  await first.screenshot({ path: join(SHOTS, 'first-run.png') })
  await freshCtx.close()

  // ── copying inside tmux ──────────────────────────────────────────────────
  // The panel's answer to the shim the old setup used: tmux forwards OSC 52 to
  // its client, the panel pushes it to the system clipboard. That write is not
  // inside a user gesture, and browsers refuse those — so the interesting case
  // is not the happy path but what the user is told when it is refused.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(500)
  const clipText = 'COPIED' + '_INSIDE_TMUX'
  const b64 = Buffer.from(clipText).toString('base64')
  const emitOSC52 = async (target) => {
    execSync(
      `tmux -L ${SOCKET} send-keys -t '=${target}:' ` +
        JSON.stringify(`printf '\\033]52;c;${b64}\\007'`) +
        ' Enter',
    )
  }
  const shellSession = (await (await authed('/api/state')).json()).sessions.find(
    (x) => x.title === 'scratchpad',
  )
  if (!shellSession) {
    note('WARN', 'clipboard', 'no shell session to copy from')
  } else {
    // This context was granted clipboard-write, so the write goes through and
    // nothing should be offered.
    await emitOSC52(shellSession.tmuxName)
    await sleep(2000)
    if (await page.locator('[data-testid="clipboard-offer"]').isVisible().catch(() => false)) {
      note('FAIL', 'clipboard',
        'the panel offered a manual copy even though the clipboard write succeeded')
    }
    const onClip = await page.evaluate(() => navigator.clipboard.readText().catch(() => ''))
    if (!onClip.includes('COPIED')) {
      note('FAIL', 'clipboard',
        `a copy inside tmux did not reach the clipboard; it holds ${JSON.stringify(onClip.slice(0, 60))}`)
    }

    // And a context in the state a real browser starts in: no clipboard
    // permission, so the write is refused. Silently doing nothing here is the
    // bug this checks for.
    const plainCtx = await browser.newContext({ serviceWorkers: 'block', viewport: { width: 1200, height: 800 } })
    const plain = await plainCtx.newPage()
    await plain.goto(BASE, { waitUntil: 'networkidle' })
    await plain.locator('[data-testid="auth-username"]').fill(USERNAME)
    await plain.locator('[data-testid="auth-password"]').fill(PASSWORD)
    await plain.locator('[data-testid="auth-submit"]').click()
    await plain.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 })
    await plain
      .locator('[data-testid="session-row"]', { hasText: 'scratchpad' })
      .first()
      .click()
    await sleep(2500)
    await emitOSC52(shellSession.tmuxName)
    let offered = false
    for (let i = 0; i < 20; i++) {
      if (await plain.locator('[data-testid="clipboard-offer"]').isVisible().catch(() => false)) {
        offered = true
        break
      }
      await sleep(400)
    }
    if (!offered) {
      note('FAIL', 'clipboard',
        'the browser refused the clipboard write and the panel said nothing; a copy inside ' +
        'tmux vanishes with no indication')
    } else {
      await plain.screenshot({ path: join(SHOTS, 'clipboard-offer.png') })
      await plain.locator('[data-testid="clipboard-offer"]').click()
      await sleep(600)
      if (await plain.locator('[data-testid="clipboard-offer"]').isVisible().catch(() => false)) {
        note('FAIL', 'clipboard', 'the offer stayed on screen after being taken')
      }
    }
    await plainCtx.close()
  }

  // ── two viewers, one note ────────────────────────────────────────────────
  // "open it in many places and they stay in sync" was the first thing asked
  // for, and it was true of sessions and false of the notepad: a note written
  // in one window never appeared in the other, and the second window's next
  // save replaced it. Silent loss of the user's own writing.
  // The mobile section left this page at phone width, where the right panel
  // is not rendered at all.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(600)
  // ── the PWA, in the one context that allows a worker ─────────────────────
  //
  // Every other context in this file blocks service workers so that page.route
  // keeps working, which leaves the worker itself driven by nothing. This is
  // the counterweight: registration, and the manifest that decides whether a
  // browser offers to install at all.
  {
    const pwaCtx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    const pwa = await pwaCtx.newPage()
    await pwa.goto(BASE, { waitUntil: 'networkidle' })
    if (await pwa.locator('[data-testid="auth-submit"]').isVisible().catch(() => false)) {
      await pwa.locator('[data-testid="auth-username"]').fill(USERNAME)
      await pwa.locator('[data-testid="auth-password"]').fill(PASSWORD)
      await pwa.locator('[data-testid="auth-submit"]').click()
    }
    await pwa.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 }).catch(() => {})

    const manifestRes = await pwa.request.get(`${BASE}/manifest.webmanifest`)
    if (!manifestRes.ok()) {
      note('FAIL', 'pwa', `the manifest is not served: ${manifestRes.status()}`)
    } else {
      const m = await manifestRes.json().catch(() => null)
      // The four a browser actually reads before offering to install. A
      // manifest that parses and is missing one of these is a manifest that
      // does nothing, silently.
      const missing = ['name', 'start_url', 'display', 'icons'].filter((k) => !m?.[k])
      if (missing.length) {
        note('FAIL', 'pwa', `the manifest is missing ${missing.join(', ')}, so nothing will offer to install it`)
      }
      const sizes = (m?.icons ?? []).map((i) => i.sizes)
      if (!sizes.some((z) => String(z).includes('512'))) {
        note('FAIL', 'pwa', `no 512px icon in the manifest (${JSON.stringify(sizes)}); Android will not install without one`)
      }
      if (!(m?.icons ?? []).some((i) => String(i.purpose ?? '').includes('maskable'))) {
        note('WARN', 'pwa', 'no maskable icon, so Android will letterbox the app icon')
      }
    }

    const swReady = await pwa.evaluate(async () => {
      if (!('serviceWorker' in navigator)) return 'unsupported'
      try {
        const reg = await Promise.race([
          navigator.serviceWorker.ready,
          new Promise((r) => setTimeout(() => r(null), 8000)),
        ])
        return reg ? 'ready' : 'timeout'
      } catch (e) {
        return `error: ${String(e)}`
      }
    })
    if (swReady !== 'ready') {
      note('FAIL', 'pwa',
        `the service worker never became ready (${swReady}); without one there is no install ` +
        'offer and no notification on Android, where the Notification constructor is refused')
    } else {
      note('PASS', 'pwa', 'the manifest is complete and the service worker registers')
    }
    await pwaCtx.close()
  }

  const bCtx = await browser.newContext({ serviceWorkers: 'block', viewport: { width: 1200, height: 900 } })
  const b = await bCtx.newPage()
  await b.goto(BASE, { waitUntil: 'networkidle' })
  await b.locator('[data-testid="auth-username"]').fill(USERNAME)
  await b.locator('[data-testid="auth-password"]').fill(PASSWORD)
  await b.locator('[data-testid="auth-submit"]').click()
  await b.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 })
  await sleep(2000)
  for (const p of [page, b]) {
    // An earlier section collapses the panel, and a collapsed panel has no
    // tabs to click.
    if (!(await p.locator('[data-testid="right-panel"]').isVisible().catch(() => false))) {
      await p.locator('[data-testid="right-show"]').click().catch(() => {})
      await sleep(500)
    }
    await p.locator('[data-testid="panel-tab-notes"]').click().catch(() => {})
    await sleep(500)
  }
  const noteBox = (p) => p.locator('[data-testid="notes"] textarea')
  const noteStatus = (p) => p.locator('[data-testid="notes-status"]')

  await page.bringToFront()
  const written = 'WRITTEN' + '_IN_THE_FIRST_WINDOW'
  await noteBox(page).fill(written)
  let arrived = ''
  for (let i = 0; i < 25; i++) {
    arrived = await noteBox(b).inputValue().catch(() => '')
    if (arrived === written) break
    await sleep(400)
  }
  if (arrived !== written) {
    note('FAIL', 'sync',
      `a note written in one window never reached the other; it shows ${JSON.stringify(arrived)}`)
  }

  // Now the case the timestamp check exists for: the second window is midway
  // through typing when the first one saves. Adopting silently would delete
  // what is being typed; overwriting silently would delete what was saved.
  await b.bringToFront()
  const halfTyped = 'HALF' + '_TYPED_HERE'
  await noteBox(b).fill(halfTyped)
  // Inside the save debounce, so this lands under the pending write.
  await authed(`/api/projects/${proj.id}/notes`, {
    method: 'PUT',
    body: JSON.stringify({ content: 'WROTE_FROM_SOMEWHERE_ELSE' }),
  })
  await sleep(3000)
  const kept = await noteBox(b).inputValue().catch(() => '')
  const noteState = await noteStatus(b).getAttribute('data-status').catch(() => null)
  if (kept !== halfTyped) {
    note('FAIL', 'sync',
      `text being typed was replaced by another window's save: ${JSON.stringify(kept)}`)
  }
  if (noteState !== 'conflict') {
    note('FAIL', 'sync',
      `the note reports status ${JSON.stringify(noteState)} after a rejected save; the user is not ` +
      'being told their text is unsaved')
  }
  const stored = await (await authed(`/api/projects/${proj.id}/notes`)).json()
  if (stored.content !== 'WROTE_FROM_SOMEWHERE_ELSE') {
    note('FAIL', 'sync',
      `a stale window overwrote the stored note: ${JSON.stringify(stored.content)}`)
  }
  await b.screenshot({ path: join(SHOTS, 'note-conflict.png') })
  await bCtx.close()
  await page.bringToFront()
  await page.locator('[data-testid="panel-tab-files"]').click().catch(() => {})
  await sleep(400)

  // ── moving files in and out ──────────────────────────────────────────────
  // "even file transfer" was in the brief and nothing had been built. The
  // interesting half is the drop: uploading is only useful if the path ends up
  // at the prompt, because going to look it up afterwards is most of the work.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(600)
  // The first project is rooted at the harness's working directory, not at
  // DATA — that belongs to the second project, whose tab is not open.
  const projRoot = process.cwd()
  writeFileSync(join(projRoot, 'download-me.txt'), 'DOWNLOADED_CONTENT_OK\n')
  // Uploads refuse to overwrite, so a leftover from the last run would make
  // this fail with a 409 that has nothing to do with the code under test.
  rmSync(join(projRoot, 'dropped-note.txt'), { force: true })
  await page.locator('[data-testid="panel-tab-files"]').click().catch(() => {})
  await sleep(700)
  // The listing is a snapshot; the file was written after it was taken.
  await page.locator('[data-testid="file-refresh"]').click().catch(() => {})
  await sleep(900)
  const fileRow = page.locator('[data-testid="file-entry"]', { hasText: 'download-me.txt' }).first()
  if ((await fileRow.count()) === 0) {
    note('WARN', 'files', 'the new file did not appear in the tree; skipping the transfer check')
  } else {
    const [download] = await Promise.all([
      page.waitForEvent('download', { timeout: 10000 }).catch(() => null),
      fileRow.locator('[data-testid="file-download"]').click({ force: true }),
    ])
    if (!download) {
      note('FAIL', 'files', 'clicking download produced no download')
    } else {
      const to = join(SHOTS, 'downloaded.txt')
      await download.saveAs(to)
      const got = readFileSync(to, 'utf8')
      if (!got.includes('DOWNLOADED_CONTENT_OK')) {
        note('FAIL', 'files', `the downloaded file contains ${JSON.stringify(got.slice(0, 80))}`)
      }
      if (download.suggestedFilename() !== 'download-me.txt') {
        note('FAIL', 'files',
          `the download is named ${JSON.stringify(download.suggestedFilename())}, not the file's name`)
      }
    }

    // Dropping onto the terminal. DataTransfer has to be built in the page —
    // Playwright cannot hand a real one across the boundary.
    const dropped = await page.evaluateHandle(() => {
      const dt = new DataTransfer()
      dt.items.add(new File(['DROPPED_FILE_BODY'], 'dropped-note.txt', { type: 'text/plain' }))
      return dt
    })
    const target = page.locator('[data-testid="drop-overlay"]')
    const zone = page.locator('.xterm-screen').first()
    await zone.dispatchEvent('dragover', { dataTransfer: dropped })
    await sleep(300)
    if (!(await target.isVisible().catch(() => false))) {
      note('FAIL', 'files', 'dragging files over the terminal shows nothing; the drop looks unsupported')
    }
    await zone.dispatchEvent('drop', { dataTransfer: dropped })
    await sleep(2500)

    // The upload is only half of it: the path has to be waiting at the prompt.
    //
    // Named, not focused. A synthetic drop leaves focus on nothing in
    // particular, and this used to read whichever terminal `vibepanelScreen`
    // guessed at -- which was right here and wrong for the bottom terminal
    // check, in the same run.
    const mainID = await zone.locator('xpath=ancestor::*[@data-session-id][1]')
      .getAttribute('data-session-id').catch(() => null)
    let typed = ''
    for (let i = 0; i < 25; i++) {
      typed = await screenText(page, undefined, mainID ? { id: mainID } : undefined)
      if (typed.includes('dropped-note.txt')) break
      await sleep(400)
    }
    if (!typed.includes('dropped-note.txt')) {
      note('FAIL', 'files',
        'a file dropped on the terminal did not put its path at the prompt: ' +
        JSON.stringify(typed.replace(/\s+/g, ' ').trim().slice(-160)))
    }
    const landed = await (await authed(`/api/projects/${proj.id}/files?path=`)).json()
    if (!(landed.entries ?? []).some((e) => e.name === 'dropped-note.txt')) {
      note('FAIL', 'files', 'the dropped file is not in the project directory')
    }
    // A screenshot pasted into the terminal.
    //
    // xterm's paste handling is for text, so an image landed nowhere: ctrl-V
    // did nothing and there was no way to tell whether the panel had ignored it
    // or the clipboard was empty. Dropping a file already worked; this is the
    // same journey for people who took a screenshot rather than saved one,
    // which on every desktop is the faster half.
    rmSync(join(projRoot, 'pasted.png'), { force: true })
    await page.locator('.xterm-screen').first().click()
    // Built and dispatched inside the page rather than through Playwright's
    // dispatchEvent, which does not carry a DataTransfer across the boundary as
    // `clipboardData`. The first version of this check did, and reported the
    // feature broken while it worked -- a fixture failure wearing a product
    // failure's clothes.
    await page.evaluate(() => {
      const bytes = Uint8Array.from(atob(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
      ), (c) => c.charCodeAt(0))
      const dt = new DataTransfer()
      dt.items.add(new File([bytes], 'pasted.png', { type: 'image/png' }))
      const target = document.querySelector('.xterm-screen') ?? document.body
      target.dispatchEvent(new ClipboardEvent('paste', {
        clipboardData: dt,
        bubbles: true,
        cancelable: true,
      }))
    })
    let pastedPath = ''
    for (let i = 0; i < 25; i++) {
      pastedPath = await screenText(page)
      // Whitespace stripped: the terminal wraps, so the path arrives split as
      // ".../pasted\n.png". The first version of this check missed it and
      // reported a working feature broken.
      if (pastedPath.replace(/\s/g, '').includes('pasted.png')) break
      await sleep(400)
    }
    const pastedLanded = await (await authed(`/api/projects/${proj.id}/files?path=`)).json()
    if (!(pastedLanded.entries ?? []).some((e) => e.name === 'pasted.png')) {
      note('FAIL', 'files',
        'an image pasted into the terminal did not reach the project directory')
    } else if (!pastedPath.replace(/\s/g, '').includes('pasted.png')) {
      note('FAIL', 'files',
        'a pasted image was uploaded and its path never reached the prompt, so the one thing ' +
        `the feature is for -- handing the agent a screenshot -- did not happen: ${JSON.stringify(pastedPath.slice(-160))}`)
    } else {
      note('PASS', 'files', 'a pasted screenshot is uploaded and its path put at the prompt')
    }
    rmSync(join(projRoot, 'pasted.png'), { force: true })

    // A filename carrying a tab, which is the one control character that can
    // travel the whole way.
    //
    // The finding this comes from said a newline, and a newline cannot get
    // here: the browser escapes LF, CR and the double quote in a multipart
    // filename before the request leaves the page, so a file dropped as
    // "two\nlines.txt" lands on disk as "two%0Alines.txt" -- measured, by an
    // earlier version of this very check finding the encoded name at the
    // prompt. Nor can the rest of them: Go's MIME header parser refuses the
    // Content-Disposition line outright, 400 "malformed MIME header line", for
    // 0x15 and for ESC.
    //
    // Tab is the exception, because textproto treats it as ordinary header
    // whitespace. It arrives, it lands on disk with a raw 0x09 in the name, and
    // at a prompt readline reads it as "complete this", not as a character. So
    // the path the user is invited to press enter on is whatever completion did
    // to it.
    //
    // The assertion is on the bytes rather than on the behaviour, deliberately:
    // what completion does depends on what else is in the directory, and a
    // check whose expected value moves with the fixture is worse than no check.
    // What must hold is that no control byte reached the prompt at all.
    const oddName = 'ZULU\tKILO.txt'
    rmSync(join(projRoot, oddName), { force: true })
    await page.keyboard.press('Control+c')
    await sleep(500)
    const dropped2 = await page.evaluateHandle(() => {
      const dt = new DataTransfer()
      dt.items.add(new File(['TAB_NAME_BODY'], 'ZULU\tKILO.txt', { type: 'text/plain' }))
      return dt
    })
    await zone.dispatchEvent('dragover', { dataTransfer: dropped2 })
    await sleep(200)
    await zone.dispatchEvent('drop', { dataTransfer: dropped2 })
    await sleep(2500)

    let dropText = ''
    for (let i = 0; i < 25; i++) {
      dropText = await screenText(page)
      if (dropText.includes('KILO.txt')) break
      await sleep(400)
    }
    const landed2 = await (await authed(`/api/projects/${proj.id}/files?path=`)).json()
    if (!(landed2.entries ?? []).some((e) => e.name === oddName)) {
      note('WARN', 'files',
        'the tab-named file did not land on disk, so the prompt check below proves nothing')
    } else if (!dropText.replace(/\s/g, '').includes('ZULU\\x09KILO.txt')) {
      // One branch for both failure modes, because they are one failure. With
      // the tab sent raw, readline reads it as "complete this" and what lands
      // at the prompt is whatever completion made of a half-typed path -- which
      // measured as the second half of the name never appearing at all, not as
      // a tab sitting visibly in it. Reporting that as "never reached the
      // prompt" would send the next reader looking at the upload.
      //
      // Whitespace is stripped first: the terminal wraps and innerText reports
      // a space at the wrap, which split "\x09" down the middle and failed this
      // the first time it ran. Stripping cannot hide what it looks for -- a raw
      // tab is whitespace too, so an unquoted name collapses to ZULUKILO.txt
      // and still does not match.
      note('FAIL', 'files',
        'the path at the prompt is not the name of the file that was uploaded. A tab in a ' +
        'filename survives the whole way -- the browser escapes only LF, CR and the quote, ' +
        'and Go accepts a tab in the header -- and readline reads it as completion: ' +
        JSON.stringify(dropText.replace(/\s+/g, ' ').trim().slice(-160)))
    }

    // A filename that renders its own suffix backwards.
    //
    // FileTree sanitises with an inline safeText(e.name), and nothing asserted
    // it -- the browser checks do exercise the rendering path, they locate rows
    // by hasText, but every name in them is plain ASCII, so nothing there
    // touched the sanitising either.
    //
    // A file called "invoice\u202Egnp.pdf" displays as "invoicefdp.png" in any
    // terminal or file list that honours the override. This is the panel's own
    // list, showing whatever an agent last wrote to disk.
    const bidiName = 'invoice\u202Egnp.pdf'
    writeFileSync(join(projRoot, bidiName), 'BIDI_NAME_BODY')
    await page.locator('[data-testid="file-refresh"]').click().catch(() => {})
    await sleep(900)
    const rows = await page.locator('[data-testid="file-entry"]').allInnerTexts().catch(() => [])
    const shown = rows.find((r) => r.includes('gnp.pdf') || r.includes('invoice'))
    if (!shown) {
      note('WARN', 'files', 'the bidi-named file never appeared in the tree; nothing was measured')
    } else if (shown.includes('\u202E')) {
      note('FAIL', 'files',
        `the file tree renders a name containing an override raw: ${JSON.stringify(shown)}. ` +
        'It displays its own suffix backwards, which is how a .pdf reads as a .png.')
    } else {
      note('PASS', 'files', `a bidi filename is neutralised in the tree: ${JSON.stringify(shown.trim())}`)
    }
    rmSync(join(projRoot, bidiName), { force: true })

    await page.screenshot({ path: join(SHOTS, 'file-transfer.png') })
    for (const leftover of ['download-me.txt', 'dropped-note.txt', oddName]) {
      rmSync(join(projRoot, leftover), { force: true })
    }
  }

  // ── a link out of the project is shown, and is not openable ──────────────
  // Readable used to be decided from the file mode alone, so a symlink whose
  // target sat outside the project root read as an ordinary file and clicking
  // it served whatever it pointed at. An agent writes into this directory;
  // creating such a link is one command.
  //
  // Listed rather than hidden, deliberately: the link is a fact about the
  // directory, and a tree that omits it lies about what is there. So this
  // asserts both halves — the row exists, and it offers nothing to click.
  const outsideTarget = join(DATA, 'outside-the-project.txt')
  writeFileSync(outsideTarget, 'SHOULD_NOT_BE_SERVED\n')
  const escapeLink = join(projRoot, 'escape-link.txt')
  rmSync(escapeLink, { force: true })
  symlinkSync(outsideTarget, escapeLink)
  await page.locator('[data-testid="panel-tab-files"]').click().catch(() => {})
  await sleep(400)
  await page.locator('[data-testid="file-refresh"]').click().catch(() => {})
  await sleep(900)
  const escRow = page.locator('[data-testid="file-entry"]', { hasText: 'escape-link.txt' }).first()
  if ((await escRow.count()) === 0) {
    note('FAIL', 'files/escape', 'a symlink leaving the project is missing from the tree entirely')
  } else {
    if ((await escRow.locator('[data-testid="file-escapes"]').count()) === 0) {
      note('FAIL', 'files/escape',
        'a symlink pointing outside the project is listed with no sign that it does')
    }
    if ((await escRow.locator('[data-testid="file-download"]').count()) > 0) {
      note('FAIL', 'files/escape',
        'the panel offers to download a symlink whose target is outside the project')
    }
    await page.screenshot({ path: join(SHOTS, 'file-escape.png') })
  }
  // The UI is one half. The endpoint answers on its own, and a URL typed by
  // hand does not go through the tree at all.
  //
  // The control runs first and is not optional. A misremembered path makes
  // every request 404, and a 404 is indistinguishable from a refusal — this
  // check has already been written once against an endpoint that did not
  // exist, and read as a clean pass.
  const control = join(projRoot, 'control-file.txt')
  writeFileSync(control, 'CONTROL_OK\n')
  const ctlResp = await authed(`/api/projects/${proj.id}/download?path=control-file.txt`)
  if (!ctlResp.ok) {
    note('FAIL', 'files/escape',
      `the download endpoint refused an ordinary file (${ctlResp.status}); ` +
      'the escape check below cannot tell a refusal from a wrong URL')
  } else {
    const escResp = await authed(`/api/projects/${proj.id}/download?path=escape-link.txt`)
    if (escResp.ok) {
      const body = await escResp.text()
      note('FAIL', 'files/escape',
        `the download endpoint served a link out of the project (${escResp.status}), ` +
        `returning ${JSON.stringify(body.slice(0, 40))}`)
    }
  }
  rmSync(control, { force: true })
  rmSync(escapeLink, { force: true })
  rmSync(outsideTarget, { force: true })

  // ── a dead process does not look like a finished job ─────────────────────
  // tmux keeps a dead pane on screen, and the panel used to read that as
  // "done" — the same thing it says about an agent that finished the work. A
  // crash at 2am and a successful refactor were the same green check.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(600)
  const flag = join(DATA, 'restart-flag')
  const dieOnce = `test -f ${flag} || { touch ${flag}; echo boom >&2; exit 3; }; sleep 120`
  for (const [title, command] of [
    ['dies', ['bash', '-c', dieOnce]],
    ['quits', ['bash', '-c', 'true']],
  ]) {
    await authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId: proj.id, title, command }),
    })
  }

  const rowOf = (title) => page.locator('[data-testid="session-row"]', { hasText: title }).first()
  const glyphOf = async (title) => {
    const svg = rowOf(title).locator('svg[role="img"]').first()
    return {
      label: await svg.getAttribute('aria-label').catch(() => null),
      shape: await svg.innerHTML().catch(() => null),
    }
  }
  let crash = null
  for (let i = 0; i < 40; i++) {
    crash = await glyphOf('dies')
    if (crash.label?.startsWith('Exited')) break
    await sleep(500)
  }
  const clean = await glyphOf('quits')
  const running = await glyphOf('scratchpad')

  if (crash?.label !== 'Exited with status 3') {
    note('FAIL', 'exit',
      `a session whose process died with status 3 is labelled ${JSON.stringify(crash?.label)}; ` +
      'a crash is being reported as an ordinary state')
  }
  if (clean.label !== 'Exited') {
    note('FAIL', 'exit', `a session that exited cleanly is labelled ${JSON.stringify(clean.label)}`)
  }
  // Red line 4: shape, not only hue. Three different situations must not draw
  // the same glyph, or the distinction dies with the first colour-blind user
  // or the first dim screen.
  const shapes = [
    ['crashed', crash?.shape],
    ['exited cleanly', clean.shape],
    ['running', running.shape],
  ]
  for (let i = 0; i < shapes.length; i++) {
    for (let j = i + 1; j < shapes.length; j++) {
      if (shapes[i][1] && shapes[i][1] === shapes[j][1]) {
        note('FAIL', 'exit',
          `"${shapes[i][0]}" and "${shapes[j][0]}" draw an identical glyph, so only colour ` +
          'could tell them apart')
      }
    }
  }
  // The number, in text, next to the shape that cannot carry it.
  const badge = await rowOf('dies').innerText()
  if (!badge.includes('exit 3')) {
    note('FAIL', 'exit', `the crashed row does not show the exit status: ${JSON.stringify(badge)}`)
  }
  await page.screenshot({ path: join(SHOTS, 'exited-sessions.png') })

  // Red line 4, mechanically: state is never carried by colour alone.
  //
  // Here rather than earlier: this is the point in the run where a crashed
  // session and a cleanly exited one are both on screen, and those two are the
  // pair the rule exists for. Run before anything had exited, the check
  // compared working against done and passed against a build where the crash
  // glyph had been replaced by a red copy of the clean one.
  // The sidebar at this point holds several states at once, and each dot is an
  // svg with a <title>. Strip the colours and two dots that mean different
  // things must still look different — a check that would have caught somebody
  // "simplifying" the crashed cross into a red version of the clean square,
  // which is exactly the kind of change that looks tidier in a diff and is
  // invisible to a colourblind reader at 2am.
  const glyphs = await page.evaluate(() => {
    const out = []
    for (const svg of document.querySelectorAll('svg[role="img"]')) {
      const label = (svg.querySelector('title')?.textContent ?? '').trim()
      if (!label) continue
      // Geometry only: element names and their shape attributes. Dashes count
      // as shape, because a dashed outline is visible without colour.
      const shape = [...svg.children]
        .filter((n) => n.tagName !== 'title')
        .map((n) => {
          const attrs = ['d', 'points', 'r', 'cx', 'cy', 'width', 'height', 'x', 'y',
            'stroke-dasharray', 'stroke-width']
            .map((a) => n.getAttribute(a))
            .filter(Boolean)
            .join(',')
          return `${n.tagName}(${attrs})`
        })
        .join('+')
      out.push({ label, shape })
    }
    return out
  })
  // What each label means, by category rather than by first word.
  //
  // The first version took the first word, which made "Exited" and "Exited
  // with status 3" the same meaning — so replacing the crashed cross with a
  // red copy of the clean square passed. Those two are the pair the red line
  // exists for: finished and crashed, told apart at a glance.
  const meaningOf = (label) => {
    if (/^Exited with status/.test(label)) return 'crashed'
    if (/^Exited/.test(label)) return 'exited cleanly'
    if (/^Gone/.test(label)) return 'gone'
    return label.split(/[ —]/)[0].toLowerCase()
  }
  const byShape = new Map()
  for (const g of glyphs) {
    const meaning = meaningOf(g.label)
    const seen = byShape.get(g.shape)
    if (seen && seen !== meaning) {
      note('FAIL', 'ui',
        `"${seen}" and "${meaning}" are drawn with identical geometry, so they differ only by ` +
        'colour')
    }
    byShape.set(g.shape, meaning)
  }
  if (byShape.size < 2) {
    note('WARN', 'ui', `only ${byShape.size} distinct state glyph(s) on screen to compare`)
  }


  // A corpse you can only delete is not much use at 2am.
  const restart = rowOf('dies').locator('[data-testid="restart-session"]')
  if (!(await restart.isVisible().catch(() => false))) {
    note('FAIL', 'exit', 'a dead session offers no way to start it again')
  } else {
    await restart.click()
    let revived = false
    for (let i = 0; i < 30; i++) {
      const g = await glyphOf('dies')
      if (g.label && !g.label.startsWith('Exited')) { revived = true; break }
      await sleep(500)
    }
    if (!revived) {
      note('FAIL', 'exit', 'restarting a dead session left it looking dead')
    }
  }

  // The same way out, from the header of the session you are looking at.
  //
  // Two buttons, two places, and only the sidebar one had ever been pressed.
  // The header one is the one you reach for after reading the stack trace that
  // just scrolled past, which is the whole reason it exists — and an
  // affordance nobody clicks is one a refactor can quietly detach.
  //
  // Its own session and its own flag file: the check above revives `dies`, and
  // a die-once script is what makes "it restarted" distinguishable from "it
  // crashed again immediately".
  const headerFlag = join(DATA, 'header-restart-flag')
  await authed('/api/sessions', {
    method: 'POST',
    body: JSON.stringify({
      projectId: proj.id,
      title: 'header-restart',
      command: ['bash', '-c',
        `test -f ${headerFlag} || { touch ${headerFlag}; echo boom >&2; exit 3; }; sleep 120`],
    }),
  })
  let headerDead = false
  for (let i = 0; i < 40; i++) {
    await sleep(500)
    if ((await rowOf('header-restart').innerText().catch(() => '')).includes('exit 3')) {
      headerDead = true
      break
    }
  }
  if (!headerDead) {
    note('WARN', 'exit', 'the header restart check had no dead session to work with')
  } else {
    await rowOf('header-restart').click()
    await sleep(1200)
    const headerBtn = page.locator('[data-testid="restart-current"]')
    if (!(await headerBtn.isVisible().catch(() => false))) {
      note('FAIL', 'exit',
        'a crashed session offers no way to run it again from the header you are already ' +
        'looking at, which is where you are when you find out')
    } else {
      const tip = await headerBtn.getAttribute('title')
      if (!/status 3/.test(tip ?? '')) {
        note('WARN', 'exit',
          `the restart tooltip does not name the status it is recovering from: ${JSON.stringify(tip)}`)
      }
      await headerBtn.click()
      let back = false
      for (let i = 0; i < 40; i++) {
        await sleep(500)
        if (!(await rowOf('header-restart').innerText().catch(() => '')).includes('exit 3')) {
          back = true
          break
        }
      }
      if (!back) {
        note('FAIL', 'exit',
          'clicking restart in the header left the session showing its old exit status')
      } else if (await headerBtn.isVisible().catch(() => false)) {
        note('FAIL', 'exit',
          'the header still offers restart after a successful one, so it still says the ' +
          'session is dead')
      }
    }
  }

  // ── the panel says when it is guessing ───────────────────────────────────
  // Without state reporting the heuristic has only the terminal bell, and
  // Claude Code does not ring it when it stops for a decision — so the state
  // the panel exists to show would be silently missed. Saying so is the
  // difference between a limitation and a lie.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(600)
  // A link named after the agent, so the pane reports that command. What the
  // program does is irrelevant; the detector keys on its name.
  execSync(`ln -sf "$(command -v sleep)" ${JSON.stringify(join(DATA, 'claude'))}`)
  await authed('/api/sessions', {
    method: 'POST',
    body: JSON.stringify({ projectId: proj.id, command: [join(DATA, 'claude'), '600'] }),
  })

  let sawNotice = false
  for (let i = 0; i < 30; i++) {
    if (await page.locator('[data-testid="state-guessed-notice"]').isVisible().catch(() => false)) {
      sawNotice = true
      break
    }
    await sleep(500)
  }
  if (!sawNotice) {
    const st = await (await authed('/api/state')).json()
    note('FAIL', 'honesty',
      `an agent is running with nothing reporting its state and the panel does not say so; ` +
      `stateGuessed=${st.stateGuessed}, commands=${JSON.stringify(st.sessions.map((x) => x.command))}`)
  } else {
    // It has to lead somewhere, not just complain.
    await page.locator('[data-testid="state-guessed-notice"]').click()
    await sleep(800)
    if (!(await page.locator('[data-testid="settings"]').isVisible().catch(() => false))) {
      note('FAIL', 'honesty', 'the notice does not open the place that fixes it')
    } else {
      // Opening the dialog is not enough. It shows one group at a time, so a
      // notice that lands on any other one has sent somebody to a page about
      // something else and told them it was the fix.
      const onReporting = await page
        .locator('[data-section="reporting"]')
        .isVisible()
        .catch(() => false)
      if (!onReporting) {
        const at = await page.locator('[data-testid="settings-body"]').getAttribute('data-group')
          .catch(() => null)
        note('FAIL', 'honesty',
          `the notice opens the settings dialog on ${at}, not on state reporting`)
      }
      await page.locator('[data-testid="settings-close"]').click()
      await sleep(400)
    }
    await page.screenshot({ path: join(SHOTS, 'guessing.png') })
  }

  // ── passkeys ─────────────────────────────────────────────────────────────
  // Driven through a virtual authenticator, because the only way to know a
  // WebAuthn implementation works is to complete a ceremony with a browser.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(500)
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })

  const openSettings = page.locator('[data-testid="settings-open"]')
  if (!(await openSettings.isVisible().catch(() => false))) {
    note('FAIL', 'settings', 'no settings control')
  } else {
    await openSettings.click()
    await sleep(900)

    // ── settings ───────────────────────────────────────────────────────────
    //
    // Five groups on a rail, one on screen. Every block below says which one
    // it needs; the gear opens on Sessions, which is where the hooks are.
    const rail = await page.locator('[data-testid="settings-rail"] [role="tab"]').count()
    if (rail !== 5) {
      note('FAIL', 'settings', `the settings rail has ${rail} groups on it, expected 5`)
    }
    await page.screenshot({ path: join(SHOTS, 'settings.png') })

    // What it would write has to be readable before agreeing to it, and it
    // has to be valid JSON — a snippet that does not parse is worse than none.
    await page.locator('[data-testid="hooks-preview"]').click()
    await sleep(400)
    const snippet = await page.locator('[data-testid="hooks-status"] pre').first().innerText().catch(() => '')
    try {
      JSON.parse(snippet)
    } catch {
      note('FAIL', 'settings', `the hook snippet is not valid JSON: ${JSON.stringify(snippet.slice(0, 200))}`)
    }

    const installBtn = page.locator('[data-testid="hooks-install"]')
    if (await installBtn.isVisible().catch(() => false)) {
      await installBtn.click()
      let installed = false
      for (let i = 0; i < 25; i++) {
        if (await page.locator('[data-testid="hooks-remove"]').isVisible().catch(() => false)) {
          installed = true
          break
        }
        await sleep(400)
      }
      if (!installed) {
        note('FAIL', 'settings', 'installing the hooks did not change the state')
      } else {
        // Installing hooks does nothing to the sessions already open, because
        // an agent reads them when it starts — and in a panel built for a
        // dozen long-lived agents, that is all of them. Without a word about
        // it the status says installed, every state stays guessed, and there
        // is nothing on screen connecting the two.
        //
        // Claude Code cannot explain it either. Its own instruction, in the
        // binary: "Tell the user to open `/hooks` once (reloads config) or
        // restart — you can't do this yourself".
        const noteShown = await page
          .locator('[data-testid="hooks-restart-note"]')
          .isVisible()
          .catch(() => false)
        if (!noteShown) {
          note('FAIL', 'settings',
            'installing hooks says nothing about the sessions already running, which ' +
            'will keep guessing until each one reloads or restarts')
        }
        const statusText = await page.locator('[data-testid="hooks-status"]').innerText().catch(() => '')
        if (/reporting \d+ events/.test(statusText)) {
          note('FAIL', 'settings',
            'the status claims the hooks are reporting; the panel has read a file and ' +
            'heard from nothing, and for every open session it is not true')
        }

        // Removable, or it is a change to somebody's configuration with no way
        // back.
        await page.locator('[data-testid="hooks-remove"]').click()
        let removed = false
        for (let i = 0; i < 25; i++) {
          if (await page.locator('[data-testid="hooks-install"]').isVisible().catch(() => false)) {
            removed = true
            break
          }
          await sleep(400)
        }
        if (!removed) note('FAIL', 'settings', 'the hooks could not be removed again')
      }
    }

    // The hook install must have landed in the throwaway home, not the real
    // one. Getting this wrong edits the machine's actual agent configuration,
    // which is what happened the first time this check ran.
    const settingsPathShown = await page
      .locator('[data-testid="hooks-status"]')
      .innerText()
      .catch(() => '')
    if (!settingsPathShown.includes(FAKE_HOME)) {
      note('FAIL', 'settings',
        `the hooks target is outside the throwaway home: ${JSON.stringify(settingsPathShown.slice(0, 200))}`)
    }

    await settingsGroup(page, 'panel')
    const status = await page.locator('[data-testid="settings-status"]').innerText().catch(() => '')
    for (const want of ['Version', 'Uptime', 'Sessions', 'tmux socket', 'Listening']) {
      if (!status.includes(want)) {
        note('FAIL', 'settings', `status is missing ${want}: ${JSON.stringify(status.slice(0, 200))}`)
      }
    }
    if (/undefined|NaN/.test(status)) {
      note('FAIL', 'settings', `status rendered a broken value: ${JSON.stringify(status)}`)
    }

    // The status has to keep being true while you are looking at it.
    //
    // Half of what this dialog shows is live — uptime, session count, how many
    // browsers are watching — and it was fetched once when the dialog opened.
    // A settings page for observing the backend that stops observing the
    // moment you open it answers a question about the past.
    const uptimeNow = () =>
      page.locator('[data-testid="settings-status"]').innerText().catch(() => '')
    const statusBefore = await uptimeNow()
    await sleep(6000)
    const statusAfter = await uptimeNow()
    if (statusBefore && statusBefore === statusAfter) {
      note('FAIL', 'settings',
        'the status block is identical six seconds later, so nothing is being refreshed while ' +
        `the dialog is open: ${JSON.stringify(statusBefore.slice(0, 120))}`)
    }

    await settingsGroup(page, 'account')
    const audit = await page.locator('[data-testid="settings-audit"]').innerText().catch(() => '')
    if (!audit.includes('login')) {
      note('WARN', 'settings', 'the activity log shows no sign-in')
    }

    // Changing the password, from the page rather than from SQLite.
    //
    // There was no way to do it from anywhere: the wizard set one once and
    // nothing could replace it, so "this leaked" meant stopping the panel and
    // editing the database by hand.
    const wrongFirst = page.locator('[data-testid="password-current"]')
    if ((await wrongFirst.count()) === 0) {
      note('FAIL', 'settings', 'there is no way to change the password')
    } else {
      // The current one is required: a stolen cookie must not be enough to
      // lock the owner out of their own panel.
      await wrongFirst.fill('not the password')
      await page.locator('[data-testid="password-next"]').fill('a brand new long password')
      await page.locator('[data-testid="password-submit"]').click()
      await sleep(1200)
      if (!(await page.locator('[data-testid="password-error"]').isVisible().catch(() => false))) {
        note('FAIL', 'settings', 'a wrong current password was accepted, or reported nothing')
      }

      await wrongFirst.fill(PASSWORD)
      await page.locator('[data-testid="password-next"]').fill(NEW_PASSWORD)
      await page.locator('[data-testid="password-submit"]').click()
      let changed = false
      for (let i = 0; i < 20; i++) {
        if (await page.locator('[data-testid="password-done"]').isVisible().catch(() => false)) {
          changed = true
          break
        }
        await sleep(300)
      }
      if (!changed) {
        const why = await page.locator('[data-testid="password-error"]').innerText().catch(() => '')
        note('FAIL', 'settings', `the password did not change: ${JSON.stringify(why)}`)
      } else {
        // This browser keeps working — being signed out of the page you just
        // used to change your password reads as the change having failed.
        const stillIn = await page.locator('[data-testid="sidebar"]').isVisible().catch(() => false)
        if (!stillIn) {
          note('FAIL', 'settings', 'changing the password signed out the browser that changed it')
        }
        // And the old one is genuinely gone.
        const relog = await fetch(`${BASE}/api/auth/login`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ username: USERNAME, password: PASSWORD }),
        })
        if (relog.ok) {
          note('FAIL', 'settings', 'the old password still signs in after a change')
        }
      }
    }

    // ── the board editor ───────────────────────────────────────────────────
    //
    // The wall's board is arranged by dragging it now, and a drag is exactly
    // the thing a unit test cannot see: `board/edit.ts` has the arithmetic
    // pinned, and none of that says whether a press on a palette entry and a
    // release over the canvas produce a widget on the board.
    //
    // Pointer Events by hand rather than Playwright's `dragTo`, which uses the
    // mouse API and so does not exercise the same path a touch screen takes —
    // and the panel's whole reason for using Pointer Events is that HTML5 drag
    // and drop never fires on touch.
    {
      await settingsGroup(page, 'sharing')
      const nameField = page.locator('[data-testid="share-name"]')
      if ((await nameField.count()) === 0) {
        note('FAIL', 'board', 'the settings page offers no way to make a share link')
      } else {
        await nameField.fill('render-check wall')
        await page.locator('[data-testid="share-create"]').click()
        let row = false
        for (let i = 0; i < 20; i++) {
          if ((await page.locator('[data-testid="share-edit"]').count()) > 0) { row = true; break }
          await sleep(300)
        }
        if (!row) {
          note('FAIL', 'board', 'creating a share link produced no row to edit')
        } else {
          await page.locator('[data-testid="share-edit"]').first().click()
          const canvas = page.locator('[data-testid="board-canvas"]')
          const shown = await canvas.isVisible().catch(() => false)
          if (!shown) {
            note('FAIL', 'board', 'editing a link showed no canvas to arrange')
          } else {
            const before = await page.locator('[data-slot-index]').count()
            if (before === 0) {
              note('FAIL', 'board', 'the canvas drew no widgets for a board that has some')
            }

            // Drag one entry out of the palette and drop it on the canvas.
            const item = page.locator('[data-testid="palette-item"]').first()
            await page.locator('[data-testid="board-editor"]').scrollIntoViewIfNeeded()
            await sleep(300)
            const from = await item.boundingBox()
            const onto = await canvas.boundingBox()
            const view = page.viewportSize()
            const inView = (b) =>
              b && b.y >= 0 && b.y + b.height <= view.height && b.x + b.width <= view.width
            if (!from || !onto) {
              note('FAIL', 'board', 'the palette or the canvas has no box to drag between')
            } else if (!inView(from) || !inView(onto)) {
              // A drag needs both ends on screen at once. If the settings dialog
              // has put one of them off the fold this says so rather than
              // reporting a failure the layout caused.
              note('WARN', 'board',
                'the palette and the canvas are not both in the viewport, so the drag was ' +
                'not exercised')
            } else {
              await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2)
              await page.mouse.down()
              // Two moves: the first crosses the threshold, the second is what
              // decides the landing place. One move is a gesture the threshold
              // is allowed to swallow.
              await page.mouse.move(onto.x + onto.width / 2, onto.y + onto.height / 4, { steps: 8 })
              await page.mouse.move(onto.x + onto.width / 3, onto.y + onto.height / 3, { steps: 8 })
              const marker = await page
                .locator('[data-testid="canvas-marker"][data-on="true"]').count()
              if (marker === 0) {
                note('FAIL', 'board',
                  'nothing was drawn where the widget would land; a drop target you cannot ' +
                  'see is a guessing game')
              }
              await page.mouse.up()
              await sleep(600)
              const after = await page.locator('[data-slot-index]').count()
              if (after !== before + 1) {
                note('FAIL', 'board',
                  `dragging a widget out of the palette left ${after} on the canvas, was ${before}`)
              }
            }

            // Select a tile and resize it with the keyboard. The canvas is the
            // only way to arrange a board now, so a pointer-only canvas would
            // have taken the feature away from anybody who cannot use one.
            const grab = page.locator('[data-testid="canvas-grab"]').first()
            await grab.click()
            const spanSelect = page.locator('[data-testid="widget-span"]')
            if ((await spanSelect.count()) === 0) {
              note('FAIL', 'board', 'selecting a tile opened no inspector for it')
            } else {
              const wide = await spanSelect.inputValue()
              await grab.press('Shift+ArrowLeft')
              await sleep(400)
              if ((await spanSelect.inputValue()) === wide) {
                note('FAIL', 'board',
                  `shift-arrow did not resize the selected widget; it is still ${wide}/12`)
              }
            }

            // Escape gets you out of a drag from anywhere. A drag you can only
            // leave by finding a neutral place to release is one people abandon
            // by dropping the tile somewhere they did not want it.
            const held = await page.locator('[data-slot-index]').count()
            const item2 = page.locator('[data-testid="palette-item"]').nth(1)
            const box2 = await item2.boundingBox()
            const onto2 = await canvas.boundingBox()
            if (box2 && onto2 && inView(box2) && inView(onto2)) {
              const onto = onto2
              await page.mouse.move(box2.x + box2.width / 2, box2.y + box2.height / 2)
              await page.mouse.down()
              await page.mouse.move(onto.x + onto.width / 2, onto.y + onto.height / 2, { steps: 8 })
              await page.keyboard.press('Escape')
              await page.mouse.up()
              await sleep(500)
              if ((await page.locator('[data-slot-index]').count()) !== held) {
                note('FAIL', 'board', 'Escape did not cancel a drag that was in progress')
              }
            }

            // The template gallery is picked by looking at it.
            const thumbs = await page.locator('[data-testid="gallery-thumb"]').count()
            if (thumbs < 8) {
              note('FAIL', 'board',
                `the template gallery drew ${thumbs} thumbnails; choosing a board by name out ` +
                'of a dropdown is what this replaced')
            }
            // The edit is live: the wall follows without a save button.
            let saved = false
            for (let i = 0; i < 20; i++) {
              const said = await page.locator('[data-testid="share-edit-status"]').innerText()
                .catch(() => '')
              if (said) { saved = true; break }
              await sleep(300)
            }
            if (!saved) {
              note('FAIL', 'board', 'the editor never said whether the change had reached the panel')
            }
            // Every control in this row says what it sets, and says it on
            // screen.
            //
            // The row wraps to one control per line at the width the settings
            // panel gives it, and the two selects carried their names in
            // `title` only. What arrived was "Fill the screen" with "Normal"
            // and "Never" under it: a heading and two dropdowns about nothing.
            // A tooltip is not a label -- it needs a pointer to exist, and a
            // phone has none.
            for (const [id, want] of [['board-density', 'Density'], ['board-rotate', 'Rotate pages']]) {
              const sel = page.locator(`[data-testid="${id}"]`)
              const labelled = await sel.evaluate((el) => {
                const lab = el.closest('label')
                return lab ? lab.innerText.trim() : ''
              }).catch(() => '')
              if (!labelled.includes(want)) {
                note('FAIL', 'board', `${id} is not labelled on screen; the row reads "${labelled}"`)
              }
            }

            // And fill is a checkbox, so its state is a shape (red line 4).
            // It was a transparent `.vp-control` whose entire on-state was the
            // text turning accent-coloured, with nothing else carrying it.
            const fill = page.locator('[data-testid="board-fill"]')
            if ((await fill.getAttribute('type')) !== 'checkbox') {
              note('FAIL', 'board', 'fill does not carry its state as a shape; colour was doing it alone')
            } else {
              const was = await fill.isChecked()
              await fill.click()
              await sleep(200)
              if ((await fill.isChecked()) === was) {
                note('FAIL', 'board', 'the fill checkbox did not change when clicked')
              } else {
                note('PASS', 'board', `fill toggles and is a checkbox (${was} -> ${!was})`)
                await fill.click()
                await sleep(200)
              }
            }

            await page.screenshot({ path: join(SHOTS, 'board-editor.png') })
            await page.locator('[data-testid="share-edit-cancel"]').click()
            await sleep(300)
          }
        }
      }
    }

    // Naming the passkey is the panel's own field now, not window.prompt. The
    // dialog is asked for from inside the settings modal, so this also covers
    // it being drawn above one.
    await settingsGroup(page, 'account')
    await page.locator('[data-testid="passkey-add"]').click()
    const named = await page
      .waitForSelector('[data-testid="confirm-field"]', { timeout: 5000 })
      .then(() => true)
      .catch(() => false)
    if (!named) {
      note('FAIL', 'passkey', 'adding a passkey asked for no name')
    } else {
      await page.locator('[data-testid="confirm-field"]').fill('Virtual key')
      await page.locator('[data-testid="confirm-yes"]').click()
    }
    let registered = false
    for (let i = 0; i < 30; i++) {
      if ((await page.locator('[data-testid="passkey-row"]').count()) > 0) { registered = true; break }
      await sleep(400)
    }
    if (!registered) {
      note('FAIL', 'passkey', 'registering a passkey produced no entry')
    } else {
      // Now the part that matters: signing in with it and no password.
      await page.locator('[data-testid="settings-close"]').click()
      await sleep(400)
      await page.locator('[data-testid="sign-out"]').click()
      await page.waitForSelector('[data-testid="login-form"]', { timeout: 10000 })
        .catch(() => note('FAIL', 'passkey', 'signing out did not return to the sign-in screen'))
      await sleep(600)

      const passkeyButton = page.locator('[data-testid="passkey-signin"]')
      if (!(await passkeyButton.isVisible().catch(() => false))) {
        note('FAIL', 'passkey', 'the sign-in screen offers no passkey option')
      } else {
        await passkeyButton.click()
        const back = await page
          .waitForSelector('[data-testid="sidebar"], [data-testid="sidebar-rail"]', { timeout: 15000 })
          .then(() => true)
          .catch(() => false)
        if (!back) {
          const err = await page.locator('[data-testid="auth-error"]').innerText().catch(() => '')
          note('FAIL', 'passkey', `signing in with a passkey failed: ${JSON.stringify(err)}`)
        }
        await page.screenshot({ path: join(SHOTS, 'passkey.png') })
      }
    }
  }

} catch (e) {
  note('FAIL', 'harness', String(e))
} finally {
  for (const e of [...new Set(pageErrors)]) note('FAIL', 'js', `uncaught: ${e}`)
  await cleanup()
}

const order = { FAIL: 0, WARN: 1, INFO: 2 }
findings.sort((a, b) => order[a.sev] - order[b.sev])
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`\n=== render check: ${fails} FAIL, ${findings.filter(f => f.sev === 'WARN').length} WARN ===`)
for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
console.log(`\nscreenshots: ${SHOTS}`)
// Flush before exiting, and then exit deliberately.
//
// Node's stdout is asynchronous when it is a pipe — which it is whenever this
// runs under make, CI, or anything capturing the output — and process.exit()
// abandons whatever has not been flushed. The findings and the verdict are the
// last thing printed and therefore the first thing lost: three runs of the
// scale check in a row produced a different amount of output each time, one of
// them stopping mid-way with no verdict at all, and the missing lines were
// read as the run having crashed.
//
// Setting only process.exitCode would flush, but it also waits for the event
// loop to drain, and one stray handle from a browser or a child process would
// hang the check instead of ending it. Flush, then exit.
await new Promise((resolve) => process.stdout.write('', resolve))
process.exit(fails > 0 ? 1 : 0)
