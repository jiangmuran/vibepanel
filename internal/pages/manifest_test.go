package pages

import (
	"strings"
	"testing"
)

func TestAManifestIsStrictAboutWhatItSays(t *testing.T) {
	for _, tc := range []struct {
		name, raw, wantErr string
	}{
		{"minimal", `{"sdk":1,"name":"Lobby"}`, ""},
		{"every section", `{"sdk":1,"name":"x","sections":["sessions","todos","spend","trend","flow","feed","repo"],
			"spend":{"days":30,"months":true,"heatmap":true,"split":["tool","project","model"]},
			"repo":{"days":14,"prs":true},"flow":{"by":"day","days":7}}`, ""},
		{"wrong sdk", `{"sdk":2,"name":"x"}`, "sdk must be 1"},
		{"no name", `{"sdk":1}`, "name is required"},
		{"long name", `{"sdk":1,"name":"` + strings.Repeat("字", MaxName+1) + `"}`, "at most"},
		// A misspelt key that is silently ignored is a page that draws nothing
		// and never says why.
		{"unknown key", `{"sdk":1,"name":"x","section":["sessions"]}`, "unknown field"},
		{"unknown section", `{"sdk":1,"name":"x","sections":["paths"]}`, "unknown section"},
		{"twice", `{"sdk":1,"name":"x","sections":["feed","feed"]}`, "twice"},
		{"options without section", `{"sdk":1,"name":"x","spend":{"days":3}}`, `need "spend"`},
		{"repo options without section", `{"sdk":1,"name":"x","repo":{"prs":true}}`, `need "repo"`},
		{"flow by", `{"sdk":1,"name":"x","sections":["flow"],"flow":{"by":"week"}}`, "flow.by"},
		{"days too many", `{"sdk":1,"name":"x","sections":["spend"],"spend":{"days":9999}}`, "spend.days"},
		{"negative days", `{"sdk":1,"name":"x","sections":["repo"],"repo":{"days":-1}}`, "repo.days"},
		{"split", `{"sdk":1,"name":"x","sections":["spend"],"spend":{"split":["path"]}}`, "spend.split"},
		// The hosts are a fixed list. A field anybody could fill in would be
		// "may send what it read to X".
		{"script host", `{"sdk":1,"name":"x","scriptHosts":["unpkg.com"]}`, "not allowed"},
		{"allowed host", `{"sdk":1,"name":"x","scriptHosts":["cdn.jsdelivr.net"]}`, ""},
		{"viewport", `{"sdk":1,"name":"x","viewports":["watch"]}`, "unknown screen"},
		{"two values", `{"sdk":1,"name":"x"}{}`, "more than one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.raw))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refused a valid manifest: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// A manifest reads into exactly the sections it names and the options it sets,
// and nothing else: a page must not receive a section it did not ask for.
func TestAManifestNeedsExactlyWhatItNames(t *testing.T) {
	one := map[string]Needs{
		SectionSessions: {Sessions: true},
		SectionTodos:    {Todos: true},
		SectionSpend:    {Spend: true},
		SectionTrend:    {Trend: true},
		SectionFlow:     {Flow: true, FlowHours: true},
		SectionFeed:     {Feed: true},
		SectionRepo:     {Repo: true},
	}
	for _, section := range Sections() {
		m := Manifest{SDK: 1, Name: "x", Sections: []string{section}}
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: %v", section, err)
		}
		if got := m.Needs(); got != one[section] {
			t.Errorf("%s needs %+v, want %+v", section, got, one[section])
		}
	}

	full := Manifest{SDK: 1, Name: "x", Sections: []string{SectionSpend, SectionRepo, SectionFlow},
		Spend: &SpendOptions{Days: 12, Months: true, Heatmap: true, Split: []string{"model"}},
		Repo:  &RepoOptions{Days: 9, PRs: true}, Flow: &FlowOptions{By: "day", Days: 7}}
	if err := full.Validate(); err != nil {
		t.Fatal(err)
	}
	want := Needs{Spend: true, SpendDays: 12, SpendMonths: true, SpendHeatmap: true, SpendModels: true,
		Repo: true, RepoDays: 9, RepoPRs: true, Flow: true, FlowDays: 7}
	if got := full.Needs(); got != want {
		t.Errorf("needs %+v\nwant  %+v", got, want)
	}

	// Options present, but not the day range: months are asked for and days
	// are not, so days must not be sent.
	months := Manifest{SDK: 1, Name: "x", Sections: []string{SectionSpend, SectionRepo},
		Spend: &SpendOptions{Months: true}, Repo: &RepoOptions{PRs: true}}
	if got := months.Needs(); got.SpendDays != 0 || got.RepoDays != 0 || !got.SpendMonths || !got.RepoPRs {
		t.Errorf("options without days: %+v", got)
	}

	if got := (Manifest{SDK: 1, Name: "none"}).Needs(); got != (Needs{}) {
		t.Errorf("a page with no sections needs %+v", got)
	}
}

// A stored manifest is read leniently: a wall must not go dark because a
// later build stopped accepting something it published. It falls closed --
// what no longer validates is dropped, never widened.
func TestAStoredManifestDropsWhatNoLongerValidates(t *testing.T) {
	raw := []byte(`{"sdk":1,"name":"Lobby","sections":["sessions","retired"],
		"scriptHosts":["cdn.jsdelivr.net","evil.example"],"spend":{"days":3}}`)
	m := DecodeStored(raw)
	if m.Name != "Lobby" {
		t.Errorf("name = %q", m.Name)
	}
	if !contains(m.Sections, SectionSessions) || contains(m.Sections, "retired") {
		t.Errorf("sections = %v", m.Sections)
	}
	if contains(m.ScriptHosts, "evil.example") {
		t.Error("a host that no longer validates survived a lenient read")
	}
	if err := m.Validate(); err != nil {
		t.Errorf("the lenient read produced an invalid manifest: %v", err)
	}
	if got := DecodeStored([]byte("not json")); got.Validate() != nil {
		t.Errorf("garbage decoded to an invalid manifest: %+v", got)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
