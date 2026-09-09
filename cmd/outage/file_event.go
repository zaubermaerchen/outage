package main

// This file provides the CLI-side regular-file classification used for setup checks.

import (
	"os"
	"time"
)

const filePollInterval = 100 * time.Millisecond

func isRegularFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
