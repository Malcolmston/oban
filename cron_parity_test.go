package oban

import (
	"testing"
	"time"
)

// The vectors in this file are transcribed directly from Elixir Oban's own
// test suite, test/oban/cron/expression_test.exs
// (github.com/sorentwo/oban), so the Go port is validated against the
// upstream library's exact expected values rather than against re-derived
// behaviour. Oban is the Elixir project this package mirrors; see README.md
// and web/src/data.ts (node: "sorentwo/oban").
//
// Mapping of upstream API onto the Go port:
//
//	Oban.Cron.Expression.parse!/1  -> ParseCron / MustParseCron
//	Oban.Cron.Expression.now?/2    -> (*Schedule).Matches
//	Oban.Cron.Expression.next_at/2 -> (*Schedule).Next
//	Oban.Cron.Expression.last_at/2 -> (*Schedule).Prev
//
// Two upstream behaviours are deliberately supersetted by this port and are
// therefore NOT asserted as rejections here: weekday 7 as an alias for Sunday
// and lowercase three-letter month/weekday names. Both are exercised as
// accepted input by the port's own cron tests.

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// matchingHours returns the sorted set of hours (with all other fields wild or
// zero) that the schedule matches on 2026-01-01, used to compare against the
// upstream ".hours" MapSet assertions.
func matchingHours(t *testing.T, expr string) []int {
	t.Helper()
	s := MustParseCron(expr)
	var out []int
	for h := 0; h < 24; h++ {
		if s.Matches(utc(2026, time.January, 1, h, 0)) {
			out = append(out, h)
		}
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParityCronStepRanges mirrors the "step ranges are calculated from the
// lower value of the range" test: a bare value with a step opens a range up to
// the field maximum.
func TestParityCronStepRanges(t *testing.T) {
	cases := []struct {
		expr string
		want []int
	}{
		{"0 0/12 * * *", []int{0, 12}},
		{"0 1/7 * * *", []int{1, 8, 15, 22}},
		{"0 1-14/7 * * *", []int{1, 8}},
	}
	for _, c := range cases {
		if got := matchingHours(t, c.expr); !equalInts(got, c.want) {
			t.Errorf("hours(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

// TestParityCronNextAt mirrors next_at/2 "calculating the next time a cron will
// match". Each vector is Oban's asserted UTC result.
func TestParityCronNextAt(t *testing.T) {
	cases := []struct {
		expr string
		from time.Time
		want time.Time
	}{
		{"* * * * *", utc(2024, 11, 21, 0, 55), utc(2024, 11, 21, 0, 56)},
		{"*/2 * * * *", utc(2024, 11, 21, 0, 55), utc(2024, 11, 21, 0, 56)},
		{"5 * * * *", utc(2024, 11, 21, 0, 6), utc(2024, 11, 21, 1, 5)},
		{"0 13 * * *", utc(2024, 11, 21, 1, 2), utc(2024, 11, 21, 13, 0)},
		{"0 */2 * * *", utc(2024, 11, 21, 3, 2), utc(2024, 11, 21, 4, 0)},
		{"0 0 1 * *", utc(2024, 11, 21, 1, 1), utc(2024, 12, 1, 0, 0)},
		{"0 0-2 5 9 *", utc(2024, 11, 21, 1, 1), utc(2025, 9, 5, 0, 0)},
		{"0 0-2 5 9 *", utc(2024, 9, 4, 0, 0), utc(2024, 9, 5, 0, 0)},
		// AND rule: Sept 5th that is also a Sunday. 2027-09-05 is the first.
		{"0 1 5 9 SUN", utc(2024, 11, 21, 0, 0), utc(2027, 9, 5, 1, 0)},
	}
	for _, c := range cases {
		got := MustParseCron(c.expr).Next(c.from)
		if !got.Equal(c.want) {
			t.Errorf("Next(%q, %v) = %v, want %v", c.expr, c.from, got, c.want)
		}
	}
}

// TestParityCronLastAt mirrors last_at/2 "calculating the last time a cron
// matched". Each vector is Oban's asserted UTC result.
func TestParityCronLastAt(t *testing.T) {
	cases := []struct {
		expr string
		from time.Time
		want time.Time
	}{
		{"* * * * *", utc(2024, 11, 21, 0, 55), utc(2024, 11, 21, 0, 54)},
		{"*/2 * * * *", utc(2024, 11, 21, 0, 55), utc(2024, 11, 21, 0, 54)},
		{"5 * * * *", utc(2024, 11, 21, 0, 6), utc(2024, 11, 21, 0, 5)},
		{"0 1 * * *", utc(2024, 11, 21, 1, 2), utc(2024, 11, 21, 1, 0)},
		{"0 */2 * * *", utc(2024, 11, 21, 3, 2), utc(2024, 11, 21, 2, 0)},
		{"0 0 1 * *", utc(2024, 11, 21, 1, 1), utc(2024, 11, 1, 0, 0)},
		{"0 0-2 5 9 *", utc(2024, 11, 21, 1, 1), utc(2024, 9, 5, 2, 0)},
		{"0 0-2 5 9 *", utc(2024, 9, 4, 0, 0), utc(2023, 9, 5, 2, 0)},
		// AND rule: Sept 5th that is also a Monday. 2022-09-05 is the latest.
		{"0 1 5 9 MON", utc(2024, 11, 21, 0, 0), utc(2022, 9, 5, 1, 0)},
	}
	for _, c := range cases {
		got := MustParseCron(c.expr).Prev(c.from)
		if !got.Equal(c.want) {
			t.Errorf("Prev(%q, %v) = %v, want %v", c.expr, c.from, got, c.want)
		}
	}
}

// TestParityCronNowWeekday mirrors now?/2 "literal days of the week match the
// current datetime": with a Sunday base, weekday N matches base+N days.
func TestParityCronNowWeekday(t *testing.T) {
	// 2020-03-15 22:00:00Z is a Sunday.
	sundayBase := time.Date(2020, 3, 15, 22, 0, 0, 0, time.UTC)
	for dow := 0; dow <= 6; dow++ {
		dt := sundayBase.AddDate(0, 0, dow)
		expr := "* * * * " + itoaSmall(dow)
		if !MustParseCron(expr).Matches(dt) {
			t.Errorf("Matches(%q, %v) = false, want true", expr, dt)
		}
	}
}

func itoaSmall(n int) string { return string(rune('0' + n)) }

// TestParityCronParseErrors mirrors the "out of bounds fails" cases that this
// port also rejects. (Weekday 7 and lowercase names are intentional supersets
// and are excluded.)
func TestParityCronParseErrors(t *testing.T) {
	bad := []string{
		"* * * *",     // too few fields
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"* * 32 * *",  // day out of range
		"* * * 13 *",  // month out of range
		"*/0 * * * *", // zero step
		"ONE * * * *", // non-numeric minute
	}
	for _, expr := range bad {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) = nil error, want rejection", expr)
		}
	}
}

// TestParityCronDayMonthAlignment mirrors "day/month combinations that can
// never align are rejected" plus the two aligning expressions that parse.
func TestParityCronDayMonthAlignment(t *testing.T) {
	reject := []string{
		"0 0 30 2 *",
		"0 0 31 4 *",
		"0 0 31 2,4,6,9,11 *",
		"0 0 30,31 FEB *",
	}
	for _, expr := range reject {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) = nil error, want rejection of impossible pairing", expr)
		}
	}
	accept := []string{
		"0 0 30 2,4 *", // April has a 30th
		"0 0 29 2 *",   // February has a 29th in leap years
	}
	for _, expr := range accept {
		if _, err := ParseCron(expr); err != nil {
			t.Errorf("ParseCron(%q) = %v, want success", expr, err)
		}
	}
}
