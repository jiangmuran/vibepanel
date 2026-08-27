<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**同时跑十几个 coding agent，一眼看出是哪一个在等你。**

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![运行时依赖：只要 tmux](https://img.shields.io/badge/%E8%BF%90%E8%A1%8C%E6%97%B6%E4%BE%9D%E8%B5%96-%E5%8F%AA%E8%A6%81%20tmux-3fb950)](#环境要求)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

[English](README.md) · **简体中文**

</div>

![面板](docs/images/panel-zh.png)

<sup>左栏是全部会话，按项目分组，卡住的排在最上面。三角：停下来问你话了。圆点：还在干活。
对勾：跑完了。叉：进程非零退出。中间下方那一条是挂在当前会话下的便签终端。右栏是这个项目的
文件树，底下常驻机器的 CPU、内存和磁盘。</sup>

## 它是什么

一个 Go 单二进制，起一个网页。它创建的每个会话都是一个真的 tmux 会话，里面是项目目录下的
一个 shell。跑什么由你敲：`claude`、`codex`、一个测试循环、一个 `tail -f`。

面板管的是 tmux 完全不管的那一半：会话归到项目下、改过的名字不会被改回去、状态一眼能读、
需要你处理的排在最前面、每个项目自带笔记待办和文件树，以及一套手机上真的能用的界面。

你跑起来的东西不属于面板。面板重启、升级、被 kill，agent 照常在 tmux 里跑着。

顺带说清楚几件常被误会的事：它不封装 agent，不改你的提示词，不代理你的 API 请求；
它不是多人协作工具，只有一个账号；它也不打算替掉 tmux。

## 适合谁

你手上同时开着好几个 agent，跨好几个仓库，跑在一台一直开着的机器上——自己的工作站，
或者一台你用手机也会去看两眼的 VPS。

如果你一次只开一个 agent，而且那个终端就在眼前，那不需要这个。

装之前先知道三件事：

- **Linux，amd64 或 arm64。** 机器监控读 `/proc`，安装脚本写 systemd unit。
  `darwin/arm64` 的二进制也会构建、面板也能跑，但监控是空的，进程守护得你自己来。
- **单用户。** 没有分权，没有共享。要给第二块屏幕看，用[只读分享链接](#只读分享链接)。
- **agent 以你的身份跑**，用你的密钥、dotfiles 和仓库。谁进得了面板，谁就等于拿到你的 shell。

面板界面是中英双语的，`docs/` 下的文档是英文的。

## 环境要求

tmux 3.3 或更新——`apt install tmux`，或者让安装脚本替你装。除此之外什么都不用：
发布版是静态单二进制，前端、数据库驱动和 TLS 客户端都在里面。

3.3 这条线来自 `allow-passthrough`。更老的 tmux 照样能把面板跑起来，只是 agent TUI 用来画
进度条和发通知的转义序列会被吞掉。`vibepanel doctor` 会报出来。

CI 跑在 tmux 3.4 上，开发机上是 3.6。想指定别的：
`TEST_TMUX_BIN=/path/to/tmux go test ./...`。

## 安装

| | 什么时候用 | 会话能活过 | 需要 root | 开机自启 |
|---|---|---|---|---|
| **系统服务**（拿得到 root 时的默认） | 拿得到 root。机器内存吃得紧，或者要求没人登录时面板也起着 | 面板重启、面板崩溃、你登出——并且内存紧张时内核最后才动它 | 装的时候一次 | 是 |
| **user service** | 这里拿不到 root，或者是共享机器上你自己的账号 | 面板重启、面板崩溃、你登出 | 不需要 | 是，靠 lingering |
| **直接跑** | 只是试一下，或者已有顺手的进程管理 | 面板重启 | 不需要 | 否 |
| **Docker** | 想要隔离，丢会话无所谓 | **什么都活不下来**——容器里 tmux 是 entrypoint 的子进程，`docker restart` 会带走所有 agent | 不需要 | 看容器策略 |

只装一种，绝不要两种都装：两个 unit 就是一个 tmux socket 上两个面板，安装脚本会拒绝，
而不是悄悄再装一个。macOS 上只有一种，LaunchAgent。

一行命令，Linux 和 macOS 都行，机器上什么都没有也行：

```sh
curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh
```

它会认出平台、下载对应的发布包、拿发布的 `SHA256SUMS` 校验，对不上就拒绝解包；tmux 缺失
或太旧时会问你要不要装；然后装服务——Linux 上是 systemd unit，macOS 上是 launchd
LaunchAgent。已经解开发布包的话，`./deploy/install.sh` 就是同一个安装脚本，只是不用下载。

它会问你装哪一种，把接下来要做的事列出来，等你点头才动手。只有 stdin 和 stdout 都是终端
时才提问，管道里跑就是无人值守那条路。全部选项在 `... | sh -s -- --help`。

然后打开 `http://<主机>:8443`，粘贴它打印出来的 setup token，设一个密码——或者直接在安装
脚本里建账号：`--username you --password-file /path/to/pw`。**故意没有** `--password <值>`：
那是把密码写进 shell history 和 `ps`。

装完之后，不管用哪种方式跑的，都是同一条命令：

```sh
vibepanel service status | start | stop | restart | logs | token | upgrade | uninstall
```

<details>
<summary><b>系统级服务</b></summary>

```sh
./deploy/install.sh --system          # 要替换已有的 user unit 就加 --migrate
```

同一个面板、同一份数据、同一个账号，unit 里也会切到 `User=<你>`。它多出来的是
`OOMScoreAdjust=-500`，这个值 user unit 拿不到。有了它，面板和握着全部会话的
tmux server 就是内核最后才杀的东西。机器内存吃得紧，或者要求没人登录时面板也已经起来，就选它。

两份 unit 都带着 `MemoryHigh=20G` 和 `MemoryMax=26G`，是按 32 GB 机器跑十几个 agent 配的。
小 VPS 上请往下调。

**两种只能装一种。** 两个 unit 就是一个 tmux socket 和一个数据库上跑两个面板，症状是
「面板会忘事」。安装脚本检测到另一种就会拒绝，`--migrate` 会先停掉并删掉旧的再装新的。

</details>

<details>
<summary><b>无人值守：CI 和 <code>curl | bash</code></b></summary>

```sh
./deploy/install.sh --yes --enable    # 不问，装 user service，并启动
./deploy/install.sh --yes --system    # 不问，装系统级服务（需要 root）
./deploy/install.sh --help
```

`--yes` 全取默认值，`--enable` 启动服务，`--user` / `--system` 指定 unit 种类，
`--migrate` 允许把已装的那一种换成另一种。拿不到 root 时它会直说，然后改装 user service。

装 user service 时脚本还会替你开 lingering。不开的话，unit 会在你最后一个登录会话结束时停掉。

</details>

<details>
<summary><b>从源码构建</b></summary>

```sh
cd web && npm ci && npm run build && cd ..
make build            # CGO_ENABLED=0 go build -o vibepanel ./cmd/vibepanel
./vibepanel doctor
./vibepanel serve
```

</details>

<details>
<summary><b>Docker</b></summary>

```sh
docker compose -f deploy/docker-compose.yml up -d
```

容器里重启面板会杀掉所有会话，镜像内部改不了这件事：tmux 是 entrypoint 的子进程，容器就是
边界，`docker restart` 和任何重建都会把 agent 一起带走。而且 agent 只看得见容器里的工具、
密钥和仓库。只有在会话丢了也无所谓的前提下才这么跑。

</details>

## 怎么用

1. **加一个项目**：一个名字，一个目录。选择器上面那个框会看你输的内容决定做什么——
   以 `/` 或 `~` 开头就当路径跳过去，否则就是筛当前目录。
2. **开一个会话**：先挑用什么启动。第一项就是项目目录下的一个 shell——按钮以前干的
   就是这件事；下面的是启动配置。
3. **改个名**：会话会自己从 pane 标题取名，但你手动改过之后自动标题就不再覆盖它。
4. **关掉标签页**：会话是 tmux 会话，它毫无察觉。

同一个项目内的会话按紧急程度排序，停下来问你话的那个永远在最上面。想固定住的可以钉住。
每个会话下面还能挂便签终端（截图底部那一条），同一个目录，用来在 agent 干活时顺手
`git status` 一下。

<div align="center">
<img src="docs/images/panel-light.png" width="49%" alt="浅色主题">
<img src="docs/images/phone.png" width="20%" alt="手机端">
</div>

### 状态

形状和颜色同时表意，黑着灯用手机也读得出来。

| | | |
|---|---|---|
| ▲ | **等你处理** | agent 停下来了，要人；排最前面 |
| ● | **在动** | 正在出输出，或者在思考 |
| ✓ | **已完成** | 干完了，或者是一个停在提示符上的 shell |

进程没了另有形状：非零退出是叉，状态码写在提示文字里；干净退出是空心方块；tmux 会话本身
不见了是虚线方块。活着的会话可以手动标成「等你处理」或「已完成」，一直保持到它有新动静为止。

### 手机端

不是把桌面端缩小，是另一套外壳：独立的命令输入框（对中文输入法友好）、一排软键
（`esc` `tab` `ctrl`，以及 agent 真正会问你的 `y`/`n`/`1`/`2`）、双手柄拖选复制。

加到主屏就是一个 PWA，带通知：会话变成「等你处理」时推一次，你正看着页面时不推。
这是浏览器通知，不是 Web Push：页面还活着时才送得到，后台标签页和装成 App 都算，
浏览器整个关掉就收不到。

### 右栏

每个项目五个 tab，底下常驻一条机器 CPU / 内存 / 磁盘，切到哪个 tab 都看得见。

- **文件**：浏览、点一下下载。拖到文件树上或者直接拖到终端上就是上传，文件落在会话旁边，
  绝对路径直接替你敲进命令行，回车就能用；截图粘贴进终端也是同一条路。预览按内容嗅探
  类型而不是看扩展名，支持文本、PNG、JPEG、GIF、WebP、AVIF、PDF，上限 8 MiB；
  长文本按 256 KiB 或 4000 行截断。SVG 按文本显示。
- **监控**：机器整体，外加每个会话的 CPU 和内存，按会话的整棵进程树汇总。百分比用整机做
  分母，不是「100% = 一核」。
- **笔记** / **待办**：每个项目一份 markdown 和一张清单，停手就自动保存。
- **Token**：agent 自己记下来花了多少 token，今天的和最近三十天的。数字只来自 Claude Code
  和 Codex 自己写的 transcript，不估算，也不换算成钱；找不到 transcript 时显示破折号，
  不显示 0。

## 会话为什么杀不死

面板重启不花任何代价：

```sh
systemctl --user restart vibepanel   # 面板消失又回来
tmux -L vibepanel ls                 # 会话一个不少，还在跑
```

面板只是以客户端身份 attach 上去，从不持有会话进程的父 PTY；而且它跑在自己的 socket
（`-L vibepanel`）配自己的配置文件上，所以可以和你已有的 tmux 或 zellij 装在一起互不干扰。
重连时浏览器会发现后端换了版本并提示刷新。

**重启机器是另一回事。** tmux server 就是个普通进程，回滚记录在它的内存里，机器一关两样都没。

面板会把重建一个会话需要的东西记下来：建它时用的命令、目录、名字和排序位置，以及最后
2000 行或 256 KiB 的回滚记录，每 30 秒抓一次，关机时再抓一次。正常关机一行输出都不会丢，
断电最多丢半分钟。

开机之后面板会问你要不要恢复，一个或者全部，并把每个会话将要执行的命令和目录列出来。
也可以给某个会话单独打开「开机自动恢复」，默认是关的。

**进程回不来。** agent 的上下文活在那个进程和它跟服务商的一段对话里，重跑命令启动的是一个
全新的、什么都不记得的 agent。恢复出来的 pane 会在新进程上方打一条横幅说明这件事，
会话本身还会留一个 `restored` 标记。

## 升级

**设置 → 更新** 会去 GitHub 取最新 release，用同一个 release 里发布的 `SHA256SUMS` 校验，
换掉二进制并重启服务，旧的留成 `.old`。它只在你按下按钮时发生：没有定时检查，没有心跳，
没有遥测。

连不上 `api.github.com` 的机器只会得到一个报错。换二进制这条路永远可用，境内机器也更省事：

```sh
tar -xzf vibepanel_<新版本>_linux_amd64.tar.gz
cd vibepanel_<新版本>_linux_amd64
./deploy/install.sh              # 换掉二进制并重启服务
```

它会沿用你现在装着的那种 unit，不再问一遍。两种方式下会话都不会跟着重启。

有两件事的行为值得知道：

- **改过的 tmux 配置要到下一次 `start-server` 才生效。** tmux 只读一次配置，而面板从不杀
  自己的 server，所以升级之后是新文件在磁盘上、旧设置在内存里。设置页和 `vibepanel doctor`
  都会告诉你。要应用就得 `tmux -L vibepanel kill-server`，代价是全部会话。
- **旧二进制会拒绝打开被新版本迁移过的数据库**，并把两个版本号都说出来，而不是打开它、
  然后悄悄丢掉自己不认识的列。

其他情况看 [docs/runbook.md](docs/runbook.md)，它是按症状组织的（英文）。

## 状态上报

什么都不装时，面板从输出流里猜：刚才有字节出来是*在动*，收到终端响铃是*等你处理*，
pane 回到 shell 提示符是*已完成*。安静的会话不会被当成跑完了。

**设置 → 状态上报** 里 Claude Code 和 Codex 各有一个按钮。它把一小段 hook 合并进 agent
自己的配置文件，写之前先把内容原样给你看，并备份被改的文件。hook 读两个由面板注入到每个
会话里的环境变量，然后把状态 POST 回来：

```json
{"sessionId": "…", "state": "waiting"}
```

面板之外启动的会话没有这两个变量，所以同一份全局配置对它们不起任何作用；卸载时也只删掉
vibepanel 自己加的那几条。这个 hook 是个 `/bin/sh` 脚本，里面调 `curl`；没有 curl 的话
面板退回到猜，并且不会报错。

Claude Code 有四个事件，能报 *working*、*waiting* 和 *done*。Codex 只有一个 `notify`，
所以 Codex 会话只报 *waiting*，其余靠猜。

其他 agent 没有做一键安装，但任何东西都可以自己 POST 到 `/api/hook/state`，
格式见 [docs/api.md](docs/api.md)。

## 挂到公网上

面板默认监听 `:8443`，也就是所有网卡，本来就是照着直接面对公网设计的。

所有接口都需要凭据，WebSocket 也一样——它就是终端。例外有三个：健康检查；agent hook 接口，
它收的是注入到每个会话里的令牌；以及只读看板，它收的是分享 token，只在一个 `GET` 上认，
换到别的接口一律 401。

首次启动会把一次性的 setup token 打到控制台：能读到服务端输出的人才有资格认领这个面板。
建好账号之后这个接口就永久关闭。登录失败按来源地址指数退避，`--allow-from` 进一步限制谁能
够到面板。这两者判断的都是 `--trusted-proxies` 说可信的那个地址；没配可信代理时就是 socket
对端，`X-Forwarded-For` 完全不看。

**Passkey** 是加在密码之上的，不会成为唯一入口。WebAuthn 要求 Relying Party ID 是一个可注册
域名，所以 IP 地址无论 TLS 怎么配都注册不了 passkey，用域名。`vibepanel doctor` 和登录页都会
告诉你当前配置支不支持。

**API 令牌**和密码互相独立：改密码会踢掉所有浏览器但不动令牌，吊销令牌也不影响密码。
令牌只在创建时那一次响应里能读到。

**证书**由面板自己管，两条路：

```sh
# 自己的证书，文件变了自动重载
vibepanel --domain panel.example.com --tls files \
          --tls-cert /etc/ssl/panel.pem --tls-key /etc/ssl/panel.key

# 或者自动签发和续期
CLOUDFLARE_API_TOKEN=… vibepanel --domain panel.example.com \
          --tls acme --acme-dns cloudflare --acme-email you@example.com
```

自动证书走 DNS-01，因为 HTTP-01 要占 80 端口；目前接了的 provider 是 cloudflare。
非环回地址上留着 `--tls off` 会在启动时告警。

### 启动配置

**设置 → 启动配置**：给一套启动方式起个名字——一条 argv，加上启动它时的环境变量。
它存在的理由就是 apihost：同一个 agent 指向官方、指向公司代理、指向自建网关，
三份配置只差一个变量，而每次都重新敲一遍正是这个功能要替掉的事。

内置了 shell、`claude`、`codex`、`opencode` 四个，不能改；复制一份出来，副本里
那个 agent 会读的变量名已经填好了——claude 是 `ANTHROPIC_BASE_URL` 和
`ANTHROPIC_AUTH_TOKEN`，codex 是 `OPENAI_BASE_URL` 和 `OPENAI_API_KEY`。
值留空的变量**根本不会设**，所以一份填了一半的配置跑起来和裸终端里跑一模一样。

**故意没有「API host」这个字段。** endpoint 装在哪个变量里是各家 agent 自己定的，
opencode 干脆没有——它按 provider 在自己的配置文件里选。做成一个字段，面板就得替
别人家的工具维护这份映射，一直维护到猜错的那天。

变量可以标成**密钥**，标了之后它的值**再也不会发回浏览器**：设置页面只显示变量名，
和「已经存了一个」。它通过 tmux 交给进程，不走命令行，所以 `ps` 里看不到，
审计日志里也没有。但它**没有加密**——就是面板数据库文件里的明文，和里面别的东西一样。
拿一把跟数据库放在一起的钥匙去加密，那不叫加密，那叫给混淆起个好听的名字。

会话会记住自己是被哪个配置启动的，所以重启之后恢复它，回来的不只是命令，还有 endpoint。

### 只读分享链接

**设置 → 只读分享链接** 会生成一个可以挂在另一块屏幕上的地址：
`https://<面板>/share/<token>`。它只打开一个看板，除此之外什么都打不开——没有终端、
没有写入、没有文件浏览，也没法用一个链接再生成一个。

看板长什么样，是生成链接时选的。十九个起手式，按「谁在看这块屏」分组：

| | |
|---|---|
| 自己正在干活 | 总览 · 有没有事要我处理 · 等待队列 · 会话方阵 · 全都要 |
| 墙上的那块屏 | 四个数字加一个钟 · 一个数字占满 · 现在有多忙 · 三页轮播 |
| 管机器的人 | 只看机器 · 有没有出事 |
| 老板和领导 | 花了多少 vs 做出来什么 · 钱去哪了 · 数字少字大的那个 · 这一年 |
| 盯着一件事看 | 按项目 · 哪个模型在干活 · 今天花了多少 |

起手式只是起手式，不是模式。每个组件都能挪、能改宽、能换成别的数、能换个维度拆
（按 agent、按项目、按模型、按天、按月），还有二十一种组件可以加。看板跟链接一起存着，
宽度随屏幕收窄——同一个看板在手机上是摘要，在电视上就是铺开的四十块。

数据是面板本来就知道的那些：状态和已经维持了多久、每个会话的 CPU 和内存、机器、待办
完成度，以及 agent 自己记的 token 账。只有 token，不折算成钱：价格按模型、按档位、按时间
都不一样，拿过期价目表算出来的金额，是一个看起来很确定的错数。

范围可以限定成整个面板、一个项目或一个会话。限定到项目的那种，就是发给一起做这个项目的人
的。范围由服务端按链接自己那行强制执行；项目后来删掉了，链接显示「什么都没有」，而不是
退回去显示全部。

生成时选详细程度。**只有数量**（默认）只显示形状和数字，一个字都不带；**加上名字**会把
会话标题和项目名也发出去。两档都不会发送路径、工作目录、命令行、主机名和面板自己的 id。
看板可以事后改，档位和范围不行——等你想改的时候地址早就躺在别人邮件里了，把它能看到的
东西改宽，拿着地址的人根本不会知道。

链接本身就是凭证，谁拿到谁就能看。数据库里只存哈希，所以生成的那一刻是唯一能读到它的时候。
每个链接单独吊销，可以设过期时间，生成和吊销都会记进审计日志。

看板会用文字和形状写明*实时*、*重连中*还是*已断开*，页头永远写着最后一次读数的时间和距今多久。

## 参数与排查

每个 flag 都有对应的 `VIBEPANEL_<大写下划线>` 环境变量，flag 优先。没人读的 `VIBEPANEL_*`
变量会在启动时和 `doctor` 里被报出来，而不是被忽略。

| Flag | 默认值 | 说明 |
|---|---|---|
| `--data-dir` | `~/.local/share/vibepanel` | 数据库、tmux 配置、ACME 状态 |
| `--addr` | `:8443` | 监听地址 |
| `--domain` | — | 公网主机名；同时是 WebAuthn 的 Relying Party ID |
| `--tls` | `off` | `off` / `files` / `acme` |
| `--tls-cert` / `--tls-key` | — | 配合 `--tls files`；文件变了自动重载 |
| `--acme-dns` | — | `--tls acme` 用的 DNS-01 provider（`cloudflare`） |
| `--acme-email` | — | 给 CA 的联系邮箱 |
| `--acme-directory` | Let's Encrypt | 测试时指向 staging |
| `--allow-from` | — | 允许访问面板的 CIDR，逗号分隔；空表示不限 |
| `--trusted-proxies` | — | 其 `X-Forwarded-For` 可信的 CIDR |
| `--tmux-socket` | `vibepanel` | 保持专用，才谈得上隔离 |
| `--static-dir` | — | 从磁盘而不是内嵌产物提供前端 |

同一个二进制也是管理 CLI：`serve`、`project`、`session`、`hook`、`doctor`、`version`。
`doctor` 一次打印十五行，不会遇到第一个失败就停：tmux 和它的版本、数据目录、数据库以及
一次真实写入、磁盘、socket 隔离性、正在跑的 tmux server 配置是不是已经过期、还活着的会话
手里的 hook 地址和令牌面板还认不认、passkey 能不能用，以及没人读的 `VIBEPANEL_*` 变量。

## 用程序驱动它

```sh
TOKEN=…   # 设置 → API 令牌

curl -sX POST https://panel.example.com:8443/api/sessions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

`command` 是一个 argv；不传就是一个 shell，也就是面板自己界面发的那种。前端能做的每一件事
都能通过同一套 API 做到。[docs/api.md](docs/api.md)（英文）是完整接口面，而且和路由表双向
校验：漏写的接口和多写的段落都会让构建红。

## 设计取舍

真正会改变你操作方式的是三条：

- **网页是视图，不是状态。** 关掉它、在三个地方同时打开、命令跑到一半刷新，会话毫无察觉。
- ***已完成*指进程退出了**，不是指会话安静了。
- **颜色永远不是唯一的信息载体。**

那些从外面看会显得莫名其妙的决定，理由写在 [docs/design.md](docs/design.md)（英文）里。
[docs/build-log.md](docs/build-log.md)（英文）是按时间顺序记「做了什么、又被什么绊了一跤」的
施工记录。

## 开发

```sh
make check         # vet、gofmt、eslint、Go 测试、前端单测 —— 快速门禁
make verify        # 全部，含浏览器检查（约 20 分钟）
make head-check    # 在 HEAD 的干净 worktree 里构建并测试，而不是你的工作区
```

`make check` 从不启动浏览器。这个项目大部分 bug 都是启动浏览器的那几个查出来的：

| | |
|---|---|
| `make first-run-check` | 首次设置向导和第一个项目 |
| `make render-check` | 布局、状态、尺寸仲裁、右栏、移动端、剪贴板、passkey |
| `make stress-check` | 宽字符、全屏程序、回滚、输出洪水、断线 |
| `make restart-check` | 杀掉后端；会话和登录态必须活下来 |
| `make scale-check` | 两打会话：快照大小、侧栏可达性、轮询 |
| `make tls-check` | 自带 TLS：wss、Secure cookie、换证书 |
| `make install-check` | 把 `deploy/install.sh` 的每条分支都走一遍，不需要 sudo |
| `make release-check` | 打出发布包，并从一个临时 HOME 里跑起来 |

tmux 封装是拿真的 tmux 在一个一次性 socket 上测的，不是 mock。本文里的截图由
`web/scripts/shots.mjs` 启动真的二进制拍出来。

`AGENTS.md`（英文）是约定和红线。

## 许可证

[MIT](LICENSE)
