// Does the panel actually work over its own TLS?
//
//   node scripts/tls-check.mjs ./vibepanel [screenshot-dir]
//
// Separate from the other checks and slower than all of them, because it waits
// out a certificate reload interval. It exists because every other check runs
// over http on localhost, and the deployment this was built for terminates its
// own TLS on a public hostname. Three things only happen there: the WebSocket
// upgrades to wss, the session cookie carries Secure, and a certificate gets
// replaced under a running server.
import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, rmSync, mkdirSync, copyFileSync, renameSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sweepStaleSockets } from '../web/scripts/lib/stale.mjs'

const BIN = process.argv[2] ?? new URL('../vibepanel', import.meta.url).pathname
const SHOTS = process.argv[3] ?? join(tmpdir(), 'vptls-shots')
mkdirSync(SHOTS, { recursive: true })

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vptls-${process.pid}`

// Before anything else: a run killed with SIGKILL cannot clean up after
// itself, and what it leaves behind is a tmux server holding live sessions.
sweepStaleSockets((msg) => console.log(`==> ${msg}`))
const DATA = mkdtempSync(join(tmpdir(), 'vptls-'))
const HOME = mkdtempSync(join(tmpdir(), 'vptls-home-'))
const CERTS = mkdtempSync(join(tmpdir(), 'vptls-certs-'))

const findings = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/** A self-signed certificate valid for localhost, labelled so it can be told apart. */
const makeCert = (name) => {
  const key = join(CERTS, `${name}.key`)
  const crt = join(CERTS, `${name}.crt`)
  execSync(
    `openssl req -x509 -newkey rsa:2048 -sha256 -days 2 -nodes ` +
      `-keyout ${key} -out ${crt} -subj "/CN=${name}.vibepanel.test" ` +
      `-addext "subjectAltName=DNS:localhost,IP:127.0.0.1"`,
    { stdio: 'ignore' },
  )
  return { key, crt }
}
// Both helpers silence x509 as well as s_client. Only s_client was quiet, so
// the readiness loop below — which calls this until the server answers —
// printed "Could not find certificate from <stdin>" on its first attempt, in
// the middle of a run that passes. A green run that prints an error teaches
// people to stop reading the output.
const servedFingerprint = () => {
  try {
    return execSync(
      `echo | openssl s_client -connect 127.0.0.1:${PORT} -servername localhost 2>/dev/null ` +
        `| openssl x509 -noout -fingerprint -sha256 2>/dev/null`,
      { encoding: 'utf8' },
    ).trim()
  } catch {
    return ''
  }
}
const servedSubject = () => {
  try {
    return execSync(
      `echo | openssl s_client -connect 127.0.0.1:${PORT} -servername localhost 2>/dev/null ` +
        `| openssl x509 -noout -subject 2>/dev/null`,
      { encoding: 'utf8' },
    ).trim()
  } catch {
    return ''
  }
}

const first = makeCert('first')
const second = makeCert('second')
const LIVE_CRT = join(CERTS, 'live.crt')
const LIVE_KEY = join(CERTS, 'live.key')
copyFileSync(first.crt, LIVE_CRT)
copyFileSync(first.key, LIVE_KEY)

let serverLog = ''
const server = spawn(BIN, ['serve'], {
  env: {
    ...process.env,
    HOME,
    VIBEPANEL_DATA_DIR: DATA,
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
    VIBEPANEL_DOMAIN: 'localhost',
    VIBEPANEL_TLS: 'files',
    VIBEPANEL_TLS_CERT: LIVE_CRT,
    VIBEPANEL_TLS_KEY: LIVE_KEY,
  },
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
  await sleep(400)
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  for (const dir of [DATA, HOME, CERTS]) {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ }
  }
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => void cleanup().finally(() => process.exit(130)))
}

const BASE = `https://localhost:${PORT}`
const PASSWORD = 'a sufficiently long password'
const USERNAME = 'tls'

try {
  let up = false
  for (let i = 0; i < 120; i++) {
    if (servedFingerprint()) { up = true; break }
    await sleep(200)
  }
  if (!up) throw new Error(`nothing answered a TLS handshake:\n${serverLog}`)

  const startSubject = servedSubject()
  if (!startSubject.includes('first.vibepanel.test')) {
    note('FAIL', 'tls', `the server is not serving the configured certificate: ${startSubject}`)
  }

  // A plain http request to the https port must not hang, and must not serve
  // anything. Go's own answer is a fixed 400 saying the client spoke HTTP to
  // an HTTPS server, which is right: it cannot redirect, because a redirect
  // would have to come after a handshake that is never going to happen. What
  // matters is that no application response comes back over the wire.
  try {
    const res = await fetch(`http://localhost:${PORT}/api/health`, {
      signal: AbortSignal.timeout(4000),
    })
    const body = await res.text()
    if (res.ok) {
      note('FAIL', 'tls', `the TLS port served ${res.status} to a plaintext request: ${body.slice(0, 80)}`)
    } else if (!/HTTPS|TLS|https/i.test(body)) {
      note('WARN', 'tls',
        `a plaintext request to the TLS port got ${res.status} with a body that does not explain ` +
        `why: ${JSON.stringify(body.slice(0, 80))}`)
    }
  } catch (e) {
    // Refused or reset is fine too; hanging is not, which the timeout catches.
    if (/timeout|abort/i.test(String(e))) {
      note('FAIL', 'tls', 'a plaintext request to the TLS port hung until the timeout')
    }
  }

  browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({
    viewport: { width: 1280, height: 860 },
    ignoreHTTPSErrors: true,
  })
  const page = await ctx.newPage()
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(String(e)))

  await page.goto(BASE, { waitUntil: 'networkidle' })
  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(serverLog)?.[1]
  if (!token) throw new Error(`no setup token:\n${serverLog}`)
  await page.locator('[data-testid="setup-token"]').fill(token)
  await page.locator('[data-testid="auth-username"]').fill(USERNAME)
  await page.locator('[data-testid="auth-password"]').fill(PASSWORD)
  await page.locator('[data-testid="auth-submit"]').click()
  await page.waitForSelector('[data-testid="sidebar"]', { timeout: 20000 })

  // Secure on the cookie is the reason for doing any of this over TLS: a
  // session that opens a terminal must not be sent in the clear.
  const cookies = await ctx.cookies()
  const session = cookies.find((c) => c.value && c.name.toLowerCase().includes('session'))
  if (!session) {
    note('FAIL', 'cookie', `no session cookie was set: ${cookies.map((c) => c.name).join(', ')}`)
  } else {
    if (!session.secure) note('FAIL', 'cookie', 'the session cookie is not Secure over TLS')
    if (!session.httpOnly) note('FAIL', 'cookie', 'the session cookie is readable by script')
    if (session.sameSite !== 'Strict') {
      note('FAIL', 'cookie', `SameSite is ${session.sameSite}, not Strict`)
    }
  }

  // A terminal over wss, which is the part that silently fails when a proxy or
  // a scheme is wrong.
  const proj = await page.evaluate(async () => {
    const res = await fetch('/api/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: '/tmp', name: 'tls' }),
    })
    return res.json()
  })
  await page.evaluate(async (projectId) => {
    await fetch('/api/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ projectId, title: 'over-tls', command: ['bash', '--norc', '--noprofile', '-i'] }),
    })
  }, proj.id)
  await page.locator('[data-testid="session-row"]', { hasText: 'over-tls' }).first().click()
  await sleep(2500)

  const scheme = await page.evaluate(() =>
    window.performance
      .getEntriesByType('resource')
      .map((e) => e.name)
      .find((n) => n.startsWith('ws')) ?? null,
  )
  const marker = 'OVER' + '_WSS_OK'
  await page.locator('.xterm-helper-textarea').click()
  await page.keyboard.type(`echo ${marker.slice(0, 4)}"${marker.slice(4)}"\n`)
  let sawMarker = false
  for (let i = 0; i < 30; i++) {
    const text = await page.locator('.xterm-screen').first().innerText().catch(() => '')
    if (text.includes(marker)) { sawMarker = true; break }
    await sleep(400)
  }
  if (!sawMarker) {
    note('FAIL', 'wss', 'nothing typed over TLS reached the session; the WebSocket did not work')
  }
  if (scheme && !scheme.startsWith('wss:')) {
    note('FAIL', 'wss', `the socket connected over ${scheme.split(':')[0]}: on an https page`)
  }
  await page.screenshot({ path: join(SHOTS, 'over-tls.png') })

  // The settings page has to say when the certificate runs out.
  //
  // A certificate nobody renewed does not announce itself; it stops working
  // one morning. The panel warns in its log as the date approaches, and a log
  // on a machine nobody reads is not where an operator should first learn it —
  // so the date is on the page they actually open.
  await page.locator('[data-testid="settings-open"], header button[title*="Settings" i]')
    .first().click().catch(() => {})
  await sleep(1500)
  const statusText = await page
    .locator('[data-testid="settings-status"]')
    .innerText()
    .catch(() => '')
  if (!statusText) {
    note('WARN', 'tls', 'could not open the settings dialog to check the certificate row')
  } else if (!/Certificate/i.test(statusText)) {
    note('FAIL', 'tls',
      `the settings page does not mention the certificate at all: ${JSON.stringify(statusText.slice(0, 200))}`)
  } else if (!/day|expired/i.test(statusText)) {
    note('FAIL', 'tls',
      `the certificate row carries no date or countdown: ${JSON.stringify(statusText.slice(0, 200))}`)
  }
  await page.screenshot({ path: join(SHOTS, 'settings-cert.png') })
  await page.locator('[data-testid="settings-close"]').click().catch(() => {})
  await sleep(500)

  // ── replacing the certificate under a running server ─────────────────────
  // The reason the file source polls instead of watching: a certificate is
  // renewed by a process that writes and renames, and a watcher on the old
  // inode sees nothing.
  const before = servedFingerprint()
  renameSync(second.crt, LIVE_CRT)
  renameSync(second.key, LIVE_KEY)
  let swapped = false
  for (let i = 0; i < 90; i++) {
    if (servedFingerprint() !== before) { swapped = true; break }
    await sleep(1000)
  }
  if (!swapped) {
    note('FAIL', 'tls', 'a replaced certificate was never picked up; renewal needs a restart')
  } else if (!servedSubject().includes('second.vibepanel.test')) {
    note('FAIL', 'tls', `after the swap the server serves ${servedSubject()}`)
  }

  // The page that was already open must not have been disturbed by any of it.
  const stillMarker = 'AFTER' + '_THE_SWAP'
  await page.locator('.xterm-helper-textarea').click()
  await page.keyboard.type(`echo ${stillMarker.slice(0, 5)}"${stillMarker.slice(5)}"\n`)
  let stillWorks = false
  for (let i = 0; i < 30; i++) {
    const text = await page.locator('.xterm-screen').first().innerText().catch(() => '')
    if (text.includes(stillMarker)) { stillWorks = true; break }
    await sleep(400)
  }
  if (!stillWorks) {
    note('FAIL', 'tls', 'an open session stopped working when the certificate was replaced')
  }

  // A broken certificate must not take the listener down with it: keeping the
  // old pair is the difference between a warning and an outage during a
  // botched renewal.
  //
  // The wait has to outlast the reload interval. An earlier version of this
  // slept two seconds against a source that polls once a minute, so the file
  // had not been looked at when the assertion ran: it confirmed that a server
  // which had not yet noticed anything was still working, which is not a
  // property worth checking.
  const goodFingerprint = servedFingerprint()
  execSync(`printf 'not a certificate\\n' > ${LIVE_CRT}`)
  // Long enough to cross the reload interval with room to spare, checking the
  // whole way rather than only at the end: an outage that lasts a few seconds
  // in the middle is still an outage.
  for (let i = 0; i < 75; i++) {
    if (!servedFingerprint()) {
      note('FAIL', 'tls',
        'the listener stopped answering handshakes after a corrupt certificate was written; ' +
        'a bad renewal would be an outage')
      break
    }
    await sleep(1000)
  }
  // Still serving what it had before, rather than having picked up the rubbish.
  if (servedFingerprint() !== goodFingerprint) {
    note('FAIL', 'tls',
      `after a corrupt certificate the server serves ${servedSubject()}; it should have kept ` +
      'the last good pair')
  }
  for (const e of pageErrors) note('FAIL', 'js', `uncaught: ${e}`)
} catch (err) {
  note('FAIL', 'harness', String(err?.stack ?? err))
} finally {
  await cleanup()
}

for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`=== tls check: ${fails} FAIL, ${findings.length - fails} WARN ===`)
console.log(`screenshots: ${SHOTS}`)
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
process.exit(fails ? 1 : 0)
