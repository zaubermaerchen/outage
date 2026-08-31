//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

// This file verifies Unix signal interruption and pipe behavior.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (r *blockingReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	close(r.done)
	return 0, io.EOF
}

type blockingWriter struct {
	started     chan struct{}
	release     chan struct{}
	done        chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	doneOnce    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.startedOnce.Do(func() { close(w.started) })
	<-w.release
	w.doneOnce.Do(func() { close(w.done) })
	return len(p), nil
}

func (w *blockingWriter) Release() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func sendUSR1(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
}

func waitForResult(t *testing.T, result <-chan int) int {
	t.Helper()
	select {
	case code := <-result:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for outage to exit")
		return -1
	}
}

func TestRunExitsOnUSR1WhileInputIsBlocked(t *testing.T) {
	reader := &blockingReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	var stdout, stderr bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- run([]string{"--event", "signal:USR1"}, reader, &stdout, &stderr)
	}()

	<-reader.started
	sendUSR1(t)
	if code := waitForResult(t, result); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	close(reader.release)
	select {
	case <-reader.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for blocked reader cleanup")
	}
}

func TestRunExitsOnUSR1WhileOutputIsBlocked(t *testing.T) {
	writer := &blockingWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	var stderr bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- run([]string{"--event", "signal:SIGUSR1"}, bytes.NewReader([]byte("payload")), writer, &stderr)
	}()

	<-writer.started
	sendUSR1(t)
	if code := waitForResult(t, result); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	writer.Release()
	select {
	case <-writer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for blocked writer cleanup")
	}
}

func TestProcessHandlesClosedStdinPipe(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "--event", "signal:USR1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	waited = true
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			t.Fatalf("exit code = %d; stderr = %q", exitErr.ExitCode(), stderr.String())
		}
		t.Fatal(waitErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.Bytes())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.Bytes())
	}
}

func TestProcessExitsOnUSR1WithInputStillOpen(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "--event", "signal:USR1")
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = stdinRead
	cmd.Stderr = stderrWrite
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Keep only the child's read end so a later parent write proves that the
	// child closed its stdin when it exited.
	_ = stdinRead.Close()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	waited := false
	t.Cleanup(func() {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdout.Close()
		_ = stderrRead.Close()
		_ = stderrWrite.Close()
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
		}
	})

	if _, err := stdinWrite.Write([]byte("input")); err != nil {
		t.Fatalf("writing input: %v", err)
	}
	readBack := make([]byte, len("input"))
	if _, err := io.ReadFull(stdout, readBack); err != nil {
		t.Fatalf("reading copied input: %v", err)
	}
	if string(readBack) != "input" {
		t.Fatalf("stdout = %q, want %q", readBack, "input")
	}

	signalErr := syscall.Kill(cmd.Process.Pid, syscall.SIGUSR1)
	if signalErr != nil {
		t.Fatal(signalErr)
	}
	select {
	case waitErr := <-waitDone:
		waited = true
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				t.Fatalf("exit code = %d", exitErr.ExitCode())
			}
			t.Fatal(waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after USR1")
	}
	if _, err := stdinWrite.Write([]byte("after USR1")); err == nil {
		t.Fatal("stdin write succeeded after child exit, want closed pipe")
	} else if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("stdin write after child exit: %v, want EPIPE or closed-pipe error", err)
	}
	_ = stderrWrite.Close()
	stderrData, err := io.ReadAll(stderrRead)
	if err != nil {
		t.Fatal(err)
	}
	if len(stderrData) != 0 {
		t.Fatalf("stderr = %q, want empty", stderrData)
	}
}

func TestProcessExitsOnUSR1WhileStdoutPipeIsFull(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "--event", "signal:USR1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	writer := &blockingWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = writer
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	type processWaitResult struct {
		state *os.ProcessState
		err   error
	}
	waitDone := make(chan processWaitResult, 1)
	go func() {
		state, err := cmd.Process.Wait()
		waitDone <- processWaitResult{state: state, err: err}
	}()
	inputDone := make(chan error, 1)
	inputStarted := false
	inputFinished := false
	waited := false
	t.Cleanup(func() {
		writer.Release()
		_ = stdin.Close()
		_ = stderr.Close()
		if !waited {
			_ = cmd.Process.Kill()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
			}
		}
		select {
		case <-writer.done:
		case <-time.After(5 * time.Second):
		}
		if inputStarted && !inputFinished {
			select {
			case <-inputDone:
			case <-time.After(5 * time.Second):
			}
		}
	})

	inputStarted = true
	go func() {
		_, err := stdin.Write(bytes.Repeat([]byte("input"), 1<<20))
		inputDone <- err
	}()
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child stdout writer to block")
	}

	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-waitDone:
		waited = true
		if result.err != nil {
			var exitErr *exec.ExitError
			if errors.As(result.err, &exitErr) {
				t.Fatalf("exit code = %d", exitErr.ExitCode())
			}
			t.Fatal(result.err)
		}
		if result.state.ExitCode() != 0 {
			t.Fatalf("exit code = %d, want 0", result.state.ExitCode())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child to exit after USR1")
	}
	writer.Release()
	select {
	case <-writer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stdout copier cleanup")
	}
	_ = stdin.Close()
	select {
	case <-inputDone:
		inputFinished = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input writer cleanup")
	}
}

func TestRunReportsEPIPE(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--event", "signal:USR1"}, strings.NewReader("input"), failingWriter{err: syscall.EPIPE}, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Fatalf("stderr = %q, want broken-pipe diagnostic", stderr.String())
	}
}

func TestProcessReportsDownstreamClosure(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "--event", "signal:SIGUSR1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = stdin.Write(bytes.Repeat([]byte("x"), 1<<20))
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	waited = true
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatal(waitErr)
		}
		if exitErr.ExitCode() != 1 {
			t.Fatalf("exit code = %d, want 1; stderr = %q", exitErr.ExitCode(), stderr.String())
		}
	} else {
		t.Fatalf("exit code = 0, want 1; stderr = %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Fatalf("stderr = %q, want broken-pipe diagnostic", stderr.String())
	}
}
