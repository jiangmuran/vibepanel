import type { SharePageParam, ShareParamValue } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { paramValue } from './usePages'

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/**
 * A page's parameters, as the form a link's owner fills in.
 *
 * Built from the page's own manifest, so the form offers exactly what the page
 * declared; the server checks every value against the same manifest again
 * when it is saved, and a page republished with a narrower range gets the
 * default in place of a value that no longer fits.
 */
export function ParamsForm({
  specs,
  values,
  idPrefix,
  onChange,
}: {
  specs: SharePageParam[]
  values: Record<string, ShareParamValue>
  idPrefix: string
  onChange: (next: Record<string, ShareParamValue>) => void
}) {
  useLang()
  if (specs.length === 0) return <p className="text-vp-sm text-ink-3">{t('page.noParams')}</p>
  const set = (key: string, v: ShareParamValue) => onChange({ ...values, [key]: v })
  return (
    <div data-testid="page-params" className="grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2 @3xl:grid-cols-3">
      {specs.map((spec) => {
        const id = `${idPrefix}-${spec.key}`
        const value = paramValue(spec, values)
        const label = safeText(spec.label || spec.key)
        return (
          <div key={spec.key} className="min-w-0">
            <label htmlFor={id} className="mb-1 block text-vp-sm text-ink-3">
              {label}
            </label>
            {spec.type === 'text' && (
              <input
                id={id}
                data-param={spec.key}
                value={String(value)}
                maxLength={spec.max ?? 80}
                onChange={(e) => set(spec.key, e.target.value)}
                className={INPUT}
              />
            )}
            {spec.type === 'color' && (
              <div className="flex items-center gap-2">
                <input
                  id={id}
                  data-param={spec.key}
                  type="color"
                  value={String(value)}
                  onChange={(e) => set(spec.key, e.target.value)}
                  className="h-8 w-10 shrink-0 cursor-pointer rounded-vp border border-hairline bg-surface-2"
                />
                <code className="font-mono text-vp-sm text-ink-2">{String(value)}</code>
              </div>
            )}
            {spec.type === 'number' && (
              <input
                id={id}
                data-param={spec.key}
                type="number"
                min={spec.min}
                max={spec.max}
                step="any"
                value={Number(value)}
                onChange={(e) => {
                  const n = e.target.valueAsNumber
                  if (Number.isFinite(n)) set(spec.key, n)
                }}
                className={INPUT}
              />
            )}
            {spec.type === 'enum' && (
              <select
                id={id}
                data-param={spec.key}
                value={String(value)}
                onChange={(e) => set(spec.key, e.target.value)}
                className={INPUT}
              >
                {(spec.values ?? []).map((v) => (
                  <option key={v} value={v}>
                    {safeText(v)}
                  </option>
                ))}
              </select>
            )}
            {spec.type === 'bool' && (
              <input
                id={id}
                data-param={spec.key}
                type="checkbox"
                checked={value === true}
                onChange={(e) => set(spec.key, e.target.checked)}
                className="h-4 w-4"
              />
            )}
          </div>
        )
      })}
    </div>
  )
}
