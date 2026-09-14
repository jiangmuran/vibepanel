# Share pages: an SDK for the read-only link, and pages people write themselves

A share link used to draw one thing: a board, assembled from a fixed registry of
widgets. It can now draw a **share page** instead — HTML the owner wrote, usually
by asking an agent in one of the panel's own sessions to write it, drawing the
same redacted snapshot through a versioned API and a small SDK.

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
 ~/vibepanel-pages/lobby/  ──publish──▶     (immutable, in SQLite)
        ▲                                         │
        │ Preview pane (sandboxed iframe          │ share_links.page_id
        │ on a 15-minute preview link)            │
        └───────────────────────────────  GET /share/{token}/            ──▶  index.html
                                          GET /share/{token}/{path}      ──▶  its files
                                          GET /share/{token}/vibepanel.js ─▶  the SDK, from the binary
                                          GET /api/share/{token}/v1/snapshot ◀── the SDK polls
                                                  │
                                          buildShareDashboard (the one redaction)
```

| layer | what it is | where |
|---|---|---|
| **Data** | `GET /api/share/{token}/v1/snapshot`: the redacted snapshot, sections chosen by the page's manifest | `internal/httpapi/sharepage.go` |
| **SDK** | `vibepanel.js` + `vibepanel.d.ts`: polling, connection state, clock, fixtures, safe text, the badge, the Preview's instruments | `internal/pages/sdk/` |
| **Page** | static files plus `vibepanel.json`, stored as immutable versions | `internal/store/pages.go`, `internal/pages/` |

A board is still a board. A link draws a board until it is pointed at a page,
and back again; the board editor is unchanged.

## 2. What stays true

The four properties at the top of `internal/httpapi/share.go` carry over:

1. **It is a capability.** 32 random bytes in the URL, a SHA-256 in the table.
2. **It is read-only because of where it is registered.** A share token reaches
   four `GET`s — the dashboard, the v1 snapshot, a page's files at
   `/share/{token}` and `/share/{token}/*` — and
   `TestAShareTokenReachesOnlyTheseRoutes` walks the router and fails on
   anything else under either prefix. `currentUser` still never consults
   `share_links`. Red line 8 in `AGENTS.md` says this and says why the count
   went from one to four.
3. **It discloses less than the panel knows.** The v1 snapshot is the same
   `share*` structs through the same builder, restated as `shareSnapshot`. A page
   cannot add a field: the page is HTML and the fields are Go.
4. **A page can only subtract.** The manifest names sections, and
   `Manifest.Board()` compiles them into a `store.Board` of the widgets that
   need exactly those sections, which the existing builder reads. There is no
   second decision about what to compute, so a page has no vocabulary a board
   lacks. `detail` and `scope` stay on the link, fixed by a signed-in owner.

**Nothing a viewer sends decides anything the response carries** holds for the
snapshot as it does for the dashboard: sections come from the stored version,
and `TestWhatAPageSendsCannotChangeItsSnapshot` puts `sections=…&detail=…` on
the query string and compares.

## 3. The data: `v1/snapshot`

The response is the dashboard's fields minus the board's own (`board`,
`locked`), plus `v`, `page` (`{id, version, draft}`, or `null` on a board link),
`sections` and `params`. `docs/api.md` has the example.

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
- **Names are `''` under `counts`**, as on the dashboard; `vp.name(row)` turns
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
jitter and backoff, pausing while hidden; the connection states the dashboard
already had (`connecting`, `live`, `reconnecting`, `disconnected`, `revoked` —
terminal on `401` and `410`); the panel's clock from `at`; reloading after a
random 0–5 s when the page or its version changes; fixtures under `?fixture=`;
and, only when framed or under `?shot=1`, the Preview's instruments (§6).
`web/src/sdk.test.ts` runs it against a fake window.

## 5. Pages

**Storage.** `share_pages`, `share_page_versions` (immutable; `candidate` marks a
trial's), `share_page_blobs` (content-addressed; publishing one changed file
stores one file), `share_page_files`. `share_links` gains `page_id`,
`pin_version`, `pin_until`, `params` and `purpose` (`''` or `preview`). In the
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
refused, bounds from the board's registry) and lenient on the way out
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

**Start.** Settings → Sharing → Share pages: a name, a template (`blank`, `wall`,
`spend`, `built`, `glance`) or an existing directory, and "start an agent
session". The directory (default `~/vibepanel-pages/<slug>`) is scaffolded —
the template, `AGENTS.md` and `CLAUDE.md`, the SDK and its types, fixtures, a
`.gitignore` — made a git repository with one commit where git and an identity
allow, and registered. Then the panel adds it as a project, opens the ordinary
launch picker, and three seconds after the agent starts types a first line at
its prompt without pressing Enter. `vibepanel page init` does the same from a
shell, less the session.

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
  already draws the page to it for 5–30 minutes; **Keep** publishes it,
  **Revert** or doing nothing puts the screen back. The revert is decided on
  read (`ShareLink.ResolvePageVersion`), so nothing has to run and a restart
  cannot leave a trial on a wall.
- On a narrow pane, a one-line box sends to the session with Enter, because the
  person typing is the person sending.

**Parameters** are edited per link in Settings, under the link, and reach an
open page within a poll without a reload.

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
| T3 | a page discloses more than its link | the one redaction; the manifest compiles to a board; `detail`/`scope` on the link | — | `TestAManifestCompilesToExactlyTheSectionsItNames`, `TestTheSnapshotAnswersToTheTokenAlone` |
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

Audit events: `page.created`, `page.published`, `page.rolled_back`,
`page.moved`, `page.deleted`, `share.page_changed`, `share.params_changed`,
`share.page_trial`. Preview links are not audited: one is minted per pane.

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

- **Boards as a page kind, and retiring the board editor.** A link draws a board
  or a page; existing links are untouched. Whether pages replace boards for the
  person using them is the thing to find out before deleting anything.
- **A separate origin for pages.** The response-header sandbox on the panel's
  origin is the boundary, as it is for directory previews.

## 10. Decisions taken

1. Red line 8 is a list of `GET`s pinned by a test, not "exactly one".
2. `Access-Control-Allow-Origin: *` on the share API, refusals included, never
   credentials.
3. The sandbox header on the panel's own origin is the boundary.
4. `scriptHosts` from a fixed list of two CDNs.
5. Boards stay.

## 11. Files

| | |
|---|---|
| `internal/pages/` | manifest, parameters, bundle reading, lint, scaffold, templates, the SDK |
| `internal/store/pages.go` | pages, versions, blobs; `share.go` for the link columns |
| `internal/httpapi/sharepage.go` | page files, the v1 snapshot, the CSP, the memo |
| `internal/httpapi/pagesadmin.go` | settings routes: pages, drafts, publish, previews, trials, fork, link pages |
| `internal/httpapi/sharefixtures.go` | fixtures, and shaping one to a page |
| `cmd/vibepanel/page.go` | `vibepanel page …` |
| `web/src/components/pages/` | Preview pane, page line, settings list, link editor, parameters form, frame messages |
| `web/scripts/pages-check.mjs` | the browser check |

## 12. Where the build departed from the plan

- **Names stay `''` rather than becoming `null` in v1.** `null` would have meant
  a second copy of every section struct with pointer names, which is the second
  reduction of the panel's state `buildShareDashboard` exists to be the only one
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
