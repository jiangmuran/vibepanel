# Share pages: an SDK for the read-only link, and pages people write themselves

A share link draws a **share page** — HTML the owner wrote, usually by asking an
agent in one of the panel's own sessions to write it, drawing the panel's
redacted snapshot through a versioned API and a small SDK. It used to draw a
board, assembled from a fixed registry of widgets; for one release it could draw
either, and then boards were removed (§13).

The board's limit was never the data. It was the vocabulary: every new way of
drawing a number was a Go widget kind, a React component, a preset, a
`board-check` budget and a build-log entry. The data a link may disclose was
already one fixed, redacted struct, so that struct is published, anybody can
draw it, and the page is put in the same kind of sandbox `preview_render.go`
already used for HTML the panel did not write.

This document is the design as built. §12 lists where the build departed from
the plan that preceded it, and why.

## 1. The shape

```
 the owner's panel                         the panel (Go)                            a wall
 ─────────────────                         ──────────────                            ──────
 session: agent edits                      share_pages / versions / blobs
 <data>/pages/page-lobby/ ──publish──▶     (immutable, in SQLite)
        ▲                                         │
        │ Preview pane (sandboxed iframe          │ share_links.page_id
        │ on a 15-minute preview link)            │
        └───────────────────────────────  GET /share/{token}/            ──▶  index.html
                                          GET /share/{token}/{path}      ──▶  its files
                                          GET /share/{token}/vibepanel.js ─▶  the SDK, from the binary
                                          GET /api/share/{token}/v1/snapshot ◀── the SDK polls
                                                  │
                                          buildShareReading (the one redaction)
```

| layer | what it is | where |
|---|---|---|
| **Data** | `GET /api/share/{token}/v1/snapshot`: the redacted snapshot, sections chosen by the page's manifest | `internal/httpapi/sharepage.go` |
| **SDK** | `vibepanel.js` + `vibepanel.d.ts`: polling, connection state, clock, fixtures, safe text, the badge, the Preview's instruments | `internal/pages/sdk/` |
| **Page** | static files plus `vibepanel.json`, stored as immutable versions | `internal/store/pages.go`, `internal/pages/` |

Every handed-out link draws a page. A link is created on a page and can be
re-pointed, pinned and given settings afterwards; there is nothing else for it
to draw.

## 2. What stays true

The four properties at the top of `internal/httpapi/share.go` carry over:

1. **It is a capability.** 32 random bytes in the URL, a SHA-256 in the table.
2. **It is read-only because of where it is registered.** A share token reaches
   three `GET`s — the v1 snapshot, and a page's files at `/share/{token}` and
   `/share/{token}/*` — and `TestAShareTokenReachesOnlyTheseRoutes` walks the
   router and fails on anything else under either prefix. `currentUser` still
   never consults `share_links`. Red line 8 in `AGENTS.md` says this and says
   why the list is what it is.
3. **It discloses less than the panel knows.** The v1 snapshot is the same
   `share*` structs through the same builder, restated as `shareSnapshot`. A page
   cannot add a field: the page is HTML and the fields are Go.
4. **A page can only subtract.** The manifest names sections, and
   `Manifest.Needs()` reads them into `pages.Needs` — switches and bounded day
   counts, nothing that names a table, a column or a path — which is all the
   builder is given. A page has no vocabulary for anything the builder does not
   already compute. `detail` and `scope` stay on the link, fixed by a signed-in
   owner.

**Nothing a viewer sends decides anything the response carries**: sections come
from the stored version,
and `TestWhatAPageSendsCannotChangeItsSnapshot` puts `sections=…&detail=…` on
the query string and compares.

## 3. The data: `v1/snapshot`

The response is the redacted reading's fields plus `v`, `page` (`{id, version,
draft}`), `sections` and `params`. `docs/api.md` has the example. A link that
draws no page — only a board link whose conversion failed (§13) — answers `410`.

- **Additive only.** Fields and sections may be added in v1; nothing is renamed,
  retyped or removed. `TestTheSDKTypesMatchTheSnapshot` compares every struct
  with its interface in `vibepanel.d.ts`, both directions.
- **Memoised per link for one second.** Twenty screens, or one page polling in
  a loop, cost one build a second. The viewer book is still fed per request.
- **Readable from anywhere, without credentials.** `Access-Control-Allow-Origin:
  *` is set by `shareReadableAnywhere`, which runs *before* the token check so
  that the refusals carry it too. It was in the handler first, and a revoked
  wall could not see its own `401` — the browser hid the status and the SDK
  reported "reconnecting" forever. `pages-check` found it.
- **Names are `''` under `counts`**, as they always were; `vp.name(row)` turns
  that into `null`. See §12.

## 4. The SDK

One plain script, no build and no dependencies, served from the binary at
`./vibepanel.js` inside every page, so a page always runs the SDK that matches
the panel serving it. A copy is scaffolded into the directory for reading.

```js
const vp = VibePanel.connect()               // token and base from the address; or { base, token }
vp.on('snapshot', (s) => draw(s))
vp.on('params', (p) => apply(p))             // without a reload
vp.badge(document.querySelector('#status'))  // shape and word
vp.text(el, value)  vp.name(row, 'session')  vp.since(unix)  vp.now()
vp.fmt.tokens(n)  vp.fmt.bytes(n)  vp.fmt.percent(n)  vp.fmt.duration(s)
vp.storage                                    // localStorage throws in a sandbox
```

What it owns because every hand-written page would get it wrong: polling with
jitter and backoff, pausing while hidden; the connection states
(`connecting`, `live`, `reconnecting`, `disconnected`, `revoked` —
terminal on `401` and `410`); the panel's clock from `at`; reloading after a
random 0–5 s when the page or its version changes; fixtures under `?fixture=`;
and, only when framed or under `?shot=1`, the Preview's instruments (§6).
`web/src/sdk.test.ts` runs it against a fake window.

## 5. Pages

**Storage.** `share_pages`, `share_page_versions` (immutable; `candidate` marks a
trial's), `share_page_blobs` (content-addressed; publishing one changed file
stores one file), `share_page_files`. `share_links` gains `page_id`,
`pin_version`, `pin_until`, `params` and `purpose` (`''`, `preview`, or `peek`
for a View link, §6). In the
database rather than on disk, because a page is served to anybody holding a
token and a directory is something a symlink or an agent can change under a
wall. Deleting a page a link draws is refused rather than cascaded.

**The manifest.**

```jsonc
{
  "sdk": 1,
  "name": "Lobby",
  "sections": ["sessions", "spend", "repo"],
  "spend":  { "days": 30, "months": false, "heatmap": false, "split": ["tool"] },
  "repo":   { "days": 14, "prs": true },
  "flow":   { "by": "hour", "days": 0 },
  "params": [ { "key": "title", "type": "text", "max": 40, "default": "Lobby" } ],
  "scriptHosts": [],
  "viewports": ["tv-1080", "phone"]
}
```

Strict on the way in (unknown keys refused, options without their section
refused, day ranges bounded by `pages.MaxDays`) and lenient on the way out
(`DecodeStored` drops what a later build no longer accepts, never widening).
Parameter types are `text`, `color`, `number`, `enum` and `bool`; values are
checked strictly when a link sets them and resolved leniently when a page reads
them, so a narrowed range gives the default rather than breaking a wall.

**Files.** 64 files, 5 MiB, 2 MiB each; an extension allowlist, and the bytes
checked against it (a `.png` must be a PNG, a script must be UTF-8 with no NUL);
dot-paths, `node_modules` and symlinks refused or skipped with a reason.
`vibepanel.js`, `vibepanel.d.ts` and `vibepanel.json` are never published as
files. The Preview reads one file at a time and the publish reads the directory
whole, **through the same rules** (`pages.ReadFile`, `pages.ReadDir`), so the
Preview cannot show what the publish would refuse.

**Serving.** Every file of a page, the SDK included, carries:

```
Content-Security-Policy: default-src 'none';
  script-src 'unsafe-inline' {origin}/share/{token}/ [https://{scriptHost}/…];
  style-src 'unsafe-inline' {origin}/share/{token}/;
  img-src {origin}/share/{token}/ data: blob:;  font-src … data:;  media-src … data: blob:;
  connect-src {origin}/api/share/{token}/v1/ {origin}/share/{token}/;
  worker-src 'none'; manifest-src 'none'; frame-src 'none';
  form-action 'none'; base-uri 'none';
  frame-ancestors 'none'            ('self' on a preview link, which the panel frames)
  sandbox allow-scripts
Permissions-Policy: camera=(), microphone=(), geolocation=(), …
Access-Control-Allow-Origin: *     (a page fetching its own fixture is cross-origin)
Cache-Control: no-store
```

The origin is written out rather than `'self'`, which matches nothing from an
opaque origin. `scriptHosts` accepts only `cdnjs.cloudflare.com` and
`cdn.jsdelivr.net`, and never widens `connect-src`.

## 6. The workflow

**The settings list.** Settings → Sharing is one list: each page — its name,
`vN published` or not published, its directory — with **Open**, **Publish**,
versions, fork and delete, and under it the links that draw it. A link is made
from its page's row, so there is no "which page" choice to get wrong.

**Start.** **New page**: a name, a template (`blank`, `wall`, `spend`, `built`,
`glance`) or an existing directory, and "start an agent session". The directory
— by default `page-<name>` under the pages directory, see below — is scaffolded:
the template, `AGENTS.md`, `CLAUDE.md` and a `README.md` with links back to these
docs, the SDK and its types, fixtures, a `.gitignore`; made a repository with one commit where the
tools and an identity allow; and registered. Then the panel opens it (below),
which adds it as a project called `page-<slug>`, opens the ordinary launch
picker, and three seconds after the agent starts types a first line at its
prompt without pressing Enter. `vibepanel page init` does the same from a shell
(given `--name` and no directory, into the same default), less the session.

**Where pages go.** Unset, the pages directory is `<data dir>/pages`, and nothing
is stored: move the data directory and the pages default moves with it. The
owner can name another on the line under the page list (`PUT
/api/settings/pages/root`); a directory is only stored if a file can be written
in it. It is resolved each time a page is made (`ResolvePagesRoot`), down a
fallback — the setting, `<data dir>/pages`, `~/.local/share/vibepanel/pages`
when the data directory is elsewhere, a `vibepanel-pages-<uid>` directory in
the temporary directory — and the catalogue's `pagesRootInfo` says which rung
was used and why the ones above it were not, so a disk that went away is a
sentence on the settings page rather than a page that cannot be made. The
directory name keeps the page's name in any script (`page-走廊电视墙`), because
an ASCII slug made every Chinese-named page `page-page`.

**Export and import.** A page travels as a zip: `vibepanel.json` at the top and
the page's files, without the companions every directory gets. Export is the
published version (or `?version=N`, or the directory for a page never
published); import reads the archive by the publish rules
(`pages.ReadArchive`: every path through `ValidPath`, every file through the
content sniff, every limit an error, a path that leaves the page refused, one
wrapping folder looked through) and makes a new unpublished page under the
pages directory. `vibepanel page export` and `vibepanel page import` are the
same code. Audited as `page.imported`.

**Freedom.** The scaffolded `AGENTS.md` separates what the sandbox enforces from
what is the page author's business, and says the template is a starting point.
`blank` is a heading, the connection badge and one line of counts — no layout to
inherit. An agent that reads the old instructions as a house style builds the
same wall every time.

**Open, and restore.** `POST /api/settings/pages/{id}/open` is what **Open** does:
if the directory is gone it writes the published version back (`CheckoutVersion`,
the same code as `vibepanel page checkout`) — into the old path if that can be
created, otherwise a new `page-…` directory under the pages directory — and a
never-published page comes back as the blank template; then it finds the project
whose path is the directory or makes `page-<slug>`. The page is its versions in
the database, so a deleted directory, a deleted project or a new machine is one
press rather than a command. The settings page then opens the Preview beside the
project and starts an agent when the page is new, was restored, or has nothing
running. Audited as `page.restored` when something was written.

**Fixtures** are synthetic and built from the snapshot structs in
`sharefixtures.go`: `busy` (40 sessions, 12 projects), `empty` (nothing, and
nothing counted yet), `counts` (no names), `hostile` (markup, RTL overrides and
200-character CJK in every name), `stale`, `revoked`. Never exported from the
running panel: a fixture with real session titles would be a file in a
directory about to be pushed somewhere. Served to a page, a fixture is shaped to
that page — its sections only, its options applied, its parameters filled — so a
page that forgot to ask for a section fails against the fixture too.

**The Preview pane.** A project whose directory is a page's draft gets a line
under the repository's in the file tree. Pressed, the Preview has the side
panel; pressed again, the window, with up to three screens side by side.

- The frame is on a **preview link**: a real share link, fifteen minutes,
  renewed while open, not listed and not editable, that draws the draft. A
  signed-in preview route cannot work (the sandbox that protects the cookie is
  the thing that stops a page sending it) and would be a second path to the
  same bytes if it could.
- **Reload after the directory settles**: the fingerprint (sizes and mtimes) is
  asked for twice a second and the frame reloads only when two answers agree on
  something new. `pages-check` writes a file three times in 450 ms and counts
  one reload.
- **Errors are shown and written**: the SDK in a frame reports `error`,
  `unhandledrejection`, `console.error` and `securitypolicyviolation`; the pane
  lists them and writes `.vibepanel/errors.json` for the agent.
- **Pick**: an outline follows the pointer inside the frame; a click types
  `[index.html · 1920×1080] section.tiles > div.tile “…” (40,120 460×300) ` at the
  selected session's prompt, with control characters, line breaks and bidi
  controls removed, and no Enter. The pane accepts a message only when
  `event.source` is one of its frames' windows — every sandboxed frame's origin
  is `"null"`, so the origin proves nothing.
- Screens and data: the largest screen already showing the page first, then the
  manifest's; live `counts`, live `names`, or any fixture.
- **Publish** shows the changes against the published version and a note.
- **Try on a screen** freezes the draft into a candidate and pins one link that
  already draws the page to it for 1–60 minutes (10 by default); **Keep** publishes it,
  **Revert** or doing nothing puts the screen back. The revert is decided on
  read (`ShareLink.ResolvePageVersion`), so nothing has to run and a restart
  cannot leave a trial on a wall.
- On a narrow pane, a one-line box sends to the session with Enter, because the
  person typing is the person sending.

**Publish, then a link.** Publish from the Preview, from the page's row, or with
`vibepanel page publish`; each stores the directory as it is as the next version.
**New link** under the page is disabled until there is a published version. The
form takes a name, a label for the screen, scope, detail, expiry and the page's
parameters, and the response is the only time the URL is readable — the panel
keeps its SHA-256 — so the list shows it once, with copy and open.

**Parameters** are edited per link in Settings, under the link, and reach an
open page within a poll without a reload. So do the version pin, the name and
the label.

**View.** Because a handed-out token cannot be read back, the eye on a link's row
calls `POST /api/settings/shares/{id}/view`, which mints a **peek link**: fifteen
minutes, not listed, not editable, swept like a preview link, with the original's
page, pin, trial, parameters, detail and scope. It draws exactly what the wall
does, through the same route, and opens in a new tab whose `opener` is cut before
it navigates.

**For the agent, without a person:**

```
vibepanel page check     # file:line: what, and the fix; exits 1 on errors
vibepanel page shot --viewport tv-1080,phone --fixture live,busy,counts,hostile
```

`shot` mints a preview link, runs a local headless Chromium (preferring
`chrome-headless-shell`, which on a desktop with no display wrote a screenshot
in under a second where the full browser hung for a minute), writes
`.vibepanel/shots/<screen>-<data>.png`, and reads back a report the SDK writes
into the page under `?shot=1`: errors, refused requests, text reading `NaN`,
`undefined`, `null` or `[object Object]`, and overflow. Where the browser cannot
build its own process sandbox (Ubuntu's AppArmor user-namespace restriction), it
says so and asks for `--no-browser-sandbox` rather than dropping the sandbox
itself. `publish`, `checkout` (a version back out into a directory), `list` and
`sync-sdk` complete it. The scaffolded `AGENTS.md` tells the agent not to
publish; the agent runs as the same user, so that is a default, not a boundary.

**Coming back later.** Publishing appends to `.vibepanel/HISTORY.md` with the
commit and whether the tree was dirty; `AGENTS.md` tells the agent to read it
first. The versions list rolls back. **Fork** copies a page's draft into a new
directory as a new page and hands it to a session, for two agents trying two
directions.

## 7. Security

The thing defended: the panel's origin holds a session cookie that is a writable
terminal, and a page is HTML the panel did not write, on that origin, opened in
browsers that are signed in.

| # | threat | control | what remains | pinned by |
|---|---|---|---|---|
| T1 | page script uses the owner's session | `sandbox allow-scripts` in the response header → opaque origin, no cookie, no storage; `connect-src` limited to the token's snapshot; no CORS on authenticated routes; `/ws` checks `Origin` | a browser sandbox bug | `pages-check` sandbox probes, each asserted by *how* it failed; with the sandbox removed they read the cookie and open a window, with `connect-src` widened too they read `/api/state` and open `/ws` |
| T2 | a page opened top-level escapes the iframe attribute | the sandbox is a header on every file | — | `TestEveryFileOfAPageIsServedInsideTheSandbox`; probes run top-level |
| T3 | a page discloses more than its link | the one redaction; the manifest chooses among fixed sections (`pages.Needs`); `detail`/`scope` on the link | — | `TestAManifestNeedsExactlyWhatItNames`, `TestTheSnapshotAnswersToTheTokenAlone` |
| T4 | markup in a session title | contained by T1; `vp.text`; the `hostile` fixture; lint on `innerHTML` | that markup can do what the page can | `pages-check` templates × hostile; `TestLintNamesWhatThePolicyWillRefuse` |
| T5 | a page sends data elsewhere | `connect-src`, `img-src`, `form-action`, no popups, `no-referrer` | CSP is not an exfiltration boundary; the data is what the URL already shows | `pages-check` outside listener |
| T6 | a third-party script | none unless the manifest names one of two CDNs; `connect-src` unchanged | a compromised package runs in the sandbox and meets T5 | `TestAScriptHostIsNamedOnlyWhenTheManifestAsks` |
| T7 | cost | 1 s memo per link; sections per manifest; git and PRs from the warm cache | — | `TestTwentyScreensOnOneLinkCostOneBuild` |
| T8 | a bundle that lies | allowlist + sniffing, limits, no symlinks, no dot-paths, files read singly (no archive extraction) | — | `TestAPageDirectoryIsRefusedRatherThanTrimmed`, `TestReadingOneDraftFileAgreesWithThePublish` |
| T9 | a share token writes | every page and link route is under `RequireAuth` | — | `TestAShareTokenReachesOnlyTheseRoutes`, `TestEverythingRequiresASession` |
| T10 | a preview link leaks | fifteen minutes; same disclosure as a real link; not revivable once expired | fifteen minutes of it | `TestAPreviewLinkDrawsTheDraftAndIsNotAWall` |
| T11 | a frame posts at the panel | source-window check, a fixed message set, rebuilt payloads, caps | — | `pick.test.ts` |
| T12 | picked text runs in a terminal | control, line-break and bidi characters stripped; bracketed paste; no Enter | an agent reads a short string that may try to steer it | `pick.test.ts`, `pages-check` pick |
| T13 | parameters widen a snapshot | only echoed | — | `TestParametersChangeNothingButParameters` |
| T14 | a trial left on a wall | resolved on read | the minutes asked for | `TestATrialOnAScreenEndsByItself` |
| T15 | writing into the draft directory | `.vibepanel/` only, refused through a symlink, bounded | — | `TestTheMetaDirectoryIsNotFollowedThroughALink` |
| T16 | a peek link discloses more than the link it copies | copies `detail`, `scope`, page, pin and parameters from the row; minted only for a real link, by a signed-in owner; fifteen minutes | fifteen minutes of it | `TestViewingALinkDrawsWhatItDrawsAndNothingMore` |
| T17 | a dead address hands a stranger the panel | `/share/{token}` never falls through to the SPA: a static page with no script under `sandbox` | — | `TestALinkThatDrawsNothingSaysSoAndIsNeverThePanel`; `pages-check` workflow/gone |

Audit events: `page.created`, `page.published`, `page.rolled_back`,
`page.moved`, `page.restored`, `page.deleted`, `share.page_changed`,
`share.params_changed`, `share.page_trial`, `share.converted`. Preview and peek
links are not audited: they disclose nothing the owner's own session does not
already show them.

Out of scope, with the reason already written elsewhere: no write route for a
page (`writable-links.md`); no page with the owner's authority
(`plugins.md` §3); no server-side code in a page.

## 8. Checks

`make pages-check` — sandbox probes in a signed-in browser (cookie, storage,
origin, API read, API write, another host by fetch and image, `/ws`, popups,
framing the panel, being framed), the workflow through the UI end to end, and
every template on a TV, a laptop and a phone against `busy`, `counts`, `hostile`
and `empty`. It is in `make verify`.

## 9. What was not built

- **A separate origin for pages.** The response-header sandbox on the panel's
  origin is the boundary, as it is for directory previews.

## 10. Decisions taken

1. Red line 8 is a list of `GET`s pinned by a test, not "exactly one".
2. `Access-Control-Allow-Origin: *` on the share API, refusals included, never
   credentials.
3. The sandbox header on the panel's own origin is the boundary.
4. `scriptHosts` from a fixed list of two CDNs.
5. Boards stayed, for one release; then they went (§13).

## 11. Files

| | |
|---|---|
| `internal/pages/` | manifest, parameters, bundle reading, lint, scaffold, templates, the SDK |
| `internal/store/pages.go` | pages, versions, blobs; `share.go` for the link columns |
| `internal/httpapi/sharepage.go` | page files, the v1 snapshot, the CSP, the memo |
| `internal/httpapi/pagesadmin.go` | settings routes: pages, drafts, publish, previews, trials, fork, link pages |
| `internal/httpapi/pageopen.go` | Open and restore, View (peek links), converting board links |
| `internal/httpapi/sharefixtures.go` | fixtures, and shaping one to a page |
| `cmd/vibepanel/page.go` | `vibepanel page …` |
| `web/src/components/pages/` | Preview pane, page line, the Sharing list (`Sharing.tsx`, `PageLinks.tsx`), parameters form, frame messages |
| `web/scripts/pages-check.mjs` | the browser check |

## 12. Where the build departed from the plan

- **Names stay `''` rather than becoming `null` in v1.** `null` would have meant
  a second copy of every section struct with pointer names, which is the second
  reduction of the panel's state `buildShareReading` exists to be the only one
  of. `vp.name()` gives pages the `null`, and the `counts` fixture makes a page
  face it.
- **Parameters are an array in the manifest**, not an object: the settings form
  draws them in the author's order, and a JSON object's order does not survive
  a Go map.
- **The Preview is a detail block of the side panel** (`page`, beside `repo`),
  not a pane tab. A tab would be on every project; the block appears only on a
  page's.
- **A trial only goes on a link that already draws the page.** One that could
  take over a screen drawing something else would have to remember what that
  was to give it back.
- **CORS moved into middleware** after `pages-check` showed a revoked wall
  saying "reconnecting" (§3).
- **Page files carry CORS too**: a page's fetch of its own fixture is
  cross-origin from an opaque origin, measured failing without it.
- **`shot` prefers the headless shell** and asks before dropping the browser's
  own sandbox (§6).

## 13. Boards removed

§10 said boards stay until it was clear whether pages replaced them. It was: the
board was the thing somebody meant to delete, and a settings section with a
board editor, thirty presets and a "shows: board or page" select on every link
was the confusion that settled it. Removed: the editor and its canvas, the
presets, the widget registry, `GET /api/share/{token}/dashboard`, the catalogue
and preview settings routes, the board fields on link requests, the SPA's
dashboard root, `board-check` and the board styles.

**Links already handed out keep working.** `Server.ConvertBoardLinks` runs at
startup, before the listener, over every link that draws no page. The stored
board is read once, raw, to pick the closest template: repository widgets
(`output`, `codechurn`, `spentmade`, `repoprojects`, `prs`) at least as many as
spend widgets → `built`; any spend widget → `spend`; the `phone` preset →
`glance`; anything else → `wall`, which is what the default board was. One page
per owner and template is scaffolded under `<data dir>/pages/page-<slug>`,
published as v1, and each link is pointed at it in one statement that also
empties the board column. Detail, scope, remark and expiry are untouched,
because they are the disclosure and the drawing never was. Each conversion is
audited as `share.converted`. A second start finds nothing to do; a failure is
logged, leaves that link answering "no longer works", and is retried on the next
start.

**A dead address stops being the panel.** `/share/<token>` for an unknown token
used to be handed to the SPA, which asked the dashboard route and showed a
revoked state — and meant a stranger holding a dead address was served the
panel's bundle, one click from its sign-in page. With no dashboard left to draw,
it now answers `404` with a static bilingual page saying the link no longer works
(or `503`, "unavailable", when the database cannot be read), under
`default-src 'none'; sandbox`.
