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
import { spawn, execSync } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdtempSync, rmSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sweepStaleSockets } from './lib/stale.mjs'
import { findUnreachable } from './lib/overflow.mjs'
import { findSmallTargets } from './lib/tap.mjs'
import { findInvisibleFocus } from './lib/focus.mjs'
import { findUnnamedControls } from './lib/names.mjs'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
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

/** note()-reporting wrapper around the tap scan. See lib/tap.mjs. */
async function scanTapTargets(target, where) {
  const small = await findSmallTargets(target)
  if (small.length > 0) {
    note('FAIL', 'mobile',
      `in ${where}, controls are too small for a thumb: ${small.join('; ')}`)
  }
}

/** note()-reporting wrapper around the shared scan. See lib/overflow.mjs. */
async function scanUnreachable(target, where) {
  const found = await findUnreachable(target, sleep)
  if (found.length > 0) {
    note('FAIL', 'ui',
      `in ${where}, content is painted outside its container with no way to scroll to it: ` +
      found.join('; '))
  }
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

// Fetch carrying the session cookie, for seeding through the API.
//
// A 404 throws rather than being returned, because a harness that asks for a
// route the server does not have gets a perfectly ordinary Response and goes
// on to draw conclusions from its body. That happened: a probe polled
// `GET /api/sessions`, which exists only for POST, with `.catch(() => [])` on
// the parse — so every refusal became an empty list, an empty list contained
// no session, and "no session" was the success condition. It reported a
// healthy result in three milliseconds and would have done so against a server
// that was switched off.
//
// 405 as well as 404, and that is not a detail: chi answers a known path with
// the wrong method with 405, so the first version of this guard checked only
// for 404 and did not catch the exact bug it was written for. It was caught by
// testing the guard rather than trusting it.
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
    viewport: { width: 1440, height: 900 },
    permissions: ['clipboard-read', 'clipboard-write'],
  })
  const page = await ctx.newPage()

  const consoleErrors = []
  const failedReqs = []
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text())
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
  const termText = await page.locator('.xterm-screen').innerText().catch(() => '')
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
  let typed = false
  for (let i = 0; i < 40; i++) {
    const t = await page.locator('.xterm-screen').innerText().catch(() => '')
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

  await page.bringToFront()
  await page.locator('.xterm-screen').click()
  await page.keyboard.type('echo SYNC_TO_SECOND_VIEWER')
  await page.keyboard.press('Enter')
  let synced = false
  for (let i = 0; i < 40; i++) {
    const t = await page2.locator('.xterm-screen').innerText().catch(() => '')
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
  const afterReload = await page.locator('.xterm-screen').innerText().catch(() => '')
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
  if (await showRight.isVisible().catch(() => false)) {
    await showRight.click()
    await sleep(600)
  }
  const rightPanel = page.locator('[data-testid="right-panel"]')
  if (!(await rightPanel.isVisible().catch(() => false))) {
    note('FAIL', 'panel', 'the side panel never appeared')
  } else {
    // Every control in the header has to stay reachable. Labelling all four
    // tabs once pushed the collapse button off the edge of a 280px column.
    for (const id of ['files', 'monitor', 'notes', 'todos']) {
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

    // System
    await page.locator('[data-testid="panel-tab-monitor"]').click()
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
      note('FAIL', 'panel/notes',
        `leaving the tab mid-edit threw the edit away; the server still has ${JSON.stringify(flushed)}`)
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
    const leavingShow = leaving.locator('[data-testid="right-show"]')
    if (await leavingShow.isVisible().catch(() => false)) {
      await leavingShow.click()
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

    // Todos
    await page.locator('[data-testid="panel-tab-todos"]').click()
    await sleep(600)
    await page.locator('[data-testid="todo-input"]').fill('ship the panel')
    await page.keyboard.press('Enter')
    let added = false
    for (let i = 0; i < 25; i++) {
      if ((await page.locator('[data-testid="todo-item"]').count()) > 0) { added = true; break }
      await sleep(300)
    }
    if (!added) {
      note('FAIL', 'panel/todos', 'adding an item produced no row')
    } else {
      // Completed items stay on the list; seeing what you just finished is
      // most of the value of ticking it off.
      await page.locator('[data-testid="todo-item"] button').first().click()
      await sleep(1200)
      const remaining = await page.locator('[data-testid="todo-item"]').count()
      if (remaining !== 1) {
        note('FAIL', 'panel/todos', `ticking an item left ${remaining} rows, want it to stay`)
      }
      const done = await page.locator('[data-testid="todo-item"][data-done="true"]').count()
      if (done !== 1) note('FAIL', 'panel/todos', 'the ticked item is not marked done')
    }

    // Notes and todos side by side.
    await page.locator('[data-testid="panel-split"]').click()
    await sleep(800)
    const split = await rightPanel.getAttribute('data-split')
    if (split !== 'true') {
      note('FAIL', 'panel', 'the split control did not show notes and todo together')
    } else {
      const bothVisible =
        (await page.locator('[data-testid="notes"]').isVisible().catch(() => false)) &&
        (await page.locator('[data-testid="todos"]').isVisible().catch(() => false))
      if (!bothVisible) note('FAIL', 'panel', 'split is on but only one of the two is showing')
    }

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
      let echoed = false
      for (let i = 0; i < 40; i++) {
        const txt = await bottomScreen.innerText().catch(() => '')
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
      const backToAuto = await page
        .locator('button[title="Sort by recent activity again"]')
        .isVisible()
        .catch(() => false)
      if (!backToAuto) {
        note('FAIL', 'reorder',
          'after a manual reorder there is no control to return to automatic ordering')
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
  const menu = page.locator('header button[title="Projects"]')
  if (await menu.isVisible().catch(() => false)) {
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
  } else {
    note('WARN', 'layout/drawer', 'no menu button at phone width; the sidebar may be taking the screen')
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
      const txt = await page.locator('.xterm-screen').innerText().catch(() => '')
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
      const txt = await page.locator('.xterm-screen').innerText().catch(() => '')
      if ((txt.match(/KEYBAR/g) ?? []).length >= 2) { keyed = true; break }
      await sleep(300)
    }
    if (!keyed) note('FAIL', 'mobile', 'the Enter key from the bar did not reach the terminal')

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
      const txt = await page.locator('.xterm-screen').innerText().catch(() => '')
      // Once, not twice. The marker is split across a quote in the command so
      // that the shell's echo of the typed line cannot contain it — which is
      // the whole point of writing it that way, and asking for two occurrences
      // made an assertion that could not pass however well the key worked.
      if ((txt.match(new RegExp(backMark, 'g')) ?? []).length >= 1) { interrupted = true; break }
      await sleep(400)
    }
    if (!interrupted) {
      const shown = await page.evaluate(() =>
        [...document.querySelectorAll('.xterm-rows > div')]
          .map((d) => d.textContent ?? '')
          .filter((t) => t.trim())
          .slice(-6),
      )
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
    const doomedCtx = await browser.newContext({ viewport: { width: 1024, height: 768 } })
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
    await scanTapTargets(touch, 'the phone drawer')
    await scanNames(touch, 'the phone drawer')
    await touch.screenshot({ path: join(SHOTS, 'mobile-drawer.png') })

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
      markerBox = await touch.evaluate((needle) => {
        const row = [...document.querySelectorAll('.xterm-rows > div')].find((d) =>
          (d.textContent ?? '').includes(needle),
        )
        if (!row) return null
        const r = row.getBoundingClientRect()
        return { x: r.x, y: r.y, w: r.width, h: r.height }
      }, marker)
      if (markerBox) break
      await sleep(400)
    }
    if (!markerBox) {
      const shown = await touch.evaluate(() =>
        [...document.querySelectorAll('.xterm-rows > div')]
          .map((d) => d.textContent ?? '')
          .filter((t) => t.trim())
          .slice(-6),
      )
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
    await touchCtx.close()
  }

  // ── what a first-time visitor actually sees ──────────────────────────────
  // Everything above runs in a context that has been clicking panels open for
  // several minutes. A new browser has no localStorage, and that is the state
  // every real user starts in — the one state the harness had never measured.
  const freshCtx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const first = await freshCtx.newPage()
  await first.goto(BASE, { waitUntil: 'networkidle' })
  await first.locator('[data-testid="auth-username"]').fill(USERNAME)
  await first.locator('[data-testid="auth-password"]').fill(PASSWORD)
  await first.locator('[data-testid="auth-submit"]').click()
  await first.waitForSelector('[data-testid="sidebar"]', { timeout: 15000 })
  await sleep(2500)
  for (const [id, what] of [
    ['right-panel', 'the files, system monitor, notes and todo panel'],
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
    const plainCtx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
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
  const bCtx = await browser.newContext({ viewport: { width: 1200, height: 900 } })
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
    let typed = ''
    for (let i = 0; i < 25; i++) {
      typed = await page.locator('.xterm-screen').first().innerText().catch(() => '')
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
    await page.screenshot({ path: join(SHOTS, 'file-transfer.png') })
    for (const leftover of ['download-me.txt', 'dropped-note.txt']) {
      rmSync(join(projRoot, leftover), { force: true })
    }
  }

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
    const status = await page.locator('[data-testid="settings-status"]').innerText().catch(() => '')
    for (const want of ['Version', 'Uptime', 'Sessions', 'tmux socket', 'Listening']) {
      if (!status.includes(want)) {
        note('FAIL', 'settings', `status is missing ${want}: ${JSON.stringify(status.slice(0, 200))}`)
      }
    }
    if (/undefined|NaN/.test(status)) {
      note('FAIL', 'settings', `status rendered a broken value: ${JSON.stringify(status)}`)
    }

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

    const audit = await page.locator('[data-testid="settings-audit"]').innerText().catch(() => '')
    if (!audit.includes('login')) {
      note('WARN', 'settings', 'the activity log shows no sign-in')
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

    await page.screenshot({ path: join(SHOTS, 'settings.png') })
    page.once('dialog', (d) => void d.accept('Virtual key'))
    await page.locator('[data-testid="passkey-add"]').click()
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
