//go:build windows

package main

// This file provides the Windows-specific file event fixtures.

import "testing"

func setupFIFO(t *testing.T, _ string) {
	t.Helper()
	t.Skip("FIFO is unsupported on Windows")
}
