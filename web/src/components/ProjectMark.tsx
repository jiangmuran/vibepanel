import { ExternalLink } from 'lucide-react'

import type { GitRemote } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { githubURL } from './repo'
import { safeText } from './text'

/**
 * A project's name, and where its code lives if that is somewhere nameable.
 *
 * 「read only和面板左下角等等地方 都加上GitHub链接和项目名」. Two surfaces show
 * this — the foot of the sidebar and the read-only dashboard's header — and
 * they show it the same way because it is the same fact.
 *
 * The link is `owner/name` in words with an "leaves this page" glyph, not a
 * brand mark. Two reasons and both are practical: the icon set this project
 * uses dropped its brand icons at v1, and `acme/payroll` says which repository
 * where a logo says only "GitHub" — which the reader could already guess from
 * the fact that there is a link at all.
 *
 * A project with no remote, or one on a host this panel does not link to, gets
 * the name alone. That has to look like an answer rather than a missing link,
 * which is why the glyph belongs to the link and never to the row: a row with a
 * name and no icon is a name; a row with an icon and no target is broken.
 */
export function ProjectMark({
  name,
  remote,
  testid,
}: {
  name: string
  remote: GitRemote | null
  testid?: string
}) {
  useLang()
  const url = githubURL(remote)
  return (
    <span data-testid={testid} className="flex min-w-0 items-center gap-2 text-vp-xs">
      <span className="min-w-0 shrink truncate text-ink" title={safeText(name)}>
        {safeText(name)}
      </span>
      {url !== null && remote !== null && (
        <a
          href={url}
          target="_blank"
          rel="noreferrer noopener"
          data-testid={testid ? `${testid}-link` : undefined}
          title={t('repo.openOn', { what: `${remote.owner}/${remote.name}` })}
          // `.vp-tap` because this is an `a[href]`, and the blanket coarse-pointer
          // floor in styles.css covers `button` and `[role=button]` only — an
          // anchor sails past it. The render check measured this one at 28px
          // tall in the phone drawer, which is the shape that rule exists for.
          className="vp-control vp-tap min-w-0 shrink gap-1 px-1 text-ink-2"
        >
          <span className="min-w-0 truncate">
            {safeText(`${remote.owner}/${remote.name}`)}
          </span>
          <ExternalLink size={10} className="shrink-0" />
        </a>
      )}
    </span>
  )
}
