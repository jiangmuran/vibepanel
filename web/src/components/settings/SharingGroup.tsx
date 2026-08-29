import { t } from '../../i18n'
import { ShareLinks } from '../ShareLinks'
import { Section } from './parts'

/**
 * The one surface that shows anything to somebody who is not signed in.
 *
 * A group of its own for a section, which is otherwise the shape to avoid.
 * Two reasons it earns it: the board editor inside it is the largest thing in
 * this dialog and wants the whole width, and red line 8 is about keeping this
 * surface visible as one thing rather than as a paragraph between two
 * unrelated ones.
 */
export function SharingGroup() {
  return (
    <Section id="shares" title={t('share.title')}>
      <ShareLinks />
    </Section>
  )
}
