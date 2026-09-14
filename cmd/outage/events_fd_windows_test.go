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

func TestRunEventsFDDoesNotWaitForFullWindowsConsumer(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	writeHandle := windows.Handle(writeEvents.Fd())
	originalMode := windowsEventPipeMode(t, writeHandle)
	fillWindowsEventPipe(t, writeHandle, originalMode)
	var setupDiagnostics bytes.Buffer
	emitter := newEventEmitter(int(writeEvents.Fd()), &setupDiagnostics)
	if emitter == nil {
		t.Fatalf("event emitter setup failed before write-path test: %q", setupDiagnostics.String())
	}
	emitter.emit("probe")
	if got := strings.Count(setupDiagnostics.String(), "events disabled:"); got != 1 {
		t.Fatalf("probe diagnostics = %q, want one write-path failure", setupDiagnostics.String())
	}
	for _, setupFailure := range []string{"protect file descriptor", "duplicate file descriptor"} {
		if strings.Contains(setupDiagnostics.String(), setupFailure) {
			t.Fatalf("probe diagnostics = %q, want write-path failure after successful setup", setupDiagnostics.String())
		}
	}
	emitter.close()

	var output, diagnostics bytes.Buffer
	status := make(chan int, 1)
	go func() {
		status <- run([]string{
			"--events-fd",
			strconv.FormatUint(uint64(writeEvents.Fd()), 10),
			"duration:0s",
		}, unreadableReader{}, &output, &diagnostics)
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
	if got := strings.Count(diagnostics.String(), "events disabled:"); got != 1 {
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
	defer func() {
		if err := windows.SetNamedPipeHandleState(handle, &originalMode, nil, nil); err != nil {
			t.Errorf("restore pipe mode %#x: %v", originalMode, err)
		}
	}()

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
