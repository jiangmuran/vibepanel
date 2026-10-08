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
  panels: [
    { slot: 'sidepanel.pane', entry: 'pane.html', title: { en: 'Stand-up', 'zh-CN': '站会' }, icon: 'list-checks' },
    { slot: 'settings.section', entry: 'section.html', title: { en: 'Paper settings', 'zh-CN': '纸的设置' }, group: 'panel' },
    { slot: 'page', entry: 'page.html', title: { en: 'Stand-up page', 'zh-CN': '站会页' }, path: 'standup' },
    { slot: 'header.item', entry: 'header.html', title: { en: 'Waiting count', 'zh-CN': '等待数' } },
  ],
  capabilities: ['read:panel', 'write:todos', 'sessions:input', 'ui:notify'],
  data: { events: { type: 'counter' } },
  server: { entry: 'server.js', on: ['session.created', 'session.state'], routes: { 'GET /digest': 'digest' } },
  process: { command: ['node', 'bot.js'], capabilities: ['read:panel'], http: { auth: 'token' } },
  unsandboxed: { entry: 'main.mjs', tested: '0.0.0 - 99.0.x' },
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
// The pane: draws the view through the SDK, and probes every escape from
// inside the sandbox, posting what happened to the parent. A probe that
// *succeeds* is the failure; each is asserted by how it failed.
writeFileSync(join(PLUGIN_DIR, 'pane.html'), `<!doctype html>
<html><head><link rel="stylesheet" href="vibepanel-ui.css"><script src="vibepanel-plugin.js"></script></head>
<body class="vp-section">
<h2>Stand-up</h2>
<p>status: <span id="status">-</span> · caps: <span id="caps"></span></p>
<ul id="sessions" class="vp-list"></ul>
<button id="notify" class="vp-button">notify</button>
<p>digest: <span id="digest">-</span></p>
<script>
  var vp = VibePanel.plugin()
  vp.on('status', function (s) { document.getElementById('status').textContent = s })
  vp.on('view', function (v) {
    document.getElementById('caps').textContent = v.caps.join(',')
    var ul = document.getElementById('sessions'); ul.innerHTML = ''
    v.sessions.forEach(function (s) {
      var li = document.createElement('li'); li.className = 'vp-item'
      var b = document.createElement('span'); vp.badge(b, s.state); li.appendChild(b)
      var t = document.createElement('span'); vp.text(t, s.name); li.appendChild(t)
      if (s.cwd) { var c = document.createElement('code'); vp.text(c, s.cwd); li.appendChild(c) }
      ul.appendChild(li)
    })
  })
  function digest() { vp.route('GET', 'digest').then(function (r) { document.getElementById('digest').textContent = r.result.sessions + ' sessions, ' + r.result.events + ' events' }) }
  vp.on('view', digest)
  document.getElementById('notify').onclick = function () {
    vp.ui.notify('hello from paper').then(function (ok) { window.parent.postMessage({ type: 'probe', name: 'notify-answer', ok: ok }, '*') })
  }
  // The probes. Results go to the parent as {type:'probe', name, ok, detail}.
  function report(name, ok, detail) { window.parent.postMessage({ type: 'probe', name: name, ok: ok, detail: String(detail || '').slice(0, 120) }, '*') }
  try { var c = document.cookie; report('cookie', true, c) } catch (e) { report('cookie', false, e.message) }
  try { localStorage.setItem('x', '1'); report('storage', true) } catch (e) { report('storage', false, e.message) }
  report('origin', self.origin !== 'null', self.origin)
  fetch(location.origin + '/api/state', { credentials: 'include' }).then(function (r) { report('api-state', r.ok, r.status) }, function (e) { report('api-state', false, e.message) })
  fetch(location.origin + '/api/settings/plugins').then(function (r) { report('api-plugins', r.ok, r.status) }, function (e) { report('api-plugins', false, e.message) })
  fetch('https://example.com/').then(function (r) { report('outside', true, r.status) }, function (e) { report('outside', false, e.message) })
  try { var ws = new WebSocket(location.origin.replace('http', 'ws') + '/ws'); ws.onopen = function () { report('ws', true) }; ws.onerror = function () { report('ws', false, 'refused') } } catch (e) { report('ws', false, e.message) }
  try { var w = window.open(location.origin + '/'); report('popup', !!w, w ? 'opened' : 'null') } catch (e) { report('popup', false, e.message) }
  try { var f = document.createElement('iframe'); f.src = location.origin + '/'; f.onload = function () { try { var d = f.contentDocument.title; report('frame-panel', true, d) } catch (e) { report('frame-panel', false, 'opaque') } }; f.onerror = function () { report('frame-panel', false, 'blocked') }; document.body.appendChild(f); setTimeout(function () { report('frame-panel', false, 'no load') }, 1500) } catch (e) { report('frame-panel', false, e.message) }
  document.addEventListener('securitypolicyviolation', function (e) { report('csp-' + e.violatedDirective.split(' ')[0], false, e.blockedURI) })
</script>
</body></html>`)
writeFileSync(join(PLUGIN_DIR, 'server.js'), `
function onEvent(ev, ctx) { ctx.data.increment('events'); ctx.log('event ' + ev.name) }
function digest(req, ctx) { return { sessions: ctx.panel.view().sessions.length, events: ctx.data.get('events') } }
`)
writeFileSync(join(PLUGIN_DIR, 'main.mjs'), `export default function (host) {
  host.css('[data-testid="plugins-page"], [data-testid="right-panel"] { outline: 2px solid var(--vp-accent) }')
  host.slots.add('header.item', () => { const b = document.createElement('span'); b.dataset.testid = 'mod-header'; b.textContent = 'mod v' + host.v; return b })
  host.slots.add('sidebar.sessionRow.trailing', (ctx) => { const b = document.createElement('span'); b.dataset.testid = 'mod-row'; b.textContent = '★'; return b })
  host.state.subscribe((s) => { document.documentElement.dataset.modSessions = String(s.sessions.length) })
}
`)
// The process: prints a tick a second for the card, and listens on the
// socket the panel named so the door has something behind it. It answers
// with what it was sent, which is how the check sees that the cookie never
// arrives and the caller header does. It refuses a request without the
// per-start secret, as the scaffold tells every author to.
writeFileSync(join(PLUGIN_DIR, 'bot.js'), `
const http = require('node:http')
let i = 0
setInterval(() => console.log('tick ' + (++i)), 1000)
http.createServer((req, res) => {
  if (req.headers['x-vibepanel-proxy'] !== process.env.VIBEPANEL_PLUGIN_PROXY_SECRET) { res.writeHead(403); return res.end('not via the panel') }
  let body = ''
  req.on('data', (c) => { body += c })
  req.on('end', () => {
    res.setHeader('Content-Type', 'application/json')
    res.setHeader('Set-Cookie', 'vp_session=stolen')
    res.end(JSON.stringify({ path: req.url, caller: req.headers['x-vibepanel-caller'], cookie: req.headers.cookie || '', auth: req.headers.authorization || '', body }))
  })
}).listen(process.env.VIBEPANEL_PLUGIN_SOCKET)
`)
writeFileSync(join(PLUGIN_DIR, 'section.html'), `<!doctype html><html><head><link rel="stylesheet" href="vibepanel-ui.css"><script src="vibepanel-plugin.js"></script></head>
<body class="vp-section"><h2>Paper settings</h2><p id="s">-</p><script>var vp = VibePanel.plugin(); vp.settings().then(function (s) { document.getElementById('s').textContent = 'quiet=' + s.values.quiet })</script></body></html>`)
writeFileSync(join(PLUGIN_DIR, 'page.html'), `<!doctype html><html><head><link rel="stylesheet" href="vibepanel-ui.css"><script src="vibepanel-plugin.js"></script></head>
<body class="vp-section"><h1 id="h">Stand-up page</h1><p id="n">-</p><script>var vp = VibePanel.plugin(); vp.on('view', function (v) { document.getElementById('n').textContent = v.sessions.length + ' sessions' })</script></body></html>`)
writeFileSync(join(PLUGIN_DIR, 'header.html'), `<!doctype html><html><head><link rel="stylesheet" href="vibepanel-ui.css"><script src="vibepanel-plugin.js"></script></head>
<body style="margin:0;padding:0 8px;line-height:28px"><span id="w">·</span><script>var vp = VibePanel.plugin(); vp.on('view', function (v) { document.getElementById('w').textContent = v.sessions.filter(function (s) { return s.state === 'waiting' }).length + ' waiting' })</script></body></html>`)
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
    // The sandbox probes are refused by CSP on purpose, and the browser logs
    // each refusal as an error; those are the pass, not a failure.
    if (m.type() === 'error' && !/Content Security Policy|Refused to connect|violates|sandboxed frame/.test(m.text())) errors.push(`console: ${m.text()}`)
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
  for (const heading of ['what', 'rung', 'cap', 'runs', 'keeps', 'danger']) {
    if (!(await screen.getByTestId(`plugin-screen-${heading}`).isVisible())) note('fail', 'screen', `no ${heading} section`)
  }
  const button = screen.getByTestId('plugin-screen-confirm')
  if ((await button.textContent())?.trim() !== 'Run this as you') note('fail', 'screen', `button says "${await button.textContent()}"`)
  else pass('screen', 'the sections and the button')
  await page.screenshot({ path: join(SHOTS, 'screen-en.png'), fullPage: true })

  // In Chinese: the same lines, the other words, the other button. The
  // switch is in the page's header behind the dialog, so the screen is
  // closed, the language changed, and the screen reopened from the card.
  await screen.getByTestId('plugin-screen-close').click()
  await page.getByTestId('plugins-lang-zh').click()
  await page.getByTestId('plugin-install').click()
  await until(() => screen.isVisible())
  if ((await button.textContent())?.trim() !== '以你的身份运行') note('fail', 'screen', `zh button says "${await button.textContent()}"`)
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
  if (!granted?.includes('3 of 4')) note('fail', 'install', `granted reads "${granted}", want 3 of 4`)
  else pass('install', 'installed disabled with the unticked box withheld')
  const detail = await api('GET', '/api/settings/plugins/paper')
  if (detail.body.granted.join(',') !== 'read:panel,ui:notify,write:todos') note('fail', 'install', `server granted ${detail.body.granted}`)

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

  // ── 4b. The frames: the pane, the probes, the section, the page, dev mode ──
  // A project and a session for the view to show.
  const projectDir = join(work, 'proj')
  mkdirSync(projectDir, { recursive: true })
  const project = (await api('POST', '/api/projects', { path: projectDir, name: 'paperproj' })).body
  const session = (await api('POST', '/api/sessions', { projectId: project.id, command: ['sleep', '600'] })).body
  await api('PATCH', `/api/sessions/${session.id}`, { title: 'write the digest' })

  await page.goto(`${BASE}/`)
  const probes = {}
  await page.exposeFunction('vpProbe', (name, ok, detail) => { probes[name] = { ok, detail } })
  await page.evaluate(() => {
    window.addEventListener('message', (e) => {
      if (e.data && e.data.type === 'probe') window.vpProbe(e.data.name, e.data.ok, e.data.detail)
    })
  })
  const extTab = page.getByTestId('panel-tab-ext:paper:0')
  if (!(await until(() => extTab.isVisible(), 10000))) {
    note('fail', 'pane', 'the plugin pane is not in the side panel strip')
  } else {
    await extTab.click()
    const frame = page.getByTestId('plugin-frame-paper-0')
    if (!(await until(() => frame.isVisible()))) note('fail', 'pane', 'the frame did not mount')
    const inner = page.frameLocator('[data-testid="plugin-frame-paper-0"]')
    const live = await until(async () => (await inner.locator('#status').textContent()) === 'live', 10000)
    if (!live) note('fail', 'pane', `the SDK never went live: ${await inner.locator('#status').textContent().catch(() => '?')}`)
    const caps = await inner.locator('#caps').textContent()
    if (!caps?.includes('read:panel') || caps.includes('sessions:input')) note('fail', 'pane', `caps in the frame: ${caps}`)
    const names = await inner.locator('#sessions').textContent()
    if (!names?.includes('write the digest')) note('fail', 'pane', `the view did not reach the frame: ${names}`)
    else if (names.includes(projectDir)) note('fail', 'pane', 'the view carries a path without read:paths')
    else pass('pane', 'the pane mounts, goes live and draws the view through handles')
    await page.screenshot({ path: join(SHOTS, 'pane.png') })

    // The service: the pane calls the plugin's own route through the frame's
    // door and gets server.js's answer; an event raised by the panel reaches
    // onEvent and the counter it keeps moves.
    const digestShown = await until(async () => ((await inner.locator('#digest').textContent()) ?? '').includes('1 sessions'), 8000)
    if (!digestShown) note('fail', 'service', `the route's answer did not reach the pane: ${await inner.locator('#digest').textContent()}`)
    const eventsBefore = Number((/(\d+) events/.exec((await inner.locator('#digest').textContent()) ?? '') ?? [])[1] ?? 0)
    await api('POST', '/api/sessions', { projectId: project.id, command: ['sleep', '600'] })
    const moved = await until(async () => {
      const txt = (await inner.locator('#digest').textContent()) ?? ''
      const n = Number((/(\d+) events/.exec(txt) ?? [])[1] ?? 0)
      return n > eventsBefore
    }, 8000)
    if (!moved) note('fail', 'service', 'session.created did not reach onEvent (the counter did not move)')
    else pass('service', "a route answers through the frame's door and an event reaches onEvent")
    const log = await api('GET', '/api/settings/plugins/paper/server/log')
    if (!JSON.stringify(log.body).includes('event session.created')) note('fail', 'service', `the server log lacks the event: ${JSON.stringify(log.body).slice(0, 200)}`)
    const owner = await api('GET', '/api/ext/paper/digest')
    if (owner.status !== 200 || !owner.body?.result) note('fail', 'service', `the owner's door: ${owner.status} ${JSON.stringify(owner.body)}`)
    else pass('service', "the owner's door answers the same route")

    // The probes: every one must have failed.
    await until(() => Object.keys(probes).length >= 7, 5000)
    for (const name of ['cookie', 'storage', 'origin', 'api-state', 'api-plugins', 'outside', 'ws', 'popup', 'frame-panel']) {
      const p = probes[name]
      if (!p) note('fail', `sandbox ${name}`, 'no result from the probe')
      else if (p.ok) note('fail', `sandbox ${name}`, `the frame escaped: ${p.detail}`)
    }
    if (Object.values(probes).every((p) => !p.ok)) pass('sandbox', `every escape refused: ${Object.keys(probes).sort().join(', ')}`)

    // ui:notify was ticked, so a toast arrives with the plugin's name.
    await inner.locator('#notify').click()
    const shown = await until(async () => (await page.locator('body').textContent())?.includes('hello from paper') ?? false, 5000)
    if (!shown) note('fail', 'notify', 'the toast did not show')
    else pass('notify', 'a granted ui:notify reaches the panel as a toast')
    // Untick ui:notify: the same message is answered null and shows nothing.
    await api('PUT', '/api/settings/plugins/paper/caps', { caps: ['read:panel', 'write:todos'] })
    await sleep(300)
    delete probes['notify-answer']
    await inner.locator('#notify').click()
    await until(() => !!probes['notify-answer'], 3000)
    if (probes['notify-answer']?.ok !== false && probes['notify-answer']?.ok !== null) note('fail', 'notify', `after unticking ui:notify the frame got ${JSON.stringify(probes['notify-answer'])}`)
    else pass('notify', 'withheld ui:notify is answered with nothing')
    await api('PUT', '/api/settings/plugins/paper/caps', { caps: ['read:panel', 'write:todos', 'ui:notify'] })
  }

  // The header item and the settings section.
  const headerItem = page.getByTestId('plugin-header-item')
  if (!(await headerItem.isVisible())) note('fail', 'header', 'no header item')
  else {
    const h = page.frameLocator('[data-testid="plugin-frame-paper-3"]')
    if (!(await until(async () => (await h.locator('#w').textContent())?.includes('waiting') ?? false, 8000))) note('fail', 'header', 'the header frame drew nothing')
    else pass('header', 'the header item draws from the view')
  }
  await page.getByTestId('settings-open').click()
  await page.getByTestId('settings-group-panel').click()
  const section = page.getByTestId('plugin-section')
  if (!(await until(() => section.isVisible(), 5000))) note('fail', 'section', 'no settings section for the plugin')
  else {
    const sf = page.frameLocator('[data-testid="plugin-frame-paper-1"]')
    if (!(await until(async () => (await sf.locator('#s').textContent())?.includes('quiet=') ?? false, 8000))) note('fail', 'section', 'the section frame did not read its settings')
    else pass('section', 'the settings section mounts under its group and reads the settings')
    await page.screenshot({ path: join(SHOTS, 'section.png') })
  }
  await page.getByTestId('settings-close').click()

  // The page slot.
  await page.goto(`${BASE}/x/standup`)
  const pf = page.frameLocator('[data-testid="plugin-frame-paper-2"]')
  if (!(await until(async () => /\d+ sessions/.test((await pf.locator('#n').textContent()) ?? ''), 10000))) note('fail', 'page', 'the plugin page did not draw the view')
  else pass('page', '/x/standup mounts the page with the view')
  await page.screenshot({ path: join(SHOTS, 'page.png') })
  await page.goto(`${BASE}/x/nothing-here`)
  if (!(await until(() => page.getByTestId('plugin-page-missing').isVisible(), 5000))) note('fail', 'page', 'an unknown page path does not say so')

  // The card's Service block shows the log and the dropped count.
  await page.goto(`${BASE}/plugins`)
  await until(() => page.getByTestId('plugin-row').isVisible())
  await page.getByTestId('plugin-log').click()
  const lines = page.getByTestId('plugin-log-lines')
  if (!(await until(async () => ((await lines.textContent().catch(() => '')) ?? '').includes('event session.created'), 5000))) note('fail', 'service', 'the card does not show the server log')
  else pass('service', 'the card shows the server log')
  await page.screenshot({ path: join(SHOTS, 'service.png'), fullPage: true })

  // The process: started as the owner after enabling, its output on the card.
  await page.getByTestId('plugin-proc').click()
  const procState = page.getByTestId('plugin-process-state')
  if (!(await until(async () => ((await procState.textContent().catch(() => '')) ?? '').includes('Running'), 8000))) note('fail', 'process', `the process is not running: ${await procState.textContent().catch(() => '?')}`)
  const out = page.getByTestId('plugin-process-output')
  if (!(await until(async () => ((await out.textContent().catch(() => '')) ?? '').includes('tick'), 8000))) note('fail', 'process', 'the process output did not reach the card')
  else pass('process', 'the supervised process runs and its output is on the card')
  await page.screenshot({ path: join(SHOTS, 'process.png'), fullPage: true })

  // The door on the panel's port: the card names the mount and the socket
  // comes up; a token minted on the card opens it, nothing else does; the
  // cookie never reaches the process and its Set-Cookie never reaches the
  // browser.
  const door = page.getByTestId('plugin-door')
  if (!(await until(() => door.isVisible(), 5000))) note('fail', 'door', 'the card has no door block')
  if (!(await until(async () => ((await page.getByTestId('plugin-door-socket').textContent().catch(() => '')) ?? '').includes('opened'), 8000))) {
    note('fail', 'door', `the socket did not come up: ${await page.getByTestId('plugin-door-socket').textContent().catch(() => '?')}`)
  }
  await page.getByTestId('plugin-door-token-name').fill('the glasses')
  await page.getByTestId('plugin-door-mint').click()
  await until(() => page.getByTestId('plugin-door-token-value').isVisible(), 5000)
  const minted = (await page.getByTestId('plugin-door-token-value').textContent()) ?? ''
  const doorResult = await page.evaluate(async (tok) => {
    const bare = await fetch('/api/plugin-http/paper/x', { method: 'POST', body: 'hi' })
    const ok = await fetch('/api/plugin-http/paper/things/1?q=2', { method: 'POST', body: 'hi', headers: { Authorization: 'Bearer ' + tok } })
    const seen = ok.ok ? await ok.json() : null
    return { bare: bare.status, ok: ok.status, seen, cookieHeader: ok.headers.get('set-cookie'), nosniff: ok.headers.get('x-content-type-options') }
  }, minted)
  if (doorResult.bare !== 401) note('fail', 'door', `without a token the door answered ${doorResult.bare}`)
  if (doorResult.ok !== 200 || !doorResult.seen) note('fail', 'door', `with the token the door answered ${doorResult.ok}`)
  else if (doorResult.seen.cookie !== '' || doorResult.seen.auth !== '' || doorResult.seen.caller !== 'token:the glasses' || doorResult.seen.path !== '/things/1?q=2' || doorResult.seen.body !== 'hi') {
    note('fail', 'door', `what the process saw: ${JSON.stringify(doorResult.seen)}`)
  } else if (doorResult.cookieHeader) note('fail', 'door', 'the process set a cookie on the panel')
  else pass('door', `a token opens the door; the process saw ${JSON.stringify(doorResult.seen)}`)
  if (!(await until(async () => ((await page.getByTestId('plugin-door-token').first().textContent().catch(() => '')) ?? '').includes('last used'), 5000))) note('warn', 'door', 'the token row does not show its last use')
  await page.getByTestId('plugin-door-revoke').first().click()
  const revokeDialog = page.locator('[data-vp-modal]').last()
  await until(() => revokeDialog.isVisible())
  await revokeDialog.getByRole('button', { name: 'Revoke' }).click()
  const afterRevoke = await page.evaluate(async (tok) => (await fetch('/api/plugin-http/paper/x', { headers: { Authorization: 'Bearer ' + tok } })).status, minted)
  if (afterRevoke !== 401) note('fail', 'door', `a revoked token still opened the door: ${afterRevoke}`)
  else pass('door', 'a revoked token is refused')
  await page.screenshot({ path: join(SHOTS, 'door.png'), fullPage: true })

  // Rung 4: nothing loads until the switch is on; then the module draws into
  // the header and the rows and sees the state; ?safe=1 loads none of it.
  await page.goto(`${BASE}/`)
  await until(() => page.getByTestId('panel-tab-ext:paper:0').isVisible(), 8000)
  await sleep(500)
  if (await page.getByTestId('mod-header').isVisible().catch(() => false)) note('fail', 'module', 'a module loaded with the switch off')
  const sw = await api('GET', '/api/settings/plugin-unsandboxed')
  if (sw.body?.enabled !== false) note('fail', 'module', `the switch defaults to ${JSON.stringify(sw.body)}`)
  await page.goto(`${BASE}/plugins`)
  await until(() => page.getByTestId('plugins-unsandboxed-switch').isVisible(), 5000)
  await page.getByTestId('plugins-unsandboxed-switch').check()
  await until(async () => (await api('GET', '/api/settings/plugin-unsandboxed')).body?.enabled === true, 5000)
  await page.goto(`${BASE}/`)
  if (!(await until(() => page.getByTestId('mod-header').isVisible(), 10000))) note('fail', 'module', 'the module did not draw into the header slot')
  if (!(await until(() => page.getByTestId('mod-row').first().isVisible(), 5000))) note('fail', 'module', 'the module did not draw into a session row')
  const modSessions = await page.evaluate(() => document.documentElement.dataset.modSessions)
  if (!modSessions || Number(modSessions) < 1) note('fail', 'module', `host.state did not reach the module: ${modSessions}`)
  else pass('module', 'the switch on, the module draws into two slots and sees the state')
  await page.screenshot({ path: join(SHOTS, 'module.png') })
  await page.goto(`${BASE}/?safe=1`)
  await until(() => page.getByTestId('panel-tab-ext:paper:0').isVisible(), 8000)
  await sleep(800)
  if (await page.getByTestId('mod-header').isVisible().catch(() => false)) note('fail', 'module', 'safe mode loaded the module')
  else pass('module', 'safe mode loads no module')
  await page.getByTestId('plugins-unsandboxed-switch').isVisible().catch(() => false)
  await api('PUT', '/api/settings/plugin-unsandboxed', { enabled: false })

  // Dev mode: the draft directory is what runs, and a change reloads.
  await page.goto(`${BASE}/plugins`)
  await until(() => page.getByTestId('plugin-row').isVisible())
  await page.getByTestId('plugin-dev-on').click()
  await until(() => page.getByTestId('plugin-dev').isVisible(), 5000)
  await page.goto(`${BASE}/`)
  await page.getByTestId('panel-tab-ext:paper:0').click()
  const devInner = page.frameLocator('[data-testid="plugin-frame-paper-0"]')
  await until(async () => (await devInner.locator('#status').textContent()) === 'live', 10000)
  const fpBefore = (await api('GET', '/api/settings/plugins/paper/draft/fingerprint')).body
  writeFileSync(join(PLUGIN_DIR, 'pane.html'), '<!doctype html><p id="changed">changed by the agent</p>')
  // Read as text rather than by visibility: a frame mid-navigation answers a
  // visibility question with a throw, and the body's words are the fact.
  const reloaded = await until(
    async () => ((await devInner.locator('body').textContent().catch(() => '')) ?? '').includes('changed by the agent'),
    10000,
  )
  if (!reloaded) {
    const fpAfter = (await api('GET', '/api/settings/plugins/paper/draft/fingerprint')).body
    note('fail', 'dev', `the frame did not reload after the draft changed: fp ${JSON.stringify(fpBefore)} → ${JSON.stringify(fpAfter)}`)
  } else pass('dev', 'a draft change reloads the frame')
  await page.goto(`${BASE}/plugins`)
  await until(() => page.getByTestId('plugin-row').isVisible())
  await page.getByTestId('plugin-dev-off').click()
  await until(() => page.getByTestId('plugin-dev-on').isVisible(), 5000)

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

  // ── 7. New plugin: a name and a template become a project the panel opens ──
  // Last, because it leaves a second plugin and a project behind, and every
  // step above addresses `plugin-row` as the one row there is.
  await page.getByTestId('plugin-new').click()
  await until(() => page.getByTestId('plugin-new-tpl-service').isVisible(), 5000)
  await page.getByTestId('plugin-new-name').fill('Standup board')
  await page.getByTestId('plugin-new-tpl-service').click()
  await page.getByTestId('plugin-new-create').click()
  await until(() => page.getByTestId('launch-picker').isVisible(), 15000)
  if (!page.url().startsWith(`${BASE}/`) || page.url().includes('project=')) {
    note('fail', 'new', `the address still carries the hand-over: ${page.url()}`)
  }
  const named = await page.getByText('plugin-standup-board').first().isVisible().catch(() => false)
  if (!named) note('fail', 'new', 'the sidebar has no project called plugin-standup-board')
  else pass('new', 'the panel opened the launch picker for the new project')
  await page.keyboard.press('Escape')
  const made = await page.evaluate(async () => {
    const r = await fetch('/api/settings/plugins/standup-board')
    return r.ok ? r.json() : { status: r.status }
  })
  if (!made.dev || !made.sourceDir?.endsWith('/plugin-standup-board') || !made.rungs?.service) {
    note('fail', 'new', `the plugin is not a dev-mode service at its directory: ${JSON.stringify(made).slice(0, 200)}`)
  } else {
    // Installed with what the template asks for, the scaffolded server.js answers.
    const ok = await page.evaluate(async () => {
      const i = await fetch('/api/settings/plugins/standup-board/install', {
        method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ caps: ['read:panel'] }),
      })
      if (!i.ok) return `install ${i.status}`
      const r = await fetch('/api/ext/standup-board/summary')
      return r.ok ? JSON.stringify(await r.json()) : `summary ${r.status}`
    })
    if (!ok.includes('"sessions"')) note('fail', 'new', `the template's route did not answer: ${ok}`)
    else pass('new', `the scaffolded service answers its own route: ${ok.slice(0, 80)}`)
  }

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
