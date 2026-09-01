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
	"sort"
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
	"github.com/jiangmuran/vibepanel/internal/tz"
	"github.com/jiangmuran/vibepanel/internal/usage"
	"github.com/jiangmuran/vibepanel/internal/version"
	"github.com/jiangmuran/vibepanel/internal/webui"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// errRestart asks the supervisor for a new process.
//
// 143 is the exit code, and it is chosen rather than convenient. systemd's
// units declare `SuccessExitStatus=143`, so this is logged as a clean stop
// rather than a crash, and `Restart=always` brings it back. launchd's
// KeepAlive is `SuccessfulExit: false` -- it restarts a job only when it exits
// *un*successfully -- so the same code has to be non-zero there. One number
// satisfies both, and 0 would satisfy only systemd.
var errRestart = errors.New("restart requested")

const restartExitCode = 143

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // the flag package already printed usage
		}
		if errors.Is(err, errRestart) {
			os.Exit(restartExitCode)
		}
		fmt.Fprintln(os.Stderr, "vibepanel:", err)
		os.Exit(1)
	}
}

// commands is the dispatch, and the only list of what this binary can do.
//
// It was a switch, and the names appeared again in the error for an unknown
// command, and `--help` listed neither. Adding the list to the usage text made
// that three copies of the same six words — the shape that has already cost
// this session twice, both times with the duplicate a few lines from the
// original. TestEveryDocumentedCommandExists compares this against the text
// `--help` prints.
var commands map[string]func([]string) error

// commandNames returns the dispatch keys in the order --help lists them.
func commandNames() []string {
	var out []string
	for _, line := range strings.Split(config.Commands, "\n") {
		if name := strings.Fields(line); len(name) > 0 {
			out = append(out, name[0])
		}
	}
	return out
}

func init() {
	commands = map[string]func([]string) error{
		"serve":   cmdServe,
		"project": cmdProject,
		"session": cmdSession,
		"doctor":  cmdDoctor,
		"hook":    cmdHook,
		"tune":    cmdTune,
		"service": cmdService,
		"account": cmdAccount,
		"version": func([]string) error { fmt.Println("vibepanel", version.String()); return nil },
	}
}

func run(args []string) error {
	// Subcommands are matched before flag parsing so that `vibepanel project
	// add --name x` can have its own flag set rather than fighting the global
	// one.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest := args[0], args[1:]
		run, ok := commands[cmd]
		if !ok {
			return fmt.Errorf("unknown command %q (try: %s)", cmd, strings.Join(commandNames(), ", "))
		}
		return run(rest)
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

	// One panel per data directory.
	//
	// Two start happily and the second one voids the premise the design rests
	// on: the panel is meant to be the only tmux client, so that there is one
	// authoritative grid and one place that decides its size. Each also keeps
	// its own detector in memory, so a bell one of them saw is invisible to the
	// other and the "waiting" it set is overwritten on the next tick.
	unlock, err := config.LockDataDir(a.cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()

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

	// Buffered by one: the handler must not block on a restart that is already
	// under way, and a second click while the first is shutting down is the
	// same request.
	restartCh := make(chan struct{}, 1)

	srv := &httpapi.Server{
		Cfg: a.cfg, DB: a.db, Tmux: a.tmux, Manager: mgr,
		Restart: restartCh,
		Hub:     ws.NewHub(), Detector: sessionpkg.NewDetector(),
		Sampler: &sysmon.Sampler{DiskPath: a.cfg.DataDir},
		Auth: &httpapi.Auth{
			Throttle:       auth.NewThrottle(),
			TrustedProxies: trusted,
			Allow:          allow,
			BlockedAudit:   auth.NewCooldown(time.Minute),
		},
		Log: logger,
	}

	// Token usage reads the agents' own transcripts out of the home directory
	// of whoever the panel runs as -- which is the same account that runs the
	// agents, because the panel starts them. Under the system unit that is a
	// different user with no transcripts, and the panel says so on screen
	// rather than reporting zero.
	if home, herr := os.UserHomeDir(); herr == nil {
		scanner := usage.DefaultScanner(home)
		// The zone the day labels are written in, which has to be the same one
		// the queries ask about.
		//
		// Scanner.Loc has existed since this was built and was never set, so
		// production always bucketed in the process's own zone -- and `today`
		// on the query side was computed separately, with a bare time.Now().
		// Setting one without the other is silent: the queries name a bucket
		// the ingest never writes, so the heatmap's last square and the
		// "today" row go empty and nothing reports an error. They are read
		// from the same setting now, and TestTheIngestAndTheQueryAgreeOnToday
		// is what says so.
		if name, err := a.db.GetSetting(ctx, httpapi.TimeZoneKey, ""); err == nil {
			if loc, lerr := tz.Load(name); lerr == nil {
				scanner.Loc = loc
			} else {
				logger.Warn("time zone setting could not be loaded", "name", name, "err", lerr)
			}
		}
		srv.Tokens = &usage.Ingester{
			Scanner: scanner,
			DB:      a.db,
			Log:     logger,
		}
		// One pass at start, in the background, so the tab is populated before
		// anybody opens it. Not blocking the listener: a first pass over a
		// year of history is seconds, and a panel that will not accept
		// connections while it reads somebody's history is a panel that looks
		// broken on every restart.
		srv.Tokens.Ensure(true)
	} else {
		logger.Warn("no home directory, so token usage has nothing to read", "err", herr)
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

	// The restart goes through the same door as SIGTERM.
	//
	// Not os.Exit from the handler: everything below this select is what makes
	// a stop lossless -- the last capture of every pane, and detaching from
	// tmux rather than killing it. A restart that skipped it would lose the
	// scrollback since the last archive tick, which is the part somebody is
	// most likely to want back.
	restarting := false
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	case <-restartCh:
		restarting = true
	}

	// Shut the HTTP server down, then detach. Detaching is not the same as
	// killing: every tmux session, and every agent inside it, keeps running.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)

	// One last capture of every session, before detaching.
	//
	// This is what makes an orderly reboot lossless. systemd stops the unit
	// first and only then goes on to kill the tmux server, so the panel is the
	// last thing running that can still read those panes — and the thirty
	// seconds the archive ticker might otherwise be behind by are precisely the
	// seconds somebody will want tomorrow, being the last thing on screen.
	//
	// After Shutdown so no request is still writing, before DetachAll because
	// capture-pane talks to tmux and not to our PTY, so the order only matters
	// for tidiness. Bounded by the same context: a machine going down is not a
	// place to block forever.
	srv.ArchiveAll(shutdownCtx)
	mgr.DetachAll()
	if restarting {
		fmt.Println("\nrestarting; tmux sessions keep running")
		return errRestart
	}
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
		profile := fs.String("profile", "", "launch profile id or name (see the settings page)")
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

		// The same resolution the HTTP path does, through the same function.
		// This path has already been the one that drifted — it built its own
		// two-variable environment and left out the hook token — and the fix
		// then was to move the building somewhere both callers share.
		prof, err := resolveProfile(ctx, a.db, *profile)
		if err != nil {
			return fmt.Errorf("session new: %w", err)
		}
		argv := fs.Args()
		if len(argv) == 0 && prof != nil {
			argv = prof.Command
		}

		err = a.tmux.Create(ctx, tmux.CreateOptions{
			Name:    tmuxName,
			Dir:     p.Path,
			Command: argv,
			// The panel's own last: tmux takes the last -e when two name the
			// same variable, so this is what stops a profile redirecting a
			// session's state reports.
			Env:    store.LaunchEnv(prof, env),
			Width:  *cols,
			Height: *rows,
		})
		if err != nil {
			return fmt.Errorf("session new: %w", err)
		}

		// A title someone typed is theirs, and the poller may not take it back.
		//
		// CreateSession defaults an unset source to 'auto', and the poller's
		// SetSessionTitle(..., TitleAuto) is gated on exactly that — so one
		// two-second tick later `--title "billing fix"` had become "node", or
		// the project directory's basename, permanently. The HTTP path says
		// TitleManual right after its insert; this is the same asymmetry that
		// made this path miss the hook token and the launch argv.
		titleSource := store.TitleAuto
		if *title != "" {
			titleSource = store.TitleManual
		}

		s, err := a.db.CreateSession(ctx, store.Session{
			ID: sid, ProjectID: p.ID, TmuxName: tmuxName,
			Title: *title, TitleSource: titleSource,
			CWD: p.Path, Cols: *cols, Rows: *rows,
			State: sessionpkg.StateWorking,
			// The same argv that was just handed to tmux. Without it a session
			// made from the CLI is one the panel cannot rebuild after a reboot
			// — the same asymmetry that made this path miss the hook token.
			LaunchCommand:   argv,
			LaunchProfileID: profileID(prof),
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
		if err := killSessionTree(ctx, a.db, a.tmux, s); err != nil {
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
	// `hook remove` takes the three of them back out.
	//
	// The settings page has always been able to do this and the CLI never
	// could, which is exactly backwards: the times you want the hooks gone are
	// the times the panel is not running to offer it -- it will not start, it
	// has been uninstalled, or the reporter is posting to an address that no
	// longer answers. It needs no database, only the data directory the script
	// lives under, so it does not open the app.
	if len(args) > 0 && args[0] == "remove" {
		return hookRemove(args[1:])
	}

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

// hookRemove takes the panel's hooks out of all three agents' configuration.
//
// Every one of them is reported, including the ones that were not there. "Not
// installed" and "removed" are different facts about somebody's machine, and a
// teardown that prints only what it touched leaves you wondering about the rest.
//
// A failure on one does not stop the others. They are three separate files
// owned by three separate tools, and an unreadable ~/.codex/config.toml is no
// reason to leave the Claude Code hooks in place.
func hookRemove(args []string) error {
	cfg, err := config.Load(args, os.Stderr)
	if err != nil {
		return err
	}
	script, err := hooks.InstallScript(filepath.Join(cfg.DataDir, "hooks"))
	if err != nil {
		return err
	}

	failed := 0
	say := func(what string, err error) {
		if err != nil {
			fmt.Printf("[FAIL] %-12s %v\n", what, err)
			failed++
			return
		}
		fmt.Printf("[ok  ] %-12s hooks removed\n", what)
	}

	st, err := hooks.UninstallClaude(script)
	say("claude", err)
	if err == nil && st.Installed {
		fmt.Printf("       claude       still reports %d event(s); something else in that file points at %s\n",
			len(st.Events), script)
	}
	_, err = hooks.UninstallCodex(script)
	say("codex", err)
	say("opencode", hooks.UninstallOpencode())

	// The script itself last, and only when nothing still refers to it.
	if failed == 0 {
		if err := os.Remove(script); err != nil && !os.IsNotExist(err) {
			fmt.Printf("[FAIL] %-12s %v\n", "reporter", err)
			failed++
		} else {
			fmt.Printf("[ok  ] %-12s %s\n", "reporter", script)
		}
	}
	if failed > 0 {
		return fmt.Errorf("hook remove: %d of them failed", failed)
	}
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

	// A panel already running is the normal state, and worth saying: several
	// of the checks below look different when something else is holding the
	// data directory, and `doctor` is often run precisely because somebody is
	// not sure whether the service is up.
	//
	// The loopback check below belongs to this branch, because this is what
	// knows whether asking is meaningful.
	if dirsOK {
		if holder := config.DataDirLockedBy(cfg.DataDir); holder != "" {
			fmt.Printf("[ok  ] running panel      %s holds %s\n", holder, cfg.DataDir)

			// Does the panel answer where its own hooks post?
			//
			// cfg.LoopbackURL() is what every session is given as
			// VIBEPANEL_URL, so this asks the one question that matters about
			// it: does anything answer there.
			//
			// Not the question the finding this came from asked. That said
			// `--addr 192.168.8.20:8443` "binds one interface and leaves
			// nothing on 127.0.0.1", and it does -- but LoopbackURL follows
			// BindHost, so the sessions are told 192.168.8.20 too and reach it
			// perfectly well. Measured. The mechanism is not the bind address
			// on its own.
			//
			// What this does catch is worth more: an address that stopped being
			// local (a VPN dropped, DHCP moved), a firewall in the way, and --
			// the likeliest of the three -- a `doctor` run without the
			// environment the service runs with, which means every other line
			// above is describing a differently-configured panel than the one
			// holding the lock.
			//
			// Every symptom of that is a silence. report.sh suppresses its own
			// failures on purpose, because a hook that makes an agent wait is
			// worse than a missed state update; the settings page reports hooks
			// as installed because it reads the agent's configuration file, not
			// whether anything arrived; and every session falls back to the
			// guessed state, which is right often enough that nobody
			// investigates. The runbook has the section and the three commands,
			// and reaching it requires already suspecting the bind address.
			//
			// Only while a panel holds the lock. With none running, loopback
			// not answering is the expected state, and a FAIL there would teach
			// people to skip this output -- the same reason passkeys report
			// "--" rather than failing.
			//
			// InsecureSkipVerify, like report.sh's --insecure, and for the
			// reason its own comment gives: "the destination is 127.0.0.1, and
			// when the panel is serving TLS its certificate is issued for the
			// public hostname, which a loopback address will never match." The
			// question is whether anything answers there, not whether the
			// certificate is right -- and probing more strictly than the hook
			// does would report a problem the hook does not have.
			probeURL := cfg.LoopbackURL() + "/api/health"
			probe := &http.Client{
				Timeout: 3 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
				},
			}
			res, perr := probe.Get(probeURL)
			if res != nil {
				res.Body.Close()
			}
			switch {
			case perr != nil:
				fail("hook endpoint", fmt.Errorf("%s does not answer: %w", probeURL, perr))
				fmt.Printf("       This is where sessions started with THIS configuration\n")
				fmt.Printf("       post their hook reports. Either the panel is not listening\n")
				fmt.Printf("       there, or this is not the configuration it was started\n")
				fmt.Printf("       with -- check the unit's environment, since every line\n")
				fmt.Printf("       above then describes a different panel than the running one.\n")
				fmt.Printf("       Nothing else would say so: report.sh swallows its own\n")
				fmt.Printf("       failures on purpose, and the settings page reads the\n")
				fmt.Printf("       agent's config rather than whether anything arrived.\n")
			case res.StatusCode >= 400:
				fail("hook endpoint", fmt.Errorf("%s answered %s", probeURL, res.Status))
			default:
				fmt.Printf("[ok  ] hook endpoint      %s answers\n", probeURL)
			}
		} else {
			fmt.Printf("[ok  ] running panel      none; nothing holds %s\n", cfg.DataDir)
			fmt.Printf("[--  ] hook endpoint      skipped: no panel is running to answer\n")
		}
	}

	// Read out of the database below and compared against what the sessions
	// hold, further down. Empty means either "no database to ask" or "no token
	// has been created yet", and the check treats both as nothing to say.
	var storedHookToken string

	if !dirsOK {
		skip("database", "the data directory is not usable")
	} else if db, err := store.Open(ctx, cfg.DBPath()); err != nil {
		fail("database", err)
	} else {
		defer db.Close()
		v, _ := db.Version(ctx)
		fmt.Printf("[ok  ] database           schema v%d at %s\n", v, cfg.DBPath())

		// Opening a database says nothing about writing to one. Measured: with
		// the database's writes capped, every line above printed [ok] and this
		// command exited 0, against a panel that could not record a single
		// thing — which is the failure the runbook sends people here to find.
		if err := db.CheckWritable(ctx); err != nil {
			fail("database writes", err)
		} else {
			fmt.Printf("[ok  ] database writes    accepted\n")
		}

		// GetSetting, deliberately, and not HookToken: that one creates the
		// token when there is not one, so a diagnostic run on a fresh panel
		// would generate a credential as a side effect. "A diagnostic that
		// changes the thing it is diagnosing is one people stop trusting" --
		// the same reason CheckWritable rolls its probe back.
		storedHookToken, _ = db.GetSetting(ctx, "hook_token", "")
	}

	// Free space where the database lives.
	//
	// The panel's quietest failure is a full disk: it answers every request,
	// serves every terminal, and stops recording anything. The number that
	// predicts it was already being sampled for the monitor panel and was not
	// being shown to the one person asking what is wrong.
	if sample := (&sysmon.Sampler{DiskPath: cfg.DataDir}).Sample(); sample.DiskTotal > 0 {
		free := sysmon.FormatBytes(sample.DiskFree)
		total := sysmon.FormatBytes(sample.DiskTotal)
		pct := float64(sample.DiskFree) / float64(sample.DiskTotal) * 100
		switch {
		case sample.DiskFree < 64<<20:
			fail("disk", fmt.Errorf("%s free of %s (%.1f%%) on %s; the panel cannot write",
				free, total, pct, sample.DiskPath))
		case sample.DiskFree < 512<<20:
			fmt.Printf("[--  ] disk               %s free of %s (%.1f%%) on %s — getting tight\n",
				free, total, pct, sample.DiskPath)
		default:
			fmt.Printf("[ok  ] disk               %s free of %s (%.1f%%)\n", free, total, pct)
		}
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

	// Is the running server using the config this binary carries?
	//
	// `-f` is read once, at start-server, and the panel never kills its server:
	// that is the premise of the whole project. EnsureServer rewrites the file
	// on every call, so the file is always current while the running server
	// goes on using whatever it read at boot. A config change therefore takes
	// effect at the next reboot or not at all -- and that covers
	// allow-passthrough, which is why tmux 3.3 is the floor, and the
	// smcup@/rmcup@ and indn@ overrides.
	//
	// It compounds with an upgrade: a new binary that is not running and a new
	// tmux config that is not loaded, both looking installed. This is the half
	// that can be seen from here.
	//
	// Not a failure. The remedy costs every session on the socket, and that is
	// a decision for whoever reads it rather than something to be demanded.
	if !serverOK {
		skip("tmux config", "there is no server to ask")
	} else if running := tm.RunningConfigStamp(ctx); running == "" {
		fmt.Printf("[--  ] tmux config        the running server predates this check; restart it to know\n")
	} else if running != tmux.ConfigStamp() {
		fmt.Printf("[--  ] tmux config        the running server started with a different config\n")
		fmt.Printf("       The file on disk is current; tmux only reads it at start-server, and\n")
		fmt.Printf("       this server has not restarted since. Nothing is broken today, but\n")
		fmt.Printf("       changes to it -- allow-passthrough, the terminal overrides -- are not\n")
		fmt.Printf("       in effect. Applying them costs every session on this socket:\n")
		fmt.Printf("         tmux -L %s kill-server\n", cfg.TmuxSocket)
	} else {
		fmt.Printf("[ok  ] tmux config        the running server has the config this binary carries\n")
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

	// What tmux reports for each live pane, because the agent match is a fact
	// about somebody else's packaging and gets it wrong silently.
	//
	// stateIsGuessed only fires when a session's foreground process is named
	// `claude` or `codex`. A script with a `#!/usr/bin/env node` line reports
	// `node` instead -- measured -- so on a machine where Claude Code was
	// installed through npm the notice saying the states are inferred never
	// appears, on exactly the sessions it is about. Nothing else would ever say
	// so: the states still look plausible, because a guess usually is.
	//
	// Printed rather than judged. A panel full of shells is not a problem, and
	// failing here would train the reader to skip doctor's output.
	if !serverOK {
		skip("agents", "there is no session list to check")
	} else {
		byCmd := map[string]int{}
		agents, nonShell := 0, 0
		for _, i := range infos {
			if !strings.HasPrefix(i.Name, "vp_") {
				continue
			}
			byCmd[i.Command]++
			if sessionpkg.IsAgentCommand(i.Command) {
				agents++
			} else if !sessionpkg.IsShellCommand(i.Command) {
				nonShell++
			}
		}
		names := make([]string, 0, len(byCmd))
		for c := range byCmd {
			if c == "" {
				c = "(none)"
			}
			names = append(names, c)
		}
		sort.Strings(names)
		switch {
		case len(byCmd) == 0:
			fmt.Printf("[ok  ] agents             no sessions to look at\n")
		case agents > 0:
			fmt.Printf("[ok  ] agents             %d recognised; tmux reports: %s\n",
				agents, strings.Join(names, " "))
		case nonShell > 0:
			fmt.Printf("[--  ] agents             none recognised; tmux reports: %s\n",
				strings.Join(names, " "))
			fmt.Printf("       %d session(s) running something that is not a shell. If one of\n", nonShell)
			fmt.Printf("       those is an agent, the panel cannot tell, and the notice saying\n")
			fmt.Printf("       its states are guessed will not appear. Claude Code installed\n")
			fmt.Printf("       through npm reports \"node\" here rather than \"claude\".\n")
		default:
			fmt.Printf("[ok  ] agents             none running; every session is a shell\n")
		}
	}

	// What the sessions were actually given, which is not what the config says.
	//
	// VIBEPANEL_URL is injected with -e when a session is created, and
	// `set-environment` on a live session reaches only panes started after it.
	// So a panel restarted with a different --addr leaves every session made
	// before the change posting its hook reports to the old address. The check
	// above cannot see this: it probes the URL the *current* configuration
	// produces, and that one answers perfectly well.
	//
	// This is the shape the runbook's "hooks say they are installed and no
	// state ever arrives" section is really about. Its previous explanation --
	// that binding one interface leaves nothing on 127.0.0.1 -- does not hold,
	// because LoopbackURL follows BindHost and the sessions are told the bound
	// address too. Measured.
	if !serverOK {
		skip("hook url", "there is no session list to check")
	} else {
		want := cfg.LoopbackURL()
		var checked, unset, stale, tokChecked, tokStale int
		example := ""
		for _, i := range infos {
			if !strings.HasPrefix(i.Name, "vp_") {
				continue
			}
			checked++
			got, gerr := tm.SessionEnvValue(ctx, i.Name, "VIBEPANEL_URL")
			if gerr != nil {
				continue
			}
			switch {
			case got == "":
				unset++
			case got != want:
				stale++
				if example == "" {
					example = got
				}
			}
			// The token travels the same way and goes stale for a different
			// reason: it is created once and never rotated, so it only changes
			// when the row holding it goes away -- a restore from a backup
			// taken before it existed, which the runbook's "database will not
			// open" section tells operators to do, or the setting being
			// cleared. A new one is generated while the sessions, which outlive
			// the database by design, keep presenting the old one. Every report
			// is then rejected, permanently for those sessions, and silently,
			// because report.sh suppresses its own failures.
			//
			// Compared, never printed: it is a credential, and doctor output
			// ends up in bug reports.
			if storedHookToken != "" {
				tok, terr := tm.SessionEnvValue(ctx, i.Name, "VIBEPANEL_TOKEN")
				if terr == nil && tok != "" {
					tokChecked++
					if tok != storedHookToken {
						tokStale++
					}
				}
			}
		}
		switch {
		case checked == 0:
			fmt.Printf("[ok  ] hook url           no sessions to check\n")
		case stale > 0:
			failed++
			fmt.Printf("[FAIL] hook url           %d of %d session(s) still post to %s, not %s\n",
				stale, checked, example, want)
			fmt.Printf("       They were created before the address changed, and a session's\n")
			fmt.Printf("       environment cannot be updated in place -- set-environment\n")
			fmt.Printf("       reaches only panes started after it. Their hook reports go\n")
			fmt.Printf("       nowhere and nothing says so. Restart those sessions from the\n")
			fmt.Printf("       panel to give them the current address.\n")
		case unset == checked:
			fmt.Printf("[--  ] hook url           no session carries one; hooks are not in use\n")
		default:
			fmt.Printf("[ok  ] hook url           %d of %d session(s) post to %s\n",
				checked-unset, checked, want)
		}

		switch {
		case storedHookToken == "":
			fmt.Printf("[--  ] hook token         no token stored yet; nothing to compare against\n")
		case tokChecked == 0:
			fmt.Printf("[--  ] hook token         no session carries one\n")
		case tokStale > 0:
			failed++
			fmt.Printf("[FAIL] hook token         %d of %d session(s) hold a token this panel no longer accepts\n",
				tokStale, tokChecked)
			fmt.Printf("       The token is created once and never rotated, so this means the\n")
			fmt.Printf("       row holding it went away -- a database restored from a backup\n")
			fmt.Printf("       taken before it existed, or the setting cleared. A session's\n")
			fmt.Printf("       environment cannot be updated in place, so those sessions will\n")
			fmt.Printf("       be refused for as long as they live. Restart them from the panel.\n")
		default:
			fmt.Printf("[ok  ] hook token         %d of %d session(s) hold the current token\n",
				tokChecked, tokChecked)
		}
	}

	// Not a failure: running without a domain is a legitimate local setup.
	// Saying FAIL here would train the reader to ignore doctor output.
	if cfg.PasskeysUsable() {
		fmt.Printf("[ok  ] passkeys           enabled (rp id %s)\n", cfg.Domain)
	} else {
		fmt.Printf("[--  ] passkeys           disabled; password login only\n")
		fmt.Printf("       needs --domain with a hostname plus TLS, or localhost\n")
	}
	// What this process is confined by, because every session inherits it.
	//
	// The unit files no longer set any of this, but a unit already installed
	// on somebody's machine does, and upgrading the binary does not rewrite
	// it. The symptom is one a person hits an hour later, inside a session,
	// with nothing pointing back here:
	//
	//	sudo: The "no new privileges" flag is set
	//
	// The panel can read its own bit, so it says so rather than leaving
	// somebody to work out why sudo stopped existing.
	switch nnp, known := noNewPrivs(); {
	case !known:
		// Not a failure and not even a warning: no /proc means macOS, and
		// there is nothing to report rather than something unknown.
	case nnp:
		fmt.Printf("[warn] confinement        no_new_privs is set on this process\n")
		fmt.Printf("       every session inherits it and cannot drop it: sudo, su, pkexec and\n")
		fmt.Printf("       every setuid binary fail inside the panel's terminals\n")
		fmt.Printf("       the shipped units no longer set NoNewPrivileges; an installed one\n")
		fmt.Printf("       predates that. Reinstall it, or delete the line and daemon-reload\n")
	default:
		fmt.Printf("[ok  ] confinement        nothing inherited that a session would notice\n")
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

// resolveProfile finds a launch profile by id, or failing that by name.
//
// By name as well because a person typing a command has the name in front of
// them and the id nowhere -- the ids are opaque hex, and a flag that only took
// one would send everybody to the settings page with a mouse. The id is tried
// first so that a profile somebody named after another profile's id cannot
// shadow it.
//
// A name that matches nothing is an error rather than "no profile". Starting an
// agent against the default endpoint because the gateway profile was spelled
// wrong is a substitution nobody notices until the bill.
func resolveProfile(ctx context.Context, db *store.DB, want string) (*store.LaunchProfile, error) {
	if want == "" {
		return nil, nil
	}
	if p, err := db.GetLaunchProfile(ctx, want); err == nil {
		return &p, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	list, err := db.ListLaunchProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if !strings.EqualFold(p.Name, want) {
			continue
		}
		// The list is redacted, so read the row again for the values it holds.
		full, err := db.GetLaunchProfile(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		return &full, nil
	}
	return nil, fmt.Errorf("no launch profile called %q", want)
}

func profileID(p *store.LaunchProfile) string {
	if p == nil {
		return ""
	}
	return p.ID
}

// killSessionTree kills a session's tmux session and those of the scratch
// terminals under it.
//
// The children first, then the parent: the caller deletes the row next and the
// child rows cascade away with it, so a child whose tmux session outlived this
// call is a process nothing in the panel can reach again. handleDeleteSession
// has always done this; the CLI killed one session and left the rest running,
// which the panel then reported at every startup as "tmux sessions on our
// socket with no database row" without saying who made them.
//
// Kept as a function rather than four lines in the switch so that the test can
// reach it. The two paths drifting apart is the shape that has cost this
// project more than any single bug.
func killSessionTree(ctx context.Context, db *store.DB, tm *tmux.Client, s store.Session) error {
	children, err := db.ListChildSessions(ctx, s.ID)
	if err != nil {
		return err
	}
	for _, c := range append(children, s) {
		if err := tm.Kill(ctx, c.TmuxName); err != nil {
			return fmt.Errorf("kill %s: %w", c.TmuxName, err)
		}
	}
	return nil
}

// noNewPrivs reports the process's no_new_privs bit, and whether it could be
// read at all.
//
// Read from /proc rather than through prctl, because that keeps CGO_ENABLED=0
// and the syscall package out of it and because the failure mode is the one
// wanted: no /proc means macOS, where there is no such bit and nothing to say.
func noNewPrivs() (set bool, known bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, found := strings.CutPrefix(line, "NoNewPrivs:")
		if !found {
			continue
		}
		return strings.TrimSpace(rest) == "1", true
	}
	// The field has existed since Linux 3.5. Missing means something has
	// changed about /proc, and "unknown" is the honest answer.
	return false, false
}
