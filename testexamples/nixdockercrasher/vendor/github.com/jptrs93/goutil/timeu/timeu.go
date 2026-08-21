package timeu

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/jptrs93/goutil/contextu"
)

const (
	Month        = time.Hour * 24 * 30
	Day          = time.Hour * 24
	RFC3339Milli = "2006-01-02T15:04:05.000Z"
)

func MustParse(val string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05Z", val)
	if err != nil {
		panic(fmt.Sprintf("failed to parse time '%v': %v", val, err))
	}
	t = t.UTC()
	return t
}

func MinTime(v ...time.Time) time.Time {
	var winner time.Time
	if len(v) == 0 {
		return winner
	}
	winner = v[0]
	for _, t := range v[1:] {
		if t.Before(winner) {
			winner = t
		}
	}
	return winner
}

func MaxTime(v ...time.Time) time.Time {
	var winner time.Time
	if len(v) == 0 {
		return winner
	}
	winner = v[0]
	for _, t := range v[1:] {
		if t.After(winner) {
			winner = t
		}
	}
	return winner
}

func IsoWeekStartEnd(year int, week int) (time.Time, time.Time) {
	// January 4 is always in ISO week 1.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	offset := (int(jan4.Weekday()) + 6) % 7
	startOfWeek1 := jan4.AddDate(0, 0, -offset)

	weekStart := startOfWeek1.AddDate(0, 0, (week-1)*7)

	calculatedYear, calculatedWeek := weekStart.ISOWeek()
	if calculatedYear != year || calculatedWeek != week || weekStart.Weekday() != time.Monday {
		panic("invalid year/week combination")
	}

	weekEnd := weekStart.AddDate(0, 0, 7)
	return weekStart, weekEnd
}

func TruncateIsoWeek(t time.Time) time.Time {
	y, w := t.ISOWeek()
	s, _ := IsoWeekStartEnd(y, w)
	return s
}

type FixedImmediateTimer struct {
	NextTriggerTime time.Time
	Interval        time.Duration
	Offset          time.Duration
	Iterations      int
}

func NewTimerWithJitter(interval time.Duration) *FixedImmediateTimer {
	return NewFixedImmediateTimer(jitter(), interval)
}

func NewFixedImmediateTimer(offset, interval time.Duration) *FixedImmediateTimer {
	// the first tick is at epoch 0 + offset then every interval after that
	tNow := time.Now().UTC()
	t0 := time.Time{}.UTC().Add(offset)
	// note we have to use milli's as max duration is 290 years
	diffMilli := tNow.UnixMilli() - t0.UnixMilli()
	n := diffMilli / interval.Milliseconds()
	nextTriggerMilli := t0.UnixMilli() + (n+1)*interval.Milliseconds()
	nextTriggerTime := time.Unix(nextTriggerMilli/1000, (nextTriggerMilli%1000)*int64(time.Millisecond)).UTC()
	return &FixedImmediateTimer{
		NextTriggerTime: nextTriggerTime,
		Interval:        interval,
		Offset:          offset,
	}
}

func (t *FixedImmediateTimer) Wait() {
	t.Iterations += 1
	pauseDuration := t.NextTriggerTime.Sub(time.Now().UTC())
	// we want the timer to trigger immediately upon starting
	// so if the first pause is > 10s then just wait a random [0,5]s and return
	if t.Iterations == 1 && pauseDuration > time.Second*10 {
		time.Sleep(time.Duration(rand.Intn(5000)) * time.Millisecond)
		return
	}
	if pauseDuration > 0 {
		time.Sleep(pauseDuration)
	}
	t.NextTriggerTime = time.Now().Add(t.Interval)
}

func jitter() time.Duration {
	maxDuration := time.Hour
	randomDuration := time.Duration(rand.Int63n(int64(maxDuration)))
	return randomDuration
}

type OffsetTicker struct {
	C    <-chan time.Time
	stop chan struct{}
	once sync.Once
}

type Schedule interface {
	NextAfter(time.Time) time.Time
}

type DailySchedule struct {
	Hour     int
	Minute   int
	Second   int
	Location *time.Location
}

func (s DailySchedule) NextAfter(now time.Time) time.Time {
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}

	now = now.In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, s.Second, 0, loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

type WeeklySchedule struct {
	Weekday  time.Weekday
	Hour     int
	Minute   int
	Second   int
	Location *time.Location
}

func (s WeeklySchedule) NextAfter(now time.Time) time.Time {
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}

	now = now.In(loc)
	daysUntil := (int(s.Weekday) - int(now.Weekday()) + 7) % 7
	nextDay := now.AddDate(0, 0, daysUntil)
	next := time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), s.Hour, s.Minute, s.Second, 0, loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 7)
	}
	return next
}

type ScheduleTicker struct {
	C    <-chan time.Time
	stop chan struct{}
	once sync.Once
}

type ScheduleTickerOption func(*scheduleTickerConfig)

type scheduleTickerConfig struct {
	randomJitterStd time.Duration
}

func WithRandomJitterStd(std time.Duration) ScheduleTickerOption {
	return func(c *scheduleTickerConfig) {
		c.randomJitterStd = std
	}
}

func NewScheduleTicker(schedule Schedule, opts ...ScheduleTickerOption) *ScheduleTicker {
	if schedule == nil {
		panic("nil schedule for NewScheduleTicker")
	}

	config := scheduleTickerConfig{}
	for _, opt := range opts {
		opt(&config)
	}

	jitter := time.Duration(0)
	if config.randomJitterStd > 0 {
		jitter = time.Duration(rand.NormFloat64() * float64(config.randomJitterStd))
	}

	c := make(chan time.Time, 1)
	t := &ScheduleTicker{
		C:    c,
		stop: make(chan struct{}),
	}
	go t.run(c, schedule, nextScheduleTickerTick(time.Now(), schedule, jitter), jitter)
	return t
}

func (t *ScheduleTicker) Stop() {
	t.once.Do(func() {
		close(t.stop)
	})
}

func (t *ScheduleTicker) run(c chan<- time.Time, schedule Schedule, next time.Time, jitter time.Duration) {
	for {
		if !next.After(time.Now()) {
			next = nextScheduleTickerTick(time.Now(), schedule, jitter)
		}

		timer := time.NewTimer(time.Until(next))
		select {
		case <-timer.C:
			select {
			case c <- next:
			default:
			}
			next = nextScheduleTickerTick(next, schedule, jitter)
		case <-t.stop:
			return
		}
	}
}

func nextScheduleTickerTick(now time.Time, schedule Schedule, jitter time.Duration) time.Time {
	return schedule.NextAfter(now.Add(-jitter)).Add(jitter)
}

func NewOffsetTicker(period time.Duration, around time.Time, jitterStd time.Duration) *OffsetTicker {
	if period <= 0 {
		panic("non-positive period for NewOffsetTicker")
	}

	jitter := time.Duration(0)
	if jitterStd > 0 {
		jitter = time.Duration(rand.NormFloat64() * float64(jitterStd))
	}

	c := make(chan time.Time, 1)
	t := &OffsetTicker{
		C:    c,
		stop: make(chan struct{}),
	}
	go t.run(c, period, nextOffsetTickerTick(time.Now(), period, around.Add(jitter)))
	return t
}

func (t *OffsetTicker) Stop() {
	t.once.Do(func() {
		close(t.stop)
	})
}

func (t *OffsetTicker) run(c chan<- time.Time, period time.Duration, next time.Time) {
	for {
		if !next.After(time.Now()) {
			next = nextOffsetTickerTick(time.Now(), period, next)
		}

		timer := time.NewTimer(time.Until(next))
		select {
		case <-timer.C:
			select {
			case c <- next:
			default:
			}
			next = next.Add(period)
		case <-t.stop:
			return
		}
	}
}

func nextOffsetTickerTick(now time.Time, period time.Duration, around time.Time) time.Time {
	if around.After(now) {
		return around
	}
	return around.Add((now.Sub(around)/period + 1) * period)
}

type Backoff struct {
	CurrentDuration time.Duration
	MaxDuration     time.Duration
	ResetDuration   time.Duration
	F               func(time.Duration) time.Duration
	lastWait        time.Time
}

func (b *Backoff) Wait() {
	b.WaitWithContext(context.Background())
}

func (b *Backoff) WaitWithContext(ctx context.Context) {
	now := time.Now()
	if b.ResetDuration > 0 && !b.lastWait.IsZero() && now.Sub(b.lastWait) > b.ResetDuration {
		b.Reset()
		b.lastWait = now
		return
	}

	b.CurrentDuration = b.F(b.CurrentDuration)
	if b.MaxDuration > 0 && b.CurrentDuration > b.MaxDuration {
		b.CurrentDuration = b.MaxDuration
	}
	contextu.Sleep(ctx, b.CurrentDuration)
	b.lastWait = time.Now()
}

func (b *Backoff) Reset() {
	b.CurrentDuration = 0
	b.lastWait = time.Time{}
}

func NewExpBackoff(maxDuration, resetDuration time.Duration) *Backoff {
	return &Backoff{
		CurrentDuration: 0,
		MaxDuration:     maxDuration,
		ResetDuration:   resetDuration,
		F: func(i time.Duration) time.Duration {
			return max(i, time.Second) * 2
		},
	}
}

func NewLinearBackoff(increment, maxDuration, resetDuration time.Duration) *Backoff {
	return &Backoff{
		CurrentDuration: 0,
		MaxDuration:     maxDuration,
		ResetDuration:   resetDuration,
		F: func(i time.Duration) time.Duration {
			return i + increment
		},
	}
}
