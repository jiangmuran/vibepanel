package httpapi

import (
	"context"
	"slices"
	"time"
)

// Whether a pane has a full-screen program drawing in it, asked sooner than
// the poller asks.
//
// The browser decides who owns the wheel from this flag (Terminal.tsx): a
// full-screen program that did not ask for the mouse gets arrow keys, a shell
// gets its own scrollback scrolled. The poller asks tmux every two seconds, so
// for up to two seconds after `less` or `vim` exits the wheel still sent
// arrows, and a shell answers Up by putting the previous command on its prompt
// line.
//
// The browser cannot tell by itself, which is the first thing anybody reaches
// for: vibepanel.conf removes smcup/rmcup from the client's terminal, so a
// pane's switch to its alternate screen is emulated inside tmux and never
// reaches the stream the browser parses. xterm's own buffer is `normal` under
// every program, always. docs/build-log.md has the measurement.
//
// What the panel does see is output: entering or leaving a full-screen program
// redraws the pane, and the redraw comes through the pump. So a burst of output
// on a session somebody is looking at schedules one `display-message` a short
// while later, and a changed answer is pushed at once. Bounded per session --
// at most one probe in flight and one per fsProbeGap -- because a program
// repainting continuously is a burst that never ends, and only for watched
// sessions, because a session nobody is looking at has no wheel to route.
// The poller still runs as before and is what corrects a probe that missed.

const (
	// fsProbeDelay lets the burst finish: a program leaving its alternate
	// screen writes the switch and the redraw together, and asking in the
	// middle of it can see the old screen.
	fsProbeDelay = 100 * time.Millisecond
	// fsProbeGap is the most often one session is probed: four a second.
	fsProbeGap = 250 * time.Millisecond
	// fsProbeBudget bounds one probe; a tmux that does not answer leaves the
	// flag as the poller last saw it.
	fsProbeBudget = 2 * time.Second
)

// fsProbeState is one session's probe bookkeeping, guarded by Server.fsMu.
type fsProbeState struct {
	pending bool      // a probe is scheduled or running
	lastRun time.Time // when the last probe asked tmux
	seen    bool      // at and on hold an answer
	at      time.Time // when that answer was asked for
	on      bool
}

// kickFullscreenProbe is called from the pump goroutine for every chunk, so it
// does no I/O: it schedules, or returns.
func (s *Server) kickFullscreenProbe(sessionID string, now time.Time) {
	if s.Tmux == nil || s.Manager == nil {
		return
	}
	l, ok := s.Manager.Get(sessionID)
	if !ok || !l.Watched() {
		return
	}
	s.fsMu.Lock()
	if s.fsProbe == nil {
		s.fsProbe = map[string]*fsProbeState{}
	}
	st := s.fsProbe[sessionID]
	if st == nil {
		st = &fsProbeState{}
		s.fsProbe[sessionID] = st
	}
	if st.pending {
		s.fsMu.Unlock()
		return
	}
	st.pending = true
	delay := fsProbeDelay
	if wait := st.lastRun.Add(fsProbeGap).Sub(now); wait > delay {
		delay = wait
	}
	s.fsMu.Unlock()
	name := l.TmuxName
	time.AfterFunc(delay, func() { s.runFullscreenProbe(sessionID, name) })
}

func (s *Server) runFullscreenProbe(sessionID, tmuxName string) {
	ctx, cancel := context.WithTimeout(context.Background(), fsProbeBudget)
	defer cancel()
	asked := time.Now()
	on, err := s.Tmux.AlternateScreen(ctx, tmuxName)

	s.fsMu.Lock()
	if s.fsProbe == nil {
		s.fsProbe = map[string]*fsProbeState{}
	}
	st := s.fsProbe[sessionID]
	if st == nil {
		st = &fsProbeState{}
		s.fsProbe[sessionID] = st
	}
	st.pending = false
	st.lastRun = asked
	s.fsAsked++
	if err != nil {
		s.fsMu.Unlock()
		return
	}
	st.seen, st.at, st.on = true, asked, on
	cur := fullscreenNow(&s.fullscreen)
	changed := slices.Contains(cur, sessionID) != on
	if changed {
		next := slices.DeleteFunc(slices.Clone(cur), func(id string) bool { return id == sessionID })
		if on {
			next = append(next, sessionID)
		}
		s.fullscreen.Store(&next)
	}
	s.fsMu.Unlock()
	if changed {
		s.notifyState()
	}
}

// storeFullscreen publishes the poller's answer, listed at listedAt.
//
// A probe that asked after the poller listed is newer for its session and
// wins: otherwise a poll that started just before `vim` exited would put the
// flag back on for two seconds, which is the window the probe exists to close.
func (s *Server) storeFullscreen(polled []string, listedAt time.Time) {
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	next := slices.Clone(polled)
	for id, st := range s.fsProbe {
		if st.seen && st.at.After(listedAt) {
			has := slices.Contains(next, id)
			if st.on && !has {
				next = append(next, id)
			} else if !st.on && has {
				next = slices.DeleteFunc(next, func(x string) bool { return x == id })
			}
			continue
		}
		// Older than this list, so the list has superseded it; dropped here
		// so the map is bounded by the sessions being probed right now.
		if !st.pending {
			delete(s.fsProbe, id)
		}
	}
	s.fullscreen.Store(&next)
}
