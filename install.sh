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

# -- the mirror -------------------------------------------------------------
#
# Where GitHub is not reachable, `--mirror` sends every fetch through a
# proxy that is: the release archive, its SHA256SUMS, and the latest-release
# lookup. The URL is the whole original URL appended to the mirror's own, which
# is the shape ghproxy-style mirrors use.
#
# It is opt-in and it is never chosen automatically, which is a decision worth
# stating because the alternative looks helpful. Both halves of the checksum
# check -- the archive and the sums it is checked against -- would then come
# from the same host, so a mirror that wanted to serve a different binary would
# only have to serve a matching SHA256SUMS beside it. That is not a reason not
# to offer a mirror; it is a reason the person installing has to be the one who
# says so. A script that silently reroutes to a third party the moment GitHub
# times out has changed who you are trusting without telling you.
#
# DEFAULT_MIRROR is where `--mirror` with no value points.
DEFAULT_MIRROR="https://github.muran.tech"
MIRROR="${VIBEPANEL_MIRROR:-}"

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

# -- which language? --------------------------------------------------------
#
# --lang wins, then LC_ALL / LC_MESSAGES / LANG (the first of the three that is
# set decides, and only it -- that is the order the C library resolves messages
# in). Anything this does not recognise leaves it English.
#
# This half never asks, and that is not laziness: under `curl ... | sh` this
# script *is* stdin, so there is nobody here to ask -- which is the same reason
# the installer inside the archive is handed /dev/tty further down. The question
# is asked there, once, where it can be answered. What happens here is that
# --lang is passed through so that the answer is not asked for twice.
VP_LANG=en
VP_LANG_DECIDED=no

vp_lang_of() { # vp_lang_of <locale or flag value> -> en | zh | nothing
  case "$1" in
    zh|zh_*|zh-*|ZH|ZH_*|ZH-*) echo zh ;;
    en|en_*|en-*|EN|EN_*|EN-*) echo en ;;
  esac
}

for _v in "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do
  [ -n "$_v" ] || continue
  _x="$(vp_lang_of "$_v")"
  [ -z "$_x" ] || { VP_LANG="$_x"; VP_LANG_DECIDED=yes; }
  break
done

# -- strings: begin ---------------------------------------------------------
#
# Same rule as deploy/install.sh, and the same reason: anything a person reads
# while deciding something -- every refusal in here says what to do next -- is
# in both languages; the progress lines that are a record of what already
# happened stay in English.
#
# Substitutions are numbered, %1$s and %2$s, and never a bare %s. printf is not
# what expands them: no shell's builtin printf can be relied on for positional
# specifiers (bash's rejects them outright), and this file has to run under
# dash and busybox ash as well. m_fill below is a prefix/suffix split, which is
# POSIX and works everywhere.
mstr() { # mstr <key> -> MS_EN / MS_ZH; non-zero if there is no such key
  MS_EN=
  MS_ZH=
  case "$1" in
    usage)
      MS_EN='vibepanel bootstrap installer

  curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh

What it does, in order: works out this machine'"'"'s OS and architecture, asks
GitHub for the latest release, downloads that archive and its SHA256SUMS,
refuses to go on unless the two agree, unpacks it into a temporary directory
and runs the installer inside it.

  --version <tag>   install this release instead of the latest
  --repo <o/r>      a fork
  --lang <en|zh>    which language both halves of the installer speak. Without
                    it, LC_ALL / LC_MESSAGES / LANG decide; if none of them
                    says, the installer in the archive asks -- it is handed a
                    terminal further down, and this script is not.
  --mirror[=<url>]  fetch through a GitHub mirror rather than GitHub.
                    Defaults to https://github.muran.tech, which authorises by
                    IP: the first request answers with a link to open in a
                    browser, and this script shows you that link and waits.
                    Note that the archive and the checksums it is checked
                    against then both come from the mirror.
  --keep            leave the unpacked archive behind and say where
  -h, --help        this, and the inner installer'"'"'s options below

Everything else is passed through to the installer in the archive, which is
where the interesting options are:

  --yes             never ask; what a pipeline and `curl | sh -s -- --yes` want
  --enable          start the service when done
  --system          the systemd system service (Linux, needs root)
  --user            the systemd user service / the macOS LaunchAgent
  --migrate         replace an install of the other kind
  --skip-tmux       do not offer to install or upgrade tmux
  --username <name> create the panel'"'"'s first account as part of the install,
                    instead of using the setup token in the browser
  --password-file <path> | --password-env <VAR> | --password-stdin
                    where that account'"'"'s password comes from. Through a
                    one-liner, --password-file is the one to use: a password
                    typed into a pipeline is a password in your scrollback,
                    and there is deliberately no --password <value> anywhere.

Linux and macOS. On Linux it installs a systemd service; on macOS a launchd
LaunchAgent. Where root is available the system service is the recommended
default and the installer says why; where it is not, the user service is, and
the installer says that too.

Afterwards, `vibepanel service` is the one command for status, start, stop,
restart, logs, the setup token, upgrade and uninstall.'
      MS_ZH='vibepanel 引导安装程序

  curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh

它按顺序做这些事：判断这台机器的操作系统和架构，向 GitHub 问最新的发布，
下载那个压缩包和它的 SHA256SUMS，两者对不上就不再往下走，把它解开到一个
临时目录里，然后运行里面的安装程序。

  --version <tag>   装这个版本，而不是最新的
  --repo <o/r>      某个 fork
  --lang <en|zh>    安装程序的两半说哪种语言。不给时由 LC_ALL /
                    LC_MESSAGES / LANG 决定；它们都没说时，由压缩包里的
                    安装程序来问 —— 下面会把终端交给它，而这个脚本没有。
  --mirror[=<url>]  通过 GitHub 镜像下载，而不是直接找 GitHub。
                    默认是 https://github.muran.tech，它按 IP 授权：第一次
                    请求会回一个要在浏览器里打开的链接，本脚本会把那个链接
                    显示出来并等你。注意，那样压缩包和用来校验它的校验和
                    就都来自镜像了。
  --keep            解开之后不删掉，并告诉你在哪里
  -h, --help        这些，外加下面那个内层安装程序的选项

其余的选项都原样传给压缩包里的安装程序，有意思的选项都在那边：

  --yes             从不发问；流水线和 `curl | sh -s -- --yes` 要的就是它
  --enable          装完就启动服务
  --system          systemd 系统服务（Linux，需要 root）
  --user            systemd 用户服务 / macOS 的 LaunchAgent
  --migrate         替换掉已经装了的另一种
  --skip-tmux       不提出安装或升级 tmux
  --username <name> 安装的同时创建面板的第一个账号，
                    而不是在浏览器里用 setup token
  --password-file <path> | --password-env <VAR> | --password-stdin
                    那个账号的密码从哪里来。用一行命令安装时，应该用
                    --password-file：敲进流水线里的密码就是留在你回滚
                    历史里的密码，而且任何地方都故意没有 --password <value>。

只支持 Linux 和 macOS。Linux 上装的是 systemd 服务，macOS 上装的是 launchd
LaunchAgent。有 root 的地方，推荐的默认值是系统服务，安装程序会说明为什么；
没有 root 的地方，默认是用户服务，它同样会说明。

装完之后，`vibepanel service` 是查看状态、启动、停止、重启、日志、setup
token、升级和卸载的唯一命令。'  ;;

    arg.version)
      MS_EN='--version needs a tag'
      MS_ZH='--version 后面要跟一个版本号' ;;
    arg.repo)
      MS_EN='--repo needs owner/name'
      MS_ZH='--repo 后面要跟 owner/name' ;;
    arg.lang)
      MS_EN='--lang needs en or zh'
      MS_ZH='--lang 后面要跟 en 或 zh' ;;

    need.fetch)
      MS_EN='neither curl nor wget is installed, and one of them has to be.
       apt install curl   |   dnf install curl   |   apk add curl'
      MS_ZH='curl 和 wget 一个都没装，而这两个总得有一个。
       apt install curl   |   dnf install curl   |   apk add curl' ;;
    need.tar)
      MS_EN='tar is not installed, and the release is a tar.gz.
       apt install tar   |   dnf install tar   |   apk add tar'
      MS_ZH='没有装 tar，而发布包是 tar.gz。
       apt install tar   |   dnf install tar   |   apk add tar' ;;

    mirror.notopen)
      MS_EN='vibepanel: %1$s will not serve this machine yet. It said:'
      MS_ZH='vibepanel: %1$s 还不肯为这台机器服务。它说：' ;;
    mirror.open)
      MS_EN='vibepanel: the mirror is open'
      MS_ZH='vibepanel: 镜像开了' ;;
    mirror.pipeline)
      MS_EN='vibepanel: open the link above in a browser, then run this again.'
      MS_ZH='vibepanel: 在浏览器里打开上面那个链接，然后再运行一次。' ;;
    mirror.retry)
      MS_EN='vibepanel: open the link above, then press enter to retry (ctrl-c to stop) '
      MS_ZH='vibepanel: 打开上面那个链接，然后按回车重试（ctrl-c 停止） ' ;;
    mirror.nostdin)
      MS_EN='nothing on stdin, so there is nobody to wait for.'
      MS_ZH='标准输入上什么都没有，所以没有人可等。' ;;
    mirror.giveup)
      MS_EN='still not authorised after %1$s tries.'
      MS_ZH='试了 %1$s 次，仍然没有授权。' ;;

    platform.os)
      MS_EN='%1$s is not a platform this project builds for. Linux and macOS only.
       From source: https://github.com/%2$s#from-source'
      MS_ZH='%1$s 不是本项目会构建的平台。只有 Linux 和 macOS。
       从源码构建：https://github.com/%2$s#from-source' ;;
    platform.arch)
      MS_EN='no release is built for %1$s. The archives are linux/amd64,
       linux/arm64 and darwin/arm64. Build it from source (needs Go and Node):
       https://github.com/%2$s#from-source'
      MS_ZH='没有为 %1$s 构建的发布包。已有的压缩包是 linux/amd64、
       linux/arm64 和 darwin/arm64。请从源码构建（需要 Go 和 Node）：
       https://github.com/%2$s#from-source' ;;
    platform.intelmac)
      MS_EN='the release only builds darwin/arm64 (Apple silicon), and this is an
       Intel Mac. Build it from source instead:
       https://github.com/%1$s#from-source'
      MS_ZH='发布包只构建 darwin/arm64（Apple silicon），而这是一台 Intel Mac。
       请改为从源码构建：
       https://github.com/%1$s#from-source' ;;

    latest.fail)
      MS_EN='could not work out the latest release from %1$s.
       Either the network is not there, or the rate limit was hit (60 an hour
       per IP, unauthenticated). Name one instead:
         curl -fsSL .../install.sh | sh -s -- --version v1.2.3'
      MS_ZH='没能从 %1$s 问出最新的发布是哪个。
       要么是网络不通，要么是撞上了限流（未认证时每个 IP 每小时 60 次）。
       那就直接指定一个：
         curl -fsSL .../install.sh | sh -s -- --version v1.2.3' ;;
    download.fail)
      MS_EN='could not download %1$s
       If that release exists, it ships no archive for %2$s.'
      MS_ZH='下载不了 %1$s
       如果那个发布是存在的，那它就没有给 %2$s 的压缩包。' ;;
    sums.fail)
      MS_EN='downloaded %1$s but not its SHA256SUMS, so there is nothing to check
       it against. Refusing to unpack an archive nobody has vouched for.'
      MS_ZH='下载到了 %1$s，却没下到它的 SHA256SUMS，于是没有东西可以拿来校验。
       没有人担保过的压缩包，不会解开。' ;;
    nosha)
      MS_EN='no sha256sum, shasum or openssl on this machine, so the archive cannot
       be verified. Refusing to install something unchecked.'
      MS_ZH='这台机器上没有 sha256sum、shasum 或 openssl，压缩包没法校验。
       没校验过的东西不会装。' ;;
    sums.missing)
      MS_EN='SHA256SUMS does not mention %1$s. That is not a corrupted
       download -- it is the wrong checksum file for this release, and an
       unverified archive is not going to be unpacked.'
      MS_ZH='SHA256SUMS 里根本没提到 %1$s。这不是下载坏了 —— 这是
       这个发布配错了校验和文件，而没校验过的压缩包不会被解开。' ;;
    checksum.mismatch)
      MS_EN='checksum mismatch on %1$s -- the download has been deleted.
         expected %2$s
         got      %3$s
       Retry; if it happens twice, do not install it.'
      MS_ZH='%1$s 的校验和对不上 —— 下载下来的文件已经删掉了。
         期望 %2$s
         实得 %3$s
       再试一次；如果两次都这样，就不要装它。' ;;

    work.nowhere)
      MS_EN='there is nowhere to unpack the release.
       %1$s will not hold a working directory that can be executed from --
       read-only, full, or mounted noexec -- and neither will %2$s.
       Download the archive by hand and run deploy/install.sh from somewhere
       that is not mounted noexec.'
      MS_ZH='没有地方可以解开这个发布包。
       %1$s 放不下一个能从里面执行东西的工作目录 —— 只读、满了，或者是
       noexec 挂载的 —— %2$s 也一样。
       请手动下载压缩包，然后在一个不是 noexec 挂载的地方运行
       deploy/install.sh。' ;;
    extract.fail)
      MS_EN='the archive would not extract'
      MS_ZH='这个压缩包解不开' ;;
    noinstaller)
      MS_EN='the archive does not contain deploy/install.sh, so there is nothing
       here that knows how to install it.'
      MS_ZH='压缩包里没有 deploy/install.sh，所以这里没有东西知道该怎么装它。' ;;
    *) return 1 ;;
  esac
  return 0
}
# -- strings: end -----------------------------------------------------------

# One substitution, done by splitting the string around the token. `case` for
# the test rather than a `[ ]`, because the token contains a `$` and a `%` and
# neither is worth escaping twice.
m_fill() { # m_fill <string> <token> <value>
  mf_s="$1"; mf_tok="$2"; mf_val="$3"; mf_out=''
  while :; do
    case "$mf_s" in
      *"$mf_tok"*)
        mf_out="$mf_out${mf_s%%"$mf_tok"*}$mf_val"
        mf_s="${mf_s#*"$mf_tok"}" ;;
      *) break ;;
    esac
  done
  printf '%s' "$mf_out$mf_s"
}

# m <key> [args...] -- the string for the chosen language, with a newline.
#
# A key that is not here, or one whose side of the pair is empty, prints a
# marker rather than an empty line: an install that silently drops the sentence
# under a question is the failure this whole arrangement exists to prevent, and
# scripts/install-check.sh walks every key in both languages looking for exactly
# this marker.
m() {
  m_key="$1"
  shift
  if ! mstr "$m_key"; then
    printf 'install.sh BUG: no string for key %s\n' "$m_key" >&2
    printf '[missing string: %s]\n' "$m_key"
    return 0
  fi
  m_s="$MS_EN"
  [ "$VP_LANG" != zh ] || m_s="$MS_ZH"
  if [ -z "$m_s" ]; then
    printf 'install.sh BUG: key %s has no %s string\n' "$m_key" "$VP_LANG" >&2
    printf '[missing string: %s/%s]\n' "$m_key" "$VP_LANG"
    return 0
  fi
  m_i=1
  while [ $# -gt 0 ]; do
    m_s="$(m_fill "$m_s" "%$m_i\$s" "$1")"
    m_i=$((m_i + 1))
    shift
  done
  printf '%s\n' "$m_s"
}

# die <key> [args...] -- say it in the chosen language, prefixed so that a
# failure inside a `curl | sh` pipeline says which script it came from, and
# stop.
die() { printf 'install.sh: %s\n' "$(m "$@")" >&2; exit 1; }

usage() { m usage; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die arg.version; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --repo) [ $# -ge 2 ] || die arg.repo; REPO="$2"; shift 2 ;;
    # Read here *and* forwarded: this half has its own refusals to say in the
    # right language, and the half in the archive is the one that asks. The
    # `=` spelling goes into FORWARD so the two-word form cannot come apart on
    # the way -- see the comment on FORWARD's splitting below.
    --lang) [ $# -ge 2 ] || die arg.lang
            LANGV="$(vp_lang_of "$2")"; [ -n "$LANGV" ] || die arg.lang
            VP_LANG="$LANGV"; VP_LANG_DECIDED=yes
            FORWARD="$FORWARD --lang=$LANGV"; shift 2 ;;
    --lang=*) LANGV="$(vp_lang_of "${1#--lang=}")"; [ -n "$LANGV" ] || die arg.lang
            VP_LANG="$LANGV"; VP_LANG_DECIDED=yes
            FORWARD="$FORWARD --lang=$LANGV"; shift ;;
    --repo=*) REPO="${1#--repo=}"; shift ;;
    # `--mirror` on its own means the default one; `--mirror <url>` means that
    # one. Bare `--mirror` may not swallow the next argument, or
    # `--mirror --yes` installs from a mirror called "--yes".
    --mirror) MIRROR="$DEFAULT_MIRROR"; shift ;;
    --mirror=*) MIRROR="${1#--mirror=}"; shift ;;
    --keep) KEEP=yes; shift ;;
    -h|--help) HELP=yes; shift ;;
    # Deliberately forwarded rather than rejected: the options worth knowing
    # about belong to the installer in the archive, and a bootstrap that has to
    # be taught each new one is a bootstrap that is a release behind.
    *) FORWARD="$FORWARD $1"; shift ;;
  esac
done

# --help must not download a release to be able to answer. Printed here rather
# than where the flag is read, so that --lang after it still decides which
# language it is printed in.
if [ "$HELP" = yes ]; then usage; exit 0; fi

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
  die need.fetch
fi

# Everything above fetches a URL. Everything below asks for one by *name*, and
# these two are the only place the mirror exists. Adding a fetch that builds its
# own URL is how half a download ends up going direct on a machine that cannot
# reach GitHub at all.
url_for() { # url_for <the real github url>
  [ -n "$MIRROR" ] || { echo "$1"; return 0; }
  # Only GitHub's own hosts are rerouted. VIBEPANEL_BASE_URL points the archive
  # somewhere else entirely -- it is how install-check serves a *tampered*
  # archive from a local HTTP server -- and sending that through a public
  # mirror would be both broken and a way to leak an internal URL to a third
  # party by setting two options that each look reasonable alone.
  case "$1" in
    https://github.com/*|https://raw.githubusercontent.com/*|https://api.github.com/*|https://objects.githubusercontent.com/*)
      echo "${MIRROR%/}/$1" ;;
    *) echo "$1" ;;
  esac
}

get() { get_to="$2"; fetch "$(url_for "$1")" "$get_to"; }
get_stdout() { fetch_stdout "$(url_for "$1")"; }

# -- the mirror wants to know you are a person ------------------------------
#
# github.muran.tech authorises by IP and answers an unauthorised request with
# 401 and a block of text naming a URL to open in a browser. That block is the
# whole point: it carries a code that expires, so printing "you are not
# authorised, go and sort it out" instead of the body it actually sent would
# make the message useless.
#
# curl's -f is why this needs its own request rather than reading a failure:
# -f discards the body on an HTTP error, which is correct for every other fetch
# here and exactly wrong for this one. The retry is dropped too -- a 401 is an
# answer, not a hiccup, and retrying it twice only delays showing the person
# the link.
mirror_notice() { # mirror_notice <url>  -> prints the body, or nothing
  if command -v curl >/dev/null 2>&1; then
    curl -sSL --proto '=https,http' "$1" 2>/dev/null || true
  else
    wget -qO- "$1" 2>/dev/null || true
  fi
}

mirror_ready() {
  [ -n "$MIRROR" ] || return 0

  probe="$(url_for "https://raw.githubusercontent.com/$REPO/main/install.sh")"
  attempt=1
  while :; do
    if fetch_stdout "$probe" >/dev/null 2>&1; then
      [ "$attempt" = 1 ] || m mirror.open
      return 0
    fi

    echo ""
    m mirror.notopen "${MIRROR%/}"
    echo ""
    mirror_notice "$probe"
    echo ""

    # Under `curl | sh` this script *is* stdin, so there is nobody to ask and
    # waiting would hang a pipeline forever. Say what to do and stop -- with a
    # status of its own, so a wrapper can tell "go and click a link" apart from
    # "the download failed".
    if [ ! -t 0 ] || [ ! -t 1 ]; then
      m mirror.pipeline
      exit 3
    fi

    [ "$attempt" -lt 5 ] || die mirror.giveup "$attempt"
    printf '%s' "$(m mirror.retry)"
    read -r _ || die mirror.nostdin
    attempt=$((attempt + 1))
  done
}

# tar, which is the one other tool this needs and the one people assume is
# always there. It is not in a from-scratch container image, and `tar: not
# found` after a successful download reads like a broken installer.
command -v tar >/dev/null 2>&1 || die need.tar

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
  *) die platform.os "$OS" "$REPO" ;;
esac
case "$MACH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die platform.arch "$MACH" "$REPO" ;;
esac
# An Intel Mac would need darwin/amd64, which is not built. Said plainly here
# rather than as a 404 later, because "no such file" reads like a broken
# installer and this is a deliberate gap.
if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ]; then
  die platform.intelmac "$REPO"
fi

# -- which release? ---------------------------------------------------------
# One handshake, before anything is downloaded. Doing it here rather than on
# first failure means the person is asked to open a link *before* the script
# has told them it is looking up a release, not in the middle of it.
mirror_ready
[ -z "$MIRROR" ] || echo "vibepanel: fetching through ${MIRROR%/} (archive and checksums both)"

if [ -z "$VERSION" ]; then
  echo "vibepanel: looking up the latest release"
  # sed rather than a JSON parser, because requiring jq to install something
  # would make the one-liner a two-liner on most machines. Nothing else in the
  # latest-release response is shaped like the tag_name key.
  VERSION="$(get_stdout "$API_URL" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1 || true)"
  [ -n "$VERSION" ] || die latest.fail "$API_URL"
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
  WORK="$(make_workdir "$FALLBACK")" || die work.nowhere "$TMPBASE" "$FALLBACK"
  echo "vibepanel: $TMPBASE is unusable (read-only or noexec); using $WORK instead"
fi
cleanup() { [ "$KEEP" = yes ] || rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

echo "vibepanel: $VERSION for $OS/$ARCH"
get "$BASE_URL/$NAME" "$WORK/$NAME" \
  || die download.fail "$BASE_URL/$NAME" "$OS/$ARCH"
get "$BASE_URL/SHA256SUMS" "$WORK/SHA256SUMS" \
  || die sums.fail "$NAME"

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
#
# Under --mirror it catches strictly less, and the comment says so where the
# check is rather than only where the flag is: both the archive and the sums it
# is compared against then come from the mirror, so it no longer says anything
# about the mirror itself. It still catches a truncated download.
if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }   # macOS ships this, not sha256sum
elif command -v openssl >/dev/null 2>&1; then
  sum() { openssl dgst -sha256 "$1" | sed 's/.*= *//'; }
else
  die nosha
fi

# build-release.sh runs `sha256sum ./*.tar.gz`, so the names in the file carry
# a leading "./". Both shapes are accepted rather than the one this repository
# happens to produce today.
WANT="$(sed -n "s|^\([0-9a-f]\{64\}\)[[:space:]][[:space:]]*[*]\{0,1\}\.\{0,1\}/\{0,1\}$NAME\$|\1|p" \
  "$WORK/SHA256SUMS" | head -1)"
[ -n "$WANT" ] || die sums.missing "$NAME"
GOT="$(sum "$WORK/$NAME")"
if [ "$WANT" != "$GOT" ]; then
  rm -f "$WORK/$NAME"
  die checksum.mismatch "$NAME" "$WANT" "$GOT"
fi
echo "vibepanel: sha256 verified"

# -- unpack, and hand over --------------------------------------------------
tar -xzf "$WORK/$NAME" -C "$WORK" || die extract.fail
DIR="$WORK/vibepanel_${VERSION}_${OS}_${ARCH}"
if [ ! -d "$DIR" ]; then
  DIR="$(find "$WORK" -maxdepth 1 -type d -name 'vibepanel_*' | head -1)"
fi
[ -n "$DIR" ] && [ -x "$DIR/deploy/install.sh" ] \
  || die noinstaller
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
