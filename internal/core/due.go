package core

import (
	"strconv"
	"strings"
	"time"
)

// DueDateLayout is the one form a due date is stored, compared and printed in:
// a plain calendar date with no time and no zone.
//
// A date rather than a timestamp is the whole timezone story. A deadline is a
// day on somebody's calendar, and a stored instant would force every reader to
// pick the zone that day is measured in. A plain date carries no zone at all,
// so the stored value is the same fact everywhere; only "is it past?" depends
// on where the reader is, and that is judged against the reader's own date —
// see DueStateOf — which is display, never semantics. Two readers either side
// of midnight can disagree about whether a task is overdue for up to a day, and
// both are right about their own calendar.
//
// Because the layout is fixed width and zero padded, two due dates compare
// correctly as strings, which is how every comparison in this package makes
// them.
const DueDateLayout = "2006-01-02"

// DueSoonDays is how far ahead "due soon" reaches: a task due today or within
// the next DueSoonDays days is due soon, and one due further out is not.
const DueSoonDays = 3

// dueMinReader is the writer-format generation a pack that records a due date
// declares. See SupportedFormatGeneration for what generation five means.
const dueMinReader = 5

// maxRelativeDueDays bounds the `+Nd` form. It is far beyond any real deadline
// and well inside the calendar the date layout can print, so the arithmetic
// never leaves four-digit years.
const maxRelativeDueDays = 36500

// ValidateDueDate checks that a value is a real calendar date in DueDateLayout.
//
// It is a shape check, like ValidateStatusToken: the value has to be exactly
// ten characters, zero padded, and name a day that exists, so 2026-02-30 and
// 2026-1-5 are both refused. An empty value is not a due date and is refused
// here too; callers that accept "no due date" ask about that first.
func ValidateDueDate(value string) error {
	if len(value) != len(DueDateLayout) {
		return Errorf(CategoryValidation, "due date %q must be a calendar date in the form YYYY-MM-DD", shortDueValue(value))
	}
	parsed, err := time.Parse(DueDateLayout, value)
	if err != nil || parsed.Format(DueDateLayout) != value {
		return Errorf(CategoryValidation, "due date %q must be a calendar date in the form YYYY-MM-DD", value)
	}
	return nil
}

// shortDueValue keeps an oversized rejected value out of an error message, the
// way the rank and size ceilings do: whatever was typed is quoted only while
// it is small enough to be worth reading.
func shortDueValue(value string) string {
	const limit = 32
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// ResolveDueDate turns what somebody typed into the absolute date that is
// stored.
//
// It accepts an ISO date (2026-12-01), `today`, `tomorrow`, and `+Nd` for N
// days after today. The relative forms are resolved here, once, against the
// writer's local date — today is the caller's — so the operation that is
// recorded only ever holds an absolute date: a history that stored `+3d` would
// mean a different day to every reader who replayed it. Anything else is a
// validation error that lists the forms.
func ResolveDueDate(input string, today time.Time) (string, error) {
	value := strings.TrimSpace(input)
	base := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	switch strings.ToLower(value) {
	case "today":
		return base.Format(DueDateLayout), nil
	case "tomorrow":
		return base.AddDate(0, 0, 1).Format(DueDateLayout), nil
	}
	if strings.HasPrefix(value, "+") && strings.HasSuffix(strings.ToLower(value), "d") && len(value) > 2 {
		digits := value[1 : len(value)-1]
		days, err := strconv.Atoi(digits)
		if err == nil && digits == strconv.Itoa(days) && days >= 0 && days <= maxRelativeDueDays {
			return base.AddDate(0, 0, days).Format(DueDateLayout), nil
		}
	}
	if err := ValidateDueDate(value); err != nil {
		return "", Errorf(CategoryValidation,
			"due date %q must be a date in the form YYYY-MM-DD, today, tomorrow, or +Nd for N days from today",
			shortDueValue(value))
	}
	return value, nil
}

// LocalDate is the reader's calendar date at an instant, in DueDateLayout. It
// is the "today" every overdue judgment is made against.
func LocalDate(at time.Time) string {
	return at.Local().Format(DueDateLayout)
}

// DueState is what a task's due date means to one reader on one day.
type DueState string

const (
	// DueStateNone is a task with no due date, one that is finished or
	// deleted, or one whose date is more than DueSoonDays days away.
	DueStateNone DueState = ""
	// DueStateOverdue is a task whose due date is before the reader's today.
	DueStateOverdue DueState = "overdue"
	// DueStateSoon is a task due today or within the next DueSoonDays days.
	DueStateSoon DueState = "due-soon"
)

// DueStateOf judges a due date against the reader's today.
//
// A finished task — one in a status the vocabulary tags done — and a deleted
// task are never overdue or due soon: a deadline met, or a task abandoned, is
// not something a reader needs flagged. today is a date in DueDateLayout; a
// value that is not one judges nothing.
func DueStateOf(due string, finished bool, today string) DueState {
	if due == "" || finished {
		return DueStateNone
	}
	day, err := time.Parse(DueDateLayout, today)
	if err != nil {
		return DueStateNone
	}
	if due < today {
		return DueStateOverdue
	}
	if due <= day.AddDate(0, 0, DueSoonDays).Format(DueDateLayout) {
		return DueStateSoon
	}
	return DueStateNone
}

// DueBefore reports whether a task's due date falls strictly before a date.
// A task with no due date is never before anything.
func DueBefore(due, date string) bool {
	return due != "" && due < date
}

// CompareDue orders two due dates earliest first, with a task that has no due
// date after every task that has one. Equal dates compare equal, so a caller's
// own tiebreak decides between them.
func CompareDue(left, right string) int {
	switch {
	case left == right:
		return 0
	case left == "":
		return 1
	case right == "":
		return -1
	case left < right:
		return -1
	default:
		return 1
	}
}
