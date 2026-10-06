package pq

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func clockToNanos(t apigen.Maybe[time.Time]) int64 {
	if !t.Present {
		return 0
	}
	return t.Value.UnixNano()
}

func nanosToClock(n int64) apigen.Maybe[time.Time] {
	if n == 0 {
		return apigen.Maybe[time.Time]{}
	}
	return apigen.Some(time.Unix(0, n))
}

func millisToTime(ms int64) apigen.Maybe[time.Time] {
	if ms == 0 {
		return apigen.Maybe[time.Time]{}
	}
	return apigen.Some(time.UnixMilli(ms))
}

func timeToMillis(t apigen.Maybe[time.Time]) int64 {
	if !t.Present {
		return 0
	}
	return t.Value.UnixMilli()
}

func unixTime(unix int64) apigen.Maybe[time.Time] {
	if unix == 0 {
		return apigen.Maybe[time.Time]{}
	}
	return apigen.Some(time.Unix(unix, 0))
}

func unixOrZero(t apigen.Maybe[time.Time]) int64 {
	if !t.Present {
		return 0
	}
	return t.Value.Unix()
}

// directoryRef is the entity form of a directory column: 0 is the implicit
// root, which the entity carries as an absent parent.
func directoryRef(id uint64) apigen.Maybe[uint64] {
	if id == 0 {
		return apigen.Maybe[uint64]{}
	}
	return apigen.Some(id)
}

type scanner interface {
	Scan(dest ...any) error
}
