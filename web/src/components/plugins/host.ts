import type { LucideIcon } from 'lucide-react'
import {
  Activity,
  Bell,
  Bot,
  CalendarDays,
  ChartBar,
  Eye,
  Flag,
  Globe,
  ListChecks,
  Megaphone,
  MessageSquare,
  Puzzle,
  Rocket,
  Sparkles,
  Star,
  Timer,
  Wrench,
  Zap,
} from 'lucide-react'

import type { PluginPanelSpec, PluginRow } from '../../protocol/wire'
import { extTab } from '../chrome'
import type { ExtTab } from '../chrome'

/**
 * The host's side of a plugin frame, without React, so it has a test
 * (docs/plugins.md §5).
 *
 * Three things are decided here and nowhere else: which messages a frame may
 * send and what each must look like, which of them a plugin's grants allow,
 * and how a manifest's panels become the panel's own tabs and sections.
 * Nothing a frame sends decides anything the host then fetches on its own
 * credential: a request is rebuilt from its checked fields, never forwarded.
 */

/**
 * The icons a manifest may name. A fixed map rather than a lookup into
 * lucide's thousand, because the bundle carries only what it imports and a
 * dynamic import is a thing this project does not do. An unknown name is
 * the puzzle piece.
 */
export const PLUGIN_ICONS: Record<string, LucideIcon> = {
  'list-checks': ListChecks,
  sparkles: Sparkles,
  puzzle: Puzzle,
  bot: Bot,
  'calendar-days': CalendarDays,
  bell: Bell,
  'chart-bar': ChartBar,
  'message-square': MessageSquare,
  rocket: Rocket,
  timer: Timer,
  flag: Flag,
  star: Star,
  zap: Zap,
  globe: Globe,
  wrench: Wrench,
  activity: Activity,
  eye: Eye,
  megaphone: Megaphone,
}

export function pluginIcon(name: string | undefined): LucideIcon {
  return (name && PLUGIN_ICONS[name]) || Puzzle
}

/** One of a plugin's frames, placed. */
export interface PluginSlotEntry {
  plugin: PluginRow
  spec: PluginPanelSpec
  /** The n-th entry of the manifest's panels, which names a pane tab. */
  n: number
}

/** A plugin that runs: enabled with an installed version, or in dev mode. */
export function runs(p: PluginRow): boolean {
  return (p.enabled && p.installedVersion > 0) || (p.dev && p.sourceDir !== '')
}

/** Every frame of every running plugin at one slot, in install order. */
export function entriesAt(plugins: PluginRow[], slot: string): PluginSlotEntry[] {
  const out: PluginSlotEntry[] = []
  for (const p of plugins) {
    if (!runs(p) || !p.panels) continue
    p.panels.forEach((spec, n) => {
      if (spec.slot === slot) out.push({ plugin: p, spec, n })
    })
  }
  return out
}

/** The side panel's plugin tabs, with what the strip draws for each. */
export interface ExtTabMeta {
  id: ExtTab
  entry: PluginSlotEntry
}

export function extTabs(plugins: PluginRow[]): ExtTabMeta[] {
  return entriesAt(plugins, 'sidepanel.pane').map((entry) => ({ id: extTab(entry.plugin.id, entry.n), entry }))
}

/** The page a plugin mounts at `/x/<path>`, if any running plugin has one. */
export function pageEntry(plugins: PluginRow[], path: string): PluginSlotEntry | null {
  return entriesAt(plugins, 'page').find((e) => e.spec.path === path) ?? null
}

// ─── what a frame may say ─────────────────────────────────────────────────

export type FrameMessage =
  | { type: 'ready' }
  | { type: 'height'; px: number }
  | { type: 'error'; message: string }
  | { type: 'open'; id: number; session?: string; project?: string; settings?: string }
  | { type: 'notify'; id: number; text: string; kind: 'info' | 'success' | 'error' }
  | { type: 'confirm'; id: number; title: string; body: string; confirm: string; cancel: string; destructive: boolean }
  | { type: 'menu'; id: number; items: { id: string; label: string; destructive: boolean }[] }

const HANDLE = /^[0-9a-f]{16}$/
const MAX_TEXT = 500

function str(v: unknown, max = MAX_TEXT): string {
  return typeof v === 'string' ? v.slice(0, max) : ''
}

/**
 * A message out of the frame, rebuilt from its checked fields, or null.
 *
 * Rebuilt rather than passed through: what arrives is whatever the plugin's
 * script put in `postMessage`, and the host only ever acts on the shape it
 * knows. A handle is sixteen hex characters; text is bounded; a kind is one
 * of three words; an id is a positive integer the frame uses to match the
 * answer.
 */
export function parseFrameMessage(raw: unknown): FrameMessage | null {
  if (typeof raw !== 'object' || raw === null) return null
  const m = raw as Record<string, unknown>
  const id = typeof m.id === 'number' && Number.isInteger(m.id) && m.id > 0 ? m.id : 0
  switch (m.type) {
    case 'ready':
      return { type: 'ready' }
    case 'height': {
      const px = typeof m.px === 'number' && Number.isFinite(m.px) ? Math.max(0, Math.min(4000, Math.round(m.px))) : 0
      return { type: 'height', px }
    }
    case 'error':
      return { type: 'error', message: str(m.message) }
    case 'open': {
      if (!id) return null
      const out: FrameMessage = { type: 'open', id }
      if (typeof m.session === 'string' && HANDLE.test(m.session)) out.session = m.session
      if (typeof m.project === 'string' && HANDLE.test(m.project)) out.project = m.project
      if (typeof m.settings === 'string' && /^[a-z]{2,20}$/.test(m.settings)) out.settings = m.settings
      if (!out.session && !out.project && !out.settings) return null
      return out
    }
    case 'notify': {
      if (!id) return null
      const kind = m.kind === 'success' || m.kind === 'error' ? m.kind : 'info'
      const text = str(m.text)
      if (!text) return null
      return { type: 'notify', id, text, kind }
    }
    case 'confirm': {
      if (!id) return null
      const title = str(m.title, 120)
      if (!title) return null
      return {
        type: 'confirm', id, title, body: str(m.body), confirm: str(m.confirm, 40), cancel: str(m.cancel, 40),
        destructive: m.destructive === true,
      }
    }
    case 'menu': {
      if (!id || !Array.isArray(m.items)) return null
      const items = m.items.slice(0, 20).flatMap((it) => {
        if (typeof it !== 'object' || it === null) return []
        const r = it as Record<string, unknown>
        const label = str(r.label, 80)
        if (!label) return []
        return [{ id: str(r.id, 40) || label, label, destructive: r.destructive === true }]
      })
      if (items.length === 0) return null
      return { type: 'menu', id, items }
    }
  }
  return null
}

/** Which capability a message needs, or null for one every frame may send. */
export function capFor(msg: FrameMessage): string | null {
  if (msg.type === 'open') return 'ui:open'
  if (msg.type === 'notify') return 'ui:notify'
  return null
}

/** Whether a plugin's grants allow a message. */
export function allowed(plugin: PluginRow, msg: FrameMessage): boolean {
  const cap = capFor(msg)
  return cap === null || plugin.granted.includes(cap)
}

// ─── dev mode ─────────────────────────────────────────────────────────────

/**
 * Whether a draft has settled on something new: the fingerprint changed and
 * then read the same twice. An agent writing three files in 450 ms is one
 * reload, not three, which pages-check measures for the Preview pane and
 * plugins-check measures for a frame.
 */
export function settled(readings: string[], loaded: string): boolean {
  if (readings.length < 2) return false
  const last = readings[readings.length - 1]
  return last !== loaded && last === readings[readings.length - 2]
}
