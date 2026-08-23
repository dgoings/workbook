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
// Unicode's bidi controls are replaced for the same reason, even though they
// are format runes rather than control ones: U+202E RIGHT-TO-LEFT OVERRIDE in a
// comment body makes the terminal draw the rest of the line backwards, so an
// attachment named "gpj.exe" reads as "exe.jpg" and a refusal reads as its own
// opposite. unicode.Bidi_Control is exactly that subset — the overrides,
// embeddings, isolates, and directional marks — which leaves the format runes
// carrying meaning rather than direction, such as the zero-width joiner holding
// an emoji sequence together, printing as they were written.
//
// It exists for text-mode output only. Stored task data stays byte-exact, and
// JSON output never needs it because encoding/json escapes every byte below
// 0x20 and a bidi control cannot reorder a quoted JSON string into a different
// field.
func DisplayLine(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
