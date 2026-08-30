package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/tz"
)

// TimeZoneKey is the settings row the panel's day boundary is stored in.
//
// Empty means the machine's own zone, which is what it was before this existed
// and stays the default: the machine belongs to the user and they have already
// set its clock once.
const TimeZoneKey = "timezone"

// zone caches the resolved location.
//
// Cached because it is read on every request that names a day -- the token
// panel polls, and a wall polls every two seconds forever -- and a
// LoadLocation per poll is a file read per poll. Invalidated by the handler
// that writes the setting rather than by a TTL, because there is exactly one
// writer and a stale day boundary is not something to discover ten minutes
// later.
type zoneCache struct {
	mu     sync.RWMutex
	loc    *time.Location
	loaded bool
}

func (z *zoneCache) get(ctx context.Context, read func(context.Context) string) *time.Location {
	z.mu.RLock()
	if z.loaded {
		loc := z.loc
		z.mu.RUnlock()
		return loc
	}
	z.mu.RUnlock()

	z.mu.Lock()
	defer z.mu.Unlock()
	if z.loaded {
		return z.loc
	}
	loc, err := tz.Load(read(ctx))
	if err != nil {
		// A stored name that no longer loads is not a reason to refuse to
		// serve. The zone database can lose a name between releases, and the
		// panel falling back to the machine's own clock is a wrong-by-an-hour
		// number rather than a page that will not open.
		loc = time.Local
	}
	z.loc = loc
	z.loaded = true
	return z.loc
}

func (z *zoneCache) forget() {
	z.mu.Lock()
	z.loaded = false
	z.mu.Unlock()
}

// loc is the zone every day boundary in the panel is measured in.
//
// One function, because "which day is this" is asked in four places -- the
// token buckets at ingest, the spend windows at query time, the git activity
// read, and the dashboard -- and four answers is a panel whose "today"
// disagrees with its own chart.
func (s *Server) loc(ctx context.Context) *time.Location {
	return s.zone.get(ctx, func(ctx context.Context) string {
		if s.DB == nil {
			return ""
		}
		name, err := s.DB.GetSetting(ctx, TimeZoneKey, "")
		if err != nil {
			return ""
		}
		return name
	})
}

// nowIn is the current time on the panel's clock.
func (s *Server) nowIn(ctx context.Context) time.Time { return time.Now().In(s.loc(ctx)) }

// today is the day label the usage tables are keyed by.
//
// The same string the ingest writes, produced the same way. These two used to
// be computed independently -- `Scanner.Loc` on one side and a bare
// `time.Now()` on the other -- so setting one without the other named a bucket
// the ingest never writes: the heatmap's last square and the "today" row go
// permanently empty, with no error anywhere.
func (s *Server) today(ctx context.Context) string {
	return dayIn(s.loc(ctx), time.Now())
}

// dayIn is the day label for an instant in a zone.
//
// A function of both rather than of the clock, so it can be tested at an
// instant chosen to fall on different dates in different zones. The version
// that read time.Now() directly could only be tested at whatever time the
// suite happened to run, and for most of the day every zone agrees -- a test
// that passes for sixteen hours and means nothing.
func dayIn(loc *time.Location, at time.Time) string {
	return at.In(loc).Format(dayLayout)
}

// dayLayout is the shape of a day label, and this is the only place it is
// written.
//
// Not a style rule. The label is produced in four files and consumed as a
// string key by every usage query, and the two sides drifted once already --
// the ingest bucketing in a configured zone while the queries formatted
// `time.Now()`. A literal layout anywhere else is a day label built from
// whatever clock happened to be in scope, so
// TestOnlyThisFileNamesTheDayLayout keeps them all coming through here.
const dayLayout = "2006-01-02"

// dayShift is a day label moved by n days, staying on the panel's calendar.
func dayShift(loc *time.Location, at time.Time, n int) string {
	return dayIn(loc, at.AddDate(0, 0, n))
}

// dayParse reads a label back into a time, for the calendar arithmetic that
// works on labels rather than on instants.
func dayParse(label string) (time.Time, error) { return time.Parse(dayLayout, label) }

// startOfDay is midnight this morning on the panel's clock, in unix seconds.
func (s *Server) startOfDay(ctx context.Context) int64 {
	return startOfLocalDay(s.nowIn(ctx))
}
