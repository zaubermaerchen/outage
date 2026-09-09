//go:build windows

package main

// This file keeps the SIGPIPE hook portable on Windows.

// ignoreSIGPIPE is a no-op because Windows has no SIGPIPE signal.
func ignoreSIGPIPE() {}
