// Package cgroup reads and writes the cgroup v2 filesystem directly.
//
// Directly, rather than through systemd, because the knobs that matter here
// are on cgroups systemd has delegated and promised not to touch: a scope the
// panel owns the inside of. Asking systemd to change them would need the bus
// for every write and root for most of them on a system unit, and the answer
// would be a cgroupfs write anyway.
//
// Only v2. A host still on the v1 hierarchy gets no isolation and says so; the
// two are different enough that supporting both would be two implementations
// of a feature whose whole point is to be dependable.
package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Mount is where the unified hierarchy is mounted. A variable so tests can
// point it at a directory of plain files for the parsing half.
var Mount = "/sys/fs/cgroup"

// cgroup2Magic is CGROUP2_SUPER_MAGIC from linux/magic.h.
const cgroup2Magic = 0x63677270

// Unified reports whether Mount is a cgroup v2 filesystem.
func Unified() bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(Mount, &st); err != nil {
		return false
	}
	return uint64(st.Type) == cgroup2Magic
}

// Of returns the cgroup a process is in, as a path relative to the mount
// ("/system.slice/vibepanel.service"). Only the v2 line counts: on a hybrid
// host /proc/<pid>/cgroup also lists v1 hierarchies, and their paths name
// different trees.
func Of(pid int) (string, error) {
	name := "/proc/self/cgroup"
	if pid > 0 {
		name = "/proc/" + strconv.Itoa(pid) + "/cgroup"
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return parseProcCgroup(string(b))
}

func parseProcCgroup(s string) (string, error) {
	for _, line := range strings.Split(s, "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, nil
		}
	}
	return "", errors.New("cgroup: no unified hierarchy entry")
}

// Dir is one cgroup, as an absolute path under Mount.
type Dir string

// At returns the Dir for a path relative to the mount.
func At(rel string) Dir { return Dir(filepath.Join(Mount, rel)) }

// Rel is the path relative to the mount, the form systemd and /proc use.
func (d Dir) Rel() string {
	r, err := filepath.Rel(Mount, string(d))
	if err != nil {
		return string(d)
	}
	return "/" + r
}

// Child is a sub-cgroup of d. It does not create it.
func (d Dir) Child(name string) Dir { return Dir(filepath.Join(string(d), name)) }

// Exists reports whether the cgroup is there.
func (d Dir) Exists() bool {
	st, err := os.Stat(string(d))
	return err == nil && st.IsDir()
}

// Ensure creates the cgroup if it is missing.
func (d Dir) Ensure() error {
	if err := os.Mkdir(string(d), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// Remove deletes an empty cgroup. A populated one refuses with EBUSY, which is
// the kernel making the check this would otherwise have to race.
func (d Dir) Remove() error { return syscall.Rmdir(string(d)) }

// Delegated reports whether this process may reorganise the cgroup: create
// children in it, move processes into them and enable controllers.
//
// Access(2) rather than a trial write. A trial write to cgroup.subtree_control
// changes it, and the one thing this must not do on a cgroup it does not own
// is change it.
func (d Dir) Delegated() bool {
	for _, f := range []string{"cgroup.procs", "cgroup.subtree_control"} {
		if syscall.Access(filepath.Join(string(d), f), 2 /* W_OK */) != nil {
			return false
		}
	}
	return syscall.Access(string(d), 2) == nil
}

// Read returns a control file's contents without the trailing newline.
func (d Dir) Read(file string) (string, error) {
	b, err := os.ReadFile(filepath.Join(string(d), file))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// Write writes one value to a control file.
//
// One write(2) per value, which cgroupfs requires: it parses each write on its
// own, so a buffered writer that splits a value across two calls writes two
// wrong values.
func (d Dir) Write(file, value string) error {
	f, err := os.OpenFile(filepath.Join(string(d), file), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write([]byte(value))
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("cgroup: write %q to %s: %w", value, filepath.Join(string(d), file), werr)
	}
	return cerr
}

// Procs lists the processes directly in this cgroup, not in its children.
func (d Dir) Procs() ([]int, error) {
	s, err := d.Read("cgroup.procs")
	if err != nil {
		return nil, err
	}
	var out []int
	for _, f := range strings.Fields(s) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// Move puts a whole process (every thread) into this cgroup.
//
// A process that has already exited is not an error: every caller moves pids
// it read a moment earlier, and a short-lived child finishing in between is
// the ordinary case on a machine running builds.
func (d Dir) Move(pid int) error {
	err := d.Write("cgroup.procs", strconv.Itoa(pid))
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// Children lists the names of direct sub-cgroups.
func (d Dir) Children() ([]string, error) {
	ents, err := os.ReadDir(string(d))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Controllers is the set a cgroup has available (cgroup.controllers) or has
// enabled for its children (cgroup.subtree_control).
func (d Dir) controllers(file string) map[string]bool {
	s, _ := d.Read(file)
	out := map[string]bool{}
	for _, c := range strings.Fields(s) {
		out[c] = true
	}
	return out
}

// Available is cgroup.controllers.
func (d Dir) Available() map[string]bool { return d.controllers("cgroup.controllers") }

// Enabled is cgroup.subtree_control.
func (d Dir) Enabled() map[string]bool { return d.controllers("cgroup.subtree_control") }

// Enable turns on, for this cgroup's children, whichever of want are
// available.
//
// Only what is available: a user manager commonly delegates cpu, memory and
// pids and not io, and asking for io there fails the whole write -- so a
// request for four controllers would enable none.
//
// EBUSY here means a process is sitting directly in this cgroup, which cgroup
// v2 forbids once controllers are enabled below it ("no internal processes").
// The caller moves it out and asks again; this does not guess where it goes.
func (d Dir) Enable(want ...string) error {
	avail, have := d.Available(), d.Enabled()
	var add []string
	for _, c := range want {
		if avail[c] && !have[c] {
			add = append(add, "+"+c)
		}
	}
	if len(add) == 0 {
		return nil
	}
	return d.Write("cgroup.subtree_control", strings.Join(add, " "))
}

// Uint reads a single-number control file. "max" reads as Unlimited.
func (d Dir) Uint(file string) (uint64, error) {
	s, err := d.Read(file)
	if err != nil {
		return 0, err
	}
	return parseUint(s)
}

// Unlimited is how "max" reads through Uint.
const Unlimited = ^uint64(0)

func parseUint(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "max" {
		return Unlimited, nil
	}
	return strconv.ParseUint(s, 10, 64)
}

// SetUint writes a number, or "max" for Unlimited.
func (d Dir) SetUint(file string, v uint64) error {
	if v == Unlimited {
		return d.Write(file, "max")
	}
	return d.Write(file, strconv.FormatUint(v, 10))
}

// KeyValues reads a flat "key value" file: memory.events, cgroup.events,
// cpu.stat, and the subset of memory.stat anything here uses.
//
// The error is carried rather than swallowed because a failed read and a real
// zero are different facts: memory.stat read as empty made the sessions' held
// memory zero, and the budget built on that zero dropped the pool to its floor
// with every session still in it.
func (d Dir) KeyValues(file string) (map[string]uint64, error) {
	s, err := d.Read(file)
	if err != nil {
		return nil, err
	}
	return parseKeyValues(s), nil
}

func parseKeyValues(s string) map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
			out[k] = n
		}
	}
	return out
}

// Pressure is one PSI reading: the share of the last ten seconds that some, or
// all, of the tasks in scope were stalled on the resource.
type Pressure struct {
	Some10 float64 `json:"some10"`
	Full10 float64 `json:"full10"`
}

// Pressure reads memory.pressure, cpu.pressure or io.pressure.
func (d Dir) Pressure(resource string) (Pressure, bool) {
	s, err := d.Read(resource + ".pressure")
	if err != nil {
		return Pressure{}, false
	}
	return ParsePressure(s), true
}

// ParsePressure parses the PSI format, shared by cgroupfs and /proc/pressure.
func ParsePressure(s string) Pressure {
	var p Pressure
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var avg10 float64
		for _, f := range fields[1:] {
			if k, v, _ := strings.Cut(f, "="); k == "avg10" {
				avg10, _ = strconv.ParseFloat(v, 64)
			}
		}
		switch fields[0] {
		case "some":
			p.Some10 = avg10
		case "full":
			p.Full10 = avg10
		}
	}
	return p
}

// Frozen reports whether cgroup.freeze has taken effect. A cgroup that cannot
// be read is treated as not frozen, the safe direction for a resume decision.
func (d Dir) Frozen() bool {
	st, _ := d.KeyValues("cgroup.events")
	return st["frozen"] == 1
}

// Freeze stops, or resumes, every process in the cgroup.
//
// The kernel's freezer rather than SIGSTOP: a stopped process can be continued
// by anything that sends SIGCONT, a shell's job control included, and a
// frozen cgroup cannot be left half-frozen by a child forked mid-signal.
func (d Dir) Freeze(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return d.Write("cgroup.freeze", v)
}

// Chown hands the delegation files of a cgroup to a user, which is what
// systemd does itself for a unit with Delegate=yes and User=.
//
// The handover on every systemd version, not a fallback: creating a scope
// with User= makes systemd chown the files as part of the job, before the
// moves into it have finished being checked. See ScopeProps.
func (d Dir) Chown(uid, gid int) error {
	for _, f := range []string{"", "cgroup.procs", "cgroup.subtree_control", "cgroup.threads"} {
		if err := os.Chown(filepath.Join(string(d), f), uid, gid); err != nil {
			return err
		}
	}
	return nil
}
