package main

// This file validates command-line arguments and coordinates event monitoring,
// version output, and stream copying.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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

	groups, err := parseConditionGroups(args)
	if err != nil {
		writeDiagnostic(errOut, err)
		return exitArgError
	}
	plan, err := canonicalConditionPlan(groups, clock.location)
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

func initialConditionSatisfaction(plan conditionPlan, startedAt time.Time, clock runtimeClock, checkFiles bool) (bool, error) {
	satisfied, err := initialConditionStates(plan, startedAt, clock, checkFiles)
	if err != nil {
		return false, err
	}
	return conditionPlanSatisfied(plan, satisfied), nil
}

func initialConditionStates(plan conditionPlan, startedAt time.Time, clock runtimeClock, checkFiles bool) ([]bool, error) {
	satisfied := make([]bool, len(plan.conditions))
	for index, raw := range plan.conditions {
		if strings.HasPrefix(raw, "file:") && !checkFiles {
			satisfied[index] = false
			continue
		}
		var err error
		satisfied[index], err = conditionAlreadySatisfied(raw, startedAt, clock)
		if err != nil {
			return nil, err
		}
	}
	return satisfied, nil
}

func conditionPlanSatisfied(plan conditionPlan, satisfied []bool) bool {
	for _, group := range plan.groups {
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

func conditionAlreadySatisfied(raw string, startedAt time.Time, clock runtimeClock) (bool, error) {
	if strings.HasPrefix(raw, "duration:") {
		duration, err := parseDurationEvent(raw)
		if err != nil {
			return false, err
		}
		return duration <= 0 || clock.now().Sub(startedAt) >= duration, nil
	}
	if strings.HasPrefix(raw, "datetime:") {
		deadline, err := parseDatetimeEvent(raw, clock.location)
		if err != nil {
			return false, err
		}
		return !deadline.After(clock.now()), nil
	}
	if strings.HasPrefix(raw, "file:") {
		regular, err := isRegularFile(strings.TrimPrefix(raw, "file:"))
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

func buildConditionTree(plan conditionPlan, startedAt time.Time, clock runtimeClock, initialSatisfied []bool) (condition.Condition, error) {
	runtime := condition.Runtime{
		Now:          clock.now,
		NewTimer:     clock.newTimer,
		PollInterval: filePollInterval,
	}
	options := []condition.Option{
		condition.WithRuntime(runtime),
		condition.WithStartTime(startedAt),
	}
	leaves := make([]condition.Condition, len(plan.conditions))
	for index, raw := range plan.conditions {
		identity, err := canonicalConditionIdentity(raw, clock.location)
		if err != nil {
			return nil, err
		}
		id := conditionIdentityID(identity)
		if initialSatisfied[index] {
			// Initial observations are latched before monitors start; represent
			// them as a typed one-shot leaf so a source changing during setup
			// cannot erase an already satisfied AND member.
			leaves[index] = condition.NewDuration(id, 0, condition.WithRuntime(runtime))
			continue
		}
		switch identity.kind {
		case "duration":
			duration, err := parseDurationEvent(raw)
			if err != nil {
				return nil, err
			}
			leaves[index] = condition.NewDuration(id, duration, options...)
		case "datetime":
			deadline, err := parseDatetimeEvent(raw, clock.location)
			if err != nil {
				return nil, err
			}
			leaves[index] = condition.NewDateTime(id, deadline, options...)
		case "file":
			path := strings.TrimPrefix(raw, "file:")
			leaves[index] = condition.NewFile(id, path, options...)
		case "signal":
			leaves[index], err = newSignalCondition(id, raw, options...)
			if err != nil {
				return nil, err
			}
		}
	}

	groups := make([]condition.Condition, 0, len(plan.groups))
	for index, members := range plan.groups {
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

func conditionIdentityID(identity conditionIdentity) string {
	return identity.kind + ":" + identity.value
}

type conditionIdentity struct {
	kind  string
	value string
}

type conditionGroup struct {
	members []string
}

type conditionPlan struct {
	conditions []string
	groups     [][]int
}

func parseConditionGroups(args []string) ([]conditionGroup, error) {
	values, err := parseConditionArguments(args)
	if err != nil {
		return nil, err
	}
	groups := make([]conditionGroup, 0, len(values))
	for _, value := range values {
		groups = append(groups, conditionGroup{
			members: strings.Split(value, " && "),
		})
	}
	return groups, nil
}

func parseConditionArguments(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("missing condition argument")
	}

	values := make([]string, 0, len(args))
	hasCondition := false
	pendingOR := false
	for _, arg := range args {
		switch {
		case arg == "--or":
			if !hasCondition {
				return nil, errors.New("--or requires a preceding condition")
			}
			if pendingOR {
				return nil, errors.New("--or requires a condition")
			}
			pendingOR = true
		case strings.HasPrefix(arg, "--or="):
			if !hasCondition {
				return nil, errors.New("--or requires a preceding condition")
			}
			if pendingOR {
				return nil, errors.New("--or requires a condition")
			}
			value := strings.TrimPrefix(arg, "--or=")
			if value == "" {
				return nil, errors.New("--or requires a condition")
			}
			values = append(values, value)
		case strings.HasPrefix(arg, "-"):
			if pendingOR {
				return nil, errors.New("--or requires a condition")
			}
			return nil, fmt.Errorf("unexpected argument %q", arg)
		default:
			if !hasCondition {
				values = append(values, arg)
				hasCondition = true
				continue
			}
			if !pendingOR {
				return nil, fmt.Errorf("unexpected argument %q", arg)
			}
			values = append(values, arg)
			pendingOR = false
		}
	}
	if pendingOR {
		return nil, errors.New("--or requires a condition")
	}
	return values, nil
}

func canonicalConditionPlan(groups []conditionGroup, location *time.Location) (conditionPlan, error) {
	plan := conditionPlan{
		conditions: make([]string, 0),
		groups:     make([][]int, 0, len(groups)),
	}
	conditionIndexes := make(map[conditionIdentity]int)
	for _, group := range groups {
		members := group.members
		groupIndexes := make([]int, 0, len(members))
		seen := make(map[conditionIdentity]struct{}, len(members))
		for _, condition := range members {
			identity, err := canonicalConditionIdentity(condition, location)
			if err != nil {
				return conditionPlan{}, err
			}
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			index, exists := conditionIndexes[identity]
			if !exists {
				index = len(plan.conditions)
				conditionIndexes[identity] = index
				plan.conditions = append(plan.conditions, condition)
			}
			groupIndexes = append(groupIndexes, index)
		}
		plan.groups = append(plan.groups, groupIndexes)
	}
	return plan, nil
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
	if signal, ok := canonicalSignalName(condition); ok {
		return conditionIdentity{kind: "signal", value: signal}, nil
	}
	return conditionIdentity{kind: "signal", value: condition}, nil
}

func validateArgs(args []string) error {
	return validateArgsAt(args, time.Local)
}

func validateArgsAt(args []string, location *time.Location) error {
	return validateArgsAtWithSignalSupport(args, location, signalEventSupported)
}

// validateArgsAtWithSignalSupport keeps syntax errors ahead of platform
// capability errors. This ensures a malformed later member is diagnosed even
// when an earlier signal is unsupported on the current platform.
func validateArgsAtWithSignalSupport(args []string, location *time.Location, signalSupported func() bool) error {
	groups, err := parseConditionGroups(args)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, condition := range group.members {
			if err := validateConditionSyntaxAt(condition, location); err != nil {
				return err
			}
		}
	}
	for _, group := range groups {
		for _, condition := range group.members {
			if err := validateConditionCapabilityAt(condition, signalSupported); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConditionSyntaxAt(event string, location *time.Location) error {
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

	if !isSignalEvent(event) {
		return fmt.Errorf("unsupported event %q", event)
	}

	return nil
}

func validateConditionCapabilityAt(event string, signalSupported func() bool) error {
	if !isSignalEvent(event) {
		return nil
	}
	if signalSupported == nil {
		signalSupported = signalEventSupported
	}
	if !signalSupported() {
		return fmt.Errorf("unsupported event %q on this platform", event)
	}

	return nil
}

func isSignalEvent(event string) bool {
	_, ok := canonicalSignalName(event)
	return ok
}

func canonicalSignalName(event string) (string, bool) {
	switch event {
	case "signal:USR1", "signal:SIGUSR1":
		return "USR1", true
	case "signal:USR2", "signal:SIGUSR2":
		return "USR2", true
	default:
		return "", false
	}
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
