package httpapi

import (
	"context"
	"time"

	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The two sections that answer "how did today go" and "what came out of it".
//
// The *structs* they fill live in share.go, deliberately, because red line 8 is
// that the redaction is one list in one file and a reviewer should be able to
// read it top to bottom. What is here is the arithmetic and the cost control,
// which is long enough to have buried that list.
//
// Both are subject to the same two rules as everything else on this surface:
//
//   - **A board can only subtract.** These sections are computed when a widget
//     on the board asks for them and not otherwise, and every widget chooses
//     among precomputed answers rather than carrying a parameter into a query.
//     The one number a widget does carry -- a day range -- is bounded by the
//     store and by the git package before it reaches either.
//
//   - **Nothing here may make the poll slow.** The event log is two indexed
//     queries over a table of at most a month. The repository half never runs
//     a process at all on this goroutine: it reads whatever internal/git's warm
//     cache has, and says how old it is. See internal/git/warm.go.

// shareFlowDays is the window the day-bucketed flow series covers when a widget
// does not say.
//
// Fourteen, which is two weeks of columns on a tile -- long enough that a
// weekend is visible in the shape and short enough that each column is still
// wide enough to see. A widget may ask for more; the store bounds how many
// buckets it will produce.
const shareFlowDays = 14

// shareFlowBucketSeconds is how finely "today" is cut: one hour.
//
// Twenty-four columns is the shape of a working day, which is the question
// somebody standing at a wall in the afternoon is asking. Anything finer is
// noise at that distance and anything coarser is four columns.
const shareFlowBucketSeconds = 3600

// shareRepoDaysDefault is the repository window when no widget says.
const shareRepoDaysDefault = 14

// maxRepoProjects bounds how many working trees one dashboard reads.
//
// Every one is a `git log` and a `git status` behind the warm cache -- so the
// bound is not on the poll, which runs nothing, but on how many background
// refreshes a single board can keep alive. A dozen is the same number the git
// tab's session list uses, and a panel with more projects than that shows the
// twelve it read and says the list was cut.
const maxRepoProjects = 12

// shareFlowFor buckets the session-event log for one link.
func (s *Server) shareFlowFor(ctx context.Context, scope scopeOf, board store.Board,
	now time.Time, dayStart int64) shareFlow {
	es := store.EventScope{ProjectID: scope.projectID, SessionID: scope.sessionID}
	// A scope that names a row which is gone must show nothing rather than
	// everything. The empty filter is "the whole panel", which is exactly the
	// failure scopeOf exists to prevent, so it is checked here as well as
	// there.
	// `missing` as well as the empty ids, and this is the one place on the
	// surface where the two come apart. The log deliberately outlives the rows
	// it names -- deleting a project must not rewrite an afternoon somebody has
	// already read -- so a scoped link whose project has been deleted still has
	// matching rows in this table, where it has no matching sessions anywhere
	// else. A dashboard that went on drawing them would be a link somebody sent
	// about one project quietly outliving that project.
	if scope.kind != store.ShareWhole &&
		(scope.missing || (es.ProjectID == "" && es.SessionID == "")) {
		return shareFlow{Every: shareFlowBucketSeconds, Buckets: []shareFlowBucket{}}
	}

	days := boardDaysFor(board, flowSeriesKinds, shareFlowDays)
	byHour := boardWantsHours(board)
	out := shareFlow{WindowDays: days, Buckets: []shareFlowBucket{}}

	if today, err := s.DB.CountSessionEvents(ctx, dayStart, es); err == nil {
		out.Today = flowTotals(today)
	} else {
		// Empty rather than fatal, the same way the checklist counts are: a
		// section that cannot be read leaves a gap and the rest of the screen
		// is still true.
		s.Log.Debug("share flow today", "err", err)
	}
	windowStart := startOfLocalDay(now.AddDate(0, 0, -(days - 1)))
	if window, err := s.DB.CountSessionEvents(ctx, windowStart, es); err == nil {
		out.Window = flowTotals(window)
	} else {
		s.Log.Debug("share flow window", "err", err)
	}

	since, bucket := windowStart, 24*3600
	if byHour {
		since, bucket = dayStart, shareFlowBucketSeconds
	}
	out.Every, out.Since = bucket, since
	// Up to the end of the current bucket, not to now: a chart whose last
	// column is three minutes wide reads as a collapse in activity every hour,
	// on the hour.
	until := since + int64(bucket)*(((now.Unix()-since)/int64(bucket))+1)
	rows, err := s.DB.SessionEventSeries(ctx, since, until, bucket, es)
	if err != nil {
		s.Log.Debug("share flow series", "err", err)
		return out
	}
	for _, r := range rows {
		out.Buckets = append(out.Buckets, bucketOfFlow(r.At, flowTotals(r)))
	}
	return out
}

func flowTotals(b store.EventBucket) shareFlowTotals {
	return shareFlowTotals{
		Started: b.Started, Waited: b.Waited, Finished: b.Finished,
		WaitSeconds: b.WaitSeconds, WaitEnded: b.WaitEnded,
	}
}

// shareFeedFor is the last few transitions, renamed for this link.
//
// The window is the same day the counts are on rather than "the last N events",
// so a feed on a quiet morning is short instead of showing yesterday evening as
// though it had just happened.
func (s *Server) shareFeedFor(ctx context.Context, sessions []store.Session, secret []byte,
	named bool, scope scopeOf, dayStart int64) shareFeed {
	out := shareFeed{Entries: []shareFeedEntry{}}
	es := store.EventScope{ProjectID: scope.projectID, SessionID: scope.sessionID}
	// `missing` as well as the empty ids, and this is the one place on the
	// surface where the two come apart. The log deliberately outlives the rows
	// it names -- deleting a project must not rewrite an afternoon somebody has
	// already read -- so a scoped link whose project has been deleted still has
	// matching rows in this table, where it has no matching sessions anywhere
	// else. A dashboard that went on drawing them would be a link somebody sent
	// about one project quietly outliving that project.
	if scope.kind != store.ShareWhole &&
		(scope.missing || (es.ProjectID == "" && es.SessionID == "")) {
		return out
	}
	rows, err := s.DB.RecentSessionEvents(ctx, dayStart, store.MaxEventFeed, es)
	if err != nil {
		s.Log.Debug("share feed", "err", err)
		return out
	}
	title := map[string]string{}
	if named {
		for _, row := range sessions {
			title[row.ID] = row.Title
		}
	}
	for _, ev := range rows {
		entry := shareFeedEntry{
			At:        ev.At,
			SessionID: shareID(secret, ev.SessionID),
			ProjectID: shareID(secret, ev.ProjectID),
			From:      ev.From, To: ev.To, ForSeconds: ev.ForSeconds,
		}
		// A session that has since been deleted has no title to look up, and
		// that is the ordinary case rather than a failure: the log outlives the
		// row on purpose so that deleting a session does not rewrite an
		// afternoon somebody has already read.
		entry.Name = title[ev.SessionID]
		out.Entries = append(out.Entries, entry)
	}
	return out
}

// shareRepoFor reads what the working trees produced, without running anything.
//
// Every figure comes from internal/git's warm cache: the poll takes what is
// there, a refresh happens on a goroutine of its own at most once per
// repository per ninety seconds, and the age of the answer is on the wire so
// the screen can say it. A wall polls every two seconds forever, so the version
// of this that calls ReadActivity directly is not a slower feature, it is a
// panel forking a process a second per project.
//
// Uncommitted work is invisible to all of this: an agent that has been editing
// for an hour without committing produces zero here, and the dirty count beside
// it is the only hint. That is said in docs/api.md rather than on the screen --
// a wall is not the place to explain its own measurement.
func (s *Server) shareRepoWork(ctx context.Context, projects []store.Project, secret []byte,
	named bool, needs map[string]bool, board store.Board, scope scopeOf, dayStart int64) shareRepo {
	days := boardDaysFor(board, repoSeriesKinds, shareRepoDaysDefault)
	out := shareRepo{
		AgeSeconds: -1, WindowDays: days,
		Days: []shareRepoDay{}, ByProject: []shareRepoProject{},
	}
	today := s.today(ctx)
	byDay := map[string]*shareRepoDay{}
	if needs[store.NeedRepoDays] {
		// The frame is built before anything is read, so a day nothing happened
		// on is a zero column rather than a missing one. A series drawn only
		// from the days with commits in them has no weekends in it.
		start := s.nowIn(ctx).AddDate(0, 0, -(days - 1))
		for i := 0; i < days; i++ {
			label := dayShift(s.loc(ctx), start, i)
			out.Days = append(out.Days, shareRepoDay{Label: label})
		}
		for i := range out.Days {
			byDay[out.Days[i].Label] = &out.Days[i]
		}
	}

	oldest := int64(-1)
	for _, p := range projects {
		if !scope.coversProject(p.ID) || p.Path == "" {
			continue
		}
		if out.Projects >= maxRepoProjects {
			break
		}
		out.Projects++
		snap, ready, age := s.Git.Repo(p.Path, days)
		if !ready {
			// Nothing read yet for this project. Counted in Projects so the
			// widget can say how many it is still waiting on, and left out of
			// everything else -- a zero here would be indistinguishable from a
			// morning with no commits in it.
			continue
		}
		out.Readable = true
		if age >= 0 && (oldest < 0 || age > oldest) {
			oldest = age
		}
		item := shareRepoProject{ID: shareID(secret, p.ID), Repo: snap.Repo}
		if named {
			item.Name = p.Name
		}
		if !snap.Repo {
			// A project directory that is not a checkout contributes nothing
			// and says so, which has to look deliberate rather than broken.
			if needs[store.NeedRepo] {
				out.ByProject = append(out.ByProject, item)
			}
			continue
		}
		out.Repos++
		item.Ahead, item.Behind = snap.Status.Ahead, snap.Status.Behind
		item.Dirty = snap.Status.Staged + snap.Status.Unstaged +
			snap.Status.Untracked + snap.Status.Conflicted
		for _, d := range snap.Activity.Days {
			t := shareRepoTotals{
				Commits: d.Commits, Added: d.Insertions, Removed: d.Deletions, Files: d.Files,
			}
			addRepo(&item.Window, t)
			addRepo(&out.Window, t)
			if d.Day == today {
				addRepo(&item.Today, t)
				addRepo(&out.Today, t)
			}
			if row, ok := byDay[d.Day]; ok {
				row.add(t)
			}
		}
		out.ByProject = append(out.ByProject, item)
	}
	if oldest >= 0 {
		out.AgeSeconds = oldest
	}
	if !needs[store.NeedRepo] {
		out.ByProject = []shareRepoProject{}
	}
	if needs[store.NeedRepoPRs] {
		prs := s.sharePRsFor(projects, named, scope, dayStart)
		out.PRs = &prs
	}
	return out
}

func addRepo(into *shareRepoTotals, add shareRepoTotals) {
	into.Commits += add.Commits
	into.Added += add.Added
	into.Removed += add.Removed
	into.Files += add.Files
}

// sharePRsFor is the pull-request rollup, for the one shape of link that may
// have it.
//
// Four conditions, all of them decisions somebody signed in made, and none of
// them a default:
//
//   - the board carries a pull-request widget, so nothing is fetched for a
//     board that does not draw one;
//   - the link is scoped to one project, because a whole-panel link has no
//     single repository and a session-scoped one would be disclosing which
//     project a session belongs to;
//   - the link is in `names` mode, because reaching github.com for a repository
//     is the same disclosure ScopeRepoOwner is gated on -- a counts-mode board
//     must not cause an outbound request naming the customer;
//   - a token is in the panel's environment, which is not something a settings
//     page can put there.
//
// And behind all four, internal/git/warm.go: at most one request per repository
// per GitHubTTL, on a goroutine of its own, shared by every viewer, and stopped
// entirely when nobody is looking.
func (s *Server) sharePRsFor(projects []store.Project, named bool, scope scopeOf,
	dayStart int64) shareRepoPRs {
	out := shareRepoPRs{AgeSeconds: -1}
	if !named || scope.kind != store.ShareProject || scope.missing || scope.cwd == "" {
		return out
	}
	token := git.Token()
	if token == "" {
		return out
	}
	// The remote through the ordinary cache, which is a read of a file in
	// .git and is what the repository tab already does. Remote.GitHub() is the
	// one place allowed to decide what a remote string means.
	snap, err := s.Git.Read(context.Background(), scope.cwd, 0)
	if err != nil || !snap.HasRemote || !snap.Remote.GitHub() {
		return out
	}
	client := s.GitHub
	client.Token = token
	sum, ready, age := s.Git.PRs(client, snap.Remote.Owner, snap.Remote.Name, dayStart)
	if !ready {
		return out
	}
	// Restated field by field rather than embedded, exactly like shareMachine:
	// a field added to git.PRSummary must not become a field on a wall.
	out = shareRepoPRs{
		Readable: true, AgeSeconds: age,
		Open: sum.Open, Draft: sum.Draft,
		Green: sum.Green, Red: sum.Red, Pending: sum.Pending,
		Approved: sum.Approved, ChangesRequested: sum.ChangesRequested,
		MergedToday: sum.MergedToday, MergedPartial: sum.MergedPartial,
	}
	_ = projects
	return out
}

// The kinds whose `days` setting decides how wide each series is.
//
// Spelled out rather than derived from `spec.days`, for the same reason
// daySeriesKinds next door is: three series over three different tables read
// three different windows, and folding them onto one setting would mean a
// board asking for a fortnight of commits also asked for a fortnight of spend.
var flowSeriesKinds = map[string]bool{"flow": true, "waits": true}

var repoSeriesKinds = map[string]bool{"codechurn": true, "spentmade": true}

// boardDaysFor is the widest window any of these kinds asks for.
//
// The widest rather than each widget's own, because there is one payload and
// two widgets on one board drawing different lengths of the same series would
// otherwise need two copies of it. A widget draws the tail of what it is given.
func boardDaysFor(board store.Board, kinds map[string]bool, fallback int) int {
	most := 0
	for _, w := range board.Widgets {
		if !kinds[w.Kind] {
			continue
		}
		n := w.Days
		if n <= 0 {
			n = fallback
		}
		if n > most {
			most = n
		}
	}
	if most == 0 {
		most = fallback
	}
	if most > store.MaxSpendDays {
		most = store.MaxSpendDays
	}
	return most
}

// boardWantsHours reports whether any flow widget is cut by the hour.
//
// One series per payload, so the finer of the two wins: a board with an hourly
// tile and a daily one gets hours, and the daily tile folds them. The other way
// round is a tile that cannot draw what it was asked for.
func boardWantsHours(board store.Board) bool {
	for _, w := range board.Widgets {
		if !flowSeriesKinds[w.Kind] {
			continue
		}
		// Empty means the kind's first `by`, which is "hour".
		if w.By == "" || w.By == "hour" {
			return true
		}
	}
	return false
}
