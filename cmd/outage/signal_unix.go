//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file installs the Unix USR1 or USR2 notification used to interrupt copying.

import (
	"os"
	"os/signal"
	"syscall"
)

func signalEventSupported() bool {
	return true
}

// ignoreSIGPIPE makes closed output pipes report EPIPE to Go writes.
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}

func installSignalMonitor(event string) (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	ignoreSIGPIPE()
	switch event {
	case "signal:USR1", "signal:SIGUSR1":
		signal.Notify(signals, syscall.SIGUSR1)
	case "signal:USR2", "signal:SIGUSR2":
		signal.Notify(signals, syscall.SIGUSR2)
	}
	return signals, func() {
		signal.Stop(signals)
	}
}
