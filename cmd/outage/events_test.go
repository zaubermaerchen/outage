package main

// This file verifies optional machine-readable condition lifecycle events.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaubermaerchen/outage/internal/condition"
)

type testLifecycleEvent struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
}

type eventCapture struct {
	*os.File
	read *os.File
}

type eventsTrackingReader struct {
	readCalled bool
}

func (reader *eventsTrackingReader) Read([]byte) (int, error) {
	reader.readCalled = true
	return 0, io.EOF
}

type eventDiagnostics struct {
	mu      sync.Mutex
	data    bytes.Buffer
	entered chan struct{}
	once    sync.Once
}

func newEventDiagnostics() *eventDiagnostics {
	return &eventDiagnostics{entered: make(chan struct{})}
}

func (diagnostics *eventDiagnostics) Write(data []byte) (int, error) {
	diagnostics.mu.Lock()
	n, err := diagnostics.data.Write(data)
	diagnostics.mu.Unlock()
	diagnostics.once.Do(func() { close(diagnostics.entered) })
	return n, err
}

func (diagnostics *eventDiagnostics) String() string {
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	return diagnostics.data.String()
}

func waitForEventWarning(t *testing.T, diagnostics *eventDiagnostics) string {
	t.Helper()
	select {
	case <-diagnostics.entered:
		return diagnostics.String()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event warning")
		return ""
	}
}

func TestRunEventsFDEmitsConditionAndCutoffEventsForInitialCondition(t *testing.T) {
	eventsFile := newEventsFile(t)
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd=" + strconv.Itoa(eventCaptureFD(t, eventsFile)),
		"duration:0s",
	}, unreadableReader{}, &output, &diagnostics)
	if status != exitOK {
		t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitOK, diagnostics.String())
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", diagnostics.String())
	}

	records := readLifecycleEvents(t, eventsFile)
	assertLifecycleEvents(t, records)
}

func TestRunEventsFDEmitsConditionAndCutoffEventsForMonitoredCondition(t *testing.T) {
	eventsFile := newEventsFile(t)
	start := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	timerFired := make(chan time.Time, 1)
	clock := runtimeClock{
		now:      func() time.Time { return start },
		location: time.UTC,
		newTimer: func(time.Duration) (<-chan time.Time, func()) { return timerFired, func() {} },
	}
	reader := &eventsBlockingReader{started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	var output, diagnostics bytes.Buffer
	status := make(chan int, 1)
	go func() {
		status <- runWithClock([]string{
			"--events-fd", strconv.Itoa(eventCaptureFD(t, eventsFile)),
			"duration:1s",
		}, reader, &output, &diagnostics, clock)
	}()

	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for monitored input read")
	}
	timerFired <- start.Add(time.Second)
	select {
	case got := <-status:
		if got != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", got, exitOK, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for monitored condition")
	}
	close(reader.release)
	select {
	case <-reader.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input cleanup")
	}

	assertLifecycleEvents(t, readLifecycleEvents(t, eventsFile))
}

func TestRunEventsFDDoesNotEmitForInputEOF(t *testing.T) {
	eventsFile := newEventsFile(t)
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd",
		strconv.Itoa(eventCaptureFD(t, eventsFile)),
		"duration:1h",
	}, strings.NewReader("input"), &output, &diagnostics)
	if status != exitOK {
		t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitOK, diagnostics.String())
	}
	if got, want := output.String(), "input"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if records := readLifecycleEvents(t, eventsFile); len(records) != 0 {
		t.Fatalf("events = %#v, want no events for input EOF", records)
	}
}

func TestRunEventsFDDoesNotEmitForCopyError(t *testing.T) {
	eventsFile := newEventsFile(t)
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd",
		strconv.Itoa(eventCaptureFD(t, eventsFile)),
		"duration:1h",
	}, failingReader{err: errors.New("read failed")}, &output, &diagnostics)
	if status != exitCopyError {
		t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitCopyError, diagnostics.String())
	}
	if records := readLifecycleEvents(t, eventsFile); len(records) != 0 {
		t.Fatalf("events = %#v, want no events for copy error", records)
	}
}

func TestWaitForCopyOrConditionPrioritizesReadyCopyCompletion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		copyErr    error
		wantStatus int
	}{
		{name: "EOF", wantStatus: exitOK},
		{name: "copy error", copyErr: errors.New("read failed"), wantStatus: exitCopyError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eventsFile := newEventsFile(t)
			var diagnostics bytes.Buffer
			emitter := newEventEmitter(eventCaptureFD(t, eventsFile), &diagnostics)
			if emitter == nil {
				t.Fatal("newEventEmitter returned nil")
			}
			t.Cleanup(emitter.close)

			events := make(chan condition.Event, 1)
			events <- condition.Event{ConditionID: "duration"}
			copyDone := make(chan error, 1)
			copyDone <- tc.copyErr

			if got := waitForCopyOrCondition(events, copyDone, emitter, &diagnostics); got != tc.wantStatus {
				t.Fatalf("waitForCopyOrCondition status = %d, want %d; diagnostics = %q", got, tc.wantStatus, diagnostics.String())
			}
			emitter.close()
			if records := readLifecycleEvents(t, eventsFile); len(records) != 0 {
				t.Fatalf("events = %#v, want no events when copy completion was already ready", records)
			}
			if tc.copyErr != nil && !strings.Contains(diagnostics.String(), tc.copyErr.Error()) {
				t.Fatalf("diagnostics = %q, want %q", diagnostics.String(), tc.copyErr)
			}
		})
	}
}

func TestHandleConditionEventRechecksCopyBeforeCutoff(t *testing.T) {
	for _, eventClosed := range []bool{false, true} {
		t.Run(map[bool]string{false: "event", true: "closed event"}[eventClosed], func(t *testing.T) {
			eventsFile := newEventsFile(t)
			var diagnostics bytes.Buffer
			emitter := newEventEmitter(eventCaptureFD(t, eventsFile), &diagnostics)
			if emitter == nil {
				t.Fatal("newEventEmitter returned nil")
			}
			t.Cleanup(emitter.close)

			copyDone := make(chan error, 1)
			copyDone <- errors.New("read failed")

			if got := handleConditionEvent(!eventClosed, copyDone, emitter, &diagnostics); got != exitCopyError {
				t.Fatalf("handleConditionEvent status = %d, want %d; diagnostics = %q", got, exitCopyError, diagnostics.String())
			}
			emitter.close()
			if records := readLifecycleEvents(t, eventsFile); len(records) != 0 {
				t.Fatalf("events = %#v, want no events after copy completion became ready", records)
			}
		})
	}
}

func TestRunEventsFDRejectsInvalidDescriptorBeforeStream(t *testing.T) {
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd",
		strconv.Itoa(int(^uint(0) >> 1)),
		"duration:1h",
	}, strings.NewReader("input"), &output, &diagnostics)
	if status != exitArgError {
		t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitArgError, diagnostics.String())
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if got := strings.Count(diagnostics.String(), "invalid --events-fd"); got != 1 {
		t.Fatalf("diagnostics = %q, want one invalid descriptor diagnostic", diagnostics.String())
	}
}

func TestEventEmitterDoesNotCloseCallerFD(t *testing.T) {
	eventsFile := newEventsFile(t)
	var diagnostics bytes.Buffer
	emitter := newEventEmitter(eventCaptureFD(t, eventsFile), &diagnostics)
	if emitter == nil {
		t.Fatal("newEventEmitter returned nil")
	}
	emitter.emit("condition-triggered")
	emitter.close()
	if _, err := eventsFile.Stat(); err != nil {
		t.Fatalf("closing emitter closed caller descriptor: %v", err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func newEventsFile(t *testing.T) *eventCapture {
	t.Helper()
	read, file, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	configureEventsTestDescriptor(t, file)
	capture := &eventCapture{File: file, read: read}
	t.Cleanup(func() {
		_ = capture.Close()
		_ = capture.read.Close()
	})
	return capture
}

func readLifecycleEvents(t *testing.T, capture *eventCapture) []testLifecycleEvent {
	t.Helper()
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(capture.read)
	var records []testLifecycleEvent
	for {
		var fields map[string]json.RawMessage
		err := decoder.Decode(&fields)
		if err == io.EOF {
			return records
		}
		if err != nil {
			t.Fatalf("decode lifecycle event: %v", err)
		}
		if len(fields) != 2 {
			t.Fatalf("lifecycle event fields = %#v, want only event and timestamp", fields)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal lifecycle event fields: %v", err)
		}
		var record testLifecycleEvent
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatalf("decode lifecycle event fields: %v", err)
		}
		records = append(records, record)
	}
}

func assertLifecycleEvents(t *testing.T, records []testLifecycleEvent) {
	t.Helper()
	want := []string{"condition-triggered", "stream-cutoff"}
	if len(records) != len(want) {
		t.Fatalf("event count = %d, want %d: %#v", len(records), len(want), records)
	}
	for index, wantEvent := range want {
		if got := records[index].Event; got != wantEvent {
			t.Errorf("event %d = %q, want %q", index, got, wantEvent)
		}
		if _, err := time.Parse(time.RFC3339Nano, records[index].Timestamp); err != nil {
			t.Errorf("event %d timestamp = %q: %v", index, records[index].Timestamp, err)
		}
	}
}

type eventsBlockingReader struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (reader *eventsBlockingReader) Read([]byte) (int, error) {
	close(reader.started)
	<-reader.release
	close(reader.done)
	return 0, io.EOF
}
