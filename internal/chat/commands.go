package chat

import (
	"strconv"
	"strings"
)

// What a person can ask for besides talking to a session.
//
// Bilingual on purpose, and the words are the ones people type on a phone
// rather than a grammar: "list", "列表", "ls" are one command. A word that is
// not a command and not an answer goes to a session, so this parser has to be
// conservative -- "stop" is a command only when it is the whole message or
// followed by a handle, because "stop using tabs" is an instruction.

// Verb is one command.
type Verb string

const (
	VerbHelp    Verb = "help"
	VerbList    Verb = "list"
	VerbScreen  Verb = "screen"
	VerbShot    Verb = "shot"
	VerbOpen    Verb = "open"
	VerbMute    Verb = "mute"
	VerbUnmute  Verb = "unmute"
	VerbFocus   Verb = "focus"
	VerbContext Verb = "context"
	VerbUsage   Verb = "usage"
	VerbMore    Verb = "more"
	VerbConfirm Verb = "confirm"
	VerbCancel  Verb = "cancel"
	VerbStop    Verb = "stop"
	// VerbAsk is the advanced mode's explicit "ask the assistant"; the text
	// after it is the question.
	VerbAsk Verb = "ask"
	// VerbApprove and VerbDeny are answers to a prompt, not commands, but
	// they are parsed here because they are single words too.
	VerbApprove Verb = "approve"
	VerbDeny    Verb = "deny"
)

// Command is a parsed message.
type Command struct {
	Verb Verb
	// Handle is the session named after the verb, 0 for none.
	Handle int
	// Arg is what followed: a line count, a duration, the question.
	Arg string
}

var verbs = map[string]Verb{
	"help": VerbHelp, "帮助": VerbHelp, "?": VerbHelp, "？": VerbHelp,
	"list": VerbList, "ls": VerbList, "列表": VerbList, "看看": VerbList,
	"screen": VerbScreen, "屏幕": VerbScreen, "看屏": VerbScreen,
	"shot": VerbShot, "截图": VerbShot,
	"open": VerbOpen, "打开": VerbOpen, "链接": VerbOpen,
	"mute": VerbMute, "静音": VerbMute,
	"unmute": VerbUnmute, "取消静音": VerbUnmute,
	"focus": VerbFocus, "切到": VerbFocus, "切换": VerbFocus,
	"context": VerbContext, "ctx": VerbContext, "上下文": VerbContext,
	"usage": VerbUsage, "用量": VerbUsage,
	"more": VerbMore, "更多": VerbMore, "继续看": VerbMore,
	"ok": VerbConfirm, "确认": VerbConfirm, "confirm": VerbConfirm,
	"cancel": VerbCancel, "取消": VerbCancel,
	"stop": VerbStop, "中断": VerbStop, "esc": VerbStop,
	"ask": VerbAsk, "问": VerbAsk, "问：": VerbAsk, "问:": VerbAsk,
}

// Words that answer a prompt. Kept short: a longer sentence that happens to
// start with "yes" is an instruction and goes to the agent as text.
var answers = map[string]Verb{
	"y": VerbApprove, "yes": VerbApprove, "允许": VerbApprove, "好": VerbApprove, "好的": VerbApprove,
	"可以": VerbApprove, "同意": VerbApprove, "approve": VerbApprove, "allow": VerbApprove,
	"n": VerbDeny, "no": VerbDeny, "拒绝": VerbDeny, "不": VerbDeny, "不行": VerbDeny,
	"deny": VerbDeny, "不要": VerbDeny,
}

// Parse reads a message. ok is false when the message is not a command and
// should go to a session as text.
func Parse(text string) (Command, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return Command{}, false
	}
	// "问：..." is one token in Chinese typing; split it so the verb table
	// sees "问".
	for _, p := range []string{"问：", "问:"} {
		if strings.HasPrefix(s, p) {
			s = "问 " + strings.TrimSpace(s[len(p):])
			break
		}
	}
	fields := strings.Fields(s)
	head := strings.ToLower(fields[0])
	if v, ok := answers[head]; ok && len(fields) == 1 {
		return Command{Verb: v}, true
	}
	v, ok := verbs[head]
	// "屏幕3" with no space, which is how a phone keyboard leaves it.
	glued := 0
	if !ok {
		if n, tail, found := trailingNumber(head); found {
			if tv, isVerb := verbs[tail]; isVerb {
				v, ok, glued = tv, true, n
			}
		}
	}
	if !ok {
		return Command{}, false
	}
	c := Command{Verb: v, Handle: glued}
	rest := fields[1:]
	switch v {
	case VerbAsk:
		c.Arg = strings.TrimSpace(strings.TrimPrefix(s, fields[0]))
		return c, c.Arg != ""
	case VerbHelp, VerbList, VerbUsage, VerbMore, VerbConfirm, VerbCancel:
		// Whole-message commands: anything after them means it was a
		// sentence, and a sentence goes to the agent.
		return c, len(rest) == 0
	}
	// Verbs that take a handle: "screen 3", "屏幕3", "mute 3 2h".
	if len(rest) > 0 && c.Handle == 0 {
		if n, err := strconv.Atoi(strings.TrimPrefix(rest[0], "#")); err == nil && n > 0 {
			c.Handle = n
			rest = rest[1:]
		}
	}
	c.Arg = strings.Join(rest, " ")
	// A stop with a sentence after it is an instruction about stopping, not
	// the command; the command is "stop" or "stop 3".
	if v == VerbStop && c.Arg != "" {
		return Command{}, false
	}
	return c, true
}

func trailingNumber(word string) (int, string, bool) {
	i := len(word)
	for i > 0 && word[i-1] >= '0' && word[i-1] <= '9' {
		i--
	}
	if i == len(word) || i == 0 {
		return 0, word, false
	}
	n, err := strconv.Atoi(word[i:])
	if err != nil || n <= 0 {
		return 0, word, false
	}
	return n, word[:i], true
}
