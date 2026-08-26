// Screenshots of the real thing, for looking at.
//
//   node scripts/shots.mjs [binary] [outdir]
//
// Not a check: nothing here passes or fails. It boots the panel exactly as
// render-check does, fills it with a plausible amount of work, and photographs
// every surface in both themes and at three widths.
//
// It exists because "the UI is ugly" is not actionable from source. Reading the
// JSX tells you what the classes are; it does not tell you that the right panel
// wastes a third of its width, that two greys are one grey apart, or that the
// sidebar's rows are 4px too tall to fit a phone. Those are visible and only
// visible.
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
const OUT = process.argv[3] ?? join(tmpdir(), 'vpshots')
mkdirSync(OUT, { recursive: true })

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vpshots-${process.pid}`
const DATA = mkdtempSync(join(tmpdir(), 'vpshots-'))
const HOME = mkdtempSync(join(tmpdir(), 'vpshots-home-'))
const PASSWORD = 'a sufficiently long password'
const BASE = `http://localhost:${PORT}`

const server = spawn(BIN, ['serve'], {
  env: {
    ...process.env,
    HOME,
    VIBEPANEL_DATA_DIR: DATA,
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
    VIBEPANEL_DOMAIN: 'localhost',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})
let log = ''
server.stdout.on('data', (d) => (log += d))
server.stderr.on('data', (d) => (log += d))

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
let browser
let cookie = ''

const authed = (path, init = {}) =>
  fetch(BASE + path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(cookie ? { Cookie: cookie } : {}),
      ...(init.headers ?? {}),
    },
  })

async function cleanup() {
  try { await browser?.close() } catch { /* gone */ }
  server.kill('SIGTERM')
  await sleep(400)
  try { spawn('tmux', ['-L', SOCKET, 'kill-server']).unref() } catch { /* none */ }
  for (const d of [DATA, HOME]) {
    try { rmSync(d, { recursive: true, force: true }) } catch { /* best effort */ }
  }
}
process.on('exit', () => { server.kill('SIGKILL') })

try {
  for (let i = 0; i < 120; i++) {
    try { if ((await fetch(BASE + '/api/health')).ok) break } catch { /* not up */ }
    await sleep(150)
  }

  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(log)?.[1]
  if (!token) throw new Error(`no setup token:\n${log}`)
  const setup = await fetch(BASE + '/api/auth/setup', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token, username: 'jmr', password: PASSWORD }),
  })
  cookie = (setup.headers.get('set-cookie') ?? '').split(';')[0]

  // Enough work on screen that the layout has to cope with something.
  const proj = await (await authed('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: process.cwd(), name: 'vibepanel' }),
  })).json()
  const other = await (await authed('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: HOME, name: 'notes' }),
  })).json()

  const mk = (projectId, cmd, title) =>
    authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId, command: cmd, title }),
    }).then((r) => r.json())

  // One that looks like an agent waiting on you, one working, one done, one
  // dead. The sidebar's whole job is telling these apart.
  const waiting = await mk(proj.id, ['sh', '-c',
    "printf '\\033[1m> refactor the auth flow\\033[0m\\n\\n'; " +
    "printf '  \\033[32m✓\\033[0m read internal/httpapi/auth.go\\n'; " +
    "printf '  \\033[32m✓\\033[0m read internal/store/auth.go\\n'; " +
    "printf '  \\033[33m?\\033[0m Apply this change to auth.go? \\033[2m(y/n)\\033[0m \\a'; " +
    'exec sleep 3000'], 'claude · auth')
  await mk(proj.id, ['sh', '-c',
    "printf '\\033[1m> add the directory picker\\033[0m\\n\\n'; " +
    "printf '  \\033[36m⠹\\033[0m writing web/src/components/DirectoryPicker.tsx\\n'; " +
    'exec sleep 3000'], 'claude · picker')
  await mk(proj.id, ['sh', '-c', "printf 'go test ./... \\n\\033[32mok\\033[0m  all packages\\n$ '; exec sh"], 'tests')
  await mk(other.id, ['sh', '-c', "printf '$ '; exec sh"], 'shell')
  await mk(proj.id, ['sh', '-c', "echo 'panic: nil map'; exit 2"], 'build')
  // A scratch terminal under the first session, so the bottom strip is real.
  await mk(proj.id, ['sh', '-c', "printf '$ '; exec sh"], 'logs').then(() => {})
  await authed('/api/sessions', {
    method: 'POST',
    body: JSON.stringify({ projectId: proj.id, parentSessionId: waiting.id, command: ['sh', '-c', "printf '$ '; exec sh"], title: 'logs' }),
  })
  await authed(`/api/projects/${proj.id}/notes`, {
    method: 'PUT',
    body: JSON.stringify({ content: '# 今天\n\n- 目录选择器做完了\n- 终端行距 1.2 → 1.0\n- 还差 PWA 通知', rev: 0 }),
  })
  for (const t of ['把右栏排版重做', '简体中文', 'PWA 通知']) {
    await authed(`/api/projects/${proj.id}/todos`, { method: 'POST', body: JSON.stringify({ text: t }) })
  }
  await sleep(2500)

  browser = await chromium.launch({ headless: true })

  const shoot = async (page, name) => {
    await sleep(500)
    await page.screenshot({ path: join(OUT, `${name}.png`) })
    console.log(`  ${name}.png`)
  }

  const login = async (page) => {
    await page.goto(BASE, { waitUntil: 'networkidle' })
    if (await page.locator('[data-testid="auth-submit"]').isVisible().catch(() => false)) {
      await page.locator('[data-testid="auth-username"]').fill('jmr')
      await page.locator('[data-testid="auth-password"]').fill(PASSWORD)
      await page.locator('[data-testid="auth-submit"]').click()
    }
    // The sidebar is a drawer on a phone, so waiting for it there waits
    // forever. The terminal is on every layout.
    await page.waitForSelector('[data-testid="sidebar"], .xterm-screen, [data-testid="compose"]', {
      timeout: 15000,
    })
    await sleep(2500)
  }

  for (const [theme, locale] of [['dark', 'zh-CN'], ['light', 'zh-CN'], ['dark', 'en-US']]) {
    const ctx = await browser.newContext({
      viewport: { width: 1600, height: 1000 },
      colorScheme: theme,
      // The language is detected from the browser, so photographing one locale
      // photographs one half of the product.
      locale,
      deviceScaleFactor: 2,
    })
    const page = await ctx.newPage()
    await page.addInitScript((t) => {
      try { localStorage.setItem('vibepanel.theme', t) } catch { /* private mode */ }
    }, theme)
    await login(page)
    const tag = `${theme}-${locale.slice(0, 2)}`
    await shoot(page, `desktop-${tag}`)

    // The right panel, one tab at a time.
    for (const tab of ['files', 'monitor', 'notes', 'todos']) {
      await page.locator(`[data-testid="panel-tab-${tab}"]`).click().catch(() => {})
      await shoot(page, `panel-${tab}-${tag}`)
    }

    if (theme === 'dark' && locale === 'zh-CN') {
      await page.locator('[data-testid="settings-open"]').click().catch(() => {})
      await shoot(page, 'settings')
      await page.locator('[data-testid="settings-close"]').click().catch(() => {})
      await sleep(400)
      // The picker, which is new.
      await page.locator('[data-testid="rail-add-project"], [title*="project" i], button:has-text("Add")').first()
        .click().catch(() => {})
      await sleep(700)
      if (await page.locator('[data-testid="dir-picker"]').isVisible().catch(() => false)) {
        await shoot(page, 'dir-picker')
        await page.locator('[data-testid="dir-cancel"]').click().catch(() => {})
      } else {
        console.log('  (could not open the directory picker; button not found)')
      }
    }
    await ctx.close()
  }

  // A phone.
  for (const [name, w, h] of [['phone', 390, 844], ['phone-small', 320, 720]]) {
    const ctx = await browser.newContext({
      viewport: { width: w, height: h },
      deviceScaleFactor: 3,
      isMobile: true,
      hasTouch: true,
      locale: 'zh-CN',
      colorScheme: 'dark',
    })
    const page = await ctx.newPage()
    await login(page)
    await shoot(page, name)
    await ctx.close()
  }

  console.log(`\nwrote to ${OUT}`)
} catch (err) {
  console.error(err?.stack ?? err)
  process.exitCode = 1
} finally {
  await cleanup()
}
