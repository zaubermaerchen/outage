package condition

// This file implements OS signal conditions.

import (
	"context"
	"os"
	"os/signal"
)

type signalCondition struct {
	id      string
	signal  os.Signal
	runtime runtime
	token   *conditionToken
}

// NewSignal creates a condition for an already-resolved operating-system
// signal. Signal names and CLI aliases remain outside this package.
func NewSignal(id string, value os.Signal, options ...Option) Condition {
	return &signalCondition{id: id, signal: value, runtime: newRuntime(options), token: &conditionToken{}}
}

func (condition *signalCondition) ID() string { return condition.id }

func (condition *signalCondition) conditionKey() *conditionToken { return condition.token }

func (condition *signalCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(condition, ctx, mode, condition.run)
}

func (condition *signalCondition) run(ctx context.Context, mode Mode, _ *runRegistry) <-chan Event {
	ctx = normalizeContext(ctx)
	events := make(chan Event, 1)
	received := make(chan os.Signal, 1)
	if condition.signal == nil {
		close(events)
		return events
	}
	signal.Notify(received, condition.signal)

	go func() {
		defer close(events)
		defer signal.Stop(received)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-received:
				if !ok {
					return
				}
				if !emitEvent(ctx, events, Event{
					ConditionID: condition.id,
					SourceIDs:   []string{condition.id},
					Time:        condition.runtime.now(),
				}) || mode == Once {
					return
				}
			}
		}
	}()

	return events
}
