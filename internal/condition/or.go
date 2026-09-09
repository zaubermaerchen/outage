package condition

// This file implements OR composition for condition event streams.

import "context"

type orCondition struct {
	id       string
	children []Condition
	runtime  runtime
	token    *conditionToken
}

// Or creates a condition that emits whenever any child emits.
func Or(id string, children ...Condition) Condition {
	return &orCondition{id: id, children: append([]Condition(nil), children...), runtime: newRuntime(nil), token: &conditionToken{}}
}

func (condition *orCondition) ID() string { return condition.id }

func (condition *orCondition) conditionKey() *conditionToken { return condition.token }

func (condition *orCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(condition, ctx, mode, condition.run)
}

func (condition *orCondition) run(ctx context.Context, mode Mode, registry *runRegistry) <-chan Event {
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
			return
		}

		merged := make(chan orMessage, len(children))
		for _, child := range children {
			go func(source <-chan Event) {
				forwardOrChild(runCtx, source, merged)
				forwardersDone <- struct{}{}
			}(child.ch)
		}
		remaining := len(children)
		for remaining > 0 {
			select {
			case message := <-merged:
				if message.closed {
					remaining--
					continue
				}
				event := compositeEvent(condition.id, message.event, condition.runtime.now)
				if !emitEvent(runCtx, events, event) {
					return
				}
				if mode == Once {
					return
				}
			case <-runCtx.Done():
				return
			}
		}
	}()

	return events
}

type orMessage struct {
	event  Event
	closed bool
}

func forwardOrChild(ctx context.Context, source <-chan Event, merged chan<- orMessage) {
	for {
		select {
		case event, ok := <-source:
			if !ok {
				select {
				case merged <- orMessage{closed: true}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case merged <- orMessage{event: event}:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
