package core

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// rebuildSides joins a diff's lines back into the two texts they came from.
func rebuildSides(lines []RelicDiffLine) (from, to string) {
	var fromText, toText strings.Builder
	write := func(builder *strings.Builder, line RelicDiffLine) {
		builder.WriteString(line.Text)
		if !line.NoNewline {
			builder.WriteByte('\n')
		}
	}
	for _, line := range lines {
		if line.Kind != DiffInsert {
			write(&fromText, line)
		}
		if line.Kind != DiffDelete {
			write(&toText, line)
		}
	}
	return fromText.String(), toText.String()
}

// requireRebuilds checks a full-context diff of a and b: one hunk, numbered
// 1.. on each side, rebuilding both texts byte for byte.
func requireRebuilds(t *testing.T, label, a, b string) []RelicDiffLine {
	t.Helper()
	hunks, _ := LineDiff([]byte(a), []byte(b), -1)
	if a == b {
		if len(hunks) != 0 {
			t.Fatalf("%s: equal texts gave %d hunks", label, len(hunks))
		}
		return nil
	}
	if len(hunks) != 1 {
		t.Fatalf("%s: full context gave %d hunks, want 1", label, len(hunks))
	}
	hunk := hunks[0]
	fromText, toText := rebuildSides(hunk.Lines)
	if fromText != a || toText != b {
		t.Fatalf("%s: rebuilt\n from %q\n   to %q\nwant\n from %q\n   to %q", label, fromText, toText, a, b)
	}
	fromNumber, toNumber := 0, 0
	for _, line := range hunk.Lines {
		if line.Kind != DiffInsert {
			fromNumber++
			if line.From != fromNumber {
				t.Fatalf("%s: from line number %d, want %d", label, line.From, fromNumber)
			}
		} else if line.From != 0 {
			t.Fatalf("%s: inserted line has from number %d", label, line.From)
		}
		if line.Kind != DiffDelete {
			toNumber++
			if line.To != toNumber {
				t.Fatalf("%s: to line number %d, want %d", label, line.To, toNumber)
			}
		} else if line.To != 0 {
			t.Fatalf("%s: deleted line has to number %d", label, line.To)
		}
		if strings.Contains(line.Text, "\n") {
			t.Fatalf("%s: line text %q holds a newline", label, line.Text)
		}
	}
	if hunk.FromLines != fromNumber || hunk.ToLines != toNumber {
		t.Fatalf("%s: hunk counts %d,%d, want %d,%d", label, hunk.FromLines, hunk.ToLines, fromNumber, toNumber)
	}
	return hunk.Lines
}

func numberedLines(count int, format string) string {
	var builder strings.Builder
	for index := 1; index <= count; index++ {
		fmt.Fprintf(&builder, format, index)
	}
	return builder.String()
}

func TestLineDiffRebuildsBothTexts(t *testing.T) {
	base := numberedLines(8, "line %d\n")
	for _, test := range []struct{ label, a, b string }{
		{"equal", base, base},
		{"empty to text", "", base},
		{"text to empty", base, ""},
		{"one line changed", base, strings.Replace(base, "line 4\n", "line four\n", 1)},
		{"inserted at top", base, "new\n" + base},
		{"inserted in the middle", base, strings.Replace(base, "line 5\n", "new\nline 5\n", 1)},
		{"inserted at the end", base, base + "new\nnewer\n"},
		{"no final newline from", "a\nb", "a\nb\n"},
		{"no final newline to", "a\nb\n", "a\nb"},
		{"no final newline either", "a\nb", "a\nc"},
		{"no final newline, last line kept", "a\nb", "z\na\nb"},
		{"CRLF", "a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n"},
		{"CR only change", "a\nb\n", "a\r\nb\n"},
		{"single line no newline", "", "x"},
	} {
		requireRebuilds(t, test.label, test.a, test.b)
	}

	lines := requireRebuilds(t, "no final newline either", "a\nb", "a\nc")
	if last := lines[len(lines)-1]; !last.NoNewline || last.Kind != DiffInsert || last.Text != "c" {
		t.Errorf("last line = %+v, want inserted c without newline", last)
	}
	lines = requireRebuilds(t, "CRLF", "a\r\nb\r\n", "a\r\nB\r\n")
	if lines[1].Text != "b\r" {
		t.Errorf("CRLF line text = %q, want the carriage return kept", lines[1].Text)
	}

	random := rand.New(rand.NewSource(1127))
	words := []string{"alpha", "beta", "gamma", "delta", "", "  indented", "same", "same"}
	original := make([]string, 1400)
	for index := range original {
		original[index] = fmt.Sprintf("%s %d", words[random.Intn(len(words))], random.Intn(50))
	}
	for run := 0; run < 500; run++ {
		edited := append([]string(nil), original...)
		for count := 1 + random.Intn(20); count > 0; count-- {
			at := random.Intn(len(edited) + 1)
			switch random.Intn(3) {
			case 0:
				edited = append(edited[:at], append([]string{fmt.Sprintf("new %d", random.Intn(1000))}, edited[at:]...)...)
			case 1:
				if at < len(edited) {
					edited = append(edited[:at], edited[at+1:]...)
				}
			case 2:
				if at < len(edited) {
					edited[at] = words[random.Intn(len(words))]
				}
			}
		}
		a := strings.Join(original, "\n") + "\n"
		b := strings.Join(edited, "\n")
		if random.Intn(2) == 0 {
			b += "\n"
		}
		requireRebuilds(t, fmt.Sprintf("random edit %d", run), a, b)
	}
}

func TestLineDiffContextAndMerging(t *testing.T) {
	base := numberedLines(30, "%d\n")
	edit := func(lines ...int) string {
		text := base
		for _, line := range lines {
			text = strings.Replace(text, fmt.Sprintf("\n%d\n", line), fmt.Sprintf("\n%d changed\n", line), 1)
		}
		return text
	}

	hunks, approximate := LineDiff([]byte(base), []byte(edit(5, 15)), 3)
	if approximate || len(hunks) != 2 {
		t.Fatalf("two edits 10 apart gave %d hunks (approximate %v), want 2", len(hunks), approximate)
	}
	for index, want := range []RelicDiffHunk{
		{FromStart: 2, FromLines: 7, ToStart: 2, ToLines: 7},
		{FromStart: 12, FromLines: 7, ToStart: 12, ToLines: 7},
	} {
		got := hunks[index]
		if got.FromStart != want.FromStart || got.FromLines != want.FromLines || got.ToStart != want.ToStart || got.ToLines != want.ToLines {
			t.Errorf("hunk %d = -%d,%d +%d,%d; want -%d,%d +%d,%d", index,
				got.FromStart, got.FromLines, got.ToStart, got.ToLines, want.FromStart, want.FromLines, want.ToStart, want.ToLines)
		}
		if len(got.Lines) != 8 || got.Lines[3].Kind != DiffDelete || got.Lines[4].Kind != DiffInsert {
			t.Errorf("hunk %d lines = %+v", index, got.Lines)
		}
	}

	hunks, _ = LineDiff([]byte(base), []byte(edit(5, 10)), 3)
	if len(hunks) != 1 || hunks[0].FromStart != 2 || hunks[0].FromLines != 12 || hunks[0].ToLines != 12 {
		t.Fatalf("two edits 5 apart = %+v, want one hunk -2,12 +2,12", hunks)
	}

	hunks, _ = LineDiff([]byte(base), []byte(edit(5, 15)), 0)
	if len(hunks) != 2 {
		t.Fatalf("context 0 gave %d hunks", len(hunks))
	}
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			if line.Kind == DiffEqual {
				t.Errorf("context 0 hunk holds an equal line %+v", line)
			}
		}
	}
	if hunks[0].FromStart != 5 || hunks[0].FromLines != 1 || hunks[0].ToStart != 5 || hunks[0].ToLines != 1 {
		t.Errorf("context 0 hunk = %+v", hunks[0])
	}

	// A pure insertion or deletion names the line before it on the side
	// without lines, as a unified diff does.
	hunks, _ = LineDiff([]byte("a\nb\n"), []byte("a\nnew\nb\n"), 0)
	if len(hunks) != 1 || hunks[0].FromStart != 1 || hunks[0].FromLines != 0 || hunks[0].ToStart != 2 || hunks[0].ToLines != 1 {
		t.Errorf("insertion hunk = %+v, want -1,0 +2,1", hunks)
	}
	hunks, _ = LineDiff(nil, []byte("a\n"), 3)
	if len(hunks) != 1 || hunks[0].FromStart != 0 || hunks[0].FromLines != 0 || hunks[0].ToStart != 1 || hunks[0].ToLines != 1 {
		t.Errorf("body added hunk = %+v, want -0,0 +1,1", hunks)
	}
	if hunks, _ := LineDiff([]byte(base), []byte(base), 3); hunks != nil {
		t.Errorf("equal texts gave %+v", hunks)
	}
}

// unrelatedText is a seeded text of about size bytes, a line break every
// twelfth word.
func unrelatedText(seed int64, size int) string {
	random := rand.New(rand.NewSource(seed))
	var builder strings.Builder
	for word := 1; builder.Len() < size; word++ {
		fmt.Fprintf(&builder, "w%x", random.Int63())
		if word%12 == 0 {
			builder.WriteByte('\n')
		} else {
			builder.WriteByte(' ')
		}
	}
	return builder.String()
}

func TestLineDiffFallsBackPastTheEditBound(t *testing.T) {
	if maxRelicDiffEditLines != 2000 {
		t.Fatalf("maxRelicDiffEditLines = %d, want 2000: the bound may be raised, never lowered", maxRelicDiffEditLines)
	}

	shared := "shared top\n"
	a := shared + unrelatedText(1, 1<<20) + "\nshared bottom\n"
	b := shared + unrelatedText(2, 1<<20) + "\nshared bottom\n"
	started := time.Now()
	hunks, approximate := LineDiff([]byte(a), []byte(b), -1)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("two unrelated 1 MiB texts took %s", elapsed)
	}
	if !approximate || len(hunks) != 1 {
		t.Fatalf("approximate %v, %d hunks; want approximate and one hunk", approximate, len(hunks))
	}
	lines := hunks[0].Lines
	fromText, toText := rebuildSides(lines)
	if fromText != a || toText != b {
		t.Fatal("the approximate diff does not rebuild both texts")
	}
	// Prefix, one delete run, one insert run, suffix.
	runs := []DiffKind{lines[0].Kind}
	for _, line := range lines[1:] {
		if line.Kind != runs[len(runs)-1] {
			runs = append(runs, line.Kind)
		}
	}
	if fmt.Sprint(runs) != fmt.Sprint([]DiffKind{DiffEqual, DiffDelete, DiffInsert, DiffEqual}) {
		t.Fatalf("runs = %v, want equal, delete, insert, equal", runs)
	}

	// Within the bound the diff is exact: 999 lines replaced and one
	// inserted are 1,999 changed lines, reported as exactly that.
	base := numberedLines(3000, "line %d\n")
	edited := strings.Replace(base, "line 1\n", "line 1\nadded\n", 1)
	for line := 1000; line < 1999; line++ {
		edited = strings.Replace(edited, fmt.Sprintf("line %d\n", line), fmt.Sprintf("LINE %d\n", line), 1)
	}
	hunks, approximate = LineDiff([]byte(base), []byte(edited), -1)
	if approximate {
		t.Fatal("1,999 changed lines are approximate")
	}
	deleted, inserted := 0, 0
	for _, line := range hunks[0].Lines {
		switch line.Kind {
		case DiffDelete:
			deleted++
		case DiffInsert:
			inserted++
		}
	}
	if deleted != 999 || inserted != 1000 {
		t.Fatalf("deleted %d, inserted %d; want 999 and 1000", deleted, inserted)
	}
	// One more changed line passes the bound.
	edited = strings.Replace(edited, "line 2500\n", "LINE 2500\n", 1)
	if _, approximate := LineDiff([]byte(base), []byte(edited), -1); !approximate {
		t.Fatal("2,001 changed lines are exact")
	}
}

func TestRelicBodyIsText(t *testing.T) {
	for _, test := range []struct {
		content []byte
		text    bool
	}{
		{[]byte("plain\x00with NUL\n"), true},
		{[]byte("héllo"), true},
		{nil, true},
		{[]byte{}, true},
		{[]byte{'a', 0xff, 'b'}, false},
	} {
		if got := RelicBodyIsText(test.content); got != test.text {
			t.Errorf("RelicBodyIsText(%q) = %v, want %v", test.content, got, test.text)
		}
	}
}
