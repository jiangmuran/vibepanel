#!/usr/bin/env python3
"""Mutation testing for what is left after the VNC feature was retracted.

Taking a feature out leaves less to guard, not nothing. Three things have to
keep holding, and each of them fails silently in the direction that matters:

  * migration v17 must actually drop `vnc_targets`. A migration that runs and
    does nothing is indistinguishable from one that was never written, and what
    it is failing to remove is a column of VNC passwords **in the clear**.
  * v14 must stay. Migrations are positional, so deleting the one that created
    the table renumbers every step after it and tells a database sitting at 14
    that it is up to date.
  * `RETIRED_TABS` must keep `'vnc'`. The feature is gone; the string is still
    in people's browsers, and it is the repair path that needs the name.

Plus the two upgrade decisions, which are opposites on purpose: a retired
*environment variable* is reported and the panel starts; a retired *flag* is
refused and it does not.

Each entry removes one guard and asserts a test goes red. A guard whose removal
leaves the suite green is decoration.

Do not edit the sources while this is running: every entry restores the file it
mutated from a copy taken before the run, and a run killed part-way leaves the
file it was holding mutated.
"""
import subprocess
import sys

WEB = ('npx', 'vitest', 'run')
STORE = ('go', 'test', './internal/store/', '-count=1')
CONFIG = ('go', 'test', './internal/config/', '-count=1')

DROP = '''	func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS vnc_targets`); err != nil {
			return fmt.Errorf("drop vnc_targets: %w", err)
		}
		return nil
	},'''

V14 = '''	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS vnc_targets (
			     id         TEXT PRIMARY KEY,
			     name       TEXT NOT NULL DEFAULT '',
			     host       TEXT NOT NULL,
			     port       INTEGER NOT NULL,
			     view_only  INTEGER NOT NULL DEFAULT 0,
			     password   TEXT NOT NULL DEFAULT '',
			     created_at INTEGER NOT NULL
			 )`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},'''

# (id, file, needle, replacement, command, cwd)
MUTATIONS = [
    ('V1 the migration runs and drops nothing',
     'internal/store/store.go',
     DROP,
     '''	func(tx *sql.Tx) error {
		return nil
	},''',
     STORE, '.'),

    ('V2 the migration reaches for a name that is not there',
     'internal/store/store.go',
     'DROP TABLE IF EXISTS vnc_targets',
     'DROP TABLE IF EXISTS vnc_target',
     STORE, '.'),

    ('V3 the rows go but the table and its password column stay',
     'internal/store/store.go',
     'DROP TABLE IF EXISTS vnc_targets',
     'DELETE FROM vnc_targets',
     STORE, '.'),

    # The tempting tidy-up: the table is gone, so take out the step that made
    # it. That is not a deletion, it is a renumbering -- every migration after
    # v14 moves down one and schemaVersion drops with them, so a database that
    # stopped at 14 is told it is current while missing three steps.
    ('V4 v14 is deleted rather than superseded',
     'internal/store/store.go',
     V14,
     '',
     STORE, '.'),

    ('V5 a retired environment variable is swallowed instead of reported',
     'internal/config/config.go',
     '\t\tif key == "VIBEPANEL_SESSION_ID" || key == "VIBEPANEL_TOKEN" ||',
     '\t\tif key == "VIBEPANEL_VNC_ALLOW" ||\n'
     '\t\t\tkey == "VIBEPANEL_SESSION_ID" || key == "VIBEPANEL_TOKEN" ||',
     CONFIG, '.'),

    ('V6 --vnc is accepted and ignored for a version',
     'internal/config/flags.go',
     '\tif err := fs.Parse(args); err != nil {',
     '\tvar ignoredVNC bool\n'
     '\tfs.BoolVar(&ignoredVNC, "vnc", false, "accepted and ignored")\n'
     '\tfs.String("vnc-allow", "", "accepted and ignored")\n'
     '\tif err := fs.Parse(args); err != nil {',
     CONFIG, '.'),

    ('V7 the retired tab list forgets the feature that was taken out',
     'web/src/components/chrome.ts',
     "export const RETIRED_TABS = ['git', 'todos', 'vnc', 'monitor', 'tokens'] as const",
     "export const RETIRED_TABS = ['git', 'todos', 'monitor', 'tokens'] as const",
     WEB, 'web'),
]


def run(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True).returncode


def main():
    only = sys.argv[1:]
    survivors = []
    ran = 0
    for name, path, needle, repl, cmd, cwd in MUTATIONS:
        if only and name.split(' ', 1)[0] not in only:
            continue
        ran += 1
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
    print(f'\n{ran} run, {len(survivors)} survived')
    for s in survivors:
        print('  survivor:', s)
    return 1 if survivors else 0


if __name__ == '__main__':
    sys.exit(main())
