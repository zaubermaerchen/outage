//go:build !windows && !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package main

import (
	"os"
	"testing"
)

func configureEventsTestDescriptor(t *testing.T, _ *os.File) {
	t.Skip("event descriptors are unsupported on this platform")
}

func eventCaptureFD(t *testing.T, _ *eventCapture) int {
	t.Helper()
	t.Skip("event descriptors are unsupported on this platform")
	return -1
}
