package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// Bringing sessions back after the machine restarts.
//
// The premise of this project is that tmux outlives the panel. It does not
// outlive the machine: `reboot` takes the tmux server and every agent in it,
// and what the panel came back to was a sidebar of rows all marked GONE with
// nothing offered but deleting them. The rows held the project, the name, the
// ordering and the directory; they did not hold the one thing needed to run
// anything again, and they held no output at all.
//
// What is honestly restorable, and it is worth being exact because "100%" is
// what people ask for:
//
//   - the session, under the same id, in the same project, with the same
//     name, the same working directory, the same pinned/sorted position, the
//     same notes and todos beside it, and the same tmux name;
//   - the command, re-executed — see store.Session.LaunchCommand for why the
//     column the panel already had could not do this;
//   - the scrollback, as far back as scrollbackLines, put into the new pane's
//     history so it is there to read and to scroll.
//
// What is not restorable, at all, by anything:
//
//   - the process, and therefore the agent's context. An agent that was
//     halfway through a refactor is gone. Re-running its command starts a new
//     one that remembers nothing. There is no mechanism that could do
//     otherwise: the state lived in a process's memory and in a provider's
//     conversation, and neither survived the power going off.
//
// That second list is why the pane gets a banner and the row gets restoredAt.
// A restore that silently starts a fresh agent under an old name, with the old
// agent's output above it, is worse than no restore — somebody reads the screen
// and believes the thing that wrote it is still there.

// scrollbackLines is how much of a pane's history is archived.
//
// tmux keeps 20,000 (history-limit in vibepanel.conf). Capturing all of it is
// affordable once and not on a timer: measured on tmux 3.6 against a full
// history of coloured 130-column output, `-S -` is 2.97 MB and 69 ms per pane,
// which at the two dozen sessions this panel is built for is 71 MB and 1.7
// seconds of tmux every pass. `-S -2000` is 304 KB and 13 ms — 7 MB and 310 ms
// across two dozen — and 2,000 lines is around forty screens, which is well
// past the point where what is on it belongs to a different question than "what
// was this doing when the machine went down".
//
// See tmux.CaptureLines for the whole table.
const scrollbackLines = 2000

// maxScrollbackBytes caps what one session may put in the database, whatever
// its 2,000 lines happen to weigh.
//
// The line bound is not a byte bound: the 304 KB measured above is a worst case
// of long, densely coloured lines, and a pane full of them would exceed this. A
// second cap is what keeps the database's size a function of how many sessions
// exist rather than of what an agent decided to print. 256 KiB across two dozen
// sessions is 6 MB, which is the number this design is willing to spend.
//
// Trimmed from the front, because the end is the part somebody wants.
const maxScrollbackBytes = 256 << 10

// archiveInterval is how often live sessions are captured.
//
// Measured end to end, six sessions each holding a full 20,000-line history of
// coloured output, on this machine: a pass that captures all six takes 40.7 ms
// and stores 150 KB each; a pass where none of them has printed since takes
// 219 µs, because the last_output_at gate skips them before tmux is asked
// anything. Scaled to two dozen that is around 160 ms every thirty seconds at
// full tilt — half a percent of one core — and under a millisecond at idle.
//
// Not on the two-second poll tick. The same work there would be 13 ms of tmux
// per busy session per tick, which is a poller that spends most of its time
// capturing screens nobody asked for.
//
// The exposure is up to thirty seconds of output lost in a *sudden* stop —
// a power cut, an OOM kill of the whole machine. An orderly reboot loses
// nothing: systemd stops the unit first, and Server.ArchiveAll runs on the way
// out.
const archiveInterval = 30 * time.Second

// restoreScript is the pane's first command when a session is rebuilt.
//
// Nothing is interpolated into it. The archive path and the recorded argv
// arrive as positional parameters, so a directory with a quote in it, a
// command with a space in it, or output containing anything at all cannot
// change what this shell runs. That is the whole reason it is written as a
// fixed string with `$@` rather than assembled per session.
//
//	$0  a name for ps
//	$1  the archive file, which holds the old scrollback and the banner
//	$2… the command to run, or nothing for a login shell
//
// The file is deleted as it is read, so a later `respawn-pane` — the restart
// button, which reuses a pane's original command — re-runs the agent without
// replaying somebody's scrollback a second time under a banner that would by
// then be a lie.
//
// `exec` in both branches: the pane's process must be the agent, not a shell
// holding one. Without it every session would carry an extra /bin/sh, the
// per-session CPU meter would report on the wrong tree, and `#{pane_dead}`
// would describe the wrapper.
const restoreScript = `f=$1; shift
if [ -f "$f" ]; then cat -- "$f"; rm -f -- "$f"; fi
if [ "$#" -gt 0 ]; then exec "$@"; fi
exec "${SHELL:-/bin/sh}" -l
`

// restoreBanner is what separates the dead session's output from the live one's.
//
// Bilingual and hard-coded, which is the one place in this project that is
// allowed: web/src/i18n.ts is the browser's dictionary, and this text is
// printed by a shell into a tmux pane by the server, where there is no browser
// and no language preference to read. Saying it in one language would leave
// half the audience reading an old screen without knowing it.
//
// A reset first. The capture carries SGR sequences (`capture-pane -e`), and the
// last line of it can leave the terminal bold, inverted or coloured; without
// the reset the banner — and then the agent's first output — inherits it.
func restoreBanner(capturedAt, restoredAt time.Time, hadScrollback, truncated bool) string {
	const rule = "────────────────────────────────────────────────────────────────────────"
	var b strings.Builder
	b.WriteString("\x1b[0m\r\n")
	b.WriteString(rule + "\r\n")
	if hadScrollback {
		b.WriteString(fmt.Sprintf("  ↑ 以上是 %s 抓取的旧回滚记录（重启前）。\r\n",
			capturedAt.Format("2006-01-02 15:04:05")))
		b.WriteString(fmt.Sprintf("  ↑ scrollback above was captured %s, before the restart.\r\n",
			capturedAt.Format("2006-01-02 15:04:05")))
		if truncated {
			b.WriteString("    更早的部分已被截断 / older lines were cut to keep the archive bounded.\r\n")
		}
	}
	// Bold via SGR, not asterisks. This is a terminal: `**new**` renders as
	// four extra characters, which is what markdown habits do to a pane.
	b.WriteString(fmt.Sprintf("  vibepanel 于 %s 重建了这个会话。下面是\x1b[1m新进程\x1b[0m，它不记得上面的任何内容。\r\n",
		restoredAt.Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("  vibepanel restored this session at %s. The process below is new "+
		"and remembers none of it.\r\n", restoredAt.Format("2006-01-02 15:04:05")))
	b.WriteString(rule + "\r\n")
	return b.String()
}

// Restorable reports whether a row describes a session that could be rebuilt.
//
// Vanished rather than merely exited: `exited` with a real wait status means the
// pane is still there holding the last screen, and the restart button already
// covers that in place. ExitStatusVanished is the tmux session itself being
// gone, which is what a reboot leaves behind.
func Restorable(row store.Session) bool {
	return row.Exited && row.ExitStatus == store.ExitStatusVanished
}

// ─── archiving ────────────────────────────────────────────────────────────

// archiveOnce captures the scrollback of every session that has produced output
// since it was last captured.
//
// The gate is last_output_at, which the PTY pump already maintains and already
// debounces to at most once a second. Without it this would capture and rewrite
// every session's blob every thirty seconds forever: at the cap that is 6 MB of
// database writes per pass on a panel where nothing is happening, which on a
// machine left running for months is the sort of background write nobody
// notices until the disk does.
func (s *Server) archiveOnce(ctx context.Context) {
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		s.Log.Debug("archive: list sessions", "err", err)
		return
	}
	live := make(map[string]bool, len(rows))
	for _, row := range rows {
		live[row.ID] = true
		if _, attached := s.Manager.Get(row.ID); !attached && row.Exited {
			// Nothing to capture from a pane that is gone, and a pane holding
			// a dead process has already been captured with its final screen.
			continue
		}
		s.archMu.Lock()
		if s.archivedOutput == nil {
			// Lazily, because every caller builds this Server as a struct
			// literal and a field they have to remember is a nil map
			// dereference in a background goroutine rather than a compile
			// error.
			s.archivedOutput = map[string]int64{}
		}
		prev, seen := s.archivedOutput[row.ID]
		s.archMu.Unlock()
		if seen && prev == row.LastOutputAt && row.ScrollbackAt > 0 {
			continue
		}
		if err := s.archiveSession(ctx, row); err != nil {
			s.Log.Debug("archive session", "session", row.ID, "err", err)
			continue
		}
		s.archMu.Lock()
		s.archivedOutput[row.ID] = row.LastOutputAt
		s.archMu.Unlock()
	}

	// Same reason Detector.Retain exists: this map is keyed by session id and
	// nothing else would ever remove an entry, so it would grow with every
	// session ever created rather than with the ones that exist.
	s.archMu.Lock()
	for id := range s.archivedOutput {
		if !live[id] {
			delete(s.archivedOutput, id)
		}
	}
	s.archMu.Unlock()
}

// ArchiveAll captures every session unconditionally, ignoring the output gate.
//
// Called on the way down. An orderly reboot stops the panel before it stops
// anything else, so this is the difference between losing up to thirty seconds
// of an agent's last output and losing none of it — which is exactly the output
// somebody will want to read tomorrow, because it is what was on screen when
// the machine went away.
func (s *Server) ArchiveAll(ctx context.Context) {
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		s.Log.Debug("archive all: list sessions", "err", err)
		return
	}
	n := 0
	for _, row := range rows {
		if err := s.archiveSession(ctx, row); err != nil {
			s.Log.Debug("archive session", "session", row.ID, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		s.Log.Info("archived scrollback before shutting down", "sessions", n)
	}
}

func (s *Server) archiveSession(ctx context.Context, row store.Session) error {
	text, err := s.Tmux.CaptureLines(ctx, row.TmuxName, scrollbackLines)
	if err != nil {
		// A session that is not there is not a failure worth recording. It is
		// the normal state of a row the poller has already marked vanished.
		if errors.Is(err, tmux.ErrNoSession) || errors.Is(err, tmux.ErrNoServer) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(text) == "" {
		// A pane that has drawn nothing. Archiving it would replace whatever is
		// already stored — which may be a real screen from before a respawn —
		// with a blank one.
		return nil
	}
	content, truncated := boundScrollback([]byte(text), maxScrollbackBytes)
	return s.DB.PutScrollback(ctx, store.Scrollback{
		SessionID:  row.ID,
		CapturedAt: time.Now().Unix(),
		Lines:      scrollbackLines,
		Truncated:  truncated,
		Content:    content,
	})
}

// boundScrollback cuts a capture down to max bytes, keeping the end.
//
// On a line boundary, so the archive never begins in the middle of an escape
// sequence. A capture sliced at an arbitrary byte can start inside `ESC [ 3 8 ;
// 5 ; 2 0 0 m`, and the tail of that sequence is then printed as literal text
// into the restored pane — the one place where being off by a few bytes
// produces visible garbage rather than a missing line.
func boundScrollback(b []byte, max int) (out []byte, truncated bool) {
	if len(b) <= max {
		return b, false
	}
	cut := b[len(b)-max:]
	if i := bytes.IndexByte(cut, '\n'); i >= 0 && i+1 < len(cut) {
		cut = cut[i+1:]
	}
	return cut, true
}

// ─── restoring ────────────────────────────────────────────────────────────

// ErrSessionIsRunning means there is nothing to restore: tmux still has it.
var ErrSessionIsRunning = errors.New("that session is still running")

// restoreSession rebuilds one session's tmux session from its row.
//
// Everything about the row is kept — the id above all, because notes, todos,
// the sidebar position and any hook already configured with this session's id
// all point at it. Only the process is new.
func (s *Server) restoreSession(ctx context.Context, rec store.Session) error {
	exists, err := s.Tmux.Has(ctx, rec.TmuxName)
	if err != nil {
		return err
	}
	if exists {
		return ErrSessionIsRunning
	}

	dir, err := s.restoreDir(ctx, rec)
	if err != nil {
		return err
	}

	now := time.Now()

	// The archive, plus the banner, in one file the pane cats and deletes.
	//
	// The banner goes in the file rather than being printed by the script so
	// that the timestamps are formatted in Go, where formatting them does not
	// mean interpolating text into a shell command.
	sb, sberr := s.DB.GetScrollback(ctx, rec.ID)
	hadScrollback := sberr == nil && len(sb.Content) > 0
	if sberr != nil && !errors.Is(sberr, store.ErrNotFound) {
		s.Log.Warn("restore: read scrollback", "session", rec.ID, "err", sberr)
	}
	payload := make([]byte, 0, len(sb.Content)+512)
	if hadScrollback {
		payload = append(payload, sb.Content...)
	}
	payload = append(payload,
		restoreBanner(time.Unix(sb.CapturedAt, 0), now, hadScrollback, sb.Truncated)...)

	path, err := s.writeRestoreFile(rec.ID, payload)
	if err != nil {
		return err
	}

	// A recorded argv is run. An unrecorded one — a row from before the panel
	// kept them — falls back to a login shell, and the UI says so before the
	// user presses anything, because a shell started under the name of an agent
	// is a lie the panel would otherwise be telling.
	argv := append([]string{"/bin/sh", "-c", restoreScript, "vibepanel-restore", path},
		rec.LaunchCommand...)

	if err := s.Tmux.Create(ctx, tmux.CreateOptions{
		Name:    rec.TmuxName,
		Dir:     dir,
		Command: argv,
		Env:     s.hookEnv(ctx, rec.ID, rec.ProjectID),
		Width:   rec.Cols,
		Height:  rec.Rows,
	}); err != nil {
		// Nothing will ever read it now, and it holds the session's output.
		_ = os.Remove(path)
		return err
	}

	// Only now. If the create above had failed the archive would be the only
	// copy left of what was on that screen.
	if err := s.DB.DeleteScrollback(ctx, rec.ID); err != nil {
		s.Log.Warn("restore: drop archived scrollback", "session", rec.ID, "err", err)
	}
	if err := s.DB.SetSessionExit(ctx, rec.ID, false, 0); err != nil {
		return err
	}
	if err := s.DB.MarkSessionRestored(ctx, rec.ID, now.Unix()); err != nil {
		return err
	}
	// The evidence the detector holds describes a process that no longer
	// exists. Keeping it would attribute the dead agent's last bell to the new
	// one, which is the same reasoning as the restart path.
	if s.Detector != nil {
		s.Detector.Forget(rec.ID)
	}
	if err := s.DB.SetSessionState(ctx, rec.ID, session.StateWorking, session.SourceHeuristic); err != nil {
		return err
	}
	if _, aerr := s.Manager.Attach(ctx, rec.ID, rec.TmuxName, rec.Cols, rec.Rows); aerr != nil {
		s.Log.Debug("attach restored session", "session", rec.ID, "err", aerr)
	}
	return nil
}

// restoreDir decides where a restored session starts.
//
// The same rule as creating one, and for the same reason: tmux answers `-c` on
// a directory that is not there by silently using $HOME, so a project whose
// worktree was pruned while the machine was off would come back as an agent
// running in somebody's home directory, filed under the project it is not in.
func (s *Server) restoreDir(ctx context.Context, rec store.Session) (string, error) {
	if isDirectory(rec.CWD) {
		return rec.CWD, nil
	}
	p, err := s.DB.GetProject(ctx, rec.ProjectID)
	if err != nil {
		return "", err
	}
	if isDirectory(p.Path) {
		return p.Path, nil
	}
	return "", fmt.Errorf("neither %s nor the project directory %s is there any more",
		rec.CWD, p.Path)
}

// writeRestoreFile puts the payload somewhere the pane's shell can read it.
//
// 0600 in a 0700 directory: it is a verbatim copy of a terminal an agent was
// working in, which on this machine is as sensitive as anything in the
// database.
func (s *Server) writeRestoreFile(sessionID string, payload []byte) (string, error) {
	dir := s.Cfg.RestoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("restore: create %s: %w", dir, err)
	}
	// The id is put in a path, so it is checked before it is. Every id the
	// panel generates is 16 hex characters (internal/id) and could not escape
	// this directory — but "could not" here rests on a package two hops away,
	// and the thing being written is a file chosen by a value that arrived in a
	// request body. A guard at the point of use costs one comparison.
	if !isPlainID(sessionID) {
		return "", fmt.Errorf("restore: refusing to build a path from session id %q", sessionID)
	}
	// The session id, not a random name: a restore that is retried after a
	// failure must not leave the previous attempt's copy behind, and the id is
	// what makes the second write land on the first.
	path := filepath.Join(dir, sessionID+".scrollback")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", fmt.Errorf("restore: write %s: %w", path, err)
	}
	return path, nil
}

// isPlainID reports whether a string is safe to put in a filename.
//
// Letters, digits, dash and underscore. No dot, so no "..", and no separator on
// any platform.
func isPlainID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// RestoreFlagged rebuilds the sessions whose owner asked for it in advance.
//
// Called from Reconcile, which is the only moment this can be right: it runs
// once, at startup, after the panel has learned which tmux sessions are
// actually there. Anything found missing here either went with the machine or
// was killed from a shell, and the flag says what to do about it.
//
// Deliberately not "restore everything": two dozen agents starting at once on
// every boot, each of them beginning to work, is a worse morning than a list of
// dead rows and a button.
func (s *Server) RestoreFlagged(ctx context.Context) {
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		s.Log.Warn("restore on boot: list sessions", "err", err)
		return
	}
	waiting := 0
	restored := 0
	for _, row := range rows {
		if !Restorable(row) {
			continue
		}
		if !row.RestoreOnBoot {
			waiting++
			continue
		}
		if err := s.restoreSession(ctx, row); err != nil {
			s.Log.Warn("restore on boot", "session", row.ID, "err", err)
			continue
		}
		restored++
	}
	if restored > 0 {
		s.Log.Info("restored sessions marked restore-on-boot",
			"sessions", restored,
			"note", "the processes are new; the agents remember nothing")
	}
	if waiting > 0 {
		s.Log.Info("sessions whose tmux session is gone and can be restored",
			"sessions", waiting)
	}
}

// ─── the endpoint ─────────────────────────────────────────────────────────

type restoreSessionsRequest struct {
	// IDs is which sessions to bring back. Explicit, never "all": the panel
	// shows what each one will run and where before anything is pressed, and an
	// endpoint that takes a flag meaning "everything" is one a stale tab can
	// fire at a list it is no longer looking at.
	IDs []string `json:"ids"`
}

type restoreResult struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
	// Error is why this one did not come back. Per session rather than per
	// request: restoring two dozen where one project directory has been deleted
	// must restore twenty-three and name the one it could not.
	Error string `json:"error,omitempty"`
}

func (s *Server) handleRestoreSessions(w http.ResponseWriter, r *http.Request) {
	var req restoreSessionsRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "no session ids given")
		return
	}
	// Detached with a bound, like deleting: restoring two dozen sessions is
	// several seconds of tmux, and a tab closed halfway through must not leave
	// half of them rebuilt and the rest untouched with nothing to say so.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()

	results := make([]restoreResult, 0, len(req.IDs))
	for _, sid := range req.IDs {
		rec, err := s.DB.GetSession(ctx, sid)
		if err != nil {
			results = append(results, restoreResult{ID: sid, Error: err.Error()})
			continue
		}
		if !Restorable(rec) {
			results = append(results, restoreResult{ID: sid,
				Error: "that session's tmux session is still there; nothing to restore"})
			continue
		}
		if err := s.restoreSession(ctx, rec); err != nil {
			results = append(results, restoreResult{ID: sid, Error: err.Error()})
			continue
		}
		results = append(results, restoreResult{ID: sid, OK: true})
	}
	s.notifyState()
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
