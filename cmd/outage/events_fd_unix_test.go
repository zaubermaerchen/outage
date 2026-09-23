//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file verifies Unix event descriptor writes do not wait for a consumer.

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func configureEventsTestDescriptor(t *testing.T, file *os.File) {
	t.Helper()
	if err := unix.SetNonblock(rawEventFD(t, file), true); err != nil {
		t.Fatal(err)
	}
}

func eventCaptureFD(t *testing.T, capture *eventCapture) int {
	t.Helper()
	return rawEventFD(t, capture.File)
}

func newObservationPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(rawEventFD(t, writeEvents), true); err != nil {
		_ = readEvents.Close()
		_ = writeEvents.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = readEvents.Close()
		_ = writeEvents.Close()
	})
	return readEvents, writeEvents
}

func TestUnixEventDescriptorRejectsUnsupportedDescriptorsBeforeInput(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "events-")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	read, blockingWrite := func() (*os.File, *os.File) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = read.Close()
			_ = write.Close()
		})
		return read, write
	}()
	readOnly := read
	if err := unix.SetNonblock(rawEventFD(t, blockingWrite), false); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		file *os.File
	}{
		{name: "regular file", file: regular},
		{name: "blocking pipe", file: blockingWrite},
		{name: "read-only pipe", file: readOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &eventsTrackingReader{}
			var output, diagnostics bytes.Buffer
			status := run([]string{
				"--events-fd",
				strconv.Itoa(rawEventFD(t, tc.file)),
				"duration:1h",
			}, input, &output, &diagnostics)
			if status != exitArgError {
				t.Fatalf("run() status = %d, want %d; diagnostics = %q", status, exitArgError, diagnostics.String())
			}
			if input.readCalled {
				t.Fatal("invalid event descriptor processed stdin")
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", output.String())
			}
			if !strings.Contains(diagnostics.String(), "invalid --events-fd") {
				t.Fatalf("diagnostics = %q, want invalid descriptor diagnostic", diagnostics.String())
			}
		})
	}
}

func TestUnixEventDescriptorAcceptsNonblockingSocket(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
	})
	if err := unix.SetNonblock(pair[0], true); err != nil {
		t.Fatal(err)
	}
	if err := validateEventDescriptor(pair[0]); err != nil {
		t.Fatalf("nonblocking socket rejected: %v", err)
	}
}

func TestUnixEventEmitterPreservesCallerFlagsAndInheritance(t *testing.T) {
	_, writeEvents := newObservationPipe(t)
	fd := rawEventFD(t, writeEvents)
	originalStatus, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	originalDescriptor, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}

	var diagnostics bytes.Buffer
	emitter := newEventEmitter(fd, &diagnostics)
	if emitter == nil {
		t.Fatalf("newEventEmitter() returned nil: %q", diagnostics.String())
	}
	ownedDescriptor, err := unix.FcntlInt(uintptr(rawEventFD(t, emitter.file)), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ownedDescriptor&unix.FD_CLOEXEC == 0 {
		t.Fatalf("owned descriptor flags = %#x, want FD_CLOEXEC", ownedDescriptor)
	}
	emitter.emit("probe")
	emitter.close()

	status, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if comparableEventStatusFlags(status) != comparableEventStatusFlags(originalStatus) {
		t.Fatalf("caller status flags = %#x, want %#x", status, originalStatus)
	}
	if descriptor != originalDescriptor {
		t.Fatalf("caller descriptor flags = %#x, want %#x", descriptor, originalDescriptor)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func comparableEventStatusFlags(flags int) int {
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		// Darwin adds its kernel-only FWASWRITTEN marker after a successful
		// write through any descriptor sharing the open-file description.
		// It is not a caller-controlled file status flag.
		const darwinKernelWrittenFlag = 1 << 16
		return flags &^ darwinKernelWrittenFlag
	}
	return flags
}

func TestUnixEventEmitterDisablesAfterCallerModeChange(t *testing.T) {
	_, writeEvents := newObservationPipe(t)
	fd := rawEventFD(t, writeEvents)
	diagnostics := newEventDiagnostics()
	emitter := newEventEmitter(fd, diagnostics)
	if emitter == nil {
		t.Fatalf("newEventEmitter() returned nil: %q", diagnostics.String())
	}
	defer emitter.close()
	if err := unix.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	emitter.emit("ready")
	emitter.emit("shutdown")
	if got := strings.Count(waitForEventWarning(t, diagnostics), "events disabled:"); got != 1 {
		t.Fatalf("warnings = %d: %q", got, diagnostics.String())
	}
}

func TestRunEventsFDContinuesAfterBrokenConsumer(t *testing.T) {
	readEvents, writeEvents := newObservationPipe(t)
	fd := rawEventFD(t, writeEvents)
	if err := readEvents.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	diagnostics := newEventDiagnostics()
	status := run([]string{
		"--events-fd",
		strconv.Itoa(fd),
		"duration:0s",
	}, unreadableReader{}, &output, diagnostics)
	if status != exitOK {
		t.Fatalf("run() status = %d, want %d; diagnostics = %q", status, exitOK, diagnostics.String())
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if got := strings.Count(waitForEventWarning(t, diagnostics), "events disabled:"); got != 1 {
		t.Fatalf("warnings = %d: %q", got, diagnostics.String())
	}
}

func TestRunEventsFDDoesNotBlockOnDiagnosticsWriter(t *testing.T) {
	readEvents, writeEvents := newObservationPipe(t)
	defer readEvents.Close()
	defer writeEvents.Close()
	fd := rawEventFD(t, writeEvents)
	filler, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(filler)
	buffer := make([]byte, 4096)
	for {
		if _, err := unix.Write(filler, buffer); err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				break
			}
			t.Fatal(err)
		}
	}

	diagnostics := &blockingDiagnosticsWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	var output bytes.Buffer
	status := make(chan int, 1)
	go func() {
		status <- run([]string{
			"--events-fd",
			strconv.Itoa(fd),
			"duration:0s",
		}, unreadableReader{}, &output, diagnostics)
	}()

	select {
	case <-diagnostics.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("event failure did not attempt diagnostic delivery")
	}
	select {
	case got := <-status:
		if got != exitOK {
			t.Fatalf("run() status = %d, want %d", got, exitOK)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("primary processing waited for a blocked diagnostic writer")
	}
	close(diagnostics.release)
	select {
	case <-diagnostics.done:
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic writer did not finish after release")
	}
}

type blockingDiagnosticsWriter struct {
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (writer *blockingDiagnosticsWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.entered) })
	<-writer.release
	close(writer.done)
	return len(data), nil
}

func rawEventFD(t *testing.T, file *os.File) int {
	t.Helper()
	connection, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var fd uintptr
	if err := connection.Control(func(raw uintptr) { fd = raw }); err != nil {
		t.Fatal(err)
	}
	return int(fd)
}

func TestRunEventsFDDoesNotWaitForFullConsumer(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	writeFD := rawEventFD(t, writeEvents)
	if err := unix.SetNonblock(writeFD, true); err != nil {
		t.Fatal(err)
	}
	originalFlags, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	originalDescriptor, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	filler, err := unix.Dup(writeFD)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(filler)
	if err := unix.SetNonblock(filler, true); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	for {
		if _, err := unix.Write(filler, buffer); err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				break
			}
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	diagnostics := newEventDiagnostics()
	status := make(chan int, 1)
	go func() {
		status <- run([]string{
			"--events-fd",
			strconv.Itoa(writeFD),
			"duration:0s",
		}, unreadableReader{}, &output, diagnostics)
	}()
	select {
	case got := <-status:
		if got != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", got, exitOK, diagnostics.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("run waited for an event consumer that could not accept a write")
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if got := strings.Count(waitForEventWarning(t, diagnostics), "events disabled:"); got != 1 {
		t.Fatalf("diagnostics = %q, want one events warning", diagnostics.String())
	}
	flags, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.O_NONBLOCK != originalFlags&unix.O_NONBLOCK {
		t.Fatalf("caller descriptor blocking mode = %#x, want %#x", flags&unix.O_NONBLOCK, originalFlags&unix.O_NONBLOCK)
	}
	descriptor, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor != originalDescriptor {
		t.Fatalf("caller descriptor flags = %#x, want %#x", descriptor, originalDescriptor)
	}
}
