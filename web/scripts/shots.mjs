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
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
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

// Every seeding call goes through here, and a non-2xx is fatal.
//
// It used to return the response and nobody looked. The note this seeds stopped
// arriving at some point -- the panel was photographed with an empty notes tab
// and its placeholder showing -- and the screenshots looked like a feature that
// did not work rather than a fixture that had not run. A seed that fails
// silently produces pictures of an empty application, which is the same failure
// as a check that stops checking.
const authed = async (path, init = {}) => {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(cookie ? { Cookie: cookie } : {}),
      ...(init.headers ?? {}),
    },
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`seeding ${init.method ?? 'GET'} ${path}: ${res.status} ${body.slice(0, 200)}`)
  }
  return res
}

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
    body: JSON.stringify({ path: HOME, name: 'dotfiles' }),
  })).json()

  const mk = (projectId, cmd, title) =>
    authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId, command: cmd, title }),
    }).then((r) => r.json())

  // One that looks like an agent waiting on you, one working, one done, one
  // dead. The sidebar's whole job is telling these apart.
  //
  // The transcript below is long on purpose. A terminal photographed with four
  // lines in it is a photograph of the background colour: the line gap, the
  // wrapping, the dim-vs-bold contrast and the whole palette are invisible
  // until something is actually on the screen. Every line-height and colour
  // problem found so far was found in a full terminal, none in an empty one.
  //
  // It is written to a file and `cat`-ed rather than printf-ed, because the
  // command crosses JS, JSON, Go's argv and sh on its way to the pane, and
  // every quote in it would otherwise have to survive all four.
  const e = String.fromCharCode(27)
  const B = `${e}[1m`, D = `${e}[2m`, R = `${e}[0m`
  const G = `${e}[32m`, Y = `${e}[33m`, C = `${e}[36m`
  const transcript = [
    `${B}> make a token and a cookie take the same path in${R}`,
    `${B}  currentUser, and keep the token ahead of the cookie${R}`,
    ``,
    `  ${G}OK${R} read ${C}internal/httpapi/auth.go${R} ${D}214 lines${R}`,
    `  ${G}OK${R} read ${C}internal/store/auth.go${R} ${D}331 lines${R}`,
    `  ${G}OK${R} grep ${C}currentUser${R} ${D}11 matches across 4 files${R}`,
    ``,
    `  Both credentials answer one question, so they should meet before the`,
    `  handler rather than inside each one. Token first: a request carrying`,
    `  both is a program, and the program meant the token.`,
    ``,
    `  ${D}internal/httpapi/auth.go${R}`,
    `  ${D}@@ -140,6 +140,10 @@ func (h *Handler) currentUser(${R}`,
    `  ${G}+  if tok := bearerToken(r); tok != "" {${R}`,
    `  ${G}+    return h.store.UserByAPIToken(r.Context(), tok)${R}`,
    `  ${G}+  }${R}`,
    `     c, err := r.Cookie(sessionCookie)`,
    `     if err != nil {`,
    `       return nil, false`,
    ``,
    `  ${G}OK${R} go build ./...              ${D}1.9s${R}`,
    `  ${G}OK${R} go test ./internal/httpapi  ${D}ok  0.42s${R}`,
    ``,
    // Box drawing and block characters, because they are where a renderer
    // shows its seams. On xterm's DOM renderer these join up only when the
    // cell size lands on whole pixels, and the hairlines of background that
    // show through the rest of the time are the "cracks" that made the panel
    // look broken.
    `  ${D}╭────────────────────────────────────────────╮${R}`,
    `  ${D}│${R}  ${G}████████████████████${R}${D}░░░░░░░░░░${R}  73%   ${D}│${R}`,
    `  ${D}│${R}  ▁▂▃▄▅▆▇█▇▆▅▄▃▂▁  ▛▀▜ ▙▄▟ ▚▞ ▐▌ ▄▀       ${D}│${R}`,
    `  ${D}╰────────────────────────────────────────────╯${R}`,
    ``,
    `  ${Y}?${R} Apply this change to ${C}internal/httpapi/auth.go${R}?`,
    `    ${D}1${R} yes   ${D}2${R} yes, and stop asking   ${D}3${R} no, tell me why`,
    ``,
    `  ${Y}>${R} `,
  ].join('\n')
  const scriptDir = join(HOME, 'shots')
  mkdirSync(scriptDir, { recursive: true })
  const transcriptFile = join(scriptDir, 'transcript')
  writeFileSync(transcriptFile, transcript)
  const logFile = join(scriptDir, 'log')
  writeFileSync(logFile, [
    `${D}12:04:31${R} serve  listening on 127.0.0.1:8443 ${D}tls=off${R}`,
    `${D}12:04:31${R} tmux   socket vibepanel ${D}3.6${R}  ${G}6 sessions adopted${R}`,
    `${D}12:07:02${R} ws     client attached ${D}session=vp_a3f1 130x46${R}`,
    `${D}12:07:19${R} hook   ${C}vp_a3f1${R} -> ${Y}waiting${R}`,
    ``,
  ].join('\n'))

  const waiting = await mk(proj.id, ['sh', '-c',
    // The bell is what makes this one `waiting` without a hook installed.
    `cat ${transcriptFile}; printf '\\a'; exec sleep 3000`], 'claude \u00b7 auth')
  await mk(proj.id, ['sh', '-c',
    `printf '${B}> add the directory picker${R}\\n\\n'; ` +
    `printf '  ${C}...${R} writing web/src/components/DirectoryPicker.tsx\\n'; ` +
    'exec sleep 3000'], 'claude \u00b7 picker')
  await mk(proj.id, ['sh', '-c', `printf 'go test ./...\\n${G}ok${R}  all packages\\n'; exec sh`], 'tests')
  await mk(other.id, ['sh', '-c', 'exec sh'], 'shell')
  await mk(proj.id, ['sh', '-c', "echo 'panic: nil map'; exit 2"], 'build')
  // A scratch terminal under the first session, so the bottom strip is real.
  await mk(proj.id, ['sh', '-c', 'exec sh'], 'logs').then(() => {})
  await authed('/api/sessions', {
    method: 'POST',
    body: JSON.stringify({ projectId: proj.id, parentSessionId: waiting.id, command: ['sh', '-c', `cat ${logFile}; exec sh`], title: 'logs' }),
  })
  await authed(`/api/projects/${proj.id}/notes`, {
    method: 'PUT',
    // No baseRev: an unconditional write is what a seeder wants. It sent
    // `rev: 0`, which is the *response* field name, and the server rejected the
    // unknown field -- so every screenshot since had an empty notes tab.
    body: JSON.stringify({ content: '# 今天\n\n- 目录选择器做完了\n- 终端行距 1.2 → 1.0\n- 还差 PWA 通知' }),
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
