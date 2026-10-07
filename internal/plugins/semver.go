package plugins

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version arithmetic for two things a manifest says about the panel: the
// version it needs (`"panel": ">=1.26"`) and, for a rung-4 module, the range
// it was tested on (`"tested": "1.26.0 - 1.26.x"`). docs/plugins.md §10.
//
// A local build reports "dev", which satisfies every constraint and is inside
// every range: a developer running the panel from source is not refused the
// plugin they are writing, and the card says "dev" rather than a number.

type semver [3]int

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+|x))?(?:[-+].*)?$`)

// parseVersion reads "v1.24.4", "1.24.4" or "1.24" (patch 0). The wildcard
// "x" is returned as -1 so a range can read it.
func parseVersion(s string) (semver, bool, error) {
	mm := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if mm == nil {
		return semver{}, false, fmt.Errorf("%q is not a version like 1.26 or 1.26.0", s)
	}
	var v semver
	v[0], _ = strconv.Atoi(mm[1])
	v[1], _ = strconv.Atoi(mm[2])
	wild := false
	switch mm[3] {
	case "":
		v[2] = 0
	case "x":
		v[2] = -1
		wild = true
	default:
		v[2], _ = strconv.Atoi(mm[3])
	}
	return v, wild, nil
}

func (a semver) less(b semver) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// IsDev reports whether a panel version string is a local build.
func IsDev(panel string) bool {
	p := strings.TrimSpace(panel)
	return p == "" || p == "dev" || strings.HasPrefix(p, "dev ")
}

// PanelVersion reads the number out of version.Version ("v1.24.4"), for the
// card and the constraint. "dev" for a local build.
func PanelVersion(v string) string {
	if IsDev(v) {
		return "dev"
	}
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, " ("); i >= 0 {
		v = v[:i]
	}
	return strings.TrimPrefix(v, "v")
}

// Constraint is a minimum panel version.
type Constraint struct{ min semver }

// ParseConstraint reads ">=1.26" or ">=1.26.3".
func ParseConstraint(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, ">=")
	if !ok {
		return Constraint{}, errors.New(`a panel constraint is ">=MAJOR.MINOR" or ">=MAJOR.MINOR.PATCH"`)
	}
	v, wild, err := parseVersion(rest)
	if err != nil || wild {
		return Constraint{}, errors.New(`a panel constraint is ">=MAJOR.MINOR" or ">=MAJOR.MINOR.PATCH"`)
	}
	return Constraint{min: v}, nil
}

// Satisfied reports whether a panel version meets the constraint.
func (c Constraint) Satisfied(panel string) bool {
	if IsDev(panel) {
		return true
	}
	v, _, err := parseVersion(PanelVersion(panel))
	if err != nil {
		return true
	}
	return !v.less(c.min)
}

// Range is the panel versions a rung-4 module was tested on.
type Range struct{ lo, hi semver }

// ParseRange reads "1.26.0 - 1.26.x", "1.26.x" or "1.26.0".
func ParseRange(s string) (Range, error) {
	s = strings.TrimSpace(s)
	lo, hi := s, s
	if a, b, ok := strings.Cut(s, " - "); ok {
		lo, hi = a, b
	}
	l, lwild, err := parseVersion(lo)
	if err != nil {
		return Range{}, err
	}
	h, hwild, err := parseVersion(hi)
	if err != nil {
		return Range{}, err
	}
	if lwild {
		l[2] = 0
	}
	if hwild {
		h[2] = 1 << 30
	}
	if h.less(l) {
		return Range{}, fmt.Errorf("%q ends before it starts", s)
	}
	return Range{lo: l, hi: h}, nil
}

// Within reports whether a panel version is inside the range.
func (r Range) Within(panel string) bool {
	if IsDev(panel) {
		return true
	}
	v, _, err := parseVersion(PanelVersion(panel))
	if err != nil {
		return true
	}
	return !v.less(r.lo) && !r.hi.less(v)
}
