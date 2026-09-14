//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file verifies Unix event descriptor writes do not wait for a consumer.

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunEventsFDDoesNotWaitForFullConsumer(t *testing.T) {
	readEvents, writeEvents, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEvents.Close()
	defer writeEvents.Close()

	writeFD := int(writeEvents.Fd())
	originalFlags, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFL, 0)
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
	if _, err := unix.FcntlInt(uintptr(writeFD), unix.F_SETFL, originalFlags); err != nil {
		t.Fatal(err)
	}

	var output, diagnostics bytes.Buffer
	status := make(chan int, 1)
	go func() {
		status <- run([]string{
			"--events-fd",
			strconv.Itoa(writeFD),
			"duration:0s",
		}, unreadableReader{}, &output, &diagnostics)
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
	if got := strings.Count(diagnostics.String(), "events disabled:"); got != 1 {
		t.Fatalf("diagnostics = %q, want one events warning", diagnostics.String())
	}
	flags, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.O_NONBLOCK != originalFlags&unix.O_NONBLOCK {
		t.Fatalf("caller descriptor blocking mode = %#x, want %#x", flags&unix.O_NONBLOCK, originalFlags&unix.O_NONBLOCK)
	}
}
