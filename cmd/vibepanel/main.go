// Command vibepanel is a web console for running many parallel coding sessions.
//
// The binary is both the server and its own admin CLI. Keeping them in one
// artefact means there is never a version skew between the tool that creates a
// session and the server that serves it.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/httpapi"
	"github.com/jiangmuran/vibepanel/internal/id"
	sessionpkg "github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/tlsmgr"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/version"
	"github.com/jiangmuran/vibepanel/internal/webui"
	"github.com/jiangmuran/vibepanel/internal/ws"
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
		case "hook":
			return cmdHook(rest)
		case "version":
			fmt.Println("vibepanel", version.String())
			return nil
		default:
			return fmt.Errorf("unknown command %q (try: serve, project, session, hook, doctor, version)", cmd)
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Expired sign-ins are already refused — AuthSessionByToken filters on
	// expires_at — so this is housekeeping rather than security. It is done at
	// all because nothing called it: the table only grew, one row per sign-in
	// that stopped meaning anything thirty days earlier.
	//
	// Here rather than in openApp, which the admin subcommands share: listing
	// projects should not quietly write to the database. At startup rather
	// than on a ticker, because a sign-in is rare enough that a panel
	// restarted every few weeks never accumulates anything worth a goroutine,
	// and a purge is easier to reason about when it happens at a moment
	// somebody chose.
	if n, perr := a.db.PurgeExpiredAuthSessions(ctx); perr != nil {
		logger.Warn("purge expired sign-ins", "err", perr)
	} else if n > 0 {
		logger.Info("purged expired sign-ins", "rows", n)
	}

	// The audit log has no reader past the fifty rows the settings page shows,
	// and nothing ever removed one. A panel on a public port collects a row per
	// refused sign-in for as long as it runs, so the table only ever grew — on
	// the same disk as the projects. The cap is generous enough that this is
	// housekeeping rather than a retention policy anybody has to think about.
	if n, perr := a.db.TrimAuditLog(ctx, store.AuditKeep); perr != nil {
		logger.Warn("trim audit log", "err", perr)
	} else if n > 0 {
		logger.Info("trimmed audit log", "rows", n)
	}
	mgr := sessionpkg.NewManager(a.tmux, sessionpkg.DefaultRingSize)

	trusted, err := auth.ParseCIDRs(a.cfg.TrustedProxies)
	if err != nil {
		return fmt.Errorf("trusted proxies: %w", err)
	}
	allow, err := auth.ParseCIDRs(a.cfg.AllowFrom)
	if err != nil {
		return fmt.Errorf("allow-from: %w", err)
	}

	srv := &httpapi.Server{
		Cfg: a.cfg, DB: a.db, Tmux: a.tmux, Manager: mgr,
		Hub: ws.NewHub(), Detector: sessionpkg.NewDetector(),
		Sampler: &sysmon.Sampler{DiskPath: a.cfg.DataDir},
		Auth: &httpapi.Auth{
			Throttle:       auth.NewThrottle(),
			TrustedProxies: trusted,
			Allow:          allow,
			BlockedAudit:   auth.NewCooldown(time.Minute),
		},
		Log: logger,
	}

	// A setup token exists only while there is no account. Printing it to the
	// console is the handover: whoever can read the server's output is the
	// person entitled to claim the panel, and anyone who merely reaches it
	// over the network is not.
	users, err := a.db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if users == 0 {
		token, terr := auth.NewToken()
		if terr != nil {
			return terr
		}
		srv.Auth.SetupToken = token
		defer func() { srv.Auth.SetupToken = "" }()
	}
	// The pump reports output and bells straight into the server, which is how
	// last_output_at stays honest and, from M4, how session state is decided.
	mgr.OnSignals = srv.HandleSignals
	// Before Reconcile, which re-derives every session from what is running
	// right now. A bell that rang before the restart is not on the wire any
	// more, and nothing else will say a session was asking for a human.
	if err := srv.RestoreState(ctx); err != nil {
		return err
	}
	if err := srv.Reconcile(ctx); err != nil {
		return err
	}
	go srv.Poll(ctx)

	httpServer := &http.Server{
		Addr:    a.cfg.Addr,
		Handler: srv.Routes(),
		// No WriteTimeout: a WebSocket carrying a terminal is a long-lived
		// response, and any deadline here would sever idle sessions on a timer.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Printf("vibepanel %s\n", version.String())
	fmt.Printf("  data dir     %s\n", a.cfg.DataDir)
	fmt.Printf("  tmux socket  %s\n", a.cfg.TmuxSocket)
	fmt.Printf("  url          %s\n", a.cfg.PublicURL())
	if !a.cfg.PasskeysUsable() {
		fmt.Printf("  passkeys     disabled (needs --domain with TLS, or localhost)\n")
	}
	if a.cfg.StaticDir != "" {
		fmt.Printf("  frontend     %s (from disk)\n", a.cfg.StaticDir)
	} else if !webui.Built() {
		fmt.Printf("  frontend     NOT BUILT — run `npm run build` in web/, or pass --static-dir\n")
	}
	if len(allow) > 0 {
		fmt.Printf("  allowed from %s\n", strings.Join(a.cfg.AllowFrom, ", "))
	}
	// Loud, and above the setup token where it cannot be scrolled past. A
	// misspelled VIBEPANEL_TLS is a panel serving plaintext on a public port
	// while its operator believes it is not.
	if len(a.cfg.UnknownEnv) > 0 {
		fmt.Printf("\n  WARNING: these environment variables are set and nothing reads them:\n")
		fmt.Printf("           %s\n", strings.Join(a.cfg.UnknownEnv, " "))
		fmt.Printf("           check the spelling against `vibepanel serve --help`; a setting\n")
		fmt.Printf("           that is not applied looks exactly like one that is.\n")
	}
	if a.cfg.PlaintextOnANetwork() {
		where := "every interface on this machine"
		if bound := a.cfg.BindHost(); bound != "" {
			where = bound
		}
		fmt.Printf("\n  WARNING: TLS is off and the panel is listening on %s.\n", where)
		fmt.Printf("           A terminal, the password you type into it and the session\n")
		fmt.Printf("           cookie all cross the network in the clear, and anyone who\n")
		fmt.Printf("           can see that traffic can replay the cookie.\n")
		fmt.Printf("           Use --tls acme or --tls files, put a proxy that terminates\n")
		fmt.Printf("           TLS in front, or bind to 127.0.0.1 if this is only for you.\n")
	}
	if srv.Auth.SetupToken != "" {
		fmt.Printf("\n  No account yet. Open %s and use this one-time setup token:\n\n      %s\n\n",
			a.cfg.PublicURL(), srv.Auth.SetupToken)
	}

	// TLS is resolved before listening. A panel that starts, binds, and only
	// then discovers it has no usable certificate greets its first visitor
	// with a handshake error and nothing that explains it.
	serveTLS := false
	switch a.cfg.TLSMode {
	case config.TLSFiles:
		src, terr := tlsmgr.NewFileSource(a.cfg.CertFile, a.cfg.KeyFile, logger)
		if terr != nil {
			return terr
		}
		defer src.Close()
		httpServer.TLSConfig = src.TLSConfig()
		serveTLS = true
		fmt.Printf("  tls          certificate files, reloaded on change\n")

	case config.TLSACME:
		fmt.Printf("  tls          requesting a certificate for %s…\n", a.cfg.Domain)
		tlsCfg, terr := tlsmgr.NewACME(ctx, tlsmgr.ACMEOptions{
			Domain:     a.cfg.Domain,
			Email:      a.cfg.ACMEEmail,
			Directory:  a.cfg.ACMEDirectory,
			Provider:   a.cfg.ACMEDNSProvider,
			StorageDir: a.cfg.ACMEDir(),
			Log:        logger,
		})
		if terr != nil {
			return terr
		}
		httpServer.TLSConfig = tlsCfg
		serveTLS = true
	}

	// What the panel is actually serving, asked of the live TLS config rather
	// than of whichever source produced it. Works for both modes, and stays
	// right if the certificate is replaced underneath.
	if serveTLS && httpServer.TLSConfig != nil {
		tlsCfg := httpServer.TLSConfig
		domain := a.cfg.Domain
		srv.CertExpiry = func() time.Time {
			cert, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{ServerName: domain})
			if err != nil || cert == nil || cert.Leaf == nil {
				return time.Time{}
			}
			return cert.Leaf.NotAfter
		}
	}

	errCh := make(chan error, 1)
	go func() {
		if serveTLS {
			// The paths are empty because the certificate comes from
			// TLSConfig, which is where reloading and renewal live.
			errCh <- httpServer.ListenAndServeTLS("", "")
			return
		}
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	// Shut the HTTP server down, then detach. Detaching is not the same as
	// killing: every tmux session, and every agent inside it, keeps running.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	mgr.DetachAll()
	fmt.Println("\nstopped; tmux sessions keep running")
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
		// this out of their environment.
		//
		// All four variables, the same ones the HTTP path injects. This used to
		// build its own list of two, so a session created with
		// `vibepanel session new` had no token to authenticate with and no
		// address to post to — and since the hook script suppresses its own
		// errors by design, the only symptom was a session whose state stayed
		// guessed forever, in a panel whose settings page said hooks were
		// installed.
		token, terr := a.db.HookToken(ctx)
		if terr != nil {
			// Not fatal: without a token the session falls back to the output
			// heuristic, which is the documented behaviour when hooks are not
			// in play. Worth saying out loud, though, because the difference is
			// invisible from inside the session.
			fmt.Fprintf(os.Stderr, "warning: no hook token (%v); this session will fall back to the output heuristic\n", terr)
			token = ""
		}
		env := hooks.SessionEnv(sid, p.ID, a.cfg.LoopbackURL(), token)
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
			State: sessionpkg.StateWorking,
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

// ─── hook ─────────────────────────────────────────────────────────────────

// cmdHook installs the reporter script and prints the configuration to paste.
//
// Printing rather than editing the user's agent configuration: those files are
// theirs, they often contain other things, and a tool that rewrites them
// silently is a tool people stop trusting. The panel's settings page will offer
// to apply this, showing exactly what it would write.
func cmdHook(args []string) error {
	ctx := context.Background()
	a, err := openApp(ctx, args)
	if err != nil {
		return err
	}
	defer a.Close()

	script, err := hooks.InstallScript(filepath.Join(a.cfg.DataDir, "hooks"))
	if err != nil {
		return err
	}

	srv := &httpapi.Server{Cfg: a.cfg, DB: a.db, Tmux: a.tmux, Log: slog.Default()}
	if _, err := srv.HookToken(ctx); err != nil {
		return fmt.Errorf("hook: %w", err)
	}

	fmt.Printf("Reporter installed at %s\n", script)
	fmt.Printf(`
State reporting is optional. Without it the panel infers state from the byte
stream, which can tell "producing output" from "quiet" and sees the terminal
bell, but cannot tell "finished" from "waiting for you". With it, the agent
says which.

The script no-ops outside a vibepanel session, so installing it globally does
not affect agents you start from an ordinary terminal.

── Claude Code ──  merge into ~/.claude/settings.json

%s

── Codex ──  add to ~/.codex/config.toml

%s

`, hooks.ClaudeSettings(script), hooks.CodexNotify(script))
	return nil
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

	// Every check that can run, runs.
	//
	// This used to return at the first failure, so a machine with three
	// problems took three runs to find them: fix the data directory, run
	// again, discover the database, run again, discover the isolation. A
	// diagnostic that stops diagnosing is asking the operator to bisect their
	// own environment.
	//
	// Returning the error also printed it twice — once as the [FAIL] line and
	// once as `vibepanel: <the same thing>` from main — which reads like a
	// crash rather than a report. The report is the output now; the returned
	// error is only a count, so the exit code still means something to a
	// script.
	failed := 0
	fail := func(label string, err error) {
		fmt.Printf("[FAIL] %-18s %v\n", label, err)
		failed++
	}
	skip := func(label, why string) {
		fmt.Printf("[--  ] %-18s skipped: %s\n", label, why)
	}
	fmt.Printf("vibepanel %s\n\n", version.String())

	tm := tmux.New(cfg.TmuxSocket, cfg.TmuxDir())
	tv, tErr := tm.Version(ctx)
	// Three outcomes, not two: missing is fatal, too old is a real degradation
	// that is not worth refusing to run over, and neither should look like the
	// other. "--" is the same marker passkeys use for "works, but not here".
	tmuxMark := ok(tErr == nil)
	tooOld := tErr == nil && !tmux.AtLeastMinimum(tv)
	if tooOld {
		tmuxMark = "--  "
	}
	if tErr != nil {
		fail("tmux binary", tErr)
		fmt.Printf("       tmux is required; install it with your package manager\n")
	} else {
		fmt.Printf("[%s] tmux binary         %s\n", tmuxMark, tv)
	}
	if tooOld {
		// Said here because every symptom of an unknown option in the config is
		// something quietly not happening: tmux reports it once at startup and
		// then behaves as though the line was never written.
		fmt.Printf("       older than %d.%d, so allow-passthrough is not applied and the\n",
			tmux.MinMajor, tmux.MinMinor)
		fmt.Printf("       sequences agent TUIs use for progress and notifications are lost\n")
	}

	dirsOK := true
	if err := cfg.EnsureDirs(); err != nil {
		fail("data dir", fmt.Errorf("%s: %w", cfg.DataDir, err))
		dirsOK = false
	} else {
		fmt.Printf("[ok  ] data dir           %s\n", cfg.DataDir)
	}

	if !dirsOK {
		skip("database", "the data directory is not usable")
	} else if db, err := store.Open(ctx, cfg.DBPath()); err != nil {
		fail("database", err)
	} else {
		defer db.Close()
		v, _ := db.Version(ctx)
		fmt.Printf("[ok  ] database           schema v%d at %s\n", v, cfg.DBPath())
	}

	// Whether one was already running is worth saying, because starting one is
	// a change and a diagnostic should not make changes silently. `doctor` on a
	// machine with nothing set up leaves a tmux server behind — harmless, since
	// the panel needs one anyway, but the operator should learn it from the
	// output rather than from `ps` six hours later.
	var infos []tmux.Info
	serverOK := false
	switch {
	case tErr != nil:
		skip("tmux server", "there is no tmux binary to talk to")
	case !dirsOK:
		// The socket and the generated config both live under the data dir.
		skip("tmux server", "the data directory is not usable")
	default:
		started := !tm.ServerRunning(ctx)
		if err := tm.EnsureServer(ctx); err != nil {
			fail("tmux server", err)
		} else if infos, err = tm.List(ctx); err != nil {
			fail("tmux server", err)
		} else {
			serverOK = true
			note := ""
			if started {
				note = " (started by this check; it is the panel's own socket)"
			}
			fmt.Printf("[ok  ] tmux server        socket %q, %d session(s)%s\n",
				cfg.TmuxSocket, len(infos), note)
		}
	}

	// Isolation is the promise that lets this run next to an existing setup.
	// Assert it rather than describe it: every session we can see must be ours.
	if !serverOK {
		skip("isolation", "there is no session list to check")
	} else {
		foreign := 0
		for _, i := range infos {
			if !strings.HasPrefix(i.Name, "vp_") {
				foreign++
			}
		}
		if foreign != 0 {
			failed++
		}
		fmt.Printf("[%s] isolation          %d foreign session(s) visible on our socket\n",
			ok(foreign == 0), foreign)
	}

	// Not a failure: running without a domain is a legitimate local setup.
	// Saying FAIL here would train the reader to ignore doctor output.
	if cfg.PasskeysUsable() {
		fmt.Printf("[ok  ] passkeys           enabled (rp id %s)\n", cfg.Domain)
	} else {
		fmt.Printf("[--  ] passkeys           disabled; password login only\n")
		fmt.Printf("       needs --domain with a hostname plus TLS, or localhost\n")
	}
	if len(cfg.UnknownEnv) == 0 {
		fmt.Printf("[ok  ] environment        no unrecognised VIBEPANEL_* variables\n")
	} else {
		fmt.Printf("[warn] environment        set but never read: %s\n",
			strings.Join(cfg.UnknownEnv, " "))
		fmt.Printf("       a misspelled name is silently not applied\n")
	}

	fmt.Printf("\nurl %s\n", cfg.PublicURL())
	if failed > 0 {
		// A count, not a repeat: the detail is above, and main prefixes
		// whatever comes back with "vibepanel:".
		return fmt.Errorf("%d check(s) failed", failed)
	}
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
