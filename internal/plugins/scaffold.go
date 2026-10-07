package plugins

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The scaffold is what a new plugin's directory starts as: a working plugin
// from a template, the SDK's types, the stylesheet's name, and the
// instructions an agent reads before it touches anything (docs/plugins.md §9).
//
// Written once, into a directory that is empty or does not exist; nothing is
// ever overwritten, because the directory may be somebody's work.

//go:embed all:scaffold/templates
var templateFS embed.FS

//go:embed scaffold/AGENTS.md scaffold/CLAUDE.md scaffold/README.md
var scaffoldFS embed.FS

// AgentsFile is the development spec, written into every scaffolded
// directory. Tests read it for the sentences it has to carry.
//
//go:embed scaffold/AGENTS.md
var AgentsFile []byte

// Template is one starting point.
type Template struct {
	ID    string `json:"id"`
	Rungs Rungs  `json:"rungs"`
}

// templateOrder is the gallery order: the lowest rung first, because the
// development spec's first sentence is to choose the lowest rung that does
// the job.
var templateOrder = []string{"theme", "pane", "service", "process", "full"}

// Templates lists the templates this build carries, with the rungs each
// climbs read from its manifest.
func Templates() []Template {
	out := []Template{}
	for _, id := range templateOrder {
		raw, err := templateFS.ReadFile("scaffold/templates/" + id + "/" + ManifestFile)
		if err != nil {
			continue
		}
		m, err := ParseManifest(fill(raw, "probe", "Probe"))
		if err != nil {
			continue
		}
		out = append(out, Template{ID: id, Rungs: m.Rungs()})
	}
	return out
}

// TemplateFS is one template's files, for the test that reads every
// template as a plugin.
func TemplateFS(id string) (fs.FS, error) {
	if !knownTemplate(id) {
		return nil, fmt.Errorf("no template %q", id)
	}
	return fs.Sub(templateFS, "scaffold/templates/"+id)
}

func knownTemplate(id string) bool {
	for _, t := range templateOrder {
		if t == id {
			return true
		}
	}
	return false
}

// ErrNotEmpty is a scaffold pointed at a directory that already has files in it.
var ErrNotEmpty = errors.New("that directory is not empty; a plugin is scaffolded into an empty or new directory")

// fill writes the id and the name into a template's text.
func fill(data []byte, id, name string) []byte {
	return []byte(strings.NewReplacer("__ID__", id, "__NAME__", name).Replace(string(data)))
}

// Scaffold writes a new plugin into dir from a template.
func Scaffold(dir, templateID, id, name string) error {
	if !knownTemplate(templateID) {
		return fmt.Errorf("no template %q", templateID)
	}
	if !ValidID(id) {
		return errors.New("id is 3-40 lower-case letters, digits and dashes, starting with a letter")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	if strings.ContainsAny(name, "\"\\\n") || len(name) > MaxName {
		return errors.New("a name is one short line without quotes")
	}
	if err := emptyOrMissing(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tfs, err := TemplateFS(templateID)
	if err != nil {
		return err
	}
	err = fs.WalkDir(tfs, ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if p == "." {
				return nil
			}
			return os.MkdirAll(filepath.Join(dir, filepath.FromSlash(p)), 0o755)
		}
		data, rerr := fs.ReadFile(tfs, p)
		if rerr != nil {
			return rerr
		}
		return writeNew(filepath.Join(dir, filepath.FromSlash(p)), fill(data, id, name))
	})
	if err != nil {
		return err
	}
	for src, dst := range map[string]string{"scaffold/AGENTS.md": "AGENTS.md", "scaffold/CLAUDE.md": "CLAUDE.md", "scaffold/README.md": "README.md"} {
		data, rerr := scaffoldFS.ReadFile(src)
		if rerr != nil {
			return rerr
		}
		if err := writeNew(filepath.Join(dir, dst), data); err != nil {
			return err
		}
	}
	if err := writeNew(filepath.Join(dir, TypesFile), Types); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, SDKFile), SDK); err != nil {
		return err
	}
	return writeNew(filepath.Join(dir, ".gitignore"), []byte(".vibepanel/\nnode_modules/\n"))
}

func emptyOrMissing(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return ErrNotEmpty
	}
	return nil
}

// writeNew writes a file that must not exist yet.
func writeNew(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // a new file in the caller's directory
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // written below, closed after
	_, err = f.Write(data)
	return err
}

// Slug turns a plugin name into an id: "Stand-up board" is stand-up-board.
// A name with nothing usable in it becomes "plugin".
func Slug(name string) string {
	var b strings.Builder
	last := '-'
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			last = r
		default:
			if last != '-' && b.Len() > 0 {
				b.WriteRune('-')
				last = '-'
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "plugin" + map[bool]string{true: "", false: "-" + s}[s == ""]
	}
	if len(s) < 3 {
		s = "plugin-" + s
	}
	return s
}
