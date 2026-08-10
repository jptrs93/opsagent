package state

import (
	"time"
)

// Time column encodings shared by the per-entity conversion files.

// clockToNanos serializes a status HLC clock to its DB integer form (unix
// nanoseconds). Zero time maps to 0 — the "no status yet" placeholder sentinel.
func clockToNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// nanosToClock is the inverse of clockToNanos.
func nanosToClock(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// millisToTime converts the DB integer form (epoch ms) to a wall-clock time,
// mapping the 0 sentinel to the zero time so an unset created_at never
// surfaces as a 1970 timestamp.
func millisToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// unixOrZero keeps a zero time.Time as a zero column rather than the 1970
// epoch, so "never approved" and "approved at the epoch" stay distinguishable.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
