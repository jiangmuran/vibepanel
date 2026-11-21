import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import { t } from '../../i18n'
import { Section } from './parts'

/** The values the server accepts; see archiveIdleChoices in archive.go. */
const CHOICES = [0, 14, 30, 60, 90] as const

/**
 * When a project leaves the sidebar by itself.
 *
 * Off by default: a project disappearing overnight is something a person
 * should have chosen. Radios rather than a select, for the reason PasteSettings
 * gives -- there are five and the whole set fits.
 */
export function ArchiveSettings() {
  const [days, setDays] = useState<number | null>(null)
  const [saved, setSaved] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    api
      .settings()
      .then((s) => setDays(s.archiveIdleDays))
      .catch((e: unknown) => setErr(String(e)))
  }, [])

  const save = (next: number) => {
    setDays(next)
    setSaved(false)
    setErr('')
    api
      .saveArchiveIdle(next)
      .then(() => setSaved(true))
      .catch((e: unknown) => setErr(String(e)))
  }

  return (
    <Section id="archive" title={t('archive.idleTitle')}>
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('archive.idleHint')}</p>
      <fieldset>
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {CHOICES.map((n) => (
            <label key={n} className="flex items-center gap-1.5 text-vp-base text-ink">
              <input
                type="radio"
                name="archive-idle"
                checked={days === n}
                disabled={days === null}
                onChange={() => save(n)}
                data-testid={`archive-idle-${n}`}
              />
              <span>{n === 0 ? t('archive.idleOff') : t('archive.idleDays', { n })}</span>
            </label>
          ))}
        </div>
      </fieldset>
      {saved && <p className="mt-2 text-vp-sm text-done-ink">{t('paste.saved')}</p>}
      {err && <p className="mt-2 text-vp-sm text-danger-ink">{err}</p>}
    </Section>
  )
}
