package main

// This file verifies command-line validation and byte-for-byte stream handling.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildOutage(t *testing.T) string {
	t.Helper()

	binaryName := "outage"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), binaryName)
	cmd := exec.Command("go", "build", "-o", binary, ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}
	return binary
}

type commandResult struct {
	stdout []byte
	stderr []byte
	code   int
}

func runOutage(t *testing.T, binary string, args []string, stdin io.Reader) commandResult {
	t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run failed: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), code: code}
}

func TestProcessPassesThroughBinaryInput(t *testing.T) {
	binary := buildOutage(t)
	input := []byte{0, 1, 2, 3, 0xff, '\n', 0}

	for _, event := range []string{"signal:USR1", "signal:SIGUSR1"} {
		result := runOutage(t, binary, []string{"--event", event}, bytes.NewReader(input))
		if runtime.GOOS == "windows" {
			if result.code != 2 {
				t.Fatalf("event %q: exit code = %d, want 2; stderr = %q", event, result.code, result.stderr)
			}
			if len(result.stdout) != 0 {
				t.Fatalf("event %q: stdout = %v, want empty", event, result.stdout)
			}
			if len(result.stderr) == 0 {
				t.Fatalf("event %q: stderr is empty, want unsupported-event diagnostic", event)
			}
			continue
		}
		if result.code != 0 {
			t.Fatalf("event %q: exit code = %d, want 0; stderr = %q", event, result.code, result.stderr)
		}
		if !bytes.Equal(result.stdout, input) {
			t.Fatalf("event %q: stdout = %v, want %v", event, result.stdout, input)
		}
		if len(result.stderr) != 0 {
			t.Fatalf("event %q: stderr = %q, want empty", event, result.stderr)
		}
	}
}

func TestProcessPassesThroughEmptyInput(t *testing.T) {
	binary := buildOutage(t)
	result := runOutage(t, binary, []string{"--event", "signal:USR1"}, strings.NewReader(""))

	wantCode := 0
	if runtime.GOOS == "windows" {
		wantCode = 2
	}
	if result.code != wantCode {
		t.Fatalf("exit code = %d, want %d; stderr = %q", result.code, wantCode, result.stderr)
	}
	if len(result.stdout) != 0 {
		t.Fatalf("stdout = %v, want empty", result.stdout)
	}
	if runtime.GOOS == "windows" && len(result.stderr) == 0 {
		t.Fatal("stderr is empty, want unsupported-event diagnostic")
	}
}

func TestProcessRejectsInvalidArgumentsWithoutReadingStdin(t *testing.T) {
	binary := buildOutage(t)
	cases := []struct {
		name string
		args []string
	}{
		{name: "missing event", args: nil},
		{name: "missing value", args: []string{"--event"}},
		{name: "unsupported value", args: []string{"--event", "signal:TERM"}},
		{name: "case-sensitive value", args: []string{"--event", "signal:usr1"}},
		{name: "duplicate event", args: []string{"--event", "signal:USR1", "--event", "signal:SIGUSR1"}},
		{name: "unknown option", args: []string{"--wat"}},
		{name: "positional argument", args: []string{"input"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "input")
			inputData := []byte("must not be read")
			if err := os.WriteFile(inputPath, inputData, 0o600); err != nil {
				t.Fatal(err)
			}
			input, err := os.Open(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()

			result := runOutage(t, binary, tc.args, input)
			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stdout = %q, stderr = %q", result.code, result.stdout, result.stderr)
			}
			if len(result.stdout) != 0 {
				t.Fatalf("stdout = %q, want empty", result.stdout)
			}
			if len(result.stderr) == 0 {
				t.Fatal("stderr is empty, want argument diagnostic")
			}
			offset, err := input.Seek(0, io.SeekCurrent)
			if err != nil {
				t.Fatal(err)
			}
			if offset != 0 {
				t.Fatalf("stdin offset = %d, want 0", offset)
			}
		})
	}
}

func TestWindowsRejectsSignalEventsWithoutReadingStdin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only signal contract")
	}

	binary := buildOutage(t)
	for _, event := range []string{"signal:USR1", "signal:SIGUSR1"} {
		t.Run(event, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "input")
			if err := os.WriteFile(inputPath, []byte("must not be read"), 0o600); err != nil {
				t.Fatal(err)
			}
			input, err := os.Open(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()

			result := runOutage(t, binary, []string{"--event", event}, input)
			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stdout = %q, stderr = %q", result.code, result.stdout, result.stderr)
			}
			if len(result.stdout) != 0 {
				t.Fatalf("stdout = %q, want empty", result.stdout)
			}
			if len(result.stderr) == 0 {
				t.Fatal("stderr is empty, want unsupported-event diagnostic")
			}
			offset, err := input.Seek(0, io.SeekCurrent)
			if err != nil {
				t.Fatal(err)
			}
			if offset != 0 {
				t.Fatalf("stdin offset = %d, want 0", offset)
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunReportsReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal event is unsupported on Windows")
	}

	var stdout, stderr bytes.Buffer
	wantErr := errors.New("read failed")
	code := run([]string{"--event", "signal:USR1"}, failingReader{err: wantErr}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.Bytes())
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantErr)
	}
}

func TestRunReportsWriteError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal event is unsupported on Windows")
	}

	var stderr bytes.Buffer
	wantErr := errors.New("write failed")
	code := run([]string{"--event", "signal:SIGUSR1"}, strings.NewReader("input"), failingWriter{err: wantErr}, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantErr)
	}
}
