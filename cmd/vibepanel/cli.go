package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// The commands that operate the running panel (docs/cli.md): everything
// the settings page and the sidebar can do, from a shell, for a panel in a
// container where there is no browser at hand. Each is one or two calls on
// the panel's API through client.go; the output is a table for a person
// and, with --json, the panel's answer for a script.

const statusUsage = `usage: vibepanel status [--json]

  the projects and sessions as the sidebar shows them: state, title, pid, live`

func cmdStatus(args []string) error {
	if wantsHelp(args) {
		fmt.Println(statusUsage)
		return nil
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the panel's state as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	c, err := openClient(ctx, fs.Args())
	if err != nil {
		return err
	}
	st, err := c.state(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(st)
	}
	printState(os.Stdout, st)
	return nil
}

func printState(w io.Writer, st panelState) {
	live := map[string]bool{}
	for _, id := range st.Live {
		live[id] = true
	}
	frozen := map[string]bool{}
	for _, id := range st.Frozen {
		frozen[id] = true
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, p := range st.Projects {
		pin := ""
		if p.Pinned {
			pin = " *"
		}
		fmt.Fprintf(tw, "%s\t%s%s\t%s\n", p.ID, p.Name, pin, p.Path)
		for _, s := range st.Sessions {
			if s.ProjectID != p.ID {
				continue
			}
			mark := map[string]string{"working": "●", "waiting": "▲", "done": "✓"}[string(s.State)]
			if mark == "" {
				mark = "○"
			}
			flags := []string{}
			if s.Exited {
				flags = append(flags, "exited "+strconv.Itoa(s.ExitStatus))
			}
			if !live[s.ID] {
				flags = append(flags, "not live")
			}
			if frozen[s.ID] {
				flags = append(flags, "frozen")
			}
			if s.Pinned {
				flags = append(flags, "pinned")
			}
			if s.Scratch {
				flags = append(flags, "shell")
			}
			fmt.Fprintf(tw, "  %s\t%s %s\t%s\t%s\n", s.ID, mark, s.State, s.Title, strings.Join(flags, ", "))
		}
	}
	if len(st.Archived) > 0 {
		fmt.Fprintf(tw, "archived:\t%d project(s)\t`vibepanel project ls` lists them\n", len(st.Archived))
	}
	_ = tw.Flush()
}

// parseInterspersed is flag.Parse with flags allowed after positional
// arguments: `project add <dir> --name N` reads as a person writes it. The
// standard parser stops at the first positional; this collects it and
// parses on. Everything after `--` is positional, as for a command line.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// ─── project, through the panel ───────────────────────────────────────────

// projectViaAPI runs a project subcommand against the running panel. handled
// is false when the panel is not running and the subcommand has an offline
// implementation to fall back to (cmdProject's own switch), and offline is
// then the arguments in the shape that switch reads: it predates names and
// positionals, and takes --path, --name and --id.
func projectViaAPI(ctx context.Context, sub string, rest []string) (handled bool, offline []string, err error) {
	fs := flag.NewFlagSet("project "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the panel's answer as JSON")
	var name, path, pid string
	auto := false
	switch sub {
	case "add":
		fs.StringVar(&name, "name", "", "display name (defaults to the directory's base name)")
		fs.StringVar(&path, "path", "", "project directory")
	case "archive", "restore", "rm":
		fs.StringVar(&pid, "id", "", "project id (or give the project as the argument)")
	case "order":
		fs.BoolVar(&auto, "auto", false, "back to sorting by activity")
	}
	args, err := parseInterspersed(fs, rest)
	if err != nil {
		return true, nil, err
	}
	c, err := openClient(ctx, nil)
	if err != nil {
		return true, nil, err
	}
	fallback := sub == "ls" || sub == "add" || sub == "archive" || sub == "restore" || sub == "rm"
	down := func(err error) (bool, []string, error) {
		if (errors.Is(err, errPanelDown) || errors.Is(err, errNoAccount)) && fallback {
			fmt.Fprintf(os.Stderr, "%s; working on the database directly\n", err)
			switch sub {
			case "add":
				if path == "" && len(args) == 1 {
					path = args[0]
				}
				return false, []string{"--path", path, "--name", name}, nil
			case "archive", "restore", "rm":
				if pid == "" && len(args) == 1 {
					pid = args[0]
				}
				return false, []string{"--id", pid}, nil
			}
			return false, nil, nil
		}
		return true, nil, err
	}
	switch sub {
	case "ls":
		st, err := c.state(ctx)
		if err != nil {
			return down(err)
		}
		if *asJSON {
			return true, nil, printJSON(map[string]any{"projects": st.Projects, "archived": st.Archived})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, p := range st.Projects {
			pin := ""
			if p.Pinned {
				pin = " *"
			}
			n := 0
			for _, s := range st.Sessions {
				if s.ProjectID == p.ID && !s.Scratch {
					n++
				}
			}
			fmt.Fprintf(tw, "%s\t%s%s\t%s\t%d session(s)\n", p.ID, p.Name, pin, p.Path, n)
		}
		for _, p := range st.Archived {
			fmt.Fprintf(tw, "%s\t%s\t%s\tarchived\n", p.ID, p.Name, p.Path)
		}
		return true, nil, tw.Flush()
	case "add":
		if path == "" && len(args) == 1 {
			path = args[0]
		}
		if path == "" {
			return true, nil, errors.New("usage: vibepanel project add <dir> [--name N]")
		}
		var p store.Project
		if err := c.do(ctx, http.MethodPost, "/api/projects", map[string]any{"name": name, "path": path}, &p); err != nil {
			return down(err)
		}
		if *asJSON {
			return true, nil, printJSON(p)
		}
		fmt.Printf("created project %s  %s  %s\n", p.ID, p.Name, p.Path)
		return true, nil, nil
	case "rename":
		if len(args) != 2 {
			return true, nil, errors.New("usage: vibepanel project rename <project> <new name>")
		}
		p, err := c.project(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		if err := c.do(ctx, http.MethodPatch, "/api/projects/"+p.ID, map[string]any{"name": args[1]}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("renamed %s: %s → %s\n", p.ID, p.Name, args[1])
		return true, nil, nil
	case "pin", "unpin":
		if len(args) != 1 {
			return true, nil, fmt.Errorf("usage: vibepanel project %s <project>", sub)
		}
		p, err := c.project(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		if err := c.do(ctx, http.MethodPatch, "/api/projects/"+p.ID, map[string]any{"pinned": sub == "pin"}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("%sned %s\n", sub, p.Name)
		return true, nil, nil
	case "order":
		if auto {
			if err := c.do(ctx, http.MethodPost, "/api/projects/reorder", map[string]any{"auto": true}, nil); err != nil {
				return true, nil, err
			}
			fmt.Println("projects sorted by activity again")
			return true, nil, nil
		}
		if len(args) == 0 {
			return true, nil, errors.New("usage: vibepanel project order <project> <project> … | --auto")
		}
		ids := make([]string, 0, len(args))
		for _, ref := range args {
			p, err := c.project(ctx, ref)
			if err != nil {
				return true, nil, err
			}
			ids = append(ids, p.ID)
		}
		if err := c.do(ctx, http.MethodPost, "/api/projects/reorder", map[string]any{"ids": ids}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("ordered %d project(s); the rest keep their places after them\n", len(ids))
		return true, nil, nil
	case "archive", "restore", "rm":
		ref := pid
		if ref == "" && len(args) == 1 {
			ref = args[0]
		}
		if ref == "" {
			return true, nil, fmt.Errorf("usage: vibepanel project %s <project>", sub)
		}
		st, err := c.state(ctx)
		if err != nil {
			return down(err)
		}
		var target store.Project
		if sub == "restore" {
			for _, a := range st.Archived {
				if a.ID == ref || a.Name == ref {
					target = store.Project{ID: a.ID, Name: a.Name}
				}
			}
			if target.ID == "" {
				return true, nil, fmt.Errorf("no archived project %q", ref)
			}
		} else if target, err = pickProject(st.Projects, ref); err != nil {
			return true, nil, err
		}
		switch sub {
		case "archive":
			err = c.do(ctx, http.MethodPost, "/api/projects/"+target.ID+"/archive", map[string]any{}, nil)
		case "restore":
			err = c.do(ctx, http.MethodPost, "/api/projects/"+target.ID+"/restore", map[string]any{}, nil)
		case "rm":
			err = c.do(ctx, http.MethodDelete, "/api/projects/"+target.ID, nil, nil)
		}
		if err != nil {
			return true, nil, err
		}
		fmt.Printf("%s: %s (%s)\n", map[string]string{"archive": "archived", "restore": "restored", "rm": "deleted"}[sub], target.Name, target.ID)
		return true, nil, nil
	}
	return false, rest, nil
}

// ─── session, through the panel ───────────────────────────────────────────

func sessionViaAPI(ctx context.Context, sub string, rest []string) (handled bool, offline []string, err error) {
	fs := flag.NewFlagSet("session "+sub, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the panel's answer as JSON")
	var project, title, sid, profile, level, choices, caption string
	var cols, rows int
	enter := false
	switch sub {
	case "attach":
		fs.StringVar(&caption, "caption", "", "a line with the file")
	case "notify":
		fs.StringVar(&level, "level", "info", "info, warn or ask")
		fs.StringVar(&choices, "choices", "", "comma-separated buttons for the phone")
	case "new":
		fs.StringVar(&project, "project", "", "project (id, name or directory)")
		fs.StringVar(&title, "title", "", "initial title")
		fs.StringVar(&profile, "profile", "", "launch profile id or name (the offline path only; the panel's launch picker otherwise)")
		fs.IntVar(&cols, "cols", 120, "initial grid width")
		fs.IntVar(&rows, "rows", 32, "initial grid height")
	case "kill":
		fs.StringVar(&sid, "id", "", "session id (or give the session as the argument)")
	case "send":
		fs.BoolVar(&enter, "enter", false, "press Enter after the text")
	}
	args, err := parseInterspersed(fs, rest)
	if err != nil {
		return true, nil, err
	}
	c, err := openClient(ctx, nil)
	if err != nil {
		return true, nil, err
	}
	fallback := sub == "ls" || sub == "new" || sub == "kill"
	down := func(err error) (bool, []string, error) {
		if (errors.Is(err, errPanelDown) || errors.Is(err, errNoAccount)) && fallback {
			fmt.Fprintf(os.Stderr, "%s; working on the database and tmux directly\n", err)
			switch sub {
			case "new":
				out := []string{"--project", project, "--title", title, "--profile", profile,
					"--cols", strconv.Itoa(cols), "--rows", strconv.Itoa(rows)}
				if len(args) > 0 {
					out = append(append(out, "--"), args...)
				}
				return false, out, nil
			case "kill":
				if sid == "" && len(args) == 1 {
					sid = args[0]
				}
				return false, []string{"--id", sid}, nil
			}
			return false, nil, nil
		}
		return true, nil, err
	}
	if sub == "new" && profile != "" {
		if _, perr := c.state(ctx); perr == nil {
			return true, nil, errors.New("--profile is for the offline path; through the panel a session takes the project's default command, or the one after --")
		}
	}
	switch sub {
	case "ls":
		st, err := c.state(ctx)
		if err != nil {
			return down(err)
		}
		if *asJSON {
			return true, nil, printJSON(map[string]any{"sessions": st.Sessions, "live": st.Live, "frozen": st.Frozen})
		}
		printState(os.Stdout, st)
		return true, nil, nil
	case "new":
		if project == "" {
			return true, nil, errors.New("usage: vibepanel session new --project <project> [--title T] [-- command…]")
		}
		if cols <= 0 || rows <= 0 || cols > 500 || rows > 200 {
			return true, nil, errors.New("--cols and --rows are a grid: 1-500 by 1-200")
		}
		p, err := c.project(ctx, project)
		if err != nil {
			if errors.Is(err, errPanelDown) || errors.Is(err, errNoAccount) {
				return down(err)
			}
			return true, nil, err
		}
		body := map[string]any{"projectId": p.ID, "title": title, "command": args, "cols": cols, "rows": rows}
		var s store.Session
		if err := c.do(ctx, http.MethodPost, "/api/sessions", body, &s); err != nil {
			return down(err)
		}
		if *asJSON {
			return true, nil, printJSON(s)
		}
		fmt.Printf("created session %s (tmux %s) in %s\n", s.ID, s.TmuxName, p.Path)
		return true, nil, nil
	case "rename":
		if len(args) != 2 {
			return true, nil, errors.New("usage: vibepanel session rename <session> <new title>")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		if err := c.do(ctx, http.MethodPatch, "/api/sessions/"+s.ID, map[string]any{"title": args[1]}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("renamed %s: %s → %s\n", s.ID, s.Title, args[1])
		return true, nil, nil
	case "pin", "unpin":
		if len(args) != 1 {
			return true, nil, fmt.Errorf("usage: vibepanel session %s <session>", sub)
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		if err := c.do(ctx, http.MethodPatch, "/api/sessions/"+s.ID, map[string]any{"pinned": sub == "pin"}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("%sned %s\n", sub, s.Title)
		return true, nil, nil
	case "state":
		if len(args) != 2 {
			return true, nil, errors.New("usage: vibepanel session state <session> <working|waiting|done>")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		if err := c.do(ctx, http.MethodPatch, "/api/sessions/"+s.ID, map[string]any{"state": args[1]}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("%s is %s\n", s.Title, args[1])
		return true, nil, nil
	case "restart":
		if len(args) != 1 {
			return true, nil, errors.New("usage: vibepanel session restart <session>")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		// The panel restarts in place and answers 204: the same session,
		// its command run again in its pane. Nothing in the answer to print,
		// so the row is read back for the receipt.
		if err := c.do(ctx, http.MethodPost, "/api/sessions/"+s.ID+"/restart", map[string]any{}, nil); err != nil {
			return true, nil, err
		}
		fresh, err := c.session(ctx, s.ID)
		if err != nil {
			return true, nil, err
		}
		if *asJSON {
			return true, nil, printJSON(fresh)
		}
		fmt.Printf("restarted %s (%s): %s\n", fresh.Title, fresh.ID, strings.Join(fresh.LaunchCommand, " "))
		return true, nil, nil
	case "kill":
		ref := sid
		if ref == "" && len(args) == 1 {
			ref = args[0]
		}
		if ref == "" {
			return true, nil, errors.New("usage: vibepanel session kill <session>")
		}
		s, err := c.session(ctx, ref)
		if err != nil {
			if errors.Is(err, errPanelDown) || errors.Is(err, errNoAccount) {
				return down(err)
			}
			return true, nil, err
		}
		if err := c.do(ctx, http.MethodDelete, "/api/sessions/"+s.ID, nil, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("killed session %s (%s)\n", s.ID, s.Title)
		return true, nil, nil
	case "send":
		if len(args) < 1 {
			return true, nil, errors.New("usage: vibepanel session send <session> [text…] [--enter]   (text from stdin when none is given)")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		text := strings.Join(args[1:], " ")
		if len(args) == 1 {
			raw, rerr := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
			if rerr != nil {
				return true, nil, rerr
			}
			text = string(raw)
		}
		if err := c.do(ctx, http.MethodPost, "/api/sessions/"+s.ID+"/input", map[string]any{"text": text, "submit": enter}, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("sent %d byte(s) to %s\n", len(text), s.Title)
		return true, nil, nil
	case "screen":
		if len(args) != 1 {
			return true, nil, errors.New("usage: vibepanel session screen <session>")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/sessions/"+s.ID+"/screen", nil, &out); err != nil {
			return true, nil, err
		}
		if *asJSON {
			return true, nil, printJSON(out)
		}
		fmt.Print(strings.TrimRight(out.Text, "\n") + "\n")
		return true, nil, nil
	case "say", "notify":
		// Speaking for a session from outside: the same routes as its own
		// `me say` and `me notify`, under the owner's credential.
		if len(args) < 2 {
			return true, nil, fmt.Errorf("usage: vibepanel session %s <session> <text> [--level L] [--choices a,b]", sub)
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		text := strings.Join(args[1:], " ")
		if sub == "say" {
			if err := c.do(ctx, http.MethodPost, "/api/sessions/"+s.ID+"/say", map[string]any{"text": text}, nil); err != nil {
				return true, nil, err
			}
			fmt.Printf("%s said it\n", s.Title)
			return true, nil, nil
		}
		list := []string{}
		for _, ch := range strings.Split(choices, ",") {
			if ch = strings.TrimSpace(ch); ch != "" {
				list = append(list, ch)
			}
		}
		var out struct {
			Contacts int `json:"contacts"`
		}
		if err := c.do(ctx, http.MethodPost, "/api/sessions/"+s.ID+"/notify", map[string]any{"text": text, "level": level, "choices": list}, &out); err != nil {
			return true, nil, err
		}
		fmt.Printf("notified for %s: every open page, %d contact(s)\n", s.Title, out.Contacts)
		return true, nil, nil
	case "attach":
		if len(args) != 2 {
			return true, nil, errors.New("usage: vibepanel session attach <session> <file> [--caption \"...\"]")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		var out attachResult
		if err := uploadFile(ctx, c.http, c.base+"/api/sessions/"+s.ID+"/attach", map[string]string{"Authorization": "Bearer " + c.token}, args[1], caption, &out); err != nil {
			return true, nil, err
		}
		fmt.Printf("attached %s (%s) to %s: %d contact(s); %s\n", out.Name, out.Size, s.Title, out.Contacts, out.URL)
		return true, nil, nil
	case "attachments":
		if len(args) != 1 {
			return true, nil, errors.New("usage: vibepanel session attachments <session>")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		var list []attachResult
		if err := c.do(ctx, http.MethodGet, "/api/sessions/"+s.ID+"/attachments", nil, &list); err != nil {
			return true, nil, err
		}
		if *asJSON {
			return true, nil, printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, a := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Name, a.Size, a.URL)
		}
		return true, nil, tw.Flush()
	case "info":
		// What the session says about itself, set by the owner: the same
		// fields `vibepanel me` sets from inside.
		if len(args) < 2 {
			return true, nil, errors.New("usage: vibepanel session info <session> summary <text> | progress <0-100> | labels a,b | clear")
		}
		s, err := c.session(ctx, args[0])
		if err != nil {
			return true, nil, err
		}
		patch := map[string]any{}
		switch args[1] {
		case "summary":
			patch["summary"] = strings.Join(args[2:], " ")
		case "progress":
			n, perr := strconv.Atoi(strings.TrimSuffix(strings.Join(args[2:], ""), "%"))
			if perr != nil || n < 0 || n > 100 {
				return true, nil, errors.New("progress is a number from 0 to 100")
			}
			patch["progress"] = n
		case "labels":
			patch["labels"] = strings.FieldsFunc(strings.Join(args[2:], ","), func(r rune) bool { return r == ',' || r == ' ' })
		case "clear":
			patch["summary"], patch["progress"], patch["labels"] = "", -1, []string{}
		default:
			return true, nil, errors.New("session info takes summary, progress, labels or clear")
		}
		if err := c.do(ctx, http.MethodPatch, "/api/sessions/"+s.ID, patch, nil); err != nil {
			return true, nil, err
		}
		fmt.Printf("info of %s: %s\n", s.Title, args[1])
		return true, nil, nil
	case "restore":
		var out any
		if err := c.do(ctx, http.MethodPost, "/api/sessions/restore", map[string]any{}, &out); err != nil {
			return true, nil, err
		}
		if *asJSON {
			return true, nil, printJSON(out)
		}
		fmt.Println("asked the panel to rebuild the sessions a restart took")
		return true, nil, nil
	}
	return false, rest, nil
}

// ─── notes and todos ──────────────────────────────────────────────────────

const noteUsage = `usage: vibepanel note <get|set> <project> [text]

  get    print the project's note
  set    replace it with the text, or with stdin when no text is given`

func cmdNote(args []string) error {
	if wantsHelp(args) {
		fmt.Println(noteUsage)
		return nil
	}
	if len(args) < 2 {
		return errors.New(noteUsage)
	}
	ctx := context.Background()
	c, err := openClient(ctx, nil)
	if err != nil {
		return err
	}
	p, err := c.project(ctx, args[1])
	if err != nil {
		return err
	}
	switch args[0] {
	case "get":
		var note store.Note
		if err := c.do(ctx, http.MethodGet, "/api/projects/"+p.ID+"/notes", nil, &note); err != nil {
			return err
		}
		fmt.Print(note.Content)
		if note.Content != "" && !strings.HasSuffix(note.Content, "\n") {
			fmt.Println()
		}
		return nil
	case "set":
		content := strings.Join(args[2:], " ")
		if len(args) == 2 {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return err
			}
			content = string(raw)
		}
		if err := c.do(ctx, http.MethodPut, "/api/projects/"+p.ID+"/notes", map[string]any{"content": content}, nil); err != nil {
			return err
		}
		fmt.Printf("note of %s: %d byte(s)\n", p.Name, len(content))
		return nil
	}
	return errors.New(noteUsage)
}

const todoUsage = `usage: vibepanel todo <ls|add|done|undo|rm> <project> [text|todo id]

  A project's todos are read by plugins (read:todos) and the chat assistant;
  the panel's own pages no longer draw them.

  ls           the project's todos
  add          add one with the text
  done, undo   tick or untick one, by id or by its text's start
  rm           remove one`

func cmdTodo(args []string) error {
	if wantsHelp(args) {
		fmt.Println(todoUsage)
		return nil
	}
	if len(args) < 2 {
		return errors.New(todoUsage)
	}
	ctx := context.Background()
	c, err := openClient(ctx, nil)
	if err != nil {
		return err
	}
	p, err := c.project(ctx, args[1])
	if err != nil {
		return err
	}
	list := func() ([]store.Todo, error) {
		var out []store.Todo
		err := c.do(ctx, http.MethodGet, "/api/projects/"+p.ID+"/todos", nil, &out)
		return out, err
	}
	pick := func(ref string) (store.Todo, error) {
		todos, err := list()
		if err != nil {
			return store.Todo{}, err
		}
		var prefix []store.Todo
		for _, t := range todos {
			if t.ID == ref {
				return t, nil
			}
			if strings.HasPrefix(strings.ToLower(t.Text), strings.ToLower(ref)) {
				prefix = append(prefix, t)
			}
		}
		if len(prefix) == 1 {
			return prefix[0], nil
		}
		if len(prefix) == 0 {
			return store.Todo{}, fmt.Errorf("no todo %q in %s", ref, p.Name)
		}
		return store.Todo{}, fmt.Errorf("%q names %d todos; use the id", ref, len(prefix))
	}
	switch args[0] {
	case "ls":
		todos, err := list()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, t := range todos {
			box := "[ ]"
			if t.Done {
				box = "[x]"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", t.ID, box, t.Text)
		}
		return tw.Flush()
	case "add":
		text := strings.TrimSpace(strings.Join(args[2:], " "))
		if text == "" {
			return errors.New("usage: vibepanel todo add <project> <text>")
		}
		var t store.Todo
		if err := c.do(ctx, http.MethodPost, "/api/projects/"+p.ID+"/todos", map[string]any{"text": text}, &t); err != nil {
			return err
		}
		fmt.Printf("added %s: %s\n", t.ID, t.Text)
		return nil
	case "done", "undo", "rm":
		if len(args) != 3 {
			return fmt.Errorf("usage: vibepanel todo %s <project> <todo>", args[0])
		}
		t, err := pick(args[2])
		if err != nil {
			return err
		}
		switch args[0] {
		case "rm":
			err = c.do(ctx, http.MethodDelete, "/api/todos/"+t.ID, nil, nil)
		default:
			err = c.do(ctx, http.MethodPatch, "/api/todos/"+t.ID, map[string]any{"done": args[0] == "done"}, nil)
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", map[string]string{"done": "ticked", "undo": "unticked", "rm": "removed"}[args[0]], t.Text)
		return nil
	}
	return errors.New(todoUsage)
}

// ─── tokens, the activity log, settings ───────────────────────────────────

const tokenUsage = `usage: vibepanel token <ls|create|revoke> [name|id]

  ls        the API tokens: name, prefix, last use
  create    mint one with the name; the token is printed once
  revoke    delete one by id or by name`

func cmdToken(args []string) error {
	if wantsHelp(args) {
		fmt.Println(tokenUsage)
		return nil
	}
	if len(args) < 1 {
		return errors.New(tokenUsage)
	}
	ctx := context.Background()
	c, err := openClient(ctx, nil)
	if err != nil {
		return err
	}
	switch args[0] {
	case "ls":
		var list []store.APIToken
		if err := c.do(ctx, http.MethodGet, "/api/settings/tokens", nil, &list); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, t := range list {
			used := "never used"
			if t.LastUsedAt > 0 {
				used = "last used " + time.Unix(t.LastUsedAt, 0).Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s…\t%s\n", t.ID, t.Name, t.Prefix, used)
		}
		return tw.Flush()
	case "create":
		if len(args) != 2 {
			return errors.New("usage: vibepanel token create <name>")
		}
		var made struct {
			Token  string `json:"token"`
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
		}
		if err := c.do(ctx, http.MethodPost, "/api/settings/tokens", map[string]any{"name": args[1]}, &made); err != nil {
			return err
		}
		fmt.Printf("%s\n", made.Token)
		fmt.Fprintf(os.Stderr, "token %s (%s…) created; shown once\n", made.ID, made.Prefix)
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: vibepanel token revoke <id|name>")
		}
		var list []store.APIToken
		if err := c.do(ctx, http.MethodGet, "/api/settings/tokens", nil, &list); err != nil {
			return err
		}
		var target store.APIToken
		for _, t := range list {
			if t.ID == args[1] || t.Name == args[1] {
				if target.ID != "" {
					return fmt.Errorf("%q names more than one token; use the id", args[1])
				}
				target = t
			}
		}
		if target.ID == "" {
			return fmt.Errorf("no token %q", args[1])
		}
		if err := c.do(ctx, http.MethodDelete, "/api/settings/tokens/"+target.ID, nil, nil); err != nil {
			return err
		}
		fmt.Printf("revoked %s (%s)\n", target.Name, target.ID)
		if target.Name == "cli" {
			_ = os.Remove(c.tokenPath())
			fmt.Println("that was the CLI's own token; the next command mints a new one")
		}
		return nil
	}
	return errors.New(tokenUsage)
}

const auditUsage = `usage: vibepanel audit [-n 50] [--json]

  the activity log, newest last`

func cmdAudit(args []string) error {
	if wantsHelp(args) {
		fmt.Println(auditUsage)
		return nil
	}
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	n := fs.Int("n", 50, "how many entries")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	c, err := openClient(ctx, fs.Args())
	if err != nil {
		return err
	}
	var entries []store.AuditEntry
	if err := c.do(ctx, http.MethodGet, "/api/settings/audit", nil, &entries); err != nil {
		return err
	}
	// The panel answers newest first; a log reads oldest first, so the last
	// line on the screen is the latest thing that happened.
	if len(entries) > *n {
		entries = entries[:*n]
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if *asJSON {
		return printJSON(entries)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", time.Unix(e.At, 0).Format("01-02 15:04:05"), e.Event, e.Username, e.IP, e.Detail)
	}
	return tw.Flush()
}

const settingsUsage = `usage: vibepanel settings <archive-idle|timezone|share-origin|share-password> <value>

  archive-idle <days>                archive a project idle for that many days; 0 never
  timezone <zone>                    the panel's time zone, e.g. Asia/Shanghai
  share-origin <origin|clear>        dedicated origin for share links, e.g. https://share.example.com
  share-origin check <origin>        test DNS, reverse proxy and HTTPS for a candidate share domain
  share-origin setup <origin>        configure HTTPS + proxy (ACME or nginx+certbot) and save it
  share-password status              show whether a shared password is set and how many public links exist
  share-password set                 set or replace the global shared password (reads from stdin/prompt)
  share-password apply-all           apply the global shared password to all existing public links
  share-password clear               clear the global shared password (when no links depend on it)`

// shareOriginRecovery is printed whenever the command line makes a name the
// share origin.
//
// The panel refuses its own address, but it can only refuse an address it
// knows is its own, and this command reaches it on loopback: a panel with no
// domain configured and nobody signed in at the moment has nothing to compare
// against. Given its own public name it then serves that name as a share
// host, which is every page of the panel answering "this link no longer
// works". The way back is one command from a shell on the machine, and the
// moment to say so is before it is needed.
const shareOriginRecovery = "that name now serves share links only. If it is the address you open the panel at, undo it from this machine: vibepanel settings share-origin clear\n"

func cmdSettings(args []string) error {
	if wantsHelp(args) {
		fmt.Println(settingsUsage)
		return nil
	}
	if len(args) < 2 || len(args) > 3 {
		return errors.New(settingsUsage)
	}
	ctx := context.Background()
	c, err := openClient(ctx, nil)
	if err != nil {
		return err
	}
	switch args[0] {
	case "archive-idle":
		if len(args) != 2 {
			return errors.New(settingsUsage)
		}
		days, err := strconv.Atoi(args[1])
		if err != nil || days < 0 {
			return errors.New("archive-idle takes a number of days")
		}
		if err := c.do(ctx, http.MethodPut, "/api/settings/archive", map[string]any{"days": days}, nil); err != nil {
			return err
		}
		fmt.Printf("projects idle for %d day(s) are archived\n", days)
		return nil
	case "timezone":
		if len(args) != 2 {
			return errors.New(settingsUsage)
		}
		if err := c.do(ctx, http.MethodPut, "/api/settings/timezone", map[string]any{"zone": args[1]}, nil); err != nil {
			return err
		}
		fmt.Printf("time zone: %s\n", args[1])
		return nil
	case "share-origin":
		if len(args) == 3 && args[1] == "check" {
			var res struct {
				Origin         string   `json:"origin"`
				Hostname       string   `json:"hostname"`
				Ready          bool     `json:"ready"`
				Status         string   `json:"status"`
				DNSOK          bool     `json:"dnsOk"`
				DNSIPs         []string `json:"dnsIps"`
				HostIPs        []string `json:"hostIps"`
				DNSMatchesHost bool     `json:"dnsMatchesHost"`
				DNSError       string   `json:"dnsError"`
				Reachable      bool     `json:"reachable"`
				HostForwarded  bool     `json:"hostForwarded"`
				SeenHost       string   `json:"seenHost"`
				ReachError     string   `json:"reachError"`
				TLSRequired    bool     `json:"tlsRequired"`
				TLSOK          bool     `json:"tlsOk"`
				TLSIssuer      string   `json:"tlsIssuer"`
				TLSNotAfter    int64    `json:"tlsNotAfter"`
				TLSError       string   `json:"tlsError"`
				Provision      struct {
					Mode         string `json:"mode"`
					CanProvision bool   `json:"canProvision"`
					Reason       string `json:"reason"`
				} `json:"provision"`
			}
			if err := c.do(ctx, http.MethodPost, "/api/settings/sharing/check", map[string]any{"origin": args[2]}, &res); err != nil {
				return err
			}
			fmt.Printf("origin:    %s\n", res.Origin)
			fmt.Printf("status:    %s (ready=%v)\n", res.Status, res.Ready)
			if res.DNSOK {
				fmt.Printf("dns:       ok (%s; host=%s; match=%v)\n", strings.Join(res.DNSIPs, ","), strings.Join(res.HostIPs, ","), res.DNSMatchesHost)
			} else {
				fmt.Printf("dns:       missing (%s; point A record to %s)\n", res.DNSError, strings.Join(res.HostIPs, ","))
			}
			if res.TLSRequired {
				if res.TLSOK {
					fmt.Printf("https:     ok (issuer=%s, expires=%s)\n", res.TLSIssuer, time.Unix(res.TLSNotAfter, 0).Format("2006-01-02"))
				} else if res.TLSError != "" {
					fmt.Printf("https:     failed (%s)\n", res.TLSError)
				} else {
					fmt.Println("https:     not ready")
				}
			}
			if res.Reachable {
				fmt.Printf("proxy:     reached this panel (Host=%s, forwarded=%v)\n", res.SeenHost, res.HostForwarded)
			} else if res.ReachError != "" {
				fmt.Printf("proxy:     %s\n", res.ReachError)
			}
			if res.Provision.CanProvision {
				fmt.Printf("auto-tls:  available (%s)\n", res.Provision.Mode)
			} else {
				fmt.Printf("auto-tls:  manual (%s)\n", res.Provision.Reason)
			}
			return nil
		}
		if len(args) == 3 && args[1] == "setup" {
			var res struct {
				Origin      string `json:"origin"`
				Mode        string `json:"mode"`
				ShareOrigin string `json:"shareOrigin"`
				Check       struct {
					Ready  bool   `json:"ready"`
					Status string `json:"status"`
				} `json:"check"`
			}
			if err := c.do(ctx, http.MethodPost, "/api/settings/sharing/provision", map[string]any{"origin": args[2]}, &res); err != nil {
				return err
			}
			fmt.Printf("provisioned %s via %s (status=%s, ready=%v)\n", res.Origin, res.Mode, res.Check.Status, res.Check.Ready)
			if res.ShareOrigin != res.Origin {
				// The panel saves the origin only once the domain is shown to
				// reach it; saying "provisioned" and stopping there would
				// read as done.
				fmt.Printf("not saved: the check did not pass. Fix what `vibepanel settings share-origin check %s` reports, then set it with `vibepanel settings share-origin %s`\n", res.Origin, res.Origin)
				return nil
			}
			fmt.Print(shareOriginRecovery)
			return nil
		}
		if len(args) != 2 {
			return errors.New(settingsUsage)
		}
		val := strings.TrimSpace(args[1])
		if val == "clear" {
			val = ""
		}
		var out struct {
			ShareOrigin string `json:"shareOrigin"`
		}
		if err := c.do(ctx, http.MethodPut, "/api/settings/sharing", map[string]any{"shareOrigin": val}, &out); err != nil {
			return err
		}
		if out.ShareOrigin == "" {
			fmt.Println("share origin: (panel address)")
		} else {
			fmt.Printf("share origin: %s\n", out.ShareOrigin)
			fmt.Print(shareOriginRecovery)
		}
		return nil
	case "share-password":
		if len(args) != 2 {
			return errors.New(settingsUsage)
		}
		switch args[1] {
		case "status":
			var st struct {
				SharedPasswordSet bool `json:"sharedPasswordSet"`
				PublicLinkCount   int  `json:"publicLinkCount"`
			}
			if err := c.do(ctx, http.MethodGet, "/api/settings/sharing", nil, &st); err != nil {
				return err
			}
			if st.SharedPasswordSet {
				fmt.Printf("shared password: set (public links: %d)\n", st.PublicLinkCount)
			} else {
				fmt.Printf("shared password: not set (public links: %d)\n", st.PublicLinkCount)
			}
			return nil
		case "set":
			pw, err := readPasswordLine()
			if err != nil {
				return err
			}
			if pw == "" {
				return errors.New("password cannot be empty; use `vibepanel settings share-password clear` to remove it")
			}
			var st struct {
				PublicLinkCount int `json:"publicLinkCount"`
			}
			if err := c.do(ctx, http.MethodPut, "/api/settings/sharing/password", map[string]any{"password": pw}, &st); err != nil {
				return err
			}
			fmt.Printf("shared password updated (existing public links: %d)\n", st.PublicLinkCount)
			return nil
		case "apply-all":
			var res struct {
				Updated int64 `json:"updated"`
			}
			if err := c.do(ctx, http.MethodPost, "/api/settings/sharing/password/apply-all", map[string]any{}, &res); err != nil {
				return err
			}
			fmt.Printf("applied shared password to %d public link(s)\n", res.Updated)
			return nil
		case "clear":
			if err := c.do(ctx, http.MethodPut, "/api/settings/sharing/password", map[string]any{"password": ""}, nil); err != nil {
				return err
			}
			fmt.Println("shared password cleared")
			return nil
		default:
			return errors.New(settingsUsage)
		}
	}
	return errors.New(settingsUsage)
}

func readPasswordLine() (string, error) {
	if isTerminal(os.Stdin) {
		return promptPassword("Shared share password (min 6 chars): ")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return "", err
	}
	return trimOneNewline(string(raw)), nil
}
