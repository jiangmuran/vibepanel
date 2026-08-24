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
const DATA = mkdtempSync(join(tmpdir(), 'vprender-'))
const SHOTS = process.argv[3] ?? join(DATA, 'shots')
mkdirSync(SHOTS, { recursive: true })

const findings = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function waitHealth(url, ms = 15000) {
  const end = Date.now() + ms
  while (Date.now() < end) {
    try {
      const r = await fetch(url + '/api/health')
      if (r.ok) return true
    } catch { /* not up yet */ }
    await sleep(150)
  }
  return false
}

// ── relative luminance / contrast, per WCAG ────────────────────────────────
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

const USERNAME = 'render-check'
const PASSWORD = 'a sufficiently long password'
let cookie = ''

/** Fetch carrying the session cookie, for seeding through the API. */
const authed = (path, init = {}) =>
  fetch(BASE + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}), ...(init.headers ?? {}) },
  })

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

  const mkSession = (cmd) =>
    authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId: proj.id, command: cmd }),
    }).then((r) => r.json())

  await mkSession(['sh', '-c', 'echo RENDER_CHECK_MARKER; exec sh'])
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
  const pageErrors = []
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
  for (const e of pageErrors) note('FAIL', 'js', `uncaught: ${e}`)

  // ── structure ────────────────────────────────────────────────────────────
  const sidebarText = await page.locator('[data-testid="sidebar"]').innerText().catch(() => '')
  if (!sidebarText.toLowerCase().includes('render-check')) {
    note('FAIL', 'ui', `sidebar does not list the project; saw: ${JSON.stringify(sidebarText)}`)
  }
  const sessionRows = await page.locator('[data-testid="session-row"]').count()
  if (sessionRows < 2) note('FAIL', 'ui', `expected 2 session rows, found ${sessionRows}`)

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
    note('WARN', 'ui', 'could not find the shell session row to select')
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
    note('WARN', 'arbitration',
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
    if (over > 1) note('WARN', `layout/${label}`, `page scrolls horizontally by ${over}px`)
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
      note('WARN', 'mobile', 'the terminal still accepts direct keystrokes at phone width')
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

    // A key from the bar has to arrive as the byte a terminal expects.
    await compose.fill('printf KEYBAR')
    await page.locator('[data-testid="compose-newline"]').click() // send without Enter
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

    // The keys that matter must be on screen without scrolling. A single
    // scrolling row put y, n and Escape off the left edge, which is exactly
    // the set a phone is there for.
    const primary = await page.locator('[data-testid="key-row-primary"]').boundingBox()
    if (!primary) {
      note('FAIL', 'mobile', 'no primary key row')
    } else {
      for (const label of ['y', 'n', 'esc', 'tab', 'ctrl', 'alt', 'enter']) {
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
    await page.locator('[data-testid="key-c"]').click().catch(() => {})
    await page.locator('[data-testid="key-1"]').click()
    await sleep(400)
    if ((await page.locator('[data-testid="key-ctrl"]').getAttribute('data-active')) !== 'false') {
      note('FAIL', 'mobile', 'ctrl stayed latched after the key it applied to')
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
    }
    await touch.screenshot({ path: join(SHOTS, 'mobile-drawer.png') })

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
  await cleanup()
}

const order = { FAIL: 0, WARN: 1, INFO: 2 }
findings.sort((a, b) => order[a.sev] - order[b.sev])
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`\n=== render check: ${fails} FAIL, ${findings.filter(f => f.sev === 'WARN').length} WARN ===`)
for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
console.log(`\nscreenshots: ${SHOTS}`)
process.exit(fails > 0 ? 1 : 0)
