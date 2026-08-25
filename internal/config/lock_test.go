package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOnlyOnePanelPerDataDirectory(t *testing.T) {
	// Two panels on one data directory started happily, and the second one
	// voids the premise the design rests on: the panel is meant to be the only
	// tmux client, so that there is one authoritative grid and one place that
	// decides its size. Measured before this existed — one session, two
	// attached clients, nothing logged by either.
	dir := t.TempDir()

	release, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	_, err = LockDataDir(dir)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v, want ErrLocked", err)
	}
	// The message has to name something the operator can act on. "Already
	// running" without a pid sends somebody hunting through ps.
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("the refusal does not say who holds it: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not say which directory: %v", err)
	}

	release()

	release2, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release2()
}

func TestDataDirLockedByDoesNotTakeTheLock(t *testing.T) {
	// doctor asks this, and a diagnostic that took the lock would report the
	// directory as free and then be holding it — or, worse, block the panel
	// from restarting for as long as it ran.
	dir := t.TempDir()

	if holder := DataDirLockedBy(dir); holder != "" {
		t.Errorf("an unused directory is held by %q", holder)
	}

	release, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()

	holder := DataDirLockedBy(dir)
	if holder == "" {
		t.Fatal("a held directory reports as free")
	}
	if !strings.Contains(holder, strconv.Itoa(os.Getpid())) {
		t.Errorf("holder = %q, which does not name the process", holder)
	}
	// And asking twice must not have changed anything.
	if again := DataDirLockedBy(dir); again != holder {
		t.Errorf("asking changed the answer: %q then %q", holder, again)
	}
}

func TestAShorterPidDoesNotLeaveTheTailOfALongerOne(t *testing.T) {
	// The file is rewritten by each holder. Without the truncate, pid 999
	// written over pid 1234567 leaves "9997" — a pid that means nothing, in a
	// message whose whole job is to name something real.
	dir := t.TempDir()
	path := filepath.Join(dir, "vibepanel.lock")
	// Deliberately far longer than any pid this process could have. The first
	// version of this test seeded "1234567", which is the same length as the
	// pids this machine hands out — so removing the truncate changed nothing
	// and the test passed either way.
	if err := os.WriteFile(path, []byte("123456789012345678901234567890\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	release, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("the lock file says %q, want %d", got, os.Getpid())
	}
}
