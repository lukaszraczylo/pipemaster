package concourse

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	cronFields    = 5
	hoursPerDay   = 24
	minutesInHour = 60
	daysInWeek    = 7
	// windowMinutes is the window width: the time resource checks at least every 5 minutes, so a narrower window
	// can pass with no check inside it.
	windowMinutes = 5
)

var dayNames = [daysInWeek]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

var dayAbbrev = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// scheduleSource maps a cron line to the source of a time resource that approximates it: an interval for a step
// that divides the hour or the day, a window of windowMinutes for a daily time, or a day list. Any other line has no
// form, so the caller keeps the cron text.
func scheduleSource(cron string) (map[string]any, bool) {
	f := strings.Fields(cron)
	if len(f) != cronFields || f[2] != "*" || f[3] != "*" {
		return nil, false
	}
	days, ok := cronDays(f[4])
	if !ok {
		return nil, false
	}
	src := map[string]any{}
	if len(days) > 0 && len(days) < daysInWeek {
		src["days"] = days
	}
	if iv, ok := cronStep(f[0], f[1]); ok {
		if len(days) > 0 {
			return nil, false
		}
		src["interval"] = iv
		return src, true
	}
	minute, mok := cronNumber(f[0], minutesInHour)
	hour, hok := cronNumber(f[1], hoursPerDay)
	if !mok || !hok {
		return nil, false
	}
	end := hour*minutesInHour + minute + windowMinutes
	// The resource tests the weekday of the check time, not of the start, so a window past midnight loses its
	// part on the next day; time-resource lord/time_lord.go daysMatch.
	if _, listed := src["days"]; listed && end > hoursPerDay*minutesInHour {
		return nil, false
	}
	end %= hoursPerDay * minutesInHour
	src["start"] = fmt.Sprintf("%02d:%02d", hour, minute)
	src["stop"] = fmt.Sprintf("%02d:%02d", end/minutesInHour, end%minutesInHour)
	return src, true
}

// cronStep reads a step in the minute field with any hour, or minute 0 with a step in the hour field.
func cronStep(minute, hour string) (string, bool) {
	switch {
	case hour == "*" && minute == "0":
		return "1h", true
	case hour == "*":
		n, ok := cronEvery(minute, minutesInHour)
		return fmt.Sprintf("%dm", n), ok
	case minute == "0":
		n, ok := cronEvery(hour, hoursPerDay)
		return fmt.Sprintf("%dh", n), ok
	}
	return "", false
}

// cronEvery reads `*` or `*/N` where N divides the period and is smaller than it.
func cronEvery(field string, period int) (int, bool) {
	if field == "*" {
		return 1, true
	}
	n, ok := strings.CutPrefix(field, "*/")
	if !ok {
		return 0, false
	}
	v, ok := cronNumber(n, period)
	return v, ok && v > 0 && period%v == 0
}

// cronNumber reads plain decimal digits below limit.
func cronNumber(s string, limit int) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	return v, err == nil && v < limit
}

// cronDays reads `*` as no list and a comma list of days or day ranges as the day names, Sunday first.
func cronDays(field string) ([]string, bool) {
	if field == "*" {
		return nil, true
	}
	set := map[int]bool{}
	for _, item := range strings.Split(field, ",") {
		lo, hi, isRange := strings.Cut(item, "-")
		a, ok := cronDay(lo)
		if !ok {
			return nil, false
		}
		b := a
		if isRange {
			if b, ok = cronDay(hi); !ok || b < a {
				return nil, false
			}
		}
		for d := a; d <= b; d++ {
			set[d%daysInWeek] = true
		}
	}
	var idx []int
	for d := range set {
		idx = append(idx, d)
	}
	sort.Ints(idx)
	names := make([]string, len(idx))
	for i, d := range idx {
		names[i] = dayNames[d]
	}
	return names, true
}

// cronDay reads 0-7 (both 0 and 7 are Sunday) or a three-letter name.
func cronDay(s string) (int, bool) {
	if d, ok := dayAbbrev[strings.ToLower(s)]; ok {
		return d, true
	}
	return cronNumber(s, daysInWeek+1)
}
