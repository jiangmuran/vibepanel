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
	"paired":     {"已配对。回复 help 看能做什么。", "Paired. Reply help for what you can do."},
	"receipt":    {"→ [%d] 已送入", "→ [%d] sent"},
	"receiptKey": {"→ [%d] %s", "→ [%d] %s"},
	"approved":   {"已允许", "allowed"},
	"denied":     {"已拒绝", "denied"},
	"interrupted": {
		"已发送中断", "interrupt sent",
	},
	"notWaiting": {
		"[%d] 现在没有在等你。",
		"[%d] is not waiting on anything.",
	},
	"working": {
		"[%d] 正在工作。等它停下，或者回复 stop %d 打断。",
		"[%d] is working. Wait for it to stop, or reply stop %d to interrupt.",
	},
	"gone": {"[%d] 已经不在了。", "[%d] is gone."},
	"atPrompt": {
		"[%d] 在等你允许或拒绝，先回 y 或 n。", "[%d] is at a permission prompt; answer y or n first.",
	},
	"imageFailed": {"图片没收到。", "The picture did not arrive."},
	"noShell":     {"[%d] 是一个 shell，没有提示可以回答。", "[%d] is a shell; there is no prompt to answer."},
	"noProfile": {
		"[%d] 跑的是 %s，面板不知道怎么替它按键。在「聊天」页的按键表里加上。",
		"[%d] runs %s and the panel has no keys for it. Add them on the Chat page.",
	},
	"several": {
		"有几个会话都在等你，说清楚是哪个：回复「3: 你的话」，或者引用它的消息。\n%s",
		"Several sessions are waiting. Say which: reply \"3: your words\" or quote its message.\n%s",
	},
	"none": {
		"没有会话在等你，也没有正在对话的会话。回复「3: 你的话」指定一个，或者 list 看看有哪些。",
		"No session is waiting and none is in focus. Reply \"3: your words\" to pick one, or list.",
	},
	"unknownHandle": {"没有 [%d] 这个会话。回复 list 看看有哪些。", "There is no [%d]. Reply list to see them."},
	"needHandle":    {"要哪一个？加上编号，比如 %s 3。", "Which one? Add the number, e.g. %s 3."},
	"listEmpty":     {"现在没有会话。", "No sessions right now."},
	"listHead":      {"会话（回复「编号: 你的话」即可对话）", "Sessions (reply \"number: your words\" to talk to one)"},
	"screenHead":    {"[%d] 屏幕", "[%d] screen"},
	"shotUnavailable": {
		"这个面板不能截图。", "This panel cannot take screenshots.",
	},
	"muted":   {"[%d] 已静音到 %s。回复 unmute %d 恢复。", "[%d] muted until %s. Reply unmute %d to undo."},
	"unmuted": {"[%d] 已恢复推送。", "[%d] unmuted."},
	"focused": {"之后没写编号的话都发给 [%d]。", "Bare replies now go to [%d]."},
	"open":    {"[%d] %s", "[%d] %s"},
	"contextHead": {
		"[%d] 最近 %d 条", "[%d] last %d",
	},
	"contextEmpty": {"[%d] 还没有记录到任何消息。", "[%d] has no messages recorded yet."},
	"more":         {"（还有 %d 字，回复 more 继续看）", "(%d more characters; reply more)"},
	"noMore":       {"没有更多了。", "Nothing more."},
	"confirmStop": {
		"要打断 [%d] 吗？回复 ok 确认，cancel 取消。", "Interrupt [%d]? Reply ok to confirm, cancel to drop it.",
	},
	"confirmSend": {
		"要给 [%d] 发送：\n%s\n回复 ok 确认，cancel 取消。", "Send to [%d]:\n%s\nReply ok to confirm, cancel to drop it.",
	},
	"nothingPending": {"没有等待确认的操作。", "Nothing is waiting for confirmation."},
	"cancelled":      {"已取消。", "Cancelled."},
	"expired":        {"那个操作已经过期了，重新发一次。", "That one has expired; send it again."},
	"usage": {
		"今天：%d 个会话变过状态，%d 次在等你。\n助手：%d 次调用，$%.2f。",
		"Today: %d sessions changed state, %d waited on you.\nAssistant: %d calls, $%.2f.",
	},
	"noAssistant": {
		"这个聊天没有开高级模式，直接用「3: 你的话」或 help 里的命令。",
		"Advanced mode is off for this chat. Use \"3: your words\" or the commands in help.",
	},
	"assistantBudget": {
		"助手今天的预算用完了，明天再试，或者直接用命令。",
		"The assistant's budget for today is spent. Try tomorrow, or use the commands.",
	},
	"assistantFailed": {"助手没答上来：%s", "The assistant could not answer: %s"},
	"confirmAnswer": {
		"要给 [%d] 回「%s」吗？回复 ok 确认，cancel 取消。", "Answer [%d] with \"%s\"? Reply ok to confirm, cancel to drop it.",
	},
	"help": {
		"回复「3: 你的话」把话送进 [3]。在等你时回 y / n（允许 / 拒绝）。\n" +
			"list 列表 · screen 3 看屏幕 · shot 3 截图 · open 3 链接\n" +
			"context 3 看最近对话 · focus 3 之后默认发给它 · mute 3 2h 静音\n" +
			"stop 3 打断 · usage 用量 · more 看剩下的\n" +
			"高级模式下直接说话，或「问：…」让助手去看。",
		"Reply \"3: your words\" to send them to [3]. While it waits, y / n allow or deny.\n" +
			"list · screen 3 (its pane as text) · shot 3 (a picture) · open 3 (a link)\n" +
			"context 3 recent messages · focus 3 make it the default · mute 3 2h\n" +
			"stop 3 interrupt · usage · more\n" +
			"In advanced mode just talk, or \"ask: …\" to have the assistant look.",
	},
	"stateWaiting": {"在等你", "waiting for you"},
	"stateWorking": {"在工作", "working"},
	"stateDone":    {"已停下", "done"},
	"kindPrompt":   {"要你允许", "needs your permission"},
	"kindQuestion": {"在问你", "is asking you"},
	"imageSaved": {
		"图片已放到 [%d] 的目录，路径已经填在它的输入框里。", "Picture saved next to [%d]; its path is typed at the prompt.",
	},
	"imageNoTarget": {
		"图片要给哪个会话？引用它的消息再发一次，或者先 focus 3。",
		"Which session is the picture for? Quote its message, or focus 3 first.",
	},
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
