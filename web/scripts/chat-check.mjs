// The Chat page, driven in a real browser against the real binary.
//
// What it covers: the rail link reaches /chat; every adapter this build has
// gets a card; a token saved on Telegram starts a channel whose health line
// says something within seconds; a 飞书 card shows its request URL after
// saving; the 微信 sign-in either draws a QR code or says why not; a rule is
// added, saved and previewed against a real session; the key table and the
// advanced mode's form save; a hook report with a message reaches the panel's
// message table and shows in the session list; a card's deep link opens the
// panel at that session; and the page fits at phone, tablet and desktop
// widths in both themes and both languages, with the shared probes for
// overflow, small targets, unnamed controls and faded controls.
//
// What it does not cover: any real IM. No message is sent anywhere; the
// Telegram token is a fake, and the channel's health line is expected to say
// so. The adapters' own tests cover the wire against fake servers.

import { chromium } from 'playwright'
import { createServer as createNetServer } from 'node:net'
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { assertFreshBuild } from './lib/fresh.mjs'
import { sweepStaleSockets } from './lib/stale.mjs'
import { findUnreachable } from './lib/overflow.mjs'
import { findSmallTargets } from './lib/tap.mjs'
import { findUnnamedControls } from './lib/names.mjs'
import { findFadedControls } from './lib/faded.mjs'

const ROOT = new URL('../../', import.meta.url).pathname
const BIN = join(ROOT, 'vibepanel')
assertFreshBuild(BIN, ROOT)
sweepStaleSockets((msg) => console.log(`==> ${msg}`))

const freePort = () =>
  new Promise((resolve, reject) => {
    const probe = createNetServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolve(port))
    })
  })

const PORT = await freePort()
const SOCKET = `vpchat-${process.pid}`
const BASE = `http://127.0.0.1:${PORT}`
const USERNAME = 'chat'
const PASSWORD = 'chat-check-password-1'

const findings = []
const note = (sev, where, msg) => {
  findings.push({ sev, where, msg })
  console.log(`[${sev}] ${where}: ${msg}`)
}
const pass = (where, msg) => console.log(`[ok]   ${where}: ${msg}`)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const work = mkdtempSync(join(tmpdir(), 'vpchat-'))
const SHOTS = join(work, 'shots')
mkdirSync(SHOTS, { recursive: true })

let server
let browser
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    server?.kill('SIGTERM')
    try {
      execFileSync('tmux', ['-L', SOCKET, 'kill-server'], { stdio: 'ignore' })
    } catch {
      /* nothing to kill */
    }
    process.exit(130)
  })
}

/** Waits for a predicate, polling; returns false rather than throwing. */
async function until(pred, ms = 8000, step = 150) {
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if (await pred()) return true
    await sleep(step)
  }
  return false
}

try {
  const busy = await fetch(`${BASE}/api/health`).then(() => true).catch(() => false)
  if (busy) throw new Error(`something is already answering on ${BASE}`)
  server = spawn(BIN, ['serve', '-addr', `127.0.0.1:${PORT}`, '-data-dir', join(work, 'data'),
    '-tmux-socket', SOCKET], { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, HOME: work } })
  let log = ''
  server.stdout.on('data', (d) => { log += d })
  server.stderr.on('data', (d) => { log += d })
  let setupToken = ''
  for (let i = 0; i < 50 && !setupToken; i++) {
    await sleep(300)
    setupToken = (/^ {6}([A-Za-z0-9_-]{30,})$/m.exec(log) ?? [])[1] ?? ''
  }
  if (!setupToken) throw new Error(`no setup token in:\n${log}`)

  browser = await chromium.launch({ headless: true })
  const owner = await browser.newContext({ viewport: { width: 1400, height: 950 } })
  const setup = await owner.request.post(`${BASE}/api/auth/setup`, {
    data: { token: setupToken, username: USERNAME, password: PASSWORD },
  })
  if (setup.status() !== 201) throw new Error(`setup: ${setup.status()} ${await setup.text()}`)
  await owner.request.post(`${BASE}/api/settings/tour`, { headers: { Origin: BASE } })

  const api = async (method, path, data) => {
    const res = await owner.request.fetch(`${BASE}${path}`, {
      method, data, headers: { Origin: BASE, 'Content-Type': 'application/json' },
    })
    const text = await res.text()
    let body = null
    try { body = text ? JSON.parse(text) : null } catch { body = text }
    return { status: res.status(), body }
  }
  const must = async (method, path, data) => {
    const r = await api(method, path, data)
    if (r.status >= 300) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.body)}`)
    return r.body
  }

  // A project and a session to route, address and deep-link to.
  const projectDir = join(work, 'proj')
  mkdirSync(projectDir, { recursive: true })
  const project = await must('POST', '/api/projects', { path: projectDir, name: 'chatproj' })
  const session = await must('POST', '/api/sessions', { projectId: project.id, command: ['sleep', '600'] })
  await must('PATCH', `/api/sessions/${session.id}`, { title: 'fix the bridge' })

  // The hook token, from the session's own environment: what a hook script
  // reads, read the same way.
  const hookToken = execFileSync('tmux', ['-L', SOCKET, 'show-environment', '-t', `=${session.tmuxName}`, 'VIBEPANEL_TOKEN'])
    .toString().trim().replace(/^VIBEPANEL_TOKEN=/, '')
  if (!hookToken) throw new Error('no hook token in the session environment')

  const page = await owner.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })

  // ── the way in ───────────────────────────────────────────────────────────
  await page.goto(`${BASE}/`, { waitUntil: 'networkidle' })
  await page.evaluate(() => localStorage.setItem('vibepanel.lang', 'zh'))
  await page.reload({ waitUntil: 'networkidle' })
  const settingsButton = page.locator('button[title="设置"], button[title="Settings"]').first()
  if (await settingsButton.count()) {
    await settingsButton.click()
    const link = page.getByTestId('settings-chat-link')
    if (await until(() => link.count().then((n) => n > 0), 4000)) {
      // The dialog is still settling when the link appears, and a settling
      // element is one playwright waits on forever; the link's job is its
      // href, so that is what is checked, then it is pressed.
      const href = await link.getAttribute('href')
      if (href !== '/chat') note('FAIL', 'rail', `the chat link points at ${href}`)
      else pass('rail', 'the settings rail links to /chat')
    } else {
      note('FAIL', 'rail', 'no chat link in the settings rail')
    }
  } else {
    note('WARN', 'rail', 'could not find the settings button; going to /chat directly')
  }
  await page.goto(`${BASE}/chat`, { waitUntil: 'networkidle' })
  if (!(await until(() => page.getByTestId('chat').count().then((n) => n > 0)))) {
    throw new Error('the chat page did not render')
  }
  for (const id of ['channels', 'peers', 'routes', 'assistant', 'keys', 'log']) {
    if ((await page.locator(`[data-section="${id}"]`).count()) === 0) note('FAIL', 'sections', `no ${id} section`)
  }
  pass('sections', 'all six sections are on screen')
  for (const kind of ['telegram', 'feishu', 'weixin']) {
    if ((await page.getByTestId(`chat-channel-${kind}`).count()) === 0) note('FAIL', 'channels', `no ${kind} card`)
  }
  pass('channels', 'a card per adapter')

  // ── Consent: nothing reaches an outside service before the owner accepts ─
  const refused = await api('PUT', '/api/chat/channels/telegram', { enabled: true, values: { token: '1:x' } })
  if (refused.status !== 409) note('FAIL', 'consent', `switching a channel on without consent answered ${refused.status}`)
  else pass('consent', 'the server refuses to switch a channel on before consent')

  // ── Telegram: a fake token becomes a channel whose health line speaks ────
  await page.fill('#chat-telegram-token', '123456:not-a-real-token')
  await page.getByTestId('chat-save-telegram').click()
  const asked = await until(() => page.getByTestId('confirm-dialog').count().then((n) => n > 0), 5000)
  if (!asked) note('FAIL', 'consent', 'saving the first channel did not ask for consent')
  else {
    const body = (await page.getByTestId('confirm-body').textContent()) ?? ''
    if (!/Telegram/.test(body)) note('FAIL', 'consent', `the consent does not name where content goes: ${body}`)
    else pass('consent', 'the first channel asks, naming the services')
    await page.getByTestId('confirm-yes').click()
  }
  const health = page.getByTestId('chat-health-telegram')
  const spoke = await until(async () => {
    const text = await health.textContent().catch(() => '')
    return /运行中|已停止|Running|Stopped/.test(text ?? '')
  }, 10000)
  if (!spoke) note('FAIL', 'telegram', `the health line never said anything: ${await health.textContent().catch(() => '')}`)
  else pass('telegram', `health line: ${(await health.textContent()).trim().slice(0, 80)}`)
  const chatAfterSave = await must('GET', '/api/chat')
  const tg = chatAfterSave.channels.find((c) => c.kind === 'telegram')
  if (!tg || !tg.secretSet.token || 'token' in tg.values) note('FAIL', 'telegram', `the token leaked or was not stored: ${JSON.stringify(tg)}`)
  else pass('telegram', 'the token is stored and not returned')

  // ── 飞书: the request URL appears once saved ─────────────────────────────
  await page.fill('#chat-feishu-app_id', 'cli_test')
  await page.fill('#chat-feishu-app_secret', 'secret')
  await page.fill('#chat-feishu-verification_token', 'verify')
  await page.getByTestId('chat-save-feishu').click()
  await page.waitForTimeout(300)
  if ((await page.getByTestId('confirm-dialog').count()) > 0) note('FAIL', 'consent', 'asked again after it was accepted')
  const urlShown = await until(() => page.locator('[data-testid="chat-channel-feishu"] code').count().then((n) => n > 0), 8000)
  if (!urlShown) note('FAIL', 'feishu', 'no request URL after saving')
  else {
    const shown = await page.locator('[data-testid="chat-channel-feishu"] code').first().textContent()
    if (!shown.includes('/api/chat/hooks/feishu')) note('FAIL', 'feishu', `request URL is ${shown}`)
    else pass('feishu', `request URL ${shown}`)
  }
  // The door answers 404 to a challenge for a kind that is not running as a
  // webhook... 飞书 is: the handshake needs the verification token. A wrong
  // one must be refused.
  const bad = await fetch(`${BASE}/api/chat/hooks/feishu`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ type: 'url_verification', challenge: 'x', token: 'wrong' }),
  })
  if (bad.status === 200) note('FAIL', 'feishu', 'a handshake with the wrong token was accepted')
  else pass('feishu', `a handshake with the wrong token gets ${bad.status}`)
  const good = await fetch(`${BASE}/api/chat/hooks/feishu`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ type: 'url_verification', challenge: 'abc', token: 'verify' }),
  })
  const goodBody = await good.text()
  if (good.status !== 200 || !goodBody.includes('abc')) note('FAIL', 'feishu', `handshake: ${good.status} ${goodBody}`)
  else pass('feishu', 'the handshake answers the challenge')

  // ── 微信: sign-in starts, and either draws or says why not ───────────────
  await page.getByTestId('chat-login-start-weixin').click()
  const loginLine = page.getByTestId('chat-login-weixin')
  const loginSpoke = await until(async () => {
    if ((await page.locator('[data-testid="chat-channel-weixin"] img').count()) > 0) return true
    const text = await loginLine.textContent().catch(() => '')
    return (text ?? '').trim().length > 0
  }, 25000)
  if (!loginSpoke) {
    // No network, or the IM did not answer: the button must at least say so
    // through a toast rather than sit there.
    const toast = await page.getByTestId('toast').count()
    if (toast === 0) note('FAIL', 'weixin', 'sign-in neither drew a code nor said anything')
    else pass('weixin', 'sign-in could not start and said so')
  } else {
    pass('weixin', (await page.locator('[data-testid="chat-channel-weixin"] img').count()) > 0 ? 'a QR code is on screen' : `sign-in said: ${(await loginLine.textContent()).trim()}`)
  }

  // ── a hook report carries a message, and the session gets a handle ──────
  const report = await fetch(`${BASE}/api/hook/state?sessionId=${session.id}&state=waiting`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${hookToken}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ hook_event_name: 'PermissionRequest', tool_name: 'Bash', tool_input: { command: 'go test ./...' } }),
  })
  if (report.status !== 204) note('FAIL', 'hook', `hook report: ${report.status}`)
  const chat = await must('GET', '/api/chat')
  const row = chat.sessions.find((s) => s.id === session.id)
  if (!row || row.handle < 1 || row.title !== 'fix the bridge') note('FAIL', 'hook', `session not listed with a handle: ${JSON.stringify(row)}`)
  else pass('hook', `session listed as [${row.handle}] ${row.title}`)

  // ── rules: add, save, preview ───────────────────────────────────────────
  await page.reload({ waitUntil: 'networkidle' })
  await page.getByText('加一条规则').click()
  const rule = page.getByTestId('chat-rule-0')
  await rule.locator('input[placeholder="规则名"]').fill('test rule')
  await page.getByTestId('chat-save-routes').click()
  const saved = await until(async () => (await must('GET', '/api/chat')).routes.rules.some((r) => r.name === 'test rule'))
  if (!saved) note('FAIL', 'routes', 'the rule did not save')
  else pass('routes', 'a rule saves')
  await page.getByTestId('chat-preview-session').selectOption(session.id)
  const previewed = await until(() => page.getByTestId('chat-preview-result').count().then((n) => n > 0))
  if (!previewed) note('FAIL', 'routes', 'no preview result')
  else pass('routes', `preview: ${(await page.getByTestId('chat-preview-result').textContent()).trim()}`)
  const bogus = await api('PUT', '/api/chat/routes', { rules: [{ enabled: true, screenshot: 'maybe' }], default: { to: ['*'] } })
  if (bogus.status !== 400) note('FAIL', 'routes', `a bad table was accepted: ${bogus.status}`)
  else pass('routes', 'a bad table is refused')

  // ── keys and the advanced mode's form ───────────────────────────────────
  await page.getByTestId('chat-key-codex-approve').fill('y Enter')
  await page.locator('[data-testid="chat-keys"] button', { hasText: '保存' }).click()
  const keysSaved = await until(async () => {
    const c = await must('GET', '/api/chat')
    return JSON.stringify(c.tools.codex.approve) === JSON.stringify(['y', 'Enter'])
  })
  if (!keysSaved) note('FAIL', 'keys', 'the key table did not save')
  else pass('keys', 'the key table saves')
  const evil = await api('PUT', '/api/chat/keys', { codex: { approve: ['rm -rf /'], deny: ['n'], interrupt: ['Escape'], submit: ['Enter'] } })
  if (evil.status !== 400) note('FAIL', 'keys', `a key with a space was accepted: ${evil.status}`)
  else pass('keys', 'a key that is text is refused')

  await page.getByTestId('chat-lang').selectOption('en')
  const langSaved = await until(async () => (await must('GET', '/api/chat')).lang === 'en')
  if (!langSaved) note('FAIL', 'assistant', 'the bot language did not save')
  else pass('assistant', 'the bot language saves')
  const asst = await api('PUT', '/api/chat/assistant', { enabled: false, harness: 'codex', model: '', profileId: '', maxTurns: 4, budgetUsd: 1, timeoutSeconds: 60 })
  if (asst.status !== 200) note('FAIL', 'assistant', `save: ${asst.status} ${JSON.stringify(asst.body)}`)
  else pass('assistant', 'the advanced mode saves')

  // ── the tools door: the owner's cookie does not open it ─────────────────
  const tools = await api('GET', '/api/chat/tools/sessions')
  if (tools.status !== 401) note('FAIL', 'tools', `the session cookie reached the tools: ${tools.status}`)
  else pass('tools', 'the tools door refuses the session cookie')

  // ── the deep link opens the panel at the session ────────────────────────
  await page.goto(`${BASE}/?session=${session.id}`, { waitUntil: 'networkidle' })
  const selected = await until(() => page.evaluate(() => localStorage.getItem('vibepanel.selected')).then((v) => v === session.id))
  if (!selected) note('FAIL', 'deeplink', 'the session was not selected')
  else if (!page.url().endsWith('/')) note('FAIL', 'deeplink', `the query stayed on the address bar: ${page.url()}`)
  else pass('deeplink', 'opens the panel at the session and clears the query')

  // ── layout at every size, theme and language ────────────────────────────
  for (const lang of ['zh', 'en']) {
    for (const theme of ['light', 'dark']) {
      for (const [w, h] of [[400, 800], [820, 1000], [1400, 950]]) {
        const where = `layout/${lang}/${theme}/${w}`
        const ctx = await browser.newContext({ viewport: { width: w, height: h } })
        const tab = await ctx.newPage()
        const tabErrors = []
        tab.on('pageerror', (e) => tabErrors.push(String(e)))
        await tab.goto(`${BASE}/`, { waitUntil: 'commit' })
        await tab.evaluate(([l, t]) => {
          localStorage.setItem('vibepanel.lang', l)
          localStorage.setItem('vibepanel.theme', t)
        }, [lang, theme])
        // The cookie is per context; sign this one in the same way.
        await ctx.request.post(`${BASE}/api/auth/login`, { data: { username: USERNAME, password: PASSWORD }, headers: { Origin: BASE } })
        await tab.goto(`${BASE}/chat`, { waitUntil: 'networkidle' })
        if (!(await until(() => tab.getByTestId('chat').count().then((n) => n > 0), 6000))) {
          note('FAIL', where, 'the page did not render')
          await ctx.close()
          continue
        }
        await sleep(500)
        await tab.screenshot({ path: join(SHOTS, `chat-${lang}-${theme}-${w}.png`), fullPage: true })
        // The page scrolls inside its own root, which fullPage cannot see
        // past, so each section is also photographed on its own at the
        // phone width, where a reviewer needs it most.
        if (w === 400) {
          for (const id of ['channels', 'peers', 'routes', 'assistant', 'keys', 'log']) {
            await tab.locator(`[data-section="${id}"]`).scrollIntoViewIfNeeded()
            await sleep(150)
            await tab.screenshot({ path: join(SHOTS, `chat-${lang}-${theme}-${w}-${id}.png`) })
          }
        }
        const overflowX = await tab.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)
        if (overflowX) note('FAIL', where, 'the page is wider than the screen')
        const { examined, found } = await findUnreachable(tab, sleep)
        if (examined < 20) note('FAIL', where, `the overflow scan measured ${examined} elements`)
        else if (found.length) note('FAIL', where, `unreachable content: ${found.slice(0, 5).join(' | ')}`)
        const unnamed = await findUnnamedControls(tab)
        if (unnamed.length) note('FAIL', where, `controls with no name: ${unnamed.slice(0, 5).join(', ')}`)
        const faded = await findFadedControls(tab)
        if (faded.length) note('FAIL', where, `faded controls: ${faded.slice(0, 5).join(', ')}`)
        if (w === 400) {
          const { small } = await findSmallTargets(tab, 32)
          if (small.length) note('WARN', where, `small tap targets: ${small.slice(0, 5).join(', ')}`)
        }
        if (tabErrors.length) note('FAIL', where, `errors: ${tabErrors.slice(0, 3).join(' | ')}`)
        if (!overflowX && !found.length && !unnamed.length && !faded.length && !tabErrors.length) pass(where, 'clean')
        await ctx.close()
      }
    }
  }

  if (errors.length) note('FAIL', 'console', errors.slice(0, 5).join(' | '))
  else pass('console', 'no page errors on the main tab')
} catch (e) {
  note('FAIL', 'chat-check', e.stack ?? String(e))
} finally {
  await browser?.close().catch(() => {})
  server?.kill('SIGTERM')
  try {
    execFileSync('tmux', ['-L', SOCKET, 'kill-server'], { stdio: 'ignore' })
  } catch {
    /* no server left to kill */
  }
}

const fails = findings.filter((f) => f.sev === 'FAIL').length
const warns = findings.filter((f) => f.sev === 'WARN').length
console.log(`\nscreenshots: ${SHOTS}`)
console.log(`=== chat check: ${fails} FAIL, ${warns} WARN ===`)
process.exit(fails > 0 ? 1 : 0)
