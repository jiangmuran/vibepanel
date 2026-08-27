package git

import (
	"context"
	"sync"
	"time"
)

// A cache in front of the working tree, because the tab that reads it polls.
//
// The cost being avoided is specific and was measured before this existed. One
// open git tab is three processes — ReadStatus, ReadLog, ReadRemote — plus one
// more per session sitting in a worktree of its own, every five seconds. The
// panel's premise is several people, or several tabs, watching the same handful
// of projects: six viewers on one repository was eighteen processes every five
// seconds walking one .git directory, for an answer that changes when an agent
// commits. `git status` on a large tree is not free, and the agents doing the
// committing are contending for the same inodes.
//
// Two mechanisms, and neither is sufficient alone:
//
//   - **A short TTL.** An answer this recent is the answer. It is deliberately
//     shorter than the tab's poll interval, so a single viewer still gets a
//     fresh read on every tick and cannot tell the cache is there; it bites
//     only when readers overlap, which is exactly when it should.
//   - **Single flight.** Requests arriving while a read is in progress wait for
//     that read rather than starting more. A TTL alone does not give this: a
//     cold entry and six simultaneous requests is still six processes, and
//     "everybody reloaded when the deploy went out" is the shape that happens.
//
// What it is not: a watcher. Nothing here notices a commit; it expires and the
// next reader pays for a read. An inotify watch on .git would be a goroutine per
// project, a descriptor budget, and a story about network filesystems, for a
// panel whose answer is allowed to be three seconds old.

// CacheTTL is how long a read stays the answer.
//
// Three seconds against the tab's five. Below the poll interval on purpose: at
// or above it, a lone viewer would see the same numbers twice and the tab would
// look stuck, which is a worse bug than the one being fixed.
const CacheTTL = 3 * time.Second

// cacheIdle is how long an untouched entry is kept before being swept.
//
// Entries are keyed by directory, and directories come from projects and
// sessions, so the map cannot grow without bound in any realistic panel. This
// exists so a machine that has had five hundred worktrees through it over a
// week does not hold five hundred statuses for the life of the process.
const cacheIdle = 5 * time.Minute

// Snapshot is one working tree, read once: everything the git tab's local half
// shows, so that a tab refresh is one cache entry rather than three.
type Snapshot struct {
	Status  Status
	Commits []Commit
	// Remote is only meaningful when HasRemote is true. A repository nobody has
	// pushed yet has no origin, which is an ordinary state and not an error.
	Remote    Remote
	HasRemote bool
}

// Cache holds recent reads of working trees.
//
// The zero value is usable and uses CacheTTL. A Server holds one; tests that
// want to see every read make their own with a zero TTL.
type Cache struct {
	// TTL overrides CacheTTL. Zero means CacheTTL; negative means no caching at
	// all, which is what a test that counts processes wants.
	TTL time.Duration

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

// cacheEntry is one read, in flight or finished.
//
// `done` is closed exactly once, by the goroutine that created the entry, and
// it is what publishes the two fields below: nothing reads them before the
// close, so the channel is the whole of the synchronisation.
type cacheEntry struct {
	done chan struct{}
	at   time.Time
	val  any
	err  error
}

func (c *Cache) ttl() time.Duration {
	if c.TTL == 0 {
		return CacheTTL
	}
	return c.TTL
}

// Read returns everything the git tab's local half shows for one directory.
func (c *Cache) Read(ctx context.Context, dir string, commits int) (Snapshot, error) {
	v, err := c.once(ctx, "read\x00"+dir, func(ctx context.Context) (any, error) {
		st, err := ReadStatus(ctx, dir)
		if err != nil {
			return Snapshot{}, err
		}
		snap := Snapshot{Status: st, Commits: []Commit{}}
		// The log and the remote are best-effort on purpose: a repository with
		// an unborn branch or a broken origin still has a branch and a file
		// list, and those are the two answers the tab is for.
		if log, lerr := ReadLog(ctx, dir, commits); lerr == nil {
			snap.Commits = log
		}
		if remote, ok, rerr := ReadRemote(ctx, dir); rerr == nil && ok {
			snap.Remote = remote
			snap.HasRemote = true
		}
		return snap, nil
	})
	snap, _ := v.(Snapshot)
	return snap, err
}

// Status returns just the working tree's state.
//
// Its own key rather than a projection of Read: the directories this is asked
// about are the ones sessions sit in, and reading a log and a remote for each
// of a dozen worktrees is the cost this file exists to avoid.
func (c *Cache) Status(ctx context.Context, dir string) (Status, error) {
	v, err := c.once(ctx, "status\x00"+dir, func(ctx context.Context) (any, error) {
		return ReadStatus(ctx, dir)
	})
	st, _ := v.(Status)
	return st, err
}

// once runs fn for key, or returns what a recent or in-flight run produced.
func (c *Cache) once(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	ttl := c.ttl()
	if ttl < 0 {
		return fn(ctx)
	}

	now := time.Now()
	c.mu.Lock()
	if e := c.entries[key]; e != nil {
		select {
		case <-e.done:
			if now.Sub(e.at) < ttl {
				c.mu.Unlock()
				return e.val, e.err
			}
		default:
			// In flight. Wait for it rather than starting a second one — but
			// on this caller's context, so a viewer who closed the tab is not
			// held by somebody else's read.
			c.mu.Unlock()
			select {
			case <-e.done:
				return e.val, e.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	e := &cacheEntry{done: make(chan struct{})}
	if c.entries == nil {
		c.entries = make(map[string]*cacheEntry)
	}
	c.sweepLocked(now, ttl)
	c.entries[key] = e
	c.mu.Unlock()

	// WithoutCancel, and this is the subtle half of single flight. The read is
	// shared, so tying it to the first caller's request context means one
	// viewer navigating away cancels the read that five others are waiting on,
	// and they get context.Canceled for a repository that is fine. git's own
	// runTimeout still bounds it, so nothing here can run forever.
	e.val, e.err = fn(context.WithoutCancel(ctx))
	e.at = time.Now()
	// All three fields are published by this close and read only after it has
	// been observed, which is the whole of the synchronisation for them.
	close(e.done)
	return e.val, e.err
}

// sweepLocked drops finished entries nothing has asked for in a while.
func (c *Cache) sweepLocked(now time.Time, ttl time.Duration) {
	for k, e := range c.entries {
		select {
		case <-e.done:
			if now.Sub(e.at) > cacheIdle+ttl {
				delete(c.entries, k)
			}
		default:
		}
	}
}
