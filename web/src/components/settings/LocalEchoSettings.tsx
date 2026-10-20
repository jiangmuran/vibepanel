import { useState } from 'react'

import { t } from '../../i18n'
import { localEchoOn, setLocalEcho } from '../localEchoLayer'
import { Section } from './parts'

/**
 * Local echo, for when the characters it draws ahead of the server are wrong.
 *
 * No reload, unlike the renderer next to it: the terminal reads the switch on
 * every keystroke, so the next key typed is drawn the new way.
 */
export function LocalEchoSettings() {
  const [on, setOn] = useState(localEchoOn)
  return (
    <Section id="localEcho" title={t('echo.title')}>
      <p className="mb-2 text-vp-sm text-ink-2">{t('echo.what')}</p>
      <fieldset className="flex flex-col gap-1" data-testid="local-echo-choice">
        <legend className="sr-only">{t('echo.title')}</legend>
        {([true, false] as const).map((v) => (
          <label key={String(v)} className="flex items-center gap-2 text-vp-base text-ink">
            <input
              type="radio"
              name="local-echo"
              value={v ? 'on' : 'off'}
              checked={on === v}
              onChange={() => {
                setLocalEcho(v)
                setOn(v)
              }}
              data-testid={`local-echo-${v ? 'on' : 'off'}`}
            />
            {t(v ? 'echo.on' : 'echo.off')}
          </label>
        ))}
      </fieldset>
      <p className="mt-2 text-vp-sm text-ink-2">{t('echo.when')}</p>
    </Section>
  )
}
