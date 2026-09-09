package condition

// This file polls paths and emits on regular-file state transitions.

import (
	"context"
	"os"
	"time"
)

const filePollInterval = 100 * time.Millisecond

type fileCondition struct {
	id      string
	path    string
	runtime runtime
	token   *conditionToken
}

// NewFile creates a condition for a path resolving to a regular file. A
// symlink is considered regular when its target is regular, matching os.Stat.
func NewFile(id, path string, options ...Option) Condition {
	return &fileCondition{id: id, path: path, runtime: newRuntime(options), token: &conditionToken{}}
}

func (condition *fileCondition) ID() string { return condition.id }

func (condition *fileCondition) conditionKey() *conditionToken { return condition.token }

func (condition *fileCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(condition, ctx, mode, condition.run)
}

func (condition *fileCondition) run(ctx context.Context, mode Mode, _ *runRegistry) <-chan Event {
	ctx = normalizeContext(ctx)
	events := make(chan Event, 1)
	regular, _ := isRegularFile(condition.path)
	if regular {
		select {
		case events <- Event{
			ConditionID: condition.id,
			SourceIDs:   []string{condition.id},
			Time:        condition.runtime.now(),
		}:
		case <-ctx.Done():
			close(events)
			return events
		}
		if mode == Once {
			close(events)
			return events
		}
	}

	go func() {
		defer close(events)
		previous := regular

		ticker := time.NewTicker(condition.runtime.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, _ := isRegularFile(condition.path)
				if current == previous {
					continue
				}
				previous = current
				if !current {
					continue
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

func isRegularFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
