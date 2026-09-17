# Working on vibepanel

A web console for running many parallel coding-agent sessions. tmux keeps the
processes alive; the browser owns everything about how they are organised,
named, sorted and surfaced.

Read `docs/design.md` for the decisions behind the shape of this before
changing anything structural; `README.md` describes the product and points
there. `docs/build-log.md` is the chronological record of what was built and
what went wrong; `docs/runbook.md` is where to look when a running deployment
misbehaves.

## Red lines

Each of these exists because the alternative broke something real.

1. **Never touch a tmux socket other than the configured one.** The panel runs
   with `-L <socket>` (default `vibepanel`) and its own `-f` config. Users run
   this next to an existing tmux or zellij setup with weeks-old sessions in it.
   A stray `tmux kill-server` without `-L` ends someone's week.

2. **Never let the panel own a PTY that a session's process is a child of.**
   Processes belong to the tmux server. The moment a session's lifetime depends
   on the Go process, `systemctl restart vibepanel` becomes destructive and the
   entire premise of the project is gone.

3. **`internal/session/state.go` is the only definition of the state enum.**
   Three things mirror it, so changing the enum tells you what else to change:
   the TypeScript constants in `web/src/protocol/wire.ts`, which are
   hand-written and compared against `AllStates`; the SQL ordering in
   `internal/store/sessions.go`, which mirrors `State.SortWeight`; and the state
   strings `internal/hooks` writes into the reporter script, the hooks merged
   into `~/.codex/hooks.json`, the Kimi Code `[[hooks]]` blocks appended to
   `~/.kimi-code/config.toml`, the zcode `hooks.events` merged into
   `~/.zcode/cli/config.json` and the block merged into
   `~/.claude/settings.json`.

   This said "two things" until the third was looked for. The first two are
   pinned by tests. The third was not, and it is the one with no type system on
   either side: `internal/hooks` does not import `internal/session` at all, so
   every state it emits is a bare literal, and the strings travel out of the
   repository into files the panel does not own.

   Drift there is silent in every direction at once. The server rejects an
   unknown state (red line 6); the reporter script suppresses its own failures
   on purpose, because a hook that makes an agent wait is worse than a missed
   update; and the settings page reports hooks as installed because it reads the
   agent's configuration file rather than whether anything ever arrived. The
   result is no error anywhere, a settings page saying it is fine, and every
   session quietly back on the heuristic. The runbook records that same symptom
   for a panel bound to one interface, from an unrelated cause.

   This used to say the TypeScript was generated. It never was: there was no
   generator and no generated file, so the rule protected nothing while
   reading as though it did.

   The same file is now pinned field by field, not only for the enum:
   `TestTypeScriptRowsMatchWhatIsSent` marshals `Session`, `Project`, `Note`,
   `Todo` and `AuditEntry` and compares the keys against the interfaces in
   `wire.ts`. The drift it catches is silent in the direction that matters,
   because the data arrives from `JSON.parse` cast to the interface: a field the
   server has stopped sending is still declared, still type-checks, and is
   `undefined` at runtime.

4. **Colour is never the only carrier of meaning.** Session states are
   distinguished by shape as well as hue (circle / triangle / check). People
   read this panel at 2am on a phone in a dark room.

5. **Theme tokens are redefined under `[data-theme]` and media queries; component
   styles are not.** Defining a component's colours inside a theme block is the
   classic cause of white-on-white after a theme switch.

6. **Validate anything that arrives from a hook.** Hook payloads are HTTP
   requests shaped by whatever the user put in their agent config. An
   unvalidated state string lands in the database and renders as nothing.

7. **Exact-match tmux targets, always: `=name:`.** tmux resolves targets by
   prefix by default, so `-t vp_ab` also matches `vp_abcd`. Use the helpers in
   `internal/tmux`, never hand-built target strings.

8. **A share token is narrowed by its route, never by a flag.**
   A share token reaches exactly five routes: three `GET`s —
   `/api/share/{token}/v1/snapshot`, and a share page's files at
   `/share/{token}` and `/share/{token}/*` — and one write with its preflight,
   `POST` and `OPTIONS /api/share/{token}/v1/actions/{name}`.
   `TestAShareTokenReachesOnlyTheseRoutes` walks the router and fails on any
   route, method or path under either prefix that is not that list.
   `share_links` is a table `currentUser` does not consult. That is what makes
   a share token presented as a cookie or a `Bearer` header an unknown string
   that every authenticated route already answers 401 to.

   **The one write** is a visitor action (`docs/page-backend.md` §6): a kiosk's
   *Vote* button, a guestbook. It changes one page's own data, or runs that
   page's `server.js`, and nothing else in the panel. It runs only when every
   one of these holds, each checked in `handleVisitorAction` and each with a
   test that removes it: the link is **interactive**, the panel allows visitor
   writes, the action is declared for visitors in the version the link draws,
   the rate limits pass, and the payload is exactly the action's input.

   That `interactive` is a per-link switch reads like the flag this rule
   forbids, and it is not, for a reason worth keeping straight. The flag this
   rule is about is a `readOnly` bit that *authenticated* handlers would have
   to check — hundreds of them, where the one that forgets hands the panel to
   whoever holds a link. `interactive` widens nothing: the routes a token
   reaches are the five above whatever it is set to, and it is read in the one
   handler that exists only for share tokens, where a missing check costs one
   page's declared data and not the panel. It narrows a route; it does not
   stand in for one. Default off, audited, and refused on a locked link.

   **Admin pages** are a second capability on their own prefixes,
   `/page-admin/{grant}/…` and `/api/page-admin/{grant}/v1/…`, minted by
   `/pages/{pageID}/admin/` from the session cookie. A grant is in
   `page_admin_grants`, which `currentUser` does not consult either; it is
   bound to the session that minted it (the lookup joins `auth_sessions`, so
   signing out ends it), lives eight hours and reaches one page's admin API.
   `TestAnAdminGrantReachesOnlyTheseRoutes` is its list, and
   `TestCredentialsDoNotCrossBetweenSurfaces` shows a grant and a share token
   each refused on the other's routes, a grant refused as a cookie or a
   `Bearer`, and an API token unable to mint one.

   This said "exactly one `GET`" until share pages, and the list changed on
   purpose rather than by drift: the snapshot is the one redaction restated as
   a published contract (it replaced the board's dashboard route when boards
   were removed), and the page files are HTML the owner published, served
   inside `sandbox allow-scripts` with a `connect-src` that names that token's
   snapshot and nothing else. A token that resolves to nothing gets a static
   "no longer works" page on `/share/{token}`, never the panel's SPA: the
   bundle is the sign-in page, one click from the door the link exists so
   nobody needs.
   `pages-check` removes each of those two layers in turn and watches a page
   become the owner — cookie, storage, API, terminal socket — so neither is
   decoration. `docs/share-pages.md` is the design.

   Two ways to undo all of it, and both look like ordinary edits. Adding a
   route under `/api/share/{token}` or `/share/{token}` widens the capability
   by one line; the test's list is where that decision is made, out loud.
   Teaching `currentUser` about `share_links`, to "reuse the auth path", turns
   every handler in the panel into one that has to check a `readOnly` flag, and
   the handler that forgets is the one written next.

   A share page is the owner's HTML; it is not a way in. No parameter reaches
   a query: the manifest chooses among the snapshot's fixed sections
   (`pages.Needs` — switches and bounded day counts) and parameters are only
   echoed. The server-side pieces a page may declare — data, `server.js`,
   sources, actions — are the owner's declarations, published with the page:
   a visitor supplies a payload checked against a schema, never code, never a
   key the page did not declare. `server.js` runs in a fresh goja runtime per
   call with no modules, network, files or timers, under a time budget.
   `allow-same-origin` never appears beside `allow-scripts`, on a share page or
   an admin page.

   The redaction is the same shape: `internal/httpapi/share.go` restates the
   fields it discloses rather than embedding `sysmon.Sample` or `store.Session`,
   so a field added to either is *not* disclosed by default. Paths, `cwd`,
   command lines, tmux names, the hostname and the panel's real ids are never
   sent, in any mode.

   The pressure this rule has actually come under, once, was not an attack: it
   was "I should not have to walk to the wall and log in to change the layout".
   The obvious answer is a `PATCH` here, one line, obviously correct in review.
   The right answer was that the person who wants to change it is not at the
   screen. They are on a laptop, signed in, so what a screen shows — its page,
   version, settings, name — is changed through the settings routes and the
   wall picks it up on its next poll, because every poll re-reads the row. The
   whole live-update feature cost the share surface nothing. If the next
   request sounds like it needs a write here, ask first where the person making
   the change actually is. The same answer covers "let me see what that link
   shows": the token is hash-only, so the owner's session mints a
   fifteen-minute copy of the link (`POST /api/settings/shares/{id}/view`)
   rather than anything under the token learning to be read back.

   Two things a viewer *does* send, on the query string of the snapshot: an
   opaque per-tab id and its viewport, for the owner's "how many screens have
   this open". They are recorded in process memory and never read back, and
   nothing a viewer sends decides anything the response carries —
   `TestWhatAViewerSaysAboutItselfCannotChangeTheSnapshot` and
   `TestWhatAPageSendsCannotChangeItsSnapshot` are what say so. A page's
   sections come from the version its owner published, not from the request.

   The surface now **reads working trees**, which it did not when this rule was
   written, so two sentences that used to be simple are not:

   - What it reads is `git log --shortstat`, and that is the disclosure
     decision rather than a parsing convenience. `--numstat` is a line per
     changed path, so it would carry every filename in somebody's repository
     through this process on the way to a wall; `%s` would carry the commit
     messages. Counts are the only thing asked for, so counts are the only
     thing that can leak. Paths, filenames, branch names, subjects, shas and
     authors are refused at both detail settings, and
     `TestTheActivityReadAsksForATimestampAndNothingElse` pins the argument
     list rather than trusting the parser.
   - It can cause **one outbound request**, to github.com, and four things must
     be true at once (pull requests asked for in the page's manifest, a
     project-scoped link, `names` mode, and a token in the environment) behind a cache that
     refreshes at most once per repository per five minutes and stops entirely
     when nobody is looking. `internal/git/warm.go` is where that is enforced;
     the thing that may not be added to it is a ticker.
   - A page's **sources** cause outbound requests too, and only to hosts the
     owner approved for that page, over https, to addresses checked after
     resolution (no loopback, private, link-local or metadata address, and the
     dialled address is the checked one), with no redirects, bounded in size
     and time, in the background and only while something is watching.
     `internal/httpapi/pagesources.go` is the guard; `publicAddr` is the list.

   Neither may be read on the request goroutine. A wall polls every two seconds
   forever, so a `git log` on that path is a fork per project per poll, and the
   poll takes what the background refresh has already produced and reports its
   age, and "not counted yet" is a distinct answer from zero.

   **The chat bridge has two more doors of the same shape.**
   `POST /api/chat/hooks/{kind}` is where an IM calls back; it is
   unauthenticated at the panel and the adapter verifies every request itself
   (signature, verification token). `GET /api/chat/tools/*` is what the
   advanced mode's agent may read: five `GET`s, a token minted per process
   and never stored, refused everywhere else and pinned by
   `TestAChatToolsTokenReachesOnlyTheseRoutes`. The session views there
   restate their fields; handles, not ids. Nothing an agent printed reaches
   the write path: the intent translator has no tools, the question answerer
   has only these, and every write from either waits for the person's `ok`.

9. **The sessions' memory limit is never on a cgroup the panel is in.** One
   session filling a shared limit evicted the panel's own pages with everything
   else, and the panel stopped answering exactly when there was something to
   look at. Sessions live in a `vibepanel-sessions*.scope`; the panel's unit holds
   the panel. And **never enable controllers on a cgroup systemd spawns into**:
   that was the first design, and `systemctl restart vibepanel` failed with
   EBUSY while any session lived. `docs/design.md` has both measurements.

## Conventions

- **Comments explain why, and what breaks otherwise.** Not what the line does.
  If a line looks arbitrary, the comment should say which failure produced it.
- **Go**: `chi` for routing. No gin/echo/fiber. `CGO_ENABLED=0` must keep
  working, which rules out any dependency needing cgo, including mattn/sqlite3.
- **Frontend**: React + Vite + TypeScript `strict` + Tailwind v4 + `lucide-react`.
  No component library, no state library: `useState`/`useReducer` plus fetch and
  one WebSocket. npm and `package-lock.json`. ESLint flat config, no Prettier.
  Two-space indent, single quotes.

  **Inside a panel or a dialog, a responsive variant is a container query**
  (`@container` plus `@3xl:`), never `sm:`/`lg:`. The settings modal's body is
  about a third of the window, so a `lg:` rule there fires roughly a thousand
  pixels early: a `lg:grid-cols-[1fr_20rem]` in the since-removed board editor
  split 540px of real space into a 208px canvas beside a 320px palette on every
  desktop, and the whole sharing page was 「排版乱、错位」 from that one mistake
  repeated. The Sharing list (`components/pages/Sharing.tsx`,
  `PageLinks.tsx`) is built on `@container` for that reason, and
  `render-check` measures that it fits the dialog at three widths.
- **Tests**: Go standard `testing`; `vitest` on the frontend. The tmux wrapper
  is tested against a real tmux on a throwaway socket rather than a mock. The bugs
  worth catching there are tmux's, and a mock reproduces none of them.

- **The browser checks are where most of the bugs have been found.** `make
  check` is the fast gate and never starts a browser; `make verify` runs
  everything, two at a time, in about twenty minutes. In between:

  | | |
  |---|---|
  | `make panes-check` | the side panel's pane layout: drag, drop, merge, restore. No binary, no tmux, ~20s |
  | `make first-run-check` | the setup wizard and the first project — every other check reaches past them |
  | `make render-check` | the largest: layout, states, arbitration, panels, mobile, clipboard, passkeys |
  | `make stress-check` | wide characters, full-screen programs, scrollback, floods, dropped sockets |
  | `make restart-check` | kill the backend; the sessions and the login must outlive it |
  | `make scale-check` | two dozen sessions: snapshot size, sidebar reachability, poller |
  | `make resources-check` | the Resources tab and a session that really runs out of memory: isolation, modes, the question across the console, ending a process from it, the countdown, the panel answering throughout, layout at three widths. Needs a user manager for the pressure half |
  | `make isolation-check` | sessions moved into a scope of their own against real systemd 249, 252 and 259 as PID 1: fresh system unit, upgrade from the old layout, a failing root step, a user unit. Needs docker |
  | `make chat-check` | the Chat page: a card per adapter, a saved token starting a channel, the 飞书 handshake, rules and their preview, the key table, the tools door, the deep link, layout at three widths in both themes and languages |
  | `make pages-check` | share pages: every escape from inside a sandboxed page, in a signed-in browser; the editing loop through the UI; every template × screen × fixture |
  | `make tls-check` | its own TLS: wss, the Secure cookie, swapping a certificate |
  | `make release-check` | build the archives and run one from a throwaway HOME |
  | `make sudo-check` | the elevated upgrade against real sudo 1.9 and sudo-rs, in containers, down every sudoers variant: a password rule, NOPASSWD for everything and for the upgrade alone, a password rule for the upgrade alone, rootpw, targetpw, requiretty, the lecture, an account sudoers does not mention. Needs docker |
  | `make install-check` | both installers down every branch: the one-liner against a local HTTP server (checksums, platforms, a tampered archive), then `deploy/install.sh` — tmux missing/old, six package managers, Linux and macOS, user unit and system unit, root and no root, no systemd at all, the refusal to install both, and the first account |

  Run the one that covers what you touched, and `verify` before anything
  structural. A change that only passes `check` has not been looked at.

  **`verify` runs them concurrently, and two is the measured number rather than
  a timid one.** It is one `make -j` invocation because seven of these depend
  on `build` and `web` is `.PHONY`: separate `make` processes would be separate
  vite builds writing `internal/webui/dist` while the binaries they produced
  were being served out of it. `release-check` is excluded from that invocation
  entirely — `build-release.sh` runs `npm ci`, which deletes `node_modules`
  from under anything holding a playwright — so it goes last, alone. At eight,
  every browser check failed: these wait on budgets tuned against an idle
  machine, and this box also runs the panel under test, whose cgroup was at
  19.5 GiB of a 26 GiB max. Raise it with `VERIFY_JOBS` on a machine that is
  not also somebody's panel; there is little to gain, because 18 of the 20
  minutes are one check.

  **Do not edit tracked source while a check is running.** `assertFreshBuild`
  refuses rather than measuring the previous build, which is right and is also
  a wasted twenty minutes.

  **A frontend change needs `make build` before it is committed.** `make check`
  runs `tsc -b` and eslint and vitest, none of which write
  `internal/webui/dist` — so a commit made after a green `check` carries the
  *previous* bundle, and every browser check still passes because they all
  rebuild first. `head-check` is the only thing that looks, and it looks
  nineteen minutes into `verify`.
- **Every one of those builds from the working tree, so none of them can tell
  you whether what you *committed* works.** They were not the same thing: HEAD
  did not compile for some time, a caller committed with the method it calls
  left untracked, while every check passed. `make head-check` builds a clean
  worktree at HEAD and runs the fast gate in it, which is what somebody cloning
  the repository gets. It takes a ref, so `scripts/head-check.sh <branch>`
  works too.

  Commit whole changes. `git add <path>` for some of the files and not the
  others is how that happened.
- **The installer is two files, and the split is deliberate.** `install.sh` at
  the repository root is the network bootstrap the one-liner pipes into `sh`:
  POSIX `sh`, no bash anywhere in it, and its whole job is to fetch a release,
  verify it against `SHA256SUMS` and hand over. `deploy/install.sh` installs
  from an unpacked archive and knows about tmux, services and everything else;
  it is bash, and bash 3.2, because macOS still ships that one. Neither may
  grow a `--password <value>` flag. `cmd/vibepanel/account.go` says why.

  Both of them speak English and 简体中文, and each holds its strings in one
  `case` between `strings: begin` and `strings: end`, behind `m <key>`. Add a
  key there and nowhere else, with both languages filled in: `make
  install-check` walks every arm in both files and fails on an empty side, on a
  pair whose substitutions disagree, and on a key nothing defines. What is in
  the table is what a person reads while deciding something: the questions, the
  plan, the errors that say what to do next, the summary, `--help`. The `verb +
  path` trace lines during the install are deliberately still English; the
  build-log entry "An installer that speaks Chinese" says why. Substitutions are
  `%1$s`, never a bare `%s`, and `m` expands them itself: bash's builtin printf
  rejects positional specifiers outright.
- **Commits**: English Conventional Commits (`feat(tmux): ...`). No
  `Co-Authored-By` trailers.
- **Docs**: English. Keep `docs/build-log.md` current as you go; a decision that
  is only in a commit message is a decision nobody will find.

## Layout

```
cmd/vibepanel/      entrypoint; also the admin CLI (serve, project, session,
                    hook, service, account, doctor, version)
internal/tmux/      tmux CLI wrapper + the embedded vibepanel.conf
internal/session/   state enum (source of truth) and, later, the session manager
internal/store/     SQLite schema, migrations, typed queries
internal/config/    flags, environment, validation
internal/id/        opaque id generation
internal/cgroup/    cgroup v2 files and the systemd scope the sessions live in
internal/resources/ placement, budget and the memory question (docs/design.md,
                    "Sessions run in a scope of their own")
internal/chat/      the chat bridge: sessions on a phone. One package per IM
                    under it (telegram, feishu, weixin), the PNG renderer
                    (shot) and the advanced mode's runner (assistant)
web/                frontend
```
