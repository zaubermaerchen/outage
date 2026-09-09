package cli

// This file verifies strict datetime parsing, DST handling, and canonical
// local-time conversion at the CLI boundary.

import (
	"bytes"
	"encoding/binary"
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

func TestParseAbsoluteDeadlineAcceptsExplicitRFC3339Timezones(t *testing.T) {
	location := time.FixedZone("caller", -5*60*60)
	for _, tc := range []struct {
		value      string
		want       time.Time
		wantOffset int
	}{
		{value: "2026-09-06T00:00:00Z", want: time.Date(2026, time.September, 6, 0, 0, 0, 0, time.UTC), wantOffset: 0},
		{value: "2026-09-06T09:00:00+09:00", want: time.Date(2026, time.September, 6, 9, 0, 0, 0, time.FixedZone("+09:00", 9*60*60)), wantOffset: 9 * 60 * 60},
		{value: "2026-09-06T09:00:00-04:30", want: time.Date(2026, time.September, 6, 9, 0, 0, 0, time.FixedZone("-04:30", -4*60*60-30*60)), wantOffset: -4*60*60 - 30*60},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseAbsoluteDeadline(tc.value, location)
			if err != nil {
				t.Fatalf("parseAbsoluteDeadline returned error: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("deadline = %s (%s), want %s (%s)", got, got.UTC(), tc.want, tc.want.UTC())
			}
			if _, offset := got.Zone(); offset != tc.wantOffset {
				t.Fatalf("timezone offset = %d, want %d", offset, tc.wantOffset)
			}
		})
	}
}

func TestParseAbsoluteDeadlineRejectsMalformedNumericOffsetWithASCIIHint(t *testing.T) {
	_, err := parseAbsoluteDeadline("2026-09-03T18:00:00+09:0X", time.UTC)
	if err == nil {
		t.Fatal("parseAbsoluteDeadline unexpectedly accepted malformed numeric offset")
	}
	const want = "datetime timezone offset must use +HH:MM or -HH:MM"
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
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
		"2026-09-03T18:00:00.1Z",
		"2026-09-03T18:00:00.123+09:00",
		"2026-09-03T18:00:00z",
		"2026-09-03T18:00:00+09",
		"2026-09-03T18:00:00+0900",
		"2026-09-03T18:00:00+24:00",
		"2026-09-03T18:00:00+09:60",
		"2026-09-03T18:00:00UTC",
		"2026-09-03T18:00:00Asia/Tokyo",
		"0000-01-01T00:00:00Z",
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
