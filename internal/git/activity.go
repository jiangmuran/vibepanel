package git

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// What has actually been built in a working tree, counted.
//
// This is the half of "花了多少 vs 做出来什么" that could not be answered before
// internal/git existed: the panel could say what the agents spent and had
// nothing at all to put beside it. Commits and changed lines are what a
// repository can honestly say, and they are what was asked for by name.
//
// **`--shortstat`, never `--numstat`, and that is a disclosure decision rather
// than a parsing one.** numstat is a line per changed path, so the natural
// implementation would carry every filename in the repository through this
// package on its way to a wall display. shortstat is one summary line per
// commit — "3 files changed, 12 insertions(+), 4 deletions(-)" — so a path
// cannot reach a caller because it never reaches this file. Subjects, authors
// and shas are absent for the same reason: `--format=%at` asks for a timestamp
// and nothing else. A commit *message* is prose about a customer, a bug or a
// deadline; a commit *count* is a number.
//
// It is also what makes the parse boring. numstat's paths are C-quoted or not
// depending on core.quotepath, and a filename with a newline in it is a
// line-based parser reading half a path as a record. shortstat has no
// user-controlled text in it at all.
//
// **`--branches`, not `HEAD` and not `--all`.** The panel exists to watch
// several agents working at once, and they work on branches; a count from HEAD
// alone reports one of six agents. `--all` would add remote-tracking refs and
// tags, so a `git fetch` in another window would show up as somebody's
// afternoon of work. Local branches are what was done on this machine, which is
// the question.
//
// Lines of code are a famously bad measure of work, and nothing here pretends
// otherwise: what the widgets do with this is pair it with commits and with
// what came out, and label it as change rather than as output. That is an
// argument about presentation, not a reason to refuse a number that was asked
// for twice.

// Activity is what happened in one working tree over a window.
type Activity struct {
	// Since is the start of the window, in unix seconds.
	Since int64
	// Commits, Insertions, Deletions and Files are the whole window.
	Commits    int
	Insertions int
	Deletions  int
	Files      int
	// Days is one entry per local day in the window, oldest first, including
	// the days nothing happened on. A series drawn from only the days with
	// commits in them is a series with no weekends in it.
	Days []ActivityDay
	// Truncated says the log hit maxActivityCommits, so the figures are a floor
	// rather than a total. A number that has silently stopped counting is the
	// defect this panel refuses everywhere else.
	Truncated bool
}

// ActivityDay is one local day of it.
type ActivityDay struct {
	// Day is "2006-01-02" on the *server's* clock, the same convention the
	// token rollups use. A browser in another timezone must not decide for
	// itself which column is today.
	Day        string
	Commits    int
	Insertions int
	Deletions  int
	// Files is how many file changes the day's commits touched, summed across
	// them rather than deduplicated -- a file changed in three commits counts
	// three times, because git counts per commit and a cross-commit union is
	// the one figure --shortstat cannot give without the paths this package
	// deliberately never reads.
	Files int
}

// maxActivityCommits bounds one read.
//
// Two thousand commits is several months of a busy repository and about 80KB of
// shortstat, well inside maxOutput. The bound matters because the window comes
// from a widget's setting: without it, "show me a year" on a monorepo is a
// process reading a hundred thousand commit diffs while a wall waits.
const maxActivityCommits = 2000

// maxActivityDays bounds the window a caller may ask for.
//
// The same 371 the token history uses, for the same reason: it is 53 whole
// weeks, and anything longer is a range with no ceiling driven by a row in a
// table.
const maxActivityDays = 371

// ReadActivity counts the commits and changed lines of the last `days` days.
//
// `days` is counted in local days ending at `now`, so "1" is today rather than
// the last twenty-four hours — which is what somebody looking at a wall in the
// afternoon means by "today".
func ReadActivity(ctx context.Context, dir string, days int, now time.Time) (Activity, error) {
	if days < 1 {
		days = 1
	}
	if days > maxActivityDays {
		days = maxActivityDays
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(days - 1))

	out := Activity{Since: start.Unix(), Days: dayFrame(start, days)}
	// --since as a unix timestamp, so the window is the one computed above
	// rather than one git parses out of a human phrase in whatever locale the
	// panel happens to be running under.
	raw, err := run(ctx, dir, "log", "--branches",
		"--since="+strconv.FormatInt(start.Unix(), 10),
		"--max-count="+strconv.Itoa(maxActivityCommits+1),
		"--no-color", "--no-show-signature", "--shortstat", "--format=%at")
	if err != nil {
		if notARepo(err) {
			return Activity{}, ErrNotARepo
		}
		// A repository with no commits yet is an empty window, not a failure.
		// It is also what a brand-new project looks like on the morning
		// somebody points a screen at it.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "does not have any commits") ||
			strings.Contains(msg, "unknown revision") {
			return out, nil
		}
		return Activity{}, err
	}
	parseActivity(string(raw), &out)
	return out, nil
}

// dayFrame is every local day in the window, oldest first, all zero.
func dayFrame(start time.Time, days int) []ActivityDay {
	out := make([]ActivityDay, days)
	for i := 0; i < days; i++ {
		out[i] = ActivityDay{Day: start.AddDate(0, 0, i).Format("2006-01-02")}
	}
	return out
}

// parseActivity reads `git log --shortstat --format=%at`.
//
// Split out from the read so every record shape can be tested without arranging
// a repository that produces one. The shapes that matter: a commit with no
// shortstat at all (a merge, or an empty commit), the singular "1 file changed",
// and a commit that only inserted or only deleted -- git omits the clause it has
// nothing to say about, so a parser splitting on commas by position reports the
// deletions of a pure insertion as its insertions.
func parseActivity(text string, out *Activity) {
	index := map[string]int{}
	for i, d := range out.Days {
		index[d.Day] = i
	}
	day := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if ts, err := strconv.ParseInt(line, 10, 64); err == nil {
			out.Commits++
			if out.Commits > maxActivityCommits {
				out.Truncated = true
				out.Commits = maxActivityCommits
				return
			}
			day = -1
			if i, ok := index[time.Unix(ts, 0).Format("2006-01-02")]; ok {
				day = i
				out.Days[i].Commits++
			}
			continue
		}
		if !strings.HasPrefix(line, " ") {
			continue
		}
		files, ins, del := parseShortstat(line)
		out.Files += files
		out.Insertions += ins
		out.Deletions += del
		if day >= 0 {
			out.Days[day].Insertions += ins
			out.Days[day].Deletions += del
			out.Days[day].Files += files
		}
	}
}

// parseShortstat reads " 3 files changed, 12 insertions(+), 4 deletions(-)".
//
// By clause rather than by position: git omits the clause it has nothing to say
// about, so a commit that only added lines has two clauses and a parser reading
// the third field as deletions reports its insertions twice.
func parseShortstat(line string) (files, insertions, deletions int) {
	for _, clause := range strings.Split(line, ",") {
		fields := strings.Fields(clause)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil || n < 0 {
			continue
		}
		switch {
		case strings.HasPrefix(fields[1], "file"):
			files = n
		case strings.HasPrefix(fields[1], "insertion"):
			insertions = n
		case strings.HasPrefix(fields[1], "deletion"):
			deletions = n
		}
	}
	return files, insertions, deletions
}
