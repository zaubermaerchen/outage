package main

// This file validates command-line arguments and coordinates event monitoring,
// version output, and stream copying.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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
       outage datetime:YYYY-MM-DDTHH:MM:SS-HH:MM
       outage 'signal:USR1 && file:/tmp/stop'

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
Conditions may be combined with the exact literal " && " separator. Every
condition must be satisfied; satisfied conditions remain latched, and need not
occur simultaneously. Operands are not trimmed. Quote a combined expression
for the shell. Leading, trailing, or consecutive separators are invalid; only
AND combinations are supported.

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

	conditions, err := canonicalConditionGroups(strings.Split(args[0], " && "), clock.location)
	if err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}
	notifications := make(chan int, len(conditions))
	satisfied := make([]bool, len(conditions))
	stops := make([]func(), 0, len(conditions))
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	pending := len(conditions)
	for index, condition := range conditions {
		isSatisfied, stop, err := installCondition(condition.value, index, startedAt, clock, notifications)
		if err != nil {
			writeDiagnostic(errOut, err)
			return exitArgError
		}
		if isSatisfied {
			satisfied[index] = true
			pending--
			continue
		}
		if stop != nil {
			stops = append(stops, stop)
		}
	}
	if pending == 0 {
		return exitOK
	}

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, in)
		copyDone <- err
	}()

	for pending > 0 {
		select {
		case index := <-notifications:
			if satisfied[index] {
				continue
			}
			satisfied[index] = true
			pending--
		case err := <-copyDone:
			if err != nil {
				writeDiagnostic(errOut, err)
				return exitCopyError
			}
			return exitOK
		}
	}
	return exitOK
}

type conditionIdentity struct {
	kind  string
	value string
}

type conditionGroup struct {
	value string
}

func canonicalConditionGroups(conditions []string, location *time.Location) ([]conditionGroup, error) {
	groups := make([]conditionGroup, 0, len(conditions))
	seen := make(map[conditionIdentity]struct{}, len(conditions))
	for _, condition := range conditions {
		identity, err := canonicalConditionIdentity(condition, location)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		groups = append(groups, conditionGroup{value: condition})
	}
	return groups, nil
}

func canonicalConditionIdentity(condition string, location *time.Location) (conditionIdentity, error) {
	if strings.HasPrefix(condition, "duration:") {
		duration, err := parseDurationEvent(condition)
		if err != nil {
			return conditionIdentity{}, err
		}
		return conditionIdentity{kind: "duration", value: fmt.Sprintf("%d", duration)}, nil
	}
	if strings.HasPrefix(condition, "datetime:") {
		deadline, err := parseDatetimeEvent(condition, location)
		if err != nil {
			return conditionIdentity{}, err
		}
		return conditionIdentity{kind: "datetime", value: deadline.UTC().Format(time.RFC3339)}, nil
	}
	if strings.HasPrefix(condition, "file:") {
		return conditionIdentity{kind: "file", value: strings.TrimPrefix(condition, "file:")}, nil
	}
	switch condition {
	case "signal:USR1", "signal:SIGUSR1":
		return conditionIdentity{kind: "signal", value: "USR1"}, nil
	case "signal:USR2", "signal:SIGUSR2":
		return conditionIdentity{kind: "signal", value: "USR2"}, nil
	default:
		return conditionIdentity{kind: "signal", value: condition}, nil
	}
}

// installCondition preserves each event monitor's existing semantics while
// forwarding its first observation into the shared AND aggregation channel.
// A small bridge is needed because the existing monitors intentionally expose
// different channel element types (signals versus timer values).
func installCondition(condition string, index int, startedAt time.Time, clock runtimeClock, notifications chan<- int) (bool, func(), error) {
	if strings.HasPrefix(condition, "duration:") {
		duration, err := parseDurationEvent(condition)
		if err != nil {
			return false, nil, err
		}
		if duration == 0 {
			return true, nil, nil
		}
		remaining := duration - clock.now().Sub(startedAt)
		if remaining <= 0 {
			return true, nil, nil
		}
		durationCh, stop := clock.newTimer(remaining)
		if durationCh == nil {
			if stop != nil {
				stop()
			}
			return true, nil, nil
		}
		ignoreSIGPIPE()
		return false, bridgeCondition(durationCh, index, notifications, stop), nil
	}

	if strings.HasPrefix(condition, "datetime:") {
		deadline, err := parseDatetimeEvent(condition, clock.location)
		if err != nil {
			return false, nil, err
		}
		if !deadline.After(startedAt) {
			return true, nil, nil
		}
		datetimeCh, stop := startDeadlineMonitor(&deadline, clock.now, clock.newTimer)
		if datetimeCh == nil {
			return true, nil, nil
		}
		ignoreSIGPIPE()
		return false, bridgeCondition(datetimeCh, index, notifications, stop), nil
	}

	if strings.HasPrefix(condition, "file:") {
		path := strings.TrimPrefix(condition, "file:")
		if _, err := os.Lstat(path); err == nil {
			return true, nil, nil
		} else if !os.IsNotExist(err) {
			return false, nil, err
		}
		ignoreSIGPIPE()
		eventCh, stop := installFileMonitor(path)
		return false, bridgeCondition(eventCh, index, notifications, stop), nil
	}

	eventCh, stop := installSignalMonitor(condition)
	return false, bridgeCondition(eventCh, index, notifications, stop), nil
}

func bridgeCondition[T any](source <-chan T, index int, notifications chan<- int, stopSource func()) func() {
	done := make(chan struct{})
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		select {
		case _, ok := <-source:
			if !ok {
				return
			}
			select {
			case notifications <- index:
			case <-done:
			}
		case <-done:
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			close(done)
			if stopSource != nil {
				stopSource()
			}
			<-forwarded
		})
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
	for _, condition := range strings.Split(event, " && ") {
		if err := validateConditionAt(condition, location); err != nil {
			return err
		}
	}
	return nil
}

func validateConditionAt(event string, location *time.Location) error {
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
