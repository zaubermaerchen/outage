package main

// This file verifies the machine-readable self-description contract.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunDescribeEmitsCompactMetadataWithoutRuntimeProcessing(t *testing.T) {
	var output, diagnostics bytes.Buffer
	nowCalled := false
	timerCreated := false
	clock := runtimeClock{
		now: func() time.Time {
			nowCalled = true
			return time.Unix(0, 0)
		},
		location: time.UTC,
		newTimer: func(time.Duration) (<-chan time.Time, func()) {
			timerCreated = true
			return make(chan time.Time), func() {}
		},
	}

	if got := runWithClock([]string{"--describe"}, outageDescriptionPanicReader{}, &output, &diagnostics, clock); got != exitOK {
		t.Fatalf("runWithClock() exit code = %d, want %d; diagnostics = %q", got, exitOK, diagnostics.String())
	}
	if nowCalled {
		t.Fatal("--describe called the runtime clock")
	}
	if timerCreated {
		t.Fatal("--describe created a condition timer")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}

	rawOutput := append([]byte(nil), output.Bytes()...)
	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(rawOutput))
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode --describe output: %v; output = %q", err, rawOutput)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("--describe output has trailing JSON/data: %v; output = %q", err, rawOutput)
	}
	if strings.Count(string(rawOutput), "\n") != 1 {
		t.Fatalf("--describe output = %q, want exactly one JSONL line", rawOutput)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimSuffix(rawOutput, []byte{'\n'})); err != nil {
		t.Fatalf("compact --describe output: %v", err)
	}
	if !bytes.Equal(compact.Bytes(), bytes.TrimSuffix(rawOutput, []byte{'\n'})) {
		t.Fatalf("--describe output = %q, want compact JSON", rawOutput)
	}

	wantFields := []string{
		"schema_version",
		"name",
		"version",
		"cli_schema",
		"stream_semantics",
		"state_machine",
		"side_effects",
	}
	if len(document) != len(wantFields) {
		t.Fatalf("top-level field count = %d, want %d: %#v", len(document), len(wantFields), document)
	}
	for _, field := range wantFields {
		if _, ok := document[field]; !ok {
			t.Errorf("description missing top-level field %q", field)
		}
	}

	var schemaVersion int
	if err := json.Unmarshal(document["schema_version"], &schemaVersion); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if schemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", schemaVersion)
	}
	var name string
	if err := json.Unmarshal(document["name"], &name); err != nil {
		t.Fatalf("name: %v", err)
	}
	if name != "outage" {
		t.Fatalf("name = %q, want outage", name)
	}
	var versionValue string
	if err := json.Unmarshal(document["version"], &versionValue); err != nil {
		t.Fatalf("version: %v", err)
	}
	if versionValue != version {
		t.Fatalf("version = %q, want %q", versionValue, version)
	}
}

func TestRunDescribeHelpHasPriority(t *testing.T) {
	var describeOutput, describeDiagnostics bytes.Buffer
	if got := run([]string{"--describe", "--help"}, outageDescriptionPanicReader{}, &describeOutput, &describeDiagnostics); got != exitOK {
		t.Fatalf("run() exit code = %d, want %d; diagnostics = %q", got, exitOK, describeDiagnostics.String())
	}
	if describeOutput.String() != helpText {
		t.Fatalf("output = %q, want help output", describeOutput.String())
	}
	if describeDiagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", describeDiagnostics.String())
	}
}

func TestRunDescribeRejectsCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--describe", "duration:1s"},
		{"duration:1s", "--describe"},
		{"--describe", "--events-fd", "3"},
		{"--events-fd", "3", "--describe"},
		{"--describe", "--version"},
		{"--describe", "--describe"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			if got := run(args, outageDescriptionPanicReader{}, &output, &diagnostics); got != exitArgError {
				t.Fatalf("run() exit code = %d, want %d; output = %q; diagnostics = %q", got, exitArgError, output.String(), diagnostics.String())
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want empty", output.String())
			}
			if !strings.Contains(diagnostics.String(), "--describe") {
				t.Fatalf("diagnostics = %q, want --describe combination error", diagnostics.String())
			}
		})
	}
}

func TestRunDescribeReportsOutputError(t *testing.T) {
	wantErr := "description output unavailable"
	var diagnostics bytes.Buffer
	if got := run([]string{"--describe"}, outageDescriptionPanicReader{}, failingWriter{err: errors.New(wantErr)}, &diagnostics); got != exitCopyError {
		t.Fatalf("run() exit code = %d, want %d", got, exitCopyError)
	}
	if got := diagnostics.String(); !strings.Contains(got, wantErr) {
		t.Fatalf("diagnostics = %q, want %q", got, wantErr)
	}
}

func TestRunDescribeMetadataIsComplete(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if got := run([]string{"--describe"}, outageDescriptionPanicReader{}, &output, &diagnostics); got != exitOK {
		t.Fatalf("run() exit code = %d, want %d; diagnostics = %q", got, exitOK, diagnostics.String())
	}

	var document struct {
		CLISchema struct {
			Options        []outageDescriptionOption        `json:"options"`
			ConditionForms []outageDescriptionConditionForm `json:"condition_forms"`
			Arguments      []struct {
				Name         string   `json:"name"`
				Type         string   `json:"type"`
				Required     bool     `json:"required"`
				Repeatable   bool     `json:"repeatable"`
				Conflicts    []string `json:"conflicts"`
				AndSeparator string   `json:"and_separator"`
				AndSemantics string   `json:"and_semantics"`
				OrOption     string   `json:"or_option"`
				OrSemantics  string   `json:"or_semantics"`
				OrForms      []string `json:"or_forms"`
			} `json:"arguments"`
		} `json:"cli_schema"`
		StreamSemantics map[string]outageDescriptionStream `json:"stream_semantics"`
		StateMachine    outageDescriptionState             `json:"state_machine"`
		SideEffects     []json.RawMessage                  `json:"side_effects"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decode --describe output: %v; output = %q", err, output.String())
	}

	wantTypes := map[string]string{
		"--describe":  "boolean",
		"--help":      "boolean",
		"--version":   "boolean",
		"--events-fd": "fd",
		"--or":        "condition",
	}
	if len(document.CLISchema.Options) != len(wantTypes) {
		t.Fatalf("option count = %d, want %d", len(document.CLISchema.Options), len(wantTypes))
	}
	seen := make(map[string]bool, len(document.CLISchema.Options))
	for _, option := range document.CLISchema.Options {
		if seen[option.Name] {
			t.Fatalf("duplicate option %q", option.Name)
		}
		seen[option.Name] = true
		if want, ok := wantTypes[option.Name]; !ok {
			t.Fatalf("unexpected option %q", option.Name)
		} else if option.Type != want {
			t.Errorf("option %s type = %q, want %q", option.Name, option.Type, want)
		}
		if option.Required {
			t.Errorf("option %s required = true, want false", option.Name)
		}
		if option.Name == "--or" && !option.Repeatable {
			t.Error("--or repeatable = false, want true")
		}
		if option.Name == "--help" && !equalOutageDescriptionStrings(option.Aliases, []string{"-h"}) {
			t.Errorf("--help aliases = %#v, want [-h]", option.Aliases)
		}
	}
	for name := range wantTypes {
		if !seen[name] {
			t.Errorf("missing option %q", name)
		}
	}
	if len(document.CLISchema.Arguments) != 1 {
		t.Fatalf("argument count = %d, want 1", len(document.CLISchema.Arguments))
	}
	argument := document.CLISchema.Arguments[0]
	if argument.Name != "CONDITION" || argument.Type != "condition" || !argument.Required || argument.Repeatable {
		t.Fatalf("condition argument = %#v, want required non-repeatable CONDITION", argument)
	}
	if argument.AndSeparator != " && " || argument.AndSemantics == "" || argument.OrOption != "--or" || argument.OrSemantics == "" {
		t.Fatalf("condition composition = %#v, want exact AND/OR rules", argument)
	}
	if !equalOutageDescriptionStrings(argument.OrForms, []string{"--or CONDITION", "--or=CONDITION"}) {
		t.Fatalf("condition OR forms = %#v, want --or CONDITION/--or=CONDITION", argument.OrForms)
	}
	for _, want := range []string{"exact literal", "not trimmed", "not escapable"} {
		if !strings.Contains(strings.ToLower(argument.AndSemantics), strings.ToLower(want)) {
			t.Errorf("AND semantics = %q, want %q", argument.AndSemantics, want)
		}
	}
	for _, want := range []string{"--or CONDITION", "--or=CONDITION"} {
		if !strings.Contains(argument.OrSemantics, want) {
			t.Errorf("OR semantics = %q, want %q", argument.OrSemantics, want)
		}
	}

	wantConditionForms := []outageDescriptionConditionForm{
		{
			Name:          "signal",
			Syntax:        []string{"signal:USR1", "signal:SIGUSR1", "signal:USR2", "signal:SIGUSR2"},
			UnsupportedOn: []string{"windows"},
		},
		{
			Name:           "file",
			Syntax:         []string{"file:<path>"},
			NonEmpty:       boolPointer(true),
			ValuePreserved: boolPointer(true),
			Trigger:        "path-resolves-to-regular-file",
		},
		{
			Name:        "duration",
			Syntax:      []string{"duration:<value>"},
			ValueFormat: "go-duration",
			NonNegative: boolPointer(true),
			ZeroAllowed: boolPointer(true),
		},
		{
			Name:                            "datetime",
			Syntax:                          []string{"datetime:YYYY-MM-DDTHH:MM", "datetime:YYYY-MM-DDTHH:MM:SS", "datetime:YYYY-MM-DDTHH:MM:SSZ", "datetime:YYYY-MM-DDTHH:MM:SS+HH:MM", "datetime:YYYY-MM-DDTHH:MM:SS-HH:MM"},
			TimezoneLessTimezone:            "process-startup",
			ExplicitTimezoneRequiresSeconds: boolPointer(true),
			Fractional:                      boolPointer(false),
			IANA:                            boolPointer(false),
			TimezoneLessDSTGap:              "invalid",
			TimezoneLessDSTOverlap:          "earliest-absolute-instant",
		},
	}
	if len(document.CLISchema.ConditionForms) != len(wantConditionForms) {
		t.Fatalf("condition form count = %d, want %d: %#v", len(document.CLISchema.ConditionForms), len(wantConditionForms), document.CLISchema.ConditionForms)
	}
	for i, want := range wantConditionForms {
		if !equalOutageDescriptionConditionForm(document.CLISchema.ConditionForms[i], want) {
			t.Errorf("condition form %d = %#v, want %#v", i, document.CLISchema.ConditionForms[i], want)
		}
	}

	wantRoles := map[string]string{
		"stdin":    "input",
		"stdout":   "passthrough",
		"stderr":   "diagnostics",
		"event_fd": "observation",
	}
	if len(document.StreamSemantics) != len(wantRoles) {
		t.Fatalf("stream interface count = %d, want %d", len(document.StreamSemantics), len(wantRoles))
	}
	for name, role := range wantRoles {
		stream, ok := document.StreamSemantics[name]
		if !ok {
			t.Fatalf("missing stream interface %q", name)
		}
		if stream.Role != role || strings.TrimSpace(stream.Description) == "" {
			t.Errorf("stream interface %s = %#v, want role %q and description", name, stream, role)
		}
	}
	if document.StreamSemantics["event_fd"].Format != "jsonl" || document.StreamSemantics["event_fd"].Option != "--events-fd" {
		t.Fatalf("event_fd = %#v, want JSONL --events-fd observation", document.StreamSemantics["event_fd"])
	}

	if document.StateMachine.InitialState != "monitoring" {
		t.Fatalf("initial state = %q, want monitoring", document.StateMachine.InitialState)
	}
	if !equalOutageDescriptionStrings(document.StateMachine.States, []string{"monitoring", "triggered", "cutoff", "eof", "error"}) {
		t.Fatalf("states = %#v, want monitoring/triggered/cutoff/eof/error", document.StateMachine.States)
	}
	if !equalOutageDescriptionStrings(document.StateMachine.Events, []string{"condition-triggered", "stream-cutoff", "eof", "error"}) {
		t.Fatalf("events = %#v, want condition-triggered/stream-cutoff/eof/error", document.StateMachine.Events)
	}
	if !equalOutageDescriptionStrings(document.StateMachine.EventFDEvents, []string{"condition-triggered", "stream-cutoff"}) {
		t.Fatalf("event FD events = %#v, want condition-triggered then stream-cutoff", document.StateMachine.EventFDEvents)
	}
	if containsOutageDescriptionString(document.StateMachine.EventFDEvents, "monitoring") {
		t.Fatal("monitoring is incorrectly listed as an event-FD record")
	}
	for _, event := range document.StateMachine.EventFDEvents {
		if !containsOutageDescriptionString(document.StateMachine.Events, event) {
			t.Fatalf("event FD event %q is not a lifecycle event", event)
		}
	}
	if len(document.StateMachine.Transitions) != 4 {
		t.Fatalf("transition count = %d, want 4", len(document.StateMachine.Transitions))
	}
	wantTransitions := []outageDescriptionTransition{
		{From: "monitoring", Event: "condition-triggered", To: "triggered"},
		{From: "triggered", Event: "stream-cutoff", To: "cutoff"},
		{From: "monitoring", Event: "eof", To: "eof"},
		{From: "monitoring", Event: "error", To: "error"},
	}
	for i, want := range wantTransitions {
		if document.StateMachine.Transitions[i] != want {
			t.Errorf("transition %d = %#v, want %#v", i, document.StateMachine.Transitions[i], want)
		}
	}
	if len(document.SideEffects) != 0 {
		t.Fatalf("side effects = %#v, want empty", document.SideEffects)
	}

	var topLevel struct {
		StateMachine map[string]json.RawMessage `json:"state_machine"`
	}
	if err := json.Unmarshal(output.Bytes(), &topLevel); err != nil {
		t.Fatalf("decode state machine fields: %v", err)
	}
	if _, ok := topLevel.StateMachine["initial_event"]; ok {
		t.Fatal("state machine unexpectedly exposes initial_event")
	}
	if _, ok := topLevel.StateMachine["event_fd_order"]; ok {
		t.Fatal("state machine unexpectedly exposes event_fd_order")
	}
}

func TestDescriptionOptionMetadataMatchesDispatchRules(t *testing.T) {
	description := newDescription()
	raw, err := json.Marshal(description)
	if err != nil {
		t.Fatalf("marshal description: %v", err)
	}
	var document struct {
		CLISchema struct {
			Options   []outageDescriptionOption `json:"options"`
			Arguments []struct {
				Conflicts []string `json:"conflicts"`
			} `json:"arguments"`
		} `json:"cli_schema"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal description: %v", err)
	}
	options := make(map[string]outageDescriptionOption, len(document.CLISchema.Options))
	for _, option := range document.CLISchema.Options {
		options[option.Name] = option
	}

	if got := options["--help"].Repeatable; !got {
		t.Fatal("--help repeatable = false, want true because duplicate help tokens are accepted")
	}
	if got := options["--help"].Priority; got != "highest" {
		t.Fatalf("--help priority = %q, want highest", got)
	}
	if got := options["--describe"].Standalone; !got {
		t.Fatal("--describe standalone = false, want true")
	}
	if got := options["--version"].Standalone; !got {
		t.Fatal("--version standalone = false, want true")
	}

	wantConflicts := map[string][]string{
		"--describe":  {"--version", "--events-fd", "--or", "CONDITION"},
		"--help":      nil,
		"--version":   {"--describe", "--events-fd", "--or", "CONDITION"},
		"--events-fd": {"--describe", "--version"},
		"--or":        {"--describe", "--version"},
		"CONDITION":   {"--describe", "--version"},
	}
	for name, want := range wantConflicts {
		if name == "CONDITION" {
			if len(document.CLISchema.Arguments) != 1 {
				t.Fatalf("argument count = %d, want 1", len(document.CLISchema.Arguments))
			}
			got := document.CLISchema.Arguments[0].Conflicts
			if !equalOutageDescriptionStrings(got, want) {
				t.Errorf("CONDITION conflicts = %#v, want %#v", got, want)
			}
			continue
		}
		option, ok := options[name]
		if !ok {
			t.Fatalf("missing option %q", name)
		}
		if !equalOutageDescriptionStrings(option.Conflicts, want) {
			t.Errorf("%s conflicts = %#v, want %#v", name, option.Conflicts, want)
		}
	}
}

func TestDescriptionEventFDDocumentsDeliveredRecordOrdering(t *testing.T) {
	description := newDescription()
	eventFD := description.StreamSemantics.EventFD.Description
	for _, want := range []string{
		"writable",
		"writable pipe handles",
		"pipe, FIFO, or socket",
		"O_NONBLOCK",
		"PIPE_NOWAIT",
		"write access",
		"before stdin is read",
		"caller retains ownership",
		"write succeeds",
		"delivered records",
		"condition-triggered then stream-cutoff order",
		"failure may omit remaining records",
		"delivered records are never reordered",
		"EOF and errors emit no event-FD records",
		"stderr",
		"warning",
		"further",
		"delivery is best-effort",
		"incomplete final JSONL record",
	} {
		if !strings.Contains(strings.ToLower(eventFD), strings.ToLower(want)) {
			t.Errorf("event FD description = %q, want %q", eventFD, want)
		}
	}
}

type outageDescriptionOption struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Required   bool     `json:"required"`
	Repeatable bool     `json:"repeatable"`
	Aliases    []string `json:"aliases"`
	Conflicts  []string `json:"conflicts"`
	Standalone bool     `json:"standalone"`
	Priority   string   `json:"priority"`
}

type outageDescriptionStream struct {
	Role        string `json:"role"`
	Description string `json:"description"`
	Format      string `json:"format"`
	Option      string `json:"option"`
}

type outageDescriptionState struct {
	InitialState  string                        `json:"initial_state"`
	States        []string                      `json:"states"`
	Events        []string                      `json:"events"`
	EventFDEvents []string                      `json:"event_fd_events"`
	Transitions   []outageDescriptionTransition `json:"transitions"`
}

type outageDescriptionConditionForm struct {
	Name                            string   `json:"name"`
	Syntax                          []string `json:"syntax"`
	UnsupportedOn                   []string `json:"unsupported_on"`
	NonEmpty                        *bool    `json:"non_empty"`
	ValuePreserved                  *bool    `json:"value_preserved"`
	Trigger                         string   `json:"trigger"`
	ValueFormat                     string   `json:"value_format"`
	NonNegative                     *bool    `json:"non_negative"`
	ZeroAllowed                     *bool    `json:"zero_allowed"`
	TimezoneLessTimezone            string   `json:"timezone_less_timezone"`
	ExplicitTimezoneRequiresSeconds *bool    `json:"explicit_timezone_requires_seconds"`
	Fractional                      *bool    `json:"fractional"`
	IANA                            *bool    `json:"iana"`
	TimezoneLessDSTGap              string   `json:"timezone_less_dst_gap"`
	TimezoneLessDSTOverlap          string   `json:"timezone_less_dst_overlap"`
}

type outageDescriptionTransition struct {
	From  string `json:"from"`
	Event string `json:"event"`
	To    string `json:"to"`
}

type outageDescriptionPanicReader struct{}

func (outageDescriptionPanicReader) Read([]byte) (int, error) {
	panic("--describe must not read stdin")
}

func equalOutageDescriptionStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalOutageDescriptionConditionForm(got, want outageDescriptionConditionForm) bool {
	return got.Name == want.Name &&
		equalOutageDescriptionStrings(got.Syntax, want.Syntax) &&
		equalOutageDescriptionStrings(got.UnsupportedOn, want.UnsupportedOn) &&
		equalOutageDescriptionBoolPointer(got.NonEmpty, want.NonEmpty) &&
		equalOutageDescriptionBoolPointer(got.ValuePreserved, want.ValuePreserved) &&
		got.Trigger == want.Trigger &&
		got.ValueFormat == want.ValueFormat &&
		equalOutageDescriptionBoolPointer(got.NonNegative, want.NonNegative) &&
		equalOutageDescriptionBoolPointer(got.ZeroAllowed, want.ZeroAllowed) &&
		got.TimezoneLessTimezone == want.TimezoneLessTimezone &&
		equalOutageDescriptionBoolPointer(got.ExplicitTimezoneRequiresSeconds, want.ExplicitTimezoneRequiresSeconds) &&
		equalOutageDescriptionBoolPointer(got.Fractional, want.Fractional) &&
		equalOutageDescriptionBoolPointer(got.IANA, want.IANA) &&
		got.TimezoneLessDSTGap == want.TimezoneLessDSTGap &&
		got.TimezoneLessDSTOverlap == want.TimezoneLessDSTOverlap
}

func equalOutageDescriptionBoolPointer(got, want *bool) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func boolPointer(value bool) *bool { return &value }

func containsOutageDescriptionString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
