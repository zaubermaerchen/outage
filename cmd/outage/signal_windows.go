//go:build windows

package main

// This file makes the Unix-only event unavailable while keeping Windows builds valid.

import "os"

func signalEventSupported() bool {
	return false
}

func installSignalMonitor() (<-chan os.Signal, func()) {
	return nil, func() {}
}
