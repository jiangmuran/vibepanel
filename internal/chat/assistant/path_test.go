package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The panel's service PATH does not have the harness; the login shell's does.
// This is `exec: "claude": executable file not found in $PATH` on a panel run
// by systemd with claude in ~/.local/bin.
func TestAHarnessOnlyTheLoginShellFindsIsFoundAndItsPathKept(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	login := dir + ":/usr/bin:/bin"
	old := loginPath
	loginPath = func() string { return login }
	defer func() { loginPath = old }()

	r, err := New(Config{Harness: "claude", WorkDir: t.TempDir(), SelfBinary: "/x/vibepanel", Env: []string{"FOO=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.Binary != bin {
		t.Fatalf("binary %q, want %q", r.cfg.Binary, bin)
	}
	var paths []string
	for _, kv := range r.env() {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, kv)
		}
	}
	if len(paths) != 1 || paths[0] != "PATH="+login {
		t.Fatalf("child PATH: %q", paths)
	}

	// Nowhere at all: refused when configured, not at the first message.
	loginPath = func() string { return "/usr/bin:/bin" }
	if _, err := New(Config{Harness: "claude", WorkDir: t.TempDir(), SelfBinary: "/x/vibepanel"}); err == nil || !strings.Contains(err.Error(), "login shell") {
		t.Fatalf("a harness nowhere: %v", err)
	}
}

// The login shell answers even when its profile prints something first.
func TestTheLoginShellsPathIsReadPastAGreeting(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\necho 'welcome to the box'\nPATH=/opt/tools/bin:/usr/bin\nexport PATH\nshift\nshift\nexec /bin/sh -c \"$1\"\n"
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sh)
	if got := shellPath(); got != "/opt/tools/bin:/usr/bin" {
		t.Fatalf("login PATH %q", got)
	}
}
