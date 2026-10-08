# Plugins: five rungs, one capability table, and an install screen that says what it means

**What is built:** steps 0 and 1 of §13 -- the manifest, the capability
table, the install screen, the theme lint, the bundle reader, the tables, the
settings routes, the plugins page, the CLI, a theme end to end; and rung 1:
grants, `/plugin/{grant}/` with the sandbox, `/api/plugin/{cred}/v1/` with
the capability matrix, the view and its handles, the event stream, the SDK,
`vibepanel-ui.css`, the bridge, the side-panel, settings, page and header
slots, and dev mode with the fingerprint reload; and rung 2: `server.js`
with `onEvent`, `onSchedule`, the plugin's routes through both doors,
`onInbound` behind a secret, a `ctx` with one member per granted capability,
`ctx.fetch` through the guarded fetcher, per-plugin sources and the server
log; and rung 3: `plugin_tokens`, the supervisor with its three tests, the
cgroup leaf, and the three red capabilities' routes; and rung 4: the module
loaded on the panel's origin behind the panel-wide switch, the `host`
object, two slots, `host.css`, safe mode and `disable --all`; and the
scaffold of §9: **New plugin** on the plugins page and `vibepanel plugin
init`, five templates, `AGENTS.md` for the agent, the directory registered
in dev mode and handed to the launch picker; and the three pins of §10: the
SDK types against the view, the contract list, and the compat corpus. The
session and project action slots, `host.override`, `vibepanel plugin shot` and `plugin run` are not,
and each section below that describes one says what it will be. This document replaces the earlier version of
this file, which argued that the panel should not have a plugin runtime at all.
§14 keeps that argument and says which parts of it still hold; the short
version is that share pages built, one feature at a time, the three things it
said would be too expensive to build — HTML the panel did not write on the
panel's own origin, server-side code under a budget, outbound requests to
approved hosts — and the request has changed from "a harness that manages
every session" (which the API does) to "let me, or an agent in one of my
sessions, add a pane, a theme, a route, a bot, live" (issue #30).

What is asked for, in the owner's words: frontend and backend plugins; for
different needs and different levels of trust; a development spec; packaged
as one file; an install screen that shows the permissions, the hosts it will
reach and whether it runs commands; permissions that do not interfere between
plugins; and real freedom — themes, features, API routes, and changing how a
piece of the panel behaves.

## 1. The shape

A plugin is a directory with a `plugin.json`, published as immutable versions
in SQLite exactly as a page is, and packaged as a zip the way a page is
exported. What a plugin *does* is one or more **rungs** of a ladder, each a
different runtime with a different credential, and the rung is what the
install screen names first:

| rung | word on the screen | what it is | runs where | credential |
|---|---|---|---|---|
| 0 | **theme** | a stylesheet of `--vp-*` tokens | the panel's page, as CSS | none |
| 1 | **panel** | HTML/JS at a mount point | a sandboxed frame with an opaque origin, like an admin page | a plugin grant, bound to the owner's session |
| 2 | **service** | `server.js`: events, a schedule, routes, sources, data | goja inside the panel's process, like a page's `server.js` | none; `ctx` carries only what was granted |
| 3 | **process** | a command the panel supervises | its own process, in the sessions' scope | a plugin token, resolved on one prefix |
| 4 | **unsandboxed** | a module loaded on the panel's origin | the panel's page, as the owner | the owner's own |

One plugin may combine rungs 0–3: a theme plus a pane plus a service is one
`plugin.json`. Rung 4 is the one that changes the rules, and it is a word on
its own line of the install screen for that reason (§7).

```
 owner's browser (signed in)        the panel (Go)                              a plugin
 ───────────────────────────        ──────────────                              ────────
 Settings → Plugins ───────────▶   /api/settings/plugins/…   install, grant, enable, dev
 a slot in the panel ──frame──▶   /plugin/{grant}/{entry}    files, CSP, the SDK          ◀── rung 1
                                   /api/plugin/{cred}/v1/…    the plugin view, data, routes ◀── rungs 1 and 3
                                   /api/ext/{id}/…            a plugin's routes, for the owner
                                   plugin_data (SQLite) ◀── server.js (goja) ──▶ sources (https)   rung 2
                                   supervisor ── argv, env, cgroup leaf ──▶ a process        rung 3
                                   <script type=module src=/plugin-code/{id}/main.mjs>       rung 4
```

Three kinds of credential reach a plugin's routes, and none of them is a flag
on another: the owner's session cookie reaches the settings routes and a
plugin's `/api/ext/` routes; a **grant** or a **token** reaches
`/api/plugin/{cred}/v1/` and nothing else. `currentUser` consults neither
`plugin_grants` nor `plugin_tokens`, so either presented as a cookie or a
`Bearer` is an unknown string, which is the property red line 8 already
rests on.

## 2. The package

```
standup/
  plugin.json        the manifest (§3)
  theme.css          rung 0
  pane.html, …       rung 1 entries and their files
  server.js          rung 2
  bot/               rung 3's files, if any
  main.mjs           rung 4
  AGENTS.md, CLAUDE.md, README.md, vibepanel-plugin.d.ts   scaffolded, never published
  .vibepanel/        errors.json, server.log, shots/   written by the panel for the agent
```

The reader is the pages reader generalised (`pages.ReadDir`, `ReadArchive`,
`ValidPath`, the content sniff): no dot-paths, no symlinks, no `node_modules`,
an extension allowlist plus `.mjs` and `.cjs`, 256 files, 20 MiB, 5 MiB each,
every path checked to stay inside the plugin. A rung-3 plugin that needs
`node_modules` ships them inside its own zip under `bot/`; the limit is the
limit. Versions are rows in `plugin_versions` with files in the same
content-addressed blob table pages use, so publishing a version that changed
one file stores one file.

Export is a zip with `plugin.json` at the top. Import reads it by the publish
rules and makes an **uninstalled** plugin: nothing runs until the install
screen has been read and confirmed.

## 3. The manifest

```jsonc
{
  "plugin": 1,
  "id": "standup",                              // [a-z0-9-]{3,40}; unique per panel; the namespace for everything below
  "name": { "en": "Stand-up", "zh-CN": "站会" },
  "version": "0.3.0",
  "panel": ">=1.26",                            // refused by name and number when the panel is older
  "description": { "en": "…", "zh-CN": "…" },
  "author": "…", "homepage": "https://…",

  "theme":  { "file": "theme.css", "name": { "en": "Paper", "zh-CN": "纸" } },

  "panels": [
    { "slot": "sidepanel.pane",   "entry": "pane.html",     "title": {…}, "icon": "list-checks" },
    { "slot": "settings.section", "entry": "settings.html", "title": {…}, "group": "panel" },
    { "slot": "session.action",   "entry": "action.html",   "title": {…}, "icon": "sparkles" },
    { "slot": "page",             "entry": "index.html",    "title": {…}, "path": "standup" }
  ],

  "capabilities": ["read:panel", "read:notes", "write:todos", "ui:open"],

  "settings": { "group": "notify", "fields": [ { "key": "quiet", "type": "bool", "default": true, "label": {…} } ] },  // drawn by the panel (§5a)
  "data":    { "digest": { "type": "text", "max": 2000 } },          // as a page's data (§2 of page-backend.md)
  "sources": [ { "key": "calendar", "url": "https://…", "every": "10m" } ],
  "server":  { "entry": "server.js", "every": "15m",
               "on": ["session.state", "session.created"],
               "routes": { "GET /digest": "digest", "POST /nudge": "nudge" } },

  "process": { "command": ["node", "bot/index.js"], "restart": "on-failure",
               "capabilities": ["read:panel", "sessions:input"],
               "env": ["SLACK_TOKEN"] },

  "unsandboxed": { "entry": "main.mjs", "tested": "1.26.0 - 1.26.x" }
}
```

Strict on the way in, unknown keys refused, every limit a number in
`internal/plugins` with a test that names it. `name` and `description` and
every `title` are bilingual objects, because the install screen and the slot
chrome are drawn by the panel in the owner's language and a plugin that has
only English is a plugin with an English line in a Chinese settings page.
Labels go through `safeText` like every other string from outside the bundle.

## 4. Capabilities: one closed list, in one file

`internal/plugins/caps.go` is the whole permission model. Each capability
has a name, a sentence in both languages for the install screen, the routes
it opens under `/api/plugin/{cred}/v1/`, and the `ctx` members a `server.js`
gets. Nothing else in the panel decides what a plugin may do; a handler under
the plugin prefix is reachable because the capability table says so, and
`TestEveryCapabilityOpensOnlyItsRoutes` mints a grant with each capability
alone, walks the router, and expects 403 everywhere the table does not name.

The list is the ladder from `writable-links.md`, read as what installed code
may do rather than what a URL may, with the disclosure line from the share
snapshot kept:

| capability | the sentence on the screen (en) | opens |
|---|---|---|
| `read:panel` | can see your projects, sessions, their names and states | `GET v1/view`, `v1/events` |
| `read:paths` | can see directories, commands and working directories | the same view with paths filled in |
| `read:terminal` | can read what is on any terminal's screen | `GET v1/sessions/{h}/screen` |
| `read:notes` / `read:todos` | can read notes / checklists | `GET v1/projects/{h}/notes`, `…/todos` |
| `write:notes` / `write:todos` | can change notes / checklists | `PUT …/notes`, `POST/PATCH/DELETE …/todos` |
| `write:state` | can mark a session working, waiting or done | `PATCH v1/sessions/{h}/state` |
| `sessions:control` | can restart and end sessions | `POST …/restart`, `DELETE v1/sessions/{h}` |
| `sessions:create` | **can start programs in your projects** | `POST v1/sessions` |
| `sessions:input` | **can type into any of your terminals** | `POST v1/sessions/{h}/input` |
| `read:resources` / `read:usage` / `read:git` | can read the machine / token usage / repository summaries | the matching `GET`s |
| `net:<host>` | will contact `host` over https | a source or a fetch to that host, resolved-address checked |
| `ui:open` | can ask the panel to open a session, project or settings page | a frame message, not a route |
| `ui:notify` | can show a toast or a notification | a frame message, not a route |

A plugin's own data store needs no capability; it is its own. Two names are
in bold because they are the two rungs `writable-links.md` refuses at any
setting for a URL. They are not refused here, because the credential is not a
URL: it is a grant that dies with the owner's session or a token in the
environment of a process the owner chose to run. What stays from that
argument is the loudness. Those two sentences are drawn in the danger tone,
above the rest, and the confirm button changes its words (§6).

What is not on the list and never will be, as capability names: the tmux
socket (red line 1), a session's PTY (red line 2), the database, the hook
endpoint, any `/api/share/` or `/page-admin/` route, another plugin's data,
the account routes (password, passkeys, API tokens), and installing plugins.
The last one matters: a plugin that can install plugins can grant itself the
rest. Rung 4 can do all of these, which is the sentence its install screen
carries.

**Hosts.** `net:<host>` is enforced for sources and for `ctx.fetch` in a
`server.js`, by `pagesources.go`'s guard — https only, resolved address
checked, no redirects, bounded, in the background and only while something is
watching — with the hosts approved **per plugin**, not per panel, so
approving `api.github.com` for one plugin approves nothing for another. For a
process or an unsandboxed module the panel cannot enforce a host list, and
the screen says so in those words: *declares that it will contact …; the
panel cannot check this.* An enforced line and a declared line are drawn
differently, because the second is a promise and the first is not.

**The view.** `GET v1/view` is a restated struct, `pluginView`, built the way
`buildShareReading` is: it names every field it discloses and embeds neither
`store.Session` nor `sysmon.Sample`, so a field added to either is not
disclosed by default. Handles (`h` above) are `HMAC(cred, real id)`: stable
for one plugin, different between plugins, never the panel's ids. Without
`read:paths` the view carries `cwd: ''`, `command: ''` and no project path,
which is the share snapshot's line; a plugin that wants to open an editor at
a path asks for `read:paths` and the screen says so.

## 5. The three runtimes

### Rung 0: a theme is a stylesheet of tokens

`theme.css` may contain one rule, `:root[data-theme='ext-<id>'] { --vp-…: …;
color-scheme: … }`, and nothing else: no other selector, no property that is
not a `--vp-*` token the base `:root` defines, no `url()`, no `@import`.
`styles.test.ts` already states that rule for the panel's own theme blocks
(red line 5); `plugins.LintTheme` states it for a plugin's and refuses the
file otherwise, at publish and at install. An installed theme appears in the
theme picker by its name, is applied by setting `data-theme`, and reaches
xterm through `terminalTheme()` reading the `--vp-term-*` tokens like any
other theme. The pre-paint script in `index.html` learns to honour
`ext-<id>` values so a reload does not flash the default theme.

No permissions, no screen beyond the name. A theme is the rung anybody may
install from anyone.

### Rung 1: a panel is a share page on the inside

A `panels[]` entry is served at `/plugin/{grant}/{entry}` with the admin
page's headers: `sandbox allow-scripts allow-forms`, an opaque origin, no
cookie, no storage, `connect-src` naming `/api/plugin/{grant}/v1/` and the
plugin's own files, `frame-ancestors 'self'`. The owner's SPA mints the grant
(`POST /api/settings/plugins/{id}/grant`) when it mounts the frame; the grant
is 32 random bytes hashed into `plugin_grants` with the plugin, the user, the
session it was minted from and the capability set *as granted* — not as the
manifest asks — and dies with that session or after eight hours. The frame
is the SPA's child, so a plugin pane in the panel is `pages-check`'s sandbox
probe list run again from a new address, and that is the first test.

The SDK, `vibepanel-plugin.js`, is served from the binary beside the entry so
a frame always runs the SDK that matches the panel:

```js
const vp = VibePanel.plugin()                 // grant and base from the address
vp.caps                                        // what was granted; a plugin degrades on this, it does not ask
vp.on('view', (v) => draw(v))                 // pushed over v1/events, coalesced like /ws
vp.on('context', (c) => …)                    // { session?, project?, theme, lang, slot, narrow }
vp.sessions.input(h, 'y\n')                   // 403 without sessions:input, and the SDK says which cap
vp.data.get / set / increment / append / reset
vp.ui.open({ session: h }) / vp.ui.notify(text) / vp.ui.height(px)
vp.route('GET', 'digest')                     // the plugin's own routes (§5, rung 2)
vp.text(el, value); vp.fmt.*; vp.badge(el)     // from the share SDK, unchanged
```

**The bridge** between the SPA and a frame is the Preview pane's, generalised:
the SPA accepts a message only when `event.source` is one of its plugin
frames' windows (every sandboxed frame's origin is `"null"`, so the origin
proves nothing), the message set is fixed (`height`, `open`, `notify`,
`ready`), payloads are rebuilt rather than forwarded, and `open` and `notify`
are refused for a plugin whose grant lacks `ui:open` or `ui:notify`.
Downward, the SPA posts `context`: which session or project the slot is
showing (as the plugin's handle), the theme name, the language, and the
current token values of the theme so a frame can draw in the panel's colours
without reaching for its stylesheet. Nothing the frame sends decides anything
the SPA then fetches on its own credential.

**Slots** are the fixed list of places the SPA will draw a frame, each an
`ErrorBoundary` so a frame that fails to load is a blank box with the
plugin's name and not a blank panel:

| slot | where | gets |
|---|---|---|
| `sidepanel.pane` | a tab in the side panel, beside Files and Notes | the selected session and project |
| `settings.section` | a `Section` in the named settings group, **only when a declarative `settings` schema is not enough** (§5a) | nothing |
| `session.action` | an item in a per-session *more* menu (`Menu.tsx`, which the sidebar does not use yet), opening the entry in a dialog -- **not built yet**: the sidebar has no menu to put it in, and a row of buttons is what the render check pins | that session |
| `project.action` | the same on a project heading -- **not built yet**, for the same reason | that project |
| `page` | `/x/<path>`, a root of its own behind `AuthGate`, like `/sharing` | nothing |
| `header.item` | a small frame in the header, for a counter or a badge | nothing |

The side-panel pane and the settings section are the two whose seams are
closed unions with tests asserting the closure (`PANEL_TABS` and the pane
layout invariant; `SETTINGS_SECTIONS` and `wiring.test.ts`). Opening them is
the frontend's main work in this design: the layout invariant becomes "the
union of groups equals the installed tabs", `parseLayout` keeps dropping
unknown ones, and a plugin tab's id is `ext:<id>:<n>` so it cannot collide
with a built-in or with another plugin's. Order within a slot is install
order, draggable with `useDragList`, stored per viewport band like the pane
layout.

### 5a. Settings and shared components: the panel draws them

「设置面板、各种公共组件」need one answer for every rung, and it is the one
pages already took for a page's data: **a plugin declares, the panel draws.**
A plugin's settings are a schema in the manifest, in the `data` vocabulary
(`text`, `number`, `bool`, `enum`, `color`, `list`, `secret`), and the panel
draws the form itself with its own `Section` and `Row`, in the owner's
language, in the current theme, inside the `@container` layout the settings
modal already has:

```jsonc
"settings": {
  "group": "notify",
  "fields": [
    { "key": "channel", "type": "enum", "values": ["slack", "email"], "default": "slack", "label": {…}, "hint": {…} },
    { "key": "quiet",   "type": "bool", "default": true, "label": {…} },
    { "key": "webhook", "type": "secret", "label": {…} }
  ]
}
```

`fields` is an array, not an object, for the reason a page's `params` are:
the form draws them in the author's order, and a JSON object's order does not
survive a Go map.

Values live in `plugin_settings(plugin_id, key)`; `ctx.settings` and
`vp.settings` read them, and a change is pushed to the plugin's view like
any other. A `secret` field is stored sealed under `secrets.key`, never read
back by a frame, and reaches a process as an environment variable and a
`server.js` only through `${secret:…}` in a source header, exactly as a
page's secrets do. A settings section drawn from a schema cannot look wrong
after a redesign of the settings modal, because the plugin never saw the
modal: it saw a schema, and the panel's own `DataForm` is what changed. The
`settings.section` frame slot stays for the case a form cannot cover, and
the screen says which kind a plugin uses.

The same rule covers the components a frame wants to look native. A frame
is a sandboxed document and cannot import the panel's React; what it gets is
served beside the SDK from the binary:

- `vibepanel-ui.css`: the panel's tokens as `:root` variables, and a short
  list of classes with stable names — `vp-control`, `vp-row`, `vp-section`,
  `vp-badge`, `vp-state-{working,waiting,done}` (shape and hue both, red line
  4), `vp-list`, `vp-field` — redefined by the panel from the same tokens the
  SPA uses. A plugin written against `vp-row` keeps matching the panel when
  the panel's row changes, because the panel's row is where the class is
  defined.
- behaviours the SDK owns because every hand-written frame gets them wrong:
  `vp.confirm(text)` asks the panel to show its own `ConfirmDialog` over the
  bridge (no raw dialogs, which `no-raw-dialogs.test.ts` already forbids in
  the panel), `vp.menu(items)` opens the panel's `Menu`, `vp.toast(text)`,
  `vp.badge(el, state)`, `vp.text(el, value)`, `vp.fmt.*`, `vp.since()`.

This is deliberately a stylesheet and six behaviours, not a widget
vocabulary. The board was a vocabulary, every new widget was a Go kind, a
React component and a check budget, and it lost to HTML; a frame draws its
own HTML with the panel's classes, and the list above is the part that has
to agree with the panel to look right.

For rung 4, `host.ui` is the panel's real primitives as a named list —
`Section`, `Row`, `Menu`, `StateDot`, `askConfirm`, `toast`, `Dialog` — and
that list is the contract: a component not on it may be imported from the
bundle, and that is the day the plugin stops being supported.

### Rung 2: a service is a page's `server.js` with more hooks

The runtime is `pageserver.go` moved to a package both pages and plugins
use: a fresh `goja` runtime per call from a compiled program cached per
version, a budget enforced by `vm.Interrupt`, data writes buffered and
committed only on a clean return, a result of at most 64 KiB, a log of 200
lines. What a plugin adds is hooks and routes:

```js
function onEvent(ev, ctx)          // "session.state", "session.created", "session.gone",
                                    // "project.archived", "todo.changed", "note.changed"; ≤ 500 ms
function onSchedule(ctx)            // every `every` (≥ 1m); ≤ 500 ms
function digest(req, ctx)           // a route: req = { method, path, query, body, caller }; ≤ 500 ms
function transform(view)            // optional: a value added to the plugin's own view, memoised; ≤ 50 ms
```

`ctx` has `data`, `sources`, `now()`, `log()` as before, plus one member per
granted capability — `ctx.sessions.list()` exists only with `read:panel`,
`ctx.todos.add()` only with `write:todos`, `ctx.fetch(url)` only for an
approved host — and each member calls the same Go function the matching v1
route calls, so a capability is one function in one place. A member that was
not granted is absent, not a stub that throws, so `vibepanel plugin check`
can read the script and say *calls ctx.notes.set but asks for no
write:notes* before anything runs.

**Events never run on the poller.** Each plugin has a bounded channel (256,
oldest dropped, the drop counted and shown on the plugin's card), fed by a
non-blocking send from the same place the flow log is fed. There is no veto
and there will not be one: a hook the panel waits on is a hook that stalls
the loop every other feature depends on, and the fail-open-with-a-deadline
version is a suggestion, which is not worth a runtime. The one synchronous
thing is `transform`, which runs on a read and cannot write.

**Routes** (`server.routes`) are reachable two ways: `GET
/api/plugin/{cred}/v1/x/digest` for the plugin's own frames and process, and
`GET /api/ext/standup/digest` under `RequireAuth` for the owner — the panel's
UI, a `curl` with an API token, another of the owner's scripts. Never
unauthenticated. A plugin that wants to be called from outside (a chat
service's callback, a CI webhook) declares `"inbound": {"path": "hook",
"secret": "HOOK_SECRET"}` and gets `POST /api/plugin-hook/standup/hook`, an
open route whose body the panel checks against an HMAC under a secret the
owner stored, which is the chat bridge's door with the verification moved
into the panel. That is a line on the install screen: *accepts requests from
the internet at …*. Four handlers at a time per plugin; a fifth waits, and a
plugin whose handlers take their whole budget is a slow plugin, not a slow
panel.

### Rung 3: a process is a supervised command

```jsonc
"process": { "command": ["node", "bot/index.js"], "restart": "on-failure",
             "capabilities": ["read:panel", "sessions:input"], "env": ["SLACK_TOKEN"] }
```

The panel starts it from the version's checked-out directory
(`<data dir>/plugins/<id>/v<N>/`), with a cleared environment plus `PATH`,
`HOME`, `LANG`, `VIBEPANEL_PLUGIN_URL` (`http://127.0.0.1:<port>/api/plugin/<token>/v1/`,
the token being the whole credential), `VIBEPANEL_PLUGIN_STATE` (a directory
of its own under `<data dir>/plugins/<id>/state/`), and each name in `env`
filled from a secret the owner stored for this plugin. The token is a row in
`plugin_tokens`, minted at enable and deleted at disable, with the granted
capability set on the row; the same v1 routes and the same SDK (`VibePanel.plugin({ base })`
from Node) serve it, and `GET v1/events` is a WebSocket carrying the plugin's
view within the hub's coalesce window.

**The supervisor** is the part the earlier document said was worth building
and worth building last, and its tests are the ones it asked for: a process
that never exits is ended at shutdown (SIGTERM, SIGKILL after five seconds — a
plugin is the panel's child on purpose; it holds no session, so red line 2
does not apply to it); one that crashes is restarted with backoff from one
second to a minute and stopped after ten failures in ten minutes, audited
`plugin.crashed`, with the card saying so; its output is a 64 KiB ring the
card shows, dropped oldest-first, so a plugin printing in a loop fills
nothing. Where a sessions scope exists the process is moved into a leaf of
the pool, `p-<id>/`, beside the sessions: the resources page measures it,
the memory question may name it, and a plugin that leaks evicts the sessions'
cache before the panel's own pages, which is the incident red line 9 is
about, kept on the right side. A panel that manages no scope runs it where it
is.

The panel ships no runtime. The install screen shows the argv verbatim and
whether its first word is on `PATH`, and `vibepanel plugin check` says so
from a shell.

**A process on the panel's port.** A process that is itself an HTTP
service -- a bot with a webhook, a voice assistant's backend, a phone app's
API -- can be mounted at `/api/plugin-http/<id>/` instead of opening a port
of its own:

```jsonc
"process": { "command": ["node", "server/main.mjs"],
             "http": { "auth": "token", "stream": true, "maxBody": "8m", "idleTimeout": "15m" } }
```

The panel guards the door; the process does business. The process listens
on a unix socket the panel names (`VIBEPANEL_PLUGIN_SOCKET`, in the user's
runtime directory, 0700), so it holds no port and the LAN cannot reach it.
Who may call is `auth`, declared and shown on the install screen in red:
`owner` is the panel's own session or API token; `token` is a **plugin
access token** the owner mints on the card, one per device, named, revoked
one at a time, its last use shown, reaching this one mount and nothing
else; `hmac` is the inbound door's check against a declared secret. There
is no anonymous mode. Before forwarding, the panel strips the cookie and
the Authorization header and sets `X-Vibepanel-Caller` (`owner:<user>`,
`token:<name>`, `hmac`) and `X-Vibepanel-Proxy`, a secret minted at every
start and handed to the process in `VIBEPANEL_PLUGIN_PROXY_SECRET`. The
second header is what makes the first trustworthy: the socket's directory
keeps other users out, but every process of *this* user can connect to it,
and a coding agent in a session is this user, so a request without the
secret is one that did not come through the panel and the process refuses
it (the scaffold says so). After the answer, the panel removes `Set-Cookie`
and every policy header, adds `nosniff`, and serves anything that is not
JSON, an event stream, text, a raster image, audio, video or a byte stream
under `Content-Security-Policy: sandbox` as an attachment -- SVG included,
since it carries script. An HTML page a process returned, rendered on the
panel's origin, would be rung 4 without the switch. Cross-origin calls are
answered only for origins on the owner's list on the card, exact matches,
never declared by the plugin, and only with a Bearer: the cookie is never a
credential across origins, so there is no CSRF to defend. Rate limits are
per plugin and per caller; streams are capped and end after an idle
timeout; bodies are capped at 16 MiB whatever is asked.

What this bounds is who can reach the process, not what the process can
do. It still runs as you; the install screen's amber line and your own
judgement of the author are what cover that, as for any rung-3 plugin.

## 6. Installing one

A plugin arrives as a file, a path on the machine, or a URL the person typed,
fetched once and shown with its SHA-256; there is no registry the panel
reads, and the panel never checks for a newer version on its own, for the
reason it does not for its own binary. From a session, `vibepanel plugin
install <dir|zip>` does the same as the screen does, prints the same words,
and takes `--grant <cap,…>` to grant; it runs as the same user, so like a
page's publish that is a default the scaffolded `AGENTS.md` states ("do not
install or grant without the person"), not a boundary, and every such grant
is audited under the user `cli` and shown on the card as granted from the
command line.

The screen is `plugins.Describe(manifest, granted) []Line`, a pure function
over the manifest, with a test that every capability and every rung produces
a line in both languages, so a capability added without words fails to
build. It says, in this order:

1. **What it is.** Name, version, author, id, hash, the panel version it
   needs — and a refusal, by name and number, when the panel is older.
2. **Which rungs.** *A theme.* *Adds a pane to the side panel and a settings
   section.* *Runs code inside the panel every 15 minutes and on session
   events.* *Runs a command as you.* *Runs on the panel's page as you.*
3. **What it may do**, one checkbox per capability, grouped as reads,
   writes, and the two bold ones, each in the sentence from the table. The
   owner may untick any; the plugin sees `vp.caps` and degrades.
4. **Where it reaches**: each host, marked *enforced* or *declared*; an
   inbound route if declared.
5. **What it runs**: the argv, verbatim, in a code block.
6. **What it keeps**: its data keys and the secret names it needs, which must
   be filled before enabling.
7. For rung 4, the paragraph in §7.

The confirm button says *Install* for a theme, *Install and grant* for rungs
1–2, and *Run this as you* for rungs 3–4, because the verb is what a person
reads at the moment of deciding. Install, enable, disable, remove, a change
of grants, a crash and a version change are audit rows with the `plugin.`
prefix, every one on the list in `TestEveryAuditEventIsAccountedFor`.

**Upgrading** publishes a new version and shows the difference in
permissions: a version asking for more is installed but not enabled until
the new lines are confirmed; one asking for the same or less is enabled on
confirm. A grant decision is attached to a plugin id, so a version cannot
widen it by being newer.

**Not from anywhere else.** No share token, admin grant, chat tools token,
plugin credential or hook request reaches the settings routes that install,
grant or enable; the existing route-list tests cover the first three, and
`TestAPluginCredentialReachesOnlyTheseRoutes` is the fourth.

## 7. Rung 4: unsandboxed, and what the screen says

This is the rung the earlier document refused hardest, and it is here
because "change how a piece of the panel behaves" has no sandboxed shape: a
sorting rule for the sidebar, a different state mark, a button in the
terminal's chrome, a replacement launch picker. Slots cover *adding*; this
covers *changing*.

`main.mjs` is loaded as an ES module on the panel's own origin, after the
SPA, and given one object:

```js
export default function (host) {               // host.v === 1
  host.slots.add('sidebar.sessionRow.trailing', (session) => element)
  host.override('stateMark', (props, Default) => element)   // a short, named, documented list -- not built yet; host.css covers most of it
  host.state.subscribe((panelState) => …)       // the same PanelState the SPA holds
  host.api                                       // the SPA's own `api` object, as the owner
  host.i18n.add({ 'standup.title': { zh, en } }) // merged into the dictionary at runtime
  host.theme, host.lang, host.toast, host.open
}
```

The install screen's last block, in the danger tone and in both languages:

> This plugin runs on the panel's own page as you. It can read every terminal,
> type into any of them, change your password, revoke your passkeys and
> install other plugins. The panel cannot limit it. Install it only if you
> trust its author as you trust yourself.

Three things bound the blast radius without pretending to bound the
capability. It is loaded only when the panel-wide switch *allow unsandboxed
plugins* is on (off by default, audited `plugins.unsandboxed`), and never on
`/share/*`, `/page-admin/*` or `/plugin/*`, which do not load the SPA at all.
Every slot it draws into is an `ErrorBoundary`, so a throw is one box.
And there is a way back that needs no working UI: `?safe=1` on the panel's
address loads no rung-0 or rung-4 code for that tab, and `vibepanel plugin
disable --all` turns everything off from a shell; the first thing the tour
for this feature says is where those two are. `host.v` is the only contract,
`override` names are a short list that may shrink, and the manifest's
`tested` range is shown on the card and turns amber the day the panel moves
past it, because this rung has no stable surface and the honest thing is to
say so on the card rather than in a changelog.

## 8. Plugins do not interfere with each other

Each is answered by a namespace the id owns, not by a rule a plugin follows:

| thing | per plugin |
|---|---|
| data and settings | rows in `plugin_data(plugin_id, key)` and `plugin_settings(plugin_id, key)`; `ctx` and `v1/` see one id |
| routes | `/api/ext/{id}/` and `v1/x/` under its own credential |
| credentials | its own grants and tokens; revoked on disable; a change of grants re-mints them |
| hosts and secrets | `plugin_hosts(plugin_id, host)`, `plugin_secrets(plugin_id, name)` |
| events | its own bounded channel; a slow plugin drops its own events |
| handlers | its own semaphore of four |
| process | its own cgroup leaf, state directory and output ring |
| slots | ids prefixed `ext:<id>:`; order per slot, user-set |
| theme | one `data-theme` value is active at a time, so two themes cannot compose; the picker chooses |
| i18n | a rung-4 plugin's keys are prefixed `<id>.` by the host, and a collision is refused |

There is no inter-plugin API. A plugin cannot name another's id in any route
or `ctx` member; a rung-4 module can reach anything, which §7 said.

## 9. Developing one, live

The issue's second line is 「动态渲染/agent自己实时加功能」: an agent in one of
the panel's sessions writes a plugin and sees it appear. That is the page
workflow with the panel itself as the preview:

- **New plugin** in Settings → Plugins: a name and a template (`theme`,
  `pane`, `service`, `process`, `full`), scaffolded into
  `<data dir>/plugins/dev/plugin-<name>/` with `AGENTS.md`, `CLAUDE.md`,
  `README.md`, the SDK and its types, fixtures and a `.gitignore`, registered
  as a project called `plugin-<name>`, and handed to the launch picker with a
  first line typed at the agent's prompt. `vibepanel plugin init` from a
  shell.
- **Dev mode** mounts the draft directory's panels and compiles its
  `server.js` live, under the grants the owner has already given *that id*.
  A draft whose manifest asks for more shows an amber line on the card and in
  the slot — *asks for `sessions:input`, not granted* — and runs with what it
  has; granting is the owner's click on the same screen as an install. The
  directory's fingerprint is polled twice a second as a page's is, and the
  frame reloads or the program recompiles when two readings agree on
  something new. Frame errors go to `.vibepanel/errors.json`, `server.js`
  errors and `ctx.log` to `.vibepanel/server.log`, both for the agent.
- `vibepanel plugin check` lints the manifest, the theme, the capability/ctx
  agreement, the CSP-refused patterns, and the argv; `vibepanel plugin shot`
  mints a grant and screenshots each panel at three widths in both themes;
  `vibepanel plugin run event|schedule|route …` runs `server.js` from the
  directory against draft data.
- **Publish** stores a version; the owner's own draft is installed by
  publishing and enabling, which is the install screen again. **Export** is
  the zip.

## 10. Surviving the panel's updates

The panel is updated in place, often, and the premise of the project is that
the backend can be replaced underneath a running browser. A plugin system
that breaks on every release is one nobody writes for twice, so this is a
section rather than a sentence.

**A plugin never imports the panel.** It talks to contracts, and each
contract has a number the manifest names:

| contract | named by | carried by |
|---|---|---|
| the manifest | `"plugin": 1` | `internal/plugins`, strict in, lenient out |
| the v1 API and the view | the `/v1/` in the prefix | `pluginView`, `vibepanel-plugin.d.ts` |
| the SDK and `vibepanel-ui.css` | served from the binary | never the plugin's copy |
| slots | the slot names | `plugins.Slots`, a closed list that only grows |
| `ctx` | the capability table | one member per capability |
| the host object | `host.v` | unstable, said so (§7) |

**Contracts only grow.** A field, a slot, a capability, a `ctx` member or a
CSS class is added and never renamed, retyped or removed within a version;
`TestTheSDKTypesMatchThePluginView` pins the view both ways, and a committed
list, `internal/plugins/testdata/contract-v1.txt`, pins the slots, the
classes, the base `:root` token names, the capability names and the settings
groups (`TestNothingAPluginCanNameWasRemoved`: a name in the list and not in
the source is red, and so is a name in the source and not in the list, so a
contract is made out loud and never by accident), so removing one is a red
test and a decision rather than a surprise on somebody's panel. When a contract must
change shape, `v2` is served beside `v1` for at least three minor releases,
the release notes carry a plugins line, and the card says *uses API v1,
which ends in 1.29*.

**The manifest is strict in and lenient out.** A stored manifest is decoded
by `DecodeStored`'s rule: what a later build no longer accepts is dropped,
never widened, so a plugin published against 1.26 still installs on 1.30 with
the options the panel still knows, and a plugin that needs one the panel
dropped is disabled with a sentence naming it, not run half-way.

**A corpus of fixture plugins is the compatibility test.**
`internal/plugins/testdata/compat/` holds one small plugin per rung and per
contract version, frozen at the release that introduced it (five for v1:
`theme`, `pane`, `service`, `process`, `full`), each with an `expect.json`
saying what it did when frozen. `TestEveryCompatFixtureStillInstallsAndRuns`
takes each through the real routes: adds, installs with the capabilities it
asks for, sets its secrets, enables, serves its frames and theme, answers
its SDK calls on a grant, calls its routes through the owner's door,
delivers it an event and runs its process, and compares with `expect.json`. A change to the panel that breaks a two-year-old
plugin fails this test on the branch that made it. The fixtures are also the
examples the documentation points at, so the examples cannot rot.

**Declarative beats imperative because declarations are the panel's to
draw.** A settings schema, a slot declaration, a data schema, a route table,
a theme of tokens: the panel renders and wires all of them, so a redesign of
the settings modal, the sidebar or the side panel moves nothing a plugin
wrote. A frame's inside is the plugin's own HTML and never touches the
panel's DOM. The only rung that depends on the panel's internals is rung 4,
and that is why it names the range it was tested on and goes amber outside
it.

**An upgrade tells the owner before it tells the plugin.** The panel's own
update screen lists each enabled plugin with the contracts it uses and
whether the new version still serves them, in the words above; `vibepanel
doctor` says the same from a shell; and a plugin that the new panel cannot
run is disabled on the first start with a card that says why and a button
that re-enables it, never silently dropped and never run broken.

**The rungs are also a stability order.** A theme is tokens, and tokens are
never removed. A panel is HTML in a frame against a versioned API. A service
is a `ctx` with one member per capability. A process is a client of the same
API. Rung 4 is none of these. Choosing the lowest rung that does the job is
the development spec's first sentence, because it is the sentence that keeps
the plugin working.

## 11. Red line 10

When any of this is built, `AGENTS.md` gets a tenth red line, and this is its
first draft:

> **A plugin credential is narrowed by its route list and the capability
> table, never by a flag a handler reads.** A grant or a plugin token reaches
> `/api/plugin/{cred}/v1/` and nothing else; which routes under it answer is
> decided in `internal/plugins/caps.go` and pinned by
> `TestEveryCapabilityOpensOnlyItsRoutes`. `currentUser` consults neither
> table. The install screen is `plugins.Describe`, a pure function over the
> manifest, so every capability has its sentence or the build fails. Nothing
> a plugin does runs on the poller's goroutine; a rung-4 module is the one
> exception to every sentence above, and the install screen says so in the
> words of `docs/plugins.md` §7.

## 12. Checks

`make plugins-check`, in `make verify`:

- every escape from a plugin frame, in a signed-in browser: the `pages-check`
  probe list run from `/plugin/{grant}/…`, each asserted by *how* it failed,
  plus two new ones — a frame posting `open` for a session its grant cannot
  see, and a frame without `ui:notify` posting `notify`;
- the install screen's words for every capability, rung and host kind, in
  both languages, at three widths;
- the dev loop through the UI: new plugin, a file written three times in
  450 ms, one reload; a manifest edited to ask for more, the amber line;
  grant, publish, enable;
- a theme plugin in both modes at three widths, and `?safe=1` showing the
  default;
- safe mode and `disable --all` after a rung-4 module that throws on load.

In Go, each a test that fails when its line of code is removed: a grant
refused on every `/api/share/`, `/page-admin/`, `/api/chat/tools/` and
authenticated route, as a path segment, a cookie and a header; a share token
and an admin grant refused on `/api/plugin/`; the capability matrix; a view
without `read:paths` carrying no path, resolved by `reflect` over the struct
rather than by a list of field names; the supervisor's three — never exits,
crashes in a loop, floods; an event channel that drops rather than blocks
while the poller runs; a `server.js` that loops, throws, or returns a
megabyte; a `net:` fetch to a loopback address refused after resolution.

## 13. Build order

Each step ships on its own and is useful alone; the next does not change the
one before it.

0. **Foundations and themes.** `internal/plugins` (manifest, limits,
   `Describe`, `LintTheme`), the tables, install/remove/enable/disable, the
   `vibepanel plugin` commands, Settings → Plugins with the install screen,
   rung 0 end to end including the picker and the pre-paint script. No new
   credential, no frame, no runtime: the screen, the words and the audit
   rows are the thing being tested.
1. **Panels.** `plugin_grants`, `/plugin/{grant}/`, the CSP, the SDK, the
   bridge, the view and its handles, the read capabilities plus notes,
   todos and state, the side-panel and settings slots with their unions
   opened, dev mode with the fingerprint reload. `plugins-check` begins here.
2. **Services.** `pageserver.go` into a shared package; events, schedule,
   routes, `ctx` by capability, sources and hosts per plugin, `inbound`.
3. **Processes.** `plugin_tokens`, the supervisor and its three tests, the
   cgroup leaf, the events socket, and only now `sessions:create`,
   `sessions:input` and `sessions:control` with their bold sentences.
4. **Unsandboxed.** The host object, the switch, safe mode, the `override`
   list, the amber `tested` range.

Scoped API tokens — the prerequisite the earlier document named — are not
needed by this order, because a plugin never holds an API token: it holds a
grant or a plugin token on its own prefix, which is scoped by construction.

## 14. What the earlier version of this document said, and what still holds

It said four things were being called a plugin — react to sessions, send an
event somewhere, add something to the interface, ship it as a file — and
that the first two were the API and a webhook, the third was arbitrary code
as the owner, and the fourth was a supervisor worth building last. It
rejected every in-process runtime (Go's `plugin` package, WASM, goja, Lua) as
a way of avoiding a process, and it named scoped API tokens as the
prerequisite.

What changed is not the reasoning but the facts under it. Share pages built a
sandboxed HTML surface on the panel's origin, a goja runtime with a budget
and a `ctx`, host-approved outbound fetches with a resolved-address guard,
session-bound grants, a per-page data store, and a dev loop with a live
preview — each with its own tests and its own threat table. "Add something to
the interface" now has a sandboxed shape, which it did not. And the request
is a different one: not a harness, but an owner — or their agent — adding to
their own panel.

What still holds, and is kept above in the same words: nothing a plugin does
may make a session's state stop updating, so there is no veto and events are
a bounded channel; a plugin never gets the tmux socket; the subprocess is a
supervisor, not a runtime, and its three tests are the ones listed; a
capability is decided in one place; and same-origin code is the owner's own
authority, which is said on the screen rather than mitigated by a message
allowlist. The one sentence that is withdrawn is "if a panel tab is wanted,
it is a pull request, not a plugin." Rung 1 is the tab.

## 15. What exists now: chat adapters

The one plugin shape the panel ships is the chat adapter (`internal/chat`,
`docs/design.md`): a Go package that registers a factory and declares its
capabilities, and is otherwise given nothing but an HTTP client and its own
persisted state. That is in-process on purpose: the write path into a pane is
the one thing a plugin must never own, and the bridge keeps it. Nothing above
changes it; a fourth IM is still a Go package, because an adapter needs the
bridge's addressing rules, which are not a plugin's to reimplement.
