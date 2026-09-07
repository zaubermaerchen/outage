//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file resolves Unix USR1 and USR2 values for typed condition monitors.

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/zaubermaerchen/outage/internal/condition"
)

func signalEventSupported() bool {
	return true
}

func newSignalCondition(id, event string, options ...condition.Option) (condition.Condition, error) {
	switch event {
	case "signal:USR1", "signal:SIGUSR1":
		return condition.NewSignal(id, syscall.SIGUSR1, options...), nil
	case "signal:USR2", "signal:SIGUSR2":
		return condition.NewSignal(id, syscall.SIGUSR2, options...), nil
	default:
		return nil, fmt.Errorf("unsupported event %q", event)
	}
}

// ignoreSIGPIPE makes closed output pipes report EPIPE to Go writes.
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
