package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/hooks"
)

// cmdTune adjusts another tool's configuration, and says so before and after.
//
// `vibepanel tune claude` prints what it would change and exits; `--apply`
// writes it, having copied the file beside itself first. The dry run is the
// default because this is the panel editing a file it does not own: the
// installer asks a question, and the answer to a question nobody could see the
// consequences of is not consent.
//
// It deliberately does not open the app. Every other subcommand does, which
// means a database, a tmux socket and a config -- none of which this needs, and
// all of which would make `tune` fail on a machine where the panel is installed
// but has never run. The installer runs it in exactly that state.
func cmdTune(args []string) error {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the changes (default: print them and exit)")
	// The installer has already asked somebody which language it speaks, and a
	// report in the other one is the half of the screen they cannot read.
	lang := fs.String("lang", "en", "en or zh")
	// Set by deploy/install.sh, which prints this and then asks the question
	// itself. Without it the dry run ends "Add --apply to write it" and the
	// next line on screen asks "Apply these?" -- one action described two ways,
	// one of which is a command the person is not being asked to run.
	asking := fs.Bool("asking", false, "a question follows; leave out the advice to re-run with --apply")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: vibepanel tune claude [--apply]\n\n")
		fs.PrintDefaults()
	}

	var what string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		what, rest = args[0], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if what != "claude" {
		return fmt.Errorf("tune: say what to tune (only `claude` so far)")
	}

	st, err := hooks.InspectTune()
	if err != nil {
		return err
	}
	if *apply {
		if st, err = hooks.ApplyTune(); err != nil {
			return err
		}
	}
	writeTuneReport(os.Stdout, st, *apply, *lang, *asking)
	return nil
}

// writeTuneReport prints one line per setting, changed or not.
//
// Both kinds, because "already set" and "just changed" are different facts
// about somebody's machine and a report showing only the second reads as though
// the rest had been left alone.
func writeTuneReport(w io.Writer, st hooks.TuneStatus, applied bool, lang string, asking bool) {
	zh := lang == "zh"
	fmt.Fprintf(w, "%s\n", st.Path)
	if !st.Exists {
		switch {
		case zh && applied:
			fmt.Fprintln(w, "  （原本没有这个文件，已新建）")
		case zh:
			fmt.Fprintln(w, "  （还没有这个文件）")
		case applied:
			fmt.Fprintln(w, "  (no such file yet; created)")
		default:
			fmt.Fprintln(w, "  (no such file yet)")
		}
	}
	for _, r := range st.Rows {
		mark := "="
		if !r.Same {
			mark = "+"
			if applied {
				mark = "*"
			}
		}
		what := r.What
		if zh && r.WhatZH != "" {
			what = r.WhatZH
		}
		fmt.Fprintf(w, "  %s %-40s %-6s  %s\n", mark, r.Key, jsonish(r.Want), what)
		// What is being replaced, when it was something rather than nothing.
		//
		// attribution.commit is the one that stings: somebody with a
		// Signed-off-by trailer configured has it overwritten with the empty
		// string, and a summary that lists only the new value tells them their
		// setting is gone by not mentioning it. Absent keys are not reported --
		// there is nothing to say about a key that was not there.
		if !r.Same && r.Have != nil {
			was := "was"
			if zh {
				was = "原本是"
			}
			fmt.Fprintf(w, "  %-42s %s %s\n", "", was, jsonish(r.Have))
		}
	}
	fmt.Fprintln(w)
	switch {
	case st.Changes == 0 && zh:
		fmt.Fprintln(w, "无需改动：上面每一条文件里已经是这样了。")
	case st.Changes == 0:
		fmt.Fprintln(w, "Nothing to change: every setting above already says this.")
	case applied && zh:
		fmt.Fprintf(w, "改了 %d 条。改之前已把原文件复制到旁边；文件里其他内容一律没动。\n", st.Changes)
	case applied:
		fmt.Fprintf(w, "Changed %d. The file was copied beside itself first; nothing else in it was touched.\n", st.Changes)
	case zh && asking:
		fmt.Fprintf(w, "会改 %d 条（标 + 的）。现在还什么都没写。\n", st.Changes)
	case zh:
		fmt.Fprintf(w, "会改 %d 条（标 + 的）。现在还什么都没写。加 --apply 才会写入。\n", st.Changes)
	case asking:
		fmt.Fprintf(w, "Would change %d (marked +). Nothing has been written.\n", st.Changes)
	default:
		fmt.Fprintf(w, "Would change %d (marked +). Nothing has been written. Add --apply to write it.\n", st.Changes)
	}
}

// jsonish renders a wanted value the way it appears in the file.
func jsonish(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return `"` + t + `"`
	default:
		return fmt.Sprint(v)
	}
}
