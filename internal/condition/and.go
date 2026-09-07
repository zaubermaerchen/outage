package condition

// This file implements AND composition, including continuous cycle resets.

import "context"

type andCondition struct {
	id       string
	children []Condition
	runtime  runtime
	token    *conditionToken
}

type indexedEvent struct {
	index  int
	event  Event
	closed bool
}

// And creates a condition that emits after every child has emitted once in a
// cycle. Continuous mode starts a fresh cycle after each aggregate event.
func And(id string, children ...Condition) Condition {
	return &andCondition{id: id, children: append([]Condition(nil), children...), runtime: newRuntime(nil), token: &conditionToken{}}
}

func (condition *andCondition) ID() string { return condition.id }

func (condition *andCondition) conditionKey() *conditionToken { return condition.token }

func (condition *andCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(condition, ctx, mode, condition.run)
}

func (condition *andCondition) run(ctx context.Context, mode Mode, registry *runRegistry) <-chan Event {
	ctx = normalizeContext(ctx)
	runCtx, cancel := context.WithCancel(ctx)
	events := make(chan Event, 1)
	children := make([]*subscription, 0, len(condition.children))
	for _, child := range condition.children {
		if child == nil {
			continue
		}
		children = append(children, registry.subscribe(child, runCtx, mode))
	}
	forwardersDone := make(chan struct{}, len(children))

	go func() {
		defer close(events)
		defer func() {
			cancel()
			for _, child := range children {
				child.unsubscribe()
			}
			for range children {
				<-forwardersDone
			}
		}()
		if len(children) == 0 {
			emitEvent(runCtx, events, Event{ConditionID: condition.id, Time: condition.runtime.now()})
			return
		}

		merged := make(chan indexedEvent, len(children))
		for index, child := range children {
			go func(index int, source <-chan Event) {
				forwardIndexedChild(runCtx, index, source, merged)
				forwardersDone <- struct{}{}
			}(index, child.ch)
		}

		matched := make([]bool, len(children))
		closedChild := make([]bool, len(children))
		matchedSources := make([][]string, len(children))
		latestTime := condition.runtime.now()
		remainingOpen := len(children)
		for remainingOpen > 0 {
			select {
			case child := <-merged:
				if child.closed {
					if closedChild[child.index] {
						continue
					}
					closedChild[child.index] = true
					remainingOpen--
					if !matched[child.index] {
						return
					}
					continue
				}
				if matched[child.index] {
					continue
				}
				matched[child.index] = true
				matchedSources[child.index] = eventSources(child.event)
				if !child.event.Time.IsZero() {
					latestTime = child.event.Time
				}
				if !allMatched(matched) {
					continue
				}

				sources := make([]string, 0, len(children))
				for _, childSources := range matchedSources {
					sources = appendUnique(sources, childSources...)
				}
				event := Event{
					ConditionID: condition.id,
					SourceIDs:   sources,
					Time:        latestTime,
				}
				if !emitEvent(runCtx, events, event) || mode == Once {
					return
				}
				matched = make([]bool, len(children))
				matchedSources = make([][]string, len(children))
				latestTime = condition.runtime.now()
				if hasClosedChild(closedChild) {
					return
				}
			case <-runCtx.Done():
				return
			}
		}
	}()

	return events
}

func forwardIndexedChild(ctx context.Context, index int, source <-chan Event, merged chan<- indexedEvent) {
	for {
		select {
		case event, ok := <-source:
			if !ok {
				select {
				case merged <- indexedEvent{index: index, closed: true}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case merged <- indexedEvent{index: index, event: event}:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func allMatched(matched []bool) bool {
	for _, value := range matched {
		if !value {
			return false
		}
	}
	return true
}

func hasClosedChild(closed []bool) bool {
	for _, value := range closed {
		if value {
			return true
		}
	}
	return false
}
