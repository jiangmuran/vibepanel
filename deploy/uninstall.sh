#!/usr/bin/env bash
# Takes vibepanel off this machine: the service, the sessions, the hooks it
# wrote into other tools, the data and the binary.
#
#   ./deploy/uninstall.sh          # say what would go, change nothing
#   ./deploy/uninstall.sh --yes    # do it, copying the database out first
#
# The default is a dry run and that is not politeness. This kills every session
# on the panel's tmux socket, and whatever those sessions were doing is gone
# with them -- a long build, an agent halfway through a change. The list is
# printed so it can be read before that happens.
#
# bash 3.2, like deploy/install.sh: macOS ships that one and always will.
set -euo pipefail

# ── the socket, and the reason this file is careful about it ──────────────
#
# Red line 1 of AGENTS.md: never touch a tmux socket other than the configured
# one. People run this beside an existing tmux or zellij with weeks-old sessions
# in it, and `tmux kill-server` without `-L` ends someone's week. Every tmux
# call below carries `-L "$SOCKET"`, and the three refusals under it exist
# because an empty or wrong value here is the whole failure.
# `-` and not `:-`, so an explicitly empty value stays empty and hits the
# refusal below. With `:-` it falls back to "vibepanel" instead, which means
# somebody who set the variable to nothing on purpose gets the default socket
# killed and the guard under this line can never run at all.
SOCKET="${VIBEPANEL_TMUX_SOCKET-vibepanel}"
DATA="${VIBEPANEL_DATA_DIR:-$HOME/.local/share/vibepanel}"
ENV_FILE="${VIBEPANEL_ENV_FILE:-$HOME/.config/vibepanel.env}"
# BIN is resolved below, once the unit paths are known, because it is read out
# of the unit rather than assumed. See the note there.

GO=no
PURGE=no
PURGE_ARCHIVES=no
KEEP_DATA=no
LEFTOVERS=no
for a in "$@"; do
  case "$a" in
    -y|--yes) GO=yes ;;
    --purge) PURGE=yes ;;
    --purge-archives) PURGE=yes; PURGE_ARCHIVES=yes ;;
    --keep-data) KEEP_DATA=yes ;;
    --dev-leftovers) LEFTOVERS=yes ;;
    -h|--help)
      sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
      echo
      echo "  -y, --yes         actually do it"
      echo "      --purge       do not copy the database out first; remove older backups"
      echo "      --purge-archives  ...and the newest data archive, which --purge keeps"
      echo "      --keep-data   leave $DATA alone"
      echo "      --dev-leftovers  also unlink dead test sockets from this repo's test runs"
      exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

# An empty socket name makes every `tmux -L "$SOCKET"` below a command against
# the *default* socket, which is the one thing this must never be.
if [ -z "$SOCKET" ]; then
  echo "refusing: VIBEPANEL_TMUX_SOCKET is empty, which would mean the default socket" >&2
  exit 2
fi
case "$SOCKET" in
  default)
    echo "refusing: the socket is named 'default'. That is where somebody's own tmux lives." >&2
    exit 2 ;;
esac

# And refuse outright if this shell is inside the server it is about to kill:
# the script would take out its own terminal partway down and leave the rest
# undone, with no output saying where it stopped.
if [ -n "${TMUX:-}" ]; then
  here="${TMUX%%,*}"
  case "$here" in
    */"$SOCKET")
      echo "refusing: this shell is inside the tmux server on socket '$SOCKET'." >&2
      echo "Run it from outside the panel -- an ordinary terminal, or your own tmux." >&2
      exit 2 ;;
  esac
fi

say() { printf '  %-9s %s\n' "$1" "$2"; }
did() { printf '[ok  ] %s\n' "$1"; }

# How many of the three agents still point at the reporter.
#
# Counted from the files rather than taken from the exit status of `hook
# remove`, because a binary from before that subcommand existed treats `remove`
# as a stray argument, installs the reporter script, prints the snippets and
# exits 0. Nothing about that says no. The only honest test of "are the hooks
# gone" is to go and look.
hooks_present() {
  local n=0
  if [ -f "$HOME/.claude/settings.json" ] && grep -q vibepanel-report "$HOME/.claude/settings.json" 2>/dev/null; then
    n=$((n + 1))
  fi
  if [ -f "$HOME/.codex/config.toml" ] && grep -q vibepanel-report "$HOME/.codex/config.toml" 2>/dev/null; then
    n=$((n + 1))
  fi
  if [ -e "$HOME/.config/opencode/plugin/vibepanel.js" ]; then
    n=$((n + 1))
  fi
  printf '%s' "$n"
}

echo "vibepanel uninstall"
echo

# ── what is here ──────────────────────────────────────────────────────────
SESSIONS="$(tmux -L "$SOCKET" list-sessions -F '#{session_name}' 2>/dev/null || true)"
N=0
[ -n "$SESSIONS" ] && N="$(printf '%s\n' "$SESSIONS" | wc -l | tr -d ' ')"

RUNNING=no
if pgrep -f "vibepanel serve" >/dev/null 2>&1; then RUNNING=yes; fi

UNIT="$HOME/.config/systemd/user/vibepanel.service"
# The same test-only prefix deploy/install.sh and cmd/vibepanel/service.go take.
# Without it nothing can drive the system-unit shape without writing to the
# developer's own /etc, which is why that shape went unchecked here.
SYSUNIT="${VIBEPANEL_DESTDIR:-}/etc/systemd/system/vibepanel.service"
PLIST="$HOME/Library/LaunchAgents/io.github.jiangmuran.vibepanel.plist"

# Where the binary is. Asked of the unit, not guessed.
#
# A system install puts it in /usr/local/bin and a user install in ~/.local/bin,
# so a fixed ~/.local/bin finds nothing on the shape deploy/install.sh picks by
# default. Both destructive steps below are gated on `[ -x "$BIN" ]`: `hook
# remove` and `service uninstall` were skipped, the unit stayed installed and
# enabled with Restart=always, and $DATA -- holding the reporter script three
# agent configs still point at -- was deleted anyway, under a "── done ──" with
# no FAIL in it and exit 0.
#
# cmd/vibepanel/service.go learned this already, and says it better: ExecStart
# is a fact about this machine, and the defaults under it are only for a unit
# that cannot be read.
SYSBIN="${VIBEPANEL_DESTDIR:-}/usr/local/bin/vibepanel"
BIN="${VIBEPANEL_BIN:-}"
if [ -z "$BIN" ]; then
  BIN="$HOME/.local/bin/vibepanel"
  if [ -f "$SYSUNIT" ]; then
    BIN="$SYSBIN"
    EXEC="$(sed -n 's/^ExecStart=\([^ ]*\).*/\1/p' "$SYSUNIT" | head -1)"
    case "$EXEC" in /*) BIN="$EXEC" ;; esac
  elif [ ! -e "$BIN" ] && [ -x "$SYSBIN" ]; then
    # No unit left to ask -- removed by hand, or by a half-finished earlier run
    # -- and the binary a system install leaves is still sitting there. Finding
    # it is what keeps the hooks from outliving the reporter script they call.
    BIN="$SYSBIN"
  fi
fi

echo "what is here:"
[ "$RUNNING" = yes ] && say "running" "a vibepanel serve process" || say "running" "nothing"
say "socket" "$SOCKET  ($N session(s))"
[ -f "$UNIT" ]    && say "unit" "$UNIT"
[ -f "$SYSUNIT" ] && say "unit" "$SYSUNIT (needs root to remove)"
[ -f "$PLIST" ]   && say "agent" "$PLIST"
[ -e "$BIN" ]     && say "binary" "$BIN"
[ -d "$DATA" ]    && say "data" "$DATA ($(du -sh "$DATA" 2>/dev/null | cut -f1))"
[ -e "$ENV_FILE" ] && say "config" "$ENV_FILE"
# Everything the panel has left beside its own directories over time: the
# copies `service upgrade` takes before it swaps the binary, the copy the hooks
# installer takes before it edits somebody's agent config, and any stray
# `vibepanel.*` next to the installed binary. None of these are found by
# removing $DATA, and all of them are this project's.
# The newest data archive is not in this list.
#
# `--purge` and `--dev-leftovers` are unrelated -- one deletes backups, the
# other unlinks dead test sockets -- and passing both to clear some sockets
# deleted the archive the earlier run had just written, which was the only
# remaining copy of that database. The flags did exactly what they say. The
# problem is that the last copy of somebody's data is one word away from gone
# while they are thinking about something else, so it takes its own word:
# --purge-archives.
newest_archive() {
  ls -t "$HOME"/vibepanel-data-*.tar.gz 2>/dev/null | head -1
}

leftover_files() {
  local f keep
  keep="$(newest_archive)"
  [ "$PURGE_ARCHIVES" = yes ] && keep=
  for f in "$HOME/vibepanel-backups" "$HOME"/vibepanel-data-*.tar.gz \
           "$HOME"/.claude/settings.json.vibepanel-backup-* \
           "$HOME"/.codex/config.toml.vibepanel-backup-* \
           "$BIN".*; do
    [ -e "$f" ] || continue
    [ -n "$keep" ] && [ "$f" = "$keep" ] && continue
    printf '%s\n' "$f"
  done
}

HOOKS_BEFORE="$(hooks_present)"
[ "$HOOKS_BEFORE" != 0 ] && say "hooks" "$HOOKS_BEFORE of claude/codex/opencode point at the reporter"
LEFT="$(leftover_files || true)"
if [ -n "$LEFT" ]; then
  n="$(printf '%s\n' "$LEFT" | wc -l | tr -d ' ')"
  if [ "$PURGE" = yes ]; then
    say "leftovers" "$n backup(s) and stray file(s) -- --purge removes them"
  else
    say "leftovers" "$n backup(s) and stray file(s) -- kept; --purge removes them"
  fi
  printf '%s\n' "$LEFT" | sed 's/^/            /'
fi
echo

if [ "$N" != 0 ]; then
  echo "these sessions and everything running in them will be killed:"
  printf '%s\n' "$SESSIONS" | sed 's/^/  /'
  echo
fi

# The other things on this machine, named so it is on the record that they are
# not in the list above. This project's first constraint was that it sits beside
# an existing setup without disturbing it, and a teardown is where that gets
# tested for real.
echo "not touched:"
for u in ttyd-dash zellij-session; do
  if systemctl --user cat "$u" >/dev/null 2>&1; then
    say "service" "$u ($(systemctl --user is-active "$u" 2>&1))"
  fi
done
if command -v zellij >/dev/null 2>&1; then
  z="$(zellij list-sessions --short 2>/dev/null | tr '\n' ' ' || true)"
  [ -n "$z" ] && say "zellij" "$z"
fi
if [ -S "${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/default" ]; then
  say "tmux" "your own default socket"
fi
echo

if [ "$GO" != yes ]; then
  echo "Nothing has been done. Add --yes to do it."
  exit 0
fi

# ── do it ─────────────────────────────────────────────────────────────────

# The hooks first, while the reporter script and the binary are both still
# there. Doing this after removing the binary means editing three of somebody
# else's configuration files by hand.
# Not `|| true`. A binary from before `hook remove` existed answers this with a
# flag error, and swallowing that leaves the reporter wired into three other
# tools' configuration while the line underneath says it was removed. Those
# hooks then point at a script this run is about to delete, and every agent on
# the machine runs a missing file on every prompt.
if [ -x "$BIN" ] && [ "$HOOKS_BEFORE" != 0 ]; then
  "$BIN" hook remove 2>&1 | sed 's/^/       /' || true
  if [ "$(hooks_present)" = 0 ]; then
    did "hooks removed from claude, codex and opencode"
  else
    echo "[FAIL] $(hooks_present) of them are still there. This binary is probably"
    echo "       older than 'hook remove', which it treats as a stray argument."
    echo "       Take them out by hand before the reporter script goes:"
    echo "         ~/.claude/settings.json, ~/.codex/config.toml,"
    echo "         ~/.config/opencode/plugin/vibepanel.js"
    HOOKS_LEFT=yes
  fi
elif [ "$HOOKS_BEFORE" != 0 ]; then
  # No binary, so nothing here can edit those three files -- and the removal of
  # $DATA further down must not go ahead without them. The same ordering rule as
  # above, reached from the other side: this used to be a silent skip, and a
  # system install came out of it with three live hooks calling a reporter
  # script this run had just deleted.
  echo "[FAIL] no vibepanel binary was found, so the hooks are still there."
  echo "       Take them out by hand:"
  echo "         ~/.claude/settings.json, ~/.codex/config.toml,"
  echo "         ~/.config/opencode/plugin/vibepanel.js"
  HOOKS_LEFT=yes
fi

# The database, before anything kills the process holding it open. --purge
# skips this; without it there is a copy, because "I wanted the sessions back"
# arrives after the sessions are gone and not before.
if [ -d "$DATA" ] && [ "$KEEP_DATA" = no ] && [ "$PURGE" = no ]; then
  ARCHIVE="$HOME/vibepanel-data-$(date +%Y%m%d-%H%M%S).tar.gz"
  tar czf "$ARCHIVE" -C "$(dirname "$DATA")" "$(basename "$DATA")" 2>/dev/null || true
  did "copied the data to $ARCHIVE"
fi

# The service, through the binary that knows which kind is installed. It stops
# it, removes the unit and removes itself.
# Only when there is one. `service uninstall` says so plainly on a machine with
# no unit, and printing "service uninstalled" under that is the script
# contradicting the line above it.
if [ -x "$BIN" ] && { [ -f "$UNIT" ] || [ -f "$SYSUNIT" ] || [ -f "$PLIST" ]; }; then
  "$BIN" service uninstall --yes 2>&1 | sed 's/^/       /' || true
  # Looked at, not taken on trust -- the same reason the hooks are counted
  # afterwards. A binary older than the fix removed the *system* unit with
  # os.Remove, which is a permission error on root's file, and this printed
  # "service uninstalled" over the top of a unit that was still there and
  # still enabled.
  if [ -f "$UNIT" ] || [ -f "$SYSUNIT" ] || [ -f "$PLIST" ]; then
    echo "[FAIL] the unit file is still there:"
    for u in "$UNIT" "$SYSUNIT" "$PLIST"; do [ -f "$u" ] && echo "         $u"; done
    echo "       Remove it yourself, then: sudo systemctl daemon-reload"
    UNIT_LEFT=yes
  else
    did "service uninstalled"
  fi
elif [ -f "$UNIT" ] || [ -f "$SYSUNIT" ] || [ -f "$PLIST" ]; then
  # A unit and no binary to run `service uninstall` with. Left enabled with
  # Restart=always, it is a panel that comes back at the next boot over a data
  # directory this run removed.
  echo "[FAIL] no vibepanel binary was found, so the service is still installed:"
  for u in "$UNIT" "$SYSUNIT" "$PLIST"; do [ -f "$u" ] && echo "         $u"; done
  echo "       Remove it yourself, then: sudo systemctl daemon-reload"
  UNIT_LEFT=yes
fi

# Anything still serving, which is the case this whole repository is about: the
# panel is often run straight out of a working tree rather than as a service.
#
# The pids are read and filtered rather than handed to `pkill -f`. A `-f`
# pattern matches whole command lines, including the command line of whatever
# invoked this script -- a shell whose `-c` argument happens to mention
# `vibepanel serve` matches, and pkill kills it. That is not hypothetical: it
# is how this got written.
#
# So: everything from this process up to init is off limits, and so is this
# process itself.
mine=" $$ "
anc=$$
while [ "$anc" -gt 1 ]; do
  anc="$(ps -o ppid= -p "$anc" 2>/dev/null | tr -d ' ')"
  [ -n "$anc" ] || break
  mine="$mine$anc "
done
if pgrep -f "vibepanel serve" >/dev/null 2>&1; then
  for p in $(pgrep -f "vibepanel serve"); do
    case "$mine" in *" $p "*) continue ;; esac
    kill "$p" 2>/dev/null || true
  done
  for i in 1 2 3 4 5 6 7 8 9 10; do
    pgrep -f "vibepanel serve" >/dev/null 2>&1 || break
    sleep 0.5
  done
  did "stopped the running panel"
fi

# The sessions. `-L "$SOCKET"` and nothing else.
#
# Always attempted, not only when there are sessions to kill. A server with
# zero sessions answers `has-session` with a failure and is still a running
# process, so the guarded version skipped it and the line below then unlinked
# its socket -- leaving a tmux server alive with no way to reach it. One was
# found doing exactly that, an hour and a half after the install it belonged
# to. `kill-server` against a dead socket only prints, and that goes to
# /dev/null.
tmux -L "$SOCKET" kill-server 2>/dev/null || true
if [ "$N" != 0 ]; then
  did "killed the tmux server on socket '$SOCKET' ($N session(s))"
fi
rm -f "${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/$SOCKET"

# What is left: the files.
# Not while something still points into it. Hooks left behind referring to a
# reporter script that no longer exists means every agent on this machine runs
# a missing file on every prompt -- which the reporter's own design hides,
# because it suppresses its failures on purpose.
if [ "$KEEP_DATA" = no ] && [ -d "$DATA" ]; then
  if [ "${HOOKS_LEFT:-no}" = yes ]; then
    echo "[--  ] kept $DATA: the hooks above still point into it"
  else
    rm -rf "$DATA"
    did "removed $DATA"
  fi
fi
if [ -e "$ENV_FILE" ]; then
  rm -f "$ENV_FILE"
  did "removed $ENV_FILE"
fi
# service uninstall removes the binary, but only when a service was installed.
# Tried, not assumed: $BIN can now be /usr/local/bin/vibepanel, which is root's,
# and a bare `rm -f` on it ends this script under `set -e` two lines before the
# summary that would have said what was left behind.
if [ -e "$BIN" ]; then
  if rm -f "$BIN" 2>/dev/null; then
    did "removed $BIN"
  else
    echo "[FAIL] $BIN is still there; taking it out needs root:"
    echo "         sudo rm -f $BIN"
  fi
fi

# Dead sockets from this repository's own test runs, which a user never has.
#
# Only ones with no server behind them, and "no server" means the server is
# gone rather than the server having no sessions. `has-session` cannot tell
# those apart: a `tmux -L x start-server` with nothing in it answers exactly
# like a socket whose process died, and unlinking that one leaves a live server
# nobody can reach again. The message tmux prints is the thing that
# distinguishes them.
if [ "$LEFTOVERS" = yes ]; then
  dir="${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)"
  n=0
  if [ -d "$dir" ]; then
    # `vp*`, not `vp<something>-<something>`. The narrower pattern wanted a
    # hyphen and the test helpers do not all put one in: vpprobe1, vpalt624372
    # and a hundred like them sat there afterwards, reported as nothing to do.
    live=""
    for s in "$dir"/vibepanel* "$dir"/vp* "$dir"/flagtest "$dir"/leaktest; do
      [ -S "$s" ] || continue
      name="$(basename "$s")"
      [ "$name" = default ] && continue
      # The output is captured and matched, not piped into grep. `tmux
      # list-sessions` against a dead socket exits 1, and under `set -o
      # pipefail` that loses the pipeline regardless of what grep found -- so
      # every socket looked alive and nothing was ever unlinked. It read as
      # correct and passed by hand, where pipefail is not set.
      out="$(tmux -L "$name" list-sessions 2>&1 || true)"
      case "$out" in
        *"no server running"*) ;;
        *) live="$live $name"; continue ;;
      esac
      rm -f "$s" && n=$((n + 1))
    done
  fi
  did "unlinked $n dead test socket(s)"

  # Named, not killed. A live server is somebody's session however the socket
  # is spelled, and the difference between "a test left this behind" and "this
  # is yours" is not something a glob can see. The command to end them is
  # printed instead, per socket, so it is a decision and not a side effect.
  if [ -n "$live" ]; then
    echo "       these test sockets still have a server on them, with something in it:"
    for name in $live; do
      what="$(tmux -L "$name" list-sessions -F '#{session_name}:#{pane_current_command}' 2>/dev/null | tr '\n' ' ')"
      printf '         %-40s %s\n' "$name" "$what"
    done
    echo "       end one with:  tmux -L <name> kill-server"
  fi

  # And the servers whose socket was never in that directory.
  #
  # The Go tests point TMUX_TMPDIR at their own t.TempDir(), so a server they
  # leave behind has its socket under /tmp/TestSomething.../ and the sweep
  # above cannot see it. Six of them were four days old on the machine this was
  # written on, still running, invisible to every check.
  #
  # Both halves of the pattern are required: a `-L` naming one of this
  # project's sockets *and* a `-f` config under /tmp. An ordinary tmux has
  # neither, and a person's own server has no temp-directory config file.
  # `-f` under /tmp *or* under the panel's own data directory. The tests use a
  # t.TempDir(); the panel uses $DATA/tmux/tmux.conf, and a server of its own
  # left running with its socket already gone is the case that turned up.
  orphans="$(ps -eo pid=,args= 2>/dev/null \
    | grep -E "tmux +-L +(vibepanel|vp)[A-Za-z0-9_-]* +-f +(/tmp/|$DATA/)" \
    | grep -v grep || true)"
  if [ -n "$orphans" ]; then
    echo "       and these servers, whose socket is in their own temp directory:"
    printf '%s\n' "$orphans" | sed 's/^ */         /' | cut -c1-100
    printf '%s\n' "$orphans" | awk '{print $1}' | while read -r pid; do
      kill "$pid" 2>/dev/null || true
    done
    did "ended $(printf '%s\n' "$orphans" | wc -l | tr -d ' ') orphaned test server(s)"
  fi
fi

# Only under --purge. Everything in that list is a copy taken before something
# irreversible, which is exactly the kind of file to think twice about deleting
# -- so the default names them and leaves them, and removing them is a separate
# word typed on purpose.
if [ "$PURGE" = yes ]; then
  now="$(leftover_files || true)"
  if [ -n "$now" ]; then
    printf '%s\n' "$now" | while IFS= read -r f; do rm -rf "$f"; done
    did "removed $(printf '%s\n' "$now" | wc -l | tr -d ' ') leftover backup(s) and stray file(s)"
  fi
fi

echo
echo "── done ──"
echo "Left alone: your own tmux, zellij, ttyd, and everything under ~/projects."
if [ "${HOOKS_LEFT:-no}" = yes ]; then
  echo "STILL THERE: the hooks in the three agent configs. See the FAIL above."
fi
if [ "${UNIT_LEFT:-no}" = yes ]; then
  echo "STILL THERE: the service unit. See the FAIL above."
fi
if [ -n "${ARCHIVE:-}" ]; then
  echo "The data is in $ARCHIVE if you want it back."
fi
if [ "$PURGE" != yes ] && [ -n "$(leftover_files || true)" ]; then
  echo "Older backups are still in your home directory; --purge removes those too."
fi
if [ "$PURGE_ARCHIVES" != yes ] && [ -n "$(newest_archive)" ]; then
  echo "Kept: $(newest_archive) -- the newest copy of the database."
  echo "      --purge-archives removes that one as well."
fi
