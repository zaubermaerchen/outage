package cli

// This package parses outage's command-line conditions into typed monitoring
// inputs. It is intentionally outage-specific and is not a reusable CLI
// framework.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Kind identifies the source represented by a ConditionSpec.
type Kind string

const (
	// SignalKind identifies a Unix signal source.
	SignalKind Kind = "signal"
	// FileKind identifies a regular-file source.
	FileKind Kind = "file"
	// DurationKind identifies an elapsed-duration source.
	DurationKind Kind = "duration"
	// DateTimeKind identifies an absolute-deadline source.
	DateTimeKind Kind = "datetime"
)

// ConditionSpec is a validated, canonical condition value ready for
// construction by the application. Exactly one typed value is populated
// according to Kind.
type ConditionSpec struct {
	ID       string
	Kind     Kind
	Signal   os.Signal
	Path     string
	Duration time.Duration
	Deadline time.Time
}

// Plan is the canonical condition tree description produced from CLI
// arguments. Groups contain indexes into Conditions and are OR alternatives;
// members within each group are AND requirements.
type Plan struct {
	Conditions []ConditionSpec
	Groups     [][]int
}

// HelpRequested reports whether the command-line invocation contains either
// supported help spelling. Help takes priority over every other argument.
func HelpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// VersionRequested reports whether the invocation is the standalone version
// command. Other arguments make --version an unexpected condition argument.
func VersionRequested(args []string) bool {
	return len(args) == 1 && args[0] == "--version"
}

// Parse validates outage arguments, resolves CLI values into typed condition
// specs, canonicalizes equivalent spellings, and removes duplicate members.
// Syntax diagnostics are returned before platform capability diagnostics.
func Parse(args []string, location *time.Location) (Plan, error) {
	return parseWithSignalSupport(args, location, signalSupported)
}

func parseWithSignalSupport(args []string, location *time.Location, supported func() bool) (Plan, error) {
	groups, err := parseConditionGroups(args)
	if err != nil {
		return Plan{}, err
	}
	for _, group := range groups {
		for _, condition := range group.members {
			if err := validateConditionSyntaxAt(condition, location); err != nil {
				return Plan{}, err
			}
		}
	}
	for _, group := range groups {
		for _, condition := range group.members {
			if err := validateConditionCapabilityAt(condition, supported); err != nil {
				return Plan{}, err
			}
		}
	}
	return canonicalPlan(groups, location)
}

type conditionGroup struct {
	members []string
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

type conditionIdentity struct {
	kind  Kind
	value string
}

func canonicalPlan(groups []conditionGroup, location *time.Location) (Plan, error) {
	plan := Plan{
		Conditions: make([]ConditionSpec, 0),
		Groups:     make([][]int, 0, len(groups)),
	}
	conditionIndexes := make(map[conditionIdentity]int)
	for _, group := range groups {
		groupIndexes := make([]int, 0, len(group.members))
		seen := make(map[conditionIdentity]struct{}, len(group.members))
		for _, raw := range group.members {
			spec, identity, err := parseConditionSpec(raw, location)
			if err != nil {
				return Plan{}, err
			}
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			index, exists := conditionIndexes[identity]
			if !exists {
				index = len(plan.Conditions)
				conditionIndexes[identity] = index
				plan.Conditions = append(plan.Conditions, spec)
			}
			groupIndexes = append(groupIndexes, index)
		}
		plan.Groups = append(plan.Groups, groupIndexes)
	}
	return plan, nil
}

func parseConditionSpec(raw string, location *time.Location) (ConditionSpec, conditionIdentity, error) {
	if strings.HasPrefix(raw, "duration:") {
		duration, err := parseDurationEvent(raw)
		if err != nil {
			return ConditionSpec{}, conditionIdentity{}, err
		}
		identity := conditionIdentity{kind: DurationKind, value: fmt.Sprintf("%d", duration)}
		return ConditionSpec{
			ID:       identityID(identity),
			Kind:     DurationKind,
			Duration: duration,
		}, identity, nil
	}
	if strings.HasPrefix(raw, "datetime:") {
		deadline, err := parseDatetimeEvent(raw, location)
		if err != nil {
			return ConditionSpec{}, conditionIdentity{}, err
		}
		identity := conditionIdentity{kind: DateTimeKind, value: deadline.UTC().Format(time.RFC3339)}
		return ConditionSpec{
			ID:       identityID(identity),
			Kind:     DateTimeKind,
			Deadline: deadline,
		}, identity, nil
	}
	if strings.HasPrefix(raw, "file:") {
		path := strings.TrimPrefix(raw, "file:")
		identity := conditionIdentity{kind: FileKind, value: path}
		return ConditionSpec{
			ID:   identityID(identity),
			Kind: FileKind,
			Path: path,
		}, identity, nil
	}

	signalName, ok := canonicalSignalName(raw)
	if !ok {
		return ConditionSpec{}, conditionIdentity{}, fmt.Errorf("unsupported event %q", raw)
	}
	signal, ok := resolveSignal(signalName)
	if !ok {
		return ConditionSpec{}, conditionIdentity{}, fmt.Errorf("unsupported event %q on this platform", raw)
	}
	identity := conditionIdentity{kind: SignalKind, value: signalName}
	return ConditionSpec{
		ID:     identityID(identity),
		Kind:   SignalKind,
		Signal: signal,
	}, identity, nil
}

func identityID(identity conditionIdentity) string {
	return string(identity.kind) + ":" + identity.value
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

func validateConditionCapabilityAt(event string, supported func() bool) error {
	if !isSignalEvent(event) {
		return nil
	}
	if supported == nil {
		supported = signalSupported
	}
	if !supported() {
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
