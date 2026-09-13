package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

const immichDateLayout = "2006-01-02"

// ImmichDatePolicy is an immutable capture-local calendar-date interval.
type ImmichDatePolicy struct {
	from      string
	toEntered string
	toBefore  string
}

type ImmichDatePolicyError struct{ Message string }

func (e *ImmichDatePolicyError) Error() string { return e.Message }

func NewImmichDatePolicy(from, to string) (ImmichDatePolicy, error) {
	parse := func(name, value string) (time.Time, error) {
		if value == "" {
			return time.Time{}, nil
		}
		t, err := time.Parse(immichDateLayout, value)
		if err != nil || t.Format(immichDateLayout) != value {
			return time.Time{}, &ImmichDatePolicyError{Message: fmt.Sprintf("Immich %s date must be a valid YYYY-MM-DD calendar date", name)}
		}
		return t, nil
	}
	fromTime, err := parse("from", from)
	if err != nil {
		return ImmichDatePolicy{}, err
	}
	toTime, err := parse("to", to)
	if err != nil {
		return ImmichDatePolicy{}, err
	}
	if !fromTime.IsZero() && !toTime.IsZero() && fromTime.After(toTime) {
		return ImmichDatePolicy{}, &ImmichDatePolicyError{Message: "Immich date from must not be after date to"}
	}
	p := ImmichDatePolicy{from: from, toEntered: to}
	if !toTime.IsZero() {
		if to != "9999-12-31" {
			p.toBefore = toTime.AddDate(0, 0, 1).Format(immichDateLayout)
		}
	}
	return p, nil
}

func (p ImmichDatePolicy) Active() bool { return p.from != "" || p.toEntered != "" }

func (p ImmichDatePolicy) Eligible(date *string) bool {
	if date == nil || *date == "" {
		return !p.Active()
	}
	return (p.from == "" || *date >= p.from) &&
		(p.toEntered == "" || (p.toBefore != "" && *date < p.toBefore) || (p.toBefore == "" && *date <= p.toEntered))
}

func (p ImmichDatePolicy) Apply(q *gorm.DB, column string) *gorm.DB {
	if p.Active() {
		q = q.Where(column + " IS NOT NULL")
	}
	if p.from != "" {
		q = q.Where(column+" >= ?", p.from)
	}
	if p.toBefore != "" {
		q = q.Where(column+" < ?", p.toBefore)
	} else if p.toEntered != "" {
		q = q.Where(column+" <= ?", p.toEntered)
	}
	return q
}

func (p ImmichDatePolicy) ApplyToMixedSources(q *gorm.DB, sourceColumn, dateColumn string) *gorm.DB {
	if !p.Active() {
		return q
	}
	condition := sourceColumn + " <> ? OR (" + dateColumn + " IS NOT NULL"
	args := []interface{}{"immich"}
	if p.from != "" {
		condition += " AND " + dateColumn + " >= ?"
		args = append(args, p.from)
	}
	if p.toBefore != "" {
		condition += " AND " + dateColumn + " < ?"
		args = append(args, p.toBefore)
	} else if p.toEntered != "" {
		condition += " AND " + dateColumn + " <= ?"
		args = append(args, p.toEntered)
	}
	return q.Where(condition+")", args...)
}

func (p ImmichDatePolicy) SearchBounds() (string, string) {
	var from, to string
	if p.from != "" {
		from = p.from + "T00:00:00Z"
	}
	if p.toBefore != "" {
		to = p.toBefore + "T00:00:00Z"
	} else if p.toEntered != "" {
		to = p.toEntered + "T23:59:59.999999999Z"
	}
	return from, to
}

func captureDate(localDateTime, dateTimeOriginal string) *string {
	for _, value := range []string{localDateTime, dateTimeOriginal} {
		if len(value) < len(immichDateLayout) {
			continue
		}
		date := value[:len(immichDateLayout)]
		if t, err := time.Parse(immichDateLayout, date); err == nil && t.Format(immichDateLayout) == date {
			return &date
		}
	}
	return nil
}
