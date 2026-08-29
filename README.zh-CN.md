<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**为多项目 多Agent同时工作的开发者打造的稳定好看高效隐私安全的开发控制台**

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

[English](README.md) · **简体中文**

</div>

## 这是什么

vibepanel 是一个针对高强度agent开发者打造的实用终端，采用前后端分离的架构，你的终端托管在拥有最低内存保证和高优先级的专用系统服务，确保不会因为OOM或应用层故障导致丢失会话。同时，我们的UI通过web访问，以便于你使用开发服务器并通过任何设备远程访问。

在安全层面，vibepanel的源码是100%开源且release通过公开GitHub action打包，同时最终打包成品是一个不联网、无依赖的go可执行文件，尽最大可能保证安全性、兼容性和性能。并且，升级/重启前端面板不会影响你的任何session和运行中的agent。面板运行时自带了TLS，可以配置https且要求使用密码/passkey登陆。

在UI/UX层面，我们设计了很多小巧思，整个管理模式是项目+Session，您可以快速查看每个项目的每个session的工作状态（完成/工作中/等待输入），在面板的右侧，我们集成了文件管理和笔记本，可以快速通过复制粘贴来传输文件或图片，就像自己的电脑一样。

在面板的下方我根据我自己的使用习惯添加了一个快速使用的终端，方便你在agent运行时查看文件/执行指令 告别/btw。同时我们针对手机端设计了一套独立的UI，并且你可以开启系统级的通知或配置自定义通知渠道，妈妈再也不怕我出门在外没法继续开发了！

我们还有一大特色功能是可以创建各种各样的只读链接，无论是想要在显示器上显示整个系统的工作状态，还是想要在大屏幕上让领导知道你消耗了多少token产出了多少代码，又或者是想要轮播展示所有agent的工作状态，我设计了大量的模版开箱即用。~~好吧我觉得这是个很小众但是确实很重要的功能点~~

我可以很荣幸的向你保证，这个项目**不是AI Slop**，而是一个我高强度自用、真正顺手的终端，我希望这个项目能够节省你的时间并带给你快乐。这个项目目前处于初步开发阶段，欢迎你带着灵感和意见加入到我们的开发工作中。

*这里等我找几个能放的项目截几张实机图，先欠着喵*



## 适合谁
适合所有同时需要打开多个终端agent进行管理，或有一台开发服务器希望能够24小时工作并使用任何设备远程操作的人。

## 交互安装脚本（Linux/Macos）

我们的交互式安装脚本支持简体中文/English，并兼容多种安装模式，具体的安装模式对比和无人值守安装可参考后文。

### 标准

```sh
curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh
```

### 如果你在网络受限地区
PS: 这个是由jiangmuran搭建的GitHub公益镜像站，为了防止滥用，第一次执行后会在终端弹出提示按照要求在网页端人机验证。

```sh
curl -fsSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh -o vibepanel-install.sh \
  || curl -sSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh
sh vibepanel-install.sh --mirror
```

### 管理工作状态

```sh
vibepanel service status | start | stop | restart | logs | token | upgrade | uninstall
```

剩下的都在 [docs/install.md](docs/install.md)（英文）：user unit 和系统 unit 怎么选、
全部参数、无人值守安装、在命令行里建第一个账号、Docker，以及从源码构建。

## 功能

[docs/features.zh-CN.md](docs/features.zh-CN.md) 
⚠️ 此内容由AI编写 ⚠️

## 参数与排查

每个 flag 都有对应的 `VIBEPANEL_<大写下划线>` 环境变量，flag 优先。没人读的
`VIBEPANEL_*` 会在启动时和 `doctor` 里被报出来，而不是被忽略，所以改过名的设置是吵的，
不是悄悄失效。完整的参数表在 [docs/install.md](docs/install.md)（英文）。

同一个二进制也是管理 CLI：`serve`、`project`、`session`、`hook`、`service`、
`account`、`doctor`、`version`。

## 用程序驱动它

```sh
TOKEN=…   # 设置 → API 令牌

curl -sX POST https://panel.example.com:18443/api/sessions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

`command` 是一个 argv；不传就是一个 shell，也就是面板自己界面发的那种。前端能做的每一件事
都能通过同一套 API 做到。[docs/api.md](docs/api.md)（英文）是完整接口面，
而且和路由表双向校验：漏写的接口和多写的段落都会让构建红。

## 设计取舍

我们的几个核心原则：

- **网页是视图，不是状态。** 关掉它、在三个地方同时打开、命令跑到一半刷新，会话毫无察觉。
- ***已完成*指进程退出了**，不是指会话安静了。
- **颜色永远不是唯一的信息载体。**


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

`AGENTS.md`是约定和红线。

## 许可证

[MIT](LICENSE)。
