import { useCallback, useEffect, useRef, useState } from 'react'
import {
  BookOpen,
  Download,
  FileUp,
  FolderCog,
  FolderOpen,
  GitFork,
  History,
  PanelsTopLeft,
  Plus,
  ShieldCheck,
  SlidersHorizontal,
  Trash2,
  Upload,
} from 'lucide-react'

import { api } from '../../protocol/api'
import type {
  Project,
  Session,
  ShareLink,
  SharePage,
  SharePageCatalogue,
  SharePageDetail,
  SharePageRow,
  SharePagesRoot,
  SharingSettings,
} from '../../protocol/wire'
import { t, useLang, type Key, type Lang } from '../../i18n'
import { askConfirm } from '../ask'
import { safeText } from '../text'
import { Menu } from '../Menu'
import { ManageDialog } from './ManageDialog'
import { Field, PageLinks } from './PageLinks'
import { BlockTitle, Chip, Empty, Mark } from './bits'

/**
 * Sharing, as one list: pages, and under each page the links that show it.
 *
 * A page is a directory an agent writes (under the data directory, as a
 * `page-…` project) and a history of published versions. A link is an address
 * for one screen that shows a page. There is nothing else to share: a link
 * cannot exist without a page, so a link is made on the page it shows.
 */

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** How often pages and links are re-read while this is on screen: for the
 *  viewer counts, which are true for a few seconds each, and for a draft an
 *  agent is changing. */
const LIST_MS = 5000

const TEMPLATE_LABEL: Record<string, Key> = {
  blank: 'page.tpl.blank',
  wall: 'page.tpl.wall',
  spend: 'page.tpl.spend',
  built: 'page.tpl.built',
  glance: 'page.tpl.glance',
  kiosk: 'page.tpl.kiosk',
}

/** Everything a page can do beyond reading, and what keeps it from the panel. */
const ARCHITECTURE_URL = 'https://github.com/jiangmuran/vibepanel/blob/main/docs/page-backend.md'

/** The long-form docs, in the reader's language. */
function docsURL(lang: Lang): string {
  return lang === 'zh'
    ? 'https://github.com/jiangmuran/vibepanel/blob/main/docs/features.zh-CN.md#给别人看的屏幕'
    : 'https://github.com/jiangmuran/vibepanel/blob/main/docs/features.md#screens-for-other-people'
}

/** The directory name the server makes of a page name (httpapi dirName), for
 *  the placeholder: letters and digits of any script, the rest a dash. */
function slugOf(name: string): string {
  return [...name.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-+|-+$/g, '')]
    .slice(0, 40)
    .join('')
    .replace(/-+$/, '')
}

export function Sharing({
  onOpenPage,
}: {
  /** Opens a page's project, with its Preview, starting an agent when
   *  `fresh` or when nothing is running in it. */
  onOpenPage?: (page: SharePage, fresh: boolean) => void
}) {
  const lang = useLang()
  const [pages, setPages] = useState<SharePageRow[]>([])
  const [notice, setNotice] = useState('')
  const importInput = useRef<HTMLInputElement>(null)
  const [links, setLinks] = useState<ShareLink[]>([])
  const [details, setDetails] = useState<Record<string, SharePageDetail>>({})
  const [catalogue, setCatalogue] = useState<SharePageCatalogue | null>(null)
  const [projects, setProjects] = useState<Project[]>([])
  const [sessions, setSessions] = useState<Session[]>([])
  const [creating, setCreating] = useState(false)
  const [sharing, setSharing] = useState<SharingSettings | null>(null)
  const [error, setError] = useState('')

  const fail = useCallback((e: unknown) => setError(e instanceof Error ? e.message : String(e)), [])

  const refresh = useCallback(async () => {
    try {
      const [p, l] = await Promise.all([api.listPages(), api.listShares()])
      setPages(p)
      setLinks(l)
      const read = await Promise.all(p.map((row) => api.page(row.id).catch(() => null)))
      setDetails(Object.fromEntries(read.filter((d) => d !== null).map((d) => [d.page.id, d])))
    } catch (e) {
      fail(e)
    }
  }, [fail])

  useEffect(() => {
    let cancelled = false
    const tick = () => {
      if (!cancelled && !document.hidden) void refresh()
    }
    // Through a timer of zero rather than a call in the effect body, which the
    // hooks lint reads as a setState during the effect.
    const first = window.setTimeout(tick, 0)
    const timer = window.setInterval(tick, LIST_MS)
    api.pageCatalogue().then(
      (c) => {
        if (!cancelled) setCatalogue(c)
      },
      () => {},
    )
    api.sharingSettings().then(
      (v) => {
        if (!cancelled) setSharing(v)
      },
      () => {},
    )
    // For the scope picker. A form that cannot fill it still works: the whole
    // panel needs no list.
    api.state().then(
      (s) => {
        if (cancelled) return
        setProjects(s.projects)
        setSessions(s.sessions)
      },
      () => {},
    )
    return () => {
      cancelled = true
      clearTimeout(first)
      clearInterval(timer)
    }
  }, [refresh])

  const importZip = async (file: File) => {
    try {
      const made = await api.importPage(file)
      setError('')
      setNotice(t('page.imported', { name: made.page.name }))
      await refresh()
    } catch (e) {
      setNotice('')
      fail(e)
    }
  }

  return (
    <div data-testid="sharing" className="@container">
      {/* The name of this page, what it is for, and the two things you start
          from -- on one line at any width with room for it. The title was
          drawn by the frame while the description was drawn here, which is
          why the two of them used to sit apart with the buttons stranded
          between them. */}
      <header className="mb-4 flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="min-w-0 max-w-2xl">
          <h1 className="text-vp-xl font-semibold tracking-tight text-ink">{t('page.title')}</h1>
          <p className="mt-1 text-vp-base leading-relaxed text-ink-2">
            {t('page.why')}{' '}
            <a
              href={docsURL(lang)}
              target="_blank"
              rel="noreferrer noopener"
              data-testid="sharing-docs"
              className="inline-flex items-center gap-1 whitespace-nowrap text-accent hover:underline"
            >
              <BookOpen size={12} />
              {t('page.docs')}
            </a>{' '}
            <a
              href={ARCHITECTURE_URL}
              target="_blank"
              rel="noreferrer noopener"
              data-testid="sharing-architecture"
              className="inline-flex items-center gap-1 whitespace-nowrap text-accent hover:underline"
            >
              <ShieldCheck size={12} />
              {t('page.architecture')}
            </a>
          </p>
        </div>
        <span className="flex shrink-0 items-center gap-2">
          <input
            ref={importInput}
            type="file"
            accept=".zip,application/zip"
            hidden
            data-testid="page-import-file"
            onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = ''
              if (file) void importZip(file)
            }}
          />
          <button
            type="button"
            data-testid="page-import"
            onClick={() => importInput.current?.click()}
            title={t('page.importWhy')}
            className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2.5 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink"
          >
            <FileUp size={13} />
            {t('page.import')}
          </button>
          {!creating && (
            <button
              type="button"
              data-testid="page-new"
              onClick={() => setCreating(true)}
              className="vp-press flex items-center gap-1 rounded-vp px-3 py-1.5 text-vp-base font-medium"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              <Plus size={13} />
              {t('page.create')}
            </button>
          )}
        </span>
      </header>

      {/* The two settings that are about sharing rather than about any one
          page, in one quiet strip. They were two loose lines between the
          heading and the list, which read as its first two rows. */}
      <div className="mb-4 flex flex-col gap-2 rounded-vp border border-hairline bg-surface px-3 py-2 @3xl:flex-row @3xl:items-center @3xl:gap-4">
        <PagesRootLine
          root={catalogue?.pagesRootInfo ?? null}
          onChanged={(next) => setCatalogue((c) => (c ? { ...c, pagesRoot: next.dir, pagesRootInfo: next } : c))}
          onError={fail}
        />
        <span className="hidden h-4 w-px shrink-0 bg-hairline @3xl:block" aria-hidden="true" />
        <VisitorWritesLine settings={sharing} onChanged={setSharing} onError={fail} />
      </div>

      {notice && (
        <p className="mb-2 text-vp-base text-ink-2" data-testid="sharing-notice">
          {safeText(notice)}
        </p>
      )}

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }} data-testid="sharing-error">
          {safeText(error)}
        </p>
      )}

      {creating && (
        <NewPage
          catalogue={catalogue}
          onCancel={() => setCreating(false)}
          onMade={(page, startAgent) => {
            setCreating(false)
            setError('')
            void refresh()
            if (startAgent) onOpenPage?.(page, true)
          }}
          onError={fail}
        />
      )}

      {pages.length === 0 && !creating ? (
        <Empty icon={PanelsTopLeft}>{t('page.none')}</Empty>
      ) : (
        // One card per page, with its links inside it. Cards rather than
        // rows separated by a rule, because this is a page on the panel's
        // ground now rather than a list inside a dialog, and a page with a
        // form and three links unfolded under it needs an edge that says
        // where it ends and the next one begins.
        <div className="grid gap-3">
          {pages.map((page) => (
            <PageCard
              key={page.id}
              page={page}
              detail={details[page.id] ?? null}
              links={links.filter((l) => l.pageId === page.id)}
              projects={projects}
              sessions={sessions}
              visitorWrites={sharing?.visitorWrites ?? true}
              onOpen={() => onOpenPage?.(page, false)}
              onChanged={() => {
                setError('')
                void refresh()
              }}
              onError={fail}
            />
          ))}
        </div>
      )}
    </div>
  )
}

/**
 * Where new pages go, in one line, and the way to change it.
 *
 * Collapsed to a sentence because most people never need it: unset, pages go
 * under the panel's data directory and nothing is stored. When the directory
 * someone chose stops working, the line says so without being opened.
 */
function PagesRootLine({
  root,
  onChanged,
  onError,
}: {
  root: SharePagesRoot | null
  onChanged: (next: SharePagesRoot) => void
  onError: (e: unknown) => void
}) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  if (!root) return null

  const save = async (dir: string) => {
    setBusy(true)
    try {
      onChanged(await api.setPagesRoot(dir))
      setEditing(false)
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  const sourceLabel =
    root.source === 'setting' ? t('page.rootSetting') : root.source === 'default' ? t('page.rootDefault') : t('page.rootFallback')

  return (
    <div data-testid="pages-root" className="min-w-0 flex-1 text-vp-sm text-ink-3">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <FolderCog size={12} className="shrink-0" />
        <span>{t('page.root')}</span>
        <code className="min-w-0 truncate font-mono text-ink-2" data-testid="pages-root-dir">
          {safeText(root.dir)}
        </code>
        <span data-testid="pages-root-source" data-source={root.source}>
          {sourceLabel}
        </span>
        {!editing && (
          <button
            type="button"
            data-testid="pages-root-change"
            onClick={() => {
              setValue(root.setting)
              setEditing(true)
            }}
            className="vp-press rounded-vp px-1.5 py-0.5 text-ink-2 hover:text-ink"
          >
            {t('page.rootChange')}
          </button>
        )}
      </div>
      {root.problem && (
        <p className="mt-1" style={{ color: 'var(--vp-state-waiting)' }} data-testid="pages-root-problem">
          {t('page.rootProblem', { why: safeText(root.problem) })}
        </p>
      )}
      {editing && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <input
            data-testid="pages-root-input"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void save(value.trim())
            }}
            placeholder={t('page.rootPlaceholder')}
            className={`${INPUT} min-w-0 flex-1 font-mono @md:max-w-xl`}
          />
          <button
            type="button"
            disabled={busy}
            data-testid="pages-root-save"
            onClick={() => void save(value.trim())}
            className="vp-press rounded-vp px-2.5 py-1 text-vp-base disabled:opacity-40"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('page.rootSave')}
          </button>
          {root.setting !== '' && (
            <button
              type="button"
              disabled={busy}
              data-testid="pages-root-reset"
              onClick={() => void save('')}
              className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2 hover:text-ink disabled:opacity-40"
            >
              {t('page.rootReset')}
            </button>
          )}
          <button
            type="button"
            onClick={() => setEditing(false)}
            className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2 hover:text-ink"
          >
            {t('share.cancel')}
          </button>
        </div>
      )}
    </div>
  )
}

/**
 * The panel-wide switch for visitor actions, in one line.
 *
 * Off refuses every action on every link at once, whatever each link says:
 * the answer to "something is writing to a wall and I do not know which".
 */
function VisitorWritesLine({
  settings,
  onChanged,
  onError,
}: {
  settings: SharingSettings | null
  onChanged: (next: SharingSettings) => void
  onError: (e: unknown) => void
}) {
  const [busy, setBusy] = useState(false)
  if (!settings) return null
  const toggle = async (on: boolean) => {
    setBusy(true)
    try {
      onChanged(await api.setSharingSettings({ visitorWrites: on }))
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div
      data-testid="visitor-writes"
      className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 text-vp-sm text-ink-3"
    >
      <label className="flex items-center gap-1.5 text-ink-2" title={t('sharing.visitorWritesWhy')}>
        <input
          type="checkbox"
          role="switch"
          data-testid="visitor-writes-toggle"
          disabled={busy}
          checked={settings.visitorWrites}
          aria-checked={settings.visitorWrites}
          onChange={(e) => void toggle(e.target.checked)}
        />
        {t('sharing.visitorWrites')}
      </label>
      <span data-testid="visitor-writes-state" data-on={settings.visitorWrites}>
        {settings.visitorWrites ? t('sharing.visitorWritesOn') : t('sharing.visitorWritesOff')}
      </span>
    </div>
  )
}

function NewPage({
  catalogue,
  onCancel,
  onMade,
  onError,
}: {
  catalogue: SharePageCatalogue | null
  onCancel: () => void
  onMade: (page: SharePage, startAgent: boolean) => void
  onError: (e: unknown) => void
}) {
  const [name, setName] = useState('')
  const [template, setTemplate] = useState('blank')
  const [dir, setDir] = useState('')
  const [startAgent, setStartAgent] = useState(true)
  const [busy, setBusy] = useState(false)
  const where = catalogue?.pagesRoot ? `${catalogue.pagesRoot}/page-${slugOf(name) || 'page'}` : ''

  const create = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    setBusy(true)
    try {
      const page = await api.createPage({ name: trimmed, template, sourceDir: dir.trim() })
      onMade(page, startAgent)
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      data-testid="page-form"
      className="vp-panel-in mb-3 rounded-vp-lg border border-accent/40 bg-surface p-4 shadow-sm"
    >
      <div className="mb-3 flex items-center gap-2">
        <Plus size={14} className="shrink-0 text-ink-3" />
        <h3 className="text-vp-md font-medium text-ink">{t('page.create')}</h3>
      </div>
      <div className="mb-3 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2">
        <Field label={t('page.nameLabel')} htmlFor="page-name">
          <input
            id="page-name"
            data-testid="page-new-name"
            value={name}
            maxLength={64}
            autoFocus
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void create()
            }}
            placeholder={t('page.namePlaceholder')}
            className={INPUT}
          />
        </Field>
        <Field label={t('page.template')} htmlFor="page-template">
          <select
            id="page-template"
            data-testid="page-new-template"
            value={template}
            onChange={(e) => setTemplate(e.target.value)}
            className={INPUT}
          >
            {(catalogue?.templates ?? [{ id: 'blank', sections: [] }]).map((tpl) => (
              <option key={tpl.id} value={tpl.id}>
                {TEMPLATE_LABEL[tpl.id] ? t(TEMPLATE_LABEL[tpl.id]) : tpl.id}
              </option>
            ))}
            <option value="">{t('page.adopt')}</option>
          </select>
        </Field>
        <div className="@md:col-span-2">
          <Field label={t('page.dir')} htmlFor="page-dir">
            <input
              id="page-dir"
              data-testid="page-new-dir"
              value={dir}
              onChange={(e) => setDir(e.target.value)}
              placeholder={template === '' ? t('page.dirExisting') : where}
              className={`${INPUT} font-mono`}
            />
          </Field>
        </div>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-hairline pt-3">
        <label className="flex items-center gap-2 text-vp-sm text-ink-2">
          <input
            type="checkbox"
            data-testid="page-new-agent"
            checked={startAgent}
            onChange={(e) => setStartAgent(e.target.checked)}
          />
          {t('page.startAgent')}
        </label>
        <span className="flex items-center gap-2">
          <button type="button" onClick={onCancel} className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2">
            {t('share.cancel')}
          </button>
          <button
            type="button"
            data-testid="page-create"
            disabled={busy || name.trim() === '' || (template === '' && dir.trim() === '')}
            onClick={() => void create()}
            className="vp-press rounded-vp px-3 py-1.5 text-vp-base disabled:opacity-40"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('page.create')}
          </button>
        </span>
      </div>
    </div>
  )
}

function PageCard({
  page,
  detail,
  links,
  projects,
  sessions,
  visitorWrites,
  onOpen,
  onChanged,
  onError,
}: {
  page: SharePageRow
  detail: SharePageDetail | null
  links: ShareLink[]
  projects: Project[]
  sessions: Session[]
  visitorWrites: boolean
  onOpen: () => void
  onChanged: () => void
  onError: (e: unknown) => void
}) {
  const [versions, setVersions] = useState(false)
  const [busy, setBusy] = useState(false)
  const [managing, setManaging] = useState(false)
  const caps = detail?.capabilities
  const manageable = caps !== undefined && (caps.data || caps.admin || caps.sources || caps.server)

  // Asked in the panel's own dialog: an inline second step cannot live in a
  // menu, and this is the one action here that cannot be undone. The server
  // refuses while any link still draws the page, so the question is about the
  // versions rather than about a wall going dark.
  const remove = async () => {
    const yes = await askConfirm({
      title: t('page.deleteTitle', { name: safeText(page.name) }),
      body: t('page.deleteBody'),
      confirm: t('page.delete'),
      cancel: t('ask.cancel'),
      destructive: true,
    })
    if (yes) await act(() => api.deletePage(page.id))
  }

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
      onChanged()
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    // One card per page: a header that says which page and what to do with
    // it, then the links that show it. It was a row of a name, a status, two
    // buttons and five glyphs, all at one weight, with the links indented
    // underneath -- which is why nothing on it said what to press.
    <article
      data-testid="page-row"
      data-page={page.id}
      className="rounded-vp-lg border border-hairline bg-surface text-vp-base shadow-sm"
    >
      <header className="flex flex-wrap items-start gap-x-3 gap-y-2 p-3 @md:p-4">
        <Mark icon={PanelsTopLeft} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="min-w-0 truncate text-vp-md font-medium text-ink">{safeText(page.name)}</h3>
            {/* Published or not, as a chip beside the name rather than as a
                word at the far end of the row: it is the first thing somebody
                needs, because an unpublished page has no links yet. */}
            <Chip tone={page.publishedVersion > 0 ? 'accent' : 'warn'} testid="page-status">
              {page.publishedVersion > 0
                ? t('page.published', { v: page.publishedVersion })
                : t('page.unpublished')}
            </Chip>
          </div>
          <code className="mt-0.5 block truncate font-mono text-vp-xs text-ink-3" data-testid="page-dir">
            {safeText(page.sourceDir)}
          </code>
          {!page.sourceExists && (
            <p className="mt-0.5 text-vp-xs" style={{ color: 'var(--vp-state-waiting)' }} data-testid="page-dir-gone">
              {page.publishedVersion > 0
                ? t('page.dirGoneRestore', { v: page.publishedVersion })
                : t('page.dirGone')}
            </p>
          )}
        </div>
        {/* What somebody does every day, in words; the rest behind one button.
            Its own line in a narrow card, so the page's name is never the
            thing that gives way to the buttons. */}
        <div className="flex w-full shrink-0 flex-wrap items-center justify-end gap-1.5 @xl:w-auto">
          <button
            type="button"
            data-testid="page-open"
            disabled={busy}
            onClick={onOpen}
            title={page.sourceExists ? t('page.openWhy') : t('page.restoreWhy')}
            className="vp-press flex items-center gap-1 rounded-vp border border-accent/50 px-2.5 py-1 text-vp-sm text-accent transition-colors duration-200 ease-vp hover:border-accent disabled:opacity-40"
          >
            <FolderOpen size={12} />
            {page.sourceExists ? t('page.open') : t('page.restore')}
          </button>
          <button
            type="button"
            data-testid="page-publish"
            disabled={busy || !page.sourceExists}
            onClick={() => void act(() => api.publishPage(page.id, ''))}
            title={t('page.publishWhy')}
            className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2.5 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink disabled:opacity-40"
          >
            <Upload size={12} />
            {t('page.publish')}
          </button>
          {manageable && (
            <button
              type="button"
              data-testid="page-manage-open"
              onClick={() => setManaging(true)}
              title={t('manage.why')}
              className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2.5 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink"
            >
              <SlidersHorizontal size={12} />
              {t('manage.open')}
            </button>
          )}
          <Menu
            testid="page-more"
            items={[
              {
                label: t('page.history'),
                icon: History,
                testid: 'page-history',
                onSelect: () => setVersions(!versions),
              },
              {
                label: t('page.fork'),
                icon: GitFork,
                testid: 'page-fork',
                disabled: busy || !page.sourceExists,
                onSelect: () => void act(() => api.forkPage(page.id, '')),
              },
              { label: t('page.export'), icon: Download, testid: 'page-export', href: api.exportPageURL(page.id) },
              {
                label: t('page.delete'),
                icon: Trash2,
                testid: 'page-delete',
                destructive: true,
                onSelect: () => void remove(),
              },
            ]}
          />
        </div>
      </header>

      {versions && detail && (
        <div className="border-t border-hairline px-3 py-3 @md:px-4">
          <BlockTitle>{t('page.history')}</BlockTitle>
          <ul data-testid="page-versions" className="mt-2 flex flex-col gap-1 text-vp-sm">
            {detail.versions.filter((v) => !v.candidate).length === 0 && (
              <li className="text-ink-3">{t('page.noVersions')}</li>
            )}
            {detail.versions
              .filter((v) => !v.candidate)
              .map((v) => (
                <li
                  key={v.version}
                  className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5"
                >
                  <span className="tabular shrink-0 font-medium text-ink">v{v.version}</span>
                  <span className="shrink-0 text-vp-xs text-ink-3">
                    {new Date(v.createdAt * 1000).toLocaleString()}
                  </span>
                  {v.commitSha && (
                    <code className="shrink-0 font-mono text-vp-xs text-ink-3">
                      {v.commitSha.slice(0, 8)}
                      {v.dirty ? '*' : ''}
                    </code>
                  )}
                  <span className="min-w-0 flex-1 truncate text-ink-2">{safeText(v.note)}</span>
                  {v.version === detail.page.publishedVersion ? (
                    <Chip tone="accent">{t('page.current')}</Chip>
                  ) : (
                    <button
                      type="button"
                      data-testid="page-rollback"
                      onClick={() => void act(() => api.rollbackPage(page.id, v.version))}
                      className="vp-press shrink-0 rounded-vp border border-hairline px-2 py-0.5 text-vp-xs text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink"
                    >
                      {t('page.rollback')}
                    </button>
                  )}
                </li>
              ))}
          </ul>
        </div>
      )}

      {managing && caps && <ManageDialog page={page} capabilities={caps} onClose={() => setManaging(false)} />}

      <PageLinks
        page={page}
        detail={detail}
        links={links}
        projects={projects}
        sessions={sessions}
        visitorWrites={visitorWrites}
        onChanged={onChanged}
        onError={(m) => onError(new Error(m))}
      />
    </article>
  )
}
