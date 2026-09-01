package usage

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// MinInterval is how long a pass's answer is treated as current.
//
// A pass is cheap when nothing changed and not free when something did.
// Measured on the machine this was written on, 568 transcripts totalling
// 2.16 GB: the first pass is 3.09 s, a pass where nothing has been written
// since is 35 ms, and a pass where one 395 MB transcript has been appended to
// is 539 ms. Thirty seconds is slower than the agents write and fast enough
// that a panel left open is never more than half a minute behind — and the
// browser asks for this only while the tab is on screen, so an unwatched panel
// does nothing at all.
const MinInterval = 30 * time.Second

// sessionLimit bounds the per-session table an API response carries.
const sessionLimit = 200

// Pass is what the last ingest run found.
type Pass struct {
	// At is zero until a pass has completed. That is the difference the UI has
	// to render: "no pass has finished yet" is not "no usage", and a zero
	// shown for the first is the failure this whole feature is built to avoid.
	At       time.Time
	Duration time.Duration
	Sources  []Source
	// Read is how many transcripts were opened; Seen is how many exist. The
	// gap between them is what the (size, mtime) cursor saved.
	Read int
	Seen int
	Err  string
}

// Ingester keeps the database in step with the transcripts on disk.
//
// One at a time, ever. Two concurrent passes would both re-read the same
// changed files and race to replace the same rows, which is not incorrect —
// each write is a whole file inside a transaction — but is pure waste, and the
// waste is measured in seconds of disk.
type Ingester struct {
	Scanner *Scanner
	DB      *store.DB
	Log     *slog.Logger

	mu      sync.Mutex
	running bool
	last    Pass
}

// Status reports the last completed pass and whether one is under way.
func (in *Ingester) Status() (Pass, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.last, in.running
}

// Ensure starts a pass in the background if the last answer is stale.
//
// Deliberately not synchronous. The first pass on a machine with a year of
// history is seconds of disk, and a request that blocked on it would look like
// a hung panel; worse, a browser that retried would queue a second one. The
// caller renders whatever the database already holds and says a pass is
// running, which is both true and the only honest thing to show.
//
// The pass runs on a background context rather than the request's, so that a
// browser navigating away does not abandon a half-finished ingest and leave
// some transcripts counted and others not.
//
// Returns whether it started one.
func (in *Ingester) Ensure(force bool) bool {
	in.mu.Lock()
	if in.running {
		in.mu.Unlock()
		return false
	}
	if !force && !in.last.At.IsZero() && time.Since(in.last.At) < MinInterval {
		in.mu.Unlock()
		return false
	}
	in.running = true
	in.mu.Unlock()

	go func() {
		p := in.run(context.Background())
		in.mu.Lock()
		in.last = p
		in.running = false
		in.mu.Unlock()
	}()
	return true
}

// Rezone changes the zone the day labels are written in and re-reads every
// transcript under the new one. It returns how many cursor rows it dropped.
//
// The zone and the cursor are one change, and neither may move while a pass is
// walking -- which is why this waits rather than taking an argument to Ensure.
// The settings handler used to do both from the request goroutine. Writing
// Scanner.Loc there is an unsynchronised write to a location a running pass
// reads for every record it buckets, and the race detector says so. Dropping
// the cursor there is worse and outlives the race: a pass that started before
// the delete is holding a snapshot of the cursor taken before it, so it goes
// on writing rows in the old zone *together with* the cursors that make them
// look current -- and every later pass then skips exactly those transcripts.
// Their labels stay in the zone the user just left, permanently, while the
// response reports the rebuild as done.
//
// The wait is honest rather than unfortunate: somebody changed a setting once,
// and a pass is seconds at worst.
func (in *Ingester) Rezone(ctx context.Context, loc *time.Location) (int64, error) {
	in.mu.Lock()
	for in.running {
		in.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		in.mu.Lock()
	}
	// Held across the wipe so that no pass can start on half of it. It is also
	// what orders the write below against every pass that comes after: they
	// are all started under this mutex, so there is a happens-before edge and
	// no pass ever reads the zone while it is being changed.
	in.running = true
	if in.Scanner != nil {
		in.Scanner.Loc = loc
	}
	in.mu.Unlock()

	n, err := in.DB.ForgetEveryUsageFile(ctx)

	in.mu.Lock()
	in.running = false
	in.mu.Unlock()
	if err != nil {
		return 0, err
	}
	in.Ensure(true)
	return n, nil
}

// run performs one pass and returns what it found.
func (in *Ingester) run(ctx context.Context) Pass {
	started := time.Now()
	p := Pass{At: started}

	stamps, err := in.DB.UsageStamps(ctx)
	if err != nil {
		p.Err = err.Error()
		p.At = time.Now()
		p.Duration = time.Since(started)
		return p
	}

	// Everything the last pass knew about, and which agent's root it was found
	// under. Whatever is still in here when the walk finishes is a transcript
	// that has been deleted, and its rows go too -- but only for a root this
	// pass actually managed to enumerate.
	gone := make(map[string]string, len(stamps))
	for path, s := range stamps {
		gone[path] = s.Tool
	}
	enumerated := make(map[string]bool, len(Tools))

	for _, tool := range Tools {
		paths, src, err := in.Scanner.Walk(tool)
		if err != nil {
			src.Problem = err.Error()
		}
		enumerated[string(tool)] = src.Complete
		for _, ref := range paths {
			path := ref.Path
			delete(gone, path)
			p.Seen++
			// The cursor, and it has to be consulted *before* the file is
			// opened. Checking it against what ReadFile stat'd would mean
			// every pass read all 2.16 GB and then decided most of it had not
			// changed, which is the incremental design costing exactly as much
			// as no incremental design at all.
			if s, ok := stamps[path]; ok && s.Size == ref.Size && s.ModifiedAt == ref.ModifiedAt {
				continue
			}
			f := in.Scanner.ReadFile(tool, path)
			p.Read++
			src.Skipped += f.Skipped
			rows := make([]store.UsageRow, 0, len(f.Buckets))
			for _, b := range f.Buckets {
				rows = append(rows, store.UsageRow{
					Day: b.Day, Tool: string(tool), Session: b.Session, CWD: b.CWD,
					Model: b.Model, Input: b.Input, Output: b.Output,
					CacheRead: b.CacheRead, CacheWrite: b.CacheWrite, Requests: b.Requests,
				})
			}
			if err := in.DB.ReplaceUsageFile(ctx, store.UsageFile{
				Path: path, Tool: string(tool), Size: f.Size, ModifiedAt: f.ModifiedAt,
				Skipped: f.Skipped, Problem: f.Problem, Rows: rows,
			}); err != nil {
				// One unreadable transcript is a gap, not a reason to abandon
				// the other four hundred.
				p.Err = err.Error()
				if in.Log != nil {
					in.Log.Warn("usage ingest failed for one transcript", "path", path, "err", err)
				}
			}
		}
		p.Sources = append(p.Sources, src)
	}

	if len(gone) > 0 {
		paths := make([]string, 0, len(gone))
		for path, tool := range gone {
			// "I could not look" is not "the files are gone". A walk that did
			// not enumerate its root returns an empty list, and sweeping on
			// that deletes the cursor and every rolled-up day for the agent in
			// one transaction -- so a remount, an agent updating itself or a
			// chmod on one subdirectory would have the panel reporting zero
			// and re-reading the whole corpus from cold afterwards. Unknown is
			// not zero, here as everywhere else in this package.
			if !enumerated[tool] {
				continue
			}
			paths = append(paths, path)
		}
		sort.Strings(paths)
		if err := in.DB.ForgetUsageFiles(ctx, paths); err != nil && p.Err == "" {
			p.Err = err.Error()
		}
	}

	p.At = time.Now()
	p.Duration = time.Since(started)
	return p
}

// RunNow performs a pass on the calling goroutine. Only for the admin CLI and
// for tests, which want the numbers to be there when the call returns rather
// than eventually.
func (in *Ingester) RunNow(ctx context.Context) Pass {
	in.mu.Lock()
	for in.running {
		in.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		in.mu.Lock()
	}
	in.running = true
	in.mu.Unlock()

	p := in.run(ctx)

	in.mu.Lock()
	in.last = p
	in.running = false
	in.mu.Unlock()
	return p
}

// SessionLimit is how many agent sessions an API response carries.
func SessionLimit() int { return sessionLimit }
