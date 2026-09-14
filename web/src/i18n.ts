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
  'auth.originTitle': { zh: '这个地址还不被信任', en: 'This address is not trusted yet' },
  'auth.originBody': {
    zh: '不信任它，登录后每次改动都会被拒绝。',
    en: 'Without it, every change after signing in is refused.',
  },
  'auth.originYouAreOn': { zh: '你在', en: 'You are on' },
  'auth.originPanelThinks': { zh: '面板认为自己是', en: 'The panel believes it is' },
  'auth.originAdd': { zh: '信任这个地址', en: 'Trust this address' },
  'auth.originCancel': { zh: '返回', en: 'Back' },
  'auth.create': { zh: '创建账号', en: 'Create account' },
  'auth.signIn': { zh: '登录', en: 'Sign in' },
  'auth.or': { zh: '或', en: 'or' },
  'auth.usePasskey': { zh: '用 passkey 登录', en: 'Use a passkey' },
  'auth.noPasskeys': { zh: 'passkey 用不了：{why}。', en: 'Passkeys unavailable: {why}.' },
  'auth.notSupported': { zh: '这里不支持', en: 'not supported here' },
  // The three reasons the server can give, as codes rather than as sentences:
  // it sends `passkeyReason` and both the login page and the settings page
  // translate it. It used to send English prose straight into a Chinese page.
  'tzone.title': { zh: '时区', en: 'Time zone' },
  'tzone.what': {
    zh: '决定用量统计里「今天」从几点开始',
    en: 'Where the day starts for every per-day number.',
  },
  'tzone.machine': { zh: '这台机器的时区', en: "the machine's own zone" },
  'tzone.save': { zh: '保存', en: 'Save' },
  'tzone.useBrowser': { zh: '用这个浏览器的：{zone}', en: "Use this browser's: {zone}" },
  'tzone.todayIs': { zh: '面板认为今天是 {day}', en: 'The panel calls today {day}.' },
  'tzone.rescan': {
    zh: '改动后会重读一遍历史记录',
    en: 'Changing it re-reads the transcript history.',
  },
  'tzone.saved': { zh: '已保存', en: 'Saved.' },
  'tzone.rebuilding': { zh: '已保存，正在重算历史', en: 'Saved; rebuilding the history.' },
  'tzone.failed': { zh: '没保存成功', en: 'Not saved.' },
  'env.grpReach': { zh: '怎么访问', en: 'How people reach it' },
  'env.grpTls': { zh: '证书', en: 'Certificate' },
  'env.grpAccess': { zh: '谁能连', en: 'Who may connect' },
  'env.lblSocket': { zh: 'tmux socket', en: 'tmux socket' },
  'env.lblAddr': { zh: '监听地址', en: 'Listen on' },
  'env.lblDomain': { zh: '域名', en: 'Domain' },
  'env.domainAlso': { zh: '也是 passkey 的 Relying Party ID', en: 'Also the passkey relying party ID.' },
  'env.lblTlsMode': { zh: '方式', en: 'How' },
  'env.lblPublicPort': { zh: '外部端口', en: 'Public port' },
  'env.publicPortWhy': { zh: '留空表示与监听端口相同', en: 'Empty means the same as the listen port.' },
  'env.lblPublicOrigins': { zh: '其他外部地址', en: 'Other public addresses' },
  'env.publicOriginsWhy': {
    zh: '反向代理未转发 Host 时填写，逗号分隔',
    en: 'For a proxy that does not forward Host. Comma separated.',
  },
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
  // Not sent by the server any more: whether the connection is secure is the
  // browser's to answer, and behind nginx the panel's own TLS mode says
  // nothing about it. This is what the *frontend* shows when
  // window.PublicKeyCredential is missing, which is exactly the non-secure
  // case.
  'pk.insecure': {
    zh: 'passkey 需要 HTTPS（localhost 除外）',
    en: 'A passkey needs HTTPS, or localhost.',
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
  'panel.page': { zh: '分享页面', en: 'Share page' },
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
  'files.preview': { zh: '把这个目录当网页打开', en: 'Serve this directory as a page' },
  'files.previewCopied': { zh: '预览链接已复制', en: 'Preview link copied' },
  'files.previewSealed': { zh: '复制预览链接', en: 'Copy a preview link' },
  'files.previewSealedWhy': { zh: '页面不能联网，安全但 CDN 会被挡', en: 'The page cannot reach the network; a CDN is blocked' },
  'files.previewExternal': { zh: '复制预览链接（允许 CDN）', en: 'Copy a preview link that may load a CDN' },
  'files.previewExternalWhy': { zh: '脚本能带走这个目录的内容', en: 'A script it loads could take this directory somewhere' },
  'files.previewAdvanced': { zh: '高级…', en: 'Advanced…' },
  'files.previewAddress': { zh: '网址', en: 'Address' },
  'files.previewAddressAuto': { zh: '留空则随机', en: 'Empty for a random one' },
  'files.previewAddressWhy': { zh: '取了名字就等于公开：谁猜到谁能看', en: 'A name you choose is public: whoever guesses it can look' },
  'files.previewExpiry': { zh: '有效期', en: 'Expires' },
  'files.previewHour': { zh: '1 小时', en: 'in an hour' },
  'files.previewDay': { zh: '1 天', en: 'in a day' },
  'files.previewWeek': { zh: '7 天', en: 'in a week' },
  'files.previewNever': { zh: '永不过期', en: 'never' },
  'files.previewCreate': { zh: '创建并复制', en: 'Create and copy' },
  'files.previewCancel': { zh: '取消', en: 'Cancel' },
  'files.newFolder': { zh: '新建目录，叫什么？', en: 'New directory, called what?' },
  'files.count': { zh: '{n} 项', en: '{n} items' },
  'files.modified': { zh: '改动时间', en: 'Modified' },

  // The side panel's checklist is gone; this line is not. It is the one entry
  // in the dictionary whose two languages take *different placeholders* —
  // Chinese counts what is done, English counts what is left — which is the
  // property i18n.test.ts exists to hold, and share pages still count
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
  // Shown when the typed path is one the picker cannot resolve to a place --
  // `~someone`, whose home only a shell knows. It used to say "outside home,
  // so it cannot be listed", which was the truth while home was also the root
  // and is a lie now that every directory on the machine can be listed.
  'dir.willUse': {
    zh: '解析不出这是哪里 —— 回车按原样使用这个路径',
    en: 'Cannot tell where this is — Enter takes the path as typed',
  },
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
  'touch.showKeys': { zh: '显示按键', en: 'Show the keys' },
  'touch.hideKeys': { zh: '收起按键', en: 'Hide the keys' },
  'compose.attach': { zh: '选图片或文件', en: 'Pick an image or a file' },
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
  'profile.reorder': { zh: '拖动排序', en: 'Drag to reorder' },
  'profile.envTemplate': { zh: '填入 {names}', en: 'Fill in {names}' },
  'profile.overriddenTag': { zh: '已改', en: 'edited' },
  'profile.restore': { zh: '恢复自带的', en: 'Restore the built-ins' },
  'profile.restoreTitle': { zh: '恢复自带的预设？', en: 'Restore the built-in profiles?' },
  'profile.restoreBody': {
    zh: '被隐藏的会回来，对自带预设的修改会撤销。你自己建的不受影响。',
    en: 'Hidden ones come back and edits to them are undone. Your own are untouched.',
  },
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
  'upd.secretHint': { zh: '{user} 的系统密码，不是面板密码', en: '{user}’s system password, not the panel’s' },
  'upd.noPassword': {
    zh: '这台机器的 sudo 执行升级不需要密码',
    en: 'sudo on this machine runs the upgrade without a password',
  },
  'upd.wrongPassword': {
    zh: 'sudo 没有接受这个密码。它要的是 {who} 的密码',
    en: 'sudo did not accept that password. It wants {who}’s password',
  },
  'upd.needPassword': { zh: 'sudo 需要密码', en: 'sudo wants a password' },
  'upd.notAllowed': {
    zh: 'sudo 不允许 {user} 执行升级，请用有权限的账号在终端里升级',
    en: 'sudo does not let {user} run the upgrade; upgrade from a shell as an account it does',
  },
  'upd.cannotElevate': {
    zh: 'NoNewPrivileges 使 sudo 无法提权，请在终端运行下面命令',
    en: 'sudo cannot become root from the panel (NoNewPrivileges); run the command below in a shell',
  },
  'upd.needsTty': {
    zh: 'sudo 只允许在终端里用（requiretty），请在终端运行下面的命令',
    en: 'sudo only runs from a terminal here (requiretty); run the command below in one',
  },
  'upd.elevated': {
    zh: '已授权，正在后台升级并重启面板…',
    en: 'Authorised; upgrading and restarting in the background…',
  },
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
    zh: '限制 Claude Code 上传的数据，并控制写入 git 记录的内容',
    en: 'Limit what Claude Code uploads, and what it writes into your git history.',
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
    zh: 'Codex 仅有一个 notify，只能上报「等你处理」',
    en: 'Codex has a single notify command, so it reports waiting only.',
  },
  'set.showWrites': { zh: '查看写入内容', en: 'Show what it writes' },
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
  'share.revoke': { zh: '吊销', en: 'Revoke' },
  'share.revokeSure': { zh: '确定吊销？', en: 'Revoke it?' },
  'share.keep': { zh: '算了', en: 'Keep it' },
  'share.expiresOn': { zh: '{date} 过期', en: 'expires {date}' },
  'share.noExpiry': { zh: '不过期', en: 'no expiry' },
  'share.open': { zh: '打开链接', en: 'Open the link' },
  'share.remark': { zh: '这块屏叫什么', en: 'What to call this screen' },
  'share.lock': { zh: '锁定', en: 'Lock' },
  'share.unlock': { zh: '解锁', en: 'Unlock' },
  'share.lockedRow': { zh: '已锁定', en: 'Locked' },
  'share.viewers': { zh: '{n} 块屏在看', en: '{n} watching' },
  'share.noViewers': { zh: '没人在看', en: 'Nobody watching' },

  'page.title': { zh: '分享', en: 'Sharing' },
  'page.why': {
    zh: '页面是 agent 写的 HTML，链接是某块屏打开它的地址。',
    en: 'Pages are HTML an agent writes; a link is the address a screen opens one at.',
  },
  'page.nameLabel': { zh: '名字', en: 'Name' },
  'page.namePlaceholder': { zh: '大厅那块屏', en: 'Lobby wall' },
  'page.template': { zh: '从哪开始', en: 'Start from' },
  'page.tpl.blank': { zh: '空白', en: 'Blank' },
  'page.tpl.wall': { zh: '会话墙', en: 'Session wall' },
  'page.tpl.spend': { zh: 'token 消耗', en: 'Token spend' },
  'page.tpl.built': { zh: '产出', en: 'What got built' },
  'page.tpl.glance': { zh: '手机速览', en: 'Phone glance' },
  'page.adopt': { zh: '已有的目录', en: 'An existing directory' },
  'page.dir': { zh: '目录', en: 'Directory' },
  'page.dirExisting': { zh: '里面有 vibepanel.json 的目录', en: 'A directory with a vibepanel.json' },
  'page.startAgent': { zh: '建好后开一个 agent 会话', en: 'Start an agent session in it' },
  'page.create': { zh: '新建页面', en: 'New page' },
  'page.none': { zh: '还没有分享页面', en: 'No pages yet' },
  'page.published': { zh: 'v{v} 已发布', en: 'v{v} published' },
  'page.unpublished': { zh: '未发布', en: 'Not published' },
  'page.links': { zh: '{n} 条链接', en: '{n} links' },
  'page.history': { zh: '版本', en: 'Versions' },
  'page.fork': { zh: '复制一份', en: 'Fork' },
  'page.delete': { zh: '删除页面', en: 'Delete page' },
  'page.deleteSure': { zh: '确定删除？', en: 'Delete it?' },
  'page.dirGone': { zh: '目录不在了', en: 'Directory missing' },
  'page.noVersions': { zh: '还没发布过', en: 'Never published' },
  'page.current': { zh: '当前', en: 'Current' },
  'page.rollback': { zh: '回滚到这版', en: 'Roll back' },
  'page.changed': { zh: '改了 {n} 处', en: '{n} changed' },
  'page.same': { zh: '和已发布的一样', en: 'Same as published' },
  'page.problems': { zh: '{e} 错误 · {w} 提醒', en: '{e} errors · {w} warnings' },
  'page.sdkStale': { zh: 'SDK 副本旧了', en: 'SDK copy is old' },
  'page.screen': { zh: '屏幕', en: 'Screen' },
  'page.liveScreen': { zh: '正在看的屏 {size}', en: 'Live screen {size}' },
  'page.viewportOption': { zh: '{name} · {w}×{h}', en: '{name} · {w}×{h}' },
  'page.data': { zh: '数据', en: 'Data' },
  'page.liveCounts': { zh: '实时 · 只有数字', en: 'Live · counts' },
  'page.liveNames': { zh: '实时 · 带名字', en: 'Live · names' },
  'page.fixture': { zh: '样例 · {name}', en: 'Fixture · {name}' },
  'page.pick': { zh: '点选元素给 agent', en: 'Point at an element for the agent' },
  'page.pickNoSession': { zh: '先选中这个项目的会话', en: 'Select a session in this project first' },
  'page.reload': { zh: '刷新', en: 'Reload' },
  'page.publish': { zh: '发布', en: 'Publish' },
  'page.publishNow': { zh: '发布为 v{v}', en: 'Publish v{v}' },
  'page.note': { zh: '改了什么', en: 'What changed' },
  'page.added': { zh: '新增', en: 'added' },
  'page.modified': { zh: '修改', en: 'changed' },
  'page.removed': { zh: '删除', en: 'removed' },
  'page.trial': { zh: '在屏上试看', en: 'Try on a screen' },
  'page.trialScreen': { zh: '哪块屏', en: 'Which screen' },
  'page.trialFor': { zh: '多久', en: 'For how long' },
  'page.minutes': { zh: '{n} 分钟', en: '{n} min' },
  'page.trialStart': { zh: '开始试看', en: 'Start' },
  'page.trialRunning': { zh: '{name} 在试看 v{v}，{time} 恢复', en: '{name} is trying v{v} until {time}' },
  'page.trialKeep': { zh: '保留', en: 'Keep' },
  'page.trialEnd': { zh: '现在恢复', en: 'Revert now' },
  'page.trialShort': { zh: '试看 v{v}', en: 'trying v{v}' },
  'page.say': { zh: '跟 agent 说…', en: 'Tell the agent…' },
  'page.send': { zh: '发送', en: 'Send' },
  'page.version': { zh: '版本', en: 'Version' },
  'page.followPublished': { zh: '跟随发布 (v{v})', en: 'Follow published (v{v})' },
  'page.pinned': { zh: '固定 v{v}', en: 'Pinned v{v}' },
  'page.noParams': { zh: '这个页面没有可调的设置', en: 'This page has no settings' },
  'page.firstPrompt': { zh: '先读 AGENTS.md。这个页面要做成：', en: 'Read AGENTS.md first. Make this page: ' },
  'page.restoredPrompt': {
    zh: '这个目录是从已发布的版本恢复的。先读 AGENTS.md。接下来要改：',
    en: 'This directory was restored from the published version. Read AGENTS.md first. Next: ',
  },
  'page.open': { zh: '打开', en: 'Open' },
  'page.openWhy': { zh: '打开它的 page- 项目，旁边是预览', en: 'Open its page- project, with the Preview beside it' },
  'page.restore': { zh: '恢复并打开', en: 'Restore and open' },
  'page.restoreWhy': {
    zh: '目录不在了：写回已发布的版本，再打开',
    en: 'The directory is gone: write the published version back, then open it',
  },
  'page.publishWhy': {
    zh: '把目录现在的样子发布成新版本，跟随发布的链接都会换上',
    en: 'Publish the directory as it is now; links following the published version switch to it',
  },
  'page.dirGoneRestore': { zh: '目录不在了，打开时从 v{v} 恢复', en: 'Directory missing; Open restores v{v}' },
  'page.openFailed': { zh: '找不到这个页面的项目', en: "Could not find this page's project" },
  'share.view': { zh: '看看这块屏（15 分钟的临时链接）', en: 'See this screen (a 15-minute link)' },
  'share.edit': { zh: '编辑', en: 'Edit' },
  'share.needsPublish': { zh: '发布之后才能新建链接', en: 'Publish before making a link' },
  'share.cancel': { zh: '取消', en: 'Cancel' },
  'share.editDone': { zh: '完成', en: 'Done' },
  'share.groupScreen': { zh: '这块屏', en: 'This screen' },
  'share.groupAccess': { zh: '能看到什么', en: 'What it can see' },
  'share.groupParams': { zh: '页面设置', en: 'Page settings' },
  'page.docs': { zh: '详细文档', en: 'Docs' },
  'page.import': { zh: '导入', en: 'Import' },
  'page.importWhy': { zh: '从 zip 新建一个页面（不会自动发布）', en: 'Make a page from a zip (not published)' },
  'page.imported': { zh: '已导入「{name}」，还没发布', en: 'Imported "{name}"; not published yet' },
  'page.export': { zh: '导出为 zip', en: 'Export as zip' },
  'page.root': { zh: '新页面放在', en: 'New pages go in' },
  'page.rootDefault': { zh: '（默认）', en: '(default)' },
  'page.rootSetting': { zh: '（自定义）', en: '(custom)' },
  'page.rootFallback': { zh: '（回退）', en: '(fallback)' },
  'page.rootChange': { zh: '更改', en: 'Change' },
  'page.rootSave': { zh: '保存', en: 'Save' },
  'page.rootReset': { zh: '恢复默认', en: 'Use the default' },
  'page.rootPlaceholder': { zh: '绝对路径，留空用默认', en: 'An absolute path; empty for the default' },
  'page.rootProblem': { zh: '没用上：{why}', en: 'Skipped: {why}' },

  'share.scopeWhole': { zh: '整个面板', en: 'The whole panel' },
  'share.scopeProject': { zh: '项目：{name}', en: 'Project: {name}' },
  'share.scopeSession': { zh: '会话：{name}', en: 'Session: {name}' },
  'share.scopeGone': { zh: '指向的东西没了', en: 'what it pointed at is gone' },
  'share.untitled': { zh: '未命名', en: 'untitled' },

  // The field names above the link forms. Short, because they sit over the
  // control rather than inside it.
  'share.nameLabel': { zh: '名字', en: 'Name' },
  'share.remarkLabel': { zh: '屏幕名', en: 'Screen name' },
  'share.scopeLabel': { zh: '范围', en: 'About' },

  // The first-run tour. Two of its five steps do something -- they install
  // the state reporters and offer the Claude Code settings -- and the rest is
  // the orientation that makes those two make sense.
  'tour.title': { zh: '首次使用', en: 'First run' },
  'tour.notSaved': { zh: '没记住，下次还会显示', en: 'Not saved; it will show again.' },
  'tour.step': { zh: '第 {n} / {of} 步', en: 'Step {n} of {of}' },
  'tour.back': { zh: '上一步', en: 'Back' },
  'tour.next': { zh: '下一步', en: 'Next' },
  'tour.done': { zh: '开始使用', en: 'Start' },
  'tour.skip': { zh: '跳过，不再显示', en: 'Skip, and do not show again' },
  'tour.on': { zh: '已开启', en: 'on' },
  'tour.turnOn': { zh: '开启', en: 'Turn on' },

  'tour.again': { zh: '再看一遍', en: 'Show it again' },
  'tour.againWhat': {
    zh: '其中两步会实际改动配置：状态上报，和 Claude Code 的其他设置',
    en: 'Two of the five steps change something: state reporting, and Claude Code\'s other settings.',
  },
  'tour.inSettings': { zh: '在设置里继续', en: 'Continue in settings' },
  'tour.notifyH': { zh: '有事找你的时候', en: 'When an agent wants you' },
  'tour.notify1': {
    zh: 'agent 需要确认时，这个浏览器会弹一条通知',
    en: 'This browser raises a notification when an agent needs an answer.',
  },
  'tour.notify2': {
    zh: '手机锁屏后页面会被冻结，那时要靠 webhook',
    en: 'A phone freezes the tab, and then only a webhook reaches you.',
  },
  'tour.tlsH': { zh: '连接加密', en: 'Encryption' },
  'tour.tlsOn': { zh: '这个连接已加密', en: 'This connection is encrypted.' },
  'tour.tlsOnWhy': {
    zh: '按浏览器实际用的协议判断，不看面板自己的设置',
    en: "Judged by the browser's own protocol, not the panel's setting.",
  },
  'tour.tlsOff': {
    zh: '现在是明文。同网络里的人能看到你输入的内容',
    en: 'This is plaintext. Anyone on the network can read what you type.',
  },
  'tour.tlsHow': {
    zh: '面板可以自己签证书，也可以由前面的反代来做',
    en: 'The panel can get its own certificate, or a proxy in front can.',
  },
  'tour.introH': { zh: '进程由 tmux 托管', en: 'Processes are held by tmux' },
  'tour.intro1': {
    zh: '会话由 tmux 托管。关闭浏览器、重启或升级面板，agent 不受影响',
    en: 'Sessions are held by tmux. Closing the browser or restarting the panel does not affect them.',
  },
  'tour.intro2': {
    zh: '左侧为会话，中间为终端，右侧为文件与笔记。会话按状态排序，待处理的在前',
    en: 'Sessions on the left, the terminal in the middle, files and notes on the right, sorted by state.',
  },

  'tour.hooksH': { zh: '状态上报', en: 'State reporting' },
  'tour.hooks1': {
    zh: '未启用时面板只能看到进程是否存在；agent 结束后进程仍在，状态不会变',
    en: 'Without it the panel only sees whether a process exists; a finished agent still has one.',
  },
  'tour.hooks2': {
    zh: '会向该工具的配置文件写入数行，写入前备份，可在设置中移除',
    en: 'It writes a few lines into that tool\'s configuration file, after backing it up. Removable in settings.',
  },
  'tour.hooksExisting': {
    zh: '启用前已存在的会话仍为推测状态，可执行 /hooks 或重启 agent',
    en: 'Sessions opened before this stay guessed. Run /hooks in them, or restart the agent.',
  },

  'tour.tuneH': { zh: 'Claude Code 的其他设置', en: 'The rest of Claude Code\'s settings' },
  'tour.tune1': {
    zh: '同一份配置里还有几项：数据上传，以及写入 git 记录的内容',
    en: 'The same file has more: data upload, and what is written into your git history.',
  },

  'tour.projectH': { zh: '添加项目', en: 'Add a project' },
  'tour.project1': {
    zh: '项目对应一个目录。用左上角的加号选择目录，并在其中创建会话',
    en: 'A project is a directory. The plus at the top left selects one; sessions are created inside it.',
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
  'spend.perDay': { zh: '日均', en: 'Daily average' },
  'spend.timesAverage': { zh: '今天是均值的 {x}×', en: 'Today is {x}× the average.' },
  'spend.summaryLine': {
    zh: '{n} 天 {total} · {requests} 次请求 · 每次 {each}',
    en: '{n} days · {total} · {requests} requests · {each} each',
  },
  'spend.whereItWent': { zh: '花在哪里', en: 'Where it went' },
  'spend.whatSpentIt': { zh: '什么花的', en: 'What spent it' },
  'spend.notInAProject': { zh: '未归入项目', en: 'Outside every project' },
  'spend.month': { zh: '按月', en: 'By month' },
  'spend.sessions': { zh: '按会话', en: 'By session' },
  'spend.projects': { zh: '按项目', en: 'By project' },
  'spend.tools': { zh: '按工具', en: 'By tool' },
  'spend.toolTitle': {
    zh: '{tool}：合计 {total}，输出 {output}，缓存读取 {cache}',
    en: '{tool}: {total} total, {output} output, {cache} cache read',
  },
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
 * For names the *server* owns, like a built-in launch profile's id: a newer
 * build can send one this build has no entry for, and `t` cannot be given a
 * key it cannot type-check. Null rather than the key itself, because the
 * caller's own fallback is always better than an internal identifier on
 * screen.
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
