// Package git holds the pure parts of orbit's batch git: how old a branch must be
// to go, reading what git prints, and whether changes stashed before a pull can be
// brought back without a conflict.
package git

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Age is how long ago something must have last changed, in calendar units: 90d,
// 12w, 6mo, 1y. Months and years are calendar ones, counted back from now.
type Age struct {
	Years, Months, Weeks, Days, Hours int
}

// ErrAge is returned for an age that cannot be read. It does not repeat what it
// was given, since the error is logged.
var ErrAge = errors.New("an age is a number and a unit: h, d, w, mo or y, such as 90d, 12w, 6mo or 1y")

// ParseAge reads an age such as 90d, 12w, 6mo or 1y. "m" is refused: it would be
// minutes to anyone used to Go's durations, and months to anyone else.
func ParseAge(text string) (Age, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	digits := 0
	for digits < len(text) && text[digits] >= '0' && text[digits] <= '9' {
		digits++
	}
	count, err := strconv.Atoi(text[:digits])
	if err != nil || count <= 0 {
		return Age{}, ErrAge
	}
	var age Age
	var limit int
	switch strings.TrimSpace(text[digits:]) {
	case "h", "hour", "hours":
		age, limit = Age{Hours: count}, maxYears*366*24
	case "d", "day", "days":
		age, limit = Age{Days: count}, maxYears*366
	case "w", "week", "weeks":
		age, limit = Age{Weeks: count}, maxYears*53
	case "mo", "month", "months":
		age, limit = Age{Months: count}, maxYears*12
	case "y", "yr", "year", "years":
		age, limit = Age{Years: count}, maxYears
	case "m":
		return Age{}, fmt.Errorf("%w (m is ambiguous: use mo for months)", ErrAge)
	default:
		return Age{}, ErrAge
	}
	if count > limit {
		return Age{}, fmt.Errorf("%w, of at most %d years", ErrAge, maxYears)
	}
	return age, nil
}

// maxYears bounds an age, far beyond any branch and short of the hours a
// time.Duration holds (292 years), past which a cutoff would wrap into the
// future and make every branch old enough.
const maxYears = 100

// Cutoff is the moment the age reaches back to from now: anything that last
// changed before it is older than the age.
func (a Age) Cutoff(now time.Time) time.Time {
	return now.AddDate(-a.Years, -a.Months, -a.Days-7*a.Weeks).Add(-time.Duration(a.Hours) * time.Hour)
}

// String writes the age as a person would say it: "6 months".
func (a Age) String() string {
	var parts []string
	for _, part := range []struct {
		count int
		unit  string
	}{{a.Years, "year"}, {a.Months, "month"}, {a.Weeks, "week"}, {a.Days, "day"}, {a.Hours, "hour"}} {
		if part.count == 0 {
			continue
		}
		unit := part.unit
		if part.count != 1 {
			unit += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", part.count, unit))
	}
	if len(parts) == 0 {
		return "0 days"
	}
	return strings.Join(parts, " ")
}
