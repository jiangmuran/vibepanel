# The HTTP API

Everything the panel's own frontend does, an agent can do. This is that
interface, written down so it can be depended on.

`docs/api.md` is checked against the router by
`TestTheAPIDocCoversEveryRoute` — an endpoint that exists and is not on this
page fails the build, and so does a line here for a route that has been
removed. The list below is therefore complete rather than merely current.

## Getting in

A browser signs in with a password and keeps a cookie. A program should not have
to: the cookie expires, it is `SameSite=Strict`, and obtaining one means posting
the password and keeping a jar. Use an API token instead.

Make one in **Settings → API tokens**, or:

```sh
curl -sX POST https://panel.example:8443/api/settings/tokens \
  -b cookies.txt -H 'Content-Type: application/json' \
  -d '{"name":"deploy bot"}'
# {"token":"pOsC…","id":"…","prefix":"pOsC7x2p","name":"deploy bot"}
```

The response is the only time the token is readable — the database keeps a
SHA-256 of it, so a leaked backup does not hand over live credentials, and
there is no "show it again".

Then, on every request:

```
Authorization: Bearer pOsC…
```

Tokens do not expire, which is the point: an agent left running for a fortnight
should not stop working because a session TTL passed. Revoke one at a time from
the settings page or with `DELETE /api/settings/tokens/{id}`; revocation takes
effect on the next request. Changing your password signs out every browser and
leaves tokens alone, and revoking a token leaves your password alone — the two
credentials are independent on purpose.

A token carries exactly the authority of the account that made it. There are no
scopes, and pretending otherwise with a `scope` field nobody enforces would be
worse than saying so.

## Conventions

- JSON in, JSON out. `Content-Type: application/json` on anything with a body.
- Errors are `{"error": "..."}` with a status that means something: `400` you
  sent something malformed, `401` sign in, `403` not allowed from here, `404` no
  such thing, `409` the state you assumed has changed, `503` the panel cannot
  reach its own database.
- Arrays are always arrays. An endpoint that has nothing to return sends `[]`,
  never `null`.
- Timestamps are Unix seconds.
- Ids are opaque strings. Do not parse them.

## Health

### `GET /api/health`

Open — no credential needed. What a monitor should watch.

```json
{"ok": true, "version": "v0.4.0", "commit": "a1b2c3d", "tmuxVersion": "3.6",
 "live": 4, "passkeys": true}
```

`ok` is `false` when the panel cannot write to its database, and `stale` then
carries the reason. The terminals are unaffected by that — they belong to tmux —
which is exactly why `ok: true` was not enough on its own.

`version` and `commit` together identify the build. Comparing them across a
reconnect is how the panel's own frontend notices it has been upgraded.

## State

### `GET /api/state`

Everything on screen, in one object: projects, sessions, which sessions are
live, the manual ordering, whether states are being guessed, whether the hooks
are installed, and any storage warning.

This is the one to poll if you are not using the WebSocket. It is also what the
socket pushes, so the shapes are identical.

### `GET /api/system`

CPU, memory, swap, disk and load. `cpuPercent` is `null` until there are two
samples to difference, and `cpuReadable` is `false` where there is no
`/proc/stat` at all — a machine that cannot be measured says so rather than
reporting zero.

### `GET /api/usage`

What each session's process tree is costing right now, keyed by session id:

```json
{"readable": true, "cores": 16,
 "sessions": {"s_abc": {"cpuPercent": 24.1, "rss": 831258624, "procs": 7}}}
```

`cpuPercent` is a share of the **whole machine**, the same denominator
`/api/system` uses — not top's, where 100% means one core. Both numbers appear
within an inch of each other in the UI, and a session reading "310%" beside a
machine reading "31%" invites exactly one wrong conclusion. `cores` is there to
convert if you want the other convention.

`rss` sums the resident set across the tree and so double-counts pages shared
with forked children; it is an over-estimate, like every tree total. `procs` is
how many processes were found, which is what says whether the reading means
anything — 1 is a bare shell.

A session whose pane has gone is **absent** rather than zero, because zero is a
real reading. `readable` is `false` where there is no `/proc` to walk.

Deliberately not part of `/api/state`: that snapshot is broadcast to every
viewer whenever it changes, and a number that moves every tick would make every
tick a broadcast.

## Projects

### `GET /api/projects/{id}/files?path=`
### `GET /api/projects/{id}/download?path=`
### `POST /api/projects/{id}/upload?path=`
### `POST /api/projects`
### `PATCH /api/projects/{id}`
### `DELETE /api/projects/{id}`
### `POST /api/projects/reorder`

`POST /api/projects` takes `{"path": "...", "name": "..."}`; a leading `~` is
expanded and the name defaults to the directory's base. `PATCH` accepts `name`
and `pinned`. `DELETE` kills every session in the project and then removes it —
it does not touch the directory.

`reorder` takes `{"ids": [...]}` in the order you want and switches the panel to
manual ordering.

Paths in `files`, `download` and `upload` are relative to the project root and
are resolved through it: a path that leaves the project is refused, symlinks
included.

## Sessions

### `POST /api/sessions`
### `PATCH /api/sessions/{id}`
### `DELETE /api/sessions/{id}`
### `POST /api/sessions/{id}/restart`

```sh
curl -sX POST .../api/sessions -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

`command` is an argv. Omit it for a shell in the project directory, which is
what the panel's own UI does — it never launches an agent for you.
`parentSessionId` makes the new session a scratch terminal under another one.

`PATCH` accepts `title` and `state`. Setting `state` is the manual override the
status dot offers, and it stands until the session does something new.

`restart` brings a **dead** session's process back in the same pane, keeping its
id, name and scrollback. It refuses a session that is still running with `409`:
two viewers looking at one panel is the ordinary case, and a stale tab offering
the button must not kill the agent somebody else just started.

`DELETE` kills the tmux session and its scratch terminals, then removes the row.

## Notes and todos

### `GET /api/projects/{id}/notes`
### `PUT /api/projects/{id}/notes`
### `GET /api/projects/{id}/todos`
### `POST /api/projects/{id}/todos`
### `PATCH /api/todos/{todoID}`
### `DELETE /api/todos/{todoID}`

`PUT` on notes takes `{"content": "...", "baseRev": N}` and answers `409` with
the current note if `baseRev` is not the revision on disk. Omit `baseRev`
entirely for an unconditional write, which is what a script that is not merging
anything wants.

Note the asymmetry, because it is the thing to get wrong: you **read** `rev` and
you **send it back as** `baseRev`. They are different names for the same number
because they are different claims — one is "this is the revision", the other is
"this is the revision I was looking at". The server rejects an unknown field, so
sending `rev` gets a `400` naming it rather than an unconditional write.

Two people editing one note in the same second is why it is a counter and not a
timestamp.

## Browsing the filesystem

### `GET /api/browse?path=`
### `POST /api/browse/mkdir`

Directories only, rooted at the home directory, for choosing where a project
should live. `mkdir` takes `{"path": "...", "name": "..."}` where `name` is one
element — not a path.

## Settings

### `GET /api/settings`
### `GET /api/settings/audit`
### `GET /api/settings/hooks`
### `POST /api/settings/hooks`
### `DELETE /api/settings/hooks`
### `GET /api/settings/tokens`
### `POST /api/settings/tokens`
### `DELETE /api/settings/tokens/{tokenID}`

`POST /api/settings/hooks` merges the state-reporting hooks into the agent's own
configuration file, backing it up first and tagging every entry so removing them
later cannot take anybody else's with it. `GET` shows what it would write before
you agree to it.

`POST` and `DELETE` take `?agent=claude` (the default, and what the parameter-less
request has always meant) or `?agent=codex`. Anything else is a `400`: the value
decides which file in the user's home directory gets edited, so an unrecognised
one has to be refused rather than resolved to whichever agent is first in the
code. Claude's four events are merged into `~/.claude/settings.json`; Codex's one
`notify` line goes into `~/.codex/config.toml`, above the first table — a
top-level key appended to the end of that file would belong to the last table in
it and Codex would never read it.

## Authentication

### `POST /api/auth/setup`
### `POST /api/auth/login`
### `POST /api/auth/logout`
### `GET /api/auth/state`
### `POST /api/auth/password`
### `GET /api/auth/passkeys`
### `DELETE /api/auth/passkeys/{credID}`
### `POST /api/auth/passkey/register/begin`
### `POST /api/auth/passkey/register/finish`
### `POST /api/auth/passkey/login/begin`
### `POST /api/auth/passkey/login/finish`

Browser flows. A program wanting in should use a token instead of any of these.

`GET /api/auth/state` is open and answers `{"configured", "authenticated",
"username", "passkeysUsable", "passkeyReason"}`. It returns `503` rather than
"not signed in" when the database cannot be read, because a client that treats
those as the same thing signs the user out during a storage fault — into a login
form that reads the same database.

## Hooks

### `POST /api/hook/state`

What the state reporter posts. Authenticated by the panel's own hook token,
which is injected into each session's environment as `VIBEPANEL_TOKEN` — not by
an API token, and not by a session.

```json
{"sessionId": "…", "state": "waiting"}
```

`state` is one of `waiting`, `working`, `done`. Anything else is refused, and a
refused token is audited as `hook.rejected`.

## The WebSocket

### `GET /ws`

One connection carries everything: terminal bytes both ways, resize, paste,
state broadcasts, and panel notifications.

- **Binary frames** are terminal traffic: `[1 byte type][4 bytes ref][payload]`,
  big-endian. Type `0` is live output, `1` is replayed scrollback.
- **Text frames** are JSON control messages with a `t` discriminator.

From the client: `subscribe`, `unsubscribe`, `resize`, `takeControl`, `paste`,
`ping`. From the server: `subscribed`, `size`, `title`, `clipboard`, `exit`,
`dropped`, `error`, `pong`, `state`, `panel`.

The handshake takes the same credential as everything else. `Origin` must match
`Host`, which is what stops another page opening this socket with your cookies
attached.

## What is not here

There is no way to attach to a session's terminal over plain HTTP — that is the
WebSocket's job, and a polling shim would be a worse version of it. There is no
API for the setup token; it is printed to the panel's own log on first run and
consumed once.
