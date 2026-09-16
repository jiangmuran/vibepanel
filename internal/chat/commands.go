package chat

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a person can ask for besides talking to a session.
//
// Bilingual on purpose, and the words are the ones people type on a phone
// rather than a grammar: "list", "列表", "ls" are one command. A word that is
// not a command and not an answer goes to a session, so this parser has to be
// conservative -- "stop" is a command only when it is the whole message or
// followed by a handle, because "stop using tabs" is an instruction.
//
// It is also lenient about the shape of a message in the ways a phone makes
// messages: a voice note transcribed as "嗯，那个，可以的。", a slash typed out
// of Telegram habit, full-width digits, a handle written "2号" or "第二个".
// Those are normalised before anything is looked up; what is delivered to a
// session is always the text as typed.

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
	// VerbAll is "allow everything", which the bridge refuses by name: one
	// request at a time is the whole point.
	VerbAll Verb = "all"
	// VerbUnknown is a slash command nobody defined. It is refused rather
	// than delivered: "/deploy" typed into a session is not what anybody
	// who typed a slash meant.
	VerbUnknown Verb = "unknown"
)

// Command is a parsed message.
type Command struct {
	Verb Verb
	// Handle is the session named, 0 for none.
	Handle int
	// Arg is what followed: a line count, a duration, the question.
	Arg string
}

var verbs = map[string]Verb{
	"help": VerbHelp, "帮助": VerbHelp, "?": VerbHelp, "？": VerbHelp, "start": VerbHelp, "怎么用": VerbHelp,
	"list": VerbList, "ls": VerbList, "列表": VerbList, "看看": VerbList, "列出会话": VerbList, "会话": VerbList,
	"screen": VerbScreen, "屏幕": VerbScreen, "看屏": VerbScreen, "在干嘛": VerbScreen, "在干什么": VerbScreen, "在干啥": VerbScreen,
	"看一下": VerbScreen, "看看屏幕": VerbScreen,
	"shot": VerbShot, "截图": VerbShot,
	"open": VerbOpen, "打开": VerbOpen, "链接": VerbOpen,
	"mute": VerbMute, "静音": VerbMute,
	"unmute": VerbUnmute, "取消静音": VerbUnmute,
	"focus": VerbFocus, "切到": VerbFocus, "切换": VerbFocus,
	"context": VerbContext, "ctx": VerbContext, "上下文": VerbContext, "最近对话": VerbContext,
	"usage": VerbUsage, "用量": VerbUsage,
	"more": VerbMore, "更多": VerbMore, "继续看": VerbMore,
	"ok": VerbConfirm, "确认": VerbConfirm, "confirm": VerbConfirm,
	"cancel": VerbCancel, "取消": VerbCancel,
	"stop": VerbStop, "中断": VerbStop, "esc": VerbStop, "停": VerbStop, "停一下": VerbStop,
	"打断": VerbStop, "停止": VerbStop,
	"ask": VerbAsk, "问": VerbAsk,
}

// Words that answer a prompt. Kept short: a longer sentence that happens to
// start with "yes" is an instruction and goes to the agent as text.
var answers = map[string]Verb{
	"y": VerbApprove, "yes": VerbApprove, "允许": VerbApprove, "好": VerbApprove, "好的": VerbApprove,
	"可以": VerbApprove, "可以的": VerbApprove, "同意": VerbApprove, "行": VerbApprove, "批准": VerbApprove,
	"approve": VerbApprove, "allow": VerbApprove,
	"n": VerbDeny, "no": VerbDeny, "拒绝": VerbDeny, "不": VerbDeny, "不行": VerbDeny,
	"deny": VerbDeny, "不要": VerbDeny, "不可以": VerbDeny, "别": VerbDeny,
}

// Confirmation words, when something is waiting for "ok". Looked at before
// the answers above, so "好的" to "send this?" confirms rather than approving
// whatever a session is asking.
var confirms = map[string]bool{
	"ok": true, "确认": true, "confirm": true, "好": true, "好的": true, "可以": true, "可以的": true,
	"是": true, "是的": true, "行": true, "对": true, "yes": true, "y": true,
}

var cancels = map[string]bool{
	"cancel": true, "取消": true, "不": true, "不要": true, "算了": true, "no": true, "n": true, "别": true,
}

var allWords = map[string]bool{"全部允许": true, "都允许": true, "全部同意": true, "都同意": true, "全部": true}

// fillers are the words a voice transcription puts around what was meant.
var fillers = []string{"嗯", "呃", "额", "啊", "那个", "就是", "然后", "那就", "麻烦"}

// Normalize reduces a message to the form commands are looked up in: full
// width digits and letters narrowed, a leading slash dropped, filler words
// and trailing punctuation removed, a word said twice said once, lower case.
func Normalize(text string) string {
	s := strings.TrimSpace(narrow(text))
	s = strings.TrimPrefix(s, "/")
	trim := func(s string) string {
		return strings.TrimFunc(s, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("。.！!～~，,、；;…", r)
		})
	}
	s = trim(s)
	for changed := true; changed; {
		changed = false
		for _, f := range fillers {
			if strings.HasPrefix(s, f) && len(s) > len(f) {
				s = trim(strings.TrimPrefix(s, f))
				changed = true
			}
		}
		// Not 嘛: "在干嘛" is a word, and trimming it left "在干".
		for _, end := range []string{"吧", "呀", "啊", "哈", "哦"} {
			if strings.HasSuffix(s, end) && utf8.RuneCountInString(s) > 1 {
				s = trim(strings.TrimSuffix(s, end))
				changed = true
			}
		}
	}
	// "好的好的", "可以可以": a word said twice is the word.
	if n := utf8.RuneCountInString(s); n >= 2 && n%2 == 0 {
		rs := []rune(s)
		if string(rs[:n/2]) == string(rs[n/2:]) {
			s = string(rs[:n/2])
		}
	}
	return strings.ToLower(s)
}

// narrow turns full-width ASCII (ｙ, ３, ：) into the narrow form a phone's
// Chinese keyboard did not type.
func narrow(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0xFF01 && r <= 0xFF5E {
			return r - 0xFEE0
		}
		if r == 0x3000 {
			return ' '
		}
		return r
	}, s)
}

// looseHandle finds a handle written the way a person says it, at the start
// or the end of a message: "2号", "第二个", "第2个", "3号可以", "停一下3号".
var looseHandle = regexp.MustCompile(`^(?:第\s*([0-9一二三四五六七八九十]{1,3})\s*个|([0-9]{1,4})\s*号)|(?:第\s*([0-9一二三四五六七八九十]{1,3})\s*个|([0-9]{1,4})\s*号)$`)

var chineseDigits = map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10}

func parseNumber(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	rs := []rune(s)
	switch len(rs) {
	case 1:
		return chineseDigits[rs[0]]
	case 2:
		if rs[0] == '十' {
			return 10 + chineseDigits[rs[1]]
		}
		if rs[1] == '十' {
			return chineseDigits[rs[0]] * 10
		}
	case 3:
		if rs[1] == '十' {
			return chineseDigits[rs[0]]*10 + chineseDigits[rs[2]]
		}
	}
	return 0
}

// SplitLooseHandle takes a spoken handle off a message: "2号可以" is handle 2
// and "可以". Whether the number is a real session is the caller's question.
func SplitLooseHandle(text string) (int, string, bool) {
	s := strings.TrimSpace(narrow(text))
	m := looseHandle.FindStringSubmatchIndex(s)
	if m == nil {
		return 0, text, false
	}
	for _, g := range []int{2, 4, 6, 8} {
		if m[g] >= 0 {
			n := parseNumber(s[m[g]:m[g+1]])
			if n <= 0 {
				return 0, text, false
			}
			rest := strings.TrimSpace(s[:m[0]] + s[m[1]:])
			rest = strings.TrimLeft(rest, ":：,，、 ")
			return n, rest, true
		}
	}
	return 0, text, false
}

// Parse reads a message. ok is false when the message is not a command and
// should go to a session as text.
func Parse(text string) (Command, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return Command{}, false
	}
	slash := strings.HasPrefix(narrow(raw), "/")
	// "问：..." is one token in Chinese typing; the question keeps its own
	// punctuation, so it is cut from the raw text.
	for _, p := range []string{"问：", "问:", "ask:", "ask："} {
		if strings.HasPrefix(strings.ToLower(raw), p) {
			q := strings.TrimSpace(raw[len(p):])
			return Command{Verb: VerbAsk, Arg: q}, q != ""
		}
	}
	s := Normalize(raw)
	if s == "" {
		return Command{}, false
	}
	if a, ok := answers[s]; ok {
		return Command{Verb: a}, true
	}
	if allWords[s] {
		return Command{Verb: VerbAll}, true
	}

	// A spoken handle around a verb: "1号在干嘛", "停一下3号", "看一下第二个".
	if n, rest, ok := SplitLooseHandle(s); ok {
		rest = Normalize(rest)
		if v, isVerb := verbs[rest]; isVerb {
			return Command{Verb: v, Handle: n}, true
		}
		if a, isAnswer := answers[rest]; isAnswer {
			return Command{Verb: a, Handle: n}, true
		}
		if allWords[rest] {
			return Command{Verb: VerbAll}, true
		}
	}

	fields := strings.Fields(s)
	head := fields[0]
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
		if slash {
			return Command{Verb: VerbUnknown, Arg: head}, true
		}
		return Command{}, false
	}
	c := Command{Verb: v, Handle: glued}
	rest := fields[1:]
	switch v {
	case VerbAsk:
		// "ask what is 3 doing": the question is the raw text after the
		// word, punctuation and all.
		q := strings.TrimSpace(raw[len(strings.Fields(raw)[0]):])
		return Command{Verb: VerbAsk, Arg: q}, q != ""
	case VerbHelp, VerbList, VerbUsage, VerbConfirm, VerbCancel:
		// Whole-message commands: anything after them means it was a
		// sentence, and a sentence goes to the agent.
		return c, len(rest) == 0
	}
	// Verbs that take a handle: "screen 3", "屏幕3", "mute 3 2h", "more 5".
	if len(rest) > 0 && c.Handle == 0 {
		h := strings.TrimPrefix(strings.TrimSuffix(rest[0], "号"), "#")
		if n, err := strconv.Atoi(h); err == nil && n > 0 {
			c.Handle = n
			rest = rest[1:]
		} else if n, _, ok := SplitLooseHandle(rest[0]); ok {
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
	// So is "more" or "看一下" with words after it that are not a handle.
	if (v == VerbMore || v == VerbScreen || v == VerbList) && c.Arg != "" {
		return Command{}, false
	}
	return c, true
}

// IsConfirm and IsCancel read a message as an answer to "reply ok".
func IsConfirm(text string) bool { return confirms[Normalize(text)] }
func IsCancel(text string) bool  { return cancels[Normalize(text)] }

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
