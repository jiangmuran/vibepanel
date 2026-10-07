import type { Lang } from '../../i18n'
import type { PluginLine, PluginRow, PluginScreen, PluginText, PluginRungs } from '../../protocol/wire'

/**
 * The install screen's arithmetic, without React, so it has a test.
 *
 * The screen is drawn from `PluginScreen`, which the server builds with
 * `plugins.Describe`: the lines, in order, and which button. What this file
 * decides is only what a browser has to decide — which boxes are ticked right
 * now, what to send when the button is pressed, and which heading a line goes
 * under — and none of it adds a line the server did not say.
 */

/** A plugin's text in the reader's language, falling back to English. */
export function textIn(text: PluginText | undefined, lang: Lang): string {
  if (!text) return ''
  return (lang === 'zh' ? text['zh-CN'] : undefined) ?? text.en
}

/** The heading each kind of line is drawn under, in the screen's order. */
export const HEADINGS = ['what', 'rung', 'cap', 'host', 'runs', 'keeps', 'danger'] as const

export type Heading = (typeof HEADINGS)[number]

/** The lines grouped under their headings, keeping the server's order inside each. */
export function grouped(screen: PluginScreen): { heading: Heading; lines: PluginLine[] }[] {
  const out: { heading: Heading; lines: PluginLine[] }[] = []
  for (const heading of HEADINGS) {
    const lines = screen.lines.filter((l) => l.kind === heading)
    if (lines.length > 0) out.push({ heading, lines })
  }
  return out
}

/** The boxes as the screen first shows them: what the server marked granted. */
export function initialTicks(screen: PluginScreen): Set<string> {
  const ticks = new Set<string>()
  for (const l of screen.lines) {
    if (l.checkable && l.granted && l.code) ticks.add(l.code)
  }
  return ticks
}

/** A checkable line's capability name. A host line's is `net:<host>`. */
export function capOf(line: PluginLine): string | null {
  if (!line.checkable || !line.code) return null
  return line.kind === 'host' ? `net:${line.code}` : line.code
}

/** What the confirm sends: every ticked box, in the screen's order. */
export function capsToSend(screen: PluginScreen, ticks: Set<string>): string[] {
  const out: string[] = []
  for (const l of screen.lines) {
    const cap = capOf(l)
    if (cap && ticks.has(l.code!)) out.push(cap)
  }
  return out
}

/** Whether the button is pressable: not refused, and not already busy. */
export function canConfirm(screen: PluginScreen, busy: boolean): boolean {
  return !busy && !screen.refused
}

/** What the card says about a plugin's state, as a key and a tone. */
export function stateOf(p: PluginRow): { key: 'enabled' | 'disabled' | 'new' | 'upgrade'; tone: 'accent' | 'plain' | 'warn'; v: number } {
  if (p.installedVersion === 0) return { key: 'new', tone: 'warn', v: p.latestVersion }
  if (p.latestVersion > p.installedVersion) return { key: 'upgrade', tone: 'warn', v: p.latestVersion }
  if (p.enabled) return { key: 'enabled', tone: 'accent', v: p.installedVersion }
  return { key: 'disabled', tone: 'plain', v: p.installedVersion }
}

/** Which rungs a plugin climbs, by their words, in ladder order. */
export function rungsOf(p: { rungs: PluginRungs }): ('theme' | 'panel' | 'service' | 'process' | 'unsandboxed')[] {
  const r = p.rungs
  const out: ('theme' | 'panel' | 'service' | 'process' | 'unsandboxed')[] = []
  if (r.theme) out.push('theme')
  if (r.panel) out.push('panel')
  if (r.service) out.push('service')
  if (r.process) out.push('process')
  if (r.unsandboxed) out.push('unsandboxed')
  return out
}

/** The lines of a list setting, as typed: one per line, blanks dropped. */
export function listLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter((s) => s !== '')
}
