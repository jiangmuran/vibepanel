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
- [ ] **T2 上下滚不动。** 面板里滚不动历史。
- [ ] **T3 全屏（alt-screen）模式要兼容。** 触摸板上下滑正常，但右边滚动条一拖就跳到跑
      claude 之前的记录——alt-screen 下不该有 scrollback，滚动条不该动主 buffer。
- [ ] **T15 右边的滚动条会挡住字。**
- [ ] **T4 粘贴图片粘不进去（原生）。**
- [ ] **T14 切换 tab 之后焦点自动回到终端。**

### 会话与恢复

- [ ] **T5 关机/重启后 100% 恢复进度。** tmux 活不过重启，需要记录并在开机后重建。
- [ ] **T10 Codex 自动安装**；以及 **codex 不会自动设标题**，进程内手动设也不行——这条链路
      没打通。*（已交给子任务，进行中）*

### 界面质感

- [ ] **T6 弹窗没质感、没动画、操作不顺手。** 已做：入场动效（背景淡入+模糊、面板
      0.97→1）、按压反馈 `vp-press`、目录选择器全键盘导航（↑↓ Home End 移动、Enter/→ 进入、
      Backspace/← 上一层）。**还没验收。**
- [ ] **T11 所有提示不能用 raw toast**（`alert`/`confirm`/浏览器原生），要自己做一个好看的。
- [ ] **T12 目录选择要能搜索**，也要能手动打路径（手动输入已有，搜索没有）。
- [ ] **T13 右边文件 tab 要能直接粘贴/上传文件，要能预览文件。**

### 数据

- [ ] **T18 右栏一个 token 统计 tab。** 每个 session 每天多少、总量多少；按项目 / 工具 /
      时间筛选；按月看；再加一张类似 GitHub 贡献图的活跃图（token 消耗 × 时间）。
      逻辑可以参考 vibeusage.com。

### 部署与运维

- [ ] **T7 交互式安装脚本**，有 root 时可以完全安装（注册系统服务、开机自启）。
      *（已交给子任务，进行中）*
- [ ] **T16 给不同人一个简单清晰的安装方式，并在不同层面做清晰的对比**（谁该用哪种装法）。
- [ ] **T8 面板内更新。**
- [ ] **T9 只读分享链接**——系统状态/宏观数据/大屏/全部 session 列表，在别的显示器上打开，
      能看见实时状态，也能看见断连状态。

### 路上产生的欠账

- [ ] **T17 浏览器检查还在读 DOM 取终端文本。** 装上 GPU renderer 之后 `.xterm-rows` 是空的，
      十三条断言同时红。已加 `window.vibepanelScreen`（读 xterm buffer）和
      `scripts/lib/screen.mjs`，改了一部分调用点，**还没改完**。

---
