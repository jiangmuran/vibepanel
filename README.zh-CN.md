<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**一个把几十个 coding agent 会话管起来的网页控制台。**

进程活着这件事交给 tmux；剩下的全部交给浏览器 —— 会话怎么归到项目下、叫什么名字、
现在哪一个在等你、按什么顺序排。

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![运行时依赖：只要 tmux](https://img.shields.io/badge/%E8%BF%90%E8%A1%8C%E6%97%B6%E4%BE%9D%E8%B5%96-%E5%8F%AA%E8%A6%81%20tmux-3fb950)](#环境要求)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

[English](README.md) · **简体中文**

</div>

![面板](docs/images/panel-zh.png)

## 它解决什么问题

同时开十几个 coding agent，终端复用器给你的是一排都叫 `bash` 的 tab。哪个 agent 在等你
确认、哪个还在干活，不一个个点进去看不出来；同一个项目的 tab 和另外五个项目的混在一起；
手机上基本没法用。

这本质上是个**任务管理**问题，只是穿了一身终端的外衣。vibepanel 把这两件事拆开：
**tmux 负责让进程活着，网页负责组织。**

## 你会得到什么

- **会话比面板活得久。** 每个会话都是独立 socket 上的一个 tmux 会话。面板重启、升级、
  崩溃，会话全都不受影响 —— 进程是 tmux server 的子进程，不是这个程序的。
- **隔着房间也看得懂的状态。** *在动*、*等你处理*、*已完成*。「等你处理」排在最前面，
  也是屏幕上最显眼的东西。装了 hook 就是 agent 自己上报，没装就从输出流里读。
- **项目分组、改了名就不会被改回去。** 你手动改过的名字，pane 的自动标题不会再覆盖它。
- **同一个会话可以在好几个地方同时看。** 每个会话只有一个权威网格，归属于最后打字的那
  一端；其他端是缩放显示而不是重排 —— agent 的 TUI 在手机上不该变成一堆碎纸。
- **手机端不是把桌面端缩小。** 单独的命令输入框（对中文输入法友好）、一排软键
  （`esc` `tab` `ctrl`，以及 agent 真正会问你的 `y`/`n`/`1`/`2`）、双手柄拖选复制。
- **传文件不用终端内协议。** 点一下就下载；把文件拖到终端上就上传 —— 文件落在会话旁边，
  绝对路径直接替你敲进命令行，回车就能用。截图直接粘贴进终端也是一样。
- **每个项目自带笔记、待办、文件树和系统负载**，都在右栏。
- **English 和简体中文**，按浏览器语言自动选，也可以在设置里切。
- **可以装成 App。** PWA + 通知权限：会话变成「等你处理」时，即使面板在后台也能推到手机上。
- **给 agent 用的 HTTP API**，令牌和密码互相独立 —— 见 [docs/api.md](docs/api.md)。
- **Passkey、密码、自带 TLS**，包括 DNS-01 自动签发续期。它是照着「直接挂在公网上」设计的。

<div align="center">
<img src="docs/images/panel-light.png" width="49%" alt="浅色主题">
<img src="docs/images/phone.png" width="20%" alt="手机端">
</div>

## 环境要求

- **tmux 3.3 或更新**（`apt install tmux`）。

  要 3.3 而不是 3.2，是因为内置配置里用了 `allow-passthrough`，这个选项 3.3 才有。旧版
  tmux 不会拒绝启动：它报一句「未知选项」，然后带着默认值继续跑，从此 agent TUI 用来做
  进度条和通知的那些转义序列被静悄悄吞掉。
- **其他什么都不需要。** 发布版是静态单二进制，前端、数据库驱动、TLS 客户端全在里面。

## 安装

在任何一台装了 tmux 的机器上，解开发布包：

```sh
tar -xzf vibepanel_<version>_linux_amd64.tar.gz
cd vibepanel_<version>_linux_amd64
./deploy/install.sh --enable          # 它碰的所有东西都在 $HOME 底下
journalctl --user -u vibepanel -n 30  # 一次性的 setup token
```

不需要 root：这是 systemd **user** service，因为面板是以你的身份、用你的密钥和你的
dotfiles 跑 agent 的。安装脚本会顺手替你开 lingering —— 这不是可选项：user service 会在
你最后一个登录会话结束时停掉，而一个「你一登出就死」的面板只是看起来能用。

打开 `http://<主机>:8443`，粘贴 setup token，设一个密码。首次配置到此结束。

<details>
<summary><b>改成系统级服务</b></summary>

如果机器内存吃得比较紧，或者你希望没人登录时面板也已经起来了，用
`deploy/vibepanel-system.service`：

```sh
sudo cp deploy/vibepanel-system.service /etc/systemd/system/vibepanel.service
sudo sed -i "s/__USER__/$USER/g; s#__HOME__#$HOME#g" /etc/systemd/system/vibepanel.service
sudo systemctl daemon-reload && sudo systemctl enable --now vibepanel
```

区别是实测出来的，不是推测：**user** unit 里写 `OOMScoreAdjust=-500`，进程实际读到的是
`100` —— 调低这个值需要 `CAP_SYS_RESOURCE`，而 user manager 没有。更糟的是
`systemd-analyze verify` 两种写法都放行。系统级 unit 会在切到 `User=` 之前设好，进程才
真的读到 `-500`：这样一来，面板和它手里握着全部会话的 tmux server，会是内核最后才动的
东西。

</details>

<details>
<summary><b>从源码构建</b></summary>

```sh
cd web && npm ci && npm run build && cd ..
make build            # CGO_ENABLED=0 go build -o vibepanel ./cmd/vibepanel
./vibepanel doctor    # 检查 tmux、数据库、磁盘，以及隔离性
./vibepanel serve
```

</details>

<details>
<summary><b>Docker</b></summary>

```sh
docker compose -f deploy/docker-compose.yml up -d
```

**在容器里，重启面板会杀掉所有会话。** 别处 tmux server 比 Go 进程活得久，这才是
`systemctl restart` 和升级都无害的原因 —— 也是这个项目全部设计的前提。在容器里 tmux 是
entrypoint 的子进程、容器就是边界，所以 `docker restart`、重新构建、任何会重建容器的操作
都会把 agent 一起带走。这一点在镜像内部无解。

而且 agent 只能用到容器里的工具、密钥和仓库，这个世界比大多数人以为的要小。只有在
「会话丢了也无所谓」的前提下才这么跑。

</details>

## 为什么会话不会死

整个系统都是围着这一条性质搭的，所以值得把它怎么保住讲清楚：

- 面板**从不**持有某个会话进程的父 PTY。它跟你一样，只是跑 `tmux attach` 当客户端。
- systemd unit 里写了 `KillMode=process`：停服务时只杀面板，tmux server 和底下所有 agent
  一个都不动。
- tmux socket 固定是 `-L vibepanel` 配自己的配置文件，绝不用默认那个。你可以把它装在一套
  已经跑了好几周的 tmux 或 zellij 旁边，`vibepanel doctor` 还会断言这个 socket 上除了它
  自己的会话什么都没有。

```sh
systemctl --user restart vibepanel   # 面板消失又回来
tmux -L vibepanel ls                 # 会话一个不少，还在跑
```

重连时浏览器会发现后端换了新版本并提示你刷新，所以「跑到一半升级」是一条横幅，而不是
一个谜。

## 怎么让面板知道 agent 在干什么

不装任何东西时，面板从输出流里猜：刚才有字节出来就是*在动*，收到终端响铃就是*等你处理*，
pane 回到 shell 提示符就是*已完成*。这是诚实的，但是粗糙。

设置页里的**状态上报**会把一小段 hook 合并进 Claude Code 或 Codex 自己的配置文件 —— 先把
要写的 JSON 原样给你看，写之前先备份。hook 读两个由面板注入到每个会话里的环境变量，然后
直接把状态 POST 回来：

```json
{"sessionId": "…", "state": "waiting"}
```

面板之外启动的会话没有这两个变量，所以同一份全局 hook 配置对它们完全是空操作。卸载时也
只会删掉 vibepanel 自己打过标记的那几条。

## 配置

每个 flag 都有对应的 `VIBEPANEL_<大写下划线>` 环境变量，flag 优先。**认不出来的**
`VIBEPANEL_*` 变量会在启动时和 `doctor` 里被报出来，而不是被忽略 —— 曾经一个拼错的
`VIBEPANEL_TLS` 意味着面板在公网端口上明文服务，而运维以为不是。

| Flag | 默认值 | 说明 |
|---|---|---|
| `--data-dir` | `~/.local/share/vibepanel` | 数据库、tmux 配置、ACME 状态 |
| `--addr` | `:8443` | 监听地址；默认是所有网卡 |
| `--domain` | — | 公网主机名；同时也是 WebAuthn 的 Relying Party ID |
| `--tls` | `off` | `off` / `files` / `acme`。非环回地址上用 `off` 会在启动时告警：终端内容、密码和会话 cookie 全都明文过网 |
| `--tls-cert` / `--tls-key` | — | 配合 `--tls files`；文件变了会自动重载 |
| `--acme-dns` | — | `--tls acme` 用的 DNS-01 provider（目前是 `cloudflare`） |
| `--acme-email` | — | 给 CA 的联系邮箱 |
| `--acme-directory` | Let's Encrypt | 测试时指向 staging |
| `--allow-from` | — | 允许访问面板的 CIDR，逗号分隔 |
| `--trusted-proxies` | — | 其 `X-Forwarded-For` 可信的 CIDR |
| `--tmux-socket` | `vibepanel` | 保持专用，才谈得上隔离 |
| `--static-dir` | — | 从磁盘而不是内嵌产物提供前端 |

### 登录

首次启动会把一次性的 setup token 打到控制台。这就是交接动作：**能读到服务端输出的人**才
有资格认领这个面板，仅仅能从网络上够到它不算。一旦建好账号，setup 接口就永久关闭。

除了健康检查和 agent hook 这两个接口，其他全部需要凭据，WebSocket 也一样 —— 它**就是**终端。

登录失败按来源地址做指数退避，`--allow-from` 进一步限制谁能够到面板。这两者判断的都是
`--trusted-proxies` 说可信的那个地址：没有配可信代理时，就是 socket 对端，
`X-Forwarded-For` 被完全忽略。一个能改写调用方身份的 header，等于把这两道控制一起关掉。

### Passkey

WebAuthn 需要安全上下文，以及一个是**可注册域名**的 Relying Party ID。**IP 地址永远不是
合法的 RP ID**，所以 `https://192.168.1.10:8443` 无论 TLS 怎么配都注册不了 passkey。用域名。

密码登录永远可用，首次启动时就设好了。Passkey 是加在它之上的，绝不会成为唯一入口。
`vibepanel doctor` 和登录页都会告诉你当前配置支不支持，不支持的话原因是什么。

### 证书

```sh
# 自己的证书，文件变了自动重载
vibepanel --domain panel.example.com --tls files \
          --tls-cert /etc/ssl/panel.pem --tls-key /etc/ssl/panel.key

# 或者自动签发和续期
CLOUDFLARE_API_TOKEN=… vibepanel --domain panel.example.com \
          --tls acme --acme-dns cloudflare --acme-email you@example.com
```

HTTP-01 校验要占 80 端口，而这个面板并不指望自己有 80 端口，所以自动证书走 DNS-01。

## 用程序驱动它

```sh
TOKEN=…   # 设置 → API 令牌

curl -sX POST https://panel.example.com:8443/api/sessions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

面板自己的前端能做的每一件事，都能通过同一套 API 做到。[docs/api.md](docs/api.md) 就是完整
接口面，而且它和路由表是**双向**校验的：存在却没写进文档的接口过不了构建，文档里描述了一个
已经删掉的路由同样过不了 —— 后者更糟，因为真的会有人照着那一段写代码。

## 一些设计取舍

**网页是视图，不是状态。** 关掉它、在三个地方同时打开、命令跑到一半刷新 —— 会话毫无察觉。
所有状态都在后端，广播给每一个连着的客户端。

**每个会话只有一个权威网格。** 桌面 200×50 和手机 45×20 没法同时是同一个终端的尺寸。面板
不做 reflow，而是保留一个由「最后交互的那一端」拥有的网格，其他端缩放去适配。所有人看到
的是同一份字节、同一个网格。

***已完成*指进程退出了，不是指会话安静了。** agent 在思考、在等一个慢工具、或者在往屏幕
以外的地方写东西时，可以任意长时间不产生输出；把这个报成「完成」，就是面板对着它唯一存在
理由的那个问题给出一个自信的错误答案。没有 hook 时，安静的运行中 agent 被报成*在动* ——
不管它是在想还是在问，这句话都成立；而真正意味着「需要人」的两个信号，终端响铃和 hook
上报，优先级都在它之上。

**颜色永远不是唯一的信息载体。** 每个状态除了颜色还有形状：圆点、三角、对勾。有人会在
凌晨两点、黑着灯的房间里、用手机看这个面板。

**文件走 HTTP，不走终端。** 终端内嵌传输协议会和全屏 TUI 打架；而且把截图放到服务器上的
目的就是交给 agent —— 所以「路径已经敲好、回车就能用」才是重点，不是细节。

## 开发

```sh
make check         # vet、gofmt、eslint、Go 测试、前端单测 —— 快速门禁
make verify        # 全部，含浏览器检查（约 20 分钟）
make head-check    # 在 HEAD 的干净 worktree 里构建并测试，而不是你的工作区
```

`make check` 从不启动浏览器，而这个项目大部分 bug 都是启动浏览器的那几个查出来的：

| | |
|---|---|
| `make first-run-check` | 首次设置向导和第一个项目 |
| `make render-check` | 布局、状态、尺寸仲裁、右栏、移动端、剪贴板、passkey |
| `make stress-check` | 宽字符、全屏程序、回滚、输出洪水、断线 |
| `make restart-check` | 杀掉后端；会话和登录态必须活下来 |
| `make scale-check` | 两打会话：快照大小、侧栏可达性、轮询 |
| `make tls-check` | 自带 TLS：wss、Secure cookie、换证书 |
| `make release-check` | 打出发布包，并从一个临时 HOME 里跑起来 |

tmux 封装是拿真的 tmux 在一个一次性 socket 上测的，不是 mock —— 那里值得抓的 bug 是 tmux
自己的，mock 一个都复现不出来。

`AGENTS.md` 是约定和红线。`docs/build-log.md` 是按时间顺序记录「做了什么、出了什么错」的
施工记录，包括一个塑造了核心设计的 tmux 3.6 崩溃。`docs/runbook.md` 是线上出问题时看的。

## 许可证

[MIT](LICENSE)
