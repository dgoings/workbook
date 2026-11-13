package cli

import (
	"testing"
	"time"
)

func TestParseDurationAcceptsDays(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]time.Duration{
		"30d":   720 * time.Hour,
		"1d12h": 36 * time.Hour,
		"5s":    5 * time.Second,
		"90m":   90 * time.Minute,
	} {
		got, err := parseDuration(value)
		if err != nil {
			t.Fatalf("parseDuration(%q) error = %v", value, err)
		}
		if got != want {
			t.Fatalf("parseDuration(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestParseDurationRejectsBadInput(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "d", "1.5d", "-1d", "0s", "abc", "0d", "1d-5h", "99999999999d"} {
		if got, err := parseDuration(value); err == nil {
			t.Fatalf("parseDuration(%q) = %v, want an error", value, got)
		}
	}
}
