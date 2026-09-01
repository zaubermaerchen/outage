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

func sendUSR2(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
}

func TestInstallSignalMonitorPanicsForUnexpectedEvent(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("installSignalMonitor did not panic for an unexpected event")
		}
	}()

	installSignalMonitor("signal:TERM")
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
		result <- run([]string{"signal:USR1"}, reader, &stdout, &stderr)
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
		result <- run([]string{"signal:SIGUSR1"}, bytes.NewReader([]byte("payload")), writer, &stderr)
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

func TestRunExitsOnUSR2WhileInputIsBlocked(t *testing.T) {
	for _, event := range []string{"signal:USR2", "signal:SIGUSR2"} {
		t.Run(event, func(t *testing.T) {
			reader := &blockingReader{
				started: make(chan struct{}),
				release: make(chan struct{}),
				done:    make(chan struct{}),
			}
			t.Cleanup(func() {
				close(reader.release)
				select {
				case <-reader.done:
				case <-time.After(5 * time.Second):
					t.Errorf("timed out waiting for blocked reader cleanup")
				}
			})
			var stdout, stderr bytes.Buffer
			result := make(chan int, 1)
			go func() {
				result <- run([]string{event}, reader, &stdout, &stderr)
			}()

			select {
			case <-reader.started:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for blocked reader to start")
			}
			sendUSR2(t)
			if code := waitForResult(t, result); code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestRunExitsOnUSR2WhileOutputIsBlocked(t *testing.T) {
	for _, event := range []string{"signal:USR2", "signal:SIGUSR2"} {
		t.Run(event, func(t *testing.T) {
			writer := &blockingWriter{
				started: make(chan struct{}),
				release: make(chan struct{}),
				done:    make(chan struct{}),
			}
			t.Cleanup(func() {
				writer.Release()
				select {
				case <-writer.done:
				case <-time.After(5 * time.Second):
					t.Errorf("timed out waiting for blocked writer cleanup")
				}
			})
			var stderr bytes.Buffer
			result := make(chan int, 1)
			go func() {
				result <- run([]string{event}, bytes.NewReader([]byte("payload")), writer, &stderr)
			}()

			select {
			case <-writer.started:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for blocked writer to start")
			}
			sendUSR2(t)
			if code := waitForResult(t, result); code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestProcessHandlesClosedStdinPipe(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "signal:USR1")
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
	cmd := exec.Command(binary, "signal:USR1")
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
	cmd := exec.Command(binary, "signal:USR1")
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		t.Fatal(err)
	}
	// Keep the read end open and unread so the child's own stdout write, rather
	// than an os/exec copier in the parent, is what reaches backpressure.
	cmd.Stdin = stdinRead
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		_ = stderr.Close()
		t.Fatal(err)
	}

	waitDone := make(chan error, 1)
	inputDone := make(chan error, 1)
	inputStart := make(chan struct{})
	inputStarted := false
	inputFinished := false
	waited := false
	sentinelReadDone := make(chan struct {
		data []byte
		err  error
	}, 1)
	sentinelReadStarted := false
	sentinelReadFinished := false
	go func() {
		waitDone <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
		}
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		_ = stderr.Close()
		if !waited {
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
			}
		}
		if sentinelReadStarted && !sentinelReadFinished {
			select {
			case <-sentinelReadDone:
			case <-time.After(5 * time.Second):
			}
		}
		if inputStarted && !inputFinished {
			select {
			case <-inputDone:
			case <-time.After(5 * time.Second):
			}
		}
	})

	// os/exec has duplicated these descriptors for the child. Keep only the
	// parent ends so closing them cannot mask the pipe state being tested.
	if err := stdinRead.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stdoutWrite.Close(); err != nil {
		t.Fatal(err)
	}

	sentinel := []byte("outage-test-sentinel")
	sentinelReadStarted = true
	go func() {
		data := make([]byte, len(sentinel))
		_, err := io.ReadFull(stdoutRead, data)
		sentinelReadDone <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	if n, err := stdinWrite.Write(sentinel); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	} else if n != len(sentinel) {
		t.Fatalf("sentinel write = %d bytes, want %d", n, len(sentinel))
	}
	select {
	case result := <-sentinelReadDone:
		sentinelReadFinished = true
		if result.err != nil {
			t.Fatalf("reading sentinel: %v", result.err)
		}
		if !bytes.Equal(result.data, sentinel) {
			t.Fatalf("sentinel = %q, want %q", result.data, sentinel)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child to copy sentinel")
	}

	inputStarted = true
	go func() {
		close(inputStart)
		_, err := stdinWrite.Write(bytes.Repeat([]byte("input"), 1<<20))
		inputDone <- err
	}()
	select {
	case <-inputStart:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input writer to start")
	}
	select {
	case err := <-inputDone:
		inputFinished = true
		t.Fatalf("input writer completed before stdout backpressure: %v", err)
	case <-time.After(time.Second):
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("child is no longer alive while input writer is blocked: %v", err)
	}

	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
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
		if cmd.ProcessState == nil {
			t.Fatal("child process state is nil after Wait")
		}
		if cmd.ProcessState.ExitCode() != 0 {
			t.Fatalf("exit code = %d, want 0", cmd.ProcessState.ExitCode())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child to exit after USR1")
	}
	if err := stdinWrite.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inputDone:
		inputFinished = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input writer cleanup")
	}
	if err := stdoutRead.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunReportsEPIPE(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"signal:USR1"}, strings.NewReader("input"), failingWriter{err: syscall.EPIPE}, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Fatalf("stderr = %q, want broken-pipe diagnostic", stderr.String())
	}
}

func TestProcessReportsDownstreamClosure(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "signal:SIGUSR1")
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
