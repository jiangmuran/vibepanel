import { useState } from 'react'

import { t, useLang } from '../../i18n'
import { api } from '../../protocol/api'
import type { PluginDetail, PluginFieldSpec, PluginSettings } from '../../protocol/wire'
import { Field, INPUT } from '../pages/bits'
import { safeText } from '../text'
import { listLines, textIn } from './screen'

/**
 * A plugin's settings, drawn by the panel from the schema the manifest
 * declared (docs/plugins.md §5a). The plugin never saw this form: it wrote a
 * schema, and a redesign of this form moves nothing it wrote.
 *
 * Secrets are the same list with a different shape: a value typed here is
 * sent once and never read back, and the field says only whether one is set.
 * Every secret the manifest names is listed, not only the secret-typed
 * settings, because a process's env or a source's header needs one too and
 * this is the one place to fill it.
 */
export function PluginSettingsForm({
  plugin,
  onChanged,
  onError,
}: {
  plugin: PluginDetail
  onChanged: () => void
  onError: (e: unknown) => void
}) {
  const lang = useLang()
  // What the server answered since the last refresh, over what the detail
  // carries: a saved value shows at once rather than on the next poll.
  const [fresh, setFresh] = useState<Record<string, unknown>>({})
  const [saved, setSaved] = useState<string | null>(null)
  const settings: PluginSettings = { fields: plugin.settings.fields, values: { ...plugin.settings.values, ...fresh } }

  const save = async (f: PluginFieldSpec, value: unknown) => {
    // Shown at once, then replaced by what the server kept: a toggle that
    // waits for the round trip reads as one that did not take.
    setFresh((v) => ({ ...v, [f.key]: value }))
    try {
      const got = await api.setPluginSetting(plugin.id, f.key, value)
      setFresh((v) => ({ ...v, [f.key]: got.value }))
      setSaved(f.key)
      window.setTimeout(() => setSaved((k) => (k === f.key ? null : k)), 1500)
      onChanged()
    } catch (e) {
      setFresh((v) => {
        const next = { ...v }
        delete next[f.key]
        return next
      })
      onError(e)
    }
  }

  const reset = async (f: PluginFieldSpec) => {
    try {
      await api.resetPluginSetting(plugin.id, f.key)
      setFresh((v) => {
        const next = { ...v }
        delete next[f.key]
        return next
      })
      onChanged()
    } catch (e) {
      onError(e)
    }
  }

  const fields = settings.fields.filter((f) => f.type !== 'secret')
  return (
    <div className="grid gap-3" data-testid="plugin-settings-form">
      {fields.length === 0 && plugin.secrets.length === 0 && (
        <p className="text-vp-base text-ink-3">{t('plg.noSettings')}</p>
      )}
      {fields.length > 0 && (
        <div className="grid gap-3 @md:grid-cols-2">
          {fields.map((f) => (
            <SettingField
              // Keyed by the value as well, so a value that changed outside
              // this form (a reset, another tab) remounts the field with it
              // rather than an effect writing state after the first paint.
              key={`${f.key}:${JSON.stringify(settings.values[f.key] ?? null)}`}
              plugin={plugin.id}
              field={f}
              value={settings.values[f.key]}
              lang={lang}
              saved={saved === f.key}
              onSave={(v) => void save(f, v)}
              onReset={() => void reset(f)}
            />
          ))}
        </div>
      )}
      {plugin.secrets.length > 0 && (
        <div className="grid gap-2">
          <h3 className="text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">{t('plg.secrets')}</h3>
          {plugin.secrets.map((s) => (
            <SecretField key={s.name} plugin={plugin.id} name={s.name} set={s.set} onChanged={onChanged} onError={onError} />
          ))}
        </div>
      )}
    </div>
  )
}

function SettingField({
  plugin,
  field: f,
  value,
  lang,
  saved,
  onSave,
  onReset,
}: {
  plugin: string
  field: PluginFieldSpec
  value: unknown
  lang: 'zh' | 'en'
  saved: boolean
  onSave: (v: unknown) => void
  onReset: () => void
}) {
  const id = `plugin-${plugin}-${f.key}`
  const label = safeText(textIn(f.label, lang))
  const hint = f.hint ? safeText(textIn(f.hint, lang)) : ''
  // Text-like fields are edited then saved, so a keystroke is not a request;
  // the rest save on change, because a toggle or a pick is the decision.
  const [draft, setDraft] = useState<string>(() => draftOf(f, value))

  const control = (() => {
    switch (f.type) {
      case 'bool':
        return (
          <label className="flex items-center gap-2 text-vp-base text-ink">
            <input id={id} type="checkbox" data-testid={id} checked={value === true} onChange={(e) => onSave(e.target.checked)} className="accent-[var(--vp-accent)]" />
            <span>{label}</span>
          </label>
        )
      case 'enum':
        return (
          <select id={id} data-testid={id} value={String(value ?? '')} onChange={(e) => onSave(e.target.value)} className={INPUT}>
            {(f.values ?? []).map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        )
      case 'color':
        return (
          <input id={id} type="color" data-testid={id} value={typeof value === 'string' ? value : '#000000'} onChange={(e) => onSave(e.target.value)} className="h-8 w-16 cursor-pointer rounded-vp border border-hairline bg-surface-2" />
        )
      case 'number':
        return (
          <input
            id={id}
            type="number"
            data-testid={id}
            value={draft}
            min={f.min}
            max={f.max}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => {
              const n = Number(draft)
              if (draft !== '' && !Number.isNaN(n) && n !== value) onSave(n)
            }}
            className={INPUT}
          />
        )
      case 'list':
        return (
          <textarea
            id={id}
            data-testid={id}
            value={draft}
            rows={3}
            placeholder={t('plg.listItem')}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => {
              const next = listLines(draft)
              if (JSON.stringify(next) !== JSON.stringify(value)) onSave(next)
            }}
            className={INPUT}
          />
        )
      default:
        return (
          <input
            id={id}
            type="text"
            data-testid={id}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => {
              if (draft !== value) onSave(draft)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
            }}
            className={INPUT}
          />
        )
    }
  })()

  return (
    <div className="min-w-0">
      {f.type === 'bool' ? control : <Field label={label} htmlFor={id}>{control}</Field>}
      <div className="mt-1 flex items-center gap-2 text-vp-xs text-ink-3">
        {hint && <span className="min-w-0 flex-1">{hint}</span>}
        {saved && <span style={{ color: 'var(--vp-state-done)' }}>{t('plg.saved')}</span>}
        <button type="button" onClick={onReset} data-testid={`${id}-reset`} className="ml-auto shrink-0 text-ink-3 hover:text-ink hover:underline">
          {t('plg.setting.reset')}
        </button>
      </div>
    </div>
  )
}

function draftOf(f: PluginFieldSpec, value: unknown): string {
  if (f.type === 'list') return Array.isArray(value) ? value.map(String).join('\n') : ''
  if (value === undefined || value === null) return ''
  return String(value)
}

function SecretField({
  plugin,
  name,
  set,
  onChanged,
  onError,
}: {
  plugin: string
  name: string
  set: boolean
  onChanged: () => void
  onError: (e: unknown) => void
}) {
  const [value, setValue] = useState('')
  const id = `plugin-${plugin}-secret-${name}`
  const save = async () => {
    if (!value) return
    try {
      await api.setPluginSecret(plugin, name, value)
      setValue('')
      onChanged()
    } catch (e) {
      onError(e)
    }
  }
  const forget = async () => {
    try {
      await api.deletePluginSecret(plugin, name)
      onChanged()
    } catch (e) {
      onError(e)
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid={id}>
      <code className="font-mono text-vp-sm text-ink">{name}</code>
      <span className="text-vp-xs" style={{ color: set ? 'var(--vp-state-done)' : 'var(--vp-state-waiting)' }} data-testid={`${id}-state`}>
        {set ? t('plg.secret.set') : t('plg.secret.unset')}
      </span>
      <input
        type="password"
        autoComplete="off"
        aria-label={`${name}`}
        placeholder={t('plg.secret.value')}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') void save()
        }}
        data-testid={`${id}-value`}
        className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent"
      />
      <button type="button" disabled={!value} onClick={() => void save()} data-testid={`${id}-save`} className="vp-outline text-vp-sm disabled:opacity-40">
        {t('plg.secret.save')}
      </button>
      {set && (
        <button type="button" onClick={() => void forget()} data-testid={`${id}-forget`} className="vp-outline text-vp-sm">
          {t('plg.secret.forget')}
        </button>
      )}
    </div>
  )
}
