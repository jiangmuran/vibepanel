import { useState } from 'react'

import type { SharePage } from '../../protocol/wire'
import { t } from '../../i18n'
import { ShareLinks } from '../ShareLinks'
import { SharePages } from '../pages/SharePages'
import { Section } from './parts'

/**
 * The one surface that shows anything to somebody who is not signed in.
 *
 * A group of its own for a section, which is otherwise the shape to avoid.
 * Two reasons it earns it: the board editor inside it is the largest thing in
 * this dialog and wants the whole width, and red line 8 is about keeping this
 * surface visible as one thing rather than as a paragraph between two
 * unrelated ones.
 *
 * Pages come first because a link draws one: the list of links offers the
 * pages that exist, so the thing a link points at is made above it.
 */
export function SharingGroup({ onStartPage }: { onStartPage?: (page: SharePage) => void }) {
  // Bumped when the pages change, so the links' page picker re-reads them
  // instead of offering one that was deleted a moment ago.
  const [pagesVersion, setPagesVersion] = useState(0)
  return (
    <>
      <Section id="pages" title={t('page.title')}>
        <SharePages onStartPage={onStartPage} onChanged={() => setPagesVersion((n) => n + 1)} />
      </Section>
      <Section id="shares" title={t('share.title')}>
        <ShareLinks pagesVersion={pagesVersion} />
      </Section>
    </>
  )
}
