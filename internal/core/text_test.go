package core

import "testing"

func TestDisplayLineReplacesControlRunesAndCollapsesWhitespace(t *testing.T) {
	// Mutation caught: stripping only ASCII escapes, collapsing whitespace
	// without removing the control bytes hidden inside a word, or neutralizing
	// Cc while leaving the bidi controls of Cf to reorder the printed line.
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
		{"right-to-left override is replaced", "open \u202Egpj.exe", "open gpj.exe"},
		{"embedding and override controls are replaced", "a\u202Ab\u202Bc\u202Cd\u202De", "a b c d e"},
		{"directional isolates are replaced", "a\u2066b\u2067c\u2068d\u2069e", "a b c d e"},
		{"directional marks are replaced", "a\u200Eb\u200Fc\u061Cd", "a b c d"},
		{"benign format runes survive", "👩\u200D💻 \uFEFFready", "👩\u200D💻 \uFEFFready"},
	}
	for _, test := range cases {
		if got := DisplayLine(test.input); got != test.want {
			t.Errorf("%s: DisplayLine(%q) = %q, want %q", test.name, test.input, got, test.want)
		}
	}
}

func TestDisplayTextKeepsLayoutAndReplacesControlRunes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"indentation and tabs are kept", "- a\n  - b\n\tcode\n", "- a\n  - b\n\tcode\n"},
		{"runs of spaces and blank lines are kept", "a   b\n\n\nc  \n", "a   b\n\n\nc  \n"},
		{"a Windows line end is one line break", "a\r\nb\r\n", "a\nb\n"},
		{"a lone carriage return is a space", "a\rb", "a b"},
		{"escape sequences lose their ESC byte", "x\x1b[2Jy\x07", "x [2Jy "},
		{"C1 controls and DEL are replaced", "a\u0085b\u009bc\x7fd", "a b c d"},
		{"bidi controls are replaced", "gpj‮.exe⁦x", "gpj .exe x"},
		{"other format runes survive", "a‍b", "a‍b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayText(tc.input); got != tc.want {
				t.Fatalf("DisplayText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
