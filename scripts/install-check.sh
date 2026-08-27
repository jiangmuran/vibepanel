#!/usr/bin/env bash
# Does deploy/install.sh do what it says, on every path through it?
#
#   scripts/install-check.sh
#
# The installer is the one piece of this project that runs *before* anything
# else works, on a machine nobody has looked at, and it is shell -- so it has no
# type system, no test framework and no compiler to catch the branch nobody
# took. It now has four decisions in it (interactive or not, user or system unit,
# root or no root, an existing install of the other kind), which is sixteen
# corners, and the interesting ones write to /etc and call `systemctl enable
# --now`.
#
# So: a throwaway HOME, a DESTDIR-style prefix instead of /etc, and a recorder
# instead of systemctl. Nothing here runs sudo, and nothing here reaches the
# systemd user manager -- which reads the *real* $HOME whatever this script sets,
# so `systemctl --user disable --now vibepanel` in the migration path would
# otherwise stop the panel of whoever is running the check.
#
# The binary it installs is a stub. The installer never executes it, and making
# this check depend on a Go build would make it slow enough to stop being run.
set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
FAILS=0
fail() { echo "[FAIL] $*"; FAILS=$((FAILS + 1)); }
ok() { echo "[ ok ] $*"; }
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

if ! command -v tmux >/dev/null 2>&1; then
  echo "install-check needs tmux: the installer refuses to run without it, which"
  echo "is itself one of the behaviours below."
  exit 1
fi

# ── the release layout the installer expects ──────────────────────────────
REL="$WORK/rel"
mkdir -p "$REL/deploy"
printf '#!/bin/sh\necho stub\n' > "$REL/vibepanel"
chmod +x "$REL/vibepanel"
cp "$REPO/deploy/install.sh" "$REL/deploy/"
cp "$REPO/deploy/vibepanel.service" "$REPO/deploy/vibepanel.env" \
   "$REPO/deploy/vibepanel-system.service" "$REL/deploy/"
chmod +x "$REL/deploy/install.sh"

# ── the doubles ───────────────────────────────────────────────────────────
#
# The recorder answers `is-active` from a marker file, because "the unit is
# already running" is what tells an upgrade to restart rather than start, and
# that is a branch with its own wording.
SCTL="$WORK/fake-systemctl"
cat > "$SCTL" <<EOF
#!/usr/bin/env bash
echo "\$*" >> "$WORK/systemctl.log"
case "\$*" in
  *is-active*) [ -e "$WORK/unit-is-active" ] && exit 0 || exit 1 ;;
esac
exit 0
EOF
chmod +x "$SCTL"

# Runs the command for real, so the unit really is written and really is
# substituted -- into $WORK, not into /etc.
ROOTCMD="$WORK/fake-root"
cat > "$ROOTCMD" <<EOF
#!/usr/bin/env bash
echo "root: \$*" >> "$WORK/root.log"
exec "\$@"
EOF
chmod +x "$ROOTCMD"

CASE=0
HOME_DIR=""
LOG=""
RC=0
newhome() {
  CASE=$((CASE + 1))
  HOME_DIR="$WORK/home$CASE"
  mkdir -p "$HOME_DIR"
  LOG="$WORK/log$CASE"
  : > "$WORK/systemctl.log"
  : > "$WORK/root.log"
  rm -f "$WORK/unit-is-active"
}
# run [--stdin <file>] -- <args...>   with the current throwaway HOME
run() {
  local input=/dev/null
  if [ "${1:-}" = "--stdin" ]; then input="$2"; shift 2; fi
  ( cd "$REL" && HOME="$HOME_DIR" \
      VIBEPANEL_DESTDIR="$HOME_DIR/root" \
      VIBEPANEL_SYSTEMCTL="$SCTL" \
      VIBEPANEL_ROOT_CMD="${ROOT_OVERRIDE-$ROOTCMD}" \
      ./deploy/install.sh "$@" <"$input" >"$LOG" 2>&1 )
  RC=$?
}
has() { grep -qF -- "$2" "$1"; }
SYSU() { echo "$HOME_DIR/root/etc/systemd/system/vibepanel.service"; }
USRU() { echo "$HOME_DIR/.config/systemd/user/vibepanel.service"; }

# ── unattended: what CI and `curl | bash` get ─────────────────────────────
echo "==> unattended, no flags (this is what release-check runs)"
newhome
run
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
[ -x "$HOME_DIR/.local/bin/vibepanel" ] && ok "the binary landed where the unit looks for it" \
  || fail "no binary at ~/.local/bin/vibepanel"
[ -f "$(USRU)" ] && ok "the user unit is installed" || fail "no user unit"
[ -f "$HOME_DIR/.config/vibepanel.env" ] && ok "an env file is in place" || fail "no env file"
has "$LOG" linger && ok "it mentions lingering" || fail "nothing said about lingering"
[ -f "$(SYSU)" ] && fail "it wrote a system unit nobody asked for" \
  || ok "it did not touch /etc"
# The whole point of the TTY test: with output redirected there must be no
# question, or every unattended caller hangs forever on an answer it cannot see
# being asked for.
has "$LOG" "proceed?" && fail "it asked a question with no terminal attached" \
  || ok "asks nothing when stdout is not a terminal"
has "$LOG" "installed: the systemd user service" && ok "it says which unit it installed" \
  || fail "the summary does not name the unit: $(tail -3 "$LOG" | tr '\n' ' ')"
has "$LOG" "not started" && ok "it says the service was not started" \
  || fail "it did not say the service is not running"

# `systemctl --user` answers for the logged-in user's manager whatever HOME this
# script sets, so on a first install "vibepanel is active" would be a different
# panel entirely -- and the next line would restart it.
: > "$WORK/systemctl.log"
touch "$WORK/unit-is-active"
newhome
touch "$WORK/unit-is-active"
run
has "$WORK/systemctl.log" "is-active" \
  && fail "it asked whether vibepanel was running before installing a unit of its own" \
  || ok "a first install does not ask about a unit that is not there yet"
has "$WORK/systemctl.log" "restart" && fail "it restarted a panel it did not install" \
  || ok "and restarts nothing"
rm -f "$WORK/unit-is-active"

echo "==> --yes is the same thing, said explicitly"
newhome
run --yes
[ $RC -eq 0 ] && [ -f "$(USRU)" ] && ok "--yes installs the user unit and exits 0" \
  || fail "--yes exited $RC"

echo "==> an edited env file survives a reinstall"
echo "VIBEPANEL_DOMAIN=edited.example" > "$HOME_DIR/.config/vibepanel.env"
run --yes
grep -q edited.example "$HOME_DIR/.config/vibepanel.env" \
  && ok "reinstalling keeps an edited env file" \
  || fail "reinstalling overwrote the env file the user had edited"

echo "==> the URL it prints follows the env file"
newhome
mkdir -p "$HOME_DIR/.config"
echo "VIBEPANEL_ADDR=:9999" > "$HOME_DIR/.config/vibepanel.env"
run --yes
has "$LOG" ":9999" && ok "the printed URL uses the configured port" \
  || fail "it printed a port the env file does not say: $(grep -i http "$LOG" | tr '\n' ' ')"

# ── --enable, and the upgrade that must restart ───────────────────────────
echo "==> --enable starts it, and says where the token is"
newhome
run --yes --enable
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC: $(tail -3 "$LOG" | tr '\n' ' ')"
has "$WORK/systemctl.log" "--user enable --now vibepanel" \
  && ok "it enabled the user unit" || fail "no enable --now: $(cat "$WORK/systemctl.log")"
has "$LOG" "started just now" && ok "it says it started the service" || fail "no 'started' line"
has "$LOG" "journalctl --user -u vibepanel" && ok "it says how to read the setup token" \
  || fail "nothing about the setup token"

echo "==> an upgrade over a running unit restarts it, without being asked"
# The failure this exists for is in docs/runbook.md: the new binary on disk and
# the old one still serving, with nothing wrong anywhere.
#
# Installed first, then marked active, because an upgrade is by definition the
# second run -- and because the installer only asks whether the unit is running
# once a unit of that kind exists in this HOME.
newhome
run --yes
: > "$WORK/systemctl.log"
touch "$WORK/unit-is-active"
run
has "$WORK/systemctl.log" "--user restart vibepanel" \
  && ok "it restarted rather than pretending to start" \
  || fail "a running unit was not restarted: $(cat "$WORK/systemctl.log")"
has "$WORK/systemctl.log" "enable --now" && fail "it also ran enable --now, which is the no-op that hid this" \
  || ok "it did not run enable --now on an active unit"
has "$LOG" "restarted (it was already running)" && ok "it says restarted, not started" \
  || fail "the summary claims a start that did not happen"
has "$LOG" "token was consumed at first install" && ok "it does not promise a token that is gone" \
  || fail "it offered a one-time token on a restart"
rm -f "$WORK/unit-is-active"

# ── the system unit ───────────────────────────────────────────────────────
echo "==> --system, with root available"
newhome
run --yes --system --enable
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
U="$(SYSU)"
if [ -f "$U" ]; then
  ok "the system unit is installed at /etc/systemd/system/vibepanel.service"
  grep -q '__USER__\|__HOME__' "$U" \
    && fail "the placeholders were never substituted; systemd would refuse this unit" \
    || ok "__USER__ and __HOME__ are substituted"
  grep -qx "User=${USER:-$(id -un)}" "$U" && ok "User= is the invoking account" \
    || fail "User= is wrong: $(grep '^User=' "$U")"
  grep -qx "ExecStart=$HOME_DIR/.local/bin/vibepanel serve" "$U" \
    && ok "ExecStart points at the binary this run installed" \
    || fail "ExecStart is wrong: $(grep '^ExecStart=' "$U")"
  # The entire reason the system unit exists.
  grep -qx "OOMScoreAdjust=-500" "$U" && ok "it is the unit that can lower the OOM score" \
    || fail "no OOMScoreAdjust in the installed unit"
  grep -qx "KillMode=process" "$U" && ok "KillMode=process survived the substitution" \
    || fail "KillMode is not process; a restart would kill every session"
else
  fail "no system unit was written"
fi
[ -f "$(USRU)" ] && fail "it installed the user unit as well" || ok "no user unit alongside it"
has "$WORK/systemctl.log" "daemon-reload" && ok "it reloaded the system manager" \
  || fail "no daemon-reload: $(cat "$WORK/systemctl.log")"
grep -q -- "^enable --now vibepanel$" "$WORK/systemctl.log" \
  && ok "it enabled the system unit (not the user one)" \
  || fail "the system unit was never enabled: $(cat "$WORK/systemctl.log")"
has "$LOG" "installed: the systemd system service" && ok "the summary names the system service" \
  || fail "the summary does not say which unit was installed"
has "$LOG" "sudo journalctl -u vibepanel" && ok "it gives the system journal command" \
  || fail "it printed the --user journal command for a system unit"

# Does systemd accept what the substitution produced? A unit with a mangled
# ExecStart installs fine and fails at `enable --now`, on a machine somebody has
# just handed their root password to.
#
# The control comes first, exactly as in release-check.sh: if systemd-analyze
# cannot run here at all, its silence about our unit means nothing.
if [ -f "$U" ] && command -v systemd-analyze >/dev/null 2>&1; then
  BROKEN="$WORK/broken.service"
  printf '[Unit]\nDescription=control\n[Service]\nExecStart=/bin/true\nNoSuchDirective=yes\n' > "$BROKEN"
  CONTROL_OUT="$(systemd-analyze verify "$BROKEN" 2>&1 || true)"
  UNIT_OUT="$(systemd-analyze verify "$U" 2>&1 || true)"
  if ! printf '%s' "$CONTROL_OUT" | grep -q "broken.service"; then
    echo "[--  ] systemd-analyze does not object to a deliberately broken unit here, so"
    echo "       its silence about the installed one proves nothing: $(printf '%s' "$CONTROL_OUT" | head -2 | tr '\n' ' ')"
  elif printf '%s' "$UNIT_OUT" | grep -q "vibepanel.service:"; then
    fail "systemd-analyze objected to the substituted system unit: $(printf '%s' "$UNIT_OUT" | grep 'vibepanel.service:' | head -2 | tr '\n' ' ')"
  else
    ok "the substituted system unit verifies"
  fi
fi

# ── never both ────────────────────────────────────────────────────────────
echo "==> it refuses to create the second unit"
newhome
run --yes                       # a user install
run --yes --system              # ...and now ask for a system one
[ $RC -eq 3 ] && ok "refuses, with a distinct exit code" || fail "exited $RC, not 3"
[ -f "$(SYSU)" ] && fail "it installed the second unit anyway" || ok "no system unit was written"
[ -f "$(USRU)" ] && ok "the existing user unit is untouched" || fail "it removed the user unit"
has "$LOG" "there is already a user service installed" && ok "it says what is in the way" \
  || fail "the refusal does not name the existing install"
has "$LOG" "--migrate" && ok "it says how to proceed on purpose" \
  || fail "the refusal offers no way forward"

echo "==> --migrate replaces one with the other"
run --yes --system --migrate
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
[ -f "$(SYSU)" ] && ok "the system unit is installed" || fail "no system unit"
[ -f "$(USRU)" ] && fail "the user unit is still there; that is two panels on one socket" \
  || ok "the user unit was removed"
has "$WORK/systemctl.log" "--user disable --now vibepanel" \
  && ok "it stopped the user unit before removing the file" \
  || fail "the old unit file went away while the service was still running"

echo "==> and the other way round"
run --yes --user
[ $RC -eq 3 ] && ok "refuses to add a user unit next to a system one" || fail "exited $RC, not 3"
run --yes --user --migrate
[ -f "$(USRU)" ] && [ ! -f "$(SYSU)" ] && ok "migrating back removes the system unit" \
  || fail "after migrating back: user=$([ -f "$(USRU)" ] && echo yes || echo no) system=$([ -f "$(SYSU)" ] && echo yes || echo no)"

echo "==> an upgrade of a system install stays a system install"
# Nothing on the command line says which kind this is, and getting it wrong
# means silently installing the second unit -- the thing above refuses to do.
newhome
run --yes --system
run --yes
[ -f "$(SYSU)" ] && [ ! -f "$(USRU)" ] && ok "a bare re-run keeps the system unit" \
  || fail "a bare re-run switched unit kinds"

# ── no root ───────────────────────────────────────────────────────────────
echo "==> no root available: it says so and falls back"
newhome
ROOT_OVERRIDE=none
run --yes --system
[ $RC -eq 0 ] && ok "exits 0 rather than failing" || fail "exited $RC when root was unavailable"
[ -f "$(USRU)" ] && ok "the user unit was installed instead" || fail "nothing was installed"
[ -f "$(SYSU)" ] && fail "it wrote to /etc without root" || ok "no system unit"
has "$LOG" "root is not available here" && ok "it says plainly why" \
  || fail "it fell back silently: $(tail -5 "$LOG" | tr '\n' ' ')"

echo "==> no root, and a system unit in the way"
newhome
unset ROOT_OVERRIDE
run --yes --system            # put one there while root works
ROOT_OVERRIDE=none
run --yes --user --migrate    # ...then try to remove it without root
[ $RC -eq 3 ] && ok "refuses rather than getting halfway" || fail "exited $RC, not 3"
[ -f "$(SYSU)" ] && ok "the system unit is still there" || fail "it deleted a unit it could not have"
[ -f "$(USRU)" ] && fail "it installed a second unit anyway" || ok "no user unit was added"
unset ROOT_OVERRIDE

# ── interactive ───────────────────────────────────────────────────────────
#
# --interactive forces the prompts on without a terminal, which is the only way
# to drive them from a script. The terminal detection itself is checked
# separately below, under a real pty.
echo "==> interactive: the menu, the plan, and the confirmation"
newhome
printf '1\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
has "$LOG" "How should the panel run?" && ok "it offers the choice when root is available" \
  || fail "no menu: $(head -20 "$LOG" | tr '\n' ' ')"
has "$LOG" "about to:" && ok "it shows the plan before doing anything" \
  || fail "it acted without showing what it would do"
has "$LOG" "proceed?" && ok "it asks before acting" || fail "no confirmation"
[ -f "$(USRU)" ] && ok "answering 1 installs the user unit" || fail "answer 1 did not install the user unit"

echo "==> interactive: answering 2 installs the system unit"
newhome
printf '2\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
[ -f "$(SYSU)" ] && ok "answering 2 installs the system unit" || fail "answer 2 did not install it"
has "$LOG" "with User=" && ok "the plan says what it will substitute" \
  || fail "the plan does not mention the substitution"

echo "==> interactive: declining the plan changes nothing"
newhome
printf '1\nn\nn\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC after declining"
[ -f "$(USRU)" ] && fail "it installed the unit after being told not to" \
  || ok "no unit was installed"
[ -e "$HOME_DIR/.local/bin/vibepanel" ] && fail "it copied the binary anyway" \
  || ok "the binary was not copied either"
has "$LOG" "nothing was changed" && ok "it says nothing was changed" || fail "it said nothing"

echo "==> interactive: the conflict is a question, not just a refusal"
newhome
run --yes
printf 'y\nn\ny\n' > "$WORK/answers"   # yes remove it, no do not start, yes proceed
run --stdin "$WORK/answers" --interactive --system
[ -f "$(SYSU)" ] && [ ! -f "$(USRU)" ] && ok "agreeing migrates" \
  || fail "the interactive migration left user=$([ -f "$(USRU)" ] && echo yes || echo no) system=$([ -f "$(SYSU)" ] && echo yes || echo no)"

newhome
run --yes
printf 'n\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive --system
[ $RC -eq 3 ] && ok "declining leaves it alone, with the refusal's exit code" \
  || fail "exited $RC, not 3"
[ -f "$(SYSU)" ] && fail "it migrated after being told not to" || ok "no system unit"

# ── the terminal detection itself ─────────────────────────────────────────
#
# Every case above forces the mode with a flag, so none of them can tell you
# whether an actual person at an actual terminal gets asked anything. That
# needs a pty.
echo "==> a real terminal gets the questions with no flag at all"
if command -v script >/dev/null 2>&1; then
  newhome
  PTY_LOG="$WORK/pty.log"
  printf '1\nn\ny\n' | script -qec "cd '$REL' && HOME='$HOME_DIR' \
    VIBEPANEL_DESTDIR='$HOME_DIR/root' VIBEPANEL_SYSTEMCTL='$SCTL' \
    VIBEPANEL_ROOT_CMD='$ROOTCMD' ./deploy/install.sh" /dev/null > "$PTY_LOG" 2>&1
  has "$PTY_LOG" "proceed?" && ok "under a pty it asks, with no --interactive" \
    || fail "a terminal got no prompt: $(head -20 "$PTY_LOG" | tr -d '\r' | tr '\n' ' ')"
  [ -f "$(USRU)" ] && ok "and it installed what was chosen" || fail "the pty run installed nothing"
else
  echo "[--  ] no script(1) here; the terminal detection was not exercised"
fi

# ── the flags that were already documented ────────────────────────────────
echo "==> the old command line still works"
newhome
run --help
[ $RC -eq 0 ] && has "$LOG" "vibepanel installer" && ok "--help exits 0 and explains itself" \
  || fail "--help exited $RC"
run --nonsense
[ $RC -eq 2 ] && ok "an unknown option is still exit 2" || fail "--nonsense exited $RC, not 2"

echo
if [ "$FAILS" -eq 0 ]; then echo "=== install check: 0 FAIL ==="; else echo "=== install check: $FAILS FAIL ==="; fi
exit "$FAILS"
