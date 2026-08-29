import { useState } from 'react'
import { api } from '../../protocol/api'
import { t } from '../../i18n'
import { Section } from './parts'

/**
 * Restarts the panel process.
 *
 * This is a button rather than advice to go and find a terminal because of the
 * one property the whole architecture is built on: tmux owns every session, so
 * a panel restart costs the websocket and nothing else. `KillMode=process` in
 * the unit and restart-check in the test suite exist to keep that true.
 *
 * It works by exiting and letting the supervisor start a new process, which is
 * why the server refuses when nothing supervises it -- on that machine the
 * button is "stop", and the tab it was pressed in is the thing that goes dark.
 */
export function RestartPanel() {
  const [state, setState] = useState<'idle' | 'going' | 'back' | 'refused'>('idle')

  const go = () => {
    setState('going')
    api
      .restartPanel()
      .then(() => waitForItToComeBack())
      .catch(() => {
        // The 409 and a dropped connection are the same rejected promise here.
        // Asking again tells them apart: a panel that answers is one that
        // refused, and one that does not is one that is on its way down.
        api
          .tuneStatus()
          .then(() => setState('refused'))
          .catch(() => waitForItToComeBack())
      })
  }

  // Polled rather than timed. How long a restart takes is the supervisor's
  // business -- systemd's RestartSec is three seconds and a slow machine adds
  // more -- and a fixed wait is either a lie or a delay.
  const waitForItToComeBack = () => {
    let tries = 0
    const tick = () => {
      tries++
      fetch('/api/health')
        .then((r) => {
          if (!r.ok) throw new Error('not yet')
          setState('back')
          // The websocket and every cached snapshot belong to the process that
          // just went away. Reloading is the honest way to reconnect to the
          // new one, and it is what somebody pressing "restart" expects.
          setTimeout(() => window.location.reload(), 400)
        })
        .catch(() => {
          if (tries < 60) setTimeout(tick, 500)
        })
    }
    // Not immediately: the old process answers /api/health right up until it
    // stops, so a poll that starts now succeeds against the panel being
    // replaced and reloads into a socket that is about to close.
    setTimeout(tick, 1500)
  }

  return (
    <Section id="restart" title={t('rst.title')}>
      <p className="mb-2 text-vp-sm text-ink-2">{t('rst.what')}</p>
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          data-testid="panel-restart"
          disabled={state === 'going' || state === 'back'}
          onClick={go}
          className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
        >
          {state === 'going' ? t('rst.going') : state === 'back' ? t('rst.back') : t('rst.go')}
        </button>
        {state === 'refused' && (
          <span className="text-vp-sm text-state-waiting" data-testid="restart-refused">
            {t('rst.unsupervised')}
          </span>
        )}
      </div>
    </Section>
  )
}
