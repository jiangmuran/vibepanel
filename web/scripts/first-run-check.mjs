// The first five minutes, in a browser.
//
//   npm run build && (cd .. && go build -o vibepanel ./cmd/vibepanel)
//   npm run check:first-run
//
// Every other harness starts by reaching past this. They complete the setup
// through `POST /api/auth/setup` because they need a cookie to seed with, so
// the one screen a new user cannot avoid — paste the one-time token, choose a
// password — had never been driven in a browser at all. Neither had adding the
// first project, which goes through a `window.prompt` and is the first thing
// anyone does after signing in.
//
// Nothing here was broken when it was first run. That is the point of writing
// it down rather than checking once: this is the screen where a regression
// costs the most and would be noticed the latest.
import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
const SHOTS = process.argv[3] ?? join(tmpdir(), 'vpfirstrun-shots')
mkdirSync(SHOTS, { recursive: true })

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vpfirstrun-${process.pid}`
const DATA = mkdtempSync(join(tmpdir(), 'vpfirstrun-'))
const BASE = `http://127.0.0.1:${PORT}`

const findings = []
const pageErrors = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let serverLog = ''
const server = spawn(BIN, ['serve', '--addr', `127.0.0.1:${PORT}`], {
  env: { ...process.env, VIBEPANEL_DATA_DIR: join(DATA, 'data'), VIBEPANEL_TMUX_SOCKET: SOCKET },
  stdio: ['ignore', 'pipe', 'pipe'],
})
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

let browser
let cleanedUp = false
async function cleanup() {
  if (cleanedUp) return
  cleanedUp = true
  try { await browser?.close() } catch { /* already gone */ }
  server.kill('SIGTERM')
  await sleep(500)
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  try { rmSync(DATA, { recursive: true, force: true }) } catch { /* best effort */ }
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => void cleanup().finally(() => process.exit(130)))
}

try {
  for (let i = 0; i < 120; i++) {
    try { if ((await fetch(BASE + '/api/health')).ok) break } catch { /* not up */ }
    await sleep(150)
  }

  // The token is printed once, at startup, and it is the only way in.
  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(serverLog)?.[1]
  if (!token) {
    note('FAIL', 'setup', `no setup token in the server output; there is no way to sign in:\n${serverLog}`)
    throw new Error('no setup token')
  }

  browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 860 } })
  const page = await ctx.newPage()
  page.on('pageerror', (e) => pageErrors.push(String(e)))
  await page.goto(BASE, { waitUntil: 'domcontentloaded' })
  await sleep(1200)

  const setupForm = page.locator('[data-testid="setup-form"]')
  if (!(await setupForm.isVisible().catch(() => false))) {
    const login = await page.locator('[data-testid="login-form"]').isVisible().catch(() => false)
    note('FAIL', 'setup',
      login
        ? 'a panel with no account offered the sign-in form; there is no account to sign in to'
        : 'a panel with no account showed neither the setup form nor the sign-in form')
    throw new Error('no setup form')
  }
  await page.screenshot({ path: join(SHOTS, 'setup.png') })

  // A password that is too short has to be refused *and explained*, on the
  // screen, without losing the token that was already typed. Silently doing
  // nothing here is a panel that cannot be set up and does not say why.
  await page.locator('[data-testid="setup-token"]').fill(token)
  await page.locator('[data-testid="auth-username"]').fill('firstrun')
  await page.locator('[data-testid="auth-password"]').fill('short')
  await page.locator('[data-testid="auth-submit"]').click()
  await sleep(1200)
  const refusal = await page.locator('[data-testid="auth-error"]').innerText().catch(() => '')
  if (!refusal.trim()) {
    note('FAIL', 'setup', 'a too-short password was refused with nothing on screen to say so')
  } else if (!/\d/.test(refusal)) {
    note('WARN', 'setup',
      `the refusal does not say how long it has to be: ${JSON.stringify(refusal.trim())}`)
  }
  if (!(await setupForm.isVisible().catch(() => false))) {
    note('FAIL', 'setup', 'a refused password left the setup form; there is no way back to it')
  }
  if ((await page.locator('[data-testid="setup-token"]').inputValue().catch(() => '')) !== token) {
    note('FAIL', 'setup',
      'the one-time token was cleared by a failed attempt, and it is printed once')
  }

  await page.locator('[data-testid="auth-password"]').fill('a sufficiently long password')
  await page.locator('[data-testid="auth-submit"]').click()
  const landed = await page.waitForSelector('[data-testid="sidebar"]', { timeout: 20000 })
    .then(() => true).catch(() => false)
  if (!landed) {
    note('FAIL', 'setup', 'completing the wizard did not land in the panel')
    throw new Error('setup did not complete')
  }
  await sleep(1500)

  // An empty panel has to say what to do next.
  const empty = await page.innerText('body')
  if (!/add.*project/i.test(empty)) {
    note('FAIL', 'empty', 'a panel with no projects does not say to add one')
  }
  await page.screenshot({ path: join(SHOTS, 'empty.png') })

  // Adding the first project, through the prompt, which is what a person does.
  let promptText = ''
  page.once('dialog', async (d) => {
    promptText = `${d.message()} | ${d.defaultValue()}`
    await d.accept(join(DATA, 'not-created-yet'))
  })
  await page.locator('button[title="Add project"]').first().click()
  await sleep(1800)
  if (!promptText.includes('|')) {
    note('FAIL', 'project', 'clicking "Add project" asked nothing')
  }
  const afterBad = await page.innerText('body')
  if (!/no such file|does not exist|cannot open/i.test(afterBad)) {
    note('FAIL', 'project',
      'a directory that does not exist was refused with nothing on screen to explain it; ' +
      'the first thing a new user does is accept the suggested path')
  }
  await page.screenshot({ path: join(SHOTS, 'bad-path.png') })

  const real = join(DATA, 'work')
  mkdirSync(real, { recursive: true })
  page.once('dialog', async (d) => { await d.accept(real) })
  await page.locator('button[title="Add project"]').first().click()
  await sleep(2500)
  if ((await page.locator('[data-testid="project-group"]').count()) === 0) {
    note('FAIL', 'project', 'a real directory was accepted but no project appeared in the sidebar')
  }
  if (/no such file|does not exist|cannot open/i.test(await page.innerText('body'))) {
    note('FAIL', 'project', 'the failed attempt is still on screen after a successful one')
  }
  await page.screenshot({ path: join(SHOTS, 'first-project.png') })
} catch (err) {
  note('FAIL', 'harness', String(err?.stack ?? err))
} finally {
  for (const e of [...new Set(pageErrors)]) note('FAIL', 'js', `uncaught: ${e}`)
  await cleanup()
}

const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`\n=== first-run check: ${fails} FAIL, ${findings.length - fails} WARN ===`)
for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
console.log(`\nscreenshots: ${SHOTS}`)
await new Promise((resolve) => process.stdout.write('', resolve))
process.exit(fails > 0 ? 1 : 0)
