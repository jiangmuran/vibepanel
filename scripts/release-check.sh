#!/usr/bin/env bash
# Does a release archive actually run on a machine that knows nothing about
# this repo?
#
#   scripts/release-check.sh
#
# The acceptance criterion for the whole distribution story is "unpack the
# tar.gz on a machine with tmux and it runs, through the first-run wizard".
# That is a claim about a file nobody in the development loop ever touches, so
# it is the one most likely to be quietly false: this builds the archives,
# unpacks one somewhere with a throwaway HOME, and drives it.
#
# Writes only to a mktemp directory and to dist/. Nothing needs root.
set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
FAILS=0
fail() { echo "[FAIL] $*"; FAILS=$((FAILS + 1)); }
ok() { echo "[ ok ] $*"; }
cleanup() {
  [ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
  [ -n "${SOCK:-}" ] && tmux -L "$SOCK" kill-server 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> building the release archives"
( cd "$REPO" && ./scripts/build-release.sh test-release >"$WORK/build.log" 2>&1 ) \
  || { sed -n '$p;1,20p' "$WORK/build.log"; fail "build-release.sh exited non-zero"; exit 1; }
ls -1 "$REPO/dist" | sed 's/^/       /'

ARCH="$REPO/dist/vibepanel_test-release_linux_amd64.tar.gz"
[ -f "$ARCH" ] || { fail "no linux/amd64 archive was produced"; exit 1; }

echo "==> checksums"
( cd "$REPO/dist" && sha256sum -c SHA256SUMS >/dev/null 2>&1 ) \
  && ok "SHA256SUMS verifies" || fail "SHA256SUMS does not verify"

echo "==> unpacking somewhere that knows nothing"
mkdir -p "$WORK/clean"
tar -xzf "$ARCH" -C "$WORK/clean" || fail "the archive does not extract"
DIR="$WORK/clean/vibepanel_test-release_linux_amd64"
for f in vibepanel LICENSE README.md deploy/vibepanel.service deploy/vibepanel.env; do
  [ -e "$DIR/$f" ] && ok "ships $f" || fail "the archive is missing $f"
done
[ -x "$DIR/vibepanel" ] && ok "the binary is executable" || fail "the binary is not executable"

echo "==> is it really static?"
if command -v ldd >/dev/null; then
  # Captured first: ldd exits non-zero for a static binary, and under pipefail
  # that fails the pipeline even when the grep matches.
  LDD="$(ldd "$DIR/vibepanel" 2>&1 || true)"
  case "$LDD" in
    *"not a dynamic executable"*|*"statically linked"*) ok "statically linked" ;;
    *) fail "the binary is dynamically linked: $(echo "$LDD" | head -3 | tr '\n' ' ')" ;;
  esac
fi

echo "==> version, stamped by the build"
V="$("$DIR/vibepanel" version 2>&1)"
echo "$V" | grep -q "test-release" && ok "reports its version: $(echo "$V" | head -1)" \
  || fail "version does not mention the release it was built as: $V"

echo "==> doctor, on a machine with nothing set up"
export HOME="$WORK/home"; mkdir -p "$HOME"
D="$("$DIR/vibepanel" doctor 2>&1)"; DRC=$?
echo "$D" | sed 's/^/       /'
[ $DRC -eq 0 ] && ok "doctor exits 0 on a healthy machine" || fail "doctor exited $DRC"
echo "$D" | grep -qi "tmux" && ok "doctor mentions tmux" || fail "doctor says nothing about tmux"

echo "==> the first-run wizard"
PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
SOCK="vprelease-$$"
cd "$DIR"
HOME="$WORK/home" VIBEPANEL_DATA_DIR="$WORK/data" VIBEPANEL_TMUX_SOCKET="$SOCK" \
  VIBEPANEL_ADDR="127.0.0.1:$PORT" VIBEPANEL_DOMAIN=localhost \
  ./vibepanel serve >"$WORK/serve.log" 2>&1 &
SRV=$!
for _ in $(seq 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null \
  && ok "serves /api/health straight out of the archive" \
  || { fail "the unpacked binary never served a request"; sed -n '1,20p' "$WORK/serve.log"; }

TOK=$(grep -A2 'one-time setup token' "$WORK/serve.log" | tail -1 | tr -d ' \r')
[ -n "$TOK" ] && ok "prints a one-time setup token" || fail "no setup token in the log"
CODE=$(curl -s -o "$WORK/setup.json" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PORT/api/auth/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOK\",\"username\":\"admin\",\"password\":\"a sufficiently long password\"}")
# 201: the first account is created, not updated.
[ "$CODE" = 201 ] && ok "the wizard accepts the token and sets a password" \
  || fail "setup returned $CODE: $(cat "$WORK/setup.json")"

# The embedded frontend is the other half of "self-contained".
BODY=$(curl -s "http://127.0.0.1:$PORT/")
echo "$BODY" | grep -qi "<div id=\"root\"\|<script" \
  && ok "serves the embedded frontend" || fail "the root path served no app: ${BODY:0:120}"
ASSET=$(echo "$BODY" | grep -o '/assets/[^"]*\.js' | head -1)
if [ -n "$ASSET" ]; then
  ACODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT$ASSET")
  [ "$ACODE" = 200 ] && ok "serves its own assets ($ASSET)" || fail "asset $ASSET returned $ACODE"
else
  fail "the served page references no built asset"
fi

echo "==> the documented install path"
kill "$SRV" 2>/dev/null; SRV=""
[ -x "$DIR/deploy/install.sh" ] && ok "the archive ships an executable install.sh" \
  || fail "the archive has no install script; the unit expects the binary at a path nothing puts it in"
if [ -x "$DIR/deploy/install.sh" ]; then
  ( cd "$DIR" && HOME="$WORK/home" ./deploy/install.sh >"$WORK/install.log" 2>&1 )
  RC=$?
  sed 's/^/       /' "$WORK/install.log"
  [ $RC -eq 0 ] && ok "install.sh exits 0" || fail "install.sh exited $RC"
  # The unit hardcodes %h/.local/bin/vibepanel; the whole point is that the
  # install puts it exactly there.
  [ -x "$WORK/home/.local/bin/vibepanel" ] && ok "the binary landed where the unit looks for it" \
    || fail "no binary at ~/.local/bin/vibepanel, which is what ExecStart names"
  [ -f "$WORK/home/.config/systemd/user/vibepanel.service" ] && ok "the unit is installed" \
    || fail "the unit was not installed"
  [ -f "$WORK/home/.config/vibepanel.env" ] && ok "an env file is in place" \
    || fail "no env file was installed"
  grep -qi "linger" "$WORK/install.log" && ok "it mentions lingering" \
    || fail "nothing said about lingering; the service would die at logout"

  # An edited env file must survive a reinstall — it holds the domain and any
  # ACME credentials.
  echo "VIBEPANEL_DOMAIN=edited.example" > "$WORK/home/.config/vibepanel.env"
  ( cd "$DIR" && HOME="$WORK/home" ./deploy/install.sh >/dev/null 2>&1 ) || true
  grep -q "edited.example" "$WORK/home/.config/vibepanel.env" \
    && ok "reinstalling keeps an edited env file" \
    || fail "reinstalling overwrote the env file the user had edited"

  if command -v systemd-analyze >/dev/null; then
    UNIT_OUT="$(HOME="$WORK/home" systemd-analyze verify "$WORK/home/.config/systemd/user/vibepanel.service" 2>&1 || true)"
    if echo "$UNIT_OUT" | grep -q "vibepanel.service:"; then
      fail "systemd-analyze objected to the installed unit: $(echo "$UNIT_OUT" | grep 'vibepanel.service:' | head -2 | tr '\n' ' ')"
    else
      ok "the installed unit verifies"
    fi
  fi
fi

echo
if [ "$FAILS" -eq 0 ]; then echo "=== release check: 0 FAIL ==="; else echo "=== release check: $FAILS FAIL ==="; fi
exit "$FAILS"
