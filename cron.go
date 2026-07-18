package oban

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed standard 5-field cron expression:
//
//	┌───────────── minute       (0-59)
//	│ ┌─────────── hour         (0-23)
//	│ │ ┌───────── day of month (1-31)
//	│ │ │ ┌─────── month        (1-12 or jan-dec)
//	│ │ │ │ ┌───── day of week  (0-6 or sun-sat; 7 also means Sunday)
//	│ │ │ │ │
//	* * * * *
//
// Each field supports a wildcard (*), single values (5), lists (1,2,3), ranges
// (1-5) and steps (*/2 or 1-10/3). Month and day-of-week accept three-letter
// English names.
//
// Day-of-month and day-of-week are combined with AND: a time matches only when
// it satisfies both fields simultaneously. This mirrors Elixir Oban's
// Oban.Cron.Expression, which requires every field (month, weekday, day, hour,
// minute) to match. Because a "*" field expands to its full range, an
// unrestricted day-of-month or day-of-week is always satisfied and the other
// field then constrains the day on its own. Note this differs from the
// traditional Vixie cron OR rule for the two day fields.
type Schedule struct {
	expr    string
	minutes uint64 // bit i set => minute i allowed (0-59)
	hours   uint64 // bits 0-23
	doms    uint64 // bits 1-31
	months  uint64 // bits 1-12
	dows    uint64 // bits 0-6 (Sunday=0)
}

// String returns the original cron expression.
func (s *Schedule) String() string { return s.expr }

type cronField struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	dowNames = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
)

// ParseCron parses a standard 5-field cron expression. It returns an error for
// malformed input.
func ParseCron(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("oban: cron %q: expected 5 fields, got %d", expr, len(fields))
	}

	s := &Schedule{expr: strings.Join(fields, " ")}
	var err error

	if s.minutes, err = parseField(fields[0], cronField{"minute", 0, 59, nil}); err != nil {
		return nil, err
	}
	if s.hours, err = parseField(fields[1], cronField{"hour", 0, 23, nil}); err != nil {
		return nil, err
	}
	if s.doms, err = parseField(fields[2], cronField{"day-of-month", 1, 31, nil}); err != nil {
		return nil, err
	}
	if s.months, err = parseField(fields[3], cronField{"month", 1, 12, monthNames}); err != nil {
		return nil, err
	}
	// Day-of-week: accept 7 as an alias for Sunday, then fold onto bit 0.
	if s.dows, err = parseField(fields[4], cronField{"day-of-week", 0, 7, dowNames}); err != nil {
		return nil, err
	}
	if s.dows&(1<<7) != 0 {
		s.dows = (s.dows &^ (1 << 7)) | 1
	}

	if err := validateDayMonth(fields[2], fields[3], s.doms, s.months); err != nil {
		return nil, err
	}
	return s, nil
}

// maxDaysPerMonth is the largest day-of-month each month can ever have, using 29
// for February so that leap-year schedules such as "0 0 29 2 *" remain valid.
// It matches the table Elixir Oban uses to reject impossible day/month pairings.
var maxDaysPerMonth = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// validateDayMonth rejects a schedule whose day-of-month and month fields can
// never align, e.g. "0 0 30 2 *" (February never has 30 days) or "0 0 31 4 *"
// (April never has 31). When either field is a wildcard the pairing is always
// satisfiable, matching Oban's Oban.Cron.Expression.validate_days/4. Otherwise
// the schedule is valid as long as at least one allowed day can occur in at
// least one allowed month.
func validateDayMonth(dayField, monField string, doms, months uint64) error {
	if dayField == "*" || monField == "*" {
		return nil
	}
	for month := 1; month <= 12; month++ {
		if months&(1<<uint(month)) == 0 {
			continue
		}
		for day := 1; day <= 31; day++ {
			if doms&(1<<uint(day)) == 0 {
				continue
			}
			if day <= maxDaysPerMonth[month] {
				return nil
			}
		}
	}
	return fmt.Errorf("oban: cron: no day in %q can occur in months %q", dayField, monField)
}

// MustParseCron is like [ParseCron] but panics on error. It is meant for
// package-level schedule declarations with known-good expressions.
func MustParseCron(expr string) *Schedule {
	s, err := ParseCron(expr)
	if err != nil {
		panic(err)
	}
	return s
}

// parseField parses one cron field into a bitmask of allowed values.
func parseField(field string, spec cronField) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(field, ",") {
		m, err := parsePart(part, spec)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	return mask, nil
}

func parsePart(part string, spec cronField) (uint64, error) {
	step := 1
	hasStep := false
	rangePart := part
	if slash := strings.IndexByte(part, '/'); slash >= 0 {
		hasStep = true
		rangePart = part[:slash]
		stepStr := part[slash+1:]
		var err error
		step, err = strconv.Atoi(stepStr)
		if err != nil || step <= 0 {
			return 0, fmt.Errorf("oban: cron %s: invalid step %q", spec.name, stepStr)
		}
	}

	lo, hi := spec.min, spec.max
	switch {
	case rangePart == "*":
		// full range
	case strings.IndexByte(rangePart, '-') > 0:
		bounds := strings.SplitN(rangePart, "-", 2)
		var err error
		if lo, err = parseValue(bounds[0], spec); err != nil {
			return 0, err
		}
		if hi, err = parseValue(bounds[1], spec); err != nil {
			return 0, err
		}
	default:
		v, err := parseValue(rangePart, spec)
		if err != nil {
			return 0, err
		}
		// A bare value with a step (e.g. "1/7") is treated as the open range
		// "value-max/step", so "1/7" on hours yields 1,8,15,22 — matching how
		// Oban's parse_range interprets "N/step". Without a step it is a single
		// value.
		if hasStep {
			lo, hi = v, spec.max
		} else {
			lo, hi = v, v
		}
	}

	if lo > hi {
		return 0, fmt.Errorf("oban: cron %s: range start %d after end %d", spec.name, lo, hi)
	}
	if lo < spec.min || hi > spec.max {
		return 0, fmt.Errorf("oban: cron %s: value out of range [%d,%d]", spec.name, spec.min, spec.max)
	}

	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

func parseValue(s string, spec cronField) (int, error) {
	s = strings.TrimSpace(s)
	if spec.names != nil {
		if v, ok := spec.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("oban: cron %s: invalid value %q", spec.name, s)
	}
	if v < spec.min || v > spec.max {
		return 0, fmt.Errorf("oban: cron %s: value %d out of range [%d,%d]", spec.name, v, spec.min, spec.max)
	}
	return v, nil
}

// maxCronSearchYears bounds Next so an unsatisfiable expression (e.g. Feb 31)
// terminates instead of looping forever.
const maxCronSearchYears = 5

// Next returns the earliest time strictly after t that matches the schedule,
// truncated to the minute and preserving t's location. If no match occurs
// within maxCronSearchYears, it returns the zero time.
func (s *Schedule) Next(t time.Time) time.Time {
	// Advance to the start of the next minute.
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(maxCronSearchYears, 0, 0)

	for t.Before(limit) {
		if s.months&(1<<uint(t.Month())) == 0 {
			// Jump to the first day of the next month.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).AddDate(0, 1, 0)
			continue
		}
		if !s.dayMatches(t) {
			// Advance to the start of the next day.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
			continue
		}
		if s.hours&(1<<uint(t.Hour())) == 0 {
			t = t.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		if s.minutes&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches reports whether t satisfies both the day-of-month and day-of-week
// fields. The two are combined with AND, following Elixir Oban. A "*" field has
// its full range set, so it is always satisfied and the other field alone
// constrains the day.
func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := s.doms&(1<<uint(t.Day())) != 0
	dowOK := s.dows&(1<<uint(t.Weekday())) != 0
	return domOK && dowOK
}
