package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/session"
)

// Fixtures are made-up snapshots a page is tested against, written into a new
// page's fixtures/ directory and loaded by the SDK with ?fixture=<name>.
//
// Built here, out of the real snapshot structs, rather than kept as JSON files
// beside the templates. A JSON file is a second copy of the wire shape that no
// compiler reads, and a fixture that has drifted from what the panel sends is a
// page tested against a panel that does not exist.
//
// Never exported from the running panel's own data. A fixture holding this
// machine's real session titles would be a file in a directory that is about to
// become a git repository with a remote.
//
// Each one exists to break a careless page visibly:
//
//	busy     forty sessions in twelve projects: overflow, a list that does not fit
//	empty    nothing running, nothing counted yet: NaN, empty frames, zero vs unknown
//	counts   every name empty: the mode that is the default
//	hostile  markup and 200-character CJK in every name: innerHTML shows itself
//	stale    live, but the panel has stopped keeping its records
//	revoked  the link no longer works

type pageFixture struct {
	Status   string         `json:"status"`
	Snapshot *shareSnapshot `json:"snapshot"`
}

// fixtureAt is the moment every fixture was "taken". Durations on screen are
// relative to it, because the SDK measures them against the snapshot's own
// clock rather than the browser's.
var fixtureAt = time.Date(2026, 9, 1, 15, 30, 0, 0, time.UTC).Unix()

// PageFixtures renders every fixture, by name, as the file content.
func PageFixtures() map[string][]byte {
	out := map[string][]byte{}
	for name, f := range map[string]pageFixture{
		"busy":    {Status: "live", Snapshot: fixtureBusy(namesPlain)},
		"empty":   {Status: "live", Snapshot: fixtureEmpty()},
		"counts":  {Status: "live", Snapshot: fixtureCounts()},
		"hostile": {Status: "live", Snapshot: fixtureBusy(namesHostile)},
		"stale":   {Status: "live", Snapshot: fixtureStale()},
		"revoked": {Status: "revoked"},
	} {
		raw, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			panic(fmt.Sprintf("fixture %s: %v", name, err)) // a struct literal that cannot marshal is a build bug
		}
		out[name] = append(raw, '\n')
	}
	return out
}

func namesPlain(kind string, i int) string {
	projects := []string{"billing-api", "web", "mobile", "infra", "docs", "search",
		"payments", "auth", "data-pipeline", "design-system", "notifications", "admin"}
	tasks := []string{"fix flaky checkout test", "migrate to the new router", "write the release notes",
		"profile the slow dashboard query", "add retry to the webhook sender", "review PR 482",
		"port the settings page", "update dependencies", "investigate memory growth",
		"draft the onboarding copy"}
	if kind == "project" {
		return projects[i%len(projects)]
	}
	return tasks[i%len(tasks)]
}

func namesHostile(kind string, i int) string {
	hostile := []string{
		`<img src=x onerror="document.body.style.background='red'">`,
		`</li></ul><h1 style="font-size:200px">INJECTED</h1>`,
		strings.Repeat("非常长的会话标题用来测试溢出和截断", 12),
		"\u202eRTL override \u202d mixed",
		`{{constructor.constructor('return 1')()}}`,
		"line one\nline two\ttabbed",
		`"; background: url(https://example.com/x); "`,
		"👩‍💻🧪🔥 emoji only",
	}
	return hostile[(i+len(kind))%len(hostile)]
}

func fixtureBusy(name func(kind string, i int) string) *shareSnapshot {
	s := fixtureBase("names")
	s.Name = "Engineering wall"
	s.Remark = "Third floor, by the kitchen"
	states := []session.State{session.StateWorking, session.StateWorking, session.StateWaiting,
		session.StateDone, session.StateWorking}
	kinds := []string{"agent", "agent", "agent", "shell", "other"}
	for p := 0; p < 12; p++ {
		s.Projects = append(s.Projects, shareProject{ID: fmt.Sprintf("p%02x", p*37+11), Name: name("project", p)})
	}
	for i := 0; i < 40; i++ {
		p := (i * 7) % 12
		st := states[i%len(states)]
		row := shareSession{
			ID: fmt.Sprintf("s%04x", i*97+5), ProjectID: s.Projects[p].ID, Name: name("session", i),
			State: st, Kind: kinds[i%len(kinds)], StateChangedAt: fixtureAt - int64(40+i*173),
			Measured: i%9 != 0, CPUPercent: float64((i*37)%180) + 0.5, RSS: uint64(80+i*13) << 20,
			Procs: 1 + i%6,
		}
		if i == 17 {
			row.Exited, row.ExitStatus = true, 1
		}
		s.Sessions = append(s.Sessions, row)
		grp := &s.Projects[p]
		grp.Total++
		switch st {
		case session.StateWaiting:
			grp.Waiting++
			s.Counts.Waiting++
			if s.Counts.LongestWaitAt == 0 || row.StateChangedAt < s.Counts.LongestWaitAt {
				s.Counts.LongestWaitAt = row.StateChangedAt
			}
		case session.StateWorking:
			grp.Working++
			s.Counts.Working++
		default:
			grp.Done++
			s.Counts.Done++
			s.Counts.DoneToday++
		}
		if row.Exited {
			s.Counts.Exited++
			s.Counts.Crashed++
		}
	}
	s.Counts.Sessions, s.Counts.Projects = len(s.Sessions), len(s.Projects)

	spend := fixtureSpend(true)
	for i, p := range s.Projects[:6] {
		spend.Projects = append(spend.Projects, shareSpendGroup{ID: p.ID, Name: p.Name,
			Total: int64(9_000_000 - i*1_300_000), Requests: int64(900 - i*120)})
	}
	s.Spend = spend

	todos := &shareTodos{Open: 14, Done: 31, ClosedToday: 6}
	for i, p := range s.Projects[:5] {
		todos.Projects = append(todos.Projects, shareTodosProject{ID: p.ID, Name: p.Name,
			Open: 1 + i%4, Done: 3 + i*2, ClosedToday: i % 3})
	}
	s.Todos = todos

	for i := 0; i < 24; i++ {
		at := fixtureAt - int64((23-i)*90)
		sname := name("session", i)
		from, to := states[i%len(states)], states[(i+1)%len(states)]
		s.Feed.Entries = append(s.Feed.Entries, shareFeedEntry{At: at, SessionID: s.Sessions[i].ID,
			ProjectID: s.Sessions[i].ProjectID, Name: sname, From: from, To: to,
			ForSeconds: int64(60 + i*47)})
	}
	for i, j := 0, len(s.Feed.Entries)-1; i < j; i, j = i+1, j-1 {
		s.Feed.Entries[i], s.Feed.Entries[j] = s.Feed.Entries[j], s.Feed.Entries[i]
	}

	s.Repo.Readable, s.Repo.AgeSeconds = true, 42
	s.Repo.Repos, s.Repo.Projects = 9, 12
	s.Repo.Today = shareRepoTotals{Commits: 23, Added: 1840, Removed: 612, Files: 97}
	s.Repo.Window = shareRepoTotals{Commits: 214, Added: 20411, Removed: 9120, Files: 812}
	for d := 13; d >= 0; d-- {
		day := dayShift(time.UTC, time.Unix(fixtureAt, 0), -d)
		s.Repo.Days = append(s.Repo.Days, shareRepoDay{Label: day, Commits: 8 + (d*5)%17,
			Added: 600 + (d*431)%1900, Removed: 200 + (d*211)%900, Files: 20 + (d*7)%60})
	}
	for i, p := range s.Projects {
		s.Repo.ByProject = append(s.Repo.ByProject, shareRepoProject{ID: p.ID, Name: p.Name,
			Repo: i != 11, Today: shareRepoTotals{Commits: (i * 3) % 7, Added: (i * 97) % 400,
				Removed: (i * 53) % 200, Files: (i * 5) % 20},
			Window: shareRepoTotals{Commits: 10 + i*4, Added: 900 + i*300, Removed: 300 + i*90, Files: 40 + i*9},
			Ahead:  i % 3, Behind: (i + 1) % 2, Dirty: (i * 2) % 5})
	}
	s.Repo.PRs = &shareRepoPRs{Readable: true, AgeSeconds: 130, Open: 11, Draft: 3, Green: 6, Red: 2,
		Pending: 3, Approved: 4, ChangesRequested: 1, MergedToday: 5}
	return s
}

func fixtureCounts() *shareSnapshot {
	s := fixtureBusy(namesPlain)
	s.Detail = "counts"
	for i := range s.Projects {
		s.Projects[i].Name = ""
	}
	for i := range s.Sessions {
		s.Sessions[i].Name = ""
	}
	for i := range s.Spend.Projects {
		s.Spend.Projects[i].Name = ""
	}
	for i := range s.Todos.Projects {
		s.Todos.Projects[i].Name = ""
	}
	for i := range s.Feed.Entries {
		s.Feed.Entries[i].Name = ""
	}
	for i := range s.Repo.ByProject {
		s.Repo.ByProject[i].Name = ""
	}
	return s
}

func fixtureStale() *shareSnapshot {
	s := fixtureBusy(namesPlain)
	s.Stale = true
	return s
}

func fixtureEmpty() *shareSnapshot {
	s := fixtureBase("names")
	s.Name = "Quiet afternoon"
	s.Spend = fixtureSpend(false)
	s.Todos = &shareTodos{Projects: []shareTodosProject{}}
	// Not counted yet, which is not the same as nothing built. A page that
	// draws "0 commits" here has confused the two.
	s.Repo.Readable, s.Repo.AgeSeconds = false, -1
	return s
}

func fixtureBase(detail string) *shareSnapshot {
	cpu := 23.5
	s := &shareSnapshot{
		V: pages.SDKVersion, Page: &shareSnapshotPage{ID: "fixture", Version: 0, Draft: true},
		Sections: pages.Sections(), Params: map[string]any{},
		At: fixtureAt, Detail: detail, UsageReadable: true,
		Machine: shareMachine{CPUReadable: true, CPUPercent: &cpu, Cores: 16, Load1: 3.2, Load5: 2.8,
			Load15: 2.1, MemTotal: 64 << 30, MemAvailable: 21 << 30, SwapTotal: 8 << 30, SwapFree: 7 << 30,
			DiskTotal: 1 << 40, DiskFree: 410 << 30, Uptime: 9*86400 + 4*3600},
		Projects: []shareProject{}, Sessions: []shareSession{},
		Trend: &shareTrend{Every: int(trendSampleEvery / time.Second), Points: []shareTrendPoint{}},
		Flow: &shareFlow{Every: 3600, Since: fixtureAt - 15*3600, WindowDays: 14,
			Buckets: []shareFlowBucket{}},
		Feed: &shareFeed{Entries: []shareFeedEntry{}},
		Repo: &shareRepo{WindowDays: 14, Days: []shareRepoDay{}, ByProject: []shareRepoProject{}},
	}
	for i := 0; i < 60; i++ {
		c := 15 + float64((i*29)%55)
		s.Trend.Points = append(s.Trend.Points, shareTrendPoint{At: fixtureAt - int64((59-i)*s.Trend.Every),
			CPU: &c, Memory: 55 + float64((i*7)%20), Load: 1.5 + float64((i*13)%30)/10,
			Tokens: int64(i) * 190_000})
	}
	for h := 0; h < 16; h++ {
		b := shareFlowBucket{At: s.Flow.Since + int64(h*3600), Started: (h * 3) % 7,
			Waited: (h * 5) % 4, Finished: (h * 2) % 6, WaitSeconds: int64((h * 311) % 2400), WaitEnded: h % 4}
		s.Flow.Buckets = append(s.Flow.Buckets, b)
		s.Flow.Today.Started += b.Started
		s.Flow.Today.Waited += b.Waited
		s.Flow.Today.Finished += b.Finished
		s.Flow.Today.WaitSeconds += b.WaitSeconds
		s.Flow.Today.WaitEnded += b.WaitEnded
	}
	s.Flow.Window = shareFlowTotals{Started: 412, Waited: 230, Finished: 377, WaitSeconds: 190_000, WaitEnded: 221}
	return s
}

func fixtureSpend(readable bool) *shareSpend {
	sp := &shareSpend{Readable: readable, WindowDays: shareSpendWindowDays,
		Days: []shareSpendBucket{}, Months: []shareSpendBucket{}, Heatmap: []shareSpendBucket{},
		Tools: []shareSpendGroup{}, Models: []shareSpendGroup{}, Projects: []shareSpendGroup{}}
	if !readable {
		return sp
	}
	sp.ScannedAt = fixtureAt - 50
	sp.Date = dayIn(time.UTC, time.Unix(fixtureAt, 0))
	sp.HoursToday = 15.5
	bucket := func(label string, scale int64) shareSpendBucket {
		b := shareSpendBucket{Label: label, Input: 400_000 * scale, Output: 90_000 * scale,
			CacheRead: 2_900_000 * scale, CacheWrite: 310_000 * scale, Requests: 120 * scale}
		b.Total = b.Input + b.Output + b.CacheRead + b.CacheWrite
		return b
	}
	totals := func(b shareSpendBucket) shareSpendTotals {
		return shareSpendTotals{Input: b.Input, Output: b.Output, CacheRead: b.CacheRead,
			CacheWrite: b.CacheWrite, Requests: b.Requests, Total: b.Total}
	}
	day := time.Unix(fixtureAt, 0).UTC()
	for d := 29; d >= 0; d-- {
		sp.Days = append(sp.Days, bucket(dayShift(time.UTC, day, -d), 1+int64((d*7)%9)))
	}
	for d := 370; d >= 0; d-- {
		sp.Heatmap = append(sp.Heatmap, bucket(dayShift(time.UTC, day, -d), int64((d*13)%10)))
	}
	for m := 11; m >= 0; m-- {
		// A month label is a day label's first seven characters, which is how
		// the spend rollup builds them too.
		sp.Months = append(sp.Months, bucket(dayIn(time.UTC, day.AddDate(0, -m, 0))[:7], 20+int64((m*5)%30)))
	}
	sp.Today = totals(sp.Days[len(sp.Days)-1])
	sp.Yesterday = totals(sp.Days[len(sp.Days)-2])
	sp.Month = totals(bucket("", 31))
	sp.LastMonth = totals(bucket("", 44))
	sp.Window = totals(bucket("", 150))
	sp.AllTime = totals(bucket("", 900))
	sp.Tools = []shareSpendGroup{{ID: "claude", Name: "claude", Total: 51_000_000, Requests: 5200},
		{ID: "codex", Name: "codex", Total: 17_000_000, Requests: 2100}}
	sp.Models = []shareSpendGroup{{ID: "claude-opus", Name: "claude-opus", Total: 40_000_000, Requests: 3000},
		{ID: "claude-sonnet", Name: "claude-sonnet", Total: 11_000_000, Requests: 2200},
		{ID: "gpt-5-codex", Name: "gpt-5-codex", Total: 17_000_000, Requests: 2100}}
	return sp
}

// shapeFixture makes a fixture look like what this page would really receive:
// the page's own sections, with everything it did not ask for null, and its
// parameters filled from the link or the defaults.
//
// Applied as a fixture is served rather than baked in at scaffold time,
// because the manifest changes after the scaffold and a fixture carrying every
// section would let a page that forgot to ask for one pass every test.
// A file that is not a fixture this build wrote is served as it is.
func shapeFixture(raw []byte, m pages.Manifest, params map[string]any) []byte {
	var f pageFixture
	if err := json.Unmarshal(raw, &f); err != nil || f.Snapshot == nil {
		return raw
	}
	s := f.Snapshot
	s.Sections = m.SectionNames()
	s.Params = pages.ResolveParams(m.Params, params)
	want := map[string]bool{}
	for _, name := range s.Sections {
		want[name] = true
	}
	if !want[pages.SectionSessions] {
		s.Sessions = []shareSession{}
	}
	if !want[pages.SectionSpend] {
		s.Spend = nil
	}
	if !want[pages.SectionTodos] {
		s.Todos = nil
	}
	if !want[pages.SectionTrend] {
		s.Trend = nil
	}
	if !want[pages.SectionFlow] {
		s.Flow = nil
	}
	if !want[pages.SectionFeed] {
		s.Feed = nil
	}
	if !want[pages.SectionRepo] {
		s.Repo = nil
	} else if m.Repo == nil || !m.Repo.PRs {
		s.Repo.PRs = nil
	}
	if s.Spend != nil {
		if m.Spend == nil || m.Spend.Days == 0 {
			s.Spend.Days = []shareSpendBucket{}
		}
		if m.Spend == nil || !m.Spend.Months {
			s.Spend.Months = []shareSpendBucket{}
		}
		if m.Spend == nil || !m.Spend.Heatmap {
			s.Spend.Heatmap = []shareSpendBucket{}
		}
		split := map[string]bool{}
		if m.Spend != nil {
			for _, d := range m.Spend.Split {
				split[d] = true
			}
		}
		if !split["tool"] {
			s.Spend.Tools = []shareSpendGroup{}
		}
		if !split["model"] {
			s.Spend.Models = []shareSpendGroup{}
		}
		if !split["project"] {
			s.Spend.Projects = []shareSpendGroup{}
		}
	}
	if s.Repo != nil && (m.Repo == nil || m.Repo.Days == 0) {
		s.Repo.Days = []shareRepoDay{}
	}
	out, err := json.Marshal(f)
	if err != nil {
		return raw
	}
	return out
}
