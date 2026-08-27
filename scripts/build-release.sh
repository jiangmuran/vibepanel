#!/usr/bin/env bash
# Builds the release archives.
#
#   scripts/build-release.sh [version]
#
# Produces dist/vibepanel_<version>_<os>_<arch>.tar.gz, each holding the binary,
# the deployment files and the licence. The frontend is built once and embedded
# into every binary, so an archive is genuinely self-contained: unpack it on a
# machine with tmux and it runs.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
PKG=github.com/jiangmuran/vibepanel/internal/version

# -s -w drops the symbol table and DWARF: nothing here is debugged from a core
# dump, and it takes several megabytes off something people download.
LDFLAGS="-s -w -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT} -X ${PKG}.Date=${DATE}"

echo "==> frontend"
( cd web && npm ci --no-audit --no-fund && npm run build )

rm -rf dist && mkdir -p dist

for target in linux/amd64 linux/arm64 darwin/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  name="vibepanel_${VERSION}_${os}_${arch}"
  echo "==> ${target}"

  # CGO_ENABLED=0 is the whole distribution story: a static binary that runs on
  # a machine you know nothing about, with no runtime to install first.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "dist/${name}/vibepanel" ./cmd/vibepanel

  mkdir -p "dist/${name}/deploy"
  # vibepanel-system.service ships too, and not as documentation: install.sh
  # offers it whenever root is available and installs it from here. It was left
  # out of the archive while the README told people to run it, so the one path
  # that needs root was the one path only a git clone had.
  cp deploy/vibepanel.service deploy/vibepanel-system.service deploy/vibepanel.env \
     "dist/${name}/deploy/"
  # The LaunchAgent ships in every archive, not only the darwin one. It costs
  # two kilobytes, and the alternative is a per-archive file list -- which is
  # the shape that left vibepanel-system.service out while the README told
  # people to run it.
  cp deploy/io.github.jiangmuran.vibepanel.plist "dist/${name}/deploy/"
  # The install script is the difference between "unpack it and it runs" and
  # five manual steps documented in a comment inside a file you have not opened.
  cp deploy/install.sh "dist/${name}/deploy/"
  chmod +x "dist/${name}/deploy/install.sh"
  cp LICENSE README.md "dist/${name}/"

  tar -czf "dist/${name}.tar.gz" -C dist "${name}"
  rm -rf "dist/${name:?}"
done

( cd dist && sha256sum ./*.tar.gz > SHA256SUMS )

echo
echo "==> dist"
ls -lh dist/
