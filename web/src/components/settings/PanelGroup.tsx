import { useRef } from 'react'
import { EnvSettings } from './EnvSettings'
import { TimeZone } from './TimeZone'
import { RestartPanel } from './RestartPanel'
import { TourAgain } from './TourAgain'
import { KeyboardSettings } from './KeyboardSettings'
import { RendererSettings } from './RendererSettings'
import { LocalEchoSettings } from './LocalEchoSettings'
import type { SettingsInfo } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { formatBytes } from '../bytes'
import { UpdateSection } from '../UpdateSection'
import { useSlidingMarker } from '../useSlidingMarker'
import { Row, Section } from './parts'

function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m ${seconds % 60}s`
}

/** Two weeks, matching the window the server logs a warning in. */
const CERT_WARN_MS = 14 * 24 * 60 * 60 * 1000

function certLabel(unixSeconds: number): string {
  const at = new Date(unixSeconds * 1000)
  const left = at.getTime() - Date.now()
  if (left <= 0) return `expired ${at.toLocaleDateString()}`
  const days = Math.floor(left / 86_400_000)
  return `${at.toLocaleDateString()} · ${days} day${days === 1 ? '' : 's'} left`
}

function certTone(unixSeconds: number): 'normal' | 'warn' | 'bad' {
  const left = unixSeconds * 1000 - Date.now()
  if (left <= 0) return 'bad'
  return left < CERT_WARN_MS ? 'warn' : 'normal'
}

/**
 * Facts about this installation, and the one action that changes them.
 *
 * Read-only for anything that lives in a flag: the panel is started by a
 * systemd unit or a compose file, and a setting that could be changed in two
 * places is a setting that disagrees with itself after the next restart.
 *
 * `info` is null until the first poll answers, which is why the status block is
 * conditional and the update block is not — one of them is a view of the
 * server and the other is a button.
 */
import type { ThemeChoice } from '../theme'
import { BUILT_IN_THEMES } from '../theme'

function swatchBg(c: 'system' | (typeof BUILT_IN_THEMES)[number]): string {
  if (c === 'light') return '#f4f5f7'
  if (c === 'dark') return '#131417'
  if (c === 'porcelain') return '#f3f0ea'
  if (c === 'wujin') return '#191816'
  return ['linear-gradient(135deg,', '#f4f5f7', '50%,', '#131417', '50%)'].join(' ')
}

export function PanelGroup({
  info,
  theme,
  onThemeChange,
}: {
  info: SettingsInfo | null
  theme?: ThemeChoice
  onThemeChange?: (next: ThemeChoice) => void
}) {
  const lang = useLang()
  const choices = ['system', ...BUILT_IN_THEMES] as const
  const themeRef = useRef<HTMLDivElement>(null)
  const marker = useSlidingMarker(themeRef, `${theme ?? ''}:${lang}`)
  return (
    <>
      {theme !== undefined && onThemeChange && (
        <div className="mb-6">
          <h3 className="mb-2 text-vp-base font-semibold tracking-tight text-ink">{t('app.theme')}</h3>
          <div
            ref={themeRef}
            className="vp-segmented w-fit max-w-full"
            role="radiogroup"
            aria-label={t('app.theme')}
            data-testid="settings-theme-picker"
            data-marker={marker ? 'on' : 'off'}
          >
            {marker && (
              <span
                aria-hidden="true"
                className="vp-marker-2d"
                style={{
                  width: marker.width,
                  height: marker.height,
                  transform: `translate3d(${marker.left}px, ${marker.top}px, 0)`,
                }}
              />
            )}
            {choices.map((c) => (
              <button
                key={c}
                type="button"
                role="radio"
                aria-checked={theme === c}
                data-active={theme === c ? 'true' : 'false'}
                data-testid={`settings-theme-${c}`}
                onClick={() => onThemeChange(c)}
                className="vp-tab shrink-0 gap-1.5 px-2.5 text-vp-sm whitespace-nowrap"
              >
                <span
                  aria-hidden="true"
                  className="h-2.5 w-2.5 shrink-0 rounded-full border border-hairline-strong"
                  style={{ background: swatchBg(c) }}
                />
                {t(`theme.${c}`)}
              </button>
            ))}
          </div>
        </div>
      )}
      <Section id="update" title={t('upd.title')}>
        <UpdateSection />
      </Section>
      {info && (
        <Section id="status" title={t('set.status')}>
          <div data-testid="settings-status">
            <Row label={t('set.version')} value={`${info.version} (${info.commit})`} />
            <Row label={t('set.uptime')} value={duration(info.uptime)} />
            <Row
              label={t('set.sessions')}
              value={`${info.sessions} on tmux ${info.tmuxVersion} · ${info.attached} attached`}
            />
            <Row label={t('set.viewers')} value={String(info.viewers)} />
            <Row label={t('set.socket')} value={info.tmuxSocket} />
            {/* The half of an upgrade nothing else can see.
                `vibepanel doctor` reports this too, and nobody runs doctor after a
                `systemctl restart`. This is where a person looks instead, and the
                cost of the remedy is stated with it rather than after they have
                typed it: kill-server ends every session on the socket. */}
            {(info.tmuxConfigStale || info.tmuxConfigUnknown) && (
              <div
                data-testid="tmux-config-stale"
                className="border-t border-hairline py-2 text-vp-base leading-relaxed text-warn-ink"
              >
                <span className="mr-1 text-ink-2">{t('set.tmuxConfigLabel')}</span>
                {info.tmuxConfigStale ? t('set.tmuxConfigStale') : t('set.tmuxConfigUnknown')}
                {info.tmuxConfigStale && (
                  <code className="mt-1 block font-mono text-vp-sm text-ink">
                    tmux -L {safeText(info.tmuxSocket)} kill-server
                  </code>
                )}
              </div>
            )}
            <Row label={t('set.data')} value={`${info.dataDir} · ${formatBytes(info.dbBytes)}`} />
            <Row label={t('set.listening')} value={`${info.addr} → ${info.url}`} />
            <Row
              label={t('set.tls')}
              value={info.tlsMode === 'off' ? 'off' : `${info.tlsMode} · ${info.domain}`}
            />
            {/* A certificate nobody renewed does not announce itself; it simply
                stops working one morning. The panel warns in its log as the date
                approaches, but a log on a machine nobody reads is not where this
                should first be noticed. */}
            {info.certExpiry !== undefined && (
              <Row
                label={t('set.cert')}
                value={certLabel(info.certExpiry)}
                tone={certTone(info.certExpiry)}
              />
            )}
            <Row
              label={t('set.access')}
              value={info.allowAll ? 'any address' : 'restricted by --allow-from'}
            />
            <Row label={t('set.signedIn')} value={info.username} />
          </div>
        </Section>
      )}
      <TimeZone info={info} />
      <EnvSettings />
      <RestartPanel />
      <KeyboardSettings />
      <RendererSettings />
      <LocalEchoSettings />
      <TourAgain />
    </>
  )
}
