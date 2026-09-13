#!/usr/bin/env bash
# The elevated upgrade against real sudo.
#
# The panel upgrades a root-owned binary by running `sudo ... service upgrade`
# for the person who pressed the button, and what sudo does with that depends
# on which sudo it is and on sudoers: a password rule, NOPASSWD for everything
# or for the upgrade alone, a password rule for the upgrade alone, rootpw,
# targetpw, requiretty, the lecture, an account sudoers does not mention. Every
# one of those broke an earlier version, and none of them can be arranged on a
# developer's machine without editing its sudoers.
#
# So each runs in a throwaway container -- Ubuntu 24.04 for sudo 1.9, Ubuntu
# 25.10 for sudo-rs -- as an unprivileged user with no terminal, which is how a
# systemd service runs it, against the real handler: the httpapi test binary,
# built here, running TestElevatedUpgradeAgainstRealSudo once per variant. The
# fast tests in `make check` replay recordings of these same runs; this is what
# notices the recordings going stale.
#
# Needs docker and network (to install sudo and tmux in the images). Without
# docker it says so and counts as a section that did not run.
set -uo pipefail
cd "$(dirname "$0")/.."

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "WARN no usable docker; the real-sudo matrix runs only in containers"
  echo "=== sudo check: 0 FAIL, 1 WARN ==="
  exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

if ! CGO_ENABLED=0 go test -c -o "$WORK/httpapi.test" ./internal/httpapi; then
  echo "=== sudo check: 1 FAIL, 0 WARN ==="
  exit 1
fi

cat > "$WORK/inside.sh" <<'INSIDE'
#!/bin/bash
set -u
export DEBIAN_FRONTEND=noninteractive
if ! { apt-get update -qq && apt-get install -y -qq sudo tmux passwd; } >/dev/null 2>&1; then
  echo "  FAIL could not install sudo and tmux in the image"
  exit 1
fi
echo "  $(sudo --version 2>&1 | head -1)"
useradd -m u
echo 'u:user-pw' | chpasswd
echo 'root:root-pw' | chpasswd
install -d -m 755 /opt/vp
cat > /opt/vp/upgrade <<'UP'
#!/bin/sh
echo "vibepanel: handing over to the installer"
if [ "$(id -u)" = 0 ]; then echo ran >> /opt/vp/ran; else echo not-root >> /opt/vp/ran; fi
UP
chmod 755 /opt/vp/upgrade
: > /opt/vp/ran
chmod 644 /opt/vp/ran
install -m 755 -o u /w/httpapi.test /home/u/httpapi.test

C=/opt/vp/upgrade
ALL="u ALL=(ALL:ALL) ALL"
fails=0
variant() {
  local name=$1 rules=$2
  printf '%s\n' "$rules" > /etc/sudoers.d/vp
  chmod 440 /etc/sudoers.d/vp
  rm -rf /var/run/sudo/ts /var/lib/sudo/ts /var/lib/sudo/lectured 2>/dev/null
  local run="cd /home/u && VP_REAL_SUDO=$name VP_REAL_SUDO_USER_PW=user-pw VP_REAL_SUDO_ROOT_PW=root-pw ./httpapi.test -test.run '^TestElevatedUpgradeAgainstRealSudo\$' -test.count=1 -test.v"
  local as_u=(su u -s /bin/bash -c "$run")
  # An old unit's NoNewPrivileges=yes: the same user, with the flag set.
  if [ "$name" = nnp ]; then
    as_u=(setpriv --reuid=u --regid=u --init-groups --no-new-privs env HOME=/home/u USER=u bash -c "$run")
  fi
  # --- PASS, not just exit 0: a test that skipped exits 0 too.
  if "${as_u[@]}" >/tmp/out 2>&1 && grep -q -- '--- PASS: TestElevatedUpgradeAgainstRealSudo' /tmp/out; then
    echo "  ok   $name"
  else
    echo "  FAIL $name"
    grep -v '^=== ' /tmp/out | tail -15 | sed 's/^/         /'
    fails=$((fails + 1))
  fi
}
variant password      "$ALL"
variant only-upgrade  "u ALL=(ALL:ALL) $C"
variant nopasswd      "u ALL=(ALL:ALL) NOPASSWD: ALL"
variant nopasswd-only "u ALL=(ALL:ALL) NOPASSWD: $C"
variant rootpw        $'Defaults rootpw\n'"$ALL"
variant targetpw      $'Defaults targetpw\n'"$ALL"
variant notsudoer     "# nothing for u"
variant requiretty    $'Defaults requiretty\n'"$ALL"
variant lecture       $'Defaults lecture=always\n'"$ALL"
variant nnp           "u ALL=(ALL:ALL) NOPASSWD: ALL"
exit "$fails"
INSIDE

FAILS=0
for image in ubuntu:24.04 ubuntu:25.10; do
  echo "$image"
  docker run --rm --hostname vphost -v "$WORK:/w:ro" "$image" bash /w/inside.sh
  n=$?
  FAILS=$((FAILS + n))
done

echo "=== sudo check: $FAILS FAIL, 0 WARN ==="
[ "$FAILS" -eq 0 ]
