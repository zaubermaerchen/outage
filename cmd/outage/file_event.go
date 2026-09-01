package main

// This file polls file event paths until one exists or the monitor is stopped.

import (
	"os"
	"sync"
	"time"
)

const filePollInterval = 100 * time.Millisecond

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
				if _, err := os.Lstat(path); err == nil {
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
