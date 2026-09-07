package session

// Bringing an agent's conversation back, not just its command line.
//
// A restore re-runs the recorded argv, and for a long time that was the whole
// story: the process was gone, so the agent was gone, and the dialog said so
// in as many words. That was true of the process and it stopped being true of
// the conversation. Both agents the panel knows how to launch keep their
// transcript on disk, keyed by the directory it was held in, and both have a
// flag that picks the most recent one back up:
//
//	claude --continue      "Continue the most recent conversation in the
//	                        current directory"
//	codex resume --last    "continue the most recent"; the picker it replaces
//	                        filters by cwd unless --all is passed
//
// A restore already puts the session back in the directory it was in -- see
// restoreDir, which refuses rather than falling back to $HOME -- so "the most
// recent conversation here" is the conversation that was running when the
// machine went down. Measured rather than read: a conversation told to
// remember 4712, a fresh process started with --continue in the same
// directory, and the number came back.
//
// What is *not* claimed: the process is still new, its subprocesses are gone,
// anything it had unsaved is gone, and a --print run halfway through an edit
// resumes with the edit unmade. This brings back what the provider kept, which
// is the conversation.
//
// opencode is deliberately absent. The panel launches it and reports its
// state, but nothing here has been verified against it, and the cost of
// guessing is a pane that dies at startup on a machine the guess was wrong
// about. store.builtinProfiles refuses to guess opencode's environment
// variables for the same reason.

// ResumeArgv turns the argv a session was launched with into the one that
// brings its conversation back.
//
// Reports false when there is nothing better to run than what is already
// there, which is every case except the two agents below: an unrecorded argv,
// a login shell, a build, a tail, an agent this does not know.
//
// The recorded arguments are carried through unchanged rather than dropped.
// Somebody who launched `claude --model x` wants that model on the way back,
// and a resume that quietly rewrote their command line into a shorter one
// would be the panel deciding what they meant.
func ResumeArgv(launch []string) ([]string, bool) {
	if len(launch) == 0 {
		return nil, false
	}
	rest := launch[1:]
	switch baseName(launch[0]) {
	case "claude":
		// Already asked for by hand. Somebody whose profile is
		// `claude --continue` gets one --continue, not two.
		if hasAny(rest, "-c", "--continue", "-r", "--resume") {
			return nil, false
		}
		return concat(launch[:1], []string{"--continue"}, rest), true
	case "codex":
		// A subcommand, not a flag, so it goes immediately after the program
		// and before anything else: `codex resume --last --model x`. The
		// arguments codex accepts before a subcommand are not the same set it
		// accepts after one, which is the one way this transformation can
		// produce a command line that will not start -- and it is why the
		// dialog prints the result rather than the original.
		if len(rest) > 0 && rest[0] == "resume" {
			return nil, false
		}
		return concat(launch[:1], []string{"resume", "--last"}, rest), true
	}
	return nil, false
}

// ResumesConversation reports whether ResumeArgv would do anything, without
// building the argv.
//
// For the callers that only need to say *whether* a session comes back with
// its conversation -- the restore banner, and the dialog's per-row line.
func ResumesConversation(launch []string) bool {
	_, ok := ResumeArgv(launch)
	return ok
}

// ResumeIsAmbiguous reports whether more than one of these sessions would
// resume out of the same directory.
//
// Both flags above mean "the most recent conversation *here*", so two sessions
// restored into one directory do not come back as two conversations: they come
// back as the same one, twice, with nothing on either screen to say so. That is
// the panel's own normal case -- the product is many agents at once -- and it
// is worse than a cold start, because a cold start is visibly a cold start.
//
// The input is every restorable session, not the ones somebody has ticked.
// Ticking one of a pair and restoring the other tomorrow lands the second on a
// conversation the first has been using all day, so the ambiguity is a property
// of what is on the machine rather than of what is selected. It also makes the
// browser and the server agree without either asking the other: they are
// looking at the same list.
//
// Keyed on the resumed program and the directory, because that pair is what the
// agent itself keys on. Two different agents in one directory are two different
// transcripts and are not ambiguous.
func ResumeIsAmbiguous(rows []ResumeCandidate, program, dir string) bool {
	if program == "" || dir == "" {
		return false
	}
	n := 0
	for _, r := range rows {
		if len(r.Launch) == 0 || r.CWD != dir {
			continue
		}
		if !ResumesConversation(r.Launch) {
			continue
		}
		if baseName(r.Launch[0]) != program {
			continue
		}
		n++
		if n > 1 {
			return true
		}
	}
	return false
}

// ResumeCandidate is the part of a session row this needs: what it ran and
// where. A struct rather than the store row, so this package does not import
// the database to answer a question about argv.
type ResumeCandidate struct {
	Launch []string
	CWD    string
}

// ResumeProgram names the agent an argv would resume, or "".
func ResumeProgram(launch []string) string {
	if !ResumesConversation(launch) {
		return ""
	}
	return baseName(launch[0])
}

func hasAny(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}

// concat builds one slice rather than appending onto launch[:1], which shares
// the caller's backing array and would overwrite launch[1] in place.
func concat(parts ...[]string) []string {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]string, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
