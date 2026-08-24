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
if command -v loginctl >/dev/null; then
  if [ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no)" = yes ]; then
    echo "lingering already enabled"
  else
    echo
    echo "  loginctl enable-linger $USER    # so the panel survives logout"
  fi
fi

if command -v systemctl >/dev/null && systemctl --user daemon-reload 2>/dev/null; then
  if [ "$ENABLE" = yes ]; then
    systemctl --user enable --now vibepanel
    echo
    echo "started. the one-time setup token is in:"
    echo "  journalctl --user -u vibepanel -n 30"
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
