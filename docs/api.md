# The HTTP API

Everything the panel's own frontend does, an agent can do. This is that
interface, written down so it can be depended on.

`docs/api.md` is checked against the router by
`TestTheAPIDocCoversEveryRoute`: an endpoint that exists and is not on this
page fails the build, and so does a line here for a route that has been
removed.

## Getting in

A browser signs in with a password and keeps a cookie. A program should not have
to: the cookie expires, it is `SameSite=Strict`, and obtaining one means posting
the password and keeping a jar. Use an API token instead.

Make one in **Settings → API tokens**, or:

```sh
curl -sX POST https://panel.example:18443/api/settings/tokens \
  -b cookies.txt -H 'Content-Type: application/json' \
  -d '{"name":"deploy bot"}'
# {"token":"pOsC…","id":"…","prefix":"pOsC7x2p","name":"deploy bot"}
```

The response is the only time the token is readable. The database keeps a
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
leaves tokens alone, and revoking a token leaves your password alone.

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

Open, no credential needed. What a monitor should watch.

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
`/proc/stat` at all. A machine that cannot be measured says so rather than
reporting zero.

### `GET /api/usage`

What each session's process tree is costing right now, keyed by session id:

```json
{"readable": true, "cores": 16,
 "sessions": {"s_abc": {"cpuPercent": 24.1, "rss": 831258624, "procs": 7}}}
```

`cpuPercent` is a share of the **whole machine**, the same denominator
`/api/system` uses, not top's, where 100% means one core. Both numbers appear
within an inch of each other in the UI, and a session reading "310%" beside a
machine reading "31%" invites exactly one wrong conclusion. `cores` is there to
convert if you want the other convention.

`rss` sums the resident set across the tree and so double-counts pages shared
with forked children; it is an over-estimate, like every tree total. `procs` is
how many processes were found, which is what says whether the reading means
anything: 1 is a bare shell.

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
directory containment: `/home/me/api-v2` is not inside `/home/me/api`.

`found: false` in `sources` means that agent contributed nothing **because
nothing could be read**, with `problem` saying why. That is not the same claim as
zero spend and must not be rendered as one. `skipped` counts records the reader
could not use, so a non-zero value makes every total below it a lower bound.

`scannedAt` is zero until the first pass over the transcripts has finished.
Until then there is no answer yet, which is also not zero. A `GET` starts a
pass in the background when the last one is more than 30 seconds old, and never
blocks on it.

Counts are normalised across the three agents: `input` is what was sent
**fresh**, with cache reads in `cacheRead`. Codex's own `input_tokens` includes
its cached part and is split here; Claude's and opencode's do not and are not.
opencode reports reasoning tokens separately and they are folded into `output`.

`days` is the range for `byDay`, `total`, `byTool`, `projects` and `sessions`,
clamped to 1–3660 and defaulting to 30. `heatmap` is always the last 371 days
(53 whole weeks) and `byMonth` is always every month. A range control should
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
and `pinned`. `DELETE` kills every session in the project and then removes it.
It does not touch the directory.

`reorder` takes `{"ids": [...]}` in the order you want and switches the panel to
manual ordering.

Paths in `files`, `download`, `preview` and `upload` are relative to the project
root and are resolved through it: a path that leaves the project is refused,
symlinks included.

`preview` is `download` with a ceiling and an opinion. It answers with the
bytes, and says what it decided they are in `X-Preview-Kind`: `text`, `image` or
`pdf`. For an image or a PDF, `X-Preview-Type` carries the media type it
matched, from a short whitelist. That header is what the caller should build a
`Blob` from, because the response itself is still `application/octet-stream`
with `nosniff` and an `attachment` disposition. Nothing a project contains is
ever offered to a browser as something to render on the panel's origin.

The kind comes from the leading bytes, never from the extension: a `Makefile` is
text and a `notes.txt` holding a tarball is not. SVG is deliberately read as
text rather than drawn: it is a document with scripting in it.

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
unchanged by it: still an attachment, still `application/octet-stream`.

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
`scratch` puts the new session in the project's terminal strip instead of the
sidebar; it belongs to the project, so the strip does not change with the
selected session. `nearSessionId` starts it in that session's current
directory rather than at the project root, which is a separate question --
either can be given without the other.

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
per id even when some failed. After a reboot the ordinary failure is a single
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
session when the panel shuts down, so an orderly reboot loses nothing and a
power cut loses at most half a minute. `scrollbackAt` on a session row is when
its archive was taken, or `0` when there is none.

`DELETE` kills the tmux session and its scratch terminals, then removes the row.

## Launch profiles

A launch profile is a name, an argv, and the environment to start it in. It
exists because the panel could be told what to run and nothing about what to run
it *with*, and the same agent pointed at Anthropic, at a company proxy and at a
self-hosted gateway is three configurations differing only in a base URL.

There is no "API host" field. Which variable carries the endpoint is the agent's
decision: `ANTHROPIC_BASE_URL` for claude, `OPENAI_BASE_URL` for codex, and for
opencode nothing at all, because its endpoint is chosen per provider in its own
configuration. A field would need that mapping to stay right for every release
of somebody else's tool, and the day it was wrong the panel would set a variable
nothing reads.

### `GET /api/launch-profiles`
### `POST /api/launch-profiles`
### `PATCH /api/launch-profiles/{profileID}`
### `DELETE /api/launch-profiles/{profileID}`
### `POST /api/launch-profiles/reorder`
### `POST /api/launch-profiles/restore`

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

`PATCH` replaces the whole profile: name, command and variables together.
There is no partial edit, because the field somebody would omit is `env`, and
"leave it alone" is how a rename keeps a key the user thought they had removed.

**Secrets.** A variable with `"secret": true` is never sent back: every read
gives `"value": ""` and `"hasValue": true`. Sending a secret back with an empty
value keeps the stored one, which is what stops a rename wiping every key; any
other value replaces it. Matching is by name, so renaming a secret variable in
the same request that saves it clears the value. Nothing about this encrypts
anything. The value is plaintext in the panel's SQLite file, and the settings
page says so. It is not in the argv, so it is not in `ps`, and it is not in the
audit log, which records profile names only.

**What is refused, and what is not.** A variable name must match
`[A-Za-z_][A-Za-z0-9_]*`: tmux accepts an empty name, a name with no `=` and a
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
configuration. It is the half that always works, and it is what the panel polls
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
several (see `internal/git/cache.go`). The window is shorter than the tab's own
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
anything: a `GET` is something a browser re-issues on its own. One press, one
GraphQL query to `api.github.com`, twenty open pull requests newest first:

```json
{"total": 3, "checkedAt": 1756000000,
 "prs": [{"number": 7, "title": "...", "branch": "feat/auth", "base": "main",
          "draft": false, "author": "someone", "url": "https://github.com/...",
          "updatedAt": 1756000000, "review": "changes_requested",
          "checks": "failure"}]}
```

`branch` is the head branch, and it is the field the panel joins to a session's
local branch. Whether the branch an agent is on is green is the question the
whole network half exists for. `review` and `checks` are GitHub's own rollups,
lowercased; either can be empty, which means no review is required and no checks
ran, not that something failed.

`400` with no request made: no token, or a remote that is not on `github.com`.
`502` when GitHub answered and the answer was not usable, including a `200`
carrying a GraphQL `errors` array, which is what a repository the token cannot
see looks like.

## Notes and todos

### `GET /api/projects/{id}/notes`
### `PUT /api/projects/{id}/notes`
### `POST /api/projects/{id}/mkdir`
### `POST /api/clipboard`
### `PUT /api/settings/paste`
### `PUT /api/settings/timezone`
### `GET /api/notes`
### `PUT /api/notes`
### `GET /api/projects/{id}/todos`
### `POST /api/projects/{id}/todos`
### `PATCH /api/todos/{todoID}`
### `DELETE /api/todos/{todoID}`

`POST /api/clipboard` takes `{"text": "..."}` and puts it in the tmux paste
buffer for this socket, without pasting it anywhere. That is the panel filling
the clipboard so a pane can take it when it wants to -- `prefix ]` in tmux, or
an agent that reads the buffer. Typing text into a pane is a different thing
and goes through the websocket.

`PUT /api/settings/paste` takes `{"dir": "panel"|"session", "then":
"type"|"buffer"|"both"}` and decides where a screenshot pasted into a terminal
lands, and what happens to its path. `dir` defaults to `panel`, which is a
directory the panel owns: a picture pasted at an agent used to land in the
session's working directory, which for an agent session is a git repository.
`GET /api/settings` carries both as `pasteDir` and `pasteThen`.

`PUT /api/settings/timezone` takes `{"zone": "Asia/Shanghai"}` -- an IANA name,
or `""` for the machine's own zone -- and decides where the day starts for
every per-day number in the panel. An unknown name is a 400 rather than a
stored value that quietly does nothing.

It is not only a label. A usage record's day is decided when the transcript is
read and written into `usage_daily.day` as a string, and every query afterwards
is a string comparison, so a new zone cannot re-label what is already there.
Changing it therefore drops the scan cursor and the next pass re-reads every
transcript; the response says how many files that was as `rebuilt`, and
`rebuilt: 0` means the zone was already what you asked for. `GET /api/settings`
carries the setting back as `timezone`, with `timezoneOffset` in minutes and
`panelDay` -- the label the usage tables are currently keyed by.

The offset is there because a page that wants to show the panel's day rather
than the viewer's needs one, and the offset is all it needs: an IANA name is a
location fact about the owner, which is the same reason the share surface
refuses the hostname.

`POST /api/projects/{id}/mkdir` takes `{"path": "sub/dir", "name": "new"}` and
makes one directory inside the project. Same helper and same refusals as the
directory picker's `/api/browse/mkdir`; the only difference is which root the
name is resolved against.

`/api/notes` with no project is the one note that belongs to none of them --
the same body, the same revision rule, the same `409`. It has its own pair of
routes rather than a reserved id under `/projects/`, because `{id}` is looked
up in the projects table and a handler that special-cases one value of a path
parameter grows a second special case.

`PUT` on notes takes `{"content": "...", "baseRev": N}` and answers `409` with
the current note if `baseRev` is not the revision on disk. Omit `baseRev`
entirely for an unconditional write, which is what a script that is not merging
anything wants.

Note the asymmetry, because it is the thing to get wrong: you **read** `rev` and
you **send it back as** `baseRev`. They are different names for the same number
because they are different claims: one is "this is the revision", the other is
"this is the revision I was looking at". The server rejects an unknown field, so
sending `rev` gets a `400` naming it rather than an unconditional write.

Two people editing one note in the same second is why it is a counter and not a
timestamp.

## Browsing the filesystem

### `GET /api/browse?path=`
### `POST /api/browse/mkdir`

Directories only, rooted at the home directory, for choosing where a project
should live. `mkdir` takes `{"path": "...", "name": "..."}` where `name` is one
element, not a path.

## Settings

### `GET /api/settings`
### `GET /api/settings/audit`
### `GET /api/settings/hooks`
### `POST /api/settings/hooks`
### `DELETE /api/settings/hooks`
### `POST /api/settings/restart`
### `POST /api/settings/tour`
### `GET /api/settings/env`
### `PUT /api/settings/env`
### `GET /api/settings/tune`
### `POST /api/settings/tune`
### `GET /api/settings/tokens`
### `POST /api/settings/tokens`
### `DELETE /api/settings/tokens/{tokenID}`

`GET /api/settings/env` reads the service's environment file -- the editable
keys only, so a response never carries `CLOUDFLARE_API_TOKEN`. It also returns
`live`, which is what *this process* is running with: a file edited an hour ago
and never applied looks identical to one that is in force, and that difference
is the reason there is a restart button next to it. `PUT` writes the same keys
and refuses any other, copying the file beside itself first and leaving every
comment, blank line and commented-out example exactly where it was. Emptying a
value comments the assignment out rather than deleting the line.

`VIBEPANEL_TMUX_SOCKET` is reported and not editable. Red line 1: a panel
pointed at another socket cannot see its own sessions, and the ones it was
managing keep running with nothing attached to them.

`POST /api/settings/tour` puts the first-run tour away. `GET /api/settings`
carries `tourDone` -- on that payload rather than a route of its own, because
the frontend fetches it before drawing anything and a tour that arrives one
request later appears over a panel somebody has started reading.

`POST /api/settings/restart` stops the panel and lets whatever supervises it
start a new one. It answers `202` before going anywhere, so the browser knows to
start polling; the sessions are untouched, because tmux owns them and the panel
is only a client. On a machine where nothing would restart it -- someone running
the binary from a terminal -- it answers `409` with `{"reason":"unsupervised"}`
rather than stopping.

`GET /api/settings/tune` reports the settings the panel offers to change in
`~/.claude/settings.json` beyond hooks: what leaves the machine, and what the
agent writes into your git history. Each row carries the key, what it does in
both languages, the value on disk and the value that would be written. `POST`
writes them, copying the file beside itself first and touching nothing else in
it. Both are the same list `vibepanel tune claude` prints.

`POST /api/settings/hooks` merges the state-reporting hooks into the agent's own
configuration file, backing it up first and tagging every entry so removing them
later cannot take anybody else's with it. `GET` shows what it would write before
you agree to it.

`POST` and `DELETE` take `?agent=claude` (the default, and what the parameter-less
request has always meant) or `?agent=codex`. Anything else is a `400`: the value
decides which file in the user's home directory gets edited, so an unrecognised
one has to be refused rather than resolved to whichever agent is first in the
code. Claude's four events are merged into `~/.claude/settings.json`; Codex's one
`notify` line goes into `~/.codex/config.toml`, above the first table: a
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
with `{state}`, `{session}`, `{project}`, `{url}` and `{time}` substituted.
That covers Bark, ntfy, Gotify, ServerChan, PushPlus, Slack, Discord and a shell
script behind a reverse proxy, without a case per service.

Two escapes, chosen by where the placeholder is. In a URL a session called
`fix a&b` arrives percent-encoded, or everything after the ampersand becomes a
different query parameter. In a body it arrives JSON-escaped, or a title with a
quote in it produces a body the destination rejects, and agent titles contain
quotes constantly.

`states` is which transitions fire it; empty means `waiting` only, which is the
one worth waking somebody for. Firing is on the *transition*, so a session that
sits waiting does not send one every two seconds.

`PUT` replaces the whole list, assigns ids to new rows and answers with what was
stored. `test` sends one immediately using the webhook **in the request body**
rather than a stored one — the moment to test is before saving — and answers
`{"ok", "said", "error"}` where `said` is what the destination replied.

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
`{"installed", "previous", "restarting", "restartWhy"}`. `restarting` is
`false`, with a reason, when the panel was started by hand and cannot bring
itself back.

**The version is not a parameter.** A request cannot name what to install; the
panel installs the latest release or refuses with `409`. The interesting case
this closes is not a typo, it is somebody with a session cookie who would like
this panel to run something else.

What the checksum buys: it detects a corrupt or truncated download. It does not
defend against a compromised release, because the sums come from the same
release as the archive, the same trust anyone gets from `curl | tar`. What
makes it defensible is that the repository is compiled into the binary rather
than configurable, so an update cannot be aimed somewhere else by a setting.

Your sessions are not restarted with the panel. `KillMode=process` in both units
is what makes the button safe to press at all.

## Read-only share links

A share link is a capability: a long random token in a URL that opens a **share
page** at `/share/<token>` on a second screen, and reaches nothing else at all.

What the link *shows* is a page: HTML the owner published, drawn from a
versioned, redacted snapshot (see [Share pages](#share-pages)). A link always
draws a page. The boards earlier builds drew are gone; the links they handed out
were converted at startup into links that draw a page built from the closest
built-in template, at the same address, with the same detail and scope, and the
conversion is audited as `share.converted`.

Five things are decided when a link is created, and two of them are permanent:

| | | |
|---|---|---|
| `pageId`, `pinVersion`, `params` | what it draws | changeable afterwards |
| `remark` | a label the owner writes for whoever is looking | changeable afterwards |
| `locked` | what it draws is fixed | changeable afterwards |
| `detail` | whether it may use words | fixed at creation |
| `scope` | which rows it is about | fixed at creation |

The first three can be changed later because none of them can disclose anything
the link did not already carry: every page reads the same redaction, narrowed by
the same `detail` and `scope`. The other two can, and by then the URL is in an
email or typed into a television, so a different mode or a different scope
means a different link, which somebody has to hand out on purpose.

### Changing a screen you are not standing in front of

The case this is built for is a television on a wall. Nobody is at it, and
walking to it to sign in is the thing that must not be necessary. So a link is
changed from `PATCH /api/settings/shares/{shareID}` and
`PUT /api/settings/shares/{shareID}/page` — ordinary authenticated routes, from
a laptop — and every open viewer picks the change up on its **next poll**, about
two seconds later, because every poll re-reads the link's row. A page that is
republished, or a link pointed at another page, reloads itself.

There is no push, no socket and no write route under the share token. A share
viewer is not authenticated, and a socket authorised once and held open for a
week would need the revalidation machinery `/ws` has; revocation takes effect
on the next poll precisely because there is nothing else to invalidate.

`remark` is a short label — the room a screen is in, the audience it is for —
cut to 80 runes. It is disclosed **under both detail modes**, deliberately.
`detail` governs whether the *panel's* words may leave the machine: session
titles and project names, read out of its own database. A remark is not the
panel's; it is a sentence the owner wrote to the person in front of the screen,
with the effect visible to them. `name` has always been sent in both modes for
the same reason.

`locked` fixes what a link draws. It is enforced on the server: a `PATCH` to a
locked link answers `409` unless it is the one that unlocks it, an unlocking
`PATCH` changes nothing else, and pointing it at a page, changing its
parameters or starting a trial on it answer `409` too. What it guards against
is not an attacker; it is a screen a customer is sitting in front of being
changed from a settings page left open on the wrong row.

`viewers` on each listed link is how many screens had it open a moment ago,
counted from the polls they were already making. It is not a column: a wall
polls every two seconds forever, and a stored count would be that many writes
for a number that is true for two seconds. A viewer that is unplugged needs no
cleanup: its entry simply stops being refreshed and ages out within fifteen
seconds. Viewers are **not** told the count; it is a fact about other people
holding the same URL.

## Directory previews

A directory served as a page, so that something an agent just built can be
looked at without a download and a local server. The link renders `index.html`
if there is one, lists the directory if there is not, and resolves relative
assets — which is the whole reason it is a directory and not a file.

```sh
curl -sX POST https://panel.example:18443/api/settings/previews \
  -b cookies.txt -H 'Content-Type: application/json' \
  -d '{"root":"/home/me/projects/site/dist","name":"site","expiresIn":86400}'
# {"id":"…","token":"Xk9…","prefix":"Xk9x2m4v","name":"site",
#  "root":"/home/me/projects/site/dist","createdAt":…,"expiresAt":…}
```

The URL to open is `https://<panel>/preview/<token>/`, and the response is the
only time the token is readable — the database keeps a SHA-256, exactly as it
does for a share link.

**The token is the authority, not your session.** That is not the original
design and it is not a shortcut: the preview is served with `Content-Security-
Policy: sandbox`, which puts the page in an opaque origin so it cannot read the
session cookie or call this API with it — and an opaque origin also cannot
*send* the cookie, so a preview that required a session could not load its own
stylesheet. Measured, in a browser: the page rendered and every asset came back
`ERR_BLOCKED_BY_ORB`. Creating a link needs a signed-in session; using one needs
the link.

So treat a preview URL as you would a share link: it is readable by whoever
holds it until it expires or is revoked. It is confined to one directory —
symlinks out are resolved and refused — it cannot reach any other route, and it
is served `no-referrer` and `noindex` so it does not leak on its own.

`expiresIn` is seconds from now, `0` for a link that does not expire, at most a
year. `root` must be a directory that exists; it is resolved once, at creation,
and stored resolved, so a symlink swapped afterwards cannot change what the link
serves.

### `GET /api/settings/previews`
### `POST /api/settings/previews`
### `DELETE /api/settings/previews/{id}`

List, create and revoke. The listing includes expired links, because "this link
has expired" is what the page showing them has to be able to say, and never
includes a token.

### `GET /api/settings/shares`
### `POST /api/settings/shares`
### `PATCH /api/settings/shares/{shareID}`
### `DELETE /api/settings/shares/{shareID}`

Making one needs the ordinary session; a share token cannot mint another, which
is what stops one leaked link becoming a supply of them.

```sh
curl -sX POST https://panel.example:18443/api/settings/shares \
  -b cookies.txt -H 'Content-Type: application/json' \
  -d '{"name":"wall display","detail":"counts","expiresIn":604800,"pageId":"3f9c…"}'
# {"token":"Jq4…","url":"https://panel.example:18443/share/Jq4…/","id":"…",
#  "prefix":"Jq4x9m2v","detail":"counts","scope":"","pageId":"3f9c…","params":{},
#  "interactive":false,"copyable":true,"expiresAt":1735689600}
```

A link is an address any number of screens may open, as often as they like. The
database keeps a SHA-256 of the token, which requests are looked up by, and the
token sealed with AES-256-GCM under `<data dir>/secrets.key` (mode `0600`, not in
the database), so the address can be asked for again with
`GET /api/settings/shares/{shareID}/url` while a copy of the database alone opens
nothing. A listed link carries `copyable`, `false` for a link made before tokens
were sealed; `interactive`; and `actionsToday`, the visitor actions run through it
since local midnight.

`pageId` is required and names a published page; `params` are that page's
parameter values for this link, checked against its manifest. A request without
a page is a `400`: a link has nothing else to draw.

`detail` is `counts` (the default) or `names`; anything else is a `400`, because
the value decides what the link discloses for as long as it exists and a default
could only fall towards saying more or towards saying less. `expiresIn` is
seconds from now, `0` for a link that does not expire, and at most a year.

`scope` is `""` (the whole panel, the default), `project` or `session`, with
`scopeId` naming which. It is checked against the rows that exist: a scope
naming a project nobody has heard of is a `400` rather than a link that shows an
empty screen forever with no way to tell a typo from a deletion. A scoped link
sees only its own project's or session's rows, only that scope's token spend and
only that scope's checklists — enforced by the handler from the stored row, not
from anything in the request. If the project or session is later deleted, the
link shows **nothing**; it does not fall back to the whole panel.

`interactive` (default `false`) lets visitors run the page's declared visitor
actions through the link; see docs/page-backend.md §6.

`PATCH` takes `{"name": "...", "remark": "...", "locked": false, "interactive":
false}` and nothing else; `interactive` may be left out to keep it as it is. Sending `detail` or `scope` is a `400`, because unknown fields are
refused: an edit that quietly did less than it asked for is worse than one that
says no. An empty `name` keeps the one the link had. On a **locked** link the
only accepted request is `{"locked": false}`; anything else is a `409`, and the
unlocking request applies nothing but the unlock.

Creation, editing, locking and revocation are audited as `share.created`,
`share.updated`, `share.locked`, `share.unlocked` and `share.revoked`; a change
to `interactive` as `share.interactive_changed`.
Revocation takes effect on the link's next poll; there is nothing else to
invalidate, because a share link has no session, no cookie and no socket.

### `GET /api/settings/shares/{shareID}/url`

`{"url", "token"}`: the link's address again, `Cache-Control: no-store`. `409`
with `{"error", "rotatable": true}` for a link made before tokens were sealed,
or one whose sealed token does not open under the current key; give it a new
address. `404` for a preview or view link.

### `POST /api/settings/shares/{shareID}/rotate`

A new token for the link: `{"url", "token"}`. The old address stops resolving in
the same statement. Allowed on a locked link — a lock fixes what a screen draws,
and a leaked address is the moment nobody should have to unlock first. Audited
as `share.rotated`.

### `GET /api/settings/sharing`
### `PUT /api/settings/sharing`

`{"visitorWrites": true}`: whether any visitor action on any link may run. On by
default; turning it off refuses the next action on every interactive link.
`PUT` requires the field and is audited as `sharing.visitor_writes`.

### `POST /api/settings/shares/{shareID}/view`

What a handed-out link shows, for its owner: `{"token", "expiresAt"}` for a new
link that copies the link's page, pin, trial, parameters, `detail` and `scope`,
lives fifteen minutes, is not listed and cannot be edited. Open
`/share/<token>/` with it.

A copy rather than the link itself, so looking never counts as the screen's
viewer or runs as it, and a copy drawn through the same routes cannot show
anything the real screen would not. A view link is never interactive. `404` for a link
that is not an ordinary handed-out one (a preview or another view). Not audited:
it discloses nothing the owner's own session does not already show them.

## Share pages

A share page is HTML the owner wrote, drawn on a share link. It reads a redacted
snapshot of the panel through a versioned contract and a small SDK. The design, the security model and the workflow are in
[share-pages.md](share-pages.md); this section is the wire.

A link serves its page's files at `/share/<token>/…` — `index.html` for the
directory, `vibepanel.js` from the binary, and every other published file by its
path — each with `Content-Security-Policy: sandbox allow-scripts` and a
`connect-src` that names that token's snapshot and nothing else. A token that
resolves to nothing — revoked, expired, never issued — gets a `404` static page
saying the link no longer works: no script, nothing of the panel's, and never
the panel's own bundle, which on an address a stranger holds would be its
sign-in page.

With the snapshot below, those are the whole of what a share token can reach.
No credential beyond the token in the path, and no other route accepts that
token at all: presenting it as a `Bearer` header or as the session cookie
answers `401` everywhere, including on `/ws`. That is enforced by where the
routes are registered rather than by a flag a handler reads.

### `GET /api/share/{token}/v1/snapshot`

What a page draws. Authenticated by the token in the path and nothing else;
`Access-Control-Allow-Origin: *` on every answer, refusals included, and never
credentials — a sandboxed page has the origin `null`, and the token is the whole
capability.

```jsonc
{
  "v": 1,
  "page": { "id": "…", "version": 3, "draft": false },  // this link's pseudonym for the page
  "sections": ["sessions", "spend"],                     // what the manifest asked for
  "params": { "title": "Lobby", "warnAt": 5 },           // every declared parameter
  "at": 1756740600, "name": "…", "remark": "…", "detail": "counts",
  "expiresAt": 0, "usageReadable": true, "stale": false,
  "machine": { … }, "counts": { … }, "projects": [ … ], "sessions": [ … ],
  "spend": { … } | null, "todos": null, "trend": null, "flow": null, "feed": null, "repo": null,
  "scope": "", "scopeName": "", "scopeRepoOwner": "", "scopeRepoName": "",
  "data": { "announcement": "…" },      // the page's public data (docs/page-backend.md §2)
  "interactive": false,                  // may this link run visitor actions right now
  "actions": { "vote": { "who": "visitor", "label": "", "input": {}, "enabled": false } },
  "sources": { "weather": { "ok": true, "fetchedAt": 0, "status": 200, "error": "", "value": {} } },
  "server": null                         // server.js transform's result
}
```

Every field is declared, with its meaning, in
`internal/pages/sdk/vibepanel.d.ts`, and a test fails when the two disagree.
`v1` is additive: fields and sections may be added, nothing is renamed, retyped
or removed. Names are `''` under `counts`.

A section not named in the page's manifest is `null` (or, for `sessions`, an
empty list). Nothing on the query string changes that: `v`, `w` and `h` are the
viewer report for the owner's count, and anything else is ignored. The body is
built at most once a second per link, however many screens poll it; the link's
own `name` and `remark` are read from its row on every poll, so an edit shows on
the next one.

`401` is a revoked, expired or unknown link; `410` is a page that is gone, has
no published version, or a link that draws no page; `422` is a preview whose
draft manifest does not validate, with the reason; `503` is the panel's
database.

### `POST /api/share/{token}/v1/actions/{name}`
### `OPTIONS /api/share/{token}/v1/actions/{name}`

The one write a share token reaches: a visitor action the page declares, on a
link the owner made interactive (docs/page-backend.md §6). The body is the
action's input as one JSON object, at most 4 KiB, or empty. The answer is always
`{"ok": true, "result": …}` or `{"ok": false, "error": "…", "retryAfter": n}`:

| status | why |
|---|---|
| `403` | the link is not interactive (or is a view copy), or visitor writes are off |
| `404` | no such visitor action in the version the link draws |
| `429` | over the action's rate for this address, 600/min for the link, or 3000/min for the panel; `Retry-After` |
| `400` | the payload is not exactly the declared input, or the data change breaks a limit |
| `401` | the link is revoked, expired or unknown |

Effects write only the page's own data, or run `server.js`'s `onVisitorAction`.
A preview link's actions write draft data. `OPTIONS` answers the browser's
preflight with `Access-Control-Allow-Origin: *`, `POST` and `Content-Type`,
never credentials. Counted per link (`actionsToday`) and audited as
`share.action`, one row when an action starts being used in a minute and one
with the count when that minute has passed.

## Admin pages

A page's own admin page, behind the panel login (docs/page-backend.md §3).

### Opening one: `/pages/{pageID}/admin/`

Signed in with the session cookie (not an API token), mints an **admin grant**
bound to that session and answers `303` to `/page-admin/{grant}/<admin.entry>`;
not signed in, `303` to `/`. `?draft=1` serves the draft directory's admin page
on draft data. `404` for a page with no `admin` in its manifest; `409` for a
live admin page on a page never published.

### Its files: `/page-admin/{grant}/…`

The page's files, for its admin page, served like a share page's: `sandbox
allow-scripts allow-forms`, `form-action 'none'`, `frame-ancestors 'self'`, a
`connect-src` that names `/api/page-admin/{grant}/v1/`. `server.js` is never
served. A grant that has expired or whose session has ended gets the plain
"no longer works" page.

### `GET /api/page-admin/{grant}/v1/snapshot`
### `GET /api/page-admin/{grant}/v1/data`
### `PUT /api/page-admin/{grant}/v1/data/{key}`
### `POST /api/page-admin/{grant}/v1/data/{key}/increment`
### `POST /api/page-admin/{grant}/v1/data/{key}/append`
### `DELETE /api/page-admin/{grant}/v1/data/{key}`
### `POST /api/page-admin/{grant}/v1/actions/{name}`
### `GET /api/page-admin/{grant}/v1/sources`
### `GET /api/page-admin/{grant}/v1/links`

The admin API. The grant in the path is the only credential; it lives 8 hours,
dies with the session that minted it (sign-out, a password change) on its next
request, and is refused anywhere else, as a share token is refused here — both
answer `401`. `OPTIONS` on any of these answers the preflight.

`snapshot` is the page's snapshot with names, over the whole panel, with
admin-visibility data and admin actions. The data routes take and answer what
the settings data routes do (see below). `actions/{name}` runs an action whose
`who` includes `admin`, answering like a visitor action. `sources` is each
source's last fetch. `links` is `{"links": [{"name", "remark", "interactive",
"viewers"}]}` — never a token or an address. Writes are audited as
`page.data_changed` under the user the grant was minted for; a refused grant as
`page.admin_rejected`.

`sessions` is empty, and `spend`, `todos`, `trend`, `flow`, `feed` and `repo`
are `null`, unless the page's manifest names them. A page can only ever
subtract: the sections a snapshot may carry are a fixed set, a manifest chooses
among them, and no manifest produces a field that is not in the list. `null` and
a zeroed object are different facts: the first is "this page does not ask for
it", and the second has a `readable` flag of its own to tell "nothing was spent"
from "nothing has been counted yet".

`spend` is tokens, never money: prices differ per model, per tier and over time,
and a currency figure from a stale table is a confident wrong number on a wall.
It carries `today`, `yesterday`, `month`, `lastMonth` and `window` totals (each
split into input, output, cache read, cache write, requests and a summed
`total`), `hoursToday` so a per-hour rate is "so far today" on the *server's*
clock, and the arrays the manifest asked for (`spend.days`, `months`,
`heatmap`, `split`): `days`, `months`, `heatmap`, `tools`, `models`,
`projects`. Its `date` is the server's local day, because the buckets
are local days and a phone abroad must not decide which square is today.

`trend` is the last fifteen minutes of the machine and the running token total,
sampled every ten seconds, for a page that draws a line rather than a number: `{"every": 10, "points": [{"at", "cpu", "memory", "load", "tokens"}]}`.
`cpu` is `null` where `/proc` could not be read, which is a different fact from
zero. It is kept in this process's memory and never stored, so on a screen that
has just been switched on, or after a restart, the line starts now rather than
having a hole in it. It is filled by the polls that draw it: a panel nobody is
watching does no work for a graph nobody is looking at.

`spend.allTime` is every token recorded within the link's scope, summed from the
months already in hand. Each bucket in `days`, `months` and `heatmap` carries
`input`, `output`, `cacheRead` and `cacheWrite` as well as `total`, which is
what a stacked bar is drawn from: the same tokens the totals already disclose,
cut the same way.

`todos` is counts only: `open`, `done`, `closedToday`, and the same per project.
The items themselves are never sent, at either `detail`. A todo line says what
somebody is about to do about a customer, a bug or a date; it is closer to a
note than to a session title.

`flow` and `feed` come out of the session-event log: one append-only row per
state transition, written where the poller already notices one. Before it the
panel kept state and no history, so every widget with a time axis on this
surface degraded to a single current number and a screen of trends was
unbuildable. `flow` is `{"every", "since", "windowDays", "today", "window",
"buckets": [{"at", "started", "waited", "finished", "waitSeconds",
"waitEnded"}]}`; `feed` is the same transitions in the order they happened, each
carrying the per-link pseudonyms, the state and the time. No new fact reaches
the wire because a page asked for a feed.

It is a **flow**, not a stock: a bucket counts transitions that happened in it,
never how many sessions were in each state at the time. Reconstructing a stock
from a flow needs a starting census and every event since, and the first dropped
write makes it wrong in a way nothing can detect. `waitSeconds` and `waitEnded`
are sent rather than an average, so a bucket where nothing finished waiting is
empty rather than a zero-second wait. The log is kept for 31 days and swept
hourly: a wall that has been up for a month should not be carrying a year of
rows.

`repo` is what the working trees produced, and it is the half of "what did it
cost / what came out of it" the panel could not answer at all until it read
repositories. `{"readable", "ageSeconds", "repos", "projects", "windowDays",
"today", "window", "days", "byProject", "prs"}`, where a totals object is
`{"commits", "added", "removed", "files"}`.

This replaced `counts.doneToday` and the checklist figure as the headline
numbers a page offers, because both of those are *self-reported*: a todo is
ticked because somebody remembered to tick it, and a session reaches `done`
because an agent's hook said so; a session left running all day never says it at
all. They measure whether the panel was told something. Commits and changed
lines are things that exist now and did not this morning, and anybody can check
them against the repository. `counts.doneToday` is still sent and is still a
real event; it is simply not a measure of output.

Added and removed lines are two numbers and never a net one: +1200/−800 is a
different day from +400/−0 and a net figure is identical in both, hiding a
refactor completely. They are labelled as *change*, not as productivity.

What to know before building against `repo`:

- It is never fresh. A wall polls every two seconds; `git log` is not a
  two-second question and a GitHub round trip certainly is not. So the poll
  reads whatever a background refresh has already produced and reports its
  `ageSeconds`; the first poll for a repository comes back with
  `readable: false`, which is "not counted yet" and not "nothing happened
  today". A working tree is re-read at most every 90 seconds, a repository's
  pull requests at most every 5 minutes, shared across every viewer of every
  link, and not at all once nobody is looking.
- `prs` is the only outbound request a wall can cause, and four things have to
  be true at once, none of them a default: the manifest asks for pull
  requests (`repo.prs`), the link is scoped to one project, `detail` is `names`, and a token is
  in the panel's environment. It is counts and rollups — `open`, `draft`,
  `green`, `red`, `pending`, `approved`, `changesRequested`, `mergedToday` — and
  never a title, a number, an author, a branch or a URL.
- Uncommitted work is invisible to all of it. An agent that has been editing
  for an hour without committing produces zero commits and zero lines here; the
  `dirty` count on `byProject` is the only sign of it.

The figures come from `git log --shortstat`, and that is the disclosure decision
rather than a parsing convenience: `--numstat` would carry every changed
filename through the panel on its way to a wall, and asking for `%s` would carry
the commit messages. A commit *count* is a number; a commit *subject* is prose
from inside somebody's repository. So no path, no filename, no branch name, no
sha, no author and no subject appears here at either `detail`, only the project
names, which follow `names` exactly like every other group in the snapshot.

`scopeRepoOwner` and `scopeRepoName` are the scoped project's repository, and
they are the one thing on this surface that reads a working tree: one
`git remote get-url`, behind the same cache the repository tab uses. Both are
empty unless **all** of: the link is scoped to a project, `detail` is `names`,
and the remote is a `github.com` one this panel is willing to link to. They are
two parsed halves and never a URL, so a viewer can build
`https://github.com/{owner}/{name}` and nothing else; the remote string and the
project's path are never sent, at either `detail`.

The narrowing is the disclosure decision, not a styling one. A repository is a
public, resolvable name that also names the organisation. Under `counts` the
link sends no names at all, so a repository link there would identify the
customer more precisely than the project path that mode exists to withhold. A
session-scoped link's `scopeName` is a session title, and hanging a repository
off it would disclose which project that session belongs to on a link that was
narrowed to one session. An unscoped link has no single repository to name.

What it deliberately does **not** carry, in either `detail` mode: the project's
path on disk, a session's `cwd`, the command line, the tmux session name, the
hostname, the sampler's disk path, any remote URL, and the panel's own session
and project ids.
A path names a customer and a home directory; a command line carries whatever an
agent was invoked with. Neither has a use on a screen behind somebody's desk.

`id` and `projectId` are pseudonyms: an HMAC of the real id under the link's own
stored hash. They are stable for the life of one link, so a list does not re-key
itself on every poll, and different for every other link, so two screens on
two walls cannot be joined into one picture of the panel.

Under `detail: "counts"` the `name` fields are empty strings and the page
numbers the groups and rows instead. Under `detail: "names"` they carry the
session title and the project name, still no paths.

`kind` is `agent`, `shell` or `other`, which is what makes a wall readable
without quoting the command. `measured` is `false` when the sampler found no
process tree for that row; `cpuPercent` there is not a reading of zero, and zero
is a real reading. Scratch terminals opened under a session are left out
entirely: they are session rows with a parent, and listing them reports two rows
for one job.

`at` is when the server took the reading, and a page counts up from it.
That is the field to use if you build your own display: a page that has
silently frozen looks exactly like a quiet system, and the numbers themselves
cannot tell you which you are looking at.

Answers are `Cache-Control: no-store`. `401` means the link was revoked, has
expired, or never existed: one answer for all three. Rejected attempts are
audited as `share.rejected`, gated to one row per source per minute. `403` is
the `--allow-from` allowlist, which applies here exactly as it does to the
panel: a share link must not be a way around it.

### `GET /api/settings/pages`
### `POST /api/settings/pages`
### `GET /api/settings/pages/catalogue`
### `GET /api/settings/pages/{pageID}`
### `PATCH /api/settings/pages/{pageID}`
### `DELETE /api/settings/pages/{pageID}`

Pages, behind the ordinary session. A share token answers `401` to all of them.

```sh
# A new page from a template, in <data dir>/pages/page-lobby
curl -sX POST https://panel.example:18443/api/settings/pages \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Lobby","template":"wall","sourceDir":""}'
# {"id":"…","name":"Lobby","sourceDir":"/home/me/.local/share/vibepanel/pages/page-lobby","publishedVersion":0,…}
```

`template` is one of the catalogue's templates, which scaffolds an empty or new
directory (never one with files in it); `""` adopts a directory that already
holds a `vibepanel.json`. `sourceDir` is absolute; empty with a template means a
new directory `page-<slug>` under `pagesRoot`, which is `pages/` in the panel's
data directory, beside the pasted screenshots. The catalogue also names the sections, the
viewports the Preview offers, the fixtures, the script hosts a manifest may
allow and the size limits.

`GET /api/settings/pages/{pageID}` is the page with its versions (candidates
marked) and the links drawing it, with their viewer counts. `PATCH` renames it
or moves its draft (`page.moved` in the audit log). `DELETE` is refused with
`409` and the links' names while any handed-out link draws it.

### `GET /api/settings/pages/{pageID}/draft`
### `GET /api/settings/pages/{pageID}/draft/fingerprint`

The draft directory as the Preview sees it: whether it reads as a page (`ok`,
`error`), its manifest, files, what was ignored and why, lint problems with file,
line and fix, and the changes against the published version by file hash.
`fingerprint` is sizes and modification times hashed — cheap enough to ask for
twice a second while a pane is open.

### `POST /api/settings/pages/{pageID}/publish`
### `POST /api/settings/pages/{pageID}/rollback`

`{"note": "…"}` stores the draft as the next version and publishes it:
`{"version": 4}`. The same reader the Preview used, the same limits (64 files,
5 MiB, 2 MiB a file, types checked against their bytes, no symlinks). Lint
problems do not block it; a directory that does not read as a page does.
`{"version": 2}` to `rollback` re-publishes an existing version. Links following
the published version reload into it on their next poll. Audited as
`page.published` and `page.rolled_back`, and a line is appended to the draft's
`.vibepanel/HISTORY.md`.

### `POST /api/settings/pages/{pageID}/preview`
### `POST /api/settings/pages/{pageID}/preview/{linkID}/renew`

`{"detail": "counts"}` mints a share link that draws the page's **draft**, lives
fifteen minutes and is not listed: `{"id", "token", "expiresAt"}`. It goes
through exactly the routes above, so a preview cannot show what a wall would
not. `renew` moves a live preview's expiry; an expired one answers `410` and is
not revived.

### `PUT /api/settings/pages/{pageID}/errors`

`{"errors": [{"kind", "message", "source", "line"}]}` — what a preview frame
reported, relayed by the signed-in pane and written to the draft's
`.vibepanel/errors.json` for the agent. Bounded to 50 errors and 500 characters
each; refused when `.vibepanel` is a symlink.

### `POST /api/settings/pages/{pageID}/trial`
### `POST /api/settings/pages/{pageID}/trial/{linkID}/keep`
### `DELETE /api/settings/pages/{pageID}/trial/{linkID}`

`{"linkId": "…", "minutes": 10}` freezes the draft into a candidate version and
puts it on one link that already draws the page, for 1 to 60 minutes:
`{"version": 5, "pinUntil": 1756741200}`. The screen reloads into it; when
`pinUntil` passes it resolves back to the published version on read, with
nothing having to run. `keep` publishes the candidate; `DELETE` ends the trial
now. Audited as `share.page_trial`.

### `POST /api/settings/pages/{pageID}/fork`

`{"name": "…", "sourceDir": ""}` copies the page's draft into a new directory
(`page-<slug>` under `pagesRoot` by default) as a new page.

### `POST /api/settings/pages/{pageID}/open`

Makes a page something an agent can be started in: `{"page", "projectId",
"restored"}`. When the page's directory is gone it is written back from the
published version — where it was when that is possible, under `pagesRoot`
otherwise, with the page's `sourceDir` updated — and `restored` is that version,
`-1` for a page never published (a blank page is scaffolded), `0` when nothing
had to be written. When no project points at the directory one is made, named
`page-<slug>`; an existing one is reused. Audited as `page.restored` when a
directory was written. A lost project or a wiped data directory is recovered by
the same request, because the page is its versions in the database.

### `PUT /api/settings/pages/root`

Where new pages go: `{"dir": "/abs/path"}`, or `{"dir": ""}` to go back to the
default. Unset, nothing is stored and a page goes under the data directory's
`pages/`. A directory is refused with `400` unless it is absolute and a file can
be written in it; pages already made stay where they are. Answers what the
catalogue's `pagesRootInfo` says, and is audited as `page.root_changed`.

The root is resolved every time a page is made, down a fallback: the setting;
`<data dir>/pages`; `~/.local/share/vibepanel/pages` when the data directory is
somewhere else; a `vibepanel-pages-<uid>` directory in the temporary directory
as the last resort. `pagesRootInfo` is `{"dir", "setting", "source", "problem"}`,
where `source` is `setting`, `default` or `fallback` and `problem` says why a
rung above `dir` was skipped.

### `GET /api/settings/pages/{pageID}/export`

The page as a zip: `vibepanel.json` at the top and the page's files beside it —
not the SDK copy, its types or `AGENTS.md`, which every directory gets. `?version=N`
picks a version; without it, the published one, or the directory as it is for a
page never published. Named `page-<name>-v<N>.zip`. `404` for a version that
does not exist.

### `POST /api/settings/pages/import`

A zip as the request body (at most 6 MiB) becomes a new page, in a new
`page-<name>` directory under the pages root, named `?name=` or the name in its
manifest. `201` with `{"page", "ignored"}`: `ignored` lists what the archive had
that a page does not keep (`AGENTS.md`, the SDK copy, a file of a type a page
cannot serve). One folder wrapping everything is looked through. The archive is
read by the publish rules — a path that leaves the page, a file that is not what
its name says, a missing `index.html` or manifest, or a limit exceeded is `400`
— and the page is not published. Audited as `page.imported`.

The answer also carries `hosts` and `secrets`, what the page's sources need
before they fetch anything (neither travels in an archive), and `dataSkipped`.
An archive exported with `?data=1` carries `vibepanel-data.json`; its values are
written into both namespaces, checked against the manifest the page arrived
with, and whatever does not fit is listed in `dataSkipped`.

### `GET /api/settings/pages/{pageID}/data`
### `DELETE /api/settings/pages/{pageID}/data`
### `PUT /api/settings/pages/{pageID}/data/{key}`
### `POST /api/settings/pages/{pageID}/data/{key}/increment`
### `POST /api/settings/pages/{pageID}/data/{key}/append`
### `DELETE /api/settings/pages/{pageID}/data/{key}`

A page's own data, docs/page-backend.md §2. `?ns=live` (the default) is what
handed-out links read; `?ns=draft` is the Preview's. The schema is the published
version's manifest for `live` (`409` for a page never published) and the draft
directory's for `draft`.

`GET` answers `{"schema", "values", "updatedAt", "bytes", "limit"}` with every
declared key, admin-visibility ones included, at its stored value or default. A
stored value that no longer fits the manifest reads as the default.

`PUT` takes `{"value": …}` and refuses a counter or a log (they change by
`increment` and `append`); `increment` takes `{"by": n}` (a whole number,
default 1, never below zero); `append` takes `{"item": {…}}` with exactly the
log item's fields, stamps `at` and keeps the newest `max` entries. Each answers
`{"value": …}` with the key's new value. `DELETE` on a key puts it back to its
default and on the collection clears the namespace; both answer `204`.

Refused with `400` and the reason: an undeclared key, a value that does not fit
its type or bounds, or a write that would take the namespace past 64 keys or
256 KiB. Every write is audited as `page.data_changed`, and shows on every
link's next poll.

### `GET /api/settings/pages/{pageID}/sources`
### `GET /api/settings/pages/{pageID}/hosts`
### `PUT /api/settings/pages/{pageID}/hosts`
### `GET /api/settings/pages/{pageID}/secrets`
### `PUT /api/settings/pages/{pageID}/secrets/{name}`
### `DELETE /api/settings/pages/{pageID}/secrets/{name}`

What a page's sources need before the panel fetches anything
(docs/page-backend.md §4). `sources` lists each declared source of the
published version (the draft's, for a page never published) as `{"key", "url",
"host", "approved", "every", "ok", "fetchedAt", "status", "error",
"secrets": [{"name", "set"}]}`.

`PUT hosts` replaces the approved list with `{"hosts": ["api.example.com",
"internal.example.com:8443"]}`: names, with `:port` only when it is not 443 —
never a URL, a wildcard or an address range, because approving a name must
approve exactly that name. At most 32; audited as `page.hosts_changed`.

`PUT secrets/{name}` takes `{"value": "…"}` (1 to 4096 bytes, no line breaks),
seals it under the panel's key and answers `204`; `GET secrets` answers
`[{"name", "setAt"}]` and never a value. A secret is replaced into a source's
headers at fetch time and redacted from its errors. Audited as
`page.secret_set` and `page.secret_deleted`, by name.

Changing hosts or secrets drops the page's last results, so the next background
tick fetches again. A fetch refuses `http`, any address that is loopback,
private, link-local (cloud metadata), CGNAT, multicast or reserved — checked on
every address the name resolves to, and the checked address is the one dialled
— any redirect, a body over `maxBytes` and anything slower than `timeout`.

### `GET /api/settings/pages/{pageID}/server/log`

The page's `server.js` log (docs/page-backend.md §5): `{"lines": [{"at",
"level", "text"}]}`, the last 200, oldest first. `level` is `info` for
`ctx.log(...)` and `error` for a hook that did not compile, threw, ran past its
budget (transform 50 ms, `onSchedule` and `onAdminAction` 500 ms,
`onVisitorAction` 200 ms), or returned more than 64 KiB — each of which also
wrote nothing. Kept in memory, so a restart empties it; the same lines are
written to `.vibepanel/server.log` in the page's directory for the agent
building it.

### `PUT /api/settings/shares/{shareID}/page`

What a link draws: `{"pageId": "…", "pinVersion": 0, "params": {"title": "Hall"}}`.
`pageId` is required. `pinVersion: 0` follows the published
version. `params` are checked against that version's manifest and refused with
the parameter's name when one does not fit; a page republished with a narrower
range gets the default in place of a stored value that no longer fits. Not the
link's `detail` or `scope`, which are fixed. Refused with `409` on a locked link.
Audited as `share.page_changed`, or `share.params_changed` when only the
parameters moved.

`POST /api/settings/shares` takes the same `pageId` and `params`.

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
those as the same thing signs the user out during a storage fault, into a login
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

From the client: `subscribe`, `unsubscribe`, `resize`, `takeControl`,
`visibility`, `paste`, `ping`. From the server: `subscribed`, `size`, `title`, `clipboard`, `exit`,
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

There is no way to attach to a session's terminal over plain HTTP: that is the
WebSocket's job, and a polling shim would be a worse version of it. There is no
API for the setup token; it is printed to the panel's own log on first run and
consumed once.
