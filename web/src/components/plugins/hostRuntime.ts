import { getLang, tKey } from '../../i18n'
import type { Lang } from '../../i18n'
import { api } from '../../protocol/api'
import type { PanelState, PluginModule } from '../../protocol/wire'
import { showToast } from '../toasts'
import { requestOpen } from './open'

/**
 * Rung 4: the host object an unsandboxed module is given, and the loader
 * (docs/plugins.md §7).
 *
 * A module is `export default function (host) {…}`, loaded on the panel's own
 * origin after the SPA, as the owner. Nothing here limits it -- it can import
 * anything the bundle exports and reach anything the owner can -- and the
 * install screen says so in red. What this file does is give it a stable,
 * small surface to build on so it does not have to reach into the bundle
 * (`host.v` is the only contract), and bound the blast radius: every slot
 * renderer runs inside a try/catch so a throw is one empty box; a module that
 * fails to load is a toast; `?safe=1` loads nothing at all.
 *
 * Slots are DOM placeholders the SPA renders (`[data-host-slot]`), filled by a
 * MutationObserver here rather than by React: the module's element is its own,
 * and React never reconciles inside it.
 */

export const HOST_VERSION = 1

export type SlotRenderer = (ctx: { slot: string; session?: string; project?: string }) => Element | null | undefined

interface Registration {
  plugin: string
  slot: string
  render: SlotRenderer
}

const registrations: Registration[] = []
let stateListeners = new Set<(s: PanelState) => void>()
let lastState: PanelState | null = null
let observer: MutationObserver | null = null

/** Whether this tab asked for safe mode: no plugin theme, no module. */
export function safeMode(search = typeof location === 'undefined' ? '' : location.search): boolean {
  return /[?&]safe=1(&|$)/.test(search)
}

/** App hands the state here on every snapshot, for host.state.subscribe. */
export function publishHostState(s: PanelState) {
  lastState = s
  for (const fn of stateListeners) {
    try {
      fn(s)
    } catch (e) {
      console.warn('plugin state listener', e)
    }
  }
}

/** One module's host object. Each module gets its own, so an i18n key or a
 *  slot registration is attributed to the plugin that made it. */
export function hostFor(plugin: string, name: string) {
  const prefix = `${plugin}.`
  return {
    v: HOST_VERSION,
    plugin,
    api,
    get lang(): Lang {
      return getLang()
    },
    get theme(): string {
      return document.documentElement.dataset.theme ?? ''
    },
    state: {
      get current(): PanelState | null {
        return lastState
      },
      subscribe(fn: (s: PanelState) => void): () => void {
        stateListeners.add(fn)
        if (lastState) fn(lastState)
        return () => {
          stateListeners.delete(fn)
        }
      },
    },
    slots: {
      /** Draw into a named placeholder. The element returned is the module's own. */
      add(slot: string, render: SlotRenderer): () => void {
        const reg: Registration = { plugin, slot, render }
        registrations.push(reg)
        fillSlots()
        return () => {
          const i = registrations.indexOf(reg)
          if (i >= 0) registrations.splice(i, 1)
          if (typeof document === 'undefined') return
          for (const el of document.querySelectorAll<HTMLElement>(`[data-host-filled="${plugin}:${slot}"]`)) el.remove()
        }
      },
    },
    /** Inject a stylesheet. Rung 4's "change how a piece behaves" is mostly this. */
    css(text: string): () => void {
      const el = document.createElement('style')
      el.dataset.hostCss = plugin
      el.textContent = String(text)
      document.head.appendChild(el)
      return () => el.remove()
    },
    i18n: {
      /** Keys are prefixed with the plugin's id by the host; a collision is refused. */
      add(entries: Record<string, { zh: string; en: string }>) {
        for (const [k, v] of Object.entries(entries)) {
          const key = k.startsWith(prefix) ? k : prefix + k
          if (tKey(key) !== null || hostDict.has(key)) throw new Error(`i18n key ${key} is taken`)
          hostDict.set(key, v)
        }
      },
      t(key: string): string {
        const full = key.startsWith(prefix) ? key : prefix + key
        const e = hostDict.get(full)
        return e ? (getLang() === 'zh' ? e.zh : e.en) : tKey(full) ?? full
      },
    },
    toast(text: string, kind: 'info' | 'success' | 'error' = 'info') {
      showToast({ kind, key: 'plg.notice', params: { name, text: String(text).slice(0, 500) } })
    },
    open(what: { session?: string; project?: string; settings?: string }) {
      requestOpen({ plugin, ...what })
    },
  }
}

const hostDict = new Map<string, { zh: string; en: string }>()

/** Fill every placeholder that has a renderer and is not filled yet. */
function fillSlots(root?: ParentNode) {
  if (typeof document === 'undefined') return
  root = root ?? document
  for (const el of root.querySelectorAll<HTMLElement>('[data-host-slot]')) {
    const slot = el.dataset.hostSlot ?? ''
    for (const reg of registrations) {
      if (reg.slot !== slot) continue
      const mark = `${reg.plugin}:${slot}`
      if (el.querySelector(`[data-host-filled="${mark}"]`)) continue
      try {
        const made = reg.render({ slot, session: el.dataset.session, project: el.dataset.project })
        if (!made) continue
        const box = document.createElement('span')
        box.dataset.hostFilled = mark
        box.className = 'contents'
        box.appendChild(made)
        el.appendChild(box)
      } catch (e) {
        console.warn(`plugin ${reg.plugin} slot ${slot}`, e)
      }
    }
  }
}

function watchSlots() {
  if (observer) return
  observer = new MutationObserver((muts) => {
    for (const m of muts) {
      for (const n of m.addedNodes) {
        if (n instanceof HTMLElement) {
          if (n.matches('[data-host-slot]')) fillSlots(n.parentNode ?? document)
          else fillSlots(n)
        }
      }
    }
  })
  observer.observe(document.body, { childList: true, subtree: true })
}

/**
 * Load every module the server lists, once per page. Never under safe mode,
 * and only on the panel's own page (the caller decides). A module that
 * fails to import is a toast naming it; one outside its tested range loads
 * with a toast saying so.
 */
export async function loadModules(): Promise<PluginModule[]> {
  if (safeMode()) return []
  let list: PluginModule[]
  try {
    list = await api.pluginModules()
  } catch {
    return []
  }
  if (list.length === 0) return []
  watchSlots()
  for (const m of list) {
    const name = getLang() === 'zh' ? (m.name['zh-CN'] ?? m.name.en) : m.name.en
    try {
      // The one dynamic import in this codebase, and the whole point of the
      // rung: code the panel does not carry, run as the owner.
      const mod = (await import(/* @vite-ignore */ m.url)) as { default?: unknown }
      if (typeof mod.default !== 'function') throw new Error('the module has no default export')
      ;(mod.default as (h: ReturnType<typeof hostFor>) => void)(hostFor(m.plugin, name))
      if (!m.within) showToast({ kind: 'info', key: 'plg.module.outside', params: { name, tested: m.tested } })
    } catch (e) {
      showToast({ kind: 'error', key: 'plg.module.failed', params: { name, why: e instanceof Error ? e.message : String(e) } })
    }
  }
  fillSlots()
  return list
}

/** For tests: forget every registration and listener. */
export function resetHost() {
  registrations.length = 0
  stateListeners = new Set()
  lastState = null
  hostDict.clear()
}
