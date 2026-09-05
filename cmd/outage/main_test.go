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
		"outage file:<path>",
		"file:<path>",
		"outage duration:<value>",
		"duration:<value>",
		"outage datetime:YYYY-MM-DDTHH:MM",
		"outage datetime:YYYY-MM-DDTHH:MM:SS",
		"outage datetime:YYYY-MM-DDTHH:MM:SSZ",
		"outage datetime:YYYY-MM-DDTHH:MM:SS+HH:MM",
		"outage datetime:YYYY-MM-DDTHH:MM:SS-HH:MM",
		"datetime:YYYY-MM-DDTHH:MM[:SS]",
		"datetime:YYYY-MM-DDTHH:MM:SS[Z|+HH:MM|-HH:MM]",
		"RFC3339",
		"Arguments:",
		"Options:",
		"-h, --help",
		"--version",
		"Help options take priority",
		"exits outage",
		"signal directly to the producer",
		"unsupported on Windows",
		"captured at startup",
		"without reading stdin",
		"DST gaps",
		"earlier absolute occurrence",
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

func TestRunFileEventExitsImmediatelyWhenPathExists(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "file",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("trigger"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trigger")
			tc.setup(t, path)

			var stdout, stderr bytes.Buffer
			code := run([]string{"file:" + path}, unreadableReader{}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunDurationEventExitsImmediatelyWithoutReadingStdin(t *testing.T) {
	for _, value := range []string{"0", "0s", "0ms"} {
		t.Run(value, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"duration:" + value}, unreadableReader{}, &stdout, &stderr)
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestValidateDurationEventAcceptsGoDurationSyntax(t *testing.T) {
	for _, event := range []string{"duration:30s", "duration:500ms", "duration:1m30s"} {
		t.Run(event, func(t *testing.T) {
			if err := validateArgs([]string{event}); err != nil {
				t.Fatalf("validateArgs(%q) = %v, want nil", event, err)
			}
		})
	}
}

func TestValidateAndConditionsRequireExactSeparatorAndPreserveOperands(t *testing.T) {
	if err := validateArgs([]string{"duration:1s && duration:2s"}); err != nil {
		t.Fatalf("validateArgs(exact AND expression) = %v, want nil", err)
	}

	for _, tc := range []struct {
		name           string
		event          string
		wantDiagnostic string
	}{
		{
			name:           "missing separator spaces",
			event:          "duration:1s&&duration:2s",
			wantDiagnostic: "duration:1s&&duration:2s",
		},
		{
			name:           "space retained on left operand",
			event:          "duration:1s  && duration:2s",
			wantDiagnostic: "duration:1s ",
		},
		{
			name:           "space retained on right operand",
			event:          "duration:1s &&  duration:2s",
			wantDiagnostic: " duration:2s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{tc.event}, unreadableReader{}, &stdout, &stderr)
			if code != exitArgError {
				t.Fatalf("event %q: exit code = %d, want %d; stderr = %q", tc.event, code, exitArgError, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("event %q: stdout = %q, want empty", tc.event, stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.wantDiagnostic) {
				t.Fatalf("event %q: stderr = %q, want diagnostic containing %q", tc.event, stderr.String(), tc.wantDiagnostic)
			}
		})
	}
}

func TestRunRejectsEmptyAndConditionMembersWithoutReadingStdin(t *testing.T) {
	for _, tc := range []struct {
		name           string
		event          string
		wantDiagnostic string
	}{
		{name: "leading separator", event: " && signal:USR1", wantDiagnostic: `""`},
		{name: "trailing separator", event: "signal:USR1 && ", wantDiagnostic: `""`},
		{name: "consecutive separators", event: "signal:USR1 &&  && signal:USR2", wantDiagnostic: `""`},
		{name: "first invalid member", event: "signal:TERM && signal:USR1", wantDiagnostic: `"signal:TERM"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{tc.event}, unreadableReader{}, &stdout, &stderr)
			if code != exitArgError {
				t.Fatalf("event %q: exit code = %d, want %d; stderr = %q", tc.event, code, exitArgError, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("event %q: stdout = %q, want empty", tc.event, stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.wantDiagnostic) {
				t.Fatalf("event %q: stderr = %q, want diagnostic containing %q", tc.event, stderr.String(), tc.wantDiagnostic)
			}
		})
	}
}

func TestValidateConditionsCheckAllSyntaxBeforePlatformCapability(t *testing.T) {
	signalUnsupported := func() bool { return false }
	for _, tc := range []struct {
		name           string
		event          string
		wantDiagnostic string
	}{
		{name: "trailing empty member", event: "signal:USR1 && ", wantDiagnostic: `""`},
		{name: "consecutive empty member", event: "signal:USR1 &&  && signal:USR2", wantDiagnostic: `""`},
		{name: "first malformed member", event: "signal:TERM && signal:USR1", wantDiagnostic: `"signal:TERM"`},
		{name: "capability checked after syntax", event: "signal:USR1 && signal:USR2", wantDiagnostic: `"signal:USR1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateArgsAtWithSignalSupport([]string{tc.event}, time.UTC, signalUnsupported)
			if err == nil {
				t.Fatalf("validateArgsAtWithSignalSupport(%q) = nil, want error", tc.event)
			}
			if !strings.Contains(err.Error(), tc.wantDiagnostic) {
				t.Fatalf("error = %q, want diagnostic containing %q", err, tc.wantDiagnostic)
			}
		})
	}
}

func TestRunAndConditionsLatchSatisfiedMembersUntilAllArrive(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, location)
	firstTimer := make(chan time.Time, 1)
	secondTimer := make(chan time.Time, 1)
	armed := make(chan time.Duration, 2)
	stopped := make(chan struct{}, 2)
	timerCalls := 0
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			timerCalls++
			armed <- delay
			if timerCalls == 1 {
				return firstTimer, func() { stopped <- struct{}{} }
			}
			return secondTimer, func() { stopped <- struct{}{} }
		},
	}
	reader := &fileEventReader{
		payload: []byte("input before AND conditions"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	readerStarted := false
	finished := false
	go func() {
		result <- runWithClock([]string{"duration:1s && duration:2s"}, reader, writer, &diagnostics, clock)
	}()

	t.Cleanup(func() {
		if readerStarted {
			close(reader.release)
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for run cleanup")
			}
		}
	})

	for _, want := range []time.Duration{time.Second, 2 * time.Second} {
		select {
		case got := <-armed:
			if got != want {
				t.Fatalf("timer delay = %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for AND condition timer")
		}
	}
	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for AND condition input reader")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for AND condition input forwarding")
	}

	// Satisfying the second member first must latch it without firing the
	// aggregate event while the first member remains pending.
	secondTimer <- now
	select {
	case code := <-result:
		t.Fatalf("run exited after one AND member with code %d", code)
	case <-time.After(100 * time.Millisecond):
	}

	firstTimer <- now
	select {
	case code := <-result:
		finished = true
		if code != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", code, exitOK, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all AND conditions")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("first AND condition timer was not cleaned up")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("second AND condition timer was not cleaned up")
	}
	if writer.output.String() != "input before AND conditions" {
		t.Fatalf("stdout = %q, want %q", writer.output.String(), "input before AND conditions")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestRunAndConditionsMatchDuplicateFilePathWithOneCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trigger")
	reader := &fileEventReader{
		payload: []byte("input before duplicate file conditions"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	readerStarted := false
	finished := false
	go func() {
		result <- run([]string{"file:" + path + " && file:" + path}, reader, writer, &diagnostics)
	}()

	t.Cleanup(func() {
		if readerStarted {
			close(reader.release)
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for run cleanup")
			}
		}
	})

	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for duplicate-file input reader")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for duplicate-file input forwarding")
	}

	if err := os.WriteFile(path, []byte("trigger"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		finished = true
		if code != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", code, exitOK, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for duplicate-file conditions")
	}
	if writer.output.String() != "input before duplicate file conditions" {
		t.Fatalf("stdout = %q, want %q", writer.output.String(), "input before duplicate file conditions")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestRunAndDurationEquivalentConditionsShareOneMonitor(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, location)
	timerC := make(chan time.Time, 1)
	armed := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	timerCalls := 0
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(time.Duration) (<-chan time.Time, func()) {
			timerCalls++
			armed <- struct{}{}
			return timerC, func() { stopped <- struct{}{} }
		},
	}
	reader := &fileEventReader{
		payload: []byte("input before equivalent durations"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	readerStarted := false
	finished := false
	go func() {
		result <- runWithClock([]string{"duration:1s && duration:1000ms"}, reader, writer, &diagnostics, clock)
	}()

	t.Cleanup(func() {
		close(reader.release)
		if readerStarted {
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for equivalent-duration run cleanup")
			}
		}
	})

	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for equivalent-duration monitor")
	}
	select {
	case <-armed:
		t.Fatal("equivalent durations installed more than one monitor")
	case <-time.After(100 * time.Millisecond):
	}
	if timerCalls != 1 {
		t.Fatalf("timer calls = %d, want 1", timerCalls)
	}
	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for equivalent-duration input reader")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for equivalent-duration input forwarding")
	}

	timerC <- now
	select {
	case code := <-result:
		finished = true
		if code != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", code, exitOK, diagnostics.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("one duration observation did not satisfy equivalent conditions")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("equivalent-duration monitor was not cleaned up")
	}
	if writer.output.String() != "input before equivalent durations" {
		t.Fatalf("stdout = %q, want %q", writer.output.String(), "input before equivalent durations")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestRunAndDatetimeEquivalentInstantsShareOneMonitor(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, location)
	timerC := make(chan time.Time, 1)
	armed := make(chan time.Duration, 2)
	stopped := make(chan struct{}, 2)
	timerCalls := 0
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			timerCalls++
			armed <- delay
			return timerC, func() { stopped <- struct{}{} }
		},
	}
	reader := &fileEventReader{
		payload: []byte("input before equivalent datetimes"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	readerStarted := false
	finished := false
	go func() {
		result <- runWithClock([]string{
			"datetime:2026-09-03T18:00:00Z && datetime:2026-09-03T19:00:00+01:00",
		}, reader, writer, &diagnostics, clock)
	}()

	t.Cleanup(func() {
		close(reader.release)
		if readerStarted {
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for equivalent-datetime run cleanup")
			}
		}
	})

	select {
	case delay := <-armed:
		if delay != time.Hour {
			t.Fatalf("timer delay = %v, want %v", delay, time.Hour)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for equivalent-datetime monitor")
	}
	select {
	case <-armed:
		t.Fatal("equivalent datetimes installed more than one monitor")
	case <-time.After(100 * time.Millisecond):
	}
	if timerCalls != 1 {
		t.Fatalf("timer calls = %d, want 1", timerCalls)
	}
	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for equivalent-datetime input reader")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for equivalent-datetime input forwarding")
	}

	timerC <- now
	select {
	case code := <-result:
		finished = true
		if code != exitOK {
			t.Fatalf("run status = %d, want %d; diagnostics = %q", code, exitOK, diagnostics.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("one datetime observation did not satisfy equivalent instants")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("equivalent-datetime monitor was not cleaned up")
	}
	if writer.output.String() != "input before equivalent datetimes" {
		t.Fatalf("stdout = %q, want %q", writer.output.String(), "input before equivalent datetimes")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
}

func TestRunDurationEventForwardsAndExitsWithoutEOF(t *testing.T) {
	const duration = 250 * time.Millisecond

	reader := &fileEventReader{
		payload: []byte("input before duration"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var stderr bytes.Buffer
	result := make(chan int, 1)
	startedAt := time.Now()
	readerStarted := false
	finished := false
	go func() {
		result <- run([]string{"duration:" + duration.String()}, reader, writer, &stderr)
	}()

	t.Cleanup(func() {
		if readerStarted {
			close(reader.release)
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for run cleanup")
			}
		}
	})

	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for duration-event input reader to start")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input to be forwarded")
	}

	select {
	case code := <-result:
		finished = true
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitOK, stderr.String())
		}
		if elapsed := time.Since(startedAt); elapsed < duration {
			t.Fatalf("duration event elapsed after %v, want at least %v", elapsed, duration)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for duration event")
	}

	if got := writer.output.String(); got != "input before duration" {
		t.Fatalf("stdout = %q, want %q", got, "input before duration")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsInvalidDurationWithoutReadingStdin(t *testing.T) {
	for _, value := range []string{"", "not-a-duration", "1", "-1s", "-0s", "-0"} {
		t.Run(value, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"duration:" + value}, unreadableReader{}, &stdout, &stderr)
			if code != exitArgError {
				t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitArgError, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "invalid duration") {
				t.Fatalf("stderr = %q, want invalid-duration diagnostic", stderr.String())
			}
		})
	}
}

func TestRunFileEventExitsImmediatelyWhenDanglingSymlinkExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated permissions on Windows")
	}

	path := filepath.Join(t.TempDir(), "trigger")
	if err := os.Symlink("missing-target", path); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	assertExistingFileEventExits(t, path)
}

func TestRunFileEventPreservesRelativePath(t *testing.T) {
	dir, err := os.MkdirTemp(".", "outage-file-event-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "trigger")
	if err := os.WriteFile(path, []byte("trigger"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertExistingFileEventExits(t, path)
}

func TestRunFileEventPreservesColonsInPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("colon-containing filenames are not portable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "trigger:name")
	if err := os.WriteFile(path, []byte("trigger"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertExistingFileEventExits(t, path)
}

func assertExistingFileEventExits(t *testing.T, path string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := run([]string{"file:" + path}, unreadableReader{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

type fileEventReader struct {
	payload   []byte
	started   chan struct{}
	release   chan struct{}
	done      chan struct{}
	delivered bool
}

func (r *fileEventReader) Read(p []byte) (int, error) {
	if !r.delivered {
		r.delivered = true
		close(r.started)
		return copy(p, r.payload), nil
	}
	<-r.release
	close(r.done)
	return 0, io.EOF
}

type fileEventWriter struct {
	output   bytes.Buffer
	copied   chan struct{}
	notified bool
}

func (w *fileEventWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	if n > 0 && !w.notified {
		w.notified = true
		close(w.copied)
	}
	return n, err
}

func TestRunFileEventWaitsAndForwardsUntilPathAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trigger")
	reader := &fileEventReader{
		payload: []byte("input before trigger"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var stderr bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- run([]string{"file:" + path}, reader, writer, &stderr)
	}()

	t.Cleanup(func() {
		close(reader.release)
		select {
		case <-reader.done:
		case <-time.After(5 * time.Second):
			t.Errorf("timed out waiting for blocked reader cleanup")
		}
	})

	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file-event input reader to start")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input to be forwarded")
	}
	select {
	case code := <-result:
		t.Fatalf("run exited before file appeared with code %d", code)
	default:
	}

	if err := os.WriteFile(path, []byte("trigger"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file event")
	}

	if got := writer.output.String(); got != "input before trigger" {
		t.Fatalf("stdout = %q, want %q", got, "input before trigger")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunFileEventExitsWhenDanglingSymlinkAppears(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated permissions on Windows")
	}

	path := filepath.Join(t.TempDir(), "trigger")
	reader := &fileEventReader{
		payload: []byte("input before dangling symlink"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var stderr bytes.Buffer
	result := make(chan int, 1)
	readerStarted := false
	finished := false
	t.Cleanup(func() {
		if readerStarted {
			close(reader.release)
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for run cleanup")
			}
		}
	})

	go func() {
		result <- run([]string{"file:" + path}, reader, writer, &stderr)
	}()

	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file-event input reader to start")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input to be forwarded")
	}
	select {
	case code := <-result:
		t.Fatalf("run exited before dangling symlink appeared with code %d", code)
	default:
	}

	if err := os.Symlink("missing-target", path); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("path mode = %v, want symlink", info.Mode())
	}

	select {
	case code := <-result:
		finished = true
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitOK, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dangling symlink file event")
	}
	if got := writer.output.String(); got != "input before dangling symlink" {
		t.Fatalf("stdout = %q, want %q", got, "input before dangling symlink")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunFileEventContinuesAfterTransientStatError(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	target := filepath.Join(parent, "trigger")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}

	reader := &fileEventReader{
		payload: []byte("input before transient stat error"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var stderr bytes.Buffer
	result := make(chan int, 1)
	readerStarted := false
	finished := false
	go func() {
		result <- run([]string{"file:" + target}, reader, writer, &stderr)
	}()

	t.Cleanup(func() {
		if readerStarted {
			close(reader.release)
			select {
			case <-reader.done:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for blocked reader cleanup")
			}
		}
		if !finished {
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for run cleanup")
			}
		}
	})

	select {
	case <-reader.started:
		readerStarted = true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file-event input reader to start")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input to be forwarded")
	}

	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("temporarily not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatalf("parent path is a directory, want a regular file")
	}

	// The input/output handshakes above establish that run reached its select.
	// Keep the observable ENOTDIR state across several polling intervals so the
	// monitor must encounter the transient error before the parent is restored.
	for i := 0; i < 3; i++ {
		select {
		case code := <-result:
			finished = true
			t.Fatalf("run exited while parent was a regular file with code %d", code)
		case <-time.After(filePollInterval):
		}
		info, err := os.Stat(parent)
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			t.Fatalf("parent path became a directory before restoration")
		}
	}

	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("trigger"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-result:
		finished = true
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file event after transient stat error")
	}
	if got := writer.output.String(); got != "input before transient stat error" {
		t.Fatalf("stdout = %q, want %q", got, "input before transient stat error")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsEmptyFileEvent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"file:"}, unreadableReader{}, &stdout, &stderr)
	if code != exitArgError {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitArgError, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr is empty, want argument diagnostic")
	}
}

func TestRunRejectsFileEventWithInvalidPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"file:" + string([]byte{0})}, unreadableReader{}, &stdout, &stderr)
	if code != exitArgError {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitArgError, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr is empty, want argument diagnostic")
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
