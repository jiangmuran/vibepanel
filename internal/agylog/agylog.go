// Package agylog reads the conversation title Antigravity CLI (agy) writes for
// itself, because agy never sets a terminal title.
//
// Measured on agy 1.5.14 across seven live sessions: #{pane_title} stays on
// the machine's hostname and nothing arrives on the client PTY over OSC 0/2,
// plain or passthrough, so every agy row in the sidebar sat under "agy". The
// binary does generate a title a couple of seconds into the first turn -- and
// updates it on /rename -- writing a prototext ConversationAnnotations record
// to <appDataDir>/annotations/<conversation-id>.pbtxt (~40 bytes:
// `title:"..."`), mirrored in conversation_summaries.db.
//
// Two things could go wrong matching a pane to a conversation, and both were
// happening on the machine this was built on:
//
//   - Two agy sessions in the same repository share a working directory and
//     prompt history file, so matching by directory picks one title for both.
//   - Subagents (invoke_subagent) run inside the same agy process, open their
//     own conversations/<subagent-id>.db descriptors, and carry an empty title
//     in conversation_summaries.db; meanwhile an idle conversation closes its
//     conversations/<id>.db descriptor altogether.
//
// What agy holds open for the whole life of the foreground conversation -- and
// never for a subagent -- is <appDataDir>/presence/<conversation-id>.lock.
// /clear and --conversation swap that descriptor as soon as the conversation
// changes. Walking /proc from the pane's PID finds the lock and names both the
// conversation and the appDataDir above it, with no SQLite connection to a WAL
// database agy owns. Where /proc does not exist (macOS), the transcriptPath
// agy's hooks report (<appDataDir>/brain/<id>/.system_generated/logs/...)
// resolves the same annotation file.
package agylog

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Proc is the slice of /proc needed to find an agy process's open lock file.
type Proc interface {
	Children(pid int) []int
	Comm(pid int) string
	FDTargets(pid int) []string
}

// SystemProc reads the host's /proc.
func SystemProc() Proc { return procFS{root: "/proc"} }

type procFS struct{ root string }

func (p procFS) Children(pid int) []int {
	tasks, err := os.ReadDir(filepath.Join(p.root, strconv.Itoa(pid), "task"))
	if err == nil {
		var out []int
		found := false
		for _, t := range tasks {
			b, rerr := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "task", t.Name(), "children"))
			if rerr != nil {
				continue
			}
			found = true
			for _, f := range strings.Fields(string(b)) {
				if n, cerr := strconv.Atoi(f); cerr == nil {
					out = append(out, n)
				}
			}
		}
		if found {
			return out
		}
	}
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		child, cerr := strconv.Atoi(e.Name())
		if cerr != nil {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(p.root, e.Name(), "stat"))
		if rerr != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(s[i+1:])
		if len(fields) >= 2 && fields[1] == strconv.Itoa(pid) {
			out = append(out, child)
		}
	}
	return out
}

func (p procFS) Comm(pid int) string {
	b, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (p procFS) FDTargets(pid int) []string {
	dir := filepath.Join(p.root, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if target, lerr := os.Readlink(filepath.Join(dir, e.Name())); lerr == nil {
			out = append(out, target)
		}
	}
	return out
}

var (
	// presenceLock matches <appDataDir>/presence/<conversation-id>.lock.
	// Anchored at .lock$ so a Linux "(deleted)" suffix on an unlinked lock is
	// ignored, and restricted to identifier bytes so the captured id cannot
	// climb out of annotations/.
	presenceLock = regexp.MustCompile(`^(.+)/presence/([A-Za-z0-9_-]+)\.lock$`)

	// transcriptFile matches <appDataDir>/brain/<conversation-id>/.system_generated/logs/<file>.
	transcriptFile = regexp.MustCompile(`^(.+)/brain/([A-Za-z0-9_-]+)/\.system_generated/logs/[^/]+$`)
)

const (
	maxDepth           = 6
	maxProcs           = 64
	maxAnnotationBytes = 16 << 10
	reresolve          = 5 * time.Second
)

func annotationFromFDs(targets []string) string {
	for _, target := range targets {
		m := presenceLock.FindStringSubmatch(filepath.ToSlash(target))
		if m != nil {
			return filepath.Join(filepath.FromSlash(m[1]), "annotations", m[2]+".pbtxt")
		}
	}
	return ""
}

// FindAnnotation walks the process tree rooted at pid for an agy process.
// agyPID is non-zero whenever an agy process was found in /proc (even before
// it opens a conversation lock); path is the conversation's annotation file, or
// "" when no conversation lock is held.
func FindAnnotation(p Proc, pid int) (agyPID int, path string) {
	if pid <= 0 {
		return 0, ""
	}
	type item struct{ pid, depth int }
	queue := []item{{pid, 0}}
	seen := map[int]bool{}
	for len(queue) > 0 && len(seen) < maxProcs {
		it := queue[0]
		queue = queue[1:]
		if seen[it.pid] {
			continue
		}
		seen[it.pid] = true
		if isAgyComm(p.Comm(it.pid)) {
			if agyPID == 0 {
				agyPID = it.pid
			}
			if annot := annotationFromFDs(p.FDTargets(it.pid)); annot != "" {
				return it.pid, annot
			}
		}
		if it.depth < maxDepth {
			for _, c := range p.Children(it.pid) {
				queue = append(queue, item{c, it.depth + 1})
			}
		}
	}
	return agyPID, ""
}

func isAgyComm(comm string) bool {
	return comm == "agy" || strings.HasPrefix(comm, "antigravity")
}

// AnnotationFromTranscript derives <appDataDir>/annotations/<id>.pbtxt from
// the transcriptPath an agy hook payload carries, or "" if path is not in
// agy's layout.
func AnnotationFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	m := transcriptFile.FindStringSubmatch(filepath.ToSlash(filepath.Clean(transcriptPath)))
	if m == nil {
		return ""
	}
	return filepath.Join(filepath.FromSlash(m[1]), "annotations", m[2]+".pbtxt")
}

// Watcher caches each session's agy process and last-read annotation bytes.
// Safe for concurrent use; meant to be called from the poller.
type Watcher struct {
	Proc Proc

	mu    sync.Mutex
	entry map[string]*watched
}

type watched struct {
	panePID    int
	agyPID     int
	resolvedAt time.Time
	path       string
	raw        []byte
	title      string
}

// Title returns the conversation title for an agy session. fromProc reports
// whether an agy process was visible in /proc: when true, /proc is
// authoritative (even if no conversation is open yet, so title is ""); when
// false (e.g. macOS), the caller may supply transcriptPath from agy's hooks.
func (w *Watcher) Title(sessionID string, panePID int, transcriptPath string, now time.Time) (title string, fromProc bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.entry == nil {
		w.entry = map[string]*watched{}
	}
	e := w.entry[sessionID]
	if e == nil {
		e = &watched{}
		w.entry[sessionID] = e
	}

	var annot string
	if panePID > 0 {
		proc := w.Proc
		if proc == nil {
			proc = SystemProc()
		}
		if e.panePID != panePID {
			e.panePID = panePID
			e.agyPID = 0
			e.resolvedAt = time.Time{}
		}
		// Once the agy process is known, reading its fd directory directly is
		// one ReadDir and catches /clear on the very next poll; only walk the
		// tree when agy has not been found yet or exited.
		if e.agyPID > 0 && isAgyComm(proc.Comm(e.agyPID)) {
			fromProc = true
			annot = annotationFromFDs(proc.FDTargets(e.agyPID))
		} else if e.resolvedAt.IsZero() || now.Sub(e.resolvedAt) >= reresolve {
			agyPID, found := FindAnnotation(proc, panePID)
			e.agyPID = agyPID
			e.resolvedAt = now
			if agyPID > 0 {
				fromProc = true
				annot = found
			}
		} else if e.agyPID > 0 {
			e.agyPID = 0
		}
	}

	if !fromProc && transcriptPath != "" {
		annot = AnnotationFromTranscript(transcriptPath)
	}
	if annot == "" {
		e.path, e.raw, e.title = "", nil, ""
		return "", fromProc
	}
	if annot != e.path {
		e.path, e.raw, e.title = annot, nil, ""
	}
	return readAnnotationFile(e), fromProc
}

func readAnnotationFile(e *watched) string {
	// Non-blocking and no controlling TTY, matching claudelog: the path comes
	// from a user process's directory or hook document and runs on the poller.
	f, err := os.OpenFile(e.path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		e.raw, e.title = nil, ""
		return ""
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		e.raw, e.title = nil, ""
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxAnnotationBytes))
	if err != nil {
		return e.title
	}
	if e.raw != nil && bytes.Equal(raw, e.raw) {
		return e.title
	}
	e.raw = raw
	e.title = ParseTitle(raw)
	return e.title
}

// Retain forgets every session not in ids.
func (w *Watcher) Retain(ids []string) {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.entry {
		if !keep[id] {
			delete(w.entry, id)
		}
	}
}

// ParseTitle extracts the top-level `title` string from a prototext
// ConversationAnnotations document.
//
// Go's google.golang.org/protobuf/encoding/prototext deliberately randomises
// whether a colon is followed by a space (`title:"..."` vs `title: "..."`),
// writes printable UTF-8 runes raw while escaping quotes, backslashes and
// control bytes, and may surround `title` with other scalar or message fields
// such as `side_question_titles: { key: "..." value: "..." }`. Depth tracking
// keeps nested fields out of reach.
func ParseTitle(b []byte) string {
	i, n := 0, len(b)
	depth := 0
	for i < n {
		// Whitespace and optional field separators (; or ,).
		if isSpace(b[i]) || b[i] == ';' || b[i] == ',' {
			i++
			continue
		}
		// Line comment.
		if b[i] == '#' {
			for i < n && b[i] != '\n' {
				i++
			}
			continue
		}
		if b[i] == '{' || b[i] == '<' {
			depth++
			i++
			continue
		}
		if b[i] == '}' || b[i] == '>' {
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if b[i] == '"' || b[i] == '\'' {
			_, next, ok := unquotePrototext(b, i)
			if !ok {
				return ""
			}
			i = next
			continue
		}
		if !isIdentStart(b[i]) {
			i++
			continue
		}
		start := i
		for i < n && isIdentPart(b[i]) {
			i++
		}
		ident := string(b[start:i])
		for i < n && isSpace(b[i]) {
			i++
		}
		if depth == 0 && ident == "title" && i < n && b[i] == ':' {
			i++ // past ':'
			for i < n && isSpace(b[i]) {
				i++
			}
			var combined []byte
			sawQuote := false
			for i < n && (b[i] == '"' || b[i] == '\'') {
				seg, next, ok := unquotePrototext(b, i)
				if !ok {
					return ""
				}
				sawQuote = true
				combined = append(combined, seg...)
				i = next
				for i < n && isSpace(b[i]) {
					i++
				}
			}
			if sawQuote {
				return cleanTitle(string(combined))
			}
			continue
		}
		// Any other field: if followed by ':', step past it; the loop's
		// brace/quote/token cases skip the value without misreading its bytes.
		if i < n && b[i] == ':' {
			i++
		}
	}
	return ""
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// unquotePrototext decodes one single- or double-quoted prototext string
// starting at b[start].
func unquotePrototext(b []byte, start int) ([]byte, int, bool) {
	quote := b[start]
	var out []byte
	for i := start + 1; i < len(b); i++ {
		c := b[i]
		if c == quote {
			return out, i + 1, true
		}
		if c == '\n' || c == 0 {
			return nil, len(b), false
		}
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(b) {
			return nil, len(b), false
		}
		esc := b[i]
		switch esc {
		case 'a':
			out = append(out, '\a')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'v':
			out = append(out, '\v')
		case '\\', '\'', '"', '?':
			out = append(out, esc)
		case 'x', 'X':
			val, consumed, ok := readHex(b[i+1:], 2, false)
			if !ok {
				return nil, len(b), false
			}
			out = append(out, byte(val))
			i += consumed
		case 'u':
			val, consumed, ok := readHex(b[i+1:], 4, true)
			if !ok || !utf8.ValidRune(rune(val)) {
				return nil, len(b), false
			}
			out = utf8.AppendRune(out, rune(val))
			i += consumed
		case 'U':
			val, consumed, ok := readHex(b[i+1:], 8, true)
			if !ok || !utf8.ValidRune(rune(val)) {
				return nil, len(b), false
			}
			out = utf8.AppendRune(out, rune(val))
			i += consumed
		default:
			if esc >= '0' && esc <= '7' {
				val := uint32(esc - '0')
				for k := 0; k < 2 && i+1 < len(b) && b[i+1] >= '0' && b[i+1] <= '7'; k++ {
					i++
					val = val*8 + uint32(b[i]-'0')
				}
				out = append(out, byte(val))
				continue
			}
			out = append(out, esc)
		}
	}
	return nil, len(b), false
}

func readHex(b []byte, digits int, exact bool) (uint32, int, bool) {
	var val uint32
	n := 0
	for n < digits && n < len(b) {
		d, ok := hexVal(b[n])
		if !ok {
			break
		}
		val = val*16 + d
		n++
	}
	if n == 0 || (exact && n != digits) {
		return 0, 0, false
	}
	return val, n, true
}

func hexVal(c byte) (uint32, bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint32(c - '0'), true
	case c >= 'a' && c <= 'f':
		return uint32(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return uint32(c-'A') + 10, true
	}
	return 0, false
}

// cleanTitle strips terminal escapes, control characters and bidi overrides,
// collapsing runs of whitespace so a multi-line annotation renders on one row.
func cleanTitle(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	var out strings.Builder
	rs := []rune(s)
	space := false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b:
			i = skipEscape(rs, i)
			continue
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			if out.Len() > 0 {
				space = true
			}
			continue
		case unicode.IsControl(r) || r == 0xFFFE || r == 0xFFFF || isBidiOverride(r):
			continue
		default:
			if space {
				out.WriteByte(' ')
				space = false
			}
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}

func isBidiOverride(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

func skipEscape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return len(rs)
	}
	switch rs[i+1] {
	case '[':
		for j := i + 2; j < len(rs); j++ {
			if rs[j] >= 0x40 && rs[j] <= 0x7e {
				return j
			}
		}
		return len(rs)
	case ']':
		for j := i + 2; j < len(rs); j++ {
			if rs[j] == 0x07 {
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
		return len(rs)
	}
	return i + 1
}
