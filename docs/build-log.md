# Build log

Chronological record of what was built and what fought back. Newest last.

## 2026-08-23 — M1: skeleton, tmux wrapper, store

First milestone: the binary can create, list and kill sessions, and `tmux -L
vibepanel ls` shows them. No HTTP server yet.

Packages landed: `internal/version`, `internal/id`, `internal/config`,
`internal/session` (state enum only), `internal/tmux`, `internal/store`, and
the `cmd/vibepanel` CLI.

### tmux 3.6 segfaults on `window-size manual`

The design called for `window-size manual` plus explicit `resize-window`: one
authoritative grid size per session, set by the panel, immune to whatever size
a second viewer happens to have. That option kills the tmux server.

```
$ tmux -L probe -f <(echo 'setw -g window-size manual') new-session -d /bin/sleep 5
server exited unexpectedly
```

The server log ends inside `spawn_window()`:

```
spawn_window: [new-session/0x...], flags=0
spawn_window: s=$0 wl=none wp0=none idx=-1
spawn_window: name=none
<server dies>
```

Reproduced on tmux 3.6 with and without `-x/-y`, and with an explicit
`default-size`. Every other value of the option (`largest`, `smallest`,
`latest`) works.

**Resolution**: use `window-size latest`. It reaches the same place by a
different route — the panel attaches exactly one client per session, so
"whichever client was last active" is always the panel, and no second viewer
can shrink the grid because no second viewer is a tmux client at all.
`resize-window` was verified to still work under `latest` (80x24 → 140x45).

### tmux target matching is prefix-based

`-t vp_ab` also resolves `vp_abcd`. Left alone, a kill aimed at one session
would eventually land on another — silently, and only once two generated names
happened to share a prefix.

**Resolution**: all targets go through helpers that emit the exact-match form.
Session-scoped commands use `=name`, pane-scoped commands use `=name:`. The
distinction matters: `has-session -t '=name:'` on a missing session reports
"no current target" instead of "can't find session", because the `:` asks for a
window of a session that does not exist. `internal/id` also generates
fixed-length names so that even a forgotten `=` cannot alias.

### `pane_current_command` is eventually consistent

For roughly 200ms after `new-session` returns, the pane still reports `tmux` —
the fork has not exec'd the real command yet.

```
1st immediate: tmux    | after 200ms: sleep
2nd immediate: tmux    | after 200ms: sleep
3rd immediate: tmux    | after 200ms: sleep
```

**Resolution**: documented on `tmux.Info.Command`; the session manager polls
rather than caching the first reading. The naming logic in M3 must not take a
session's identity from a create-time snapshot.

### Session options that only apply at pane creation

`history-limit` is a session option consulted when a pane is created, so
setting it after `new-session` leaves the first pane on tmux's 2000-line
default — and the panel's cold-start replay silently loses most of the
scrollback.

**Resolution**: everything moved into `internal/tmux/vibepanel.conf`, loaded
via `-f` at server start. This also fixed a scope bug found the same way:
`set-clipboard`, `escape-time` and `focus-events` are *server* options, while
`window-size`, `allow-set-title`, `allow-passthrough`, `monitor-bell` and
`remain-on-exit` are *window* options. Six of the sixteen options were being
set at the wrong scope in the first draft.

### Verified

- Five tmux tests against a real tmux on a throwaway socket, including
  `remain-on-exit` preserving a crashed pane's output and `capture-pane -e`
  keeping SGR escapes (cold-start replay is worthless without colour).
- Store tests, including one asserting the SQL state ordering agrees with
  `session.State.SortWeight`.
- Isolation: the panel's socket shows zero foreign sessions; the pre-existing
  ttyd/zellij setup on this host was untouched throughout.
