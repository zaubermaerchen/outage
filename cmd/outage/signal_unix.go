//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file installs the Unix USR1 notification used to interrupt copying.

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

func installSignalMonitor() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	ignoreSIGPIPE()
	signal.Notify(signals, syscall.SIGUSR1)
	return signals, func() {
		signal.Stop(signals)
	}
}
