# 待办清单

来自 2026-08-26 的一轮使用反馈。按"你能多快看见"排序，不是按难度。

勾掉的每一条后面跟着"怎么证明的"。README 自己写过一段：手工维护的状态行会说谎——它把
手机端、设置页和发布流水线写成"未做"，而三样都已经上线好一阵子了。所以这里只写有证据的。

## A — 界面

- [x] **A1 终端行的割裂感** — `lineHeight` 1.2 → 1.0。xterm 按格子刷背景色，任何行距都会在
      带底色的输出里留下一条横向缝。截图前后对比可见，`make render-check` 的行高断言钉住。
- [x] **A2 复制就是复制** — 选中即写剪贴板（`pointerup` 上触发）。浏览器拒绝写入时退回原来
      那颗按钮，不是静默失败。
- [x] **A3 粘贴图片** — 捕获阶段的 `paste` 监听拿到文件 → 走上传通道 → 绝对路径填进命令行。
      render-check 里构造真的 `ClipboardEvent` + `DataTransfer` 验证（Playwright 的
      `dispatchEvent` 不带 clipboardData，一开始把一个好功能测成了坏的）。
- [x] **A4 右栏空间利用率** — 四个 tab 改成等宽 segmented control，头部 h-8 → h-10；CPU/内存/
      磁盘常驻底部一条 `SystemStrip`，不再要切到监控 tab 才看得到。
- [x] **A5 目录选择和新建目录** — `DirectoryPicker`：面包屑、上一级、新建即进入，外加一个
      手输路径的口子（HOME 之外的地方）。后端 `browse.Dirs` / `browse.Mkdir`，名字里带
      `/` 或 `..` 一律拒绝——把这条守卫拿掉，`../escape` 就穿出去了，测试会红。
- [x] **A6 icon** — `icon.svg` + 180/192/512 PNG + manifest。标签页、主屏图标、README 头图同一张。
- [ ] **A7 整体太丑、太缩** — 做了一轮：亮色不再是纯白、暗色不再是纯黑（三层 surface +
      elevated 令牌），终端嵌进圆角边框里，网格尺寸变成 chip，间距和字号统一。
      **仍然开着**，因为"艺术品级别"是你说了算，不是我说了算。

## B — 语言

- [x] **B1 简体中文** — `web/src/i18n.ts`，`useSyncExternalStore` + localStorage，跟随
      `navigator.languages`，设置页可切。两种语言写在同一行上，漏翻一眼就看得见；
      `i18n.test.ts` 断言两边都没有留下未替换的 `{占位符}`。
- [x] **B2 "状态靠猜"的文案** — 重写成中文，短句，不再像在怪人。

## C — 平台能力

- [x] **C1 PWA** — manifest + service worker。SW **故意不缓存任何东西**：一个会缓存的 SW 会
      把面板钉在旧版本上，而这个项目的整个前提是后端可以随时重启升级。
- [x] **C2 通知权限 + 手机推送** — 只在"进入 waiting 的那一次跳变"上推，第一份快照静默播种，
      页面在前台时不推。
- [x] **C3 每个会话的 CPU / 内存** — `GET /api/usage`：一次遍历 `/proc`，按 pane pid 把整棵进程树
      的占用汇总到会话上。百分比用**整机**做分母（和上面的机器仪表同一套），不用 top 的
      「100% = 一核」——两个数字挨在一起时，会话 310% 配机器 31% 只会让人得出唯一一个错误结论。
      pane 已经没了的会话是**缺席**而不是 0（0 是一个真实读数）。render-check 端到端钉住，
      把归属逻辑拿掉会红。
- [x] **C4 开放 API + API 文档** — API 令牌（存 SHA-256，只显示一次，可单独吊销）+
      `docs/api.md`。文档和路由表**双向**校验：漏写的接口和多写的段落都会让构建红。

## D — 部署

- [x] **D1 systemd 高优先级、防 OOM** — 实测：user unit 写 `OOMScoreAdjust=-500` 进程读到
      `100`，system unit 带 `User=` 才真的是 `-500`，而 `systemd-analyze verify` 两种都放行。
      于是多了一份 `deploy/vibepanel-system.service`，把这段测量写在文件头上。
      两份 unit 都加了 `CPUWeight=200` / `IOWeight=200` / `ManagedOOMPreference=avoid`。
- [x] **D2 开机自启 + 安装脚本** — `install.sh` 现在会替你 `enable-linger`（不需要 root），
      并且单元已经在跑时是 `restart` 而不是 `start`，还会告诉你发生了哪一种。
- [x] **D3 也能当普通进程跑** — `./vibepanel serve` 一直就行；Docker 也有，但镜像头部写清楚了
      在容器里重启面板会杀掉所有会话。

## E — 发布

- [x] **E1 专业易懂的 README** — `README.md`（英文）+ `README.zh-CN.md`（简体中文），带真实截图，
      截图由 `web/scripts/shots.mjs` 从真的面板拍出来，不是画的。
- [x] **E2 推到 GitHub public** — https://github.com/jiangmuran/vibepanel ，CI 在
      `.github/workflows/check.yml`，已经绿了。CI 从第一次跑开始一共抓到六个真问题，
      全都是「在我这台机器上看不见」的那一类：tmux 3.4 不认 `allow-set-title`；tmux 3.5
      以下会把 `-F` 输出里的 0x1F 分隔符转义成 `\037`（而且不转义反斜杠，所以没法还原）；
      旧 tmux 的整屏重绘里全是换行，被当成了「屏幕前进」从而清掉了等待中的铃；
      `Inspect` 的提前返回把 `events` 发成了 `null`；一个测试的铃和 attach 在抢跑；
      还有一个测试没有像面板那样设 `TERM`。

## F — 热升级

- [x] **F1 版本漂移要说话** — 重连后比对 `/api/health` 的 `version@commit`，变了就出横幅请你刷新。
      `restart-check` 里真的构建第二个二进制（`-X version.Version=v0.0.0-upgrade-check`）来验证，
      并且确认 `BEFORE_THE_UPGRADE` 的输出在升级后还在。把检测拿掉，这条检查会红。

---

## 记账规则

和 `docs/build-log.md` 一样：改完要能证明它改对了。界面的东西用 `make render-check` 驱动真浏览器，
逻辑的东西用测试，并且**把修复挪走确认测试会红**。

---

## 路上记下的

- **`TestDetachAllDoesNotTakeTwoSecondsPerSession` 偶发失败。** 在一次同时开着真面板（3 个会话
  在被轮询）并夹着重启的 `make check` 里红了一次，之后单跑和连跑三次全绿，失败信息没抓到。
  它的断言是"超过 8 秒才算失败"而整个测试只花 2.03 秒，所以红的多半是里面的 `Create`/`Attach`
  在竞争下 `Fatalf`，不是那条计时断言。未复现，未修，先记着。
- **亮色终端的 bright 行低于 AA。** 白底上的 bright green / bright yellow 对比度不到 4.5:1。
  这是终端配色的常规取舍（要和暗色保持同一套色相），记在这里当作一个明确的决定，而不是没看见。

---

## 第二批反馈（2026-08-27，使用中陆续提的）

**这一节是权威清单。** 上下文会被压缩，聊天记录会滚走，这个文件不会。每一条要么勾掉并
写上「怎么证明的」，要么留着。全部勾完之前不算做完。

### 终端本身

- [x] **T1 终端渲染有裂缝**（clawd 玩偶、方块画、进度条）——`@xterm/addon-webgl`
      一直在 `package.json` 里，但**从来没有被 `loadAddon` 过**，所以每个会话都跑在 xterm 的
      DOM renderer 上：一个 cell 一个 span、各自画背景、按 CSS 像素定位；cell 尺寸不是整数
      像素时它们不严丝合缝，缝隙透出背景 = 裂缝。加上 `customGlyphs`（方框字符按几何画）和
      `rescaleOverlappingGlyphs`。**截图已确认**：圆角框、实心进度条、渐变块全部无缝。
      顺带加了 `设置 → 渲染器` 的 DOM 回退开关，给 WebGL 有问题的机器留一条路。
- [x] **T2 上下滚不动** —— attach 时用 `capture-pane -S - -E -1` 给回放环预热。这段代码
      曾经有过、又被正确地删掉了（当时 tmux attach 以 `ESC[?1049h` 开头，之后一切都画在
      xterm 的 alternate screen 上，那里按定义没有 scrollback）；而删除时留下的注释点名了
      能改变这一点的设置，`terminal-overrides ',*:smcup@:rmcup@'`，那条设置后来正是为此加上了。
      挪掉预热，回放里就只剩 200 行历史里的 HIST_178..200——正是你看到的现象。
      `restart-check` 端到端钉住：重启后仍能滚回重启前一百行的第一行。
- [x] **T3 全屏（alt-screen）模式** —— 浏览器看不出 TUI 在跑：tmux 按 pane 模拟 alternate
      screen 并把它合成掉，而面板又故意让 tmux 自己的 client 不进 alternate screen（否则根本
      没有 scrollback）。所以改由 poller 告诉它——`#{alternate_on}`，`internal/tmux` 一直在读、
      从来没人用过。全屏程序在画的时候不提供滚动条，视图贴底。用「贴回去」而不是「冻住」：
      一个什么都不做的滚轮事件读起来像页面卡死，而且应用自己想要滚轮（这就是触摸板在 TUI
      里本来就正常的原因）。
- [x] **T15 滚动条挡字** —— **有两条滚动条**。xterm 6 把滚动挪到了
      `.xterm-scrollable-element`（div 做的覆盖式滚动条），而 `.xterm-viewport` 变成了一个
      没有子节点的背景层（实测 830×602、`kids: []`），xterm.css 却仍然给它
      `overflow-y: scroll`——于是画出第二条永久存在、完全无用的轨道。它占多少宽度是**平台
      决定**的：经典滚动条的机器上占 8–15px 且 fit addon 会减掉；覆盖式滚动条的机器上占 0，
      fit addon 于是多排几列，真正那条 14px 的滚动条就压在最后一列字上——这就是它在有的机器
      上挡字、有的不挡的原因。现在把死的那条藏掉，用 padding 确定性地留出宽度。
      另外两条给 `.xterm-viewport::-webkit-scrollbar` 的样式自 xterm 6 升级起就没作用过。
- [x] **T4 粘贴图片** —— 终端本来就在监听 paste，但只监听在它自己的 host 元素上，而 paste
      事件是送给「谁拿着键盘」的：焦点在侧栏一行、面板 tab、或者干脆什么都没选中的时候
      （也就是几乎每次点击之后），事件根本到不了终端。现在在 `document` 上捕获阶段监听，
      **只处理带文件的 paste**（纯文本 paste 是别人往输入框里粘东西，一律不碰）。
- [x] **T14 切 tab 后焦点回终端** —— 规则写在 `focus.ts` 里：**只有当人主动选了一个终端
      （侧栏会话、底部 tab、右栏 tab），并且在那个终端真正准备好接收焦点的那一刻没有别的东西
      占着键盘时**，焦点才移动。从点击处理器调用而不是从 effect：会话死掉时 `applyState` 会
      改选第一个会话，用 effect 会因为「别处一个构建结束了」把焦点从笔记框里抢走。

### 会话与恢复

- [x] **T5 关机/重启后恢复** —— 迁移 v9 记下真正能重跑的 argv（`sessions.command` 看着像
      答案其实不是：它存的是 `#{pane_current_command}`，poller 每两秒覆盖一次），每 30 秒把
      每个会话最后 2000 行 / 256 KiB 回滚存档，重启后**列出来问你要不要恢复**而不是默默拉起
      两打 agent。重建的是会话不是进程——agent 的上下文回不来，这一点在 pane 里用中英双语横幅
      和 header 上的「已恢复」标记说了两遍。实测：整机 6 个会话满历史存档 40.7ms，
      没输出时一轮 219µs；24 个会话全部顶到上限时数据库 6.2MB。
- [x] **T10 Codex** —— 一键安装 `notify`（按行改 TOML，插在第一个 table 之前；追加到末尾会
      变成 `[某表].notify`，能解析、能过 `codex doctor`、读回来也对，但 Codex 永远不读）。
      标题链路的断点：程序发现自己在 tmux 里时用 passthrough 发 OSC，passthrough 的定义就是
      tmux 不看，所以 `pane_title` 永不改变；而面板**本来就在解析那个标题**、送到浏览器、
      然后没人接。现在 `Live` 记住 PTY 上看到的标题，`deriveTitle` 把它作为第二来源。

- [x] **T5 关机/重启后 100% 恢复进度。** 迁移 v9 记下了真正能重跑的 argv（`launch_command`——
      原来的 `command` 是 `#{pane_current_command}`，是个标签不是命令行）；`session_scrollback`
      每 30 秒存一份有界的 `capture-pane`（2000 行 / 256 KiB），关机时再存一次，所以正常
      `reboot` 一行不丢，断电最多丢半分钟。恢复是**明确的**：面板列出每个会话会跑什么命令、
      在哪个目录，一键或逐个恢复，另有 per-session 的开机自动恢复开关（默认关——开机同时
      拉起二十几个 agent 比现在这个问题更糟）。回滚记录会被打到新 pane 的 history 里，
      上面压一条中英双语横幅说明「以上属于一个已经不存在的进程」，UI 里另有 `restored` 标记。
      **恢复不了的是进程本身**——agent 的上下文随进程一起没了，重跑命令启动的是一个全新的
      agent。文档、API 和界面都不许假装不是这样。
- [ ] **T10 Codex 自动安装**；以及 **codex 不会自动设标题**，进程内手动设也不行——这条链路
      没打通。*（已交给子任务，进行中）*

### 界面质感

- [x] **T6 弹窗质感与动画** —— 背景淡入 + 模糊，面板从 0.97 和下方 10px 进场（和别处同一条
      曲线），控件按下 3% 缩放；确认框和输入框都换成自制的（见 T11）。目录选择器补上了每个
      文件对话框三十年来都有的四个键。已截图确认。顺带修了 `shots.mjs`：它用三个猜测去找
      「新建项目」按钮（一个不存在的 testid、一个被翻译过的 title、一段英文按钮文字），
      于是一直悄悄什么都没拍到，只打印一行没人看的提示。
- [x] **T11 不用 raw toast** —— 五处 `window.confirm` / `window.prompt` 全部换掉：自制
      toast 栈（形状+颜色双重编码、自动消失、可手动关、手机上锚在软键盘上方的输入条之上）
      和确认对话框（Escape 取消、Enter 确认、焦点默认落在安全那一侧、危险按钮是窄的那个）。
      `no-raw-dialogs.test.ts` 同时防住 `window.confirm(` 和裸的 `confirm(`。
- [x] **T12 目录选择能搜索** —— 一个框，两件事，由你输的内容决定：以 `/` 或 `~` 开头就是
      路径（回车跳过去），否则就是筛选当前目录。手输路径这件事本来就有，但那个框在**底部**、
      在列表和「新建目录」下面——一个没人看得见已经被回答了的答案。现在框在顶部，自动聚焦，
      方向键不用离开输入框就能选列表。
- [ ] **T13 右边文件 tab 要能直接粘贴/上传文件，要能预览文件。**

### 数据

- [ ] **T18 右栏一个 token 统计 tab。** 每个 session 每天多少、总量多少；按项目 / 工具 /
      时间筛选；按月看；再加一张类似 GitHub 贡献图的活跃图（token 消耗 × 时间）。
      逻辑可以参考 vibeusage.com。

### 部署与运维

- [x] **T7 交互式安装脚本** —— 有 TTY 就问、`--yes` 保持无人值守；检测到 root 就提供系统
      服务安装（systemd + 开机自启），没有就说明原因退回 user unit；**绝不同时装两份**。
      新增 `make install-check`（72 条断言、几秒、不用 sudo），每条守卫都挪掉验证过会红。
- [x] **T16 安装方式对比** —— 两份 README 的「安装」开头各加了一张表：user service / 系统
      服务 / 直接跑 / Docker，四行分别写清楚**什么时候用、会话能活过什么、需不需要 root、
      开机是否自启**。Docker 那一行说的是实话：容器里什么都活不下来。
- [ ] **T8 面板内更新。**
- [ ] **T9 只读分享链接**——系统状态/宏观数据/大屏/全部 session 列表，在别的显示器上打开，
      能看见实时状态，也能看见断连状态。

### 路上产生的欠账

- [x] **T17 浏览器检查改读 buffer** —— `window.vibepanelScreen` + `scripts/lib/screen.mjs`，
      四个检查脚本的九个调用点全部改完；需要 DOM 几何的两处（CJK 进格宽度、触摸拖动的行位置）
      钉在 DOM renderer 上，这也正是设置里那个渲染器开关的另一个用处。render / stress /
      restart 三项全绿。

---
