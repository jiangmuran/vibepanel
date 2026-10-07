package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jiangmuran/vibepanel/internal/httpapi"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/version"
)

// `vibepanel plugin` is the plugin workflow from a shell: docs/plugins.md §6
// and §9. Like `page`, it opens the database directly and never touches tmux.
//
// What it does that the settings page also does -- store, install, grant --
// it does with the same code (httpapi.AddPluginBundle, InstallPlugin) and
// prints the same words (plugins.Describe), so an agent reading this output
// and a person reading the screen are told the same thing. It runs as the
// same user, so like `page publish` that is a default the scaffolded
// AGENTS.md states, not a boundary; every grant it makes is audited under the
// user "cli" and the card says "granted from the command line".

const pluginUsage = `usage: vibepanel plugin <command> [flags] [dir|file.zip]

  list      list plugins and what each may do
  init      scaffold a new plugin: init <dir> --template theme|pane|service|process|full [--name N] [--dev]
  check     report what is wrong with the plugin in dir (the current directory by default)
  describe  print the install screen for dir or a zip, without storing anything
  add       store dir or a zip as a version; nothing runs until it is installed
  install   add, then install: --grant cap,cap (default: nothing) [--version N] [--enable]
  enable    turn an installed plugin on: enable <id>
  disable   turn one off: disable <id>; --all turns every plugin and the module switch off
  remove    delete a plugin, its versions, grants, settings and secrets: remove <id>
  export    write a plugin as a zip: export <id> [--version N] [-o file.zip]
  caps      print the capability table, with the sentence each puts on the screen`

func cmdPlugin(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(pluginUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return pluginList(rest)
	case "init", "new":
		return pluginInit(rest)
	case "check":
		return pluginCheck(rest)
	case "describe":
		return pluginDescribe(rest)
	case "add":
		return pluginAdd(rest, false)
	case "install":
		return pluginAdd(rest, true)
	case "enable":
		return pluginSwitch(rest, true)
	case "disable":
		return pluginSwitch(rest, false)
	case "remove", "rm":
		return pluginRemove(rest)
	case "export":
		return pluginExport(rest)
	case "caps":
		return pluginCaps(rest)
	}
	return fmt.Errorf("unknown plugin command %q\n\n%s", sub, pluginUsage)
}

// readPluginArg reads a plugin from a directory or a zip named by the one
// positional argument, the current directory by default.
func readPluginArg(fs *flag.FlagSet) (plugins.Bundle, string, error) {
	if fs.NArg() > 1 {
		return plugins.Bundle{}, "", fmt.Errorf("one directory or zip, not %d", fs.NArg())
	}
	arg := "."
	if fs.NArg() == 1 {
		arg = fs.Arg(0)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return plugins.Bundle{}, "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return plugins.Bundle{}, "", err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(abs) //nolint:gosec // the person named it
		if err != nil {
			return plugins.Bundle{}, "", err
		}
		b, err := plugins.ReadArchive(data)
		return b, "", err
	}
	b, err := plugins.ReadDir(abs)
	return b, abs, err
}

func pluginList(args []string) error {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	list, err := db.ListPlugins(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no plugins. `vibepanel plugin install <dir|zip> --grant …` adds one; see docs/plugins.md")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tVERSION\tSTATE\tRUNGS\tGRANTED")
	for _, p := range list {
		name, ver, rungs := p.ID, "?", "-"
		if versions, err := db.ListPluginVersions(ctx, p.ID); err == nil && len(versions) > 0 {
			v := versions[0]
			if p.InstalledVersion > 0 {
				if iv, err := db.PluginVersionByNumber(ctx, p.ID, p.InstalledVersion); err == nil {
					v = iv
				}
			}
			m, _ := plugins.DecodeStored(v.Manifest)
			name, ver = m.Name.EN, m.Version
			if r := m.Rungs().Names(); len(r) > 0 {
				rungs = strings.Join(r, ",")
			}
		}
		state := "not installed"
		switch {
		case p.InstalledVersion > 0 && p.Enabled:
			state = fmt.Sprintf("enabled v%d", p.InstalledVersion)
		case p.InstalledVersion > 0:
			state = fmt.Sprintf("disabled v%d", p.InstalledVersion)
		}
		caps, _ := db.PluginCaps(ctx, p.ID)
		granted := make([]string, 0, len(caps))
		for _, c := range caps {
			granted = append(granted, c.Cap)
		}
		g := strings.Join(granted, ",")
		if g == "" {
			g = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, name, ver, state, rungs, g)
	}
	return tw.Flush()
}

func pluginCheck(args []string) error {
	fs := flag.NewFlagSet("plugin check", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, _, err := readPluginArg(fs)
	if err != nil {
		fmt.Printf("plugin: error: %s\n", err)
		return errSilentFailure
	}
	fmt.Printf("ok: %s %s, %d files, %s, rungs %s\n", b.Manifest.ID, b.Manifest.Version, len(b.Files),
		humanBytes(b.Bytes), strings.Join(b.Manifest.Rungs().Names(), ","))
	for _, i := range b.Ignored {
		fmt.Printf("  ignored %s: %s\n", i.Path, i.Reason)
	}
	return nil
}

func pluginDescribe(args []string) error {
	fs := flag.NewFlagSet("plugin describe", flag.ContinueOnError)
	zh := fs.Bool("zh", false, "print the screen in Chinese")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, _, err := readPluginArg(fs)
	if err != nil {
		return err
	}
	printScreen(plugins.Describe(b.Manifest, nil, version.Version), b, *zh)
	return nil
}

// printScreen prints the install screen the way the settings page draws it:
// the same lines, the same order, the same words.
func printScreen(s plugins.Screen, b plugins.Bundle, zh bool) {
	lang := "en"
	if zh {
		lang = "zh"
	}
	if s.Refused != nil {
		fmt.Printf("REFUSED: %s\n", s.Refused.Text.In(lang))
	}
	fmt.Printf("sha256 %s\n", b.Hash())
	for _, l := range s.Lines {
		mark := "  "
		switch {
		case l.Checkable && l.Granted:
			mark = "[x]"
		case l.Checkable:
			mark = "[ ]"
		case l.Tone == plugins.ToneRed:
			mark = "!!"
		case l.Tone == plugins.ToneAmber:
			mark = " !"
		}
		code := ""
		if l.Code != "" && l.Kind != plugins.LineDanger {
			code = "  (" + l.Code + ")"
		}
		fmt.Printf("%s %s%s\n", mark, l.Text.In(lang), code)
	}
	verb := map[string]string{plugins.ConfirmInstall: "Install", plugins.ConfirmGrant: "Install and grant", plugins.ConfirmRun: "Run this as you"}[s.Confirm]
	if zh {
		verb = map[string]string{plugins.ConfirmInstall: "安装", plugins.ConfirmGrant: "安装并授权", plugins.ConfirmRun: "以你的身份运行"}[s.Confirm]
	}
	fmt.Printf("\nbutton: %s\n", verb)
}

func pluginAdd(args []string, install bool) error {
	name := "plugin add"
	if install {
		name = "plugin install"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	grant := fs.String("grant", "", "capabilities to grant, comma-separated (install only; default none)")
	ver := fs.Int("version", 0, "the version to install, default the newest (install only)")
	enable := fs.Bool("enable", false, "enable after installing, when every secret is set (install only)")
	yes := fs.Bool("yes", false, "do not print the screen first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, dir, err := readPluginArg(fs)
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	owner, err := db.FirstUserID(ctx)
	if err != nil {
		return errors.New("the panel has no account yet; finish setup first")
	}
	screen := plugins.Describe(b.Manifest, nil, version.Version)
	if !*yes {
		printScreen(screen, b, false)
		fmt.Println()
	}
	if screen.Refused != nil {
		return errors.New(screen.Refused.Text.EN)
	}
	p, created, err := httpapi.AddPluginBundle(ctx, db, owner, b, dir, "cli")
	if err != nil {
		return err
	}
	versions, _ := db.ListPluginVersions(ctx, p.ID)
	latest := 0
	if len(versions) > 0 {
		latest = versions[0].Version
	}
	if created {
		fmt.Printf("stored %s %s as v%d (new plugin)\n", p.ID, b.Manifest.Version, latest)
	} else {
		fmt.Printf("stored %s %s as v%d\n", p.ID, b.Manifest.Version, latest)
	}
	if !install {
		fmt.Println("not installed: `vibepanel plugin install` with --grant, or Settings → Plugins")
		return nil
	}
	var caps []string
	for _, c := range strings.Split(*grant, ",") {
		if c = strings.TrimSpace(c); c != "" {
			caps = append(caps, c)
		}
	}
	missing, first, err := httpapi.InstallPlugin(ctx, db, p.ID, *ver, caps, "cli")
	if err != nil {
		return err
	}
	_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: map[bool]string{true: "plugin.installed", false: "plugin.updated"}[first],
		Username: "cli", Detail: fmt.Sprintf("%s granted %s", p.ID, strings.Join(caps, ","))})
	fmt.Printf("installed %s; granted: %s\n", p.ID, orDash(strings.Join(caps, ",")))
	if len(missing) > 0 {
		fmt.Printf("disabled until these secrets are set in Settings → Plugins: %s\n", strings.Join(missing, ", "))
		return nil
	}
	if *enable {
		if err := db.SetPluginEnabled(ctx, p.ID, true); err != nil {
			return err
		}
		_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: "plugin.enabled", Username: "cli", Detail: p.ID})
		fmt.Println("enabled")
	} else {
		fmt.Println("enabled: the install enables a plugin whose secrets are set; --enable or Settings → Plugins switches it")
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pluginSwitch(args []string, on bool) error {
	fs := flag.NewFlagSet("plugin switch", flag.ContinueOnError)
	all := fs.Bool("all", false, "every plugin (disable only): the way back when a module broke the page")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if *all {
		if on {
			return errors.New("--all is for disable: enabling everything at once is not a thing to do blind")
		}
		list, err := db.ListPlugins(ctx)
		if err != nil {
			return err
		}
		for _, p := range list {
			if err := db.SetPluginEnabled(ctx, p.ID, false); err != nil {
				return err
			}
			_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: "plugin.disabled", Username: "cli", Detail: p.ID + " (--all)"})
			fmt.Printf("disabled %s\n", p.ID)
		}
		// And the module switch, which is what --all exists for.
		if err := db.SetSetting(ctx, "plugins.unsandboxed", "0"); err != nil {
			return err
		}
		_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: "plugins.unsandboxed", Username: "cli", Detail: "off (--all)"})
		fmt.Println("unsandboxed plugins: off. Restart the panel, or wait for its next poll, for running services to stop.")
		return nil
	}
	if fs.NArg() != 1 {
		return errors.New("one plugin id, or --all")
	}
	p, err := db.PluginByID(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if on && p.InstalledVersion == 0 {
		return errors.New("this plugin has not been installed; `vibepanel plugin install` first")
	}
	if err := db.SetPluginEnabled(ctx, p.ID, on); err != nil {
		return err
	}
	event, word := "plugin.disabled", "disabled"
	if on {
		event, word = "plugin.enabled", "enabled"
	}
	_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: event, Username: "cli", Detail: p.ID})
	fmt.Printf("%s %s\n", word, p.ID)
	return nil
}

func pluginRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("one plugin id")
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.DeletePlugin(ctx, args[0]); err != nil {
		return err
	}
	_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: "plugin.removed", Username: "cli", Detail: args[0]})
	fmt.Printf("removed %s\n", args[0])
	return nil
}

func pluginExport(args []string) error {
	fs := flag.NewFlagSet("plugin export", flag.ContinueOnError)
	ver := fs.Int("version", 0, "the version to export, default the installed one")
	out := fs.String("o", "", "where to write the zip (default <id>-<version>.zip)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("one plugin id")
	}
	ctx := context.Background()
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	p, err := db.PluginByID(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	v := *ver
	if v == 0 {
		v = p.InstalledVersion
	}
	if v == 0 {
		versions, _ := db.ListPluginVersions(ctx, p.ID)
		if len(versions) == 0 {
			return errors.New("no version to export")
		}
		v = versions[0].Version
	}
	data, m, err := httpapi.PluginArchive(ctx, db, p.ID, v)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = p.ID + "-" + m.Version + ".zip"
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // an archive the person asked for
		return err
	}
	fmt.Printf("wrote %s (v%d, %s)\n", path, v, humanBytes(int64(len(data))))
	return nil
}

func pluginCaps(args []string) error {
	fs := flag.NewFlagSet("plugin caps", flag.ContinueOnError)
	zh := fs.Bool("zh", false, "print the sentences in Chinese")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lang := "en"
	if *zh {
		lang = "zh"
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, c := range plugins.Capabilities() {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, c.Kind, c.Words.In(lang))
	}
	n := plugins.NetCapability("<host>")
	fmt.Fprintf(tw, "%s\t%s\t%s\n", n.Name, n.Kind, n.Words.In(lang))
	return tw.Flush()
}

// pluginInit is New plugin from a shell (docs/plugins.md §9): the same
// scaffold, into a directory of the person's choosing. --dev also stores the
// directory as a version and turns dev mode on, which is what the button
// does; without it the files are written and nothing in the panel changes,
// for a machine that has no panel on it.
func pluginInit(args []string) error {
	fs := flag.NewFlagSet("plugin init", flag.ContinueOnError)
	tpl := fs.String("template", "pane", "theme, pane, service, process or full")
	name := fs.String("name", "", "the plugin's name; the id is made from it (default: the directory's name)")
	pid := fs.String("id", "", "the plugin's id, when the one made from the name is not wanted")
	dev := fs.Bool("dev", false, "also register it with the panel in dev mode, as the New plugin button does")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("one directory: vibepanel plugin init <dir> --template <t>")
	}
	dir, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	if *name == "" {
		*name = strings.TrimPrefix(filepath.Base(dir), httpapi.PluginDirPrefix)
	}
	if *pid == "" {
		*pid = plugins.Slug(*name)
	}
	if err := plugins.Scaffold(dir, *tpl, *pid, *name); err != nil {
		return err
	}
	ctx := context.Background()
	pages.GitInit(ctx, dir)
	fmt.Printf("wrote a %s plugin %s to %s\n", *tpl, *pid, dir)
	if !*dev {
		fmt.Println("next: read AGENTS.md there; `vibepanel plugin check` and `vibepanel plugin describe` as you go;")
		fmt.Println("      `vibepanel plugin init --dev` or Settings → Plugins → From a directory + Dev mode to see it live")
		return nil
	}
	_, db, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	owner, err := db.FirstUserID(ctx)
	if err != nil {
		return errors.New("the panel has no account yet; finish setup first")
	}
	b, err := plugins.ReadDir(dir)
	if err != nil {
		return err
	}
	p, _, err := httpapi.AddPluginBundle(ctx, db, owner, b, dir, "cli")
	if err != nil {
		return err
	}
	if err := db.UpdatePluginSource(ctx, p.ID, dir, true); err != nil {
		return err
	}
	_ = db.Audit(ctx, store.AuditEntry{At: time.Now().Unix(), Event: "plugin.created", Username: "cli", Detail: p.ID + " from " + *tpl + " (" + dir + ")"})
	fmt.Printf("registered %s in dev mode: the card is on Settings → Plugins, running this directory\n", p.ID)
	fmt.Println("the panel picks the row up on its next poll; `vibepanel plugin install --grant …` when it is ready")
	return nil
}
