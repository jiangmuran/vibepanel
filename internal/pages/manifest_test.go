package pages

import (
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
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

// A manifest asks in the board's own vocabulary, which is why it cannot ask
// for more than a board can. Every section, and every option on one, must
// compile to a board the board's validator accepts and whose Needs are exactly
// what the manifest named -- no more.
func TestAManifestCompilesToExactlyTheSectionsItNames(t *testing.T) {
	needsOf := map[string][]string{
		SectionSessions: {store.NeedSessions},
		SectionTodos:    {store.NeedTodos},
		SectionSpend:    {store.NeedSpend},
		SectionTrend:    {store.NeedTrend},
		SectionFlow:     {store.NeedFlow},
		SectionFeed:     {store.NeedFeed},
		SectionRepo:     {store.NeedRepo},
	}
	for _, section := range Sections() {
		m := Manifest{SDK: 1, Name: "x", Sections: []string{section}}
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: %v", section, err)
		}
		needs := m.Board().Needs()
		for _, want := range needsOf[section] {
			if !needs[want] {
				t.Errorf("%s compiled to a board that does not need %s: %v", section, want, needs)
			}
		}
		for n := range needs {
			if !contains(needsOf[section], n) {
				t.Errorf("%s compiled to a board that also needs %s; a page must not receive a "+
					"section it did not ask for", section, n)
			}
		}
	}

	full := Manifest{SDK: 1, Name: "x", Sections: []string{SectionSpend, SectionRepo},
		Spend: &SpendOptions{Days: 12, Months: true, Heatmap: true, Split: []string{"model"}},
		Repo:  &RepoOptions{Days: 9, PRs: true}}
	if err := full.Validate(); err != nil {
		t.Fatal(err)
	}
	needs := full.Board().Needs()
	for _, want := range []string{store.NeedSpendDays, store.NeedSpendMonths, store.NeedSpendHeatmap,
		store.NeedSpendModels, store.NeedRepoDays, store.NeedRepoPRs} {
		if !needs[want] {
			t.Errorf("options did not reach the board: missing %s in %v", want, needs)
		}
	}
	for _, not := range []string{store.NeedSpendTools, store.NeedSpendProjects, store.NeedSessions} {
		if needs[not] {
			t.Errorf("the board needs %s, which nothing asked for", not)
		}
	}

	// Without the options, no series: a page that did not ask for a year of
	// days must not be sent one.
	plain := Manifest{SDK: 1, Name: "x", Sections: []string{SectionSpend, SectionRepo}}
	pn := plain.Board().Needs()
	for _, not := range []string{store.NeedSpendDays, store.NeedSpendHeatmap, store.NeedRepoDays, store.NeedRepoPRs} {
		if pn[not] {
			t.Errorf("a page with no options was given %s", not)
		}
	}

	// Options present, but not the day range: months are asked for and days
	// are not, so days must not be sent.
	months := Manifest{SDK: 1, Name: "x", Sections: []string{SectionSpend, SectionRepo},
		Spend: &SpendOptions{Months: true}, Repo: &RepoOptions{PRs: true}}
	mn := months.Board().Needs()
	if mn[store.NeedSpendDays] || mn[store.NeedRepoDays] {
		t.Errorf("options without days were given a day series: %v", mn)
	}
	if !mn[store.NeedSpendMonths] || !mn[store.NeedRepoPRs] {
		t.Errorf("the options that were set did not reach the board: %v", mn)
	}

	if _, err := store.ValidateBoard(Manifest{SDK: 1, Name: "none"}.Board()); err != nil {
		t.Errorf("a page with no sections compiles to a board the validator refuses: %v", err)
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
