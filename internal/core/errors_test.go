package core

import (
	"errors"
	"testing"
)

func TestTypedErrorsPreserveCategoryAndCause(t *testing.T) {
	cause := errors.New("disk full")
	err := Wrap(CategoryCorruptData, "cannot load task", cause)

	if got, want := err.Error(), "cannot load task: disk full"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Wrap() error does not unwrap to cause")
	}
	if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("CategoryOf() = %q, want %q", got, CategoryCorruptData)
	}
	if got, want := Errorf(CategoryValidation, "invalid %s", "rank").Error(), "invalid rank"; got != want {
		t.Fatalf("Errorf().Error() = %q, want %q", got, want)
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"invocation", Errorf(CategoryInvocation, "bad flags"), 2},
		{"not initialized", Errorf(CategoryNotInitialized, "missing config"), 3},
		{"not found", Errorf(CategoryNotFound, "missing task"), 4},
		{"validation", Errorf(CategoryValidation, "bad task"), 5},
		{"stale write", Errorf(CategoryStaleWrite, "head changed"), 6},
		{"conflict", Errorf(CategoryConflict, "both sides changed the description"), 8},
		{"corrupt data", Errorf(CategoryCorruptData, "bad state"), 7},
		{"newer writer", Errorf(CategoryNewerWriter, "a newer workbook wrote this"), 9},
		{"assigned", Errorf(CategoryAssigned, "somebody else holds it"), 10},
		{"operational", Errorf(CategoryOperational, "git failed"), 1},
		{"unknown typed category", Errorf(Category("future"), "unknown"), 7},
		{"unexpected", errors.New("network unavailable"), 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ExitCode(test.err); got != test.want {
				t.Fatalf("ExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}
