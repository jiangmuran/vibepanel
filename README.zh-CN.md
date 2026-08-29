<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**一个网页控制台：同时开十几个 coding agent，一眼看出哪一个在等人。**

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![运行时依赖：只要 tmux](https://img.shields.io/badge/%E8%BF%90%E8%A1%8C%E6%97%B6%E4%BE%9D%E8%B5%96-%E5%8F%AA%E8%A6%81%20tmux-3fb950)](#安装)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

[English](README.md) · **简体中文**

</div>

![面板](docs/images/panel-zh.png)

## 这是什么

vibepanel 是一个 Go 单二进制，起一个网页。它创建的每个会话都是一个真的 tmux 会话，
里面是项目目录下的一个 shell，跑什么由人敲进去：`claude`、`codex`、一个测试循环、
一个 `tail -f`。

进程活着靠 tmux。面板管的是 tmux 完全不管的那一半：会话归到项目下，改过的名字不会被改回去，
状态一眼能读，需要人处理的排在最前面，每个项目自带文件树和笔记，手机上也是一套能用的界面。

面板从不持有会话进程的父 PTY，所以重启它、升级它、杀掉它，agent 都照常在 tmux 里跑着。

它不是 agent 的壳，不改提示词，也不代理任何 API 请求。只有一个账号，所以谈不上团队协作，
也没打算替掉 tmux。

## 适合谁

它是为这种情况写的：手上同时开着好几个 agent，跨好几个仓库，跑在一台一直开着的机器上，
可能是自己的工作站，也可能是一台用手机也会去看两眼的 VPS。

一次只开一个 agent、而且那个终端就摆在眼前的话，这些都用不上。

## 它能做什么

- **会话活得比面板久。** 进程属于 tmux server，跑在面板专用的 socket 上，
  `systemctl restart vibepanel` 不花任何代价。
- **在等人的那个排在最上面。** 会话在项目内按紧急程度排序。Claude Code、Codex 和 opencode
  可以通过面板装的 hook 直接上报状态；没装的话，面板从输出流里判断。
- **是真的终端。** xterm.js 走 WebSocket，WebGL 渲染，全屏 TUI、宽字符、回滚都在，
  不是一个日志窗口。
- **手机端是另一套外壳，不是把桌面端缩小。** 对输入法友好的命令输入框、一排软键、
  双手柄拖选，以及会话开始等人时往 Bark / ntfy / Server酱 推一条。
- **重启机器之后能回来。** 命令、目录、名字和最后一段回滚记录都留着，开机后面板会问要不要重建。
- **给别人看的屏幕。** 一个只读链接打开一块看板，别的什么都打不开；看板由组件拼出来，
  挂在墙上的同时可以在自己电脑上改。
- **启动配置。** 一条 argv 加一组环境变量，起个名字。同一个 agent 指向三个不同的 endpoint，
  是三条配置，而不是每次重敲一遍变量。
- **界面和安装脚本都有中文。**
- **一个二进制，一个依赖。** 发布版是静态的，前端、数据库和 TLS 客户端都在里面，
  外部只需要装 tmux。

## 安装

唯一的要求是 tmux 3.3 或更新，安装脚本可以顺手装上。

```sh
curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh
```

它会认出平台，下载对应的发布包，拿发布的 `SHA256SUMS` 校验，tmux 缺失或太旧就装一个，
然后装服务：Linux 上是 systemd unit，macOS 上是 launchd LaunchAgent。动手之前会把计划
列出来等人点头。只有 stdin 和 stdout 都是终端时才提问，管道里跑就走无人值守那条路。

然后打开 `http://<主机>:8443`，粘贴它打印出来的 setup token，设一个密码。

**连不上 GitHub 的话**，加 `--mirror`，每一次下载都走镜像，默认是 `github.muran.tech`。
这个镜像按 IP 授权：第一次请求会回一段带链接的文字，要在浏览器里打开；安装脚本会把它
原样打印出来并等着。

```sh
curl -fsSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh -o vibepanel-install.sh \
  || curl -sSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh
sh vibepanel-install.sh --mirror
```

两条而不是一条，关键在那个 `||`：`curl -f` 遇到 HTTP 错误会把响应体丢掉，而对着还没给
这个 IP 授权的镜像，被丢掉的那段正是需要的链接。第二条 `curl` 只在这种情况下跑，把它打出来。

两个安装脚本都会说中文。`--lang zh` 或 `--lang en` 说了算；不给就看 `LC_ALL`、
`LC_MESSAGES`、`LANG`。终端那头有人、三个又都没说的时候，第一个问题就是问语言。

装完之后，不管用哪种方式跑的，都是同一条命令：

```sh
vibepanel service status | start | stop | restart | logs | token | upgrade | uninstall
```

剩下的都在 [docs/install.md](docs/install.md)（英文）：user unit 和系统 unit 怎么选、
全部参数、无人值守安装、在命令行里建第一个账号、Docker，以及从源码构建。

## 几个要点

- **Linux，amd64 或 arm64。** 机器监控读 `/proc`，安装脚本写 systemd unit。
  `darwin/arm64` 的二进制也会构建、面板也能跑，但监控是空的，进程守护得自己来。
- **单用户。** 没有分权，没有共享。第二块屏幕用[只读链接](docs/features.zh-CN.md#给别人看的屏幕)。
- **agent 是以跑面板的那个用户的身份跑的**，用的是那个人的密钥、dotfiles 和仓库。
  进得了面板就等于拿到一个 shell。
- **Docker 一重启，会话全没。** 容器里 tmux 是 entrypoint 的子进程，`docker restart` 和
  任何重建都会把 agent 一起带走，镜像内部改不了这件事。
- **重启机器会丢进程。** 面板能把命令和回滚记录重建出来，但重建不了 agent 的上下文。
- **比 3.3 老的 tmux 能跑，但会瘸。** 3.3 这条线来自 `allow-passthrough`，
  再老的版本会吞掉 agent TUI 用来画进度条和发通知的转义序列。`vibepanel doctor` 会报出来。

## 功能

[docs/features.zh-CN.md](docs/features.zh-CN.md) 逐块讲：会话和状态是怎么判断的、
右栏、手机端、启动配置、只读看板、重启之后剩下什么，以及挂到公网上要注意的事。

## 参数与排查

每个 flag 都有对应的 `VIBEPANEL_<大写下划线>` 环境变量，flag 优先。没人读的
`VIBEPANEL_*` 会在启动时和 `doctor` 里被报出来，而不是被忽略，所以改过名的设置是吵的，
不是悄悄失效。完整的参数表在 [docs/install.md](docs/install.md)（英文）。

同一个二进制也是管理 CLI：`serve`、`project`、`session`、`hook`、`service`、
`account`、`doctor`、`version`。

## 用程序驱动它

```sh
TOKEN=…   # 设置 → API 令牌

curl -sX POST https://panel.example.com:8443/api/sessions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

`command` 是一个 argv；不传就是一个 shell，也就是面板自己界面发的那种。前端能做的每一件事
都能通过同一套 API 做到。[docs/api.md](docs/api.md)（英文）是完整接口面，
而且和路由表双向校验：漏写的接口和多写的段落都会让构建红。

## 设计取舍

真正会改变操作方式的是三条：

- **网页是视图，不是状态。** 关掉它、在三个地方同时打开、命令跑到一半刷新，会话毫无察觉。
- ***已完成*指进程退出了**，不是指会话安静了。
- **颜色永远不是唯一的信息载体。**

那些从外面看会显得莫名其妙的决定，理由写在 [docs/design.md](docs/design.md)（英文）里。
[docs/build-log.md](docs/build-log.md)（英文）是按时间顺序记「做了什么、又被什么绊了一跤」的
施工记录。[docs/plugins.md](docs/plugins.md) 和 [docs/writable-links.md](docs/writable-links.md)
是两份写完之后决定不做的设计，连同不做的理由。

## 开发

```sh
make check         # vet、gofmt、eslint、Go 测试、前端单测 —— 快速门禁
make verify        # 全部，含浏览器检查（约 20 分钟）
make head-check    # 在 HEAD 的干净 worktree 里构建并测试，而不是当前工作区
```

`make check` 从不启动浏览器。这个项目大部分 bug 都是启动浏览器的那几个查出来的：

| | |
|---|---|
| `make panes-check` | 右栏的窗格布局：拖动、放下、合并、复位 |
| `make first-run-check` | 首次设置向导和第一个项目 |
| `make render-check` | 布局、状态、尺寸仲裁、右栏、移动端、剪贴板、passkey |
| `make stress-check` | 宽字符、全屏程序、回滚、输出洪水、断线 |
| `make restart-check` | 杀掉后端；会话和登录态必须活下来 |
| `make scale-check` | 两打会话：快照大小、侧栏可达性、轮询 |
| `make tls-check` | 自带 TLS：wss、Secure cookie、换证书 |
| `make install-check` | 两个安装脚本的每条分支，两种语言都走一遍 |
| `make release-check` | 打出发布包，并从一个临时 HOME 里跑起来 |

tmux 封装是拿真的 tmux 在一个一次性 socket 上测的，不是 mock；
`TEST_TMUX_BIN=/path/to/tmux go test ./...` 可以指定别的构建。本文里的截图由
`web/scripts/shots.mjs` 启动真的二进制拍出来。

`AGENTS.md`（英文）是约定和红线。

## 许可证

[MIT](LICENSE)。
