package main

// This file polls file event paths until one is a regular file or the monitor is stopped.

import (
	"os"
	"sync"
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

func installFileMonitor(path string) (<-chan os.Signal, func()) {
	events := make(chan os.Signal, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(done)

		ticker := time.NewTicker(filePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if regular, _ := isRegularFile(path); regular {
					events <- nil
					return
				}
			case <-stop:
				return
			}
		}
	}()

	return events, func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}
}
