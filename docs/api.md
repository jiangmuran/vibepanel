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

## Token usage

Two different things are called "usage" and "tokens" in this API, so the names
here are deliberately long. `/api/usage` above is CPU and memory *right now*.
`/api/settings/tokens` is API credentials. This section is **token spend**, read
out of the coding agents' own transcripts.

### `GET /api/token-usage?days=&project=&tool=`

What the agents recorded spending, by day, by month, by agent session, by
project and by tool.

```json
{"scannedAt": 1787900000, "scanning": false, "passMs": 35, "passError": "",
 "sources": [{"tool": "claude", "root": "/home/me/.claude/projects", "found": true,
              "files": 430, "bytes": 1257242624, "skipped": 0},
             {"tool": "codex", "root": "/home/me/.codex/sessions", "found": false,
              "problem": "not found", "files": 0, "bytes": 0, "skipped": 0}],
 "today": "2026-08-27", "from": "2026-07-29", "to": "2026-08-27", "days": 30,
 "total": {"input": 91234, "output": 5954333, "cacheRead": 812004112,
           "cacheWrite": 44120983, "requests": 67339},
 "byDay":   [{"day": "2026-08-27", "input": 12, "output": 478564, "cacheRead": 0,
              "cacheWrite": 0, "requests": 1820}],
 "heatmap": [{"day": "2026-08-27", "…": "the same shape, always the last 371 days"}],
 "byMonth": [{"day": "2026-08", "…": "the same shape, every month there has been"}],
 "byTool":  [{"tool": "claude", "input": 0, "output": 0, "cacheRead": 0,
              "cacheWrite": 0, "requests": 0, "files": 430, "skipped": 0,
              "problems": 0, "problem": ""}],
 "projects": [{"id": "p_abc", "name": "vibepanel", "path": "/home/me/vibepanel",
               "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0,
               "requests": 0}],
 "sessions": [{"session": "1e85b81b-…", "tool": "claude", "cwd": "/home/me/vibepanel",
               "models": "claude-opus-5", "firstDay": "2026-08-23",
               "lastDay": "2026-08-27", "days": 5, "input": 0, "output": 0,
               "cacheRead": 0, "cacheWrite": 0, "requests": 0,
               "projectId": "p_abc", "projectName": "vibepanel"}],
 "sessionCount": 312, "sessionLimit": 200}
```

**These are the agents' numbers, not the panel's.** They are read from the files
Claude Code and Codex write for themselves — `~/.claude/projects/**/*.jsonl` and
`~/.codex/sessions/**/*.jsonl` — so a `claude` run in a terminal this panel
never started is counted, and a session the panel did start that ran something
with no transcript is not. Nothing is estimated; there is no token count derived
from character counts anywhere in this.

`session` is the **agent's** session id, out of its own transcript. It is not a
vibepanel session id and there is no mapping between them: neither agent
publishes the id of the transcript it is writing, so the honest unit is the
agent's own session. `cwd` is what ties a session to a project, matched by
directory containment — `/home/me/api-v2` is not inside `/home/me/api`.

`found: false` in `sources` means that agent contributed nothing **because
nothing could be read**, with `problem` saying why. That is not the same claim as
zero spend and must not be rendered as one. `skipped` counts records the reader
could not use, so a non-zero value makes every total below it a lower bound.

`scannedAt` is zero until the first pass over the transcripts has finished.
Until then there is no answer yet — which is also not zero. A `GET` starts a
pass in the background when the last one is more than 30 seconds old, and never
blocks on it.

Counts are normalised across the two agents: `input` is what was sent **fresh**,
with cache reads in `cacheRead`. Codex's own `input_tokens` includes its cached
part and is split here; Claude's does not and is not.

`days` is the range for `byDay`, `total`, `byTool`, `projects` and `sessions`,
clamped to 1–3660 and defaulting to 30. `heatmap` is always the last 371 days
(53 whole weeks) and `byMonth` is always every month — a range control should
not be able to make a year grid into a broken one. `project` is a project id,
never a path; `tool` is `claude` or `codex`. An unknown value of either is a
400 rather than an empty chart.

`sessions` is capped at `sessionLimit`, biggest first, with `sessionCount`
saying how many there were.

### `POST /api/token-usage/refresh`

Reads the transcripts again now. `202` with `{"started": true}`, or `started:
false` when a pass was already running. It does not wait: a first pass over a
year of history is seconds of disk, and the numbers arrive on the next `GET`.

Transcript **contents** are never served by either endpoint. The panel reads
counts and timestamps out of those files and nothing else leaves the machine.

## Projects

### `GET /api/projects/{id}/files?path=`
### `GET /api/projects/{id}/download?path=`
### `GET /api/projects/{id}/preview?path=`
### `GET /api/projects/{id}/preview/render?path=&scripts=`
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

A text response also carries `X-Preview-Markup: html` or `svg` when a *second*
endpoint would draw the file as a page. The bytes in that response are
unchanged by it — still an attachment, still `application/octet-stream`.

`preview/render` is that second endpoint, and it is a separate route rather than
a flag for the same reason a share token is narrowed by its route: exactly one
handler in the panel can produce an inline `text/html` response out of a project
directory, and it is this one. It serves `.html`, `.htm`, `.xhtml` and `.svg`
and answers `415` to everything else; the content type comes from a two-entry
whitelist and never from the file. Over 8 MiB is `413`.

What it sends with the bytes is the feature:

- `Content-Security-Policy: default-src 'none'; img-src data: blob:; media-src
  data: blob:; font-src data:; style-src 'unsafe-inline'; base-uri 'none';
  form-action 'none'; frame-ancestors 'self'; sandbox`. `default-src 'none'` is
  what keeps a preview from making an outbound request — a remote `<img>`, a
  webfont, a nested `<iframe>` — the moment somebody clicks a file. The
  `sandbox` directive gives the document an opaque origin, so it holds even when
  this URL is opened in a tab, where an `<iframe sandbox>` attribute would not
  apply.
- `Content-Type` from the whitelist, `X-Content-Type-Options: nosniff`,
  `Content-Disposition: inline`, `Cache-Control: no-store`.

`scripts=1` — and only exactly `1` — adds `script-src 'unsafe-inline'` and makes
the sandbox `sandbox allow-scripts`. `allow-same-origin` is never emitted, in
either the header or the attribute the panel sets: with it the document would be
on the origin holding the session cookie. The effective sandbox is the
intersection of the two, so the decision is the server's and editing the
attribute in a browser does not move it.

The residual is written out in `internal/httpapi/preview_render.go`: a preview
can still draw anything it likes, and a click on a link inside it can navigate
the frame to a remote page.

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

`command` is an argv. Omit it for a shell in the project directory.
`parentSessionId` makes the new session a scratch terminal under another one.

`launchProfileId` names a launch profile, and is what the panel's own picker
sends: the profile supplies both the argv and the environment. An explicit
`command` still wins over the profile's, so a caller that knows exactly what it
wants is not made to create a profile for it; the environment always comes from
the profile. An id that names nothing is a `400`, because a session created
against the default endpoint when a gateway profile was asked for is a
substitution nobody notices until the bill.

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

## Launch profiles

A launch profile is a name, an argv, and the environment to start it in. It
exists because the panel could be told what to run and nothing about what to run
it *with*, and the same agent pointed at Anthropic, at a company proxy and at a
self-hosted gateway is three configurations differing only in a base URL.

There is no "API host" field. Which variable carries the endpoint is the agent's
decision — `ANTHROPIC_BASE_URL` for claude, `OPENAI_BASE_URL` for codex, and for
opencode nothing at all, because its endpoint is chosen per provider in its own
configuration. A field would need that mapping to stay right for every release
of somebody else's tool, and the day it was wrong the panel would set a variable
nothing reads.

### `GET /api/launch-profiles`
### `POST /api/launch-profiles`
### `PATCH /api/launch-profiles/{profileID}`
### `DELETE /api/launch-profiles/{profileID}`

```sh
curl -sX POST .../api/launch-profiles -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{
    "name": "claude via gateway",
    "command": ["claude"],
    "env": [
      {"name": "ANTHROPIC_BASE_URL", "value": "https://gw.example/v1"},
      {"name": "ANTHROPIC_AUTH_TOKEN", "value": "sk-…", "secret": true}
    ]}'
# {"id":"…","name":"claude via gateway","builtin":false,"command":["claude"],
#  "env":[{"name":"ANTHROPIC_BASE_URL","value":"https://gw.example/v1","secret":false,"hasValue":true},
#         {"name":"ANTHROPIC_AUTH_TOKEN","value":"","secret":true,"hasValue":true}],
#  "createdAt":1735689600,"updatedAt":1735689600}
```

`GET` returns the built-in catalogue followed by your own profiles, and the
order is stable: built-ins in catalogue order, rows by name. The picker is the
most-used control in the panel and a list that reorders itself under somebody's
finger is worse than one that is slightly wrong.

**A `builtin` profile is a Go constant, not a row.** Its id starts `builtin:`,
its name is translated by the frontend, and `PATCH` or `DELETE` on it is a
`400`; duplicate it and edit the copy. Built-ins carry variable *names* with
empty values — the names each agent documents — so a duplicate arrives with the
form already filled in, and using a built-in directly sets nothing, because **an
empty value is not passed to the process**. That last rule is deliberate: `FOO=`
and "FOO unset" are different to a program, and the common mistake is a
half-filled form rather than somebody wanting an empty variable.

`PATCH` replaces the whole profile — name, command and variables together.
There is no partial edit, because the field somebody would omit is `env`, and
"leave it alone" is how a rename keeps a key the user thought they had removed.

**Secrets.** A variable with `"secret": true` is never sent back: every read
gives `"value": ""` and `"hasValue": true`. Sending a secret back with an empty
value keeps the stored one, which is what stops a rename wiping every key; any
other value replaces it. Matching is by name, so renaming a secret variable in
the same request that saves it clears the value. Nothing about this encrypts
anything — the value is plaintext in the panel's SQLite file, and the settings
page says so. It is not in the argv, so it is not in `ps`, and it is not in the
audit log, which records profile names only.

**What is refused, and what is not.** A variable name must match
`[A-Za-z_][A-Za-z0-9_]*` — tmux accepts an empty name, a name with no `=` and a
name containing a newline, and all three produce a session that looks configured
and is not. A value may not contain a line break. A name starting `VIBEPANEL_`
is refused outright: those are how a session's hooks find the panel and
authenticate to it, and a profile that could set them could point every state
report a session makes at an address of its own choosing. `LD_PRELOAD`, `PATH`
and everything like them are **accepted**, because refusing them would stop
nothing — anyone who can reach this endpoint can already start a session running
an arbitrary command — while implying a boundary that is not there.

A session records which profile it was started with, as `launchProfileId`. A
restore reads the profile again for the environment, so a session comes back
pointed at the same gateway; if the profile has been deleted since, the session
still restores, without those variables, and the id it keeps is what lets a
client say the profile is gone rather than imply the session still has it.

Creating, editing and removing are audited as `profile.created`,
`profile.updated` and `profile.deleted`.
## The repository

### `GET /api/projects/{id}/git`
### `POST /api/projects/{id}/git/github`

`GET .../git` reads the project's working tree. No network, no credential, no
configuration — it is the half that always works, and it is what the panel polls
while the tab is open:

```json
{"status": {"repo": true, "branch": "main", "detached": false, "head": "1234567",
            "upstream": "origin/main", "ahead": 3, "behind": 0,
            "staged": 1, "unstaged": 2, "untracked": 4, "conflicted": 0,
            "changes": [{"path": "src/a.go", "kind": "unstaged", "renamed": ""}],
            "changesTruncated": false},
 "commits": [{"sha": "...", "subject": "...", "author": "...", "when": 1756000000}],
 "remote": {"url": "...", "host": "github.com", "owner": "o", "name": "r"},
 "github": true, "tokenSet": false,
 "sessions": [], "sessionsTruncated": false}
```

`repo: false` is an answer, not a failure: a project directory that is not a
repository gets `200` and a panel that says so in a line.

The answer may be up to three seconds old. Reads of one working tree are cached
for that long and requests arriving during a read wait for it rather than
starting another, so several tabs on one project are one `git status` and not
several — see `internal/git/cache.go`. The window is shorter than the tab's own
five-second poll, so a single viewer never sees the same numbers twice.

`changes` is capped at 100 entries; the four counts above it are always exact.
`commits` is the last 15. `sessions` lists only the project's sessions sitting on
a *different* commit than the project root — worktrees, in practice — because
six sessions in one directory are six identical rows. A session's `cwd` is
resolved through the project root like every other path, so one that has `cd`'d
outside the project simply has no row.

`tokenSet` says the panel was started with `GITHUB_TOKEN` or `GH_TOKEN` in its
environment. The token itself is never sent, and there is nowhere to store one:
it is read from the environment at the moment of the request.

`POST .../git/github` is the only outbound request in the panel besides the
update check, and it is a `POST` for that reason rather than because it changes
anything — a `GET` is something a browser re-issues on its own. One press, one
GraphQL query to `api.github.com`, twenty open pull requests newest first:

```json
{"total": 3, "checkedAt": 1756000000,
 "prs": [{"number": 7, "title": "...", "branch": "feat/auth", "base": "main",
          "draft": false, "author": "someone", "url": "https://github.com/...",
          "updatedAt": 1756000000, "review": "changes_requested",
          "checks": "failure"}]}
```

`branch` is the head branch, and it is the field the panel joins to a session's
local branch — "is the branch this agent is on green" is the question the whole
network half exists for. `review` and `checks` are GitHub's own rollups,
lowercased; either can be empty, which means no review is required and no checks
ran, not that something failed.

`400` with no request made: no token, or a remote that is not on `github.com`.
`502` when GitHub answered and the answer was not usable — including a `200`
carrying a GraphQL `errors` array, which is what a repository the token cannot
see looks like.

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

## Notifications to somewhere else

### `GET /api/settings/webhooks`
### `PUT /api/settings/webhooks`
### `POST /api/settings/webhooks/test`

The browser notification needs the panel open in a tab or installed as an app,
which leaves out the case that matters: the laptop is shut. A webhook is an
outbound HTTP request the panel makes when a session changes state.

One mechanism, not a list of providers. `{"method","url","headers","body"}`
with `{state}`, `{session}`, `{project}`, `{url}` and `{time}` substituted —
which is Bark, ntfy, Gotify, ServerChan, PushPlus, Slack, Discord and a shell
script behind a reverse proxy, without a case per service.

Two escapes, chosen by where the placeholder is. In a URL a session called
`fix a&b` arrives percent-encoded, or everything after the ampersand becomes a
different query parameter. In a body it arrives JSON-escaped, or a title with a
quote in it produces a body the destination rejects — and agent titles contain
quotes constantly.

`states` is which transitions fire it; empty means `waiting` only, which is the
one worth waking somebody for. Firing is on the *transition*, so a session that
sits waiting does not send one every two seconds.

`PUT` replaces the whole list, assigns ids to new rows and answers with what was
stored. `test` sends one immediately using the webhook **in the request body**
rather than a stored one — the moment to test is before saving — and answers
`{"ok", "said", "error"}` where `said` is what the destination replied.

## VNC displays

**These routes exist only when the panel was started with `--vnc`.** Without
it they are absent rather than guarded — every one answers `404`, and the
settings section that posts to them is not rendered. This is the one place the
panel opens an outbound TCP connection on a browser's say-so, so a deployment
that has not asked for it does not have one.


### `GET /api/vnc/targets`
### `POST /api/vnc/targets`
### `PATCH /api/vnc/targets/{targetID}`
### `DELETE /api/vnc/targets/{targetID}`
### `GET /api/vnc/targets/{targetID}/socket`

A display is `{"id","name","host","port","viewOnly","hasPassword","createdAt"}`.
`POST` and `PATCH` take `{"name","host","port","viewOnly","password"}`; the
password is write-only and never comes back, so `hasPassword` is how you tell
whether there is one. On `PATCH`, omitting `password` leaves it alone and
sending `""` clears it — a plain string could not say the first, and every
rename would have wiped it.

**The row is the only place an address comes from.** There is no endpoint that
takes a host and connects to it: the socket route takes an id, reads the row and
dials what the row says. On top of that a process-level policy — `--vnc-allow`,
defaulting to loopback only — decides which addresses this panel may reach at
all. It is checked when a row is written (so a target that could never work is a
`400` at the moment you type it, not a socket that fails forever) and again on
every connect, against **every** address the host resolves to.

`socket` is a WebSocket carrying RFB as binary frames. The panel performs the
RFB handshake on both sides: it authenticates to the display with the stored
password and then presents RFB 3.8 with security type `None` to the browser, so
the password never leaves the server and the browser never sees a challenge.
Everything after `ClientInit` is copied unmodified — the encodings are
negotiated between the two ends and the panel decodes nothing.

A `viewOnly` display is enforced here rather than in the client: key, pointer,
clipboard, resize and power messages are dropped at the proxy. A client message
whose length the proxy cannot work out closes the connection rather than being
skipped past.

Close codes carry the reason as text: `1008` for an address the policy refuses,
`1014` for a display that could not be reached or would not negotiate, `1000`
for an ordinary end.

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

## Read-only share links

A share link is a capability: a long random token in a URL that opens a
dashboard at `/share/<token>` on a second screen, and reaches nothing else at
all.

What that dashboard *shows* is a **board** — an arrangement chosen when the link
is made, stored with it, and sent back with every reading. A board is data, not
code: an ordered list of widgets, each naming a kind from a fixed registry with
options that are enums or bounded numbers. There is no widget that names a
table, a field, a directory, a URL or a template, which is what keeps a stored
row from being able to make the panel do anything.

Three decisions are made when a link is created, and two of them are permanent:

| | | |
|---|---|---|
| `board` | what it shows | changeable afterwards |
| `detail` | whether it may use words | fixed at creation |
| `scope` | which rows it is about | fixed at creation |

The board can be rearranged later because rearranging it cannot disclose
anything the link did not already carry. The other two can, and by then the URL
is in an email or typed into a television — so a different mode or a different
scope means a different link, which somebody has to hand out on purpose.

### `GET /api/settings/shares`
### `POST /api/settings/shares`
### `PATCH /api/settings/shares/{shareID}`
### `DELETE /api/settings/shares/{shareID}`

Making one needs the ordinary session; a share token cannot mint another, which
is what stops one leaked link becoming a supply of them.

```sh
curl -sX POST https://panel.example:8443/api/settings/shares \
  -b cookies.txt -H 'Content-Type: application/json' \
  -d '{"name":"wall display","detail":"counts","expiresIn":604800,"preset":"attention"}'
# {"token":"Jq4…","id":"…","prefix":"Jq4x9m2v","detail":"counts","scope":"",
#  "board":{"preset":"attention","rotate":0,"widgets":[…]},"expiresAt":1735689600}
```

The response is the only time the token is readable — the database keeps a
SHA-256 of it, exactly as it does for an API token — so the URL to paste is
`https://<panel>/share/<token>` and there is no way to ask for it again.

`detail` is `counts` (the default) or `names`; anything else is a `400`, because
the value decides what the link discloses for as long as it exists and a default
could only fall towards saying more or towards saying less. `expiresIn` is
seconds from now, `0` for a link that does not expire, and at most a year.

`preset` names a starting arrangement and `board` is an explicit one; `board`
wins, and a request with neither gets the default board. An unknown preset or an
unknown widget kind is a `400` rather than a fallback — see the catalogue below.

`scope` is `""` (the whole panel, the default), `project` or `session`, with
`scopeId` naming which. It is checked against the rows that exist: a scope
naming a project nobody has heard of is a `400` rather than a link that shows an
empty screen forever with no way to tell a typo from a deletion. A scoped link
sees only its own project's or session's rows, only that scope's token spend and
only that scope's checklists — enforced by the handler from the stored row, not
from anything in the request. If the project or session is later deleted, the
link shows **nothing**; it does not fall back to the whole panel.

`PATCH` takes `{"name": "...", "board": {...}}` and nothing else. Sending
`detail` or `scope` is a `400`, because unknown fields are refused: an edit that
quietly did less than it asked for is worse than one that says no.

Creation, editing and revocation are audited as `share.created`,
`share.updated` and `share.revoked`. Revocation takes effect on the link's next
poll; there is nothing else to invalidate, because a share link has no session,
no cookie and no socket.

### `GET /api/settings/shares/catalogue`

The vocabulary a board can be built from: every preset with the widgets it
expands to, every widget kind with the options it accepts, and the bounds.
Served rather than mirrored in the frontend, so that every option an editor
offers is an option the validator accepts.

```json
{"presets": [{"id": "attention", "audience": "working", "rotate": 0,
              "widgets": [{"kind": "attention", "span": 4}, …]}, …],
 "widgets": [{"kind": "spendsplit", "span": 2, "metrics": null, "filters": null,
              "orders": null, "groups": null, "bys": ["tool", "project", "model"],
              "days": false, "text": false, "rotate": false}, …],
 "maxWidgets": 24, "maxSpan": 4, "maxCaption": 64, "maxDays": 371}
```

A widget is `{"kind", "span", "metric"?, "filter"?, "order"?, "group"?, "by"?,
"days"?, "page"?, "rotate"?, "text"?}`. `span` is 1–4 columns in a grid that
collapses to two and then to one as the screen narrows, so one stored board is a
summary on a phone and a wall of tiles on a television. `page` puts a widget on
one page of a rotating board and the board's own `rotate` is how many seconds
each page stays; a widget's `rotate` pages through a list longer than its tile.
A field a kind does not accept is a `400` rather than an ignored value.

This is a settings route: a share token answers `401` to it, like everything
else that is not the one dashboard `GET`.

### `GET /api/share/{token}/dashboard`

The entire surface a share token can reach. No credential beyond the token in
the path, and no other route accepts that token at all: presenting it as a
`Bearer` header or as the session cookie answers `401` everywhere, including on
`/ws`. That is enforced by where the route is registered rather than by a flag a
handler reads.

```json
{"at": 1735689600, "name": "wall display", "detail": "counts", "expiresAt": 0,
 "usageReadable": true, "stale": false, "scope": "", "scopeName": "",
 "board": {"preset": "attention", "rotate": 0,
           "widgets": [{"kind": "attention", "span": 4}]},
 "machine": {"cpuReadable": true, "cpuPercent": 31.4, "cores": 16,
             "load1": 2.1, "load5": 1.8, "load15": 1.4,
             "memTotal": 33654304768, "memAvailable": 20401324032,
             "swapTotal": 0, "swapFree": 0,
             "diskTotal": 981472473088, "diskFree": 402653184000,
             "uptime": 918273},
 "counts": {"projects": 2, "sessions": 5, "waiting": 1, "working": 2,
            "done": 2, "exited": 0, "crashed": 0, "doneToday": 3,
            "longestWaitAt": 1735689000},
 "projects": [{"id": "3f9c1a…", "name": "", "waiting": 1, "working": 1,
               "done": 0, "total": 2}],
 "sessions": [{"id": "b7e20d…", "projectId": "3f9c1a…", "name": "",
               "state": "waiting", "kind": "agent", "stateChangedAt": 1735689000,
               "exited": false, "exitStatus": 0,
               "measured": true, "cpuPercent": 24.1, "rss": 831258624, "procs": 7}],
 "spend": null, "todos": null}
```

`sessions` is empty, and `spend` and `todos` are `null`, unless a widget on the
board asks for them. A board can only ever subtract: the sections a dashboard
may carry are a fixed set, a widget chooses among them, and no arrangement of
widgets produces a field that is not in the list. `null` and a zeroed object are
different facts — the first is "this board does not show it", and the second has
a `readable` flag of its own to tell "nothing was spent" from "nothing has been
counted yet".

`spend` is tokens, never money: prices differ per model, per tier and over time,
and a currency figure from a stale table is a confident wrong number on a wall.
It carries `today`, `yesterday`, `month`, `lastMonth` and `window` totals (each
split into input, output, cache read, cache write, requests and a summed
`total`), `hoursToday` so a per-hour rate is "so far today" on the *server's*
clock, and the arrays a board asked for: `days`, `months`, `heatmap`, `tools`,
`models`, `projects`. Its `date` is the server's local day, because the buckets
are local days and a phone abroad must not decide which square is today.

`todos` is counts only — `open`, `done`, `closedToday`, and the same per project.
The items themselves are never sent, at either `detail`. A todo line says what
somebody is about to do about a customer, a bug or a date; it is closer to a
note than to a session title.

`counts.doneToday` is how many sessions reached `done` since the server's local
midnight. It is the closest thing to *output* this panel honestly has: nothing
here counts lines of code, because the panel never reads a repository and a
number invented for a wall is worse than a missing one.

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

## Attaching a harness

The supported way to have a program manage every session is three things you
already have, and it is written down here because they were three unrelated
features until somebody asked for a plugin system.

1. **An API token** — Settings → API tokens — as `Authorization: Bearer …`.
   Everything the panel's own frontend does is available with it.
2. **`GET /ws`**, with the same credential, for the push. A `state` message
   carries the whole panel within 60 ms of anything changing: a session
   becoming *waiting*, a process exiting, a title moving. That is a coalesce
   window, not a poll interval, so there is nothing to tune and nothing to
   miss between polls.
3. **A webhook**, if the harness would rather be woken than stay connected.
   `PUT /api/settings/webhooks` points a state transition at any URL, method,
   headers and body of yours. It is fire-and-forget by design — it runs in a
   goroutine the poller never waits for — so a harness that wants to *act*
   holds a token from (1) and calls back.

That composition is the plugin system. There is no in-process one, and
[docs/plugins.md](plugins.md) is the argument for why: every capability such a
runtime would grant is one this token already has, and the parts it could add
that the token cannot — code on the panel's own origin, a synchronous veto in
the poller's path — are the two that must not exist.

## What is not here

There is no way to attach to a session's terminal over plain HTTP — that is the
WebSocket's job, and a polling shim would be a worse version of it. There is no
API for the setup token; it is printed to the panel's own log on first run and
consumed once.
