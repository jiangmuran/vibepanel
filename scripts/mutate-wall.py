#!/usr/bin/env python3
"""Mutation testing for the session-event log, the repository figures and the
board editor.

Each entry removes one guard and asserts that a test goes red. A guard whose
removal leaves the suite green is decoration, and the run prints it as a
survivor.

The three things this change had to be sure about, and every entry below is one
of them:

  * a disclosure decision -- what the repository half may say about somebody's
    working tree, and what it may not;
  * the event log not being able to stall a state update, which is the one
    thing the whole panel is built on;
  * the git cache not being able to fork a process per poll, on a wall that
    asks every two seconds and never stops.
"""
import subprocess
import sys

GO_WORK = ('go', 'test', './internal/httpapi/', '-count=1', '-run',
           'TestRecordingAStateChange|TestOnlyOnePlaceInThisPackage|'
           'TestTheEventLogIsSwept|TestTheLogRefuses|TestABoardOnlyPulls|'
           'TestTheFirstPollDoesNotWait|TestTheRepositorySectionCarries|'
           'TestAWallReachesGitHub|TestTheDayIsDrawn|TestAScopedLinkSeesOnly|'
           'TestWhatAViewer|TestTheDashboardNeverCarries|TestTypeScriptRows|'
           'TestABoardCanOnlyNarrow|TestTheAPIDocCovers|TestEveryBoardWord|'
           'TestThePollerDrainsTheEventLog|TestAStateThatDidNotChange|'
           'TestPullRequestsThatCouldNotBeFetched|TestABurstOfTransitions|'
           'TestAShareTokenReachesTheDashboardAndNothingElse')
GO_STORE = ('go', 'test', './internal/store/', '-count=1')
GO_GIT = ('go', 'test', './internal/git/', '-count=1')
WEB = ('npx', 'vitest', 'run')

# (id, file, needle, replacement, command, cwd)
MUTATIONS = [
    # ── the event log cannot stall a state update ─────────────────────────
    ('M1 the queue is written to with a blocking send',
     'internal/httpapi/events.go',
     '\tselect {\n\tcase ch <- ev:\n\tdefault:',
     '\tif true {\n\t\tch <- ev\n\t} else {',
     GO_WORK, '.'),
    ('M2 a dropped event is not counted',
     'internal/httpapi/events.go',
     '\t\ts.eventsDropped.Add(1)',
     '',
     GO_WORK, '.'),
    ('M3 the queue is unbounded, so the bound cannot bite',
     'internal/httpapi/events.go',
     'const eventQueue = 256',
     'const eventQueue = 1 << 20',
     GO_WORK, '.'),
    ('M3b a burst is written one row at a time',
     'internal/httpapi/events.go',
     '\t\t\tif err := s.DB.RecordSessionEvents(ctx, gather(ctx, ch, ev)); err != nil &&',
     '\t\t\tif err := s.DB.RecordSessionEvents(ctx, []store.SessionEvent{}); err != nil &&',
     GO_WORK, '.'),
    ('M3c gathering drops everything but the first of a burst',
     'internal/httpapi/events.go',
     '\t\tcase ev := <-ch:\n\t\t\tout = append(out, ev)',
     '\t\tcase <-ch:',
     GO_WORK, '.'),
    ('M4 the drain is never started',
     'internal/httpapi/api.go',
     '\tgo s.drainEvents(ctx)',
     '',
     GO_WORK, '.'),

    # ── every state change is recorded ────────────────────────────────────
    ('M5 the hook path writes the state without recording it',
     'internal/httpapi/api.go',
     '\tif err := s.setSessionState(ctx, prev, st, session.SourceHook); err != nil {',
     '\tif err := s.DB.SetSessionState(ctx, prev.ID, st, session.SourceHook); err != nil {',
     GO_WORK, '.'),
    ('M6 the manual override writes the state without recording it',
     'internal/httpapi/api.go',
     '\t\tif err := s.setSessionStateByID(ctx, sid, st, session.SourceManual); err != nil {',
     '\t\tif err := s.DB.SetSessionState(ctx, sid, st, session.SourceManual); err != nil {',
     GO_WORK, '.'),
    ('M7 a transition is recorded even when nothing changed',
     'internal/httpapi/events.go',
     '\tif st == prev.State {\n\t\treturn nil\n\t}',
     '',
     GO_WORK, '.'),

    # ── the log is bounded ────────────────────────────────────────────────
    ('M8 the sweep keeps everything',
     'internal/httpapi/events.go',
     '\tbefore := time.Now().AddDate(0, 0, -keep).Unix()',
     '\tbefore := int64(0)',
     GO_WORK, '.'),
    ('M9 the log accepts a state that is not in the enum',
     'internal/store/events.go',
     '\tif !ev.To.Valid() || ev.SessionID == "" {',
     '\tif false {',
     GO_WORK, '.'),
    ('M10 a negative dwell is stored as it arrived',
     'internal/store/events.go',
     '\t\tev.ForSeconds = 0',
     '',
     GO_STORE, '.'),
    ('M11 the series leaves out the buckets nothing happened in',
     'internal/store/events.go',
     '\tout := make([]EventBucket, n)\n\tfor i := range out {\n\t\tout[i].At = since + int64(i*bucket)\n\t}',
     '\tout := make([]EventBucket, n)',
     GO_STORE, '.'),
    ('M12 an empty log is a failure rather than zero',
     'internal/store/events.go',
     '\tvar started, waited, finished, waitSecs, waitEnded *int64',
     '\tvar started, waited, finished, waitSecs, waitEnded int64',
     GO_STORE, '.'),
    ('M13 a scoped link is not narrowed at all',
     'internal/store/events.go',
     '\tcase s.SessionID != "":\n\t\treturn " AND session_id = ?", []any{s.SessionID}\n\tcase s.ProjectID != "":\n\t\treturn " AND project_id = ?", []any{s.ProjectID}',
     '\tcase false:\n\t\treturn "", nil\n\tcase false:\n\t\treturn "", nil',
     GO_WORK, '.'),
    ('M14 a scope whose project is gone becomes an empty filter',
     'internal/httpapi/sharework.go',
     '\tif scope.kind != store.ShareWhole &&\n\t\t(scope.missing || (es.ProjectID == "" && es.SessionID == "")) {\n\t\treturn shareFlow{Every: shareFlowBucketSeconds, Buckets: []shareFlowBucket{}}\n\t}',
     '',
     GO_WORK, '.'),

    # ── the repository half: what it may say ──────────────────────────────
    ('M15 the log asks git for the commit subject and the author',
     'internal/git/activity.go',
     '"--no-color", "--no-show-signature", "--shortstat", "--format=%at")',
     '"--no-color", "--no-show-signature", "--shortstat", "--format=%at%x00%s%x00%an")',
     GO_GIT, '.'),
    ('M16 the repository section carries the branch name',
     'internal/httpapi/share.go',
     '\tAhead  int `json:"ahead"`',
     '\tBranch string `json:"branch"`\n\tAhead  int `json:"ahead"`',
     GO_WORK, '.'),
    ('M17 a fetch that has not finished is reported as readable',
     'internal/httpapi/sharework.go',
     '\tsum, ready, age := s.Git.PRs(client, snap.Remote.Owner, snap.Remote.Name, dayStart)\n\tif !ready {\n\t\treturn out\n\t}',
     '\tsum, _, age := s.Git.PRs(client, snap.Remote.Owner, snap.Remote.Name, dayStart)',
     ('go', 'test', './internal/httpapi/', '-count=1', '-run',
      'TestPullRequestsThatCouldNot|TestAWallReachesGitHub'), '.'),
    ('M18 a counts-mode board may reach github.com',
     'internal/httpapi/sharework.go',
     '\tif !named || scope.kind != store.ShareProject || scope.missing || scope.cwd == "" {',
     '\tif scope.kind != store.ShareProject || scope.missing || scope.cwd == "" {',
     GO_WORK, '.'),
    ('M19 an unscoped board may reach github.com',
     'internal/httpapi/sharework.go',
     '\tif !named || scope.kind != store.ShareProject || scope.missing || scope.cwd == "" {',
     '\tif !named || scope.missing {',
     GO_WORK, '.'),
    ('M20 pull requests are fetched whether or not a widget draws them',
     'internal/httpapi/sharework.go',
     '\tif needs[store.NeedRepoPRs] {',
     '\tif true {',
     GO_WORK, '.'),
    ('M21 the repository section is built for every board',
     'internal/httpapi/share.go',
     '\tif needs[store.NeedRepo] || needs[store.NeedRepoDays] || needs[store.NeedRepoPRs] {',
     '\tif true {',
     GO_WORK, '.'),
    ('M22 the flow section is built for every board',
     'internal/httpapi/share.go',
     '\tif needs[store.NeedFlow] {',
     '\tif true {',
     GO_WORK, '.'),
    ('M23 the feed carries real ids rather than the per-link pseudonyms',
     'internal/httpapi/sharework.go',
     '\t\t\tSessionID: shareID(secret, ev.SessionID),\n\t\t\tProjectID: shareID(secret, ev.ProjectID),',
     '\t\t\tSessionID: ev.SessionID,\n\t\t\tProjectID: ev.ProjectID,',
     GO_WORK, '.'),
    ('M24 the feed names sessions whatever the detail mode says',
     'internal/httpapi/sharework.go',
     '\tif named {\n\t\tfor _, row := range sessions {\n\t\t\ttitle[row.ID] = row.Title\n\t\t}\n\t}',
     '\tfor _, row := range sessions {\n\t\ttitle[row.ID] = row.Title\n\t}',
     GO_WORK, '.'),
    ('M25 a project name is carried at every detail mode',
     'internal/httpapi/sharework.go',
     '\t\tif named {\n\t\t\titem.Name = p.Name\n\t\t}',
     '\t\titem.Name = p.Name',
     GO_WORK, '.'),

    # ── the git cache cannot fork a process per poll ──────────────────────
    ('M26 the poll waits for the read instead of taking what is there',
     'internal/git/warm.go',
     '\t\tgo c.refresh(e, fn)',
     '\t\te.refreshing = false\n\t\tif v, ferr := fn(context.Background()); ferr == nil {\n\t\t\te.val, e.at = v, time.Now()\n\t\t}',
     GO_GIT, '.'),
    ('M27 a second caller starts a second read',
     'internal/git/warm.go',
     '\tif stale && !e.refreshing && (e.tried.IsZero() || now.Sub(e.tried) >= ttl) {',
     '\tif stale {',
     GO_GIT, '.'),
    ('M28 a key that always fails is retried on every poll',
     'internal/git/warm.go',
     '\t\te.tried = now',
     '',
     GO_GIT, '.'),
    ('M29 a cold key answers with a zero instead of "not counted yet"',
     'internal/git/warm.go',
     '\tif e.at.IsZero() {\n\t\treturn nil, false\n\t}',
     '',
     GO_GIT, '.'),
    ('M30 a directory that is not a repository is left cold forever',
     'internal/git/warm.go',
     '\t\t\tif errorsNotARepo(err) {\n\t\t\t\treturn RepoSnapshot{}, nil\n\t\t\t}',
     '',
     GO_GIT, '.'),

    # ── reading a working tree ────────────────────────────────────────────
    ('M31 shortstat is parsed by position rather than by clause',
     'internal/git/activity.go',
     '\tfor _, clause := range strings.Split(line, ",") {',
     '\tf := strings.Fields(line)\n\tif len(f) >= 4 {\n\t\tfiles, _ = strconv.Atoi(f[0])\n\t\tinsertions, _ = strconv.Atoi(f[3])\n\t}\n\tif len(f) >= 6 {\n\t\tdeletions, _ = strconv.Atoi(f[5])\n\t}\n\treturn files, insertions, deletions\n}\n\nfunc unusedShortstat(line string) (files, insertions, deletions int) {\n\tfor _, clause := range strings.Split(line, ",") {',
     GO_GIT, '.'),
    ('M32 a commit with no diff keeps the previous commit\'s day',
     'internal/git/activity.go',
     '\t\t\tday = -1\n\t\t\tif i, ok := index[time.Unix(ts, 0).Format("2006-01-02")]; ok {',
     '\t\t\tif i, ok := index[time.Unix(ts, 0).Format("2006-01-02")]; ok {',
     GO_GIT, '.'),
    ('M33 the log is unbounded however many commits there are',
     'internal/git/activity.go',
     '\t\t"--max-count="+strconv.Itoa(maxActivityCommits+1),',
     '',
     GO_GIT, '.'),
    ('M34 a window of any length may be asked for',
     'internal/git/activity.go',
     '\tif days > maxActivityDays {\n\t\tdays = maxActivityDays\n\t}',
     '',
     GO_GIT, '.'),

    # ── the board vocabulary stays closed ─────────────────────────────────
    ('M35 a density out of range is stored rather than refused',
     'internal/store/board.go',
     '\tif density < MinDensity || density > MaxDensity {\n\t\treturn Board{}, fmt.Errorf("density must be between %d and %d", MinDensity, MaxDensity)\n\t}',
     '',
     GO_STORE, '.'),
    ('M36 a density out of range is served rather than clamped',
     'internal/store/board.go',
     '\tif b.Density >= MinDensity && b.Density <= MaxDensity {\n\t\tout.Density = b.Density\n\t}',
     '\tout.Density = b.Density',
     GO_STORE, '.'),
    ('M37 a metric does not pull the section it comes out of',
     'internal/store/board.go',
     '\t\tout = append(out, metricNeeds[w.Metric]...)',
     '',
     GO_WORK, '.'),

    # ── the editor's arithmetic ───────────────────────────────────────────
    ('M38 a move right by one is spliced without adjusting the gap',
     'web/src/components/board/edit.ts',
     '  const landing = to > from ? to - 1 : to',
     '  const landing = to',
     WEB, 'web'),
    ('M39 a drag that goes nowhere still rewrites the board',
     'web/src/components/board/edit.ts',
     '  if (landing === from) return widgets',
     '',
     WEB, 'web'),
    ('M40 a resize may land on a width the editor does not offer',
     'web/src/components/board/edit.ts',
     '  let best = steps[0]\n  for (const s of steps) {\n    if (Math.abs(s - wanted) < Math.abs(best - wanted)) best = s\n  }\n  return best',
     '  return Math.max(1, Math.min(maxSpan, Math.round(wanted)))',
     WEB, 'web'),
    ('M41 a drop is aimed at the nearest tile rather than the row it is in',
     'web/src/components/board/edit.ts',
     '    if (row.length > 0 && rowTop <= y) bestRow = row\n      row = []',
     '    bestRow = row\n      row = []',
     WEB, 'web'),
    ('M42 a height drag is unbounded',
     'web/src/components/board/edit.ts',
     '  return Math.max(1, Math.min(n, Math.max(1, maxRows)))',
     '  return Math.max(1, n)',
     WEB, 'web'),
    ('M43 density is not clamped on the side that renders it',
     'web/src/components/board/density.ts',
     '  if (!Number.isFinite(n) || (n ?? 0) < SPARE) return NORMAL\n  return Math.min(Math.floor(n!), DENSE)',
     '  return n ?? NORMAL',
     WEB, 'web'),
]


def run(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True).returncode


def main():
    survivors = []
    for name, path, needle, repl, cmd, cwd in MUTATIONS:
        original = open(path).read()
        if needle not in original:
            print(f'!! {name}: the code it mutates is not there any more', flush=True)
            survivors.append(name + ' (needle missing)')
            continue
        open(path, 'w').write(original.replace(needle, repl, 1))
        try:
            code = run(cmd, cwd)
        finally:
            open(path, 'w').write(original)
        if code == 0:
            print(f'SURVIVED  {name}', flush=True)
            survivors.append(name)
        else:
            print(f'killed    {name}', flush=True)
    print(f'\n{len(MUTATIONS)} run, {len(survivors)} survived')
    for s in survivors:
        print('  survivor:', s)
    return 1 if survivors else 0


if __name__ == '__main__':
    sys.exit(main())
