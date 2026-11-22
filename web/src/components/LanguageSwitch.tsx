import { useRef } from 'react'
import { setLang, t, useLang } from '../i18n'
import { useSlidingMarker } from './useSlidingMarker'

/**
 * The two languages, each written in its own.
 *
 * Somebody who needs this cannot read the rest of the screen, which is why
 * they are looking for it — so it is never behind a word they would have to
 * translate first, and each option is spelt in the language it switches to.
 * A segmented pair rather than a dropdown: there are two, both fit, and a
 * select for two options is a click that buys nothing.
 *
 * One component for the three screens that draw it (sign-in, settings, the
 * sharing page), because the language picker was the first thing found to be
 * a segmented control that was not the panel's segmented control.
 *
 * `testid` names the screen, so a browser check can tell which one it pressed.
 */
export function LanguageSwitch({ testid }: { testid: string }) {
  const lang = useLang()
  const ref = useRef<HTMLDivElement>(null)
  const marker = useSlidingMarker(ref, lang)
  return (
    <div ref={ref} data-testid={`${testid}-language`} data-marker={marker ? 'on' : 'off'} className="vp-segmented">
      {marker && (
        <span
          aria-hidden="true"
          className="vp-marker-2d"
          style={{
            width: marker.width,
            height: marker.height,
            transform: `translate3d(${marker.left}px, ${marker.top}px, 0)`,
          }}
        />
      )}
      {(['zh', 'en'] as const).map((code) => (
        <button
          key={code}
          type="button"
          data-testid={`${testid}-lang-${code}`}
          onClick={() => setLang(code)}
          // aria-pressed says it to a screen reader; data-active is what the
          // stylesheet reads. Both, because `.vp-tab` keys its selected look
          // off aria-selected/data-active and this is a toggle group rather
          // than a tablist.
          aria-pressed={lang === code}
          data-active={lang === code}
          // nowrap, because a header is the one place this control has to
          // survive being squeezed: at 390px it folded 简体中文 into two lines
          // inside a pill built for one.
          className="vp-tab px-3 text-vp-base whitespace-nowrap"
        >
          {code === 'zh' ? t('settings.languageZh') : t('settings.languageEn')}
        </button>
      ))}
    </div>
  )
}
