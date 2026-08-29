package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/hooks"
)

// The report is the disclosure, so every key has to be in it.
//
// deploy/install.sh prints this and then asks "apply these?". Whatever the
// report leaves out is a change somebody agreed to without being shown it, and
// a key quietly missing from the output is invisible: the count at the bottom
// still moves, and the question still reads as though the list above it were
// the whole list.
func TestTheReportNamesEverySetting(t *testing.T) {
	st, err := hooks.InspectTune()
	if err != nil {
		t.Skipf("cannot read this machine's settings: %v", err)
	}
	for _, lang := range []string{"en", "zh"} {
		var buf bytes.Buffer
		writeTuneReport(&buf, st, false, lang, false)
		out := buf.String()
		for _, tw := range hooks.Tweaks() {
			key := strings.Join(tw.Path, ".")
			if !strings.Contains(out, key) {
				t.Errorf("[%s] the report does not name %s:\n%s", lang, key, out)
			}
			if !strings.Contains(out, tw.Say(lang)) {
				t.Errorf("[%s] the report does not say what %s does", lang, key)
			}
		}
		if !strings.Contains(out, st.Path) {
			t.Errorf("[%s] the report does not say which file", lang)
		}
	}
}

// The two languages are actually different.
//
// `--lang zh` reaching the wrong branch, or a `zh` that falls through to
// English, is a screen the installer's own string-table check cannot see:
// these sentences do not live in its table.
func TestTheReportChangesLanguage(t *testing.T) {
	st := hooks.TuneStatus{
		Path: "/tmp/x/settings.json",
		Rows: []hooks.TuneRow{{
			Key: "autoUploadSessions", What: "an English sentence", WhatZH: "一句中文",
			Want: false, Same: false,
		}},
		Changes: 1,
	}
	var en, zh bytes.Buffer
	writeTuneReport(&en, st, false, "en", false)
	writeTuneReport(&zh, st, false, "zh", false)

	if !strings.Contains(en.String(), "an English sentence") || strings.Contains(en.String(), "一句中文") {
		t.Errorf("en report:\n%s", en.String())
	}
	if !strings.Contains(zh.String(), "一句中文") || strings.Contains(zh.String(), "an English sentence") {
		t.Errorf("zh report:\n%s", zh.String())
	}
	// An unknown language reads rather than disappearing.
	var other bytes.Buffer
	writeTuneReport(&other, st, false, "de", false)
	if !strings.Contains(other.String(), "an English sentence") {
		t.Errorf("an unknown language produced no description:\n%s", other.String())
	}
}

// A dry run says nothing was written, and an applied run says what was.
func TestTheReportDistinguishesWouldFromDid(t *testing.T) {
	st := hooks.TuneStatus{
		Path: "/tmp/x/settings.json", Exists: true,
		Rows:    []hooks.TuneRow{{Key: "k", What: "w", WhatZH: "w", Want: false}},
		Changes: 1,
	}
	var dry, did bytes.Buffer
	writeTuneReport(&dry, st, false, "en", false)
	writeTuneReport(&did, st, true, "en", false)

	if !strings.Contains(dry.String(), "Nothing has been written") {
		t.Errorf("a dry run did not say so:\n%s", dry.String())
	}
	if strings.Contains(did.String(), "Nothing has been written") {
		t.Errorf("an applied run said nothing was written:\n%s", did.String())
	}
	if !strings.Contains(did.String(), "copied beside itself") {
		t.Errorf("an applied run did not mention the backup:\n%s", did.String())
	}
}

// What a value is being replaced with, and what it was.
func TestTheReportShowsWhatIsBeingReplaced(t *testing.T) {
	st := hooks.TuneStatus{
		Path: "/tmp/x", Exists: true,
		Rows: []hooks.TuneRow{
			{Key: "attribution.commit", What: "w", WhatZH: "w", Want: "", Have: "Signed-off-by: me"},
			{Key: "untouched", What: "w", WhatZH: "w", Want: false, Have: false, Same: true},
		},
		Changes: 1,
	}
	var buf bytes.Buffer
	writeTuneReport(&buf, st, false, "en", false)
	out := buf.String()
	if !strings.Contains(out, `was "Signed-off-by: me"`) {
		t.Errorf("the value being overwritten is not shown:\n%s", out)
	}
	// And a row that is not changing does not claim a previous value.
	if strings.Count(out, "was ") != 1 {
		t.Errorf("a row that is not changing reported a previous value:\n%s", out)
	}
}

// --asking drops the "run it again with --apply" advice and nothing else.
//
// The installer prints the dry run and then asks the question itself, so that
// sentence describes a second way to do the thing the next line is asking
// about.
func TestAskingDropsTheAdviceAndKeepsTheRest(t *testing.T) {
	st := hooks.TuneStatus{
		Path: "/tmp/x", Exists: true,
		Rows:    []hooks.TuneRow{{Key: "k", What: "en words", WhatZH: "中文", Want: false}},
		Changes: 1,
	}
	for _, lang := range []string{"en", "zh"} {
		var plain, asked bytes.Buffer
		writeTuneReport(&plain, st, false, lang, false)
		writeTuneReport(&asked, st, false, lang, true)
		if !strings.Contains(plain.String(), "--apply") {
			t.Errorf("[%s] the plain report does not offer --apply:\n%s", lang, plain.String())
		}
		if strings.Contains(asked.String(), "--apply") {
			t.Errorf("[%s] --asking still told them to run --apply:\n%s", lang, asked.String())
		}
		// The list and the count survive: this drops advice, not disclosure.
		if !strings.Contains(asked.String(), "k") || !strings.Contains(asked.String(), "1") {
			t.Errorf("[%s] --asking dropped part of the report:\n%s", lang, asked.String())
		}
	}
}
