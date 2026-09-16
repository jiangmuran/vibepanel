import { Monitor, Moon, Sun } from 'lucide-react'

import { t } from '../i18n'
import type { ThemeChoice } from './theme'

/**
 * One button that walks system → light → dark → system.
 *
 * Its own file because two roots draw it: the panel's header and the sharing
 * page's. A page that keeps the panel's cookie but not its theme control is a
 * page that goes white at 2am, which is the hour the panel is read.
 */
export function ThemeToggle({
  theme,
  onChange,
}: {
  theme: ThemeChoice
  onChange: (t: ThemeChoice) => void
}) {
  const next: Record<ThemeChoice, ThemeChoice> = { system: 'light', light: 'dark', dark: 'system' }
  const Icon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor
  return (
    <button
      type="button"
      data-testid="theme-toggle"
      onClick={() => onChange(next[theme])}
      title={t('app.themeIs', { mode: t(`theme.${theme}`) })}
      className="vp-control"
    >
      <Icon size={15} />
    </button>
  )
}
