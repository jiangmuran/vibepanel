#!/usr/bin/env bash
# Installs vibepanel. Interactive when there is somebody to ask, silent when
# there is not.
#
#   ./install.sh                    # ask, show the plan, then do it
#   ./install.sh --yes              # take the defaults, ask nothing
#   ./install.sh --yes --enable     # ...and start it now
#   ./install.sh --user             # the per-user service, even where root works
#   ./install.sh --help
#
# Run this from an unpacked release archive. The one-liner in the repository
# root (install.sh) is what fetches an archive and then runs this.
#
# Linux and macOS. On Linux this is a systemd service, system or user; on macOS
# a launchd LaunchAgent, which is the only service kind macOS has that is worth
# installing -- deploy/io.github.jiangmuran.vibepanel.plist says why.
#
# bash rather than sh, and bash 3.2 rather than 4: macOS still ships 3.2 and
# always will, so no associative arrays, no ${x,,}, no `declare -n`, and no
# bare `"${arr[@]}"` on a possibly-empty array under `set -u`.
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
# The archive layout is <dir>/vibepanel and <dir>/deploy/install.sh, so the
# binary is one level up from this script. Running it from the repo works too.
BIN_SRC="$SRC/../vibepanel"
[ -f "$BIN_SRC" ] || BIN_SRC="$SRC/../../vibepanel"

BIN_DIR="$HOME/.local/bin"
ENV_FILE="$HOME/.config/vibepanel.env"
WHO="${USER:-$(id -un)}"

# ── the overrides that exist only so this script can be tested ────────────
#
# scripts/install-check.sh drives every branch below, including the ones that
# write to /etc, call `systemctl enable --now`, and install packages. It runs
# on a developer's machine, which has other services on it and often a panel of
# its own. Running the real thing there to find out whether the script works is
# how you find out it works by breaking something that was already running.
#
#   VIBEPANEL_DESTDIR     a DESTDIR-style prefix in front of every system path,
#                         so the system unit is written into a temp directory.
#   VIBEPANEL_SYSTEMCTL   the systemctl to call. The check points it at a
#                         recorder, because `systemctl --user disable --now
#                         vibepanel` would otherwise reach the *developer's*
#                         own panel: the user manager reads the real $HOME, not
#                         the throwaway one this script was handed.
#   VIBEPANEL_ROOT_CMD    how to become root. Empty means "already are"; the
#                         literal `none` means "root is not available here",
#                         which is the fallback path and cannot otherwise be
#                         produced on a machine where sudo works.
#   VIBEPANEL_PLATFORM    linux or darwin, so the launchd half can be driven
#                         from Linux. The alternative is a macOS runner in the
#                         loop for every change to this file, which means the
#                         macOS half is checked by hand or not at all.
#   VIBEPANEL_LAUNCHCTL   the launchctl to call, for the same reason as
#                         VIBEPANEL_SYSTEMCTL.
#   VIBEPANEL_TMUX_BIN    which tmux to probe. Pointing it at a stub that
#                         prints "tmux 3.2" is the only way to exercise the
#                         too-old branch without downgrading the tmux the rest
#                         of this project's tests need.
#   VIBEPANEL_PKG_MANAGER pretend this package manager is the one installed, so
#                         the apt/dnf/pacman/zypper/apk/brew wordings can each
#                         be driven from a machine that has exactly one.
#   VIBEPANEL_PKG_RUNNER  a command put in front of the package install instead
#                         of running it. A check that really runs `apt-get
#                         install tmux` on the developer's machine is the thing
#                         all of this exists to avoid.
#
# None of them is documented outside this comment. They are not configuration.
DESTDIR="${VIBEPANEL_DESTDIR:-}"
SYSTEMCTL="${VIBEPANEL_SYSTEMCTL:-systemctl}"
LAUNCHCTL="${VIBEPANEL_LAUNCHCTL:-launchctl}"
TMUX_BIN="${VIBEPANEL_TMUX_BIN:-tmux}"
PKG_RUNNER="${VIBEPANEL_PKG_RUNNER:-}"

case "${VIBEPANEL_PLATFORM:-$(uname -s)}" in
  [Dd]arwin) PLATFORM=darwin ;;
  [Ll]inux)  PLATFORM=linux ;;
  *) echo "error: $(uname -s) is not a platform this installs on; Linux and macOS only." >&2
     exit 1 ;;
esac

# Linux paths.
USER_UNIT_DIR="$HOME/.config/systemd/user"
USER_UNIT="$USER_UNIT_DIR/vibepanel.service"
SYSTEM_UNIT="$DESTDIR/etc/systemd/system/vibepanel.service"
SYSTEM_UNIT_SRC="$SRC/vibepanel-system.service"

# macOS paths. The label is reverse-DNS because launchctl addresses jobs by it
# (gui/<uid>/<label>), so it is a name three other things have to agree on:
# this script, the plist, and `vibepanel service`.
MAC_LABEL="io.github.jiangmuran.vibepanel"
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST="$PLIST_DIR/$MAC_LABEL.plist"
PLIST_SRC="$SRC/$MAC_LABEL.plist"
MAC_LOG="$HOME/Library/Logs/vibepanel.log"

# tmux 3.3, and the reason is in internal/tmux: allow-passthrough arrived
# there. An older tmux does not refuse the embedded config -- it reports an
# unknown option, carries on with defaults, and silently swallows the sequences
# agent TUIs use for progress and notifications from then on. Every symptom is
# something not appearing.
TMUX_MIN_MAJOR=3
TMUX_MIN_MINOR=3

INTERACTIVE=auto
ENABLE=auto        # auto: ask, or restart a unit that is already running
KIND=              # user | system | agent; empty means "decide below"
MIGRATE=no
DO_TMUX=auto       # auto | no  -- --skip-tmux is the only way to say no
ASKED_SYSTEM=no    # was --system typed? macOS has to answer that, not ignore it

# The first account, if it is being created from here rather than in the
# browser. Empty means "the panel prints a setup token and you use the wizard",
# which is the path that existed first and still works.
ACCT_USER=
ACCT_STDIN=no
ACCT_FILE=
ACCT_ENV=

usage() {
  cat <<'EOF'
vibepanel installer

  ./install.sh                    ask what to install, show the plan, do it
  ./install.sh --yes              take the defaults, ask nothing
  ./install.sh --yes --enable     ...and start the service at the end
  ./install.sh --user             the per-user service, even where root works

  -y, --yes, --non-interactive  never ask; suitable for CI and curl | sh
      --interactive             ask even when stdin is not a terminal
      --enable                  start (or restart) the service when done
      --no-enable               only put the files in place
      --system                  Linux: the systemd *system* service. The
                                default wherever root is available, because it
                                is the only one that can lower OOMScoreAdjust
                                and the only one that is up before you log in.
      --user                    Linux: the systemd *user* service, which needs
                                no root. macOS: the LaunchAgent, which is the
                                only kind there and what --system gets too.
      --migrate                 if the other kind is already installed, remove
                                it. Without this the installer refuses rather
                                than leave two panels on one tmux socket.
      --skip-tmux               do not check for tmux, and never offer to
                                install or upgrade it

  The panel's first account. Without any of these, the panel prints a one-time
  setup token at startup and you create the account in the browser -- that path
  is unchanged and still works.

      --username <name>         create the first account as part of the install
      --password-stdin          read its password from this script's stdin.
                                Implies --yes: the prompts read stdin too, and
                                they cannot both have it.
      --password-file <path>    read it from a file, which is the safe way
                                through the one-liner
      --password-env <VAR>      read it from an environment variable
                                There is no --password <value>: that is a
                                password in your shell history and in `ps`.
  -h, --help

Interactive by default when stdin and stdout are both terminals.

tmux 3.3 or newer is the one prerequisite. If it is missing this offers to
install it with whichever of apt/dnf/pacman/zypper/apk/brew is on the machine,
and if it is too old it says exactly what that costs and offers the same. It
never assumes sudo works: where root is not available it prints the one command
for you to run and stops there.

Afterwards, `vibepanel service` is the single command for status, start, stop,
restart, logs, the one-time setup token, upgrade and uninstall -- on both
platforms and both service kinds, so there is nothing to remember about
systemctl --user versus launchctl.
EOF
}

# A shifting loop rather than `for arg in "$@"`, because three of the options
# take a value and the for-loop cannot see the argument after the one it is
# looking at. The `--flag=value` spellings are accepted too, because that is
# what people type into a one-liner.
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes|--non-interactive) INTERACTIVE=no ;;
    --interactive) INTERACTIVE=yes ;;
    --enable) ENABLE=yes ;;
    --no-enable) ENABLE=no ;;
    --user) KIND=user ;;
    --system) KIND=system; ASKED_SYSTEM=yes ;;
    --migrate) MIGRATE=yes ;;
    --skip-tmux) DO_TMUX=no ;;
    --username) shift; [ $# -gt 0 ] || { echo "--username needs a name" >&2; exit 2; }; ACCT_USER="$1" ;;
    --username=*) ACCT_USER="${1#--username=}" ;;
    --password-stdin) ACCT_STDIN=yes ;;
    --password-file) shift; [ $# -gt 0 ] || { echo "--password-file needs a path" >&2; exit 2; }; ACCT_FILE="$1" ;;
    --password-file=*) ACCT_FILE="${1#--password-file=}" ;;
    --password-env) shift; [ $# -gt 0 ] || { echo "--password-env needs a variable name" >&2; exit 2; }; ACCT_ENV="$1" ;;
    --password-env=*) ACCT_ENV="${1#--password-env=}" ;;
    --password|--password=*)
      # The same refusal `vibepanel account create` makes, made here as well:
      # forwarding it and letting the binary explain would mean the password
      # had already spent this whole script in `ps`.
      echo "there is no --password flag, on purpose: a password on a command line is in" >&2
      echo "your shell history and in \`ps\` output for every other user on this machine." >&2
      echo "Use --password-file <path>, --password-env <VAR> or --password-stdin." >&2
      exit 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; echo "try --help" >&2; exit 2 ;;
  esac
  shift
done

# Both, not just stdin. A prompt written to a log file is a script that hangs
# forever waiting for an answer nobody can see it asking for -- and the release
# check runs this with its output redirected.
if [ "$INTERACTIVE" = auto ]; then
  if [ -t 0 ] && [ -t 1 ]; then INTERACTIVE=yes; else INTERACTIVE=no; fi
fi

# The answer comes back in $ANSWER rather than on stdout, and the prompt is
# printed rather than passed to `read -p`. Both for the same reason: `read -p`
# writes its prompt only when stdin is a terminal, so a transcript of a run
# driven from a file -- which is how scripts/install-check.sh drives it -- shows
# the questions missing and the answers taken anyway. The prompts are the part
# under test.
ANSWER=
ask() { # ask <prompt> <default>
  local def="$2"
  printf '%s' "$1"
  read -r ANSWER || ANSWER=
  [ -n "$ANSWER" ] || ANSWER="$def"
  # A terminal has already echoed what was typed; anything else has not.
  [ -t 0 ] || printf '%s\n' "$ANSWER"
}
yesno() { # yesno <question> <y|n, the default>
  local def="$2" hint="[y/N]"
  [ "$def" = y ] && hint="[Y/n]"
  ask "$1 $hint " "$def"
  case "$ANSWER" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

if [ ! -f "$BIN_SRC" ]; then
  echo "error: no vibepanel binary next to this script" >&2
  echo "       run this from an unpacked release archive, or use the one-liner:" >&2
  echo "       curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh" >&2
  exit 1
fi

# ── can we become root? ───────────────────────────────────────────────────
#
# Asked before tmux, because the answer decides whether installing tmux is even
# on the table.
#
# Three ways, in descending order of how little they inconvenience anybody, and
# one thing that is not a way: sudo exists, but every route to it would block on
# a password prompt nobody is watching. That last case has to count as no root,
# because an installer that hangs in a CI job is worse than one that installs
# the user unit.
ROOT_CMD=()
ROOT_HOW=none
if [ "${VIBEPANEL_ROOT_CMD+set}" = set ]; then
  case "$VIBEPANEL_ROOT_CMD" in
    none) ROOT_HOW=none ;;
    '')   ROOT_HOW=root ;;
    # Word-split deliberately: the override is a command line, not a filename.
    *)    ROOT_HOW=override; read -r -a ROOT_CMD <<<"$VIBEPANEL_ROOT_CMD" ;;
  esac
elif [ "$(id -u)" = 0 ]; then
  ROOT_HOW=root
elif ! command -v sudo >/dev/null; then
  ROOT_HOW=none
elif sudo -n true 2>/dev/null; then
  ROOT_HOW=sudo; ROOT_CMD=(sudo)
elif [ "$INTERACTIVE" = yes ] && [ -t 0 ]; then
  ROOT_HOW=sudo-ask; ROOT_CMD=(sudo)
fi
HAVE_ROOT=no
[ "$ROOT_HOW" = none ] || HAVE_ROOT=yes

as_root() {
  if [ "${#ROOT_CMD[@]}" -eq 0 ]; then "$@"; else "${ROOT_CMD[@]}" "$@"; fi
}

# ── tmux: present, and new enough? ────────────────────────────────────────
#
# The one prerequisite, and finding out later means finding out from a panel
# that starts and then cannot create a session.

# tmux reports "3.4", "3.3a" for a patch release and "next-3.6" for a
# development build, so a split on "." is not enough: the letters and any
# prefix have to be dropped rather than parsed. This mirrors
# tmux.ParseVersion, including its last rule -- an unparseable version counts
# as new enough, because refusing to run over a version string that looked
# unfamiliar would be worse than the problem.
tmux_at_least_min() { # tmux_at_least_min <version string>
  local v="$1" maj min
  maj="$(printf '%s' "$v" | sed -e 's/^[^0-9]*//' -e 's/[^0-9].*$//')"
  min="$(printf '%s' "$v" | sed -e 's/^[^0-9]*//' -e 's/^[0-9]*//' -e 's/^\.//' -e 's/[^0-9].*$//')"
  if [ -z "$maj" ] || [ -z "$min" ]; then return 0; fi
  if [ "$maj" -gt "$TMUX_MIN_MAJOR" ]; then return 0; fi
  if [ "$maj" -eq "$TMUX_MIN_MAJOR" ] && [ "$min" -ge "$TMUX_MIN_MINOR" ]; then return 0; fi
  return 1
}

# Which package manager, and what it would be asked to do. Set together,
# because "apt-get is here" and "apt-get install -y tmux" are one fact.
PKG=
PKG_ARGS=()
PKG_NEEDS_ROOT=yes
detect_pkg() {
  local candidates
  # Native first on Linux, brew first on macOS. A Linux machine with linuxbrew
  # still wants its own package manager: brew's tmux would shadow the distro's
  # and be the version nobody expected.
  if [ "$PLATFORM" = darwin ]; then candidates="brew port"
  else candidates="apt-get dnf pacman zypper apk brew"; fi
  if [ -n "${VIBEPANEL_PKG_MANAGER:-}" ]; then
    PKG="$VIBEPANEL_PKG_MANAGER"
  else
    local c
    for c in $candidates; do
      if command -v "$c" >/dev/null 2>&1; then PKG="$c"; break; fi
    done
  fi
  PKG_NEEDS_ROOT=yes
  case "$PKG" in
    apt-get) PKG_ARGS=(apt-get install -y tmux) ;;
    dnf)     PKG_ARGS=(dnf install -y tmux) ;;
    pacman)  PKG_ARGS=(pacman -S --noconfirm tmux) ;;
    zypper)  PKG_ARGS=(zypper --non-interactive install tmux) ;;
    apk)     PKG_ARGS=(apk add tmux) ;;
    # Homebrew refuses to run as root and has done for years. Running it under
    # sudo anyway is how people end up with a prefix their own account cannot
    # write to, which then breaks every later brew command and not just this
    # one -- so this is not a preference, it is the only way that works.
    brew)    PKG_ARGS=(brew install tmux); PKG_NEEDS_ROOT=no ;;
    port)    PKG_ARGS=(port install tmux) ;;
    *)       PKG=; PKG_ARGS=() ;;
  esac
}

# The line a person would type, so it can be printed when we cannot run it.
pkg_command_line() {
  local prefix=""
  [ "$PKG_NEEDS_ROOT" = yes ] && prefix="sudo "
  printf '%s%s' "$prefix" "${PKG_ARGS[*]}"
}

run_pkg() {
  if [ "$PKG_NEEDS_ROOT" = no ]; then
    if [ -n "$PKG_RUNNER" ]; then "$PKG_RUNNER" "${PKG_ARGS[@]}"; else "${PKG_ARGS[@]}"; fi
  elif [ -n "$PKG_RUNNER" ]; then
    # The root command is passed through rather than dropped, so a recorder can
    # see whether the package install would have been privileged -- which is
    # the entire difference between the brew branch and the other five.
    if [ "${#ROOT_CMD[@]}" -eq 0 ]; then "$PKG_RUNNER" "${PKG_ARGS[@]}"
    else "$PKG_RUNNER" "${ROOT_CMD[@]}" "${PKG_ARGS[@]}"; fi
  else
    as_root "${PKG_ARGS[@]}"
  fi
}

# `|| true`, and not because the failure is uninteresting: under `set -e` and
# pipefail, probing a tmux that is not there aborts the installer with exit 127
# and no message at all -- which is exactly the state this function exists to
# detect and report. An empty string is the answer, not a crash.
tmux_version_of() {
  { "$TMUX_BIN" -V 2>/dev/null || true; } | sed -e 's/^tmux //' | tr -d '[:space:]'
}

TMUX_STATE=ok      # ok | missing | old
TMUX_VER=
if [ "$DO_TMUX" = no ]; then
  TMUX_STATE=skipped
elif ! command -v "$TMUX_BIN" >/dev/null 2>&1; then
  TMUX_STATE=missing
else
  TMUX_VER="$(tmux_version_of)"
  tmux_at_least_min "$TMUX_VER" || TMUX_STATE=old
fi

BANNER_DONE=no
banner() { [ "$BANNER_DONE" = yes ] && return 0; echo "vibepanel installer"; echo; BANNER_DONE=yes; }

if [ "$TMUX_STATE" != ok ] && [ "$TMUX_STATE" != skipped ]; then
  detect_pkg
  banner
  if [ "$TMUX_STATE" = missing ]; then
    echo "tmux is not installed. The panel keeps every session alive inside it, so"
    echo "there is nothing to run without it."
  else
    # Not fatal, deliberately, and for the same reason `vibepanel doctor` marks
    # it "--" rather than FAIL: the panel works, one thing about it is worse,
    # and refusing to install over that would be the installer making a
    # judgement that is not its to make.
    echo "tmux $TMUX_VER is older than $TMUX_MIN_MAJOR.$TMUX_MIN_MINOR, so the panel's config line"
    echo "allow-passthrough is not applied and the sequences agent TUIs use for"
    echo "progress and notifications are silently dropped. Everything else works."
  fi
  echo

  WANT_TMUX=no
  if [ -z "$PKG" ]; then
    echo "No package manager this knows about (apt/dnf/pacman/zypper/apk/brew) is"
    echo "on this machine, so tmux has to be installed by hand:"
    echo "  https://github.com/tmux/tmux/wiki/Installing"
  elif [ "$PKG_NEEDS_ROOT" = yes ] && [ "$HAVE_ROOT" = no ]; then
    echo "Installing it needs root, and root is not available here (no sudo, or it"
    echo "would need a password and there is nobody to type it). From an account"
    echo "that has it:"
    echo "  $(pkg_command_line)"
  elif [ "$INTERACTIVE" = yes ]; then
    if [ "$TMUX_STATE" = missing ]; then
      yesno "install it now with: $(pkg_command_line)  ?" y && WANT_TMUX=yes
    else
      yesno "try to upgrade it now with: $(pkg_command_line)  ?" n && WANT_TMUX=yes
    fi
  elif [ "$TMUX_STATE" = missing ]; then
    # Unattended, and tmux is missing: install it. This is what makes the
    # one-liner true on a machine with nothing on it, which is the whole
    # promise. --skip-tmux is how a caller says otherwise.
    echo "installing tmux with: $(pkg_command_line)"
    WANT_TMUX=yes
  else
    # Unattended and merely old: do not. The distribution's package *is* the
    # old version, so `apt-get install -y tmux` would be a no-op that reported
    # success, and an unattended run has nobody to read the difference.
    echo "Not upgrading it unattended -- the distribution's package is this same"
    echo "version, so it would change nothing and say it had. Deliberately:"
    echo "  https://github.com/tmux/tmux/wiki/Installing"
  fi

  if [ "$WANT_TMUX" = yes ]; then
    if run_pkg; then
      TMUX_VER="$(tmux_version_of)"
      if [ -z "$TMUX_VER" ]; then
        echo "the package manager reported success and there is still no tmux here."
        TMUX_STATE=missing
      elif tmux_at_least_min "$TMUX_VER"; then
        echo "tmux $TMUX_VER installed"
        TMUX_STATE=ok
      else
        # The likeliest outcome of the too-old branch, and the one worth saying
        # out loud: the distribution ships the version that is already here.
        echo "tmux $TMUX_VER is what this machine's packages offer, and it is still"
        echo "older than $TMUX_MIN_MAJOR.$TMUX_MIN_MINOR. Building from source is the only way up:"
        echo "  https://github.com/tmux/tmux/wiki/Installing"
        TMUX_STATE=old
      fi
    else
      echo "that did not work. Install tmux and run this again:"
      echo "  $(pkg_command_line)"
    fi
  fi

  if [ "$TMUX_STATE" = missing ]; then
    echo
    echo "error: nothing was installed. vibepanel needs tmux." >&2
    exit 1
  fi
  echo
fi

# ── the first account, if it is being created from here ───────────────────
# Counted with `if`, not `[ ... ] &&`: a false test as the whole of a statement
# is a failed command, and `set -e` ends the script there. Silently, before it
# has printed anything.
ACCT_SOURCES=0
if [ "$ACCT_STDIN" = yes ]; then ACCT_SOURCES=$((ACCT_SOURCES + 1)); fi
if [ -n "$ACCT_FILE" ]; then ACCT_SOURCES=$((ACCT_SOURCES + 1)); fi
if [ -n "$ACCT_ENV" ]; then ACCT_SOURCES=$((ACCT_SOURCES + 1)); fi
if [ "$ACCT_SOURCES" -gt 1 ]; then
  echo "error: choose one of --password-stdin, --password-file and --password-env." >&2
  echo "       Two of them means a script that believes it set one password and set" >&2
  echo "       another, and neither of us would know which." >&2
  exit 2
fi
if [ "$ACCT_SOURCES" -gt 0 ] && [ -z "$ACCT_USER" ]; then
  echo "error: a password was given with no --username, so there is no account to" >&2
  echo "       create. Add --username <name>, or drop the password and use the" >&2
  echo "       setup token in the browser." >&2
  exit 2
fi
if [ "$ACCT_STDIN" = yes ] && [ "$INTERACTIVE" = yes ]; then
  # They cannot both have stdin. The prompts read it line by line and
  # --password-stdin reads it to EOF, so whichever went first would consume the
  # other's input -- and the failure would be a password silently set to the
  # word "y".
  echo "error: --password-stdin needs stdin to itself, and the prompts read stdin too." >&2
  echo "       Add --yes, or use --password-file <path> instead." >&2
  exit 2
fi
if [ -n "$ACCT_FILE" ] && [ ! -r "$ACCT_FILE" ]; then
  echo "error: cannot read the password file $ACCT_FILE" >&2
  exit 2
fi

# ── things about this machine that are worth knowing before we touch it ───
#
# Every one of these is a real outcome somebody has had, and every one of them
# is silent in the same way: the install succeeds, and the thing they were
# trying to get does not happen.

# Is $HOME writable at all? Every path this script writes is under it, and
# `install: cannot create regular file` twelve lines in is a worse way to find
# out than one line saying so before anything has changed.
if [ ! -w "$HOME" ]; then
  echo "error: $HOME is not writable, and everything this installs lives under it." >&2
  echo "       Nothing was changed." >&2
  exit 1
fi
if [ -e "$BIN_DIR" ] && [ ! -w "$BIN_DIR" ]; then
  echo "error: $BIN_DIR exists and is not writable, so the binary cannot be installed." >&2
  echo "       Nothing was changed. Fix the permissions, or install it somewhere else" >&2
  echo "       and edit the unit's ExecStart to match." >&2
  exit 1
fi

# Is there a service manager here at all?
#
# systemctl being on PATH is not the question -- a container image can have it
# installed with no systemd running, WSL1 has neither, and several
# distributions ship other inits. /run/systemd/system is what systemd itself
# documents as the way to tell whether it is the init, and it is the check
# `systemctl is-system-running` makes underneath.
#
# VIBEPANEL_INIT_DIR is the last of the test-only overrides: without it the "no
# systemd here" branch is only reachable on a machine that is not the one any
# of this gets developed on.
INIT_DIR="${VIBEPANEL_INIT_DIR:-/run/systemd/system}"
SERVICE_MGR=yes
SERVICE_WHY=
if [ "$PLATFORM" = darwin ]; then
  if ! command -v "${LAUNCHCTL%% *}" >/dev/null 2>&1; then
    SERVICE_MGR=no; SERVICE_WHY="launchctl is not on PATH"
  fi
elif ! command -v "${SYSTEMCTL%% *}" >/dev/null 2>&1; then
  SERVICE_MGR=no; SERVICE_WHY="systemctl is not on PATH"
elif [ ! -d "$INIT_DIR" ]; then
  SERVICE_MGR=no
  SERVICE_WHY="systemd is not running this machine (no $INIT_DIR) -- a container, WSL1, or another init"
fi

# The systemd *user* manager needs a session bus, and XDG_RUNTIME_DIR is how
# anything finds it. Unset means `systemctl --user` cannot work from this
# shell, which is the state of a bare ssh command and of every cron job. The
# panel's own restart button tells the same two cases apart the same way, in
# internal/httpapi/update.go.
USER_BUS=yes
if [ "$PLATFORM" = linux ] && [ -z "${XDG_RUNTIME_DIR:-}" ] && [ "$(id -u)" != 0 ]; then
  USER_BUS=no
fi

# What is already here, and is it the same thing?
#
# "I ran the installer and nothing changed" is usually true, and usually means
# the same version was reinstalled. Which of the three this is costs one line
# to say and answers the question before it gets asked.
OLD_VER=
if [ -x "$BIN_DIR/vibepanel" ]; then
  OLD_VER="$("$BIN_DIR/vibepanel" version 2>/dev/null | head -1 || true)"
fi
NEW_VER="$("$BIN_SRC" version 2>/dev/null | head -1 || true)"

# A unit file at our path that we did not write.
#
# Somebody who hand-rolled a unit, a distribution package, an older layout.
# Overwriting it without asking is the one thing an installer must not do to a
# file it did not create, and the Documentation= line every unit and plist in
# this repository carries is what tells them apart.
OURS="github.com/jiangmuran/vibepanel"
foreign() { # foreign <path>
  if [ ! -f "$1" ]; then return 1; fi
  if grep -q "$OURS" "$1"; then return 1; fi
  return 0
}

# Is the port the panel is about to want already answering?
#
# bash's own /dev/tcp rather than ss, netstat or lsof: each of those is missing
# from some minimal image, and none is worth a dependency for one probe.
# The connect happens in a subshell, which is also what closes the descriptor
# again when it exits. An `exec 3>&- 2>/dev/null` in the parent to tidy up is
# the trap here: redirections on a bare `exec` apply to the shell itself for
# the rest of its life, so that line silently sent every later error message --
# including the one about a binary that will not run -- to /dev/null.
port_in_use() { # port_in_use <port>
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

HAVE_USER_UNIT=no; [ -f "$USER_UNIT" ] && HAVE_USER_UNIT=yes
HAVE_SYSTEM_UNIT=no; [ -f "$SYSTEM_UNIT" ] && HAVE_SYSTEM_UNIT=yes
HAVE_AGENT=no; [ -f "$PLIST" ] && HAVE_AGENT=yes

# ── what are we installing? ───────────────────────────────────────────────
banner
if [ "$PLATFORM" = darwin ]; then
  if [ "$HAVE_AGENT" = yes ]; then echo "  found an existing LaunchAgent:    $PLIST"; fi
else
  if [ "$HAVE_USER_UNIT" = yes ]; then echo "  found an existing user service:   $USER_UNIT"; fi
  if [ "$HAVE_SYSTEM_UNIT" = yes ]; then echo "  found an existing system service: $SYSTEM_UNIT"; fi
fi

if [ "$PLATFORM" = darwin ]; then
  # One kind, so there is nothing to ask and nothing to conflict with. --system
  # is answered further down rather than silently, because somebody who typed
  # it is expecting something this cannot give them.
  KIND=agent
elif [ -z "$KIND" ]; then
  # An existing install of either kind is the answer to the question, so the
  # question is not asked: an upgrade that offers to change the unit kind is an
  # upgrade that changes it for whoever pressed return without reading.
  if [ "$HAVE_SYSTEM_UNIT" = yes ]; then
    KIND=system
  elif [ "$HAVE_USER_UNIT" = yes ]; then
    KIND=user
  elif [ "$HAVE_ROOT" = yes ] && [ -f "$SYSTEM_UNIT_SRC" ]; then
    # The recommended default wherever root is available. It is the same panel
    # running as the same account with the same environment; what it adds is
    # being up before anyone logs in without lingering, and being able to lower
    # its OOM score at all -- measured, a user unit asking for -500 gets 100.
    KIND=system
    if [ "$INTERACTIVE" = yes ]; then
      echo "How should the panel run?"
      echo
      echo "  1) systemd *system* service (recommended; root is available here)"
      echo "     Same account, same environment -- it drops to User=$WHO. It is up"
      echo "     before anyone logs in, and it is the only one that can lower the"
      echo "     OOM score: measured, a user unit asking for -500 gets 100, a"
      echo "     system unit gets -500. Needs root once, to write one file."
      echo
      echo "  2) systemd *user* service"
      echo "     Runs as you and needs no root at all. Starts at boot once"
      echo "     lingering is on, which this will enable. Choose this on a shared"
      echo "     machine, or if you would rather nothing of yours lived in /etc."
      echo
      ask "  choice [1]: " 1
      echo
      case "$ANSWER" in
        2) KIND=user ;;
        *) KIND=system ;;
      esac
    fi
  else
    KIND=user
    # Said out loud, and here rather than in the summary. The recommended
    # default is the system service; choosing the other one without a word
    # reads as the installer having a different opinion than the README, and
    # the actual reason -- no root here -- is one the person may be able to fix.
    if [ -f "$SYSTEM_UNIT_SRC" ] && [ "$HAVE_ROOT" = no ]; then
      echo "root is not available here (no sudo, or it would need a password and"
      echo "there is nobody to type it), so this is the systemd *user* service."
      echo "It needs no root at all; what it gives up is the OOM score."
      echo
    fi
  fi
fi

# ── never both ────────────────────────────────────────────────────────────
#
# A user unit and a system unit are two panels with the same data directory on
# the same tmux socket. They do not conflict loudly; they take turns, and the
# symptom is a panel that forgets things.
CONFLICT=no
if [ "$KIND" = system ] && [ "$HAVE_USER_UNIT" = yes ]; then CONFLICT=user; fi
if [ "$KIND" = user ] && [ "$HAVE_SYSTEM_UNIT" = yes ]; then CONFLICT=system; fi

if [ "$CONFLICT" != no ] && [ "$MIGRATE" != yes ]; then
  echo
  echo "there is already a $CONFLICT service installed, and you asked for the"
  echo "$KIND one. Both at once means two panels sharing one tmux socket and one"
  echo "database, which does not fail loudly -- it loses writes."
  if [ "$INTERACTIVE" = yes ] && yesno "  remove the $CONFLICT service and install the $KIND one?" n; then
    MIGRATE=yes
  else
    echo
    echo "nothing was changed. Either keep what you have, or re-run with:"
    echo "  $0 --$KIND --migrate"
    exit 3
  fi
fi

# Removing a system unit needs root; refusing here beats getting halfway.
if [ "$MIGRATE" = yes ] && [ "$CONFLICT" = system ] && [ "$HAVE_ROOT" = no ]; then
  echo "error: removing $SYSTEM_UNIT needs root, and root is not available here." >&2
  echo "       Nothing was changed. From an account that has sudo:" >&2
  echo "         sudo systemctl disable --now vibepanel && sudo rm $SYSTEM_UNIT" >&2
  exit 3
fi

# ── the fallbacks, said plainly ───────────────────────────────────────────
FELL_BACK=no
# Answered rather than ignored. Somebody who typed --system is expecting
# something macOS cannot give them, and silently installing a different thing
# is how an installer gets a reputation for lying.
if [ "$PLATFORM" = darwin ] && [ "$ASKED_SYSTEM" = yes ]; then
  echo
  echo "macOS has no equivalent of the systemd system service worth installing."
  echo "A LaunchDaemon would run as root at boot and then have to drop back to"
  echo "your account to be any use, and the one thing the Linux system unit"
  echo "buys -- a lower OOM score -- does not exist here: macOS has no"
  echo "oom_score_adj, and jetsam cannot be biased from a plist."
  echo "Installing the LaunchAgent, which is the macOS answer."
  FELL_BACK=yes
fi
if [ "$KIND" = system ] && [ "$HAVE_ROOT" = no ]; then
  echo
  echo "root is not available here (no sudo, or it would need a password and"
  echo "there is nobody to type it), so the system service cannot be installed."
  echo "Installing the user service instead -- it gives up the OOM score and"
  echo "needs lingering to start at boot, and nothing else."
  KIND=user
  FELL_BACK=yes
fi
if [ "$KIND" = system ] && [ ! -f "$SYSTEM_UNIT_SRC" ]; then
  echo
  echo "this archive does not ship vibepanel-system.service; installing the user"
  echo "service instead."
  KIND=user
  FELL_BACK=yes
fi
# No service manager at all: install the binary and say so. Refusing would be
# wrong -- the panel runs perfectly well from a shell, and a container or a WSL1
# machine is a place people genuinely use it -- but pretending a service was
# installed would be worse than either.
if [ "$SERVICE_MGR" = no ]; then
  echo
  echo "no service manager here: $SERVICE_WHY."
  echo "Installing the binary and the env file only. Nothing will start the panel"
  echo "for you, so start it yourself, or from whatever supervises this machine:"
  echo "  $BIN_DIR/vibepanel serve"
  KIND=none
  FELL_BACK=yes
fi
if [ "$KIND" = agent ] && [ ! -f "$PLIST_SRC" ]; then
  echo "error: this archive does not ship $MAC_LABEL.plist, so there is" >&2
  echo "       nothing to install as a service on macOS." >&2
  exit 1
fi

# ── a file at our path that we did not write ──────────────────────────────
TARGET_FILE=
case "$KIND" in
  user) TARGET_FILE="$USER_UNIT" ;;
  system) TARGET_FILE="$SYSTEM_UNIT" ;;
  agent) TARGET_FILE="$PLIST" ;;
esac
if [ -n "$TARGET_FILE" ] && foreign "$TARGET_FILE"; then
  echo
  echo "there is already a file at"
  echo "  $TARGET_FILE"
  echo "and it was not written by this installer -- it has no vibepanel"
  echo "Documentation= line in it. A hand-written unit, a distribution package,"
  echo "or an older layout; whichever it is, overwriting it loses whatever was"
  echo "configured in it, and there is no copy anywhere."
  if [ "$INTERACTIVE" = yes ] && yesno "  replace it?" n; then
    echo "  (replacing it. The old one is not backed up.)"
  else
    echo
    echo "nothing was changed. Move it aside and run this again:"
    echo "  mv $TARGET_FILE $TARGET_FILE.bak"
    exit 3
  fi
fi

# ── start it? ─────────────────────────────────────────────────────────────
#
# Whether the unit is already running decides the wording and, when nobody was
# asked, the default: an upgrade that leaves the old binary running is the
# failure docs/runbook.md has an entry for, so a running unit is restarted
# without being asked. A stopped one is not started behind your back.
sctl_user() { "$SYSTEMCTL" --user "$@"; }
sctl_sys() { as_root "$SYSTEMCTL" "$@"; }
lctl() { "$LAUNCHCTL" "$@"; }
GUI="gui/$(id -u)"

# Asked only when a unit of that kind is already installed. `systemctl --user`
# talks to the manager for the *logged-in* user, which read its own $HOME at
# login and does not care what this script was handed -- so on a first install
# into a different HOME (which is exactly what scripts/release-check.sh does)
# "vibepanel is active" can be true and be somebody else's panel. Restarting
# that would be this installer reaching outside the tree it was pointed at.
RUNNING=no
if [ "$KIND" = agent ]; then
  if [ "$HAVE_AGENT" = yes ] && command -v "${LAUNCHCTL%% *}" >/dev/null 2>&1; then
    lctl print "$GUI/$MAC_LABEL" >/dev/null 2>&1 && RUNNING=yes
  fi
elif command -v "${SYSTEMCTL%% *}" >/dev/null 2>&1; then
  if [ "$KIND" = user ] && [ "$HAVE_USER_UNIT" = yes ]; then
    sctl_user is-active --quiet vibepanel 2>/dev/null && RUNNING=yes
  elif [ "$KIND" = system ] && [ "$HAVE_SYSTEM_UNIT" = yes ]; then
    sctl_sys is-active --quiet vibepanel 2>/dev/null && RUNNING=yes
  fi
fi

if [ "$ENABLE" = auto ]; then
  if [ "$RUNNING" = yes ]; then
    ENABLE=yes    # a restart, and it is not optional -- see above
  elif [ "$INTERACTIVE" = yes ]; then
    if yesno "start the service now?" y; then ENABLE=yes; else ENABLE=no; fi
  else
    ENABLE=no
  fi
fi

# ── which port, and is anything on it? ────────────────────────────────────
#
# Read before the plan rather than after the install, because "the port is
# taken" is a thing to be told while there is still a decision to make. The
# panel would start, fail to bind, and be restarted by systemd every three
# seconds -- a unit that looks active in `systemctl status` for the first two
# seconds of each cycle.
PORT=8443
if [ -e "$ENV_FILE" ]; then
  ADDR="$(grep -E '^[[:space:]]*VIBEPANEL_ADDR=' "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' "' || true)"
  case "$ADDR" in *:*) P="${ADDR##*:}"; if [ -n "$P" ]; then PORT="$P"; fi ;; esac
fi
HOST="$(hostname 2>/dev/null || echo localhost)"

PORT_TAKEN=no
# Not while a panel of ours is already running: that is the upgrade case, the
# thing on the port is the panel being replaced, and warning about it would be
# the installer reporting itself as a conflict.
if [ "$RUNNING" = no ] && port_in_use "$PORT"; then
  PORT_TAKEN=yes
fi

# ── the plan, before anything happens ─────────────────────────────────────
echo
echo "about to:"
# Which of the three this is. "I ran it and nothing happened" is nearly always
# a reinstall of the same build, and saying so costs a line.
if [ -z "$OLD_VER" ]; then
  echo "  install  $BIN_DIR/vibepanel   ($NEW_VER)"
elif [ "$OLD_VER" = "$NEW_VER" ]; then
  echo "  replace  $BIN_DIR/vibepanel   (the same build: $NEW_VER)"
else
  echo "  replace  $BIN_DIR/vibepanel   ($OLD_VER -> $NEW_VER)"
fi
case "$KIND" in
  user)   echo "  install  $USER_UNIT   (systemd user service)" ;;
  system) echo "  install  $SYSTEM_UNIT   (systemd system service, as root)"
          echo "           with User=$WHO and HOME=$HOME substituted in" ;;
  agent)  echo "  install  $PLIST   (launchd LaunchAgent)"
          echo "           with HOME=$HOME substituted in" ;;
  none)   echo "  install  no service: $SERVICE_WHY" ;;
esac
if [ -e "$ENV_FILE" ]; then
  echo "  keep     $ENV_FILE   (already there, yours to edit)"
else
  echo "  install  $ENV_FILE   (edit it before exposing the panel)"
fi
if [ "$CONFLICT" != no ] && [ "$MIGRATE" = yes ]; then
  echo "  remove   the existing $CONFLICT service, so there is only ever one"
fi
if [ "$KIND" = user ]; then
  echo "  enable   lingering for $WHO, so it starts at boot and survives logout"
fi
if [ "$ENABLE" = yes ]; then
  if [ "$RUNNING" = yes ]; then
    echo "  restart  vibepanel (your sessions belong to tmux and survive it)"
  else
    echo "  start    vibepanel"
  fi
fi
if [ -n "$ACCT_USER" ]; then
  echo "  create   the panel's first account, as $ACCT_USER"
  echo "           (the panel will then not print a setup token at startup)"
fi
if [ "$PORT_TAKEN" = yes ]; then
  echo
  echo "  WARNING: something is already listening on port $PORT, so the panel will"
  echo "           start, fail to bind and be restarted on a three-second loop."
  echo "           Set VIBEPANEL_ADDR in $ENV_FILE to a free port first."
fi
if [ "$ROOT_HOW" = sudo-ask ] && [ "$KIND" = system ]; then
  echo
  echo "  sudo will ask for your password."
fi
echo

if [ "$INTERACTIVE" = yes ]; then
  if ! yesno "proceed?" y; then echo "nothing was changed."; exit 0; fi
  echo
fi

# ── do it ─────────────────────────────────────────────────────────────────
mkdir -p "$BIN_DIR" "$(dirname "$ENV_FILE")"
install -m 0755 "$BIN_SRC" "$BIN_DIR/vibepanel"
echo "installed $BIN_DIR/vibepanel"

# Does the file that was just written actually run *here*?
#
# Three different things make this fail and they are indistinguishable
# afterwards: $HOME on a filesystem mounted noexec (containers, hardened
# machines, some NFS), an SELinux or AppArmor policy that will not execute a
# file with that label, and an archive for the wrong architecture. All three
# install perfectly, and all three surface later as a service that restarts
# every three seconds with `Exec format error` or `Permission denied` in a
# journal nobody has been told to read yet.
#
# Checked by running it, not by looking at the mount table: `findmnt` is not
# everywhere, the mount options do not describe SELinux, and neither of them
# says anything about the architecture.
if ! "$BIN_DIR/vibepanel" version >/dev/null 2>&1; then
  echo >&2
  echo "error: $BIN_DIR/vibepanel is installed and will not run on this machine." >&2
  echo "       Three things do this and they look identical from here:" >&2
  echo "         - the filesystem holding $HOME is mounted noexec" >&2
  echo "         - SELinux or AppArmor refuses to execute a file with that label" >&2
  echo "         - the archive is for a different architecture ($(uname -m) here)" >&2
  echo "       What it says when you run it directly is the thing that tells them apart:" >&2
  echo "         $BIN_DIR/vibepanel version" >&2
  echo "       No service was installed; there would be nothing for it to start." >&2
  exit 1
fi

# Never overwrite the env file: it is the one file here the user edits, and it
# holds the domain, the TLS choice and any ACME credentials.
if [ -e "$ENV_FILE" ]; then
  echo "kept      $ENV_FILE (already there, left alone)"
else
  install -m 0600 "$SRC/vibepanel.env" "$ENV_FILE"
  echo "installed $ENV_FILE — edit it before exposing the panel"
fi

# The old install goes away before the new one arrives, so an interruption in
# between leaves nothing running rather than two things running.
if [ "$MIGRATE" = yes ] && [ "$CONFLICT" = user ]; then
  sctl_user disable --now vibepanel >/dev/null 2>&1 || true
  rm -f "$USER_UNIT"
  sctl_user daemon-reload >/dev/null 2>&1 || true
  echo "removed   $USER_UNIT (migrated to the system service)"
elif [ "$MIGRATE" = yes ] && [ "$CONFLICT" = system ]; then
  sctl_sys disable --now vibepanel >/dev/null 2>&1 || true
  as_root rm -f "$SYSTEM_UNIT"
  sctl_sys daemon-reload >/dev/null 2>&1 || true
  echo "removed   $SYSTEM_UNIT (migrated to the user service)"
fi

# ── the first account, before the service starts ──────────────────────────
#
# Before, deliberately. With an account already in the database the panel does
# not print a setup token at all, so there is never a moment where a live token
# is sitting in a journal that somebody else can read.
#
# Through the binary's own subcommand rather than by touching the database:
# argon2id, the length rules and the refusal to overwrite an existing account
# all live in one place, and `vibepanel account create` is that place.
ACCOUNT_MADE=no
if [ -n "$ACCT_USER" ]; then
  ACCT_ARGS=(account create --username "$ACCT_USER")
  if [ "$ACCT_STDIN" = yes ]; then ACCT_ARGS+=(--password-stdin); fi
  if [ -n "$ACCT_FILE" ]; then ACCT_ARGS+=(--password-file "$ACCT_FILE"); fi
  if [ -n "$ACCT_ENV" ]; then ACCT_ARGS+=(--password-env "$ACCT_ENV"); fi
  # With the env file applied, so the account lands in the same data directory
  # the service will open. Without this, an install that sets VIBEPANEL_DATA_DIR
  # creates the account in one database and serves out of another -- and the
  # panel then greets its first visitor with a setup token, as if nothing had
  # happened.
  if ( set -a
       # shellcheck disable=SC1090
       if [ -f "$ENV_FILE" ]; then . "$ENV_FILE"; fi
       set +a
       "$BIN_DIR/vibepanel" "${ACCT_ARGS[@]}" ); then
    ACCOUNT_MADE=yes
  else
    # Not fatal. The files are in place and the browser wizard still works, so
    # ending here would leave a half-installed machine over a password that can
    # be typed again in thirty seconds.
    echo
    echo "the account was not created (see above). Everything else is installed, and"
    echo "the panel will print a one-time setup token at startup as it always did."
  fi
fi

STARTED=no      # no | started | restarted

if [ "$KIND" = none ]; then
  : # nothing to install; the message was printed where the decision was made
elif [ "$KIND" = agent ]; then
  mkdir -p "$PLIST_DIR" "$(dirname "$MAC_LOG")"
  # Substituted into a temp file and installed, like the system unit, so a sed
  # that fails leaves no half-rewritten plist behind: launchd will happily load
  # a truncated one and then fail in a way that says nothing about why.
  TMP_PLIST="$(mktemp "${TMPDIR:-/tmp}/vibepanel-plist.XXXXXX")"
  sed -e "s#__HOME__#$HOME#g" "$PLIST_SRC" > "$TMP_PLIST"
  install -m 0644 "$TMP_PLIST" "$PLIST"
  rm -f "$TMP_PLIST"
  echo "installed $PLIST"

  if command -v "${LAUNCHCTL%% *}" >/dev/null 2>&1; then
    if [ "$ENABLE" = yes ]; then
      if [ "$RUNNING" = yes ]; then
        # kickstart -k, not bootout+bootstrap. The pair has a window in which
        # the job is unloaded, and if anything goes wrong between them the
        # panel is simply gone; -k restarts in place.
        lctl kickstart -k "$GUI/$MAC_LABEL"
        STARTED=restarted
      else
        lctl bootstrap "$GUI" "$PLIST"
        STARTED=started
      fi
    fi
  else
    echo
    echo "no launchctl here; from a login session on that Mac:"
    echo "  launchctl bootstrap $GUI $PLIST"
  fi
  # The macOS equivalent of the lingering paragraph, and the one real gap
  # against the Linux user unit.
  echo "note      a LaunchAgent runs in your login session: it starts when you"
  echo "          log in and stops when you log out. macOS has no lingering."
elif [ "$KIND" = user ]; then
  mkdir -p "$USER_UNIT_DIR"
  install -m 0644 "$SRC/vibepanel.service" "$USER_UNIT"
  echo "installed $USER_UNIT"

  # Without lingering a user service stops when the last session for that user
  # ends. For a panel whose entire purpose is outliving your terminal, that is
  # the difference between working and appearing to work until you log out.
  #
  # Done rather than suggested. "Start at boot" is the thing people install a
  # service for, and a printed command is a step that gets skipped -- after
  # which the panel works perfectly until the first reboot, which is the worst
  # moment to find out. It needs no root for your own account (measured), and
  # the line below says what happened so it is not a change made behind your
  # back.
  if command -v loginctl >/dev/null; then
    if [ "$(loginctl show-user "$WHO" -p Linger --value 2>/dev/null || echo no)" = yes ]; then
      echo "lingering already on — the panel starts at boot and survives logout"
    elif loginctl enable-linger "$WHO" 2>/dev/null; then
      echo "enabled lingering — the panel now starts at boot and survives logout"
      echo "  (undo with: loginctl disable-linger $WHO)"
    else
      echo
      echo "could not enable lingering; without it the panel stops when you log out:"
      echo "  loginctl enable-linger $WHO"
    fi
  fi

  if command -v "${SYSTEMCTL%% *}" >/dev/null && sctl_user daemon-reload 2>/dev/null; then
    if [ "$ENABLE" = yes ]; then
      # `enable --now` is a no-op on a unit that is already enabled and active,
      # which is exactly the state an upgrade finds. The new binary went to disk
      # a few lines above and the old one kept serving, while this printed
      # "started. the one-time setup token is in: journalctl ..." -- a start
      # that did not happen, and a token consumed at first install.
      #
      # `restart` is the whole fix, and this is the one project where it is
      # free: the tmux server outlives the Go process, so every session
      # survives, which is what restart-check exists to prove.
      if [ "$RUNNING" = yes ]; then
        sctl_user restart vibepanel
        STARTED=restarted
      else
        sctl_user enable --now vibepanel
        STARTED=started
      fi
    fi
  else
    echo
    echo "no user systemd session here; from a login shell on that machine:"
    echo "  systemctl --user daemon-reload && systemctl --user enable --now vibepanel"
  fi
else
  # __USER__/__HOME__ are substituted here rather than with `sudo sed -i` on the
  # installed copy, so the only privileged write is a single `install`. A sed
  # that fails then leaves no half-rewritten unit under /etc.
  TMP_UNIT="$(mktemp "${TMPDIR:-/tmp}/vibepanel-unit.XXXXXX")"
  sed -e "s/__USER__/$WHO/g" -e "s#__HOME__#$HOME#g" "$SYSTEM_UNIT_SRC" > "$TMP_UNIT"
  as_root mkdir -p "$(dirname "$SYSTEM_UNIT")"
  as_root install -m 0644 "$TMP_UNIT" "$SYSTEM_UNIT"
  rm -f "$TMP_UNIT"
  echo "installed $SYSTEM_UNIT"

  sctl_sys daemon-reload
  if [ "$ENABLE" = yes ]; then
    if [ "$RUNNING" = yes ]; then
      sctl_sys restart vibepanel
      STARTED=restarted
    else
      sctl_sys enable --now vibepanel
      STARTED=started
    fi
  fi
  echo "note      a system service needs no lingering; it is up before anyone logs in"
fi

# ── what actually happened ────────────────────────────────────────────────
#
# Every line here is a fact about this run, not a description of the script.
# "started" and "restarted" are different facts and the setup token only exists
# for one of them.
case "$KIND" in
  user)
    JOURNAL="journalctl --user -u vibepanel -n 30"
    WHAT="the systemd user service ($USER_UNIT)"
    ;;
  system)
    JOURNAL="sudo journalctl -u vibepanel -n 30"
    WHAT="the systemd system service ($SYSTEM_UNIT)"
    ;;
  agent)
    JOURNAL="tail -n 30 $MAC_LOG"
    WHAT="the launchd LaunchAgent ($PLIST)"
    ;;
  none)
    JOURNAL="whatever your init writes"
    WHAT="the binary only -- no service, because $SERVICE_WHY"
    ;;
esac
# One command for all three, which is the point of it existing. The raw one is
# printed alongside for the person who wants to know what it does.
VPCTL="$BIN_DIR/vibepanel service"

echo
echo "── done ──"
echo "installed: $WHAT"
if [ "$ACCOUNT_MADE" = yes ]; then
  # Said once, here, and nowhere in the branches below: with an account in
  # place there is no token, and every line about finding one would send
  # somebody looking for something that was never printed.
  echo "account:   $ACCT_USER, created just now -- there is no setup token to find"
fi
case "$STARTED" in
  started)
    echo "state:     started just now"
    echo
    if [ "$ACCOUNT_MADE" = yes ]; then
      echo "open  http://$HOST:$PORT  and log in as $ACCT_USER."
    else
      echo "the one-time setup token:"
      echo "  $VPCTL token          # or: $JOURNAL"
      echo
      echo "then open  http://$HOST:$PORT  and paste it."
    fi
    ;;
  restarted)
    echo "state:     restarted (it was already running)"
    echo "           your sessions are untouched -- they belong to tmux, not to"
    echo "           the panel process."
    echo
    echo "the setup token was consumed at first install. The log, if you need it:"
    echo "  $VPCTL logs           # or: $JOURNAL"
    echo
    echo "the panel is at  http://$HOST:$PORT"
    ;;
  *)
    if [ "$KIND" = none ]; then
      echo "state:     not started, and nothing here can start it for you"
      echo
      echo "  $BIN_DIR/vibepanel serve"
    else
      echo "state:     not started; the files are in place"
      echo
      echo "  $VPCTL start"
      if [ "$ACCOUNT_MADE" != yes ]; then
        echo "  $VPCTL token          # the one-time setup token"
      fi
    fi
    echo
    echo "then open  http://$HOST:$PORT"
    ;;
esac
echo
if [ "$KIND" = none ]; then
  echo "afterwards: $VPCTL upgrade   (the rest of it needs a service to talk to)"
else
  echo "afterwards: $VPCTL {status|start|stop|restart|logs|token|upgrade|uninstall}"
fi

# Installed, and not typeable.
#
# ~/.local/bin is on PATH by default on a lot of distributions and on none of
# the others, and the difference is invisible until `vibepanel` says "command
# not found" about a file the last twenty lines said had been installed. The
# exact line to add, for the shell that is actually being used -- a generic
# "add it to your PATH" is the advice people skip.
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    RCFILE="~/.profile"
    case "$(basename "${SHELL:-sh}")" in
      bash) RCFILE="~/.bashrc" ;;
      zsh)  RCFILE="~/.zshrc" ;;
      fish) RCFILE="~/.config/fish/config.fish" ;;
    esac
    echo
    echo "note:      $BIN_DIR is not on your PATH, so \"vibepanel\" will not be a"
    echo "           command you can type. The service does not care -- it uses the"
    echo "           full path -- but you will want it. Add to $RCFILE:"
    if [ "$RCFILE" = "~/.config/fish/config.fish" ]; then
      echo "             fish_add_path $BIN_DIR"
    else
      echo "             export PATH=\"\$HOME/.local/bin:\$PATH\""
    fi
    ;;
esac

# `systemctl --user` needs a session bus, and this shell has not got one.
if [ "$KIND" = user ] && [ "$USER_BUS" = no ]; then
  echo
  echo "note:      XDG_RUNTIME_DIR is not set in this shell, so the systemd *user*"
  echo "           manager cannot be reached from here -- which is the state of a"
  echo "           bare non-login ssh command and of every cron job. The unit is"
  echo "           installed; enable it from a real login session:"
  echo "             ssh -t $WHO@$HOST 'systemctl --user enable --now vibepanel'"
fi
if [ "$TMUX_STATE" = old ]; then
  echo
  echo "note:      tmux $TMUX_VER is older than $TMUX_MIN_MAJOR.$TMUX_MIN_MINOR. The panel works; progress and"
  echo "           notification sequences from agent TUIs will not reach it."
fi

# The other unit, and why it is not the default here.
#
# A user service cannot lower its own oom_score_adj -- the kernel refuses
# without CAP_SYS_RESOURCE, and `systemd-analyze verify` accepts the directive
# anyway, so it is a setting that looks applied and does nothing. Measured: a
# user unit asking for -500 gets 100; a system unit with User= gets -500.
#
# Only mentioned when it was never offered. Having just declined it in a menu
# and then being told about it reads as a script that was not listening.
if [ "$KIND" = user ] && [ "$FELL_BACK" = no ] && [ "$INTERACTIVE" != yes ]; then
  echo
  echo "if this machine runs close to its memory and you want the kernel to look"
  echo "elsewhere first, there is a system unit that can actually say so:"
  echo "  ./install.sh --system --migrate   (needs root; the user unit cannot)"
fi
