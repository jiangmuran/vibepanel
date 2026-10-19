import { useState } from 'react'
import { RotateCw } from 'lucide-react'

import { t } from '../../i18n'
import { rendererPreference, setRendererPreference } from '../renderer'
import { Section } from './parts'

/**
 * The terminal's renderer, for when the GPU one draws wrong.
 *
 * Each terminal picks its renderer as it opens, so a change here reaches the
 * ones already on screen only after a reload. That is said under the choice
 * and offered as a button once something changed, rather than tearing down
 * live terminals to rebuild them: a remount drops scrollback that tmux would
 * have to replay, for a switch somebody makes once.
 */
export function RendererSettings() {
  const [loaded] = useState(rendererPreference)
  const [choice, setChoice] = useState(loaded)
  return (
    <Section id="renderer" title={t('render.title')}>
      <p className="mb-2 text-vp-sm text-ink-2">{t('render.what')}</p>
      <fieldset className="flex flex-col gap-1" data-testid="renderer-choice">
        <legend className="sr-only">{t('render.title')}</legend>
        {(['auto', 'dom'] as const).map((v) => (
          <label key={v} className="flex items-center gap-2 text-vp-base text-ink">
            <input
              type="radio"
              name="renderer"
              value={v}
              checked={choice === v}
              onChange={() => {
                setRendererPreference(v)
                setChoice(v)
              }}
              data-testid={`renderer-${v}`}
            />
            {t(v === 'auto' ? 'render.auto' : 'render.dom')}
          </label>
        ))}
      </fieldset>
      {choice !== loaded && (
        <div className="mt-3 flex items-center gap-3" data-testid="renderer-reload-note">
          <p className="text-vp-sm text-ink-2">{t('render.reload')}</p>
          <button
            type="button"
            data-testid="renderer-reload"
            onClick={() => window.location.reload()}
            className="vp-press flex shrink-0 items-center gap-2 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <RotateCw size={14} />
            {t('render.reloadNow')}
          </button>
        </div>
      )}
    </Section>
  )
}
