<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**为多项目 多Agent同时工作的开发者打造的稳定好看高效隐私安全的开发控制台**

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![license: PolyForm Noncommercial](https://img.shields.io/badge/license-PolyForm%20Noncommercial-orange)](LICENSE)

[English](README.md) · **简体中文**

</div>

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/hero-dark.png">
    <img src="docs/images/hero-light.png" width="100%" alt="vibepanel：左边是项目和它们的 session，中间是正在干活的 agent 和下方的临时终端，右边是文件、token 用量和机器负载">
  </picture>
</p>

## 这是什么

vibepanel 是一个针对高强度agent开发者打造的实用终端，采用前后端分离的架构，你的终端托管在拥有最低内存保证和高优先级的专用系统服务，确保不会因为OOM或应用层故障导致丢失会话。同时，我们的UI通过web访问，以便于你使用开发服务器并通过任何设备远程访问。

在安全层面，vibepanel的源码全部公开且release通过公开GitHub action打包，同时最终打包成品是一个不联网、无依赖的go可执行文件，尽最大可能保证安全性、兼容性和性能。并且，升级/重启前端面板不会影响你的任何session和运行中的agent。面板运行时自带了TLS，可以配置https且要求使用密码/passkey登陆。

在UI/UX层面，我们设计了很多小巧思，整个管理模式是项目+Session，您可以快速查看每个项目的每个session的工作状态（完成/工作中/等待输入），在面板的右侧，我们集成了文件管理和笔记本，可以快速通过复制粘贴来传输文件或图片，就像自己的电脑一样。

在面板的下方我根据我自己的使用习惯添加了一个快速使用的终端，方便你在agent运行时查看文件/执行指令 告别/btw。同时我们针对手机端设计了一套独立的UI，并且你可以开启系统级的通知或配置自定义通知渠道，妈妈再也不怕我出门在外没法继续开发了！

连网页都不用开。把面板和 Telegram 机器人、飞书应用或者微信连起来，session 需要你的时候就会发一张卡片过来：哪个 session、在等什么，Telegram 和飞书上下面直接就是「允许」「拒绝」两个按钮。回一句 `3: 再跑一遍测试` 就发进了 3 号 session，`截图 3` 会把终端截成图发回来。打开高级模式还能直接说人话，比如「让写文档的那个加一条 changelog」，后台的 Claude Code 或 Codex 会弄清楚你说的是哪个 session，然后等你回 `ok` 才动手。

面板能分清 agent 是真的做完了还是只是安静了一会：Claude Code、Codex、Kimi Code、zcode、opencode 都是点一下就装好状态上报的 hook，写之前会先给你看要改什么并备份原文件。升级也是一个按钮：设置 → 更新，下载新版、对照 `SHA256SUMS` 校验、先把新程序跑一遍确认能启动再换上去、重启，所有 session 照常跑着。

我们还有一大特色功能是分享链接，无论是想要在显示器上显示整个系统的工作状态，还是想要在大屏幕上让领导知道你消耗了多少token产出了多少代码，都可以。屏幕上显示什么，是你让agent写的一个页面：它有自己的项目，终端旁边就是实时预览，从模版开始，写好了发布，建个链接，拿到屏幕上打开就行。预览点一下就能放大到整个窗口，手机、笔记本、电视各种尺寸随便切，挂上墙之前就知道墙上长什么样。页面默认和面板的数据放在一起，也能导出成 zip 搬到别的面板上。~~好吧我觉得这是个很小众但是确实很重要的功能点~~

我可以很荣幸的向你保证，这个项目**不是AI Slop**，而是一个我高强度自用、真正顺手的终端，我希望这个项目能够节省你的时间并带给你快乐。这个项目目前处于初步开发阶段，欢迎你带着灵感和意见加入到我们的开发工作中。

<p align="center">
  <img src="docs/images/share-zoom.png" width="98%" alt="分享页面的预览放大到整个窗口，按 1920x1080 电视的尺寸显示">
</p>
<p align="center">
  <img src="docs/images/share-settings.png" width="49%" alt="设置里的分享：每个页面和它的版本，下面是显示它的链接">
  <img src="docs/images/share-wall.png" width="49%" alt="会话墙模版在 1920x1080 屏幕上的样子">
</p>
<p align="center"><sub>分享页面：agent 在实时预览旁边写，预览点开能放大看，写好发布，建个链接就能挂到屏幕上。<a href="docs/features.zh-CN.md#给别人看的屏幕">怎么用</a></sub></p>

<p align="center">
  <img src="docs/images/mobile-session.png" width="36%" alt="手机上的 session，带输入框和按键栏">
  <img src="docs/images/mobile-projects.png" width="36%" alt="手机上的项目和 session 列表">
</p>
<p align="center"><sub>手机端是专门设计的一套界面</sub></p>

<p align="center">
  <img src="docs/images/token-usage.png" width="68%" alt="按天、按项目、按模型统计的 token 消耗">
  <img src="docs/images/monitor.png" width="28%" alt="机器负载，以及每个 session 占用了多少">
</p>
<p align="center"><sub>每个项目、每个模型花了多少 token，每个 session 占了多少机器</sub></p>



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

<p align="center">
  <img src="docs/images/panel-crash-zh.png" width="80%" alt="网页只是个窗口；面板崩了、重启、升级，都只是连上去看，tmux 里的 agent 一个不少，还在跑">
</p>


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

[PolyForm Noncommercial 1.0.0](LICENSE)，附加署名条款。个人使用、学习、研究等非商业用途免费，也可以修改和分享修改后的版本，但必须原样保留作者署名和协议声明，不得删除或改动，并注明基于 vibepanel。商业用途（公司内部使用、作为托管服务提供、打包进产品等）需要另外获得授权，请联系 [jmr@jiangmuran.com](mailto:jmr@jiangmuran.com)。

v1.20.1 及之前的版本以 MIT 协议发布，按 MIT 获得的副本仍然适用 MIT。
