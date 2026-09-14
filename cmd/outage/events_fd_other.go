//go:build !windows && !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package main

// This file disables event streams on platforms without descriptor support.

import (
	"errors"
	"os"
)

var errEventFDUnsupported = errors.New("event file descriptors are unsupported on this platform")

func setEventDescriptorNonInheritable(int) error {
	return errEventFDUnsupported
}

func duplicateEventFile(int) (*os.File, error) {
	return nil, errEventFDUnsupported
}

func writeEvent(*os.File, []byte) (int, error) {
	return 0, errEventFDUnsupported
}
