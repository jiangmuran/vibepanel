package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The reporting section does not offer every agent the panel knows about, and
// the setting is what decides. Reported as 「设置里面可以隐藏/配置」: a panel
// running one agent had five rows of install buttons for other people's tools.
func TestReportingOffersTheDefaultAgentsUntilToldOtherwise(t *testing.T) {
	ts, _ := newTestServer(t)
	st := hookStatusOf(t, ts.URL, ts.Client())
	if !slices.Equal(st.AgentsShown, hookAgentsShownDefault) {
		t.Errorf("a panel nobody has configured offers %v, want %v",
			st.AgentsShown, hookAgentsShownDefault)
	}
}

func TestAgentsShownIsStoredAndReadBack(t *testing.T) {
	ts, _ := newTestServer(t)
	putHookAgents(t, ts.URL, ts.Client(), `{"agents":["zcode","claude"]}`, http.StatusOK)
	st := hookStatusOf(t, ts.URL, ts.Client())
	// hookAgents order, not the order they were sent in: the rows must not
	// reshuffle when somebody ticks one back on.
	if !slices.Equal(st.AgentsShown, []string{"claude", "zcode"}) {
		t.Errorf("stored [zcode claude] and read back %v", st.AgentsShown)
	}
}

// "None of them" is a real answer -- somebody who runs one agent and installed
// its hooks from the CLI wants none of these rows -- and it has to survive a
// reload. It is the case a comma-separated setting cannot spell: an empty
// string is also what "never set" looks like, and that came back as the
// default list.
func TestAgentsShownCanBeEmptyAndStayEmpty(t *testing.T) {
	ts, _ := newTestServer(t)
	putHookAgents(t, ts.URL, ts.Client(), `{"agents":[]}`, http.StatusOK)
	st := hookStatusOf(t, ts.URL, ts.Client())
	if len(st.AgentsShown) != 0 {
		t.Errorf("turned every row off and got %v back", st.AgentsShown)
	}
}

// The names decide which rows a person can reach, so one nobody recognises has
// to be refused rather than stored: stored, it would sit in the setting for
// good, and nothing on the page could take it out again.
func TestAgentsShownRefusesANameTheServerDoesNotKnow(t *testing.T) {
	ts, _ := newTestServer(t)
	putHookAgents(t, ts.URL, ts.Client(), `{"agents":["claude","clyde"]}`, http.StatusBadRequest)
	st := hookStatusOf(t, ts.URL, ts.Client())
	if !slices.Equal(st.AgentsShown, hookAgentsShownDefault) {
		t.Errorf("a refused write changed the setting to %v", st.AgentsShown)
	}
}

// Install and uninstall answer with the same payload the GET does, and the
// page replaces everything it has with it. A field only the GET carried
// vanished from the page the moment somebody pressed a button -- and this one
// decides which rows are drawn, so the rows would have gone with it.
func TestInstallingAnswersWithTheAgentsShownToo(t *testing.T) {
	ts, _ := newTestServer(t)
	t.Setenv("HOME", t.TempDir())
	res, err := ts.Client().Post(ts.URL+"/api/settings/hooks?agent=opencode", "application/json", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("install: %s: %s", res.Status, b)
	}
	var st hookStatusResponse
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(st.AgentsShown) == 0 {
		t.Error("the install answer does not say which agents the page offers")
	}
}

// The frontend has its own table of these -- the settings rows and the tour
// read it -- and it is hand-written, like wire.ts and for the same reason.
// An agent the server accepts and the page never offers is a button nobody can
// press; one the page offers and the server refuses is a 400 on a tick.
func TestTypeScriptAgentTableMatchesTheServer(t *testing.T) {
	const path = "../../web/src/components/hookAgents.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so nothing was compared: %v", path, err)
	}
	block := regexp.MustCompile(`(?s)HOOK_AGENTS[^=]*= \[(.*?)\n\]`).FindStringSubmatch(string(src))
	if block == nil {
		t.Fatalf("no HOOK_AGENTS table found in %s; the reader is reading nothing", path)
	}
	ids := regexp.MustCompile(`id: '([a-z]+)'`).FindAllStringSubmatch(block[1], -1)
	var declared []string
	for _, m := range ids {
		declared = append(declared, m[1])
	}
	if !slices.Equal(declared, hookAgents) {
		t.Errorf("%s offers %v; the server accepts %v", path, declared, hookAgents)
	}
	// And the HookAgent union the calls are typed against.
	union := regexp.MustCompile(`export type HookAgent = ([^\n]+)`).
		FindStringSubmatch(mustRead(t, "../../web/src/protocol/wire.ts"))
	if union == nil {
		t.Fatal("no HookAgent type found in wire.ts")
	}
	for _, agent := range hookAgents {
		if !strings.Contains(union[1], "'"+agent+"'") {
			t.Errorf("wire.ts HookAgent does not include %q, which the server accepts", agent)
		}
	}
}

// hookStatusOf is GET /api/settings/hooks, decoded.
func hookStatusOf(t *testing.T, base string, client *http.Client) hookStatusResponse {
	t.Helper()
	res, err := client.Get(base + "/api/settings/hooks")
	if err != nil {
		t.Fatalf("GET hooks: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("GET hooks: %s: %s", res.Status, b)
	}
	var st hookStatusResponse
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return st
}

// putHookAgents is the settings page ticking a box, and what came back.
func putHookAgents(t *testing.T, base string, client *http.Client, body string, want int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/api/settings/hooks/agents", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT agents: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("PUT agents: %s: %s (want %d)", res.Status, b, want)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
