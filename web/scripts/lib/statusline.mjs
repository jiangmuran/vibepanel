import { join } from 'node:path'
import { rows as screenRows } from './screen.mjs'

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

/** A quiet alternate screen must survive revealing a passive viewer. */
export async function checkStatusline({ browser, ctx, page, base, mkSession, authed, shots, note }) {
  const home = await page.locator('[data-testid="main-terminal"]:visible').first()
    .getAttribute('data-main-session')
  if (!home) throw new Error('no initial session for the status-line check')
  const marker = 'STATUSLINE_MODEL | work | ctx 25%'
  const footer = await mkSession(['sh', '-c',
    "printf '\\033[?1049h'; " +
    "paint() { printf '\\033[2J\\033[HSTATUSLINE_FIXTURE\\033[999;1HSTATUSLINE_MODEL | work | ctx 25%%\\033[H'; }; " +
    'trap paint WINCH; paint; while :; do sleep 1; done'], 'statusline-regression')
  if (!footer.id) throw new Error('the status-line fixture was not created')
  const viewer = await browser.newContext({
    serviceWorkers: 'block',
    // Phone layout puts the session rows in a closed drawer. This check needs
    // a smaller desktop viewer, so it can switch sessions through the sidebar.
    viewport: { width: 960, height: 700 },
    deviceScaleFactor: 1.3,
  })
  let passive
  try {
    await viewer.addCookies(await ctx.cookies())
    await page.bringToFront()
    await page.locator(`[data-testid="session-row"][data-session-id="${footer.id}"]`).click()
    await page.waitForFunction(({ id, marker }) =>
      window.vibepanelScreen?.({ id })?.at(-1)?.includes(marker),
    { id: footer.id, marker }, { timeout: 10000 })

    passive = await viewer.newPage()
    await passive.goto(base, { waitUntil: 'networkidle' })
    await passive.locator(`[data-testid="session-row"][data-session-id="${footer.id}"]`).click()
    await passive.locator('[data-testid="take-control"]:visible').waitFor({ timeout: 8000 })
    await passive.locator(
      `[data-testid="main-terminal"][data-main-session="${footer.id}"] [data-testid="terminal-load-progress"]`,
    ).waitFor({ state: 'hidden' })
    await sleep(1000)
    const authoritative = ((await (await authed('/api/state')).json()).sessions ?? [])
      .find(s => s.id === footer.id)
    if (!authoritative || authoritative.rows <= 0) throw new Error('no status-line fixture grid')

    for (let i = 0; i < 3; i++) {
      await passive.locator(`[data-testid="session-row"][data-session-id="${home}"]`).click()
      await sleep(250)
      await passive.locator(`[data-testid="session-row"][data-session-id="${footer.id}"]`).click()
      // Let both the reveal frame and ResizeObserver run, without using the
      // window resize that repaired the screen in the original report.
      await passive.evaluate(() => new Promise(resolve =>
        requestAnimationFrame(() => requestAnimationFrame(resolve))))
      await sleep(250)
      const rows = await screenRows(passive, { id: footer.id })
      if (rows.length !== authoritative.rows || rows.at(-1)?.trimEnd() !== marker) {
        throw new Error(
          `FOOTER_LOST: switch ${i + 1}: ${rows.length} local rows for ${authoritative.rows} server rows; ` +
          `last row ${JSON.stringify(rows.at(-1))}`,
        )
      }
    }
    await passive.screenshot({ path: join(shots, 'statusline-after-switch.png') })
    note('PASS', 'statusline', 'a passive viewer kept the last row across three session switches')
  } catch (error) {
    await passive?.screenshot({ path: join(shots, 'statusline-failure.png') }).catch(() => {})
    throw error
  } finally {
    await viewer.close()
    await page.bringToFront()
    await page.locator(`[data-testid="session-row"][data-session-id="${home}"]`)
      .click({ timeout: 5000 })
      .catch(error => note('FAIL', 'statusline/cleanup', String(error)))
    await authed(`/api/sessions/${footer.id}`, { method: 'DELETE' })
  }
}
