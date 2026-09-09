//go:build windows

package cli

// This file keeps Unix-only signal values unavailable on Windows while
// allowing the portable CLI parser to build there.

import "os"

func signalSupported() bool {
	return false
}

func resolveSignal(string) (os.Signal, bool) {
	return nil, false
}
