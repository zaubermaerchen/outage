//go:build windows

package main

// This file verifies Windows event pipe writes do not wait for a consumer.

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func configureEventsTestDescriptor(t *testing.T, file *os.File) {
	t.Helper()
	handle := windows.Handle(file.Fd())
	mode := windowsEventPipeMode(t, handle)
	nowaitMode := mode | windows.PIPE_NOWAIT
	if err := windows.SetNamedPipeHandleState(handle, &nowaitMode, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func eventCaptureFD(t *testing.T, capture *eventCapture) int {
	t.Helper()
	return int(capture.File.Fd())
}

func TestRunEventsFDDoesNotWaitForFullWindowsConsumer(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	writeHandle := windows.Handle(writeEvents.Fd())
	originalMode := windowsEventPipeMode(t, writeHandle)
	nowaitMode := originalMode | windows.PIPE_NOWAIT
	if err := windows.SetNamedPipeHandleState(writeHandle, &nowaitMode, nil, nil); err != nil {
		t.Fatal(err)
	}
	originalMode = nowaitMode
	fillWindowsEventPipe(t, writeHandle, originalMode)
	setupDiagnostics := newEventDiagnostics()
	emitter := newEventEmitter(int(writeEvents.Fd()), setupDiagnostics)
	if emitter == nil {
		t.Fatalf("event emitter setup failed before write-path test: %q", setupDiagnostics.String())
	}
	emitter.emit("probe")
	if got := strings.Count(waitForEventWarning(t, setupDiagnostics), "events disabled:"); got != 1 {
		t.Fatalf("probe diagnostics = %q, want one write-path failure", setupDiagnostics.String())
	}
	for _, setupFailure := range []string{"protect file descriptor", "duplicate file descriptor"} {
		if strings.Contains(setupDiagnostics.String(), setupFailure) {
			t.Fatalf("probe diagnostics = %q, want write-path failure after successful setup", setupDiagnostics.String())
		}
	}
	emitter.close()

	var output bytes.Buffer
	diagnostics := newEventDiagnostics()
	status := make(chan int, 1)
	go func() {
		status <- run([]string{
			"--events-fd",
			strconv.Itoa(int(writeEvents.Fd())),
			"duration:0s",
		}, unreadableReader{}, &output, diagnostics)
	}()

	select {
	case got := <-status:
		if got != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", got, exitOK, diagnostics.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run waited for an event consumer that could not accept a write")
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if got := strings.Count(waitForEventWarning(t, diagnostics), "events disabled:"); got != 1 {
		t.Fatalf("diagnostics = %q, want one events warning", diagnostics.String())
	}
	for _, setupFailure := range []string{"protect file descriptor", "duplicate file descriptor"} {
		if strings.Contains(diagnostics.String(), setupFailure) {
			t.Fatalf("diagnostics = %q, want write-path failure after successful setup", diagnostics.String())
		}
	}
	if got := windowsEventPipeMode(t, writeHandle); got != originalMode {
		t.Fatalf("event pipe mode = %#x, want %#x", got, originalMode)
	}
}

func TestWindowsEventDescriptorRejectsBlockingPipeBeforeInput(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	handle := windows.Handle(writeEvents.Fd())
	mode := windowsEventPipeMode(t, handle) &^ windows.PIPE_NOWAIT
	if err := windows.SetNamedPipeHandleState(handle, &mode, nil, nil); err != nil {
		t.Fatal(err)
	}
	input := &eventsTrackingReader{}
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd",
		strconv.Itoa(int(writeEvents.Fd())),
		"duration:1h",
	}, input, &output, &diagnostics)
	if status != exitArgError {
		t.Fatalf("run() status = %d, want %d; diagnostics = %q", status, exitArgError, diagnostics.String())
	}
	if input.readCalled || output.Len() != 0 {
		t.Fatal("invalid event descriptor processed the primary stream")
	}
	if !strings.Contains(diagnostics.String(), "invalid --events-fd") {
		t.Fatalf("diagnostics = %q, want invalid descriptor diagnostic", diagnostics.String())
	}
}

func TestWindowsEventDescriptorChecksPipeWriteAccess(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	if err := checkWindowsPipeWritable(windows.Handle(readEvents.Fd())); err == nil {
		t.Fatal("read-only pipe unexpectedly passed write-access check")
	}
	if err := checkWindowsPipeWritable(windows.Handle(writeEvents.Fd())); err != nil {
		t.Fatalf("write pipe failed write-access check: %v", err)
	}
}

func TestWindowsEventDescriptorRejectsReadOnlyNowaitPipeBeforeInput(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	readOnlyHandle := windows.Handle(readEvents.Fd())
	var mode uint32
	if err := windows.GetNamedPipeHandleState(readOnlyHandle, &mode, nil, nil, nil, nil, 0); err != nil {
		t.Skipf("cannot inspect read-only pipe mode: %v", err)
	}
	mode |= windows.PIPE_NOWAIT
	if err := windows.SetNamedPipeHandleState(readOnlyHandle, &mode, nil, nil); err != nil {
		t.Skipf("cannot configure read-only pipe as PIPE_NOWAIT: %v", err)
	}

	input := &eventsTrackingReader{}
	var output, diagnostics bytes.Buffer
	status := run([]string{
		"--events-fd",
		strconv.Itoa(int(readEvents.Fd())),
		"duration:1h",
	}, input, &output, &diagnostics)
	if status != exitArgError {
		t.Fatalf("run() status = %d, want %d; diagnostics = %q", status, exitArgError, diagnostics.String())
	}
	if input.readCalled || output.Len() != 0 {
		t.Fatal("read-only event descriptor processed the primary stream")
	}
	if !strings.Contains(diagnostics.String(), "invalid --events-fd") {
		t.Fatalf("diagnostics = %q, want invalid descriptor diagnostic", diagnostics.String())
	}
}

func windowsEventPipeMode(t *testing.T, handle windows.Handle) uint32 {
	t.Helper()
	var mode uint32
	if err := windows.GetNamedPipeHandleState(handle, &mode, nil, nil, nil, nil, 0); err != nil {
		t.Fatalf("GetNamedPipeHandleState(%v): %v", handle, err)
	}
	return mode
}

func fillWindowsEventPipe(t *testing.T, handle windows.Handle, originalMode uint32) {
	t.Helper()
	nowaitMode := originalMode | windows.PIPE_NOWAIT
	if err := windows.SetNamedPipeHandleState(handle, &nowaitMode, nil, nil); err != nil {
		t.Fatalf("SetNamedPipeHandleState(%v, PIPE_NOWAIT): %v", handle, err)
	}

	buffer := make([]byte, 4096)
	var total uint32
	for total < 16<<20 {
		var written uint32
		err := windows.WriteFile(handle, buffer, &written, nil)
		total += written
		if err != nil {
			return
		}
		if written == 0 {
			// PIPE_NOWAIT reports a full anonymous pipe as a successful write
			// with zero bytes written on some Windows versions.
			return
		}
	}
	t.Fatalf("filled %d bytes without making event pipe unavailable", total)
}
