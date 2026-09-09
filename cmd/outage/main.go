package main

// This file validates command-line arguments and coordinates event monitoring,
// version output, and stream copying.

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zaubermaerchen/outage/internal/cli"
	"github.com/zaubermaerchen/outage/internal/condition"
)

const (
	exitOK        = 0
	exitCopyError = 1
	exitArgError  = 2
)

const helpText = `Usage: outage CONDITION [--or CONDITION]...
Usage: outage signal:USR1
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
       outage 'signal:USR1 && file:/tmp/stop' --or duration:30s

Copy stdin to stdout until the event is received. Receiving the event exits outage;
it does not send a signal directly to the producer.
Signal events are unsupported on Windows.
File events exit when the specified path resolves to a regular file.
Duration events use Go duration syntax and exit after the specified time has elapsed.
Datetime events use the process-local wall clock captured at startup and begin
monitoring immediately. A datetime already reached exits without reading stdin;
DST gaps and malformed values are invalid. RFC3339 timezone-qualified values
require seconds and use numeric offsets or Z; IANA timezone names and fractional
values are invalid.
For an ambiguous DST overlap, the earlier absolute occurrence is selected.
Conditions may be combined with the exact literal " && " separator inside one
AND group. Every member of an AND group must be satisfied; satisfied conditions
remain latched, and need not occur simultaneously. Use --or between alternative
groups; outage exits when any group is satisfied. Operands are not trimmed.
Quote a combined expression for the shell. Leading, trailing, or consecutive
separators are invalid.

Arguments:
  signal:USR1                Exit on USR1 (signal:SIGUSR1 is an alias).
  signal:USR2                Exit on USR2 (signal:SIGUSR2 is an alias).
  file:<path>                Exit when the specified path resolves to a regular file.
  duration:<value>           Exit after the duration has elapsed.
  datetime:YYYY-MM-DDTHH:MM[:SS]
                             Exit when the local wall clock reaches the datetime
                             (seconds may be omitted).
  datetime:YYYY-MM-DDTHH:MM:SS[Z|+HH:MM|-HH:MM]
                             RFC3339 form; explicit timezones require seconds.
Options:
  --or CONDITION            Use CONDITION as an alternative group. May be
                            written as --or=CONDITION.
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

	if cli.HelpRequested(args) {
		ignoreSIGPIPE()
		if _, err := fmt.Fprint(out, helpText); err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}

	if cli.VersionRequested(args) {
		ignoreSIGPIPE()
		if _, err := fmt.Fprintf(out, "outage %s\n", version); err != nil {
			writeDiagnostic(errOut, err)
			return exitCopyError
		}
		return exitOK
	}

	plan, err := cli.Parse(args, clock.location)
	if err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}
	initialSatisfied, err := initialConditionStates(plan, startedAt, clock, true)
	if err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}
	satisfied := conditionPlanSatisfied(plan, initialSatisfied)
	root, err := buildConditionTree(plan, startedAt, clock, initialSatisfied)
	if err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}
	ignoreSIGPIPE()
	monitorContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := root.Run(monitorContext, condition.Once)
	if satisfied {
		// Start every monitor before returning so alternative timers and signal
		// subscriptions are cleaned up by the deferred context cancellation.
		return exitOK
	}
	// Parsing and monitor setup can consume enough time for a deadline or
	// duration to become satisfied before stdin forwarding starts. Recheck the
	// typed conditions while no copy goroutine exists so an immediate event
	// cannot race an input error.
	select {
	case _, ok := <-events:
		if ok {
			return exitOK
		}
		return exitOK
	default:
	}
	satisfied, err = initialConditionSatisfaction(plan, startedAt, clock, false)
	if err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}
	if satisfied {
		return exitOK
	}

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, in)
		copyDone <- err
	}()

	for {
		select {
		case _, ok := <-events:
			if !ok {
				return exitOK
			}
			return exitOK
		case err := <-copyDone:
			if err != nil {
				writeDiagnostic(errOut, err)
				return exitCopyError
			}
			return exitOK
		}
	}
}

func initialConditionSatisfaction(plan cli.Plan, startedAt time.Time, clock runtimeClock, checkFiles bool) (bool, error) {
	satisfied, err := initialConditionStates(plan, startedAt, clock, checkFiles)
	if err != nil {
		return false, err
	}
	return conditionPlanSatisfied(plan, satisfied), nil
}

func initialConditionStates(plan cli.Plan, startedAt time.Time, clock runtimeClock, checkFiles bool) ([]bool, error) {
	satisfied := make([]bool, len(plan.Conditions))
	for index, spec := range plan.Conditions {
		if spec.Kind == cli.FileKind && !checkFiles {
			satisfied[index] = false
			continue
		}
		var err error
		satisfied[index], err = conditionAlreadySatisfied(spec, startedAt, clock)
		if err != nil {
			return nil, err
		}
	}
	return satisfied, nil
}

func conditionPlanSatisfied(plan cli.Plan, satisfied []bool) bool {
	for _, group := range plan.Groups {
		complete := true
		for _, index := range group {
			if !satisfied[index] {
				complete = false
				break
			}
		}
		if complete {
			return true
		}
	}
	return false
}

func conditionAlreadySatisfied(spec cli.ConditionSpec, startedAt time.Time, clock runtimeClock) (bool, error) {
	switch spec.Kind {
	case cli.DurationKind:
		return spec.Duration <= 0 || clock.now().Sub(startedAt) >= spec.Duration, nil
	case cli.DateTimeKind:
		return !spec.Deadline.After(clock.now()), nil
	case cli.FileKind:
		regular, err := isRegularFile(spec.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		return regular, nil
	}
	return false, nil
}

func buildConditionTree(plan cli.Plan, startedAt time.Time, clock runtimeClock, initialSatisfied []bool) (condition.Condition, error) {
	runtime := condition.Runtime{
		Now:          clock.now,
		NewTimer:     clock.newTimer,
		PollInterval: filePollInterval,
	}
	options := []condition.Option{
		condition.WithRuntime(runtime),
		condition.WithStartTime(startedAt),
	}
	leaves := make([]condition.Condition, len(plan.Conditions))
	for index, spec := range plan.Conditions {
		id := spec.ID
		if initialSatisfied[index] {
			// Initial observations are latched before monitors start; represent
			// them as a typed one-shot leaf so a source changing during setup
			// cannot erase an already satisfied AND member.
			leaves[index] = condition.NewDuration(id, 0, condition.WithRuntime(runtime))
			continue
		}
		switch spec.Kind {
		case cli.DurationKind:
			leaves[index] = condition.NewDuration(id, spec.Duration, options...)
		case cli.DateTimeKind:
			leaves[index] = condition.NewDateTime(id, spec.Deadline, options...)
		case cli.FileKind:
			leaves[index] = condition.NewFile(id, spec.Path, options...)
		case cli.SignalKind:
			leaves[index] = condition.NewSignal(id, spec.Signal, options...)
		}
	}

	groups := make([]condition.Condition, 0, len(plan.Groups))
	for index, members := range plan.Groups {
		children := make([]condition.Condition, 0, len(members))
		for _, member := range members {
			children = append(children, leaves[member])
		}
		if len(children) == 1 {
			groups = append(groups, children[0])
		} else {
			groups = append(groups, condition.And(fmt.Sprintf("and:%d", index), children...))
		}
	}
	if len(groups) == 1 {
		return groups[0], nil
	}
	return condition.Or("or:root", groups...), nil
}

func writeDiagnostic(errOut io.Writer, err error) {
	_, _ = fmt.Fprintf(errOut, "outage: %v\n", err)
}
