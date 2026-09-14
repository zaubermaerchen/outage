package cli

// This file verifies CLI syntax and validation ordering at the package
// boundary.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseEventsFDOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{name: "separate", args: []string{"--events-fd", "3", "duration:1s"}, want: 3},
		{name: "equals", args: []string{"duration:1s", "--events-fd=7"}, want: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse(tc.args, time.UTC)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if !plan.EventsFDSet {
				t.Fatal("EventsFDSet = false, want true")
			}
			if plan.EventsFD != tc.want {
				t.Fatalf("EventsFD = %d, want %d", plan.EventsFD, tc.want)
			}
			if len(plan.Conditions) != 1 || plan.Conditions[0].Kind != DurationKind {
				t.Fatalf("conditions = %#v, want one duration condition", plan.Conditions)
			}
		})
	}
}

func TestParseRejectsInvalidEventsFDOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing separate value", args: []string{"duration:1s", "--events-fd"}},
		{name: "missing before option", args: []string{"--events-fd", "--or", "duration:1s"}},
		{name: "non numeric", args: []string{"duration:1s", "--events-fd=three"}},
		{name: "below minimum", args: []string{"duration:1s", "--events-fd", "2"}},
		{name: "duplicate", args: []string{"--events-fd=3", "--events-fd", "4", "duration:1s"}},
		{name: "negative", args: []string{"duration:1s", "--events-fd", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.args, time.UTC); err == nil {
				t.Fatalf("Parse(%q) error = nil, want error", tc.args)
			}
		})
	}
}

func TestParseEventsFDDoesNotTreatItsValueAsCondition(t *testing.T) {
	fd := strconv.Itoa(3)
	plan, err := Parse([]string{"--events-fd", fd, "duration:1s", "--or", "duration:2s"}, time.UTC)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(plan.Conditions) != 2 || len(plan.Groups) != 2 {
		t.Fatalf("plan = %#v, want two conditions in two groups", plan)
	}
}

func TestParseChecksAllSyntaxBeforePlatformCapability(t *testing.T) {
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
			_, err := parseWithSignalSupport([]string{tc.event}, time.UTC, signalUnsupported)
			if err == nil {
				t.Fatalf("parseWithSignalSupport(%q) = nil, want error", tc.event)
			}
			if !strings.Contains(err.Error(), tc.wantDiagnostic) {
				t.Fatalf("error = %q, want diagnostic containing %q", err, tc.wantDiagnostic)
			}
		})
	}
}

func TestParseORGroupsCheckAllSyntaxBeforePlatformCapability(t *testing.T) {
	signalUnsupported := func() bool { return false }
	_, err := parseWithSignalSupport(
		[]string{"signal:USR1", "--or", "signal:USR2 && "},
		time.UTC,
		signalUnsupported,
	)
	if err == nil {
		t.Fatal("parseWithSignalSupport unexpectedly accepted malformed OR group")
	}
	if !strings.Contains(err.Error(), `unsupported event ""`) {
		t.Fatalf("error = %q, want malformed member diagnostic before signal capability", err)
	}
}
