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
   strings `internal/hooks` writes into the reporter script, the Codex `notify`
   line and the block merged into `~/.claude/settings.json`.

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
   session quietly back on the heuristic — the same symptom the runbook records
   for a panel bound to one interface, from an unrelated cause.

   This used to say the TypeScript was generated. It never was — there was no
   generator and no generated file — so the rule protected nothing while
   reading as though it did.

   The same file is now pinned field by field, not only for the enum:
   `TestTypeScriptRowsMatchWhatIsSent` marshals `Session`, `Project`, `Note`,
   `Todo` and `AuditEntry` and compares the keys against the interfaces in
   `wire.ts`. The drift it catches is silent in the direction that matters —
   the data arrives from `JSON.parse` cast to the interface, so a field the
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

8. **A read-only share token is narrowed by its route, never by a flag.**
   `registerShareRoutes` mounts exactly one `GET` below `requireShareToken`,
   and `share_links` is a table `currentUser` does not consult — which is what
   makes a share token presented as a cookie or a `Bearer` header an unknown
   string that every authenticated route already answers 401 to.

   Two ways to undo that, and both look like ordinary edits. Adding a second
   route under `/api/share/{token}` widens the capability by one line. Teaching
   `currentUser` about `share_links` — to "reuse the auth path" — turns every
   handler in the panel into one that has to check a `readOnly` flag, and the
   handler that forgets is the one written next.

   The redaction is the same shape: `internal/httpapi/share.go` restates the
   fields it discloses rather than embedding `sysmon.Sample` or `store.Session`,
   so a field added to either is *not* disclosed by default. Paths, `cwd`,
   command lines, tmux names, the hostname and the panel's real ids are never
   sent, in any mode.

## Conventions

- **Comments explain why, and what breaks otherwise.** Not what the line does.
  If a line looks arbitrary, the comment should say which failure produced it.
- **Go**: `chi` for routing. No gin/echo/fiber. `CGO_ENABLED=0` must keep
  working — that rules out any dependency needing cgo, including mattn/sqlite3.
- **Frontend**: React + Vite + TypeScript `strict` + Tailwind v4 + `lucide-react`.
  No component library, no state library — `useState`/`useReducer` plus fetch and
  one WebSocket. npm and `package-lock.json`. ESLint flat config, no Prettier.
  Two-space indent, single quotes.
- **Tests**: Go standard `testing`; `vitest` on the frontend. The tmux wrapper
  is tested against a real tmux on a throwaway socket, not a mock — the bugs
  worth catching there are tmux's, and a mock reproduces none of them.

- **The browser checks are where most of the bugs have been found.** `make
  check` is the fast gate and never starts a browser; `make verify` runs
  everything and takes about twenty minutes. In between:

  | | |
  |---|---|
  | `make panes-check` | the side panel's pane layout: drag, drop, merge, restore. No binary, no tmux, ~20s |
  | `make first-run-check` | the setup wizard and the first project — every other check reaches past them |
  | `make render-check` | the largest: layout, states, arbitration, panels, mobile, clipboard, passkeys |
  | `make stress-check` | wide characters, full-screen programs, scrollback, floods, dropped sockets |
  | `make restart-check` | kill the backend; the sessions and the login must outlive it |
  | `make scale-check` | two dozen sessions: snapshot size, sidebar reachability, poller |
  | `make tls-check` | its own TLS: wss, the Secure cookie, swapping a certificate |
  | `make release-check` | build the archives and run one from a throwaway HOME |
  | `make install-check` | both installers down every branch: the one-liner against a local HTTP server (checksums, platforms, a tampered archive), then `deploy/install.sh` — tmux missing/old, six package managers, Linux and macOS, user unit and system unit, root and no root, no systemd at all, the refusal to install both, and the first account |

  Run the one that covers what you touched, and `verify` before anything
  structural. A change that only passes `check` has not been looked at.
- **Every one of those builds from the working tree, so none of them can tell
  you whether what you *committed* works.** They were not the same thing: HEAD
  did not compile for some time — a caller committed, the method it calls left
  untracked — while every check passed. `make head-check` builds a clean
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
  grow a `--password <value>` flag — see `cmd/vibepanel/account.go` for why.
- **Commits**: English Conventional Commits (`feat(tmux): ...`). No
  `Co-Authored-By` trailers.
- **Docs**: English. Keep `docs/build-log.md` current as you go; a decision that
  is only in a commit message is a decision nobody will find.

## Layout

```
cmd/vibepanel/      entrypoint; also the admin CLI (serve, project, session, doctor)
internal/tmux/      tmux CLI wrapper + the embedded vibepanel.conf
internal/session/   state enum (source of truth) and, later, the session manager
internal/store/     SQLite schema, migrations, typed queries
internal/config/    flags, environment, validation
internal/id/        opaque id generation
web/                frontend
```
