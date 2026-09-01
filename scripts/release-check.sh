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
for f in vibepanel LICENSE README.md deploy/vibepanel.service \
         deploy/vibepanel-system.service deploy/vibepanel.env \
         deploy/io.github.jiangmuran.vibepanel.plist; do
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
# Every invocation below gets the throwaway socket, not just the server.
#
# SOCK used to be set further down, next to the one command that was given it,
# so `vibepanel doctor` above ran against the *default* socket — and doctor
# calls EnsureServer, so it started a tmux server named `vibepanel` on the
# machine running the check and left it there. Six hours later it was still
# running, holding a config file in a temp directory that no longer existed.
# On a machine where the panel is actually deployed that is the user's socket.
SOCK="vprelease-$$"
export VIBEPANEL_TMUX_SOCKET="$SOCK"
D="$("$DIR/vibepanel" doctor 2>&1)"; DRC=$?
echo "$D" | sed 's/^/       /'
[ $DRC -eq 0 ] && ok "doctor exits 0 on a healthy machine" || fail "doctor exited $DRC"
echo "$D" | grep -qi "tmux" && ok "doctor mentions tmux" || fail "doctor says nothing about tmux"

# A diagnostic has to keep diagnosing.
#
# It used to return at the first failure, so a machine with three problems took
# three runs to find them: fix the data directory, run again, discover the
# database, run again, discover the isolation. It also returned the error it
# had just printed, so every failure appeared twice and the report read like a
# crash.
BAD="$WORK/unwritable"; mkdir -p "$BAD"; chmod 555 "$BAD"
E="$(VIBEPANEL_DATA_DIR="$BAD" "$DIR/vibepanel" doctor 2>&1)"; ERC=$?
chmod 755 "$BAD"
[ $ERC -ne 0 ] && ok "doctor exits non-zero when a check fails" \
  || fail "doctor exited 0 with an unusable data directory"
echo "$E" | grep -q "FAIL.*data dir" && ok "doctor names the unusable data directory" \
  || fail "doctor did not report the data directory: $E"
echo "$E" | grep -q "skipped:" && ok "doctor says which checks it could not run" \
  || fail "doctor stopped at the first failure instead of reporting the rest: $E"
echo "$E" | grep -qc "environment" >/dev/null && \
  echo "$E" | grep -q "environment" && ok "doctor still reaches the later checks" \
  || fail "checks after the failure never ran: $E"
if [ "$(echo "$E" | grep -c "permission denied")" -le 1 ]; then
  ok "the failure is reported once, not echoed again by main"
else
  fail "the same error is printed more than once: $E"
fi

echo "==> the first-run wizard"
PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
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

echo "==> a session created from the command line can report its state"
# The admin CLI builds sessions too, and it used to inject two of the three
# variables report.sh needs — no token and no address. The script suppresses
# its own errors by design, so a session made this way installed cleanly and
# reported nothing, forever, in a panel whose settings page said hooks were
# installed. Nothing but looking inside the session would show it.
kill "$SRV" 2>/dev/null; SRV=""
sleep 0.5
CLI_ENV_OK=1
PROJ_DIR="$WORK/cliproj"; mkdir -p "$PROJ_DIR"
HOME="$WORK/home" VIBEPANEL_DATA_DIR="$WORK/data" \
  ./vibepanel project add --path "$PROJ_DIR" --name cliproj >"$WORK/cli.log" 2>&1 \
  || { fail "project add failed: $(tail -2 "$WORK/cli.log")"; CLI_ENV_OK=0; }
if [ "$CLI_ENV_OK" = 1 ]; then
  # `project add` prints "created project <id> <name> <path>".
  PROJ_ID=$(grep -o 'created project [0-9a-f]*' "$WORK/cli.log" | head -1 | awk '{print $3}')
  [ -n "$PROJ_ID" ] || { fail "could not read a project id back: $(tail -2 "$WORK/cli.log")"; CLI_ENV_OK=0; }
fi
if [ "$CLI_ENV_OK" = 1 ]; then
  HOME="$WORK/home" VIBEPANEL_DATA_DIR="$WORK/data" \
    ./vibepanel session new --project "$PROJ_ID" --title clitest -- sleep 300 >>"$WORK/cli.log" 2>&1 \
    || { fail "session new failed: $(tail -2 "$WORK/cli.log")"; CLI_ENV_OK=0; }
fi
if [ "$CLI_ENV_OK" = 1 ]; then
  SESS=$(tmux -L "$SOCK" list-sessions -F '#{session_name}' 2>/dev/null | head -1)
  ENV_OUT=$(tmux -L "$SOCK" show-environment -t "=$SESS:" 2>&1 || true)
  MISSING=""
  for V in VIBEPANEL_SESSION_ID VIBEPANEL_TOKEN VIBEPANEL_URL; do
    printf '%s' "$ENV_OUT" | grep -q "^$V=" || MISSING="$MISSING $V"
  done
  [ -z "$MISSING" ] && ok "a CLI-created session carries every variable the hook reads" \
    || fail "a CLI-created session is missing:$MISSING"
fi

echo "==> the bootstrap the one-liner runs"
# Not fetched. scripts/install-check.sh drives install.sh against a local HTTP
# server; here the only question is whether the file people are told to pipe
# into sh is present, executable and syntactically valid under a POSIX shell --
# a bashism in it is invisible on a machine whose /bin/sh is bash, and fatal on
# Debian and Alpine, which is most of them.
[ -f "$REPO/install.sh" ] && ok "the repository ships install.sh at its root" \
  || fail "no install.sh at the repository root; the one-liner has nothing to fetch"
if [ -f "$REPO/install.sh" ]; then
  for SHELL_BIN in dash busybox sh; do
    case "$SHELL_BIN" in
      busybox) command -v busybox >/dev/null 2>&1 && { busybox sh -n "$REPO/install.sh" \
                 && ok "install.sh parses under busybox sh" \
                 || fail "install.sh does not parse under busybox sh"; } ;;
      *) command -v "$SHELL_BIN" >/dev/null 2>&1 && { "$SHELL_BIN" -n "$REPO/install.sh" \
           && ok "install.sh parses under $SHELL_BIN" \
           || fail "install.sh does not parse under $SHELL_BIN"; } ;;
    esac
  done
fi

echo "==> the documented install path"
[ -x "$DIR/deploy/install.sh" ] && ok "the archive ships an executable install.sh" \
  || fail "the archive has no install script; the unit expects the binary at a path nothing puts it in"
if [ -x "$DIR/deploy/install.sh" ]; then
  # --yes explicitly, even though a redirected stdout already turns the prompts
  # off. This runs under `make`, which may hand the script a terminal on stdin,
  # and a check that hangs waiting for an answer nobody sees is worse than one
  # that fails. scripts/install-check.sh is where the interactive path is
  # driven; here the question is only whether the shipped archive installs.
  #
  # --no-enable for a sharper reason: `systemctl --user` reaches the manager for
  # the logged-in user, which does not care what HOME this check sets. On a
  # machine where the developer runs the panel, a run that decides to start or
  # restart "vibepanel" would be starting or restarting theirs.
  #
  # VIBEPANEL_ROOT_CMD=none is new and is not decoration. The recommended
  # default is now the system service wherever root is available, so on a
  # machine with passwordless sudo -- which is every CI runner -- an unadorned
  # run of this would write /etc/systemd/system/vibepanel.service for real and
  # enable it. `none` is the documented way to say "root is not available
  # here", and it makes this check exercise exactly the branch it is asserting
  # about: the no-root default.
  ( cd "$DIR" && HOME="$WORK/home" VIBEPANEL_ROOT_CMD=none \
      ./deploy/install.sh --yes --no-enable >"$WORK/install.log" 2>&1 )
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
  # The last thing it prints is the only summary anybody reads, and it has been
  # wrong before: "started" printed over an `enable --now` that was a no-op.
  grep -q "installed: the systemd user service" "$WORK/install.log" \
    && ok "it says which unit it installed" \
    || fail "the installer did not name the unit it installed: $(tail -3 "$WORK/install.log" | tr '\n' ' ')"
  grep -q "installed: the systemd system service" "$WORK/install.log" \
    && fail "it installed the system unit with no root available" \
    || ok "with no root, the unattended default is the user unit"
  grep -q "root is not available here" "$WORK/install.log" \
    && ok "and it says why rather than falling back silently" \
    || fail "it chose the user unit without saying root was unavailable"

  # An edited env file must survive a reinstall — it holds the domain and any
  # ACME credentials.
  echo "VIBEPANEL_DOMAIN=edited.example" > "$WORK/home/.config/vibepanel.env"
  ( cd "$DIR" && HOME="$WORK/home" VIBEPANEL_ROOT_CMD=none \
      ./deploy/install.sh --yes --no-enable >/dev/null 2>&1 ) || true
  grep -q "edited.example" "$WORK/home/.config/vibepanel.env" \
    && ok "reinstalling keeps an edited env file" \
    || fail "reinstalling overwrote the env file the user had edited"

  if command -v systemd-analyze >/dev/null; then
    UNIT_OUT="$(HOME="$WORK/home" systemd-analyze verify "$WORK/home/.config/systemd/user/vibepanel.service" 2>&1 || true)"

    # Prove the tool works here before trusting its silence.
    #
    # This check used to read "no complaint about our unit" as a pass, so if
    # systemd-analyze could not run at all — no dbus, a sandbox it cannot
    # enter, an early error — it printed "the installed unit verifies" about a
    # file it had never opened. Absence of evidence is not evidence.
    #
    # The exit status is no good as a substitute: this machine has unrelated
    # units with deprecated directives, so a non-zero exit says nothing about
    # ours. A control does: a unit with an invented directive must be objected
    # to, and if it is not, silence about the real unit means nothing either.
    BROKEN="$WORK/broken.service"
    printf '[Unit]\nDescription=control\n[Service]\nExecStart=/bin/true\nNoSuchDirective=yes\n' > "$BROKEN"
    CONTROL_OUT="$(HOME="$WORK/home" systemd-analyze verify "$BROKEN" 2>&1 || true)"
    if ! printf '%s' "$CONTROL_OUT" | grep -q "broken.service"; then
      # Carry what it actually said. Without this the message asserts a
      # conclusion about the tool and offers nothing to check it against, and
      # the whole point of the control is that conclusions need evidence.
      fail "systemd-analyze reports nothing wrong with a deliberately broken unit here, so its silence about ours proves nothing. It said: $(printf '%s' "$CONTROL_OUT" | head -3 | tr '\n' ' ')"
    elif echo "$UNIT_OUT" | grep -q "vibepanel.service:"; then
      fail "systemd-analyze objected to the installed unit: $(echo "$UNIT_OUT" | grep 'vibepanel.service:' | head -2 | tr '\n' ' ')"
    else
      ok "the installed unit verifies"
    fi
  fi
fi

echo "==> the management command, against what the install just wrote"
# The mapping is unit-tested; what cannot be unit-tested is whether the real
# binary, on the real files the real installer produced, resolves to the same
# thing. --dry-run so nothing is started, stopped or restarted on the machine
# running this.
#
# VIBEPANEL_DESTDIR as well as HOME, and it is the difference between a check
# and a check that only passes somewhere. `service` resolves the system unit
# first -- deliberately, because that is the order the installer resolves an
# ambiguous re-run in -- so on a machine that actually runs the panel as a
# system service it found /etc/systemd/system/vibepanel.service, answered
# `sudo systemctl status vibepanel`, and both assertions below failed for a
# reason that has nothing to do with the release. That is the worst kind of red:
# it fires on exactly the machines where somebody is most likely to shrug at it,
# and the next real failure here is the one they shrug at too.
#
# $WORK/root has no system unit because the install above had no root and wrote
# a user one, which is the state these two lines are asserting about.
SVCENV=(HOME="$WORK/home" VIBEPANEL_DESTDIR="$WORK/root")
SVC="$(env "${SVCENV[@]}" "$DIR/vibepanel" service --dry-run status 2>&1)"
echo "$SVC" | grep -q "systemctl --user status vibepanel" \
  && ok "it resolves the installed user unit: $SVC" \
  || fail "vibepanel service did not find the unit the installer wrote: $SVC"
SVCU="$(env "${SVCENV[@]}" "$DIR/vibepanel" service --dry-run uninstall 2>&1)"
echo "$SVCU" | grep -q "$WORK/home/.config/systemd/user/vibepanel.service" \
  && ok "uninstall names the unit file it would remove" \
  || fail "uninstall does not name the right file: $SVCU"
echo "$SVCU" | grep -q "$WORK/data" \
  && fail "uninstall would remove the data directory" \
  || ok "and would not touch the data directory"

echo "==> the first account, created from the command line"
# The other half of the installer's --username: the same argon2id path the
# browser wizard uses, exercised against a real database rather than a stub.
ACCTDIR="$WORK/acctdata"
PWF="$WORK/acct.pw"
printf 'a sufficiently long password\n' > "$PWF"
A_OUT="$(HOME="$WORK/home" VIBEPANEL_DATA_DIR="$ACCTDIR" \
  "$DIR/vibepanel" account create --username admin --password-file "$PWF" 2>&1)"
A_RC=$?
[ $A_RC -eq 0 ] && ok "it creates the first account: $(echo "$A_OUT" | head -1)" \
  || fail "account create exited $A_RC: $A_OUT"
# The safety property: it is never a password reset. Anybody who can run the
# binary can run this, and under the system unit that is a wider set than the
# people who should be able to change the password.
A_OUT2="$(HOME="$WORK/home" VIBEPANEL_DATA_DIR="$ACCTDIR" \
  "$DIR/vibepanel" account create --username admin --password-file "$PWF" 2>&1)"
A_RC2=$?
[ $A_RC2 -ne 0 ] && ok "and refuses to create a second one" \
  || fail "it created a second account, which makes it a password reset"
echo "$A_OUT2" | grep -q "already has an account" && ok "saying why" \
  || fail "the refusal does not explain itself: $A_OUT2"
# And a password on the command line is refused rather than accepted quietly:
# `ps` shows it to every other user on the machine for as long as it runs.
A_OUT3="$(HOME="$WORK/home" VIBEPANEL_DATA_DIR="$WORK/acctdata2" \
  "$DIR/vibepanel" account create --username admin --password hunter2hunter2 2>&1)"
[ $? -ne 0 ] && ok "a password on the command line is refused" \
  || fail "--password was accepted"
echo "$A_OUT3" | grep -q "shell history" && ok "with the reason" || fail "no reason given: $A_OUT3"

# ── the container image ───────────────────────────────────────────────────
#
# Nothing built it. No target, no script, none of the seven checks -- while the
# Dockerfile pins node:24-alpine, golang:1.26-alpine and alpine:3.21, and
# deploy/docker-compose.yml builds from it. A shipped artifact with exactly the
# property head-check was written to remove: nothing told you whether what was
# committed works.
#
# Built *and* run, because building is not the question. The first time this
# ran, both worked -- the image came up and /api/health answered ok, with tmux
# 3.5a from alpine:3.21 rather than the 3.6 on the host, which is past the 3.3
# that doctor calls the floor.
#
# Skipped rather than failed without docker: a machine that builds release
# archives is not required to have a container runtime, and a FAIL there teaches
# people to skip this output.
echo
if ! command -v docker >/dev/null 2>&1; then
  echo "[--  ] container image: skipped, no docker on this machine"
elif [ "${SKIP_DOCKER:-}" = "1" ]; then
  echo "[--  ] container image: skipped, SKIP_DOCKER=1"
else
  IMG="vibepanel-release-check:$$"
  # $REPO, not "." -- this script does not run from the repository root, and
  # the first version of this failed with "open Dockerfile: no such file or
  # directory" while the image it was meant to test built fine by hand.
  if ! docker build -q -t "$IMG" "$REPO" >/dev/null 2>"$WORK/docker-build.log"; then
    fail "docker build failed: $(tail -3 "$WORK/docker-build.log" | tr '\n' ' ')"
  else
    ok "the container image builds"
    DATA="$WORK/container-data"
    mkdir -p "$DATA" && chmod 777 "$DATA"
    PORT=18499
    CID="$(docker run -d --rm -p "$PORT:18443" \
      -e VIBEPANEL_ADDR=0.0.0.0:18443 -e VIBEPANEL_DOMAIN=localhost \
      -v "$DATA:/data" "$IMG" 2>"$WORK/docker-run.log" || true)"
    if [ -z "$CID" ]; then
      fail "the image would not start: $(tail -3 "$WORK/docker-run.log" | tr '\n' ' ')"
    else
      UP=""
      for _ in $(seq 60); do
        if curl -sf --max-time 3 "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then UP=1; break; fi
        sleep 1
      done
      if [ -z "$UP" ]; then
        fail "the container ran and never answered /api/health: $(docker logs "$CID" 2>&1 | tail -3 | tr '\n' ' ')"
      else
        ok "the container answers /api/health ($(curl -s --max-time 3 "http://127.0.0.1:$PORT/api/health"))"
      fi
      docker rm -f "$CID" >/dev/null 2>&1 || true
    fi
    docker image rm -f "$IMG" >/dev/null 2>&1 || true
  fi
fi

echo
if [ "$FAILS" -eq 0 ]; then echo "=== release check: 0 FAIL ==="; else echo "=== release check: $FAILS FAIL ==="; fi
exit "$FAILS"
