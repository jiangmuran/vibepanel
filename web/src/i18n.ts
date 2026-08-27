import { useSyncExternalStore } from 'react'

/**
 * Two languages, no library.
 *
 * The project rule is no state library and no component library, and an i18n
 * package would be the third dependency doing what forty lines do. What is
 * actually needed here is a lookup, a stored preference, and a way to make the
 * tree re-render when that preference changes — which is `useSyncExternalStore`
 * plus a Map.
 *
 * Both languages sit on the same line of the dictionary rather than in two
 * files. A missing translation is then impossible to add by accident: there is
 * no second file to forget. It also means a reviewer reading one string sees
 * what it says in both, which is the thing that catches "确定" translating a
 * button that says "Remove".
 */
export type Lang = 'zh' | 'en'

const KEY = 'vibepanel.lang'

/**
 * What to show before anybody has chosen.
 *
 * `navigator.languages`, not `navigator.language`: a browser set to English
 * with Chinese second is telling you both, and the first is the answer. Any
 * `zh` variant counts — zh-CN, zh-TW, zh-Hans — because a Traditional reader
 * given Simplified is inconvenienced, and one given English is stuck.
 */
function detect(): Lang {
  const list = typeof navigator === 'undefined' ? [] : (navigator.languages ?? [navigator.language])
  for (const l of list) {
    if (!l) continue
    if (l.toLowerCase().startsWith('zh')) return 'zh'
    if (l.toLowerCase().startsWith('en')) return 'en'
  }
  return 'en'
}

function stored(): Lang | null {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'zh' || v === 'en' ? v : null
  } catch {
    // Private mode. Falling back to detection is right: a preference that
    // cannot be saved is still a preference for this tab.
    return null
  }
}

let current: Lang = stored() ?? detect()
const listeners = new Set<() => void>()

export function getLang(): Lang {
  return current
}

export function setLang(next: Lang) {
  if (next === current) return
  current = next
  try {
    localStorage.setItem(KEY, next)
  } catch {
    /* private mode: this tab still switches */
  }
  // The <html lang> matters to more than CSS: a screen reader picks its voice
  // from it, and so does the browser's own "translate this page" offer.
  try {
    document.documentElement.lang = next === 'zh' ? 'zh-CN' : 'en'
  } catch {
    /* no document in a unit test */
  }
  for (const fn of listeners) fn()
}

/** Subscribe to language changes, in the shape useSyncExternalStore wants. */
export function useLang(): Lang {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    getLang,
    // The server snapshot. There is no SSR here, but getServerSnapshot is not
    // optional in React 19 and returning the detected value is the honest one.
    getLang,
  )
}

type Entry = { zh: string; en: string }

/**
 * Every string the panel shows.
 *
 * Keyed by where it appears rather than by what it says, so that changing the
 * English does not orphan the Chinese.
 */
const DICT = {
  'app.projects': { zh: '项目', en: 'Projects' },
  'app.noSessionShort': { zh: '未选中会话', en: 'No session selected' },
  'app.projectsWaiting': { zh: '项目 —— {n} 个在等你', en: 'Projects — {n} waiting for you' },
  'app.addProject': { zh: '新建项目', en: 'Add a project' },
  'app.noProjects': { zh: '先加一个项目', en: 'Add a project to get started' },
  'app.noSession': { zh: '选一个会话，或者新建一个', en: 'Select or create a session' },
  'app.settings': { zh: '设置', en: 'Settings' },
  'app.signOut': { zh: '退出登录', en: 'Sign out' },
  'app.theme': { zh: '切换主题', en: 'Switch theme' },
  'app.gridSize': { zh: '所有观看端看到的网格', en: 'The grid every viewer of this session is seeing' },
  'app.restart': { zh: '重启', en: 'restart' },
  'app.hidePanel': { zh: '收起面板', en: 'Hide panel' },
  'app.showPanel': { zh: '展开面板', en: 'Show panel' },

  'set.tmuxConfigStale': {
    zh: '正在跑的 tmux server 用的是旧配置。tmux 只在启动时读一次配置文件，而面板从不杀自己的 server —— 所以升级之后，新配置在磁盘上、旧配置在内存里。今天不会坏任何东西，但配置里的改动没有生效。要应用它，代价是这个 socket 上的**全部会话**：',
    en: 'The running tmux server started with an older config. tmux reads its config once, at start-server, and the panel never kills its server — so after an upgrade the new file is on disk and the old settings are in memory. Nothing is broken today, but changes in it are not in effect. Applying them costs every session on this socket:',
  },
  'set.tmuxConfigUnknown': {
    zh: '正在跑的 tmux server 早于这项检查，问不出来。重启它之后才能知道。',
    en: 'The running tmux server predates this check, so the question has no answer until it is restarted.',
  },
  'set.tmuxConfigLabel': { zh: 'tmux 配置', en: 'tmux config' },

  'auth.setupTitle': { zh: '初始化 vibepanel', en: 'Set up vibepanel' },
  'auth.setupHint': {
    zh: '把服务端打印的一次性 token 粘进来，然后设一个账号。',
    en: 'Paste the one-time token the server printed, then choose an account.',
  },
  'auth.signInHint': { zh: '登录后就能看到你的会话。', en: 'Sign in to reach your sessions.' },
  'auth.setupToken': { zh: '一次性 token', en: 'Setup token' },
  'auth.username': { zh: '用户名', en: 'Username' },
  'auth.password': { zh: '密码', en: 'Password' },
  'auth.passwordHint': {
    zh: '至少 12 个字符。长度比标点管用。',
    en: 'At least 12 characters. Length beats punctuation.',
  },
  'auth.working': { zh: '处理中…', en: 'Working…' },
  'auth.create': { zh: '创建账号', en: 'Create account' },
  'auth.signIn': { zh: '登录', en: 'Sign in' },
  'auth.or': { zh: '或', en: 'or' },
  'auth.usePasskey': { zh: '用 passkey 登录', en: 'Use a passkey' },
  'auth.noPasskeys': { zh: 'passkey 用不了：{why}。', en: 'Passkeys unavailable: {why}.' },
  'auth.notSupported': { zh: '这里不支持', en: 'not supported here' },

  'app.stale': {
    zh: '面板已经停止记录会话在做什么。终端本身不受影响。',
    en: 'The panel has stopped recording what the sessions are doing. The terminals are unaffected.',
  },
  'app.showTerminals': { zh: '展开底部终端', en: 'Show terminals' },
  'app.sortByActivity': { zh: '改回按活跃度排序 —— 你的排列会留着', en: 'Sort by recent activity instead — your arrangement is kept' },
  'app.restartHint': { zh: '在同一个 pane 里重跑这个会话的命令', en: "Restart this session's command in the same pane" },
  'app.showPanelShort': { zh: '展开侧栏', en: 'Show side panel' },
  'app.clipboardRefused': {
    zh: '浏览器拒绝了一次不是由点击触发的剪贴板写入',
    en: 'The browser refused a clipboard write that did not come from a click',
  },
  'app.closeProjects': { zh: '关闭项目列表', en: 'Close projects' },
  'files.escapeLink': {
    zh: '这个链接指向项目外面，面板不会打开它。',
    en: 'This link points outside the project. The panel will not open it.',
  },
  'todos.addShort': { zh: '添加', en: 'Add' },
  'err.tryAgain': { zh: '再试一次', en: 'Try again' },
  'settings.passwordChanged': {
    zh: '已修改。其他浏览器都已被登出。',
    en: 'Changed. Every other browser has been signed out.',
  },
  'key.interrupt': { zh: '中断 (Ctrl-C)', en: 'Interrupt (Ctrl-C)' },
  'key.enter': { zh: '回车', en: 'Enter' },
  'key.sticky': { zh: '作用于下一个按键', en: 'Applies to the next key' },
  'key.up': { zh: '上', en: 'Up' },
  'key.down': { zh: '下', en: 'Down' },
  'key.left': { zh: '左', en: 'Left' },
  'key.right': { zh: '右', en: 'Right' },
  'key.home': { zh: '行首', en: 'Home' },
  'key.end': { zh: '行尾', en: 'End' },
  'key.pageUp': { zh: '上一页', en: 'Page up' },
  'key.pageDown': { zh: '下一页', en: 'Page down' },

  'session.new': { zh: '新建会话', en: 'New session' },
  'session.kill': { zh: '结束会话', en: 'Kill session' },
  'session.rename': { zh: '重命名', en: 'Rename' },
  'session.pin': { zh: '置顶', en: 'Pin' },
  'session.unpin': { zh: '取消置顶', en: 'Unpin' },
  'session.markAs': { zh: '{state} —— 点一下改成{other}', en: '{state} — click to mark as {other}' },
  'session.markWaiting': { zh: '等你处理', en: 'waiting' },
  'session.markDone': { zh: '已完成', en: 'done' },
  'session.exited': { zh: '已退出', en: 'Exited' },
  'session.waiting': { zh: '等你处理', en: 'Waiting for you' },
  'session.working': { zh: '工作中', en: 'Working' },
  'session.done': { zh: '已完成', en: 'Done' },

  'panel.files': { zh: '文件', en: 'Files' },
  'panel.monitor': { zh: '监控', en: 'Monitor' },
  'panel.notes': { zh: '笔记', en: 'Notes' },
  'panel.todos': { zh: '待办', en: 'Todo' },
  'panel.splitOn': { zh: '笔记和待办一起显示', en: 'Show notes and todo together' },
  'panel.splitOff': { zh: '一次显示一个', en: 'Show one at a time' },

  'files.refresh': { zh: '刷新', en: 'Refresh' },
  'files.download': { zh: '下载', en: 'Download' },
  'files.empty': { zh: '这个目录是空的', en: 'Nothing here' },
  'files.escapes': { zh: '指向项目之外', en: 'points outside the project' },

  'todos.add': { zh: '加一条待办', en: 'Add an item' },
  'todos.leftOf': { zh: '{done} / {total} 已完成', en: '{left} of {total} left' },
  'todos.markDone': { zh: '标记为完成', en: 'Mark done' },
  'todos.markNotDone': { zh: '标记为未完成', en: 'Mark not done' },
  'todos.empty': { zh: '还没有待办', en: 'Nothing to do' },

  'notes.saved': { zh: '已保存', en: 'Saved' },
  'notes.saving': { zh: '保存中…', en: 'Saving…' },
  'notes.loading': { zh: '读取中…', en: 'Loading…' },
  'notes.unsaved': { zh: '未保存', en: 'Unsaved' },
  'notes.error': { zh: '保存失败', en: 'Could not save' },
  'notes.conflict': { zh: '别处改过了', en: 'Changed elsewhere' },
  'notes.placeholder': { zh: '这个项目的笔记，Markdown', en: 'Notes for this project, in Markdown' },

  'monitor.cpu': { zh: 'CPU', en: 'CPU' },
  'monitor.memory': { zh: '内存', en: 'Memory' },
  'monitor.disk': { zh: '磁盘', en: 'Disk' },
  'monitor.swap': { zh: '交换', en: 'Swap' },
  'monitor.cores': { zh: '{n} 核', en: '{n} cores' },
  'monitor.free': { zh: '{size} 可用', en: '{size} free' },
  'monitor.reading': { zh: '读取中…', en: 'Reading…' },
  'monitor.sampling': { zh: '采样中…', en: 'sampling…' },
  'monitor.unavailable': { zh: '读不到', en: 'unavailable' },
  'monitor.of': { zh: '{used} / {total}', en: '{used} of {total}' },
  'monitor.up': { zh: '已运行 {d}', en: 'up {d}' },
  'monitor.perSession': { zh: '各会话占用', en: 'Per session' },
  'monitor.noSessions': { zh: '没有在跑的会话', en: 'Nothing running' },
  'monitor.procs': { zh: '{n} 个进程', en: '{n} processes' },
  'monitor.oneProc': { zh: '1 个进程', en: '1 process' },
  'monitor.noProc': {
    zh: '这台机器读不到 /proc，量不了每个会话的占用。',
    en: 'No /proc here, so per-session usage cannot be measured.',
  },
  'monitor.strip': { zh: '点开监控标签看完整数据', en: 'Open the monitor tab for the rest' },

  'dir.title': { zh: '选一个目录', en: 'Choose a directory' },
  'dir.up': { zh: '上一层', en: 'Up one level' },
  'dir.empty': { zh: '这里没有子目录 — 可以直接选它，或者新建一个', en: 'No subdirectories — use this one, or make one' },
  'dir.truncated': { zh: '目录太多，只显示了 {shown} / {total} 个', en: 'Showing {shown} of {total}' },
  'dir.newFolder': { zh: '在这里新建目录', en: 'New folder here' },
  'dir.newName': { zh: '新目录的名字', en: 'Name' },
  'dir.create': { zh: '创建', en: 'Create' },
  'dir.manual': { zh: '或直接输入路径，支持 ~', en: 'Or type a path — ~ works' },
  'dir.cancel': { zh: '取消', en: 'Cancel' },
  'dir.use': { zh: '使用这个目录', en: 'Use this directory' },

  'files.up': { zh: '上一层', en: 'Up one level' },
  'files.reread': { zh: '重新读取这个目录', en: 'Read this directory again' },
  'files.reading': { zh: '读取中…', en: 'Reading…' },
  'files.truncated': { zh: '目录太大，只显示了 {shown} / {total} 项', en: 'Showing {shown} of {total} items' },
  'files.downloadOne': { zh: '下载 {name}', en: 'Download {name}' },
  'files.panel': { zh: '项目文件；可以把文件拖进来或粘贴进来', en: 'Project files — drop or paste files here' },
  'files.choose': { zh: '上传到这个目录', en: 'Upload into this directory' },

  'upload.dropHere': { zh: '松手就上传到 {dir}', en: 'Drop to upload into {dir}' },
  'upload.one': { zh: '上传中…', en: 'Uploading a file…' },
  'upload.many': { zh: '{n} 个文件上传中…', en: 'Uploading {n} files…' },
  'upload.doneOne': { zh: '已上传 1 个文件', en: '1 file uploaded' },
  'upload.doneMany': { zh: '已上传 {n} 个文件', en: '{n} files uploaded' },
  'upload.failed': { zh: '上传失败', en: 'Upload failed' },

  'preview.title': { zh: '预览', en: 'Preview' },
  'preview.close': { zh: '关闭预览', en: 'Close preview' },
  'preview.loading': { zh: '读取中…', en: 'Reading…' },
  'preview.lines': { zh: '{n} 行', en: '{n} lines' },
  'preview.oneLine': { zh: '1 行', en: '1 line' },
  'preview.empty': { zh: '空文件', en: 'Empty file' },
  // Truncation is never silent. A preview that just stops is the same defect as
  // a directory listing that just stops, which this panel already refuses.
  'preview.truncated': {
    zh: '文件太长，这里只显示了前 {n} 行 —— 下载下来看全部。',
    en: 'Too long to show here; these are the first {n} lines. Download it for the rest.',
  },
  'preview.tooBig': {
    zh: '{size} 超过了 {limit} 的预览上限。预览是点一下就发生的事，所以它有上限；下载没有。',
    en: 'At {size} this is past the {limit} preview limit. A preview happens on one click, so it has a ceiling; the download does not.',
  },
  'preview.none': {
    zh: '这个文件不是文本、图片或 PDF（{size}），没法在这里显示。',
    en: 'Not text, an image or a PDF ({size}), so there is nothing honest to show here.',
  },
  'preview.pdfFallback': {
    zh: '这个浏览器不显示内嵌 PDF。下载下来看。',
    en: 'This browser will not show a PDF inline. Download it instead.',
  },
  'preview.imageAlt': { zh: '{name} 的预览', en: 'Preview of {name}' },
  'preview.open': { zh: '预览 {name}', en: 'Preview {name}' },
  'preview.enter': { zh: '进入 {name}', en: 'Open {name}' },
  'bottom.close': { zh: '关闭终端', en: 'Close terminal' },
  'bottom.hide': { zh: '收起终端', en: 'Hide terminals' },
  'bottom.new': { zh: '新建终端', en: 'New terminal' },
  'bottom.empty': { zh: '这里还没有终端', en: 'No terminals here yet' },
  'bottom.resize': { zh: '拖动调整高度', en: 'Drag to resize' },
  'panel.resize': { zh: '拖动调整宽度', en: 'Drag to resize' },
  'panel.noProject': { zh: '还没有选中项目', en: 'No project selected' },
  'project.reorder': { zh: '拖动排序', en: 'Drag to reorder' },
  'project.remove': { zh: '把这个项目从面板移除', en: 'Remove this project from the panel' },
  'project.orderManual': { zh: '回到你排好的顺序', en: 'Back to the order you arranged' },
  'todos.edit': { zh: '双击编辑', en: 'Double click to edit' },
  'todos.delete': { zh: '删除', en: 'Delete' },
  'compose.placeholder': { zh: '输入命令…', en: 'Type a command…' },
  'compose.send': { zh: '发送', en: 'Send' },
  'settings.title': { zh: '设置', en: 'Settings' },
  'settings.close': { zh: '关闭', en: 'Close' },
  'settings.language': { zh: '语言', en: 'Language' },
  'settings.languageZh': { zh: '简体中文', en: '简体中文' },
  'settings.languageEn': { zh: 'English', en: 'English' },

  'tok.title': { zh: 'API 令牌', en: 'API tokens' },
  'tok.why': {
    zh: '给 agent 或脚本用来管理这个面板。它不会过期，可以单独吊销 —— 改密码不影响它，吊销它也不影响密码。接口文档在 docs/api.md。',
    en: 'For an agent or a script to drive this panel. They do not expire and are revoked one at a time — changing your password leaves them alone, and revoking one leaves your password alone. The API is documented in docs/api.md.',
  },
  'tok.name': { zh: '给它起个名字', en: 'What is it for' },
  'tok.create': { zh: '新建令牌', en: 'New token' },
  'tok.once': {
    zh: '只显示这一次。数据库里存的是它的哈希，关掉就找不回来了。',
    en: 'Shown once. The database keeps only a hash of it, so closing this is losing it.',
  },
  'tok.copy': { zh: '复制', en: 'Copy' },
  'tok.copied': { zh: '已复制', en: 'Copied' },
  'tok.done': { zh: '我存好了', en: 'I have saved it' },
  'tok.none': { zh: '还没有令牌', en: 'None yet' },
  'tok.neverUsed': { zh: '从未使用', en: 'never used' },
  'tok.revoke': { zh: '吊销', en: 'Revoke' },

  'set.passkeysWhy': {
    zh: '用这台设备代替密码登录。密码依然有效 —— passkey 是多一条路，永远不是唯一的路。',
    en: 'Sign in with this device instead of a password. The password keeps working — a passkey is an addition, never the only way in.',
  },
  'set.working': { zh: '处理中…', en: 'Working…' },
  'set.hide': { zh: '收起', en: 'Hide' },

  'set.status': { zh: '状态', en: 'Status' },
  'set.version': { zh: '版本', en: 'Version' },
  'set.uptime': { zh: '已运行', en: 'Uptime' },
  'set.sessions': { zh: '会话', en: 'Sessions' },
  'set.viewers': { zh: '观看端', en: 'Viewers' },
  'set.socket': { zh: 'tmux socket', en: 'tmux socket' },
  'set.data': { zh: '数据目录', en: 'Data' },
  'set.listening': { zh: '监听', en: 'Listening' },
  'set.tls': { zh: 'TLS', en: 'TLS' },
  'set.cert': { zh: '证书', en: 'Certificate' },
  'set.access': { zh: '访问来源', en: 'Access' },
  'set.signedIn': { zh: '当前登录', en: 'Signed in as' },
  'set.reporting': { zh: '状态上报', en: 'State reporting' },
  'set.reportingWhy': {
    zh: '没有它，面板只能从输出推测会话在干什么 —— 分得出“在动”和“安静”，也收得到终端响铃，但分不出“已完成”和“等你处理”。装上之后，是 agent 自己说。',
    en: 'Without this the panel infers what a session is doing from its output, which can tell working from quiet and sees the terminal bell, but cannot tell finished from waiting for you. With it, the agent says which.',
  },
  'set.claudeCode': { zh: 'Claude Code', en: 'Claude Code' },
  'set.codex': { zh: 'Codex', en: 'Codex' },
  'set.settingsFile': { zh: '配置文件', en: 'Settings file' },
  'set.notInstalled': { zh: '未安装', en: 'not installed' },
  // "installed" is a claim about a file, not about behaviour: the panel has
  // read a config, it has not heard from an agent.
  'set.installedEvents': { zh: '已安装，{n} 个事件', en: 'installed for {n} events' },
  'set.installedNotify': { zh: '已安装（notify）', en: 'installed as notify' },
  'set.install': { zh: '安装', en: 'Install' },
  // Codex has one notify slot for one event, so a Codex session can report
  // "waiting" and nothing else. Saying so on the page is cheaper than the
  // runbook section that exists because nobody knew.
  'set.codexOneEvent': {
    zh: 'Codex 只有一个 notify，一条命令对一个事件，所以只能上报“等你处理”。',
    en: 'Codex has one notify command for one event, so it can only ever report waiting.',
  },
  'set.showWrites': { zh: '看看会写什么', en: 'Show what it writes' },
  'set.remove': { zh: '移除', en: 'Remove' },
  'set.password': { zh: '密码', en: 'Password' },
  'set.passwordWhy': {
    zh: '改密码会让其他所有浏览器退出登录，当前这个不受影响。',
    en: 'Changing it signs every other browser out. This one stays signed in.',
  },
  'set.currentPassword': { zh: '当前密码', en: 'Current password' },
  'set.newPassword': { zh: '新密码', en: 'New password' },
  'set.change': { zh: '修改', en: 'Change' },
  'set.passkeys': { zh: 'Passkey', en: 'Passkeys' },
  'set.noPasskeys': { zh: '还没有绑定', en: 'None registered.' },
  'set.activity': { zh: '最近活动', en: 'Recent activity' },

  'term.takeControl': { zh: '接管', en: 'take control' },
  'term.takeControlWhy': {
    zh: '另一个观看端拥有这个网格（{cols}×{rows}），你这边能放下 {mine}。你仍然可以打字。接管会让所有人重排，正在跑的 TUI 会有反应。',
    en: 'Another viewer owns this grid ({cols}x{rows}); this window fits {mine}. You can still type. Taking over reflows it for everyone, which a running TUI will notice.',
  },

  'notify.waitingTitle': { zh: '有 agent 在等你', en: 'An agent is waiting' },
  'notify.waitingBody': { zh: '{name} 停下来等你处理了', en: '{name} has stopped and needs you' },
  'notify.title': { zh: '通知', en: 'Notifications' },
  'notify.explain': {
    zh: '当有会话变成“等你处理”而你没在看面板时，推一条通知。需要页面开着 —— 后台标签页或装成 App 都算。',
    en: 'A notification when a session starts waiting and you are looking somewhere else. Needs the page to be alive — a background tab or an installed app both count.',
  },
  'notify.enable': { zh: '打开通知', en: 'Turn on notifications' },
  'notify.on': { zh: '已打开', en: 'On' },
  'notify.denied': { zh: '浏览器拒绝了通知权限，要在浏览器设置里改', en: 'The browser refused permission; change it in the browser\'s own settings' },
  'notify.insecure': {
    zh: '通知需要 HTTPS（localhost 除外）',
    en: 'Notifications need HTTPS, or localhost',
  },

  'upgrade.title': { zh: '面板已经升级', en: 'The panel has been upgraded' },
  'upgrade.body': {
    zh: '这个标签页还在跑旧版界面。你的会话没有受到影响 —— 它们属于 tmux，不属于面板进程。',
    en: 'This tab is still running the old interface. Your sessions are unaffected — they belong to tmux, not to the panel process.',
  },
  'upgrade.reload': { zh: '刷新载入新版', en: 'Reload' },
  'upgrade.later': { zh: '待会儿', en: 'Later' },

  'guessed.installed': {
    zh: '状态还在靠猜：装 hook 之前就开着的会话要重开一次才会上报。在里面输入 /hooks，或者重启那个 agent。',
    en: 'States are still guessed. Sessions open before reporting was installed keep guessing until they reload — run /hooks in each, or restart the agent.',
  },
  'guessed.notInstalled': {
    zh: '状态是从输出猜的，只有终端响铃能传到面板，而大多数 agent 不响铃 —— 所以“等你处理”会被漏掉。点这里打开状态上报。',
    en: 'States are guessed from output, and only the terminal bell reaches the panel. The agent most people run here does not ring one, so "waiting for you" will be missed. Turn on state reporting.',
  },
} satisfies Record<string, Entry>

export type Key = keyof typeof DICT

/**
 * One string, in the current language.
 *
 * Substitution is `{name}` and deliberately not a template literal in the
 * caller: "3 of 5 left" and "5 个里还剩 3 个" put the numbers in different
 * places, and a caller that concatenates has already decided the order.
 */
export function t(key: Key, params?: Record<string, string | number>): string {
  const entry = DICT[key]
  let out = entry[current] ?? entry.en
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      out = out.replaceAll(`{${k}}`, String(v))
    }
  }
  return out
}
