package condition

// This package provides channel-based condition monitoring and composition.

import (
	"context"
	"sync"
	"time"
)

// Mode controls whether a condition stops after its first event or continues
// forwarding events while it can still produce them.
type Mode int

const (
	Once Mode = iota
	Continuous
)

// Event identifies the condition that emitted an event and the leaf sources
// that contributed to it. SourceIDs are copied before an event is published.
type Event struct {
	ConditionID string
	SourceIDs   []string
	Time        time.Time
}

// Condition monitors one source or a composition of other conditions.
type Condition interface {
	ID() string
	Run(context.Context, Mode) <-chan Event
}

// Runtime contains the time and file-polling dependencies used by conditions.
// Zero fields use the process runtime. It is exported so callers can make
// deadline behavior deterministic in tests without making the package know
// about any command-line representation.
type Runtime struct {
	Now          func() time.Time
	NewTimer     func(time.Duration) (<-chan time.Time, func())
	PollInterval time.Duration
}

// Option customizes a condition's runtime dependencies.
type Option func(*runtime)

// WithRuntime applies the supplied runtime dependencies to a condition.
func WithRuntime(value Runtime) Option {
	return func(target *runtime) {
		if value.Now != nil {
			target.now = value.Now
		}
		if value.NewTimer != nil {
			target.newTimer = value.NewTimer
		}
		if value.PollInterval != 0 {
			target.pollInterval = value.PollInterval
		}
	}
}

// WithStartTime makes duration monitoring measure from the supplied startup
// instant rather than the instant at which its Run method is called.
func WithStartTime(value time.Time) Option {
	return func(target *runtime) {
		target.startTime = value
		target.hasStartTime = true
	}
}

type runtime struct {
	now          func() time.Time
	newTimer     func(time.Duration) (<-chan time.Time, func())
	pollInterval time.Duration
	startTime    time.Time
	hasStartTime bool
}

func newRuntime(options []Option) runtime {
	value := runtime{
		now:          time.Now,
		newTimer:     newTimer,
		pollInterval: filePollInterval,
	}
	for _, option := range options {
		if option != nil {
			option(&value)
		}
	}
	if value.now == nil {
		value.now = time.Now
	}
	if value.newTimer == nil {
		value.newTimer = newTimer
	}
	if value.pollInterval <= 0 {
		value.pollInterval = filePollInterval
	}
	return value
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func newTimer(delay time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(delay)
	return timer.C, func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// internalCondition lets composite conditions share one child monitor when a
// caller uses the same leaf in more than one branch. External implementations
// still work through the public Condition interface.
type internalCondition interface {
	Condition
	run(context.Context, Mode, *runRegistry) <-chan Event
	conditionKey() *conditionToken
}

type conditionToken struct{ _ byte }

type runRegistry struct {
	ctx context.Context

	mu      sync.Mutex
	streams map[any]*sharedStream
	// Immediate child events must wait until every branch has subscribed so a
	// shared leaf cannot emit and close before a later branch is attached.
	pending []*sharedStream
	started bool
}

type sharedStream struct {
	registry *runRegistry
	key      any
	ctx      context.Context
	cancel   context.CancelFunc
	source   <-chan Event
	finished chan struct{}

	mu     sync.Mutex
	subs   map[*subscription]struct{}
	closed bool
}

type subscription struct {
	stream *sharedStream
	ctx    context.Context
	ch     chan Event
}

func newRunRegistry(ctx context.Context) *runRegistry {
	return &runRegistry{
		ctx:     normalizeContext(ctx),
		streams: make(map[any]*sharedStream),
	}
}

func (registry *runRegistry) subscribe(child Condition, ctx context.Context, mode Mode) *subscription {
	ctx = normalizeContext(ctx)
	key := conditionRegistryKey(child)
	sub := &subscription{ctx: ctx, ch: make(chan Event, 1)}
	start := false

	registry.mu.Lock()
	stream := registry.streams[key]
	if stream == nil {
		streamCtx, cancel := context.WithCancel(registry.ctx)
		stream = &sharedStream{
			registry: registry,
			key:      key,
			ctx:      streamCtx,
			cancel:   cancel,
			finished: make(chan struct{}),
			subs:     make(map[*subscription]struct{}),
		}
		registry.streams[key] = stream
		start = true
	}
	sub.stream = stream
	stream.mu.Lock()
	if stream.closed {
		close(sub.ch)
	} else {
		stream.subs[sub] = struct{}{}
	}
	stream.mu.Unlock()
	registry.mu.Unlock()
	if start {
		stream.source = runChild(child, stream.ctx, mode, registry)
		registry.startStream(stream)
	}
	return sub
}

func (registry *runRegistry) startStream(stream *sharedStream) {
	registry.mu.Lock()
	if registry.started {
		registry.mu.Unlock()
		go stream.dispatch()
		return
	}
	registry.pending = append(registry.pending, stream)
	registry.mu.Unlock()
}

func (registry *runRegistry) startPending() {
	registry.mu.Lock()
	if registry.started {
		registry.mu.Unlock()
		return
	}
	registry.started = true
	pending := registry.pending
	registry.pending = nil
	registry.mu.Unlock()
	for _, stream := range pending {
		go stream.dispatch()
	}
}

func runChild(child Condition, ctx context.Context, mode Mode, registry *runRegistry) <-chan Event {
	if implementation, ok := child.(internalCondition); ok {
		return implementation.run(ctx, mode, registry)
	}
	return child.Run(ctx, mode)
}

func (stream *sharedStream) dispatch() {
	defer stream.finish()
	for {
		select {
		case event, ok := <-stream.source:
			if !ok {
				return
			}
			stream.mu.Lock()
			subs := make([]*subscription, 0, len(stream.subs))
			for sub := range stream.subs {
				subs = append(subs, sub)
			}
			stream.mu.Unlock()

			for _, sub := range subs {
				select {
				case sub.ch <- cloneEvent(event):
				case <-sub.ctx.Done():
					sub.unsubscribe()
				case <-stream.ctx.Done():
					return
				}
			}
		case <-stream.ctx.Done():
			return
		}
	}
}

func (stream *sharedStream) finish() {
	stream.mu.Lock()
	stream.closed = true
	for sub := range stream.subs {
		close(sub.ch)
		delete(stream.subs, sub)
	}
	stream.mu.Unlock()
	stream.cancel()
	close(stream.finished)
}

func (sub *subscription) unsubscribe() {
	stream := sub.stream
	stream.registry.mu.Lock()
	stream.mu.Lock()
	if _, exists := stream.subs[sub]; !exists {
		stream.mu.Unlock()
		stream.registry.mu.Unlock()
		return
	}
	delete(stream.subs, sub)
	empty := len(stream.subs) == 0 && !stream.closed
	if empty && stream.registry.streams[stream.key] == stream {
		delete(stream.registry.streams, stream.key)
	}
	stream.mu.Unlock()
	stream.registry.mu.Unlock()
	if empty {
		stream.cancel()
	}
}

func conditionRegistryKey(child Condition) any {
	if implementation, ok := child.(internalCondition); ok {
		if key := implementation.conditionKey(); key != nil {
			return key
		}
	}
	// External implementations are intentionally not shared: their concrete
	// state and Run semantics are outside this package's control.
	return &conditionToken{}
}

func emitEvent(ctx context.Context, out chan<- Event, event Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func eventSources(event Event) []string {
	if len(event.SourceIDs) != 0 {
		return append([]string(nil), event.SourceIDs...)
	}
	if event.ConditionID == "" {
		return nil
	}
	return []string{event.ConditionID}
}

func cloneEvent(event Event) Event {
	event.SourceIDs = append([]string(nil), event.SourceIDs...)
	return event
}

func appendUnique(values []string, additions ...string) []string {
	for _, addition := range additions {
		seen := false
		for _, value := range values {
			if value == addition {
				seen = true
				break
			}
		}
		if !seen {
			values = append(values, addition)
		}
	}
	return values
}

func compositeEvent(id string, source Event, now func() time.Time) Event {
	at := source.Time
	if at.IsZero() {
		at = now()
	}
	return Event{
		ConditionID: id,
		SourceIDs:   eventSources(source),
		Time:        at,
	}
}

func runRoot(condition Condition, ctx context.Context, mode Mode, run func(context.Context, Mode, *runRegistry) <-chan Event) <-chan Event {
	ctx = normalizeContext(ctx)
	registry := newRunRegistry(ctx)
	events := run(ctx, mode, registry)
	registry.startPending()
	return events
}
