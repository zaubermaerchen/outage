//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package condition

// This file verifies repeated Unix signal delivery and cancellation cleanup.

import (
	"context"
	"os"
	"syscall"
	"testing"
)

func TestSignalContinuousEmitsEveryNotificationAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := NewSignal("signal", syscall.SIGUSR1).Run(ctx, Continuous)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	if err := process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if event := receiveEvent(t, events); event.ConditionID != "signal" {
		t.Fatalf("first event condition ID = %q, want signal", event.ConditionID)
	}
	if err := process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if event := receiveEvent(t, events); event.ConditionID != "signal" {
		t.Fatalf("second event condition ID = %q, want signal", event.ConditionID)
	}

	cancel()
	if _, ok := receiveClosed(t, events); ok {
		t.Fatal("signal condition did not close after cancellation")
	}
}
