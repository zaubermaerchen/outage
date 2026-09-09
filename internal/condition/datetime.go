package condition

// This file implements absolute deadline conditions and long-range timer
// rearming without exposing date parsing or CLI formats to the package.

import (
	"context"
	"time"
)

type dateTimeCondition struct {
	id       string
	deadline time.Time
	runtime  runtime
	token    *conditionToken
}

// NewDateTime creates a condition that emits when deadline is reached.
func NewDateTime(id string, deadline time.Time, options ...Option) Condition {
	return &dateTimeCondition{id: id, deadline: deadline, runtime: newRuntime(options), token: &conditionToken{}}
}

func (condition *dateTimeCondition) ID() string { return condition.id }

func (condition *dateTimeCondition) conditionKey() *conditionToken { return condition.token }

func (condition *dateTimeCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(condition, ctx, mode, condition.run)
}

func (condition *dateTimeCondition) run(ctx context.Context, _ Mode, _ *runRegistry) <-chan Event {
	ctx = normalizeContext(ctx)
	events := make(chan Event, 1)
	now := condition.runtime.now
	deadline := condition.deadline
	current := now()

	if deadline.After(current) {
		wait := deadline.Sub(current)
		current = now()
		wait = deadline.Sub(current)
		if wait > 0 {
			capped := !current.Add(wait).Equal(deadline)
			timerC, stopTimer := condition.runtime.newTimer(cappedWait(wait))
			if timerC != nil {
				go condition.emitDeadline(ctx, events, deadline, timerC, stopTimer, capped)
				return events
			}
			if stopTimer != nil {
				stopTimer()
			}
		}
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

func (condition *dateTimeCondition) emitDeadline(ctx context.Context, events chan<- Event, deadline time.Time, timerC <-chan time.Time, stopTimer func(), capped bool) {
	defer close(events)
	stopTimerOnce := func() {
		if stopTimer == nil {
			return
		}
		stop := stopTimer
		stopTimer = nil
		stop()
	}
	defer stopTimerOnce()
	for {
		select {
		case fired, ok := <-timerC:
			if !ok {
				return
			}
			if !capped {
				emitEvent(ctx, events, Event{
					ConditionID: condition.id,
					SourceIDs:   []string{condition.id},
					Time:        fired,
				})
				return
			}
			current := condition.runtime.now()
			wait := deadline.Sub(current)
			if wait <= 0 {
				emitEvent(ctx, events, Event{
					ConditionID: condition.id,
					SourceIDs:   []string{condition.id},
					Time:        fired,
				})
				return
			}
			stopTimerOnce()
			capped = !current.Add(wait).Equal(deadline)
			timerC, stopTimer = condition.runtime.newTimer(cappedWait(wait))
			if timerC == nil {
				stopTimerOnce()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

const maxTimerDuration = time.Duration(1<<63 - 1)

func cappedWait(wait time.Duration) time.Duration {
	if wait < 0 {
		return 0
	}
	if wait > maxTimerDuration {
		return maxTimerDuration
	}
	return wait
}
