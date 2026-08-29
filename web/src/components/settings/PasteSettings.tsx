import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import { t } from '../../i18n'
import { Section } from './parts'

/**
 * Where a screenshot pasted into a terminal goes.
 *
 * It used to go into the session's working directory, which for an agent
 * session is a git repository -- so pasting a picture at an agent dirtied the
 * tree, and the file was still there afterwards. 「粘贴图片不要直接粘贴到项目
 * 根目录啊」. The panel's own directory is the default now; the project stays an
 * option, because dragging a file onto the tree still means "put it here".
 *
 * And what happens to the path. Typed at the prompt is what the panel always
 * did. The tmux paste buffer is the other half: it fills the clipboard so the
 * pane can take the path when it wants to, rather than finding it typed.
 */
export function PasteSettings() {
  const [dir, setDir] = useState('panel')
  const [then, setThen] = useState('type')
  const [saved, setSaved] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    api
      .settings()
      .then((s) => {
        setDir(s.pasteDir)
        setThen(s.pasteThen)
      })
      .catch((e: unknown) => setErr(String(e)))
  }, [])

  const save = (nextDir: string, nextThen: string) => {
    setDir(nextDir)
    setThen(nextThen)
    setSaved(false)
    api
      .savePaste(nextDir, nextThen)
      .then(() => setSaved(true))
      .catch((e: unknown) => setErr(String(e)))
  }

  return (
    <Section id="paste" title={t('paste.title')}>
      <div className="flex flex-col gap-3">
        <Choice
          label={t('paste.where')}
          value={dir}
          onChange={(v) => save(v, then)}
          testid="paste-dir"
          options={[
            { value: 'panel', label: t('paste.wherePanel') },
            { value: 'session', label: t('paste.whereSession') },
          ]}
        />
        <Choice
          label={t('paste.then')}
          value={then}
          onChange={(v) => save(dir, v)}
          testid="paste-then"
          options={[
            { value: 'type', label: t('paste.thenType') },
            { value: 'buffer', label: t('paste.thenBuffer') },
            { value: 'both', label: t('paste.thenBoth') },
          ]}
        />
      </div>
      {saved && <p className="mt-2 text-vp-sm text-state-done">{t('paste.saved')}</p>}
      {err && <p className="mt-2 text-vp-sm text-state-crashed">{err}</p>}
    </Section>
  )
}

/**
 * Radios, not a select.
 *
 * Both options are worth reading before choosing one, and a select shows the
 * one you already have. There are two and three of them; the whole set fits.
 */
function Choice({
  label,
  value,
  options,
  onChange,
  testid,
}: {
  label: string
  value: string
  options: { value: string; label: string }[]
  onChange: (v: string) => void
  testid: string
}) {
  return (
    <fieldset>
      <legend className="mb-1 text-vp-sm text-ink-2">{label}</legend>
      <div className="flex flex-col gap-1">
        {options.map((o) => (
          <label key={o.value} className="flex items-start gap-2 text-vp-base text-ink">
            <input
              type="radio"
              name={testid}
              checked={value === o.value}
              onChange={() => onChange(o.value)}
              data-testid={`${testid}-${o.value}`}
              className="mt-1"
            />
            <span>{o.label}</span>
          </label>
        ))}
      </div>
    </fieldset>
  )
}
