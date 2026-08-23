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

## 2026-08-23 — M2: terminals end to end

The browser can open a session, type into it, close the tab and come back to
find its scrollback intact, and watch the same session from two places at once.
A single static binary serves the whole thing.

Landed: `internal/session` (ring buffer, OSC scanner, attachment manager),
`internal/ws` (multiplexed protocol), `internal/httpapi` (REST, reconcile,
tmux poller), `internal/webui` (embedded frontend), and `web/` (React 19 +
Vite + Tailwind v4 + xterm.js 6).

### Size arbitration, and why the browser scales instead of reflowing

A desktop at 200×50 and a phone at 45×20 cannot both size the same tmux
session. Reflowing to the smaller one turns an agent's full-screen TUI into
confetti under the person actually using it.

The rule that shipped: the backend attaches exactly one tmux client per
session, so tmux itself never has to arbitrate. One viewer *controls* the grid
— whoever last typed — and everyone else renders at that grid and scales it
with a CSS transform. Typing claims control implicitly, which is what makes it
feel right on a phone: you start typing, and the session becomes phone-sized.
A passive viewer's resize is ignored rather than rejected, because a browser
window being resized is not an error.

### Bugs found by tests rather than by users

**`Done()` fired before the manager forgot the session.** The pump's cleanup
closed the channel and *then* removed the entry from the live map, leaving a
window where a caller woken by `Done()` still saw the session listed. Long
enough for a reconnect to attach to a corpse. Deregistration now happens first,
so the signal means what it says.

**The OSC terminator was being counted as an application bell.** Every OSC
sequence ends in BEL, and the shell sets its title on every prompt — so a naive
bell detector marks every session "waiting for you" several times a second.
The scanner consumes the terminator as part of the sequence, and there is a
test pinning it.

**Ring buffer: filling exactly to capacity was flagged as overflow.** That made
`Snapshot` trim a prefix of real output on a buffer that had lost nothing.

**Ring buffer: `trimPartialEscape` ate the first character of ordinary text.**
An empty parameter prefix counted as "all parameter bytes", so any line
starting with a letter looked like the tail of a CSI sequence. The trim now
requires at least one parameter byte, and the remaining ambiguity (digits then
a letter, which is genuinely indistinguishable from a truncated sequence) is
pinned as a documented false positive rather than pretended away.

### Frontend notes

Every version in the first `package.json` was wrong — guessed from memory
rather than looked up. xterm is on 6.0, not 5.6; lucide-react on 1.33, not
0.550; vite on 8.2. Worth remembering that this ecosystem moves faster than
recall.

The React 19 lint rules caught two real correctness problems: a ref written
during render (replaced with the `setSelected` updater form) and a state poll
that used `setInterval` (replaced with a self-scheduling loop, which also stops
requests piling up when the server is slow).

Polling `/api/state` every two seconds is a stopgap. Terminal output already
arrives over the WebSocket; the project and session lists should too, and that
loop should disappear in M3.

### Verified

- Full terminal round trip over a real WebSocket against a real tmux: subscribe,
  type `echo`, read the output back.
- Session survives the server being torn down and rebuilt.
- A slow viewer is dropped rather than stalling the pump that feeds everyone.
- `remain-on-exit`, replay after reconnect, and resize reaching tmux, all with
  `-race`.
- 13 MB static binary (`CGO_ENABLED=0`) serving the embedded frontend, with
  fingerprinted assets marked immutable and SPA fallback working.

## 2026-08-23 — what a real browser found

Added `web/scripts/render-check.mjs`: it boots the real binary against a
throwaway tmux socket, drives it with headless Chromium, and asserts on console
errors, failed requests, contrast ratios, theme behaviour, two-viewer sync and
replay. Every bug below came out of its first three runs. None of them were
visible to the Go tests, which had been passing throughout.

### Replay was typing into the shell

The ring buffer contains everything the application sent, terminal capability
queries included. A freshly created xterm answers those as it parses them — and
after a reload the answer goes to the shell, which types it at the prompt:

```
$ echo MARKER_ALPHA
MARKER_ALPHA
$ ^[[?1;2c^[[>0;276;0c
```

Every page reload was injecting junk into whatever the session was doing. If an
agent had been sitting on a confirmation prompt, a refresh would have answered
it with garbage.

Replay now travels as its own frame type (`FrameReplay`), and the client
suppresses everything the terminal wants to send while it parses it. Enumerating
the query sequences to strip would have been the fragile version of this fix;
suppressing responses catches the ones nobody thought of.

### Writing to a session was claiming the grid

`Live.Write` set the controller, on the theory that typing is a clear statement
of intent. It is not: xterm sends device-attribute and focus replies down the
same channel as keystrokes, so a viewer claimed the grid merely by loading the
page. The narrow second viewer in the harness took ownership on connect and then
reflowed the session on every window resize — 147 columns down to 13.

Fixing the immediate bug also settled a design question the plan had gotten
wrong. Even for genuine keystrokes, claiming the grid is not what the user
wants: glancing at a session on a phone and answering "y" to a prompt is the
mobile use case this panel exists for, and resizing the grid to 45 columns
underneath the desktop that is mid-edit is not part of that. Input and ownership
are now fully separate — passive viewers type freely, and taking the grid is an
explicit tap.

### Three more ownership bugs behind the first

- **Nobody owned a fresh session.** The first viewer was told it was passive,
  so it rendered at the stored grid and scaled it into a corner of a window it
  could have filled — while showing a "take control" button for a session
  nobody else was using.
- **A departing controller handed the grid to whoever was left.** Added as a
  fix, wrong on contact: refreshing your own page gave the grid to the phone
  across the room, which immediately reflowed everything and left the returning
  desktop stuck watching 13 columns. Now the grid freezes where it was and the
  returning viewer reclaims it on subscribe.
- **An unowned grid could be claimed by a resize.** Caught by the test written
  for the fix above. A phone rotating in a pocket would claim a session the
  desktop was about to come back to. Only the owner may resize; claiming is
  either arriving or tapping.

### The terminal palette lagged one theme change behind

React runs a child's effects before its parent's. `TerminalView` re-read the CSS
custom properties to rebuild the xterm palette before `App` had written
`data-theme`, so the largest surface on the page stayed white in dark mode. The
attribute is now written synchronously in the setter, before the state update.

### Contrast

Apple's own `secondaryLabel` (0.60 alpha) and `tertiaryLabel` (0.30) measure
3.44:1 and 1.73:1 against the frosted surfaces they sit on here. The second of
those is barely visible. Retuned to 5.2:1 and 3.0:1, and the labels that carry
information ("idle", the grid readout) moved off the tertiary token.

### Two smaller things

Shell sessions displayed their raw tmux id, because `deriveTitle` returned ""
for them — the fallback is now the directory the shell is in. And the selected
session was reset by every reload to whichever session had printed most
recently, so a session that prints constantly stole your place on each refresh.

### Harness bugs worth recording

Three of the first run's findings were the harness being wrong, which is worth
knowing about the next one: it compared a name against CSS-uppercased text, it
walked `<style>` contents and xterm's offscreen measurement nodes as if they
were visible text, and it forced the theme by setting the attribute directly —
which changes the CSS but not React's state, so it measured a combination no
user can produce. It also read a background tab, which Chromium throttles until
it stops painting.
