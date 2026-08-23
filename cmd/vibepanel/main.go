// Command vibepanel is a web console for running many parallel coding sessions.
//
// The binary is both the server and its own admin CLI. Keeping them in one
// artefact means there is never a version skew between the tool that creates a
// session and the server that serves it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // the flag package already printed usage
		}
		fmt.Fprintln(os.Stderr, "vibepanel:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Subcommands are matched before flag parsing so that `vibepanel project
	// add --name x` can have its own flag set rather than fighting the global
	// one.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest := args[0], args[1:]
		switch cmd {
		case "serve":
			return cmdServe(rest)
		case "project":
			return cmdProject(rest)
		case "session":
			return cmdSession(rest)
		case "doctor":
			return cmdDoctor(rest)
		case "version":
			fmt.Println("vibepanel", version.String())
			return nil
		default:
			return fmt.Errorf("unknown command %q (try: serve, project, session, doctor, version)", cmd)
		}
	}
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Println("vibepanel", version.String())
		return nil
	}
	return cmdServe(args)
}

// app bundles the pieces every subcommand needs.
type app struct {
	cfg  config.Config
	db   *store.DB
	tmux *tmux.Client
}

func openApp(ctx context.Context, args []string) (*app, error) {
	cfg, err := config.Load(args, os.Stderr)
	if err != nil {
		return nil, err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, err
	}
	tm := tmux.New(cfg.TmuxSocket, cfg.TmuxDir())
	if err := tm.EnsureServer(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &app{cfg: cfg, db: db, tmux: tm}, nil
}

func (a *app) Close() { a.db.Close() }

// ─── serve ────────────────────────────────────────────────────────────────

func cmdServe(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := openApp(ctx, args)
	if err != nil {
		return err
	}
	defer a.Close()

	fmt.Printf("vibepanel %s\n", version.String())
	fmt.Printf("  data dir     %s\n", a.cfg.DataDir)
	fmt.Printf("  tmux socket  %s\n", a.cfg.TmuxSocket)
	fmt.Printf("  listen       %s\n", a.cfg.Addr)
	fmt.Printf("  url          %s\n", a.cfg.PublicURL())
	if !a.cfg.PasskeysUsable() {
		fmt.Printf("  passkeys     unavailable (needs --domain with TLS, or localhost)\n")
	}

	// The HTTP server arrives in M2 together with the terminal transport.
	// Until then `serve` exists so the wiring above is exercised on every run.
	fmt.Println("\nHTTP server not implemented yet (milestone M2). Ctrl-C to exit.")
	<-ctx.Done()
	fmt.Println("\nshutting down; tmux sessions keep running")
	return nil
}

// ─── project ──────────────────────────────────────────────────────────────

func cmdProject(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: vibepanel project <add|ls|rm> [args]")
	}
	ctx := context.Background()
	sub, rest := args[0], args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("project add", flag.ContinueOnError)
		name := fs.String("name", "", "display name (defaults to the directory's base name)")
		path := fs.String("path", "", "project directory")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("project add: --path is required")
		}
		abs, err := filepath.Abs(*path)
		if err != nil {
			return fmt.Errorf("project add: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("project add: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("project add: %s is not a directory", abs)
		}
		if *name == "" {
			*name = filepath.Base(abs)
		}
		a, err := openApp(ctx, fs.Args())
		if err != nil {
			return err
		}
		defer a.Close()
		p, err := a.db.CreateProject(ctx, id.New(), *name, abs)
		if err != nil {
			return err
		}
		fmt.Printf("created project %s  %s  %s\n", p.ID, p.Name, p.Path)
		return nil

	case "ls":
		a, err := openApp(ctx, rest)
		if err != nil {
			return err
		}
		defer a.Close()
		ps, err := a.db.ListProjects(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPATH\tPINNED\tLAST ACTIVE")
		for _, p := range ps {
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\n", p.ID, p.Name, p.Path, p.Pinned, ago(p.LastActiveAt))
		}
		return w.Flush()

	case "rm":
		fs := flag.NewFlagSet("project rm", flag.ContinueOnError)
		pid := fs.String("id", "", "project id")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *pid == "" {
			return errors.New("project rm: --id is required")
		}
		a, err := openApp(ctx, fs.Args())
		if err != nil {
			return err
		}
		defer a.Close()
		// Kill the tmux sessions first. The database rows cascade away on
		// delete, and a row that vanishes while its tmux session lives on
		// leaves an orphan nothing in the UI can reach.
		sessions, err := a.db.ListProjectSessions(ctx, *pid)
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if err := a.tmux.Kill(ctx, s.TmuxName); err != nil {
				return fmt.Errorf("kill %s: %w", s.TmuxName, err)
			}
		}
		if err := a.db.DeleteProject(ctx, *pid); err != nil {
			return err
		}
		fmt.Printf("deleted project %s and %d session(s)\n", *pid, len(sessions))
		return nil
	}
	return fmt.Errorf("unknown project subcommand %q", sub)
}

// ─── session ──────────────────────────────────────────────────────────────

func cmdSession(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: vibepanel session <new|ls|kill> [args]")
	}
	ctx := context.Background()
	sub, rest := args[0], args[1:]

	switch sub {
	case "new":
		fs := flag.NewFlagSet("session new", flag.ContinueOnError)
		project := fs.String("project", "", "project id")
		title := fs.String("title", "", "initial title (otherwise taken from the pane title)")
		cols := fs.Int("cols", 120, "initial grid width")
		rows := fs.Int("rows", 32, "initial grid height")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *project == "" {
			return errors.New("session new: --project is required")
		}
		a, err := openApp(ctx, nil)
		if err != nil {
			return err
		}
		defer a.Close()

		p, err := a.db.GetProject(ctx, *project)
		if err != nil {
			return fmt.Errorf("session new: %w", err)
		}

		sid := id.New()
		tmuxName := id.TmuxName(sid)

		// The hooks that report precise state identify themselves by reading
		// this out of their environment. A session created without it simply
		// falls back to the output heuristic, which is why the hook script can
		// be installed globally without affecting anything outside the panel.
		env := []string{
			"VIBEPANEL_SESSION_ID=" + sid,
			"VIBEPANEL_PROJECT_ID=" + p.ID,
		}
		err = a.tmux.Create(ctx, tmux.CreateOptions{
			Name:    tmuxName,
			Dir:     p.Path,
			Command: fs.Args(),
			Env:     env,
			Width:   *cols,
			Height:  *rows,
		})
		if err != nil {
			return fmt.Errorf("session new: %w", err)
		}

		s, err := a.db.CreateSession(ctx, store.Session{
			ID: sid, ProjectID: p.ID, TmuxName: tmuxName,
			Title: *title, CWD: p.Path, Cols: *cols, Rows: *rows,
			State: session.StateWorking,
		})
		if err != nil {
			// The tmux session exists but we cannot track it. Removing it is
			// better than leaving a process the panel will never show again.
			_ = a.tmux.Kill(context.WithoutCancel(ctx), tmuxName)
			return fmt.Errorf("session new: %w", err)
		}
		if err := a.db.TouchProject(ctx, p.ID); err != nil {
			return err
		}
		fmt.Printf("created session %s (tmux %s) in %s\n", s.ID, s.TmuxName, p.Path)
		return nil

	case "ls":
		a, err := openApp(ctx, rest)
		if err != nil {
			return err
		}
		defer a.Close()

		rowsDB, err := a.db.ListSessions(ctx)
		if err != nil {
			return err
		}
		live := map[string]tmux.Info{}
		infos, err := a.tmux.List(ctx)
		if err != nil {
			return err
		}
		for _, i := range infos {
			live[i.Name] = i
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATE\tTITLE\tCOMMAND\tSIZE\tTMUX\tCWD")
		for _, s := range rowsDB {
			info, ok := live[s.TmuxName]
			status := "GONE"
			cmd, size, cwd := s.Command, fmt.Sprintf("%dx%d", s.Cols, s.Rows), s.CWD
			if ok {
				status = "live"
				if info.Dead {
					status = "dead"
				}
				cmd = info.Command
				size = fmt.Sprintf("%dx%d", info.Width, info.Height)
				cwd = info.Path
			}
			title := s.Title
			if title == "" && ok {
				title = info.Title
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				s.ID, s.State, truncate(title, 24), cmd, size, status, cwd)
		}
		return w.Flush()

	case "kill":
		fs := flag.NewFlagSet("session kill", flag.ContinueOnError)
		sid := fs.String("id", "", "session id")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *sid == "" {
			return errors.New("session kill: --id is required")
		}
		a, err := openApp(ctx, fs.Args())
		if err != nil {
			return err
		}
		defer a.Close()
		s, err := a.db.GetSession(ctx, *sid)
		if err != nil {
			return err
		}
		if err := a.tmux.Kill(ctx, s.TmuxName); err != nil {
			return err
		}
		if err := a.db.DeleteSession(ctx, s.ID); err != nil {
			return err
		}
		fmt.Printf("killed session %s (tmux %s)\n", s.ID, s.TmuxName)
		return nil
	}
	return fmt.Errorf("unknown session subcommand %q", sub)
}

// ─── doctor ───────────────────────────────────────────────────────────────

// cmdDoctor reports whether the environment can support the panel, and proves
// the panel is isolated from any other tmux on the box.
func cmdDoctor(args []string) error {
	ctx := context.Background()
	cfg, err := config.Load(args, os.Stderr)
	if err != nil {
		return err
	}

	ok := func(cond bool) string {
		if cond {
			return "ok  "
		}
		return "FAIL"
	}
	fmt.Printf("vibepanel %s\n\n", version.String())

	tm := tmux.New(cfg.TmuxSocket, cfg.TmuxDir())
	tv, tErr := tm.Version(ctx)
	fmt.Printf("[%s] tmux binary         %s\n", ok(tErr == nil), tv)
	if tErr != nil {
		fmt.Printf("       tmux is required; install it with your package manager\n")
		return tErr
	}

	if err := cfg.EnsureDirs(); err != nil {
		fmt.Printf("[FAIL] data dir           %s: %v\n", cfg.DataDir, err)
		return err
	}
	fmt.Printf("[ok  ] data dir           %s\n", cfg.DataDir)

	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		fmt.Printf("[FAIL] database           %v\n", err)
		return err
	}
	defer db.Close()
	v, _ := db.Version(ctx)
	fmt.Printf("[ok  ] database           schema v%d at %s\n", v, cfg.DBPath())

	if err := tm.EnsureServer(ctx); err != nil {
		fmt.Printf("[FAIL] tmux server        %v\n", err)
		return err
	}
	infos, err := tm.List(ctx)
	if err != nil {
		fmt.Printf("[FAIL] tmux server        %v\n", err)
		return err
	}
	fmt.Printf("[ok  ] tmux server        socket %q, %d session(s)\n", cfg.TmuxSocket, len(infos))

	// Isolation is the promise that lets this run next to an existing setup.
	// Assert it rather than describe it: every session we can see must be ours.
	foreign := 0
	for _, i := range infos {
		if !strings.HasPrefix(i.Name, "vp_") {
			foreign++
		}
	}
	fmt.Printf("[%s] isolation          %d foreign session(s) visible on our socket\n", ok(foreign == 0), foreign)

	// Not a failure: running without a domain is a legitimate local setup.
	// Saying FAIL here would train the reader to ignore doctor output.
	if cfg.PasskeysUsable() {
		fmt.Printf("[ok  ] passkeys           enabled (rp id %s)\n", cfg.Domain)
	} else {
		fmt.Printf("[--  ] passkeys           disabled; password login only\n")
		fmt.Printf("       needs --domain with a hostname plus TLS, or localhost\n")
	}
	fmt.Printf("\nurl %s\n", cfg.PublicURL())
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func ago(unix int64) string {
	if unix == 0 {
		return "never"
	}
	d := time.Since(time.Unix(unix, 0)).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
