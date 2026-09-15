package pages

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The scaffold is what a new page's directory starts as: a working page from
// a template, the SDK and its types, fixtures, and the instructions an agent
// reads before it touches anything.
//
// Everything here is written once, into a directory that is empty or does not
// exist. Scaffold never overwrites a file, because the directory it is pointed
// at may be somebody's work, and "create a page here" deleting an index.html
// that was already there is not a mistake anyone gets to make twice.

//go:embed all:templates
var templateFS embed.FS

//go:embed scaffold/AGENTS.md scaffold/CLAUDE.md scaffold/README.md scaffold/gitignore
var scaffoldFS embed.FS

// Template is one starting point in the New page gallery.
type Template struct {
	ID string `json:"id"`
	// Sections is what its manifest asks for, so the gallery can say what a
	// template shows without the frontend reading the manifest itself.
	Sections []string `json:"sections"`
}

// templateOrder is the gallery order: the plain starting point first, then
// the templates by how often a screen like that gets put up.
var templateOrder = []string{"blank", "wall", "spend", "built", "glance", "kiosk"}

// Templates lists the templates this build carries.
func Templates() []Template {
	out := []Template{}
	for _, id := range templateOrder {
		raw, err := templateFS.ReadFile("templates/" + id + "/" + ManifestFile)
		if err != nil {
			continue
		}
		m, err := ParseManifest(raw)
		if err != nil {
			continue
		}
		out = append(out, Template{ID: id, Sections: m.SectionNames()})
	}
	return out
}

// TemplateFS is one template's files, for the test that lints every template
// and for pages-check.
func TemplateFS(id string) (fs.FS, error) {
	if !knownTemplate(id) {
		return nil, fmt.Errorf("no template %q", id)
	}
	return fs.Sub(templateFS, "templates/"+id)
}

func knownTemplate(id string) bool {
	for _, t := range templateOrder {
		if t == id {
			if _, err := templateFS.ReadFile("templates/" + id + "/" + ManifestFile); err == nil {
				return true
			}
		}
	}
	return false
}

// ErrNotEmpty is a scaffold pointed at a directory that already has files in it.
var ErrNotEmpty = errors.New("that directory is not empty; a page is scaffolded into an empty or new directory")

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a page name into a directory name: "Lobby wall" is lobby-wall.
// A name with nothing ASCII in it -- 大厅 -- becomes "page", and the caller
// adds a suffix if that is taken.
func Slug(name string) string {
	s := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		return "page"
	}
	return s
}

// Scaffold writes a new page into dir from a template, with fixtures.
//
// fixtures is name → JSON, produced by the HTTP layer from the real snapshot
// structs so a fixture cannot drift from what the panel sends.
func Scaffold(dir, templateID, name string, fixtures map[string][]byte) error {
	if !knownTemplate(templateID) {
		return fmt.Errorf("no template %q", templateID)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a page needs a name")
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
		if p == ManifestFile {
			data, rerr = renameManifest(data, name)
			if rerr != nil {
				return rerr
			}
		}
		return writeNew(filepath.Join(dir, filepath.FromSlash(p)), data)
	})
	if err != nil {
		return err
	}
	return writeCommon(dir, fixtures)
}

// ScaffoldFrom writes a copy of an existing page into dir under a new name:
// its files as they are, its manifest renamed, and the same companions a
// template gets. For Fork, where two agents try two directions from one page.
//
// A companion the page's files already provide -- a fixture the author edited,
// say -- is kept rather than replaced.
func ScaffoldFrom(dir, name string, b Bundle, fixtures map[string][]byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a page needs a name")
	}
	if err := emptyOrMissing(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m := b.Manifest
	m.Name = name
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, ManifestFile), append(raw, '\n')); err != nil {
		return err
	}
	have := map[string]bool{}
	for _, f := range b.Files {
		if !ValidPath(f.Path) {
			return fmt.Errorf("bad path %q", f.Path)
		}
		if err := writeNew(filepath.Join(dir, filepath.FromSlash(f.Path)), f.Data); err != nil {
			return err
		}
		have[f.Path] = true
	}
	kept := map[string][]byte{}
	for fname, data := range fixtures {
		if !have["fixtures/"+fname+".json"] {
			kept[fname] = data
		}
	}
	return writeCommon(dir, kept)
}

// writeCommon writes what every page directory has besides its own files.
func writeCommon(dir string, fixtures map[string][]byte) error {
	for src, dst := range map[string]string{
		"scaffold/AGENTS.md": "AGENTS.md",
		"scaffold/CLAUDE.md": "CLAUDE.md",
		"scaffold/README.md": "README.md",
		"scaffold/gitignore": ".gitignore",
	} {
		data, rerr := scaffoldFS.ReadFile(src)
		if rerr != nil {
			return rerr
		}
		if err := writeNew(filepath.Join(dir, dst), data); err != nil {
			return err
		}
	}
	if err := writeNew(filepath.Join(dir, SDKFile), SDK); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, TypesFile), Types); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, ArchitectureFile), Architecture); err != nil {
		return err
	}
	if len(fixtures) > 0 {
		if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
			return err
		}
		for fname, data := range fixtures {
			if !FixtureName(fname) {
				return fmt.Errorf("bad fixture name %q", fname)
			}
			if err := writeNew(filepath.Join(dir, "fixtures", fname+".json"), data); err != nil {
				return err
			}
		}
	}
	return nil
}

var fixtureName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// FixtureName reports whether a fixture name is one the SDK will load. The
// same pattern the SDK checks, so a fixture written here is one it can open.
func FixtureName(s string) bool { return fixtureName.MatchString(s) }

// SyncSDK rewrites a page directory's copy of the SDK, its types and
// ARCHITECTURE.md with this build's. The copies are for editors, agents and
// offline work; the page itself always loads the panel's.
func SyncSDK(dir string) error {
	for p, data := range map[string][]byte{SDKFile: SDK, TypesFile: Types, ArchitectureFile: Architecture} {
		target := filepath.Join(dir, p)
		if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil { //nolint:gosec // a page directory is the user's own
			return err
		}
	}
	return nil
}

// SDKCurrent reports whether a directory's SDK copy is this build's.
func SDKCurrent(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, SDKFile)) //nolint:gosec // reading the user's own page
	return err == nil && string(data) == string(SDK)
}

func renameManifest(raw []byte, name string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("template manifest: %w", err)
	}
	m["name"] = name
	out, err := json.MarshalIndent(orderedManifest(m), "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	if _, err := ParseManifest(out); err != nil {
		return nil, err
	}
	return out, nil
}

// orderedManifest keeps sdk and name first when a template's manifest is
// rewritten, so the file an agent opens reads top-down.
func orderedManifest(m map[string]any) any {
	type kv struct {
		k string
		v any
	}
	var out []kv
	for _, k := range []string{"sdk", "name"} {
		if v, ok := m[k]; ok {
			out = append(out, kv{k, v})
		}
	}
	known := []string{"sections", "spend", "repo", "flow", "params", "data", "admin", "sources", "server", "actions",
		"scriptHosts", "viewports"}
	for _, k := range known {
		if v, ok := m[k]; ok {
			out = append(out, kv{k, v})
		}
	}
	// Anything else after, sorted: a key this list has not heard of is still
	// the template's, and a rename that dropped it would publish a different
	// page. That is how the kiosk template first lost its data.
	var rest []string
	for k := range m {
		if k != "sdk" && k != "name" && !slices.Contains(known, k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, kv{k, m[k]})
	}
	return orderedJSON(func(yield func(string, any) bool) {
		for _, e := range out {
			if !yield(e.k, e.v) {
				return
			}
		}
	})
}

type orderedJSON func(yield func(string, any) bool)

func (o orderedJSON) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	first := true
	var err error
	o(func(k string, v any) bool {
		kb, kerr := json.Marshal(k)
		vb, verr := json.Marshal(v)
		if kerr != nil || verr != nil {
			err = errors.Join(kerr, verr)
			return false
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
		return true
	})
	b.WriteByte('}')
	return []byte(b.String()), err
}

func emptyOrMissing(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		// A fresh `git init` is still an empty directory for this purpose.
		if e.Name() == ".git" {
			continue
		}
		return ErrNotEmpty
	}
	return nil
}

// writeNew creates a file that must not already exist.
func writeNew(p string, data []byte) error {
	if dir := path.Dir(filepath.ToSlash(p)); dir != "" {
		if err := os.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // the user's own new page
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
