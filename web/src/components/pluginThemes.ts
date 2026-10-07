import { useEffect, useState } from 'react'

import { api } from '../protocol/api'
import type { PluginThemeRow } from '../protocol/wire'
import { onPluginsRev } from './plugins/rev'

/**
 * The installed plugin themes, shared by every theme toggle on the page and
 * re-read when the plugins revision changes (rev.ts), so a theme installed
 * while a tab is open joins the cycle without a reload.
 */

let cached: PluginThemeRow[] | null = null
const listeners = new Set<() => void>()

function refreshPluginThemes() {
  api.pluginThemes().then(
    (list) => {
      cached = list
      for (const fn of listeners) fn()
    },
    () => {},
  )
}

onPluginsRev(refreshPluginThemes)

export function usePluginThemes(): PluginThemeRow[] {
  const [themes, setThemes] = useState<PluginThemeRow[]>(cached ?? [])
  useEffect(() => {
    const fn = () => setThemes(cached ?? [])
    listeners.add(fn)
    if (cached === null) refreshPluginThemes()
    else fn()
    return () => {
      listeners.delete(fn)
    }
  }, [])
  return themes
}
