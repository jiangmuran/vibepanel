package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// The two things a share dashboard needs that a single reading cannot give it:
// who else is looking, and what the last few minutes looked like.
//
// Both live in this process's memory and neither is a row. That is the whole
// design decision, and it is worth stating before the code.
//
// **Neither is a write route.** registerShareRoutes still mounts exactly one
// GET below requireShareToken — red line 8 — and nothing here changes that. A
// viewer does not POST its presence; the poll it was already making is the
// signal, and the three query parameters it may carry (`v`, `w`, `h`) are
// recorded here and are never read back by anything the share token can reach.
// A caller cannot change what the dashboard says by changing them, and a test
// pins that: two polls with different parameters produce the same payload.
//
// **Neither is a column.** A wall polls every two seconds and never stops, so a
// "viewers" column would be tens of thousands of writes a day through SQLite's
// one write lock for a number that is only true for the next two seconds —
// which is the same arithmetic that made TouchShareLink rate-limited. And a
// count in a table survives a restart, which is exactly wrong: after
// `systemctl restart vibepanel` the truthful answer is "nobody, until somebody
// polls again", and a stored one would say four.
//
// **A connection that dies silently needs no cleanup.** There is nothing held
// open to notice dying. A viewer that has been unplugged simply stops
// refreshing its entry and ages out within shareViewerTTL. This is the direct
// benefit of polling over a socket: a socket needs a close handler that fires
// reliably, and the one that does not fire is the one that leaves a phantom
// viewer on the owner's screen forever.

// shareViewerTTL is how long after its last poll a viewer still counts.
//
// The dashboard polls every two seconds, so this is roughly seven missed polls.
// Long enough that a wifi hiccup does not make the owner's count flicker while
// they are watching it, short enough that a television somebody switched off
// stops being counted before they have finished walking back to their desk.
const shareViewerTTL = 15 * time.Second

// shareViewersPerLink bounds how many viewers one link tracks.
//
// Not a defence — whoever holds the link can open sixty-four tabs as easily as
// they can send sixty-four ids — but the bound that stops one link's map from
// growing without limit. Over the cap the oldest entry is dropped, so the count
// saturates rather than lying upwards without end.
const shareViewersPerLink = 64

// shareViewerIDMax is how long a viewer's own id may be.
//
// It is an opaque key and nothing more: it is hashed under the link's secret
// before it is stored, so what is kept is fixed-width whatever arrives. The
// bound is on the work done before the hash.
const shareViewerIDMax = 64

// shareViewport is the largest sensible screen, in pixels, either way.
//
// A bound rather than a validation: the number is shown to the owner so they
// know what shape of screen they are composing for, and "8192" from a browser
// that has been told to lie is a number on a settings row rather than anything
// the panel acts on. Zero means the viewer did not say.
const shareViewportMax = 16384

type shareViewer struct {
	at time.Time
	w  int
	h  int
}

// shareViewerBook is who is looking at what, right now.
type shareViewerBook struct {
	mu sync.Mutex
	// by link id, then by the viewer's hashed key.
	links map[string]map[string]shareViewer
}

// shareLinksTracked bounds how many links have a viewer map at once.
//
// Links are made by an authenticated owner, so this is not a bound against an
// attacker; it is the bound that stops a panel with two hundred links keeping
// two hundred maps alive. Over the cap the whole thing is dropped rather than
// evicted cleverly: the cost of being wrong is that every wall is uncounted for
// fifteen seconds, and an LRU here would be more machinery than it manages.
const shareLinksTracked = 256

// saw records that one viewer of one link asked just now.
func (b *shareViewerBook) saw(linkID, viewerKey string, w, h int, now time.Time) {
	if linkID == "" || viewerKey == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.links == nil || len(b.links) > shareLinksTracked {
		b.links = map[string]map[string]shareViewer{}
	}
	seen := b.links[linkID]
	if seen == nil {
		seen = map[string]shareViewer{}
		b.links[linkID] = seen
	}
	prune(seen, now)
	if _, already := seen[viewerKey]; !already && len(seen) >= shareViewersPerLink {
		dropOldest(seen)
	}
	seen[viewerKey] = shareViewer{at: now, w: w, h: h}
}

// count reports how many viewers one link has, and the largest screen among
// them.
//
// The largest rather than the most recent: the owner is composing for a screen
// they cannot see, and if two are on the link — a television and the phone they
// checked it from — the television is the one the board is for.
func (b *shareViewerBook) count(linkID string, now time.Time) (n, w, h int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := b.links[linkID]
	if seen == nil {
		return 0, 0, 0
	}
	prune(seen, now)
	if len(seen) == 0 {
		// Kept rather than deleted: the map is one allocation and the link is
		// about to be polled again. forget() is what removes one for good.
		return 0, 0, 0
	}
	for _, v := range seen {
		if v.w*v.h > w*h {
			w, h = v.w, v.h
		}
	}
	return len(seen), w, h
}

// forget drops a link's viewers, for a link that has just been revoked.
func (b *shareViewerBook) forget(linkID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.links, linkID)
}

func prune(seen map[string]shareViewer, now time.Time) {
	for k, v := range seen {
		if now.Sub(v.at) > shareViewerTTL {
			delete(seen, k)
		}
	}
}

func dropOldest(seen map[string]shareViewer) {
	oldest, at := "", time.Time{}
	for k, v := range seen {
		if at.IsZero() || v.at.Before(at) {
			oldest, at = k, v.at
		}
	}
	delete(seen, oldest)
}

// shareViewerFrom reads the three things a viewer may say about itself.
//
// All three are optional and none of them decides anything the response
// carries. An absent or malformed id falls back to the caller's address, so a
// viewer running an older build, or a browser with storage switched off, still
// counts as one screen rather than as none.
func shareViewerFrom(r *http.Request, fallback string) (raw string, w, h int) {
	q := r.URL.Query()
	raw = q.Get("v")
	if len(raw) == 0 || len(raw) > shareViewerIDMax || !isHex(raw) {
		// "ip:" rather than the bare address, so a viewer that sent the literal
		// string of somebody's address cannot collide with that address.
		raw = "ip:" + fallback
	}
	return raw, viewportNumber(q.Get("w")), viewportNumber(q.Get("h"))
}

func viewportNumber(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > shareViewportMax {
		return 0
	}
	return n
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ─── the last few minutes ─────────────────────────────────────────────────

// trendSampleEvery is how often a point is added to the ring.
//
// Ten seconds: fine enough that a line drawn from it moves while somebody
// watches, coarse enough that a fifteen-minute window is ninety points rather
// than four hundred and fifty, which is a chart nobody can see the shape of and
// a payload that is mostly timestamps.
const trendSampleEvery = 10 * time.Second

// trendPoints is how many are kept: fifteen minutes at the cadence above.
//
// Fifteen because that is about as far back as "is it busy right now" reaches.
// An hour of history is a different question and the day series already answers
// it; a wall wants to see the last few minutes move.
const trendPoints = 90

// trendRing is the machine and the token total, sampled and kept in a circle.
//
// In memory and never a table, and the reason is not only cost. A restart is
// supposed to lose this: the honest line after `systemctl restart vibepanel` is
// a short one that starts now, not a long one with a hole in it that renders as
// a cliff nobody can explain.
//
// It is filled by the handler that draws it rather than by the poller. A panel
// nobody is watching does no work for a graph nobody is looking at — the same
// rule the token ingester already follows, and the reason a wall that has just
// been switched on draws a short line for its first minutes.
type trendRing struct {
	points []trendPoint
	last   time.Time
}

type trendPoint struct {
	at int64
	// cpu is whole-machine percent, or -1 where /proc could not be read. A
	// separate value from zero, which is a real and different reading.
	cpu float64
	// memory and swap are the fraction in use, 0..1.
	memory float64
	load   float64
	// tokens is the running total for the day, within this link's scope. The
	// widget draws the differences; the total is what survives a sample being
	// dropped, and a difference would not.
	tokens int64
}

// observe adds a point if enough time has passed since the last one.
func (t *trendRing) observe(now time.Time, p trendPoint) {
	if !t.last.IsZero() && now.Sub(t.last) < trendSampleEvery {
		return
	}
	t.last = now
	t.points = append(t.points, p)
	if len(t.points) > trendPoints {
		t.points = t.points[len(t.points)-trendPoints:]
	}
}

// trendFrom builds the point one reading describes.
func trendFrom(now time.Time, sample sysmon.Sample, tokens int64) trendPoint {
	p := trendPoint{at: now.Unix(), cpu: -1, load: sample.Load1, tokens: tokens}
	if sample.CPUReadable && sample.CPUPercent != nil {
		p.cpu = *sample.CPUPercent
	}
	if sample.MemTotal > 0 {
		p.memory = float64(sample.MemTotal-sample.MemAvailable) / float64(sample.MemTotal)
	}
	return p
}

// observeTrend records a point for one scope and returns that scope's ring.
//
// Keyed by the scope's directory, exactly as the spend cache is, because the
// token half of a point is that scope's total: a link scoped to one project
// must not draw the panel's line. The machine half is the same for every scope
// and is duplicated rather than shared, which is four floats per scope and not
// worth a second structure to avoid.
func (s *Server) observeTrend(key string, now time.Time, p trendPoint) []trendPoint {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	if s.trendRings == nil || len(s.trendRings) > shareSpendCacheMax {
		s.trendRings = map[string]*trendRing{}
	}
	ring := s.trendRings[key]
	if ring == nil {
		ring = &trendRing{}
		s.trendRings[key] = ring
	}
	ring.observe(now, p)
	out := make([]trendPoint, len(ring.points))
	copy(out, ring.points)
	return out
}
