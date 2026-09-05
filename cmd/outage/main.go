package main

// This file validates command-line arguments and coordinates event monitoring,
// version output, and stream copying.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	exitOK        = 0
	exitCopyError = 1
	exitArgError  = 2
)

const helpText = `Usage: outage signal:USR1
       outage signal:SIGUSR1
       outage signal:USR2
       outage signal:SIGUSR2
       outage file:<path>
       outage duration:<value>
       outage datetime:YYYY-MM-DDTHH:MM
       outage datetime:YYYY-MM-DDTHH:MM:SS
       outage datetime:YYYY-MM-DDTHH:MM:SSZ
       outage datetime:YYYY-MM-DDTHH:MM:SS+HH:MM

Copy stdin to stdout until the event is received. Receiving the event exits outage;
it does not send a signal directly to the producer.
Signal events are unsupported on Windows.
File events exit when the specified path exists.
Duration events use Go duration syntax and exit after the specified time has elapsed.
Datetime events use the process-local wall clock captured at startup and begin
monitoring immediately. A datetime already reached exits without reading stdin;
DST gaps and malformed values are invalid. RFC3339 timezone-qualified values
require seconds and use numeric offsets or Z; IANA timezone names and fractional
values are invalid.
For an ambiguous DST overlap, the earlier absolute occurrence is selected.

Arguments:
  signal:USR1                Exit on USR1 (signal:SIGUSR1 is an alias).
  signal:USR2                Exit on USR2 (signal:SIGUSR2 is an alias).
  file:<path>                Exit when the specified path exists.
  duration:<value>           Exit after the duration has elapsed.
  datetime:YYYY-MM-DDTHH:MM[:SS]
                             Exit when the local wall clock reaches the datetime
                             (seconds may be omitted).
  datetime:YYYY-MM-DDTHH:MM:SS[Z|+HH:MM|-HH:MM]
                             RFC3339 form; explicit timezones require seconds.
Options:
  --version                 Print the version (standalone).
  -h, --help                Show this help.
Help options take priority over every other argument.
`

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out io.Writer, errOut io.Writer) int {
	return runWithClock(args, in, out, errOut, defaultRuntimeClock())
}

func runWithClock(args []string, in io.Reader, out io.Writer, errOut io.Writer, clock runtimeClock) int {
	clock = clock.normalized()
	startedAt := clock.now()

	for _, arg := range args {
		if arg != "-h" && arg != "--help" {
			continue
		}
		ignoreSIGPIPE()
		if _, err := fmt.Fprint(out, helpText); err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}

	if len(args) == 1 && args[0] == "--version" {
		ignoreSIGPIPE()
		if _, err := fmt.Fprintf(out, "outage %s\n", version); err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}

	if err := validateArgsAt(args, clock.location); err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}

	event := args[0]
	var eventCh <-chan os.Signal
	var durationCh <-chan time.Time
	var datetimeCh <-chan time.Time
	var stopEventMonitor func()
	if strings.HasPrefix(event, "duration:") {
		duration, err := parseDurationEvent(event)
		if err != nil {
			writeDiagnostic(errOut, err)
			return exitArgError
		}
		if duration == 0 {
			return exitOK
		}
		remaining := duration - clock.now().Sub(startedAt)
		if remaining <= 0 {
			return exitOK
		}
		durationCh, stopEventMonitor = clock.newTimer(remaining)
		if durationCh == nil {
			if stopEventMonitor != nil {
				stopEventMonitor()
			}
			return exitOK
		}
		ignoreSIGPIPE()
	} else if strings.HasPrefix(event, "datetime:") {
		deadline, err := parseDatetimeEvent(event, clock.location)
		if err != nil {
			writeDiagnostic(errOut, err)
			return exitArgError
		}
		if !deadline.After(startedAt) {
			return exitOK
		}
		datetimeCh, stopEventMonitor = startDeadlineMonitor(&deadline, clock.now, clock.newTimer)
		if datetimeCh == nil {
			return exitOK
		}
		ignoreSIGPIPE()
	} else if strings.HasPrefix(event, "file:") {
		path := strings.TrimPrefix(event, "file:")
		if _, err := os.Lstat(path); err == nil {
			return exitOK
		} else if !os.IsNotExist(err) {
			writeDiagnostic(errOut, err)
			return exitArgError
		}
		ignoreSIGPIPE()
		eventCh, stopEventMonitor = installFileMonitor(path)
	} else {
		eventCh, stopEventMonitor = installSignalMonitor(event)
	}
	if stopEventMonitor != nil {
		defer stopEventMonitor()
	}

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, in)
		copyDone <- err
	}()

	select {
	case <-eventCh:
		return exitOK
	case <-durationCh:
		return exitOK
	case <-datetimeCh:
		return exitOK
	case err := <-copyDone:
		if err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}
}

func validateArgs(args []string) error {
	return validateArgsAt(args, time.Local)
}

func validateArgsAt(args []string, location *time.Location) error {
	if len(args) == 0 {
		return errors.New("missing event argument")
	}
	if strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("unexpected argument %q", args[0])
	}
	if len(args) > 1 {
		return fmt.Errorf("unexpected argument %q", args[1])
	}

	event := args[0]
	if strings.HasPrefix(event, "duration:") {
		_, err := parseDurationEvent(event)
		return err
	}
	if strings.HasPrefix(event, "datetime:") {
		_, err := parseDatetimeEvent(event, location)
		return err
	}
	if strings.HasPrefix(event, "file:") {
		if strings.TrimPrefix(event, "file:") == "" {
			return fmt.Errorf("invalid file event %q", event)
		}
		return nil
	}

	if event != "signal:USR1" && event != "signal:SIGUSR1" &&
		event != "signal:USR2" && event != "signal:SIGUSR2" {
		return fmt.Errorf("unsupported event %q", event)
	}
	if !signalEventSupported() {
		return fmt.Errorf("unsupported event %q on this platform", event)
	}

	return nil
}

func parseDurationEvent(event string) (time.Duration, error) {
	value := strings.TrimPrefix(event, "duration:")
	if strings.HasPrefix(value, "-") {
		return 0, fmt.Errorf("invalid duration %q: duration must not be negative", event)
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", event, err)
	}
	return duration, nil
}

func writeDiagnostic(errOut io.Writer, err error) {
	_, _ = fmt.Fprintf(errOut, "outage: %v\n", err)
}
