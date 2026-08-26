import type { Session } from './protocol/wire'
import { t } from './i18n'
import { sessionLabel } from './components/label'

/**
 * Telling you an agent is waiting, when you are not looking at the panel.
 *
 * This is the thing the whole product is for. The sidebar answers "which of
 * these needs me" while you are looking at it; a notification answers it while
 * you are not, which is most of the time an agent spends working.
 *
 * Deliberately not Web Push. Push would notify with the panel closed, and it
 * costs a push service, VAPID keys, subscription storage and a server that can
 * reach the open internet — for a panel that is normally on your own machine or
 * your own network, and that you keep open in a tab. What this does instead
 * works whenever the page is alive, including in a background tab and in an
 * installed PWA, which is the case that matters.
 *
 * Through the service worker rather than `new Notification()`, because Android
 * refuses that constructor outright and the registration is the only way in.
 */
const KEY = 'vibepanel.notify'

export function notifyEnabled(): boolean {
  try {
    return localStorage.getItem(KEY) === 'on'
  } catch {
    return false
  }
}

export function setNotifyEnabled(on: boolean) {
  try {
    localStorage.setItem(KEY, on ? 'on' : 'off')
  } catch {
    /* private mode: this tab still honours it for as long as it lives */
  }
}

export function notifySupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    'Notification' in window &&
    'serviceWorker' in navigator &&
    // A secure context, which over plain HTTP means localhost only. Saying so
    // is better than a button that asks for permission and is refused by the
    // browser with no explanation the user can act on.
    window.isSecureContext
  )
}

/**
 * Ask, from a click.
 *
 * Permission prompts fired on load are the reason browsers now ignore them, and
 * Safari refuses one that is not inside a user gesture at all.
 */
export async function requestNotifyPermission(): Promise<NotificationPermission> {
  if (!notifySupported()) return 'denied'
  const result = await Notification.requestPermission()
  setNotifyEnabled(result === 'granted')
  return result
}

let lastState = new Map<string, string>()

/**
 * One notification per session that has just started waiting.
 *
 * Only on the transition, never on the state: the poller broadcasts every two
 * seconds, and a notification per broadcast is a phone nobody keeps installed.
 *
 * The first snapshot after a load seeds the map without notifying. Opening the
 * panel to three waiting agents you already knew about, and being told about
 * all three, is the same mistake one step earlier.
 */
export function notifyOnWaiting(sessions: Session[], focused: boolean) {
  const seeded = lastState.size > 0
  const next = new Map<string, string>()
  const newlyWaiting: Session[] = []
  for (const s of sessions) {
    next.set(s.id, s.state)
    if (seeded && s.state === 'waiting' && lastState.get(s.id) !== 'waiting') {
      newlyWaiting.push(s)
    }
  }
  lastState = next
  if (!seeded || newlyWaiting.length === 0) return
  // Not while you are looking at it. The sidebar has already moved the session
  // to the top and drawn a triangle on it; a notification on top of that is
  // noise, and noise is how notifications get turned off.
  if (focused) return
  if (!notifyEnabled() || !notifySupported() || Notification.permission !== 'granted') return

  void navigator.serviceWorker.ready
    .then((reg) => {
      for (const s of newlyWaiting) {
        void reg.showNotification(t('notify.waitingTitle'), {
          body: t('notify.waitingBody', { name: sessionLabel(s) }),
          icon: '/icon-192.png',
          badge: '/icon-192.png',
          // One notification per session rather than a pile: a second report
          // about the same session replaces the first.
          tag: `vibepanel-waiting-${s.id}`,
          data: { sessionId: s.id },
        })
      }
    })
    .catch(() => {
      // No registration. Nothing to do and nothing worth telling anybody: the
      // settings page is where the state of this is answered.
    })
}

/** Forget what was seen, so a reconnect does not announce the whole list. */
export function resetNotifyState() {
  lastState = new Map()
}
