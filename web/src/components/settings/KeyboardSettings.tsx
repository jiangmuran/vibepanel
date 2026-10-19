import { Keyboard } from 'lucide-react'

import { t } from '../../i18n'
import { requestShortcuts } from '../keyprefs'
import { SideArrowsChoice } from '../ShortcutsDialog'
import { Section } from './parts'

/** The one choice the keys have, and the way to the whole list of them. */
export function KeyboardSettings() {
  return (
    <Section id="keys" title={t('keys.title')}>
      <SideArrowsChoice />
      <button
        type="button"
        data-testid="shortcuts-open"
        onClick={requestShortcuts}
        className="vp-press mt-3 flex items-center gap-2 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
      >
        <Keyboard size={14} />
        {t('keys.open')}
      </button>
    </Section>
  )
}
