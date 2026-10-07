/*
 * vibepanel-plugin.js — the plugin SDK, contract v1. docs/plugins.md §5.
 *
 * The panel serves this file beside every frame's entry, whatever the plugin's
 * directory holds, so a frame always runs the SDK that matches the panel
 * serving it. A copy sits in a scaffolded directory for editors; it is not
 * published.
 *
 *   const vp = VibePanel.plugin()
 *   vp.caps                                  // what was granted; degrade on it, do not ask
 *   vp.on('view', (v) => draw(v))            // the plugin's view of the panel, pushed
 *   vp.on('context', (c) => ...)             // { session, project, theme, lang, slot, narrow }
 *   vp.on('status', (st) => ...)             // 'connecting' | 'live' | 'reconnecting' | 'disconnected' | 'revoked'
 *   vp.data.get / set / increment / append / reset
 *   vp.settings()                            // the values the panel's form holds
 *   vp.notes.get(h) / vp.notes.set(h, text)  // 403 without the capability
 *   vp.todos.list(h) / add(h, text) / set(h, {done}) / remove(h)
 *   vp.sessions.state(h, 'done') / screen(h)
 *   vp.ui.open({ session: h }) / vp.ui.notify(text) / vp.ui.height(px)
 *   vp.ui.confirm({ title, body, confirm, cancel }) / vp.ui.toast(text)
 *   vp.route('GET', 'digest')                // the plugin's own routes, once it has a service
 *   vp.text(el, value); vp.badge(el, state); vp.fmt.*; vp.since(unix)
 *
 * Every request carries the grant in the address and nothing else: the frame
 * has no cookie and no storage (sandbox allow-scripts), and the grant reaches
 * /api/plugin/<grant>/v1/ and nothing else on the panel.
 *
 * Written as one plain script with no build and no dependencies, because it
 * is read by the people and agents writing plugins as much as it is run.
 */
(function (global) {
  'use strict'

  var SDK_VERSION = 1
  var POLL_MS = 2000
  var MAX_BACKOFF_MS = 30000
  var RECONNECTING_MS = 10000
  var GRANT_IN_PATH = /\/plugin\/([A-Za-z0-9_-]{20,})(?:\/|$)/

  // ─── text that lies about itself ─────────────────────────────────────────
  // The characters the panel's own safeText replaces: C0 and C1 controls,
  // the soft hyphen, the bidi embeddings, overrides, isolates and marks, and
  // the zero-width invisibles. Built from code points so this file holds no
  // invisible characters of its own.
  var DECEPTIVE = (function () {
    var ranges = [[0x00, 0x1f], [0x7f, 0x9f], [0xad, 0xad], [0x202a, 0x202e], [0x2066, 0x2069],
      [0x200e, 0x200f], [0x200b, 0x200b], [0x2060, 0x2060], [0xfeff, 0xfeff], [0x061c, 0x061c]]
    var cls = ''
    for (var i = 0; i < ranges.length; i++) {
      cls += String.fromCharCode(ranges[i][0]) + '-' + String.fromCharCode(ranges[i][1])
    }
    return new RegExp('[' + cls + ']', 'g')
  })()

  function honest(s) {
    return String(s == null ? '' : s).replace(DECEPTIVE, String.fromCharCode(0xfffd))
  }

  function trim(v) {
    return v < 10 ? v.toFixed(1).replace(/\.0$/, '') : v.toFixed(0)
  }

  var fmt = {
    tokens: function (n) {
      if (typeof n !== 'number' || !isFinite(n)) return '—'
      var abs = Math.abs(n)
      if (abs < 1000) return String(Math.round(n))
      if (abs < 1e6) return trim(n / 1e3) + 'K'
      if (abs < 1e9) return trim(n / 1e6) + 'M'
      return trim(n / 1e9) + 'B'
    },
    bytes: function (n) {
      if (typeof n !== 'number' || !isFinite(n) || n < 0) return '—'
      var units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
      var i = 0
      while (n >= 1024 && i < units.length - 1) {
        n /= 1024
        i++
      }
      return (i === 0 ? String(n) : trim(n)) + ' ' + units[i]
    },
    percent: function (n, digits) {
      if (typeof n !== 'number' || !isFinite(n)) return '—'
      return n.toFixed(digits || 0) + '%'
    },
    duration: function (seconds) {
      if (typeof seconds !== 'number' || !isFinite(seconds)) return '—'
      if (seconds < 60) return Math.max(0, Math.floor(seconds)) + 's'
      var d = Math.floor(seconds / 86400)
      var h = Math.floor((seconds % 86400) / 3600)
      var m = Math.floor((seconds % 3600) / 60)
      if (d > 0) return d + 'd ' + h + 'h'
      if (h > 0) return h + 'h ' + m + 'm'
      return m + 'm'
    },
    number: function (n) {
      if (typeof n !== 'number' || !isFinite(n)) return '—'
      return n.toLocaleString()
    },
  }

  function framed() {
    try {
      return global.parent !== global
    } catch (e) {
      return true
    }
  }

  // ─── the bridge to the panel ────────────────────────────────────────────
  //
  // Messages go to the parent with targetOrigin '*'. That is safe here and
  // only here: a frame's files carry frame-ancestors 'self', so the only
  // window that can be this frame's parent is the panel's, and what is sent
  // is a request the panel checks against the grant (ui:open, ui:notify) or a
  // size. Answers come back tagged with the request's id.
  var pending = {}
  var nextID = 1

  function post(msg) {
    if (!framed()) return
    try {
      global.parent.postMessage(msg, '*')
    } catch (e) {
      /* a parent that went away is not the frame's problem */
    }
  }

  function ask(type, payload) {
    return new Promise(function (resolve) {
      if (!framed()) {
        resolve(null)
        return
      }
      var id = nextID++
      pending[id] = resolve
      var msg = { type: type, id: id }
      for (var k in payload) if (Object.prototype.hasOwnProperty.call(payload, k)) msg[k] = payload[k]
      post(msg)
      global.setTimeout(function () {
        if (pending[id]) {
          delete pending[id]
          resolve(null)
        }
      }, 60000)
    })
  }

  // ─── the client ─────────────────────────────────────────────────────────

  function Client(options) {
    options = options || {}
    var self = this
    var grant = options.grant || (GRANT_IN_PATH.exec(global.location.pathname) || [])[1] || ''
    var base = options.base || (global.location.origin + '/api/plugin/' + grant + '/v1/')
    this.version = SDK_VERSION
    this.caps = []
    this.plugin = null
    this.status = 'connecting'
    this.context = { session: null, project: null, theme: '', lang: 'en', slot: '', narrow: false }
    this.view = null
    this.fmt = fmt

    var handlers = {}
    var lastGood = 0
    var backoff = POLL_MS
    var timer = 0
    var stream = null
    var closed = false

    function emit(name, arg) {
      var list = handlers[name] || []
      for (var i = 0; i < list.length; i++) {
        try {
          list[i](arg)
        } catch (e) {
          try {
            global.console.error(e)
          } catch (e2) {
            /* no console */
          }
        }
      }
    }

    function setStatus(st) {
      if (self.status === st) return
      self.status = st
      emit('status', st)
    }

    function request(method, path, body) {
      var init = { method: method, headers: {} }
      if (body !== undefined) {
        init.headers['Content-Type'] = 'application/json'
        init.body = JSON.stringify(body)
      }
      return global.fetch(base + path.replace(/^\//, ''), init).then(function (res) {
        if (res.status === 401 || res.status === 410) {
          setStatus('revoked')
        }
        return res.text().then(function (text) {
          var data = null
          try {
            data = text ? JSON.parse(text) : null
          } catch (e) {
            data = { error: text }
          }
          if (!res.ok) {
            var err = new Error((data && data.error) || ('HTTP ' + res.status))
            err.status = res.status
            err.cap = data && data.cap
            throw err
          }
          return data
        })
      })
    }
    this.request = request

    function applyView(v) {
      self.view = v
      if (v && v.caps) self.caps = v.caps
      if (v && v.plugin) self.plugin = v.plugin
      lastGood = Date.now()
      backoff = POLL_MS
      setStatus('live')
      emit('view', v)
    }

    function poll() {
      if (closed) return
      request('GET', self.caps.indexOf('read:panel') >= 0 || self.caps.indexOf('read:paths') >= 0 || !self.plugin ? 'view' : 'me')
        .then(function (v) {
          applyView(v)
          schedule(POLL_MS)
        })
        .catch(function (e) {
          if (e && e.status === 403) {
            // No read:panel: the view is refused, and 'me' is what the plugin
            // can know. Not an outage.
            request('GET', 'me').then(applyView).catch(function () {})
            schedule(POLL_MS * 5)
            return
          }
          if (self.status !== 'revoked') {
            setStatus(Date.now() - lastGood < RECONNECTING_MS ? 'reconnecting' : 'disconnected')
          }
          backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
          schedule(backoff)
        })
    }

    function schedule(ms) {
      if (closed) return
      if (stream) return
      global.clearTimeout(timer)
      timer = global.setTimeout(poll, ms)
    }

    // Push first, poll as the fallback: the events route is a stream the
    // panel writes to within its coalesce window whenever anything changes.
    function connectStream() {
      if (!global.EventSource || options.poll) return false
      try {
        stream = new global.EventSource(base + 'events')
      } catch (e) {
        stream = null
        return false
      }
      stream.addEventListener('view', function (ev) {
        try {
          applyView(JSON.parse(ev.data))
        } catch (e) {
          /* a frame cannot do anything with a bad message */
        }
      })
      stream.onerror = function () {
        // EventSource reconnects by itself; while it does, the poll covers.
        if (self.status === 'live') setStatus('reconnecting')
        try {
          stream.close()
        } catch (e) {
          /* already closed */
        }
        stream = null
        schedule(backoff)
      }
      return true
    }

    this.on = function (name, fn) {
      ;(handlers[name] = handlers[name] || []).push(fn)
      if (name === 'view' && self.view) fn(self.view)
      if (name === 'context') fn(self.context)
      if (name === 'status') fn(self.status)
      return self
    }
    this.off = function (name, fn) {
      handlers[name] = (handlers[name] || []).filter(function (f) {
        return f !== fn
      })
      return self
    }
    this.close = function () {
      closed = true
      global.clearTimeout(timer)
      if (stream) stream.close()
    }
    this.refresh = function () {
      return request('GET', 'view').then(applyView)
    }

    // ─── what the capabilities open ──────────────────────────────────────
    this.data = {
      get: function () {
        return request('GET', 'data')
      },
      set: function (key, value) {
        return request('PUT', 'data/' + encodeURIComponent(key), { value: value })
      },
      increment: function (key, by) {
        return request('POST', 'data/' + encodeURIComponent(key) + '/increment', { by: by == null ? 1 : by })
      },
      append: function (key, item) {
        return request('POST', 'data/' + encodeURIComponent(key) + '/append', { item: item })
      },
      reset: function (key) {
        return request('DELETE', 'data/' + encodeURIComponent(key))
      },
    }
    this.settings = function () {
      return request('GET', 'settings')
    }
    this.notes = {
      get: function (project) {
        return request('GET', 'projects/' + encodeURIComponent(project) + '/notes')
      },
      set: function (project, content, baseRev) {
        return request('PUT', 'projects/' + encodeURIComponent(project) + '/notes',
          baseRev == null ? { content: content } : { content: content, baseRev: baseRev })
      },
    }
    this.todos = {
      list: function (project) {
        return request('GET', 'projects/' + encodeURIComponent(project) + '/todos')
      },
      add: function (project, text) {
        return request('POST', 'projects/' + encodeURIComponent(project) + '/todos', { text: text })
      },
      set: function (todo, patch) {
        return request('PATCH', 'todos/' + encodeURIComponent(todo), patch)
      },
      remove: function (todo) {
        return request('DELETE', 'todos/' + encodeURIComponent(todo))
      },
    }
    this.sessions = {
      state: function (session, state) {
        return request('PATCH', 'sessions/' + encodeURIComponent(session) + '/state', { state: state })
      },
      screen: function (session) {
        return request('GET', 'sessions/' + encodeURIComponent(session) + '/screen')
      },
    }
    this.projects = {
      git: function (project) {
        return request('GET', 'projects/' + encodeURIComponent(project) + '/git')
      },
    }
    this.resources = function () {
      return request('GET', 'resources')
    }
    this.usage = function () {
      return request('GET', 'usage')
    }
    this.route = function (method, path, body) {
      return request(method, 'x/' + String(path).replace(/^\//, ''), body)
    }

    // ─── the panel around the frame ──────────────────────────────────────
    var sizeTimer = 0
    this.ui = {
      /** Ask the panel for this height. Called for you after each view, from the document's own height. */
      height: function (px) {
        post({ type: 'height', px: Math.max(0, Math.round(px)) })
      },
      /** Ask the panel to open a session, a project or a settings section. Refused without ui:open. */
      open: function (what) {
        return ask('open', what || {})
      },
      /** A toast in the panel. Refused without ui:notify. */
      notify: function (text, kind) {
        return ask('notify', { text: honest(text), kind: kind || 'info' })
      },
      toast: function (text, kind) {
        return ask('notify', { text: honest(text), kind: kind || 'info' })
      },
      /** The panel's own confirm dialog. Resolves true or false. */
      confirm: function (q) {
        q = q || {}
        return ask('confirm', {
          title: honest(q.title), body: honest(q.body || ''), confirm: honest(q.confirm || ''),
          cancel: honest(q.cancel || ''), destructive: !!q.destructive,
        }).then(function (a) {
          return a === true
        })
      },
      /** The panel's own menu, at the frame's position. Resolves the chosen item's id or null. */
      menu: function (items) {
        var clean = []
        for (var i = 0; i < (items || []).length && i < 20; i++) {
          clean.push({ id: String(items[i].id || i), label: honest(items[i].label), destructive: !!items[i].destructive })
        }
        return ask('menu', { items: clean })
      },
    }

    function fitHeight() {
      global.clearTimeout(sizeTimer)
      sizeTimer = global.setTimeout(function () {
        var doc = global.document
        if (!doc || !doc.documentElement) return
        var h = Math.max(doc.documentElement.scrollHeight, doc.body ? doc.body.scrollHeight : 0)
        self.ui.height(h)
      }, 50)
    }

    // ─── drawing helpers ─────────────────────────────────────────────────
    this.text = function (el, value) {
      if (el) el.textContent = honest(value)
      return el
    }
    this.honest = honest
    this.since = function (unix) {
      if (typeof unix !== 'number' || unix <= 0) return '—'
      return fmt.duration(Math.max(0, Math.floor(Date.now() / 1000) - unix))
    }
    /** A state mark: shape and hue, never hue alone. */
    this.badge = function (el, state) {
      if (!el) return el
      var shape = { working: '●', waiting: '▲', done: '✓' }[state] || '○'
      el.setAttribute('data-state', state || '')
      el.className = (el.className || '').replace(/\bvp-state-\S+/g, '').trim() + ' vp-state vp-state-' + (state || 'none')
      el.textContent = shape
      el.setAttribute('title', state || '')
      return el
    }

    // ─── messages from the panel ─────────────────────────────────────────
    global.addEventListener('message', function (ev) {
      var m = ev.data
      if (!m || typeof m !== 'object') return
      if (m.type === 'context' && m.context) {
        var c = m.context
        self.context = {
          session: c.session || null, project: c.project || null, theme: String(c.theme || ''),
          lang: c.lang === 'zh' ? 'zh' : 'en', slot: String(c.slot || ''), narrow: !!c.narrow,
        }
        try {
          global.document.documentElement.lang = self.context.lang === 'zh' ? 'zh-CN' : 'en'
          global.document.documentElement.setAttribute('data-theme', self.context.theme)
        } catch (e) {
          /* no document */
        }
        emit('context', self.context)
      } else if (m.type === 'answer' && m.id && pending[m.id]) {
        var resolve = pending[m.id]
        delete pending[m.id]
        resolve(m.hasOwnProperty('value') ? m.value : null)
      } else if (m.type === 'refresh') {
        self.refresh().catch(function () {})
      }
    })

    // Errors and size, for the panel's dev mode and for the pane's height.
    if (framed()) {
      global.addEventListener('error', function (e) {
        post({ type: 'error', message: String((e && e.message) || '').slice(0, 500) })
      })
      global.addEventListener('unhandledrejection', function (e) {
        post({ type: 'error', message: String((e && e.reason && e.reason.message) || e.reason || '').slice(0, 500) })
      })
      try {
        if (global.ResizeObserver) {
          new global.ResizeObserver(fitHeight).observe(global.document.documentElement)
        }
      } catch (e) {
        /* no observer: the first view sets a size */
      }
      this.on('view', fitHeight)
      post({ type: 'ready' })
    }

    if (!connectStream()) schedule(0)
    else poll()
  }

  global.VibePanel = global.VibePanel || {}
  global.VibePanel.version = SDK_VERSION
  global.VibePanel.plugin = function (options) {
    return new Client(options)
  }
  global.VibePanel.fmt = fmt
  global.VibePanel.honest = honest
})(typeof window !== 'undefined' ? window : globalThis)
