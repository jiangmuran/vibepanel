import { useEffect, useState } from 'react'
import { api } from '../../protocol/api'
import type { TuneStatus } from '../../protocol/wire'
import { t, getLang } from '../../i18n'
import { Section } from './parts'

/**
 * The settings the panel offers to change in Claude Code's own settings.json,
 * beyond the state-reporting hooks.
 *
 * Every key is listed with the value on disk and the value that would replace
 * it, before the button. That is not a courtesy: this writes a file belonging
 * to another tool, and a button labelled "optimise" over an unnamed list is
 * consent to nothing in particular. The installer prints the same list for the
 * same reason.
 *
 * The descriptions come from the server. They live beside the keys in
 * internal/hooks so they cannot drift from what is actually written, and
 * copying them into i18n.ts would be exactly that drift.
 */
export function TuneClaude() {
  const [st, setSt] = useState<TuneStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [applied, setApplied] = useState(0)

  const load = () => {
    api
      .tuneStatus()
      .then((s) => {
        setSt(s)
        setErr('')
      })
      .catch((e: unknown) => setErr(String(e)))
  }
  useEffect(load, [])

  const apply = () => {
    setBusy(true)
    api
      .tuneApply()
      .then((before) => {
        // The count comes from the answer, which is the comparison as it stood
        // *before* the write. Reading it off the reloaded state would always
        // say zero.
        setApplied(before.changes)
        load()
      })
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false))
  }

  const zh = getLang() === 'zh'
  return (
    <Section id="tune" title={t('tune.title')}>
      {!st ? (
        <p className="text-vp-sm text-ink-3">{err || t('tune.loading')}</p>
      ) : (
        <TuneBody st={st} zh={zh} busy={busy} applied={applied} err={err} apply={apply} />
      )}
    </Section>
  )
}

/**
 * The list and the button, once there is something to draw.
 *
 * Its own component so the section above is declared exactly once. Two of
 * them -- one for the loading state, one for the loaded one -- is what a deep
 * link resolves against, and settings/wiring.test.ts counts them.
 */
function TuneBody({
  st,
  zh,
  busy,
  applied,
  err,
  apply,
}: {
  st: TuneStatus
  zh: boolean
  busy: boolean
  applied: number
  err: string
  apply: () => void
}) {
  return (
    <>
      <p className="mb-2 text-vp-sm text-ink-2">{t('tune.what')}</p>
      <div className="mb-3 overflow-x-auto rounded-vp border border-hairline">
        <table className="w-full border-collapse text-vp-sm">
          <tbody>
            {st.rows.map((r) => (
              <tr key={r.key} className="border-b border-hairline last:border-0" data-tune-row={r.key}>
                <td className="px-2 py-1.5 align-top">
                  {/* The state as a shape, not only a colour (red line 4): a
                      tick for already-set, a dot for would-change. */}
                  <span
                    aria-hidden
                    className={r.same ? 'text-state-done' : 'text-accent'}
                    title={r.same ? t('tune.already') : t('tune.would')}
                  >
                    {r.same ? '✓' : '•'}
                  </span>
                </td>
                <td className="px-2 py-1.5 align-top font-mono text-vp-xs break-all text-ink">
                  {r.key}
                </td>
                <td className="px-2 py-1.5 align-top text-ink-2">{zh ? r.whatZh : r.what}</td>
                <td className="px-2 py-1.5 text-right align-top font-mono text-vp-xs whitespace-nowrap text-ink-2">
                  {JSON.stringify(r.want)}
                  {/* What is being replaced, when it was something. A row that
                      lists only the new value tells somebody their setting is
                      gone by not mentioning it. */}
                  {!r.same && r.have !== undefined && r.have !== null && (
                    <div className="text-ink-3">
                      {t('tune.was', { v: JSON.stringify(r.have) })}
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
          disabled={busy || st.changes === 0}
          data-testid="tune-apply"
          onClick={apply}
        >
          {st.changes === 0 ? t('tune.nothing') : t('tune.apply', { n: st.changes })}
        </button>
        <span className="text-vp-sm text-ink-3">{t('tune.backup', { p: st.path })}</span>
      </div>
      {applied > 0 && st.changes === 0 && (
        <p className="mt-2 text-vp-sm text-state-done" data-testid="tune-done">
          {t('tune.applied', { n: applied })}
        </p>
      )}
      {err && <p className="mt-2 text-vp-sm text-state-crashed">{err}</p>}
    </>
  )
}
