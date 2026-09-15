package pages

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/browse"
)

// MetaDir is where the panel and its tools write into a page's directory:
// screenshots, the Preview pane's error log, the publish history. A dot
// directory, so nothing in it is ever part of a page.
const MetaDir = ".vibepanel"

// MaxMetaBytes bounds one file the panel writes there. The error log is the
// one written from a request, and it must not be a way to fill a disk.
const MaxMetaBytes = 256 << 10

// WriteMeta writes one file under a page directory's .vibepanel/.
//
// Refuses when .vibepanel, or the file, is a symlink: the directory is the
// user's own, and the panel following a link planted there would be the panel
// writing wherever the link points.
func WriteMeta(root, name string, data []byte) error {
	if strings.ContainsAny(name, `/\`) || name == "" || strings.HasPrefix(name, ".") {
		return fmt.Errorf("bad meta file name %q", name)
	}
	if len(data) > MaxMetaBytes {
		return fmt.Errorf("%s is larger than %d KiB", name, MaxMetaBytes>>10)
	}
	dir, err := metaDir(root)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	if info, lerr := os.Lstat(target); lerr == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s/%s is not a regular file", MetaDir, name)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // the user's own page directory
		return err
	}
	return os.Rename(tmp, target)
}

// AppendHistory adds one line to .vibepanel/HISTORY.md, creating it with a
// heading the first time.
func AppendHistory(root, line string) error {
	dir, err := metaDir(root)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, "HISTORY.md")
	info, lerr := os.Lstat(target)
	if lerr == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s/HISTORY.md is not a regular file", MetaDir)
	}
	fresh := errors.Is(lerr, fs.ErrNotExist)
	if lerr == nil && info.Size() > 4*MaxMetaBytes {
		// A history that long is being written by something in a loop. The
		// versions are in the database; this file is a courtesy.
		return nil
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644) //nolint:gosec // as above
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // the write below reports
	if fresh {
		if _, err := f.WriteString("# Publish history\n\nWritten by vibepanel on every publish. Newest last.\n\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString("- " + strings.ReplaceAll(strings.TrimSpace(line), "\n", " ") + "\n")
	return err
}

func metaDir(root string) (string, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return "", fmt.Errorf("cannot reach the page directory: %w", err)
	}
	dir := filepath.Join(real, MetaDir)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o755); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !info.IsDir():
		return "", fmt.Errorf("%s is not a directory", MetaDir)
	}
	return dir, nil
}

// GitInit makes a new page directory a repository with one commit, best
// effort.
//
// Best effort because it is a convenience: a machine with no git, or no
// identity configured, gets a page that works and no history of it. The commit
// uses the user's own identity or none -- the panel does not invent an author
// for somebody else's repository.
func GitInit(ctx context.Context, dir string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		return cmd.Run()
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return
	}
	if run("init", "-q") != nil {
		return
	}
	if run("add", "-A") != nil {
		return
	}
	// No signing and no hooks: a global commit.gpgsign waits on a pinentry
	// nobody will see from inside a request, and a global hook is somebody
	// else's policy for somebody else's repositories.
	_ = run("-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", "Start a vibepanel page")
}
