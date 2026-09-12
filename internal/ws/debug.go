package ws

import "os"

// debugTiming logs how long each subscribe took, when VIBEPANEL_DEBUG_TIMING
// is set.
//
// The timings arrived with the change that made a cold session appear on a
// phone -- attach, first replay chunk, the whole replay -- and they were what
// showed where the wait actually was. They were logged at Info, on every
// subscribe, which is a diagnostic left switched on after the thing it was
// diagnosing was fixed.
//
// That matters more here than it looks. `main.go` builds the logger at
// slog.LevelInfo with nothing to change it, so there is no level to turn down;
// and terminals now stay mounted, so a subscribe is no longer something that
// happens when somebody switches session -- it happens for every mounted
// terminal on every page load, and again for all of them on every reconnect. A
// phone on a flaky link writes that line over and over, into a journal, for a
// question nobody is asking any more.
//
// An environment gate rather than slog.LevelDebug, because Debug with a
// hardcoded Info handler is not "off by default", it is unreachable without
// editing the source -- and the way to ask this panel for diagnostics already
// exists: VIBEPANEL_DEBUG_CHUNKS dumps PTY chunks the same way.
// internal/config already exempts the whole VIBEPANEL_DEBUG_ prefix from the
// "nothing reads this variable" warning, so this needs registering nowhere.
var debugTiming = os.Getenv("VIBEPANEL_DEBUG_TIMING") != ""
