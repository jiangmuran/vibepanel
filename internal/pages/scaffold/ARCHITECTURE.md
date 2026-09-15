# Share pages with a backend: data, admin pages, sources, server code, visitor actions

A share page started as HTML that reads one redacted snapshot. This document is
everything a page can do beyond that, why each piece is shaped the way it is,
and what stops each from becoming a way into the panel. It is written for three
readers at once: the person who owns a panel, the agent writing a page (a copy
of this file is written into every page directory as `ARCHITECTURE.md`, and a
test keeps the two identical), and whoever changes this code next.

Every capability here is **opt-in, per page, in `vibepanel.json`**. A page that
declares none of them is exactly the page [share-pages.md](share-pages.md)
describes, and nothing below changes for it.

## 1. The whole picture

```
                      ┌──────────────────────── the panel's process ────────────────────────┐
  owner's browser     │                                                                     │
  (signed in)  ──────▶│  /api/settings/pages/{id}/…      manage: data, hosts, secrets, links │
      │               │  /pages/{id}/admin/  ── 303 ──▶  /page-admin/{grant}/  (admin page)   │
      │               │  /api/page-admin/{grant}/v1/…    admin API: data, actions, sources   │
      │               │                                                                     │
  a screen            │  /share/{token}/…                the page's files                    │
  (no account) ──────▶│  /api/share/{token}/v1/snapshot  read                                │
                      │  /api/share/{token}/v1/actions/{name}   write, if the link allows it │
                      │                                                                     │
                      │  page data (SQLite) ◀── server.js (goja sandbox) ──▶ sources (HTTPS) │
                      └─────────────────────────────────────────────────────────────────────┘
```

Three kinds of caller, three kinds of credential, three sets of routes. None of
them is a flag on another:

| caller | credential | where it is looked up | what it can reach |
|---|---|---|---|
| the owner, in the panel | the panel's session cookie | `sessions` | the settings routes, including everything below |
| an admin page | an **admin grant** in the URL path | `page_admin_grants` | one page's admin API |
| a screen | a **share token** in the URL path | `share_links` | one link's snapshot, its page's files, and — only on an interactive link — that page's declared visitor actions |

A share token presented where a grant is expected, or a grant where a session
is expected, is an unknown string and answers `401`. Tests walk the router for
each prefix (`TestAShareTokenReachesOnlyTheseRoutes`,
`TestAnAdminGrantReachesOnlyTheseRoutes`).

## 2. Page data

A page's own small store: an announcement, today's goals, a leaderboard, the
votes a lobby screen collected. Declared in the manifest, written by the owner
(settings, admin page, `vibepanel page data`), by `server.js`, or by a visitor
action; read by the page in its snapshot.

```json
"data": {
  "announcement": { "type": "text", "max": 280, "default": "" },
  "goals":        { "type": "list", "item": { "type": "text", "max": 80 }, "max": 20 },
  "votes":        { "type": "counter" },
  "guestbook":    { "type": "log", "item": { "type": "object", "fields": {
                      "name": { "type": "text", "max": 40 },
                      "msg":  { "type": "text", "max": 140 } } }, "max": 200 },
  "notes":        { "type": "text", "max": 2000, "visibility": "admin" }
}
```

**Types.** `text` (`max` runes, ≤ 10000), `number` (`min`, `max`), `bool`,
`enum` (`values`), `color` (`#rrggbb`), `list` (an array of `item`, at most
`max` ≤ 500), `object` (`fields`, one level of the scalar types), `json`
(anything, `maxBytes` ≤ 65536), `counter` (an integer ≥ 0, changed only by
`increment`), `log` (append-only, keeps the newest `max` ≤ 1000 entries of
`item`, each stamped with `at`).

**Limits.** At most 64 keys; a whole page's data at most 256 KiB encoded. A
write that would exceed either is refused, not truncated.

**Visibility.** `"visibility": "public"` (the default) is sent in every link's
snapshot, in both detail modes — like a link's remark, it is text the owner
chose to put on a screen, not the panel's own names. `"visibility": "admin"`
is never in a share snapshot and is readable only through the admin API and
settings. A visitor-written `log` is public unless marked otherwise, and a
screen shows every visitor what other visitors wrote: the owner decides that
when they declare it.

**Two namespaces.** `live` is what handed-out links read and write. `draft` is
what the Preview pane, a draft admin page and `vibepanel page run` use, so
trying a page never changes what a wall shows. Publishing does not copy data;
data is not part of a version. A key present in storage but no longer declared
by the version a link draws is not sent; it is kept until the owner clears it.

**Storage.** `share_page_data(page_id, ns, key, value, updated_at, updated_by)`,
validated against the manifest of the version being written for (the published
one for `live`, the draft directory's for `draft`). Every owner write is
audited as `page.data_changed`; visitor writes are counted, not one row each
(see §6).

## 3. Admin pages, and who may open them

A page may ship its own admin UI: `admin/index.html` and whatever beside it.

```json
"admin": { "entry": "admin/index.html" }
```

Without one, settings draws a form from the data schema (the same way it draws
link parameters), which is enough for most pages.

**It requires the panel login.** The address to open is
`/pages/{pageID}/admin/` on the panel. That route is behind the ordinary session:
not signed in, it goes to the sign-in page and back. Signed in, the server mints
an **admin grant** and answers `303` to `/page-admin/{grant}/`.

**Why a grant, and not the cookie.** The admin page is HTML the owner — usually
an agent — wrote. Run with the panel's cookie on the panel's origin, it would be
the owner: every API, the terminal socket, the password change. So it is served
exactly like a share page: `Content-Security-Policy: sandbox allow-scripts
allow-forms`, an opaque origin with no cookie and no storage, and a `connect-src`
that names `/api/page-admin/{grant}/v1/` and nothing else. The grant is the only
credential it holds, and the grant reaches one page's admin API.

**A grant** is 32 random bytes, stored as a SHA-256 in `page_admin_grants`
with the page, the user and **the session it was minted from**. It lives
8 hours, and it dies with that session: signing out, a password change or a
revoked session ends every grant minted from it on the next request. It is
never listed, never shown, and not a share token — `share_links` does not
contain it.

**Embedding.** Settings opens the admin page in a dialog as an iframe of
`/pages/{pageID}/admin/`; the grant's pages send `frame-ancestors 'self'`, so
another site cannot frame an admin page. Opened directly, it is a normal tab.

**Draft.** `/pages/{pageID}/admin/?draft=1` serves the admin page from the draft
directory and writes to the `draft` namespace, for the Preview pane.

**The admin API** (`/api/page-admin/{grant}/v1/`):

| route | what |
|---|---|
| `GET snapshot` | the page's snapshot with `detail: names`, whole panel, and admin-visibility data included |
| `GET data` | every declared key, both visibilities |
| `PUT data/{key}` | replace a value (any type except `counter` and `log`) |
| `POST data/{key}/increment` | `{"by": n}`, counters |
| `POST data/{key}/append` | `{"item": …}`, logs |
| `DELETE data/{key}` | back to the default |
| `POST actions/{name}` | run an action declared with `who` including `admin` |
| `GET sources` | each source's last fetch: `ok`, `fetchedAt`, `error` |
| `GET links` | the page's links: name, remark, interactive, viewers — no tokens, no URLs |

Writes through a grant are audited with the user the grant was minted for.

## 4. Sources

Data the server fetches for the page, so the page itself never needs the
network:

```json
"sources": [
  { "key": "weather", "url": "https://api.example.com/v1/now?city=shanghai",
    "every": "10m", "headers": { "Authorization": "Bearer ${secret:WEATHER_TOKEN}" },
    "maxBytes": 65536, "timeout": "5s", "parse": "json" }
]
```

`every` is 1 minute to 24 hours; `timeout` at most 10 seconds; `maxBytes` at
most 1 MiB; `parse` is `json` or `text`. Only `GET`. The result is in the
snapshot as `sources.weather: {ok, fetchedAt, status, error, value}`.

**The owner approves each host, per page.** A source whose host is not approved
fetches nothing and says `host not approved`. Approval is in settings (the page
card's *Sources*), shown with the full URL; approving `api.example.com` does not
approve anything else. Hosts are part of neither a version nor an export.

**What the fetch refuses**, checked on the resolved address, not the name:
`http:` (https only); loopback, private (RFC 1918, RFC 4193), link-local,
CGNAT, multicast, unspecified and the cloud metadata addresses; a port other
than 443 unless the owner approved `host:port`; a redirect (any — a redirect is
a second URL nobody approved); a response over `maxBytes`; anything slower than
`timeout`. The connection is made to the address that was checked, so a DNS
answer that changes between the check and the connect cannot point it
somewhere else.

**Secrets.** `${secret:NAME}` in a header is replaced at fetch time with a value
the owner stored in settings for that page. Secrets are encrypted at rest
(§7), never sent to a page, an admin page, a snapshot, a log line or an export,
and a header that contains one is redacted in every error message. A name not
stored makes the source fail with `secret NAME is not set`.

**When it runs.** Like the repository reads: in the background, only while
something is looking at the page (a link polled in the last 5 minutes, the
Preview pane, an admin page), never on a request goroutine. A snapshot reports
the last result and its age.

## 5. `server.js`

Code the owner writes that runs **inside the panel's process, in a JavaScript
sandbox** ([goja](https://github.com/dop251/goja), pure Go):

```json
"server": { "entry": "server.js", "every": "5m" }
```

```js
// server.js — every function is optional
function transform(input) {            // on each snapshot build, ≤ 50 ms
  // input: { snapshot, data, sources }
  return { leader: input.data.votes > 10 ? 'yes' : 'no' }   // → snapshot.server
}
function onSchedule(ctx) {             // every `every` (≥ 1m), while watched, ≤ 500 ms
  ctx.data.set('announcement', 'Stand-up at ' + ctx.now().getHours())
}
function onAdminAction(name, payload, ctx) {   // ≤ 500 ms
  if (name === 'reset') ctx.data.reset('votes')
  return { ok: true }
}
function onVisitorAction(name, payload, ctx) { // ≤ 200 ms
  // ctx.visitor: { id, link }  — a per-link pseudonym, and the link's name
  ctx.data.increment('votes')
  return { total: ctx.data.get('votes') }
}
```

**What `ctx` has**: `data.get/set/increment/append/reset` (validated exactly as
§2; `set` is refused from `onVisitorAction` unless the key is listed in the
action's `writes`), `sources` (last results, read-only), `now()`, `log(...)`
(to the page's server log, last 200 lines, shown in settings and in
`.vibepanel/server.log` for the agent).

**What it does not have**: `require`, modules, file access, processes, the
network (use sources), timers, the panel's API, or any other page's data.
There is no global state between calls: each call gets a fresh runtime from the
compiled program.

**Limits**: the time budgets above, enforced by interrupting the runtime; a
return value of at most 64 KiB encoded; a call that throws, times out or
returns too much is reported (settings, `server.log`, the Preview's errors) and
changes nothing — data writes made before the failure in that call are rolled
back.

**Whose code this is.** `server.js` is the owner's code, published with the
page; a screen can only make it run through a declared visitor action. The
sandbox is there so that a mistake — an infinite loop, a huge result — costs a
failed call and not the panel, not to make untrusted code safe to run: a
visitor never supplies code, only a payload validated against the action's
input schema.

## 6. Visitor actions: a screen that writes

A kiosk with a *Vote* button, a guestbook, a "we're in a meeting" toggle at a
door. Visitors write **only to this page's data, only through actions the page
declared, only on a link the owner made interactive.**

```json
"actions": {
  "vote":  { "who": "visitor", "effect": { "increment": "votes" }, "rate": "5/min" },
  "sign":  { "who": "visitor", "input": { "name": { "type": "text", "max": 40 },
                                          "msg":  { "type": "text", "max": 140 } },
             "effect": { "append": "guestbook" }, "rate": "2/min" },
  "reset": { "who": "admin", "effect": "server" }
}
```

`who` is `visitor`, `admin` or `both`. `effect` is `{"increment": key}`,
`{"append": key}` (the input becomes the item), `{"set": key}` (admin only), or
`"server"` (calls `onVisitorAction` / `onAdminAction`). `input` is the object
schema the payload must match, using §2's scalar types.

**Seven conditions, all checked on every call** to
`POST /api/share/{token}/v1/actions/{name}`:

1. The link exists, has not expired, and passes the address allowlist — the
   same middleware as the snapshot.
2. **The link is interactive.** A per-link switch, off by default, set by the
   owner when making or editing the link, audited as
   `share.interactive_changed`. A preview link writes to `draft`; a
   fifteen-minute *View* copy of a link is never interactive.
3. **The panel allows visitor writes at all** (Settings → Sharing, on by
   default; off refuses every action on every link immediately).
4. The action is declared, with `who` including `visitor`, **in the version
   the link is drawing** — not the draft, not a newer version.
5. **Rate limits** pass: the action's `rate` per client address (default
   `30/min`), 600 actions per minute per link, 3000 per minute across the
   panel. Over any of them is `429` with `Retry-After`. Checked before the body
   is read, so a flood costs a lookup, and a refused call still counts against
   its address.
6. The body is JSON, at most 4 KiB, and matches `input` exactly: no extra
   fields, every string free of control characters (line breaks included) and
   bidi overrides, every limit held.
7. The effect's own limits hold (§2's sizes, a log's `max`, the data cap).

The answer is `{ok, result}` or `{ok: false, error, retryAfter}`. The next
snapshot carries the change, and the SDK asks for it immediately.

**What this does not open.** Actions write page data and run `server.js`;
nothing else. No panel table, no session, no process, no terminal, no file.
In [writable-links.md](writable-links.md)'s ladder this is a rung below the
first one, and the rungs it refuses stay refused. The share token still reaches
no other write: `TestAShareTokenReachesOnlyTheseRoutes` lists exactly one
`POST`, and a test proves that route refuses on a non-interactive link, on an
undeclared action, on a draft-only action and with visitor writes turned off.

**Cross-origin.** The route answers `OPTIONS` with
`Access-Control-Allow-Origin: *`, `POST` and `Content-Type`, never credentials —
the token in the path is the capability, as for the snapshot.

**Accountability.** Each action is counted per link and per action; the audit
log gets one `share.action` row per link per action per minute with the count
and the client addresses, not a row per click. A visitor's pseudonym
(`ctx.visitor.id`) is derived from the link and the client and joins to nothing.

## 7. Links: reusable, and copyable again

A share link is an address. Any number of screens may open it, as often as they
like, until it is revoked or expires. It is not a one-time link.

**The address can be copied again at any time** from the link's row. The token
is stored twice: as a SHA-256, which is what a request is looked up by, and
encrypted with AES-256-GCM under a key in `<data dir>/secrets.key` (created
with mode `0600` on first use, never in the database). So a copy of the
database alone does not contain a single working address; the database *and*
the key file do, which is the same machine the panel runs on.

Links made before this existed have no encrypted copy and cannot be shown
again. Their row offers **New address**, which issues a new token (the old URL
stops working) and is audited as `share.rotated`. Any link can be rotated
this way — the answer to "this URL leaked".

The same key encrypts source secrets (§4).

## 8. The SDK

```js
const vp = VibePanel.connect()
vp.on('snapshot', (s) => { s.data; s.sources; s.server; s.actions; s.interactive })
vp.on('data', (d) => …)                     // when data changed
const r = await vp.action('vote')            // { ok, result } | { ok: false, error, retryAfter }
```

In an admin page the same file detects `/page-admin/` in its address and adds
`vp.admin`: `data()`, `set(key, value)`, `increment(key, by)`,
`append(key, item)`, `reset(key)`, `sources()`, `links()`, and `vp.action`
runs admin actions. `vibepanel.d.ts` declares all of it.

## 9. Developing one

- `vibepanel page check` validates `data`, `admin`, `sources`, `server` and
  `actions` together: an action whose effect names an undeclared key, a source
  header naming a secret with no `${secret:}` syntax, a `server` effect with no
  `server.js`.
- The Preview pane has **Admin** beside the frames (the draft admin page, on
  `draft` data) and **Data** (the draft namespace, editable, with *Reset*).
  Visitor actions in the Preview write to `draft`.
- `vibepanel page data get|set|reset [--live] <key> [value]`.
- `vibepanel page run transform|schedule|action <name> [payload]` runs
  `server.js` against `draft` data and prints the result and the log.
- Fixtures may carry `data`, `sources` and `server`; `fixtures/*.json` are
  shaped to the manifest as before.
- Export includes `admin/` and `server.js`; `?data=1` adds the `live` data as
  `vibepanel-data.json` (a name reserved at a page's root, so it cannot be
  mistaken for one of the page's own files). Secrets and approved hosts are
  never exported; import writes the data into `live` and `draft`, checked
  against the manifest the page arrived with, and answers the hosts and secret
  names the page needs.

## 10. What an owner turns on, and where

| capability | declared in | switched on by |
|---|---|---|
| data | `vibepanel.json` | publishing |
| admin page | `vibepanel.json` + `admin/` | publishing; opening requires login |
| sources | `vibepanel.json` | approving each host, storing each secret |
| server.js | `vibepanel.json` + `server.js` | publishing |
| visitor actions | `vibepanel.json` | publishing **and** making a link interactive **and** visitor writes allowed |

## 11. Threats, and what answers each

| threat | answer |
|---|---|
| A page's HTML acts as the owner | Every page and admin page is sandboxed with an opaque origin; no cookie reaches it; `connect-src` names one credential's API |
| A leaked share URL writes | Only on an interactive link; only declared actions; only page data; rate limited; rotate the link |
| A leaked admin URL | The grant dies with the session that minted it and after 8 hours; it reaches one page's admin API |
| A visitor floods writes | Per-address, per-link and panel-wide rate limits; data size caps; the global switch |
| A visitor injects markup | Input schemas reject control and bidi characters; the SDK's `vp.text` and the templates render text as text; the `hostile` fixture now includes visitor data |
| A source reaches the internal network | Approved hosts only, https, resolved-address checks, no redirects, pinned connection |
| A secret leaks | Encrypted at rest; never in a snapshot, page, admin page, log line or export; redacted in errors |
| `server.js` hangs or explodes | Interrupted at its budget; output capped; failed calls roll back |
| The database is copied off the machine | Tokens and secrets are encrypted under a key that is not in it |
| A page someone imported does something unexpected | Imports are unpublished; hosts and secrets must be approved and stored by the new owner; links are not interactive until made so |

## 12. The settings API for all of it

Behind the ordinary session, like every settings route. Shapes are JSON.

**Links**

| route | body → answer |
|---|---|
| `POST /api/settings/shares` | adds `"interactive": false` |
| `PATCH /api/settings/shares/{id}` | adds optional `"interactive": bool` (refused on a locked link like the rest) |
| `GET /api/settings/shares/{id}/url` | → `{"url", "token"}`; `409 {"error", "rotatable": true}` for a link made before addresses were kept |
| `POST /api/settings/shares/{id}/rotate` | → `{"url", "token"}`; the old address stops working |

A listed link adds `"interactive": bool`, `"copyable": bool` (false for a link
with no encrypted token), and `"actionsToday": n`.

**Visitor writes, panel-wide**

| route | body → answer |
|---|---|
| `GET /api/settings/sharing` | → `{"visitorWrites": true}` |
| `PUT /api/settings/sharing` | `{"visitorWrites": bool}` → the same; audited `sharing.visitor_writes` |

**Data** (`ns` is `live`, the default, or `draft`)

| route | body → answer |
|---|---|
| `GET /api/settings/pages/{id}/data?ns=` | → `{"schema": {key: spec}, "values": {key: value}, "updatedAt": {key: unix}, "bytes": n, "limit": 262144}` |
| `PUT /api/settings/pages/{id}/data/{key}?ns=` | `{"value": …}` → `{"value": …}` |
| `POST /api/settings/pages/{id}/data/{key}/increment?ns=` | `{"by": n}` → `{"value": n}` |
| `POST /api/settings/pages/{id}/data/{key}/append?ns=` | `{"item": …}` → `{"value": [...]}` |
| `DELETE /api/settings/pages/{id}/data/{key}?ns=` | → `204`, back to the default |
| `DELETE /api/settings/pages/{id}/data?ns=` | → `204`, every key back to its default |

The schema is the published version's for `live` and the draft directory's
for `draft`.

**Sources, hosts and secrets**

| route | body → answer |
|---|---|
| `GET /api/settings/pages/{id}/sources` | → `[{"key", "url", "host", "approved", "every", "ok", "fetchedAt", "status", "error", "secrets": [{"name", "set"}]}]` |
| `GET /api/settings/pages/{id}/hosts` | → `{"hosts": ["api.example.com"]}` |
| `PUT /api/settings/pages/{id}/hosts` | `{"hosts": [...]}` → the same; audited `page.hosts_changed` |
| `GET /api/settings/pages/{id}/secrets` | → `[{"name", "setAt"}]`, never a value |
| `PUT /api/settings/pages/{id}/secrets/{name}` | `{"value": "…"}` → `204`; audited `page.secret_set` (the name, never the value) |
| `DELETE /api/settings/pages/{id}/secrets/{name}` | → `204`; audited `page.secret_deleted` |

**server.js**

| route | body → answer |
|---|---|
| `GET /api/settings/pages/{id}/server/log` | → `{"lines": [{"at", "level", "text"}]}`, the last 200 |

**A page's capabilities.** `GET /api/settings/pages/{id}` adds
`"capabilities": {"data", "admin", "sources", "server", "actions", "visitorActions"}`
(booleans, from the published version), and a manifest in any response carries
`data`, `admin`, `sources`, `server` and `actions` as declared.

**Admin pages.** `GET /pages/{id}/admin/` (and `?draft=1`) as in §3; settings
embeds it.
