#!/usr/bin/env bash
# Installs vibepanel as a systemd *user* service.
#
#   ./install.sh            # put the files in place, print the next steps
#   ./install.sh --enable   # ...and start it now
#
# Run this from an unpacked release archive. Everything it touches is under
# $HOME; nothing needs root.
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
# The archive layout is <dir>/vibepanel and <dir>/deploy/install.sh, so the
# binary is one level up from this script. Running it from the repo works too.
BIN_SRC="$SRC/../vibepanel"
[ -f "$BIN_SRC" ] || BIN_SRC="$SRC/../../vibepanel"

BIN_DIR="$HOME/.local/bin"
UNIT_DIR="$HOME/.config/systemd/user"
ENV_FILE="$HOME/.config/vibepanel.env"
ENABLE=no

for arg in "$@"; do
  case "$arg" in
    --enable) ENABLE=yes ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

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

mkdir -p "$BIN_DIR" "$UNIT_DIR" "$(dirname "$ENV_FILE")"
install -m 0755 "$BIN_SRC" "$BIN_DIR/vibepanel"
echo "installed $BIN_DIR/vibepanel"
install -m 0644 "$SRC/vibepanel.service" "$UNIT_DIR/vibepanel.service"
echo "installed $UNIT_DIR/vibepanel.service"

# Never overwrite the env file: it is the one file here the user edits, and it
# holds the domain, the TLS choice and any ACME credentials.
if [ -e "$ENV_FILE" ]; then
  echo "kept      $ENV_FILE (already there, left alone)"
else
  install -m 0600 "$SRC/vibepanel.env" "$ENV_FILE"
  echo "installed $ENV_FILE — edit it before exposing the panel"
fi

# Without lingering a user service stops when the last session for that user
# ends. For a panel whose entire purpose is outliving your terminal, that is
# the difference between working and appearing to work until you log out.
#
# Done rather than suggested. "Start at boot" is the thing people install a
# service for, and a printed command is a step that gets skipped -- after which
# the panel works perfectly until the first reboot, which is the worst moment
# to find out. It needs no root for your own account (measured), and the line
# below says what happened so it is not a change made behind your back.
if command -v loginctl >/dev/null; then
  if [ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no)" = yes ]; then
    echo "lingering already on — the panel starts at boot and survives logout"
  elif loginctl enable-linger "$USER" 2>/dev/null; then
    echo "enabled lingering — the panel now starts at boot and survives logout"
    echo "  (undo with: loginctl disable-linger $USER)"
  else
    echo
    echo "could not enable lingering; without it the panel stops when you log out:"
    echo "  loginctl enable-linger $USER"
  fi
fi

if command -v systemctl >/dev/null && systemctl --user daemon-reload 2>/dev/null; then
  if [ "$ENABLE" = yes ]; then
    # `enable --now` is a no-op on a unit that is already enabled and active,
    # which is exactly the state an upgrade finds. The new binary went to disk a
    # few lines above and the old one kept serving, while this printed "started.
    # the one-time setup token is in: journalctl ..." -- a start that did not
    # happen, and a token consumed at first install.
    #
    # `restart` is the whole fix, and this is the one project where it is free:
    # the tmux server outlives the Go process, so every session survives, which
    # is what restart-check exists to prove.
    #
    # Which of the two happened is printed, because "started" and "restarted"
    # are different facts and the setup token only exists for one of them.
    if systemctl --user is-active --quiet vibepanel; then
      systemctl --user restart vibepanel
      echo
      echo "restarted. your sessions are untouched -- they belong to tmux, not to"
      echo "the panel process. the setup token was consumed at first install;"
      echo "if you need to see the log:"
      echo "  journalctl --user -u vibepanel -n 30"
    else
      systemctl --user enable --now vibepanel
      echo
      echo "started. the one-time setup token is in:"
      echo "  journalctl --user -u vibepanel -n 30"
    fi
  else
    echo
    echo "  systemctl --user enable --now vibepanel"
    echo "  journalctl --user -u vibepanel -n 30   # the one-time setup token"
  fi
else
  echo
  echo "no user systemd session here; from a login shell on that machine:"
  echo "  systemctl --user daemon-reload && systemctl --user enable --now vibepanel"
fi

# The other unit, and why it is not the default.
#
# A user service cannot lower its own oom_score_adj -- the kernel refuses
# without CAP_SYS_RESOURCE, and `systemd-analyze verify` accepts the directive
# anyway, so it is a setting that looks applied and does nothing. Measured: a
# user unit asking for -500 gets 100; a system unit with User= gets -500.
#
# Most people do not need it. Say so once, here, rather than in a README nobody
# opens while installing.
echo
echo "if this machine runs close to its memory and you want the kernel to look"
echo "elsewhere first, there is a system unit that can actually say so:"
echo "  deploy/vibepanel-system.service   (needs root; the user unit cannot)"
