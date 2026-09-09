package condition

// This file implements elapsed-duration conditions.

import (
	"context"
	"time"
)

type durationCondition struct {
	id       string
	duration time.Duration
	runtime  runtime
	token    *conditionToken
}

// NewDuration creates a condition that emits after duration has elapsed.
// Durations are naturally one-shot, so Continuous mode has the same finite
// lifecycle as Once mode.
func NewDuration(id string, duration time.Duration, options ...Option) Condition {
	return &durationCondition{id: id, duration: duration, runtime: newRuntime(options), token: &conditionToken{}}
}

func (condition *durationCondition) ID() string { return condition.id }

func (condition *durationCondition) conditionKey() *conditionToken { return condition.token }

func (condition *durationCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(condition, ctx, mode, condition.run)
}

func (condition *durationCondition) run(ctx context.Context, _ Mode, _ *runRegistry) <-chan Event {
	ctx = normalizeContext(ctx)
	events := make(chan Event, 1)

	now := condition.runtime.now
	start := now()
	if condition.runtime.hasStartTime {
		start = condition.runtime.startTime
	}
	wait := condition.duration
	if elapsed := now().Sub(start); elapsed >= wait {
		wait = 0
	} else {
		wait -= elapsed
	}

	var timerC <-chan time.Time
	var stopTimer func()
	if wait > 0 {
		timerC, stopTimer = condition.runtime.newTimer(wait)
	}
	if wait <= 0 || timerC == nil {
		if stopTimer != nil {
			stopTimer()
		}
		select {
		case events <- Event{
			ConditionID: condition.id,
			SourceIDs:   []string{condition.id},
			Time:        now(),
		}:
		case <-ctx.Done():
		}
		close(events)
		return events
	}

	go func() {
		defer close(events)
		if stopTimer != nil {
			defer stopTimer()
		}
		select {
		case fired, ok := <-timerC:
			if !ok {
				return
			}
			emitEvent(ctx, events, Event{
				ConditionID: condition.id,
				SourceIDs:   []string{condition.id},
				Time:        fired,
			})
		case <-ctx.Done():
		}
	}()

	return events
}
