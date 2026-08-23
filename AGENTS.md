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
   The TypeScript constants are generated from it. The SQL ordering in
   `internal/store/sessions.go` mirrors `State.SortWeight` and there is a test
   asserting they agree — if you change one, the test tells you about the other.

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
