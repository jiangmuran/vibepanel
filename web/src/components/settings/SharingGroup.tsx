import type { SharePage } from '../../protocol/wire'
import { t } from '../../i18n'
import { Sharing } from '../pages/Sharing'
import { Section } from './parts'

/**
 * The one surface that shows anything to somebody who is not signed in.
 *
 * A group of its own for one section, which is otherwise the shape to avoid:
 * red line 8 is about keeping this surface visible as one thing rather than as
 * a paragraph between two unrelated ones. Pages and the links that show them
 * are one list, because a link cannot exist without the page it shows.
 */
export function SharingGroup({ onOpenPage }: { onOpenPage?: (page: SharePage, fresh: boolean) => void }) {
  return (
    <Section id="pages" title={t('page.title')}>
      <Sharing onOpenPage={onOpenPage} />
    </Section>
  )
}
