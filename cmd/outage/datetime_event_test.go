package main

// This file verifies strict local datetime parsing and deadline monitoring.

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseAbsoluteDeadlineAcceptsSupportedForms(t *testing.T) {
	location := time.FixedZone("test", 9*60*60)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{
		{value: "2026-09-03T18:00", want: time.Date(2026, time.September, 3, 18, 0, 0, 0, location)},
		{value: "2026-09-03T18:00:07", want: time.Date(2026, time.September, 3, 18, 0, 7, 0, location)},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseAbsoluteDeadline(tc.value, location)
			if err != nil {
				t.Fatalf("parseAbsoluteDeadline returned error: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("deadline = %s, want %s", got, tc.want)
			}
			if got.Location() != location {
				t.Fatalf("location = %v, want %v", got.Location(), location)
			}
		})
	}
}

func TestParseAbsoluteDeadlineRejectsMalformedValues(t *testing.T) {
	location := time.UTC
	for _, value := range []string{
		"",
		"2026-09-03",
		"2026-09-03T18",
		"2026-09-03T18:00:00:00",
		"2026-09-03T18:00 ",
		"2026-09-03 18:00",
		"2026-09-03t18:00",
		"２０２６-09-03T18:00",
		"2026-9-03T18:00",
		"2026-09-3T18:00",
		"2026-09-03T8:00",
		"2026-09-03T18:0",
		"0000-01-01T00:00",
		"2026-02-29T12:00",
		"2026-09-03T24:00",
		"2026-09-03T23:60",
		"2026-09-03T23:59:60",
		"2026-09-03T18:00.1",
		"2026-09-03T18:00Z",
		"2026-09-03T18:00+09:00",
		"2026-09-03T18:00UTC",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseAbsoluteDeadline(value, location); err == nil {
				t.Fatal("parseAbsoluteDeadline unexpectedly accepted malformed value")
			}
		})
	}
}

func TestParseAbsoluteDeadlineUsesEarlierInstantForDSTOverlap(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("load timezone: %v", err)
	}
	got, err := parseAbsoluteDeadline("2024-11-03T01:30", location)
	if err != nil {
		t.Fatalf("parseAbsoluteDeadline returned error: %v", err)
	}
	want := time.Date(2024, time.November, 3, 1, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	if !got.Equal(want) {
		t.Fatalf("deadline = %s (%s), want %s (%s)", got, got.UTC(), want, want.UTC())
	}
}

func TestParseAbsoluteDeadlineRejectsDSTGap(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("load timezone: %v", err)
	}
	if _, err := parseAbsoluteDeadline("2024-03-10T02:30", location); err == nil {
		t.Fatal("parseAbsoluteDeadline unexpectedly accepted a DST gap")
	}
}

func TestEarliestLocalInstantFindsShortLivedHistoricalOffset(t *testing.T) {
	location, err := time.LoadLocationFromTZData("Synthetic/Short", shortLivedOffsetTZif(t))
	if err != nil {
		t.Fatalf("load synthetic timezone: %v", err)
	}
	parsed := time.Unix(30, 0).In(location)
	if !sameLocalDateTime(parsed, 1970, time.January, 1, 0, 0, 0) {
		t.Fatalf("later occurrence = %s, want 1970-01-01T00:00:00", parsed)
	}

	got := earliestLocalInstant(parsed, 1970, time.January, 1, 0, 0, 0, location)
	want := time.Unix(0, 0).In(location)
	if !got.Equal(want) {
		t.Fatalf("earliest occurrence = %s (%s), want %s (%s)", got, got.UTC(), want, want.UTC())
	}
}

func TestParseAbsoluteDeadlineHandlesTransitionAtZeroTime(t *testing.T) {
	location, err := time.LoadLocationFromTZData("Synthetic/Zero", zeroTimeTransitionTZif(t))
	if err != nil {
		t.Fatalf("load synthetic timezone: %v", err)
	}

	got, err := parseAbsoluteDeadline("0001-01-01T00:00:00", location)
	if err != nil {
		t.Fatalf("parseAbsoluteDeadline returned error: %v", err)
	}
	want := time.Date(1, time.January, 1, 0, 0, -30, 0, time.UTC).In(location)
	if !got.Equal(want) {
		t.Fatalf("earliest occurrence = %s (%s), want %s (%s)", got, got.UTC(), want, want.UTC())
	}
}

func TestRunDatetimeEventUsesStartupLocalTimeAndArmsBeforeReading(t *testing.T) {
	location := time.FixedZone("test", 9*60*60)
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	timerArmed := make(chan time.Duration, 1)
	timerFired := make(chan time.Time, 1)
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(delay time.Duration) (<-chan time.Time, func()) {
			timerArmed <- delay
			return timerFired, func() {}
		},
	}
	input := &datetimeFirstReadGate{started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	var output bytes.Buffer
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runWithClock([]string{"datetime:2026-09-03T18:00"}, input, &output, &diagnostics, clock)
	}()

	select {
	case got := <-timerArmed:
		if got != time.Second {
			t.Fatalf("timer delay = %v, want 1s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("datetime monitor did not arm before input read")
	}
	select {
	case <-input.started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not attempt the first read")
	}

	if got := output.String(); got != "" {
		t.Fatalf("output = %q, want empty while first read is blocked", got)
	}
	timerFired <- now
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatalf("run status = %d, diagnostics = %q", code, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after datetime timer")
	}
	close(input.release)
	select {
	case <-input.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for blocked reader cleanup")
	}
}

func TestRunDatetimeEventEqualOrPastDoesNotReadStdin(t *testing.T) {
	location := time.FixedZone("test", -5*60*60)
	now := time.Date(2026, time.September, 3, 18, 0, 1, 0, location)
	for _, value := range []string{"datetime:2026-09-03T18:00", "datetime:2026-09-03T18:00:01"} {
		t.Run(value, func(t *testing.T) {
			timerCreated := false
			clock := runtimeClock{
				now:      func() time.Time { return now },
				location: location,
				newTimer: func(time.Duration) (<-chan time.Time, func()) {
					timerCreated = true
					return make(chan time.Time), func() {}
				},
			}
			var output, diagnostics bytes.Buffer
			status := runWithClock([]string{value}, unreadableReader{}, &output, &diagnostics, clock)
			if status != exitOK {
				t.Fatalf("run status = %d, diagnostics = %q", status, diagnostics.String())
			}
			if timerCreated {
				t.Fatal("immediately satisfied datetime unexpectedly created a timer")
			}
		})
	}
}

func TestRunDatetimeEventForwardsAndExitsWithoutEOF(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	timerFired := make(chan time.Time, 1)
	stopped := make(chan struct{})
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(time.Duration) (<-chan time.Time, func()) {
			return timerFired, func() { close(stopped) }
		},
	}
	reader := &fileEventReader{
		payload: []byte("input before datetime"),
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	writer := &fileEventWriter{copied: make(chan struct{})}
	var diagnostics bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runWithClock([]string{"datetime:2026-09-03T18:00:00"}, reader, writer, &diagnostics, clock)
	}()

	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for datetime-event input reader to start")
	}
	select {
	case <-writer.copied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for input to be forwarded")
	}
	timerFired <- now
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d; diagnostics = %q", code, exitOK, diagnostics.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for datetime event")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("datetime timer was not cleaned up")
	}
	if got := writer.output.String(); got != "input before datetime" {
		t.Fatalf("stdout = %q, want %q", got, "input before datetime")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q, want empty", diagnostics.String())
	}
	close(reader.release)
	select {
	case <-reader.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for blocked reader cleanup")
	}
}

func TestRunDatetimeEventStopsTimerWhenInputCompletes(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	stopped := make(chan struct{})
	clock := runtimeClock{
		now:      func() time.Time { return now },
		location: location,
		newTimer: func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() { close(stopped) }
		},
	}
	var output, diagnostics bytes.Buffer
	status := runWithClock([]string{"datetime:2026-09-03T18:00:00"}, strings.NewReader("input"), &output, &diagnostics, clock)
	if status != exitOK {
		t.Fatalf("run status = %d, diagnostics = %q", status, diagnostics.String())
	}
	if output.String() != "input" {
		t.Fatalf("output = %q, want %q", output.String(), "input")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("datetime timer was not cleaned up after input completion")
	}
}

func TestStartDeadlineMonitorRechecksNowBeforeArming(t *testing.T) {
	location := time.UTC
	firstNow := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	deadline := firstNow.Add(time.Second)
	nowCalls := 0
	timerCreated := false
	timerC := make(chan time.Time)
	eventC, stop := startDeadlineMonitor(&deadline, func() time.Time {
		nowCalls++
		if nowCalls == 1 {
			return firstNow
		}
		return deadline
	}, func(time.Duration) (<-chan time.Time, func()) {
		timerCreated = true
		return timerC, func() {}
	})
	if stop != nil {
		defer stop()
	}
	if nowCalls < 2 {
		t.Fatalf("now sampled %d times, want initial and pre-arm samples", nowCalls)
	}
	if eventC != nil {
		t.Fatal("deadline monitor returned an event channel after deadline became satisfied")
	}
	if timerCreated {
		t.Fatal("deadline monitor armed a timer after deadline became satisfied")
	}
}

func TestStartDeadlineMonitorRearmsCappedWaitAndCleansUpIdempotently(t *testing.T) {
	location := time.UTC
	initialNow := time.Date(2026, time.September, 3, 17, 59, 59, 0, location)
	deadline := time.Date(9999, time.December, 31, 23, 59, 59, 0, location)
	nearDeadline := deadline.Add(-2 * time.Second)
	firstTimer := make(chan time.Time, 1)
	secondTimer := make(chan time.Time, 1)
	armed := make(chan time.Duration, 2)
	stopped := make(chan int, 2)
	timerErrors := make(chan int, 1)
	nowCalls := 0
	timerCalls := 0
	eventC, stop := startDeadlineMonitor(&deadline, func() time.Time {
		nowCalls++
		if nowCalls <= 2 {
			return initialNow
		}
		return nearDeadline
	}, func(delay time.Duration) (<-chan time.Time, func()) {
		timerCalls++
		index := timerCalls
		if index > 2 {
			timerErrors <- index
			return nil, nil
		}
		armed <- delay
		var timerC <-chan time.Time
		switch index {
		case 1:
			timerC = firstTimer
		case 2:
			timerC = secondTimer
		}
		return timerC, func() { stopped <- index }
	})
	if eventC == nil || stop == nil {
		t.Fatal("future deadline monitor did not return event channel and cleanup")
	}
	defer stop()

	const maxTimerWait = time.Duration(1<<63 - 1)
	select {
	case got := <-armed:
		if got != maxTimerWait {
			t.Fatalf("initial timer delay = %v, want capped delay %v", got, maxTimerWait)
		}
	case got := <-timerErrors:
		t.Fatalf("newTimer called unexpectedly at arm %d", got)
	case <-time.After(5 * time.Second):
		t.Fatal("initial capped timer was not armed")
	}

	firstTimer <- initialNow
	select {
	case got := <-stopped:
		if got != 1 {
			t.Fatalf("stopped timer = %d, want initial timer 1", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial timer was not cleaned up before rearming")
	}
	select {
	case got := <-armed:
		if got != 2*time.Second {
			t.Fatalf("rearmed timer delay = %v, want 2s", got)
		}
	case got := <-timerErrors:
		t.Fatalf("newTimer called unexpectedly at arm %d", got)
	case <-time.After(5 * time.Second):
		t.Fatal("deadline monitor did not rearm after capped timer")
	}

	secondTimer <- nearDeadline
	select {
	case <-eventC:
	case got := <-timerErrors:
		t.Fatalf("newTimer called unexpectedly at arm %d", got)
	case <-time.After(5 * time.Second):
		t.Fatal("deadline monitor did not emit event after final timer")
	}
	select {
	case got := <-stopped:
		if got != 2 {
			t.Fatalf("stopped timer = %d, want rearmed timer 2", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rearmed timer was not cleaned up")
	}

	stop()
	stop()
	select {
	case got := <-stopped:
		t.Fatalf("cleanup stopped timer %d more than once", got)
	case got := <-timerErrors:
		t.Fatalf("newTimer called unexpectedly at arm %d", got)
	default:
	}
}

func TestRunRejectsInvalidDatetimeWithoutReadingStdin(t *testing.T) {
	for _, value := range []string{
		"",
		"2026-09-03 18:00",
		"2026-09-03t18:00",
		"2026-09-03T24:00",
		"2026-02-29T18:00",
		"2026-09-03T18:00Z",
		"2026-09-03T18:00+00:00",
		"2026-09-03T18:00:00.1",
	} {
		t.Run(value, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			status := run([]string{"datetime:" + value}, unreadableReader{}, &output, &diagnostics)
			if status != exitArgError {
				t.Fatalf("run status = %d, want %d; diagnostics = %q", status, exitArgError, diagnostics.String())
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want empty", output.String())
			}
			if !strings.Contains(diagnostics.String(), "invalid datetime") {
				t.Fatalf("diagnostics = %q, want invalid-datetime diagnostic", diagnostics.String())
			}
		})
	}
}

type datetimeFirstReadGate struct {
	started   chan struct{}
	release   chan struct{}
	done      chan struct{}
	delivered bool
}

func (r *datetimeFirstReadGate) Read(p []byte) (int, error) {
	if !r.delivered {
		r.delivered = true
		close(r.started)
		<-r.release
		return copy(p, []byte("deadline-opened")), nil
	}
	close(r.done)
	return 0, io.EOF
}

func shortLivedOffsetTZif(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	data.WriteString("TZif")
	data.Write(make([]byte, 16))
	for _, count := range []uint32{0, 0, 0, 3, 4, 8} {
		if err := binary.Write(&data, binary.BigEndian, count); err != nil {
			t.Fatalf("write TZif header: %v", err)
		}
	}
	for _, transition := range []int32{-20, 10, 20} {
		if err := binary.Write(&data, binary.BigEndian, transition); err != nil {
			t.Fatalf("write TZif transition: %v", err)
		}
	}
	data.Write([]byte{1, 2, 3})
	for _, zone := range []struct {
		offset int32
		name   byte
	}{
		{offset: -7200, name: 0},
		{offset: 0, name: 2},
		{offset: 36000, name: 4},
		{offset: -30, name: 6},
	} {
		if err := binary.Write(&data, binary.BigEndian, zone.offset); err != nil {
			t.Fatalf("write TZif offset: %v", err)
		}
		data.WriteByte(0)
		data.WriteByte(zone.name)
	}
	data.WriteString("D\x00A\x00B\x00C\x00")
	return data.Bytes()
}

func zeroTimeTransitionTZif(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	writeDatetimeTZifHeader(t, &data, '2', 0, 1, 2)
	writeDatetimeTZifZone(t, &data, 0, 0)
	data.WriteString("X\x00")

	writeDatetimeTZifHeader(t, &data, '2', 1, 2, 4)
	if err := binary.Write(&data, binary.BigEndian, int64(-62135596800)); err != nil {
		t.Fatalf("write TZif transition: %v", err)
	}
	data.WriteByte(1)
	writeDatetimeTZifZone(t, &data, 30, 0)
	writeDatetimeTZifZone(t, &data, 0, 2)
	data.WriteString("A\x00B\x00")
	data.WriteString("\n\n")
	return data.Bytes()
}

func writeDatetimeTZifHeader(t *testing.T, data *bytes.Buffer, version byte, transitions, zones, names uint32) {
	t.Helper()
	data.WriteString("TZif")
	data.WriteByte(version)
	data.Write(make([]byte, 15))
	for _, count := range []uint32{0, 0, 0, transitions, zones, names} {
		if err := binary.Write(data, binary.BigEndian, count); err != nil {
			t.Fatalf("write TZif header: %v", err)
		}
	}
}

func writeDatetimeTZifZone(t *testing.T, data *bytes.Buffer, offset int32, name byte) {
	t.Helper()
	if err := binary.Write(data, binary.BigEndian, offset); err != nil {
		t.Fatalf("write TZif offset: %v", err)
	}
	data.WriteByte(0)
	data.WriteByte(name)
}
