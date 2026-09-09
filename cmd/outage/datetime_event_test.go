package main

// This file verifies deadline monitoring using already-typed CLI values.

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunDatetimeEventUsesStartupLocalTimeAndArmsBeforeReading(t *testing.T) {
	location := time.FixedZone("test", 9*60*60)
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	timerArmed := make(chan time.Duration, 1)
	timerFired := make(chan time.Time, 1)
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			timerArmed <- delay
			return timerFired, func() {}
		},
	}
	input := &datetimeFirstReadGate{started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	var output bytes.Buffer
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runWithClock([]string{"datetime:2026-09-03T18:00"}, input, &output, &diagnostics, clock)
	}()

	select {
	case got := <-timerArmed:
		if got != time.Second {
			t.Fatalf("timer delay = %v, want 1s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("datetime monitor did not arm before input read")
	}
	select {
	case <-input.started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not attempt the first read")
	}

	if got := output.String(); got != "" {
		t.Fatalf("output = %q, want empty while first read is blocked", got)
	}
	timerFired <- now
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatalf("run status = %d, diagnostics = %q", code, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after datetime timer")
	}
	close(input.release)
	select {
	case <-input.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for blocked reader cleanup")
	}
}

func TestRunDatetimeEventEqualOrPastDoesNotReadStdin(t *testing.T) {
	location := time.FixedZone("test", -5*60*60)
	now := time.Date(2026, time.September, 3, 18, 0, 1, 0, location)
	for _, value := range []string{"datetime:2026-09-03T18:00", "datetime:2026-09-03T18:00:01"} {
		t.Run(value, func(t *testing.T) {
			timerCreated := false
			clock := runtimeClock{
				now:      func() time.Time { return now },
				location: location,
				newTimer: func(time.Duration) (<-chan time.Time, func()) {
					timerCreated = true
					return make(chan time.Time), func() {}
				},
			}
			var output, diagnostics bytes.Buffer
			status := runWithClock([]string{value}, unreadableReader{}, &output, &diagnostics, clock)
			if status != exitOK {
				t.Fatalf("run status = %d, diagnostics = %q", status, diagnostics.String())
			}
			if timerCreated {
				t.Fatal("immediately satisfied datetime unexpectedly created a timer")
			}
		})
	}
}

func TestRunDatetimeEventForwardsAndExitsWithoutEOF(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	timerFired := make(chan time.Time, 1)
	stopped := make(chan struct{})
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(time.Duration) (<-chan time.Time, func()) {
			return timerFired, func() { close(stopped) }
		},
	}
	reader := &fileEventReader{
		payload: []byte("input before datetime"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runWithClock([]string{"datetime:2026-09-03T18:00:00"}, reader, writer, &diagnostics, clock)
	}()

	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for datetime-event input reader to start")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input to be forwarded")
	}
	timerFired <- now
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d; diagnostics = %q", code, exitOK, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for datetime event")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("datetime timer was not cleaned up")
	}
	if got := writer.output.String(); got != "input before datetime" {
		t.Fatalf("stdout = %q, want %q", got, "input before datetime")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
	close(reader.release)
	select {
	case <-reader.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for blocked reader cleanup")
	}
}

func TestRunDatetimeEventStopsTimerWhenInputCompletes(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	stopped := make(chan struct{})
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() { close(stopped) }
		},
	}
	var output, diagnostics bytes.Buffer
	status := runWithClock([]string{"datetime:2026-09-03T18:00:00"}, strings.NewReader("input"), &output, &diagnostics, clock)
	if status != exitOK {
		t.Fatalf("run status = %d, diagnostics = %q", status, diagnostics.String())
	}
	if output.String() != "input" {
		t.Fatalf("output = %q, want %q", output.String(), "input")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("datetime timer was not cleaned up after input completion")
	}
}

func TestRunRejectsInvalidDatetimeWithoutReadingStdin(t *testing.T) {
	for _, value := range []string{
		"",
		"2026-09-03 18:00",
		"2026-09-03t18:00",
		"2026-09-03T24:00",
		"2026-02-29T18:00",
		"2026-09-03T18:00Z",
		"2026-09-03T18:00+00:00",
		"2026-09-03T18:00:00.1",
	} {
		t.Run(value, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			status := run([]string{"datetime:" + value}, unreadableReader{}, &output, &diagnostics)
			if status != exitArgError {
				t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitArgError, diagnostics.String())
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want empty", output.String())
			}
			if !strings.Contains(diagnostics.String(), "invalid datetime") {
				t.Fatalf("diagnostics = %q, want invalid-datetime diagnostic", diagnostics.String())
			}
		})
	}
}

type datetimeFirstReadGate struct {
	started   chan struct{}
	release   chan struct{}
	done      chan struct{}
	delivered bool
}

func (r *datetimeFirstReadGate) Read(p []byte) (int, error) {
	if !r.delivered {
		r.delivered = true
		close(r.started)
		<-r.release
		return copy(p, []byte("deadline-opened")), nil
	}
	close(r.done)
	return 0, io.EOF
}
