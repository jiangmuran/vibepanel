import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowDown, ArrowUp, Minus, Plus, RotateCcw, X } from 'lucide-react'

import { api } from '../../protocol/api'
import type { SharePageData, SharePageDataNamespace, SharePageDataSpec } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { askConfirm } from '../ask'
import { safeText } from '../text'

/**
 * A page's data, as a form drawn from its declared schema.
 *
 * The same idea as ParamsForm, one level deeper: the manifest says what each
 * key is, so the form offers exactly that and the server checks every value
 * against the same declaration when it is saved. docs/page-backend.md §2.
 *
 * Saved a moment after a field stops changing, like a link's parameters. Keys
 * nobody is editing are re-read every few seconds, because visitors and
 * server.js write to the same store while the form is open.
 */

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

const SAVE_AFTER_MS = 700
const POLL_MS = 5000

/** How many log entries are drawn, newest first. */
const LOG_SHOWN = 20

type Status = 'saving' | 'saved' | 'error'

function fail(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** The value a spec starts from when nothing is stored. */
function blank(spec: SharePageDataSpec): unknown {
  if (spec.default !== undefined) return spec.default
  switch (spec.type) {
    case 'text':
      return ''
    case 'number':
      return spec.min ?? 0
    case 'bool':
      return false
    case 'enum':
      return spec.values?.[0] ?? ''
    case 'color':
      return '#000000'
    case 'list':
    case 'log':
      return []
    case 'object': {
      const out: Record<string, unknown> = {}
      for (const [k, f] of Object.entries(spec.fields ?? {})) out[k] = blank(f)
      return out
    }
    case 'counter':
      return 0
    default:
      return null
  }
}

function kiB(n: number): string {
  return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KiB`
}

export function DataForm({ pageId, ns }: { pageId: string; ns: SharePageDataNamespace }) {
  useLang()
  const [data, setData] = useState<SharePageData | null>(null)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [status, setStatus] = useState<Record<string, Status>>({})
  const [error, setError] = useState('')
  const timers = useRef<Record<string, number>>({})
  // Keys with an edit that has not reached the server: a poll must not put the
  // old value back under somebody's cursor.
  const dirty = useRef<Set<string>>(new Set())

  const load = useCallback(async () => {
    try {
      const next = await api.pageData(pageId, ns)
      setData(next)
      setValues((was) => {
        const merged: Record<string, unknown> = { ...next.values }
        for (const k of dirty.current) if (k in was) merged[k] = was[k]
        return merged
      })
      setError('')
    } catch (e) {
      setError(fail(e))
    }
  }, [pageId, ns])

  useEffect(() => {
    const first = window.setTimeout(() => void load(), 0)
    const timer = window.setInterval(() => {
      if (!document.hidden) void load()
    }, POLL_MS)
    const pending = timers.current
    return () => {
      clearTimeout(first)
      clearInterval(timer)
      for (const id of Object.values(pending)) clearTimeout(id)
    }
  }, [load])

  const mark = (key: string, s: Status) => setStatus((was) => ({ ...was, [key]: s }))

  const run = async (key: string, fn: () => Promise<unknown>) => {
    mark(key, 'saving')
    try {
      await fn()
      mark(key, 'saved')
      setError('')
    } catch (e) {
      mark(key, 'error')
      setError(fail(e))
    }
    dirty.current.delete(key)
    await load()
  }

  const change = (key: string, value: unknown) => {
    setValues((was) => ({ ...was, [key]: value }))
    dirty.current.add(key)
    clearTimeout(timers.current[key])
    timers.current[key] = window.setTimeout(
      () => void run(key, () => api.setPageData(pageId, key, value, ns)),
      SAVE_AFTER_MS,
    )
  }

  const reset = (key: string) => {
    clearTimeout(timers.current[key])
    void run(key, () => api.resetPageData(pageId, key, ns))
  }

  const resetAll = async () => {
    const yes = await askConfirm({
      title: t('data.resetAllTitle'),
      body: t('data.resetAllBody'),
      confirm: t('data.resetAll'),
      cancel: t('ask.cancel'),
      destructive: true,
    })
    if (!yes) return
    try {
      await api.clearPageData(pageId, ns)
      dirty.current.clear()
      setError('')
    } catch (e) {
      setError(fail(e))
    }
    await load()
  }

  if (!data) {
    return (
      <div data-testid="data-form" className="text-vp-sm text-ink-3">
        {error ? safeText(error) : t('data.loading')}
      </div>
    )
  }

  const keys = Object.keys(data.schema)
  return (
    <div data-testid="data-form" data-ns={ns} className="@container flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 text-vp-sm text-ink-3">
        <span data-testid="data-ns">{ns === 'draft' ? t('data.nsDraft') : t('data.nsLive')}</span>
        <span className="tabular" data-testid="data-bytes">
          {t('data.bytes', { used: kiB(data.bytes), limit: kiB(data.limit) })}
        </span>
        <span className="flex-1" />
        {keys.length > 0 && (
          <button
            type="button"
            data-testid="data-reset-all"
            onClick={() => void resetAll()}
            className="vp-press rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink"
          >
            {t('data.resetAll')}
          </button>
        )}
      </div>
      {error && (
        <p className="text-vp-sm" style={{ color: 'var(--vp-state-crashed)' }} data-testid="data-error">
          {safeText(error)}
        </p>
      )}
      {keys.length === 0 && <p className="text-vp-sm text-ink-3">{t('data.none')}</p>}
      {keys.map((key) => {
        const spec = data.schema[key]
        const value = key in values ? values[key] : blank(spec)
        const id = `data-${ns}-${key}`
        const at = data.updatedAt[key] ?? 0
        return (
          <div key={key} data-testid="data-field" data-key={key} className="min-w-0 border-t border-hairline pt-2 first:border-t-0 first:pt-0">
            <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-vp-sm">
              <label htmlFor={id} className="text-ink-2">
                {safeText(spec.label || key)}
              </label>
              <code className="font-mono text-vp-xs text-ink-3">{safeText(key)}</code>
              {spec.visibility === 'admin' && (
                <span className="text-vp-xs text-ink-3" data-testid="data-admin-only">
                  {t('data.adminOnly')}
                </span>
              )}
              <span className="flex-1" />
              <span className="text-vp-xs text-ink-3" data-testid="data-status" data-status={status[key] ?? ''}>
                {status[key] === 'saving'
                  ? t('data.saving')
                  : status[key] === 'error'
                    ? t('data.failed')
                    : at > 0
                      ? new Date(at * 1000).toLocaleString()
                      : ''}
              </span>
              <button
                type="button"
                data-testid="data-reset"
                onClick={() => reset(key)}
                title={t('data.reset')}
                aria-label={t('data.reset')}
                className="vp-control"
              >
                <RotateCcw size={12} />
              </button>
            </div>
            <Editor
              id={id}
              spec={spec}
              value={value}
              onChange={(next) => change(key, next)}
              onIncrement={(by) => void run(key, () => api.incrementPageData(pageId, key, by, ns))}
            />
          </div>
        )
      })}
    </div>
  )
}

function Editor({
  id,
  spec,
  value,
  onChange,
  onIncrement,
}: {
  id: string
  spec: SharePageDataSpec
  value: unknown
  onChange: (next: unknown) => void
  onIncrement: (by: number) => void
}) {
  switch (spec.type) {
    case 'counter':
      return (
        <div className="flex items-center gap-2">
          <span id={id} className="tabular text-vp-lg text-ink" data-testid="data-counter">
            {Number(value) || 0}
          </span>
          <button
            type="button"
            onClick={() => onIncrement(-1)}
            title={t('data.decrement')}
            aria-label={t('data.decrement')}
            className="vp-control"
          >
            <Minus size={12} />
          </button>
          <button
            type="button"
            onClick={() => onIncrement(1)}
            title={t('data.increment')}
            aria-label={t('data.increment')}
            className="vp-control"
          >
            <Plus size={12} />
          </button>
        </div>
      )
    case 'log':
      return <LogView id={id} spec={spec} value={value} />
    case 'json':
      return <JsonEditor id={id} value={value} onChange={onChange} />
    case 'list':
      return <ListEditor id={id} spec={spec} value={value} onChange={onChange} />
    case 'object':
      return <ObjectEditor id={id} spec={spec} value={value} onChange={onChange} />
    default:
      return <Scalar id={id} spec={spec} value={value} onChange={onChange} />
  }
}

/** text, number, bool, enum, color: the types ParamsForm already draws. */
function Scalar({
  id,
  spec,
  value,
  onChange,
}: {
  id: string
  spec: SharePageDataSpec
  value: unknown
  onChange: (next: unknown) => void
}) {
  switch (spec.type) {
    case 'text': {
      const max = spec.max ?? 280
      return max > 120 ? (
        <textarea
          id={id}
          value={String(value ?? '')}
          maxLength={max}
          rows={Math.min(6, Math.ceil(max / 120))}
          onChange={(e) => onChange(e.target.value)}
          className={`${INPUT} resize-y`}
        />
      ) : (
        <input id={id} value={String(value ?? '')} maxLength={max} onChange={(e) => onChange(e.target.value)} className={INPUT} />
      )
    }
    case 'number':
      return (
        <input
          id={id}
          type="number"
          min={spec.min}
          max={spec.max}
          step="any"
          value={Number(value ?? 0)}
          onChange={(e) => {
            const n = e.target.valueAsNumber
            if (Number.isFinite(n)) onChange(n)
          }}
          className={INPUT}
        />
      )
    case 'bool':
      return (
        <input id={id} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} className="h-4 w-4" />
      )
    case 'enum':
      return (
        <select id={id} value={String(value ?? '')} onChange={(e) => onChange(e.target.value)} className={INPUT}>
          {(spec.values ?? []).map((v) => (
            <option key={v} value={v}>
              {safeText(v)}
            </option>
          ))}
        </select>
      )
    case 'color':
      return (
        <div className="flex items-center gap-2">
          <input
            id={id}
            type="color"
            value={String(value ?? '#000000')}
            onChange={(e) => onChange(e.target.value)}
            className="h-8 w-10 shrink-0 cursor-pointer rounded-vp border border-hairline bg-surface-2"
          />
          <code className="font-mono text-vp-sm text-ink-2">{safeText(String(value ?? ''))}</code>
        </div>
      )
    default:
      return <JsonEditor id={id} value={value} onChange={onChange} />
  }
}

function ObjectEditor({
  id,
  spec,
  value,
  onChange,
}: {
  id: string
  spec: SharePageDataSpec
  value: unknown
  onChange: (next: unknown) => void
}) {
  const obj = value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : {}
  return (
    <div id={id} className="grid grid-cols-1 gap-2 @md:grid-cols-2">
      {Object.entries(spec.fields ?? {}).map(([k, f]) => (
        <div key={k} className="min-w-0">
          <label htmlFor={`${id}-${k}`} className="mb-0.5 block text-vp-xs text-ink-3">
            {safeText(f.label || k)}
          </label>
          <Scalar
            id={`${id}-${k}`}
            spec={f}
            value={k in obj ? obj[k] : blank(f)}
            onChange={(next) => onChange({ ...obj, [k]: next })}
          />
        </div>
      ))}
    </div>
  )
}

function ListEditor({
  id,
  spec,
  value,
  onChange,
}: {
  id: string
  spec: SharePageDataSpec
  value: unknown
  onChange: (next: unknown) => void
}) {
  const items = Array.isArray(value) ? value : []
  const item = spec.item ?? { type: 'text' as const }
  const max = spec.max ?? 500
  const move = (from: number, to: number) => {
    if (to < 0 || to >= items.length) return
    const next = items.slice()
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    onChange(next)
  }
  return (
    <div id={id} data-testid="data-list" className="flex flex-col gap-1.5">
      {items.map((v, i) => (
        <div key={i} className="flex items-start gap-1">
          <div className="min-w-0 flex-1">
            {item.type === 'object' ? (
              <ObjectEditor
                id={`${id}-${i}`}
                spec={item}
                value={v}
                onChange={(next) => onChange(items.map((x, j) => (j === i ? next : x)))}
              />
            ) : (
              <Scalar
                id={`${id}-${i}`}
                spec={item}
                value={v}
                onChange={(next) => onChange(items.map((x, j) => (j === i ? next : x)))}
              />
            )}
          </div>
          <button
            type="button"
            onClick={() => move(i, i - 1)}
            disabled={i === 0}
            title={t('data.up')}
            aria-label={t('data.up')}
            className="vp-control disabled:opacity-40"
          >
            <ArrowUp size={12} />
          </button>
          <button
            type="button"
            onClick={() => move(i, i + 1)}
            disabled={i === items.length - 1}
            title={t('data.down')}
            aria-label={t('data.down')}
            className="vp-control disabled:opacity-40"
          >
            <ArrowDown size={12} />
          </button>
          <button
            type="button"
            onClick={() => onChange(items.filter((_, j) => j !== i))}
            title={t('data.remove')}
            aria-label={t('data.remove')}
            className="vp-control"
          >
            <X size={12} />
          </button>
        </div>
      ))}
      <div className="flex items-center gap-2">
        <button
          type="button"
          data-testid="data-list-add"
          disabled={items.length >= max}
          onClick={() => onChange([...items, blank(item)])}
          className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink disabled:opacity-40"
        >
          <Plus size={12} />
          {t('data.add')}
        </button>
        <span className="tabular text-vp-xs text-ink-3">
          {items.length} / {max}
        </span>
      </div>
    </div>
  )
}

/** JSON as text, saved only while it parses. */
function JsonEditor({ id, value, onChange }: { id: string; value: unknown; onChange: (next: unknown) => void }) {
  const [text, setText] = useState(() => JSON.stringify(value ?? null, null, 2))
  const [bad, setBad] = useState(false)
  return (
    <div>
      <textarea
        id={id}
        data-testid="data-json"
        value={text}
        rows={6}
        spellCheck={false}
        onChange={(e) => {
          setText(e.target.value)
          try {
            const parsed: unknown = JSON.parse(e.target.value)
            setBad(false)
            onChange(parsed)
          } catch {
            setBad(true)
          }
        }}
        className={`${INPUT} resize-y font-mono text-vp-sm`}
      />
      {bad && (
        <p className="mt-0.5 text-vp-xs" style={{ color: 'var(--vp-state-waiting)' }} data-testid="data-json-bad">
          {t('data.jsonBad')}
        </p>
      )}
    </div>
  )
}

/** A log is append-only: the newest entries, read-only, and a reset beside them. */
function LogView({ id, spec, value }: { id: string; spec: SharePageDataSpec; value: unknown }) {
  const entries = Array.isArray(value) ? (value as unknown[]) : []
  const shown = entries.slice(-LOG_SHOWN).reverse()
  const fields = spec.item?.type === 'object' ? Object.keys(spec.item.fields ?? {}) : []
  return (
    <div id={id} data-testid="data-log" className="text-vp-sm">
      <p className="mb-1 text-vp-xs text-ink-3">{t('data.logCount', { n: entries.length, max: spec.max ?? 1000 })}</p>
      {shown.length === 0 ? (
        <p className="text-ink-3">{t('data.logEmpty')}</p>
      ) : (
        <ul className="max-h-48 space-y-0.5 overflow-y-auto">
          {shown.map((entry, i) => {
            const e = entry && typeof entry === 'object' ? (entry as Record<string, unknown>) : { value: entry }
            const at = typeof e.at === 'number' ? e.at : 0
            const body =
              fields.length > 0
                ? fields.map((f) => String(e[f] ?? '')).join(' · ')
                : typeof e.value === 'string'
                  ? e.value
                  : JSON.stringify(e.value ?? entry)
            return (
              <li key={i} className="flex gap-2">
                {at > 0 && <span className="tabular shrink-0 text-ink-3">{new Date(at * 1000).toLocaleTimeString()}</span>}
                <span className="min-w-0 break-words text-ink">{safeText(body)}</span>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
