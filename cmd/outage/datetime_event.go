package main

// This file parses datetime event values and monitors absolute deadlines.

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// runtimeClock contains the process-wide time dependencies used by event
// execution. Keeping these dependencies at the execution boundary allows
// absolute deadlines to be tested without waiting on wall-clock time.
type runtimeClock struct {
	now      func() time.Time
	location *time.Location
	newTimer func(time.Duration) (<-chan time.Time, func())
}

func defaultRuntimeClock() runtimeClock {
	location := time.Local
	if location == nil {
		location = time.UTC
	}
	return runtimeClock{
		now:      time.Now,
		location: location,
		newTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			timer := time.NewTimer(delay)
			return timer.C, func() {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
		},
	}
}

func (clock runtimeClock) normalized() runtimeClock {
	if clock.now == nil {
		clock.now = time.Now
	}
	if clock.location == nil {
		clock.location = time.Local
		if clock.location == nil {
			clock.location = time.UTC
		}
	}
	if clock.newTimer == nil {
		clock.newTimer = defaultRuntimeClock().newTimer
	}
	return clock
}

// parseDatetimeEvent parses the event's exact datetime: prefix and deadline,
// returning a diagnostic that identifies the event type.
func parseDatetimeEvent(event string, location *time.Location) (time.Time, error) {
	if !strings.HasPrefix(event, "datetime:") {
		return time.Time{}, fmt.Errorf("invalid datetime %q: missing datetime: prefix", event)
	}
	value := event[len("datetime:"):]
	deadline, err := parseAbsoluteDeadline(value, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid datetime %q: %w", event, err)
	}
	return deadline, nil
}

// parseAbsoluteDeadline accepts the documented local wall-clock layouts and
// strict RFC3339 values with an explicit timezone. The explicit checks are
// intentional: time.Parse and time.Date otherwise accept or normalize several
// values outside the CLI contract.
func parseAbsoluteDeadline(value string, location *time.Location) (time.Time, error) {
	location = normalizeLocation(location)
	if (len(value) == 20 && value[19] == 'Z') ||
		(len(value) == 25 && (value[19] == '+' || value[19] == '-')) {
		if len(value) == 25 {
			// time.Parse normalizes some out-of-range offset minutes, so
			// validate the textual RFC3339 offset before parsing it.
			if !allASCIIDigits(value[20:22]) || value[22] != ':' || !allASCIIDigits(value[23:25]) {
				return time.Time{}, fmt.Errorf("datetime timezone offset must use +HH:MM or -HH:MM")
			}
			if parseASCIIDigits(value[20:22]) > 23 || parseASCIIDigits(value[23:25]) > 59 {
				return time.Time{}, fmt.Errorf("datetime timezone offset is out of range")
			}
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, fmt.Errorf("datetime must use RFC3339 with seconds and timezone: %w", err)
		}
		if parsed.Year() < 1 || parsed.Year() > 9999 {
			return time.Time{}, fmt.Errorf("datetime year must be between 0001 and 9999")
		}
		return parsed, nil
	}
	if len(value) != len("2006-01-02T15:04") && len(value) != len("2006-01-02T15:04:05") {
		return time.Time{}, fmt.Errorf("datetime must use YYYY-MM-DDTHH:MM[:SS]")
	}
	if value[4] != '-' || value[7] != '-' || value[10] != 'T' || value[13] != ':' {
		return time.Time{}, fmt.Errorf("datetime must use YYYY-MM-DDTHH:MM[:SS]")
	}
	if len(value) == 19 && value[16] != ':' {
		return time.Time{}, fmt.Errorf("datetime must use YYYY-MM-DDTHH:MM:SS")
	}
	if !allASCIIDigits(value[:4]) || !allASCIIDigits(value[5:7]) ||
		!allASCIIDigits(value[8:10]) || !allASCIIDigits(value[11:13]) ||
		!allASCIIDigits(value[14:16]) {
		return time.Time{}, fmt.Errorf("datetime contains non-numeric fields")
	}
	year := parseASCIIDigits(value[:4])
	month := time.Month(parseASCIIDigits(value[5:7]))
	day := parseASCIIDigits(value[8:10])
	hour := parseASCIIDigits(value[11:13])
	minute := parseASCIIDigits(value[14:16])
	second := 0
	if len(value) == 19 {
		if !allASCIIDigits(value[17:19]) {
			return time.Time{}, fmt.Errorf("datetime contains non-numeric fields")
		}
		second = parseASCIIDigits(value[17:19])
	}
	if year < 1 || year > 9999 {
		return time.Time{}, fmt.Errorf("datetime year must be between 0001 and 9999")
	}

	parsed := time.Date(year, month, day, hour, minute, second, 0, location)
	if !sameLocalDateTime(parsed, year, month, day, hour, minute, second) {
		return time.Time{}, fmt.Errorf("datetime is not a valid local datetime")
	}
	return earliestLocalInstant(parsed, year, month, day, hour, minute, second, location), nil
}

func normalizeLocation(location *time.Location) *time.Location {
	if location != nil {
		return location
	}
	if time.Local != nil {
		return time.Local
	}
	return time.UTC
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func parseASCIIDigits(value string) int {
	result := 0
	for index := 0; index < len(value); index++ {
		result = result*10 + int(value[index]-'0')
	}
	return result
}

func sameLocalDateTime(value time.Time, year int, month time.Month, day, hour, minute, second int) bool {
	return value.Year() == year && value.Month() == month && value.Day() == day &&
		value.Hour() == hour && value.Minute() == minute && value.Second() == second && value.Nanosecond() == 0
}

// earliestLocalInstant checks every TZif interval that can map to the
// requested wall time. This preserves the earlier absolute occurrence for a
// DST overlap, including zones with short-lived historical offsets.
func earliestLocalInstant(parsed time.Time, year int, month time.Month, day, hour, minute, second int, location *time.Location) time.Time {
	location = normalizeLocation(location)
	start, end := parsed.ZoneBounds()
	// Compare the full value instead of calling IsZero: a real TZif boundary at
	// 0001-01-01T00:00:00Z is a zero instant carrying location information.
	if start == (time.Time{}) && end == (time.Time{}) {
		// UTC and FixedZone locations have one unbounded interval, so the
		// already validated parse is the only possible occurrence. This also
		// avoids imposing TZif's int32 offset representation on FixedZone's
		// unrestricted int offset.
		return parsed
	}

	wall := time.Date(year, month, day, hour, minute, second, 0, time.UTC)
	best := parsed
	considerOffset := func(offset int) {
		const (
			minInt64              = -1 << 63
			maxInt64              = 1<<63 - 1
			unixToInternalSeconds = 62135596800
			maxTimeUnix           = maxInt64 - unixToInternalSeconds
		)
		wallUnix := wall.Unix()
		offsetSeconds := int64(offset)
		if offsetSeconds > 0 && wallUnix < minInt64+offsetSeconds ||
			offsetSeconds < 0 && wallUnix > maxInt64+offsetSeconds {
			return
		}
		candidateUnix := wallUnix - offsetSeconds
		// time.Time stores seconds relative to year 1, so larger Unix values
		// wrap that internal representation even though they fit in int64.
		if candidateUnix > maxTimeUnix {
			return
		}
		candidate := time.Unix(candidateUnix, 0).In(location)
		if sameLocalDateTime(candidate, year, month, day, hour, minute, second) && candidate.Before(best) {
			best = candidate
		}
	}

	if start != (time.Time{}) {
		_, offset := start.Add(-time.Nanosecond).Zone()
		considerOffset(offset)
	}
	if end != (time.Time{}) {
		_, offset := end.Zone()
		considerOffset(offset)
	}

	const (
		minTZifOffset = -1 << 31
		maxTZifOffset = 1<<31 - 1
	)
	lastCandidate := wall.Add(-time.Duration(minTZifOffset) * time.Second)
	for current := wall.Add(-time.Duration(maxTZifOffset) * time.Second).In(location); !current.After(lastCandidate); {
		_, offset := current.Zone()
		considerOffset(offset)

		_, end := current.ZoneBounds()
		// As above, only the literal zero Time is the unbounded-end sentinel.
		if end == (time.Time{}) || end.After(lastCandidate) || !end.After(current) {
			break
		}
		current = end
	}
	return best
}

// startDeadlineMonitor arms the deadline timer synchronously and returns an
// event channel plus an idempotent cleanup function. Re-arming handles dates
// farther away than time.Duration can represent.
func startDeadlineMonitor(deadline *time.Time, now func() time.Time, newTimer func(time.Duration) (<-chan time.Time, func())) (<-chan time.Time, func()) {
	if deadline == nil {
		return nil, nil
	}
	if now == nil {
		now = time.Now
	}
	if newTimer == nil {
		newTimer = defaultRuntimeClock().newTimer
	}

	current := now()
	wait := deadline.Sub(current)
	if wait <= 0 {
		return nil, nil
	}
	// Parsing and setup can consume the remaining interval. Mirror dam's
	// second sample so a deadline reached before timer creation is treated as
	// immediately satisfied instead of arming an already-expired timer.
	current = now()
	wait = deadline.Sub(current)
	if wait <= 0 {
		return nil, nil
	}
	capped := !current.Add(wait).Equal(*deadline)
	if capped {
		wait = time.Duration(1<<63 - 1)
	}
	timerC, stopTimer := newTimer(wait)
	if timerC == nil {
		if stopTimer != nil {
			stopTimer()
		}
		return nil, nil
	}

	state := &deadlineMonitorState{done: make(chan struct{})}
	if !state.arm(stopTimer) {
		return nil, nil
	}
	events := make(chan time.Time, 1)
	go func() {
		for {
			select {
			case fired, ok := <-timerC:
				state.disarm()
				if !ok {
					return
				}
				if !capped {
					events <- fired
					return
				}
				current = now()
				wait = deadline.Sub(current)
				if wait <= 0 {
					events <- fired
					return
				}
				capped = !current.Add(wait).Equal(*deadline)
				if capped {
					wait = time.Duration(1<<63 - 1)
				}
				timerC, stopTimer = newTimer(wait)
				if timerC == nil {
					if stopTimer != nil {
						stopTimer()
					}
					return
				}
				if !state.arm(stopTimer) {
					return
				}
			case <-state.done:
				state.disarm()
				return
			}
		}
	}()

	return events, state.stop
}

type deadlineMonitorState struct {
	done chan struct{}

	stopOnce  sync.Once
	mu        sync.Mutex
	stopped   bool
	stopTimer func()
}

func (state *deadlineMonitorState) arm(stopTimer func()) bool {
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		if stopTimer != nil {
			stopTimer()
		}
		return false
	}
	state.stopTimer = stopTimer
	state.mu.Unlock()
	return true
}

func (state *deadlineMonitorState) disarm() {
	state.mu.Lock()
	stopTimer := state.stopTimer
	state.stopTimer = nil
	state.mu.Unlock()
	if stopTimer != nil {
		stopTimer()
	}
}

func (state *deadlineMonitorState) stop() {
	state.stopOnce.Do(func() {
		state.mu.Lock()
		state.stopped = true
		stopTimer := state.stopTimer
		state.stopTimer = nil
		state.mu.Unlock()
		close(state.done)
		if stopTimer != nil {
			stopTimer()
		}
	})
}
