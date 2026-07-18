package oban

import (
	"fmt"
	"strings"
	"time"
)

// cronMacros maps the common named schedule shorthands to their equivalent
// 5-field cron expressions, matching the set understood by Vixie cron and
// Elixir Oban.
var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// ParseCronSpec parses a cron expression that may be either a standard 5-field
// expression (handed straight to [ParseCron]) or one of the named shorthands
// "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight" and
// "@hourly". The shorthand is case-insensitive. It returns an error for an
// unknown macro or a malformed 5-field expression.
func ParseCronSpec(spec string) (*Schedule, error) {
	trimmed := strings.TrimSpace(spec)
	if strings.HasPrefix(trimmed, "@") {
		expr, ok := cronMacros[strings.ToLower(trimmed)]
		if !ok {
			return nil, fmt.Errorf("oban: cron %q: unknown macro", spec)
		}
		return ParseCron(expr)
	}
	return ParseCron(trimmed)
}

// MustParseCronSpec is like [ParseCronSpec] but panics on error. It is meant for
// package-level schedule declarations with known-good expressions.
func MustParseCronSpec(spec string) *Schedule {
	s, err := ParseCronSpec(spec)
	if err != nil {
		panic(err)
	}
	return s
}

// Matches reports whether t (truncated to the minute) satisfies the schedule.
// Seconds and sub-second components of t are ignored.
func (s *Schedule) Matches(t time.Time) bool {
	t = t.Truncate(time.Minute)
	if s.months&(1<<uint(t.Month())) == 0 {
		return false
	}
	if !s.dayMatches(t) {
		return false
	}
	if s.hours&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if s.minutes&(1<<uint(t.Minute())) == 0 {
		return false
	}
	return true
}

// Upcoming returns the next n fire times strictly after t, in ascending order,
// each computed with [Schedule.Next]. A non-positive n yields an empty slice.
// If the schedule stops matching within Next's search horizon the slice is
// returned short.
func (s *Schedule) Upcoming(t time.Time, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	cur := t
	for i := 0; i < n; i++ {
		next := s.Next(cur)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

// cronPrevSearchYears bounds Prev so an unsatisfiable expression terminates
// instead of looping forever, matching the forward search horizon.
const cronPrevSearchYears = 5

// Prev returns the latest time strictly before t that matches the schedule,
// truncated to the minute and preserving t's location. If no match occurs
// within the search horizon it returns the zero time. It is the mirror of
// [Schedule.Next] and is useful for computing the most recent scheduled run.
func (s *Schedule) Prev(t time.Time) time.Time {
	// Step back to the end of the previous minute.
	t = t.Truncate(time.Minute).Add(-time.Minute)
	limit := t.AddDate(-cronPrevSearchYears, 0, 0)

	for t.After(limit) {
		if s.months&(1<<uint(t.Month())) == 0 {
			// Jump to the last minute of the previous month.
			firstOfMonth := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
			t = firstOfMonth.Add(-time.Minute)
			continue
		}
		if !s.dayMatches(t) {
			// Jump to the last minute of the previous day.
			startOfDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			t = startOfDay.Add(-time.Minute)
			continue
		}
		if s.hours&(1<<uint(t.Hour())) == 0 {
			startOfHour := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
			t = startOfHour.Add(-time.Minute)
			continue
		}
		if s.minutes&(1<<uint(t.Minute())) == 0 {
			t = t.Add(-time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
