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
	Options   []cliOptionDescription   `json:"options"`
	Arguments []cliArgumentDescription `json:"arguments"`
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
}

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
	InitialState string            `json:"initial_state"`
	InitialEvent string            `json:"initial_event"`
	States       []string          `json:"states"`
	Events       []string          `json:"events"`
	EventFDOrder []string          `json:"event_fd_order"`
	Transitions  []stateTransition `json:"transitions"`
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
			Arguments: []cliArgumentDescription{
				{
					Name:         "CONDITION",
					Type:         "condition",
					Required:     true,
					Conflicts:    []string{"--describe", "--version"},
					AndSeparator: " && ",
					AndSemantics: "Members separated by the exact literal separator are AND requirements; every member must be satisfied and remains latched.",
					OrOption:     "--or",
					OrSemantics:  "Each --or CONDITION starts an alternative AND group; outage exits when any group is satisfied.",
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
				Description: "Optional machine-readable lifecycle event stream written as JSONL when --events-fd is configured. When the caller-supplied descriptor is writable and each event write succeeds, any delivered records retain the condition-triggered then stream-cutoff order. A setup or event write failure may omit remaining records; outage writes one warning to stderr and disables further event records, so delivery is best-effort but records are never reordered. EOF and errors emit no lifecycle records.",
				Format:      "jsonl",
				Option:      "--events-fd",
			},
		},
		StateMachine: stateDescription{
			InitialState: "monitoring",
			InitialEvent: "monitoring",
			States:       []string{"monitoring", "triggered", "cutoff", "eof", "error"},
			Events:       []string{"monitoring", "condition-triggered", "stream-cutoff", "eof", "error"},
			EventFDOrder: []string{"condition-triggered", "stream-cutoff"},
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
