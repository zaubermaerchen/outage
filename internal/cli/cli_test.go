package cli

// This file verifies parsing, canonicalization, and typed condition values.

import (
	"reflect"
	"testing"
	"time"
)

func TestHelpAndVersionDetection(t *testing.T) {
	for _, args := range [][]string{
		{"-h"},
		{"--help", "--wat"},
		{"--wat", "--help"},
	} {
		if !HelpRequested(args) {
			t.Errorf("HelpRequested(%q) = false, want true", args)
		}
	}
	for _, args := range [][]string{
		{"--help"},
		{"--version", "--version"},
		{"--version", "duration:1s"},
	} {
		if VersionRequested(args) {
			t.Errorf("VersionRequested(%q) = true, want false", args)
		}
	}
	if !VersionRequested([]string{"--version"}) {
		t.Error(`VersionRequested(["--version"]) = false, want true`)
	}
}

func TestParseCanonicalizesAndDeduplicatesTypedConditions(t *testing.T) {
	plan, err := Parse([]string{
		"duration:1s && duration:1000ms && file:/tmp/stop",
		"--or",
		"file:/tmp/stop && datetime:2026-09-03T18:00:00Z",
	}, time.UTC)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got, want := len(plan.Conditions), 3; got != want {
		t.Fatalf("condition count = %d, want %d", got, want)
	}
	wantGroups := [][]int{{0, 1}, {1, 2}}
	if !reflect.DeepEqual(plan.Groups, wantGroups) {
		t.Fatalf("groups = %#v, want %#v", plan.Groups, wantGroups)
	}

	if got, want := plan.Conditions[0].Kind, DurationKind; got != want {
		t.Fatalf("first condition kind = %q, want %q", got, want)
	}
	if got, want := plan.Conditions[0].Duration, time.Second; got != want {
		t.Fatalf("duration = %v, want %v", got, want)
	}
	if got, want := plan.Conditions[1].Kind, FileKind; got != want {
		t.Fatalf("second condition kind = %q, want %q", got, want)
	}
	if got, want := plan.Conditions[1].Path, "/tmp/stop"; got != want {
		t.Fatalf("file path = %q, want %q", got, want)
	}
	if got, want := plan.Conditions[2].Kind, DateTimeKind; got != want {
		t.Fatalf("third condition kind = %q, want %q", got, want)
	}
	wantDeadline := time.Date(2026, time.September, 3, 18, 0, 0, 0, time.UTC)
	if got := plan.Conditions[2].Deadline; !got.Equal(wantDeadline) {
		t.Fatalf("datetime = %v, want %v", got, wantDeadline)
	}
}

func TestParseResolvesSignalAliasToTypedValue(t *testing.T) {
	if !signalSupported() {
		t.Skip("signals are unsupported on this platform")
	}

	plan, err := Parse([]string{"signal:SIGUSR1"}, time.UTC)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got, want := len(plan.Conditions), 1; got != want {
		t.Fatalf("condition count = %d, want %d", got, want)
	}
	if plan.Conditions[0].Signal == nil {
		t.Fatal("signal value is nil, want resolved os.Signal")
	}
}
