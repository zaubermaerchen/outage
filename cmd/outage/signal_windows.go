//go:build windows

package main

// This file rejects Unix-only signal conditions while keeping Windows builds valid.

import (
	"fmt"

	"github.com/zaubermaerchen/outage/internal/condition"
)

func signalEventSupported() bool {
	return false
}

func newSignalCondition(_ string, event string, _ ...condition.Option) (condition.Condition, error) {
	return nil, fmt.Errorf("unsupported event %q on this platform", event)
}

// ignoreSIGPIPE is a no-op because Windows has no SIGPIPE signal.
func ignoreSIGPIPE() {}
