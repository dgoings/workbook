package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// requireNoTerminalControl fails when text holds a character a terminal acts
// on, other than the newline that ends each line.
func requireNoTerminalControl(t *testing.T, label, text string) {
	t.Helper()
	for _, r := range text {
		if r == '\n' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Fatalf("%s: %q holds the control character %U", label, text, r)
		}
	}
}

// One part's body under `relic show <relic>#<part> --body` is the exact stored
// bytes when standard output is a file or a pipe, which is the case that needs
// them, and is filtered line by line like every other stored text when
// standard output is a terminal.
//
// No test here has a terminal to write to, and no other test in this package
// stands in for one, so the terminal case is asked of writeRelicPartBody
// directly, which runRelicShow calls with what writerIsTerminal says of
// standard output.
func TestRelicShowPartBodyIsExactForAPipeAndFilteredForATerminal(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Bodies", "--type", "spec").Relic.ID
	hostile := "\x1b[31mRED\x1b[0m \x1b]0;pwned\x07\nsecond \xe2\x80\xaeline\r\n\n  indented\twith a tab\n"
	binary := string([]byte{0xff, 0x1b, '[', '2', 'J', 0x00})
	added := mustUpdateRelic(t, repository, id,
		"--add-part", "--part-type", "note", "--body-file", writeRelicBodyFile(t, "hostile.txt", hostile),
		"--add-part", "--part-type", "note", "--body-file", writeRelicBodyFile(t, "binary.bin", binary),
		"--add-part", "--part-type", "note", "--body-file", writeRelicBodyFile(t, "empty.txt", ""),
		"--add-part", "--part-type", "note", "--body-file", writeRelicBodyFile(t, "unended.txt", "no newline at the end"))
	text, raw, empty, unended := added.Parts[0].ID, added.Parts[1].ID, added.Parts[2].ID, added.Parts[3].ID

	// A buffer is not a terminal, as a file or a pipe is not.
	for part, want := range map[string]string{text: hostile, raw: binary, empty: "", unended: "no newline at the end"} {
		code, stdout, stderr := run(t, repository, "relic", "show", id+"#"+part, "--body")
		if code != 0 || stderr != "" || stdout != want {
			t.Fatalf("show --body into a buffer = (%d, %q, %q), want exactly %q", code, stdout, stderr, want)
		}
	}

	toTerminal := func(part string) string {
		t.Helper()
		var output bytes.Buffer
		if err := writeRelicPartBody(&output, shownRelic(t, repository, id+"#"+part, "--body"), true); err != nil {
			t.Fatalf("writeRelicPartBody(terminal) error = %v", err)
		}
		return output.String()
	}
	shown := toTerminal(text)
	requireNoTerminalControl(t, "a body shown on a terminal", shown)
	// It is what the whole relic shows of the same body, without the indent.
	code, whole, stderr := run(t, repository, "relic", "show", id, "--body")
	if code != 0 || stderr != "" {
		t.Fatalf("relic show --body = (%d, %q, %q)", code, whole, stderr)
	}
	_, after, found := strings.Cut(whole, "  Body:\n")
	if !found {
		t.Fatalf("relic show --body = %q, want a body under the first part", whole)
	}
	lines, _, _ := strings.Cut(after, "#"+raw)
	var want strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(lines, "\n"), "\n") {
		want.WriteString(strings.TrimPrefix(line, "    ") + "\n")
	}
	if shown != want.String() {
		t.Fatalf("a body shown on a terminal = %q, want the whole relic's lines of it, %q", shown, want.String())
	}
	if !strings.Contains(shown, "RED") || !strings.Contains(shown, "pwned") || !strings.Contains(shown, "second") {
		t.Fatalf("a body shown on a terminal = %q, want its text kept", shown)
	}

	if got := toTerminal(empty); got != "" {
		t.Fatalf("an empty body shown on a terminal = %q, want nothing", got)
	}
	if got := toTerminal(unended); got != "no newline at the end\n" {
		t.Fatalf("a body with no last newline shown on a terminal = %q, want its line ended", got)
	}
	notText := toTerminal(raw)
	requireNoTerminalControl(t, "a body that is not text shown on a terminal", notText)
	if !strings.Contains(notText, "not text") || !strings.Contains(notText, "6 bytes") || strings.Contains(notText, "[2J") {
		t.Fatalf("a body that is not text shown on a terminal = %q, want a line saying so and none of its bytes", notText)
	}
}

// The base is echoed quoted in a refusal, in text output as in JSON, and a
// version typed in lower case names the version.
func TestRelicBodyBaseIsEchoedSafelyAndMatchedWithoutCase(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Bases", "--type", "spec").Relic.ID
	added := mustUpdateRelic(t, repository, id,
		"--add-part", "--part-type", "note", "--body-file", writeRelicBodyFile(t, "first.md", "first\n"),
		"--add-part", "--part-type", "note")
	part, bodiless := added.Parts[0].ID, added.Parts[1].ID
	second := writeRelicBodyFile(t, "second.md", "second\n")
	head := relicHeadOf(t, repository, id)

	hostile := "x\x1b[31mRED\xe2\x80\xae\x07"
	for label, target := range map[string]string{"a part with a body": part, "a part with no body": bodiless} {
		code, stdout, stderr := run(t, repository, "relic", "update", id, "--part", target, "--body-file", second, "--base", hostile)
		if code != 6 || stdout != "" || !strings.Contains(stderr, "RED") {
			t.Fatalf("%s, a base holding escapes = (%d, %q, %q), want exit 6", label, code, stdout, stderr)
		}
		requireNoTerminalControl(t, label+", a base holding escapes", stderr)
	}
	if now := relicHeadOf(t, repository, id); now != head {
		t.Fatalf("a refused save moved the relic from %s to %s", head, now)
	}

	saved := mustUpdateRelic(t, repository, id, "--part", part, "--body-file", second, "--base", strings.ToLower(part))
	if !saved.Changed || saved.Parts[0].Body.Version == part {
		t.Fatalf("a save from the current version typed in lower case = %+v, want a new version", saved)
	}
	message := relicErrorMessage(t, repository, 6, core.CategoryStaleWrite,
		"update", id, "--part", part, "--body-file", second, "--base", strings.ToLower(part))
	if !strings.Contains(message, "not "+part+";") || strings.Contains(message, strings.ToLower(part)) {
		t.Fatalf("a stale base typed in lower case: message %q, want the version shown as it is stored", message)
	}
	assertRelicHistoriesValid(t, repository, 1)
}
