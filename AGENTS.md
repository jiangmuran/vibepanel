# Working on vibepanel

A web console for running many parallel coding-agent sessions. tmux keeps the
processes alive; the browser owns everything about how they are organised,
named, sorted and surfaced.

Read `README.md` for the design and the decisions behind it before changing
anything structural. `docs/build-log.md` is the chronological record of what
was built and what went wrong; `docs/runbook.md` is where to look when a
running deployment misbehaves.

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
   Two things mirror it and both are pinned by tests, so changing the enum tells
   you what else to change: the TypeScript constants in
   `web/src/protocol/wire.ts`, which are hand-written and compared against
   `AllStates`, and the SQL ordering in `internal/store/sessions.go`, which
   mirrors `State.SortWeight`.

   This used to say the TypeScript was generated. It never was — there was no
   generator and no generated file — so the rule protected nothing while
   reading as though it did.

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
  | `make first-run-check` | the setup wizard and the first project — every other check reaches past them |
  | `make render-check` | the largest: layout, states, arbitration, panels, mobile, clipboard, passkeys |
  | `make stress-check` | wide characters, full-screen programs, scrollback, floods, dropped sockets |
  | `make restart-check` | kill the backend; the sessions and the login must outlive it |
  | `make scale-check` | two dozen sessions: snapshot size, sidebar reachability, poller |
  | `make tls-check` | its own TLS: wss, the Secure cookie, swapping a certificate |
  | `make release-check` | build the archives and run one from a throwaway HOME |

  Run the one that covers what you touched, and `verify` before anything
  structural. A change that only passes `check` has not been looked at.
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
