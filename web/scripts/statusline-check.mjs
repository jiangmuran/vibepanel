// The status-line regression on its own, without the platform-dependent checks
// in render-check. Both entry points execute the same fixture and assertions.
import { chromium } from 'playwright'
import { spawn, execFileSync } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { assertFreshBuild } from './lib/fresh.mjs'
import { checkStatusline } from './lib/statusline.mjs'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
assertFreshBuild(BIN, new URL('../../', import.meta.url).pathname)
const DATA = mkdtempSync(join(tmpdir(), 'vpstatusline-'))
const HOME_DIR = join(DATA, 'home')
const SHOTS = process.argv[3] ?? join(DATA, 'shots')
mkdirSync(HOME_DIR, { recursive: true })
mkdirSync(SHOTS, { recursive: true })
// The existing check prefix is on the stale-socket sweeper's allowlist.
const SOCKET = `vpcheck-${process.pid}`
const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const BASE = `http://localhost:${PORT}`
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
const findings = []
const note = (severity, area, message) => findings.push(`[${severity}] ${area}: ${message}`)
let browser
let cookie = ''
let cleaned = false
let serverLog = ''
const server = spawn(BIN, ['serve'], {
  env: {
    ...process.env,
    HOME: HOME_DIR,
    VIBEPANEL_DATA_DIR: join(DATA, 'data'),
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
    VIBEPANEL_DOMAIN: 'localhost',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})
server.stdout.on('data', data => { serverLog += data })
server.stderr.on('data', data => { serverLog += data })
server.on('error', error => { serverLog += String(error) })

async function cleanup() {
  if (cleaned) return
  cleaned = true
  try { await browser?.close() } catch { /* already closed */ }
  server.kill('SIGTERM')
  await sleep(400)
  try { execFileSync('tmux', ['-L', SOCKET, 'kill-server'], { stdio: 'ignore' }) } catch { /* already gone */ }
  rmSync(join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid()}`, SOCKET), { force: true })
  // Keep screenshots outside the disposable server data so failures can be read.
  for (const dir of [join(DATA, 'data'), HOME_DIR]) rmSync(dir, { recursive: true, force: true })
}
for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(signal, () => void cleanup().finally(() => process.exit(130)))
}

async function authed(path, init = {}) {
  const response = await fetch(BASE + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}), ...init.headers },
  })
  if (!response.ok) throw new Error(`${init.method ?? 'GET'} ${path}: ${response.status} ${await response.text()}`)
  return response
}

try {
  let healthy = false
  for (let i = 0; i < 100; i++) {
    try { healthy = (await fetch(BASE + '/api/health')).ok } catch { /* starting */ }
    if (healthy) break
    await sleep(200)
  }
  if (!healthy) throw new Error(`server did not become healthy: ${serverLog}`)
  const token = serverLog.match(/one-time setup token:\s*\n\s*\n\s*(\S+)/)?.[1]
  if (!token) throw new Error(`no setup token: ${serverLog}`)
  const setup = await authed('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token, username: 'statusline-check', password: 'a sufficiently long test password' }),
  })
  cookie = setup.headers.getSetCookie().map(value => value.split(';')[0]).join('; ')
  if (!cookie) throw new Error('setup returned no session cookie')
  await authed('/api/settings/tour', { method: 'POST' })
  const project = await (await authed('/api/projects', {
    method: 'POST', body: JSON.stringify({ path: DATA, name: 'statusline-check' }),
  })).json()
  const mkSession = async (command, title) => (await authed('/api/sessions', {
    method: 'POST', body: JSON.stringify({ projectId: project.id, command, title }),
  })).json()
  const away = await mkSession(['sh', '-c', 'exec sleep 300'], 'statusline-away')
  browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ serviceWorkers: 'block', viewport: { width: 1440, height: 900 } })
  await ctx.addCookies(cookie.split('; ').map(value => {
    const at = value.indexOf('=')
    return { name: value.slice(0, at), value: value.slice(at + 1), url: BASE }
  }))
  const page = await ctx.newPage()
  page.on('pageerror', error => note('FAIL', 'js', String(error)))
  await page.goto(BASE, { waitUntil: 'networkidle' })
  await page.locator(`[data-testid="session-row"][data-session-id="${away.id}"]`).click()
  await page.locator('[data-testid="main-terminal"]:visible').waitFor()
  await checkStatusline({ browser, ctx, page, base: BASE, mkSession, authed, shots: SHOTS, note })
} catch (error) {
  note('FAIL', 'statusline', error.stack ?? String(error))
} finally {
  await cleanup()
}

for (const finding of findings) console.log(finding)
console.log(`screenshots: ${SHOTS}`)
const failed = findings.some(finding => finding.startsWith('[FAIL]'))
await new Promise(resolve => process.stdout.write('', resolve))
process.exit(failed ? 1 : 0)
