package cli

// This file verifies CLI syntax and validation ordering at the package
// boundary.

import (
	"strings"
	"testing"
	"time"
)

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
