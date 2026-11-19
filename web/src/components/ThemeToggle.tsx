import { Amphora, Eclipse, Monitor, Moon, Palette, Sun } from 'lucide-react'

import { t, useLang } from '../i18n'
import { usePluginThemes } from './pluginThemes'
import { BUILT_IN_THEMES, isPluginTheme } from './theme'
import type { BuiltInTheme, ThemeChoice } from './theme'

// One glyph per built-in theme, so the button says which one is on without
// relying on what colour the button itself has become.
const ICONS: Record<'system' | BuiltInTheme, typeof Sun> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
  porcelain: Amphora,
  wujin: Eclipse,
}

/**
 * One button that walks system → each built-in theme → each plugin theme →
 * system.
 *
 * Its own file because three roots draw it: the panel's header, the sharing
 * page's and the chat page's. A page that keeps the panel's cookie but not its
 * theme control is a page that goes white at 2am, which is the hour the panel
 * is read.
 *
 * The plugin themes are read once per page load, here rather than by each
 * root, because the three roots would otherwise each need to know the list
 * exists; a theme installed while a tab is open joins the cycle when the
 * plugins revision changes (rev.ts), which also re-requests its stylesheet.
 */
export function ThemeToggle({
  theme,
  onChange,
}: {
  theme: ThemeChoice
  onChange: (t: ThemeChoice) => void
}) {
  const lang = useLang()
  const themes = usePluginThemes()

  const cycle: ThemeChoice[] = ['system', ...BUILT_IN_THEMES, ...themes.map((x) => x.attr as ThemeChoice)]
  // A chosen plugin theme that is no longer installed is still the choice on
  // screen; the next press leaves it for the start of the cycle.
  const at = cycle.indexOf(theme)
  const next = cycle[(at + 1) % cycle.length]
  const plugin = isPluginTheme(theme) ? themes.find((x) => x.attr === theme) : undefined
  const Icon = isPluginTheme(theme) ? Palette : ICONS[theme]
  const name = plugin ? (lang === 'zh' ? (plugin.name['zh-CN'] ?? plugin.name.en) : plugin.name.en) : theme
  const title = isPluginTheme(theme)
    ? t('plg.theme.pick', { name })
    : t('app.themeIs', { mode: t(`theme.${theme}`) })
  return (
    <button
      type="button"
      data-testid="theme-toggle"
      onClick={() => onChange(next)}
      title={title}
      className="vp-control"
    >
      <Icon size={15} />
    </button>
  )
}
