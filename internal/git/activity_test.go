package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The shapes `git log --shortstat` actually emits, none of which the obvious
// parser gets right.
func TestTheShortstatShapesGitActuallyEmits(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		line                    string
		files, insert, deleting int
	}{
		{"the ordinary one", " 3 files changed, 12 insertions(+), 4 deletions(-)", 3, 12, 4},
		// The singular. A parser matching "files changed" exactly reports zero
		// files for every commit that touched one, which is most of them.
		{"one file", " 1 file changed, 2 insertions(+)", 1, 2, 0},
		// git omits the clause it has nothing to say about, so the third field
		// of a pure insertion is not the deletions -- there is no third field.
		{"only deletions", " 2 files changed, 9 deletions(-)", 2, 0, 9},
		{"only insertions", " 1 file changed, 1 insertion(+)", 1, 1, 0},
		{"a mode change alone", " 1 file changed, 0 insertions(+), 0 deletions(-)", 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, i, d := parseShortstat(tc.line)
			if f != tc.files || i != tc.insert || d != tc.deleting {
				t.Fatalf("got %d files, +%d, -%d; want %d, +%d, -%d",
					f, i, d, tc.files, tc.insert, tc.deleting)
			}
		})
	}
}

// A merge commit has no shortstat at all, and the line after it is the *next*
// commit's timestamp. A parser that carries the previous commit's day forward
// attributes the next commit's lines to the wrong bucket.
func TestACommitWithNoDiffDoesNotStealTheNextOnesLines(t *testing.T) {
	day := time.Date(2026, 8, 27, 12, 0, 0, 0, time.Local)
	out := Activity{Days: dayFrame(day.AddDate(0, 0, -1), 2)}
	yesterday := day.AddDate(0, 0, -1).Unix()
	// Three commits: a merge today with no stats, one yesterday with stats, and
	// one from *outside* the two-day frame that also has stats. The last is the
	// case the reset exists for -- its day is not in the frame, so its lines
	// belong to nothing, and a parser that leaves the previous commit's day
	// standing puts them on yesterday.
	outside := day.AddDate(0, 0, -9).Unix()
	text := itoa(day.Unix()) + "\n" +
		itoa(yesterday) + "\n 2 files changed, 5 insertions(+)\n" +
		itoa(outside) + "\n 4 files changed, 900 insertions(+)\n"
	parseActivity(text, &out)

	if out.Commits != 3 {
		t.Fatalf("counted %d commits, want 3", out.Commits)
	}
	if out.Days[1].Insertions != 0 {
		t.Errorf("today gained %d insertions from a commit that has none",
			out.Days[1].Insertions)
	}
	if out.Days[0].Insertions != 5 {
		t.Errorf("yesterday has %d insertions, want 5 -- a commit from outside the frame "+
			"has been attributed to it", out.Days[0].Insertions)
	}
	// The window totals still count it: the figures are "what happened", and
	// only the per-day series is confined to the frame.
	if out.Insertions != 905 {
		t.Errorf("the window totals %d insertions, want 905", out.Insertions)
	}
}

// What this package asks git for, pinned as a list.
//
// The property is "it never asks for text", and it is enforced by the argument
// list rather than by the parser: a `--numstat` carries every changed filename
// in somebody's repository through this process on its way to a wall, and a
// `%s` carries the commit messages. Neither is needed, so neither is asked for,
// and this is what says so when somebody adds a field to the format string
// because it seemed harmless.
func TestTheActivityReadAsksForATimestampAndNothingElse(t *testing.T) {
	src, err := os.ReadFile("activity.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, banned := range []struct{ needle, why string }{
		{"--numstat", "a line per changed path, so every filename in the repository"},
		{"--name-only", "the paths again"},
		{"--name-status", "the paths again"},
		{"%s", "the commit subject: prose from inside somebody's repository"},
		{"%b", "the commit body"},
		{"%an", "the author's name"},
		{"%ae", "the author's address"},
		{"%H", "the sha"},
		{"%d", "the ref names, which are branch names"},
	} {
		if strings.Contains(text, `"`+banned.needle) || strings.Contains(text, banned.needle+`"`) ||
			strings.Contains(text, banned.needle+"%") {
			t.Errorf("the activity read asks git for %s -- %s", banned.needle, banned.why)
		}
	}
	if !strings.Contains(text, `"--format=%at"`) {
		t.Error("the format is no longer a bare timestamp; whatever replaced it is what " +
			"this package now reads out of somebody's repository")
	}
	// And the bound, which is what stops "show me a year" on a monorepo from
	// being a process reading a hundred thousand commit diffs while a wall
	// waits.
	if !strings.Contains(text, "--max-count=") {
		t.Error("the log is no longer bounded; a window setting on a widget now decides " +
			"how many commits one read walks")
	}
}

// A window longer than the bound is clamped rather than obeyed.
//
// Against a real repository, and that is not incidental: a directory that is
// not a checkout returns before the frame is built at all, so the version of
// this test that used a bare t.TempDir() passed with the clamp deleted.
func TestAWindowLongerThanTheBoundIsClamped(t *testing.T) {
	dir := oneCommitRepo(t)
	act, err := ReadActivity(context.Background(), dir, 100000, time.Now())
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if len(act.Days) > maxActivityDays {
		t.Errorf("asked for 100000 days and got a frame of %d; the range comes from a row "+
			"in a table and has to have a ceiling", len(act.Days))
	}
	if len(act.Days) != maxActivityDays {
		t.Errorf("the frame is %d days, want the bound of %d", len(act.Days), maxActivityDays)
	}
}

// oneCommitRepo is a working tree with a single empty commit in it.
func oneCommitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "one"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot arrange a repository: %v: %s", err, out)
		}
	}
	return tmp
}

// A key whose read always fails is retried on the TTL, not on every call.
//
// The fork-per-poll arriving through the error path. `at` never advances for a
// key that has never succeeded, so without a separate "when did we last try"
// the one case that is always wrong -- a project directory that is not a
// checkout -- is the one that starts a process on every single poll.
func TestAFailingKeyIsNotRetriedOnEveryPoll(t *testing.T) {
	c := &Cache{}
	tries := make(chan struct{}, 64)
	fn := func(ctx context.Context) (any, error) {
		tries <- struct{}{}
		return nil, errAlways
	}
	for i := 0; i < 40; i++ {
		if _, ok := c.warm("k", time.Hour, fn); ok {
			t.Fatalf("call %d answered for a key that has never been read", i)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if n := len(tries); n > 1 {
		t.Errorf("forty polls started %d reads of a key that always fails; one is the whole "+
			"point of the TTL", n)
	}
}

var errAlways = errors.New("git: nope")

// A window is days on the server's clock, not a rolling 24 hours: somebody
// looking at a wall in the afternoon means "since this morning" by today.
func TestTheWindowIsWholeLocalDays(t *testing.T) {
	tmp := oneCommitRepo(t)
	act, err := ReadActivity(context.Background(), tmp, 3, time.Now())
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if len(act.Days) != 3 {
		t.Fatalf("asked for 3 days and got %d", len(act.Days))
	}
	if act.Commits != 1 {
		t.Fatalf("counted %d commits, want 1", act.Commits)
	}
	// Every day is present, including the empty ones. A series drawn from only
	// the days with commits in them has no quiet afternoons in it.
	for i, d := range act.Days {
		if d.Day == "" {
			t.Fatalf("day %d has no label", i)
		}
	}
}

// A directory that is not a checkout is an ordinary answer, and it must be
// *cached* as one -- otherwise the one case that always fails is the one that
// starts a process on every poll.
func TestADirectoryThatIsNotARepositoryIsAnAnswer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	c := &Cache{WarmFor: time.Hour}
	dir := t.TempDir()
	if _, ok, _ := c.Repo(dir, 7); ok {
		t.Fatal("a cold key answered before anything had been read")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok, _ := c.Repo(dir, 7); ok {
			if snap.Repo {
				t.Fatal("a temporary directory reported itself as a working tree")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the warm cache never produced an answer for a directory that is not a repository")
}

// The property the wall depends on: a poll never runs anything, however often
// it asks.
func TestAWarmReadNeverRunsOnTheCallersGoroutine(t *testing.T) {
	c := &Cache{WarmFor: time.Hour}
	runs := make(chan struct{}, 16)
	// Slow but finite, deliberately. The obvious fixture blocks the read on a
	// channel the test closes afterwards, and a version of warm() that ran the
	// read inline would then *deadlock* rather than fail -- which is a red run
	// ten minutes later instead of a failed assertion, and unusable in a
	// mutation sweep.
	const slow = 250 * time.Millisecond
	fn := func(ctx context.Context) (any, error) {
		runs <- struct{}{}
		time.Sleep(slow)
		return 7, nil
	}

	start := time.Now()
	for i := 0; i < 50; i++ {
		if _, ok := c.warm("k", time.Hour, fn); ok {
			t.Fatalf("call %d got an answer before the first read finished", i)
		}
	}
	if waited := time.Since(start); waited > slow/4 {
		t.Fatalf("fifty polls took %v against a %v read; the poll is waiting for it, and a "+
			"wall asking every two seconds must never be the thing that runs `git log`",
			waited, slow)
	}

	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Fatal("the refresh never started")
	}
	select {
	case <-runs:
		t.Fatal("fifty polls started more than one read")
	case <-time.After(50 * time.Millisecond):
	}

	// And it does land, without anybody asking differently.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := c.warm("k", time.Hour, fn); ok {
			if v != 7 {
				t.Fatalf("the warm answer is %v, want 7", v)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the refresh never produced an answer")
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
