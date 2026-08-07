package core

import (
	"strings"
	"unicode"
)

// DisplayLine renders untrusted task text as one terminal-safe line. Control
// runes become spaces — an ESC sequence in a title could otherwise redraw the
// row into a forged task, and a newline in a description could forge a
// structured field line — and whitespace runs collapse to a single space.
//
// It exists for text-mode output only. Stored task data stays byte-exact, and
// JSON output never needs it because encoding/json escapes every byte below
// 0x20.
func DisplayLine(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
