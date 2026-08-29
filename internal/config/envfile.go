package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EnvFilePath is the file the service reads its environment from.
//
// The panel is not told this by anything: systemd loads it with
// `EnvironmentFile=` before the process exists, so by the time there is a
// process the file has already done its job and left no trace of where it was.
// The installer writes it to one place and the unit reads it from the same
// place, and this is that place.
func EnvFilePath() string {
	if p := os.Getenv("VIBEPANEL_ENV_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "vibepanel.env")
}

// EditableEnv is the settings the panel offers to change in that file.
//
// Not every variable: the list is the ones somebody would go and edit, and it
// deliberately leaves out two kinds.
//
// Secrets -- CLOUDFLARE_API_TOKEN -- because a page that shows them puts an
// ACME credential in every screenshot and every share of that screen.
//
// VIBEPANEL_TMUX_SOCKET, because red line 1 is about that value and a text box
// is not the place to change it: a panel pointed at a different socket cannot
// see its own sessions, and the ones it was managing keep running with nothing
// attached to them. It is shown, and it is shown as a fact.
var EditableEnv = []string{
	"VIBEPANEL_ADDR",
	"VIBEPANEL_DOMAIN",
	"VIBEPANEL_TLS_MODE",
	"VIBEPANEL_CERT_FILE",
	"VIBEPANEL_KEY_FILE",
	"VIBEPANEL_ACME_DNS_PROVIDER",
	"VIBEPANEL_ACME_EMAIL",
	"VIBEPANEL_ACME_DIRECTORY",
	"VIBEPANEL_ALLOW_FROM",
	"VIBEPANEL_TRUSTED_PROXIES",
}

func editable(key string) bool {
	for _, k := range EditableEnv {
		if k == key {
			return true
		}
	}
	return false
}

// ReadEnvFile returns the assignments in the file, ignoring commented ones.
//
// A missing file is an empty map and not an error: the installer writes one,
// and a panel started from a working tree has never had one.
func ReadEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := assignment(line)
		if ok {
			out[k] = v
		}
	}
	return out, nil
}

// assignment splits a live `K=V` line. Comments and blanks are not
// assignments, and neither is a line whose key is not a plausible variable
// name -- prose in this file often contains an `=`.
func assignment(line string) (string, string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	k, v, ok := strings.Cut(t, "=")
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		return "", "", false
	}
	for _, r := range k {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return "", "", false
		}
	}
	return k, strings.Trim(strings.TrimSpace(v), `"`), true
}

// PatchEnvFile rewrites the assignments in `set` and leaves everything else
// exactly as it was.
//
// Everything else is most of the file. It ships with a paragraph above each
// variable explaining what it does and what happens if it is wrong, and half
// the variables are present as commented-out examples. A writer that emitted
// `K=V` lines from a map would produce a working file with all of that gone --
// and the person who opens it next has lost the only documentation they had.
//
// So: a key already assigned is replaced in place, a key that exists only as a
// commented example is uncommented in place so it stays under its own
// paragraph, and a key that is nowhere is appended. An empty value removes the
// assignment by commenting it out rather than deleting the line, because "this
// used to be set" is worth being able to see.
func PatchEnvFile(path string, set map[string]string) error {
	if path == "" {
		return fmt.Errorf("config: no env file path")
	}
	for k := range set {
		if !editable(k) {
			return fmt.Errorf("config: %s is not editable from here", k)
		}
	}

	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: read %s: %w", path, err)
	}

	// A copy before any edit, the same rule the hooks installer follows: this
	// file is the user's, it holds things they typed, and a tool that rewrites
	// it wholesale is a tool people stop trusting.
	if len(original) > 0 {
		backup := fmt.Sprintf("%s.vibepanel-backup-%s", path, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, original, 0o600); err != nil {
			return fmt.Errorf("config: back up %s: %w", path, err)
		}
	}

	lines := strings.Split(string(original), "\n")
	done := map[string]bool{}

	for i, line := range lines {
		t := strings.TrimSpace(line)
		key := ""
		if k, _, ok := assignment(line); ok {
			key = k
		} else if strings.HasPrefix(t, "#") {
			// A commented example, `#VIBEPANEL_DOMAIN=panel.example.com`.
			if k, _, ok := assignment(strings.TrimPrefix(t, "#")); ok {
				key = k
			}
		}
		if key == "" || done[key] {
			continue
		}
		v, want := set[key]
		if !want {
			continue
		}
		done[key] = true
		if v == "" {
			// Commented out rather than removed, and only if it was live.
			if !strings.HasPrefix(t, "#") {
				lines[i] = "#" + line
			}
			continue
		}
		lines[i] = key + "=" + v
	}

	// Whatever the file has never mentioned. Sorted, because a map's order is
	// not an order and a file that reshuffles itself on every save is a file
	// nobody can diff.
	var missing []string
	for k, v := range set {
		if !done[k] && v != "" {
			missing = append(missing, k+"="+v)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) == "" {
			lines = lines[:n-1]
		}
		lines = append(lines, "", "# Added from the panel's settings page.")
		lines = append(lines, missing...)
		lines = append(lines, "")
	}

	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	// The mode the file had, and 0600 for a new one: it can hold an ACME
	// credential.
	mode := os.FileMode(0o600)
	if info, serr := os.Stat(path); serr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".vibepanel.tmp"
	if err := os.WriteFile(tmp, []byte(body), mode); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("config: set mode on %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	return nil
}
