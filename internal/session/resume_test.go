package session

import (
	"reflect"
	"testing"
)

func TestResumeArgvKnowsTheTwoAgentsItHasBeenMeasuredAgainst(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch []string
		want   []string
	}{
		{"claude", []string{"claude"}, []string{"claude", "--continue"}},
		{
			"claude keeps the arguments it was given",
			[]string{"claude", "--model", "opus"},
			[]string{"claude", "--continue", "--model", "opus"},
		},
		{
			"claude by absolute path is still claude",
			[]string{"/home/x/.local/bin/claude"},
			[]string{"/home/x/.local/bin/claude", "--continue"},
		},
		// A subcommand, so it goes first. `codex --model x resume --last`
		// is not a command line codex accepts.
		{"codex", []string{"codex"}, []string{"codex", "resume", "--last"}},
		{
			"codex keeps its arguments, after the subcommand",
			[]string{"codex", "--model", "gpt"},
			[]string{"codex", "resume", "--last", "--model", "gpt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResumeArgv(tc.launch)
			if !ok {
				t.Fatalf("ResumeArgv(%v) declined; this is an agent that resumes", tc.launch)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResumeArgv(%v) = %v, want %v", tc.launch, got, tc.want)
			}
		})
	}
}

// The input argv must come back untouched.
//
// concat exists for this: appending onto launch[:1] shares the caller's backing
// array, so building the resumed argv would overwrite the caller's launch[1] --
// and the caller here is a restore that goes on to show the user what it ran.
func TestResumeArgvDoesNotEditWhatItWasGiven(t *testing.T) {
	launch := []string{"claude", "--model", "opus"}
	if _, ok := ResumeArgv(launch); !ok {
		t.Fatal("ResumeArgv declined claude")
	}
	if !reflect.DeepEqual(launch, []string{"claude", "--model", "opus"}) {
		t.Errorf("the caller's argv was rewritten in place: %v", launch)
	}
}

func TestResumeArgvDeclinesWhatItCannotBringBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch []string
	}{
		// A row from before the panel recorded argvs. It comes back as a login
		// shell, and there is nothing to resume about a login shell.
		{"nothing recorded", nil},
		{"a login shell", []string{"bash", "-l"}},
		{"an ordinary command", []string{"npm", "run", "dev"}},
		// Launched asking for it already. Two --continue flags is at best
		// noise on the command line the dialog prints.
		{"claude already continuing", []string{"claude", "--continue"}},
		{"claude already continuing, short", []string{"claude", "-c"}},
		{"claude resuming a named session", []string{"claude", "--resume", "abc"}},
		{"codex already resuming", []string{"codex", "resume", "--last"}},
		// Not measured against, so not guessed at. The cost of being wrong is
		// a pane that dies at startup.
		{"opencode", []string{"opencode"}},
		// The program is what decides, not a substring of an argument:
		// `--model claude-opus-5` is not claude.
		{"an argument that merely mentions an agent", []string{"env", "X=claude", "sh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := ResumeArgv(tc.launch); ok {
				t.Errorf("ResumeArgv(%v) = %v, want a refusal", tc.launch, got)
			}
			if ResumesConversation(tc.launch) {
				t.Errorf("ResumesConversation(%v) said yes", tc.launch)
			}
			if p := ResumeProgram(tc.launch); p != "" {
				t.Errorf("ResumeProgram(%v) = %q, want empty", tc.launch, p)
			}
		})
	}
}

// Two agents restored into one directory would resume the same conversation.
//
// Both flags mean "the most recent conversation here", so this is not two
// conversations coming back; it is one conversation coming back twice, with
// nothing on either screen to say so.
func TestTwoSessionsInOneDirectoryCannotBothResume(t *testing.T) {
	rows := []ResumeCandidate{
		{Launch: []string{"claude"}, CWD: "/w/api"},
		{Launch: []string{"claude"}, CWD: "/w/api"},
		{Launch: []string{"claude"}, CWD: "/w/web"},
		{Launch: []string{"codex"}, CWD: "/w/api"},
		{Launch: []string{"bash", "-l"}, CWD: "/w/api"},
	}

	if !ResumeIsAmbiguous(rows, "claude", "/w/api") {
		t.Error("two claude sessions in /w/api were not reported as ambiguous")
	}
	// One of a kind in its directory: nothing to be confused with.
	if ResumeIsAmbiguous(rows, "claude", "/w/web") {
		t.Error("the only claude session in /w/web was reported as ambiguous")
	}
	// A different agent in the same directory is a different transcript.
	if ResumeIsAmbiguous(rows, "codex", "/w/api") {
		t.Error("the only codex session in /w/api was reported as ambiguous")
	}
	// A shell in the directory is not a claimant on anybody's conversation.
	if ResumeIsAmbiguous(rows, "bash", "/w/api") {
		t.Error("a login shell was treated as something that resumes")
	}
}

// A row with no directory recorded cannot be reasoned about, and answering
// "ambiguous" for it would turn every such row into a cold start on the
// strength of a missing field.
func TestAmbiguityNeedsBothHalvesOfTheKey(t *testing.T) {
	rows := []ResumeCandidate{
		{Launch: []string{"claude"}, CWD: ""},
		{Launch: []string{"claude"}, CWD: ""},
	}
	if ResumeIsAmbiguous(rows, "claude", "") {
		t.Error("an empty directory matched itself")
	}
	if ResumeIsAmbiguous(rows, "", "/w/api") {
		t.Error("an empty program matched itself")
	}
}
