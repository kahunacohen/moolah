package core

import (
	"fmt"
	"time"
)

const dateLayout = "2006-01-02"

// Date is a calendar date without time or time zone.
type Date struct {
	t time.Time // always UTC midnight; the zero value means "no date"
}

// ParseDate parses a YYYY-MM-DD date.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("invalid date %q: use YYYY-MM-DD", s)
	}
	return Date{t: t}, nil
}

// MustDate parses a YYYY-MM-DD date and panics on error. For tests and constants.
func MustDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// DateOf returns the calendar date of t in t's location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{t: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// String returns the date as YYYY-MM-DD, or "" for the zero date.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.t.Format(dateLayout)
}

// IsZero reports whether d is the zero date.
func (d Date) IsZero() bool { return d.t.IsZero() }

// Before reports whether d is before o.
func (d Date) Before(o Date) bool { return d.t.Before(o.t) }

// After reports whether d is after o.
func (d Date) After(o Date) bool { return d.t.After(o.t) }

// AddDays returns d plus n days.
func (d Date) AddDays(n int) Date { return Date{t: d.t.AddDate(0, 0, n)} }

// MarshalText implements encoding.TextMarshaler.
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }
