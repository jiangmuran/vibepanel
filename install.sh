#!/bin/sh
# vibepanel, from nothing, in one line:
#
#   curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh
#
# Detects the platform, fetches the matching release archive from GitHub,
# checks it against the published SHA256SUMS, unpacks it and hands over to the
# installer inside it (deploy/install.sh), which is the part that knows about
# tmux, services and everything else.
#
# POSIX sh on purpose. The line above pipes this into whatever /bin/sh is,
# which on Debian is dash and on Alpine is busybox ash. Nothing here may assume
# bash: no arrays, no [[ ]], no local, no ${x,,}.
#
# To pass options through, `sh` needs -s:
#
#   curl -fsSL .../install.sh | sh -s -- --yes
#   curl -fsSL .../install.sh | sh -s -- --user --enable
#   curl -fsSL .../install.sh | sh -s -- --help
set -eu

REPO="${VIBEPANEL_REPO:-jiangmuran/vibepanel}"

# -- four overrides that exist only so this script can be tested -----------
#
# scripts/install-check.sh drives all of the above against a local HTTP server
# holding archives it built a second earlier. Nothing in this project's checks
# may fetch and run code from the internet, and the checksum path in particular
# has to be exercised with a *tampered* archive, which is not something GitHub
# will serve on request.
#
#   VIBEPANEL_BASE_URL    where the archives and SHA256SUMS live. Replaces the
#                         GitHub releases directory outright.
#   VIBEPANEL_API_URL     the endpoint asked for the latest tag.
#   VIBEPANEL_VERSION     the tag, so no lookup happens at all.
#   VIBEPANEL_UNAME_S/M   what `uname -s`/`uname -m` are taken to have said, so
#                         the darwin and the unsupported-arch branches can be
#                         driven from a Linux machine. Every other way of
#                         testing those needs a Mac in the loop.
#
# None of them is documented outside this comment. They are not configuration.
API_URL="${VIBEPANEL_API_URL:-https://api.github.com/repos/$REPO/releases/latest}"
BASE_URL="${VIBEPANEL_BASE_URL:-}"
VERSION="${VIBEPANEL_VERSION:-}"

KEEP=no
HELP=no
FORWARD=""

die() { echo "install.sh: $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
vibepanel bootstrap installer

  curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh

What it does, in order: works out this machine's OS and architecture, asks
GitHub for the latest release, downloads that archive and its SHA256SUMS,
refuses to go on unless the two agree, unpacks it into a temporary directory
and runs the installer inside it.

  --version <tag>   install this release instead of the latest
  --repo <o/r>      a fork
  --keep            leave the unpacked archive behind and say where
  -h, --help        this, and the inner installer's options below

Everything else is passed through to the installer in the archive, which is
where the interesting options are:

  --yes             never ask; what a pipeline and `curl | sh -s -- --yes` want
  --enable          start the service when done
  --system          the systemd system service (Linux, needs root)
  --user            the systemd user service / the macOS LaunchAgent
  --migrate         replace an install of the other kind
  --skip-tmux       do not offer to install or upgrade tmux
  --username <name> create the panel's first account as part of the install,
                    instead of using the setup token in the browser
  --password-file <path> | --password-env <VAR> | --password-stdin
                    where that account's password comes from. Through a
                    one-liner, --password-file is the one to use: a password
                    typed into a pipeline is a password in your scrollback,
                    and there is deliberately no --password <value> anywhere.

Linux and macOS. On Linux it installs a systemd service; on macOS a launchd
LaunchAgent. Where root is available the system service is the recommended
default and the installer says why; where it is not, the user service is, and
the installer says that too.

Afterwards, `vibepanel service` is the one command for status, start, stop,
restart, logs, the setup token, upgrade and uninstall.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version needs a tag"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --repo) [ $# -ge 2 ] || die "--repo needs owner/name"; REPO="$2"; shift 2 ;;
    --repo=*) REPO="${1#--repo=}"; shift ;;
    --keep) KEEP=yes; shift ;;
    -h|--help) usage; HELP=yes; shift ;;
    # Deliberately forwarded rather than rejected: the options worth knowing
    # about belong to the installer in the archive, and a bootstrap that has to
    # be taught each new one is a bootstrap that is a release behind.
    *) FORWARD="$FORWARD $1"; shift ;;
  esac
done

# --help must not download a release to be able to answer.
if [ "$HELP" = yes ]; then exit 0; fi

# -- how do we fetch? -------------------------------------------------------
#
# One of the two is on essentially every machine, and which one is missing
# differs per distribution -- Debian minimal has neither -- which is worth
# naming rather than failing with "wget: not found" from inside a pipe.
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto '=https,http' --retry 2 -o "$2" "$1"; }
  fetch_stdout() { curl -fsSL --proto '=https,http' --retry 2 "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
  fetch_stdout() { wget -qO- "$1"; }
else
  die "neither curl nor wget is installed, and one of them has to be.
       apt install curl   |   dnf install curl   |   apk add curl"
fi

# tar, which is the one other tool this needs and the one people assume is
# always there. It is not in a from-scratch container image, and `tar: not
# found` after a successful download reads like a broken installer.
command -v tar >/dev/null 2>&1 || die "tar is not installed, and the release is a tar.gz.
       apt install tar   |   dnf install tar   |   apk add tar"

# -- which archive? ---------------------------------------------------------
#
# The names have to match scripts/build-release.sh exactly:
# vibepanel_<version>_<os>_<arch>.tar.gz, for linux/amd64, linux/arm64 and
# darwin/arm64. Anything else has no archive to download, and saying which
# three exist beats a 404 from a URL the person never saw.
OS="$(printf '%s' "${VIBEPANEL_UNAME_S:-$(uname -s)}" | tr '[:upper:]' '[:lower:]')"
MACH="${VIBEPANEL_UNAME_M:-$(uname -m)}"
case "$OS" in
  linux)  ;;
  darwin) ;;
  *) die "$OS is not a platform this project builds for. Linux and macOS only.
       From source: https://github.com/$REPO#from-source" ;;
esac
case "$MACH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "no release is built for $MACH. The archives are linux/amd64,
       linux/arm64 and darwin/arm64. Build it from source (needs Go and Node):
       https://github.com/$REPO#from-source" ;;
esac
# An Intel Mac would need darwin/amd64, which is not built. Said plainly here
# rather than as a 404 later, because "no such file" reads like a broken
# installer and this is a deliberate gap.
if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ]; then
  die "the release only builds darwin/arm64 (Apple silicon), and this is an
       Intel Mac. Build it from source instead:
       https://github.com/$REPO#from-source"
fi

# -- which release? ---------------------------------------------------------
if [ -z "$VERSION" ]; then
  echo "vibepanel: looking up the latest release"
  # sed rather than a JSON parser, because requiring jq to install something
  # would make the one-liner a two-liner on most machines. Nothing else in the
  # latest-release response is shaped like the tag_name key.
  VERSION="$(fetch_stdout "$API_URL" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1 || true)"
  [ -n "$VERSION" ] || die "could not work out the latest release from $API_URL.
       Either the network is not there, or the rate limit was hit (60 an hour
       per IP, unauthenticated). Name one instead:
         curl -fsSL .../install.sh | sh -s -- --version v1.2.3"
fi

[ -n "$BASE_URL" ] || BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
NAME="vibepanel_${VERSION}_${OS}_${ARCH}.tar.gz"

# Somewhere to unpack that we can also *run* from.
#
# The whole pattern here is download, unpack, execute -- and /tmp is mounted
# noexec on a large fraction of hardened machines, on many CI images and by
# default in some container runtimes. The failure is `./deploy/install.sh:
# Permission denied` on a file that is right there and is mode 0755, which
# sends people to chmod rather than to the mount table.
#
# So it is checked rather than assumed, and there is a fallback that is not
# /tmp. ~/.cache is on the same filesystem as everything else this installs.
workdir_usable() { # workdir_usable <dir>
  printf '#!/bin/sh\nexit 0\n' > "$1/probe" 2>/dev/null || return 1
  chmod +x "$1/probe" 2>/dev/null || return 1
  "$1/probe" 2>/dev/null || return 1
  rm -f "$1/probe"
  return 0
}
# One function, two ways the same directory can be unusable: it cannot be
# created there at all (a read-only or full /tmp, a TMPDIR that does not
# exist), or it can be created and not executed from (noexec). Both end the
# same way, so they are asked as one question.
make_workdir() { # make_workdir <parent>
  d="$(mktemp -d "$1/vibepanel-install.XXXXXX" 2>/dev/null)" || return 1
  if workdir_usable "$d"; then
    printf '%s' "$d"
    return 0
  fi
  rm -rf "$d"
  return 1
}
TMPBASE="${TMPDIR:-/tmp}"
if WORK="$(make_workdir "$TMPBASE")"; then
  :
else
  FALLBACK="${XDG_CACHE_HOME:-$HOME/.cache}/vibepanel"
  mkdir -p "$FALLBACK" 2>/dev/null || true
  WORK="$(make_workdir "$FALLBACK")" || die "there is nowhere to unpack the release.
       $TMPBASE will not hold a working directory that can be executed from --
       read-only, full, or mounted noexec -- and neither will $FALLBACK.
       Download the archive by hand and run deploy/install.sh from somewhere
       that is not mounted noexec."
  echo "vibepanel: $TMPBASE is unusable (read-only or noexec); using $WORK instead"
fi
cleanup() { [ "$KEEP" = yes ] || rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

echo "vibepanel: $VERSION for $OS/$ARCH"
fetch "$BASE_URL/$NAME" "$WORK/$NAME" \
  || die "could not download $BASE_URL/$NAME
       If that release exists, it ships no archive for $OS/$ARCH."
fetch "$BASE_URL/SHA256SUMS" "$WORK/SHA256SUMS" \
  || die "downloaded $NAME but not its SHA256SUMS, so there is nothing to check
       it against. Refusing to unpack an archive nobody has vouched for."

# -- verify, and refuse to go on if it does not -----------------------------
#
# This is the whole reason the download is a separate step from the unpack. A
# `curl | tar xz` pipeline runs whatever arrives, and the first moment anybody
# would notice is after it has been installed.
#
# What it buys and what it does not, because the difference matters: SHA256SUMS
# comes from the same host over the same TLS as the archive, so this catches a
# truncated download, a corrupted mirror and a proxy that rewrote the bytes. It
# is not a signature and does not defend against whoever can publish releases.
if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }   # macOS ships this, not sha256sum
elif command -v openssl >/dev/null 2>&1; then
  sum() { openssl dgst -sha256 "$1" | sed 's/.*= *//'; }
else
  die "no sha256sum, shasum or openssl on this machine, so the archive cannot
       be verified. Refusing to install something unchecked."
fi

# build-release.sh runs `sha256sum ./*.tar.gz`, so the names in the file carry
# a leading "./". Both shapes are accepted rather than the one this repository
# happens to produce today.
WANT="$(sed -n "s|^\([0-9a-f]\{64\}\)[[:space:]][[:space:]]*[*]\{0,1\}\.\{0,1\}/\{0,1\}$NAME\$|\1|p" \
  "$WORK/SHA256SUMS" | head -1)"
[ -n "$WANT" ] || die "SHA256SUMS does not mention $NAME. That is not a corrupted
       download -- it is the wrong checksum file for this release, and an
       unverified archive is not going to be unpacked."
GOT="$(sum "$WORK/$NAME")"
if [ "$WANT" != "$GOT" ]; then
  rm -f "$WORK/$NAME"
  die "checksum mismatch on $NAME -- the download has been deleted.
         expected $WANT
         got      $GOT
       Retry; if it happens twice, do not install it."
fi
echo "vibepanel: sha256 verified"

# -- unpack, and hand over --------------------------------------------------
tar -xzf "$WORK/$NAME" -C "$WORK" || die "the archive would not extract"
DIR="$WORK/vibepanel_${VERSION}_${OS}_${ARCH}"
if [ ! -d "$DIR" ]; then
  DIR="$(find "$WORK" -maxdepth 1 -type d -name 'vibepanel_*' | head -1)"
fi
[ -n "$DIR" ] && [ -x "$DIR/deploy/install.sh" ] \
  || die "the archive does not contain deploy/install.sh, so there is nothing
       here that knows how to install it."
if [ "$KEEP" = yes ]; then echo "vibepanel: unpacked at $DIR"; fi

# stdin, and why this is not simply `exec ./deploy/install.sh`.
#
# Under `curl | sh` this script *is* stdin: the shell is still reading itself
# from the pipe. A child that reads stdin would eat the rest of this file, and
# the installer's prompts would be answered with fragments of its own source.
#
# So the installer never inherits that pipe. When there is a person at a
# terminal -- stdout is one and /dev/tty opens -- it gets /dev/tty and can ask
# its questions, which is the whole point of the tmux and service prompts and
# is otherwise unreachable through a one-liner. When there is not, it gets
# /dev/null and its own rule applies: stdin is not a terminal, so it asks
# nothing and takes the defaults.
CDIR="$DIR"
cd "$CDIR"
# shellcheck disable=SC2086  # FORWARD is a command line, and is meant to split
if [ -t 0 ]; then
  exec ./deploy/install.sh $FORWARD
elif [ -t 1 ] && [ -c /dev/tty ] && (: >/dev/tty) 2>/dev/null; then
  exec ./deploy/install.sh $FORWARD </dev/tty
else
  exec ./deploy/install.sh $FORWARD </dev/null
fi
