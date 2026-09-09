//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package cli

// This file resolves outage's supported Unix signal names into os.Signal
// values before they reach the condition monitoring package.

import (
	"os"
	"syscall"
)

func signalSupported() bool {
	return true
}

func resolveSignal(name string) (os.Signal, bool) {
	switch name {
	case "USR1":
		return syscall.SIGUSR1, true
	case "USR2":
		return syscall.SIGUSR2, true
	default:
		return nil, false
	}
}
