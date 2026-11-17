import { t } from '../../i18n'
import { safeBody } from '../text'
import { Code } from './CodeBlock'
import Markdown from './Markdown'
import type { Notebook as Doc, Output } from './notebook'

/**
 * A Jupyter notebook, drawn as it reads in Jupyter: markdown cells through the
 * document renderer, code cells through the code body in the kernel's
 * language, each with its execution count and its outputs under it.
 *
 * What an output may become is decided in notebook.ts: text is text, a
 * picture is an <img> of a data URL whose type was chosen there, an error is
 * its name and its traceback. HTML is drawn as its source and never rendered
 * -- a notebook is somebody's file, and its HTML would run on the panel's
 * origin. Source is the JSON it is.
 */
export default function Notebook({ doc, projectId, path }: { doc: Doc; projectId: string; path: string }) {
  return (
    <div data-testid="viewer-notebook" className="min-h-0 flex-1 overflow-auto px-2 py-3 @md:px-4">
      {doc.cells.map((c, i) => (
        <section key={i} data-cell={c.type} className="min-w-0">
          {c.type === 'markdown' ? (
            <Markdown text={c.source} projectId={projectId} path={path} />
          ) : c.type === 'raw' ? (
            <Gutter label="">
              <pre className="my-2 whitespace-pre-wrap break-words font-mono text-vp-sm text-ink-2">{safeBody(c.source)}</pre>
            </Gutter>
          ) : (
            <>
              <Gutter label={`[${c.count ?? ' '}]`}>
                <Code code={c.source} lang={doc.language} />
              </Gutter>
              {c.outputs.map((o, j) => (
                <Gutter key={j} label="">
                  <OutputView o={o} />
                </Gutter>
              ))}
            </>
          )}
        </section>
      ))}
    </div>
  )
}

/** The execution count's column, which outputs keep empty to line up under. */
function Gutter({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 gap-2">
      <span
        aria-hidden={label === '' ? true : undefined}
        data-testid={label === '' ? undefined : 'nb-count'}
        className="tabular w-10 shrink-0 pt-3 text-right font-mono text-vp-xs text-ink-3 select-none"
      >
        {label}
      </span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}

function OutputView({ o }: { o: Output }) {
  const pre = 'whitespace-pre-wrap break-words font-mono text-vp-sm'
  switch (o.kind) {
    case 'text':
      return (
        <div data-testid="nb-output" data-output={o.stderr ? 'stderr' : 'text'} className="my-1">
          {/* Not only a tint: a word as well (red line 4). */}
          {o.stderr && <span className="text-vp-xs text-ink-2">{t('nb.stderr')}</span>}
          <pre className={`${pre} ${o.stderr ? 'rounded-md bg-[color-mix(in_srgb,var(--vp-tok-deleted)_10%,transparent)] px-2 py-1' : ''} text-ink`}>
            {safeBody(o.text)}
          </pre>
        </div>
      )
    case 'image':
      return (
        <img
          src={o.src}
          alt={t('nb.picture')}
          data-testid="nb-output"
          data-output="image"
          className="vp-checker my-1 h-auto max-w-full"
        />
      )
    case 'html':
      return (
        <div data-testid="nb-output" data-output="html" className="my-1">
          <span className="text-vp-xs text-ink-2">{t('nb.htmlAsText')}</span>
          <pre className={`${pre} text-ink-2`}>{safeBody(o.text)}</pre>
        </div>
      )
    case 'error':
      return (
        <div data-testid="nb-output" data-output="error" className="my-1 rounded-md border border-hairline px-2 py-1">
          <p className="font-mono text-vp-sm font-semibold text-ink">
            {safeBody(o.name)}
            {o.value && `: ${safeBody(o.value)}`}
          </p>
          {o.traceback && <pre className={`${pre} text-ink-2`}>{safeBody(o.traceback)}</pre>}
        </div>
      )
  }
}
