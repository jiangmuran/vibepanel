package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The guard that keeps the product's "does not phone home" promise true.
//
// Everything else in this package is a read off a disk. The allowlist is the
// only thing standing between that and a call site that adds "fetch" because
// ahead/behind was stale -- which would work, would look like an improvement in
// review, and would put an outbound request on whatever schedule the panel
// polls at.
func TestTheNetworkSubcommandsAreRefused(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{
		"fetch", "pull", "push", "clone", "ls-remote", "submodule",
		"archive", "config", "commit", "checkout", "reset", "clean",
	} {
		_, err := run(context.Background(), dir, sub, "--help")
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("run(%q) = %v, want ErrNotAllowed. Every one of these either reaches a "+
				"network or writes somebody's repository.", sub, err)
		}
	}
	if _, err := run(context.Background(), dir); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("run() with no subcommand = %v, want ErrNotAllowed", err)
	}
}

func TestTheReadingSubcommandsAreAllowed(t *testing.T) {
	// The other direction: an allowlist that has stopped allowing anything is a
	// git tab that is permanently empty, and every test above would still pass.
	for _, sub := range []string{"status", "log", "remote"} {
		if !allowed[sub] {
			t.Errorf("%q is not allowed, so the panel can no longer read a working tree", sub)
		}
	}
}

// The environment a subcommand runs in, which is three separate failures.
func TestTheEnvironmentIsOverriddenRatherThanAppendedTo(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_DIR", "/somewhere/else/.git")

	seen := map[string]string{}
	for _, kv := range env() {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := seen[k]; dup {
			// Which duplicate the child reads is the libc's business, so a
			// duplicate is a setting that is applied on some machines.
			t.Errorf("%s appears twice in the environment", k)
		}
		seen[k] = v
	}
	// Without this the panel takes .git/index.lock while the agent in that
	// directory is running `git commit`, and the agent is the one that fails.
	if seen["GIT_OPTIONAL_LOCKS"] != "0" {
		t.Errorf("GIT_OPTIONAL_LOCKS = %q, want 0", seen["GIT_OPTIONAL_LOCKS"])
	}
	// A credential prompt with no terminal behind it is a request goroutine
	// that never returns, and it takes graceful shutdown with it.
	if seen["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", seen["GIT_TERMINAL_PROMPT"])
	}
	// An inherited GIT_DIR points every invocation at one repository whatever
	// directory it was told to run in.
	if seen["GIT_DIR"] != "" {
		t.Errorf("GIT_DIR = %q, want empty", seen["GIT_DIR"])
	}
}

// The status parser, against every record shape rather than against whatever a
// repository happened to produce.
func TestParseStatusV2(t *testing.T) {
	rec := func(parts ...string) []byte { return []byte(strings.Join(parts, "\x00") + "\x00") }

	t.Run("reads the branch, the upstream and how far apart they are", func(t *testing.T) {
		st := parseStatusV2(rec(
			"# branch.oid 1234567890abcdef",
			"# branch.head feat/auth",
			"# branch.upstream origin/feat/auth",
			"# branch.ab +3 -1",
		))
		if st.Branch != "feat/auth" || st.Upstream != "origin/feat/auth" {
			t.Errorf("branch %q upstream %q", st.Branch, st.Upstream)
		}
		if st.Ahead != 3 || st.Behind != 1 {
			t.Errorf("ahead %d behind %d, want 3 and 1", st.Ahead, st.Behind)
		}
		if st.Head != "1234567" {
			t.Errorf("head = %q", st.Head)
		}
	})

	t.Run("says detached rather than inventing a branch name", func(t *testing.T) {
		st := parseStatusV2(rec("# branch.oid abcdef1234", "# branch.head (detached)"))
		if !st.Detached || st.Branch != "" {
			t.Errorf("detached %v branch %q", st.Detached, st.Branch)
		}
	})

	t.Run("has no head on a repository with no commits", func(t *testing.T) {
		st := parseStatusV2(rec("# branch.oid (initial)", "# branch.head main"))
		if st.Head != "" {
			t.Errorf("head = %q, want empty on an unborn branch", st.Head)
		}
	})

	t.Run("counts a half-staged file as both", func(t *testing.T) {
		// X is the index against HEAD and Y is the worktree against the index.
		// A parser that reads only one of them reports "1 change" about a file
		// that was added and then edited again, which is the case where the
		// difference matters most.
		st := parseStatusV2(rec("1 MM N... 100644 100644 100644 aaa bbb src/main.go"))
		if st.Staged != 1 || st.Unstaged != 1 {
			t.Errorf("staged %d unstaged %d, want 1 and 1", st.Staged, st.Unstaged)
		}
		if len(st.Changes) != 2 || st.Changes[0].Path != "src/main.go" {
			t.Errorf("changes = %+v", st.Changes)
		}
	})

	t.Run("does not count an unmodified side", func(t *testing.T) {
		st := parseStatusV2(rec("1 .M N... 100644 100644 100644 aaa bbb a.txt"))
		if st.Staged != 0 || st.Unstaged != 1 {
			t.Errorf("staged %d unstaged %d, want 0 and 1", st.Staged, st.Unstaged)
		}
	})

	t.Run("reads a rename without swallowing the next entry", func(t *testing.T) {
		// With -z the original path is its own NUL record. A parser that treats
		// every record as an entry reports "old.txt" as an untracked file and
		// nothing errors -- there is simply a wrong line on screen.
		st := parseStatusV2(rec(
			"2 R. N... 100644 100644 100644 aaa bbb R100 new.txt",
			"old.txt",
			"? really-untracked.txt",
		))
		if st.Staged != 1 {
			t.Errorf("staged = %d, want 1", st.Staged)
		}
		if st.Untracked != 1 {
			t.Errorf("untracked = %d, want 1: the rename's source is not an untracked file",
				st.Untracked)
		}
		if len(st.Changes) != 2 {
			t.Fatalf("changes = %+v", st.Changes)
		}
		if st.Changes[0].Path != "new.txt" || st.Changes[0].Renamed != "old.txt" {
			t.Errorf("rename = %+v", st.Changes[0])
		}
		if st.Changes[1].Path != "really-untracked.txt" {
			t.Errorf("untracked = %+v", st.Changes[1])
		}
	})

	t.Run("reads a path with spaces in it", func(t *testing.T) {
		st := parseStatusV2(rec("1 .M N... 100644 100644 100644 aaa bbb docs/a file.md"))
		if len(st.Changes) != 1 || st.Changes[0].Path != "docs/a file.md" {
			t.Errorf("changes = %+v", st.Changes)
		}
	})

	t.Run("counts a conflict as its own thing", func(t *testing.T) {
		// Folded into "unstaged" it would be a number nobody reacts to, and a
		// conflict is the one state on this panel that stops an agent.
		st := parseStatusV2(rec(
			"u UU N... 100644 100644 100644 100644 aaa bbb ccc src/conflict.go",
		))
		if st.Conflicted != 1 || st.Unstaged != 0 || st.Staged != 0 {
			t.Errorf("%+v", st)
		}
		if len(st.Changes) != 1 || st.Changes[0].Kind != KindConflict {
			t.Errorf("changes = %+v", st.Changes)
		}
	})

	t.Run("keeps the counts exact when the list is capped", func(t *testing.T) {
		var parts []string
		for i := 0; i < maxChanges+50; i++ {
			parts = append(parts, "? file")
		}
		st := parseStatusV2(rec(parts...))
		if st.Untracked != maxChanges+50 {
			t.Errorf("untracked = %d; the number is the useful part and must not be capped "+
				"with the list", st.Untracked)
		}
		if len(st.Changes) != maxChanges || !st.ChangesTruncated {
			t.Errorf("changes %d truncated %v", len(st.Changes), st.ChangesTruncated)
		}
	})

	t.Run("is clean when there is nothing", func(t *testing.T) {
		st := parseStatusV2(rec("# branch.head main"))
		if st.Dirty() {
			t.Error("a tree with no entries reported dirty")
		}
	})
}

func TestParseRemote(t *testing.T) {
	for _, tc := range []struct {
		raw               string
		host, owner, name string
		github            bool
	}{
		{"git@github.com:jiangmuran/vibepanel.git", "github.com", "jiangmuran", "vibepanel", true},
		{"ssh://git@github.com/jiangmuran/vibepanel.git", "github.com", "jiangmuran", "vibepanel", true},
		{"https://github.com/jiangmuran/vibepanel.git", "github.com", "jiangmuran", "vibepanel", true},
		{"https://github.com/jiangmuran/vibepanel", "github.com", "jiangmuran", "vibepanel", true},
		{"https://someone:token@github.com/jiangmuran/vibepanel.git", "github.com", "jiangmuran", "vibepanel", true},
		{"git://github.com/jiangmuran/vibepanel.git", "github.com", "jiangmuran", "vibepanel", true},
		// Case in the host, which DNS does not care about and a string
		// comparison does.
		{"git@GitHub.com:o/r.git", "github.com", "o", "r", true},
		// Not GitHub, and it must not be treated as if it were: a panel that
		// guessed an API host from a remote is one clone away from being told
		// where to send a token.
		{"git@gitlab.com:o/r.git", "gitlab.com", "o", "r", false},
		{"https://github.example.com/o/r.git", "github.example.com", "o", "r", false},
		// A local path is a remote too, and it is not a URL.
		{"/srv/repos/thing.git", "", "", "", false},
		// Nested groups, which GitLab has and GitHub does not.
		{"git@gitlab.com:group/sub/r.git", "gitlab.com", "", "", false},
		// The character class. These end up in a request to another machine.
		{"https://github.com/o/../../etc.git", "github.com", "", "", false},
		{"https://github.com/o/r r.git", "github.com", "", "", false},
	} {
		got := ParseRemote(tc.raw)
		if got.Host != tc.host || got.Owner != tc.owner || got.Name != tc.name {
			t.Errorf("ParseRemote(%q) = host %q owner %q name %q, want %q %q %q",
				tc.raw, got.Host, got.Owner, got.Name, tc.host, tc.owner, tc.name)
		}
		if got.GitHub() != tc.github {
			t.Errorf("ParseRemote(%q).GitHub() = %v, want %v", tc.raw, got.GitHub(), tc.github)
		}
	}
}

func TestTokenComesFromTheEnvironmentAndNowhereElse(t *testing.T) {
	for _, key := range TokenEnv {
		t.Setenv(key, "")
	}
	if Token() != "" {
		t.Error("a token appeared with nothing in the environment")
	}
	t.Setenv("GH_TOKEN", "  second  ")
	if Token() != "second" {
		t.Errorf("Token() = %q, want the trimmed value", Token())
	}
	t.Setenv("GITHUB_TOKEN", "first")
	if Token() != "first" {
		t.Errorf("Token() = %q; GITHUB_TOKEN comes first", Token())
	}
}

// Against a real repository, because the bugs worth catching in a format parser
// are the format's.
func TestAgainstARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	sh := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	t.Run("says so about a directory that is not a repository", func(t *testing.T) {
		if _, err := ReadStatus(ctx, dir); !errors.Is(err, ErrNotARepo) {
			t.Fatalf("ReadStatus on a plain directory = %v, want ErrNotARepo", err)
		}
	})

	sh("init", "-b", "main")
	sh("config", "user.email", "t@example.com")
	sh("config", "user.name", "T")

	t.Run("reads an empty repository without failing", func(t *testing.T) {
		st, err := ReadStatus(ctx, dir)
		if err != nil {
			t.Fatalf("ReadStatus: %v", err)
		}
		if !st.Repo || st.Branch != "main" || st.Dirty() {
			t.Errorf("%+v", st)
		}
		// A repository with no commits has an empty log, not an error. This is
		// what a project looks like on its first day.
		log, err := ReadLog(ctx, dir, 5)
		if err != nil || len(log) != 0 {
			t.Errorf("ReadLog on an unborn branch = %v, %v", log, err)
		}
	})

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh("add", "a.txt")
	sh("commit", "-m", "first: a subject with  spaces")

	t.Run("reads the log", func(t *testing.T) {
		log, err := ReadLog(ctx, dir, 5)
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		if len(log) != 1 {
			t.Fatalf("log = %+v", log)
		}
		if log[0].Subject != "first: a subject with  spaces" {
			t.Errorf("subject = %q", log[0].Subject)
		}
		if log[0].Author != "T" || log[0].When == 0 || len(log[0].SHA) != 40 {
			t.Errorf("commit = %+v", log[0])
		}
	})

	t.Run("sees an untracked file and a modified one", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := ReadStatus(ctx, dir)
		if err != nil {
			t.Fatalf("ReadStatus: %v", err)
		}
		if st.Unstaged != 1 || st.Untracked != 1 || !st.Dirty() {
			t.Errorf("%+v", st)
		}
	})

	t.Run("has no remote until there is one", func(t *testing.T) {
		if _, ok, err := ReadRemote(ctx, dir); ok || err != nil {
			t.Errorf("ReadRemote = %v, %v; a local-only repository is the ordinary case", ok, err)
		}
		sh("remote", "add", "origin", "git@github.com:o/r.git")
		remote, ok, err := ReadRemote(ctx, dir)
		if err != nil || !ok || !remote.GitHub() {
			t.Fatalf("ReadRemote = %+v, %v, %v", remote, ok, err)
		}
		if remote.Owner != "o" || remote.Name != "r" {
			t.Errorf("remote = %+v", remote)
		}
	})
}
