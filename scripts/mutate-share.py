#!/usr/bin/env python3
"""Mutation testing for the guards added in this change.

Each entry removes one guard and asserts that a test goes red. A guard whose
removal leaves the suite green is decoration, and the run prints it as a
survivor.
"""
import subprocess
import sys

GO_SHARE = ('go', 'test', './internal/httpapi/', '-count=1', '-run',
            'TestAShareTokenReachesTheDashboardAndNothingElse|TestAnOwnersEdit|'
            'TestTheRemarkIsShown|TestARemarkIs|TestALockedBoard|TestLockingAnd|'
            'TestWhatAViewer|TestTheOwnerSees|TestAViewerIsNot|TestThePreview|'
            'TestTheTrend|TestTheDashboardNeverCarries|TestTypeScriptRows|'
            'TestABoardCanOnlyNarrow|TestEditingALink|TestTheAPIDocCovers|'
            'TestEveryAuditEvent')
GO_STORE = ('go', 'test', './internal/store/', '-count=1')
WEB = ('npx', 'vitest', 'run')

# (id, file, needle, replacement, command, cwd)
MUTATIONS = [
    # ── the share surface is one GET ───────────────────────────────────────
    ('M1 preview mounted under the share token',
     'internal/httpapi/share.go',
     '\t\tr.Get("/dashboard", s.handleShareDashboard)',
     '\t\tr.Get("/dashboard", s.handleShareDashboard)\n\t\tr.Get("/preview", s.handleSharePreview)',
     GO_SHARE, '.'),
    ('M2 the reported viewport decides what is sent',
     'internal/httpapi/share.go',
     '\tout, err := s.buildShareDashboard(r.Context(), sc.link, sc.secret)',
     '\tout, err := s.buildShareDashboard(r.Context(), sc.link, sc.secret)\n\tif r.URL.Query().Get("w") == "1" {\n\t\tout.Sessions = nil\n\t}',
     GO_SHARE, '.'),
    ('M2b the viewer id decides what is sent',
     'internal/httpapi/share.go',
     '\tout, err := s.buildShareDashboard(r.Context(), sc.link, sc.secret)',
     '\tout, err := s.buildShareDashboard(r.Context(), sc.link, sc.secret)\n\tif r.URL.Query().Get("v") == "bb22" {\n\t\tout.Name = ""\n\t}',
     GO_SHARE, '.'),

    # ── the redaction restates rather than embeds ──────────────────────────
    ('M3 the builder always uses names',
     'internal/httpapi/share.go',
     '\tnamed := store.ShareDetail(sc.link.Detail) == store.ShareNames',
     '\tnamed := true',
     GO_SHARE, '.'),
    ('M4 the machine copy grows the sampler\'s disk path',
     'internal/httpapi/share.go',
     '\tUptime int64 `json:"uptime"`\n}',
     '\tUptime int64 `json:"uptime"`\n\n\tDiskPath string `json:"diskPath"`\n}',
     GO_SHARE, '.'),
    ('M5 the trend is sent whether or not a board draws one',
     'internal/httpapi/share.go',
     '\tif needs[store.NeedTrend] {',
     '\tif true {',
     GO_SHARE, '.'),

    # ── the remark's bound ─────────────────────────────────────────────────
    ('M6 TruncateRemark cuts bytes instead of runes',
     'internal/store/share.go',
     'func TruncateRemark(s string) string { return truncateRunes(s, MaxRemark) }',
     'func TruncateRemark(s string) string {\n\tif len(s) <= MaxRemark {\n\t\treturn s\n\t}\n\treturn s[:MaxRemark]\n}',
     GO_SHARE, '.'),
    ('M7 the edit path stores a remark unbounded',
     'internal/store/share.go',
     'name, encoded, TruncateRemark(remark), locked, id)',
     'name, encoded, remark, locked, id)',
     GO_SHARE, '.'),
    ('M8 the create path stores a remark unbounded',
     'internal/store/share.go',
     '\tremark = TruncateRemark(remark)\n',
     '',
     GO_SHARE, '.'),
    ('M9 the dashboard renders the remark without safeText',
     'web/src/components/Dashboard.tsx',
     '{safeText(data.remark)}',
     '{data.remark}',
     WEB, 'web'),

    # ── the lock ───────────────────────────────────────────────────────────
    ('M10 the lock is not checked at all',
     'internal/httpapi/share.go',
     '\tif current.Locked {',
     '\tif false {',
     GO_SHARE, '.'),
    ('M11 unlocking also applies the request it came with',
     'internal/httpapi/share.go',
     'if uerr := s.DB.UpdateShareLink(r.Context(), linkID, current.Name, current.Remark,\n\t\t\tcurrent.Board, false); uerr != nil {',
     'if uerr := s.DB.UpdateShareLink(r.Context(), linkID, req.Name, req.Remark,\n\t\t\tstore.SanitiseBoard(*req.Board), false); uerr != nil {',
     GO_SHARE, '.'),
    ('M12 the lock is read from the request rather than the row',
     'internal/httpapi/share.go',
     '\tcurrent, err := s.DB.ShareLinkByID(r.Context(), linkID)',
     '\tcurrent, err := s.DB.ShareLinkByID(r.Context(), linkID)\n\tcurrent.Locked = req.Locked != nil && *req.Locked',
     GO_SHARE, '.'),

    # ── the viewer count ───────────────────────────────────────────────────
    ('M13 entries are never aged out',
     'internal/httpapi/sharelive.go',
     'func prune(seen map[string]shareViewer, now time.Time) {\n\tfor k, v := range seen {\n\t\tif now.Sub(v.at) > shareViewerTTL {\n\t\t\tdelete(seen, k)\n\t\t}\n\t}\n}',
     'func prune(seen map[string]shareViewer, now time.Time) {}',
     GO_SHARE, '.'),
    ('M14 a revoked link keeps its viewers',
     'internal/httpapi/share.go',
     '\ts.viewers.forget(linkID)',
     '\t_ = linkID',
     GO_SHARE, '.'),
    ('M15 viewers are counted by address rather than by screen',
     'internal/httpapi/sharelive.go',
     '\traw = q.Get("v")\n\tif len(raw) == 0 || len(raw) > shareViewerIDMax || !isHex(raw) {',
     '\traw = q.Get("v")\n\tif true {',
     GO_SHARE, '.'),
    ('M16 the largest screen is not the one reported',
     'internal/httpapi/sharelive.go',
     '\t\tif v.w*v.h > w*h {',
     '\t\tif false {',
     GO_SHARE, '.'),

    # ── the owner's edit reaching the screen ───────────────────────────────
    ('M17 the edit path drops the remark',
     'internal/httpapi/share.go',
     '\tif err := s.DB.UpdateShareLink(r.Context(), linkID, name, req.Remark, board,',
     '\tif err := s.DB.UpdateShareLink(r.Context(), linkID, name, "", board,',
     GO_SHARE, '.'),
    ('M18 a board arrives without the fill it was drawn with',
     'internal/store/board.go',
     '\tout := Board{Grid: GridColumns, Preset: b.Preset, Rotate: b.Rotate, Fill: b.Fill,',
     '\tout := Board{Grid: GridColumns, Preset: b.Preset, Rotate: b.Rotate,',
     GO_SHARE, '.'),

    # ── the grid ───────────────────────────────────────────────────────────
    ('M19 the read path does not convert an old board',
     'internal/store/board.go',
     'func SanitiseBoard(b Board) Board {\n\tb = normaliseGrid(b)',
     'func SanitiseBoard(b Board) Board {',
     GO_STORE, '.'),
    ('M20 the write path does not convert an old board',
     'internal/store/board.go',
     'func ValidateBoard(b Board) (Board, error) {\n\tb = normaliseGrid(b)',
     'func ValidateBoard(b Board) (Board, error) {',
     GO_STORE, '.'),
    ('M21 the conversion clamps instead of leaving a bad span to be refused',
     'internal/store/board.go',
     '\t\tif out.Widgets[i].Span > 0 && out.Widgets[i].Span <= 4 {\n\t\t\tout.Widgets[i].Span *= scale\n\t\t}',
     '\t\tif out.Widgets[i].Span > 0 {\n\t\t\tout.Widgets[i].Span *= scale\n\t\t\tif out.Widgets[i].Span > GridColumns {\n\t\t\t\tout.Widgets[i].Span = GridColumns\n\t\t\t}\n\t\t}',
     GO_STORE, '.'),
    ('M22 a widget height is unbounded',
     'internal/store/board.go',
     '\tif w.Height < 1 || w.Height > MaxRows {\n\t\treturn Widget{}, fmt.Errorf("height must be between 1 and %d", MaxRows)\n\t}',
     '',
     GO_STORE, '.'),
    ('M23 a preset may use a width the editor cannot offer',
     'internal/store/presets.go',
     '{Kind: "remark", Span: 8},',
     '{Kind: "remark", Span: 7},',
     GO_STORE, '.'),

    # ── the frontend collapse ──────────────────────────────────────────────
    ('M24 a board does not collapse on a narrow screen',
     'web/src/components/board/viewer.ts',
     '  if (width < WHOLE_BELOW) return 12\n  if (width < HALF_BELOW) return Math.max(asked, 6)\n  return asked',
     '  return asked',
     WEB, 'web'),
    ('M25 a tile asks the grid for whatever the row said',
     'web/src/components/board/Tile.tsx',
     '  if (!Number.isFinite(height) || (height ?? 0) <= 1) return 1\n  return Math.min(Math.floor(height!), MAX_ROWS)',
     '  return height ?? 1',
     WEB, 'web'),
]


def run(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True).returncode


def main():
    survivors = []
    for i, (name, path, needle, repl, cmd, cwd) in enumerate(MUTATIONS, 1):
        original = open(path).read()
        if needle not in original:
            print(f'!! {name}: the code it mutates is not there any more')
            survivors.append(name + ' (needle missing)')
            continue
        open(path, 'w').write(original.replace(needle, repl, 1))
        try:
            code = run(cmd, cwd)
        finally:
            open(path, 'w').write(original)
        if code == 0:
            print(f'SURVIVED  {name}')
            survivors.append(name)
        else:
            print(f'killed    {name}')
    print(f'\n{len(MUTATIONS)} run, {len(survivors)} survived')
    for s in survivors:
        print('  survivor:', s)
    return 1 if survivors else 0


if __name__ == '__main__':
    sys.exit(main())
