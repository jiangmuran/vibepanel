#!/usr/bin/env bash
# Sessions in a scope of their own, against real systemd as PID 1.
#
# What this proves, per systemd version, as root inside a container:
#
#   fresh     the system unit's ExecStartPre puts a new tmux server in
#             vibepanel-sessions-<uid>.scope; the panel sorts a session into its own
#             cgroup and gives it back an ordinary OOM score; restarting the
#             panel leaves both where they are.
#   upgrade   a panel that kept its sessions in its own unit (the old layout)
#             is replaced by the new unit; after one restart the running tmux
#             server and the session inside it are in the scope, and the
#             session is the same process it was.
#   user      the same two for a user unit, which has no root step and does
#             the moving itself through the user manager.
#   blocked   the root step failing (busctl gone) does not stop the panel
#             starting; it runs with the sessions where they were.
#
# Why these versions: 249 refuses User= on a scope (the helper hands the
# cgroup over itself), 252 is Debian 12, 259 is what this was built on.
#
# Needs docker that can run a privileged container. Not part of `check`; run it
# whenever internal/resources, internal/cgroup, prepare.go or deploy/ changes.
set -uo pipefail
cd "$(dirname "$0")/.."

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "WARN no usable docker; this runs systemd in containers"
  echo "=== isolation check: 0 FAIL, 1 WARN ==="
  exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

if ! CGO_ENABLED=0 go build -o "$WORK/vibepanel" ./cmd/vibepanel; then
  echo "=== isolation check: 1 FAIL, 0 WARN ==="
  exit 1
fi
cp deploy/vibepanel-system.service deploy/vibepanel.service "$WORK/"

cat > "$WORK/inside.sh" <<'INSIDE'
set -u
FAILS=0
ok()   { echo "  ok   $*"; }
fail() { echo "  FAIL $*"; FAILS=$((FAILS + 1)); }
waitfor() { # seconds, command...
  local end=$((SECONDS + $1)); shift
  until "$@" >/dev/null 2>&1; do
    [ $SECONDS -ge $end ] && return 1
    sleep 0.3
  done
}

id u >/dev/null 2>&1 || useradd -m -s /bin/bash u
UH=/home/u
install -m 755 /w/vibepanel /usr/local/bin/vibepanel
as_u() { runuser -u u -- env HOME=$UH XDG_RUNTIME_DIR=/run/user/$(id -u u) "$@"; }
tmuxpid() { as_u tmux -L vibepanel display-message -p '#{pid}' 2>/dev/null; }
cg() { cut -d: -f3 "/proc/$1/cgroup" 2>/dev/null; }
healthy() { curl -sf http://127.0.0.1:18443/api/health; }
SCOPE=/system.slice/vibepanel-sessions-$(id -u u).scope
new_unit() {
  sed -e "s/__USER__/u/g" -e "s#__HOME__#$UH#g" -e "s#__BIN__#/usr/local/bin/vibepanel#g" \
    /w/vibepanel-system.service > /etc/systemd/system/vibepanel.service
}
# The layout before this change: no root step, sessions left in the unit.
old_unit() {
  new_unit
  sed -i -e '/^ExecStartPre=/d' -e '/VIBEPANEL_SESSIONS_PREPARED/d' /etc/systemd/system/vibepanel.service
  sed -i 's#^ExecStart=.*#Environment=VIBEPANEL_ISOLATION=off\nExecStart=/usr/local/bin/vibepanel serve#' /etc/systemd/system/vibepanel.service
}
reset() {
  systemctl stop vibepanel 2>/dev/null
  as_u tmux -L vibepanel kill-server 2>/dev/null
  systemctl stop "$(basename "$SCOPE")" 2>/dev/null
  sleep 1
  rm -rf $UH/.local/share/vibepanel
}
start() {
  systemctl daemon-reload
  systemctl restart vibepanel
  waitfor 30 healthy || { fail "$1: panel never answered"; journalctl -u vibepanel -n 20 --no-pager | sed 's/^/         /'; return 1; }
}
session_in_scope() { # tmux name, pane pid
  [ "$(cg "$2")" = "$SCOPE/pool/s-$1" ]
}

echo "  $(systemctl --version | head -1)"

# ── fresh ─────────────────────────────────────────────────────────────────
reset
new_unit
if start fresh; then
  S=$(tmuxpid)
  if [ -n "$S" ] && [ "$(cg "$S")" = "$SCOPE/pool/tmux" ]; then
    ok "fresh: tmux server in the scope"
  else
    fail "fresh: tmux server ${S:-none} in $(cg "${S:-0}")"
    journalctl -u vibepanel -n 15 --no-pager | sed 's/^/         /'
  fi
  P=$(systemctl show -p MainPID --value vibepanel)
  [ "$(cg "$P")" = "/system.slice/vibepanel.service" ] && ok "fresh: panel alone in its unit" || fail "fresh: panel in $(cg "$P")"
  as_u tmux -L vibepanel new-session -d -s vp_fresh 'sleep 1000'
  PANE=$(as_u tmux -L vibepanel display-message -p -t '=vp_fresh:' '#{pane_pid}')
  if waitfor 10 session_in_scope vp_fresh "$PANE"; then ok "fresh: session sorted into its own cgroup"; else fail "fresh: pane in $(cg "$PANE")"; fi
  [ "$(cat /proc/$PANE/oom_score_adj)" -ge 0 ] && ok "fresh: session's OOM score raised" || fail "fresh: session oom_score_adj $(cat /proc/$PANE/oom_score_adj)"
  systemctl restart vibepanel
  waitfor 30 healthy && [ "$(tmuxpid)" = "$S" ] && session_in_scope vp_fresh "$PANE" \
    && ok "fresh: restart leaves tmux and the session in place" || fail "fresh: after restart server $(tmuxpid) pane in $(cg "$PANE")"
fi

# ── upgrade ───────────────────────────────────────────────────────────────
reset
old_unit
if start upgrade-old; then
  as_u tmux -L vibepanel new-session -d -s vp_old 'sleep 1000'
  S=$(tmuxpid)
  PANE=$(as_u tmux -L vibepanel display-message -p -t '=vp_old:' '#{pane_pid}')
  if [ "$(cg "$PANE")" = "/system.slice/vibepanel.service" ]; then
    ok "upgrade: the old layout keeps the session in the panel's unit"
  else
    fail "upgrade: old layout put the pane in $(cg "$PANE")"
  fi
  new_unit
  if start upgrade-new; then
    [ "$(tmuxpid)" = "$S" ] && [ -d "/proc/$PANE" ] && ok "upgrade: same tmux server, same session process" \
      || fail "upgrade: server $S -> $(tmuxpid), pane $PANE alive=$([ -d /proc/$PANE ] && echo y || echo n)"
    [ "$(cg "$S")" = "$SCOPE/pool/tmux" ] && ok "upgrade: server moved into the scope" \
      || fail "upgrade: server in $(cg "$S")"
    if waitfor 10 session_in_scope vp_old "$PANE"; then ok "upgrade: session moved into its own cgroup"; else fail "upgrade: pane in $(cg "$PANE")"; fi
    left=$(cat /sys/fs/cgroup/system.slice/vibepanel.service/cgroup.procs | wc -l)
    [ "$left" -le 2 ] && ok "upgrade: nothing left behind in the panel's unit" \
      || fail "upgrade: $left processes still in the panel's unit: $(cat /sys/fs/cgroup/system.slice/vibepanel.service/cgroup.procs | xargs -r ps -o comm= -p | tr '\n' ' ')"
  fi
fi

# ── hostile env file ──────────────────────────────────────────────────────
# The account owns the env file the unit reads, and the root step used to run
# whatever "systemctl" that file's PATH found. It must run the system's.
reset
new_unit
install -d -o u -g u $UH/.config $UH/evil
printf '#!/bin/sh\ntouch /tmp/pwned-by-path\nexec /usr/bin/systemctl "$@"\n' > $UH/evil/systemctl
cp $UH/evil/systemctl $UH/evil/busctl
chmod 755 $UH/evil/systemctl $UH/evil/busctl
chown -R u:u $UH/evil
printf 'PATH=%s/evil:/usr/bin:/bin\n' "$UH" > $UH/.config/vibepanel.env
chown u:u $UH/.config/vibepanel.env
rm -f /tmp/pwned-by-path
if start hostile; then
  [ ! -e /tmp/pwned-by-path ] && ok "hostile: the root step ignored the env file's PATH" \
    || fail "hostile: root ran a program from the account's PATH"
  S=$(tmuxpid)
  [ "$(cg "$S")" = "$SCOPE/pool/tmux" ] && ok "hostile: and still moved the sessions" || fail "hostile: server in $(cg "${S:-0}")"
fi
rm -f $UH/.config/vibepanel.env /tmp/pwned-by-path

# ── not the account's ─────────────────────────────────────────────────────
# A root process left in the unit's cgroup (a sudo from a session, say) must
# not be handed to the account with the scope.
reset
old_unit
if start foreign-old; then
  as_u tmux -L vibepanel new-session -d -s vp_f 'sleep 1000'
  # A root sleep, put straight into the panel's unit cgroup.
  (exec -a vp-root-sleeper sleep 1000) &
  ROOTPID=$!
  echo $ROOTPID > /sys/fs/cgroup/system.slice/vibepanel.service/cgroup.procs
  new_unit
  if start foreign-new; then
    case "$(cg $ROOTPID)" in
      $SCOPE*) fail "foreign: a root process was moved into the account's scope" ;;
      *) ok "foreign: a root process in the unit stayed out of the account's scope" ;;
    esac
    [ "$(stat -c %U /sys/fs/cgroup$SCOPE/cgroup.procs)" = u ] && ok "foreign: the scope was still handed over" \
      || fail "foreign: scope owned by $(stat -c %U /sys/fs/cgroup$SCOPE/cgroup.procs)"
  fi
  kill $ROOTPID 2>/dev/null
fi

# ── blocked ───────────────────────────────────────────────────────────────
reset
new_unit
mv /usr/bin/busctl /usr/bin/busctl.off
if start blocked; then
  ok "blocked: the panel starts when the root step fails"
  S=$(tmuxpid)
  [ "$(cg "$S")" = "/system.slice/vibepanel.service" ] && ok "blocked: sessions stay where they were" \
    || fail "blocked: server in $(cg "${S:-0}")"
fi
mv /usr/bin/busctl.off /usr/bin/busctl

# ── user ──────────────────────────────────────────────────────────────────
reset
rm -f /etc/systemd/system/vibepanel.service
systemctl daemon-reload
UID_U=$(id -u u)
loginctl enable-linger u
waitfor 20 as_u systemctl --user is-system-running || waitfor 5 as_u systemctl --user show -p Version || fail "user: no user manager"
install -d -o u -g u $UH/.config/systemd/user
sed 's#%h/.local/bin/vibepanel#/usr/local/bin/vibepanel#' /w/vibepanel.service > $UH/.config/systemd/user/vibepanel.service
chown u:u $UH/.config/systemd/user/vibepanel.service
sed -i 's#^ExecStart=.*#Environment=VIBEPANEL_ISOLATION=off\nExecStart=/usr/local/bin/vibepanel serve#' $UH/.config/systemd/user/vibepanel.service
as_u systemctl --user daemon-reload
as_u systemctl --user restart vibepanel
if waitfor 30 healthy; then
  as_u tmux -L vibepanel new-session -d -s vp_user 'sleep 1000'
  S=$(tmuxpid)
  PANE=$(as_u tmux -L vibepanel display-message -p -t '=vp_user:' '#{pane_pid}')
  sed -i '/VIBEPANEL_ISOLATION=off/d' $UH/.config/systemd/user/vibepanel.service
  as_u systemctl --user daemon-reload
  as_u systemctl --user restart vibepanel
  waitfor 30 healthy || fail "user: panel did not come back"
  US=/user.slice/user-$UID_U.slice/user@$UID_U.service/app.slice/vibepanel-sessions.scope
  in_user_scope() { [ "$(cg "$S")" = "$US/pool/tmux" ] && [ "$(cg "$PANE")" = "$US/pool/s-vp_user" ]; }
  if waitfor 15 in_user_scope; then
    ok "user: the panel moved its own sessions into a user scope"
  else
    fail "user: server in $(cg "$S"), pane in $(cg "$PANE")"
    as_u journalctl --user -u vibepanel -n 10 --no-pager 2>/dev/null | sed 's/^/         /'
  fi
  [ "$(tmuxpid)" = "$S" ] && [ -d "/proc/$PANE" ] && ok "user: same server, same session" || fail "user: the session did not survive"
else
  fail "user: panel never answered"
  as_u journalctl --user -u vibepanel -n 20 --no-pager 2>/dev/null | sed 's/^/         /'
fi

exit $FAILS
INSIDE

FAILS=0
for base in ubuntu:22.04 debian:12 ubuntu:26.04; do
  tag="vibepanel-isolation-check:$(echo "$base" | tr ':.' '--')"
  if ! docker image inspect "$tag" >/dev/null 2>&1; then
    printf 'FROM %s\nRUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq systemd systemd-sysv dbus tmux procps curl >/dev/null && rm -f /etc/systemd/system/*.wants/*\nCMD ["/sbin/init"]\n' "$base" \
      | docker build -q -t "$tag" - >/dev/null || { echo "FAIL could not build $tag"; FAILS=$((FAILS + 1)); continue; }
  fi
  echo "$base"
  name="vp-isolation-$$-$(echo "$base" | tr ':.' '--')"
  docker run -d --name "$name" --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock \
    -v "$WORK:/w:ro" "$tag" >/dev/null
  # PID 1 has to be up before anything asks it for a unit.
  for _ in $(seq 50); do
    docker exec "$name" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break
    sleep 0.2
  done
  docker exec "$name" bash /w/inside.sh
  FAILS=$((FAILS + $?))
  docker rm -f "$name" >/dev/null
done

echo "=== isolation check: $FAILS FAIL, 0 WARN ==="
[ "$FAILS" -eq 0 ]
