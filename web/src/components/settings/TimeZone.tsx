import { useState } from 'react'

import { api } from '../../protocol/api'
import type { SettingsInfo } from '../../protocol/wire'
import { t } from '../../i18n'
import { showToast } from '../toasts'
import { Section } from './parts'

/**
 * What the panel calls a day.
 *
 * It decides more than a label. Every usage number is bucketed by a
 * `YYYY-MM-DD` string written when the transcript was read, and "today", "this
 * week" and the heatmap are all string comparisons against it — so this
 * setting is the boundary those buckets are cut on, not a display preference.
 * Changing it re-reads the history, which is why the button says so.
 *
 * The browser's own zone is offered as one press because it is right almost
 * every time: the person configuring this is usually sitting in the zone they
 * want the day to follow. It is a suggestion rather than the default, because
 * the panel is often on a machine somewhere else and a wall in another country
 * should not silently re-cut a year of history the first time somebody opens
 * the settings page on holiday.
 */
export function TimeZone({ info }: { info: SettingsInfo | null }) {
  const here = typeof Intl === 'undefined' ? '' : Intl.DateTimeFormat().resolvedOptions().timeZone
  const [draft, setDraft] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const current = info?.timezone ?? ''
  const value = draft ?? current

  const save = (zone: string) => {
    setBusy(true)
    api
      .setTimeZone(zone)
      .then((r) => {
        setDraft(null)
        showToast(
          r.rebuilt > 0
            ? { kind: 'success', key: 'tzone.rebuilding', detail: r.nowLabel }
            : { kind: 'success', key: 'tzone.saved', detail: r.nowLabel },
        )
      })
      .catch((e: unknown) => showToast({ kind: 'error', key: 'tzone.failed', detail: String(e) }))
      .finally(() => setBusy(false))
  }

  return (
    <Section id="timezone" title={t('tzone.title')}>
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('tzone.what')}</p>

      <div className="mb-2 flex flex-wrap items-center gap-2">
        <input
          value={value}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={t('tzone.machine')}
          data-testid="tz-input"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
        />
        <button
          type="button"
          disabled={busy || value === current}
          onClick={() => save(value)}
          data-testid="tz-save"
          className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
        >
          {t('tzone.save')}
        </button>
      </div>

      {here !== '' && here !== current && (
        <button
          type="button"
          disabled={busy}
          onClick={() => save(here)}
          data-testid="tz-use-browser"
          className="vp-press mb-2 rounded-vp px-2 py-1 text-vp-sm text-ink-2 underline-offset-2 hover:text-ink hover:underline disabled:opacity-50"
        >
          {t('tzone.useBrowser', { zone: here })}
        </button>
      )}

      <p className="text-vp-sm text-ink-3" data-testid="tz-today">
        {t('tzone.todayIs', { day: info?.panelDay ?? '—' })}
      </p>
      {/* Said before it is pressed, not after. Re-reading a year of
          transcripts is seconds of disk and it happens once; being surprised
          by it is the part worth avoiding. */}
      <p className="mt-1 text-vp-sm text-ink-3">{t('tzone.rescan')}</p>
    </Section>
  )
}
