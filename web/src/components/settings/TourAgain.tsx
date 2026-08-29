import { useState } from 'react'
import { GraduationCap } from 'lucide-react'

import { api } from '../../protocol/api'
import { t } from '../../i18n'
import { Section } from './parts'

/**
 * Opens the first-run tour again.
 *
 * It is dismissed for good on purpose -- it is read once per person, not once
 * per browser -- and "for good" needs a way back or it is a thing people are
 * afraid to close. The two steps that do something are also the two worth
 * revisiting: the reporters, and Claude Code's settings.
 *
 * Reloads rather than opening the tour in place. Whether it shows is decided
 * once when the panel mounts, from the same payload the settings page reads,
 * and a second source of truth for that is how it comes back at the wrong
 * moment.
 */
export function TourAgain() {
  const [busy, setBusy] = useState(false)
  return (
    <Section id="tour" title={t('tour.title')}>
      <p className="mb-2 text-vp-sm text-ink-2">{t('tour.againWhat')}</p>
      <button
        type="button"
        disabled={busy}
        data-testid="tour-again"
        onClick={() => {
          setBusy(true)
          api
            .tourAgain()
            .then(() => window.location.reload())
            .catch(() => setBusy(false))
        }}
        className="vp-press flex items-center gap-2 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
      >
        <GraduationCap size={14} />
        {t('tour.again')}
      </button>
    </Section>
  )
}
