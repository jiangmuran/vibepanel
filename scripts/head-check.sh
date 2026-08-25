#!/usr/bin/env bash
# Does the committed history build?
#
#   scripts/head-check.sh [ref]
#
# Defaults to HEAD. A ref can be given to check a branch, or to check that this
# script notices a commit that is known not to build.
#
# Every other check in this project drives a binary built from the working
# tree, so all of them are silent about the difference between "my tree works"
# and "what I committed works". They were not the same. HEAD had not compiled
# for some time:
#
#   internal/httpapi/api.go:321: s.DB.HookToken undefined
#
# The caller was committed and the method it calls was not, because commits
# were made by naming paths (`git add <file>`) while the dependency sat
# untracked. Everything stayed green throughout.
#
# So this builds a worktree at HEAD, with nothing from the working tree in it,
# and runs the fast gate there. It is what somebody cloning the repository
# gets.
#
# Writes only to a mktemp directory. Nothing needs root.
set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
REF="${1:-HEAD}"
WORK="$(mktemp -d)"
FAILS=0
fail() { echo "[FAIL] $*"; FAILS=$((FAILS + 1)); }
ok() { echo "[ ok ] $*"; }
cleanup() {
  git -C "$REPO" worktree remove --force "$WORK" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

# Uncommitted work is not a failure — it is the normal state of a working
# tree — but it is the thing that makes this check necessary rather than
# theoretical, so say so.
DIRTY="$(git -C "$REPO" status --porcelain | wc -l)"
if [ "$DIRTY" -gt 0 ]; then
  echo "==> $DIRTY uncommitted changes in the working tree; $REF is checked without them"
fi

rm -rf "$WORK"
if ! git -C "$REPO" worktree add -q --detach "$WORK" "$REF"; then
  fail "could not create a worktree at $REF"
  exit 1
fi

# A worktree under /tmp whose git directory lives elsewhere trips git's
# ownership check, and the Go toolchain turns that into a build failure that
# has nothing to do with the code. Stamping the binary with VCS information is
# not what is being tested here.
export GOFLAGS=-buildvcs=false

cd "$WORK" || exit 1

# CGO_ENABLED=0, because that is what `make build` and the release script do,
# and this check exists to be what somebody cloning the repository gets.
#
# What it catches, measured rather than assumed. With CGO_ENABLED=0 the toolchain
# does not reject cgo, it *excludes* the files that use it — so a cgo file
# nothing references is silently dropped and both builds pass. A cgo symbol that
# is referenced, which is what adding a real dependency looks like, fails to
# build here and compiles fine under the default. That is the case worth
# catching: it otherwise surfaces at `make release-check`, or as a binary that
# will not start on the machine it was copied to.
#
# The forbidden-dependency list in internal/config names four packages. This
# tests the property those names stand for.
if CGO_ENABLED=0 go build ./... 2>"$WORK/.build.err"; then
  ok "go build (CGO_ENABLED=0)"
else
  fail "$REF does not compile:"
  sed 's/^/       /' "$WORK/.build.err" | head -20
fi

if go vet ./... 2>"$WORK/.vet.err"; then
  ok "go vet"
else
  fail "go vet:"
  sed 's/^/       /' "$WORK/.vet.err" | head -20
fi

if go test ./... >"$WORK/.test.out" 2>&1; then
  ok "go test"
else
  fail "go test:"
  grep -vE '^ok|no test files' "$WORK/.test.out" | head -20 | sed 's/^/       /'
fi

# npm ci rather than npm install: the lockfile is part of what is being
# checked, and a clone gets exactly what it pins.
cd "$WORK/web" || exit 1
if npm ci --silent >"$WORK/.npm.out" 2>&1; then
  ok "npm ci"
else
  fail "npm ci:"
  tail -20 "$WORK/.npm.out" | sed 's/^/       /'
fi

if npx tsc -b >"$WORK/.tsc.out" 2>&1; then
  ok "tsc"
else
  fail "tsc:"
  tail -20 "$WORK/.tsc.out" | sed 's/^/       /'
fi

if npx eslint . >"$WORK/.lint.out" 2>&1; then
  ok "eslint"
else
  fail "eslint:"
  tail -20 "$WORK/.lint.out" | sed 's/^/       /'
fi

if npx vitest run >"$WORK/.vitest.out" 2>&1; then
  ok "vitest"
else
  fail "vitest:"
  grep -E 'FAIL|✕|Tests ' "$WORK/.vitest.out" | head -20 | sed 's/^/       /'
fi

# internal/webui/dist is committed, because the binary embeds it and `go build`
# has to work on a machine with no npm. That makes it possible to commit a
# frontend change without rebuilding it: everything above passes, the binary
# compiles, and it serves the previous UI. Nothing else looks at this — the
# browser checks build first, so they never see it.
#
# Rebuilding here and asking git whether anything moved is the whole test.
if npm run build >"$WORK/.webbuild.out" 2>&1; then
  DRIFT="$(git -C "$WORK" status --porcelain -- internal/webui/dist)"
  if [ -n "$DRIFT" ]; then
    fail "the committed frontend bundle is not what these sources build:"
    echo "$DRIFT" | head -10 | sed 's/^/       /'
    echo "       The binary embeds internal/webui/dist, so $REF serves a UI that"
    echo "       does not match its own web/src. Run \`make build\` and commit it."
  else
    ok "internal/webui/dist matches web/src"
  fi
else
  fail "npm run build:"
  tail -20 "$WORK/.webbuild.out" | sed 's/^/       /'
fi

echo
if [ "$FAILS" -gt 0 ]; then
  echo "=== head check: $FAILS FAIL — the working tree is not what was committed ==="
  exit 1
fi
echo "=== head check: $REF builds and passes ==="
