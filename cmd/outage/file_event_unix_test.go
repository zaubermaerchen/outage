//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file provides Unix-specific file event fixtures.

import (
	"syscall"
	"testing"
)

func setupFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("FIFO creation unavailable: %v", err)
	}
}
