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
# run. The default is the real list, in the order that fails fastest: `check`
# first, then head-check, because everything below it builds from the working
# tree and is therefore silent about the difference between "my tree works" and
# "what I committed works".
TARGETS=${VERIFY_TARGETS:-"check panes-check install-check head-check first-run-check render-check stress-check restart-check scale-check tls-check release-check"}

LOG=$(mktemp -t vibepanel-verify.XXXXXX)
trap 'rm -f "$LOG"' EXIT

failed=""
for t in $TARGETS; do
  echo "── make $t ──"
  if ! make "$t" 2>&1 | tee -a "$LOG"; then
    failed="$failed $t"
  fi
done

echo
echo "── what each check reported ──"
# Two spellings in the tree: render-check and its siblings print "=== x check:
# 0 FAIL, 0 WARN ===", release-check prints its own line. Anything of that
# shape counts.
if ! grep -hE '^=== ' "$LOG" | sed 's/^/  /'; then
  echo "  (nothing printed a summary line, which is itself worth knowing)"
fi

warns=$(grep -hoE '[0-9]+ WARN' "$LOG" | awk '{s+=$1} END{print s+0}')

echo
if [ -n "$failed" ]; then
  echo "verify: FAILED —$failed"
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
