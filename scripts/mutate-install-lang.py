#!/usr/bin/env python3
"""Mutation testing for the installer's two languages.

Each entry removes one guard and asserts that scripts/install-check.sh goes
red. A guard whose removal leaves the check green is decoration, and the run
prints it as a survivor.

Two survived the first pass and both were fixed by making the check say what it
wanted rather than by adding a guard: the unknown-key marker was covered only
by the empty-side one (they are different failures and must read differently),
and the LC_ALL precedence was exercised with a locale it recognises, where the
two behaviours agree. See docs/build-log.md, "An installer that speaks Chinese".

    scripts/mutate-install-lang.py

Roughly forty seconds a mutation, because every one of them runs the whole
install check -- which is the point: the guard has to be pinned by the file
that is actually run, not by a test written next to it.
"""
import subprocess
import sys

CHECK = ('scripts/install-check.sh',)

DEP = 'deploy/install.sh'
BOOT = 'install.sh'
CHK = 'scripts/install-check.sh'

# (id, file, needle, replacement). A tuple of needles and a tuple of
# replacements means one mutation made of several edits -- M12 needs two,
# because either edit alone still prints --help once in the right language.
MUTATIONS = [
    # ── the table cannot become half a table ───────────────────────────────
    ('M1 a Chinese string goes missing', DEP,
     "      MS_ZH='就这样做吗？' ;;",
     "      MS_ZH='' ;;"),
    ('M2 an empty side prints an empty line', DEP,
     '  if [ -z "$s" ]; then\n'
     "    printf 'vibepanel installer BUG: key %s has no %s string\\n' \"$key\" \"$VP_LANG\" >&2\n"
     "    printf '[missing string: %s/%s]\\n' \"$key\" \"$VP_LANG\"\n"
     '    return 0\n'
     '  fi\n',
     ''),
    ('M3 an unknown key is not reported as unknown', DEP,
     '  if ! mstr "$key"; then\n'
     "    printf 'vibepanel installer BUG: no string for key %s\\n' \"$key\" >&2\n"
     "    printf '[missing string: %s]\\n' \"$key\"\n"
     '    return 0\n'
     '  fi\n',
     '  mstr "$key" || true\n'),
    ('M4 a translation loses a substitution', DEP,
     '  安装     %1$s   (systemd 用户服务)',
     '  安装     一个 unit   (systemd 用户服务)'),

    # ── the question, and when it may not be asked ─────────────────────────
    ('M5 it asks with nobody there', DEP,
     'if [ "$VP_LANG_DECIDED" = no ] && [ "$INTERACTIVE" = yes ] && [ "$ACCT_STDIN" = no ]; then',
     'if [ "$VP_LANG_DECIDED" = no ] && [ "$ACCT_STDIN" = no ]; then'),
    ('M6 it asks over --password-stdin', DEP,
     'if [ "$VP_LANG_DECIDED" = no ] && [ "$INTERACTIVE" = yes ] && [ "$ACCT_STDIN" = no ]; then',
     'if [ "$VP_LANG_DECIDED" = no ] && [ "$INTERACTIVE" = yes ]; then'),
    ('M7 it is no longer the first thing on the screen', DEP,
     '  m lang.ask\n  ask "$(m lang.prompt)" 1',
     "  echo 'vibepanel installer'\n  m lang.ask\n  ask \"$(m lang.prompt)\" 1"),

    # ── the precedence ─────────────────────────────────────────────────────
    ('M8 LC_ALL falls through to LANG', DEP,
     '    x="$(vp_lang_of "$v")"\n'
     '    if [ -n "$x" ]; then VP_LANG="$x"; VP_LANG_DECIDED=yes; fi\n'
     '    return 0\n'
     '  done',
     '    x="$(vp_lang_of "$v")"\n'
     '    if [ -n "$x" ]; then VP_LANG="$x"; VP_LANG_DECIDED=yes; return 0; fi\n'
     '  done'),
    ('M9 --lang is read after the environment', DEP,
     'vp_lang_from_env\nvp_lang_from_args "$@"',
     'vp_lang_from_args "$@"\nvp_lang_from_env'),
    ('M10 --lang takes a language that does not exist', DEP,
     '    --lang) shift; [ $# -gt 0 ] || { me arg.lang; exit 2; }\n'
     '            [ -n "$(vp_lang_of "$1")" ] || { me arg.lang; exit 2; } ;;',
     '    --lang) shift; [ $# -gt 0 ] || { me arg.lang; exit 2; } ;;'),

    # ── the two halves have to agree ───────────────────────────────────────
    ('M11 the bootstrap keeps --lang to itself', BOOT,
     '            VP_LANG="$LANGV"; VP_LANG_DECIDED=yes\n'
     '            FORWARD="$FORWARD --lang=$LANGV"; shift 2 ;;',
     '            VP_LANG="$LANGV"; VP_LANG_DECIDED=yes; shift 2 ;;'),
    # Two edits, because either alone leaves --help printed once in the right
    # language: this is the whole of putting it back where it used to be.
    ('M12 the bootstrap prints --help before reading --lang', BOOT,
     ('    -h|--help) HELP=yes; shift ;;',
      'if [ "$HELP" = yes ]; then usage; exit 0; fi'),
     ('    -h|--help) usage; HELP=yes; shift ;;',
      'if [ "$HELP" = yes ]; then exit 0; fi')),
    ('M13 the bootstrap ignores the locale', BOOT,
     'for _v in "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do\n'
     '  [ -n "$_v" ] || continue\n'
     '  _x="$(vp_lang_of "$_v")"\n'
     '  [ -z "$_x" ] || { VP_LANG="$_x"; VP_LANG_DECIDED=yes; }\n'
     '  break\n'
     'done',
     ''),

    # ── the check's own guards ─────────────────────────────────────────────
    ('M14 the key walk finds nothing and passes', CHK,
     """  keys="$(grep -oE '^    [a-z][a-z0-9._]*\\)' "$blk" | tr -d ' )' | sort -u)\"""",
     """  keys="$(grep -oE '^ZZZZ[a-z]*\\)' "$blk" | tr -d ' )' | sort -u)\""""),
    ('M15 the check stops pinning the locale', CHK,
     '      LC_ALL="$LC_OVERRIDE" LC_MESSAGES="$LCM_OVERRIDE" LANG="$LANG_OVERRIDE" \\\n',
     ''),
]


def main():
    survivors = []
    for name, path, needle, repl in MUTATIONS:
        original = open(path, encoding='utf-8').read()
        edits = list(zip(needle, repl)) if isinstance(needle, tuple) else [(needle, repl)]
        if any(n not in original for n, _ in edits):
            print(f'!! {name}: the code it mutates is not there any more')
            survivors.append(name + ' (needle missing)')
            continue
        mutated = original
        for n, r in edits:
            mutated = mutated.replace(n, r, 1)
        open(path, 'w', encoding='utf-8').write(mutated)
        try:
            # A pinned locale, because one of the mutations is the check
            # forgetting to pin it -- and it can only be seen from outside.
            env = {'LANG': 'en_US.UTF-8', 'PATH': '/usr/bin:/bin:/usr/local/bin',
                   'HOME': subprocess.os.environ.get('HOME', '/tmp')}
            code = subprocess.run(CHECK, capture_output=True, text=True,
                                  env=env).returncode
        finally:
            open(path, 'w', encoding='utf-8').write(original)
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
