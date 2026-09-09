package condition

// This file verifies condition lifecycle, event metadata, and composition.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDurationOnceEmitsAndCloses(t *testing.T) {
	timer := make(chan time.Time, 1)
	stopped := make(chan struct{}, 1)
	clock := Runtime{
		Now: func() time.Time { return time.Unix(10, 0) },
		NewTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			if delay != 5*time.Second {
				t.Fatalf("timer delay = %v, want 5s", delay)
			}
			return timer, func() { stopped <- struct{}{} }
		},
	}
	events := NewDuration("duration", 5*time.Second, WithRuntime(clock)).Run(context.Background(), Once)
	timer <- time.Unix(15, 0)

	event := receiveEvent(t, events)
	want := Event{ConditionID: "duration", SourceIDs: []string{"duration"}, Time: time.Unix(15, 0)}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("duration Once channel did not close")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("duration timer was not stopped")
	}
}

func TestDateTimeContinuousEmitsOnceAndStopsTimer(t *testing.T) {
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, time.UTC)
	fired := make(chan time.Time, 1)
	stopped := make(chan struct{}, 1)
	deadline := now.Add(time.Second)
	runtime := Runtime{
		Now: func() time.Time { return now },
		NewTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			if delay != time.Second {
				t.Fatalf("timer delay = %v, want 1s", delay)
			}
			return fired, func() { stopped <- struct{}{} }
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := NewDateTime("datetime", deadline, WithRuntime(runtime)).Run(ctx, Continuous)
	fired <- now
	if event := receiveEvent(t, events); event.ConditionID != "datetime" {
		t.Fatalf("condition ID = %q, want datetime", event.ConditionID)
	}
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("datetime Continuous channel did not close")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("datetime timer was not stopped")
	}
}

func TestDateTimeRechecksNowBeforeArming(t *testing.T) {
	firstNow := time.Date(2026, time.September, 3, 17, 59, 59, 0, time.UTC)
	deadline := firstNow.Add(time.Second)
	nowCalls := 0
	timerCreated := false
	runtime := Runtime{
		Now: func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return firstNow
			}
			return deadline
		},
		NewTimer: func(time.Duration) (<-chan time.Time, func()) {
			timerCreated = true
			return make(chan time.Time), func() {}
		},
	}

	events := NewDateTime("datetime", deadline, WithRuntime(runtime)).Run(context.Background(), Once)
	event := receiveEvent(t, events)
	if event.Time != deadline {
		t.Fatalf("event time = %v, want %v", event.Time, deadline)
	}
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("datetime channel did not close after immediate event")
	}
	if nowCalls < 2 {
		t.Fatalf("now sampled %d times, want initial and pre-arm samples", nowCalls)
	}
	if timerCreated {
		t.Fatal("datetime armed a timer after the deadline became satisfied")
	}
}

func TestDateTimeRearmsCappedTimer(t *testing.T) {
	initial := time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)
	deadline := time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)
	near := deadline.Add(-2 * time.Second)
	first := make(chan time.Time, 1)
	second := make(chan time.Time, 1)
	armed := make(chan time.Duration, 2)
	stopped := make(chan int, 2)
	nowCalls := 0
	timerCalls := 0
	runtime := Runtime{
		Now: func() time.Time {
			nowCalls++
			if nowCalls <= 2 {
				return initial
			}
			return near
		},
		NewTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			timerCalls++
			armed <- delay
			if timerCalls == 1 {
				return first, func() { stopped <- 1 }
			}
			return second, func() { stopped <- 2 }
		},
	}
	events := NewDateTime("datetime", deadline, WithRuntime(runtime)).Run(context.Background(), Once)
	if got := receiveDuration(t, armed); got != maxTimerDuration {
		t.Fatalf("initial timer delay = %v, want %v", got, maxTimerDuration)
	}
	first <- initial
	if got := receiveDuration(t, armed); got != 2*time.Second {
		t.Fatalf("rearmed timer delay = %v, want 2s", got)
	}
	second <- near
	if event := receiveEvent(t, events); event.ConditionID != "datetime" {
		t.Fatalf("condition ID = %q, want datetime", event.ConditionID)
	}
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("datetime channel did not close")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("datetime timer was not stopped")
		}
	}
}

func TestDateTimeStopsEachCappedTimerOnceWhenRearmFails(t *testing.T) {
	initial := time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)
	deadline := time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)
	near := deadline.Add(-2 * time.Second)
	first := make(chan time.Time, 1)
	stopped := make(chan int, 4)
	nowCalls := 0
	timerCalls := 0
	runtime := Runtime{
		Now: func() time.Time {
			nowCalls++
			if nowCalls <= 2 {
				return initial
			}
			return near
		},
		NewTimer: func(time.Duration) (<-chan time.Time, func()) {
			timerCalls++
			index := timerCalls
			if index == 1 {
				return first, func() { stopped <- index }
			}
			return nil, func() { stopped <- index }
		},
	}

	events := NewDateTime("datetime", deadline, WithRuntime(runtime)).Run(context.Background(), Once)
	first <- initial
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("datetime emitted an event after the failed rearm")
	}

	var calls []int
	for {
		select {
		case index := <-stopped:
			calls = append(calls, index)
		default:
			want := []int{1, 2}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("timer stop calls = %v, want %v", calls, want)
			}
			return
		}
	}
}

func TestFileContinuousEmitsOnEachFalseToTrueTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trigger")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := NewFile("file", path).Run(ctx, Continuous)

	assertNoEvent(t, events)
	writeRegularFile(t, path)
	assertEventID(t, events, "file")
	assertNoEvent(t, events)
	removeFile(t, path)
	assertNoEvent(t, events)
	writeRegularFile(t, path)
	assertEventID(t, events, "file")
}

func TestFileContinuousInitialRegularFileEmitsImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trigger")
	writeRegularFile(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := NewFile("file", path).Run(ctx, Continuous)
	assertEventID(t, events, "file")
	assertNoEvent(t, events)
	removeFile(t, path)
	assertNoEvent(t, events)
	writeRegularFile(t, path)
	assertEventID(t, events, "file")
}

func TestAndContinuousResetsAfterCompletionAndIncludesNestedLeafIDs(t *testing.T) {
	first := newManualCondition("first")
	second := newManualCondition("second")
	third := newManualCondition("third")
	root := And("and", Or("nested", first, second), third)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := root.Run(ctx, Continuous)

	first.Emit(time.Unix(1, 0))
	third.Emit(time.Unix(2, 0))
	assertEventID(t, events, "and")
	second.Emit(time.Unix(3, 0))
	assertNoEvent(t, events)
	third.Emit(time.Unix(4, 0))
	event := receiveEvent(t, events)
	want := Event{ConditionID: "and", SourceIDs: []string{"second", "third"}, Time: time.Unix(4, 0)}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}

	first.Emit(time.Unix(5, 0))
	assertNoEvent(t, events)
	third.Emit(time.Unix(6, 0))
	event = receiveEvent(t, events)
	want = Event{ConditionID: "and", SourceIDs: []string{"first", "third"}, Time: time.Unix(6, 0)}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("second event = %#v, want %#v", event, want)
	}
}

func TestAndDoesNotLoseImmediateLeafWhileWaitingForAnotherChild(t *testing.T) {
	now := time.Date(2026, time.September, 3, 18, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "ready")
	writeRegularFile(t, path)
	cases := []struct {
		name      string
		condition Condition
	}{
		{
			name:      "zero duration",
			condition: NewDuration("immediate", 0, WithRuntime(Runtime{Now: func() time.Time { return now }})),
		},
		{
			name:      "past datetime",
			condition: NewDateTime("immediate", now.Add(-time.Second), WithRuntime(Runtime{Now: func() time.Time { return now }})),
		},
		{
			name:      "existing regular file",
			condition: NewFile("immediate", path, WithRuntime(Runtime{Now: func() time.Time { return now }})),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := newManualCondition("waiting")
			events := And("and", tc.condition, waiting).Run(ctx, Once)
			waiting.Emit(now)
			event := receiveEvent(t, events)
			if !reflect.DeepEqual(event.SourceIDs, []string{"immediate", "waiting"}) {
				t.Fatalf("source IDs = %#v, want [immediate waiting]", event.SourceIDs)
			}
		})
	}
}

func TestSharedImmediateLeafReachesEveryCompositeBranch(t *testing.T) {
	now := time.Date(2026, time.September, 3, 18, 0, 0, 0, time.UTC)
	leaf := NewDuration("immediate", 0, WithRuntime(Runtime{
		Now: func() time.Time { return now },
	}))
	root := And("and", Or("left", leaf), Or("right", leaf))

	events := root.Run(context.Background(), Once)
	event := receiveEvent(t, events)
	want := Event{ConditionID: "and", SourceIDs: []string{"immediate"}, Time: now}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}
}

func TestRegistryDelaysImmediateSharedLeafUntilTreeSubscriptionsAreBuilt(t *testing.T) {
	now := time.Date(2026, time.September, 3, 18, 0, 0, 0, time.UTC)
	leaf := NewDuration("immediate", 0, WithRuntime(Runtime{
		Now: func() time.Time { return now },
	}))
	root := newStagedSharedCondition(leaf)
	result := make(chan (<-chan Event), 1)
	go func() {
		result <- root.Run(context.Background(), Once)
	}()

	first := <-root.firstSubscriber
	select {
	case event, ok := <-first.ch:
		if ok {
			t.Fatalf("shared leaf emitted before the tree was built: %#v", event)
		}
		t.Fatal("shared leaf closed before the tree was built")
	case <-time.After(25 * time.Millisecond):
	}
	close(root.allowSecond)

	events := <-result
	event := receiveEvent(t, events)
	if !reflect.DeepEqual(event, Event{ConditionID: "staged", SourceIDs: []string{"immediate"}, Time: now}) {
		t.Fatalf("event = %#v, want staged immediate event", event)
	}
}

func TestOrContinuousForwardsEveryChildTrigger(t *testing.T) {
	first := newManualCondition("first")
	second := newManualCondition("second")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := Or("or", first, second).Run(ctx, Continuous)

	first.Emit(time.Unix(1, 0))
	if got := receiveEvent(t, events); !reflect.DeepEqual(got, Event{
		ConditionID: "or", SourceIDs: []string{"first"}, Time: time.Unix(1, 0),
	}) {
		t.Fatalf("first event = %#v", got)
	}
	second.Emit(time.Unix(2, 0))
	if got := receiveEvent(t, events); !reflect.DeepEqual(got, Event{
		ConditionID: "or", SourceIDs: []string{"second"}, Time: time.Unix(2, 0),
	}) {
		t.Fatalf("second event = %#v", got)
	}
}

func TestContinuousCompositionDeliversFiniteChildBeforeClosing(t *testing.T) {
	for _, tc := range []struct {
		name string
		root Condition
	}{
		{name: "or", root: Or("or", NewDuration("duration", time.Millisecond))},
		{name: "and", root: And("and", NewDuration("duration", time.Millisecond))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			events := tc.root.Run(ctx, Continuous)
			if event := receiveEvent(t, events); event.ConditionID == "" {
				t.Fatal("finite child emitted an empty condition ID")
			}
			if _, ok := receiveClosed(t, events); ok {
				t.Fatal("continuous composition channel did not close")
			}
		})
	}
}

func TestRunCancellationClosesCondition(t *testing.T) {
	manual := newManualCondition("manual")
	ctx, cancel := context.WithCancel(context.Background())
	events := Or("or", manual).Run(ctx, Continuous)
	cancel()
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("condition channel did not close after cancellation")
	}
}

func TestCompositeForwardersStopWhenRunContextIsCanceled(t *testing.T) {
	t.Run("and", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := make(chan Event)
		merged := make(chan indexedEvent, 1)
		merged <- indexedEvent{}
		done := make(chan struct{})
		go func() {
			forwardIndexedChild(ctx, 0, source, merged)
			close(done)
		}()
		source <- Event{ConditionID: "burst"}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("AND forwarder remained blocked after cancellation")
		}
	})

	t.Run("or", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := make(chan Event)
		merged := make(chan orMessage, 1)
		merged <- orMessage{}
		done := make(chan struct{})
		go func() {
			forwardOrChild(ctx, source, merged)
			close(done)
		}()
		source <- Event{ConditionID: "burst"}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("OR forwarder remained blocked after cancellation")
		}
	})
}

func TestRegistryRegistersSubscriberBeforeStartingChild(t *testing.T) {
	observed := make(chan int, 1)
	child := &registryObserverCondition{id: "child", observed: observed, token: &conditionToken{}}
	registry := newRunRegistry(context.Background())
	subscription := registry.subscribe(child, context.Background(), Once)
	defer subscription.unsubscribe()
	if got := <-observed; got != 1 {
		t.Fatalf("subscriber count while child started = %d, want 1", got)
	}
}

func TestRegistryStopsDispatchForNilChildStream(t *testing.T) {
	registry := newRunRegistry(context.Background())
	subscription := registry.subscribe(nilCondition{id: "nil"}, context.Background(), Once)
	registry.startPending()
	subscription.unsubscribe()
	select {
	case <-subscription.stream.finished:
	case <-time.After(time.Second):
		t.Fatal("nil child stream did not stop after cancellation")
	}
}

func TestRegistryDoesNotShareDistinctConditionsWithSameID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan time.Time)
	runtime := Runtime{
		Now: func() time.Time { return time.Unix(10, 0) },
		NewTimer: func(time.Duration) (<-chan time.Time, func()) {
			return waiting, func() {}
		},
	}
	root := And(
		"and",
		NewDuration("same", 0, WithRuntime(runtime)),
		NewDuration("same", time.Hour, WithRuntime(runtime)),
	)
	events := root.Run(ctx, Once)
	select {
	case event := <-events:
		t.Fatalf("distinct same-ID conditions shared event = %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestRegistryResubscribeDoesNotAttachToCanceledStream(t *testing.T) {
	for attempt := 0; attempt < 1000; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		child := newPulseCondition()
		registry := newRunRegistry(ctx)
		first := registry.subscribe(child, ctx, Continuous)
		registry.startPending()

		unsubscribed := make(chan struct{})
		go func() {
			first.unsubscribe()
			close(unsubscribed)
		}()
		second := registry.subscribe(child, ctx, Continuous)
		<-unsubscribed

		select {
		case _, ok := <-second.ch:
			if !ok {
				cancel()
				t.Fatalf("attempt %d: resubscriber received a closed canceled stream", attempt)
			}
		case <-time.After(2 * time.Millisecond):
		}
		child.pulse()
		select {
		case event, ok := <-second.ch:
			if !ok {
				cancel()
				t.Fatalf("attempt %d: resubscriber stream closed before its event", attempt)
			}
			if event.ConditionID != child.ID() {
				cancel()
				t.Fatalf("attempt %d: event condition ID = %q, want %q", attempt, event.ConditionID, child.ID())
			}
		case <-time.After(time.Second):
			cancel()
			t.Fatalf("attempt %d: resubscriber did not receive a fresh event", attempt)
		}
		second.unsubscribe()
		cancel()
	}
}

type manualCondition struct {
	id     string
	mu     sync.Mutex
	subs   []chan Event
	closed bool
}

type registryObserverCondition struct {
	id       string
	observed chan<- int
	token    *conditionToken
}

type nilCondition struct{ id string }

func (c nilCondition) ID() string { return c.id }

func (nilCondition) Run(context.Context, Mode) <-chan Event { return nil }

type pulseCondition struct {
	token   *conditionToken
	mu      sync.Mutex
	current chan struct{}
}

func newPulseCondition() *pulseCondition {
	return &pulseCondition{
		token: &conditionToken{},
	}
}

func (c *pulseCondition) ID() string { return "pulse" }

func (c *pulseCondition) conditionKey() *conditionToken { return c.token }

func (c *pulseCondition) Run(context.Context, Mode) <-chan Event {
	return make(chan Event)
}

func (c *pulseCondition) run(ctx context.Context, _ Mode, _ *runRegistry) <-chan Event {
	out := make(chan Event, 1)
	trigger := make(chan struct{}, 1)
	c.mu.Lock()
	c.current = trigger
	c.mu.Unlock()
	go func() {
		defer close(out)
		select {
		case <-trigger:
			select {
			case out <- Event{ConditionID: c.ID(), SourceIDs: []string{c.ID()}, Time: time.Unix(1, 0)}:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	return out
}

func (c *pulseCondition) pulse() {
	c.mu.Lock()
	trigger := c.current
	c.mu.Unlock()
	trigger <- struct{}{}
}

type stagedSharedCondition struct {
	child           Condition
	firstSubscriber chan *subscription
	allowSecond     chan struct{}
	token           *conditionToken
}

func newStagedSharedCondition(child Condition) *stagedSharedCondition {
	return &stagedSharedCondition{
		child:           child,
		firstSubscriber: make(chan *subscription, 1),
		allowSecond:     make(chan struct{}),
		token:           &conditionToken{},
	}
}

func (c *stagedSharedCondition) ID() string { return "staged" }

func (c *stagedSharedCondition) conditionKey() *conditionToken { return c.token }

func (c *stagedSharedCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	return runRoot(c, ctx, mode, c.run)
}

func (c *stagedSharedCondition) run(ctx context.Context, mode Mode, registry *runRegistry) <-chan Event {
	first := registry.subscribe(c.child, ctx, mode)
	c.firstSubscriber <- first
	<-c.allowSecond
	second := registry.subscribe(c.child, ctx, mode)
	out := make(chan Event, 1)
	go func() {
		defer close(out)
		firstEvent, ok := <-first.ch
		if !ok {
			return
		}
		if _, ok := <-second.ch; !ok {
			return
		}
		out <- Event{ConditionID: c.ID(), SourceIDs: eventSources(firstEvent), Time: firstEvent.Time}
	}()
	return out
}

func (c *registryObserverCondition) ID() string { return c.id }

func (c *registryObserverCondition) conditionKey() *conditionToken { return c.token }

func (c *registryObserverCondition) Run(context.Context, Mode) <-chan Event {
	return make(chan Event)
}

func (c *registryObserverCondition) run(ctx context.Context, _ Mode, registry *runRegistry) <-chan Event {
	registry.mu.Lock()
	stream := registry.streams[c.token]
	count := 0
	if stream != nil {
		stream.mu.Lock()
		count = len(stream.subs)
		stream.mu.Unlock()
	}
	registry.mu.Unlock()
	c.observed <- count
	out := make(chan Event)
	go func() {
		<-ctx.Done()
		close(out)
	}()
	return out
}

func newManualCondition(id string) *manualCondition {
	return &manualCondition{id: id}
}

func (c *manualCondition) ID() string { return c.id }

func (c *manualCondition) Run(ctx context.Context, mode Mode) <-chan Event {
	out := make(chan Event, 8)
	c.mu.Lock()
	c.subs = append(c.subs, out)
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.closed {
			c.closed = true
			for _, sub := range c.subs {
				close(sub)
			}
		}
	}()
	return out
}

func (c *manualCondition) Emit(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sub := range c.subs {
		sub <- Event{ConditionID: c.id, SourceIDs: []string{c.id}, Time: at}
	}
}

func receiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("condition channel closed before event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for condition event")
		return Event{}
	}
}

func receiveClosed(t *testing.T, events <-chan Event) (Event, bool) {
	t.Helper()
	select {
	case event, ok := <-events:
		return event, ok
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for condition channel closure")
		return Event{}, false
	}
}

func receiveDuration(t *testing.T, values <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for timer")
		return 0
	}
}

func assertEventID(t *testing.T, events <-chan Event, want string) {
	t.Helper()
	if got := receiveEvent(t, events); got.ConditionID != want {
		t.Fatalf("condition ID = %q, want %q", got.ConditionID, want)
	}
}

func assertNoEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	select {
	case event, ok := <-events:
		t.Fatalf("unexpected condition event = %#v, open = %t", event, ok)
	case <-time.After(2 * filePollInterval):
	}
}

func writeRegularFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("trigger"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
