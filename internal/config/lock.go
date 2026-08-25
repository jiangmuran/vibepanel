package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrLocked means another vibepanel already has this data directory.
var ErrLocked = errors.New("config: data directory is in use")

// LockDataDir takes an exclusive lock on the data directory for the life of
// the process, and returns the function that releases it.
//
// Two panels on one data directory start perfectly happily, and the second one
// breaks the premise the whole design rests on. Measured: one session, one
// panel, one tmux client; a second panel started on the same data directory
// and socket and there were two clients on that session, with nothing logged
// by either.
//
// Two tmux clients on a session is not a memory problem, or not mainly. The
// panel is meant to be the only client so that there is one authoritative grid
// and one place that decides its size; with two, the resize arbitration the
// mobile story is built on has no meaning. And each panel keeps its own
// detector in memory, so a bell seen by one is invisible to the other and the
// "waiting" it sets is overwritten by the other's "working" on the next tick.
//
// A lock file rather than a pid check: a pid in a file is a guess about a
// process that may have died and been replaced, and flock is released by the
// kernel however the holder exits — including a SIGKILL, which is exactly the
// case a pid file gets wrong.
//
// Only `serve` takes it. The admin subcommands read and write the database
// briefly and must keep working while the panel is running.
func LockDataDir(dir string) (release func(), err error) {
	path := filepath.Join(dir, "vibepanel.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readHolder(path)
		_ = f.Close()
		if holder != "" {
			return nil, fmt.Errorf("%w: %s is already running with %s", ErrLocked, holder, dir)
		}
		return nil, fmt.Errorf("%w: another vibepanel is already running with %s", ErrLocked, dir)
	}
	// The pid is for the error message the *next* process prints, not for
	// deciding anything. Truncate first: a shorter pid must not leave the tail
	// of a longer one behind.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// readHolder returns a description of whoever holds the lock, or "".
func readHolder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return ""
	}
	return fmt.Sprintf("pid %d", pid)
}

// DataDirLockedBy reports who holds the data directory lock, or "" if nobody
// does. It does not take the lock.
//
// For `doctor`, where a panel already running is the normal state and worth
// saying rather than worth failing over.
func DataDirLockedBy(dir string) string {
	path := filepath.Join(dir, "vibepanel.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return ""
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if h := readHolder(path); h != "" {
			return h
		}
		return "another process"
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return ""
}
