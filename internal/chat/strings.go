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
		"这个面板还不认识你。配对码 %s，在面板的「聊天」页里输入后就能收到推送和回复。",
		"This panel does not know you yet. Pairing code %s. Enter it on the panel's Chat page to be paired.",
	},
	"pairingHere": {
		"配对码要在面板的「聊天」页里输入，发到这里没有用。%s",
		"The pairing code goes into the panel's Chat page, not this chat. %s",
	},
	"paired":  {"已配对。回复「帮助」看能做什么。", "Paired. Reply help for what you can do."},
	"receipt": {"→ [%d] 已送入", "→ [%d] sent"},
	"receiptFocus": {
		"→ [%d] 已送入（没写编号的话默认发给它，「切到 编号」可以换）",
		"→ [%d] sent (bare replies go to it; focus <number> to change)",
	},
	"receiptKey":    {"→ [%d] %s", "→ [%d] %s"},
	"receiptKeyCmd": {"→ [%d] %s：%s", "→ [%d] %s: %s"},
	"approved":      {"已允许", "allowed"},
	"denied":        {"已拒绝", "denied"},
	"answeredBy":    {"%s（%s）", "%s by %s"},
	"answeredElsewhere": {
		"[%d] 已经被 %s %s：%s", "[%d] was %s by %s: %s",
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
		"[%d] 在等你允许：\n%s\n先回「%d: 好」或「%d: 不」，再发文字。",
		"[%d] is asking for permission:\n%s\nAnswer %d: yes or %d: no first, then send words.",
	},
	"showPrompt": {
		"[%d] 要你允许：\n%s\n确定就回「%d: 好」，不行回「%d: 不」。",
		"[%d] is asking for permission:\n%s\nReply %d: yes to allow, %d: no to deny.",
	},
	"stalePrompt": {
		"你回的那条已经过去了。[%d] 现在要你允许：\n%s\n确定就回「%d: 好」，不行回「%d: 不」。",
		"That request is over. [%d] is now asking:\n%s\nReply %d: yes to allow, %d: no to deny.",
	},
	"staleGone": {
		"你回的那条已经过去了，[%d] 现在没有在等你。",
		"That request is over; [%d] is not waiting on anything now.",
	},
	"showQuestion": {
		"[%d] 在问你：\n%s\n回复「%d: 你的回答」。",
		"[%d] is asking:\n%s\nReply %d: your answer.",
	},
	"showUnknownWait": {
		"[%d] 在等你，面板不知道它具体问什么。先「屏幕 %d」看看，再回「%d: 好」。",
		"[%d] is waiting and the panel does not know on what. Reply screen %d to look, then %d: yes.",
	},
	"imageFailed": {"图片没收到。", "The picture did not arrive."},
	"noShell":     {"[%d] 是一个 shell，没有提示可以回答。", "[%d] is a shell; there is no prompt to answer."},
	"noProfile": {
		"[%d] 跑的是 %s，面板不知道怎么替它按键。在「聊天」页的按键表里加上。",
		"[%d] runs %s and the panel has no keys for it. Add them on the Chat page.",
	},
	"several": {
		"有几个会话都在等你，说清楚是哪个：回复「编号: 你的话」，或者引用它的消息。\n%s",
		"Several sessions are waiting. Say which: reply \"number: your words\" or quote its message.\n%s",
	},
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
		"面板的地址是本机地址，手机打不开。在面板设置里填上外部能访问的地址。",
		"The panel's address is a local one a phone cannot open. Set a public address in the panel's settings.",
	},
	"contextHead": {
		"[%d] 最近 %d 条", "[%d] last %d",
	},
	"contextEmpty": {"[%d] 还没有记录到任何消息。", "[%d] has no messages recorded yet."},
	"more":         {"（还有 %d 字，回复「更多」继续看）", "(%d more characters; reply more)"},
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
		"（之前等确认的「%s」作废了）", "(the earlier \"%s\" waiting for ok is dropped)",
	},
	"nothingPending": {"没有等待确认的操作。", "Nothing is waiting for confirmation."},
	"cancelled":      {"已取消。", "Cancelled."},
	"expired":        {"那个操作已经过期了，重新发一次。", "That one has expired; send it again."},
	"missed": {
		"你不在的时候，这些在等你（刚才那条没有执行，看完再发一次）：",
		"While you were away these started waiting (your last message was not run; send it again after reading):",
	},
	"usage": {
		"今天：%d 个会话变过状态，%d 次在等你。\n助手：%d 次调用，$%.2f。",
		"Today: %d sessions changed state, %d waited on you.\nAssistant: %d calls, $%.2f.",
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
		"回复「%[1]d: 你的话」把话送进 [%[1]d]。它要你允许时回「%[1]d: 好」或「%[1]d: 不」。\n" +
			"列表 · 屏幕 %[1]d · 截图 %[1]d · 打开 %[1]d\n" +
			"上下文 %[1]d 看最近对话 · 切到 %[1]d 之后默认发给它 · 静音 %[1]d 2小时\n" +
			"停 %[1]d 打断 · 用量 · 更多 看剩下的",
		"Reply \"%[1]d: your words\" to send them to [%[1]d]. When it asks for permission, %[1]d: yes or %[1]d: no.\n" +
			"list · screen %[1]d (its pane as text) · shot %[1]d (a picture) · open %[1]d (a link)\n" +
			"context %[1]d recent messages · focus %[1]d make it the default · mute %[1]d 2h\n" +
			"stop %[1]d interrupt · usage · more",
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
	"linkLabel": {"在面板打开", "open"},
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
