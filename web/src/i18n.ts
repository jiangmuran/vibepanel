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
  // The header's four controls all said their piece in English on a Chinese
  // page: `Theme: dark`, `Signed in as ... — sign out`, `Connection: open`.
  // None of them tripped the untranslated check, because all three were
  // template literals and it looks for quoted attributes and lines of prose.
  'app.themeIs': { zh: '主题：{mode} —— 点一下换下一个', en: 'Theme: {mode} — click for the next one' },
  'theme.system': { zh: '跟随系统', en: 'System' },
  'theme.light': { zh: '浅色', en: 'Light' },
  'theme.dark': { zh: '深色', en: 'Dark' },
  'app.signedInAs': { zh: '当前登录 {user} —— 点一下退出', en: 'Signed in as {user} — click to sign out' },
  'app.connection': { zh: '连接：{status}', en: 'Connection: {status}' },
  'conn.open': { zh: '已连接', en: 'open' },
  'conn.connecting': { zh: '连接中', en: 'connecting' },
  'conn.closed': { zh: '已断开', en: 'closed' },
  'app.gridSize': { zh: '所有观看端看到的网格', en: 'The grid every viewer of this session is seeing' },
  'app.restart': { zh: '重启', en: 'restart' },
  'app.hidePanel': { zh: '收起面板', en: 'Hide panel' },
  'app.showPanel': { zh: '展开面板', en: 'Show panel' },

  'set.tmuxConfigStale': {
    zh: '正在跑的 tmux server 用的是旧配置。要应用新配置，得结束这个 socket 上的全部会话：',
    en: 'The running tmux server started with an older config. Applying the new one ends every session on this socket:',
  },
  'set.tmuxConfigUnknown': {
    zh: 'tmux server 早于这项检查，问不出来。重启之后才知道。',
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
  // The three reasons the server can give, as codes rather than as sentences:
  // it sends `passkeyReason` and both the login page and the settings page
  // translate it. It used to send English prose straight into a Chinese page.
  'env.grpReach': { zh: '怎么访问', en: 'How people reach it' },
  'env.grpTls': { zh: '证书', en: 'Certificate' },
  'env.grpAccess': { zh: '谁能连', en: 'Who may connect' },
  'env.lblSocket': { zh: 'tmux socket', en: 'tmux socket' },
  'env.lblAddr': { zh: '监听地址', en: 'Listen on' },
  'env.lblDomain': { zh: '域名', en: 'Domain' },
  'env.domainAlso': { zh: '也是 passkey 的 Relying Party ID', en: 'Also the passkey relying party ID.' },
  'env.lblTlsMode': { zh: '方式', en: 'How' },
  'env.lblCert': { zh: '证书文件', en: 'Certificate file' },
  'env.lblKey': { zh: '私钥文件', en: 'Key file' },
  'env.lblProvider': { zh: 'DNS 服务商', en: 'DNS provider' },
  'env.lblEmail': { zh: '联系邮箱', en: 'Contact email' },
  'env.lblDirectory': { zh: 'ACME 目录', en: 'ACME directory' },
  'env.lblAllow': { zh: '允许的网段', en: 'Allowed networks' },
  'env.lblProxies': { zh: '可信代理', en: 'Trusted proxies' },
  'env.allowAll': { zh: '留空表示不限制来源', en: 'Empty means any address may connect.' },
  'env.tlsOff': { zh: '不加密', en: 'Off' },
  'env.tlsFiles': { zh: '自己的证书', en: 'Own files' },
  'env.tlsAcme': { zh: '自动申请', en: 'Automatic' },
  'env.lblToken': { zh: 'API Token', en: 'API token' },
  'env.tokenSet': { zh: '已设置，不显示', en: 'Set. Never shown.' },
  'env.tokenUnset': { zh: '还没设置', en: 'Not set' },
  'env.tokenClear': { zh: '清除', en: 'Clear' },
  'pk.no-domain': {
    zh: '还没设置域名。passkey 需要一个主机名当作 Relying Party ID',
    en: 'no domain is set; a passkey needs a hostname as its Relying Party ID',
  },
  'pk.ip-domain': {
    zh: 'IP 地址不能当 Relying Party ID，得用域名',
    en: 'an IP address cannot be a Relying Party ID; it has to be a name',
  },
  'pk.no-tls': {
    zh: 'passkey 需要 HTTPS（localhost 除外）',
    en: 'a passkey needs HTTPS, or localhost',
  },
  'pk.where': {
    zh: '在「本机 → 网络与访问」里设 VIBEPANEL_DOMAIN',
    en: 'Set VIBEPANEL_DOMAIN under This panel → Network and access.',
  },
  'pk.rpid': { zh: 'Relying Party ID', en: 'Relying party ID' },
  'pk.add': { zh: '添加 passkey', en: 'Add a passkey' },
  'pk.waiting': { zh: '等待你的设备…', en: 'Waiting for your device…' },
  'pk.used': { zh: '{when} 用过', en: 'used {when}' },
  'pk.neverUsed': { zh: '没用过', en: 'never used' },
  'pk.hostMismatch': {
    zh: '这个页面在 {host}，不是上面那个 ID',
    en: 'This page is at {host}, not that ID — a passkey is refused here.',
  },
  'auth.loading': { zh: '正在连接…', en: 'Connecting…' },
  'auth.firstRun': { zh: '首次启动', en: 'First run' },
  'auth.tokenWhere': { zh: '服务端启动时打印在控制台里。', en: 'The server printed it to its console at startup.' },
  'auth.stepAccount': { zh: '你的账号', en: 'Your account' },
  'auth.passkeyHint': { zh: '用指纹或面容，不用打字', en: 'Fingerprint or face, nothing to type' },
  'auth.showPassword': { zh: '显示密码', en: 'Show password' },
  'auth.hidePassword': { zh: '隐藏密码', en: 'Hide password' },

  'app.stale': {
    zh: '面板已经停止记录会话在做什么。终端本身不受影响。',
    en: 'The panel has stopped recording what the sessions are doing. The terminals are unaffected.',
  },
  'app.showTerminals': { zh: '展开底部终端', en: 'Show terminals' },
  'app.sortByActivity': { zh: '改回按活跃度排序 —— 你的排列会留着', en: 'Sort by recent activity instead — your arrangement is kept' },
  'app.restartHintStatus': { zh: '命令以 status {n} 退出。在同一个 pane 里重跑它', en: 'The command exited with status {n}. Restart it in the same pane' },
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
  'err.tryAgain': { zh: '再试一次', en: 'Try again' },
  'settings.passwordChanged': {
    zh: '已修改。其他浏览器都已被登出。',
    en: 'Changed. Every other browser has been signed out.',
  },
  'key.interrupt': { zh: '中断 (Ctrl-C)', en: 'Interrupt (Ctrl-C)' },
  'key.enter': { zh: '回车', en: 'Enter' },
  'key.sticky': { zh: '作用于下一个按键', en: 'Applies to the next key' },
  'key.shiftTab': { zh: '很多 agent 用它切换模式', en: 'Agents bind this to cycle modes.' },
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
  'panel.git': { zh: '仓库', en: 'Repo' },
  'panel.monitor': { zh: '监控', en: 'Monitor' },
  'panel.notes': { zh: '笔记', en: 'Notes' },
  'panel.tokens': { zh: '用量', en: 'Tokens' },
  'panel.tablist': { zh: '侧栏面板', en: 'Side panel sections' },
  'panel.dockDivider': { zh: '上下分隔', en: 'Split this tab' },

  // The three states a block has. One verb for all of them, in all three
  // blocks, because the gesture is the same one everywhere it appears.
  'detail.open': { zh: '展开{what}', en: 'Open {what}' },
  'detail.back': { zh: '返回', en: 'Back' },
  'detail.full': { zh: '全屏显示', en: 'Fill the window' },
  'repo.openOn': { zh: '在 GitHub 上打开 {what}', en: 'Open {what} on GitHub' },

  // The pane layout. Every one of these is also reachable by dragging a tab;
  // they are here because dragging is a mouse gesture and the panel has to be
  // rearrangeable without one.
  'pane.menu': { zh: '这一格的布局', en: 'Pane layout' },
  'pane.moveUp': { zh: '移到上一格', en: 'Move to the pane above' },
  'pane.moveDown': { zh: '移到下一格', en: 'Move to the pane below' },
  'pane.mergeUp': { zh: '并入上一格', en: 'Merge into the pane above' },
  'pane.mergeDown': { zh: '并入下一格', en: 'Merge into the pane below' },
  'pane.reset': { zh: '恢复默认布局', en: 'Restore the default layout' },
  'pane.dropBefore': { zh: '放到这一格上面', en: 'New pane above' },
  'pane.dropJoin': { zh: '和这一格并排', en: 'Add to these tabs' },
  'pane.dropAfter': { zh: '放到这一格下面', en: 'New pane below' },

  'files.refresh': { zh: '刷新', en: 'Refresh' },
  'files.download': { zh: '下载', en: 'Download' },
  'files.empty': { zh: '这个目录是空的', en: 'Nothing here' },
  'files.escapes': { zh: '指向项目之外', en: 'points outside the project' },
  'files.newFolder': { zh: '新建目录，叫什么？', en: 'New directory, called what?' },
  'files.count': { zh: '{n} 项', en: '{n} items' },
  'files.modified': { zh: '改动时间', en: 'Modified' },

  // The side panel's checklist is gone; this line is not. It is the one entry
  // in the dictionary whose two languages take *different placeholders* —
  // Chinese counts what is done, English counts what is left — which is the
  // property i18n.test.ts exists to hold, and the wall boards still count
  // todos. Deleting it would delete the only fixture for a rule that applies
  // to every future line.
  'todos.leftOf': { zh: '{done} / {total} 已完成', en: '{left} of {total} left' },

  'notes.saved': { zh: '已保存', en: 'Saved' },
  'notes.saving': { zh: '保存中…', en: 'Saving…' },
  'notes.loading': { zh: '读取中…', en: 'Loading…' },
  'notes.unsaved': { zh: '未保存', en: 'Unsaved' },
  'notes.error': { zh: '保存失败', en: 'Could not save' },
  'notes.conflict': { zh: '别处改过了', en: 'Changed elsewhere' },
  'notes.placeholder': { zh: '这个项目的笔记，Markdown', en: 'Notes for this project, in Markdown' },
  'notes.chars': { zh: '{n} 字', en: '{n} chars' },
  'notes.lines': { zh: '{n} 行', en: '{n} lines' },

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
  'monitor.load': { zh: '负载', en: 'Load' },
  'monitor.perCore': { zh: '每核 {n}', en: '{n} per core' },
  'monitor.mount': { zh: '挂载点', en: 'Mount' },
  'monitor.total': { zh: '合计', en: 'Total' },
  'monitor.machine': { zh: '这台机器', en: 'Machine' },
  'monitor.state': { zh: '状态时长', en: 'In this state' },

  'dir.title': { zh: '选一个目录', en: 'Choose a directory' },
  'dir.here': { zh: '当前位置', en: 'Where you are' },
  'dir.up': { zh: '上一层', en: 'Up one level' },
  'dir.editPath': { zh: '把路径当文本改', en: 'Edit this path as text' },
  'dir.empty': {
    zh: '这下面没有别的目录了 —— 可以直接用它，也可以新建一个',
    en: 'Nothing below this one — use it as it is, or make a folder',
  },
  'dir.loading': { zh: '正在读取…', en: 'Reading…' },
  'dir.truncated': { zh: '目录太多，只显示了 {shown} / {total} 个', en: 'Showing {shown} of {total}' },
  'dir.newFolder': { zh: '在这里新建目录', en: 'New folder here' },
  'dir.newName': { zh: '新目录的名字', en: 'Name' },
  'dir.create': { zh: '创建', en: 'Create' },
  'dir.search': { zh: '输入以筛选，或者直接写一个路径', en: 'Type to filter, or write a path' },
  'dir.matches': { zh: '{n} / {total} 个匹配', en: '{n} of {total}' },
  'dir.noMatch': { zh: '这里没有叫「{q}」的目录', en: 'Nothing here is called “{q}”' },
  'dir.createNamed': { zh: '新建「{name}」', en: 'Create “{name}”' },
  'dir.willGo': { zh: '回车进这个目录', en: 'Enter goes here' },
  'dir.willUse': {
    zh: '在主目录之外，列不出里面有什么 —— 回车直接用这个路径',
    en: 'Outside home, so it cannot be listed — Enter takes it as it is',
  },
  'dir.goHere': { zh: '进入', en: 'Go here' },
  'dir.usePath': { zh: '用这个路径', en: 'Use this path' },
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
    zh: '{size} 超过 {limit} 的预览上限。下载可以看全部。',
    en: 'At {size} this is past the {limit} preview limit. Download it to see all of it.',
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
  'preview.rendered': { zh: '{name} 的页面', en: 'The page {name}' },
  'preview.rendered.short': { zh: '页面', en: 'Page' },
  'preview.source': { zh: '源码', en: 'Source' },
  'preview.scriptsOn': { zh: '脚本已开', en: 'Scripts on' },
  'preview.scriptsOff': { zh: '脚本已关', en: 'Scripts off' },

  'git.reading': { zh: '读取中…', en: 'Reading…' },
  'git.notARepo': { zh: '这个目录不是 git 仓库。', en: 'This directory is not a git repository.' },
  'git.detached': { zh: '游离 HEAD', en: 'detached' },
  'git.noUpstream': { zh: '没有上游分支', en: 'no upstream' },
  'git.aheadBehind': { zh: '领先 {a}，落后 {b}', en: '{a} ahead, {b} behind' },
  'git.clean': { zh: '工作区干净', en: 'Nothing uncommitted' },
  'git.staged': { zh: '已暂存', en: 'staged' },
  'git.unstaged': { zh: '未暂存', en: 'unstaged' },
  'git.untracked': { zh: '未跟踪', en: 'untracked' },
  'git.conflicted': { zh: '冲突', en: 'conflicted' },
  'git.conflictWord': { zh: '有冲突', en: 'conflict' },
  'git.changesTruncated': { zh: '共 {n} 处改动，只列出前面一部分', en: 'Listing part of {n} changes' },
  'git.elsewhere': { zh: '在别的分支上的会话', en: 'Sessions on another branch' },
  'git.sessionsTruncated': { zh: '目录太多，没有全部读取', en: 'Too many directories to read them all' },
  'git.uncommitted': { zh: '{n} 处未提交', en: '{n} uncommitted' },
  'git.recent': { zh: '最近提交', en: 'Recent commits' },
  'git.upstream': { zh: 'GitHub', en: 'GitHub' },
  'git.ask': { zh: '查询', en: 'Check' },
  'git.notGitHub': { zh: '这个仓库的 origin 不在 github.com。', en: "This repository's origin is not on github.com." },
  'git.noToken': {
    zh: '面板启动时没有 GITHUB_TOKEN 或 GH_TOKEN。',
    en: 'The panel was started without GITHUB_TOKEN or GH_TOKEN.',
  },
  'git.noPRs': { zh: '没有开着的 PR。', en: 'No open pull requests.' },
  'git.prsTruncated': { zh: '共 {total} 个，显示 {shown} 个', en: 'Showing {shown} of {total}' },
  'git.draft': { zh: '草稿', en: 'draft' },
  'git.checksPass': { zh: '检查通过', en: 'checks pass' },
  'git.checksFail': { zh: '检查失败', en: 'checks fail' },
  'git.checksRunning': { zh: '检查中', en: 'checks running' },
  'git.checksNone': { zh: '没有检查', en: 'no checks' },
  'git.reviewApproved': { zh: '已批准', en: 'approved' },
  'git.reviewChanges': { zh: '要求修改', en: 'changes requested' },
  'git.reviewRequired': { zh: '待评审', en: 'review required' },
  'git.reviewNone': { zh: '不需要评审', en: 'no review required' },
  'bottom.close': { zh: '关闭终端', en: 'Close terminal' },
  'bottom.hide': { zh: '收起终端', en: 'Hide terminals' },
  'bottom.new': { zh: '新建终端', en: 'New terminal' },
  'bottom.newIn': { zh: '在 {dir} 里新建终端', en: 'New terminal in {dir}' },
  'bottom.label': { zh: '终端', en: 'Terminals' },
  'bottom.empty': { zh: '这里还没有终端', en: 'No terminals here yet' },
  'bottom.resize': { zh: '拖动调整高度', en: 'Drag to resize' },
  'panel.resize': { zh: '拖动调整宽度', en: 'Drag to resize' },
  'panel.noProject': { zh: '还没有选中项目', en: 'No project selected' },
  'project.reorder': { zh: '拖动排序', en: 'Drag to reorder' },
  'project.remove': { zh: '把这个项目从面板移除', en: 'Remove this project from the panel' },
  'project.orderManual': { zh: '回到你排好的顺序', en: 'Back to the order you arranged' },
  'compose.placeholder': { zh: '输入命令…', en: 'Type a command…' },
  'compose.send': { zh: '发送', en: 'Send' },
  'settings.title': { zh: '设置', en: 'Settings' },
  'settings.close': { zh: '关闭', en: 'Close' },
  'settings.language': { zh: '语言', en: 'Language' },
  'settings.groups': { zh: '设置分组', en: 'Settings groups' },

  // The five names on the rail. Each one is the word somebody would think of
  // before they open the dialog, which is the only test a group name has to
  // pass -- see settings/groups.ts for what is in each.
  'grp.sessions': { zh: '会话', en: 'Sessions' },
  'grp.notify': { zh: '通知', en: 'Notifications' },
  'grp.sharing': { zh: '分享', en: 'Sharing' },
  'grp.account': { zh: '账户', en: 'Account' },
  'grp.panel': { zh: '本机', en: 'This panel' },
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
    zh: '用这台设备代替密码登录。密码依然有效。',
    en: 'Sign in with this device instead of a password. The password keeps working.',
  },
  'set.working': { zh: '处理中…', en: 'Working…' },
  'set.hide': { zh: '收起', en: 'Hide' },

  'set.status': { zh: '状态', en: 'Status' },

  'profile.title': { zh: '启动配置', en: 'Launch profiles' },
  'profile.why': {
    zh: '一套启动参数和环境变量，起个名字。新建会话时挑一个。',
    en: 'An argv and a set of environment variables, under a name. Pick one when you start a session.',
  },
  'profile.name.builtin:shell': { zh: '终端', en: 'Shell' },
  'profile.name.builtin:claude': { zh: 'Claude Code', en: 'Claude Code' },
  'profile.name.builtin:codex': { zh: 'Codex', en: 'Codex' },
  'profile.name.builtin:opencode': { zh: 'opencode', en: 'opencode' },
  'profile.builtinTag': { zh: '内置', en: 'Built in' },
  'profile.none': { zh: '还没有自己的配置', en: 'None of your own yet' },
  'profile.new': { zh: '新建配置', en: 'New profile' },
  'profile.duplicate': { zh: '复制一份来改', en: 'Duplicate and edit' },
  'profile.edit': { zh: '编辑', en: 'Edit' },
  'profile.remove': { zh: '删掉', en: 'Remove' },
  'profile.save': { zh: '保存', en: 'Save' },
  'profile.cancel': { zh: '取消', en: 'Cancel' },
  'profile.name': { zh: '名字', en: 'Name' },
  'profile.command': { zh: '命令', en: 'Command' },
  'profile.commandHint': { zh: '留空就是登录 shell', en: 'Empty means a login shell' },
  'profile.env': { zh: '环境变量', en: 'Environment' },
  'profile.envName': { zh: '变量名', en: 'Name' },
  'profile.envValue': { zh: '值', en: 'Value' },
  'profile.envAdd': { zh: '加一个变量', en: 'Add a variable' },
  'profile.envRemove': { zh: '删掉这个变量', en: 'Remove this variable' },
  'profile.secret': { zh: '按密钥处理', en: 'Treat as a secret' },
  'profile.secretKept': { zh: '已存，留空就不动它', en: 'Stored — leave empty to keep it' },
  'profile.secretRenamed': {
    zh: '改了变量名就要重新填一次值。',
    en: 'Renaming a variable means entering its value again.',
  },
  'profile.plaintext': {
    zh: '密钥明文存在面板的数据库里，只是不会再发回浏览器。',
    en: 'Keys are stored in the panel\'s database in plain text; they are just never sent back to a browser.',
  },
  'profile.envSet': { zh: '{n} 个变量', en: '{n} variables' },
  'profile.envSetOne': { zh: '1 个变量', en: '1 variable' },
  'profile.copySuffix': { zh: '{name} 副本', en: '{name} copy' },
  'profile.pick': { zh: '用哪个配置启动', en: 'Start with' },
  'profile.gone': { zh: '它用的启动配置已经删掉了', en: 'The launch profile it used has been removed' },
  'profile.removeTitle': { zh: '删掉「{name}」？', en: 'Remove “{name}”?' },
  'profile.removeBody': {
    zh: '正在跑的会话不受影响；用它启动过的会话重建时会少掉这些变量。',
    en: 'Running sessions are unaffected; a session restored later starts without these variables.',
  },
  'wh.title': { zh: '推送通知', en: 'Push notifications' },
  'wh.why': {
    zh: '会话变成「等你处理」时往手机发一条，面板关着也行。',
    en: 'One to your phone when a session starts waiting, with the panel closed.',
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
  'wh.needsUrl': { zh: '有个 webhook 还没填地址', en: 'A webhook has no URL yet.' },
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
    zh: '开发版构建，没法和发布版比较。',
    en: 'A development build. There is nothing to compare against.',
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
    zh: '已装上 {v}。这个面板不是 systemd 起的，要你自己重启：{why}',
    en: 'Installed {v}. This panel was not started by systemd, so start it yourself: {why}',
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

  // Claude Code's own settings, beyond the state-reporting hooks. The
  // per-key descriptions are NOT here: they arrive with the rows from
  // internal/hooks, so they cannot drift from the keys they describe.
  'tune.title': { zh: 'Claude Code 配置', en: 'Claude Code settings' },
  'tune.loading': { zh: '正在读取…', en: 'reading…' },
  'tune.what': {
    zh: '决定哪些东西离开这台机器，以及 agent 往你 git 历史里写什么。',
    en: 'What leaves this machine, and what the agent writes into your git history.',
  },
  'tune.already': { zh: '已经是这样', en: 'already set' },
  'tune.would': { zh: '会改这一条', en: 'would change' },
  'tune.was': { zh: '原本是 {v}', en: 'was {v}' },
  'tune.apply': { zh: '应用这 {n} 条', en: 'Apply {n}' },
  'tune.nothing': { zh: '无需改动', en: 'Nothing to change' },
  'tune.applied': { zh: '改了 {n} 条。', en: 'Changed {n}.' },
  'tune.backup': { zh: '先备份 {p}，其他内容不动', en: 'copies {p} first; nothing else in it changes' },

  // The service's environment file, as fields.
  'env.title': { zh: '网络与访问', en: 'Network and access' },
  'env.what': {
    zh: '这些写在服务的环境文件里，下次启动生效。',
    en: 'These live in the service\'s environment file and take effect on the next start.',
  },
  'env.save': { zh: '保存到文件', en: 'Save to the file' },
  'env.backup': { zh: '先备份，注释和其他行都不动', en: 'copied first; comments and other lines are left alone' },
  'env.pending': {
    zh: '文件已改，但面板还跑在旧设置上。下面点重启。',
    en: 'The file has changed and the panel is still running the old settings. Restart below.',
  },
  'env.socketFixed': {
    zh: '不在这里改：换了它面板就看不见自己的会话',
    en: 'not editable here: a panel on another socket cannot see its own sessions',
  },

  // Where a pasted screenshot goes.
  'paste.title': { zh: '粘贴进终端的图片', en: 'Images pasted into a terminal' },
  'paste.where': { zh: '文件放哪', en: 'Where the file goes' },
  'paste.wherePanel': { zh: '面板自己的目录，不碰你的仓库', en: 'A directory the panel owns, not your repository' },
  'paste.whereSession': { zh: '会话当前的工作目录', en: "The session's working directory" },
  'paste.then': { zh: '然后做什么', en: 'And then' },
  'paste.thenType': { zh: '把路径敲到提示符上', en: 'Type the path at the prompt' },
  'paste.thenBuffer': { zh: '放进 tmux 粘贴缓冲区', en: 'Put the path in the tmux paste buffer' },
  'paste.thenBoth': { zh: '两个都做', en: 'Both' },
  'paste.saved': { zh: '已保存。', en: 'Saved.' },

  // Restarting the panel itself.
  'rst.title': { zh: '重启面板', en: 'Restart the panel' },
  'rst.what': {
    zh: '会话不受影响：进程归 tmux，面板只是连上去的客户端。',
    en: 'Sessions are untouched: tmux owns them and the panel is a client.',
  },
  'rst.go': { zh: '重启', en: 'Restart' },
  'rst.going': { zh: '正在重启…', en: 'restarting…' },
  'rst.back': { zh: '回来了。', en: 'back.' },
  'rst.unsupervised': {
    zh: '这个面板不是由服务管的，停了就不会自己起来。',
    en: 'Nothing supervises this panel, so stopping it would not bring it back.',
  },

  // "installed" is a claim about a file, not about behaviour: the panel has
  // read a config, it has not heard from an agent.
  'set.installedEvents': { zh: '已安装，{n} 个事件', en: 'installed for {n} events' },
  'set.installedNotify': { zh: '已安装（notify）', en: 'installed as notify' },
  'set.install': { zh: '安装', en: 'Install' },
  // Codex has one notify slot for one event, so a Codex session can report
  // "waiting" and nothing else. Saying so on the page is cheaper than the
  // runbook section that exists because nobody knew.
  'set.codexOneEvent': {
    zh: 'Codex 只有一个 notify，只能上报“等你处理”。',
    en: 'Codex has one notify command, so it only reports waiting.',
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
  'notify.browser': { zh: '这个浏览器', en: 'This browser' },
  'notify.explain': {
    zh: '会话变成“等你处理”时推一条。后台标签页或装成 App 都算开着。',
    en: 'One when a session starts waiting. A background tab or an installed app both count as open.',
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
    zh: '这个标签页还在跑旧版界面。会话不受影响。',
    en: 'This tab is still running the old interface. Your sessions are unaffected.',
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
  'spend.requestsShort': { zh: '{n} 次请求', en: '{n} requests' },
  'spend.sparkDays': { zh: '近 {n} 天', en: 'the last {n} days' },
  'toast.copyFailed': { zh: '复制没成功，浏览器拒绝了', en: 'The copy did not go through' },
  'app.clipboardOffer': { zh: '复制了 {n} 个字符 —— 点一下放进剪贴板', en: 'Copied {n} characters — click to put them on your clipboard' },
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
    zh: '里面的进程会被终止。pane 和滚动历史留着。',
    en: 'The process is terminated. The pane and its scrollback stay.',
  },
  'ask.revokeTitle': { zh: '吊销 {name}？', en: 'Revoke {name}?' },
  'ask.revokeBody': {
    zh: '拿着 {prefix}… 的程序会立刻失效，而且没法撤销。',
    en: 'Anything holding {prefix}… stops working immediately, and it cannot be undone.',
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
    zh: '进程回不来。重跑命令启动的是一个全新的 agent，不记得之前的任何东西。',
    en: 'The processes cannot come back. Re-running the command starts a new agent that remembers none of it.',
  },
  'restore.open': { zh: '看看要恢复哪些', en: 'Choose what to restore' },
  'restore.later': { zh: '待会儿', en: 'Later' },
  'restore.dialogTitle': { zh: '恢复会话', en: 'Restore sessions' },
  'restore.selectAll': { zh: '全选', en: 'Select all' },
  'restore.selectNone': { zh: '全不选', en: 'Select none' },
  'restore.willRun': { zh: '将运行', en: 'will run' },
  'restore.willRunShell': {
    zh: '将启动一个登录 shell —— 原来跑的是什么没有记录',
    en: 'will start a login shell — what it was running was never recorded',
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
    zh: '在 {when} 重建过。分隔线以上的内容属于一个已经不存在的进程。',
    en: 'Rebuilt at {when}. Everything above the banner belongs to a process that no longer exists.',
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
    zh: '会话名和项目名可能带客户或仓库名。路径和命令行永远不发。',
    en: 'Titles and project names can carry a customer or a repository. Paths and command lines are never sent.',
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
  'share.remark': { zh: '这块屏叫什么', en: 'What to call this screen' },
  'share.remarkWhy': {
    zh: '看的人也能看到这行字。',
    en: 'Whoever opens the link sees this line.',
  },
  'share.lock': { zh: '锁定', en: 'Lock' },
  'share.unlock': { zh: '解锁', en: 'Unlock' },
  'share.lockedRow': { zh: '已锁定', en: 'Locked' },
  'share.viewers': { zh: '{n} 块屏在看', en: '{n} watching' },
  'share.noViewers': { zh: '没人在看', en: 'Nobody watching' },
  'share.viewport': { zh: '{w}×{h}', en: '{w}×{h}' },
  'share.preview': { zh: '这块屏现在的样子', en: 'What that screen shows' },
  'share.previewGone': { zh: '取不到预览', en: 'No preview' },
  'share.previewCold': { zh: '还没有屏打开过它', en: 'Nothing has opened it yet' },

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
    zh: '面板已停止记录会话状态，下面可能是旧的。终端不受影响。',
    en: 'The panel has stopped recording session states. What is below may be old; the terminals are unaffected.',
  },
  'dash.expiresIn': { zh: '{when} 后过期', en: 'expires in {when}' },
  'dash.longestWait': { zh: '最久的等了 {d}', en: 'longest {d}' },
  'dash.nothingWaiting': { zh: '没有在等你的', en: 'Nothing is waiting' },
  'dash.allClear': { zh: '都不用管', en: 'All clear' },
  'dash.unknown': { zh: '未知', en: 'unknown' },
  'dash.noSpendYet': { zh: '还没数过', en: 'Not counted yet' },
  'dash.spendAt': { zh: '统计于 {time}', en: 'counted {time}' },
  'dash.lastDays': { zh: '近 {n} 天', en: 'last {n} days' },
  'dash.today': { zh: '今天', en: 'Today' },
  'dash.thisMonth': { zh: '本月', en: 'This month' },
  'dash.input': { zh: '输入', en: 'Input' },
  'dash.output': { zh: '输出', en: 'Output' },
  'dash.cacheRead': { zh: '缓存读', en: 'Cache read' },
  'dash.cacheWrite': { zh: '缓存写', en: 'Cache write' },
  'dash.requests': { zh: '请求', en: 'Requests' },
  'dash.tokens': { zh: 'token', en: 'tokens' },
  'dash.outsideProjects': { zh: '项目之外', en: 'Outside every project' },
  'dash.noExits': { zh: '没有退出的会话', en: 'Nothing has exited' },
  'dash.exited': { zh: '已退出', en: 'Exited' },
  'dash.exitStatus': { zh: '退出码 {n}', en: 'exit {n}' },
  'dash.vanished': { zh: '会话消失了', en: 'vanished' },
  'dash.noMeasurements': { zh: '读不到进程数据', en: 'No process readings' },
  'dash.emptyWidget': { zh: '没有数据', en: 'Nothing to show' },
  'dash.heatmapLess': { zh: '少', en: 'less' },
  'dash.heatmapMore': { zh: '多', en: 'more' },
  'dash.heatmapDay': { zh: '{date}：{n} token', en: '{date}: {n} tokens' },
  'dash.uptimeLabel': { zh: '已运行', en: 'Uptime' },
  'dash.noSwap': { zh: '没有交换区', en: 'No swap' },
  'dash.closedToday': { zh: '今天完成 {n} 条', en: '{n} closed today' },
  'dash.perHourToday': { zh: '今天平均每小时', en: 'per hour today' },
  'dash.commitsToday': { zh: '今天的提交', en: 'Commits today' },
  'dash.linesAdded': { zh: '新增行', en: 'Lines added' },
  'dash.linesRemoved': { zh: '删除行', en: 'Lines removed' },
  'dash.filesToday': { zh: '动过的文件', en: 'Files touched' },
  'dash.notRead': { zh: '还没读到', en: 'Not counted yet' },
  'dash.readAgo': { zh: '{d} 前读的', en: 'read {d} ago' },
  'dash.notARepo': { zh: '不是仓库', en: 'Not a repository' },
  'dash.someNotRepos': { zh: '{n} 个项目不是仓库', en: '{n} projects are not repositories' },
  'dash.spent': { zh: '花了', en: 'Spent' },
  'dash.made': { zh: '做出来', en: 'Made' },
  'dash.prsOpen': { zh: '开着的', en: 'Open' },
  'dash.prsDraft': { zh: '{n} 个草稿', en: '{n} draft' },
  'dash.prsMerged': { zh: '今天合并', en: 'Merged today' },
  'dash.checksGreen': { zh: 'CI 过了', en: 'Checks green' },
  'dash.checksRed': { zh: 'CI 挂了', en: 'Checks red' },
  'dash.flowStarted': { zh: '开工', en: 'started' },
  'dash.flowWaited': { zh: '等人', en: 'waiting' },
  'dash.flowFinished': { zh: '做完', en: 'finished' },
  'dash.typicalWait': { zh: '平均等 {d}，共 {n} 次', en: 'typically {d}, over {n} waits' },
  'dash.noWaitsToday': { zh: '今天没人等过', en: 'Nothing waited today' },
  'dash.waitsCounted': { zh: '{n} 次', en: 'over {n} waits' },
  'dash.feedQuiet': { zh: '今天还没动静', en: 'Nothing yet today' },
  'dash.perMinute': { zh: '每分钟', en: 'per minute' },
  'dash.allTime': { zh: '一共用掉的 token', en: 'Tokens all time' },
  'dash.lastMinutes': { zh: '近 {n} 分钟', en: 'last {n} min' },
  'dash.trendShort': { zh: '刚开始记', en: 'Just started' },
  'dash.health': { zh: '面板状况', en: 'Panel health' },
  'dash.healthRecords': { zh: '记录是新的', en: 'Records current' },
  'dash.healthUsage': { zh: '能读到进程', en: 'Processes readable' },
  'dash.healthExpiry': { zh: '链接有效期', en: 'Link expires' },
  'dash.healthOk': { zh: '正常', en: 'ok' },
  'dash.healthNot': { zh: '有问题', en: 'no' },
  'dash.colInput': { zh: '输入', en: 'Input' },
  'dash.colOutput': { zh: '输出', en: 'Output' },
  'dash.colCacheRead': { zh: '读缓存', en: 'Cache read' },
  'dash.colCacheWrite': { zh: '写缓存', en: 'Cache write' },
  'dash.locked': { zh: '已锁定', en: 'Locked' },
  'dash.perHourAverage': { zh: '近 {days} 天平均每小时 {n}', en: '{n}/hour over {days} days' },
  'dash.requestsPerHour': { zh: '每小时 {n} 个请求', en: '{n} requests/hour' },
  'dash.noComparison': { zh: '没得比', en: 'nothing to compare' },
  'dash.upBy': { zh: '多 {n}%', en: '{n}% more' },
  'dash.downBy': { zh: '少 {n}%', en: '{n}% less' },
  'dash.versusYesterday': { zh: '昨天 {n}', en: 'yesterday {n}' },
  'dash.versusLastMonth': { zh: '上月 {n}', en: 'last month {n}' },
  'dash.oneProject': { zh: '一个项目', en: 'One project' },
  'dash.oneSession': { zh: '一个会话', en: 'One session' },

  // The board vocabulary. Every id here is a string the server owns, and a Go
  // test walks the widget registry to fail if one of them has no entry.
  'board.kind.attention': { zh: '需要我吗', en: 'Needs me?' },
  'board.kind.states': { zh: '状态计数', en: 'State tallies' },
  'board.kind.bignumber': { zh: '一个大数字', en: 'One big number' },
  'board.kind.clock': { zh: '时钟', en: 'Clock' },
  'board.kind.caption': { zh: '一行说明', en: 'Caption' },
  'board.kind.sessiongrid': { zh: '会话方阵', en: 'Session tiles' },
  'board.kind.sessionlist': { zh: '会话列表', en: 'Session list' },
  'board.kind.projects': { zh: '按项目', en: 'By project' },
  'board.kind.machine': { zh: '机器', en: 'Machine' },
  'board.kind.gauge': { zh: '仪表盘', en: 'Gauge' },
  'board.kind.uptime': { zh: '运行时长与负载', en: 'Uptime and load' },
  'board.kind.cputop': { zh: '最耗资源的会话', en: 'Heaviest sessions' },
  'board.kind.exits': { zh: '退出情况', en: 'Exits' },
  'board.kind.todos': { zh: '清单进度', en: 'Checklists' },
  'board.kind.output': { zh: '今天的产出', en: 'What came out' },
  'board.kind.spendtotals': { zh: 'token 合计', en: 'Token totals' },
  'board.kind.spendrate': { zh: '烧得多快', en: 'How fast' },
  'board.kind.spendcompare': { zh: '跟上次比', en: 'Against last time' },
  'board.kind.spendbars': { zh: 'token 走势', en: 'Tokens over time' },
  'board.kind.spendsplit': { zh: 'token 去向', en: 'Where it went' },
  'board.kind.spendheatmap': { zh: '一年的格子', en: 'The year, day by day' },
  'board.kind.statebar': { zh: '状态色带', en: 'State strip' },
  'board.kind.nowstrip': { zh: '此刻一行', en: 'Right now, one row' },
  'board.kind.kinds': { zh: 'agent 还是 shell', en: 'Agents and shells' },
  'board.kind.busiest': { zh: '哪个项目最忙', en: 'Busiest projects' },
  'board.kind.timeline': { zh: '各等了多久', en: 'How long each has been' },
  'board.kind.health': { zh: '面板状况', en: 'Panel health' },
  'board.kind.machinearea': { zh: '机器曲线', en: 'Machine, over time' },
  'board.kind.tokenburn': { zh: 'token 实时消耗', en: 'Token burn' },
  'board.kind.odometer': { zh: '总用量', en: 'All time' },
  'board.kind.sparkline': { zh: '迷你走势', en: 'Sparkline' },
  'board.kind.spendstack': { zh: '分层走势', en: 'Stacked over time' },
  'board.kind.datetime': { zh: '大时钟', en: 'Clock and date' },
  'board.kind.remark': { zh: '这块屏的名字', en: "This screen's name" },
  'board.kind.heading': { zh: '小标题', en: 'Section heading' },
  'board.kind.rule': { zh: '分隔线', en: 'Divider' },
  'board.kind.spacer': { zh: '留白', en: 'Spacer' },

  'board.by.cpu': { zh: '按 CPU', en: 'CPU' },
  'board.by.memory': { zh: '按内存', en: 'Memory' },
  'board.by.load': { zh: '按负载', en: 'Load' },

  'board.screen.phone': { zh: '手机', en: 'Phone' },
  'board.screen.laptop': { zh: '电脑', en: 'Laptop' },
  'board.screen.wall': { zh: '墙上的屏', en: 'Wall screen' },
  'board.screen.bigwall': { zh: '4K 大屏', en: '4K wall' },

  'board.audience.working': { zh: '给正在干活的自己', en: 'While you are working' },
  'board.audience.wall': { zh: '给墙上的屏', en: 'For a screen on a wall' },
  'board.audience.ops': { zh: '给管机器的人', en: 'For whoever runs the machine' },
  'board.audience.manager': { zh: '给老板和领导', en: 'For a manager' },
  'board.audience.detail': { zh: '盯着一件事看', en: 'A closer look at one thing' },

  'board.metric.todosOpen': { zh: '还没做的', en: 'Open' },
  'board.metric.todosDone': { zh: '已做完的', en: 'Done' },
  'board.metric.todoPercent': { zh: '清单完成度', en: 'Checklist' },
  'board.metric.doneToday': { zh: '今天做完的', en: 'Finished today' },
  'board.metric.tokensPerHour': { zh: '每小时 token', en: 'Tokens per hour' },

  'board.group.project': { zh: '按项目分组', en: 'By project' },
  'board.group.state': { zh: '按状态分组', en: 'By state' },
  'board.group.none': { zh: '不分组', en: 'One list' },

  'board.by.day': { zh: '按天', en: 'By day' },
  'board.by.month': { zh: '按月', en: 'By month' },
  'board.by.tool': { zh: '按 agent', en: 'By agent' },
  'board.by.project': { zh: '按项目', en: 'By project' },
  'board.by.model': { zh: '按模型', en: 'By model' },
  'board.by.hour': { zh: '按小时', en: 'By hour' },
  'board.by.lines': { zh: '按代码行', en: 'Lines changed' },
  'board.by.commits': { zh: '按提交数', en: 'Commits' },
  'board.by.files': { zh: '按文件数', en: 'Files touched' },

  'board.kind.flow': { zh: '今天的节奏', en: 'How the day went' },
  'board.kind.waits': { zh: '等了多久', en: 'How long things waited' },
  'board.kind.feed': { zh: '刚刚发生的', en: 'What just happened' },
  'board.kind.codechurn': { zh: '代码改动走势', en: 'Code, over time' },
  'board.kind.spentmade': { zh: '花费对产出', en: 'Spent and made' },
  'board.kind.repoprojects': { zh: '各项目产出', en: 'Output by project' },
  'board.kind.prs': { zh: 'Pull request', en: 'Pull requests' },

  'board.metric.commitsToday': { zh: '今天的提交', en: 'Commits today' },
  'board.metric.commitsWindow': { zh: '这段时间的提交', en: 'Commits in the window' },
  'board.metric.linesAdded': { zh: '新增行', en: 'Lines added' },
  'board.metric.linesRemoved': { zh: '删除行', en: 'Lines removed' },
  'board.metric.linesChanged': { zh: '改动行', en: 'Lines changed' },
  'board.metric.filesToday': { zh: '今天动过的文件', en: 'Files touched today' },
  'board.metric.openPRs': { zh: '开着的 PR', en: 'Open pull requests' },
  'board.metric.prsMergedToday': { zh: '今天合并的 PR', en: 'Merged today' },
  'board.metric.checksRed': { zh: 'CI 挂了的 PR', en: 'Checks failing' },
  'board.metric.startedToday': { zh: '今天开工的', en: 'Started today' },
  'board.metric.waitsToday': { zh: '今天等过人的', en: 'Waited today' },
  'board.metric.avgWaitToday': { zh: '平均等多久', en: 'Typical wait' },

  'board.preset.newsroom': { zh: '中台大屏', en: 'The room screen' },
  'board.presetWhy.newsroom': {
    zh: '今天做出来了什么，旁边是花了多少。',
    en: 'What got built today, with what it cost beside it.',
  },
  'board.preset.deskwall': { zh: '坐在屏幕前', en: 'Sitting in front of it' },
  'board.presetWhy.deskwall': {
    zh: '同一块屏，但每块都说得更多。',
    en: 'The same screen, with everything on it saying more.',
  },
  'board.preset.made': { zh: '今天做出了什么', en: 'What got built' },
  'board.presetWhy.made': {
    zh: '提交、改动行、合并的 PR，不谈花费。',
    en: 'Commits, changed lines and merged pull requests. No cost figures.',
  },
  'board.preset.spentmade': { zh: '花费对产出', en: 'Spent and made' },
  'board.presetWhy.spentmade': {
    zh: '同一条时间轴上两条线，自己比。',
    en: 'Two series on one time axis. No ratio; you do the comparing.',
  },
  'board.preset.shipping': { zh: 'PR 和它前面的活', en: 'Pull requests' },
  'board.presetWhy.shipping': {
    zh: '开着的、CI 挂的、今天合并的。',
    en: 'What is open, what is red, and what went in today.',
  },
  'board.preset.today': { zh: '今天怎么过的', en: 'How today went' },
  'board.presetWhy.today': {
    zh: '开工、等人、做完，按小时排开。',
    en: 'Started, waiting and finished, hour by hour.',
  },
  'board.preset.phone': { zh: '手机上看', en: 'On a phone' },
  'board.presetWhy.phone': {
    zh: '一列三块，站着看四秒。',
    en: 'One column, three things, four seconds standing up.',
  },
  'board.preset.burn': { zh: 'token 实时消耗', en: 'Token burn' },
  'board.presetWhy.burn': {
    zh: '今天烧了多少，烧得多快，烧在哪。',
    en: 'What it is spending, how fast, and on what.',
  },
  'board.preset.atrium': { zh: '走廊大屏', en: 'Corridor screen' },
  'board.presetWhy.atrium': {
    zh: '大部分时候是个钟，顺带看一眼会话。',
    en: 'A clock most of the time, with the sessions beside it.',
  },
  'board.preset.exec': { zh: '给领导看', en: 'For leadership' },
  'board.presetWhy.exec': {
    zh: '大数字、会动的线、铺满一整块 4K 屏。',
    en: 'A hero, a line that moves, and enough texture to fill a 4K wall.',
  },
  'board.preset.client': { zh: '给客户看', en: 'For a client' },
  'board.presetWhy.client': {
    zh: '只看他自己的项目，不显示任何名字。',
    en: "Their own project only, with nobody's names on it.",
  },
  'board.preset.overview': { zh: '总览', en: 'Overview' },
  'board.presetWhy.overview': {
    zh: '状态、机器、所有会话。看板本来的样子。',
    en: 'States, the machine, every session. What the dashboard was before boards.',
  },
  'board.preset.attention': { zh: '需要我吗', en: 'Does anything need me' },
  'board.presetWhy.attention': {
    zh: '一个大数字加在等的会话，隔着房间就能看清。',
    en: 'One large number and the sessions behind it, readable across a room.',
  },
  'board.preset.wall': { zh: '会话墙', en: 'Session wall' },
  'board.presetWhy.wall': {
    zh: '每个会话一块，几十个一眼扫完。',
    en: 'One tile per session, for taking in dozens at once.',
  },
  'board.preset.queue': { zh: '等待队列', en: 'Queue' },
  'board.presetWhy.queue': {
    zh: '按等了多久排，最久的在最上面。',
    en: 'Sorted by how long each has waited, longest first.',
  },
  'board.preset.machine': { zh: '只看机器', en: 'The machine' },
  'board.presetWhy.machine': {
    zh: 'CPU、内存、磁盘、交换区，加上最耗资源的会话。',
    en: 'CPU, memory, disk and swap, plus what is costing the most.',
  },
  'board.preset.health': { zh: '有没有出事', en: 'Anything broken' },
  'board.presetWhy.health': {
    zh: '退出、异常退出、压力，和出问题的那几个会话。',
    en: 'Exits, crashes, pressure, and the sessions in trouble.',
  },
  'board.preset.projects': { zh: '按项目看', en: 'By project' },
  'board.presetWhy.projects': {
    zh: '每个项目的进度和花费，而不是每个会话。',
    en: 'Progress and spend per project rather than per session.',
  },
  'board.preset.glance': { zh: '路过一眼', en: 'At a glance' },
  'board.presetWhy.glance': {
    zh: '四个数字和一个钟。给每天路过的那块屏。',
    en: 'Four numbers and a clock, for a screen somebody walks past.',
  },
  'board.preset.single': { zh: '就一个数', en: 'A single number' },
  'board.presetWhy.single': {
    zh: '一块屏，一个数字，占满。',
    en: 'One screen, one figure, filling it.',
  },
  'board.preset.spendToday': { zh: '今天花了多少', en: "Today's spend" },
  'board.presetWhy.spendToday': {
    zh: '今天的 token 和请求数，按 agent 分。',
    en: "Today's tokens and requests, split by agent.",
  },
  'board.preset.spendMonth': { zh: '这个月花了多少', en: "This month's spend" },
  'board.presetWhy.spendMonth': {
    zh: '本月合计、每天的柱子、每个项目的份额。',
    en: 'The month, day by day, and which project it went to.',
  },
  'board.preset.cost': { zh: '钱去哪了', en: 'Where it went' },
  'board.presetWhy.cost': {
    zh: '按 agent、按项目、按月，四张表。',
    en: 'By agent, by project, by month. Four tables and no charts.',
  },
  'board.preset.year': { zh: '这一年', en: 'The year' },
  'board.presetWhy.year': {
    zh: '53 周的格子图，加上每个月的合计。',
    en: 'Fifty-three weeks as a grid, with the monthly totals under it.',
  },
  'board.preset.dense': { zh: '全都要', en: 'Everything' },
  'board.presetWhy.dense': {
    zh: '给真的凑近了在看的人。',
    en: 'For somebody who is actually looking.',
  },
  'board.preset.answer': { zh: '等着回话的', en: 'Needs an answer' },
  'board.presetWhy.answer': {
    zh: '只排在等你的那些，别的一概不显示。屏幕越大排得越开。',
    en: 'Only the ones waiting on you, laid out as wide as the screen allows.',
  },
  'board.preset.pulse': { zh: '现在多忙', en: 'How busy right now' },
  'board.presetWhy.pulse': {
    zh: '每小时烧多少、在跑几个、这两周的走势。',
    en: 'Tokens per hour, what is running, and the last fortnight.',
  },
  'board.preset.rotating': { zh: '三页轮播', en: 'Three pages, cycling' },
  'board.presetWhy.rotating': {
    zh: '要回话的、在跑的、花了多少，每 20 秒换一页。',
    en: 'What needs answering, what is running, what it costs — twenty seconds each.',
  },
  'board.preset.boss': { zh: '给老板看', en: 'For your boss' },
  'board.presetWhy.boss': {
    zh: '花了多少，和做出来了什么，并排放。',
    en: 'What it cost and what came out of it, side by side.',
  },
  'board.preset.leadership': { zh: '给领导看', en: 'For leadership' },
  'board.presetWhy.leadership': {
    zh: '进度、本月、这一年。数字少，字大。',
    en: 'Progress, the month, the year. Few numbers, large ones.',
  },
  'board.preset.models': { zh: '哪个模型在干活', en: 'Which model is working' },
  'board.presetWhy.models': {
    zh: '按模型和 agent 拆开，加上烧的速度。',
    en: 'Split by model and by agent, with the rate beside it.',
  },

  'board.metric.waiting': { zh: '在等你', en: 'Waiting' },
  'board.metric.working': { zh: '在工作', en: 'Working' },
  'board.metric.done': { zh: '已完成', en: 'Done' },
  'board.metric.sessions': { zh: '会话数', en: 'Sessions' },
  'board.metric.projects': { zh: '项目数', en: 'Projects' },
  'board.metric.crashed': { zh: '异常退出', en: 'Crashed' },
  'board.metric.exited': { zh: '已退出', en: 'Exited' },
  'board.metric.longestWait': { zh: '等最久的', en: 'Longest wait' },
  'board.metric.cpu': { zh: 'CPU', en: 'CPU' },
  'board.metric.memory': { zh: '内存', en: 'Memory' },
  'board.metric.disk': { zh: '磁盘', en: 'Disk' },
  'board.metric.swap': { zh: '交换区', en: 'Swap' },
  'board.metric.load': { zh: '负载', en: 'Load' },
  'board.metric.uptime': { zh: '运行时长', en: 'Uptime' },
  'board.metric.tokensToday': { zh: '今天的 token', en: 'Tokens today' },
  'board.metric.tokensMonth': { zh: '本月的 token', en: 'Tokens this month' },
  'board.metric.tokensWindow': { zh: '近 30 天的 token', en: 'Tokens, 30 days' },
  'board.metric.requestsToday': { zh: '今天的请求', en: 'Requests today' },

  'board.filter.all': { zh: '全部', en: 'Everything' },
  'board.filter.active': { zh: '还在跑的', en: 'Still running' },
  'board.filter.waiting': { zh: '在等你的', en: 'Waiting for you' },
  'board.filter.trouble': { zh: '出问题的', en: 'In trouble' },

  'board.order.state': { zh: '按状态', en: 'By state' },
  'board.order.waited': { zh: '按等待时长', en: 'By how long it waited' },
  'board.order.cpu': { zh: '按 CPU', en: 'By CPU' },

  'board.palette': { zh: '组件库', en: 'Widgets' },
  'board.templates': { zh: '现成模版', en: 'Templates' },
  'board.canvasEmpty': { zh: '从右边拖一个过来', en: 'Drag a widget in from the right' },
  'board.grab': { zh: '拖动 {name}', en: 'Drag {name}' },
  'board.pickOne': { zh: '点一个组件来改它', en: 'Pick a widget to change it' },
  'board.newLink': { zh: '还没建，先排版', en: 'Not created yet' },
  'board.density': { zh: '信息密度', en: 'Density' },
  'board.densitySpare': { zh: '只放大数字', en: 'One thing at a time' },
  'board.densityNormal': { zh: '正常', en: 'Normal' },
  'board.densityDense': { zh: '尽量多放', en: 'As much as it knows' },
  'board.edit': { zh: '编辑看板', en: 'Edit the board' },
  'board.editing': { zh: '正在编辑：{name}', en: 'Editing {name}' },
  'board.preset': { zh: '从哪个开始', en: 'Start from' },
  'board.widgets': { zh: '{n} 个组件', en: '{n} widgets' },
  'board.add': { zh: '加一个', en: 'Add' },
  'board.remove': { zh: '删掉', en: 'Remove' },
  'board.up': { zh: '往前', en: 'Move up' },
  'board.down': { zh: '往后', en: 'Move down' },
  'board.width': { zh: '宽度', en: 'Width' },
  'board.widthOf': { zh: '{n}/12 宽', en: '{n}/12' },
  'board.days': { zh: '天数', en: 'Days' },
  'board.caption': { zh: '写点什么', en: 'What it says' },
  'board.metric': { zh: '看哪个数', en: 'Metric' },
  'board.filter': { zh: '显示哪些', en: 'Show' },
  'board.order': { zh: '排序', en: 'Order' },
  'board.save': { zh: '保存', en: 'Save' },
  'board.cancel': { zh: '不改了', en: 'Cancel' },
  'board.full': { zh: '最多 {n} 个组件', en: 'At most {n} widgets' },
  'board.empty': { zh: '至少留一个组件', en: 'Keep at least one widget' },
  'board.saved': { zh: '已保存', en: 'Saved' },
  'board.by': { zh: '怎么拆', en: 'Split by' },
  'board.group': { zh: '怎么分组', en: 'Group' },
  'board.rotate': { zh: '轮播', en: 'Rotate pages' },
  'board.rotateList': { zh: '列表翻页', en: 'Page the list' },
  'board.rotateNever': { zh: '不轮播', en: 'Never' },
  'board.rotateEvery': { zh: '每 {n} 秒', en: 'every {n}s' },
  'board.page': { zh: '第几页', en: 'Page' },
  'board.pageOf': { zh: '第 {n} 页', en: 'Page {n}' },
  'board.height': { zh: '高度', en: 'Height' },
  'board.heightOf': { zh: '{n} 行高', en: '{n} rows' },
  'board.fill': { zh: '铺满整屏', en: 'Fill the screen' },
  'board.screen': { zh: '放在哪块屏', en: 'For which screen' },
  'board.saving': { zh: '正在保存…', en: 'Saving…' },
  'board.live': { zh: '改动直接上屏', en: 'Changes go live' },
  'board.locked': { zh: '已锁定，先解锁再改', en: 'Locked. Unlock to edit.' },

  'share.scope': { zh: '这个链接给谁看什么', en: 'What this link is about' },
  'share.scopeWhole': { zh: '整个面板', en: 'The whole panel' },
  'share.scopeProject': { zh: '项目：{name}', en: 'Project: {name}' },
  'share.scopeSession': { zh: '会话：{name}', en: 'Session: {name}' },
  'share.scopeGone': { zh: '指向的东西没了', en: 'what it pointed at is gone' },
  'share.untitled': { zh: '未命名', en: 'untitled' },

  // The field names above the two forms. Short, because they sit over the
  // control rather than inside it -- `share.name`, `share.remark` and
  // `share.scope` are the longer prompts, and a prompt that has to be read
  // before you can name the field is a placeholder, which is what these
  // replaced.
  'share.nameLabel': { zh: '名字', en: 'Name' },
  'share.remarkLabel': { zh: '屏幕名', en: 'Screen name' },
  'share.scopeLabel': { zh: '范围', en: 'About' },

  // The first-run tour. Two of its five steps do something -- they install
  // the state reporters and offer the Claude Code settings -- and the rest is
  // the orientation that makes those two make sense.
  'tour.title': { zh: '先花一分钟', en: 'One minute, first' },
  'tour.step': { zh: '第 {n} / {of} 步', en: 'Step {n} of {of}' },
  'tour.back': { zh: '上一步', en: 'Back' },
  'tour.next': { zh: '下一步', en: 'Next' },
  'tour.done': { zh: '开始用', en: 'Start' },
  'tour.skip': { zh: '跳过，不再显示', en: 'Skip, and do not show again' },
  'tour.on': { zh: '已开启', en: 'on' },
  'tour.turnOn': { zh: '开启', en: 'Turn on' },

  'tour.again': { zh: '再看一遍', en: 'Show it again' },
  'tour.againWhat': {
    zh: '五步里有两步是真的在做事：装状态上报，和 Claude Code 的其他设置。',
    en: 'Two of the five steps do something: state reporting, and Claude Code\'s other settings.',
  },
  'tour.introH': { zh: '进程归 tmux，不归这个面板', en: 'tmux owns the processes, not this panel' },
  'tour.intro1': {
    zh: '每个会话都是一个 tmux 会话。关掉浏览器、重启面板、升级版本，里面的 agent 照样在跑。',
    en: 'Every session is a tmux session. Close the browser, restart the panel, upgrade it — the agent inside keeps running.',
  },
  'tour.intro2': {
    zh: '左边是会话，中间是终端，右边是文件和笔记。会话按状态排序，等你处理的排最前。',
    en: 'Sessions on the left, the terminal in the middle, files and notes on the right. Sessions sort by state, and the ones waiting for you come first.',
  },

  'tour.hooksH': { zh: '让 agent 自己报状态', en: 'Let the agents report their own state' },
  'tour.hooks1': {
    zh: '不开这个，面板只能看见「有个进程在跑」。agent 做完了进程还在，所以会一直是蓝的。',
    en: 'Without this the panel sees a running process and nothing else. A finished agent is still a running process, so it stays blue.',
  },
  'tour.hooks2': {
    zh: '开启会往对应工具的配置文件里加几行，先备份，随时可以在设置里撤掉。',
    en: 'Turning it on adds a few lines to that tool\'s own configuration file. It is backed up first, and the settings page removes it again.',
  },
  'tour.hooksExisting': {
    zh: '开启前就开着的会话还是靠猜。在里面输入 /hooks，或者重启那个 agent。',
    en: 'Sessions that were already open stay guessed. Run /hooks inside them, or restart the agent.',
  },

  'tour.tuneH': { zh: 'Claude Code 的其他设置', en: 'The rest of Claude Code\'s settings' },
  'tour.tune1': {
    zh: '同一个文件里，还有几条决定什么东西离开这台机器、agent 往 git 历史里写什么。',
    en: 'The same file has a few more: what leaves this machine, and what the agent writes into your git history.',
  },

  'tour.projectH': { zh: '加一个项目', en: 'Add a project' },
  'tour.project1': {
    zh: '项目就是一个目录。左上角的加号选目录，然后在里面开会话。',
    en: 'A project is a directory. The plus at the top left picks one, and sessions are started inside it.',
  },
  'tour.project2': {
    zh: '每个会话可以选一个启动方式：claude、codex、opencode，或者就是一个 shell。',
    en: 'Each session picks how it starts: claude, codex, opencode, or just a shell.',
  },

  'tour.restH': { zh: '其余的在哪', en: 'Where the rest is' },
  'tour.rest1': {
    zh: '端口、域名、TLS、访问白名单在设置页的「这个面板」里改，改完点重启。',
    en: 'Port, domain, TLS and who may reach it are under "This panel" in settings. Press restart after.',
  },
  'tour.rest2': {
    zh: '重启只断开连接，会话不受影响 —— 这是这套架构唯一真正的承诺。',
    en: 'A restart costs the connection and nothing else. That is what this architecture promises.',
  },

  // The notes tab's second scope. Pressing the tab you are already on swaps
  // between them, so the name has to say which one you are looking at.
  'panel.notesGlobal': { zh: '全局笔记（再点回项目）', en: 'Global notes (press again for the project)' },

  'guessed.installed': {
    zh: '装 hook 前开着的会话还在靠猜。在里面输入 /hooks 或重启它。',
    en: 'Sessions open before reporting was installed are still guessed. Run /hooks in each, or restart the agent.',
  },
  // Says which state is *never* reached, not which one is unreliable.
  //
  // It used to say "waiting for you can be missed", which understates it by a
  // lot and reads as an edge case. Without reporting, a finished agent is
  // still a running process, and the heuristic has no way to tell that from
  // one that is thinking -- so every session that has finished stays blue,
  // permanently, and the first thing anybody asks is why.
  'guessed.notInstalled': {
    zh: '状态靠猜：agent 还在跑就一直是蓝的，做完了也不会变绿。点这里打开上报。',
    en: 'Guessed: a finished agent stays blue, because its process is still running. Turn on reporting.',
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
  'spend.today': { zh: '今日消耗', en: 'Today' },
  'spend.rangeDays': { zh: '近 {n} 天', en: 'Last {n} days' },
  'spend.week': { zh: '本周消耗', en: 'This week' },
  'spend.thisProject': { zh: '本项目消耗', en: 'This project' },
  'spend.noProject': { zh: '没选项目', en: 'no project selected' },
  'spend.headline': { zh: '消耗', en: 'Spend' },
  'spend.thisMonth': { zh: '本月', en: 'This month' },
  'spend.lastMonth': { zh: '上月', en: 'Last month' },
  'spend.sessionCount': { zh: '{n} 个 agent 会话', en: '{n} agent sessions' },
  'spend.source': { zh: '数据来源', en: 'Where this came from' },
  'spend.breakdown': { zh: '构成', en: 'Breakdown' },
  'spend.perRequest': { zh: '每次请求', en: 'Per request' },
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
    zh: '正在统计…',
    en: 'Counting…',
  },
  'spend.neverScanned': {
    zh: '暂无数据 —— 不是 0。',
    en: 'No data yet — not zero.',
  },
  // `{ago}` arrives already relative -- "3天前", "3 days ago" -- from
  // formatAgo, which is Intl's phrasing rather than a suffix table of ours.
  // It used to read '{ago}前读的' / 'read {ago} ago' and say the word twice.
  'spend.scannedAgo': { zh: '截至 {ago}前', en: 'as of {ago} ago' },
  'spend.whose': {
    zh: 'agent 自己记的账，包括不经过面板跑的。',
    en: "Counted from the agents' own records, including runs this panel did not start.",
  },
    'spend.agentSessionNote': { zh: '按 agent 自己的会话统计。', en: "Counted by the agent's own sessions." },
  'spend.sourceMissing': { zh: '{tool}：不知道（{why}）', en: '{tool}: unknown ({why})' },
  'spend.sourceRead': { zh: '{tool}：读了 {files} 个文件', en: '{tool}: {files} files read' },
  'spend.lowerBound': {
    zh: '{n} 条记录读不出来，下面是下限。',
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
/**
 * One string by a key that is only known at runtime, or null.
 *
 * The dashboard's widget kinds, presets and metrics are strings the *server*
 * owns: a board written by a newer build can name a widget this one has never
 * heard of, and `t` cannot be given a key it cannot type-check. Returning null
 * rather than the key itself is the point — a wall showing "board.kind.foo" has
 * put an internal identifier on a screen behind somebody's desk, and the
 * caller's own fallback is always better than that.
 *
 * A Go test walks the widget registry and fails if any kind, preset, metric,
 * filter or order here has no entry, so the null branch is for a *future*
 * server rather than for a translation somebody forgot.
 */
export function tKey(key: string, params?: Record<string, string | number>): string | null {
  const entry = (DICT as Record<string, Entry | undefined>)[key]
  if (!entry) return null
  let out = entry[current] ?? entry.en
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      out = out.replaceAll(`{${k}}`, String(v))
    }
  }
  return out
}

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
