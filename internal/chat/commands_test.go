package chat

import "testing"

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
		// Sentences go to the agent.
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
