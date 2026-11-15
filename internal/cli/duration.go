package cli

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const day = 24 * time.Hour

// errNonPositiveDuration marks a duration that parsed but is zero or negative,
// so a caller can word that case differently from a malformed value.
var errNonPositiveDuration = errors.New("duration must be positive")

// parseDuration accepts everything time.ParseDuration accepts plus a leading
// whole-number day segment such as 30d or 1d12h, where a day is 24h. It
// rejects a zero or negative result.
func parseDuration(value string) (time.Duration, error) {
	rest := value
	var total time.Duration
	digits := 0
	hasDays := false
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits < len(rest) && rest[digits] == 'd' {
		var days int64
		for _, c := range rest[:digits] {
			days = days*10 + int64(c-'0')
			if days > int64(math.MaxInt64/day) {
				return 0, fmt.Errorf("duration %q is too large", value)
			}
		}
		total = time.Duration(days) * day
		hasDays = true
		rest = rest[digits+1:]
		if strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, "+") {
			return 0, fmt.Errorf("duration %q has a signed segment after the days", value)
		}
	}
	if rest != "" || !hasDays {
		parsed, err := time.ParseDuration(rest)
		if err != nil {
			return 0, err
		}
		if parsed > math.MaxInt64-total {
			return 0, fmt.Errorf("duration %q is too large", value)
		}
		total += parsed
	}
	if total <= 0 {
		return 0, errNonPositiveDuration
	}
	return total, nil
}
