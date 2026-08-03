package core

import (
	"errors"
	"fmt"
)

type Category string

const (
	CategoryInvocation     Category = "invalid-invocation"
	CategoryNotInitialized Category = "not-initialized"
	CategoryNotFound       Category = "not-found"
	CategoryValidation     Category = "validation"
	// CategoryStaleWrite reports a race that the caller did not cause and
	// cannot fix by changing anything: a local ref or a validation-cache row
	// moved between observation and write. Retrying the identical command is
	// the correct response and will usually succeed.
	CategoryStaleWrite  Category = "stale-write"
	CategoryCorruptData Category = "corrupt-data"
	// CategoryConflict reports concurrent intent that Workbook refuses to
	// decide on the caller's behalf. Retrying the identical command reproduces
	// it; the caller must read the envelope's conflict list, choose a
	// resolution, and issue the ordinary command again with new input.
	CategoryConflict    Category = "conflict"
	CategoryOperational Category = "operational"
)

type Error struct {
	Category Category
	Message  string
	Cause    error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return e.Message + ": " + e.Cause.Error()
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func Errorf(category Category, format string, args ...any) error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...)}
}

func Wrap(category Category, message string, cause error) error {
	return &Error{Category: category, Message: message, Cause: cause}
}

func CategoryOf(err error) Category {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}

func ExitCode(err error) int {
	switch CategoryOf(err) {
	case CategoryInvocation:
		return 2
	case CategoryNotInitialized:
		return 3
	case CategoryNotFound:
		return 4
	case CategoryValidation:
		return 5
	case CategoryStaleWrite:
		return 6
	case CategoryCorruptData:
		return 7
	case CategoryConflict:
		return 8
	case CategoryOperational:
		return 1
	}

	var typed *Error
	if errors.As(err, &typed) {
		return 7
	}
	return 1
}
