package agylog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeProc struct {
	children map[int][]int
	comm     map[int]string
	fds      map[int][]string
}

func (f fakeProc) Children(pid int) []int     { return f.children[pid] }
func (f fakeProc) Comm(pid int) string        { return f.comm[pid] }
func (f fakeProc) FDTargets(pid int) []string { return f.fds[pid] }

func TestParseTitleReadsPrototextVariants(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		// Exact byte shapes captured from ~/.gemini/antigravity-cli/annotations/*.pbtxt.
		{"compact ASCII with apostrophe",
			`title:"Master's Thesis Annual Report Reading Analysis"`,
			"Master's Thesis Annual Report Reading Analysis"},
		{"compact raw UTF-8 CJK",
			`title:"简历与尽调材料整理"`,
			"简历与尽调材料整理"},
		// Go's prototext randomises `:` vs `: ` across marshals.
		{"space after colon",
			`title: "Fix Antigravity Auto-Rename Issue"`,
			"Fix Antigravity Auto-Rename Issue"},
		{"escaped quotes, backslashes and octal UTF-8",
			`title: "Fix \"allow-passthrough\" in C:\\path \347\256\200"`,
			`Fix "allow-passthrough" in C:\path 简`},
		{"unicode and hex escapes",
			`title:"\u7b80\U00005386\x21"`,
			"简历!"},
		{"adjacent string literals concatenated",
			`title: "part one: " "part two"`,
			"part one: part two"},
		{"nested fields and comments do not shadow top-level title", `
# title:"commented out"
starred: true
last_user_view_time: {
  seconds: 1795351135
}
side_question_titles {
  key: "q1"
  value: "nested title:\"wrong\""
}
other_message <
  title: "inside angle brackets"
>
group_title: "not the field"
title: "the real conversation"
pinned: false
`, "the real conversation"},
		{"escapes and bidi overrides are stripped",
			"title:\"hello\\n\\t\x1b[31mworld\x1b[0m\\u202eevil\"",
			"hello worldevil"},
		{"empty title",
			`title:""`,
			""},
		{"no title field",
			`starred: true pinned: false`,
			""},
		{"unterminated string",
			`title:"cut off`,
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseTitle([]byte(tc.raw)); got != tc.want {
				t.Errorf("ParseTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindAnnotationFollowsPresenceLockNotSubagentDB(t *testing.T) {
	// Shaped from live /proc/<pid>/fd on agy 1.5.14: while a subagent runs,
	// conversations/<subagent>.db is open in the same process and its summary
	// has no title; presence/<root>.lock stays on the user's conversation.
	p := fakeProc{
		children: map[int][]int{10: {20}, 20: {21}},
		comm:     map[int]string{10: "bash", 20: "agy", 21: "node", 99: "agy"},
		fds: map[int][]string{
			20: {
				"/dev/pts/3",
				"/home/u/.gemini/antigravity-cli/conversations/9bb1efb7-4cdc-40c9-9e7e-eb44c25bb51f.db",
				"/home/u/.gemini/antigravity-cli/presence/deadbeef.lock (deleted)",
				"/home/u/.gemini/antigravity-cli/presence/17072632-7c5a-4f63-aa77-dde55977b126.lock",
			},
			99: {
				"/home/u/.gemini/antigravity-cli/presence/other-session.lock",
			},
		},
	}
	pid, path := FindAnnotation(p, 10)
	if pid != 20 {
		t.Fatalf("agyPID = %d, want 20", pid)
	}
	want := filepath.FromSlash("/home/u/.gemini/antigravity-cli/annotations/17072632-7c5a-4f63-aa77-dde55977b126.pbtxt")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	// An agy sitting on the welcome screen has no lock yet, and still reports
	// agyPID > 0 so Linux does not fall back to a stale hook transcript.
	pNoLock := fakeProc{
		comm: map[int]string{30: "agy"},
		fds:  map[int][]string{30: {"/dev/pts/4"}},
	}
	pid, path = FindAnnotation(pNoLock, 30)
	if pid != 30 || path != "" {
		t.Fatalf("welcome screen: pid=%d path=%q, want 30 and empty", pid, path)
	}
}

func TestWatcherTracksTitleChangesAndConversationSwitches(t *testing.T) {
	appDir := t.TempDir()
	annotDir := filepath.Join(appDir, "annotations")
	if err := os.MkdirAll(annotDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fds := map[int][]string{
		42: {filepath.Join(appDir, "presence", "conv-1.lock")},
	}
	w := Watcher{
		Proc: fakeProc{
			comm: map[int]string{42: "agy"},
			fds:  fds,
		},
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	// Before agy generates a title, no .pbtxt exists yet.
	if got, fromProc := w.Title("s1", 42, "", now); got != "" || !fromProc {
		t.Fatalf("before title: got=%q fromProc=%v, want empty and true", got, fromProc)
	}

	// First prompt: agy writes annotations/conv-1.pbtxt.
	conv1 := filepath.Join(annotDir, "conv-1.pbtxt")
	if err := os.WriteFile(conv1, []byte(`title:"Fix Antigravity Auto-Rename Issue"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Title("s1", 42, "", now.Add(2*time.Second)); got != "Fix Antigravity Auto-Rename Issue" {
		t.Fatalf("first title = %q", got)
	}

	// /rename to a same-length string with no clock advance: byte comparison
	// catches what an mtime+size check on a coarse filesystem would miss.
	if err := os.WriteFile(conv1, []byte(`title:"Fix Antigravity Sidebar Renaming!"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Title("s1", 42, "", now.Add(4*time.Second)); got != "Fix Antigravity Sidebar Renaming!" {
		t.Fatalf("renamed title = %q", got)
	}

	// /clear starts conv-2: the cached agyPID re-reads fd targets immediately
	// rather than waiting out reresolve, and the old title does not linger
	// while conv-2 has no .pbtxt yet.
	fds[42] = []string{filepath.Join(appDir, "presence", "conv-2.lock")}
	if got, _ := w.Title("s1", 42, "", now.Add(5*time.Second)); got != "" {
		t.Fatalf("after /clear before prompt = %q, want empty", got)
	}
	conv2 := filepath.Join(annotDir, "conv-2.pbtxt")
	if err := os.WriteFile(conv2, []byte(`title: "Three.js 游戏重做调研与方案"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Title("s1", 42, "", now.Add(6*time.Second)); got != "Three.js 游戏重做调研与方案" {
		t.Fatalf("conv-2 title = %q", got)
	}

	w.Retain(nil)
	if len(w.entry) != 0 {
		t.Fatalf("Retain left %d entries", len(w.entry))
	}
}

func TestHookTranscriptFallbackWorksWithoutProc(t *testing.T) {
	appDir := t.TempDir()
	annotDir := filepath.Join(appDir, "annotations")
	if err := os.MkdirAll(annotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const cid = "461fc59a-af39-44c7-a5c6-0a65669b6570"
	if err := os.WriteFile(filepath.Join(annotDir, cid+".pbtxt"), []byte(`title:"macOS hook path"`), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(appDir, "brain", cid, ".system_generated", "logs", "transcript.jsonl")

	// Empty fakeProc stands in for macOS, where /proc is absent.
	w := Watcher{Proc: fakeProc{}}
	got, fromProc := w.Title("s1", 99, transcript, time.Now())
	if fromProc {
		t.Fatal("fromProc = true with no /proc")
	}
	if got != "macOS hook path" {
		t.Fatalf("got %q, want %q", got, "macOS hook path")
	}

	if bad := AnnotationFromTranscript("/tmp/not-agy/transcript.jsonl"); bad != "" {
		t.Errorf("non-agy transcript resolved to %q", bad)
	}
}

func TestProcFSReadsTaskChildrenAndStatFallback(t *testing.T) {
	root := t.TempDir()
	// PID 10 (bash) with task/10/children naming PID 20 (agy).
	if err := os.MkdirAll(filepath.Join(root, "10", "task", "10"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "10", "comm"), []byte("bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "10", "task", "10", "children"), []byte("20 \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fdDir := filepath.Join(root, "20", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "20", "comm"), []byte("agy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockTarget := filepath.Join(root, "appdata", "presence", "abc-123.lock")
	if err := os.Symlink(lockTarget, filepath.Join(fdDir, "9")); err != nil {
		t.Fatal(err)
	}

	p := procFS{root: root}
	pid, annot := FindAnnotation(p, 10)
	want := filepath.Join(root, "appdata", "annotations", "abc-123.pbtxt")
	if pid != 20 || annot != want {
		t.Fatalf("task/children path: pid=%d annot=%q, want 20 and %q", pid, annot, want)
	}

	// Remove task/10/children to force the /proc/<pid>/stat fallback (including
	// a comm with spaces and parentheses).
	if err := os.Remove(filepath.Join(root, "10", "task", "10", "children")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "20", "stat"), []byte("20 (agy (worker)) S 10 20 10 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, annot = FindAnnotation(p, 10)
	if pid != 20 || annot != want {
		t.Fatalf("stat fallback: pid=%d annot=%q, want 20 and %q", pid, annot, want)
	}
}
