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
	"time"
)

func buildOutage(t *testing.T) string {
	return buildOutageWithVersion(t, "")
}

func buildOutageWithVersion(t *testing.T, value string) string {
	t.Helper()

	binaryName := "outage"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), binaryName)
	args := []string{"build", "-o", binary}
	if value != "" {
		args = append(args, "-ldflags", "-X main.version="+value)
	}
	args = append(args, ".")
	cmd := exec.Command("go", args...)
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

func TestProcessPrintsVersion(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		wantVersion string
	}{
		{name: "default", wantVersion: "dev"},
		{name: "injected", version: "v0.1.0", wantVersion: "v0.1.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary := buildOutageWithVersion(t, tc.version)
			result := runOutage(t, binary, []string{"--version"}, nil)

			if result.code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", result.code, result.stderr)
			}
			if string(result.stdout) != "outage "+tc.wantVersion+"\n" {
				t.Fatalf("stdout = %q, want %q", result.stdout, "outage "+tc.wantVersion+"\n")
			}
			if len(result.stderr) != 0 {
				t.Fatalf("stderr = %q, want empty", result.stderr)
			}
		})
	}
}

func TestProcessReportsVersionOutputClosure(t *testing.T) {
	testProcessReportsOutputClosure(t, []string{"--version"})
}

func TestProcessReportsHelpOutputClosure(t *testing.T) {
	testProcessReportsOutputClosure(t, []string{"--help"})
}

func testProcessReportsOutputClosure(t *testing.T, args []string) {
	t.Helper()

	binary := buildOutage(t)
	cmd := exec.Command(binary, args...)
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := stdoutRead.Close(); err != nil {
		_ = stdoutWrite.Close()
		t.Fatal(err)
	}
	cmd.Stdout = stdoutWrite
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdoutWrite.Close()
		t.Fatal(err)
	}
	if err := stdoutWrite.Close(); err != nil {
		t.Fatal(err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	select {
	case waitErr := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("args %v: wait error = %v, want exit status 1; stderr = %q", args, waitErr, stderr.Bytes())
		}
		if exitErr.ExitCode() != 1 {
			t.Fatalf("args %v: exit code = %d, want 1; stderr = %q", args, exitErr.ExitCode(), stderr.Bytes())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
		t.Fatal("timed out waiting for version command after stdout closure")
	}
	if stderr.Len() == 0 {
		t.Fatalf("args %v: stderr is empty, want output-write diagnostic", args)
	}
}

func TestRunHelpAliasesAreIdentical(t *testing.T) {
	var outputs [2][]byte
	for i, arg := range []string{"-h", "--help"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg}, unreadableReader{}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("args %q: exit code = %d, want 0; stderr = %q", arg, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("args %q: stderr = %q, want empty", arg, stderr.String())
		}
		if !bytes.HasSuffix(stdout.Bytes(), []byte{'\n'}) {
			t.Fatalf("args %q: stdout = %q, want trailing newline", arg, stdout.String())
		}
		outputs[i] = stdout.Bytes()
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatalf("short help = %q, long help = %q; want identical output", outputs[0], outputs[1])
	}
}

func TestRunHelpDocumentsSupportedUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, unreadableReader{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	for _, want := range []string{
		"Usage: outage signal:USR1",
		"signal:SIGUSR1",
		"outage signal:USR2",
		"signal:SIGUSR2",
		"Arguments:",
		"Options:",
		"-h, --help",
		"--version",
		"Help options take priority",
		"exits outage",
		"signal directly to the producer",
		"unsupported on Windows",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help = %q, want substring %q", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), "--event") {
		t.Fatalf("help = %q, must not document removed --event option", stdout.String())
	}
}

func TestRunHelpHasPriorityOverEveryOtherArgument(t *testing.T) {
	cases := [][]string{
		{"--help", "--version"},
		{"--version", "--help"},
		{"signal:USR1", "--help"},
		{"--help", "signal:USR1"},
		{"--wat", "--help"},
		{"--help", "--wat"},
		{"--event", "--help"},
		{"-h", "--help", "--help"},
		{"signal:TERM", "-h"},
	}

	var want []byte
	for i, args := range cases {
		var stdout, stderr bytes.Buffer
		code := run(args, unreadableReader{}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("args %q: exit code = %d, want 0; stderr = %q", args, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("args %q: stderr = %q, want empty", args, stderr.String())
		}
		if i == 0 {
			want = append([]byte(nil), stdout.Bytes()...)
		}
		if !bytes.Equal(stdout.Bytes(), want) {
			t.Fatalf("args %q: stdout differs from help output", args)
		}
	}
}

func TestProcessExitsWithOpenStdinForHelp(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "--help")
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinWrite.Close()
	cmd.Stdin = stdinRead
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdinRead.Close()
		t.Fatal(err)
	}
	_ = stdinRead.Close()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	waited := false
	t.Cleanup(func() {
		_ = stdinRead.Close()
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		<-waitDone
	})

	select {
	case waitErr := <-waitDone:
		waited = true
		if waitErr != nil {
			t.Fatalf("wait failed: %v; stdout = %q, stderr = %q", waitErr, stdout.Bytes(), stderr.Bytes())
		}
		if stdout.String() != string(helpText) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), helpText)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	case <-time.After(5 * time.Second):
		killErr := cmd.Process.Kill()
		waitErr := <-waitDone
		waited = true
		t.Fatalf("timed out waiting for help command with open stdin (kill error: %v, wait error: %v)", killErr, waitErr)
	}
}

type unreadableReader struct{}

func (unreadableReader) Read([]byte) (int, error) {
	return 0, errors.New("stdin must not be read")
}

func TestRunPrintsVersionWithoutReadingStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, unreadableReader{}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if stdout.String() != "outage dev\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "outage dev\n")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunReportsHelpWriteError(t *testing.T) {
	var stderr bytes.Buffer
	wantErr := errors.New("write failed")
	code := run([]string{"--help"}, unreadableReader{}, failingWriter{err: wantErr}, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantErr)
	}
}

func TestRunWithoutHelpPreservesEventPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal event is unsupported on Windows")
	}

	input := []byte("input without help")
	var stdout, stderr bytes.Buffer
	code := run([]string{"signal:USR1"}, bytes.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), input) {
		t.Fatalf("stdout = %q, want %q", stdout.Bytes(), input)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunReportsVersionWriteError(t *testing.T) {
	var stderr bytes.Buffer
	wantErr := errors.New("write failed")
	code := run([]string{"--version"}, strings.NewReader("input"), failingWriter{err: wantErr}, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantErr)
	}
}

func TestProcessExitsWithOpenStdinForVersion(t *testing.T) {
	binary := buildOutage(t)
	cmd := exec.Command(binary, "--version")
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinWrite.Close()
	cmd.Stdin = stdinRead
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdinRead.Close()
		t.Fatal(err)
	}
	_ = stdinRead.Close()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("wait failed: %v; stdout = %q, stderr = %q", err, stdout.Bytes(), stderr.Bytes())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
		t.Fatal("timed out waiting for version command with open stdin")
	}

	if stdout.String() != "outage dev\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "outage dev\n")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestProcessPassesThroughBinaryInput(t *testing.T) {
	binary := buildOutage(t)
	input := []byte{0, 1, 2, 3, 0xff, '\n', 0}

	for _, event := range []string{
		"signal:USR1",
		"signal:SIGUSR1",
		"signal:USR2",
		"signal:SIGUSR2",
	} {
		result := runOutage(t, binary, []string{event}, bytes.NewReader(input))
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
	result := runOutage(t, binary, []string{"signal:USR1"}, strings.NewReader(""))

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
		name           string
		args           []string
		wantDiagnostic string
	}{
		{name: "missing event", args: nil},
		{name: "bare removed event option", args: []string{"--event"}, wantDiagnostic: "--event"},
		{name: "removed event option form", args: []string{"--event", "signal:USR1"}, wantDiagnostic: "--event"},
		{name: "unsupported value", args: []string{"signal:TERM"}},
		{name: "case-sensitive value", args: []string{"signal:usr1"}},
		{name: "case-sensitive USR2 value", args: []string{"signal:usr2"}},
		{name: "extra event", args: []string{"signal:USR1", "signal:SIGUSR1"}},
		{name: "unknown option", args: []string{"--wat"}},
		{name: "short version alias", args: []string{"-v"}},
		{name: "positional argument", args: []string{"input"}},
		{name: "version with event", args: []string{"--version", "signal:USR1"}},
		{name: "event with version", args: []string{"signal:USR1", "--version"}},
		{name: "version with unknown option", args: []string{"--version", "--wat"}},
		{name: "unknown option with version", args: []string{"--wat", "--version"}},
		{name: "version with positional argument", args: []string{"--version", "input"}},
		{name: "positional argument with version", args: []string{"input", "--version"}},
		{name: "repeated version", args: []string{"--version", "--version"}},
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
			if tc.wantDiagnostic != "" && !strings.Contains(string(result.stderr), tc.wantDiagnostic) {
				t.Fatalf("stderr = %q, want diagnostic containing %q", result.stderr, tc.wantDiagnostic)
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
	for _, event := range []string{
		"signal:USR1",
		"signal:SIGUSR1",
		"signal:USR2",
		"signal:SIGUSR2",
	} {
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

			result := runOutage(t, binary, []string{event}, input)
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
	code := run([]string{"signal:USR1"}, failingReader{err: wantErr}, &stdout, &stderr)
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
	code := run([]string{"signal:SIGUSR1"}, strings.NewReader("input"), failingWriter{err: wantErr}, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantErr)
	}
}
