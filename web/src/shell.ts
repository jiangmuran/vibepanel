/**
 * Quote a path so it can be typed at a shell prompt.
 *
 * A plain name goes through untouched, a name needing quotes gets single
 * quotes, and a name containing a control character gets ANSI-C quoting --
 * $'...' -- where every control character becomes an escape and no control
 * byte reaches the PTY at all.
 *
 * The third case is what this file exists for, and the reachable version of it
 * is narrower than it looks. Quoting cannot help there: a control character
 * inside single quotes is still a control character when readline sees it.
 *
 * What can actually arrive here was measured rather than assumed, because the
 * finding that prompted this said "a newline" and a newline cannot get here:
 *
 *   - LF, CR and the double quote never leave the browser. The HTML spec has
 *     the multipart filename percent-encode them, so a file dropped as
 *     "two\nlines.txt" lands on disk as "two%0Alines.txt". Watched happening:
 *     an earlier version of the browser check found the encoded name at the
 *     prompt.
 *   - Every other control character is refused by Go's MIME header parser,
 *     400 "malformed MIME header line", for 0x15 and for ESC alike.
 *   - Tab is the exception, because textproto treats it as ordinary header
 *     whitespace. It arrives, it lands on disk with a raw 0x09 in the name, and
 *     at a prompt readline reads it as "complete this". Measured: the second
 *     half of the filename then never appears at the prompt at all.
 *
 * So one character gets through today, and the guard is written for the class
 * rather than for that character. The set that can arrive is decided by two
 * parsers this project does not own, and a name reaching the *screen* already
 * goes through safeText for the same reason -- it is whatever an agent or a
 * download wrote to disk. This was the one place those bytes reached a shell.
 *
 * The caveat, stated rather than hidden. $'...' is bash and zsh; fish and dash
 * do not expand it. There a tab-named file arrives literally wrong and the user
 * gets a "no such file" they can read and edit, which is a worse path than
 * bash's and a much better failure than a prompt that silently lost half the
 * name. The alternative was sending it as a bracketed paste, where control
 * characters are content; not taken, because the server brackets a paste only
 * if the pane's application asked for bracketed paste -- it fixes the case
 * where the shell already copes and not the case where it does not.
 */
export function shellQuote(path: string): string {
  // Nothing a shell would treat specially.
  if (!/[^\w@%+=:,./-]/.test(path)) return path

  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001F\u007F]/.test(path)) {
    const escaped = path
      // Backslash first, or the escapes introduced below are escaped again.
      .replace(/\\/g, '\\\\')
      .replace(/'/g, "\\'")
      // eslint-disable-next-line no-control-regex
      .replace(/[\u0000-\u001F\u007F]/g, (c) =>
        '\\x' + c.charCodeAt(0).toString(16).padStart(2, '0'),
      )
    return "$'" + escaped + "'"
  }

  return "'" + path.replace(/'/g, "'\\''") + "'"
}

/**
 * Read a typed command line as an argv.
 *
 * Whitespace separates words; single and double quotes hold a word together;
 * a backslash escapes the next character. That is the whole rule, and the
 * things it deliberately does not do are the point:
 *
 *   - no `$VAR`, no `~`, no `*`. The argv is handed to tmux as separate words
 *     and exec'd directly, not run through a shell -- measured against tmux
 *     3.6, where `new-session ... /bin/echo 'a; touch x'` printed the semicolon
 *     and created nothing. So expanding anything here would be this function
 *     inventing a shell that is not there, and every disagreement between the
 *     invention and a real one is a command that does something else.
 *   - no operators. `&&`, `|` and `>` are ordinary characters in a word,
 *     because there is nothing downstream that could act on them.
 *
 * The inverse of shellQuote closely enough to round-trip what people type, and
 * joinArgv is what puts a stored argv back in the field.
 */
export function splitArgv(line: string): string[] {
  const out: string[] = []
  let word = ''
  let started = false
  let quote: '"' | "'" | null = null

  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (c === '\\' && i + 1 < line.length && quote !== "'") {
      word += line[++i]
      started = true
      continue
    }
    if (quote) {
      if (c === quote) quote = null
      else word += c
      continue
    }
    if (c === '"' || c === "'") {
      quote = c
      // An empty pair of quotes is a real, empty argument, and losing it turns
      // `foo "" bar` into a two-word command.
      started = true
      continue
    }
    if (/\s/.test(c)) {
      if (started) out.push(word)
      word = ''
      started = false
      continue
    }
    word += c
    started = true
  }
  if (started) out.push(word)
  return out
}

/**
 * Render an argv the way it would be typed.
 *
 * The empty word is spelled out rather than left to shellQuote, which returns
 * it unchanged -- correct for a path, which is never empty, and a word that
 * vanishes here. `['a', '', 'b']` would come back as a two-word command, so an
 * edit that only opened the field and saved it would change what runs.
 */
export function joinArgv(argv: string[]): string {
  return argv.map((a) => (a === '' ? "''" : shellQuote(a))).join(' ')
}
