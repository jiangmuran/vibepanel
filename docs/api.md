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
### `GET /api/projects/{id}/preview?path=`
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

Paths in `files`, `download`, `preview` and `upload` are relative to the project
root and are resolved through it: a path that leaves the project is refused,
symlinks included.

`preview` is `download` with a ceiling and an opinion. It answers with the
bytes, and says what it decided they are in `X-Preview-Kind`: `text`, `image` or
`pdf`. For an image or a PDF, `X-Preview-Type` carries the media type it
matched, from a short whitelist — that header is what the caller should build a
`Blob` from, because the response itself is still `application/octet-stream`
with `nosniff` and an `attachment` disposition. Nothing a project contains is
ever offered to a browser as something to render on the panel's origin.

The kind comes from the leading bytes, never from the extension: a `Makefile` is
text and a `notes.txt` holding a tarball is not. SVG is deliberately read as
text rather than drawn — it is a document with scripting in it.

Three limits, and each answers differently:

- Over **8 MiB** (`previewMaxBytes`), an image or a PDF is `413`. Half a picture
  draws nothing, so there is nothing useful to truncate to.
- Text is **truncated**, never refused: at 256 KiB or 4000 lines, whichever
  comes first, cut back to the last whole line. `X-Preview-Truncated: true` says
  it bit, and the panel says so on screen. A two-gigabyte log is worth clicking;
  only the top of it is ever read.
- Anything else — a NUL byte or invalid UTF-8 in what was read — is `415`, which
  is an answer rather than a failure: there is a file, and `download` will hand
  it to you.

A directory is `400`, and so is a FIFO, a socket or a device node: opening a
FIFO with no writer never returns, and it would take the request goroutine and
graceful shutdown with it.

## Sessions

### `POST /api/sessions`
### `PATCH /api/sessions/{id}`
### `DELETE /api/sessions/{id}`
### `POST /api/sessions/{id}/restart`
### `POST /api/sessions/restore`

```sh
curl -sX POST .../api/sessions -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

`command` is an argv. Omit it for a shell in the project directory, which is
what the panel's own UI does — it never launches an agent for you.
`parentSessionId` makes the new session a scratch terminal under another one.

The argv is kept, on the row, as `launchCommand`. Do not confuse it with
`command`, which is `#{pane_current_command}` — the name of whatever is in the
pane right now, rewritten every two seconds, `"node"` for an agent and `"bash"`
for a shell somebody used. `launchCommand` is what a restore executes;
`command` is a label. `launchRecorded` is `false` on rows written before the
panel kept the argv at all, which is a different thing from an empty
`launchCommand` (a login shell, and exactly reproducible).

`PATCH` accepts `title`, `state`, `pinned`, `sortIndex`, `clearSortIndex` and
`restoreOnBoot`. Setting `state` is the manual override the status dot offers,
and it stands until the session does something new. `restoreOnBoot` asks for
this session to be rebuilt at the next startup that finds its tmux session
missing, without confirmation; it is off by default, and it should stay off for
anything you would not want two dozen of starting at once.

`restart` brings a **dead** session's process back in the same pane, keeping its
id, name and scrollback. It refuses a session that is still running with `409`:
two viewers looking at one panel is the ordinary case, and a stale tab offering
the button must not kill the agent somebody else just started. If the *tmux
session itself* is gone — which is what a reboot leaves behind — `restart` does
what `restore` does, because there is no pane to respawn into.

`restore` rebuilds sessions whose tmux session no longer exists:

```sh
curl -sX POST .../api/sessions/restore -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"ids":["…","…"]}'
# {"results":[{"id":"…","ok":true},{"id":"…","ok":false,"error":"…"}]}
```

`ids` is required and there is no "all" flag. It answers `200` with one result
per id even when some failed — after a reboot the ordinary failure is a single
project directory that was pruned while the machine was off, and refusing the
whole batch over it would leave twenty-three sessions dead to report one.

**What restore restores, and what it cannot.** The session comes back under the
same id, in the same project, with the same name, working directory, ordering
and tmux name; the recorded `launchCommand` is executed again; and the archived
scrollback is printed into the new pane's history above a banner saying when it
was captured. The **process** does not come back. An agent's context lived in
that process and in a provider's conversation, and neither survived the machine
going down: what starts is a new agent that remembers none of it. Anything you
build on this should say so where a person will read it.

The scrollback is captured every 30 seconds for sessions that have produced
output, bounded to the last 2,000 lines and 256 KiB, and once more for every
session when the panel shuts down — so an orderly reboot loses nothing and a
power cut loses at most half a minute. `scrollbackAt` on a session row is when
its archive was taken, or `0` when there is none.

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

<<<<<<< HEAD
## Updating

### `GET /api/update`
### `POST /api/update`

`GET` asks GitHub what the newest release of `jiangmuran/vibepanel` is and
answers with the tag, whether it is ahead of what is running, the release page
and its notes. A panel with no route to GitHub answers `200` with
`{"current": "...", "unreachable": "..."}` rather than failing: an air-gapped
box is a normal state, not a broken one.

`POST` downloads that release's archive for this exact GOOS/GOARCH, checks it
against the `SHA256SUMS` published in the same release, unpacks the binary,
moves the running one aside to `<path>.old`, renames the new one into place, and
then asks systemd to restart the unit. It answers before restarting, with
`{"installed", "previous", "restarting", "restartWhy"}` — `restarting: false`
and a reason when the panel was started by hand and cannot bring itself back.

**The version is not a parameter.** A request cannot name what to install; the
panel installs the latest release or refuses with `409`. The interesting case
this closes is not a typo, it is somebody with a session cookie who would like
this panel to run something else.

What the checksum buys: it detects a corrupt or truncated download. It does not
defend against a compromised release, because the sums come from the same
release as the archive — the same trust anyone gets from `curl | tar`. What
makes it defensible is that the repository is compiled into the binary rather
than configurable, so an update cannot be aimed somewhere else by a setting.

Your sessions are not restarted with the panel. `KillMode=process` in both units
is what makes the button safe to press at all.
=======
## Read-only share links

A share link is a capability: a long random token in a URL that opens the
dashboard at `/share/<token>` on a second screen — machine load, per-session CPU
and memory, every session with its state, grouped by project — and reaches
nothing else at all.

### `GET /api/settings/shares`
### `POST /api/settings/shares`
### `DELETE /api/settings/shares/{shareID}`

Making one needs the ordinary session; a share token cannot mint another, which
is what stops one leaked link becoming a supply of them.

```sh
curl -sX POST https://panel.example:8443/api/settings/shares \
  -b cookies.txt -H 'Content-Type: application/json' \
  -d '{"name":"wall display","detail":"counts","expiresIn":604800}'
# {"token":"Jq4…","id":"…","prefix":"Jq4x9m2v","detail":"counts","expiresAt":1735689600}
```

The response is the only time the token is readable — the database keeps a
SHA-256 of it, exactly as it does for an API token — so the URL to paste is
`https://<panel>/share/<token>` and there is no way to ask for it again.

`detail` is `counts` (the default) or `names`; anything else is a `400`, because
the value decides what the link discloses for as long as it exists and a default
could only fall towards saying more or towards saying less. `expiresIn` is
seconds from now, `0` for a link that does not expire, and at most a year.

Creation and revocation are audited as `share.created` and `share.revoked`.
Revocation takes effect on the link's next poll; there is nothing else to
invalidate, because a share link has no session, no cookie and no socket.

### `GET /api/share/{token}/dashboard`

The entire surface a share token can reach. No credential beyond the token in
the path, and no other route accepts that token at all: presenting it as a
`Bearer` header or as the session cookie answers `401` everywhere, including on
`/ws`. That is enforced by where the route is registered rather than by a flag a
handler reads.

```json
{"at": 1735689600, "name": "wall display", "detail": "counts", "expiresAt": 0,
 "usageReadable": true, "stale": false,
 "machine": {"cpuReadable": true, "cpuPercent": 31.4, "cores": 16,
             "load1": 2.1, "load5": 1.8, "load15": 1.4,
             "memTotal": 33654304768, "memAvailable": 20401324032,
             "swapTotal": 0, "swapFree": 0,
             "diskTotal": 981472473088, "diskFree": 402653184000,
             "uptime": 918273},
 "counts": {"projects": 2, "sessions": 5, "waiting": 1, "working": 2,
            "done": 2, "exited": 0, "crashed": 0},
 "projects": [{"id": "3f9c1a…", "name": "", "waiting": 1, "working": 1,
               "done": 0, "total": 2}],
 "sessions": [{"id": "b7e20d…", "projectId": "3f9c1a…", "name": "",
               "state": "waiting", "kind": "agent", "stateChangedAt": 1735689000,
               "exited": false, "exitStatus": 0,
               "measured": true, "cpuPercent": 24.1, "rss": 831258624, "procs": 7}]}
```

What it deliberately does **not** carry, in either `detail` mode: the project's
path on disk, a session's `cwd`, the command line, the tmux session name, the
hostname, the sampler's disk path, and the panel's own session and project ids.
A path names a customer and a home directory; a command line carries whatever an
agent was invoked with. Neither has a use on a screen behind somebody's desk.

`id` and `projectId` are pseudonyms: an HMAC of the real id under the link's own
stored hash. They are stable for the life of one link, so a list does not re-key
itself on every poll, and different for every other link, so two dashboards on
two walls cannot be joined into one picture of the panel.

Under `detail: "counts"` the `name` fields are empty strings and the page
numbers the groups and rows instead. Under `detail: "names"` they carry the
session title and the project name — still no paths.

`kind` is `agent`, `shell` or `other`, which is what makes a wall readable
without quoting the command. `measured` is `false` when the sampler found no
process tree for that row; `cpuPercent` there is not a reading of zero, and zero
is a real reading. Scratch terminals opened under a session are left out
entirely: they are session rows with a parent, and listing them reports two rows
for one job.

`at` is when the server took the reading, and the dashboard counts up from it.
That is the field to use if you build your own display — a page that has
silently frozen looks exactly like a quiet system, and the numbers themselves
cannot tell you which you are looking at.

Answers are `Cache-Control: no-store`. `401` means the link was revoked, has
expired, or never existed — one answer for all three, and rejected attempts are
audited as `share.rejected`, gated to one row per source per minute. `403` is
the `--allow-from` allowlist, which applies here exactly as it does to the
panel: a share link must not be a way around it.
>>>>>>> worktree-agent-a300b55d058841cd8

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
