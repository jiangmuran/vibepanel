package git

import (
	"context"
	"strconv"
	"time"
)

// A second cache, with the opposite failure in mind.
//
// cache.go exists so that six tabs polling one project are one `git status`.
// Its contract is "an answer this recent is the answer", and a caller that
// arrives on a cold entry *waits* for the read. That is right for a tab
// somebody just opened and wrong for the thing this file is for.
//
// The read-only dashboard polls every two seconds and never stops, on a screen
// with nobody standing at it. `git log --shortstat` over a month is not a
// two-second question, and a GitHub round trip is not one either — so a poll
// that ever waits for one is a wall that stutters, and a poll that ever *starts*
// one per project is a panel forking a process a second. Both are the same bug
// seen from two ends.
//
// So: **the poll never runs anything.** It reads what is already there and says
// how old it is; a refresh happens on a goroutine of its own, at most one per
// key at a time, and only ever because somebody asked. The first poll for a key
// therefore returns "nothing yet", which the payload carries as a flag and the
// widget renders as "counting" — the same shape shareSpend.Readable already
// uses, and for the same reason: zero and "not counted yet" are different facts
// and the first one is a lie about a repository.
//
// What this is not, and must not become: a poller. Nothing here has a ticker.
// A key that stops being asked about stops being refreshed and is swept, so a
// panel nobody is watching does no work at all — the same rule the token
// ingester and the trend ring already follow. That is what keeps the GitHub
// half of this honest: internal/git/github.go says the network runs when
// somebody presses a button, and a wall that an owner deliberately pointed at a
// board with a pull-request tile on it is that press, held down. It is bounded
// by the TTL, shared across every viewer of every link, and gone the moment the
// screen is switched off.

// WarmTTL is how long a background-refreshed answer stays current.
//
// Ninety seconds for a working tree. Commits do not land every two seconds, and
// the figure this feeds is "how many today" — a number whose next value is a
// minute and a half away at worst. Against a wall polling every two seconds
// that is one `git log` per project per ninety seconds instead of forty-five per
// second, which is the difference between a feature and a fork bomb.
const WarmTTL = 90 * time.Second

// GitHubTTL is the same for the network half, and it is longer on purpose.
//
// Five minutes is 288 requests a day for a repository somebody has a screen
// pointed at, against a GraphQL budget of 5,000 points an hour. Ninety seconds
// would be 960 and still fine; five minutes is chosen so that adding a second
// and a third wall changes nothing, because they share the entry.
const GitHubTTL = 5 * time.Minute

// warmIdle is how long an unasked-for key is kept before being dropped.
//
// Longer than warmTTL by enough that a rotating board — one whose repository
// tile is only on page three — does not lose its entry between appearances, and
// short enough that a link somebody revoked stops holding a snapshot.
const warmIdle = 15 * time.Minute

// warmEntry is one background-refreshed answer.
type warmEntry struct {
	val any
	at  time.Time
	// refreshing is held for the life of one refresh, so a second caller
	// arriving mid-flight starts nothing. Not a channel, because nobody waits:
	// the whole point is that the caller returns immediately either way.
	refreshing bool
	// asked is the last time somebody wanted this key, which is what the sweep
	// ages out. Separate from `at`: a key being asked for constantly but failing
	// to refresh must not be swept out from under the screen asking.
	asked time.Time
	// tried is when a refresh was last *started*, successful or not, and it is
	// what rate-limits the failing case.
	//
	// Without it, the one situation that always fails -- a project directory
	// that is not a repository, a mount that has gone away -- is the one that
	// starts a process on every single poll, because `at` never advances. That
	// is the fork-per-poll this file exists to prevent, arriving through the
	// error path instead of the happy one.
	tried time.Time
}

// Warm returns the last answer for a key and refreshes it behind the caller.
//
// `ok` is false only until the first refresh has finished. After that the value
// is returned however stale it is, with the caller free to say how old it is —
// a wall showing a ninety-second-old commit count next to the time it was taken
// is honest, and a wall showing nothing while it waits is not.
func (c *Cache) warm(key string, ttl time.Duration, fn func(context.Context) (any, error)) (any, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.warmed == nil {
		c.warmed = map[string]*warmEntry{}
	}
	c.sweepWarmLocked(now)
	e := c.warmed[key]
	if e == nil {
		e = &warmEntry{}
		c.warmed[key] = e
	}
	e.asked = now
	stale := e.at.IsZero() || now.Sub(e.at) >= ttl
	// Both clocks, and the second one is the load-bearing half. `at` gates "is
	// the answer old"; `tried` gates "may another process be started", which is
	// the only thing standing between a wall's two-second poll and a fork per
	// poll for every key that has never succeeded.
	if stale && !e.refreshing && (e.tried.IsZero() || now.Sub(e.tried) >= ttl) {
		e.refreshing = true
		e.tried = now
		// Detached from the caller's context, deliberately. The caller is a
		// two-second poll and is about to return; tying the refresh to it would
		// cancel every read the moment the response was written. Nothing here
		// can run forever: run() bounds every git invocation at runTimeout and
		// the GitHub client bounds its own request.
		go c.refresh(e, fn)
	}
	if e.at.IsZero() {
		return nil, false
	}
	return e.val, true
}

func (c *Cache) refresh(e *warmEntry, fn func(context.Context) (any, error)) {
	val, err := fn(context.Background())
	c.mu.Lock()
	defer c.mu.Unlock()
	e.refreshing = false
	if err != nil {
		// The previous answer is kept and its age goes on climbing, which is
		// what the screen shows. Replacing it with a zero would turn "the
		// repository could not be read just now" into "nothing happened today",
		// and there is nobody standing at the wall to tell them apart. A key
		// that has never succeeded stays "not counted yet", which is a third
		// thing again and the one the widget says out loud.
		return
	}
	e.val, e.at = val, time.Now()
}

// WarmAge is how old a warm answer is, in seconds, or -1 when there is none.
//
// Read separately rather than returned by warm() so that the value's type stays
// the caller's business. Every widget drawn from one of these shows it.
func (c *Cache) WarmAge(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.warmed[key]
	if e == nil || e.at.IsZero() {
		return -1
	}
	return int64(time.Since(e.at) / time.Second)
}

func (c *Cache) sweepWarmLocked(now time.Time) {
	for k, e := range c.warmed {
		if e.refreshing {
			continue
		}
		if now.Sub(e.asked) > warmIdle {
			delete(c.warmed, k)
		}
	}
}

func (c *Cache) warmTTL() time.Duration {
	if c.WarmFor != 0 {
		return c.WarmFor
	}
	return WarmTTL
}

// activityKey names one window of one working tree, on one calendar.
//
// The days are in the key because a board asking for a week and one asking for
// a month are two different reads, and folding them onto one entry would mean
// whichever screen polled last decided what the other one saw.
//
// The zone is in it for the same reason and one more: the day labels in the
// answer are built in it, so an entry read under the machine's zone would go on
// being served for ninety seconds after somebody set the panel's -- which is
// the settings page appearing to do nothing.
func activityKey(dir string, days int, loc *time.Location) string {
	return "activity\x00" + dir + "\x00" + strconv.Itoa(days) + "\x00" + loc.String()
}

// RepoSnapshot is one working tree as a wall may know it.
//
// Both halves under one key, so a project is two processes per refresh rather
// than two keys refreshing on their own clocks -- which would put a commit count
// and the branch state it sits next to at different ages on the same tile.
type RepoSnapshot struct {
	Activity Activity
	Status   Status
	// Repo is false for a directory that is not a working tree, which is an
	// ordinary state for a project and not an error.
	Repo bool
}

// Repo is one working tree's activity and branch state, from the warm cache.
//
// `ok` is false until the first read has finished, which is what the payload
// carries as "not counted yet". Nothing here waits: see the top of this file.
//
// loc is the panel's configured zone, not the machine's, and it is a parameter
// because this package has no way to ask. Everything the caller compares these
// day labels against -- its day frame, its "is this today" -- is on the panel's
// calendar, so a read taken on time.Local files a commit made seconds ago under
// yesterday and reports zero for today whenever the two are on different dates.
func (c *Cache) Repo(dir string, days int, loc *time.Location) (RepoSnapshot, bool, int64) {
	key := activityKey(dir, days, loc)
	v, ok := c.warm(key, c.warmTTL(), func(ctx context.Context) (any, error) {
		act, err := ReadActivity(ctx, dir, days, time.Now().In(loc))
		if err != nil {
			// Not a repository is an answer rather than a failure, and it has
			// to be *cached* as one: an error would leave the entry cold and
			// the TTL is then the only thing between a directory that will
			// never be a checkout and a process started for it forever.
			if errorsNotARepo(err) {
				return RepoSnapshot{}, nil
			}
			return nil, err
		}
		snap := RepoSnapshot{Activity: act, Repo: true}
		// Best effort, exactly as Read's log and remote are: a tree whose status
		// cannot be read still has a commit count worth showing.
		if st, serr := ReadStatus(ctx, dir); serr == nil {
			snap.Status = st
		}
		return snap, nil
	})
	snap, _ := v.(RepoSnapshot)
	return snap, ok, c.WarmAge(key)
}

func errorsNotARepo(err error) bool {
	return err == ErrNotARepo || notARepo(err)
}

// Remote is one working tree's origin, from the warm cache.
//
// Its own key rather than a projection of Cache.Read, and that is the whole
// point of it existing. Read is the *foreground* cache: it runs three processes
// on the caller's goroutine, and one of them is
// `git log --format=%H%x00%an%x00%at%x00%s`. A wall asking "what is this
// project's repository called" through Read was therefore forking three
// processes per poll, forever, and pulling every recent commit's subject and
// author name into this process to arrive at two words that are already in the
// remote URL. This is one `git remote get-url origin`, on a goroutine of its
// own, at most once per directory per WarmTTL.
//
// `ok` is false until the first read has finished, so the first poll after a
// restart names no repository and the next one does -- which looks the same on
// screen as the four other ways a project has no repository to name.
func (c *Cache) Remote(dir string) (Remote, bool) {
	v, ok := c.warm("remote\x00"+dir, c.warmTTL(), func(ctx context.Context) (any, error) {
		remote, has, err := ReadRemote(ctx, dir)
		if err != nil {
			// Not a repository is an answer rather than a failure and has to be
			// cached as one, exactly as it is for Repo: an error leaves the
			// entry cold, and `tried` is then the only thing between a
			// directory that will never be a checkout and a process started for
			// it on every poll.
			if errorsNotARepo(err) {
				return Remote{}, nil
			}
			return nil, err
		}
		if !has {
			// A repository nobody has pushed. An ordinary state, and it must be
			// cached for the same reason.
			return Remote{}, nil
		}
		return remote, nil
	})
	r, _ := v.(Remote)
	return r, ok
}

// PRSummary is what a wall may know about a repository's pull requests.
//
// Counts and rollup states, and nothing that is a name: no titles, no numbers,
// no authors, no branch names, no URLs. See internal/httpapi/share.go for the
// disclosure argument; this type is the shape of it, so that adding a field
// here is the reviewable moment rather than adding one at a call site.
type PRSummary struct {
	Open  int `json:"open"`
	Draft int `json:"draft"`
	// Green, Red and Pending are the head commit's check rollup, counted. A
	// pull request whose checks have not run is in none of the three.
	Green   int `json:"green"`
	Red     int `json:"red"`
	Pending int `json:"pending"`
	// Approved and ChangesRequested are GitHub's own review rollup, counted.
	Approved         int `json:"approved"`
	ChangesRequested int `json:"changesRequested"`
	// MergedToday is how many were merged since the start of the server's local
	// day. Counted from the most recently updated merged pull requests, so it
	// is a floor on a repository merging more than mergedCount a day -- which
	// the flag below says.
	MergedToday   int  `json:"mergedToday"`
	MergedPartial bool `json:"mergedPartial"`
}

// PRs is a repository's pull-request summary, from the warm cache.
//
// The token is not in the key. It is a credential, keys are held in a map for
// the life of the process, and two links looking at one repository are one
// entry whichever of them polled first.
func (c *Cache) PRs(client Client, owner, name string, dayStart int64) (PRSummary, bool, int64) {
	key := "prs\x00" + owner + "\x00" + name
	ttl := c.GitHubFor
	if ttl == 0 {
		ttl = GitHubTTL
	}
	v, ok := c.warm(key, ttl, func(ctx context.Context) (any, error) {
		return client.Summarise(ctx, owner, name, dayStart)
	})
	sum, _ := v.(PRSummary)
	return sum, ok, c.WarmAge(key)
}
