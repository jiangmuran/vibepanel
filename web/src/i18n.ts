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
    zh: '正在跑的 tmux server 用的是旧配置。要应用新配置，得结束这个 socket 上的全部会话：',
    en: 'The running tmux server started with an older config. Applying the new one ends every session on this socket:',
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
  'panel.tokens': { zh: '用量', en: 'Tokens' },
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
  'dir.search': { zh: '筛选，或输入以 / 或 ~ 开头的路径', en: 'Filter, or type a path starting with / or ~' },
  'dir.jumpHint': { zh: '回车跳到这个路径', en: 'Enter goes to this path' },
  'dir.filterHint': { zh: '{n} 个匹配 —— 回车进入选中的那个', en: '{n} match — Enter opens the highlighted one' },
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
    zh: '给 agent 或脚本用。不会过期，可以单独吊销。',
    en: 'For an agent or a script. They do not expire and are revoked one at a time.',
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
  'wh.title': { zh: '推送通知', en: 'Push notifications' },
  'wh.why': {
    zh: '会话变成「等你处理」时，往你的手机发一条。浏览器通知要面板开着，这个不用。',
    en: 'Send one to your phone when a session starts waiting. A browser notification needs the panel open; this does not.',
  },
  'wh.name': { zh: '名字', en: 'Name' },
  'wh.enabled': { zh: '启用', en: 'On' },
  'wh.test': { zh: '发一条试试', en: 'Send a test' },
  'wh.remove': { zh: '删掉', en: 'Remove' },
  'wh.save': { zh: '保存', en: 'Save' },
  'wh.body': { zh: '请求体，可用 {session} {state} {project} {url} {time}', en: 'Body — {session} {state} {project} {url} {time}' },
  'wh.testOk': { zh: '发出去了', en: 'Sent' },
  'wh.testFailed': { zh: '没发出去', en: 'Not sent' },
  'wh.saveFailed': { zh: '没保存上', en: 'Not saved' },
  'wh.placeholder': { zh: '还有 YOUR_ 没替换成你自己的 key。', en: 'A YOUR_ placeholder is still there instead of your key.' },
  'wh.presetBark': { zh: 'Bark', en: 'Bark' },
  'wh.presetNtfy': { zh: 'ntfy', en: 'ntfy' },
  'wh.presetServerChan': { zh: 'Server酱', en: 'ServerChan' },
  'wh.presetCustom': { zh: '自定义', en: 'Custom' },
  'upd.title': { zh: '更新', en: 'Updates' },
  'upd.check': { zh: '看看有没有新版本', en: 'Check for a new version' },
  'upd.checking': { zh: '正在问…', en: 'Asking…' },
  'upd.upToDate': { zh: '已经是最新的（{v}）', en: 'Up to date ({v})' },
  'upd.available': { zh: '有新版本：{v}（当前 {cur}）', en: '{v} is available (running {cur})' },
  'upd.noRelease': { zh: '这个仓库还没有发布过版本', en: 'That repository has no releases yet' },
  'upd.noAsset': { zh: '{v} 没有给这个平台的包', en: '{v} has no archive for this platform' },
  'upd.unreachable': { zh: '连不上 GitHub：{why}', en: 'Could not reach GitHub: {why}' },
  'upd.devBuild': {
    zh: '这是一个开发版构建，没法和发布版比较——面板不会用一个它读不懂的版本号说服自己去覆盖自己。',
    en: 'This is a development build, so there is nothing to compare against — the panel will not talk itself into overwriting itself over a version string it cannot read.',
  },
  'upd.apply': { zh: '更新并重启', en: 'Update and restart' },
  'upd.applying': { zh: '正在下载并校验…', en: 'Downloading and checking…' },
  'upd.confirmTitle': { zh: '更新到 {v}？', en: 'Update to {v}?' },
  'upd.confirmBody': {
    zh: '下载、校验、替换二进制，然后重启面板。**会话不受影响。**',
    en: 'Downloads, verifies and replaces the binary, then restarts the panel. **Your sessions are unaffected.**',
  },
  'upd.done': { zh: '已装上 {v}，正在重启面板…', en: 'Installed {v}; the panel is restarting…' },
  'upd.doneNoRestart': {
    zh: '已装上 {v}。这个面板不是 systemd 起的，所以要你自己重启它才会生效：{why}',
    en: 'Installed {v}. This panel was not started by systemd, so it takes effect the next time you start it yourself: {why}',
  },
  'upd.failed': { zh: '更新失败：{why}', en: 'Update failed: {why}' },
  'upd.notes': { zh: '这个版本的说明', en: 'What changed' },
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
    zh: '装上之后由 agent 自己上报状态，不再靠输出去猜。',
    en: 'The agent reports its own state instead of the panel guessing from output.',
  },
  'set.claudeCode': { zh: 'Claude Code', en: 'Claude Code' },
  'set.opencode': { zh: 'opencode', en: 'opencode' },
  'set.installedPlugin': { zh: '已装插件', en: 'Plugin installed' },
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
    zh: '另一个观看端拥有这个网格（{cols}×{rows}），你这边能放下 {mine}。接管会让所有人重排。',
    en: 'Another viewer owns this grid ({cols}x{rows}); this window fits {mine}. Taking over reflows it for everyone.',
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

  // What the panel says in the corner when something has just happened, and
  // what it asks before something cannot be taken back. Both used to be the
  // browser's: window.alert in the operating system's language and
  // window.confirm in the operating system's chrome, neither of them reachable
  // from this dictionary at all.
  'toast.dismiss': { zh: '关掉这条提示', en: 'Dismiss' },
  'toast.uploadingOne': { zh: '正在上传 1 个文件…', en: 'Uploading one file…' },
  'toast.uploadingMany': { zh: '正在上传 {n} 个文件…', en: 'Uploading {n} files…' },
  'toast.uploadedOne': {
    zh: '已上传，路径打在命令行上了',
    en: 'Uploaded. The path is on the command line.',
  },
  'toast.uploadedMany': {
    zh: '{n} 个文件已上传，路径打在命令行上了',
    en: '{n} files uploaded. The paths are on the command line.',
  },
  'toast.uploadFailed': { zh: '上传失败', en: 'The upload failed' },
  'toast.copied': { zh: '已复制到剪贴板', en: 'Copied to your clipboard' },
  'toast.passkeyGone': { zh: '这个 passkey 删不掉', en: 'That passkey could not be removed' },

  'ask.cancel': { zh: '取消', en: 'Cancel' },
  'ask.remove': { zh: '移除', en: 'Remove' },
  'ask.kill': { zh: '结束它', en: 'Kill it' },
  'ask.add': { zh: '添加', en: 'Add' },
  'ask.removeProjectTitle': { zh: '把 {name} 从面板移除？', en: 'Remove {name} from the panel?' },
  'ask.removeProjectNone': {
    zh: '它现在没有会话。目录本身不动。',
    en: 'It has no sessions right now. The directory itself is left alone.',
  },
  'ask.removeProjectOne': {
    zh: '它的 1 个会话会被杀掉。目录本身不动。',
    en: 'Its 1 session will be killed. The directory itself is left alone.',
  },
  'ask.removeProjectMany': {
    zh: '它的 {n} 个会话会被杀掉。目录本身不动。',
    en: 'Its {n} sessions will be killed. The directory itself is left alone.',
  },
  'ask.killTitle': { zh: '结束 {name}？', en: 'Kill {name}?' },
  'ask.killBody': {
    zh: '里面的进程会被终止。pane 和滚动历史会留着，所以还能回去看它最后说了什么。',
    en: 'The process is terminated. The pane and its scrollback stay, so what it said last is still there to read.',
  },
  'ask.revokeTitle': { zh: '吊销 {name}？', en: 'Revoke {name}?' },
  'ask.revokeBody': {
    zh: '拿着 {prefix}… 的程序会立刻失效，而且没法撤销 —— 令牌只在创建时显示过一次。',
    en: 'Anything holding {prefix}… stops working immediately, and it cannot be undone: the token was shown once, when it was made.',
  },
  'ask.passkeyNameTitle': { zh: '给这个 passkey 起个名字', en: 'Name this passkey' },
  'ask.passkeyNameBody': {
    zh: '写清楚是哪台设备。以后要移除的时候，这是你唯一认得出它的东西。',
    en: 'Say which device it is. When you come to remove one, the name is all you have to tell them apart.',
  },
  'ask.passkeyNameField': { zh: '名字', en: 'Name' },
  'ask.passkeyNameDefault': { zh: '这台设备', en: 'This device' },
  'ask.removePasskeyTitle': { zh: '移除 {name}？', en: 'Remove {name}?' },
  'ask.removePasskeyBody': {
    zh: '这台设备就不能再免密码登录了。密码不受影响。',
    en: 'That device can no longer sign in without a password. Your password is unaffected.',
  },

  // Restoring after the machine restarted.
  //
  // Every one of these is written to be honest about the half that cannot come
  // back. A restore that reads as "your work is back" is worse than no restore
  // at all, because somebody believes it.
  'restore.title': { zh: '有会话没能活过这次重启', en: 'Sessions did not survive the restart' },
  'restore.body': {
    zh: '{n} 个会话没能活过重启。可以按原来的命令和目录重建，并放回重启前的回滚记录。',
    en: '{n} sessions did not survive the restart. They can be rebuilt with the command and directory they had, with the scrollback from before.',
  },
  'restore.warning': {
    zh: '进程本身回不来。agent 的上下文随进程一起没了，重跑命令启动的是一个全新的、什么都不记得的 agent。',
    en: 'The processes cannot come back. An agent’s context went with its process, and re-running the command starts a new one that remembers none of it.',
  },
  'restore.open': { zh: '看看要恢复哪些', en: 'Choose what to restore' },
  'restore.later': { zh: '待会儿', en: 'Later' },
  'restore.dialogTitle': { zh: '恢复会话', en: 'Restore sessions' },
  'restore.selectAll': { zh: '全选', en: 'Select all' },
  'restore.selectNone': { zh: '全不选', en: 'Select none' },
  'restore.willRun': { zh: '将运行', en: 'will run' },
  'restore.willRunShell': {
    zh: '将启动一个登录 shell —— 这个会话是在面板开始记录命令之前建的，原来跑的是什么已经无从得知',
    en: 'will start a login shell — this session predates the panel recording commands, so what it was running is not known',
  },
  'restore.willRunShellKnown': {
    zh: '将启动一个登录 shell —— 它当初就是这么建的',
    en: 'will start a login shell — that is what it was created as',
  },
  'restore.scrollbackFrom': { zh: '回滚记录：{when}', en: 'scrollback from {when}' },
  'restore.noScrollback': { zh: '没有存下回滚记录', en: 'no scrollback was archived' },
  'restore.onBoot': { zh: '以后开机自动恢复', en: 'Restore this one automatically next time' },
  'restore.onBootWhy': {
    zh: '下次启动直接重建，不再问。',
    en: 'Rebuild it at startup without asking.',
  },
  'restore.go': { zh: '恢复选中的 {n} 个', en: 'Restore {n}' },
  'restore.working': { zh: '恢复中…', en: 'Restoring…' },
  'restore.failed': { zh: '{n} 个没能恢复', en: '{n} could not be restored' },
  'restore.close': { zh: '关闭', en: 'Close' },
  'restore.gone': { zh: '这个会话的 tmux 会话没了，可以重建', en: 'The tmux session is gone; rebuild it' },
  'restore.badge': { zh: '已恢复', en: 'restored' },
  'restore.badgeWhy': {
    zh: '这个会话在 {when} 被重建过。屏幕上分隔线以上的内容属于一个已经不存在的进程。',
    en: 'This session was rebuilt at {when}. Everything above the banner on screen belongs to a process that no longer exists.',
  },
  'share.title': { zh: '只读分享链接', en: 'Read-only share links' },
  'share.why': {
    zh: '放在另一块屏幕上的只读地址。只能看这一个页面，随时可以吊销。',
    en: 'A read-only address for a second screen. It reaches that page and nothing else, and can be revoked.',
  },
  'share.name': { zh: '给它起个名字', en: 'What is it for' },
  'share.create': { zh: '新建链接', en: 'New link' },
  'share.shows': { zh: '显示', en: 'Shows' },
  'share.detailCounts': { zh: '只有数量和状态', en: 'Counts and states' },
  'share.detailNames': { zh: '加上名字', en: 'Names as well' },
  'share.detailWhy': {
    zh: '会话名和项目名可能带上客户或仓库名，默认不显示。路径和命令行永远不发出去。',
    en: 'Titles and project names can carry a customer or a repository, so they are off by default. Paths and command lines are never sent.',
  },
  'share.expiry': { zh: '有效期', en: 'Expires' },
  'share.expiryNever': { zh: '永不过期', en: 'Never' },
  'share.expiryDay': { zh: '1 天', en: '1 day' },
  'share.expiryWeek': { zh: '7 天', en: '7 days' },
  'share.expiryMonth': { zh: '30 天', en: '30 days' },
  'share.once': {
    zh: '只显示这一次。数据库里存的是它的哈希，关掉这条就再也拿不回来了。',
    en: 'Shown once. The database keeps only a hash of it, so closing this is losing it.',
  },
  'share.none': { zh: '还没有分享链接', en: 'None yet' },
  'share.revoke': { zh: '吊销', en: 'Revoke' },
  'share.revokeSure': { zh: '确定吊销？', en: 'Revoke it?' },
  'share.keep': { zh: '算了', en: 'Keep it' },
  'share.expiresOn': { zh: '{date} 过期', en: 'expires {date}' },
  'share.noExpiry': { zh: '不过期', en: 'no expiry' },
  'share.open': { zh: '打开这个看板', en: 'Open this dashboard' },

  'dash.readOnly': { zh: '只读看板', en: 'Read-only dashboard' },
  'dash.live': { zh: '实时', en: 'Live' },
  'dash.reconnecting': { zh: '重连中', en: 'Reconnecting' },
  'dash.disconnected': { zh: '已断开', en: 'Disconnected' },
  'dash.connecting': { zh: '连接中', en: 'Connecting' },
  'dash.gone': { zh: '这个链接不能用了', en: 'This link no longer works' },
  'dash.goneWhy': {
    zh: '它被吊销了，或者已经过期。跟面板的主人再要一个。',
    en: 'It was revoked, or it has expired. Ask whoever owns the panel for another one.',
  },
  'dash.asOf': { zh: '数据时间 {time}', en: 'as of {time}' },
  'dash.agoNow': { zh: '刚刚', en: 'just now' },
  'dash.agoSeconds': { zh: '{n} 秒前', en: '{n}s ago' },
  'dash.agoMinutes': { zh: '{n} 分钟前', en: '{n}m ago' },
  'dash.agoHours': { zh: '{n} 小时前', en: '{n}h ago' },
  'dash.frozen': {
    zh: '这些数字已经 {ago} 没有更新过了',
    en: 'These numbers have not moved since {ago}',
  },
  'dash.waiting': { zh: '等你处理', en: 'Waiting' },
  'dash.working': { zh: '工作中', en: 'Working' },
  'dash.done': { zh: '已完成', en: 'Done' },
  'dash.sessions': { zh: '会话', en: 'Sessions' },
  'dash.projects': { zh: '项目', en: 'Projects' },
  'dash.crashed': { zh: '异常退出', en: 'Crashed' },
  'dash.load': { zh: '负载', en: 'Load' },
  'dash.nothing': { zh: '没有在跑的会话', en: 'Nothing running' },
  'dash.group': { zh: '第 {n} 组', en: 'Group {n}' },
  'dash.row': { zh: '会话 {n}', en: 'Session {n}' },
  'dash.kindAgent': { zh: 'agent', en: 'agent' },
  'dash.kindShell': { zh: 'shell', en: 'shell' },
  'dash.kindOther': { zh: '进程', en: 'process' },
  'dash.forTime': { zh: '已 {d}', en: 'for {d}' },
  'dash.anonymous': { zh: '这个链接不显示名字', en: 'This link shows no names' },
  'dash.stale': {
    zh: '面板已经停止记录会话状态，下面的状态可能是旧的。终端本身不受影响。',
    en: 'The panel has stopped recording session states, so what is below may be old. The terminals are unaffected.',
  },
  'dash.expiresIn': { zh: '{when} 后过期', en: 'expires in {when}' },

  'guessed.installed': {
    zh: '状态还在靠猜：装 hook 之前就开着的会话要重开一次才会上报。在里面输入 /hooks，或者重启那个 agent。',
    en: 'States are still guessed. Sessions open before reporting was installed keep guessing until they reload — run /hooks in each, or restart the agent.',
  },
  'guessed.notInstalled': {
    zh: '状态是猜的，“等你处理”可能被漏掉。点这里打开状态上报。',
    en: 'States are being guessed, so "waiting for you" can be missed. Turn on state reporting.',
  },
  // Token spend. Prefixed `spend.` and not `tok.`: `tok.` is already
  // API credentials above, and two unrelated meanings of "token" sharing a
  // key prefix is how somebody translates the wrong string.
  // Half of these exist to say what a number is *not*: the
  // feature's whole risk is a confident zero standing in for "the file was not
  // there", and every one of those cases needs its own sentence.
  'spend.title': { zh: 'Token 用量', en: 'Token usage' },
  'spend.open': { zh: '打开完整视图', en: 'Open the full view' },
  'spend.close': { zh: '关闭', en: 'Close' },
  'spend.today': { zh: '今天', en: 'Today' },
  'spend.rangeDays': { zh: '近 {n} 天', en: 'Last {n} days' },
  // The segmented control has four of these side by side and "Last 365 days"
  // four times does not fit; the long form stays for headings.
  'spend.rangeShort': { zh: '{n} 天', en: '{n}d' },
  'spend.filesRead': { zh: '读了 {n} 个文件', en: '{n} files read' },
  'spend.allTime': { zh: '全部时间', en: 'All time' },
  'spend.total': { zh: '合计', en: 'Total' },
  'spend.input': { zh: '新输入', en: 'Fresh input' },
  'spend.output': { zh: '输出', en: 'Output' },
  'spend.cacheRead': { zh: '缓存读取', en: 'Cache read' },
  'spend.cacheWrite': { zh: '缓存写入', en: 'Cache write' },
  'spend.requests': { zh: '请求数', en: 'Requests' },
  'spend.tokens': { zh: 'token', en: 'tokens' },
  'spend.day': { zh: '按天', en: 'By day' },
  'spend.month': { zh: '按月', en: 'By month' },
  'spend.sessions': { zh: '按会话', en: 'By session' },
  'spend.projects': { zh: '按项目', en: 'By project' },
  'spend.tools': { zh: '按工具', en: 'By tool' },
  'spend.heatmap': { zh: '近一年', en: 'The last 12 months' },
  'spend.less': { zh: '少', en: 'Less' },
  'spend.more': { zh: '多', en: 'More' },
  'spend.legend': {
    zh: '颜色越深用得越多；每格上有确切数字。',
    en: 'Darker is more. The exact figure is on every square.',
  },
  'spend.cellSpent': { zh: '{day}：{n} tokens', en: '{day}: {n} tokens' },
  'spend.cellNone': { zh: '{day}：没有记录', en: '{day}: nothing recorded' },
  'spend.cellOutside': { zh: '{day}：不在读取范围内', en: '{day}: outside the range that was read' },
  'spend.filterProject': { zh: '项目', en: 'Project' },
  'spend.filterTool': { zh: '工具', en: 'Tool' },
  'spend.filterRange': { zh: '时间范围', en: 'Range' },
  'spend.all': { zh: '全部', en: 'All' },
  'spend.outsideProjects': { zh: '不在任何项目里', en: 'Outside every project' },
  'spend.noData': { zh: '这个范围里没有记录。', en: 'Nothing was recorded in this range.' },
  'spend.refresh': { zh: '重新读取', en: 'Read again' },
  'spend.refreshing': { zh: '正在读取…', en: 'Reading…' },
  'spend.scanning': {
    zh: '正在读取 agent 自己的记录，读完就会出现在这里。',
    en: 'Reading what the agents recorded. The numbers appear when it finishes.',
  },
  'spend.neverScanned': {
    zh: '还没读过任何记录，所以这里是空的 —— 不是 0。',
    en: 'Nothing has been read yet, so this is empty rather than zero.',
  },
  'spend.scannedAgo': { zh: '{ago}前读的', en: 'read {ago} ago' },
  'spend.whose': {
    zh: 'agent 自己记的账，包括不经过面板跑的。',
    en: "Counted from the agents' own records, including runs this panel did not start.",
  },
  'spend.agentSessionNote': {
    zh: '这里的“会话”是 agent 自己的会话，不是面板里的会话 —— 两边没有可靠的对应关系。',
    en: "A session here is the agent's own, not one of the panel's: there is no reliable mapping between them.",
  },
  'spend.sourceMissing': {
    zh: '找不到 {tool} 的记录（{why}），所以它这里是“不知道”，不是 0。',
    en: 'No {tool} records could be read ({why}), so its figure is unknown rather than zero.',
  },
  'spend.sourceRead': { zh: '{tool}：读了 {files} 个文件', en: '{tool}: {files} files read' },
  'spend.lowerBound': {
    zh: '有 {n} 条记录读不出来，所以下面的数字是下限。',
    en: '{n} records could not be read, so the figures below are a lower bound.',
  },
  'spend.passError': { zh: '上一次读取出错：{why}', en: 'The last pass failed: {why}' },
  'spend.capped': {
    zh: '按用量排前 {n} 个，共 {total} 个。',
    en: 'The largest {n} of {total}.',
  },
  'spend.model': { zh: '模型', en: 'Model' },
  'spend.directory': { zh: '目录', en: 'Directory' },
  'spend.lastSeen': { zh: '最后一天', en: 'Last day' },
  'spend.unknownModel': { zh: '未记录模型', en: 'model not recorded' },
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
