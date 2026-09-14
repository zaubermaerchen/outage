package main

// This file emits the optional machine-readable condition lifecycle stream.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type lifecycleEventRecord struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
}

type eventEmitter struct {
	file        *os.File
	diagnostics io.Writer
	now         func() time.Time

	mu       sync.Mutex
	disabled bool
	closed   bool
}

func newEventEmitter(fd int, diagnostics io.Writer) *eventEmitter {
	return newEventEmitterWithClock(fd, diagnostics, time.Now)
}

func newEventEmitterWithClock(fd int, diagnostics io.Writer, now func() time.Time) *eventEmitter {
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	if fd < 3 {
		writeDiagnostic(diagnostics, fmt.Errorf("events disabled: invalid file descriptor %d", fd))
		return nil
	}
	if err := setEventDescriptorNonInheritable(fd); err != nil {
		writeDiagnostic(diagnostics, fmt.Errorf("events disabled: protect file descriptor %d: %w", fd, err))
		return nil
	}
	file, err := duplicateEventFile(fd)
	if err != nil {
		writeDiagnostic(diagnostics, fmt.Errorf("events disabled: duplicate file descriptor %d: %w", fd, err))
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &eventEmitter{file: file, diagnostics: diagnostics, now: now}
}

func (emitter *eventEmitter) emit(event string) {
	if emitter == nil {
		return
	}
	emitter.emitRecord(lifecycleEventRecord{
		Event:     event,
		Timestamp: emitter.now().UTC().Format(time.RFC3339Nano),
	})
}

func (emitter *eventEmitter) emitRecord(record lifecycleEventRecord) {
	if emitter == nil {
		return
	}

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.disabled || emitter.closed {
		return
	}

	line, err := json.Marshal(record)
	if err == nil {
		line = append(line, '\n')
		var written int
		written, err = writeEvent(emitter.file, line)
		if err == nil && written != len(line) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		emitter.disabled = true
		writeDiagnostic(emitter.diagnostics, fmt.Errorf("events disabled: %w", err))
	}
}

func (emitter *eventEmitter) close() {
	if emitter == nil {
		return
	}
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.closed {
		return
	}
	emitter.closed = true
	_ = emitter.file.Close()
}

func emitCutoffEvents(emitter *eventEmitter) {
	if emitter == nil {
		return
	}
	emitter.emit("condition-triggered")
	emitter.emit("stream-cutoff")
}
