package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jiangmuran/vibepanel/internal/httpapi"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// `vibepanel page data`, `page run` and `page docs`: a page's backend from the
// shell of the agent building it. docs/page-backend.md §9.

const pageDataUsage = `usage: vibepanel page data get [--live] [key]
       vibepanel page data set [--live] <key> <value>
       vibepanel page data reset [--live] <key>

The page in the current directory (or --dir). Draft data unless --live. A value
that parses as JSON is that JSON; anything else is a string.`

const pageRunUsage = `usage: vibepanel page run transform [--fixture name]
       vibepanel page run schedule
       vibepanel page run action <name> [payload JSON] [--admin]

Runs server.js from the page's directory against its draft data, and prints the
result and what it logged. Data it writes is written, to draft.`

// pageBackendDir is --dir, resolved, and the page registered for it.
func pageBackendDir(ctx context.Context, db *store.DB, dir string) (store.SharePage, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return store.SharePage{}, err
	}
	return pageFor(ctx, db, abs)
}

func pageData(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(pageDataUsage)
		return nil
	}
	verb := args[0]
	fs := flag.NewFlagSet("page data "+verb, flag.ContinueOnError)
	live := fs.Bool("live", false, "the published page's data rather than the draft's")
	dir := fs.String("dir", ".", "the page's directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ns := store.PageDataDraft
	if *live {
		ns = store.PageDataLive
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	page, err := pageBackendDir(ctx, db, *dir)
	if err != nil {
		return err
	}

	switch verb {
	case "get":
		if fs.NArg() > 1 {
			return errors.New(pageDataUsage)
		}
		values, _, err := httpapi.PageDataValues(ctx, db, page, ns)
		if err != nil {
			return err
		}
		if fs.NArg() == 1 {
			v, ok := values[fs.Arg(0)]
			if !ok {
				return fmt.Errorf("vibepanel.json declares no data %q", fs.Arg(0))
			}
			return printJSON(v)
		}
		return printJSON(values)
	case "set", "reset":
		want := 2
		if verb == "reset" {
			want = 1
		}
		if fs.NArg() != want {
			return errors.New(pageDataUsage)
		}
		var value any
		if verb == "set" {
			value = parseLoose(fs.Arg(1))
		}
		got, err := httpapi.ChangePageData(ctx, db, page, ns, verb, fs.Arg(0), value)
		if err != nil {
			return err
		}
		if err := printJSON(got); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "A panel that is running shows this within five seconds.")
		return nil
	}
	return fmt.Errorf("unknown page data command %q\n\n%s", verb, pageDataUsage)
}

func pageRun(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(pageRunUsage)
		return nil
	}
	what := args[0]
	fs := flag.NewFlagSet("page run "+what, flag.ContinueOnError)
	dir := fs.String("dir", ".", "the page's directory")
	fixture := fs.String("fixture", "", "transform: the snapshot in fixtures/<name>.json")
	admin := fs.Bool("admin", false, "action: run it as an admin, for an action both may run")
	// Flags may come after the positional arguments, as the usage shows them.
	var positional []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	var name string
	var payload map[string]any
	switch what {
	case "transform", "schedule":
		if len(positional) != 0 {
			return errors.New(pageRunUsage)
		}
	case "action":
		if len(positional) < 1 || len(positional) > 2 {
			return errors.New(pageRunUsage)
		}
		name = positional[0]
		if len(positional) == 2 {
			if err := json.Unmarshal([]byte(positional[1]), &payload); err != nil || payload == nil {
				return errors.New("the payload is one JSON object, like '{\"name\":\"ann\"}'")
			}
		}
	default:
		return fmt.Errorf("unknown page run command %q\n\n%s", what, pageRunUsage)
	}

	var snapshot json.RawMessage
	if *fixture != "" {
		if !pages.FixtureName(*fixture) {
			return errors.New("fixture names are lower-case letters, digits and dashes")
		}
		raw, err := os.ReadFile(filepath.Join(*dir, "fixtures", *fixture+".json")) //nolint:gosec // a name checked above, in the user's own page
		if err != nil {
			return err
		}
		var f struct {
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("fixtures/%s.json: %w", *fixture, err)
		}
		snapshot = f.Snapshot
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	page, err := pageBackendDir(ctx, db, *dir)
	if err != nil {
		return err
	}
	run, err := httpapi.RunPageServer(ctx, db, page, what, name, payload, snapshot, *admin)
	for _, l := range run.Log {
		fmt.Fprintf(os.Stderr, "%s %s\n", l.Level, l.Text)
	}
	if err != nil {
		return err
	}
	if what == "transform" {
		fmt.Fprintln(os.Stderr, "ctx.sources is empty here: sources are fetched by the running panel.")
	}
	return printJSON(run.Result)
}

func pageDocs(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: vibepanel page docs")
	}
	_, err := os.Stdout.Write(pages.Architecture)
	return err
}

// printJSON prints indented JSON. Map keys come out sorted, so two runs diff
// cleanly.
func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// parseLoose reads a value typed on a command line: JSON when it is JSON, so
// 3, true and ["a","b"] are what they look like, and a string otherwise, so
// hello needs no quotes.
func parseLoose(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}
