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
	"testing"
	"time"

	"github.com/zaubermaerchen/outage/internal/condition"
)

type testLifecycleEvent struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
}

func TestRunEventsFDEmitsConditionAndCutoffEventsForInitialCondition(t *testing.T) {
	eventsFile := newEventsFile(t)
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd=" + strconv.FormatUint(uint64(eventsFile.Fd()), 10),
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
			"--events-fd", strconv.FormatUint(uint64(eventsFile.Fd()), 10),
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
		strconv.FormatUint(uint64(eventsFile.Fd()), 10),
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
		strconv.FormatUint(uint64(eventsFile.Fd()), 10),
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
			emitter := newEventEmitter(int(eventsFile.Fd()), &diagnostics)
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
			emitter := newEventEmitter(int(eventsFile.Fd()), &diagnostics)
			if emitter == nil {
				t.Fatal("newEventEmitter returned nil")
			}
			t.Cleanup(emitter.close)

			copyDone := make(chan error, 1)
			copyDone <- errors.New("read failed")

			if got := handleConditionEvent(!eventClosed, copyDone, emitter, &diagnostics); got != exitCopyError {
				t.Fatalf("handleConditionEvent status = %d, want %d; diagnostics = %q", got, exitCopyError, diagnostics.String())
			}
			if records := readLifecycleEvents(t, eventsFile); len(records) != 0 {
				t.Fatalf("events = %#v, want no events after copy completion became ready", records)
			}
		})
	}
}

func TestRunEventsFDWriteSetupFailureWarnsOnceAndPreservesStream(t *testing.T) {
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd",
		strconv.Itoa(int(^uint(0) >> 1)),
		"duration:1h",
	}, strings.NewReader("input"), &output, &diagnostics)
	if status != exitOK {
		t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitOK, diagnostics.String())
	}
	if got, want := output.String(), "input"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := strings.Count(diagnostics.String(), "events disabled:"); got != 1 {
		t.Fatalf("diagnostics = %q, want one events warning", diagnostics.String())
	}
}

func TestEventEmitterDoesNotCloseCallerFD(t *testing.T) {
	eventsFile := newEventsFile(t)
	var diagnostics bytes.Buffer
	emitter := newEventEmitter(int(eventsFile.Fd()), &diagnostics)
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

func newEventsFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "outage-events-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readLifecycleEvents(t *testing.T, file *os.File) []testLifecycleEvent {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(file)
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
