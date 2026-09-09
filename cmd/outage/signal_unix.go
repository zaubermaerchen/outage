//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file installs the Unix SIGPIPE behavior needed by stream forwarding.

import (
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE makes closed output pipes report EPIPE to Go writes.
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
