#!/usr/bin/env bash
#
# Every check there is, with what each one reported collected under the verdict.
#
# `make verify` used to end in `@echo "all checks passed"`, printed over any
# number of warnings. A WARN does not change a check's exit code, deliberately:
# it separates "the thing under test failed" from "the setup for it did not
# happen", and making setup flakiness fail the gate is how a gate stops being
# run. But several warnings mean whole sections were skipped -- no second
# project to drag, no uploaded file in the tree, no dead session for the header
# check -- and render-check alone has twenty-four of them.
#
# So a run could skip six sections and end with "all checks passed", twenty
# minutes after the warnings scrolled past in the middle of eight checks'
# output. That is the shape `head-check` was written to remove: "HEAD had not
# compiled for some time, while every check passed."
#
# Every check already ends with a line of the same form -- `=== render check: 0
# FAIL, 0 WARN ===`. Collecting them and printing them under the verdict is the
# whole fix. Making a WARN fail the build is the wrong half of the trade.
set -uo pipefail

cd "$(dirname "$0")/.."

# Overridable so the collection logic can be exercised without a twenty-minute
# run. The default is the real list; the order no longer decides anything, see
# the scheduling note below.
TARGETS=${VERIFY_TARGETS:-"check panes-check install-check head-check first-run-check board-check render-check stress-check restart-check scale-check tls-check release-check"}

# ─── how these run at the same time ───────────────────────────────────────
#
# Serially this is about forty-five minutes of mostly waiting: a browser check
# spends its time sleeping on animations, polls and floods, not on a CPU. So
# they run together, and the two things that stops being safe about are both
# about the working tree rather than about load.
#
# 1. `web` is .PHONY, so every `make <something>-check` rebuilds the frontend
#    into internal/webui/dist. Seven of these targets depend on `build`, and
#    running seven `make` processes would have seven vite builds writing that
#    directory while the binaries they just produced are being served from it.
#    One `make -j` invocation is the fix and is what make is for: a shared
#    prerequisite is made once, and everything that depends on it waits.
#    Scheduling this by hand in bash would reintroduce exactly the race.
#
# 2. release-check cannot be in that invocation at all. It calls
#    build-release.sh, which runs `npm ci` in web/ -- and npm ci *deletes*
#    node_modules before reinstalling it. Anything holding a playwright or an
#    eslint out of that directory at that moment loses it mid-run. It rebuilds
#    internal/webui/dist too. So it owns the tree, and it goes last, alone.
#
# What is *not* a reason to keep these serial: the checks that assert on
# wall-clock time. There are three, and the headroom is not close -- /api/state
# measures 2 ms against a 1000 ms bound, a session switch 76-111 ms against
# 4000, the twenty-thousand-line flood has ninety seconds. They are bounds on
# "did this fall over", not benchmarks.
EXCLUSIVE=${VERIFY_EXCLUSIVE:-"release-check"}

parallel=""
serial=""
for t in $TARGETS; do
  case " $EXCLUSIVE " in
    *" $t "*) serial="$serial $t" ;;
    *)        parallel="$parallel $t" ;;
  esac
done

cpus=$(nproc 2>/dev/null || echo 4)

# Two, measured rather than reasoned about, because reasoning about it was
# wrong twice in the same afternoon.
#
# The first guess was cores bounded by MemAvailable, at a couple of GiB per
# browser check, which on a sixteen core machine came out at eight. Every
# browser check went red -- render-check 8 FAIL, board-check dead before it
# printed a line -- and the whole suite was "finished" in five and a half
# minutes. Two separate things were wrong with it.
#
# These checks wait on budgets tuned against an idle machine. render-check's
# mobile scroll samples the top row twenty times at 250 ms, and its own comment
# says that budget was widened so it would not "fail one run in five".
# Oversubscribe the box and all twenty samples land mid-repaint. At four that
# one assertion still failed; the rest were green.
#
# And a memory reading at t=0 is not a budget. This machine runs the panel it
# is testing: a service whose cgroup sat at 19.5 GiB of a 26 GiB max while the
# suite ran. MemAvailable said 21 GiB free because that service had just
# restarted -- it grows back into it. Eight browser checks on top of that took
# the machine far enough into memory pressure that the real panel restarted
# underneath. tmux kept its sessions, which is red line 2 doing exactly its job
# and is the only reason that was a nuisance rather than a data loss. A suite
# that restarts the thing it is testing is not one anybody runs twice.
#
# Two costs almost nothing against four: the run is 18 of its 20 minutes inside
# render-check, which does not go faster for having company. That is also why
# there is no point reaching for more -- the floor is one long check, not the
# number of slots. Raise it with VERIFY_JOBS on a machine that is not also
# somebody's panel.
JOBS=${VERIFY_JOBS:-2}
[ "$JOBS" -lt 1 ] && JOBS=1
[ "$JOBS" -gt "$cpus" ] && JOBS=$cpus

# make's own load governor, which is the dynamic half: -j says how many may run,
# -l says not to start another while the one-minute load average is already at
# or above this. It lags, which is the right failure -- it lets a burst through
# and then holds, rather than throttling a suite that is mostly asleep.
#
# It makes make print `Nothing to be done for 'x-check'` after a recipe that
# ran perfectly well, which is alarming in a gate whose whole subject is checks
# that did not run. Measured before believing it: at -l 0.1 against a machine
# already at load 4, all eight targets' recipes still ran. -l defers jobs, it
# never drops them, and the "every target reported" pass below is what says so
# on a real run rather than a stub.
LOAD=${VERIFY_LOAD:-$cpus}

# Targets that print no `=== ... ===` line of their own. `check` is the fast
# gate -- vet, gofmt, eslint, go test, vitest -- and none of those has one.
SILENT=${VERIFY_SILENT:-"check"}

LOG=$(mktemp -t vibepanel-verify.XXXXXX)
trap 'rm -f "$LOG"' EXIT

started=$(date +%s)
failed=""

if [ -n "${parallel# }" ]; then
  # Read here rather than kept from the sizing above, which no longer computes
  # one: what is free when the suite starts is not what will be free ten
  # minutes in, and printing it as though it were a budget is how it got used
  # as one.
  free_gib=$(awk '/MemAvailable/ {print int($2 / 1048576)}' /proc/meminfo 2>/dev/null || echo '?')
  echo "── ${JOBS} at a time (${cpus} cpus, ${free_gib} GiB free right now, load cap ${LOAD}) ──"
  echo "──$parallel"
  echo
  # --output-sync=target holds each target's output and prints it in one piece
  # when it finishes. The cost is real and worth naming: nothing appears from a
  # twenty-minute render-check until it is over. Interleaving eight checks line
  # by line is not a thing anybody can read, and these all print a summary line
  # at the end anyway.
  #
  # -k so one failure does not cancel the other seven. Which target failed then
  # has to come out of the log rather than an exit code, which is what the grep
  # below is for.
  make -k -j"$JOBS" -l"$LOAD" --output-sync=target $parallel 2>&1 | tee -a "$LOG"
  # Both spellings make uses: a recipe that failed, and a target it refused to
  # start because something it needed had already failed.
  # Only names that were asked for. `check` is `lint test tmux-notice`, so a
  # failing vitest reports `[Makefile:NN: test] Error 2` as well, and a verdict
  # naming `test` sends whoever reads it looking for a target that is not in the
  # list they ran.
  for t in $(grep -hoE "\[(Makefile:[0-9]+: )?[a-z-]+\] Error" "$LOG" |
             sed -E 's/.*: ([a-z-]+)\] Error/\1/;s/^\[([a-z-]+)\] Error/\1/' | sort -u); do
    case " $TARGETS " in *" $t "*) failed="$failed $t" ;; esac
  done
  for t in $(grep -hoE "Target '[a-z-]+' not remade" "$LOG" |
             sed -E "s/Target '([a-z-]+)'.*/\1/" | sort -u); do
    case " $failed " in *" $t "*) ;; *) failed="$failed $t" ;; esac
  done
fi

for t in ${serial# }; do
  echo
  echo "── make $t (alone: it rebuilds the tree these all read) ──"
  if ! make "$t" 2>&1 | tee -a "$LOG"; then
    failed="$failed $t"
  fi
done

elapsed=$(( $(date +%s) - started ))

echo
echo "── what each check reported ──"
# Two spellings in the tree: render-check and its siblings print "=== x check:
# 0 FAIL, 0 WARN ===", release-check prints its own line. Anything of that
# shape counts.
if ! grep -hE '^=== ' "$LOG" | sed 's/^/  /'; then
  echo "  (nothing printed a summary line, which is itself worth knowing)"
fi

# Every target that was asked for has to have reported, and this is the pass
# that says so.
#
# Serially, a check that did not run was a check that was not reached, and the
# failure above it was right there on the screen. Running eleven at once, "did
# not run" stops being self-evident: a scheduling mistake, a target quietly
# skipped, a script that died before its first line of output -- all of them
# look exactly like a clean run, because the thing that is missing is output.
# That is this file's own subject turned on the file itself.
#
# Missing *and* failed is ordinary: it died before it got that far, and the
# failure is already named. Missing and not failed is the one that matters, and
# it fails the run, because a gate that skipped a section and said nothing is
# worse than one that failed.
skipped=""
for t in $TARGETS; do
  case " $SILENT " in *" $t "*) continue ;; esac
  grep -qE "^=== ${t%-check} check" "$LOG" && continue
  case " $failed " in
    *" $t "*) echo "  === $t: failed, and printed no summary line of its own ===" ;;
    *)        skipped="$skipped $t" ;;
  esac
done
for t in $skipped; do
  echo "  === $t: NO SUMMARY LINE AND NO FAILURE — it did not run ==="
done

warns=$(grep -hoE '[0-9]+ WARN' "$LOG" | awk '{s+=$1} END{print s+0}')

echo
printf 'verify: %dm%02ds\n' $(( elapsed / 60 )) $(( elapsed % 60 ))
if [ -n "$failed" ] || [ -n "$skipped" ]; then
  [ -n "$failed" ]  && echo "verify: FAILED —$failed"
  [ -n "$skipped" ] && echo "verify: DID NOT RUN —$skipped"
  exit 1
fi
if [ "$warns" -gt 0 ]; then
  # Not a failure, and not silence either. A warning means a section did not
  # run, and the verdict is the only place anybody looks after twenty minutes.
  echo "all checks passed, with $warns warning(s): that many sections did not run."
  echo "Read the lines above before believing the first half of this sentence."
else
  echo "all checks passed, with no warnings: every section ran."
fi
