// The service worker exists for two things, and caching is not one of them.
//
// Installability: a browser will not offer "add to home screen" without a
// registered worker that has a fetch handler. Notifications: on Android the
// `new Notification()` constructor is refused outright, and the only way to
// show one from a page is through this registration.
//
// It deliberately does NOT cache the app shell. A panel that serves a cached
// bundle after its binary has been upgraded is the exact failure the upgrade
// notice was added to catch, and a service worker is a very effective way to
// make that permanent -- people have shipped stale PWAs for weeks. Everything
// goes to the network; the panel is on your own machine or your own network,
// so there is nothing an offline cache would buy that is worth that risk.
self.addEventListener('install', () => {
  // Take over immediately rather than waiting for every tab to close. The old
  // worker has no state worth preserving, and a worker that activates "later"
  // is one whose fixes arrive at a time nobody can predict.
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim())
})

self.addEventListener('fetch', (event) => {
  // Anything that is not a GET goes to the network without this worker in the
  // way: returning without respondWith is the browser's own request, as if no
  // worker were registered.
  //
  // It used to pass everything through, writes included, and an upload is the
  // one request where that costs something. The page hands the whole body to
  // the worker, which sends it again; XMLHttpRequest's upload progress then
  // measures the hand-over, which is instant, so a large file's bar ran to the
  // end and sat there until the real transfer finished. And every response --
  // a proxy's bare 413 included -- showed in DevTools as "from ServiceWorker",
  // which read as this worker being what refused the file.
  if (event.request.method !== 'GET') return
  // Required for installability, and otherwise a pass-through. Written out
  // rather than left empty so that the next person to open this file sees the
  // decision instead of an apparent omission.
  event.respondWith(fetch(event.request))
})

// Tapping the notification should put you in front of the session it is about,
// not open a second copy of the panel.
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const sessionId = event.notification.data?.sessionId
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
      for (const client of list) {
        if ('focus' in client) {
          if (sessionId) client.postMessage({ t: 'focus-session', sessionId })
          return client.focus()
        }
      }
      return self.clients.openWindow(sessionId ? `/?session=${encodeURIComponent(sessionId)}` : '/')
    }),
  )
})
