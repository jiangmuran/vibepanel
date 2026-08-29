import { useState } from 'react'

import { t } from '../../i18n'
import {
  notifyEnabled,
  notifySupported,
  requestNotifyPermission,
  setNotifyEnabled,
} from '../../notify'
import { Webhooks } from '../Webhooks'
import { Section } from './parts'

/**
 * How you are told an agent wants you — both ways, in one place.
 *
 * They were four sections apart, which meant "turn on notifications" found
 * whichever of the two you happened to scroll past first. They are not the same
 * mechanism — one is a permission this browser holds and the other is a request
 * the panel makes to somebody else's server — but they answer the same
 * question, and the question is what people arrive with.
 */
export function NotificationsGroup() {
  const [state, setState] = useState<NotificationPermission>(
    typeof Notification === 'undefined' ? 'denied' : Notification.permission,
  )
  const [on, setOn] = useState(notifyEnabled())

  return (
    <>
      {/* Asked for from a click, never on load: a permission prompt fired on
          arrival is why browsers stopped showing them, and Safari refuses one
          outside a gesture at all. */}
      <Section id="browser" title={t('notify.browser')}>
        <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('notify.explain')}</p>
        {!notifySupported() ? (
          <p className="text-vp-base text-ink-3">{t('notify.insecure')}</p>
        ) : state === 'denied' ? (
          <p className="text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('notify.denied')}
          </p>
        ) : state === 'granted' && on ? (
          <div className="flex items-center gap-2">
            <span className="text-vp-base text-ink">{t('notify.on')}</span>
            <button
              type="button"
              data-testid="notify-off"
              onClick={() => {
                setNotifyEnabled(false)
                setOn(false)
              }}
              className="vp-press rounded-vp border border-hairline px-2 py-1 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              {t('dir.cancel')}
            </button>
          </div>
        ) : (
          <button
            type="button"
            data-testid="notify-enable"
            onClick={() => {
              void requestNotifyPermission().then((p) => {
                setState(p)
                setOn(p === 'granted')
              })
            }}
            className="rounded-vp px-3 py-1.5 text-vp-base"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('notify.enable')}
          </button>
        )}
      </Section>

      <Section id="webhooks" title={t('wh.title')}>
        <Webhooks />
      </Section>
    </>
  )
}
