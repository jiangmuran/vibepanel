package httpapi

import (
	"context"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Recording what a session used to be doing, without ever being able to stop it
// doing it.
//
// The panel's whole premise is that its idea of what every session is doing
// stays current. `pollOnce` is the loop that maintains it, and it runs against
// every session every two seconds. So the rule for anything hung off a state
// change is the rule fireWebhooks already follows and states: *it may not be on
// that goroutine*. A destination that takes eight seconds to answer, a database
// that has gone to lunch, a disk that is full — none of them may become the
// reason the sidebar stops updating.
//
// fireWebhooks solves it with a goroutine per send. That is right for an
// outbound HTTP request and wrong here: this writes to the same SQLite file the
// poller is writing to, through one write lock, and an unbounded number of
// goroutines contending for it is a worse failure than the one being avoided.
//
// So: a bounded channel and one drain. The producer side is a non-blocking send
// and *nothing else* — no database call, no lock, no allocation that can wait —
// which is what makes "the event log cannot stall a state update" a property of
// the code shape rather than a thing to be careful about. When the channel is
// full the event is dropped and counted, because a lost row in a trend is
// visibly better than a panel that has stopped knowing what is running.
//
// The drain writes in batches, and that is a contention control rather than a
// throughput one -- see eventSettle. One transition per transaction was two
// dozen separate acquisitions of SQLite's single write lock for the burst a
// restart produces, competing with the poller's own writes and with whatever a
// person was saving at the time; a note save came back `database is locked (5)`
// in a browser check that had never produced one before.
//
// The whole of the write path into `session_events` is in this file, and one
// test says so: `s.DB.SetSessionState` appears in this package exactly once,
// here. That is what stops the sixth
// handler that changes a state from being the one that forgets to record it —
// there were five when this was written, and two of them (the hook and the
// manual override) are the paths the poller would never have noticed, because
// by the time it looks the detector already agrees with the row.

// eventQueue is how many transitions may be waiting to be written.
//
// Two dozen sessions all changing state in the same tick is the burst this has
// to swallow without dropping anything, and it is what a restart looks like.
// 256 is an order of magnitude past that, and it is 256 structs of six small
// fields — a few kilobytes held for as long as the drain is behind.
const eventQueue = 256

// eventSweepEvery is how often the log is trimmed back to its retention.
//
// Hourly, and the exact figure does not matter: the retention is 31 days, so
// being an hour late is being 0.1% over. What matters is that it happens at all
// on a panel that is never restarted, which is the case this whole feature is
// for — a wall that has been up for a month.
const eventSweepEvery = time.Hour

// setSessionState writes a session's new state and records the transition.
//
// The only place in this package that calls store.SetSessionState. Every
// handler that changes a state goes through here, so that the recording cannot
// be forgotten by whichever one is written next — see the note at the top of
// this file for why that mattered more than it looks.
//
// `prev` is the row as it was before the write, which every caller already has.
// The comparison is made here rather than in the store because "did this change"
// and "write this" are one question at every call site and the store's own
// no-op elision is about the timestamp, not about the log.
func (s *Server) setSessionState(ctx context.Context, prev store.Session,
	st session.State, src session.Source) error {
	if err := s.DB.SetSessionState(ctx, prev.ID, st, src); err != nil {
		return err
	}
	if st == prev.State {
		return nil
	}
	s.noteTransition(prev, st, time.Now())
	return nil
}

// setSessionStateByID is the same for a handler that holds only an id.
//
// One extra read, on a path a person is waiting on rather than one the poller
// runs twenty-four times a second. The alternative is a second way of writing a
// state that skips the log, which is exactly the hole this file closes.
func (s *Server) setSessionStateByID(ctx context.Context, id string,
	st session.State, src session.Source) error {
	prev, err := s.DB.GetSession(ctx, id)
	if err != nil {
		return err
	}
	return s.setSessionState(ctx, prev, st, src)
}

// noteTransition queues one transition, and cannot block.
//
// Never call this with anything that has to be computed first. Everything it
// needs is on the row the caller was already holding, which is the point: the
// producer side of this is a struct literal and a select with a default.
func (s *Server) noteTransition(prev store.Session, to session.State, now time.Time) {
	ch := s.eventChan()
	dwell := int64(0)
	if prev.StateChangedAt > 0 {
		dwell = now.Unix() - prev.StateChangedAt
	}
	ev := store.SessionEvent{
		At: now.Unix(), SessionID: prev.ID, ProjectID: prev.ProjectID,
		From: prev.State, To: to, ForSeconds: dwell,
	}
	select {
	case ch <- ev:
	default:
		// Dropped, deliberately, and this is the branch the whole file exists
		// to be able to take. A full queue means the drain is behind — a slow
		// disk, a long sweep, a database lock somebody else is holding — and
		// the two options are "lose a row in a chart" and "stop the panel
		// knowing what its sessions are doing". It is not close.
		s.eventsDropped.Add(1)
	}
}

// eventChan returns the queue, making it on first use.
//
// Lazily, so a Server assembled by hand in a test has a working one without
// having to know about it — the same reasoning TreeSampler's zero value follows.
// A nil channel would block forever on send, which is the one failure mode this
// file may not have.
func (s *Server) eventChan() chan store.SessionEvent {
	s.eventsOnce.Do(func() {
		s.events = make(chan store.SessionEvent, eventQueue)
	})
	return s.events
}

// EventsDropped is how many transitions were lost because the queue was full.
//
// Exported so the property can be tested from outside this file rather than by
// reaching into the counter. Deliberately not on the wire and not on the
// settings page: the number is only ever interesting next to a disk that is
// misbehaving, and the stale banner already says that in words somebody can act
// on. A gauge for it would be one more thing to explain.
func (s *Server) EventsDropped() int64 { return s.eventsDropped.Load() }

// drainEvents writes queued transitions and trims the log, until ctx ends.
//
// Started by Poll. One goroutine, not one per event: these all contend for the
// same SQLite write lock, so writing them in order costs nothing and a fan-out
// would turn a slow disk into a thundering herd against the poller's own writes.
func (s *Server) drainEvents(ctx context.Context) {
	ch := s.eventChan()
	every := s.EventSweepEvery
	if every <= 0 {
		every = eventSweepEvery
	}
	sweep := time.NewTicker(every)
	defer sweep.Stop()
	// Once at startup as well as on the ticker. A panel that is restarted every
	// day would otherwise never reach the first tick, and the log would be the
	// one table here that only ever grows.
	s.sweepEvents(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			s.sweepEvents(ctx)
		case ev := <-ch:
			// context.WithoutCancel is deliberately *not* used. This goroutine's
			// context is the server's, so a shutdown should stop writing rather
			// than hold the process open for a row in a chart.
			if err := s.DB.RecordSessionEvents(ctx, gather(ctx, ch, ev)); err != nil &&
				ctx.Err() == nil {
				// Debug, and not through noteStale. A transition that did not
				// reach the log is a gap in a trend; it is not the panel losing
				// track of what the sessions are doing, and the banner that says
				// so must keep meaning that.
				s.Log.Debug("record session events", "err", err)
			}
		}
	}
}

// eventSettle is how long the drain waits for more before writing.
//
// A quarter of a second, and it is a *contention* control rather than a
// throughput one. SQLite takes one write lock for the whole database; the
// poller writes every two seconds and the panel saves notes, scrollback and
// audit rows around it. Writing each transition on its own turned a burst --
// two dozen sessions changing state in the same tick, which is what a restart
// looks like -- into two dozen separate lock acquisitions competing with all of
// that, and it showed up as a note save coming back `database is locked (5)` in
// a browser check that had never produced one before.
//
// Nothing here is latency-sensitive: the wall's next poll is two seconds away
// and the chart is drawn from whole minutes.
const eventSettle = 250 * time.Millisecond

// eventBatch bounds one transaction. Smaller than the queue on purpose: a
// backlog is written as several transactions rather than one long one, so a
// drain that has fallen behind does not hold the write lock while it catches
// up.
const eventBatch = 64

// gather collects everything already queued behind `first`, briefly.
//
// It waits eventSettle for more rather than draining only what is there, so
// that a burst arriving over a few milliseconds is one transaction rather than
// a race between the drain and the producer. It never waits for a batch to
// fill: the timer is the whole of the bound, and the cap is the other.
func gather(ctx context.Context, ch <-chan store.SessionEvent,
	first store.SessionEvent) []store.SessionEvent {
	out := []store.SessionEvent{first}
	settle := time.NewTimer(eventSettle)
	defer settle.Stop()
	for len(out) < eventBatch {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-settle.C:
			return out
		case <-ctx.Done():
			// Written anyway. The context is checked again by the write, which
			// is where a shutdown mid-batch turns into a dropped batch rather
			// than a half-written one.
			return out
		}
	}
	return out
}

// sweepEvents trims the log back to its retention window.
func (s *Server) sweepEvents(ctx context.Context) {
	keep := s.EventKeepDays
	if keep <= 0 {
		keep = store.EventRetentionDays
	}
	before := time.Now().AddDate(0, 0, -keep).Unix()
	n, err := s.DB.SweepSessionEvents(ctx, before)
	if err != nil {
		if ctx.Err() == nil {
			s.Log.Warn("sweep session events", "err", err)
		}
		return
	}
	if n > 0 {
		s.Log.Info("swept session events", "rows", n, "keptDays", keep)
	}
}
