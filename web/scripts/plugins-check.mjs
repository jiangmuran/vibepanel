// The plugins page, driven in a real browser against the real binary.
// docs/plugins.md §12.
//
// What it covers: the rail link reaches /plugins; a plugin read from a
// directory arrives with its install screen and nothing running; the screen
// draws every line the server sent, in both languages, with the danger line
// first and the button's words escalating; a box unticked is a capability not
// granted, and the card says so; a missing secret installs the plugin
// disabled and the settings form fills it; the theme a plugin carries joins
// the toggle's cycle, is in the document before first paint on a reload, and
// recolours the page; the settings the panel draws save and reset; removing
// takes everything; and the page fits at phone, tablet and desktop widths in
// both themes, with the shared probes for overflow, small targets, unnamed
// controls and faded controls.
//
// What it does not cover yet: frames, services and processes, which do not
// exist yet; each later rung adds its section here.

import { chromium } from 'playwright'
import { createServer as createNetServer } from 'node:net'
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
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
const SOCKET = `vpplug-${process.pid}`
const BASE = `http://127.0.0.1:${PORT}`
const USERNAME = 'plug'
const PASSWORD = 'plugins-check-password-1'

const findings = []
const note = (sev, where, msg) => {
  findings.push({ sev, where, msg })
  console.log(`[${sev}] ${where}: ${msg}`)
}
const pass = (where, msg) => console.log(`[ok]   ${where}: ${msg}`)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const work = mkdtempSync(join(tmpdir(), 'vpplug-'))
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

async function until(pred, ms = 8000, step = 150) {
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if (await pred()) return true
    await sleep(step)
  }
  return false
}

// The plugin under test: a theme, a pane (which has no runtime yet and is
// here for the screen), a danger capability to untick, a setting of each
// kind, and a secret.
const PLUGIN_DIR = join(work, 'paper')
mkdirSync(PLUGIN_DIR, { recursive: true })
writeFileSync(join(PLUGIN_DIR, 'plugin.json'), JSON.stringify({
  plugin: 1, id: 'paper', name: { en: 'Paper', 'zh-CN': '纸' }, version: '1.2.0',
  description: { en: 'A warm theme and a stand-up pane.', 'zh-CN': '一个暖色主题和一个站会 pane。' },
  theme: { file: 'theme.css', name: { en: 'Paper', 'zh-CN': '纸' }, scheme: 'light' },
  panels: [{ slot: 'sidepanel.pane', entry: 'pane.html', title: { en: 'Stand-up', 'zh-CN': '站会' } }],
  capabilities: ['read:panel', 'write:todos', 'sessions:input'],
  settings: { fields: [
    { key: 'quiet', type: 'bool', default: true, label: { en: 'Quiet hours', 'zh-CN': '安静时段' } },
    { key: 'channel', type: 'enum', values: ['slack', 'email'], default: 'slack', label: { en: 'Channel', 'zh-CN': '渠道' } },
    { key: 'greeting', type: 'text', default: 'hello', label: { en: 'Greeting', 'zh-CN': '问候语' }, hint: { en: 'Shown at the top.' } },
    { key: 'limit', type: 'number', min: 1, max: 9, default: 3, label: { en: 'Limit', 'zh-CN': '上限' } },
    { key: 'tint', type: 'color', default: '#aabbcc', label: { en: 'Tint', 'zh-CN': '色调' } },
    { key: 'names', type: 'list', label: { en: 'Names', 'zh-CN': '名字' } },
    { key: 'token', type: 'secret', label: { en: 'Token', 'zh-CN': '令牌' } },
  ] },
}, null, 2))
writeFileSync(join(PLUGIN_DIR, 'theme.css'), `/* Paper */
:root[data-theme='ext-paper'] {
  color-scheme: light;
  --vp-bg: #f4f1ea;
  --vp-surface: #fbf9f4;
  --vp-surface-2: #efeadf;
  --vp-ink: #2a2520;
  --vp-accent: #a0522d;
}
`)
writeFileSync(join(PLUGIN_DIR, 'pane.html'), '<!doctype html><p>stand-up</p>')
writeFileSync(join(PLUGIN_DIR, 'AGENTS.md'), '# not published')

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

  const page = await owner.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(`console: ${m.text()}`)
  })

  // ── 1. The way in: the rail link ──────────────────────────────────────
  await page.goto(`${BASE}/`)
  await page.getByTestId('settings-open').click()
  const link = page.getByTestId('settings-plugins-link')
  if (!(await link.isVisible())) note('fail', 'rail', 'no Plugins link in the settings rail')
  await link.click()
  await page.waitForURL(`${BASE}/plugins`)
  if (!(await page.getByTestId('plugins-empty').isVisible())) note('fail', 'page', 'the empty state is not shown')
  else pass('rail', 'the settings rail reaches /plugins, empty')

  // ── 2. A plugin arrives from a directory, with its screen ─────────────
  await page.getByTestId('plugin-add-dir').click()
  await page.getByTestId('plugin-dir').fill(PLUGIN_DIR)
  await page.getByTestId('plugin-dir-read').click()
  const screen = page.getByTestId('plugin-install-screen')
  if (!(await until(() => screen.isVisible()))) note('fail', 'arrive', 'the install screen did not open after reading the directory')
  const row = page.getByTestId('plugin-row')
  const state = await row.getAttribute('data-state')
  if (state !== 'new') note('fail', 'arrive', `the card says ${state}, not "new": something ran before the screen`)
  else pass('arrive', 'read from a directory, not installed')

  // Every kind of line the server sent is on screen, the danger line first
  // among the capabilities, and the button says "Install and grant".
  const capCodes = await screen.locator('[data-testid="plugin-screen-cap"] li').evaluateAll((els) => els.map((e) => e.dataset.code))
  if (capCodes[0] !== 'sessions:input') note('fail', 'screen', `the danger line is not first: ${capCodes}`)
  for (const heading of ['what', 'rung', 'cap', 'keeps']) {
    if (!(await screen.getByTestId(`plugin-screen-${heading}`).isVisible())) note('fail', 'screen', `no ${heading} section`)
  }
  const button = screen.getByTestId('plugin-screen-confirm')
  if ((await button.textContent())?.trim() !== 'Install and grant') note('fail', 'screen', `button says "${await button.textContent()}"`)
  else pass('screen', 'the sections and the button')
  await page.screenshot({ path: join(SHOTS, 'screen-en.png'), fullPage: true })

  // In Chinese: the same lines, the other words, the other button. The
  // switch is in the page's header behind the dialog, so the screen is
  // closed, the language changed, and the screen reopened from the card.
  await screen.getByTestId('plugin-screen-close').click()
  await page.getByTestId('plugins-lang-zh').click()
  await page.getByTestId('plugin-install').click()
  await until(() => screen.isVisible())
  if ((await button.textContent())?.trim() !== '安装并授权') note('fail', 'screen', `zh button says "${await button.textContent()}"`)
  const zhDanger = await screen.locator('[data-code="sessions:input"]').textContent()
  if (!zhDanger?.includes('能往你的任何终端里输入')) note('fail', 'screen', `zh danger line: ${zhDanger}`)
  else pass('screen', 'the same screen in Chinese')
  await page.screenshot({ path: join(SHOTS, 'screen-zh.png'), fullPage: true })
  await screen.getByTestId('plugin-screen-close').click()
  await page.getByTestId('plugins-lang-en').click()
  await page.getByTestId('plugin-install').click()
  await until(() => screen.isVisible())

  // Untick the danger box and confirm.
  const dangerBox = screen.getByTestId('plugin-cap-sessions:input')
  if (!(await dangerBox.isChecked())) note('fail', 'screen', 'the danger box is unticked on a first install; every box starts ticked')
  await dangerBox.uncheck()
  await button.click()
  await until(async () => !(await screen.isVisible()))
  const notice = await page.getByTestId('plugins-notice').textContent()
  if (!notice?.includes('TOKEN')) note('fail', 'install', `the notice does not name the missing secret: ${notice}`)
  await until(async () => (await row.getAttribute('data-state')) === 'disabled')
  if ((await row.getAttribute('data-state')) !== 'disabled') note('fail', 'install', `after install the card says ${await row.getAttribute('data-state')}; a missing secret installs disabled`)
  const granted = await page.getByTestId('plugin-granted').textContent()
  if (!granted?.includes('2 of 3')) note('fail', 'install', `granted reads "${granted}", want 2 of 3`)
  else pass('install', 'installed disabled with the unticked box withheld')
  const detail = await api('GET', '/api/settings/plugins/paper')
  if (detail.body.granted.join(',') !== 'read:panel,write:todos') note('fail', 'install', `server granted ${detail.body.granted}`)

  // Enable is refused by name until the secret is set.
  const refused = await api('POST', '/api/settings/plugins/paper/enable', {})
  if (refused.status !== 409 || !JSON.stringify(refused.body).includes('TOKEN')) note('fail', 'secret', `enable without the secret: ${refused.status} ${JSON.stringify(refused.body)}`)

  // ── 3. The settings the panel draws, and the secret ───────────────────
  await page.getByTestId('plugin-settings').click()
  const form = page.getByTestId('plugin-settings-form')
  if (!(await until(() => form.isVisible()))) note('fail', 'settings', 'the settings form did not open')
  for (const key of ['quiet', 'channel', 'greeting', 'limit', 'tint', 'names']) {
    if (!(await page.getByTestId(`plugin-paper-${key}`).isVisible())) note('fail', 'settings', `no control for ${key}`)
  }
  await page.getByTestId('plugin-paper-quiet').uncheck()
  await page.getByTestId('plugin-paper-greeting').fill('good morning')
  await page.getByTestId('plugin-paper-greeting').press('Enter')
  await page.getByTestId('plugin-paper-names').fill('ann\nbob')
  await page.getByTestId('plugin-paper-names').blur()
  await sleep(400)
  const values = (await api('GET', '/api/settings/plugins/paper/settings')).body.values
  if (values.quiet !== false || values.greeting !== 'good morning' || JSON.stringify(values.names) !== '["ann","bob"]') {
    note('fail', 'settings', `saved values ${JSON.stringify(values)}`)
  } else pass('settings', 'a bool, a text and a list saved from the form')
  await page.getByTestId('plugin-paper-greeting-reset').click()
  await sleep(400)
  if ((await api('GET', '/api/settings/plugins/paper/settings')).body.values.greeting !== 'hello') note('fail', 'settings', 'reset did not restore the default')

  const secretState = page.getByTestId('plugin-paper-secret-TOKEN-state')
  if ((await secretState.textContent())?.trim() !== 'Not set') note('fail', 'secret', `state reads ${await secretState.textContent()}`)
  await page.getByTestId('plugin-paper-secret-TOKEN-value').fill('s3cret')
  await page.getByTestId('plugin-paper-secret-TOKEN-save').click()
  await until(async () => (await secretState.textContent())?.trim() === 'Set')
  if ((await secretState.textContent())?.trim() !== 'Set') note('fail', 'secret', 'the secret did not register as set')
  else pass('secret', 'set from the form, never read back')
  await page.screenshot({ path: join(SHOTS, 'settings.png'), fullPage: true })

  // ── 4. Enable, and the theme ──────────────────────────────────────────
  await page.getByTestId('plugin-enable').click()
  await until(async () => (await row.getAttribute('data-state')) === 'enabled')
  if ((await row.getAttribute('data-state')) !== 'enabled') note('fail', 'enable', 'the card does not say enabled')
  const css = await (await owner.request.get(`${BASE}/plugin-themes.css`)).text()
  if (!css.includes(":root[data-theme='ext-paper']") || !css.includes('--vp-bg: #f4f1ea;')) note('fail', 'theme', `the theme sheet:\n${css}`)
  else pass('theme', 'the enabled theme is served')

  // The toggle walks to the plugin theme on a fresh load (the list is read
  // once per page), and the page recolours.
  await page.reload()
  const bgBefore = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--vp-bg').trim())
  for (let i = 0; i < 4; i++) {
    await page.getByTestId('theme-toggle').click()
    await sleep(80)
    if ((await page.evaluate(() => document.documentElement.dataset.theme)) === 'ext-paper') break
  }
  const attr = await page.evaluate(() => document.documentElement.dataset.theme)
  const bgAfter = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--vp-bg').trim())
  if (attr !== 'ext-paper') note('fail', 'theme', `the toggle never reached the plugin theme (data-theme=${attr})`)
  else if (bgAfter !== '#f4f1ea' || bgAfter === bgBefore) note('fail', 'theme', `--vp-bg is ${bgAfter} under the plugin theme`)
  else pass('theme', 'the toggle reaches it and the page recolours')
  await page.screenshot({ path: join(SHOTS, 'theme.png'), fullPage: true })

  // Before first paint on a reload: the stored choice is applied by the
  // inline script and the sheet is linked, so the first frame is the theme.
  await page.reload()
  const firstPaint = await page.evaluate(() => ({
    attr: document.documentElement.dataset.theme,
    bg: getComputedStyle(document.documentElement).getPropertyValue('--vp-bg').trim(),
    link: !!document.querySelector('link[href="/plugin-themes.css"]'),
  }))
  if (firstPaint.attr !== 'ext-paper' || firstPaint.bg !== '#f4f1ea' || !firstPaint.link) note('fail', 'theme', `after reload: ${JSON.stringify(firstPaint)}`)
  else pass('theme', 'survives a reload without a flash')
  // Back to system for the layout shots.
  await page.evaluate(() => { localStorage.setItem('vibepanel.theme', 'system') })

  // ── 5. Layout at three widths, both themes ────────────────────────────
  for (const [name, width] of [['phone', 390], ['tablet', 820], ['desktop', 1400]]) {
    for (const theme of ['light', 'dark']) {
      await page.evaluate((t) => { localStorage.setItem('vibepanel.theme', t) }, theme)
      await page.setViewportSize({ width, height: 900 })
      await page.reload()
      await until(() => page.getByTestId('plugin-row').isVisible())
      await page.getByTestId('plugin-settings').click()
      await sleep(200)
      const wide = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)
      if (wide) note('fail', `layout ${name} ${theme}`, 'the page scrolls sideways')
      const { examined, found } = await findUnreachable(page, sleep)
      if (examined < 10) note('fail', `layout ${name} ${theme}`, `the overflow scan measured ${examined} elements`)
      else if (found.length) note('fail', `layout ${name} ${theme}`, `unreachable: ${found.slice(0, 3).join(' | ')}`)
      if (name === 'phone') {
        const { small } = await findSmallTargets(page, 32)
        if (small.length) note('warn', `layout ${name} ${theme}`, `small targets: ${small.slice(0, 5).join(', ')}`)
      }
      const unnamed = await findUnnamedControls(page)
      if (unnamed.length) note('fail', `layout ${name} ${theme}`, `unnamed controls: ${unnamed.slice(0, 5).join(', ')}`)
      const faded = await findFadedControls(page)
      if (faded.length) note('fail', `layout ${name} ${theme}`, `faded: ${faded.slice(0, 5).join(', ')}`)
      await page.screenshot({ path: join(SHOTS, `layout-${name}-${theme}.png`), fullPage: true })
    }
  }
  pass('layout', 'three widths, two themes')
  await page.setViewportSize({ width: 1400, height: 950 })
  await page.evaluate(() => { localStorage.setItem('vibepanel.theme', 'system') })
  await page.reload()

  // ── 6. Remove takes everything ────────────────────────────────────────
  await until(() => page.getByTestId('plugin-row').isVisible())
  await page.getByTestId('plugin-remove').click()
  const dialog = page.locator('[data-vp-modal]').last()
  await until(() => dialog.isVisible())
  await dialog.getByRole('button', { name: 'Remove' }).click()
  await until(() => page.getByTestId('plugins-empty').isVisible())
  if (!(await page.getByTestId('plugins-empty').isVisible())) note('fail', 'remove', 'the list is not empty after removing')
  const gone = await api('GET', '/api/settings/plugins/paper')
  if (gone.status !== 404) note('fail', 'remove', `the plugin still answers: ${gone.status}`)
  const sheet = await (await owner.request.get(`${BASE}/plugin-themes.css`)).text()
  if (sheet.includes('ext-paper')) note('fail', 'remove', 'the theme is still served after removing')
  else pass('remove', 'the row, the grants, the secret and the theme are gone')

  if (errors.length) note('fail', 'console', errors.slice(0, 5).join('\n'))
} catch (e) {
  note('fail', 'script', e instanceof Error ? e.stack ?? e.message : String(e))
} finally {
  await browser?.close()
  server?.kill('SIGTERM')
  try {
    execFileSync('tmux', ['-L', SOCKET, 'kill-server'], { stdio: 'ignore' })
  } catch {
    /* nothing to kill */
  }
}

console.log(`\nshots: ${SHOTS}`)
const fails = findings.filter((f) => f.sev === 'fail')
if (fails.length) {
  console.log(`\n${fails.length} failure(s)`)
  process.exit(1)
}
console.log('\nplugins-check: ok')
