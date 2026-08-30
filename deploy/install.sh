#!/usr/bin/env bash
# Installs vibepanel. Interactive when there is somebody to ask, silent when
# there is not.
#
#   ./install.sh                    # ask, show the plan, then do it
#   ./install.sh --yes              # take the defaults, ask nothing
#   ./install.sh --yes --enable     # ...and start it now
#   ./install.sh --user             # the per-user service, even where root works
#   ./install.sh --help
#
# Run this from an unpacked release archive. The one-liner in the repository
# root (install.sh) is what fetches an archive and then runs this.
#
# Linux and macOS. On Linux this is a systemd service, system or user; on macOS
# a launchd LaunchAgent, which is the only service kind macOS has that is worth
# installing -- deploy/io.github.jiangmuran.vibepanel.plist says why.
#
# bash rather than sh, and bash 3.2 rather than 4: macOS still ships 3.2 and
# always will, so no associative arrays, no ${x,,}, no `declare -n`, and no
# bare `"${arr[@]}"` on a possibly-empty array under `set -u`.
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
# The archive layout is <dir>/vibepanel and <dir>/deploy/install.sh, so the
# binary is one level up from this script. Running it from the repo works too.
BIN_SRC="$SRC/../vibepanel"
[ -f "$BIN_SRC" ] || BIN_SRC="$SRC/../../vibepanel"

# Set again once the unit kind is known: a system service does not put its
# binary in one account's home. See the note where that happens.
BIN_DIR="$HOME/.local/bin"
ENV_FILE="$HOME/.config/vibepanel.env"
WHO="${USER:-$(id -un)}"
# Whether --tune-claude was said: yes, never, or unset (ask, when there is
# somebody to ask and a claude to ask about).
TUNE_CLAUDE_FLAG=

# ── the overrides that exist only so this script can be tested ────────────
#
# scripts/install-check.sh drives every branch below, including the ones that
# write to /etc, call `systemctl enable --now`, and install packages. It runs
# on a developer's machine, which has other services on it and often a panel of
# its own. Running the real thing there to find out whether the script works is
# how you find out it works by breaking something that was already running.
#
#   VIBEPANEL_DESTDIR     a DESTDIR-style prefix in front of every system path,
#                         so the system unit is written into a temp directory.
#   VIBEPANEL_SYSTEMCTL   the systemctl to call. The check points it at a
#                         recorder, because `systemctl --user disable --now
#                         vibepanel` would otherwise reach the *developer's*
#                         own panel: the user manager reads the real $HOME, not
#                         the throwaway one this script was handed.
#   VIBEPANEL_ROOT_CMD    how to become root. Empty means "already are"; the
#                         literal `none` means "root is not available here",
#                         which is the fallback path and cannot otherwise be
#                         produced on a machine where sudo works.
#   VIBEPANEL_PLATFORM    linux or darwin, so the launchd half can be driven
#                         from Linux. The alternative is a macOS runner in the
#                         loop for every change to this file, which means the
#                         macOS half is checked by hand or not at all.
#   VIBEPANEL_LAUNCHCTL   the launchctl to call, for the same reason as
#                         VIBEPANEL_SYSTEMCTL.
#   VIBEPANEL_TMUX_BIN    which tmux to probe. Pointing it at a stub that
#                         prints "tmux 3.2" is the only way to exercise the
#                         too-old branch without downgrading the tmux the rest
#                         of this project's tests need.
#   VIBEPANEL_PKG_MANAGER pretend this package manager is the one installed, so
#                         the apt/dnf/pacman/zypper/apk/brew wordings can each
#                         be driven from a machine that has exactly one.
#   VIBEPANEL_PKG_RUNNER  a command put in front of the package install instead
#                         of running it. A check that really runs `apt-get
#                         install tmux` on the developer's machine is the thing
#                         all of this exists to avoid.
#   VIBEPANEL_SUDO        which sudo to run. The sudo-ask path -- sudo that
#                         works but wants a password -- cannot be produced on
#                         a machine where sudo is passwordless, and it is the
#                         path where somebody is told what choosing the system
#                         service will cost them. A stub answers `-n true` with
#                         a failure and `-v` with whichever the case wants.
#   VIBEPANEL_CLAUDE_BIN  which claude to find, or the literal `none` for a
#                         machine that has none. Without it the Claude Code
#                         question appears on a developer's box and not on a
#                         CI runner, so the interactive checks would consume a
#                         different number of answers in the two places -- the
#                         answers after it shift by one and the plan gets
#                         whatever the next line happens to be. That is how
#                         this was found.
#
# None of them is documented outside this comment. They are not configuration.
DESTDIR="${VIBEPANEL_DESTDIR:-}"
SYSTEMCTL="${VIBEPANEL_SYSTEMCTL:-systemctl}"
LAUNCHCTL="${VIBEPANEL_LAUNCHCTL:-launchctl}"
TMUX_BIN="${VIBEPANEL_TMUX_BIN:-tmux}"
PKG_RUNNER="${VIBEPANEL_PKG_RUNNER:-}"
SUDO="${VIBEPANEL_SUDO:-sudo}"

# ── which language does this installer speak? ─────────────────────────────
#
# Three ways, in this order: --lang, then the environment, then the person.
#
#   --lang zh | --lang en   explicit, and it wins. The bootstrap (install.sh at
#                           the repository root) forwards it here.
#   LC_ALL, LC_MESSAGES,    the first of the three that is set decides, and only
#   LANG                    it -- that is the order the C library resolves
#                           messages in, and LC_ALL=C beside LANG=zh_CN means
#                           the person asked for C.
#   the question below      when neither of the above answered and there is
#                           somebody to ask.
#
# Only zh* and en* are read. A locale this does not know is not a guess to be
# made -- de_DE implies neither of the two languages this speaks -- so it leaves
# the question open, and the question is only asked when there is a terminal.
# Unattended, undecided means English: a `curl | sh` inside a pipeline must
# never stop to ask which language to fail in.
VP_LANG=en
VP_LANG_DECIDED=no
# Whether --lang said so, as opposed to a locale. Only a flag skips the
# question: see the block that asks it.
VP_LANG_FROM_FLAG=no

# `return` after the first variable that is set, whatever it said. Falling
# through from an LC_ALL this does not recognise to LANG would be this script
# overruling the variable the C library gives priority to.
vp_lang_of() { # vp_lang_of <locale or flag value> -> en | zh | nothing
  case "$1" in
    zh|zh_*|zh-*|ZH|ZH_*|ZH-*) echo zh ;;
    en|en_*|en-*|EN|EN_*|EN-*) echo en ;;
  esac
}

vp_lang_from_env() {
  local v x
  for v in "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do
    [ -n "$v" ] || continue
    x="$(vp_lang_of "$v")"
    if [ -n "$x" ]; then VP_LANG="$x"; VP_LANG_DECIDED=yes; fi
    return 0
  done
}

# Read before the argument loop, so `--lang zh --help` and `--help --lang zh`
# print the same page. The loop below sees --lang again and steps over it.
vp_lang_from_args() {
  local a want=no x
  for a in "$@"; do
    if [ "$want" = yes ]; then
      want=no
      x="$(vp_lang_of "$a")"
      if [ -n "$x" ]; then VP_LANG="$x"; VP_LANG_DECIDED=yes; VP_LANG_FROM_FLAG=yes; fi
      continue
    fi
    case "$a" in
      --lang) want=yes ;;
      --lang=*) vp_lang_from_args --lang "${a#--lang=}" ;;
    esac
  done
}

vp_lang_from_env
vp_lang_from_args "$@"

# ── strings: begin ────────────────────────────────────────────────────────
#
# Every sentence a person reads while deciding something, in both languages, on
# adjacent lines.
#
# The rule for what is in here, because a half-translated screen is worse than
# either language on its own: if a line is part of a decision -- a question, the
# plan printed before anything is touched, an error saying what to do next, the
# closing summary and its notes -- it is in here. If it is a record of something
# that already happened, and it is a verb and a path, it stays in English where
# it is: `installed /home/x/.local/bin/vibepanel` reads the same either way, and
# translating it would only put a second language in the middle of a block
# nobody reads for meaning.
#
# bash 3.2 has no associative arrays -- macOS ships 3.2 and always will -- so
# this is one `case`. Keys are added here and nowhere else, and every one of
# them carries both languages: scripts/install-check.sh walks every arm below
# and fails if either side is empty, which is the whole reason a pair cannot
# quietly become a single.
#
# Substitutions are %1$s, %2$s, and always numbered -- never a bare %s. Two
# reasons, and the first is not a preference: bash's builtin printf has no
# positional specifiers at all (`printf: \`$': invalid format character`), and
# bash 3.2's is the one macOS has, so `m` substitutes by hand rather than
# through a format string. The second is that Chinese does not put the noun
# where English does, and a pair that used bare %s on both sides would silently
# swap two arguments in one language only. Numbering them also means the strings
# are data and not formats: a literal % in a message cannot break anything.
mstr() { # mstr <key>  -> MS_EN / MS_ZH; non-zero if there is no such key
  MS_EN=
  MS_ZH=
  case "$1" in
    # Both sides are the same on purpose, and it is the one key where that is
    # right: this is asked before anybody has chosen, so a person who reads only
    # one of the two languages has to be able to find their own name in it.
    lang.ask)
      MS_EN='Which language should this installer speak? / 安装程序用哪种语言？

  1) English
  2) 简体中文'
      MS_ZH='Which language should this installer speak? / 安装程序用哪种语言？

  1) English
  2) 简体中文' ;;
    # The question above is bilingual, so its prompt is too -- at that moment
    # neither language has been ruled out.
    lang.prompt)
      MS_EN='  choice / 选择 [%1$s]: '
      MS_ZH='  choice / 选择 [%1$s]: ' ;;
    # The service-kind menu below, which is asked after the language is known.
    choice.prompt)
      MS_EN='  choice [1]: '
      MS_ZH='  选择 [1]：' ;;

    banner)
      MS_EN='vibepanel installer'
      MS_ZH='vibepanel 安装程序' ;;

    usage)
      MS_EN='vibepanel installer

  ./install.sh                    ask what to install, show the plan, do it
  ./install.sh --yes              take the defaults, ask nothing
  ./install.sh --yes --enable     ...and start the service at the end
  ./install.sh --user             the per-user service, even where root works

  -y, --yes, --non-interactive  never ask; suitable for CI and curl | sh
      --interactive             ask even when stdin is not a terminal
      --lang <en|zh>            which language this installer speaks. Without
                                it, LC_ALL / LC_MESSAGES / LANG decide; if none
                                of them says and there is somebody to ask, the
                                first question is which language.
      --enable                  start (or restart) the service when done
      --no-enable               only put the files in place
      --system                  Linux: the systemd *system* service. The
                                default wherever root is available, because it
                                is the only one that can lower OOMScoreAdjust
                                and the only one that is up before you log in.
      --user                    Linux: the systemd *user* service, which needs
                                no root. macOS: the LaunchAgent, which is the
                                only kind there and what --system gets too.
      --migrate                 if the other kind is already installed, remove
                                it. Without this the installer refuses rather
                                than leave two panels on one tmux socket.
      --skip-tmux               do not check for tmux, and never offer to
                                install or upgrade it
      --tune-claude             adjust Claude Code'"'"'s own settings.json as well:
                                what leaves the machine, and what the agent
                                writes into your git history. Every key is
                                printed before anything is written and the file
                                is copied beside itself first. Without this it
                                is offered only when there is somebody to ask
                                and a claude on PATH -- never under --yes.
      --no-tune-claude          do not offer it at all

  The panel'"'"'s first account. Without any of these, the panel prints a one-time
  setup token at startup and you create the account in the browser -- that path
  is unchanged and still works.

      --username <name>         create the first account as part of the install
      --password-stdin          read its password from this script'"'"'s stdin.
                                Implies --yes: the prompts read stdin too, and
                                they cannot both have it.
      --password-file <path>    read it from a file, which is the safe way
                                through the one-liner
      --password-env <VAR>      read it from an environment variable
                                There is no --password <value>: that is a
                                password in your shell history and in `ps`.
  -h, --help

Interactive by default when stdin and stdout are both terminals.

tmux 3.3 or newer is the one prerequisite. If it is missing this offers to
install it with whichever of apt/dnf/pacman/zypper/apk/brew is on the machine,
and if it is too old it says exactly what that costs and offers the same. It
never assumes sudo works: where root is not available it prints the one command
for you to run and stops there.

Afterwards, `vibepanel service` is the single command for status, start, stop,
restart, logs, the one-time setup token, upgrade and uninstall -- on both
platforms and both service kinds, so there is nothing to remember about
systemctl --user versus launchctl.'
      MS_ZH='vibepanel 安装程序

  ./install.sh                    询问装什么，先给出计划，再动手
  ./install.sh --yes              全部用默认值，什么都不问
  ./install.sh --yes --enable     ……并在结束时启动服务
  ./install.sh --user             用按用户的服务，哪怕这里 root 可用

  -y, --yes, --non-interactive  从不发问；CI 和 curl | sh 要的就是它
      --interactive             即使标准输入不是终端也发问
      --lang <en|zh>            安装程序说哪种语言。不给时由 LC_ALL /
                                LC_MESSAGES / LANG 决定；它们都没说，
                                而这里有人可问时，第一个问题就是问语言。
      --enable                  结束时启动（或重启）服务
      --no-enable               只把文件放到位
      --system                  Linux：systemd *system* 服务。只要 root 可用
                                就是默认值，因为只有它能把 OOMScoreAdjust
                                调低，也只有它在你登录之前就已经起来了。
      --user                    Linux：systemd *user* 服务，完全不需要 root。
                                macOS：LaunchAgent，那边只有这一种，
                                --system 得到的也是它。
      --migrate                 如果已经装了另一种，就删掉它。不加这个时，
                                安装程序会拒绝，而不是留下两个面板共用
                                一个 tmux socket。
      --skip-tmux               不检查 tmux，也不提出安装或升级
      --tune-claude             顺带调整 Claude Code 自己的 settings.json：哪些东西
                                会离开这台机器，以及 agent 往你的 git 历史里写什么。
                                写之前会逐条列出，并先把原文件复制到旁边。不加这个
                                参数时，只有在有人可问、且 PATH 上有 claude 的情况
                                下才会问一次 —— --yes 下永远不问。
      --no-tune-claude          完全不提这件事

  面板的第一个账号。一个都不给时，面板会在启动时打印一次性的 setup token，
  你在浏览器里创建账号 —— 那条路没有变，仍然能用。

      --username <name>         安装的同时创建第一个账号
      --password-stdin          从本脚本的标准输入读它的密码。隐含 --yes：
                                提问也要读标准输入，两者不能都占着它。
      --password-file <path>    从文件里读，这是通过一行命令安装时安全的做法
      --password-env <VAR>      从环境变量里读
                                没有 --password <value>：那是把密码留在你的
                                shell 历史里，也留在 `ps` 的输出里。
  -h, --help

标准输入和标准输出都是终端时，默认发问。

唯一的前置条件是 tmux 3.3 或更新。缺了它，本程序会用机器上
apt/dnf/pacman/zypper/apk/brew 里的那一个提出安装；版本太旧时，会说清楚那
究竟损失了什么，并提出同样的升级。它从不假定 sudo 能用：没有 root 时，它只
把那一条命令打印出来，然后停在那里。

装完之后，`vibepanel service` 是查看状态、启动、停止、重启、日志、一次性
setup token、升级和卸载的唯一命令 —— 两个平台、两种服务都一样，不用去记
systemctl --user 和 launchctl 的分别。'  ;;

    arg.lang)
      MS_EN='--lang needs en or zh'
      MS_ZH='--lang 后面要跟 en 或 zh' ;;
    arg.username)
      MS_EN='--username needs a name'
      MS_ZH='--username 后面要跟一个名字' ;;
    arg.pwfile)
      MS_EN='--password-file needs a path'
      MS_ZH='--password-file 后面要跟一个路径' ;;
    arg.pwenv)
      MS_EN='--password-env needs a variable name'
      MS_ZH='--password-env 后面要跟一个变量名' ;;
    arg.password)
      MS_EN='there is no --password flag, on purpose: a password on a command line is in
your shell history and in `ps` output for every other user on this machine.
Use --password-file <path>, --password-env <VAR> or --password-stdin.'
      MS_ZH='没有 --password 这个选项，这是故意的：命令行上的密码会留在你的 shell
历史里，也会出现在这台机器上任何其他用户都能看到的 `ps` 输出里。
请用 --password-file <path>、--password-env <VAR> 或 --password-stdin。' ;;
    arg.unknown)
      MS_EN='unknown option: %1$s
try --help'
      MS_ZH='不认识的选项：%1$s
试试 --help' ;;

    pre.platform)
      MS_EN='error: %1$s is not a platform this installs on; Linux and macOS only.'
      MS_ZH='错误：%1$s 不是本程序能安装的平台；只支持 Linux 和 macOS。' ;;
    pre.nobinary)
      MS_EN='error: no vibepanel binary next to this script
       run this from an unpacked release archive, or use the one-liner:
       curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh'
      MS_ZH='错误：这个脚本旁边没有 vibepanel 可执行文件
       请在解开的发布包里运行它，或者用那一行命令：
       curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh' ;;
    pre.homero)
      MS_EN='error: %1$s is not writable, and everything this installs lives under it.
       Nothing was changed.'
      MS_ZH='错误：%1$s 不可写，而本程序装的每样东西都在它下面。
       什么都没有改动。' ;;
    pre.bindirro)
      MS_EN='error: %1$s exists and is not writable, so the binary cannot be installed.
       Nothing was changed. Fix the permissions, or install it somewhere else
       and edit the unit'"'"'s ExecStart to match.'
      MS_ZH='错误：%1$s 已存在且不可写，可执行文件装不进去。
       什么都没有改动。要么修好权限，要么装到别处，并把 unit 里的
       ExecStart 改成对应的路径。' ;;

    tmux.missing)
      MS_EN='tmux is not installed. The panel keeps every session alive inside it, so
there is nothing to run without it.'
      MS_ZH='没有装 tmux。面板的每个会话都活在它里面，没有它就没有东西可跑。' ;;
    tmux.old)
      MS_EN='tmux %1$s is older than %2$s, so the panel'"'"'s config line
allow-passthrough is not applied and the sequences agent TUIs use for
progress and notifications are silently dropped. Everything else works.'
      MS_ZH='tmux %1$s 比 %2$s 旧，面板配置里的 allow-passthrough 那一行不会生效，
agent 的 TUI 用来发进度和通知的转义序列会被悄悄丢掉。其余一切照常。' ;;
    tmux.nopkg)
      MS_EN='No package manager this knows about (apt/dnf/pacman/zypper/apk/brew) is
on this machine, so tmux has to be installed by hand:
  https://github.com/tmux/tmux/wiki/Installing'
      MS_ZH='这台机器上没有本程序认识的包管理器（apt/dnf/pacman/zypper/apk/brew），
所以 tmux 得自己动手装：
  https://github.com/tmux/tmux/wiki/Installing' ;;
    tmux.noroot)
      MS_EN='Installing it needs root, and root is not available here (no sudo, or it
would need a password and there is nobody to type it). From an account
that has it:
  %1$s'
      MS_ZH='装它需要 root，而这里没有 root（没有 sudo，或者 sudo 会要密码而这里
没有人来输）。请在有 root 的账号下运行：
  %1$s' ;;
    tmux.offer.install)
      MS_EN='install it now with: %1$s  ?'
      MS_ZH='现在就用这条命令装上：%1$s  ？' ;;
    tmux.offer.upgrade)
      MS_EN='try to upgrade it now with: %1$s  ?'
      MS_ZH='现在就用这条命令试着升级：%1$s  ？' ;;
    tmux.autoinstall)
      MS_EN='installing tmux with: %1$s'
      MS_ZH='正在用这条命令安装 tmux：%1$s' ;;
    tmux.noupgrade)
      MS_EN='Not upgrading it unattended -- the distribution'"'"'s package is this same
version, so it would change nothing and say it had. Deliberately:
  https://github.com/tmux/tmux/wiki/Installing'
      MS_ZH='无人值守时不升级它 —— 发行版的包就是同一个版本，升级什么都不会改变，
却会报告成功。这是故意的：
  https://github.com/tmux/tmux/wiki/Installing' ;;
    tmux.gone)
      MS_EN='the package manager reported success and there is still no tmux here.'
      MS_ZH='包管理器报告成功了，而这里仍然没有 tmux。' ;;
    tmux.installed)
      MS_EN='tmux %1$s installed'
      MS_ZH='已装上 tmux %1$s' ;;
    tmux.samever)
      MS_EN='tmux %1$s is what this machine'"'"'s packages offer, and it is still
older than %2$s. Building from source is the only way up:
  https://github.com/tmux/tmux/wiki/Installing'
      MS_ZH='tmux %1$s 就是这台机器的软件源能给的版本，它仍然比 %2$s 旧。
只能从源码编译才能再往上：
  https://github.com/tmux/tmux/wiki/Installing' ;;
    tmux.pkgfail)
      MS_EN='that did not work. Install tmux and run this again:
  %1$s'
      MS_ZH='没成功。装好 tmux 之后再运行一次：
  %1$s' ;;
    tmux.none)
      MS_EN='error: nothing was installed. vibepanel needs tmux.'
      MS_ZH='错误：什么都没有安装。vibepanel 需要 tmux。' ;;

    acct.twosources)
      MS_EN='error: choose one of --password-stdin, --password-file and --password-env.
       Two of them means a script that believes it set one password and set
       another, and neither of us would know which.'
      MS_ZH='错误：--password-stdin、--password-file 和 --password-env 只能选一个。
       给两个，就是一个自以为设了这个密码、其实设了另一个的脚本，
       而你我都不知道到底是哪一个。' ;;
    acct.nouser)
      MS_EN='error: a password was given with no --username, so there is no account to
       create. Add --username <name>, or drop the password and use the
       setup token in the browser.'
      MS_ZH='错误：给了密码却没有 --username，于是没有账号可创建。
       要么补上 --username <name>，要么去掉密码，在浏览器里用
       setup token。' ;;
    acct.stdinclash)
      MS_EN='error: --password-stdin needs stdin to itself, and the prompts read stdin too.
       Add --yes, or use --password-file <path> instead.'
      MS_ZH='错误：--password-stdin 要独占标准输入，而那些提问也要读标准输入。
       请加上 --yes，或者改用 --password-file <path>。' ;;
    acct.unreadable)
      MS_EN='error: cannot read the password file %1$s'
      MS_ZH='错误：读不了密码文件 %1$s' ;;
    acct.failed)
      MS_EN='the account was not created (see above). Everything else is installed, and
the panel will print a one-time setup token at startup as it always did.'
      MS_ZH='账号没有创建成功（原因见上）。其余的都装好了，面板会像以前一样在启动时
打印一次性的 setup token。' ;;

    found.agent)
      MS_EN='  found an existing LaunchAgent:    %1$s'
      MS_ZH='  发现已有的 LaunchAgent：       %1$s' ;;
    found.user)
      MS_EN='  found an existing user service:   %1$s'
      MS_ZH='  发现已有的用户级服务：         %1$s' ;;
    found.system)
      MS_EN='  found an existing system service: %1$s'
      MS_ZH='  发现已有的系统级服务：         %1$s' ;;

    kind.menu)
      MS_EN='How should the panel run?

  1) systemd *system* service (recommended; root is available here)
     Same account, same environment -- it drops to User=%1$s. It is up
     before anyone logs in, and it is the only one that can lower the
     OOM score: measured, a user unit asking for -500 gets 100, a
     system unit gets -500. Needs root once, to write one file.

  2) systemd *user* service
     Runs as you and needs no root at all. Starts at boot once
     lingering is on, which this will enable. Choose this on a shared
     machine, or if you would rather nothing of yours lived in /etc.
'
      MS_ZH='面板要以哪种方式运行？

  1) systemd *system* 服务（推荐；这里 root 可用）
     还是同一个账号、同一套环境 —— 它会降到 User=%1$s。它在任何人登录
     之前就已经起来，而且只有它能把 OOM 分数调低：实测，用户级 unit
     写 -500 拿到的是 100，系统级 unit 拿到的才是 -500。
     只需要一次 root，用来写一个文件。

  2) systemd *user* 服务
     以你的身份运行，完全不需要 root。开启 lingering 之后就会开机自启，
     本程序会替你开。共用的机器上，或者你不希望 /etc 里有你的东西时，
     选这个。
' ;;
    kind.noroot)
      MS_EN='root is not available here (no sudo, or it would need a password and
there is nobody to type it), so this is the systemd *user* service.
It needs no root at all; what it gives up is the OOM score.'
      MS_ZH='这里没有 root（没有 sudo，或者 sudo 会要密码而这里没有人来输），
所以装的是 systemd *user* 服务。它完全不需要 root；放弃的是 OOM 分数。' ;;

    conflict.head)
      MS_EN='there is already a %1$s service installed, and you asked for the
%2$s one. Both at once means two panels sharing one tmux socket and one
database, which does not fail loudly -- it loses writes.'
      MS_ZH='这里已经装了 %1$s 服务，而你要的是 %2$s 的那个。两个同时在，
就是两个面板共用一个 tmux socket 和一个数据库；它不会大声报错 ——
它会丢写入。' ;;
    conflict.ask)
      MS_EN='  remove the %1$s service and install the %2$s one?'
      MS_ZH='  删掉 %1$s 服务，改装 %2$s 的那个？' ;;
    conflict.stop)
      MS_EN='nothing was changed. Either keep what you have, or re-run with:
  %1$s'
      MS_ZH='什么都没有改动。要么保持现状，要么这样再运行一次：
  %1$s' ;;
    conflict.needroot)
      MS_EN='error: removing %1$s needs root, and root is not available here.
       Nothing was changed. From an account that has sudo:
         sudo systemctl disable --now vibepanel && sudo rm %1$s'
      MS_ZH='错误：删掉 %1$s 需要 root，而这里没有 root。
       什么都没有改动。请在有 sudo 的账号下运行：
         sudo systemctl disable --now vibepanel && sudo rm %1$s' ;;

    fallback.darwin)
      MS_EN='macOS has no equivalent of the systemd system service worth installing.
A LaunchDaemon would run as root at boot and then have to drop back to
your account to be any use, and the one thing the Linux system unit
buys -- a lower OOM score -- does not exist here: macOS has no
oom_score_adj, and jetsam cannot be biased from a plist.
Installing the LaunchAgent, which is the macOS answer.'
      MS_ZH='macOS 上没有值得装的、与 systemd 系统服务对应的东西。
LaunchDaemon 会在开机时以 root 运行，然后还得降回你的账号才有用；
而 Linux 系统 unit 换来的那一样东西 —— 更低的 OOM 分数 —— 在这里
根本不存在：macOS 没有 oom_score_adj，jetsam 也没法从 plist 里调。
所以装 LaunchAgent，那是 macOS 上的答案。' ;;
    fallback.noroot)
      MS_EN='root is not available here (no sudo, or it would need a password and
there is nobody to type it), so the system service cannot be installed.
Installing the user service instead -- it gives up the OOM score and
needs lingering to start at boot, and nothing else.'
      MS_ZH='这里没有 root（没有 sudo，或者 sudo 会要密码而这里没有人来输），
所以装不了系统服务。改装用户服务 —— 它放弃的只是 OOM 分数，
以及要靠 lingering 才能开机自启，除此之外没有别的。' ;;
    fallback.nosrc)
      MS_EN='this archive does not ship vibepanel-system.service; installing the user
service instead.'
      MS_ZH='这个压缩包里没有 vibepanel-system.service；改装用户服务。' ;;
    fallback.nosvc)
      MS_EN='no service manager here: %1$s.
Installing the binary and the env file only. Nothing will start the panel
for you, so start it yourself, or from whatever supervises this machine:
  %2$s serve'
      MS_ZH='这里没有服务管理器：%1$s。
只装可执行文件和环境变量文件。不会有东西替你启动面板，所以请自己启动，
或者交给这台机器上负责看管进程的东西：
  %2$s serve' ;;
    err.noplist)
      MS_EN='error: this archive does not ship %1$s.plist, so there is
       nothing to install as a service on macOS.'
      MS_ZH='错误：这个压缩包里没有 %1$s.plist，所以在 macOS 上
       没有东西可以装成服务。' ;;

    foreign.head)
      MS_EN='there is already a file at
  %1$s
and it was not written by this installer -- it has no vibepanel
Documentation= line in it. A hand-written unit, a distribution package,
or an older layout; whichever it is, overwriting it loses whatever was
configured in it, and there is no copy anywhere.'
      MS_ZH='这个位置已经有一个文件：
  %1$s
而它不是本安装程序写的 —— 里面没有 vibepanel 的 Documentation= 那一行。
可能是手写的 unit、发行版的包，或者更早的布局；无论是哪一种，覆盖它都会
丢掉里面配置过的一切，而且任何地方都没有副本。' ;;
    foreign.ask)
      MS_EN='  replace it?'
      MS_ZH='  要替换它吗？' ;;
    foreign.replacing)
      MS_EN='  (replacing it. The old one is not backed up.)'
      MS_ZH='  （这就替换。旧的那个不会备份。）' ;;
    foreign.stop)
      MS_EN='nothing was changed. Move it aside and run this again:
  mv %1$s %1$s.bak'
      MS_ZH='什么都没有改动。把它挪开，然后再运行一次：
  mv %1$s %1$s.bak' ;;

    enable.ask)
      MS_EN='start the service now?'
      MS_ZH='现在就启动服务吗？' ;;

    plan.head)
      MS_EN='about to:'
      MS_ZH='接下来会：' ;;
    plan.bin.install)
      MS_EN='  install  %1$s   (%2$s)'
      MS_ZH='  安装     %1$s   (%2$s)' ;;
    plan.bin.same)
      MS_EN='  replace  %1$s   (the same build: %2$s)'
      MS_ZH='  替换     %1$s   (同一个构建：%2$s)' ;;
    plan.bin.upgrade)
      MS_EN='  replace  %1$s   (%2$s -> %3$s)'
      MS_ZH='  替换     %1$s   (%2$s -> %3$s)' ;;
    plan.unit.user)
      MS_EN='  install  %1$s   (systemd user service)'
      MS_ZH='  安装     %1$s   (systemd 用户服务)' ;;
    plan.unit.system)
      MS_EN='  install  %1$s   (systemd system service, as root)
           with User=%2$s and HOME=%3$s substituted in'
      MS_ZH='  安装     %1$s   (systemd 系统服务，以 root 写入)
           其中 User=%2$s、HOME=%3$s 会被替换进去' ;;
    plan.unit.agent)
      MS_EN='  install  %1$s   (launchd LaunchAgent)
           with HOME=%2$s substituted in'
      MS_ZH='  安装     %1$s   (launchd LaunchAgent)
           其中 HOME=%2$s 会被替换进去' ;;
    plan.unit.none)
      MS_EN='  install  no service: %1$s'
      MS_ZH='  安装     不装服务：%1$s' ;;
    plan.env.keep)
      MS_EN='  keep     %1$s   (already there, yours to edit)'
      MS_ZH='  保留     %1$s   (已经在那里了，归你自己改)' ;;
    plan.env.install)
      MS_EN='  install  %1$s   (edit it before exposing the panel)'
      MS_ZH='  安装     %1$s   (把面板暴露出去之前先改它)' ;;
    plan.remove)
      MS_EN='  remove   the existing %1$s service, so there is only ever one'
      MS_ZH='  删除     现有的 %1$s 服务，这样任何时候都只有一个' ;;
    plan.linger)
      MS_EN='  enable   lingering for %1$s, so it starts at boot and survives logout'
      MS_ZH='  开启     %1$s 的 lingering，让它开机自启、注销后仍在' ;;
    plan.restart)
      MS_EN='  restart  vibepanel (your sessions belong to tmux and survive it)'
      MS_ZH='  重启     vibepanel（你的会话属于 tmux，重启活得下来）' ;;
    plan.start)
      MS_EN='  start    vibepanel'
      MS_ZH='  启动     vibepanel' ;;
    plan.account)
      MS_EN='  create   the panel'"'"'s first account, as %1$s
           (the panel will then not print a setup token at startup)'
      MS_ZH='  创建     面板的第一个账号，用户名 %1$s
           （那样面板启动时就不会再打印 setup token）' ;;
    state.failed)
      MS_EN='State:     it was started and is not running.'
      MS_ZH='状态：    已经启动过，但现在没有在运行。' ;;
    failed.port)
      MS_EN='  Something else is on port %1$s, so the panel binds, fails and is
  restarted every few seconds. Set VIBEPANEL_ADDR in %2$s to a
  free port, then start it again.'
      MS_ZH='  端口 %1$s 上有别的东西，所以面板绑定失败、每隔几秒被重启一次。
  在 %2$s 里把 VIBEPANEL_ADDR 换成一个没人占的端口，再启动一次。'  ;;
    failed.look)
      MS_EN='  The whole log:  %1$s'
      MS_ZH='  完整日志：  %1$s' ;;
    failed.after)
      MS_EN='  Then:  %1$s start'
      MS_ZH='  然后：  %1$s start' ;;
    claude.found)
      MS_EN='Claude Code is here: %1$s'
      MS_ZH='这台机器上有 Claude Code：%1$s' ;;
    claude.what)
      MS_EN='Its settings.json can be adjusted too. These decide what leaves the
machine and what the agent writes into your git history:'
      MS_ZH='它的 settings.json 也可以一并调整。下面这些决定了哪些东西会离开这台
机器，以及 agent 往你的 git 历史里写什么：' ;;
    claude.cannot)
      MS_EN='  skipped  Claude Code settings: could not read them. Nothing was changed.'
      MS_ZH='  跳过     Claude Code 设置：读不出来。什么都没有改。' ;;
    claude.ask)
      MS_EN='Apply these to Claude Code?'
      MS_ZH='要给 Claude Code 应用这些吗？' ;;
    claude.asroot)
      MS_EN='  skipped  Claude Code settings: this is running as root and the file
           belongs to %1$s. Run `vibepanel tune claude --apply` as %1$s.'
      MS_ZH='  跳过     Claude Code 设置：现在是以 root 运行，而这个文件是 %1$s 的。
           请以 %1$s 的身份运行 `vibepanel tune claude --apply`。' ;;
    plan.claude)
      MS_EN='  adjust   %1$s (copied beside itself first)'
      MS_ZH='  调整     %1$s（会先把原文件复制到旁边）' ;;
    plan.port)
      MS_EN='  WARNING: something is already listening on port %1$s, so the panel will
           start, fail to bind and be restarted on a three-second loop.
           Set VIBEPANEL_ADDR in %2$s to a free port first.'
      MS_ZH='  警告：  端口 %1$s 上已经有东西在监听，面板会启动、绑定失败，
          然后被每三秒重启一次。请先在 %2$s 里把 VIBEPANEL_ADDR
          设成一个没人占的端口。' ;;
    kind.sudoask)
      MS_EN='  Choosing 1 stops once to ask for your password. Nothing else here does.
'
      MS_ZH='  选 1 会停下来问你一次密码。这里别的步骤都不会。
' ;;
    kind.nosudo)
      MS_EN='  sudo would not have it, so the system service is not on the table
  after all. %1$s is probably not in sudoers on this machine. Installing
  the user service instead -- it needs no root and works the same.'
      MS_ZH='  sudo 没通过，所以系统服务这条路走不了。%1$s 在这台机器上大概不在
  sudoers 里。改装用户服务 —— 它不需要 root，用起来一样。' ;;
    plan.sudo)
      MS_EN='  sudo will ask for your password.'
      MS_ZH='  sudo 会问你要密码。' ;;
    plan.proceed)
      MS_EN='proceed?'
      MS_ZH='就这样做吗？' ;;
    plan.nochange)
      MS_EN='nothing was changed.'
      MS_ZH='什么都没有改动。' ;;

    err.exec)
      MS_EN='error: %1$s is installed and will not run on this machine.
       Three things do this and they look identical from here:
         - the filesystem holding %2$s is mounted noexec
         - SELinux or AppArmor refuses to execute a file with that label
         - the archive is for a different architecture (%3$s here)
       What it says when you run it directly is the thing that tells them apart:
         %1$s version
       No service was installed; there would be nothing for it to start.'
      MS_ZH='错误：%1$s 已经装好了，但在这台机器上跑不起来。
       有三件事会造成这个现象，而从这里看它们一模一样：
         - 放着 %2$s 的文件系统是 noexec 挂载的
         - SELinux 或 AppArmor 拒绝执行带那个标签的文件
         - 这个压缩包是给别的架构的（这里是 %3$s）
       直接运行它时它说的那句话，才是把三者区分开的东西：
         %1$s version
       没有装任何服务；装了也没有东西可启动。' ;;

    mac.nolaunchctl)
      MS_EN='no launchctl here; from a login session on that Mac:
  launchctl bootstrap %1$s %2$s'
      MS_ZH='这里没有 launchctl；请在那台 Mac 的登录会话里运行：
  launchctl bootstrap %1$s %2$s' ;;
    mac.note)
      MS_EN='note      a LaunchAgent runs in your login session: it starts when you
          log in and stops when you log out. macOS has no lingering.'
      MS_ZH='注意      LaunchAgent 跑在你的登录会话里：你登录时它才启动，
          你注销时它就停。macOS 没有 lingering 这回事。' ;;

    linger.on)
      MS_EN='lingering already on — the panel starts at boot and survives logout'
      MS_ZH='lingering 本来就开着 —— 面板会开机自启，注销后仍在' ;;
    linger.enabled)
      MS_EN='enabled lingering — the panel now starts at boot and survives logout
  (undo with: loginctl disable-linger %1$s)'
      MS_ZH='已开启 lingering —— 面板从此开机自启，注销后仍在
  （要撤销：loginctl disable-linger %1$s）' ;;
    linger.failed)
      MS_EN='could not enable lingering; without it the panel stops when you log out:
  loginctl enable-linger %1$s'
      MS_ZH='开不了 lingering；没有它，你一注销面板就停：
  loginctl enable-linger %1$s' ;;
    user.nosession)
      MS_EN='no user systemd session here; from a login shell on that machine:
  systemctl --user daemon-reload && systemctl --user enable --now vibepanel'
      MS_ZH='这里没有用户级 systemd 会话；请在那台机器的登录 shell 里运行：
  systemctl --user daemon-reload && systemctl --user enable --now vibepanel' ;;
    sys.note)
      MS_EN='note      a system service needs no lingering; it is up before anyone logs in'
      MS_ZH='注意      系统服务不需要 lingering；任何人登录之前它就已经起来了' ;;

    what.user)
      MS_EN='the systemd user service (%1$s)'
      MS_ZH='systemd 用户服务（%1$s）' ;;
    what.system)
      MS_EN='the systemd system service (%1$s)'
      MS_ZH='systemd 系统服务（%1$s）' ;;
    what.agent)
      MS_EN='the launchd LaunchAgent (%1$s)'
      MS_ZH='launchd LaunchAgent（%1$s）' ;;
    what.none)
      MS_EN='the binary only -- no service, because %1$s'
      MS_ZH='只有可执行文件 —— 没有服务，因为%1$s' ;;

    done.rule)
      MS_EN='── done ──'
      MS_ZH='── 完成 ──' ;;
    done.installed)
      MS_EN='installed: %1$s'
      MS_ZH='已安装：  %1$s' ;;
    done.account)
      MS_EN='account:   %1$s, created just now -- there is no setup token to find'
      MS_ZH='账号：    %1$s，刚刚创建 —— 没有 setup token 要去找' ;;
    state.started)
      MS_EN='state:     started just now'
      MS_ZH='状态：    刚刚启动' ;;
    state.restarted)
      MS_EN='state:     restarted (it was already running)
           your sessions are untouched -- they belong to tmux, not to
           the panel process.'
      MS_ZH='状态：    已重启（它本来就在跑）
          你的会话没被动过 —— 它们属于 tmux，不属于面板进程。' ;;
    state.notstarted)
      MS_EN='state:     not started; the files are in place'
      MS_ZH='状态：    没有启动；文件都已就位' ;;
    state.nonestarted)
      MS_EN='state:     not started, and nothing here can start it for you'
      MS_ZH='状态：    没有启动，而这里没有东西能替你启动它' ;;
    open.login)
      MS_EN='open  %1$s  and log in as %2$s.'
      MS_ZH='打开  %1$s  ，用 %2$s 登录。' ;;
    box.open)
      MS_EN='1  open   %1$s'
      MS_ZH='1  打开   %1$s' ;;
    box.login)
      MS_EN='2  log in as %1$s'
      MS_ZH='2  用 %1$s 登录' ;;
    box.token)
      MS_EN='2  token  %1$s'
      MS_ZH='2  令牌   %1$s' ;;
    box.tokencmd)
      MS_EN='2  token  %1$s token'
      MS_ZH='2  令牌   %1$s token' ;;
    token.head)
      MS_EN='the one-time setup token:'
      MS_ZH='一次性的 setup token：' ;;
    token.cmd)
      MS_EN='  %1$s token          # or: %2$s'
      MS_ZH='  %1$s token          # 或者：%2$s' ;;
    token.thenopen)
      MS_EN='then open  %1$s  and paste it.'
      MS_ZH='然后打开  %1$s  ，把它粘进去。' ;;
    restarted.token)
      MS_EN='the setup token was consumed at first install. The log, if you need it:
  %1$s logs           # or: %2$s'
      MS_ZH='setup token 在第一次安装时就用掉了。要看日志的话：
  %1$s logs           # 或者：%2$s' ;;
    restarted.url)
      MS_EN='the panel is at  %1$s'
      MS_ZH='面板在  %1$s' ;;
    notstarted.cmd)
      MS_EN='  %1$s start'
      MS_ZH='  %1$s start' ;;
    notstarted.token)
      MS_EN='  %1$s token          # the one-time setup token'
      MS_ZH='  %1$s token          # 一次性的 setup token' ;;
    notstarted.serve)
      MS_EN='  %1$s serve'
      MS_ZH='  %1$s serve' ;;
    after.none)
      MS_EN='afterwards: %1$s upgrade   (the rest of it needs a service to talk to)'
      MS_ZH='之后：    %1$s upgrade   （其余的都得有个服务可说话才行）' ;;
    after.svc)
      MS_EN='afterwards: %1$s {status|start|stop|restart|logs|token|upgrade|uninstall}'
      MS_ZH='之后：    %1$s {status|start|stop|restart|logs|token|upgrade|uninstall}' ;;

    note.path)
      MS_EN='note:      %1$s is not on your PATH, so "vibepanel" will not be a
           command you can type. The service does not care -- it uses the
           full path -- but you will want it. Add to %2$s:'
      MS_ZH='注意：    %1$s 不在你的 PATH 上，所以 "vibepanel" 不会是一条
          你能直接敲的命令。服务本身不在乎 —— 它用的是全路径 —— 但你会
          想要它。在 %2$s 里加上：' ;;
    note.xdg)
      MS_EN='note:      XDG_RUNTIME_DIR is not set in this shell, so the systemd *user*
           manager cannot be reached from here -- which is the state of a
           bare non-login ssh command and of every cron job. The unit is
           installed; enable it from a real login session:
             ssh -t %1$s@%2$s '"'"'systemctl --user enable --now vibepanel'"'"''
      MS_ZH='注意：    这个 shell 里没有设 XDG_RUNTIME_DIR，所以从这里够不到
          systemd 的 *用户* 管理器 —— 一条不带登录的 ssh 命令、以及每个
          cron 任务，都是这个状态。unit 已经装好了；请在真正的登录会话里
          启用它：
             ssh -t %1$s@%2$s '"'"'systemctl --user enable --now vibepanel'"'"''  ;;
    note.tmuxold)
      MS_EN='note:      tmux %1$s is older than %2$s. The panel works; progress and
           notification sequences from agent TUIs will not reach it.'
      MS_ZH='注意：    tmux %1$s 比 %2$s 旧。面板能用；agent 的 TUI 发出的进度和
          通知序列到不了它那里。' ;;
    note.systemunit)
      MS_EN='if this machine runs close to its memory and you want the kernel to look
elsewhere first, there is a system unit that can actually say so:
  ./install.sh --system --migrate   (needs root; the user unit cannot)'
      MS_ZH='如果这台机器的内存一直吃得很紧，而你希望内核先去找别人麻烦，
那么有一个系统 unit 能真的把这件事说出口：
  ./install.sh --system --migrate   （需要 root；用户 unit 做不到）' ;;
    *) return 1 ;;
  esac
  return 0
}
# ── strings: end ──────────────────────────────────────────────────────────

# m <key> [args...] -- the string for the chosen language, with a newline.
#
# A key that is not in the table, or one whose side of the pair is empty, does
# not print an empty line. An empty line is the worst possible failure here: the
# question above it still gets asked, the consequence under it is simply gone,
# and nothing anywhere reports a problem. So it prints a marker where the
# sentence should have been and says so on stderr as well, and
# scripts/install-check.sh walks every key in both languages looking for exactly
# that marker.
m() {
  local key="$1" s i
  shift
  if ! mstr "$key"; then
    printf 'vibepanel installer BUG: no string for key %s\n' "$key" >&2
    printf '[missing string: %s]\n' "$key"
    return 0
  fi
  s="$MS_EN"
  [ "$VP_LANG" = zh ] && s="$MS_ZH"
  if [ -z "$s" ]; then
    printf 'vibepanel installer BUG: key %s has no %s string\n' "$key" "$VP_LANG" >&2
    printf '[missing string: %s/%s]\n' "$key" "$VP_LANG"
    return 0
  fi
  # By hand, not through printf: see the note above the table. An argument that
  # is never referenced leaves the message intact, and a %1$s with no argument
  # is left standing in the output where it is impossible to miss.
  i=1
  while [ $# -gt 0 ]; do
    s="${s//%$i\$s/$1}"
    i=$((i + 1))
    shift
  done
  printf '%s\n' "$s"
}
# The same thing on stderr, which is where every error in this script goes.
me() { m "$@" >&2; }

# ── the box ───────────────────────────────────────────────────────────────
#
# The last thing this script prints is the only part most people read, and it
# was a column of `label:  value` lines that the eye slides off -- 「安装成功后
# 信息也特别乱」. What matters is two facts and one command, so they go in a
# rectangle and everything else stays outside it.

# vp_cols is the display width of a string.
#
# Counted from bytes rather than with `wc -m`, because `wc -m` needs a UTF-8
# LC_CTYPE and this runs on whatever the machine has -- a minimal container
# with LANG unset counts characters as bytes and every line comes out ragged.
#
# Bytes minus continuation bytes is the character count, and each CJK character
# is three bytes and two columns, so adding the number of them back gives the
# width. That holds for everything the box is allowed to contain, which is why
# vp_box refuses anything else.
vp_cols() {
  local bytes cont
  bytes=$(printf '%s' "$1" | LC_ALL=C wc -c | tr -d ' ')
  cont=$(printf '%s' "$1" | LC_ALL=C tr -dc '\200-\277' | LC_ALL=C wc -c | tr -d ' ')
  printf '%s' $(( (bytes - cont) + cont / 2 ))
}

vp_rule() {
  local n="$1" out= i=0
  while [ "$i" -lt "$n" ]; do out="$out─"; i=$((i + 1)); done
  printf '%s' "$out"
}

# vp_box draws its stdin inside a rectangle.
#
# The right edge is aligned, which is the whole reason vp_cols exists: a box
# whose closing bars do not line up looks more broken than no box at all.
vp_box() {
  local line w max=0 i=0
  local lines=()
  while IFS= read -r line; do
    lines[$i]="$line"
    i=$((i + 1))
    w=$(vp_cols "$line")
    [ "$w" -gt "$max" ] && max="$w"
  done
  printf '  ┌%s┐\n' "$(vp_rule $((max + 4)))"
  for line in "${lines[@]}"; do
    w=$(vp_cols "$line")
    printf '  │  %s%*s  │\n' "$line" "$((max - w))" ""
  done
  printf '  └%s┘\n' "$(vp_rule $((max + 4)))"
}

case "${VIBEPANEL_PLATFORM:-$(uname -s)}" in
  [Dd]arwin) PLATFORM=darwin ;;
  [Ll]inux)  PLATFORM=linux ;;
  *) me pre.platform "$(uname -s)"
     exit 1 ;;
esac

# Linux paths.
USER_UNIT_DIR="$HOME/.config/systemd/user"
USER_UNIT="$USER_UNIT_DIR/vibepanel.service"
SYSTEM_UNIT="$DESTDIR/etc/systemd/system/vibepanel.service"
SYSTEM_UNIT_SRC="$SRC/vibepanel-system.service"

# macOS paths. The label is reverse-DNS because launchctl addresses jobs by it
# (gui/<uid>/<label>), so it is a name three other things have to agree on:
# this script, the plist, and `vibepanel service`.
MAC_LABEL="io.github.jiangmuran.vibepanel"
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST="$PLIST_DIR/$MAC_LABEL.plist"
PLIST_SRC="$SRC/$MAC_LABEL.plist"
MAC_LOG="$HOME/Library/Logs/vibepanel.log"

# tmux 3.3, and the reason is in internal/tmux: allow-passthrough arrived
# there. An older tmux does not refuse the embedded config -- it reports an
# unknown option, carries on with defaults, and silently swallows the sequences
# agent TUIs use for progress and notifications from then on. Every symptom is
# something not appearing.
TMUX_MIN_MAJOR=3
TMUX_MIN_MINOR=3

INTERACTIVE=auto
ENABLE=auto        # auto: ask, or restart a unit that is already running
KIND=              # user | system | agent; empty means "decide below"
MIGRATE=no
DO_TMUX=auto       # auto | no  -- --skip-tmux is the only way to say no
ASKED_SYSTEM=no    # was --system typed? macOS has to answer that, not ignore it

# The first account, if it is being created from here rather than in the
# browser. Empty means "the panel prints a setup token and you use the wizard",
# which is the path that existed first and still works.
ACCT_USER=
ACCT_STDIN=no
ACCT_FILE=
ACCT_ENV=

usage() { m usage; }

# A shifting loop rather than `for arg in "$@"`, because three of the options
# take a value and the for-loop cannot see the argument after the one it is
# looking at. The `--flag=value` spellings are accepted too, because that is
# what people type into a one-liner.
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes|--non-interactive) INTERACTIVE=no ;;
    # Taken already, by vp_lang_from_args above -- before anything could be
    # printed in the language this flag was about to change. Read a second time
    # here to step over the value and to refuse one that names neither
    # language, which the pre-scan cannot do: it runs before --help.
    --lang) shift; [ $# -gt 0 ] || { me arg.lang; exit 2; }
            [ -n "$(vp_lang_of "$1")" ] || { me arg.lang; exit 2; } ;;
    --lang=*) [ -n "$(vp_lang_of "${1#--lang=}")" ] || { me arg.lang; exit 2; } ;;
    --interactive) INTERACTIVE=yes ;;
    --enable) ENABLE=yes ;;
    --no-enable) ENABLE=no ;;
    --user) KIND=user ;;
    --system) KIND=system; ASKED_SYSTEM=yes ;;
    --migrate) MIGRATE=yes ;;
    --skip-tmux) DO_TMUX=no ;;
    # Off unless said, including under --yes. See the section it controls.
    --tune-claude) TUNE_CLAUDE_FLAG=yes ;;
    --no-tune-claude) TUNE_CLAUDE_FLAG=never ;;
    --username) shift; [ $# -gt 0 ] || { me arg.username; exit 2; }; ACCT_USER="$1" ;;
    --username=*) ACCT_USER="${1#--username=}" ;;
    --password-stdin) ACCT_STDIN=yes ;;
    --password-file) shift; [ $# -gt 0 ] || { me arg.pwfile; exit 2; }; ACCT_FILE="$1" ;;
    --password-file=*) ACCT_FILE="${1#--password-file=}" ;;
    --password-env) shift; [ $# -gt 0 ] || { me arg.pwenv; exit 2; }; ACCT_ENV="$1" ;;
    --password-env=*) ACCT_ENV="${1#--password-env=}" ;;
    --password|--password=*)
      # The same refusal `vibepanel account create` makes, made here as well:
      # forwarding it and letting the binary explain would mean the password
      # had already spent this whole script in `ps`.
      me arg.password
      exit 2 ;;
    -h|--help) usage; exit 0 ;;
    *) me arg.unknown "$1"; exit 2 ;;
  esac
  shift
done

# Both, not just stdin. A prompt written to a log file is a script that hangs
# forever waiting for an answer nobody can see it asking for -- and the release
# check runs this with its output redirected.
if [ "$INTERACTIVE" = auto ]; then
  if [ -t 0 ] && [ -t 1 ]; then INTERACTIVE=yes; else INTERACTIVE=no; fi
fi

# The answer comes back in $ANSWER rather than on stdout, and the prompt is
# printed rather than passed to `read -p`. Both for the same reason: `read -p`
# writes its prompt only when stdin is a terminal, so a transcript of a run
# driven from a file -- which is how scripts/install-check.sh drives it -- shows
# the questions missing and the answers taken anyway. The prompts are the part
# under test.
ANSWER=
ask() { # ask <prompt> <default>
  local def="$2"
  printf '%s' "$1"
  read -r ANSWER || ANSWER=
  [ -n "$ANSWER" ] || ANSWER="$def"
  # A terminal has already echoed what was typed; anything else has not.
  [ -t 0 ] || printf '%s\n' "$ANSWER"
}
yesno() { # yesno <question> <y|n, the default>
  local def="$2" hint="[y/N]"
  [ "$def" = y ] && hint="[Y/n]"
  ask "$1 $hint " "$def"
  case "$ANSWER" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

# ── the language, when neither the flag nor the environment said ──────────
#
# First, and that is the whole point of where it is. A language chosen after
# three questions have been answered in English is a language chosen too late,
# so this sits above the tmux offer, above the service menu and above the plan
# -- there is nothing before it but argument parsing.
#
# Never when --password-stdin is in play. The prompts and the password cannot
# both have stdin; that is refused further down, but the refusal is two hundred
# lines away and this question would get there first -- reading the first line
# of the password and, because stdin is not a terminal, echoing it into the log.
# Asked whenever there is somebody to ask, and the locale only picks which
# answer enter takes.
#
# It used to be skipped entirely when LC_ALL/LC_MESSAGES/LANG named a language
# this speaks, on the reasoning that the person had already said. They had not:
# `LANG=en_US.UTF-8` is what a server image ships with, and it is set on
# machines whose owner would rather read Chinese. The result was an installer
# that never offered, on the machines where offering matters. A flag is
# different -- `--lang zh` is somebody saying it about this run -- so that one
# still skips the question.
if [ "$VP_LANG_FROM_FLAG" = no ] && [ "$INTERACTIVE" = yes ] && [ "$ACCT_STDIN" = no ]; then
  m lang.ask
  # The default is what the locale said, so enter on a zh_CN machine keeps
  # Chinese and enter anywhere else keeps English.
  lang_default=1
  if [ "$VP_LANG" = zh ]; then lang_default=2; fi
  ask "$(m lang.prompt "$lang_default")" "$lang_default"
  case "$ANSWER" in
    2) VP_LANG=zh ;;
    *) VP_LANG=en ;;
  esac
  VP_LANG_DECIDED=yes
  echo
fi

if [ ! -f "$BIN_SRC" ]; then
  me pre.nobinary
  exit 1
fi

# ── can we become root? ───────────────────────────────────────────────────
#
# Asked before tmux, because the answer decides whether installing tmux is even
# on the table.
#
# Three ways, in descending order of how little they inconvenience anybody, and
# one thing that is not a way: sudo exists, but every route to it would block on
# a password prompt nobody is watching. That last case has to count as no root,
# because an installer that hangs in a CI job is worse than one that installs
# the user unit.
ROOT_CMD=()
ROOT_HOW=none
if [ "${VIBEPANEL_ROOT_CMD+set}" = set ]; then
  case "$VIBEPANEL_ROOT_CMD" in
    none) ROOT_HOW=none ;;
    '')   ROOT_HOW=root ;;
    # Word-split deliberately: the override is a command line, not a filename.
    *)    ROOT_HOW=override; read -r -a ROOT_CMD <<<"$VIBEPANEL_ROOT_CMD" ;;
  esac
elif [ "$(id -u)" = 0 ]; then
  ROOT_HOW=root
elif ! command -v "$SUDO" >/dev/null; then
  ROOT_HOW=none
elif "$SUDO" -n true 2>/dev/null; then
  ROOT_HOW=sudo; ROOT_CMD=("$SUDO")
elif [ "$INTERACTIVE" = yes ] && [ -t 0 ]; then
  ROOT_HOW=sudo-ask; ROOT_CMD=("$SUDO")
fi
HAVE_ROOT=no
[ "$ROOT_HOW" = none ] || HAVE_ROOT=yes

as_root() {
  if [ "${#ROOT_CMD[@]}" -eq 0 ]; then "$@"; else "${ROOT_CMD[@]}" "$@"; fi
}

# Did it actually come up?
#
# `systemctl enable --now` returns 0 when systemd has accepted the job, not
# when the process is still alive a second later. A panel whose port is taken
# binds, fails, and is restarted every three seconds -- `Restart=always` --
# and the installer printed "── done ──  started" over the top of it. That is
# what was reported: eighteen restarts, and a summary telling the person to go
# and fetch a token from a service that was not running.
#
# Polled rather than slept once, because a clean start is instant and only a
# failing one needs the seconds.
FAILED_WHY=
svc_settled() { # svc_settled <user|system>
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if [ "$1" = system ]; then
      sctl_sys is-active --quiet vibepanel 2>/dev/null && return 0
    else
      sctl_user is-active --quiet vibepanel 2>/dev/null && return 0
    fi
    sleep 0.5
  done
  # The reason, from the unit's own log, because "it did not start" without it
  # sends somebody to a journal command they have to be told twice to run.
  if [ "$1" = system ]; then
    FAILED_WHY="$(as_root journalctl -u vibepanel -n 20 --no-pager 2>/dev/null \
      | grep -iE "bind|permission|no such file|exec format|fatal|error" | tail -3 || true)"
  else
    FAILED_WHY="$(journalctl --user -u vibepanel -n 20 --no-pager 2>/dev/null \
      | grep -iE "bind|permission|no such file|exec format|fatal|error" | tail -3 || true)"
  fi
  return 1
}

# ── tmux: present, and new enough? ────────────────────────────────────────
#
# The one prerequisite, and finding out later means finding out from a panel
# that starts and then cannot create a session.

# tmux reports "3.4", "3.3a" for a patch release and "next-3.6" for a
# development build, so a split on "." is not enough: the letters and any
# prefix have to be dropped rather than parsed. This mirrors
# tmux.ParseVersion, including its last rule -- an unparseable version counts
# as new enough, because refusing to run over a version string that looked
# unfamiliar would be worse than the problem.
tmux_at_least_min() { # tmux_at_least_min <version string>
  local v="$1" maj min
  maj="$(printf '%s' "$v" | sed -e 's/^[^0-9]*//' -e 's/[^0-9].*$//')"
  min="$(printf '%s' "$v" | sed -e 's/^[^0-9]*//' -e 's/^[0-9]*//' -e 's/^\.//' -e 's/[^0-9].*$//')"
  if [ -z "$maj" ] || [ -z "$min" ]; then return 0; fi
  if [ "$maj" -gt "$TMUX_MIN_MAJOR" ]; then return 0; fi
  if [ "$maj" -eq "$TMUX_MIN_MAJOR" ] && [ "$min" -ge "$TMUX_MIN_MINOR" ]; then return 0; fi
  return 1
}

# Which package manager, and what it would be asked to do. Set together,
# because "apt-get is here" and "apt-get install -y tmux" are one fact.
PKG=
PKG_ARGS=()
PKG_NEEDS_ROOT=yes
detect_pkg() {
  local candidates
  # Native first on Linux, brew first on macOS. A Linux machine with linuxbrew
  # still wants its own package manager: brew's tmux would shadow the distro's
  # and be the version nobody expected.
  if [ "$PLATFORM" = darwin ]; then candidates="brew port"
  else candidates="apt-get dnf pacman zypper apk brew"; fi
  if [ -n "${VIBEPANEL_PKG_MANAGER:-}" ]; then
    PKG="$VIBEPANEL_PKG_MANAGER"
  else
    local c
    for c in $candidates; do
      if command -v "$c" >/dev/null 2>&1; then PKG="$c"; break; fi
    done
  fi
  PKG_NEEDS_ROOT=yes
  case "$PKG" in
    apt-get) PKG_ARGS=(apt-get install -y tmux) ;;
    dnf)     PKG_ARGS=(dnf install -y tmux) ;;
    pacman)  PKG_ARGS=(pacman -S --noconfirm tmux) ;;
    zypper)  PKG_ARGS=(zypper --non-interactive install tmux) ;;
    apk)     PKG_ARGS=(apk add tmux) ;;
    # Homebrew refuses to run as root and has done for years. Running it under
    # sudo anyway is how people end up with a prefix their own account cannot
    # write to, which then breaks every later brew command and not just this
    # one -- so this is not a preference, it is the only way that works.
    brew)    PKG_ARGS=(brew install tmux); PKG_NEEDS_ROOT=no ;;
    port)    PKG_ARGS=(port install tmux) ;;
    *)       PKG=; PKG_ARGS=() ;;
  esac
}

# The line a person would type, so it can be printed when we cannot run it.
pkg_command_line() {
  local prefix=""
  [ "$PKG_NEEDS_ROOT" = yes ] && prefix="sudo "
  printf '%s%s' "$prefix" "${PKG_ARGS[*]}"
}

run_pkg() {
  if [ "$PKG_NEEDS_ROOT" = no ]; then
    if [ -n "$PKG_RUNNER" ]; then "$PKG_RUNNER" "${PKG_ARGS[@]}"; else "${PKG_ARGS[@]}"; fi
  elif [ -n "$PKG_RUNNER" ]; then
    # The root command is passed through rather than dropped, so a recorder can
    # see whether the package install would have been privileged -- which is
    # the entire difference between the brew branch and the other five.
    if [ "${#ROOT_CMD[@]}" -eq 0 ]; then "$PKG_RUNNER" "${PKG_ARGS[@]}"
    else "$PKG_RUNNER" "${ROOT_CMD[@]}" "${PKG_ARGS[@]}"; fi
  else
    as_root "${PKG_ARGS[@]}"
  fi
}

# `|| true`, and not because the failure is uninteresting: under `set -e` and
# pipefail, probing a tmux that is not there aborts the installer with exit 127
# and no message at all -- which is exactly the state this function exists to
# detect and report. An empty string is the answer, not a crash.
tmux_version_of() {
  { "$TMUX_BIN" -V 2>/dev/null || true; } | sed -e 's/^tmux //' | tr -d '[:space:]'
}

TMUX_STATE=ok      # ok | missing | old
TMUX_VER=
if [ "$DO_TMUX" = no ]; then
  TMUX_STATE=skipped
elif ! command -v "$TMUX_BIN" >/dev/null 2>&1; then
  TMUX_STATE=missing
else
  TMUX_VER="$(tmux_version_of)"
  tmux_at_least_min "$TMUX_VER" || TMUX_STATE=old
fi

BANNER_DONE=no
banner() { [ "$BANNER_DONE" = yes ] && return 0; m banner; echo; BANNER_DONE=yes; }

if [ "$TMUX_STATE" != ok ] && [ "$TMUX_STATE" != skipped ]; then
  detect_pkg
  banner
  if [ "$TMUX_STATE" = missing ]; then
    m tmux.missing
  else
    # Not fatal, deliberately, and for the same reason `vibepanel doctor` marks
    # it "--" rather than FAIL: the panel works, one thing about it is worse,
    # and refusing to install over that would be the installer making a
    # judgement that is not its to make.
    m tmux.old "$TMUX_VER" "$TMUX_MIN_MAJOR.$TMUX_MIN_MINOR"
  fi
  echo

  WANT_TMUX=no
  if [ -z "$PKG" ]; then
    m tmux.nopkg
  elif [ "$PKG_NEEDS_ROOT" = yes ] && [ "$HAVE_ROOT" = no ]; then
    m tmux.noroot "$(pkg_command_line)"
  elif [ "$INTERACTIVE" = yes ]; then
    if [ "$TMUX_STATE" = missing ]; then
      yesno "$(m tmux.offer.install "$(pkg_command_line)")" y && WANT_TMUX=yes
    else
      yesno "$(m tmux.offer.upgrade "$(pkg_command_line)")" n && WANT_TMUX=yes
    fi
  elif [ "$TMUX_STATE" = missing ]; then
    # Unattended, and tmux is missing: install it. This is what makes the
    # one-liner true on a machine with nothing on it, which is the whole
    # promise. --skip-tmux is how a caller says otherwise.
    m tmux.autoinstall "$(pkg_command_line)"
    WANT_TMUX=yes
  else
    # Unattended and merely old: do not. The distribution's package *is* the
    # old version, so `apt-get install -y tmux` would be a no-op that reported
    # success, and an unattended run has nobody to read the difference.
    m tmux.noupgrade
  fi

  if [ "$WANT_TMUX" = yes ]; then
    if run_pkg; then
      TMUX_VER="$(tmux_version_of)"
      if [ -z "$TMUX_VER" ]; then
        m tmux.gone
        TMUX_STATE=missing
      elif tmux_at_least_min "$TMUX_VER"; then
        m tmux.installed "$TMUX_VER"
        TMUX_STATE=ok
      else
        # The likeliest outcome of the too-old branch, and the one worth saying
        # out loud: the distribution ships the version that is already here.
        m tmux.samever "$TMUX_VER" "$TMUX_MIN_MAJOR.$TMUX_MIN_MINOR"
        TMUX_STATE=old
      fi
    else
      m tmux.pkgfail "$(pkg_command_line)"
    fi
  fi

  if [ "$TMUX_STATE" = missing ]; then
    echo
    me tmux.none
    exit 1
  fi
  echo
fi

# ── the first account, if it is being created from here ───────────────────
# Counted with `if`, not `[ ... ] &&`: a false test as the whole of a statement
# is a failed command, and `set -e` ends the script there. Silently, before it
# has printed anything.
ACCT_SOURCES=0
if [ "$ACCT_STDIN" = yes ]; then ACCT_SOURCES=$((ACCT_SOURCES + 1)); fi
if [ -n "$ACCT_FILE" ]; then ACCT_SOURCES=$((ACCT_SOURCES + 1)); fi
if [ -n "$ACCT_ENV" ]; then ACCT_SOURCES=$((ACCT_SOURCES + 1)); fi
if [ "$ACCT_SOURCES" -gt 1 ]; then
  me acct.twosources
  exit 2
fi
if [ "$ACCT_SOURCES" -gt 0 ] && [ -z "$ACCT_USER" ]; then
  me acct.nouser
  exit 2
fi
if [ "$ACCT_STDIN" = yes ] && [ "$INTERACTIVE" = yes ]; then
  # They cannot both have stdin. The prompts read it line by line and
  # --password-stdin reads it to EOF, so whichever went first would consume the
  # other's input -- and the failure would be a password silently set to the
  # word "y".
  me acct.stdinclash
  exit 2
fi
if [ -n "$ACCT_FILE" ] && [ ! -r "$ACCT_FILE" ]; then
  me acct.unreadable "$ACCT_FILE"
  exit 2
fi

# ── things about this machine that are worth knowing before we touch it ───
#
# Every one of these is a real outcome somebody has had, and every one of them
# is silent in the same way: the install succeeds, and the thing they were
# trying to get does not happen.

# Is $HOME writable at all? Every path this script writes is under it, and
# `install: cannot create regular file` twelve lines in is a worse way to find
# out than one line saying so before anything has changed.
if [ ! -w "$HOME" ]; then
  me pre.homero "$HOME"
  exit 1
fi
if [ -e "$BIN_DIR" ] && [ ! -w "$BIN_DIR" ]; then
  me pre.bindirro "$BIN_DIR"
  exit 1
fi

# Is there a service manager here at all?
#
# systemctl being on PATH is not the question -- a container image can have it
# installed with no systemd running, WSL1 has neither, and several
# distributions ship other inits. /run/systemd/system is what systemd itself
# documents as the way to tell whether it is the init, and it is the check
# `systemctl is-system-running` makes underneath.
#
# VIBEPANEL_INIT_DIR is the last of the test-only overrides: without it the "no
# systemd here" branch is only reachable on a machine that is not the one any
# of this gets developed on.
INIT_DIR="${VIBEPANEL_INIT_DIR:-/run/systemd/system}"
SERVICE_MGR=yes
SERVICE_WHY=
if [ "$PLATFORM" = darwin ]; then
  if ! command -v "${LAUNCHCTL%% *}" >/dev/null 2>&1; then
    SERVICE_MGR=no; SERVICE_WHY="launchctl is not on PATH"
  fi
elif ! command -v "${SYSTEMCTL%% *}" >/dev/null 2>&1; then
  SERVICE_MGR=no; SERVICE_WHY="systemctl is not on PATH"
elif [ ! -d "$INIT_DIR" ]; then
  SERVICE_MGR=no
  SERVICE_WHY="systemd is not running this machine (no $INIT_DIR) -- a container, WSL1, or another init"
fi

# The systemd *user* manager needs a session bus, and XDG_RUNTIME_DIR is how
# anything finds it. Unset means `systemctl --user` cannot work from this
# shell, which is the state of a bare ssh command and of every cron job. The
# panel's own restart button tells the same two cases apart the same way, in
# internal/httpapi/update.go.
USER_BUS=yes
if [ "$PLATFORM" = linux ] && [ -z "${XDG_RUNTIME_DIR:-}" ] && [ "$(id -u)" != 0 ]; then
  USER_BUS=no
fi

# What is already here, and is it the same thing?
#
# "I ran the installer and nothing changed" is usually true, and usually means
# the same version was reinstalled. Which of the three this is costs one line
# to say and answers the question before it gets asked.
OLD_VER=
if [ -x "$BIN_DIR/vibepanel" ]; then
  OLD_VER="$("$BIN_DIR/vibepanel" version 2>/dev/null | head -1 || true)"
fi
NEW_VER="$("$BIN_SRC" version 2>/dev/null | head -1 || true)"

# A unit file at our path that we did not write.
#
# Somebody who hand-rolled a unit, a distribution package, an older layout.
# Overwriting it without asking is the one thing an installer must not do to a
# file it did not create, and the Documentation= line every unit and plist in
# this repository carries is what tells them apart.
OURS="github.com/jiangmuran/vibepanel"
foreign() { # foreign <path>
  if [ ! -f "$1" ]; then return 1; fi
  if grep -q "$OURS" "$1"; then return 1; fi
  return 0
}

# Is the port the panel is about to want already answering?
#
# bash's own /dev/tcp rather than ss, netstat or lsof: each of those is missing
# from some minimal image, and none is worth a dependency for one probe.
# The connect happens in a subshell, which is also what closes the descriptor
# again when it exits. An `exec 3>&- 2>/dev/null` in the parent to tidy up is
# the trap here: redirections on a bare `exec` apply to the shell itself for
# the rest of its life, so that line silently sent every later error message --
# including the one about a binary that will not run -- to /dev/null.
port_in_use() { # port_in_use <port>
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

HAVE_USER_UNIT=no; [ -f "$USER_UNIT" ] && HAVE_USER_UNIT=yes
HAVE_SYSTEM_UNIT=no; [ -f "$SYSTEM_UNIT" ] && HAVE_SYSTEM_UNIT=yes
HAVE_AGENT=no; [ -f "$PLIST" ] && HAVE_AGENT=yes

# ── what are we installing? ───────────────────────────────────────────────
banner
if [ "$PLATFORM" = darwin ]; then
  if [ "$HAVE_AGENT" = yes ]; then m found.agent "$PLIST"; fi
else
  if [ "$HAVE_USER_UNIT" = yes ]; then m found.user "$USER_UNIT"; fi
  if [ "$HAVE_SYSTEM_UNIT" = yes ]; then m found.system "$SYSTEM_UNIT"; fi
fi

if [ "$PLATFORM" = darwin ]; then
  # One kind, so there is nothing to ask and nothing to conflict with. --system
  # is answered further down rather than silently, because somebody who typed
  # it is expecting something this cannot give them.
  KIND=agent
elif [ -z "$KIND" ]; then
  # An existing install of either kind is the answer to the question, so the
  # question is not asked: an upgrade that offers to change the unit kind is an
  # upgrade that changes it for whoever pressed return without reading.
  if [ "$HAVE_SYSTEM_UNIT" = yes ]; then
    KIND=system
  elif [ "$HAVE_USER_UNIT" = yes ]; then
    KIND=user
  elif [ "$HAVE_ROOT" = yes ] && [ -f "$SYSTEM_UNIT_SRC" ]; then
    # The recommended default wherever root is available. It is the same panel
    # running as the same account with the same environment; what it adds is
    # being up before anyone logs in without lingering, and being able to lower
    # its OOM score at all -- measured, a user unit asking for -500 gets 100.
    KIND=system
    if [ "$INTERACTIVE" = yes ]; then
      m kind.menu "$WHO"
      # Said where the choice is made, not two screens later in the plan.
      #
      # The menu's "root is available here" covers two different situations:
      # sudo that answers without asking, and sudo that will stop and want a
      # password. Only one of those is worth knowing before picking, and it was
      # only mentioned afterwards -- so somebody who would happily have typed
      # their password read "recommended" and had no idea what it would cost,
      # and somebody who did not want to be asked found out after choosing.
      if [ "$ROOT_HOW" = sudo-ask ]; then
        m kind.sudoask
      fi
      ask "$(m choice.prompt)" 1
      echo
      case "$ANSWER" in
        2) KIND=user ;;
        *) KIND=system ;;
      esac
    fi
    # Ask for the password now, while there is still a decision to change.
    #
    # `sudo -n true` fails for two different reasons and this script could not
    # tell them apart: sudo that wants a password, and an account that is not
    # in sudoers at all. The second one only surfaced when the first `as_root`
    # ran -- after the binary was on disk -- and took the install down with it.
    #
    # `sudo -v` asks once and answers both questions. It also caches the
    # credential, so the writes further down do not each stop again.
    if [ "$KIND" = system ] && [ "$ROOT_HOW" = sudo-ask ]; then
      if ! "$SUDO" -v; then
        echo
        m kind.nosudo "$WHO"
        echo
        KIND=user
        HAVE_ROOT=no
        ROOT_HOW=none
        ROOT_CMD=()
      fi
    fi
  else
    KIND=user
    # Said out loud, and here rather than in the summary. The recommended
    # default is the system service; choosing the other one without a word
    # reads as the installer having a different opinion than the README, and
    # the actual reason -- no root here -- is one the person may be able to fix.
    if [ -f "$SYSTEM_UNIT_SRC" ] && [ "$HAVE_ROOT" = no ]; then
      m kind.noroot
      echo
    fi
  fi
fi

# ── where the binary goes, now that the kind is known ─────────────────────
#
# A system unit whose ExecStart points into one account's home is not a system
# service. It breaks when that home is on an encrypted or network filesystem
# that is not mounted at boot, and `sudo vibepanel` -- which is what the
# summary tells people to type, and what anybody with a system unit reaches for
# -- fails with `command not found`, because ~/.local/bin is not on root's
# PATH. That was reported from a real install, along with everything downstream
# of it.
#
# So: /usr/local/bin for the system unit, ~/.local/bin for the user unit. The
# user unit's ExecStart is `%h/.local/bin/vibepanel`, which systemd expands per
# account and is right as it is.
if [ "$KIND" = system ]; then
  BIN_DIR="$DESTDIR/usr/local/bin"
fi

# The writability check again, for the directory actually chosen. The earlier
# one ran before the kind was known and could only ask about $HOME.
if [ -e "$BIN_DIR" ] && [ ! -w "$BIN_DIR" ] && [ "$KIND" != system ]; then
  me pre.bindirro "$BIN_DIR"
  exit 1
fi

# ── never both ────────────────────────────────────────────────────────────
#
# A user unit and a system unit are two panels with the same data directory on
# the same tmux socket. They do not conflict loudly; they take turns, and the
# symptom is a panel that forgets things.
CONFLICT=no
if [ "$KIND" = system ] && [ "$HAVE_USER_UNIT" = yes ]; then CONFLICT=user; fi
if [ "$KIND" = user ] && [ "$HAVE_SYSTEM_UNIT" = yes ]; then CONFLICT=system; fi

if [ "$CONFLICT" != no ] && [ "$MIGRATE" != yes ]; then
  echo
  m conflict.head "$CONFLICT" "$KIND"
  if [ "$INTERACTIVE" = yes ] && yesno "$(m conflict.ask "$CONFLICT" "$KIND")" n; then
    MIGRATE=yes
  else
    echo
    m conflict.stop "$0 --$KIND --migrate"
    exit 3
  fi
fi

# Removing a system unit needs root; refusing here beats getting halfway.
if [ "$MIGRATE" = yes ] && [ "$CONFLICT" = system ] && [ "$HAVE_ROOT" = no ]; then
  me conflict.needroot "$SYSTEM_UNIT"
  exit 3
fi

# ── the fallbacks, said plainly ───────────────────────────────────────────
FELL_BACK=no
# Answered rather than ignored. Somebody who typed --system is expecting
# something macOS cannot give them, and silently installing a different thing
# is how an installer gets a reputation for lying.
if [ "$PLATFORM" = darwin ] && [ "$ASKED_SYSTEM" = yes ]; then
  echo
  m fallback.darwin
  FELL_BACK=yes
fi
if [ "$KIND" = system ] && [ "$HAVE_ROOT" = no ]; then
  echo
  m fallback.noroot
  KIND=user
  FELL_BACK=yes
fi
if [ "$KIND" = system ] && [ ! -f "$SYSTEM_UNIT_SRC" ]; then
  echo
  m fallback.nosrc
  KIND=user
  FELL_BACK=yes
fi
# No service manager at all: install the binary and say so. Refusing would be
# wrong -- the panel runs perfectly well from a shell, and a container or a WSL1
# machine is a place people genuinely use it -- but pretending a service was
# installed would be worse than either.
if [ "$SERVICE_MGR" = no ]; then
  echo
  m fallback.nosvc "$SERVICE_WHY" "$BIN_DIR/vibepanel"
  KIND=none
  FELL_BACK=yes
fi
if [ "$KIND" = agent ] && [ ! -f "$PLIST_SRC" ]; then
  me err.noplist "$MAC_LABEL"
  exit 1
fi

# ── a file at our path that we did not write ──────────────────────────────
TARGET_FILE=
case "$KIND" in
  user) TARGET_FILE="$USER_UNIT" ;;
  system) TARGET_FILE="$SYSTEM_UNIT" ;;
  agent) TARGET_FILE="$PLIST" ;;
esac
if [ -n "$TARGET_FILE" ] && foreign "$TARGET_FILE"; then
  echo
  m foreign.head "$TARGET_FILE"
  if [ "$INTERACTIVE" = yes ] && yesno "$(m foreign.ask)" n; then
    m foreign.replacing
  else
    echo
    m foreign.stop "$TARGET_FILE"
    exit 3
  fi
fi

# ── start it? ─────────────────────────────────────────────────────────────
#
# Whether the unit is already running decides the wording and, when nobody was
# asked, the default: an upgrade that leaves the old binary running is the
# failure docs/runbook.md has an entry for, so a running unit is restarted
# without being asked. A stopped one is not started behind your back.
sctl_user() { "$SYSTEMCTL" --user "$@"; }
sctl_sys() { as_root "$SYSTEMCTL" "$@"; }
lctl() { "$LAUNCHCTL" "$@"; }
GUI="gui/$(id -u)"

# Asked only when a unit of that kind is already installed. `systemctl --user`
# talks to the manager for the *logged-in* user, which read its own $HOME at
# login and does not care what this script was handed -- so on a first install
# into a different HOME (which is exactly what scripts/release-check.sh does)
# "vibepanel is active" can be true and be somebody else's panel. Restarting
# that would be this installer reaching outside the tree it was pointed at.
RUNNING=no
if [ "$KIND" = agent ]; then
  if [ "$HAVE_AGENT" = yes ] && command -v "${LAUNCHCTL%% *}" >/dev/null 2>&1; then
    lctl print "$GUI/$MAC_LABEL" >/dev/null 2>&1 && RUNNING=yes
  fi
elif command -v "${SYSTEMCTL%% *}" >/dev/null 2>&1; then
  if [ "$KIND" = user ] && [ "$HAVE_USER_UNIT" = yes ]; then
    sctl_user is-active --quiet vibepanel 2>/dev/null && RUNNING=yes
  elif [ "$KIND" = system ] && [ "$HAVE_SYSTEM_UNIT" = yes ]; then
    sctl_sys is-active --quiet vibepanel 2>/dev/null && RUNNING=yes
  fi
fi

if [ "$ENABLE" = auto ]; then
  if [ "$RUNNING" = yes ]; then
    ENABLE=yes    # a restart, and it is not optional -- see above
  elif [ "$INTERACTIVE" = yes ]; then
    if yesno "$(m enable.ask)" y; then ENABLE=yes; else ENABLE=no; fi
  else
    ENABLE=no
  fi
fi

# ── which port, and is anything on it? ────────────────────────────────────
#
# Read before the plan rather than after the install, because "the port is
# taken" is a thing to be told while there is still a decision to make. The
# panel would start, fail to bind, and be restarted by systemd every three
# seconds -- a unit that looks active in `systemctl status` for the first two
# seconds of each cycle.
# The same number as internal/config.DefaultPort, checked against it by
# TestTheInstallerAgreesAboutThePort. This is only the fallback: an env file
# that names a port wins, which is the line below.
PORT=18443
if [ -e "$ENV_FILE" ]; then
  ADDR="$(grep -E '^[[:space:]]*VIBEPANEL_ADDR=' "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' "' || true)"
  case "$ADDR" in *:*) P="${ADDR##*:}"; if [ -n "$P" ]; then PORT="$P"; fi ;; esac
fi
HOST="$(hostname 2>/dev/null || echo localhost)"

PORT_TAKEN=no
# Not while a panel of ours is already running: that is the upgrade case, the
# thing on the port is the panel being replaced, and warning about it would be
# the installer reporting itself as a conflict.
if [ "$RUNNING" = no ] && port_in_use "$PORT"; then
  PORT_TAKEN=yes
fi

# ── Claude Code, if it is here ────────────────────────────────────────────
#
# The panel already offers to write this person's ~/.claude/settings.json --
# that is what the hooks installer does, and it is how the panel learns what an
# agent is doing. This asks about the rest of the file: a handful of settings
# that decide what leaves the machine and what ends up in their git history.
#
# Asked here, above the plan, for the same reason tmux is: the plan is the last
# screen before anything is touched and everything on it has to be decided by
# then.
#
# Off by default at every non-interactive call site. This edits a file that
# belongs to another tool, and a `--yes` run -- CI, a reinstall script, the
# release check -- has nobody reading the summary. `--tune-claude` turns it on
# for those.
TUNE_CLAUDE=no
CLAUDE_BIN="${VIBEPANEL_CLAUDE_BIN:-$(command -v claude 2>/dev/null || true)}"
# An `if`, not `[ ... ] && VAR=`: under `set -e` the second form exits the
# script whenever the test is false, which is the common case.
if [ "$CLAUDE_BIN" = none ]; then CLAUDE_BIN=; fi
if [ "$TUNE_CLAUDE_FLAG" = never ]; then
  TUNE_CLAUDE=no
elif [ "$TUNE_CLAUDE_FLAG" = yes ]; then
  TUNE_CLAUDE=yes
elif [ -n "$CLAUDE_BIN" ] && [ "$INTERACTIVE" = yes ]; then
  echo
  m claude.found "$CLAUDE_BIN"
  m claude.what
  echo
  # The list is the binary's, not this script's, and it is printed before the
  # question rather than described by it. Seven keys paraphrased in a sentence
  # is not something anybody can say yes to, and a paraphrase kept in this file
  # would drift from the keys it names -- which is the failure that would
  # matter here, because the summary is the whole consent.
  # If the list cannot be produced, the question is not asked. A yes to
  # "apply these?" with nothing above it saying which is consent to a blank
  # page, and the reasons this fails -- an older binary with no `tune`, a
  # settings.json that is not valid JSON -- are all reasons not to write to
  # the file anyway.
  if "$BIN_SRC" tune claude --lang "$VP_LANG" --asking; then
    echo
    if yesno "$(m claude.ask)" n; then
      TUNE_CLAUDE=yes
    fi
  else
    m claude.cannot
  fi
fi

# ── the plan, before anything happens ─────────────────────────────────────
echo
m plan.head
# Which of the three this is. "I ran it and nothing happened" is nearly always
# a reinstall of the same build, and saying so costs a line.
if [ -z "$OLD_VER" ]; then
  m plan.bin.install "$BIN_DIR/vibepanel" "$NEW_VER"
elif [ "$OLD_VER" = "$NEW_VER" ]; then
  m plan.bin.same "$BIN_DIR/vibepanel" "$NEW_VER"
else
  m plan.bin.upgrade "$BIN_DIR/vibepanel" "$OLD_VER" "$NEW_VER"
fi
case "$KIND" in
  user)   m plan.unit.user "$USER_UNIT" ;;
  system) m plan.unit.system "$SYSTEM_UNIT" "$WHO" "$HOME" ;;
  agent)  m plan.unit.agent "$PLIST" "$HOME" ;;
  none)   m plan.unit.none "$SERVICE_WHY" ;;
esac
if [ -e "$ENV_FILE" ]; then
  m plan.env.keep "$ENV_FILE"
else
  m plan.env.install "$ENV_FILE"
fi
if [ "$CONFLICT" != no ] && [ "$MIGRATE" = yes ]; then
  m plan.remove "$CONFLICT"
fi
if [ "$KIND" = user ]; then
  m plan.linger "$WHO"
fi
if [ "$ENABLE" = yes ]; then
  if [ "$RUNNING" = yes ]; then
    m plan.restart
  else
    m plan.start
  fi
fi
if [ -n "$ACCT_USER" ]; then
  m plan.account "$ACCT_USER"
fi
if [ "$TUNE_CLAUDE" = yes ]; then
  m plan.claude "$HOME/.claude/settings.json"
fi
if [ "$PORT_TAKEN" = yes ]; then
  echo
  m plan.port "$PORT" "$ENV_FILE"
fi
if [ "$ROOT_HOW" = sudo-ask ] && [ "$KIND" = system ]; then
  echo
  m plan.sudo
fi
echo

if [ "$INTERACTIVE" = yes ]; then
  if ! yesno "$(m plan.proceed)" y; then m plan.nochange; exit 0; fi
  echo
fi

# ── do it ─────────────────────────────────────────────────────────────────
mkdir -p "$(dirname "$ENV_FILE")"
# Through as_root for the system directory, which is the whole reason the two
# are separate calls: /usr/local/bin is root's and ~/.local/bin is not.
if [ "$KIND" = system ]; then
  as_root mkdir -p "$BIN_DIR"
  as_root install -m 0755 "$BIN_SRC" "$BIN_DIR/vibepanel"
else
  mkdir -p "$BIN_DIR"
  install -m 0755 "$BIN_SRC" "$BIN_DIR/vibepanel"
fi
echo "installed $BIN_DIR/vibepanel"

# Does the file that was just written actually run *here*?
#
# Three different things make this fail and they are indistinguishable
# afterwards: $HOME on a filesystem mounted noexec (containers, hardened
# machines, some NFS), an SELinux or AppArmor policy that will not execute a
# file with that label, and an archive for the wrong architecture. All three
# install perfectly, and all three surface later as a service that restarts
# every three seconds with `Exec format error` or `Permission denied` in a
# journal nobody has been told to read yet.
#
# Checked by running it, not by looking at the mount table: `findmnt` is not
# everywhere, the mount options do not describe SELinux, and neither of them
# says anything about the architecture.
if ! "$BIN_DIR/vibepanel" version >/dev/null 2>&1; then
  echo >&2
  me err.exec "$BIN_DIR/vibepanel" "$HOME" "$(uname -m)"
  exit 1
fi

# Claude Code's settings, if that was the answer.
#
# Run through the installed binary rather than $BIN_SRC, so what writes the
# file is the build that is now on the machine -- the same reason `version` is
# checked above rather than trusted.
#
# `id -u` and not `$KIND`, because the question is whose home directory
# ~/.claude means. Under sudo this script is root and $HOME may be root's, and
# writing root's Claude Code settings is not what anybody answering that
# question meant. It is skipped with a line saying so, rather than guessed at:
# the person can run one command afterwards, and a wrong guess here edits a
# file nobody looked at.
if [ "$TUNE_CLAUDE" = yes ]; then
  if [ "$(id -u)" = 0 ] && [ "$WHO" != root ]; then
    m claude.asroot "$WHO"
  else
    echo
    "$BIN_DIR/vibepanel" tune claude --apply --lang "$VP_LANG" || true
    echo
  fi
fi

# Never overwrite the env file: it is the one file here the user edits, and it
# holds the domain, the TLS choice and any ACME credentials.
if [ -e "$ENV_FILE" ]; then
  echo "kept      $ENV_FILE (already there, left alone)"
else
  install -m 0600 "$SRC/vibepanel.env" "$ENV_FILE"
  echo "installed $ENV_FILE — edit it before exposing the panel"
fi

# The old install goes away before the new one arrives, so an interruption in
# between leaves nothing running rather than two things running.
if [ "$MIGRATE" = yes ] && [ "$CONFLICT" = user ]; then
  sctl_user disable --now vibepanel >/dev/null 2>&1 || true
  rm -f "$USER_UNIT"
  sctl_user daemon-reload >/dev/null 2>&1 || true
  echo "removed   $USER_UNIT (migrated to the system service)"
elif [ "$MIGRATE" = yes ] && [ "$CONFLICT" = system ]; then
  sctl_sys disable --now vibepanel >/dev/null 2>&1 || true
  as_root rm -f "$SYSTEM_UNIT"
  sctl_sys daemon-reload >/dev/null 2>&1 || true
  echo "removed   $SYSTEM_UNIT (migrated to the user service)"
fi

# ── the first account, before the service starts ──────────────────────────
#
# Before, deliberately. With an account already in the database the panel does
# not print a setup token at all, so there is never a moment where a live token
# is sitting in a journal that somebody else can read.
#
# Through the binary's own subcommand rather than by touching the database:
# argon2id, the length rules and the refusal to overwrite an existing account
# all live in one place, and `vibepanel account create` is that place.
ACCOUNT_MADE=no
if [ -n "$ACCT_USER" ]; then
  ACCT_ARGS=(account create --username "$ACCT_USER")
  if [ "$ACCT_STDIN" = yes ]; then ACCT_ARGS+=(--password-stdin); fi
  if [ -n "$ACCT_FILE" ]; then ACCT_ARGS+=(--password-file "$ACCT_FILE"); fi
  if [ -n "$ACCT_ENV" ]; then ACCT_ARGS+=(--password-env "$ACCT_ENV"); fi
  # With the env file applied, so the account lands in the same data directory
  # the service will open. Without this, an install that sets VIBEPANEL_DATA_DIR
  # creates the account in one database and serves out of another -- and the
  # panel then greets its first visitor with a setup token, as if nothing had
  # happened.
  if ( set -a
       # shellcheck disable=SC1090
       if [ -f "$ENV_FILE" ]; then . "$ENV_FILE"; fi
       set +a
       "$BIN_DIR/vibepanel" "${ACCT_ARGS[@]}" ); then
    ACCOUNT_MADE=yes
  else
    # Not fatal. The files are in place and the browser wizard still works, so
    # ending here would leave a half-installed machine over a password that can
    # be typed again in thirty seconds.
    echo
    m acct.failed
  fi
fi

STARTED=no      # no | started | restarted | failed

if [ "$KIND" = none ]; then
  : # nothing to install; the message was printed where the decision was made
elif [ "$KIND" = agent ]; then
  mkdir -p "$PLIST_DIR" "$(dirname "$MAC_LOG")"
  # Substituted into a temp file and installed, like the system unit, so a sed
  # that fails leaves no half-rewritten plist behind: launchd will happily load
  # a truncated one and then fail in a way that says nothing about why.
  TMP_PLIST="$(mktemp "${TMPDIR:-/tmp}/vibepanel-plist.XXXXXX")"
  sed -e "s#__HOME__#$HOME#g" "$PLIST_SRC" > "$TMP_PLIST"
  install -m 0644 "$TMP_PLIST" "$PLIST"
  rm -f "$TMP_PLIST"
  echo "installed $PLIST"

  if command -v "${LAUNCHCTL%% *}" >/dev/null 2>&1; then
    if [ "$ENABLE" = yes ]; then
      if [ "$RUNNING" = yes ]; then
        # kickstart -k, not bootout+bootstrap. The pair has a window in which
        # the job is unloaded, and if anything goes wrong between them the
        # panel is simply gone; -k restarts in place.
        lctl kickstart -k "$GUI/$MAC_LABEL"
        STARTED=restarted
      else
        lctl bootstrap "$GUI" "$PLIST"
        STARTED=started
      fi
    fi
  else
    echo
    m mac.nolaunchctl "$GUI" "$PLIST"
  fi
  # The macOS equivalent of the lingering paragraph, and the one real gap
  # against the Linux user unit.
  m mac.note
elif [ "$KIND" = user ]; then
  mkdir -p "$USER_UNIT_DIR"
  install -m 0644 "$SRC/vibepanel.service" "$USER_UNIT"
  echo "installed $USER_UNIT"

  # Without lingering a user service stops when the last session for that user
  # ends. For a panel whose entire purpose is outliving your terminal, that is
  # the difference between working and appearing to work until you log out.
  #
  # Done rather than suggested. "Start at boot" is the thing people install a
  # service for, and a printed command is a step that gets skipped -- after
  # which the panel works perfectly until the first reboot, which is the worst
  # moment to find out. It needs no root for your own account (measured), and
  # the line below says what happened so it is not a change made behind your
  # back.
  if command -v loginctl >/dev/null; then
    if [ "$(loginctl show-user "$WHO" -p Linger --value 2>/dev/null || echo no)" = yes ]; then
      m linger.on
    elif loginctl enable-linger "$WHO" 2>/dev/null; then
      m linger.enabled "$WHO"
    else
      echo
      m linger.failed "$WHO"
    fi
  fi

  if command -v "${SYSTEMCTL%% *}" >/dev/null && sctl_user daemon-reload 2>/dev/null; then
    if [ "$ENABLE" = yes ]; then
      # `enable --now` is a no-op on a unit that is already enabled and active,
      # which is exactly the state an upgrade finds. The new binary went to disk
      # a few lines above and the old one kept serving, while this printed
      # "started. the one-time setup token is in: journalctl ..." -- a start
      # that did not happen, and a token consumed at first install.
      #
      # `restart` is the whole fix, and this is the one project where it is
      # free: the tmux server outlives the Go process, so every session
      # survives, which is what restart-check exists to prove.
      if [ "$RUNNING" = yes ]; then
        sctl_user restart vibepanel
        STARTED=restarted
      else
        sctl_user enable --now vibepanel
        STARTED=started
      fi
      svc_settled user || STARTED=failed
    fi
  else
    echo
    m user.nosession
  fi
else
  # __USER__/__HOME__ are substituted here rather than with `sudo sed -i` on the
  # installed copy, so the only privileged write is a single `install`. A sed
  # that fails then leaves no half-rewritten unit under /etc.
  TMP_UNIT="$(mktemp "${TMPDIR:-/tmp}/vibepanel-unit.XXXXXX")"
  sed -e "s/__USER__/$WHO/g" -e "s#__HOME__#$HOME#g" \
      -e "s#__BIN__#$BIN_DIR/vibepanel#g" "$SYSTEM_UNIT_SRC" > "$TMP_UNIT"
  as_root mkdir -p "$(dirname "$SYSTEM_UNIT")"
  as_root install -m 0644 "$TMP_UNIT" "$SYSTEM_UNIT"
  rm -f "$TMP_UNIT"
  echo "installed $SYSTEM_UNIT"

  sctl_sys daemon-reload
  if [ "$ENABLE" = yes ]; then
    if [ "$RUNNING" = yes ]; then
      sctl_sys restart vibepanel
      STARTED=restarted
    else
      sctl_sys enable --now vibepanel
      STARTED=started
    fi
    svc_settled system || STARTED=failed
  fi
  m sys.note
fi

# ── what actually happened ────────────────────────────────────────────────
#
# Every line here is a fact about this run, not a description of the script.
# "started" and "restarted" are different facts and the setup token only exists
# for one of them.
case "$KIND" in
  user)
    JOURNAL="journalctl --user -u vibepanel -n 30"
    WHAT="$(m what.user "$USER_UNIT")"
    ;;
  system)
    JOURNAL="sudo journalctl -u vibepanel -n 30"
    WHAT="$(m what.system "$SYSTEM_UNIT")"
    ;;
  agent)
    JOURNAL="tail -n 30 $MAC_LOG"
    WHAT="$(m what.agent "$PLIST")"
    ;;
  none)
    JOURNAL="whatever your init writes"
    WHAT="$(m what.none "$SERVICE_WHY")"
    ;;
esac
# One command for all three, which is the point of it existing. The raw one is
# printed alongside for the person who wants to know what it does.
VPCTL="$BIN_DIR/vibepanel service"

echo
m done.rule
m done.installed "$WHAT"
if [ "$ACCOUNT_MADE" = yes ]; then
  # Said once, here, and nowhere in the branches below: with an account in
  # place there is no token, and every line about finding one would send
  # somebody looking for something that was never printed.
  m done.account "$ACCT_USER"
fi
case "$STARTED" in
  started)
    m state.started
    echo
    if [ "$ACCOUNT_MADE" = yes ]; then
      {
        m box.open "http://$HOST:$PORT"
        m box.login "$ACCT_USER"
      } | vp_box
    else
      # Fetched, not described.
      #
      # This used to print the command that prints the token, so the first
      # thing anybody did after a successful install was run a second command
      # and read a wall of log. The service is up by the time we are here and
      # `service token` is exactly that command, so the installer runs it.
      #
      # Best effort on purpose: a journal that has not flushed, a system with
      # no journal at all, or a sudo that asks again all end with an empty
      # string, and the box then says where to get it instead. An installer
      # that fails at the last line over a nicety is worse than one that
      # prints a command.
      TOKEN="$("$BIN_DIR/vibepanel" service token 2>/dev/null | tr -d '[:space:]')"
      if [ -n "$TOKEN" ]; then
        {
          m box.open "http://$HOST:$PORT"
          m box.token "$TOKEN"
        } | vp_box
      else
        {
          m box.open "http://$HOST:$PORT"
          m box.tokencmd "$VPCTL"
        } | vp_box
      fi
      # Below the box, not in it. The box holds the two things to do; this is
      # what that command runs underneath, which matters when it does not work
      # -- a system unit whose journal needs sudo, or a machine with no journal
      # at all. Keeping it inside would put a second command in a rectangle
      # whose whole point is that there are two lines in it.
      echo
      m token.cmd "$VPCTL" "$JOURNAL"
    fi
    ;;
  failed)
    m state.failed
    if [ -n "$FAILED_WHY" ]; then
      echo
      printf '%s\n' "$FAILED_WHY" | sed 's/^/  /'
    fi
    echo
    if printf '%s' "$FAILED_WHY" | grep -qi "address already in use"; then
      m failed.port "$PORT" "$ENV_FILE"
    else
      m failed.look "$JOURNAL"
    fi
    echo
    m failed.after "$VPCTL"
    ;;
  restarted)
    m state.restarted
    echo
    m restarted.token "$VPCTL" "$JOURNAL"
    echo
    m restarted.url "http://$HOST:$PORT"
    ;;
  *)
    if [ "$KIND" = none ]; then
      m state.nonestarted
      echo
      m notstarted.serve "$BIN_DIR/vibepanel"
    else
      m state.notstarted
      echo
      m notstarted.cmd "$VPCTL"
      if [ "$ACCOUNT_MADE" != yes ]; then
        m notstarted.token "$VPCTL"
      fi
    fi
    echo
    m token.thenopen "http://$HOST:$PORT"
    ;;
esac
echo
if [ "$KIND" = none ]; then
  m after.none "$VPCTL"
else
  m after.svc "$VPCTL"
fi

# Installed, and not typeable.
#
# ~/.local/bin is on PATH by default on a lot of distributions and on none of
# the others, and the difference is invisible until `vibepanel` says "command
# not found" about a file the last twenty lines said had been installed. The
# exact line to add, for the shell that is actually being used -- a generic
# "add it to your PATH" is the advice people skip.
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    RCFILE="~/.profile"
    case "$(basename "${SHELL:-sh}")" in
      bash) RCFILE="~/.bashrc" ;;
      zsh)  RCFILE="~/.zshrc" ;;
      fish) RCFILE="~/.config/fish/config.fish" ;;
    esac
    echo
    m note.path "$BIN_DIR" "$RCFILE"
    if [ "$RCFILE" = "~/.config/fish/config.fish" ]; then
      echo "             fish_add_path $BIN_DIR"
    else
      echo "             export PATH=\"\$HOME/.local/bin:\$PATH\""
    fi
    ;;
esac

# `systemctl --user` needs a session bus, and this shell has not got one.
if [ "$KIND" = user ] && [ "$USER_BUS" = no ]; then
  echo
  m note.xdg "$WHO" "$HOST"
fi
if [ "$TMUX_STATE" = old ]; then
  echo
  m note.tmuxold "$TMUX_VER" "$TMUX_MIN_MAJOR.$TMUX_MIN_MINOR"
fi

# The other unit, and why it is not the default here.
#
# A user service cannot lower its own oom_score_adj -- the kernel refuses
# without CAP_SYS_RESOURCE, and `systemd-analyze verify` accepts the directive
# anyway, so it is a setting that looks applied and does nothing. Measured: a
# user unit asking for -500 gets 100; a system unit with User= gets -500.
#
# Only mentioned when it was never offered. Having just declined it in a menu
# and then being told about it reads as a script that was not listening.
if [ "$KIND" = user ] && [ "$FELL_BACK" = no ] && [ "$INTERACTIVE" != yes ]; then
  echo
  m note.systemunit
fi
