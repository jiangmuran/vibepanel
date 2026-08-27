#!/usr/bin/env bash
# Installs vibepanel. Interactive when there is somebody to ask, silent when
# there is not.
#
#   ./install.sh                    # ask, show the plan, then do it
#   ./install.sh --yes              # take the defaults, ask nothing
#   ./install.sh --yes --enable     # ...and start it now
#   ./install.sh --system           # the system unit (needs root; it will ask)
#   ./install.sh --help
#
# Run this from an unpacked release archive. The user service touches nothing
# outside $HOME. The system service writes one file under /etc and is the only
# part that needs root.
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
# The archive layout is <dir>/vibepanel and <dir>/deploy/install.sh, so the
# binary is one level up from this script. Running it from the repo works too.
BIN_SRC="$SRC/../vibepanel"
[ -f "$BIN_SRC" ] || BIN_SRC="$SRC/../../vibepanel"

BIN_DIR="$HOME/.local/bin"
USER_UNIT_DIR="$HOME/.config/systemd/user"
USER_UNIT="$USER_UNIT_DIR/vibepanel.service"
ENV_FILE="$HOME/.config/vibepanel.env"
WHO="${USER:-$(id -un)}"

# ── three overrides that exist only so this script can be tested ──────────
#
# scripts/install-check.sh drives every branch below, including the one that
# writes to /etc and calls `systemctl enable --now`. It runs on a developer's
# machine, which has other services on it and often a panel of its own. Running
# the real thing there to find out whether the script works is how you find out
# it works by breaking something that was already running.
#
#   VIBEPANEL_DESTDIR    a DESTDIR-style prefix in front of every system path,
#                        so the system unit is written into a temp directory.
#   VIBEPANEL_SYSTEMCTL  the systemctl to call. The check points it at a
#                        recorder, because `systemctl --user disable --now
#                        vibepanel` would otherwise reach the *developer's* own
#                        panel: the user manager reads the real $HOME, not the
#                        throwaway one this script was handed.
#   VIBEPANEL_ROOT_CMD   how to become root. Empty means "already are"; the
#                        literal `none` means "root is not available here",
#                        which is the fallback path and cannot otherwise be
#                        produced on a machine where sudo works.
#
# None of them is documented outside this comment. They are not configuration.
DESTDIR="${VIBEPANEL_DESTDIR:-}"
SYSTEM_UNIT="$DESTDIR/etc/systemd/system/vibepanel.service"
SYSTEMCTL="${VIBEPANEL_SYSTEMCTL:-systemctl}"

INTERACTIVE=auto
ENABLE=auto        # auto: ask, or restart a unit that is already running
KIND=              # user | system; empty means "decide below"
MIGRATE=no

usage() {
  cat <<'EOF'
vibepanel installer

  ./install.sh                    ask what to install, show the plan, do it
  ./install.sh --yes              take the defaults, ask nothing
  ./install.sh --yes --enable     ...and start the service at the end
  ./install.sh --system           install the system unit (needs root)

  -y, --yes, --non-interactive  never ask; suitable for CI and curl | bash
      --interactive             ask even when stdin is not a terminal
      --enable                  start (or restart) the service when done
      --no-enable               only put the files in place
      --user                    the systemd *user* service (the default)
      --system                  the systemd *system* service; needs root, and
                                is the only one that can lower OOMScoreAdjust
      --migrate                 if the other kind is already installed, remove
                                it. Without this the installer refuses rather
                                than leave two panels on one tmux socket.
  -h, --help

Interactive by default when stdin and stdout are both terminals.
EOF
}

for arg in "$@"; do
  case "$arg" in
    -y|--yes|--non-interactive) INTERACTIVE=no ;;
    --interactive) INTERACTIVE=yes ;;
    --enable) ENABLE=yes ;;
    --no-enable) ENABLE=no ;;
    --user) KIND=user ;;
    --system) KIND=system ;;
    --migrate) MIGRATE=yes ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $arg" >&2; echo "try --help" >&2; exit 2 ;;
  esac
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
  echo "       run this from an unpacked release archive" >&2
  exit 1
fi

# tmux is the one prerequisite, and finding out later means finding out from a
# panel that starts and then cannot create a session.
if ! command -v tmux >/dev/null; then
  echo "error: tmux is not installed. The panel keeps sessions alive with it;" >&2
  echo "       there is nothing to run without it." >&2
  exit 1
fi

# ── can we become root? ───────────────────────────────────────────────────
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

HAVE_USER_UNIT=no; [ -f "$USER_UNIT" ] && HAVE_USER_UNIT=yes
HAVE_SYSTEM_UNIT=no; [ -f "$SYSTEM_UNIT" ] && HAVE_SYSTEM_UNIT=yes
SYSTEM_UNIT_SRC="$SRC/vibepanel-system.service"

# ── what are we installing? ───────────────────────────────────────────────
echo "vibepanel installer"
echo
if [ "$HAVE_USER_UNIT" = yes ]; then echo "  found an existing user service:   $USER_UNIT"; fi
if [ "$HAVE_SYSTEM_UNIT" = yes ]; then echo "  found an existing system service: $SYSTEM_UNIT"; fi

if [ -z "$KIND" ]; then
  # An existing install of either kind is the answer to the question, so the
  # question is not asked: an upgrade that offers to change the unit kind is an
  # upgrade that changes it for whoever pressed return without reading.
  if [ "$HAVE_SYSTEM_UNIT" = yes ]; then
    KIND=system
  elif [ "$HAVE_USER_UNIT" = yes ]; then
    KIND=user
  elif [ "$INTERACTIVE" = yes ] && [ "$HAVE_ROOT" = yes ] && [ -f "$SYSTEM_UNIT_SRC" ]; then
    echo "How should the panel run?"
    echo
    echo "  1) systemd *user* service   (default)"
    echo "     Runs as you, with your keys and your dotfiles. Needs no root."
    echo "     Starts at boot once lingering is on, which this will enable."
    echo
    echo "  2) systemd *system* service (root is available on this machine)"
    echo "     Same account, same environment -- it drops to User=$WHO. The one"
    echo "     thing it can do that the user unit cannot is lower the OOM score:"
    echo "     measured, a user unit asking for -500 gets 100, a system unit"
    echo "     gets -500. Worth it if this machine runs close to its memory."
    echo
    ask "  choice [1]: " 1
    echo
    case "$ANSWER" in
      2) KIND=system ;;
      *) KIND=user ;;
    esac
  else
    KIND=user
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

# ── the fallback, said plainly ────────────────────────────────────────────
FELL_BACK=no
if [ "$KIND" = system ] && [ "$HAVE_ROOT" = no ]; then
  echo
  echo "root is not available here (no sudo, or it would need a password and"
  echo "there is nobody to type it), so the system service cannot be installed."
  echo "Installing the user service instead -- it is the right default anyway;"
  echo "the only thing it gives up is the OOM score."
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

# ── start it? ─────────────────────────────────────────────────────────────
#
# Whether the unit is already running decides the wording and, when nobody was
# asked, the default: an upgrade that leaves the old binary running is the
# failure docs/runbook.md has an entry for, so a running unit is restarted
# without being asked. A stopped one is not started behind your back.
sctl_user() { "$SYSTEMCTL" --user "$@"; }
sctl_sys() { as_root "$SYSTEMCTL" "$@"; }

# Asked only when a unit of that kind is already installed. `systemctl --user`
# talks to the manager for the *logged-in* user, which read its own $HOME at
# login and does not care what this script was handed -- so on a first install
# into a different HOME (which is exactly what scripts/release-check.sh does)
# "vibepanel is active" can be true and be somebody else's panel. Restarting
# that would be this installer reaching outside the tree it was pointed at.
RUNNING=no
if command -v "${SYSTEMCTL%% *}" >/dev/null 2>&1; then
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

# ── the plan, before anything happens ─────────────────────────────────────
echo
echo "about to:"
echo "  install  $BIN_DIR/vibepanel"
if [ "$KIND" = user ]; then
  echo "  install  $USER_UNIT   (systemd user service)"
else
  echo "  install  $SYSTEM_UNIT   (systemd system service, as root)"
  echo "           with User=$WHO and HOME=$HOME substituted in"
fi
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

STARTED=no      # no | started | restarted

if [ "$KIND" = user ]; then
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
  TMP_UNIT="$(mktemp)"
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
PORT=8443
if [ -e "$ENV_FILE" ]; then
  ADDR="$(grep -E '^[[:space:]]*VIBEPANEL_ADDR=' "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' "' || true)"
  case "$ADDR" in *:*) P="${ADDR##*:}"; [ -n "$P" ] && PORT="$P" ;; esac
fi
HOST="$(hostname 2>/dev/null || echo localhost)"

if [ "$KIND" = user ]; then
  JOURNAL="journalctl --user -u vibepanel -n 30"
  CTL="systemctl --user"
  WHAT="the systemd user service ($USER_UNIT)"
else
  JOURNAL="sudo journalctl -u vibepanel -n 30"
  CTL="sudo systemctl"
  WHAT="the systemd system service ($SYSTEM_UNIT)"
fi

echo
echo "── done ──"
echo "installed: $WHAT"
case "$STARTED" in
  started)
    echo "state:     started just now"
    echo
    echo "the one-time setup token is in the log:"
    echo "  $JOURNAL"
    echo
    echo "then open  http://$HOST:$PORT  and paste it."
    ;;
  restarted)
    echo "state:     restarted (it was already running)"
    echo "           your sessions are untouched -- they belong to tmux, not to"
    echo "           the panel process."
    echo
    echo "the setup token was consumed at first install. The log, if you need it:"
    echo "  $JOURNAL"
    echo
    echo "the panel is at  http://$HOST:$PORT"
    ;;
  *)
    echo "state:     not started; the files are in place"
    echo
    echo "  $CTL enable --now vibepanel"
    echo "  $JOURNAL   # the one-time setup token"
    echo
    echo "then open  http://$HOST:$PORT"
    ;;
esac

# The other unit, and why it is not the default.
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
