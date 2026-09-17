package chat

import (
	"fmt"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// What the bridge says back.
//
// Every string a phone shows is here, in both languages, so a reviewer sees
// them side by side and the rule the panel's UI follows -- say the thing, do
// not argue for it -- is checkable in one file. Chinese first because that
// is what the person who asked for this reads; the language is a chat
// setting, not a guess from the IM.

// LangKey is the settings row for the chat language.
const LangKey = "chat.lang"

func pick(lang, zh, en string) string {
	if lang == "en" {
		return en
	}
	return zh
}

// msg renders one of the strings below with its arguments.
func msg(lang, key string, args ...any) string {
	s, ok := strs[key]
	if !ok {
		return key
	}
	return fmt.Sprintf(pick(lang, s[0], s[1]), args...)
}

var strs = map[string][2]string{
	"pairing": {
		"这个面板还不认识你。配对码 %s，在面板的「消息通道」页里输入后就能收到推送和回复。",
		"This panel does not know you yet. Pairing code %s. Enter it on the panel's Messaging page to be paired.",
	},
	"pairingHere": {
		"配对码要在面板的「消息通道」页里输入，发到这里没有用。%s",
		"The pairing code goes into the panel's Messaging page, not this chat. %s",
	},
	"paired":        {"已配对。回复「帮助」看能做什么。", "Paired. Reply help for what you can do."},
	"receipt":       {"→ [%d] 已送入", "→ [%d] sent"},
	"receiptFocus":  {"→ [%d] 已送入 · 默认会话", "→ [%d] sent · focus"},
	"stillWaiting":  {"（%s 还在等你）", "(%s still waiting on you)"},
	"receiptKey":    {"→ [%d] %s", "→ [%d] %s"},
	"receiptKeyCmd": {"→ [%d] %s：%s", "→ [%d] %s: %s"},
	"approved":      {"已允许", "allowed"},
	"denied":        {"已拒绝", "denied"},
	"answeredBy":    {"%s（%s）", "%s by %s"},
	"answeredElsewhere": {
		"[%[1]d] 已被 %[2]s %[3]s：%[4]s", "[%[1]d] was %[3]s by %[2]s: %[4]s",
	},
	"interrupted": {
		"已发送中断", "interrupt sent",
	},
	"notWaiting": {
		"[%d] 现在没有在等你。",
		"[%d] is not waiting on anything.",
	},
	"notWorking": {
		"[%d] 没在工作，不用打断。",
		"[%d] is not working; there is nothing to interrupt.",
	},
	"working": {
		"[%d] 正在工作。等它停下，或者回复「停 %d」打断。",
		"[%d] is working. Wait for it to stop, or reply stop %d to interrupt.",
	},
	"gone": {"[%d] 已经结束了。", "[%d] has ended."},
	"atPrompt": {
		"没有发送。[%d] 在等你允许：\n%s\n允许回「%d号可以」，拒绝回「%d号不行」。",
		"Not sent. [%d] is asking for permission:\n%s\nReply %d yes to allow, %d no to deny.",
	},
	"stopAtPrompt": {
		"[%d] 没在工作，在等你允许：\n%s\n不让它做就回「%d号不行」。",
		"[%d] is not working; it is asking for permission:\n%s\nTo stop it, reply %d no.",
	},
	"showPrompt": {
		"还没执行，这条你还没看过。[%d] 要你允许：\n%s\n允许回「%d号可以」，拒绝回「%d号不行」。",
		"Not done yet; you had not seen this. [%d] is asking for permission:\n%s\nReply %d yes to allow, %d no to deny.",
	},
	"stalePrompt": {
		"没有执行，你回的那条请求已经处理过了。[%d] 现在要你允许：\n%s\n允许回「%d号可以」，拒绝回「%d号不行」。",
		"Not done; that request was already handled. [%d] is now asking:\n%s\nReply %d yes to allow, %d no to deny.",
	},
	"staleGone": {
		"没有执行，你回的那条请求已经处理过了，[%d] 现在没有在等你。",
		"Not done; that request was already handled, and [%d] is not waiting on anything now.",
	},
	"showQuestion": {
		"没有发送，这个问题你还没看过。[%d] 在问你：\n%s\n回「%d: 你的回答」。",
		"Not sent; you had not seen this question. [%d] is asking:\n%s\nReply %d: your answer.",
	},
	"showUnknownWait": {
		"还没执行。[%d] 在等你，面板不知道它具体问什么。先「屏幕 %d」看看，再回「%d号可以」。",
		"Not done yet. [%d] is waiting and the panel does not know on what. Reply screen %d to look, then %d yes.",
	},
	"unclearQuote": {
		"没有执行。你引用的消息说不清是哪个会话的哪条请求，回「编号号可以」，比如「%d号可以」。",
		"Not done. The quoted message does not say which request; reply with the number, e.g. %d yes.",
	},
	"quotedNotWaiting": {
		"没有执行。你引用的是 [%d]，它现在没在等你。",
		"Not done. You quoted [%d], which is not waiting on anything.",
	},
	"nothingWaiting": {"没有会话在等你允许。", "No session is waiting for permission."},
	"unclearCommand": {"没听懂，也没有发给任何会话。%s的说法是：%s", "Not understood, and nothing was sent. %s is: %s"},
	"hintPrompt":     {"允许回「%d号可以」，拒绝回「%d号不行」", "Reply %d yes to allow, %d no to deny"},
	"hintQuestion":   {"回「%d: 你的回答」", "Reply %d: your answer"},
	"handled":        {"已在电脑上处理", "handled at the computer"},
	"unfocused":      {"不再有默认会话，没写编号的话只发给唯一在等你的那个。", "No focus now; bare replies go only to the one session waiting."},
	"imageFailed":    {"图片没收到。", "The picture did not arrive."},
	"noShell":        {"[%d] 是一个 shell，没有提示可以回答。", "[%d] is a shell; there is no prompt to answer."},
	"noProfile": {
		"[%d] 跑的是 %s，面板不知道怎么替它按键。在「消息通道」页的按键表里加上。",
		"[%d] runs %s and the panel has no keys for it. Add them on the Messaging page.",
	},
	"several": {
		"有几个会话都在等你，说清楚是哪个：回复「编号: 你的话」，或者引用它的消息。\n%s",
		"Several sessions are waiting. Say which: reply \"number: your words\" or quote its message.\n%s",
	},
	"severalAnswer": {
		"有几个会话都在等你允许，说清楚是哪个，比如「%d号可以」。\n%s",
		"Several sessions are asking for permission. Say which, e.g. %d yes.\n%s",
	},
	"notYes": {
		"「%s」没有当成允许。[%d] 还在等：\n%s\n要允许回「%d号可以」。",
		"\"%s\" was not taken as allow. [%d] still waits:\n%s\nReply %d yes to allow.",
	},
	"mutedNote":    {"（[%d] 静音中，所以没推给你）", "([%d] is muted, so it was not sent to you)"},
	"othersAsking": {"%s 在等你允许。", "%s waiting for permission."},
	"otherMissed":  {"另有 %d 条通知没送到。", "%d other notices were not delivered."},
	"none": {
		"没有会话在等你，也没有默认会话。回复「编号: 你的话」指定一个，或者「列表」看看有哪些。",
		"No session is waiting and none is in focus. Reply \"number: your words\" to pick one, or list.",
	},
	"unknownHandle": {"没有 [%d] 这个会话。回复「列表」看看有哪些。", "There is no [%d]. Reply list to see them."},
	"unknownCommand": {
		"没有 /%s 这个命令，也没有发给任何会话。回复「帮助」看能做什么。",
		"There is no /%s command, and nothing was sent. Reply help for what you can do.",
	},
	"refuseAll": {
		"不能一次全部允许，每个请求要单独看。\n%s",
		"Requests cannot be allowed all at once; each one is answered on its own.\n%s",
	},
	"needHandle": {"要哪一个？加上编号，比如「%s 3」。", "Which one? Add the number, e.g. %s 3."},
	"listEmpty":  {"现在没有会话。", "No sessions right now."},
	"listHead":   {"会话（回复「编号: 你的话」即可对话）", "Sessions (reply \"number: your words\" to talk to one)"},
	"listMuted":  {"静音中", "muted"},
	"listFocus":  {"默认会话", "focus"},
	"screenHead": {"[%d] 屏幕", "[%d] screen"},
	"shotUnavailable": {
		"这个面板不能截图。", "This panel cannot take screenshots.",
	},
	"muted":        {"[%d] 已静音到 %s。回复「取消静音 %d」恢复。", "[%d] muted until %s. Reply unmute %d to undo."},
	"mutedDefault": {"没看懂「%s」，按两小时算。", "Did not understand \"%s\"; muted for two hours."},
	"unmuted":      {"[%d] 已恢复推送。", "[%d] unmuted."},
	"focused":      {"之后没写编号的话都发给 [%d]。", "Bare replies now go to [%d]."},
	"open":         {"[%d] 在面板里打开：%s", "[%d] in the panel: %s"},
	"noPublicURL": {
		"面板的地址是本机地址，手机打不开。在面板的 设置 → 本机 → 怎么访问 里填上域名。",
		"The panel's address is a local one a phone cannot open. Set a domain in Settings → This panel → How people reach it.",
	},
	"contextHead": {
		"[%d] 最近 %d 条", "[%d] last %d",
	},
	"contextEmpty": {"[%d] 还没有记录到任何消息。", "[%d] has no messages recorded yet."},
	"more":         {"（还有 %d 字，回「更多 %d」继续看）", "(%d more characters; reply more %d)"},
	"moreHead":     {"[%d] 续", "[%d] continued"},
	"moreEnd":      {"（完）", "(end)"},
	"noMore":       {"没有更多了。", "Nothing more."},
	"confirmStop": {
		"要打断 [%d] 吗？回复「确认」执行，「取消」放弃。", "Interrupt [%d]? Reply ok to confirm, cancel to drop it.",
	},
	"confirmSend": {
		"要给 [%d] 发送：\n%s\n回复「确认」发送，「取消」放弃。", "Send to [%d]:\n%s\nReply ok to confirm, cancel to drop it.",
	},
	"replaced": {
		"（刚才「%s」的确认已作废）", "(the earlier confirmation to %s is dropped)",
	},
	"describeStop":   {"打断 [%d]", "interrupt [%d]"},
	"describeSend":   {"发给 [%d]", "send to [%d]"},
	"describeAnswer": {"%s [%d]", "%s [%d]"},
	"nothingPending": {"没有等待确认的操作。", "Nothing is waiting for confirmation."},
	"cancelled":      {"已取消。", "Cancelled."},
	"expired":        {"那个操作已经过期了，重新发一次。", "That one has expired; send it again."},
	"missed": {
		"你不在的时候，这些在等你：",
		"While you were away these started waiting:",
	},
	"missedHeld": {
		"你不在的时候，这些在等你（刚才那条没有执行，看完再发一次）：",
		"While you were away these started waiting (your last message was not run; send it again after reading):",
	},
	"usage": {
		"今天：状态变化 %d 次，其中 %d 次在等你。\n助手：%d 次调用，$%.2f。",
		"Today: %d state changes, %d of them waiting on you.\nAssistant: %d calls, $%.2f.",
	},
	"noAssistant": {
		"这个聊天没有开高级模式，直接用「编号: 你的话」或「帮助」里的命令。",
		"Advanced mode is off for this chat. Use \"number: your words\" or the commands in help.",
	},
	"assistantBudget": {
		"助手今天的预算用完了，明天再试，或者直接用命令。",
		"The assistant's budget for today is spent. Try tomorrow, or use the commands.",
	},
	"assistantFailed": {"助手没答上来：%s", "The assistant could not answer: %s"},
	"confirmAnswer": {
		"[%d] 要你允许：\n%s\n要回「%s」吗？回复「确认」执行，「取消」放弃。",
		"[%d] is asking:\n%s\nAnswer \"%s\"? Reply ok to confirm, cancel to drop it.",
	},
	"help": {
		"回「%[1]d: 你的话」把话送进 [%[1]d]。它要你允许时回「%[1]d号可以」或「%[1]d号不行」。\n" +
			"列表 · 屏幕 %[1]d · 截图 %[1]d · 打开 %[1]d · 上下文 %[1]d\n" +
			"切到 %[1]d 之后默认发给它，不切了 取消 · 静音 %[1]d 半小时 · 停 %[1]d 打断 · 用量\n" +
			"长消息回「更多」往下看",
		"Reply \"%[1]d: your words\" to send them to [%[1]d]. When it asks for permission, %[1]d yes or %[1]d no.\n" +
			"list · screen %[1]d · shot %[1]d · open %[1]d · context %[1]d\n" +
			"focus %[1]d makes it the default, unfocus clears it · mute %[1]d 30m · stop %[1]d interrupt · usage\n" +
			"more continues a long message",
	},
	"helpAdvanced": {
		"高级模式：直接说你想做什么，或者「问：…」让助手去看。",
		"Advanced mode: just say what you want, or \"ask: …\" to have the assistant look.",
	},
	"stateWaiting": {"在等你", "waiting for you"},
	"stateWorking": {"在工作", "working"},
	"stateDone":    {"已停下", "done"},
	"kindPrompt":   {"要你允许", "needs your permission"},
	"kindQuestion": {"在问你", "is asking you"},
	"imageSaved": {
		"图片已存好，路径填在 [%d] 的输入框里，还没发送。接着发一句话说明它，会和图片一起送出。",
		"Picture saved; its path is typed into [%d] and not sent yet. Send a line about it and both go together.",
	},
	"imageSent": {"→ [%d] 图片和文字已送入", "→ [%d] picture and words sent"},
	"imageNoTarget": {
		"图片要给哪个会话？引用它的消息再发一次，或者先「切到 编号」。",
		"Which session is the picture for? Quote its message, or focus a number first.",
	},
	"linkLabel":    {"在面板打开", "open"},
	"menuPlan":     {"计划：", "The plan:"},
	"menuPlanHint": {"回「1」按计划执行（自动），「2」执行但逐个确认修改，或者直接写要改什么。", "Reply 1 to go ahead (auto), 2 to go ahead approving edits, or write what to change."},
	"menuStep":     {"（第 %d/%d 题）", "(question %d of %d)"},
	"menuHintSingle": {
		"回编号选一个；也可以直接写你自己的答案，或「2，补充说明」；回「跳过」先不答。",
		"Reply a number, or write your own answer, or \"2, extra words\"; skip to leave it.",
	},
	"menuHintMulti": {
		"可多选：回「1 3」；加自己的答案写成「1 3，你的话」；回「跳过」先不答。",
		"Several allowed: reply \"1 3\"; add your own as \"1 3, your words\"; skip to leave it.",
	},
	"menuHintPreview": {
		"回编号选一个；「2，补充说明」带备注；只写文字就作为备注提交。",
		"Reply a number; \"2, a note\" adds a note; words alone are sent as a note.",
	},
	"menuAtLaptop":      {"这道题带预览又是多选，手机上答不了，请在面板里答。", "This question has previews and several choices; answer it in the panel."},
	"menuScreen":        {"回「截图 %d」看看。", " Reply shot %d to look."},
	"menuEmpty":         {"[%d] 没收到答案。", "[%d] No answer in that."},
	"menuPlanPick":      {"[%d] 计划只能回「1」或「2」，或者直接写要改什么。", "[%d] Reply 1 or 2 for the plan, or write what to change."},
	"menuOutOfRange":    {"[%d] 没有这个编号。", "[%d] There is no such number."},
	"menuOnlyOne":       {"[%d] 这道题只能选一个。", "[%d] This question takes one choice."},
	"menuSkipped":       {"跳过了", "skipped"},
	"menuFeedback":      {"要改：%s", "change: %s"},
	"menuStale":         {"没有提交，你回的是之前的题。[%d] 现在问的是：", "Not sent; that was an earlier question. [%d] is now asking:"},
	"menuUnseen":        {"还没提交，这道题你还没看过。[%d] 在问：", "Not sent yet; you had not seen this. [%d] is asking:"},
	"menuNotYesNo":      {"没有执行。[%d] 问的是选择题，不是允许或拒绝：", "Not done. [%d] is asking a question with options, not for permission:"},
	"menuNotWords":      {"没有发送。[%d] 正在问你：", "Not sent. [%d] is asking you:"},
	"menuReceiptNext":   {"→ [%d] 已选：%s。下一题：", "→ [%d] answered: %s. Next:"},
	"menuReceipt":       {"→ [%d] 已回答：%s", "→ [%d] answered: %s"},
	"menuAnsweredBy":    {"已回答（%s）", "answered by %s"},
	"systemUnavailable": {"这个面板读不到机器状态。", "This panel cannot read the machine's state."},
	"systemHead":        {"机器状态 · 已运行 %s", "Machine · up %s"},
	"systemCPU":         {"CPU %.0f%%（%d 核）· 负载 %.2f / %.2f / %.2f", "CPU %.0f%% (%d cores) · load %.2f / %.2f / %.2f"},
	"systemCPUWait":     {"CPU 采样中（%d 核）· 负载 %.2f / %.2f / %.2f", "CPU sampling (%d cores) · load %.2f / %.2f / %.2f"},
	"systemMem":         {"内存 已用 %s / %s（%.0f%%）", "Memory %s / %s used (%.0f%%)"},
	"systemSwap":        {"交换 已用 %s / %s", "Swap %s / %s used"},
	"systemDisk":        {"磁盘 已用 %s / %s（%.0f%%），剩 %s", "Disk %s / %s used (%.0f%%), %s free"},
	"systemTop":         {"最占资源的会话：", "Sessions using the most:"},
	"systemAlertsOff":   {"告警：关", "Alerts: off"},
	"systemAlertsOn": {
		"告警：开 · CPU ≥%d%% 持续 %d 分钟 · 内存 ≥%d%% · 磁盘 ≥%d%%",
		"Alerts: on · CPU ≥%d%% for %d min · memory ≥%d%% · disk ≥%d%%",
	},
	"systemAlertsMuted": {"（你静音到 %s）", " (muted for you until %s)"},
	"alertCPU":          {"▲ 机器告警：CPU %.0f%%，已持续 %d 分钟。", "▲ Machine alert: CPU at %.0f%% for %d minutes."},
	"alertMem":          {"▲ 机器告警：内存已用 %.0f%%，只剩 %s。", "▲ Machine alert: memory %.0f%% used, %s left."},
	"alertDisk":         {"▲ 机器告警：磁盘已用 %.0f%%，只剩 %s（%s）。", "▲ Machine alert: disk %.0f%% used, %s left (%s)."},
	"alertTopCPU":       {"用得最多：[%d] %s · CPU %.0f%%", "Using the most: [%d] %s · CPU %.0f%%"},
	"alertTopMem":       {"用得最多：[%d] %s · 内存 %s", "Using the most: [%d] %s · memory %s"},
	"recoveredCPU":      {"✓ 机器恢复：CPU 降到 %.0f%%。", "✓ Machine recovered: CPU down to %.0f%%."},
	"recoveredMem":      {"✓ 机器恢复：内存降到 %.0f%%。", "✓ Machine recovered: memory down to %.0f%%."},
	"recoveredDisk":     {"✓ 机器恢复：磁盘降到 %.0f%%。", "✓ Machine recovered: disk down to %.0f%%."},
	"alertHint": {
		"回「系统」看详情，「静音告警 1小时」暂停。", "Reply system for details, mute alerts 1h to pause.",
	},
	"alertsMuted":      {"告警已静音到 %s。回「恢复告警」取消。", "Alerts muted until %s. Reply unmute alerts to undo."},
	"alertsUnmuted":    {"告警已恢复。", "Alerts are back on."},
	"mutedDefaultHour": {"没看懂「%s」，按一小时算。", "Did not understand \"%s\"; muted for an hour."},
}

// stateText renders a state, with the message kind sharpening "waiting".
func stateText(lang, state, kind string) string {
	switch session.State(state) {
	case session.StateWaiting:
		switch kind {
		case store.MessagePrompt:
			return msg(lang, "kindPrompt")
		case store.MessageQuestion:
			return msg(lang, "kindQuestion")
		}
		return msg(lang, "stateWaiting")
	case session.StateWorking:
		return msg(lang, "stateWorking")
	case session.StateDone:
		return msg(lang, "stateDone")
	}
	return state
}
