package chat

import (
	"testing"
	"time"
)

func TestParseCommands(t *testing.T) {
	cases := []struct {
		in   string
		want Command
		ok   bool
	}{
		{"help", Command{Verb: VerbHelp}, true},
		{"帮助", Command{Verb: VerbHelp}, true},
		{"list", Command{Verb: VerbList}, true},
		{"列表", Command{Verb: VerbList}, true},
		{"screen 3", Command{Verb: VerbScreen, Handle: 3}, true},
		{"屏幕3", Command{Verb: VerbScreen, Handle: 3}, true},
		{"截图 #4", Command{Verb: VerbShot, Handle: 4}, true},
		{"mute 3 2h", Command{Verb: VerbMute, Handle: 3, Arg: "2h"}, true},
		{"静音 3", Command{Verb: VerbMute, Handle: 3}, true},
		{"context 3 20", Command{Verb: VerbContext, Handle: 3, Arg: "20"}, true},
		{"上下文", Command{Verb: VerbContext}, true},
		{"stop 3", Command{Verb: VerbStop, Handle: 3}, true},
		{"stop", Command{Verb: VerbStop}, true},
		{"ok", Command{Verb: VerbConfirm}, true},
		{"确认", Command{Verb: VerbConfirm}, true},
		{"y", Command{Verb: VerbApprove}, true},
		{"允许", Command{Verb: VerbApprove}, true},
		{"n", Command{Verb: VerbDeny}, true},
		{"拒绝", Command{Verb: VerbDeny}, true},
		{"问：3 号在干嘛", Command{Verb: VerbAsk, Arg: "3 号在干嘛"}, true},
		{"ask what is 3 doing", Command{Verb: VerbAsk, Arg: "what is 3 doing"}, true},
		{"usage", Command{Verb: VerbUsage}, true},
		{"more", Command{Verb: VerbMore}, true},
		// The way a phone and a voice note write them.
		{"嗯，好的。", Command{Verb: VerbApprove}, true},
		{"好的好的", Command{Verb: VerbApprove}, true},
		{"可以吧", Command{Verb: VerbApprove}, true},
		{"ｙ", Command{Verb: VerbApprove}, true},
		{"/start", Command{Verb: VerbHelp}, true},
		{"/list", Command{Verb: VerbList}, true},
		{"/deploy now", Command{Verb: VerbUnknown, Arg: "deploy"}, true},
		{"2号可以", Command{Verb: VerbApprove, Handle: 2}, true},
		{"第二个在干嘛", Command{Verb: VerbScreen, Handle: 2}, true},
		{"停一下3号", Command{Verb: VerbStop, Handle: 3}, true},
		{"3号 看一下", Command{Verb: VerbScreen, Handle: 3}, true},
		{"看一下 第十二个", Command{Verb: VerbScreen, Handle: 12}, true},
		{"更多 5", Command{Verb: VerbMore, Handle: 5}, true},
		{"全部允许", Command{Verb: VerbAll}, true},
		{"3 在干嘛", Command{Verb: VerbScreen, Handle: 3}, true},
		{"3号咋样了", Command{Verb: VerbScreen, Handle: 3}, true},
		{"看看3", Command{Verb: VerbScreen, Handle: 3}, true},
		{"4 停", Command{Verb: VerbStop, Handle: 4}, true},
		{"不切了", Command{Verb: VerbUnfocus}, true},
		{"取消切到", Command{Verb: VerbUnfocus}, true},
		{"ok了", Command{Verb: VerbConfirm}, true},
		{"3 continue", Command{}, false},
		{"系统", Command{Verb: VerbSystem}, true},
		{"监控。", Command{Verb: VerbSystem}, true},
		{"system", Command{Verb: VerbSystem}, true},
		{"系统有点卡", Command{}, false},
		{"静音告警 1小时", Command{Verb: VerbMuteAlerts, Arg: "1小时"}, true},
		{"静音告警2h", Command{Verb: VerbMuteAlerts, Arg: "2h"}, true},
		{"mute alerts 30m", Command{Verb: VerbMuteAlerts, Arg: "30m"}, true},
		{"静音告警", Command{Verb: VerbMuteAlerts}, true},
		{"恢复告警", Command{Verb: VerbUnmuteAlerts}, true},
		{"unmute alerts", Command{Verb: VerbUnmuteAlerts}, true},
		{"那个，2号先静音半小时吧。", Command{Verb: VerbMute, Handle: 2, Arg: "半小时"}, true},
		{"静音３号半小时", Command{Verb: VerbMute, Handle: 3, Arg: "半小时"}, true},
		{"静音3号 半小时", Command{Verb: VerbMute, Handle: 3, Arg: "半小时"}, true},
		{"取消静音3", Command{Verb: VerbUnmute, Handle: 3}, true},
		{"屏幕3号", Command{Verb: VerbScreen, Handle: 3}, true},
		{"静音一下那个测试的", Command{Verb: VerbUnclear, Arg: "静音\x00静音 3 半小时"}, true},
		{"没问题", Command{Verb: VerbApprove}, true},
		{"打开3号文件看看", Command{}, false},
		{"停车场的代码改一下", Command{}, false},
		{"都允许吧", Command{Verb: VerbAll}, true},
		// Sentences go to the agent.
		{"更多的测试", Command{}, false},
		{"看一下 README 再改", Command{}, false},
		{"2号机器上的日志", Command{}, false},
		{"stop using tabs", Command{}, false},
		{"yes please add tests", Command{}, false},
		{"list the files", Command{}, false},
		{"help me with this", Command{}, false},
		{"3: continue", Command{}, false},
		{"continue", Command{}, false},
		{"问：", Command{}, false},
		{"", Command{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Parse(%q) = %+v %v, want %+v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestLoosenReadsSpokenHandlesOnlyForRealSessions(t *testing.T) {
	cands := []Candidate{{SessionID: "a", Handle: 3}, {SessionID: "b", Handle: 12}}
	for in, want := range map[string]string{
		"3号，嗯，选A吧，小改就行。":  "3: 选A吧，小改就行。",
		"【3】拒绝":           "【3】拒绝",
		"3 继续":            "3: 继续",
		"3号 继续":           "3: 继续",
		"第十二个：加个测试":       "12: 加个测试",
		"3: already":      "3: already",
		"2 files is fine": "2 files is fine", // no [2]
		"3 4 5":           "3 4 5",
		"continue":        "continue",
	} {
		if got := loosen(in, cands); got != want {
			t.Errorf("loosen(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDurationReadsHowPeopleSayIt(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"": 2 * time.Hour, "30m": 30 * time.Minute, "半小时": 30 * time.Minute, "两个小时": 2 * time.Hour,
		"3天": 72 * time.Hour, "15分钟": 15 * time.Minute, "１ｈ": time.Hour, "45": 45 * time.Minute,
	} {
		got, ok := parseDuration(in, 2*time.Hour)
		if !ok || got != want {
			t.Errorf("parseDuration(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	if got, ok := parseDuration("until lunch", 2*time.Hour); ok || got != 2*time.Hour {
		t.Errorf("an unreadable duration: %v %v", got, ok)
	}
}

func TestPairingCodesAreReadAsTyped(t *testing.T) {
	for in, want := range map[string]string{"123456": "123456", "１２３ ４５６": "123456", "123-456": "123456", "12345a": "", "hello": ""} {
		if got := pairingDigits(in); got != want {
			t.Errorf("pairingDigits(%q) = %q, want %q", in, got, want)
		}
	}
	if isPairingCode("my code is 123456") || !isPairingCode(" 123 456 ") {
		t.Error("isPairingCode")
	}
}

func TestStripMarkdownLeavesTheWords(t *testing.T) {
	in := "## Status\n**[3]** is `waiting` on __tests__, see [the panel](https://x.test/a)\n```\nmake\n```"
	want := "Status\n[3] is waiting on tests, see the panel https://x.test/a\nmake"
	if got := stripMarkdown(in); got != want {
		t.Errorf("stripMarkdown = %q, want %q", got, want)
	}
}
