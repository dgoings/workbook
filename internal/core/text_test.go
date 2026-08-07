package core

import "testing"

func TestDisplayLineReplacesControlRunesAndCollapsesWhitespace(t *testing.T) {
	// Mutation caught: stripping only ASCII escapes, or collapsing whitespace
	// without removing the control bytes hidden inside a word.
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text is unchanged", "Plan storage", "Plan storage"},
		{"escape sequences lose their ESC byte", "benign\x1b[2K\x1b[1Gforged", "benign [2K [1Gforged"},
		{"newlines become single spaces", "ok\nStatus: done\n\nHead: x", "ok Status: done Head: x"},
		{"tabs and carriage returns collapse", "a\tb\r\nc", "a b c"},
		{"unicode control runes are replaced", "a\u0085b\u009bc", "a b c"},
		{"surrounding whitespace is trimmed", " \n padded \t ", "padded"},
		{"non-control unicode survives", "éclair ☂", "éclair ☂"},
	}
	for _, test := range cases {
		if got := DisplayLine(test.input); got != test.want {
			t.Errorf("%s: DisplayLine(%q) = %q, want %q", test.name, test.input, got, test.want)
		}
	}
}
