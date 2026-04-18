// Package clock provides timestamp pairs (wall, monotonic) suitable for
// recording market data across heterogeneous sources.
//
// Wall (UnixNano) is the only axis that can be compared across processes
// and machines — it is the cross-source sync axis. Monotonic is anchored
// to process start; use it to measure intervals without being affected by
// NTP, DST, or clock-set adjustments.
package clock

import "time"

// Stamp is a wall + monotonic timestamp pair.
type Stamp struct {
	Wall int64 `json:"t"` // unix-nanoseconds (UTC)
	Mono int64 `json:"m"` // process monotonic-nanoseconds
}

var processStart = time.Now()

// Now returns a Stamp for the current instant. Both fields are read from
// the same time.Now() call so they refer to the same physical event.
func Now() Stamp {
	now := time.Now()
	return Stamp{
		Wall: now.UnixNano(),
		Mono: int64(now.Sub(processStart)),
	}
}

// SinceStart returns nanoseconds elapsed since the package was first loaded.
func SinceStart() time.Duration { return time.Since(processStart) }
