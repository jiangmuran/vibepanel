package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/httpapi"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// `vibepanel page` is the share-page workflow from a shell, and mostly from the
// shell of an agent working inside a page's directory. Its output is written
// for that reader: every problem is a file, a line, what, and what to do, and a
// command that found problems exits non-zero so a loop can tell.
//
// It opens the database directly, like `project` does, and never touches tmux.
// A lint that started the panel's tmux server as a side effect would be a lint
// with a side effect.

const pageUsage = `usage: vibepanel page <command> [flags] [dir]

  init      scaffold a new page and register it: into dir, or with --name and no
            dir into <data dir>/pages/page-<slug>
  check     report what is wrong with the page in dir (the current directory by default)
  shot      screenshot the draft through a preview link, and report what broke
  publish   store the draft as the next published version
  checkout  write a published version back out into a directory
  list      list pages
  sync-sdk  replace dir's copy of vibepanel.js and vibepanel.d.ts with this build's`

func cmdPage(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(pageUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "init":
		return pageInit(rest)
	case "check":
		return pageCheck(rest)
	case "shot":
		return pageShot(rest)
	case "publish":
		return pagePublish(rest)
	case "checkout":
		return pageCheckout(rest)
	case "list", "ls":
		return pageList(rest)
	case "sync-sdk":
		return pageSyncSDK(rest)
	}
	return fmt.Errorf("unknown page command %q\n\n%s", sub, pageUsage)
}

// openDB opens the panel's database without its tmux server.
func openDB(ctx context.Context) (config.Config, *store.DB, error) {
	cfg, err := config.Load(nil, io.Discard)
	if err != nil {
		return cfg, nil, err
	}
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return cfg, nil, err
	}
	return cfg, db, nil
}

// dirArg is the directory a command is about: the one positional argument, or
// the current directory.
func dirArg(fs *flag.FlagSet) (string, error) {
	if fs.NArg() > 1 {
		return "", fmt.Errorf("one directory, not %d", fs.NArg())
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	return filepath.Abs(dir)
}

// pageFor finds the page registered for a directory.
func pageFor(ctx context.Context, db *store.DB, dir string) (store.SharePage, error) {
	page, err := db.SharePageBySourceDir(ctx, dir)
	if errors.Is(err, store.ErrNotFound) {
		// Resolved as well as typed: a directory reached through a symlink is
		// registered under whichever path the page was made with.
		if real, rerr := filepath.EvalSymlinks(dir); rerr == nil && real != dir {
			page, err = db.SharePageBySourceDir(ctx, real)
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		return store.SharePage{}, fmt.Errorf("%s is not a page the panel knows about; "+
			"`vibepanel page init %s` makes one, or add it in Settings → Sharing", dir, dir)
	}
	return page, err
}

// ─── init ─────────────────────────────────────────────────────────────────

func pageInit(args []string) error {
	fs := flag.NewFlagSet("page init", flag.ContinueOnError)
	template := fs.String("template", "blank", "starting point: "+templateIDs())
	name := fs.String("name", "", "the page's name (defaults to the directory's name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cfg, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	// A name and no directory puts the page where the settings page would:
	// under the data directory as page-<slug>, not in whatever directory the
	// command happened to be typed in.
	var dir string
	if fs.NArg() == 0 && *name != "" {
		dir = httpapi.NewPageDir(cfg.PagesDir(), *name)
	} else if dir, err = dirArg(fs); err != nil {
		return err
	}
	if *name == "" {
		*name = filepath.Base(dir)
	}
	owner, err := db.FirstUserID(ctx)
	if err != nil {
		return errors.New("the panel has no account yet; finish setup first")
	}
	if err := pages.Scaffold(dir, *template, *name, httpapi.PageFixtures()); err != nil {
		return err
	}
	pages.GitInit(ctx, dir)
	page, err := db.CreateSharePage(ctx, id.New(), owner, *name, dir)
	if err != nil {
		return err
	}
	fmt.Printf("created page %s  %s\n  %s\n\n", page.ID, page.Name, dir)
	fmt.Println("Next: open the directory in an agent session, and see AGENTS.md.")
	fmt.Println("  vibepanel page check   what is wrong")
	fmt.Println("  vibepanel page shot    what it looks like")
	return nil
}

func templateIDs() string {
	var ids []string
	for _, t := range pages.Templates() {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

// ─── check ────────────────────────────────────────────────────────────────

func pageCheck(args []string) error {
	fs := flag.NewFlagSet("page check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print problems as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := dirArg(fs)
	if err != nil {
		return err
	}
	b, problems := pages.LintDir(dir)
	if b.Files != nil && !pages.SDKCurrent(dir) {
		problems = append(problems, pages.Problem{File: pages.SDKFile, Severity: pages.SeverityWarning,
			Code: "sdk-stale", Message: "this copy of the SDK is not the panel's; the page loads the panel's",
			Fix: "vibepanel page sync-sdk"})
	}
	errs := 0
	for _, p := range problems {
		if p.Severity == pages.SeverityError {
			errs++
		}
	}
	if *asJSON {
		out, _ := json.MarshalIndent(map[string]any{"problems": emptyProblems(problems),
			"files": len(b.Files), "bytes": b.Bytes}, "", "  ")
		fmt.Println(string(out))
	} else {
		for _, p := range problems {
			fmt.Println(p.String())
		}
		if len(problems) == 0 {
			fmt.Printf("ok: %d files, %s\n", len(b.Files), humanBytes(b.Bytes))
		} else {
			fmt.Printf("\n%d errors, %d warnings\n", errs, len(problems)-errs)
		}
	}
	if errs > 0 {
		return errSilentFailure
	}
	return nil
}

// errSilentFailure exits 1 without printing "vibepanel: …": the command has
// already said what was wrong, in full.
var errSilentFailure = errors.New("")

func emptyProblems(p []pages.Problem) []pages.Problem {
	if p == nil {
		return []pages.Problem{}
	}
	return p
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ─── shot ─────────────────────────────────────────────────────────────────

// shotReport is what the SDK writes into the page under ?shot=1.
type shotReport struct {
	Status   string   `json:"status"`
	Sections []string `json:"sections"`
	Errors   []struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Source  string `json:"source"`
		Line    int    `json:"line"`
	} `json:"errors"`
	Suspicious []string `json:"suspicious"`
	OverflowX  bool     `json:"overflowX"`
	OverflowY  bool     `json:"overflowY"`
}

var reportTag = regexp.MustCompile(`(?s)<script[^>]*id="vp-report"[^>]*>(.*?)</script>`)

func pageShot(args []string) error {
	fs := flag.NewFlagSet("page shot", flag.ContinueOnError)
	viewports := fs.String("viewport", "tv-1080,phone", "screens, comma-separated: "+viewportNames())
	fixtures := fs.String("fixture", "live", "data, comma-separated: live (the panel's real data) or a fixture name")
	detail := fs.String("detail", "counts", "what a live preview may say: counts or names")
	wait := fs.Duration("wait", 4*time.Second, "how long the page gets to draw")
	chrome := fs.String("chrome", "", "the browser to run (default: VIBEPANEL_CHROME, then Chrome or Chromium on PATH)")
	noSandbox := fs.Bool("no-browser-sandbox", false,
		"run the browser without its own process sandbox, for machines where it cannot start one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := dirArg(fs)
	if err != nil {
		return err
	}
	bin := findChrome(*chrome)
	if bin == "" {
		return errors.New("no Chrome or Chromium found; install one, or pass --chrome / set VIBEPANEL_CHROME. " +
			"The Preview pane in the panel works without it")
	}

	ctx := context.Background()
	cfg, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	page, err := pageFor(ctx, db, dir)
	if err != nil {
		return err
	}
	owner, err := db.SharePageOwner(ctx, page.ID)
	if err != nil {
		return err
	}
	if !store.ValidShareDetail(store.ShareDetail(*detail)) {
		return errors.New("--detail is counts or names")
	}
	link, token, err := httpapi.MintPreviewLink(ctx, db, page, store.ShareDetail(*detail), store.ShareWhole, "", owner)
	if err != nil {
		return err
	}
	defer func() {
		// Gone when the command is, rather than in fifteen minutes.
		_ = db.DeleteShareLink(context.Background(), link.ID)
	}()

	base := os.Getenv("VIBEPANEL_URL")
	if base == "" {
		base = cfg.LoopbackURL()
	}
	shots := filepath.Join(dir, pages.MetaDir, "shots")
	if err := os.MkdirAll(shots, 0o755); err != nil {
		return err
	}

	failed := false
	for _, vname := range splitList(*viewports) {
		vp, ok := pages.ViewportNamed(vname)
		if !ok {
			return fmt.Errorf("unknown viewport %q; screens are %s", vname, viewportNames())
		}
		for _, fixture := range splitList(*fixtures) {
			if fixture != "live" && !pages.FixtureName(fixture) {
				return fmt.Errorf("bad fixture name %q", fixture)
			}
			q := url.Values{"shot": {"1"}}
			if fixture != "live" {
				q.Set("fixture", fixture)
			}
			target := strings.TrimRight(base, "/") + "/share/" + token + "/?" + q.Encode()
			out := filepath.Join(shots, vp.Name+"-"+fixture+".png")
			report, rerr := runShot(ctx, bin, target, out, vp, *wait, *noSandbox)
			rel, _ := filepath.Rel(dir, out)
			fmt.Printf("%s · %s  %s\n", vp.Name, fixture, rel)
			if errors.Is(rerr, errNoBrowserSandbox) {
				// Every later shot fails the same way; say it once, with the
				// way out, rather than eight times.
				return rerr
			}
			if rerr != nil {
				fmt.Printf("  could not take it: %v\n", rerr)
				failed = true
				continue
			}
			if describeReport(report) {
				failed = true
			}
		}
	}
	if failed {
		return errSilentFailure
	}
	return nil
}

// describeReport prints what went wrong in one shot, and says whether anything did.
func describeReport(r shotReport) bool {
	bad := false
	if r.Status != "" && r.Status != "live" {
		fmt.Printf("  status  %s\n", r.Status)
	}
	for _, e := range r.Errors {
		loc := e.Source
		if e.Line > 0 {
			loc = fmt.Sprintf("%s:%d", e.Source, e.Line)
		}
		fmt.Printf("  %-8s %s %s\n", e.Kind, loc, e.Message)
		bad = true
	}
	for _, s := range r.Suspicious {
		fmt.Printf("  text     %q rendered on screen\n", s)
		bad = true
	}
	if r.OverflowX {
		fmt.Println("  layout   the page is wider than the screen")
		bad = true
	}
	if r.OverflowY {
		fmt.Println("  layout   the page is taller than the screen (fine on a phone, not on a wall)")
	}
	return bad
}

// errNoBrowserSandbox is Chrome refusing to start because it cannot build its
// process sandbox -- Ubuntu 23.10 and later restrict the user namespaces it
// uses. Not retried without one on the user's behalf: turning a sandbox off is
// a decision, and it is theirs. The page itself stays in its CSP sandbox
// either way.
var errNoBrowserSandbox = errors.New("the browser cannot start its sandbox on this machine " +
	"(AppArmor restricts user namespaces). Run again with --no-browser-sandbox to take the " +
	"screenshot anyway; the page is still sandboxed by its own policy")

func runShot(ctx context.Context, bin, target, out string, vp pages.Viewport, wait time.Duration,
	noSandbox bool) (shotReport, error) {
	profile, err := os.MkdirTemp("", "vibepanel-shot-")
	if err != nil {
		return shotReport{}, err
	}
	defer os.RemoveAll(profile)
	headless := "--headless=new"
	if strings.Contains(filepath.Base(bin), "headless-shell") {
		headless = "--headless"
	}
	common := []string{
		headless, "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--hide-scrollbars", "--mute-audio", "--user-data-dir=" + profile,
		fmt.Sprintf("--window-size=%d,%d", vp.Width, vp.Height),
		fmt.Sprintf("--virtual-time-budget=%d", wait.Milliseconds()),
		// The loopback address and a certificate made for the public name do
		// not match. This browser talks to this machine's panel and nothing
		// else, with a throwaway profile.
		"--ignore-certificate-errors",
	}
	if noSandbox || (runtime.GOOS == "linux" && os.Geteuid() == 0) {
		common = append(common, "--no-sandbox")
	}
	budget := wait + 30*time.Second

	sctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(sctx, bin, append(common, "--screenshot="+out, target)...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		if bytes.Contains(msg, []byte("No usable sandbox")) {
			return shotReport{}, errNoBrowserSandbox
		}
		return shotReport{}, fmt.Errorf("%v: %s", err, lastLine(msg))
	}

	dctx, dcancel := context.WithTimeout(ctx, budget)
	defer dcancel()
	var dom bytes.Buffer
	dump := exec.CommandContext(dctx, bin, append(common, "--dump-dom", target)...)
	dump.Stdout = &dom
	if err := dump.Run(); err != nil {
		return shotReport{}, fmt.Errorf("reading the page back: %v", err)
	}
	m := reportTag.FindSubmatch(dom.Bytes())
	if m == nil {
		return shotReport{}, errors.New("the page wrote no report; does index.html load vibepanel.js?")
	}
	var r shotReport
	if err := json.Unmarshal(m[1], &r); err != nil {
		return shotReport{}, fmt.Errorf("the report is unreadable: %v", err)
	}
	return r, nil
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// findChrome looks for a browser to screenshot with. Not bundled: a page shot
// is a convenience for a machine that has one, and the Preview pane is the
// path that needs nothing.
func findChrome(flagged string) string {
	for _, c := range []string{flagged, os.Getenv("VIBEPANEL_CHROME")} {
		if c != "" {
			return c
		}
	}
	// The headless shell first. It is the build made for exactly this, and the
	// full browser in headless mode was measured hanging on --screenshot of
	// about:blank for a minute on a desktop with no display, where the shell
	// wrote the file in under a second.
	if p, err := exec.LookPath("chrome-headless-shell"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	playwright := func(pattern ...string) string {
		if home == "" {
			return ""
		}
		matches, _ := filepath.Glob(filepath.Join(append([]string{home, ".cache", "ms-playwright"}, pattern...)...))
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
		return ""
	}
	// A browser a test runner already downloaded is a browser.
	if p := playwright("chromium_headless_shell-*", "chrome-headless-shell-*", "chrome-headless-shell"); p != "" {
		return p
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return playwright("chromium-*", "chrome-linux*", "chrome")
}

func viewportNames() string {
	var names []string
	for _, v := range pages.Viewports {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ─── publish, checkout, list, sync ────────────────────────────────────────

func pagePublish(args []string) error {
	fs := flag.NewFlagSet("page publish", flag.ContinueOnError)
	note := fs.String("note", "", "what changed, for the history")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := dirArg(fs)
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	page, err := pageFor(ctx, db, dir)
	if err != nil {
		return err
	}
	version, err := httpapi.FreezeDraft(ctx, db, page, *note, false)
	if err != nil {
		return err
	}
	if aerr := db.Audit(ctx, store.AuditEntry{Event: "page.published", Username: "cli",
		Detail: page.Name + " v" + fmt.Sprint(version)}); aerr != nil {
		fmt.Fprintln(os.Stderr, "warning: not recorded in the audit log:", aerr)
	}
	fmt.Printf("published %s v%d\n", page.Name, version)
	return nil
}

func pageCheckout(args []string) error {
	fs := flag.NewFlagSet("page checkout", flag.ContinueOnError)
	pageID := fs.String("page", "", "the page's id (see `vibepanel page list`)")
	version := fs.Int("version", 0, "which version (default: the published one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pageID == "" {
		return errors.New("page checkout: --page is required")
	}
	dir, err := dirArg(fs)
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	page, err := db.SharePageByID(ctx, *pageID)
	if err != nil {
		return fmt.Errorf("no page %s", *pageID)
	}
	v := *version
	if v == 0 {
		v = page.PublishedVersion
	}
	if _, err := db.SharePageVersionByNumber(ctx, page.ID, v); err != nil {
		return fmt.Errorf("%s has no version %d", page.Name, v)
	}
	if err := httpapi.CheckoutVersion(ctx, db, page, v, dir); err != nil {
		return err
	}
	fmt.Printf("wrote %s v%d into %s\n", page.Name, v, dir)
	if dir != page.SourceDir {
		fmt.Printf("the page's draft is still %s; change it in Settings → Sharing to edit here\n", page.SourceDir)
	}
	return nil
}

func pageList(args []string) error {
	fs := flag.NewFlagSet("page list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	list, err := db.ListSharePages(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPUBLISHED\tDRAFT")
	for _, p := range list {
		pub := "—"
		if p.PublishedVersion > 0 {
			pub = fmt.Sprintf("v%d", p.PublishedVersion)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, pub, p.SourceDir)
	}
	return w.Flush()
}

func pageSyncSDK(args []string) error {
	fs := flag.NewFlagSet("page sync-sdk", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := dirArg(fs)
	if err != nil {
		return err
	}
	if _, err := pages.ReadManifestFile(dir); err != nil {
		return err
	}
	if err := pages.SyncSDK(dir); err != nil {
		return err
	}
	fmt.Printf("%s and %s are this build's\n", pages.SDKFile, pages.TypesFile)
	return nil
}
