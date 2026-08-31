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

func installSignalMonitor() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	// Catch SIGPIPE so a closed stdout pipe is reported by os.File.Write as EPIPE
	// instead of terminating the process before the copy can report the error.
	signal.Ignore(syscall.SIGPIPE)
	signal.Notify(signals, syscall.SIGUSR1)
	return signals, func() {
		signal.Stop(signals)
	}
}
