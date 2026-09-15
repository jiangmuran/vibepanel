/*
 * vibepanel.js — the share page SDK, contract v1.
 *
 * The panel serves this file at ./vibepanel.js inside every page, whatever the
 * page's directory holds, so a page always runs the SDK that matches the panel
 * serving it. A copy sits in a scaffolded directory for editors; it is not
 * published.
 *
 *   const vp = VibePanel.connect()
 *   vp.on('snapshot', (s) => draw(s))
 *   vp.on('status', (st) => ...)       // 'connecting' | 'live' | 'reconnecting' | 'disconnected' | 'revoked'
 *   vp.badge(document.querySelector('#status'))
 *
 * vibepanel.d.ts declares every field of a snapshot. docs/share-pages.md is the
 * design, including what a page cannot do and why.
 *
 * Written as one plain script with no build and no dependencies, because it is
 * read by the people and agents writing pages as much as it is run.
 */
(function (global) {
  'use strict'

  var SDK_VERSION = 1

  /** How often a wall asks, when all is well: two seconds, the number walls have always polled at. */
  var POLL_MS = 2000
  /** The longest a failing wall waits between attempts. */
  var MAX_BACKOFF_MS = 30000
  /**
   * How long after the last good answer a failure is "reconnecting" rather
   * than "disconnected". A wall that lost one poll is not a wall that is down,
   * and one that has been silent for ten seconds is not one that is fine.
   */
  var RECONNECTING_MS = 10000
  /** The most a room of screens staggers its reload after a publish. */
  var RELOAD_JITTER_MS = 5000

  var TOKEN_IN_PATH = /\/share\/([A-Za-z0-9_-]{20,})(?:\/|$)/
  var FIXTURE_NAME = /^[a-z0-9][a-z0-9-]{0,39}$/

  // ─── small helpers ──────────────────────────────────────────────────────

  function query(name) {
    try {
      return new URL(global.location.href).searchParams.get(name)
    } catch (e) {
      return null
    }
  }

  function randomHex(n) {
    var out = ''
    try {
      var bytes = new Uint8Array(n / 2)
      global.crypto.getRandomValues(bytes)
      for (var i = 0; i < bytes.length; i++) out += ('0' + bytes[i].toString(16)).slice(-2)
      return out
    } catch (e) {
      for (var j = 0; j < n; j++) out += Math.floor(Math.random() * 16).toString(16)
      return out
    }
  }

  function framed() {
    try {
      return global.parent !== global
    } catch (e) {
      return true
    }
  }

  function isZh() {
    var lang = ''
    try {
      lang = (global.document.documentElement.lang || global.navigator.language || '').toLowerCase()
    } catch (e) {
      lang = ''
    }
    return lang.indexOf('zh') === 0
  }

  // ─── text that lies about itself ─────────────────────────────────────────
  //
  // The characters the panel's own safeText replaces, for the same reason:
  // a session title is set by whatever program ran in the pane, and a bidi
  // override in one reverses everything drawn after it -- measured on the
  // hostile fixture, where "RTL override" rendered as "edirrevo LTR". C0 and
  // C1 controls, the soft hyphen, the bidi embeddings, overrides, isolates and
  // marks, and the zero-width invisibles that make two names one row of
  // pixels. U+200C and U+200D are left alone: emoji sequences and Persian
  // need them.
  //
  // Built from code points rather than written as a literal class, so this
  // file holds no invisible characters of its own.
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
    return String(s).replace(DECEPTIVE, String.fromCharCode(0xfffd))
  }

  // ─── formatting ─────────────────────────────────────────────────────────
  //
  // For a screen read at three metres. 41,283,904 is correct and unusable;
  // 41M is what somebody standing up reads. One decimal below ten and none
  // above, the board's rule: "9.4M" and "412M" are the same width to read.

  function trim(v) {
    return v < 10 ? v.toFixed(1).replace(/\.0$/, '') : v.toFixed(0)
  }

  var fmt = {
    /** 480, 9.4K, 41M, 2.1B. */
    tokens: function (n) {
      if (typeof n !== 'number' || !isFinite(n)) return '—'
      var abs = Math.abs(n)
      if (abs < 1000) return String(Math.round(n))
      if (abs < 1e6) return trim(n / 1e3) + 'K'
      if (abs < 1e9) return trim(n / 1e6) + 'M'
      return trim(n / 1e9) + 'B'
    },
    /** 512 B, 3.4 GiB. */
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
    /** 42%. Null and NaN are "—", never 0%: an unmeasured CPU is not an idle one. */
    percent: function (n, digits) {
      if (typeof n !== 'number' || !isFinite(n)) return '—'
      return n.toFixed(digits || 0) + '%'
    },
    /** 45s, 14m, 3h 5m, 2d 4h. */
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
    /** 1,234,567 in the reader's own grouping. */
    number: function (n) {
      if (typeof n !== 'number' || !isFinite(n)) return '—'
      return n.toLocaleString()
    },
  }

  // ─── an in-memory Storage ────────────────────────────────────────────────
  //
  // A page runs in a sandbox with no origin, and there localStorage does not
  // return null -- it throws, on the first line that touches it. This is the
  // same interface, kept for as long as the page is open.

  function MemoryStorage() {
    var data = {}
    return {
      getItem: function (k) {
        return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null
      },
      setItem: function (k, v) {
        data[String(k)] = String(v)
      },
      removeItem: function (k) {
        delete data[k]
      },
      clear: function () {
        data = {}
      },
      key: function (i) {
        return Object.keys(data)[i] || null
      },
      get length() {
        return Object.keys(data).length
      },
    }
  }

  // ─── the Preview pane's instruments ──────────────────────────────────────
  //
  // Only when the page is framed (the Preview pane) or asked for a report
  // (`vibepanel page shot`). A page on a wall runs none of this.
  //
  // Messages go to the parent with targetOrigin '*'. That is safe here and
  // only here: a page link is never framable (frame-ancestors 'none'), and a
  // preview link is framable by the panel alone (frame-ancestors 'self'), so
  // the only window that can be this page's parent is the panel's. And what is
  // sent is what the page already shows.

  var MAX_REPORTED = 50

  function Instruments(client) {
    var errors = []
    var flushTimer = 0
    var reportEl = null
    var shot = query('shot') === '1'
    var inFrame = framed()
    var active = shot || inFrame

    function post(msg) {
      if (!inFrame) return
      try {
        global.parent.postMessage(msg, '*')
      } catch (e) {
        /* a parent that went away is not the page's problem */
      }
    }

    function record(kind, message, source, line) {
      if (errors.length >= MAX_REPORTED) return
      errors.push({
        kind: kind,
        message: String(message || '').slice(0, 500),
        source: String(source || '').replace(/^.*\/share\/[^/]+\//, ''),
        line: line || 0,
      })
      schedule()
    }

    function schedule() {
      if (flushTimer) return
      flushTimer = global.setTimeout(function () {
        flushTimer = 0
        post({ type: 'vp.errors', errors: errors.slice() })
        writeReport()
      }, 250)
    }

    function suspiciousText() {
      var found = []
      var doc = global.document
      if (!doc || !doc.body || !doc.createTreeWalker) return found
      var walker = doc.createTreeWalker(doc.body, 4 /* NodeFilter.SHOW_TEXT */)
      var node
      while ((node = walker.nextNode()) && found.length < 20) {
        var parent = node.parentNode
        if (parent && (parent.nodeName === 'SCRIPT' || parent.nodeName === 'STYLE')) continue
        var text = (node.nodeValue || '').trim()
        if (!text) continue
        if (text === 'null' || /\bNaN\b|\bundefined\b|\[object Object\]|\bInfinity\b/.test(text)) {
          found.push(text.slice(0, 80))
        }
      }
      return found
    }

    function writeReport() {
      if (!shot) return
      var doc = global.document
      if (!doc || !doc.body) return
      if (!reportEl) {
        reportEl = doc.createElement('script')
        reportEl.type = 'application/json'
        reportEl.id = 'vp-report'
        doc.body.appendChild(reportEl)
      }
      var root = doc.documentElement
      reportEl.textContent = JSON.stringify({
        status: client.status,
        sections: client.snapshot ? client.snapshot.sections : [],
        errors: errors,
        suspicious: suspiciousText(),
        overflowX: root.scrollWidth > global.innerWidth + 1,
        overflowY: root.scrollHeight > global.innerHeight + 1,
        width: global.innerWidth,
        height: global.innerHeight,
      })
    }

    // ── picking an element for the agent ──
    var picking = false
    var outline = null

    function describe(el) {
      var parts = []
      var node = el
      while (node && node.nodeType === 1 && node.nodeName !== 'BODY' && parts.length < 5) {
        var part = node.nodeName.toLowerCase()
        if (node.id && /^[A-Za-z][\w-]*$/.test(node.id)) {
          parts.unshift(part + '#' + node.id)
          break
        }
        var classes = (typeof node.className === 'string' ? node.className : '')
          .split(/\s+/)
          .filter(function (c) {
            return /^[A-Za-z_][\w-]*$/.test(c)
          })
          .slice(0, 2)
        if (classes.length) part += '.' + classes.join('.')
        var parentNode = node.parentNode
        if (parentNode && parentNode.children) {
          var same = 0
          var index = 0
          for (var i = 0; i < parentNode.children.length; i++) {
            if (parentNode.children[i].nodeName === node.nodeName) {
              same++
              if (parentNode.children[i] === node) index = same
            }
          }
          if (same > 1 && !classes.length) part += ':nth-of-type(' + index + ')'
        }
        parts.unshift(part)
        node = node.parentNode
      }
      return parts.join(' > ')
    }

    function box(el) {
      var r = el.getBoundingClientRect()
      return { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) }
    }

    function onMove(e) {
      if (!picking || !outline) return
      var b = box(e.target)
      outline.style.left = b.x + 'px'
      outline.style.top = b.y + 'px'
      outline.style.width = b.w + 'px'
      outline.style.height = b.h + 'px'
    }

    function onClick(e) {
      if (!picking) return
      e.preventDefault()
      e.stopPropagation()
      var el = e.target
      var text = (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 60)
      post({
        type: 'vp.picked',
        selector: describe(el),
        text: text,
        rect: box(el),
        viewport: { w: global.innerWidth, h: global.innerHeight },
      })
      setPicking(false)
    }

    function onKey(e) {
      if (picking && e.key === 'Escape') setPicking(false)
    }

    function setPicking(on) {
      var doc = global.document
      if (on === picking || !doc || !doc.body) return
      picking = on
      if (on) {
        outline = doc.createElement('div')
        outline.setAttribute('data-vp-pick', '')
        outline.style.cssText =
          'position:fixed;pointer-events:none;z-index:2147483647;' +
          'outline:2px solid #4f7cff;outline-offset:1px;background:rgba(79,124,255,.12);' +
          'left:0;top:0;width:0;height:0'
        doc.body.appendChild(outline)
        doc.addEventListener('mousemove', onMove, true)
        doc.addEventListener('click', onClick, true)
        doc.addEventListener('keydown', onKey, true)
        doc.documentElement.style.cursor = 'crosshair'
      } else {
        if (outline && outline.parentNode) outline.parentNode.removeChild(outline)
        outline = null
        doc.removeEventListener('mousemove', onMove, true)
        doc.removeEventListener('click', onClick, true)
        doc.removeEventListener('keydown', onKey, true)
        doc.documentElement.style.cursor = ''
        post({ type: 'vp.pick', on: false })
      }
    }

    if (active) {
      global.addEventListener('error', function (e) {
        // A resource that failed to load reports on the element, with no
        // message; a script error reports on the window, with one.
        var target = e.target
        if (target && target !== global && (target.src || target.href)) {
          record('load', 'could not load ' + (target.src || target.href), '', 0)
          return
        }
        record('error', e.message, e.filename, e.lineno)
      }, true)
      global.addEventListener('unhandledrejection', function (e) {
        var reason = e.reason
        record('rejection', reason && reason.message ? reason.message : String(reason), '', 0)
      })
      global.document.addEventListener('securitypolicyviolation', function (e) {
        record('policy', 'refused ' + (e.blockedURI || 'inline') + ' (' + e.effectiveDirective + ')',
          e.sourceFile, e.lineNumber)
      })
      var original = global.console && global.console.error
      if (original) {
        global.console.error = function () {
          var parts = []
          for (var i = 0; i < arguments.length; i++) {
            var a = arguments[i]
            parts.push(a && a.message ? a.message : typeof a === 'object' ? safeJSON(a) : String(a))
          }
          record('console', parts.join(' '), '', 0)
          return original.apply(this, arguments)
        }
      }
    }
    if (inFrame) {
      global.addEventListener('message', function (e) {
        // The parent and nothing else. Its origin is the panel's, but the
        // check that means something is the window: a message from any other
        // frame is not the Preview pane, whatever it claims.
        if (e.source !== global.parent) return
        var data = e.data
        if (!data || typeof data !== 'object') return
        if (data.type === 'vp.pick') setPicking(Boolean(data.on))
      })
    }

    return {
      active: active,
      snapshot: function () {
        if (!active) return
        post({ type: 'vp.ready', page: client.snapshot ? client.snapshot.page : null, status: client.status })
        // After the page has drawn from it: listeners run synchronously, so a
        // frame later is after the page's own DOM work.
        if (shot) global.requestAnimationFrame(function () { writeReport() })
      },
      status: function () {
        post({ type: 'vp.status', status: client.status })
        writeReport()
      },
      error: function (message) {
        record('snapshot', message, '', 0)
      },
    }
  }

  function safeJSON(v) {
    try {
      return JSON.stringify(v)
    } catch (e) {
      return String(v)
    }
  }

  // ─── the client ──────────────────────────────────────────────────────────

  function Client(options) {
    options = options || {}
    var self = this
    var listeners = { snapshot: [], status: [], params: [], error: [] }
    var stopped = false
    var timer = 0
    var failures = 0
    var lastOk = 0
    var offset = 0
    var pageKey
    var paramsKey = ''
    var waitingForVisible = false
    var viewer = randomHex(16)

    var href = global.location ? global.location.href : ''
    var base = options.base
    var token = options.token
    try {
      var url = new URL(href)
      if (!base) base = url.protocol + '//' + url.host
      if (!token) {
        var m = TOKEN_IN_PATH.exec(url.pathname)
        token = m ? m[1] : ''
      }
    } catch (e) {
      /* no location: a test, or a page handed base and token */
    }
    base = (base || '').replace(/\/+$/, '')

    this.snapshot = null
    this.status = 'connecting'
    this.params = {}
    this.error = null
    this.storage = MemoryStorage()
    this.fmt = fmt
    this.version = SDK_VERSION

    var instruments = Instruments(this)

    function emit(event, value) {
      var list = listeners[event].slice()
      for (var i = 0; i < list.length; i++) {
        try {
          list[i](value, self)
        } catch (e) {
          // One broken listener must not stop the others drawing, and must
          // not stop the poll. Reported where somebody can see it.
          if (global.console && global.console.error) global.console.error(e)
        }
      }
    }

    function setStatus(next) {
      if (self.status === next) return
      self.status = next
      emit('status', next)
      instruments.status()
    }

    function accept(snapshot) {
      if (!snapshot || typeof snapshot !== 'object') return
      if (typeof snapshot.at === 'number') offset = snapshot.at - Date.now() / 1000
      var key = snapshot.page ? snapshot.page.id + ':' + snapshot.page.version : 'none'
      if (pageKey === undefined) {
        pageKey = key
      } else if (key !== pageKey && !(snapshot.page && snapshot.page.draft)) {
        // A new version was published, a trial started or ended, or the link
        // now draws something else. Staggered, so a room of screens does not
        // arrive at the panel in the same second.
        pageKey = key
        stop()
        global.setTimeout(function () {
          global.location.reload()
        }, Math.random() * RELOAD_JITTER_MS)
        return
      }
      self.snapshot = snapshot
      self.error = null
      emit('snapshot', snapshot)
      var nextParams = snapshot.params || {}
      var nextKey = safeJSON(nextParams)
      if (nextKey !== paramsKey) {
        paramsKey = nextKey
        self.params = nextParams
        emit('params', nextParams)
      }
      instruments.snapshot()
    }

    function schedule(ms) {
      if (stopped) return
      var jittered = ms * (0.8 + Math.random() * 0.4)
      timer = global.setTimeout(tick, jittered)
    }

    function fail(message) {
      failures++
      if (message) {
        self.error = message
        emit('error', message)
        instruments.error(message)
      }
      setStatus(lastOk > 0 && Date.now() - lastOk < RECONNECTING_MS ? 'reconnecting' : 'disconnected')
      schedule(Math.min(POLL_MS * Math.pow(2, failures), MAX_BACKOFF_MS))
    }

    function tick() {
      timer = 0
      if (stopped) return
      if (global.document && global.document.hidden) {
        // Nobody is looking at this tab. The next look asks straight away.
        waitingForVisible = true
        return
      }
      var url = base + '/api/share/' + encodeURIComponent(token) + '/v1/snapshot' +
        '?v=' + viewer +
        '&w=' + Math.round(global.innerWidth || 0) +
        '&h=' + Math.round(global.innerHeight || 0)
      global.fetch(url, { cache: 'no-store', credentials: 'omit' }).then(function (res) {
        if (res.status === 401 || res.status === 410) {
          // Revoked, expired, or the page is gone. Terminal: it is not going to
          // start working, and asking again forever is an unauthenticated
          // request in a loop against an endpoint that records rejections. So
          // this branch schedules nothing, which is the whole of the stop; a
          // `stopped = true` here was measured to change nothing and removed.
          return res.json().catch(function () { return {} }).then(function (body) {
            self.error = body && body.error ? body.error : null
            setStatus('revoked')
          })
        }
        if (!res.ok) {
          return res.json().catch(function () { return {} }).then(function (body) {
            fail(body && body.error ? body.error : 'the panel answered ' + res.status)
          })
        }
        return res.json().then(function (snapshot) {
          failures = 0
          lastOk = Date.now()
          accept(snapshot)
          if (stopped) return
          setStatus('live')
          schedule(POLL_MS)
        })
      }).catch(function () {
        fail(null)
      })
    }

    function stop() {
      stopped = true
      if (timer) global.clearTimeout(timer)
      timer = 0
    }

    function onVisible() {
      if (waitingForVisible && global.document && !global.document.hidden && !stopped) {
        waitingForVisible = false
        tick()
      }
    }

    // ── public ──

    this.on = function (event, fn) {
      if (!listeners[event]) throw new Error('vibepanel: no event "' + event + '"')
      listeners[event].push(fn)
      // A listener added after the first snapshot still hears the current one,
      // so the order a page wires things up in does not matter.
      if (event === 'snapshot' && self.snapshot) fn(self.snapshot, self)
      if (event === 'status') fn(self.status, self)
      if (event === 'params' && self.snapshot) fn(self.params, self)
      return function off() {
        var i = listeners[event].indexOf(fn)
        if (i >= 0) listeners[event].splice(i, 1)
      }
    }

    /** The panel's clock, in unix seconds, corrected for this screen's. */
    this.now = function () {
      return Date.now() / 1000 + offset
    }

    /** How long ago a unix time was, on the panel's clock: "14m". */
    this.since = function (unix) {
      if (typeof unix !== 'number' || unix <= 0) return '—'
      return fmt.duration(self.now() - unix)
    }

    /**
     * Put a value in an element as text. Null, undefined and '' become the
     * placeholder. Always textContent: a session title is text somebody else
     * chose, and innerHTML would draw it as markup. The characters that
     * reorder or hide text are replaced, as the panel does.
     */
    this.text = function (el, value, placeholder) {
      if (!el) return el
      var empty = value === null || value === undefined || value === ''
      el.textContent = empty ? (placeholder === undefined ? '—' : placeholder) : honest(value)
      return el
    }

    /**
     * A row's name, or null when this link does not send names (detail is
     * "counts"). Pass a fallback to get that instead of null. The name has the
     * reordering and invisible characters replaced, so it is safe to use
     * outside vp.text too.
     */
    this.name = function (row, fallback) {
      var n = row && typeof row.name === 'string' && row.name !== '' ? honest(row.name) : null
      return n === null && fallback !== undefined ? fallback : n
    }

    /** Draw the connection state into an element, shape and word both. */
    this.badge = function (el) {
      if (!el) return function () {}
      function draw() {
        renderBadge(el, self.status, self.snapshot)
      }
      draw()
      var offStatus = self.on('status', draw)
      var offSnapshot = self.on('snapshot', draw)
      return function () {
        offStatus()
        offSnapshot()
      }
    }

    this.stop = stop

    // ── start ──

    if (global.document && global.document.addEventListener) {
      global.document.addEventListener('visibilitychange', onVisible)
    }

    var fixture = options.fixture || query('fixture')
    if (options.snapshot) {
      global.setTimeout(function () {
        lastOk = Date.now()
        accept(options.snapshot)
        setStatus(options.status || 'live')
      }, 0)
    } else if (fixture) {
      stopped = true
      if (!FIXTURE_NAME.test(fixture)) {
        global.setTimeout(function () { fail('fixture names are lower-case letters, digits and dashes') }, 0)
      } else {
        global.fetch('fixtures/' + fixture + '.json', { cache: 'no-store', credentials: 'omit' })
          .then(function (res) {
            if (!res.ok) throw new Error('no fixture called ' + fixture)
            return res.json()
          })
          .then(function (f) {
            if (f.snapshot) accept(f.snapshot)
            setStatus(f.status || 'live')
          })
          .catch(function (e) {
            self.error = e.message
            emit('error', e.message)
            instruments.error(e.message)
            setStatus('disconnected')
          })
      }
    } else if (!token) {
      global.setTimeout(function () {
        stopped = true
        self.error = 'no share token in the address; open the page through its share link'
        emit('error', self.error)
        instruments.error(self.error)
        setStatus('revoked')
      }, 0)
    } else {
      global.setTimeout(tick, 0)
    }
  }

  // ─── the badge ──────────────────────────────────────────────────────────
  //
  // The connection is the first thing on a wall to read, because a dashboard
  // that silently froze looks exactly like a quiet system. Shape carries it as
  // well as colour: a dot in a ring, a ring with a gap, a ring struck through,
  // a broken chain. People read these at night on a phone.

  var SVG = 'http://www.w3.org/2000/svg'

  var WORDS = {
    en: { connecting: 'connecting', live: 'live', reconnecting: 'reconnecting',
      disconnected: 'disconnected', revoked: 'link no longer valid', stale: 'not updating' },
    zh: { connecting: '连接中', live: '实时', reconnecting: '重新连接中',
      disconnected: '已断开', revoked: '链接已失效', stale: '数据未更新' },
  }

  var COLOURS = {
    live: 'var(--vp-live, #2f9e62)',
    connecting: 'var(--vp-waiting, #c98a1a)',
    reconnecting: 'var(--vp-waiting, #c98a1a)',
    disconnected: 'var(--vp-trouble, #d64545)',
    revoked: 'var(--vp-trouble, #d64545)',
  }

  function shape(status) {
    var paths = {
      live: [['circle', { cx: 12, cy: 12, r: 10, fill: 'none', 'stroke-width': 2 }],
        ['circle', { cx: 12, cy: 12, r: 4.5, stroke: 'none', fillWith: true }]],
      connecting: [['path', { d: 'M12 2 A10 10 0 1 1 4.9 5.0', fill: 'none', 'stroke-width': 2.4 }]],
      disconnected: [['circle', { cx: 12, cy: 12, r: 10, fill: 'none', 'stroke-width': 2.4 }],
        ['path', { d: 'M5 19 L19 5', 'stroke-width': 2.4 }]],
      revoked: [['path', { d: 'M9.5 14.5 L7 17 A3.5 3.5 0 0 1 2 12 L4.5 9.5', fill: 'none', 'stroke-width': 2.2 }],
        ['path', { d: 'M14.5 9.5 L17 7 A3.5 3.5 0 0 1 22 12 L19.5 14.5', fill: 'none', 'stroke-width': 2.2 }],
        ['path', { d: 'M4 4 L20 20', 'stroke-width': 2.2 }]],
    }
    return paths[status === 'reconnecting' ? 'connecting' : status] || paths.connecting
  }

  function renderBadge(el, status, snapshot) {
    var doc = el.ownerDocument
    var words = isZh() ? WORDS.zh : WORDS.en
    var colour = COLOURS[status] || COLOURS.connecting
    var label = words[status] || status
    if (status === 'live' && snapshot && snapshot.stale) label += ' · ' + words.stale

    while (el.firstChild) el.removeChild(el.firstChild)
    el.setAttribute('data-vp-status', status)
    el.style.display = el.style.display || 'inline-flex'
    el.style.alignItems = 'center'
    el.style.gap = '0.4em'

    var svg = doc.createElementNS(SVG, 'svg')
    svg.setAttribute('viewBox', '0 0 24 24')
    svg.setAttribute('width', '1em')
    svg.setAttribute('height', '1em')
    svg.setAttribute('role', 'img')
    svg.setAttribute('aria-label', label)
    var parts = shape(status)
    for (var i = 0; i < parts.length; i++) {
      var node = doc.createElementNS(SVG, parts[i][0])
      var attrs = parts[i][1]
      for (var k in attrs) {
        if (k === 'fillWith') continue
        node.setAttribute(k, String(attrs[k]))
      }
      if (attrs.fillWith) node.setAttribute('fill', colour)
      else node.setAttribute('stroke', colour)
      node.setAttribute('stroke-linecap', 'round')
      svg.appendChild(node)
    }
    var text = doc.createElement('span')
    text.textContent = label
    el.appendChild(svg)
    el.appendChild(text)
  }

  // ─── export ─────────────────────────────────────────────────────────────

  var VibePanel = {
    version: SDK_VERSION,
    connect: function (options) {
      return new Client(options)
    },
    fmt: fmt,
  }

  if (typeof module === 'object' && module && module.exports) module.exports = VibePanel
  global.VibePanel = VibePanel
})(typeof window !== 'undefined' ? window : globalThis)
