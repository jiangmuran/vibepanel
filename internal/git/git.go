// Package git reads a working tree, and only reads it.
//
// Two properties are the whole point of this package, and both are enforced
// here rather than left to the callers:
//
//   - **It never writes.** Nothing in a panel should commit, stage, merge or
//     rebase. The agents do that; a panel that also did it would be a second
//     writer racing the first one over the same index.
//   - **It never uses the network.** The product promise is that the panel does
//     not phone home, and the git CLI is the easiest way in the whole
//     repository to break that by accident: fetch, pull, ls-remote and
//     submodule update all look like reads. So the subcommand is checked
//     against an allowlist (see allowed) instead of being trusted because the
//     caller looked careful.
//
// Every argument is an argument. Nothing here builds a command line out of a
// string, because a branch name is attacker-controlled the moment somebody
// clones a repository and "--upload-pack=..." is a valid branch name as far as
// a shell is concerned.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNotARepo means the directory is not inside a working tree. It is an
// ordinary answer, not a failure: most project directories are repositories and
// some are not, and the panel says so rather than showing an error.
var ErrNotARepo = errors.New("git: not a working tree")

// ErrNotAllowed means a caller asked for a subcommand this package refuses.
//
// It exists to be returned to a programmer, never to a user: reaching it means
// somebody added a call site that would have made the panel talk to a network
// or write to somebody's repository.
var ErrNotAllowed = errors.New("git: subcommand not allowed here")

// allowed is every subcommand this package may run.
//
// Deny by default, and the list is short because the panel's whole job here is
// answering four questions: what branch, what is uncommitted, what was
// committed recently, and where does origin point. Nothing else has been
// needed and nothing else may be added without arguing with this comment.
//
// The two that will be argued for are fetch (to make ahead/behind current) and
// ls-remote (to see the remote's branches). Both are the network, both need
// credentials, and both would run on whatever schedule the panel polls on --
// which is exactly the thing the product says it does not do. The GitHub half
// of this feature is a separate package with a button in front of it.
var allowed = map[string]bool{
	"status": true,
	"log":    true,
	"remote": true,
}

// runTimeout bounds one invocation.
//
// A repository on a network filesystem that has gone away makes status hang
// rather than fail, and this endpoint is reached by clicking a tab. Five
// seconds is long enough for status on a very large tree with a cold cache and
// short enough that a wedged mount is a message rather than a spinner.
const runTimeout = 5 * time.Second

// maxOutput bounds what one invocation may return.
//
// status in a directory where somebody untarred a dependency tree lists every
// untracked file, and that is megabytes before anybody has done anything
// unusual. The bound is on bytes held rather than on entries parsed, because
// the cost being avoided is holding it, not counting it.
const maxOutput = 4 << 20

// run executes one subcommand in dir and returns its stdout.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) == 0 || !allowed[args[0]] {
		sub := "(none)"
		if len(args) > 0 {
			sub = args[0]
		}
		return nil, fmt.Errorf("%w: %s", ErrNotAllowed, sub)
	}
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	// -c before the subcommand, so it applies to this invocation only and
	// leaves the repository's own configuration alone.
	//
	// core.fsmonitor is the one worth naming: it is a *program path* read out
	// of the repository's own config, and status runs it. The panel runs as the
	// same user as the agents and in a directory they already have a shell in,
	// so this is not a boundary that can be created here -- but the panel runs
	// this the moment somebody opens a tab, which is a lower bar than "somebody
	// typed a command", and turning it off costs nothing.
	full := append([]string{"-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	b := out.Bytes()
	if len(b) > maxOutput {
		b = b[:maxOutput]
	}
	return b, nil
}

// setVars are the environment variables run() gives every invocation.
//
// They are also removed from the inherited environment before being appended:
// exec passes the slice through untouched and which duplicate wins is the
// child libc's business, not ours.
var setVars = []string{
	// A repository whose remote needs credentials makes git ask, and a prompt
	// with no terminal behind it is a request goroutine that never returns.
	// This package never touches a remote, so this only ever fires when
	// something else has already gone wrong -- which is when it matters.
	"GIT_TERMINAL_PROMPT=0",
	// The one with a visible cost if it is missing. status refreshes the index
	// and takes .git/index.lock to do it, and the agent in that directory is
	// running `git add` and `git commit` at the same time. A panel that polls
	// status would lose the agent a commit with "Unable to create index.lock:
	// File exists". Optional locks off means status reports from what it can
	// read without taking the lock.
	"GIT_OPTIONAL_LOCKS=0",
	"GIT_PAGER=cat",
	"PAGER=cat",
}

// unsetVars have to be *absent*, not empty, which is a different operation.
//
// Getting that wrong cost the whole feature once. An empty value is not "unset"
// to git: `GIT_DIR=` names a git directory whose path is the empty string, and
// every invocation under it dies with "fatal: not a git repository" naming the
// empty string. That message is the one notARepo() matches, so every tree here
// reported itself as "not a repository" -- the ordinary answer for a directory
// that is not one -- and nothing anywhere logged an error.
//
// The three GIT_* ones would otherwise point every invocation at whatever
// repository the panel was started from rather than at cmd.Dir; nothing sets
// them today, a systemd unit written by hand could. The two askpass programs
// are belt to GIT_TERMINAL_PROMPT's braces.
var unsetVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_ASKPASS",
	"SSH_ASKPASS",
}

func env() []string {
	drop := make(map[string]bool, len(setVars)+len(unsetVars))
	for _, kv := range setVars {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	for _, k := range unsetVars {
		drop[k] = true
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(setVars))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, setVars...)
}

// Change is one path git has something to say about.
type Change struct {
	Path string `json:"path"`
	// Kind is what a reader needs to know about it, not the two-letter code:
	// staged, unstaged, untracked or conflict. The codes are precise and
	// unreadable, and this panel is looked at rather than studied.
	Kind string `json:"kind"`
	// Renamed carries where the file came from, empty when it did not move.
	Renamed string `json:"renamed"`
}

// The four kinds a Change can be.
const (
	KindStaged    = "staged"
	KindUnstaged  = "unstaged"
	KindUntracked = "untracked"
	KindConflict  = "conflict"
)

// Status is one working tree, as it is right now.
type Status struct {
	Repo     bool   `json:"repo"`
	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	Head     string `json:"head"`
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`

	Staged     int `json:"staged"`
	Unstaged   int `json:"unstaged"`
	Untracked  int `json:"untracked"`
	Conflicted int `json:"conflicted"`

	Changes []Change `json:"changes"`
	// ChangesTruncated says the list above is a prefix. A file list that
	// silently stops is the same defect as a directory listing that silently
	// stops, which this panel already refuses to do.
	ChangesTruncated bool `json:"changesTruncated"`
}

// Dirty is whether there is anything uncommitted at all.
func (s Status) Dirty() bool {
	return s.Staged+s.Unstaged+s.Untracked+s.Conflicted > 0
}

// maxChanges bounds the file list a status carries.
//
// The counts above are always exact -- they are counted before this bites --
// so a tree with nine thousand untracked files reports nine thousand and shows
// the first hundred. The number is the useful part; the list is context.
const maxChanges = 100

// ReadStatus answers "what is this working tree doing" in one invocation.
//
// --porcelain=v2 --branch -z rather than the v1 format or rev-parse plus
// status: v2 carries the branch, the upstream and the ahead/behind counts in
// the same output as the file list, so the answer is one process rather than
// four, and -z is what makes a path with a newline in it parseable at all.
func ReadStatus(ctx context.Context, dir string) (Status, error) {
	out, err := run(ctx, dir, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal")
	if err != nil {
		if notARepo(err) {
			return Status{}, ErrNotARepo
		}
		return Status{}, err
	}
	st := parseStatusV2(out)
	st.Repo = true
	return st, nil
}

// notARepo recognises git's own way of saying it.
//
// Matched on the message because git exits 128 for this and for a dozen other
// things, and the caller needs to tell "there is no repository here" -- an
// ordinary answer for a project directory -- from "this repository is broken".
func notARepo(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not a git repository") ||
		strings.Contains(msg, "detected dubious ownership")
}

// parseStatusV2 reads the format git-status(1) documents, from NUL-separated
// records.
//
// Split out from ReadStatus so it can be tested against every record shape
// without arranging a repository that produces one. Rename entries are the
// reason: with -z the original path is its *own* record rather than a
// tab-separated suffix, so a parser that treats every record as one entry
// reports the old path of a renamed file as an untracked file. Nothing errors;
// there is simply a wrong line on screen.
func parseStatusV2(data []byte) Status {
	var st Status
	st.Changes = []Change{}
	recs := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		switch {
		case strings.HasPrefix(rec, "# branch.oid "):
			oid := strings.TrimPrefix(rec, "# branch.oid ")
			if oid != "(initial)" && len(oid) >= 7 {
				st.Head = oid[:7]
			}
		case strings.HasPrefix(rec, "# branch.head "):
			head := strings.TrimPrefix(rec, "# branch.head ")
			if head == "(detached)" {
				st.Detached = true
			} else {
				st.Branch = head
			}
		case strings.HasPrefix(rec, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(rec, "# branch.upstream ")
		case strings.HasPrefix(rec, "# branch.ab "):
			st.Ahead, st.Behind = parseAheadBehind(strings.TrimPrefix(rec, "# branch.ab "))
		case strings.HasPrefix(rec, "1 "), strings.HasPrefix(rec, "2 "):
			renamed := ""
			if rec[0] == '2' && i+1 < len(recs) {
				// The original path is the next record. Consumed here so it is
				// not read again as an entry of its own.
				i++
				renamed = recs[i]
			}
			xy, path := ordinaryEntry(rec)
			if len(xy) != 2 || path == "" {
				continue
			}
			// X is the index against HEAD, Y is the worktree against the
			// index, and a file can be both. Counted as both on purpose: "3
			// staged, 2 unstaged" is two facts about one tree, and a file half
			// added is exactly the case worth seeing.
			if xy[0] != '.' {
				st.Staged++
				st.Changes = append(st.Changes, Change{Path: path, Kind: KindStaged, Renamed: renamed})
			}
			if xy[1] != '.' {
				st.Unstaged++
				st.Changes = append(st.Changes, Change{Path: path, Kind: KindUnstaged, Renamed: renamed})
			}
		case strings.HasPrefix(rec, "u "):
			if _, path := unmergedEntry(rec); path != "" {
				st.Conflicted++
				st.Changes = append(st.Changes, Change{Path: path, Kind: KindConflict})
			}
		case strings.HasPrefix(rec, "? "):
			st.Untracked++
			st.Changes = append(st.Changes, Change{Path: rec[2:], Kind: KindUntracked})
		}
		// "! " is an ignored file. --untracked-files=normal without --ignored
		// never emits one, and if it ever did it is not news.
	}
	if len(st.Changes) > maxChanges {
		st.Changes = st.Changes[:maxChanges]
		st.ChangesTruncated = true
	}
	return st
}

// ordinaryEntry pulls the XY code and the path out of a "1" or "2" record.
//
// Field counts differ between the two (a rename carries a similarity score),
// and the path is the last field and may contain spaces -- so it is taken by
// counting fields from the front rather than by splitting the whole record.
func ordinaryEntry(rec string) (string, string) {
	// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
	// 2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>
	fields := 8
	if rec[0] == '2' {
		fields = 9
	}
	rest := rec
	for n := 0; n < fields; n++ {
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			return "", ""
		}
		rest = rest[i+1:]
	}
	parts := strings.SplitN(rec, " ", 3)
	if len(parts) < 3 {
		return "", ""
	}
	return parts[1], rest
}

// unmergedEntry pulls the XY code and the path out of a "u" record.
func unmergedEntry(rec string) (string, string) {
	// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
	rest := rec
	for n := 0; n < 10; n++ {
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			return "", ""
		}
		rest = rest[i+1:]
	}
	parts := strings.SplitN(rec, " ", 3)
	if len(parts) < 3 {
		return "", ""
	}
	return parts[1], rest
}

// parseAheadBehind reads "+3 -1".
func parseAheadBehind(s string) (int, int) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, 0
	}
	ahead, err1 := strconv.Atoi(strings.TrimPrefix(parts[0], "+"))
	behind, err2 := strconv.Atoi(strings.TrimPrefix(parts[1], "-"))
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	if ahead < 0 {
		ahead = -ahead
	}
	if behind < 0 {
		behind = -behind
	}
	return ahead, behind
}

// Commit is one entry in the log.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	When    int64  `json:"when"`
}

// maxCommits bounds the log however many are asked for.
//
// The question this panel answers is "what has been happening in here while I
// was watching four agents", and that is the last screenful. Anyone who wants
// the history has the log in a session, one keystroke away.
const maxCommits = 50

// ReadLog returns the most recent commits, newest first.
func ReadLog(ctx context.Context, dir string, n int) ([]Commit, error) {
	if n <= 0 {
		n = 15
	}
	if n > maxCommits {
		n = maxCommits
	}
	// %x00 as the field separator and -z between records. A commit subject can
	// contain anything a person can type, tabs and newlines included, so every
	// printable separator is a parsing bug waiting for the first commit message
	// somebody pasted a diff into.
	out, err := run(ctx, dir, "log", "-z",
		"--max-count="+strconv.Itoa(n),
		"--no-color", "--no-show-signature",
		"--format=%H%x00%an%x00%at%x00%s")
	if err != nil {
		if notARepo(err) {
			return nil, ErrNotARepo
		}
		// A repository with no commits yet is not a failure to report; it is an
		// empty log, which is what a new project looks like.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "does not have any commits") ||
			strings.Contains(msg, "unknown revision") {
			return []Commit{}, nil
		}
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	commits := []Commit{}
	for i := 0; i+3 < len(fields); i += 4 {
		when, _ := strconv.ParseInt(fields[i+2], 10, 64)
		commits = append(commits, Commit{
			SHA:     fields[i],
			Author:  fields[i+1],
			When:    when,
			Subject: fields[i+3],
		})
	}
	return commits, nil
}

// Remote is where origin points, once it has been recognised.
type Remote struct {
	// URL is the raw remote, kept because a host this panel does not know how
	// to talk to is still worth naming on screen.
	URL   string `json:"url"`
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// GitHub reports whether this remote is one the network half can query.
//
// github.com only, and not because other hosts are unwelcome: a GitHub
// Enterprise install answers on its own API host, and guessing that from a git
// remote is how a panel that promises not to phone home ends up resolving a
// hostname somebody's DNS points anywhere.
func (r Remote) GitHub() bool {
	return r.Host == "github.com" && r.Owner != "" && r.Name != ""
}

// ReadRemote reads the URL of origin.
//
// `remote get-url` rather than `config --get remote.origin.url`: config is not
// on the allowlist and should not be, because git config with one more argument
// is a write.
func ReadRemote(ctx context.Context, dir string) (Remote, bool, error) {
	out, err := run(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		if notARepo(err) {
			return Remote{}, false, ErrNotARepo
		}
		// No remote called origin is the normal state of a local-only
		// repository, not something to report.
		return Remote{}, false, nil
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return Remote{}, false, nil
	}
	return ParseRemote(url), true, nil
}

// ParseRemote pulls host, owner and repository out of a remote URL.
//
// Every shape git accepts for the same repository:
//
//	git@github.com:owner/repo.git
//	ssh://git@github.com/owner/repo.git
//	https://github.com/owner/repo.git
//	https://user:token@github.com/owner/repo
//	git://github.com/owner/repo.git
//
// Written by hand rather than through net/url, which parses the first of those
// as a path with no host at all -- scp-like syntax is not a URL and never was.
//
// Owner and name are validated against what GitHub allows rather than accepted
// as whatever was between the slashes. They end up in a request to another
// machine, and "the string came out of a file in the working tree" is exactly
// the provenance that deserves a character class.
func ParseRemote(raw string) Remote {
	r := Remote{URL: raw}
	rest := strings.TrimSpace(raw)

	switch {
	case strings.Contains(rest, "://"):
		_, after, _ := strings.Cut(rest, "://")
		host, path, ok := strings.Cut(after, "/")
		if !ok {
			return r
		}
		if _, h, found := strings.Cut(host, "@"); found {
			host = h
		}
		// A port is not part of the host for this purpose, and a remote on a
		// non-default port is not github.com anyway.
		if h, _, found := strings.Cut(host, ":"); found {
			host = h
		}
		r.Host = strings.ToLower(host)
		r.Owner, r.Name = ownerAndName(path)
	case strings.Contains(rest, ":"):
		// scp-like. The colon separates host from path, and anything before an
		// "@" is a user name.
		host, path, _ := strings.Cut(rest, ":")
		if _, h, found := strings.Cut(host, "@"); found {
			host = h
		}
		r.Host = strings.ToLower(host)
		r.Owner, r.Name = ownerAndName(path)
	}
	return r
}

func ownerAndName(path string) (string, string) {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(name, "/") {
		return "", ""
	}
	if !validSegment(owner) || !validSegment(name) {
		return "", ""
	}
	return owner, name
}

// validSegment is the character class GitHub allows in an owner or repository
// name. Deliberately not a general "no slashes" check: this string is put into
// a request to a third party.
func validSegment(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}
