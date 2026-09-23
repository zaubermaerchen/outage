package main

// This file defines and renders outage's machine-readable CLI description.

import (
	"encoding/json"
	"io"
)

type description struct {
	SchemaVersion   int               `json:"schema_version"`
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	CLISchema       cliDescription    `json:"cli_schema"`
	StreamSemantics streamDescription `json:"stream_semantics"`
	StateMachine    stateDescription  `json:"state_machine"`
	SideEffects     []sideEffect      `json:"side_effects"`
}

type cliDescription struct {
	Options        []cliOptionDescription     `json:"options"`
	ConditionForms []conditionFormDescription `json:"condition_forms"`
	Arguments      []cliArgumentDescription   `json:"arguments"`
}

type cliOptionDescription struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Required   bool     `json:"required"`
	Repeatable bool     `json:"repeatable"`
	Aliases    []string `json:"aliases,omitempty"`
	Conflicts  []string `json:"conflicts,omitempty"`
	Standalone bool     `json:"standalone,omitempty"`
	Priority   string   `json:"priority,omitempty"`
}

type cliArgumentDescription struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Required     bool     `json:"required"`
	Repeatable   bool     `json:"repeatable"`
	Conflicts    []string `json:"conflicts,omitempty"`
	AndSeparator string   `json:"and_separator"`
	AndSemantics string   `json:"and_semantics"`
	OrOption     string   `json:"or_option"`
	OrSemantics  string   `json:"or_semantics"`
	OrForms      []string `json:"or_forms"`
}

// conditionFormDescription is the small, static public vocabulary for
// positional condition values. Pointer booleans preserve explicit false
// constraints while omitting constraints that do not apply to a form.
type conditionFormDescription struct {
	Name                            string   `json:"name"`
	Syntax                          []string `json:"syntax"`
	UnsupportedOn                   []string `json:"unsupported_on,omitempty"`
	NonEmpty                        *bool    `json:"non_empty,omitempty"`
	ValuePreserved                  *bool    `json:"value_preserved,omitempty"`
	Trigger                         string   `json:"trigger,omitempty"`
	ValueFormat                     string   `json:"value_format,omitempty"`
	NonNegative                     *bool    `json:"non_negative,omitempty"`
	ZeroAllowed                     *bool    `json:"zero_allowed,omitempty"`
	TimezoneLessTimezone            string   `json:"timezone_less_timezone,omitempty"`
	ExplicitTimezoneRequiresSeconds *bool    `json:"explicit_timezone_requires_seconds,omitempty"`
	Fractional                      *bool    `json:"fractional,omitempty"`
	IANA                            *bool    `json:"iana,omitempty"`
	TimezoneLessDSTGap              string   `json:"timezone_less_dst_gap,omitempty"`
	TimezoneLessDSTOverlap          string   `json:"timezone_less_dst_overlap,omitempty"`
}

func conditionBool(value bool) *bool { return &value }

type streamDescription struct {
	Stdin   streamInterfaceDescription `json:"stdin"`
	Stdout  streamInterfaceDescription `json:"stdout"`
	Stderr  streamInterfaceDescription `json:"stderr"`
	EventFD streamInterfaceDescription `json:"event_fd"`
}

type streamInterfaceDescription struct {
	Role        string `json:"role"`
	Description string `json:"description"`
	Format      string `json:"format,omitempty"`
	Option      string `json:"option,omitempty"`
}

type stateDescription struct {
	InitialState  string            `json:"initial_state"`
	States        []string          `json:"states"`
	Events        []string          `json:"events"`
	EventFDEvents []string          `json:"event_fd_events"`
	Transitions   []stateTransition `json:"transitions"`
}

type stateTransition struct {
	From  string `json:"from"`
	Event string `json:"event"`
	To    string `json:"to"`
}

type sideEffect struct{}

func describeRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--describe" {
			return true
		}
	}
	return false
}

func newDescription() description {
	return description{
		SchemaVersion: 1,
		Name:          "outage",
		Version:       version,
		CLISchema: cliDescription{
			Options: []cliOptionDescription{
				{
					Name:       "--describe",
					Type:       "boolean",
					Conflicts:  []string{"--version", "--events-fd", "--or", "CONDITION"},
					Standalone: true,
				},
				{
					Name:       "--help",
					Type:       "boolean",
					Repeatable: true,
					Aliases:    []string{"-h"},
					Priority:   "highest",
				},
				{
					Name:       "--version",
					Type:       "boolean",
					Conflicts:  []string{"--describe", "--events-fd", "--or", "CONDITION"},
					Standalone: true,
				},
				{
					Name:      "--events-fd",
					Type:      "fd",
					Conflicts: []string{"--describe", "--version"},
				},
				{
					Name:       "--or",
					Type:       "condition",
					Repeatable: true,
					Conflicts:  []string{"--describe", "--version"},
				},
			},
			ConditionForms: []conditionFormDescription{
				{
					Name:          "signal",
					Syntax:        []string{"signal:USR1", "signal:SIGUSR1", "signal:USR2", "signal:SIGUSR2"},
					UnsupportedOn: []string{"windows"},
				},
				{
					Name:           "file",
					Syntax:         []string{"file:<path>"},
					NonEmpty:       conditionBool(true),
					ValuePreserved: conditionBool(true),
					Trigger:        "path-resolves-to-regular-file",
				},
				{
					Name:        "duration",
					Syntax:      []string{"duration:<value>"},
					ValueFormat: "go-duration",
					NonNegative: conditionBool(true),
					ZeroAllowed: conditionBool(true),
				},
				{
					Name:                            "datetime",
					Syntax:                          []string{"datetime:YYYY-MM-DDTHH:MM", "datetime:YYYY-MM-DDTHH:MM:SS", "datetime:YYYY-MM-DDTHH:MM:SSZ", "datetime:YYYY-MM-DDTHH:MM:SS+HH:MM", "datetime:YYYY-MM-DDTHH:MM:SS-HH:MM"},
					TimezoneLessTimezone:            "process-startup",
					ExplicitTimezoneRequiresSeconds: conditionBool(true),
					Fractional:                      conditionBool(false),
					IANA:                            conditionBool(false),
					TimezoneLessDSTGap:              "invalid",
					TimezoneLessDSTOverlap:          "earliest-absolute-instant",
				},
			},
			Arguments: []cliArgumentDescription{
				{
					Name:         "CONDITION",
					Type:         "condition",
					Required:     true,
					Conflicts:    []string{"--describe", "--version"},
					AndSeparator: " && ",
					AndSemantics: "Members are separated by the exact literal \" && \" separator; operands are not trimmed and the separator is not escapable. Every member must be satisfied and remains latched.",
					OrOption:     "--or",
					OrSemantics:  "Each --or CONDITION or --or=CONDITION starts an alternative AND group; outage exits when any group is satisfied.",
					OrForms:      []string{"--or CONDITION", "--or=CONDITION"},
				},
			},
		},
		StreamSemantics: streamDescription{
			Stdin: streamInterfaceDescription{
				Role:        "input",
				Description: "Primary input stream read from stdin and forwarded until a condition cutoff, EOF, or an I/O error.",
			},
			Stdout: streamInterfaceDescription{
				Role:        "passthrough",
				Description: "Primary output stream containing the original input bytes byte-for-byte and in order until cutoff.",
			},
			Stderr: streamInterfaceDescription{
				Role:        "diagnostics",
				Description: "Human-readable diagnostics, warnings, and I/O or argument errors; it is separate from the passthrough data stream.",
			},
			EventFD: streamInterfaceDescription{
				Role:        "observation",
				Description: "Optional machine-readable lifecycle event stream written as JSONL when --events-fd is configured. Unix accepts only writable pipe, FIFO, or socket descriptors already configured with O_NONBLOCK; Windows accepts only writable pipe handles whose PIPE_NOWAIT mode and write access are verified at startup. Unsupported or unverifiable descriptors are configuration errors reported before stdin is read. The caller retains ownership and must preserve the required mode while outage runs. When the caller-supplied descriptor is writable and each event write succeeds, delivered records retain the condition-triggered then stream-cutoff order. Delivery is best-effort: an event write failure may omit remaining records; outage attempts one warning to stderr and disables further event records, and delivered records are never reordered. EOF and errors emit no event-FD records. A short write may leave an incomplete final JSONL record.",
				Format:      "jsonl",
				Option:      "--events-fd",
			},
		},
		StateMachine: stateDescription{
			InitialState:  "monitoring",
			States:        []string{"monitoring", "triggered", "cutoff", "eof", "error"},
			Events:        []string{"condition-triggered", "stream-cutoff", "eof", "error"},
			EventFDEvents: []string{"condition-triggered", "stream-cutoff"},
			Transitions: []stateTransition{
				{From: "monitoring", Event: "condition-triggered", To: "triggered"},
				{From: "triggered", Event: "stream-cutoff", To: "cutoff"},
				{From: "monitoring", Event: "eof", To: "eof"},
				{From: "monitoring", Event: "error", To: "error"},
			},
		},
		SideEffects: []sideEffect{},
	}
}

func printDescription(out io.Writer) error {
	return json.NewEncoder(out).Encode(newDescription())
}
