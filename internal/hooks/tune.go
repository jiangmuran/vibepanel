package hooks

import (
	"fmt"
	"os"
)

// Settings the panel offers to change in ~/.claude/settings.json, beyond hooks.
//
// This file writes somebody else's configuration, which is why it is here
// beside the hooks installer rather than anywhere else: that code already
// copies the file before touching it, merges rather than replaces, keeps the
// mode the file had, and writes nothing when nothing would change. All of that
// applies unchanged, and none of it is worth having twice.
//
// The rule for what may go in Tweaks: a key verified to exist in the installed
// CLI, with a description taken from the CLI or the published schema rather
// than from what its name suggests. Two of these are absent from
// json.schemastore.org's copy of the settings schema and present in the binary,
// so the schema alone is not the test -- and a plausible-looking key that does
// nothing is worse than no feature, because the summary would report it as
// applied.

// A Tweak is one key, its wanted value, and what it actually does.
type Tweak struct {
	// Path into the JSON document. Nested for attribution.* and env.*.
	Path []string
	Want any
	// What it does, in one line, for the summary the installer prints. Sourced
	// from the CLI's own describe() text or the published schema; see the file
	// comment.
	//
	// Both languages here rather than in deploy/install.sh's string table, and
	// that is the reason this is not a plain string. The table is the right
	// home for a sentence the installer owns; these sentences describe a
	// specific key and belong beside it, because a description that drifts from
	// the key it names is a summary that reports something other than what was
	// written -- and the installer prints this list as the thing somebody says
	// yes to.
	What   string
	WhatZH string
}

// Say picks the language. Anything but "zh" is English, so a missing or
// unexpected value reads rather than disappears.
func (t Tweak) Say(lang string) string {
	if lang == "zh" && t.WhatZH != "" {
		return t.WhatZH
	}
	return t.What
}

// Tweaks is the set, in the order the summary lists them.
func Tweaks() []Tweak {
	return []Tweak{
		{
			Path:   []string{"autoUploadSessions"},
			Want:   false,
			What:   "sessions are not mirrored to claude.ai (the CLI calls this view-only mirroring)",
			WhatZH: "会话不再镜像到 claude.ai（CLI 自己的说法是只读镜像）",
		},
		{
			Path:   []string{"remoteControlAtStartup"},
			Want:   false,
			What:   "Remote Control does not connect by itself when a session starts",
			WhatZH: "会话启动时不自动连接 Remote Control",
		},
		{
			Path:   []string{"attribution", "commit"},
			Want:   "",
			What:   "no attribution trailer on commits (an empty string hides it)",
			WhatZH: "提交信息里不带署名尾行（空字符串即隐藏）",
		},
		{
			Path:   []string{"attribution", "pr"},
			Want:   "",
			What:   "no attribution block in pull request descriptions",
			WhatZH: "PR 描述里不带署名段落",
		},
		{
			Path:   []string{"attribution", "sessionUrl"},
			Want:   false,
			What:   "no Claude-Session link appended to commits or pull requests",
			WhatZH: "提交和 PR 里不再追加 Claude-Session 链接",
		},
		{
			// Superseded by attribution.commit above and set anyway: a machine
			// running an older Claude Code reads this one and does not know
			// the other exists. It costs a line and it is what "no
			// Co-Authored-By" means on a build from before the change.
			Path:   []string{"includeCoAuthoredBy"},
			Want:   false,
			What:   "no Co-Authored-By byline (the older key, for older builds)",
			WhatZH: "不带 Co-Authored-By 署名（旧键，给旧版本用）",
		},
		{
			// Not a cache control, whatever it is reached for. What it
			// suppresses is the `anthropic-billing-header` carrying
			// `cc_version` and `cc_entrypoint`; there is no cache-busting
			// nonce setting in the CLI, and this file will not imply one.
			Path:   []string{"env", "CLAUDE_CODE_ATTRIBUTION_HEADER"},
			Want:   "0",
			What:   "the anthropic-billing-header (cc_version, cc_entrypoint) is not sent",
			WhatZH: "不再发送 anthropic-billing-header（cc_version、cc_entrypoint）",
		},
	}
}

// A TuneRow is one tweak measured against what is on disk.
type TuneRow struct {
	Key string
	// Both languages, carried through from the Tweak so the caller picks.
	What   string
	WhatZH string
	Have   any
	Want   any
	// Same is true when the file already says what we would write. The
	// installer lists these too: "already set" and "changed" are different
	// facts and a summary that shows only the second one reads as though the
	// rest were left alone.
	Same bool
}

// TuneStatus is the whole comparison, plus where the file is.
type TuneStatus struct {
	Path string
	// Exists is false on a fresh machine. Not an error: the file is created.
	Exists bool
	Rows   []TuneRow
	// Changes counts the rows that are not Same.
	Changes int
}

// InspectTune reports what applying would change, without changing anything.
func InspectTune() (TuneStatus, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return TuneStatus{}, err
	}
	st := TuneStatus{Path: path}

	doc, err := readSettings(path)
	if err != nil {
		if !os.IsNotExist(err) {
			// Invalid JSON, and this is the one case worth stopping for: the
			// merge below would drop everything the file holds. The hooks
			// installer takes the same line.
			return st, err
		}
		doc = map[string]any{}
	} else {
		st.Exists = true
	}

	for _, tw := range Tweaks() {
		have, ok := lookup(doc, tw.Path)
		row := TuneRow{
			Key:    keyOf(tw.Path),
			What:   tw.What,
			WhatZH: tw.WhatZH,
			Have:   have,
			Want:   tw.Want,
			Same:   ok && sameJSON(have, tw.Want),
		}
		if !row.Same {
			st.Changes++
		}
		st.Rows = append(st.Rows, row)
	}
	return st, nil
}

// ApplyTune writes the tweaks, copying the file beside itself first.
//
// Returns the comparison as it was *before* the write, because that is what a
// summary is for: a list saying every row already agrees, printed after making
// them agree, tells nobody what happened.
func ApplyTune() (TuneStatus, error) {
	// InspectTune reads the same file this then writes, so the read and the
	// write are one cycle and the lock covers both -- see editMu.
	editMu.Lock()
	defer editMu.Unlock()
	st, err := InspectTune()
	if err != nil {
		return st, err
	}
	if st.Changes == 0 {
		return st, nil
	}

	doc := map[string]any{}
	if st.Exists {
		if doc, err = readSettings(st.Path); err != nil {
			return st, err
		}
		// Only when there is something to copy. A backup of a file that does
		// not exist is a confusing empty file next to a new one.
		if err := backup(st.Path); err != nil {
			return st, err
		}
	}

	for _, tw := range Tweaks() {
		if err := place(doc, tw.Path, tw.Want); err != nil {
			return st, err
		}
	}
	if err := writeSettings(st.Path, doc); err != nil {
		return st, err
	}
	return st, nil
}

// lookup walks a path, reporting whether every step existed.
func lookup(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, step := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[step]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// place sets one path, creating the objects on the way.
//
// A non-object where an object has to go is refused rather than replaced.
// `"env": "something"` is not a shape this understands, and overwriting it
// would throw away whatever the user meant by it.
func place(doc map[string]any, path []string, want any) error {
	cur := doc
	for i, step := range path[:len(path)-1] {
		next, ok := cur[step]
		if !ok || next == nil {
			m := map[string]any{}
			cur[step] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("hooks: %s is not an object, refusing to replace it", keyOf(path[:i+1]))
		}
		cur = m
	}
	cur[path[len(path)-1]] = want
	return nil
}

// sameJSON compares a value read back from JSON against a wanted Go value.
//
// Only bool and string are wanted by anything here, and both survive a JSON
// round trip as themselves, so this is an equality test and not a conversion
// table. A number would arrive as float64 and would need one -- which is the
// reason this is a function with a comment rather than a `==` at the call site.
func sameJSON(have, want any) bool {
	switch w := want.(type) {
	case bool:
		h, ok := have.(bool)
		return ok && h == w
	case string:
		h, ok := have.(string)
		return ok && h == w
	default:
		return false
	}
}

func keyOf(path []string) string {
	out := ""
	for i, s := range path {
		if i > 0 {
			out += "."
		}
		out += s
	}
	return out
}
