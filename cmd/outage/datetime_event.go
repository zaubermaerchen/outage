package main

// This file defines the process clock dependencies used by condition monitors.

import (
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
