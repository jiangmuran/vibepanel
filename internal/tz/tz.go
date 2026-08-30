// Package tz resolves the panel's configured time zone.
//
// One place, because "which day is this" is asked in four: the token buckets
// at ingest, the spend windows at query time, the git activity read, and every
// date the browser prints. Four answers to that question is a panel whose
// "today" disagrees with its own chart.
package tz

import (
	"fmt"
	"time"

	// The zone database, compiled in.
	//
	// CGO_ENABLED=0 already means no system time zone lookup through libc, and
	// `time.LoadLocation` then falls back to reading /usr/share/zoneinfo --
	// which a distroless image, a scratch container and a stripped VM do not
	// have. The whole distribution story is a static binary on a machine you
	// know nothing about, so a setting that works on the developer's laptop
	// and returns "unknown time zone Asia/Shanghai" on the box it was built
	// for is not a setting. ~450 KB, paid once.
	_ "time/tzdata"
)

// Load turns a stored setting into a location.
//
// The empty string is not an error and not UTC: it means the machine's own
// zone, which is the right default because the machine is the user's and they
// have already set it once. UTC as a default would be defensible on a server
// and is wrong here -- at UTC+8 it cuts the working evening in half, and the
// question being asked is "how much did I spend today".
func Load(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	// "Local" and "UTC" are accepted by LoadLocation and mean what they say.
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local, fmt.Errorf("tz: %w", err)
	}
	return loc, nil
}

// Valid reports whether a name can be stored.
//
// Separate from Load so a handler can refuse a bad value with a 400 rather
// than store it and quietly fall back -- a setting that silently does
// something other than what the page shows is the failure this avoids.
func Valid(name string) bool {
	_, err := Load(name)
	return err == nil
}

// Name is what to show for a location.
func Name(loc *time.Location) string {
	if loc == nil {
		return time.Local.String()
	}
	return loc.String()
}
