#!/usr/bin/env bash
# Do install.sh and deploy/install.sh do what they say, on every path through
# them?
#
#   scripts/install-check.sh
#
# The installer is the one piece of this project that runs *before* anything
# else works, on a machine nobody has looked at, and it is shell -- so it has no
# type system, no test framework and no compiler to catch the branch nobody
# took. It now has seven decisions in it (interactive or not, tmux present /
# old / missing, which package manager, Linux or macOS, user or system unit,
# root or no root, an existing install of the other kind), and the interesting
# ones write to /etc, install packages and call `systemctl enable --now`.
#
# So: a throwaway HOME, a DESTDIR-style prefix instead of /etc, recorders
# instead of systemctl, launchctl and the package manager, a stub instead of
# tmux, and -- for the bootstrap that the one-liner pipes into sh -- a local
# HTTP server instead of GitHub. Nothing here runs sudo, nothing here installs
# a package, nothing here fetches anything from the internet, and nothing here
# reaches the systemd user manager -- which reads the *real* $HOME whatever this
# script sets, so `systemctl --user disable --now vibepanel` in the migration
# path would otherwise stop the panel of whoever is running the check.
#
# The binary it installs is a stub. The installer never executes it, and making
# this check depend on a Go build would make it slow enough to stop being run.
set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
FAILS=0
fail() { echo "[FAIL] $*"; FAILS=$((FAILS + 1)); }
ok() { echo "[ ok ] $*"; }
cleanup() {
  [ -n "${HTTPD:-}" ] && kill "$HTTPD" 2>/dev/null
  [ -n "${MIRRORD:-}" ] && kill "$MIRRORD" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

if ! command -v tmux >/dev/null 2>&1; then
  echo "install-check needs tmux: the installer refuses to run without it, which"
  echo "is itself one of the behaviours below."
  exit 1
fi

# ── the release layout the installer expects ──────────────────────────────
REL="$WORK/rel"
mkdir -p "$REL/deploy"
# The stub records its argv, because the installer now *runs* what it installs:
# once to prove the file executes on this machine at all, and once more when it
# was asked to create the first account. Both are commands worth asserting on.
#
# The version is written into the stub rather than read from a file beside it,
# so the copy the installer put in ~/.local/bin keeps saying what it said when
# it was copied -- which is the whole of "is this an upgrade or a reinstall".
stub_version() { # stub_version <version string>
  cat > "$REL/vibepanel" <<EOF
#!/bin/sh
echo "\$*" >> "$WORK/binary.log"
[ "\$1" = version ] && echo "vibepanel $1"
exit 0
EOF
  chmod +x "$REL/vibepanel"
}
stub_version v1.0.0
cp "$REPO/deploy/install.sh" "$REL/deploy/"
cp "$REPO/deploy/vibepanel.service" "$REPO/deploy/vibepanel.env" \
   "$REPO/deploy/vibepanel-system.service" \
   "$REPO/deploy/io.github.jiangmuran.vibepanel.plist" "$REL/deploy/"
chmod +x "$REL/deploy/install.sh"

MAC_LABEL=io.github.jiangmuran.vibepanel

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
  # A start makes the unit active, the way a real one does. The installer now
  # checks that it actually came up -- `enable --now` returning 0 only means
  # systemd took the job, and a panel whose port is taken fails a moment later
  # while the summary says "started". A stub that never becomes active would
  # make every case below report that failure.
  #
  # unit-refuses-to-start is how the failing side is driven.
  *enable\ --now*|*\ start*|*restart*)
    [ -e "$WORK/unit-refuses-to-start" ] || : > "$WORK/unit-is-active" ;;
esac
exit 0
EOF
chmod +x "$SCTL"

# launchctl, same shape. `print gui/<uid>/<label>` is how the installer asks
# whether the agent is loaded, and it is the difference between bootstrap and
# kickstart -k.
LCTL="$WORK/fake-launchctl"
cat > "$LCTL" <<EOF
#!/usr/bin/env bash
echo "\$*" >> "$WORK/launchctl.log"
case "\$1" in
  print) [ -e "$WORK/agent-is-loaded" ] && exit 0 || exit 1 ;;
esac
exit 0
EOF
chmod +x "$LCTL"

# Runs the command for real, so the unit really is written and really is
# substituted -- into $WORK, not into /etc.
ROOTCMD="$WORK/fake-root"
cat > "$ROOTCMD" <<EOF
#!/usr/bin/env bash
echo "root: \$*" >> "$WORK/root.log"
exec "\$@"
EOF
chmod +x "$ROOTCMD"

# The package manager, which must never actually run. `apt-get install -y tmux`
# on the machine running this check is precisely the class of accident every
# other double here exists to prevent -- and it needs root, so it would also be
# the one thing in this file that prompts for a password.
#
# It records the whole line *including* the root command, because whether the
# install was privileged is the entire difference between the brew branch and
# the other five. And it can be told to produce a tmux, so that "the package
# manager succeeded" and "there is now a new-enough tmux" are two separable
# facts -- they came apart in the real world, which is how a distribution
# shipping 3.2 behaves.
PKGREC="$WORK/fake-pkg"
cat > "$PKGREC" <<EOF
#!/usr/bin/env bash
echo "\$*" >> "$WORK/pkg.log"
if [ -e "$WORK/pkg-installs-tmux" ]; then
  mkdir -p "$WORK/pkgbin"
  printf '#!/bin/sh\necho "tmux %s"\n' "\$(cat "$WORK/pkg-tmux-version" 2>/dev/null || echo 3.6)" \
    > "$WORK/pkgbin/tmux"
  chmod +x "$WORK/pkgbin/tmux"
fi
exit 0
EOF
chmod +x "$PKGREC"

# What the installer looks at to decide whether systemd is the init here, and
# what it looks at to decide whether the *user* manager is reachable. Both are
# given fixed values rather than inherited: otherwise this check asserts
# different things on a developer's laptop, in a container and on a CI runner,
# and the branch that only appears in one of the three is the one nobody sees.
mkdir -p "$WORK/fake-init"
INIT_DEFAULT="$WORK/fake-init"
XDG_DEFAULT="/run/user/$(id -u)"

# A tmux that is too old, and a path where no tmux is.
mkdir -p "$WORK/oldbin" "$WORK/nobin"
printf '#!/bin/sh\necho "tmux 3.2"\n' > "$WORK/oldbin/tmux"
chmod +x "$WORK/oldbin/tmux"

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
  : > "$WORK/launchctl.log"
  : > "$WORK/root.log"
  : > "$WORK/pkg.log"
  rm -f "$WORK/unit-is-active" "$WORK/agent-is-loaded" \
        "$WORK/pkg-installs-tmux" "$WORK/pkg-tmux-version"
  rm -rf "$WORK/pkgbin"
  # Reset to "an ordinary Linux machine with root and a working tmux". Every
  # block that wants something else says so straight after calling this, and
  # nothing leaks from the block before -- which it did, once, and made an
  # assertion about the no-root path pass for a run that had root all along.
  ROOT_OVERRIDE="$ROOTCMD"
  # `--lang en` on every run unless a block clears it.
  #
  # The language question is asked whenever there is somebody to ask -- a
  # locale only picks which answer enter takes -- so without this every
  # interactive block below is one question longer than its answer list, and
  # the answers after it land on the wrong questions. A flag is the only thing
  # that skips it, which is what makes it usable here. The blocks that are
  # about the question itself set LANGFLAG= and drive it.
  LANGFLAG="--lang en"
  # No Claude Code, unless a block says otherwise.
  #
  # Default `none` rather than "whatever this machine has", because otherwise
  # the Claude Code question appears on a developer's box and not on a CI
  # runner. Every interactive block below feeds a fixed list of answers on
  # stdin, so one extra question consumes the answer meant for the next one and
  # the plan is accepted by a line that was meant to decline it -- which passed
  # on CI and failed here.
  CLAUDE_OVERRIDE=none
  PLAT_OVERRIDE=linux
  TMUX_OVERRIDE=tmux
  PKG_OVERRIDE=
  INIT_OVERRIDE="$INIT_DEFAULT"
  XDG_OVERRIDE="$XDG_DEFAULT"
  LC_OVERRIDE=en_US.UTF-8
  LCM_OVERRIDE=
  LANG_OVERRIDE=
  : > "$WORK/binary.log"
  stub_version v1.0.0
}
# run [--stdin <file>] -- <args...>   with the current throwaway HOME
#
# The three locale variables are set on every run, never inherited. The
# installer reads them to decide which language to speak, so a developer with
# LANG=zh_CN would otherwise run a check that asserts on English sentences the
# installer had stopped printing -- and see it fail for a reason that has
# nothing to do with what they changed. en_US.UTF-8 is the default because it
# is the answer every case below except the language block wants; that block
# sets LC_OVERRIDE itself, and empty means "the environment says nothing".
run() {
  local input=/dev/null lang=""
  if [ "${1:-}" = "--stdin" ]; then input="$2"; shift 2; fi
  # The language flag goes on only when the run can be asked.
  #
  # An unattended run never reaches the question, and the blocks that check
  # what LC_ALL / LC_MESSAGES / LANG do are all unattended -- adding --lang to
  # those would be the harness overruling the thing under test.
  case " $* " in *" --interactive "*) lang="$LANGFLAG" ;; esac
  ( cd "$REL" && HOME="$HOME_DIR" \
      LC_ALL="$LC_OVERRIDE" LC_MESSAGES="$LCM_OVERRIDE" LANG="$LANG_OVERRIDE" \
      VIBEPANEL_DESTDIR="$HOME_DIR/root" \
      VIBEPANEL_SYSTEMCTL="$SCTL" \
      VIBEPANEL_LAUNCHCTL="$LCTL" \
      VIBEPANEL_PLATFORM="$PLAT_OVERRIDE" \
      VIBEPANEL_TMUX_BIN="$TMUX_OVERRIDE" \
      VIBEPANEL_PKG_MANAGER="$PKG_OVERRIDE" \
      VIBEPANEL_PKG_RUNNER="$PKGREC" \
      VIBEPANEL_INIT_DIR="$INIT_OVERRIDE" \
      XDG_RUNTIME_DIR="$XDG_OVERRIDE" \
      VIBEPANEL_ROOT_CMD="$ROOT_OVERRIDE" \
      VIBEPANEL_CLAUDE_BIN="$CLAUDE_OVERRIDE" \
      ./deploy/install.sh $lang "$@" <"$input" >"$LOG" 2>&1 )
  RC=$?
}
has() { grep -qF -- "$2" "$1"; }
SYSU() { echo "$HOME_DIR/root/etc/systemd/system/vibepanel.service"; }
USRU() { echo "$HOME_DIR/.config/systemd/user/vibepanel.service"; }
PLST() { echo "$HOME_DIR/Library/LaunchAgents/$MAC_LABEL.plist"; }

# ── tmux, which is checked before anything else happens ───────────────────
#
# It is checked first because everything below it is pointless without tmux,
# and because installing a panel that cannot create a session is a worse
# outcome than refusing.
echo "==> tmux missing, unattended: it installs it, and says so"
newhome
PKG_OVERRIDE=apt-get
touch "$WORK/pkg-installs-tmux"
# The probe is pointed at pkgbin/, where the recorder writes its stub: nothing
# is there yet, and something is there afterwards. "The package manager ran"
# and "there is now a tmux" are two separate facts, and the next case is the
# one where they come apart.
TMUX_OVERRIDE="$WORK/pkgbin/tmux"
run --yes
[ $RC -eq 0 ] && ok "exits 0 once tmux is installed" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
has "$LOG" "tmux is not installed" && ok "it says tmux is missing, in one sentence" \
  || fail "it did not say what was wrong: $(head -5 "$LOG" | tr '\n' ' ')"
has "$WORK/pkg.log" "apt-get install -y tmux" && ok "it ran the package manager" \
  || fail "nothing was installed: $(cat "$WORK/pkg.log")"
has "$WORK/pkg.log" "fake-root apt-get" && ok "and did it as root" \
  || fail "apt-get was run unprivileged, which cannot work: $(cat "$WORK/pkg.log")"
has "$LOG" "tmux 3.6 installed" && ok "it says which version arrived" \
  || fail "it did not confirm the install: $(head -8 "$LOG" | tr '\n' ' ')"
[ -f "$(USRU)" ] || [ -f "$(SYSU)" ] && ok "and then carried on and installed the panel" \
  || fail "it stopped after installing tmux"

echo "==> the package manager succeeded and tmux still is not there"
# Which is not hypothetical: a package name that resolves to nothing, a
# repository that is not configured, an `apt-get install` that exits 0 having
# printed a warning. The installer must not claim a tmux it cannot find.
newhome
TMUX_OVERRIDE="$WORK/nobin/tmux"
PKG_OVERRIDE=apt-get     # and no pkg-installs-tmux marker
run --yes
[ $RC -eq 1 ] && ok "exits 1 rather than installing a panel that cannot run" \
  || fail "exited $RC after failing to install tmux"
has "$LOG" "still no tmux here" && ok "it says the install did not take" \
  || fail "it did not notice: $(tail -5 "$LOG" | tr '\n' ' ')"
[ -e "$HOME_DIR/.local/bin/vibepanel" ] && fail "it installed the binary anyway" \
  || ok "nothing was installed"

echo "==> tmux missing and root is not available: it says exactly what to run"
newhome
TMUX_OVERRIDE="$WORK/nobin/tmux"
PKG_OVERRIDE=apt-get
ROOT_OVERRIDE=none
touch "$WORK/pkg-installs-tmux"
run --yes
[ $RC -eq 1 ] && ok "exits 1" || fail "exited $RC"
[ -s "$WORK/pkg.log" ] && fail "it tried to install a package with no root" \
  || ok "it did not try to become root"
has "$LOG" "sudo apt-get install -y tmux" && ok "it prints the one command to type" \
  || fail "it did not say what to run: $(tail -6 "$LOG" | tr '\n' ' ')"

echo "==> no package manager it knows: it points at the tmux instructions"
newhome
TMUX_OVERRIDE="$WORK/nobin/tmux"
PKG_OVERRIDE=nothing-like-this
run --yes
[ $RC -eq 1 ] && ok "exits 1" || fail "exited $RC"
has "$LOG" "tmux/tmux/wiki/Installing" && ok "it links the install instructions" \
  || fail "it left the person with nowhere to go: $(tail -5 "$LOG" | tr '\n' ' ')"

echo "==> brew is never run as root"
# Homebrew refuses to run as root, and running it under sudo anyway leaves a
# prefix the user's own account cannot write to -- which breaks every later
# brew command, not just this one.
newhome
PLAT_OVERRIDE=darwin
TMUX_OVERRIDE="$WORK/pkgbin/tmux"
PKG_OVERRIDE=brew
touch "$WORK/pkg-installs-tmux"
run --yes
grep -q "^brew install tmux$" "$WORK/pkg.log" && ok "brew install tmux, with nothing in front of it" \
  || fail "brew was run through something: $(cat "$WORK/pkg.log")"

echo "==> each package manager gets its own command line"
for M in apt-get:"apt-get install -y tmux" \
         dnf:"dnf install -y tmux" \
         pacman:"pacman -S --noconfirm tmux" \
         zypper:"zypper --non-interactive install tmux" \
         apk:"apk add tmux"; do
  MGR="${M%%:*}"; WANT="${M#*:}"
  newhome
  TMUX_OVERRIDE="$WORK/pkgbin/tmux"
  PKG_OVERRIDE="$MGR"
  touch "$WORK/pkg-installs-tmux"
  run --yes
  has "$WORK/pkg.log" "$WANT" && ok "$MGR: $WANT" \
    || fail "$MGR produced: $(cat "$WORK/pkg.log")"
done

echo "==> tmux too old: it is a warning, not a refusal"
newhome
TMUX_OVERRIDE="$WORK/oldbin/tmux"
run --yes
[ $RC -eq 0 ] && ok "exits 0 -- an old tmux is a degradation, not a blocker" \
  || fail "exited $RC over a tmux that works"
has "$LOG" "older than 3.3" && ok "it says what is wrong" || fail "no version complaint"
has "$LOG" "allow-passthrough" && ok "it names the setting that stops applying" \
  || fail "it did not say what an old tmux costs"
[ -s "$WORK/pkg.log" ] && fail "it upgraded a package unattended, which the distro cannot satisfy" \
  || ok "it does not try to upgrade unattended"
has "$LOG" "tmux 3.2 is older than 3.3" && ok "and the closing summary repeats it" \
  || fail "the summary does not mention it, so it scrolls past: $(tail -4 "$LOG" | tr '\n' ' ')"
[ -f "$(SYSU)" ] || [ -f "$(USRU)" ] && ok "it installed the panel anyway" \
  || fail "nothing was installed"

echo "==> tmux too old, offered an upgrade, and the distro has the same version"
newhome
TMUX_OVERRIDE="$WORK/oldbin/tmux"
PKG_OVERRIDE=apt-get
touch "$WORK/pkg-installs-tmux"      # ...but at pkgbin, not where we are looking
printf 'y\n1\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
has "$LOG" "try to upgrade it now" && ok "it offers, rather than doing it" \
  || fail "no offer was made: $(head -10 "$LOG" | tr '\n' ' ')"
has "$WORK/pkg.log" "apt-get install -y tmux" && ok "accepting runs the package manager" \
  || fail "the answer was ignored"
has "$LOG" "is what this machine's packages offer" \
  && ok "and it says plainly that the upgrade changed nothing" \
  || fail "it claimed an upgrade that did not happen: $(head -14 "$LOG" | tr '\n' ' ')"

echo "==> --skip-tmux does not check at all"
newhome
TMUX_OVERRIDE="$WORK/nobin/tmux"
run --yes --skip-tmux
[ $RC -eq 0 ] && ok "exits 0 with no tmux anywhere" || fail "exited $RC"
has "$LOG" "tmux is not installed" && fail "it checked anyway" || ok "it said nothing about tmux"

# ── unattended: what CI and `curl | sh` get ───────────────────────────────
echo "==> unattended with no root (this is what release-check runs)"
newhome
ROOT_OVERRIDE=none
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
# The recommended default is the system service; picking the other one without
# a word reads as the installer disagreeing with the README, and the real
# reason is one the person may be able to do something about.
has "$LOG" "root is not available here" && ok "and why it is the user unit and not the system one" \
  || fail "it chose the user unit silently: $(head -12 "$LOG" | tr '\n' ' ')"
has "$LOG" "vibepanel service {status" && ok "it points at the management command" \
  || fail "it never mentions `vibepanel service`: $(tail -4 "$LOG" | tr '\n' ' ')"

echo "==> unattended with root: the system service is the recommended default"
# The change the owner asked for. It is the same panel as the same account with
# the same environment; what it adds is being up before anyone logs in without
# lingering, and being able to lower its OOM score at all.
newhome
run
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
[ -f "$(SYSU)" ] && ok "the system unit is what a bare run installs where root works" \
  || fail "it defaulted to the user unit even with root available"
[ -f "$(USRU)" ] && fail "it installed the user unit as well" || ok "and only that one"
has "$LOG" "installed: the systemd system service" && ok "the summary says so" \
  || fail "the summary does not name it: $(tail -3 "$LOG" | tr '\n' ' ')"

# `systemctl --user` answers for the logged-in user's manager whatever HOME this
# script sets, so on a first install "vibepanel is active" would be a different
# panel entirely -- and the next line would restart it.
newhome
ROOT_OVERRIDE=none
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
ROOT_OVERRIDE=none
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
ROOT_OVERRIDE=none
mkdir -p "$HOME_DIR/.config"
echo "VIBEPANEL_ADDR=:9999" > "$HOME_DIR/.config/vibepanel.env"
run --yes
has "$LOG" ":9999" && ok "the printed URL uses the configured port" \
  || fail "it printed a port the env file does not say: $(grep -i http "$LOG" | tr '\n' ' ')"

# ── --enable, and the upgrade that must restart ───────────────────────────
echo "==> --enable starts it, and says where the token is"
newhome
ROOT_OVERRIDE=none
run --yes --enable
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC: $(tail -3 "$LOG" | tr '\n' ' ')"
has "$WORK/systemctl.log" "--user enable --now vibepanel" \
  && ok "it enabled the user unit" || fail "no enable --now: $(cat "$WORK/systemctl.log")"
has "$LOG" "started just now" && ok "it says it started the service" || fail "no 'started' line"
has "$LOG" "vibepanel service token" && ok "it says how to get the setup token" \
  || fail "nothing about the setup token"
has "$LOG" "journalctl --user -u vibepanel" && ok "and what that command does underneath" \
  || fail "it only offers a command with no explanation of where the token lives"

echo "==> an upgrade over a running unit restarts it, without being asked"
# The failure this exists for is in docs/runbook.md: the new binary on disk and
# the old one still serving, with nothing wrong anywhere.
#
# Installed first, then marked active, because an upgrade is by definition the
# second run -- and because the installer only asks whether the unit is running
# once a unit of that kind exists in this HOME.
newhome
ROOT_OVERRIDE=none
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
  # /usr/local/bin, not the account's home.
  #
  # A system unit whose ExecStart is under one account's home breaks when that
  # home is not mounted at boot, and `sudo vibepanel` -- which is what the
  # summary tells people to type -- is `command not found`, because
  # ~/.local/bin is not on root's PATH.
  grep -qx "ExecStart=$HOME_DIR/root/usr/local/bin/vibepanel serve" "$U" \
    && ok "ExecStart points at the system binary this run installed" \
    || fail "ExecStart is wrong: $(grep '^ExecStart=' "$U")"
  grep -q "$HOME_DIR/.local/bin" "$U" \
    && fail "the system unit still refers to the account's home" \
    || ok "and nothing in the unit points into ~/.local/bin"
  [ -x "$HOME_DIR/root/usr/local/bin/vibepanel" ] \
    && ok "and the binary is there" || fail "no binary at the path the unit names"
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

# ── macOS ─────────────────────────────────────────────────────────────────
#
# Driven from Linux through VIBEPANEL_PLATFORM. The alternative is a Mac in the
# loop for every change to the installer, which in practice means the macOS
# half is checked by hand once and never again.
echo "==> macOS installs a LaunchAgent"
newhome
PLAT_OVERRIDE=darwin
run --yes --enable
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
P="$(PLST)"
if [ -f "$P" ]; then
  ok "the plist is installed in ~/Library/LaunchAgents"
  grep -q '__HOME__' "$P" && fail "__HOME__ was never substituted; launchd would run the wrong paths" \
    || ok "__HOME__ is substituted"
  grep -q "<string>$HOME_DIR/Library/Logs/vibepanel.log</string>" "$P" \
    && ok "it logs to ~/Library/Logs/vibepanel.log, since launchd has no journal" \
    || fail "no log path in the plist: $(grep -A1 StandardOutPath "$P" | tr '\n' ' ')"
  # The launchd half of KillMode=process, and the same red line: the tmux
  # server holds every session and must outlive the panel process.
  grep -q 'AbandonProcessGroup' "$P" && ok "AbandonProcessGroup is set" \
    || fail "without AbandonProcessGroup launchd kills the tmux server with the panel"
  grep -q 'SuccessfulExit' "$P" && ok "KeepAlive is conditional, so a stop actually stops it" \
    || fail "an unconditional KeepAlive makes the service impossible to stop"
else
  fail "no plist was written"
fi
[ -f "$(USRU)" ] && fail "it wrote a systemd unit on macOS" || ok "no systemd unit"
[ -f "$(SYSU)" ] && fail "it wrote to /etc on macOS" || ok "nothing under /etc"
has "$WORK/launchctl.log" "bootstrap gui/$(id -u) $P" && ok "it bootstrapped the agent" \
  || fail "the agent was never loaded: $(cat "$WORK/launchctl.log")"
has "$LOG" "installed: the launchd LaunchAgent" && ok "the summary names it" \
  || fail "the summary does not say what was installed: $(tail -4 "$LOG" | tr '\n' ' ')"
# The one real gap against the Linux user unit, and it has to be said at the
# moment of installing rather than discovered at the next logout.
has "$LOG" "stops when you log out" && ok "it says a LaunchAgent dies at logout" \
  || fail "it never mentions that macOS has no lingering"

echo "==> macOS restarts in place rather than unloading and hoping"
run --yes            # the plist now exists
: > "$WORK/launchctl.log"
touch "$WORK/agent-is-loaded"
run
has "$WORK/launchctl.log" "kickstart -k gui/$(id -u)/$MAC_LABEL" \
  && ok "a loaded agent is kickstarted, not booted out and back in" \
  || fail "it did not restart in place: $(cat "$WORK/launchctl.log")"
has "$WORK/launchctl.log" "bootout" && fail "it unloaded the agent, which is a window with no panel" \
  || ok "and never unloads it"
has "$LOG" "restarted (it was already running)" && ok "it says restarted, not started" \
  || fail "the summary claims a start that did not happen"
rm -f "$WORK/agent-is-loaded"

echo "==> --system on macOS is answered, not ignored"
newhome
PLAT_OVERRIDE=darwin
run --yes --system
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC"
has "$LOG" "macOS has no equivalent of the systemd system service" \
  && ok "it says why there is no system service here" \
  || fail "it silently installed something else: $(head -12 "$LOG" | tr '\n' ' ')"
has "$LOG" "oom_score_adj" && ok "and what specifically does not exist on macOS" \
  || fail "it does not say what the Linux system unit buys that macOS cannot"
[ -f "$(PLST)" ] && ok "the LaunchAgent was installed instead" || fail "nothing was installed"
[ -f "$(SYSU)" ] && fail "it wrote a systemd unit into /etc on a Mac" || ok "nothing under /etc"

# ── never both ────────────────────────────────────────────────────────────
echo "==> it refuses to create the second unit"
newhome
run --yes --user                # a user install, asked for by name
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

echo "==> an upgrade of a user install stays a user install"
# The mirror of the above, and the one that broke when the default changed:
# with root available, a bare re-run over a user install must not decide the
# recommended default applies to it.
newhome
run --yes --user
run --yes
[ -f "$(USRU)" ] && [ ! -f "$(SYSU)" ] && ok "a bare re-run keeps the user unit" \
  || fail "the new default overrode an existing user install"

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
run --yes --system            # put one there while root works
ROOT_OVERRIDE=none
run --yes --user --migrate    # ...then try to remove it without root
[ $RC -eq 3 ] && ok "refuses rather than getting halfway" || fail "exited $RC, not 3"
[ -f "$(SYSU)" ] && ok "the system unit is still there" || fail "it deleted a unit it could not have"
[ -f "$(USRU)" ] && fail "it installed a second unit anyway" || ok "no user unit was added"

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
has "$LOG" "1) systemd *system* service (recommended" \
  && ok "the system service is offered first, as the recommended default" \
  || fail "the recommended default is not first: $(grep -n ')' "$LOG" | head -4 | tr '\n' ' ')"
has "$LOG" "about to:" && ok "it shows the plan before doing anything" \
  || fail "it acted without showing what it would do"
has "$LOG" "proceed?" && ok "it asks before acting" || fail "no confirmation"
[ -f "$(SYSU)" ] && ok "answering 1 installs the system unit" || fail "answer 1 did not install the system unit"
has "$LOG" "with User=" && ok "the plan says what it will substitute" \
  || fail "the plan does not mention the substitution"

echo "==> interactive: answering 2 installs the user unit"
newhome
printf '2\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
[ -f "$(USRU)" ] && ok "answering 2 installs the user unit" || fail "answer 2 did not install it"
[ -f "$(SYSU)" ] && fail "it installed the system unit too" || ok "and only that one"

echo "==> interactive: no menu when there is no root to offer it with"
newhome
ROOT_OVERRIDE=none
printf 'n\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
has "$LOG" "How should the panel run?" \
  && fail "it offered a choice one of whose answers cannot be carried out" \
  || ok "with no root there is one answer, and it is not asked as a question"
[ -f "$(USRU)" ] && ok "the user unit was installed" || fail "nothing was installed"

# ── Claude Code's own settings ────────────────────────────────────────────
#
# The installer writes a file belonging to another tool, so the branch that
# matters most is the one where it does not.

echo "==> Claude Code: not asked about when there is no claude"
newhome
printf '1\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
has "$LOG" "Claude Code" \
  && fail "it brought up Claude Code on a machine that has none" \
  || ok "no claude, no question"

echo "==> Claude Code: never under --yes, even when it is installed"
newhome
CLAUDE_OVERRIDE="$WORK/fake-claude"
: > "$WORK/fake-claude"
run --yes --user
has "$LOG" "Claude Code" \
  && fail "it touched another tool's config in a run with nobody watching" \
  || ok "--yes does not edit ~/.claude"
[ -e "$HOME_DIR/.claude/settings.json" ] \
  && fail "it wrote settings.json under --yes" \
  || ok "and wrote nothing there"

echo "==> Claude Code: the list is asked for before the question"
newhome
CLAUDE_OVERRIDE="$WORK/fake-claude"
printf '1\nn\nn\ny\n' > "$WORK/answers"   # user service, do not start, no to claude, yes to the plan
run --stdin "$WORK/answers" --interactive
# What the keys are is internal/hooks' business and is pinned there. What this
# checks is that the script asks for them, and asks *before* the question --
# the stub in $REL is a shell script that logs its argv, so the list itself
# cannot come from here.
grep -q '^tune claude --lang en --asking$' "$WORK/binary.log" \
  && ok "the dry run is asked for" \
  || fail "it asked without asking the binary what it would write: $(cat "$WORK/binary.log")"
grep -q -- '--apply' "$WORK/binary.log" \
  && fail "it applied after being told not to: $(cat "$WORK/binary.log")" \
  || ok "declining applies nothing"

echo "==> Claude Code: accepted, and the language goes with it"
newhome
CLAUDE_OVERRIDE="$WORK/fake-claude"
# The flag, not the locale: the language question is asked at a terminal
# whatever the environment says, so the locale alone no longer settles what
# `tune` is handed.
LANGFLAG="--lang zh"
LC_OVERRIDE=zh_CN.UTF-8
printf '1\nn\ny\ny\n' > "$WORK/answers"   # user service, do not start, yes to claude, yes to the plan
run --stdin "$WORK/answers" --interactive
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC"
grep -q '^tune claude --apply --lang zh$' "$WORK/binary.log" \
  && ok "applied, and in the language the installer is speaking" \
  || fail "not applied as expected: $(cat "$WORK/binary.log")"

echo "==> Claude Code: nothing is asked when the list cannot be produced"
newhome
CLAUDE_OVERRIDE="$WORK/fake-claude"
# A binary that refuses `tune`, which is what an older one does.
cat > "$REL/vibepanel" <<EOF
#!/bin/sh
echo "\$*" >> "$WORK/binary.log"
[ "\$1" = version ] && echo "vibepanel v1.0.0"
[ "\$1" = tune ] && exit 2
exit 0
EOF
chmod +x "$REL/vibepanel"
printf '1\nn\ny\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
has "$LOG" "could not read them" && ok "it says it skipped them" \
  || fail "a binary that cannot tune said nothing about it"
grep -q -- '--apply' "$WORK/binary.log" \
  && fail "it applied anyway, having shown nothing" \
  || ok "and applied nothing"
stub_version v1.0.0

echo "==> Claude Code: --tune-claude works without a terminal, --no-tune-claude refuses"
newhome
CLAUDE_OVERRIDE="$WORK/fake-claude"
run --yes --user --tune-claude
grep -q -- '--apply' "$WORK/binary.log" \
  && ok "--tune-claude applies with nobody to ask" \
  || fail "--tune-claude did nothing: $(cat "$WORK/binary.log")"
newhome
CLAUDE_OVERRIDE="$WORK/fake-claude"
printf '1\nn\ny\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive --no-tune-claude
has "$LOG" "Claude Code" && fail "--no-tune-claude asked anyway" \
  || ok "--no-tune-claude does not bring it up"

echo "==> interactive: declining the plan changes nothing"
newhome
printf '1\nn\nn\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC after declining"
[ -f "$(SYSU)" ] && fail "it installed the unit after being told not to" \
  || ok "no unit was installed"
[ -e "$HOME_DIR/.local/bin/vibepanel" ] && fail "it copied the binary anyway" \
  || ok "the binary was not copied either"
has "$LOG" "nothing was changed" && ok "it says nothing was changed" || fail "it said nothing"

echo "==> interactive: the conflict is a question, not just a refusal"
newhome
run --yes --user
printf 'y\nn\ny\n' > "$WORK/answers"   # yes remove it, no do not start, yes proceed
run --stdin "$WORK/answers" --interactive --system
[ -f "$(SYSU)" ] && [ ! -f "$(USRU)" ] && ok "agreeing migrates" \
  || fail "the interactive migration left user=$([ -f "$(USRU)" ] && echo yes || echo no) system=$([ -f "$(SYSU)" ] && echo yes || echo no)"

newhome
run --yes --user
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
  # The first answer is the language, which a real terminal is now always
  # asked. This block builds its own command line and gets no LANGFLAG, which
  # is right: it is the one case that is about what a person at a terminal
  # actually meets.
  printf '1\n1\nn\ny\n' | script -qec "cd '$REL' && HOME='$HOME_DIR' \
    VIBEPANEL_DESTDIR='$HOME_DIR/root' VIBEPANEL_SYSTEMCTL='$SCTL' \
    VIBEPANEL_LAUNCHCTL='$LCTL' VIBEPANEL_PLATFORM=linux \
    VIBEPANEL_PKG_RUNNER='$PKGREC' VIBEPANEL_CLAUDE_BIN=none \
    VIBEPANEL_ROOT_CMD='$ROOTCMD' ./deploy/install.sh" /dev/null > "$PTY_LOG" 2>&1
  has "$PTY_LOG" "Which language" \
    && ok "a terminal is asked the language even with a locale set" \
    || fail "no language question at a terminal"
  has "$PTY_LOG" "proceed?" && ok "under a pty it asks, with no --interactive" \
    || fail "a terminal got no prompt: $(head -20 "$PTY_LOG" | tr -d '\r' | tr '\n' ' ')"
  [ -f "$(SYSU)" ] && ok "and it installed what was chosen" || fail "the pty run installed nothing"
else
  echo "[--  ] no script(1) here; the terminal detection was not exercised"
fi

# ── the flags that were already documented ────────────────────────────────
echo "==> vibepanel service takes its flags in either order"
# `vibepanel service --dry-run status` and `vibepanel service logs -f` are both
# things people type, and Go's flag package stops at the first non-flag -- so
# one of the two silently printed the usage text instead of doing anything.
if [ -x "$REPO/vibepanel" ]; then
  newhome
  ROOT_OVERRIDE=none
  run --yes
  # DESTDIR too, not just HOME. Without it this reads the *developer's* real
  # /etc/systemd/system/vibepanel.service and reports whatever that machine
  # has -- which it did the moment the relative-path bug in systemUnitPath was
  # fixed, because until then no system unit was ever found and the fallback
  # to the user unit made this look isolated.
  A="$(HOME="$HOME_DIR" VIBEPANEL_DESTDIR="$HOME_DIR/root" "$REPO/vibepanel" service --dry-run status 2>&1)"
  B="$(HOME="$HOME_DIR" VIBEPANEL_DESTDIR="$HOME_DIR/root" "$REPO/vibepanel" service status --dry-run 2>&1)"
  [ "$A" = "$B" ] && [ "$A" = "systemctl --user status vibepanel" ] \
    && ok "both orders resolve to: $A" \
    || fail "the two orders disagree: [$A] vs [$B]"
else
  echo "[--  ] no built binary at $REPO/vibepanel; the flag order was not exercised"
fi

echo "==> the old command line still works"
newhome
run --help
[ $RC -eq 0 ] && has "$LOG" "vibepanel installer" && ok "--help exits 0 and explains itself" \
  || fail "--help exited $RC"
has "$LOG" "vibepanel service" && ok "--help says what to use afterwards" \
  || fail "--help does not mention the management command"
run --nonsense
[ $RC -eq 2 ] && ok "an unknown option is still exit 2" || fail "--nonsense exited $RC, not 2"

# ── the first account, created from the installer ─────────────────────────
#
# The panel's other door: a one-time setup token printed at startup and typed
# into the browser. That one is unchanged. This is for the person installing
# over ssh who would rather not go and read a journal.
echo "==> --username with a password file creates the first account"
newhome
ROOT_OVERRIDE=none
PWFILE="$WORK/pw$CASE"
printf 'a sufficiently long password\n' > "$PWFILE"
run --yes --username admin --password-file "$PWFILE"
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
has "$WORK/binary.log" "account create --username admin --password-file $PWFILE" \
  && ok "it asked the binary to create the account, with the file it was given" \
  || fail "the account command was wrong: $(cat "$WORK/binary.log")"
has "$LOG" "created just now -- there is no setup token" \
  && ok "and says the setup token will not exist" \
  || fail "it did not say what that changes: $(tail -6 "$LOG" | tr '\n' ' ')"
# The token instructions must be *gone*, not merely accompanied. Somebody sent
# to look for a token that was never printed concludes the install failed.
has "$LOG" "the one-time setup token" \
  && fail "it still tells them to find a setup token that will never be printed" \
  || ok "it does not offer a token that no longer exists"

echo "==> a password on the command line is refused, with the reason"
newhome
ROOT_OVERRIDE=none
run --yes --username admin --password hunter2hunter2
[ $RC -eq 2 ] && ok "exits 2" || fail "exited $RC, not 2"
has "$LOG" "shell history" && ok "it says why there is no such flag" \
  || fail "the refusal does not explain itself: $(cat "$LOG")"
[ -e "$HOME_DIR/.local/bin/vibepanel" ] && fail "it installed anyway" || ok "nothing was installed"

echo "==> two password sources, and a password with no account, are refused"
newhome
ROOT_OVERRIDE=none
printf 'a sufficiently long password\n' > "$WORK/pw$CASE"
run --yes --username admin --password-file "$WORK/pw$CASE" --password-env NOPE
[ $RC -eq 2 ] && ok "two sources: exits 2" || fail "exited $RC, not 2"
has "$LOG" "choose one of" && ok "and says to pick one" || fail "no explanation"
run --yes --password-file "$WORK/pw$CASE"
[ $RC -eq 2 ] && ok "a password with no --username: exits 2" || fail "exited $RC, not 2"
has "$LOG" "no account to" && ok "and says there is nothing to create" || fail "no explanation"

echo "==> --password-stdin and the prompts cannot both have stdin"
newhome
ROOT_OVERRIDE=none
run --interactive --username admin --password-stdin
[ $RC -eq 2 ] && ok "exits 2 rather than reading the answers as a password" \
  || fail "exited $RC, not 2"
has "$LOG" "needs stdin to itself" && ok "it says which two things collide" \
  || fail "no explanation: $(cat "$LOG")"

# ── the machine, and the ways it is not the one this was written on ───────
echo "==> a binary that will not run here is caught immediately"
# noexec, SELinux and the wrong architecture are indistinguishable afterwards
# and identical from here: the file installs, and the service restarts every
# three seconds with a message in a journal nobody has been told to read yet.
newhome
ROOT_OVERRIDE=none
printf '#!/nonexistent/interpreter\nexit 0\n' > "$REL/vibepanel"
chmod +x "$REL/vibepanel"
run --yes
stub_version v1.0.0
[ $RC -eq 1 ] && ok "exits 1" || fail "exited $RC; it installed a binary that cannot run"
has "$LOG" "will not run on this machine" && ok "it says so plainly" \
  || fail "no diagnosis: $(tail -4 "$LOG" | tr '\n' ' ')"
has "$LOG" "noexec" && has "$LOG" "SELinux" && ok "and names the three things that do this" \
  || fail "it does not say what to look at"
[ -f "$(USRU)" ] && fail "it installed a unit for a binary that cannot start" \
  || ok "no service was installed"

echo "==> no systemd: the binary only, and it says so"
newhome
ROOT_OVERRIDE=none
INIT_OVERRIDE="$WORK/there-is-no-systemd-here"
run --yes
[ $RC -eq 0 ] && ok "exits 0 -- the panel runs fine from a shell" || fail "exited $RC"
[ -x "$HOME_DIR/.local/bin/vibepanel" ] && ok "the binary is installed" || fail "no binary"
[ -f "$(USRU)" ] && fail "it wrote a unit no manager will ever read" || ok "no unit was written"
has "$LOG" "no service manager here" && ok "it says there is no service manager" \
  || fail "it installed silently: $(tail -6 "$LOG" | tr '\n' ' ')"
has "$LOG" "vibepanel serve" && ok "and how to start it by hand" \
  || fail "it left them with nothing to run"

echo "==> no session bus: the unit is installed and the limitation is said"
newhome
ROOT_OVERRIDE=none
XDG_OVERRIDE=
run --yes
[ -f "$(USRU)" ] && ok "the unit is still installed" || fail "nothing was installed"
has "$LOG" "XDG_RUNTIME_DIR is not set" && ok "it says why systemctl --user cannot work here" \
  || fail "the user manager was unreachable and nothing said so: $(tail -6 "$LOG" | tr '\n' ' ')"

echo "==> an unwritable HOME is one line, before anything changes"
newhome
ROOT_OVERRIDE=none
chmod 555 "$HOME_DIR"
run --yes
RC_SAVED=$RC
chmod 755 "$HOME_DIR"
[ $RC_SAVED -eq 1 ] && ok "exits 1" || fail "exited $RC_SAVED"
has "$LOG" "is not writable" && ok "it names the problem" \
  || fail "it failed somewhere in the middle instead: $(tail -3 "$LOG" | tr '\n' ' ')"

echo "==> a unit file it did not write is not overwritten"
newhome
ROOT_OVERRIDE=none
mkdir -p "$(dirname "$(USRU)")"
printf '[Unit]\nDescription=somebody else vibepanel\n[Service]\nExecStart=/usr/bin/true\n' > "$(USRU)"
run --yes
[ $RC -eq 3 ] && ok "refuses, with the refusal exit code" || fail "exited $RC, not 3"
grep -q "somebody else" "$(USRU)" && ok "the file is untouched" \
  || fail "it overwrote a unit it did not write"
has "$LOG" "was not written by this installer" && ok "it says why it stopped" \
  || fail "no explanation: $(tail -5 "$LOG" | tr '\n' ' ')"
has "$LOG" ".bak" && ok "and what to do about it" || fail "no way forward"
printf 'y\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive --user
grep -q "somebody else" "$(USRU)" && fail "agreeing did not replace it" \
  || ok "agreeing replaces it"

echo "==> it says whether this is an install, a reinstall or an upgrade"
newhome
ROOT_OVERRIDE=none
run --yes
has "$LOG" "install  $HOME_DIR/.local/bin/vibepanel   (vibepanel v1.0.0)" \
  && ok "a first install names the version going on" \
  || fail "the plan does not say what is being installed: $(grep -A2 'about to' "$LOG" | tr '\n' ' ')"
run --yes
has "$LOG" "the same build" && ok "re-running the one-liner says it is the same build" \
  || fail "a reinstall of the same version is not distinguished: $(grep -A2 'about to' "$LOG" | tr '\n' ' ')"
stub_version v2.0.0
run --yes
has "$LOG" "vibepanel v1.0.0 -> vibepanel v2.0.0" && ok "and an upgrade says both versions" \
  || fail "an upgrade does not say what it replaces: $(grep -A2 'about to' "$LOG" | tr '\n' ' ')"

echo "==> a port that is already taken is a warning, not a surprise later"
if command -v python3 >/dev/null 2>&1; then
  newhome
  ROOT_OVERRIDE=none
  BUSY=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
  python3 -c "
import socket,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(('127.0.0.1',$BUSY)); s.listen(1); time.sleep(20)
" &
  BUSYPID=$!
  sleep 0.5
  mkdir -p "$HOME_DIR/.config"
  echo "VIBEPANEL_ADDR=127.0.0.1:$BUSY" > "$HOME_DIR/.config/vibepanel.env"
  run --yes
  kill "$BUSYPID" 2>/dev/null
  has "$LOG" "already listening on port $BUSY" \
    && ok "it says the port is taken before installing over it" \
    || fail "nothing said about a port that will not bind: $(grep -i port "$LOG" | tr '\n' ' ')"
  has "$LOG" "three-second loop" && ok "and what that would look like" \
    || fail "it does not say what happens next"
else
  echo "[--  ] no python3 here; the busy-port warning was not exercised"
fi

echo "==> a PATH without ~/.local/bin gets the exact line to add"
newhome
ROOT_OVERRIDE=none
run --yes
# $HOME_DIR/.local/bin is a temp directory, so it is never on the real PATH:
# this case is the default one, and the *absence* of the note would be the bug.
has "$LOG" "is not on your PATH" && ok "it says the binary will not be typeable" \
  || fail "it installed something the person cannot run: $(tail -8 "$LOG" | tr '\n' ' ')"
has "$LOG" "export PATH=" && ok "and gives the line to paste" \
  || fail "the advice is not something that can be copied"

# ── the language, which is decided before anything else is asked ──────────
#
# Three ways in, in this order: --lang, then LC_ALL / LC_MESSAGES / LANG, then
# the question. The one that matters most is the one that must *not* happen: an
# unattended run has nobody to answer, and a question there does not fail, it
# hangs -- a pipeline stopped forever on a prompt written to a log file.

echo "==> every string exists in both languages"
#
# The whole reason the two languages can be kept honest. A key whose Chinese
# side was never written does not fail: it prints an empty line in the middle
# of somebody's install, under a question they have just been asked. So every
# key in both tables is called in both languages and the marker `m` prints
# instead of an empty line is what this looks for.
strings_of() { # strings_of <script> <label> -> a sourceable file of the table + m()
  local f="$1" out="$WORK/strings-$2.sh"
  # From the table's opening marker down to the end of m() itself, which is
  # everything the walk needs and nothing that would run on being sourced.
  sed -n '/strings: begin/,/^m() {/p' "$f" > "$out"
  sed -n '/^m() {/,/^}$/p' "$f" | tail -n +2 >> "$out"
  printf '%s' "$out"
}
check_strings() { # check_strings <script> <label>
  local f="$1" label="$2" blk keys count driver out
  blk="$(strings_of "$f" "$label")"
  # The case arms are the key list. Four spaces and a lower-case letter is the
  # shape of every one of them; `*)` is the fallthrough and is not a key.
  keys="$(grep -oE '^    [a-z][a-z0-9._]*\)' "$blk" | tr -d ' )' | sort -u)"
  count="$(printf '%s\n' "$keys" | grep -c . || true)"
  # A sed that stopped matching would produce an empty list and a check that
  # passes by testing nothing, which is the failure mode of every walk like
  # this one.
  if [ "$count" -lt 20 ]; then
    fail "$label: found only $count keys, so the table was not read at all"
    return
  fi
  driver="$WORK/strings-$label-driver.sh"
  cat "$blk" > "$driver"
  cat >> "$driver" <<'EOS'
bad=0
for k in $KEYS; do
  # The two sides must also agree on their substitutions. A translation that
  # dropped a %2$s leaves the path or the version out of one language only,
  # and nothing else here would notice.
  mstr "$k"
  ten="$(printf '%s' "$MS_EN" | grep -o '%[0-9]\$s' | sort -u | tr '\n' ' ')"
  tzh="$(printf '%s' "$MS_ZH" | grep -o '%[0-9]\$s' | sort -u | tr '\n' ' ')"
  if [ "$ten" != "$tzh" ]; then echo "$k: en has [$ten], zh has [$tzh]"; bad=1; fi
  for L in en zh; do
    VP_LANG="$L"
    out="$(m "$k" one two three 2>&1)"
    case "$out" in
      ''|*'missing string'*) echo "$k/$L is missing"; bad=1 ;;
    esac
  done
done
VP_LANG=en
# Both halves: the marker where the sentence should have been, and a line that
# says which key. The two failures are different jobs -- a key nothing defines
# is a typo at a call site, an empty side is a translation nobody wrote -- and
# a message that cannot tell them apart sends the reader to the wrong file.
out="$(m no.such.key.at.all 2>&1)"
case "$out" in
  *'missing string'*) ;;
  *) echo "an unknown key printed [$out] instead of a marker"; bad=1 ;;
esac
case "$out" in
  *'no string for key'*) ;;
  *) echo "an unknown key was not reported as unknown: [$out]"; bad=1 ;;
esac
# The other half of the same guard, and the shape drift actually takes: the key
# is there, one language is not. Every key above has both sides today, so the
# only way to pin the behaviour is to hand `m` a pair with one side missing.
mstr() { MS_EN='the english one'; MS_ZH=''; return 0; }
VP_LANG=zh
out="$(m any.key.at.all 2>&1)"
case "$out" in
  *'missing string'*) ;;
  *) echo "a half-translated key printed [$out] instead of saying so"; bad=1 ;;
esac
exit $bad
EOS
  if out="$(KEYS="$keys" bash "$driver" 2>&1)"; then
    ok "$label: $count keys, both languages, matching substitutions"
  else
    fail "$label: $(printf '%s' "$out" | head -6 | tr '\n' '; ')"
  fi
}
check_strings "$REPO/deploy/install.sh" deploy
check_strings "$REPO/install.sh" bootstrap

echo "==> unattended never asks which language, whatever the environment says"
# The one that hangs rather than fails. `--yes` with nothing in the environment
# is a pipeline, and a question there is a pipeline stopped forever on a prompt
# nobody can see being asked.
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=
run --yes
[ $RC -eq 0 ] && ok "exits 0 with no locale set at all" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
has "$LOG" "Which language" && fail "it asked a question no pipeline can answer" \
  || ok "it does not ask"
has "$LOG" "about to:" && ok "and falls back to English rather than guessing" \
  || fail "an undecided language did not produce English: $(head -6 "$LOG" | tr '\n' ' ')"

echo "==> interactive with nothing in the environment: it asks, and asks first"
newhome
LANGFLAG=
LC_OVERRIDE=
printf '2\n1\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
has "$LOG" "Which language should this installer speak?" && ok "it asks" \
  || fail "no language question: $(head -6 "$LOG" | tr '\n' ' ')"
has "$LOG" "简体中文" && ok "both languages are named in their own language" \
  || fail "the choice does not name itself in Chinese"
# First, and not merely present. A language chosen after three questions have
# been answered in English is a language chosen too late.
LANGLINE="$(grep -n "Which language" "$LOG" | head -1 | cut -d: -f1)"
MENULINE="$(grep -n "vibepanel" "$LOG" | head -1 | cut -d: -f1)"
[ -n "$LANGLINE" ] && [ "$LANGLINE" -lt "${MENULINE:-999}" ] \
  && ok "it is the first thing on the screen, before the banner" \
  || fail "something was printed before the language question (line $LANGLINE vs $MENULINE)"
has "$LOG" "面板要以哪种方式运行？" && ok "answering 2 puts the service menu in Chinese" \
  || fail "the menu after the answer is not Chinese: $(sed -n '6,12p' "$LOG" | tr '\n' ' ')"
has "$LOG" "就这样做吗？" && ok "and so is the confirmation under the plan" \
  || fail "the plan was confirmed in another language than the question"
[ -f "$(SYSU)" ] && ok "and the answers after it still land where they did" \
  || fail "choosing a language shifted every later answer by one"

echo "==> a locale is a default, not an answer"
# LANG=en_US.UTF-8 is what a server image ships with. Treating it as "they have
# already told us" meant the installer never offered Chinese on exactly the
# machines where offering matters -- which is how this was reported.
newhome
LANGFLAG=
LC_OVERRIDE=en_US.UTF-8
printf '2\n1\nn\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
has "$LOG" "Which language" \
  && ok "an English locale is still asked" || fail "it took the locale as the answer"
has "$LOG" "选择 [1]" \
  && ok "and the English locale is what enter would take" || fail "the default does not follow the locale"
has "$LOG" "接下来会：" \
  && ok "answering 2 overrides the locale" || fail "the answer did not beat the environment"

newhome
LANGFLAG=
LC_OVERRIDE=zh_CN.UTF-8
printf '\n1\nn\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive
has "$LOG" "选择 [2]" \
  && ok "a Chinese locale makes 2 the default" || fail "the zh locale did not become the default"
has "$LOG" "接下来会：" \
  && ok "and pressing enter keeps it" || fail "enter did not take the locale's language"

echo "==> --lang still skips the question entirely"
newhome
LANGFLAG=
LC_OVERRIDE=
printf '1\nn\nn\ny\n' > "$WORK/answers"
run --stdin "$WORK/answers" --interactive --lang en
has "$LOG" "Which language" \
  && fail "--lang was given and it asked anyway" \
  || ok "a flag is somebody saying it about this run"

echo "==> a zh locale installs in Chinese and asks nothing"
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=zh_CN.UTF-8
run --yes
[ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
has "$LOG" "Which language" && fail "it asked about a language the environment had already named" \
  || ok "it does not ask what LC_ALL already said"
has "$LOG" "接下来会：" && ok "the plan is in Chinese" \
  || fail "LC_ALL=zh_CN did not produce Chinese: $(head -8 "$LOG" | tr '\n' ' ')"
has "$LOG" "── 完成 ──" && ok "and so is the summary at the end" \
  || fail "the run ends in English: $(tail -8 "$LOG" | tr '\n' ' ')"

echo "==> --lang beats the environment, in both directions"
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=en_US.UTF-8
run --yes --lang zh
has "$LOG" "接下来会：" && ok "--lang zh under an English locale is Chinese" \
  || fail "the flag lost to the environment: $(head -6 "$LOG" | tr '\n' ' ')"
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=zh_CN.UTF-8
run --yes --lang en
has "$LOG" "about to:" && ok "--lang en under a Chinese locale is English" \
  || fail "the flag lost to the environment: $(head -6 "$LOG" | tr '\n' ' ')"
run --yes --lang=zh
has "$LOG" "接下来会：" && ok "and --lang=zh is the same flag" || fail "--lang=zh was not read"

echo "==> LC_ALL, then LC_MESSAGES, then LANG -- the first one that is set"
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=
LCM_OVERRIDE=zh_CN.UTF-8
run --yes
has "$LOG" "接下来会：" && ok "LC_MESSAGES decides when LC_ALL is unset" \
  || fail "LC_MESSAGES was not consulted"
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=
LANG_OVERRIDE=zh_CN.UTF-8
run --yes
has "$LOG" "接下来会：" && ok "LANG decides when neither of the others is set" \
  || fail "LANG was not consulted"
# The one that is easy to get backwards: LC_ALL is the override, so a C library
# would never look at LANG once it is set. Neither may this.
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=en_US.UTF-8
LANG_OVERRIDE=zh_CN.UTF-8
run --yes
has "$LOG" "about to:" && ok "LC_ALL wins over LANG, as the C library has it" \
  || fail "LANG overruled LC_ALL"
# A locale that names neither language is not a guess to be made. Unattended,
# that is English.
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=de_DE.UTF-8
run --yes
has "$LOG" "about to:" && ok "a locale it does not know is English, not a guess" \
  || fail "an unknown locale produced something else"
# And an LC_ALL it does not recognise still ends the question. Falling through
# to LANG from there looks like helpfulness and is the installer overruling the
# variable the C library gives priority to: LC_ALL=C beside LANG=zh_CN is
# somebody asking for C.
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=C
LANG_OVERRIDE=zh_CN.UTF-8
run --yes
has "$LOG" "about to:" && ok "an LC_ALL it does not know is not handed down to LANG" \
  || fail "LANG was consulted behind an LC_ALL that was set"

echo "==> --lang with a language it does not have is refused"
newhome
ROOT_OVERRIDE=none
run --yes --lang de
[ $RC -eq 2 ] && ok "exits 2" || fail "exited $RC, not 2"
has "$LOG" "--lang needs en or zh" && ok "and says what it takes" \
  || fail "no explanation: $(cat "$LOG")"
run --yes --lang
[ $RC -eq 2 ] && ok "a bare --lang is refused too" || fail "exited $RC, not 2"

echo "==> --help is in the chosen language"
newhome
run --lang zh --help
[ $RC -eq 0 ] && has "$LOG" "vibepanel 安装程序" && ok "--lang zh --help is Chinese" \
  || fail "--help ignored the language: $(head -3 "$LOG" | tr '\n' ' ')"
has "$LOG" "--lang <en|zh>" && ok "and documents the flag itself" \
  || fail "--help does not mention --lang"
run --help --lang zh
has "$LOG" "vibepanel 安装程序" && ok "and the flag is read whichever side of --help it is on" \
  || fail "--help --lang zh printed the other language"

echo "==> the language question never reads a password"
# --password-stdin and the prompts cannot both have stdin; that is refused, but
# the refusal is two hundred lines further down and this question would get
# there first -- reading the first line of the password and, because stdin is
# not a terminal, echoing it into the log.
newhome
ROOT_OVERRIDE=none
LC_OVERRIDE=
printf 'correct horse battery staple\n' > "$WORK/pw-lang"
run --stdin "$WORK/pw-lang" --interactive --username admin --password-stdin
[ $RC -eq 2 ] && ok "exits 2, as it did before" || fail "exited $RC, not 2"
has "$LOG" "correct horse battery staple" \
  && fail "the language question ate the password and echoed it into the log" \
  || ok "the password was not read, and not echoed"
has "$LOG" "Which language" && fail "it asked anyway" || ok "and the question was not asked"

# ── the bootstrap: install.sh at the repository root ──────────────────────
#
# The one-liner, driven against a local HTTP server holding archives built a
# second ago. Never against GitHub: a check that fetches and runs code from the
# internet is a check that installs whatever the internet is serving today, and
# the checksum path in particular has to be exercised with a *tampered*
# archive, which is not something a release host will serve on request.
echo
echo "==> the one-liner (install.sh at the repository root)"
BOOT="$REPO/install.sh"
[ -x "$BOOT" ] && ok "it exists and is executable" || fail "no executable install.sh at the repo root"
if command -v dash >/dev/null 2>&1; then
  dash -n "$BOOT" && ok "it parses under dash, which is /bin/sh on Debian" \
    || fail "install.sh is not POSIX sh; the one-liner is piped into /bin/sh"
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "[--  ] no python3 here; the download and checksum paths were not exercised"
else
  SERVE="$WORK/serve"; mkdir -p "$SERVE"
  # Two archives, so that platform detection is asked a question with two
  # possible answers rather than one.
  for TGT in linux_amd64 darwin_arm64; do
    STAGE="$WORK/stage_$TGT"; rm -rf "$STAGE"; mkdir -p "$STAGE"
    cp -r "$REL" "$STAGE/vibepanel_v9.9.9_$TGT"
    tar -czf "$SERVE/vibepanel_v9.9.9_$TGT.tar.gz" -C "$STAGE" "vibepanel_v9.9.9_$TGT"
  done
  ( cd "$SERVE" && sha256sum ./*.tar.gz > SHA256SUMS )
  printf '{"tag_name": "v9.9.9", "name": "nine"}\n' > "$SERVE/latest.json"
  # A tampered archive with a checksum line that no longer describes it: one
  # byte appended after the sums were taken, which is what a corrupted mirror
  # or a rewriting proxy produces.
  cp "$SERVE/vibepanel_v9.9.9_linux_amd64.tar.gz" "$SERVE/vibepanel_v9.9.8_linux_amd64.tar.gz"
  sed 's/v9\.9\.9_linux_amd64/v9.9.8_linux_amd64/' "$SERVE/SHA256SUMS" \
    | grep 9.9.8 >> "$SERVE/SHA256SUMS"
  printf 'x' >> "$SERVE/vibepanel_v9.9.8_linux_amd64.tar.gz"
  # And one with no checksum line at all.
  cp "$SERVE/vibepanel_v9.9.9_linux_amd64.tar.gz" "$SERVE/vibepanel_v9.9.7_linux_amd64.tar.gz"

  PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
  ( cd "$SERVE" && python3 -m http.server "$PORT" --bind 127.0.0.1 >/dev/null 2>&1 ) &
  HTTPD=$!
  BASE="http://127.0.0.1:$PORT"
  for _ in $(seq 40); do
    curl -sf "$BASE/SHA256SUMS" >/dev/null 2>&1 && break
    sleep 0.1
  done

  # BOOTENV holds the per-case overrides; bootrun takes the arguments. Two
  # lists rather than one with a separator, because `env` has no way to be told
  # where the assignments stop.
  BOOTENV=()
  bootrun() {
    CASE=$((CASE + 1))
    HOME_DIR="$WORK/home$CASE"; mkdir -p "$HOME_DIR"
    LOG="$WORK/log$CASE"
    env HOME="$HOME_DIR" \
      LC_ALL=en_US.UTF-8 LC_MESSAGES= LANG= \
      VIBEPANEL_BASE_URL="$BASE" \
      VIBEPANEL_DESTDIR="$HOME_DIR/root" \
      VIBEPANEL_SYSTEMCTL="$SCTL" VIBEPANEL_LAUNCHCTL="$LCTL" \
      VIBEPANEL_PKG_RUNNER="$PKGREC" VIBEPANEL_ROOT_CMD=none \
      "${BOOTENV[@]}" sh "$BOOT" "$@" >"$LOG" 2>&1
    RC=$?
    BOOTENV=()
  }

  echo "==> it downloads, verifies and installs"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9)
  bootrun --yes --no-enable
  [ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
  has "$LOG" "sha256 verified" && ok "it says it verified the archive" \
    || fail "nothing about the checksum: $(head -5 "$LOG" | tr '\n' ' ')"
  [ -x "$HOME_DIR/.local/bin/vibepanel" ] && ok "the installer inside the archive ran" \
    || fail "the archive was fetched and nothing was installed"
  has "$LOG" "installed: the systemd user service" && ok "and finished the whole way through" \
    || fail "the inner installer did not reach its summary: $(tail -3 "$LOG" | tr '\n' ' ')"

  echo "==> a tampered archive is refused, and deleted"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.8)
  bootrun --yes --no-enable
  [ $RC -ne 0 ] && ok "exits non-zero" || fail "it installed an archive whose checksum did not match"
  has "$LOG" "checksum mismatch" && ok "it says the checksum did not match" \
    || fail "it did not name the problem: $(tail -3 "$LOG" | tr '\n' ' ')"
  [ -e "$HOME_DIR/.local/bin/vibepanel" ] && fail "it installed it anyway" \
    || ok "nothing was installed"
  has "$LOG" "expected" && has "$LOG" "got" && ok "and prints both sums" \
    || fail "the mismatch message says which two things disagreed?"

  echo "==> an archive SHA256SUMS does not mention is refused too"
  # A different failure from a mismatch, and worth its own words: it means the
  # checksum file belongs to another release, not that the download broke.
  BOOTENV=(VIBEPANEL_VERSION=v9.9.7)
  bootrun --yes --no-enable
  [ $RC -ne 0 ] && ok "exits non-zero" || fail "it unpacked an archive nobody had vouched for"
  has "$LOG" "does not mention" && ok "it says the checksum file has no line for it" \
    || fail "it reported the wrong thing: $(tail -3 "$LOG" | tr '\n' ' ')"
  [ -e "$HOME_DIR/.local/bin/vibepanel" ] && fail "it installed it anyway" || ok "nothing was installed"

  echo "==> platform detection picks the archive for this machine"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_UNAME_S=Darwin VIBEPANEL_UNAME_M=arm64
           VIBEPANEL_PLATFORM=darwin)
  bootrun --yes --no-enable
  [ $RC -eq 0 ] && ok "exits 0 on a Mac" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
  has "$LOG" "v9.9.9 for darwin/arm64" && ok "it names the platform it resolved" \
    || fail "it did not say which archive it wanted: $(head -4 "$LOG" | tr '\n' ' ')"
  [ -f "$HOME_DIR/Library/LaunchAgents/$MAC_LABEL.plist" ] \
    && ok "and the LaunchAgent is what got installed" \
    || fail "a Mac download did not produce a LaunchAgent"

  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_UNAME_S=Linux VIBEPANEL_UNAME_M=aarch64)
  bootrun --yes
  has "$LOG" "linux/arm64" && ok "aarch64 resolves to the linux/arm64 archive" \
    || fail "aarch64 asked for something else: $(head -4 "$LOG" | tr '\n' ' ')"

  echo "==> an architecture with no release says so, and downloads nothing"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_UNAME_M=riscv64)
  bootrun --yes
  [ $RC -ne 0 ] && ok "exits non-zero" || fail "it carried on with an architecture nothing is built for"
  has "$LOG" "riscv64" && ok "it names the architecture" || fail "it did not say what was unsupported"
  has "$LOG" "from source" && ok "and says what to do instead" || fail "no way forward offered"

  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_UNAME_S=FreeBSD)
  bootrun --yes
  [ $RC -ne 0 ] && ok "and an OS with no release, likewise" || fail "FreeBSD was accepted"

  echo "==> an Intel Mac is told plainly, not sent to a 404"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_UNAME_S=Darwin VIBEPANEL_UNAME_M=x86_64)
  bootrun --yes
  [ $RC -ne 0 ] && ok "exits non-zero" || fail "it tried to install a darwin/amd64 archive that is not built"
  has "$LOG" "Intel Mac" && ok "it says what the machine is" || fail "the message does not name the case"

  echo "==> with no --version it asks for the latest release"
  BOOTENV=(VIBEPANEL_API_URL="$BASE/latest.json")
  bootrun --yes --no-enable
  [ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
  has "$LOG" "v9.9.9" && ok "it resolved the tag from the release metadata" \
    || fail "it did not find a version: $(head -4 "$LOG" | tr '\n' ' ')"

  echo "==> --help answers without touching the network"
  BOOTENV=(VIBEPANEL_API_URL="http://127.0.0.1:1/nope" VIBEPANEL_BASE_URL="http://127.0.0.1:1")
  bootrun --help
  [ $RC -eq 0 ] && ok "exits 0 with nowhere to download from" || fail "--help exited $RC"
  has "$LOG" "curl -fsSL" && ok "and shows the one-liner" || fail "--help does not show the command"
  has "$LOG" "vibepanel service" && ok "and says what manages it afterwards" \
    || fail "--help never mentions the management command"

  echo "==> a TMPDIR it cannot work in is replaced, not fatal"
  # The pattern here is download, unpack, *execute*, and /tmp is read-only or
  # mounted noexec on more machines than people expect -- hardened hosts,
  # several CI images, some container runtimes. A read-only TMPDIR is the half
  # of that a check can produce without root; noexec is the other half of the
  # same guard.
  RO="$WORK/readonly-tmp"; mkdir -p "$RO"; chmod 555 "$RO"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 TMPDIR="$RO")
  bootrun --yes --no-enable
  chmod 755 "$RO"
  [ $RC -eq 0 ] && ok "exits 0 anyway" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
  has "$LOG" "is unusable" && ok "it says the temp directory would not do" \
    || fail "it did not say it had moved: $(head -4 "$LOG" | tr '\n' ' ')"
  [ -x "$HOME_DIR/.local/bin/vibepanel" ] && ok "and installed from somewhere else" \
    || fail "nothing was installed"

  echo "==> a release that is not there fails with the URL it tried"
  BOOTENV=(VIBEPANEL_VERSION=v0.0.0)
  bootrun --yes
  [ $RC -ne 0 ] && ok "exits non-zero" || fail "a missing release was treated as success"
  has "$LOG" "could not download" && ok "it says what it could not fetch" \
    || fail "no explanation: $(tail -3 "$LOG" | tr '\n' ' ')"

  echo "==> the bootstrap says its own refusals in the chosen language"
  # Its own, because by the time the archive is unpacked it is the installer
  # inside it doing the talking -- and every message here is one that stops the
  # install, which is exactly the kind that has to be readable.
  BOOTENV=(VIBEPANEL_VERSION=v0.0.0 LC_ALL=zh_CN.UTF-8)
  bootrun --yes
  [ $RC -ne 0 ] && ok "exits non-zero" || fail "a missing release was treated as success"
  has "$LOG" "下载不了" && ok "a download that failed says so in Chinese" \
    || fail "the refusal is not in the locale's language: $(tail -3 "$LOG" | tr '\n' ' ')"

  BOOTENV=(VIBEPANEL_VERSION=v0.0.0)
  bootrun --yes --lang zh
  has "$LOG" "下载不了" && ok "and --lang says it without a locale to help" \
    || fail "--lang did not reach the bootstrap's own messages"

  echo "==> and hands the language to the installer in the archive"
  # The half that is easy to leave out: the bootstrap gets it right, the
  # archive's installer is never told, and the screen changes language halfway
  # through for no reason the person can see.
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9)
  bootrun --yes --no-enable --lang zh
  [ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
  has "$LOG" "── 完成 ──" && ok "the inner installer finished in Chinese too" \
    || fail "--lang stopped at the bootstrap: $(tail -4 "$LOG" | tr '\n' ' ')"
  has "$LOG" "已安装：" && ok "and named what it installed in the same language" \
    || fail "the summary is in the other language"

  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 LC_ALL=zh_CN.UTF-8)
  bootrun --yes --no-enable
  has "$LOG" "── 完成 ──" && ok "a zh locale reaches it too, with no flag at all" \
    || fail "the locale did not survive the hand-over: $(tail -4 "$LOG" | tr '\n' ' ')"

  echo "==> the bootstrap's --help is in the chosen language"
  BOOTENV=(VIBEPANEL_API_URL="http://127.0.0.1:1/nope" VIBEPANEL_BASE_URL="http://127.0.0.1:1")
  bootrun --lang zh --help
  [ $RC -eq 0 ] && has "$LOG" "vibepanel 引导安装程序" && ok "--lang zh --help is Chinese" \
    || fail "--help exited $RC in the wrong language: $(head -2 "$LOG" | tr '\n' ' ')"
  BOOTENV=(VIBEPANEL_API_URL="http://127.0.0.1:1/nope" VIBEPANEL_BASE_URL="http://127.0.0.1:1")
  bootrun --help --lang zh
  has "$LOG" "vibepanel 引导安装程序" \
    && ok "and the flag counts on either side of --help" \
    || fail "--help was printed before --lang was read"

  # ── the mirror ───────────────────────────────────────────────────────────
  #
  # A stand-in for github.muran.tech on a second port: 401 with a verification
  # block until a flag file appears, then serving the same archives. The real
  # one authorises by IP and cannot be driven from a check at all -- and the
  # branch worth pinning is not "does the mirror work", it is "does the person
  # who cannot reach it get told what to click, or a bare failure".
  MPORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
  MFLAG="$WORK/mirror-authorised"
  ( python3 "$REPO/scripts/lib/fake-mirror.py" "$MPORT" "$SERVE" "$MFLAG" >/dev/null 2>&1 ) &
  MIRRORD=$!
  for _ in $(seq 40); do
    curl -s -o /dev/null "http://127.0.0.1:$MPORT/x" && break
    sleep 0.1
  done

  echo "==> not authorised: the notice reaches the person, not a bare failure"
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_MIRROR="http://127.0.0.1:$MPORT")
  bootrun --yes --no-enable
  [ $RC -eq 3 ] && ok "exits 3, which is its own status and not a download failure" \
    || fail "exited $RC; a wrapper cannot tell 'go and click a link' from 'it broke'"
  has "$LOG" "verify?code=DEADBEEF" && ok "it prints the URL the mirror sent" \
    || fail "the link never reached the person: $(tail -5 "$LOG" | tr '\n' ' ')"
  [ -x "$HOME_DIR/.local/bin/vibepanel" ] && fail "it installed anyway" \
    || ok "and nothing was installed"

  echo "==> authorised: the archive travels through the mirror"
  : > "$MFLAG"
  # A github.com base, so url_for reroutes it. With the local BASE_URL of every
  # other case the mirror would be bypassed and this would pass without ever
  # having used it.
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_MIRROR="http://127.0.0.1:$MPORT"
           VIBEPANEL_BASE_URL="https://github.com/x/y/releases/download/v9.9.9")
  bootrun --yes --no-enable
  [ $RC -eq 0 ] && ok "exits 0" || { fail "exited $RC"; sed 's/^/       /' "$LOG"; }
  has "$LOG" "fetching through http://127.0.0.1:$MPORT" \
    && ok "it says where it is fetching from" \
    || fail "it used a mirror without saying so: $(head -5 "$LOG" | tr '\n' ' ')"
  has "$LOG" "sha256 verified" && ok "and still checks the archive" \
    || fail "the checksum was skipped on the mirror path"
  [ -x "$HOME_DIR/.local/bin/vibepanel" ] && ok "and installed" \
    || fail "nothing was installed through the mirror"

  echo "==> a base URL that is not GitHub is never rerouted"
  # The mirror is pointed at a port with nothing on it. If VIBEPANEL_BASE_URL
  # were rerouted this could not possibly succeed -- which is the point: two
  # options that each look reasonable alone must not send an internal URL to a
  # third party.
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9 VIBEPANEL_MIRROR="http://127.0.0.1:$MPORT")
  bootrun --yes --no-enable
  [ $RC -eq 0 ] && ok "the local base URL was used directly" \
    || { fail "exited $RC: a non-GitHub base URL went through the mirror"; sed 's/^/       /' "$LOG"; }

  echo "==> bare --mirror does not eat the option after it"
  # `--mirror --yes` must mean the default mirror and --yes, not a mirror
  # called "--yes". Driven through --help because --help answers before any
  # fetch happens: if --mirror swallowed it, HELP is never set and the script
  # goes looking for a release instead of printing usage -- which is the same
  # shape as the real failure, where --yes is eaten and the installer starts
  # asking questions in a pipeline that cannot answer.
  BOOTENV=(VIBEPANEL_VERSION=v9.9.9)
  bootrun --mirror --help
  [ $RC -eq 0 ] && has "$LOG" "vibepanel bootstrap installer" \
    && ok "the option after bare --mirror is still an option" \
    || fail "bare --mirror swallowed --help: exited $RC, $(head -2 "$LOG" | tr '\n' ' ')"

  kill "$MIRRORD" 2>/dev/null; MIRRORD=""
  kill "$HTTPD" 2>/dev/null; HTTPD=""
fi

# ── the service has to actually be running ────────────────────────────────
#
# `systemctl enable --now` returns 0 when systemd accepts the job. A panel
# whose port is taken binds, fails, and is restarted every few seconds, and the
# installer printed "── done ──  started" over eighteen restarts of that. The
# person was then told to fetch a token from a service that was not running.

echo "==> a service that does not stay up is reported, not called started"
newhome
: > "$WORK/unit-refuses-to-start"
run --yes --user --enable
[ $RC -eq 0 ] && ok "exits 0: the files are installed, which is true" || fail "exited $RC"
has "$LOG" "is not running" && ok "the summary says it is not running" \
  || fail "it reported a start that did not happen: $(tail -6 "$LOG" | tr '\n' ' ')"
has "$LOG" "the one-time setup token is in" \
  && fail "it sent them to fetch a token from a service that is not up" \
  || ok "and does not send them after a token"
rm -f "$WORK/unit-refuses-to-start"

echo "==> and a service that does stay up is still called started"
newhome
run --yes --user --enable
has "$LOG" "is not running" && fail "a healthy start was reported as a failure" \
  || ok "a start that worked is not reported as one that did not"

# ── deploy/uninstall.sh ───────────────────────────────────────────────────
#
# The teardown kills every session on a tmux socket and deletes a data
# directory, so the blast radius is the thing under test and not the happy
# path. Four bugs came out of running it once by hand -- a `set -o pipefail`
# that made every dead socket look alive, a glob that missed half the test
# sockets, a success line printed over a binary that had silently done nothing,
# and a `pgrep -f` that matched the shell running it.

uninst() { # uninst <home> <socket> [args...]
  local h="$1" sock="$2"; shift 2
  ( HOME="$h" \
    VIBEPANEL_TMUX_SOCKET="$sock" \
    VIBEPANEL_DATA_DIR="$h/.local/share/vibepanel" \
    VIBEPANEL_BIN="$h/.local/bin/vibepanel" \
    VIBEPANEL_ENV_FILE="$h/.config/vibepanel.env" \
    "$REPO/deploy/uninstall.sh" "$@" >"$LOG" 2>&1 )
  RC=$?
}

# A home with the shape a real install leaves, and a binary that removes hooks.
teardown_home() { # teardown_home <"real"|"deaf">
  newhome
  mkdir -p "$HOME_DIR/.local/bin" "$HOME_DIR/.local/share/vibepanel/hooks" \
           "$HOME_DIR/.claude" "$HOME_DIR/.codex" "$HOME_DIR/.config/opencode/plugin"
  TD_REPORT="$HOME_DIR/.local/share/vibepanel/hooks/vibepanel-report.sh"
  : > "$TD_REPORT"
  printf '{"model":"opus","hooks":{"Stop":[{"hooks":[{"command":"%s done"}]}]}}\n' \
    "$TD_REPORT" > "$HOME_DIR/.claude/settings.json"
  printf 'notify = ["%s", "waiting"]\n' "$TD_REPORT" > "$HOME_DIR/.codex/config.toml"
  : > "$HOME_DIR/.config/opencode/plugin/vibepanel.js"
  echo x > "$HOME_DIR/.config/vibepanel.env"
  if [ "$1" = real ]; then
    # Removes the hooks, the way the real `vibepanel hook remove` does.
    cat > "$HOME_DIR/.local/bin/vibepanel" <<EOF
#!/bin/sh
if [ "\$1" = hook ] && [ "\$2" = remove ]; then
  rm -f "$HOME_DIR/.config/opencode/plugin/vibepanel.js"
  printf '{"model":"opus"}\n' > "$HOME_DIR/.claude/settings.json"
  : > "$HOME_DIR/.codex/config.toml"
fi
exit 0
EOF
  else
    # A binary from before `hook remove`: takes the argument, does nothing,
    # exits 0. This is the one that has to be caught by looking at the files.
    printf '#!/bin/sh\nexit 0\n' > "$HOME_DIR/.local/bin/vibepanel"
  fi
  chmod +x "$HOME_DIR/.local/bin/vibepanel"
}

echo "==> uninstall: the dry run changes nothing"
teardown_home real
TD_SOCK="vpuninst$$a"
tmux -L "$TD_SOCK" new-session -d -s vp_one 'sleep 120'
uninst "$HOME_DIR" "$TD_SOCK"
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC"
has "$LOG" "Nothing has been done" && ok "it says so" || fail "no such line"
has "$LOG" "vp_one" && ok "it names the session it would kill" || fail "the session list was not printed"
tmux -L "$TD_SOCK" has-session -t =vp_one 2>/dev/null \
  && ok "the session is still running" || fail "a dry run killed a session"
[ -e "$HOME_DIR/.local/bin/vibepanel" ] && ok "the binary is still there" || fail "a dry run deleted it"

echo "==> uninstall: it refuses a socket it must not touch"
for bad in "" default; do
  uninst "$HOME_DIR" "$bad" --yes
  [ $RC -eq 2 ] && ok "refuses socket '${bad:-<empty>}' with its own status" \
    || fail "socket '${bad:-<empty>}' exited $RC, not 2"
done
tmux -L "$TD_SOCK" has-session -t =vp_one 2>/dev/null \
  && ok "and killed nothing on the way" || fail "a refused run still killed the session"

echo "==> uninstall: it kills its own socket and no other"
OTHER="vpuninst$$b"
tmux -L "$OTHER" new-session -d -s not_ours 'sleep 120'
uninst "$HOME_DIR" "$TD_SOCK" --yes --purge
[ $RC -eq 0 ] && ok "exits 0" || fail "exited $RC: $(tail -3 "$LOG")"
tmux -L "$TD_SOCK" has-session 2>/dev/null \
  && fail "the panel's own socket survived" || ok "the panel's sessions are gone"
tmux -L "$OTHER" has-session -t =not_ours 2>/dev/null \
  && ok "the other socket is untouched" || fail "it killed a socket that was not its own"
tmux -L "$OTHER" kill-server 2>/dev/null || true
[ -d "$HOME_DIR/.local/share/vibepanel" ] && fail "the data directory is still there" \
  || ok "the data directory is gone"
[ -e "$HOME_DIR/.config/vibepanel.env" ] && fail "the env file is still there" \
  || ok "the env file is gone"
grep -q vibepanel-report "$HOME_DIR/.claude/settings.json" 2>/dev/null \
  && fail "the claude hooks are still there" || ok "the hooks are gone"

echo "==> uninstall: a binary that cannot remove hooks is caught by looking"
teardown_home deaf
TD_SOCK2="vpuninst$$c"
tmux -L "$TD_SOCK2" new-session -d -s vp_two 'sleep 120'
uninst "$HOME_DIR" "$TD_SOCK2" --yes --purge
has "$LOG" "still there" && ok "it says the hooks are still installed" \
  || fail "it accepted an exit status of 0 as proof: $(grep -c . "$LOG") lines, $(tail -2 "$LOG")"
[ -d "$HOME_DIR/.local/share/vibepanel" ] \
  && ok "and kept the data directory the hooks point into" \
  || fail "it deleted the reporter out from under hooks that still call it"
tmux -L "$TD_SOCK2" kill-server 2>/dev/null || true

echo "==> uninstall: backups are kept unless --purge"
teardown_home real
: > "$HOME_DIR/vibepanel-data-20200101-000000.tar.gz"
mkdir -p "$HOME_DIR/vibepanel-backups"
uninst "$HOME_DIR" "vpuninst$$d" --yes
[ -e "$HOME_DIR/vibepanel-data-20200101-000000.tar.gz" ] \
  && ok "an old archive survives a plain run" || fail "it deleted a backup nobody asked it to"
[ -d "$HOME_DIR/vibepanel-backups" ] && ok "so does the backup directory" \
  || fail "the backup directory was removed without --purge"
has "$LOG" "--purge removes" && ok "and it says how to remove them" || fail "it did not mention them"
# Two archives, so "the newest one" is a distinguishable thing rather than the
# only one.
: > "$HOME_DIR/vibepanel-data-20991231-235959.tar.gz"
uninst "$HOME_DIR" "vpuninst$$d" --yes --purge
[ -e "$HOME_DIR/vibepanel-data-20200101-000000.tar.gz" ] \
  && fail "--purge left the old archive" || ok "--purge removes the old archive"
[ -d "$HOME_DIR/vibepanel-backups" ] && fail "--purge left the backup directory" \
  || ok "and the backup directory"

# The newest one survives --purge, and takes a word of its own.
#
# --purge and --dev-leftovers are unrelated, and passing both to clear a few
# sockets deleted the archive an earlier run had written -- the only remaining
# copy of that database. The flags did what they said; the last copy of
# somebody's data should not be one word away while they are thinking about
# sockets.
[ -e "$HOME_DIR/vibepanel-data-20991231-235959.tar.gz" ] \
  && ok "--purge keeps the newest archive" \
  || fail "--purge deleted the last copy of the database"
has "$LOG" "purge-archives removes that one" \
  && ok "and says which word removes it" || fail "it kept it silently"
uninst "$HOME_DIR" "vpuninst$$d" --yes --purge-archives
[ -e "$HOME_DIR/vibepanel-data-20991231-235959.tar.gz" ] \
  && fail "--purge-archives left it" || ok "--purge-archives removes it"

echo
if [ "$FAILS" -eq 0 ]; then echo "=== install check: 0 FAIL ==="; else echo "=== install check: $FAILS FAIL ==="; fi
exit "$FAILS"
