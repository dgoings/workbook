package core

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMergeLinesTable(t *testing.T) {
	six := "1\n2\n3\n4\n5\n6\n"
	crlf := "1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n"
	for _, test := range []struct {
		name                     string
		ancestor, current, other string
		want                     string
		reason                   LineMergeReason
		overlaps                 []LineMergeOverlap
	}{
		{
			name:     "different-lines",
			ancestor: six, current: "1\nA2\n3\n4\n5\n6\n", other: "1\n2\n3\n4\nB5\n6\n",
			want: "1\nA2\n3\n4\nB5\n6\n",
		},
		{
			name:     "start-and-end",
			ancestor: six, current: "A1\n2\n3\n4\n5\n6\n", other: "1\n2\n3\n4\n5\nB6\n",
			want: "A1\n2\n3\n4\n5\nB6\n",
		},
		{
			name:     "adjacent-conflicts",
			ancestor: six, current: "1\n2\nA3\n4\n5\n6\n", other: "1\n2\n3\nB4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 3, BaseLines: 2}},
		},
		{
			name:     "insert-same-point-conflicts",
			ancestor: six, current: "1\n2\nA\n3\n4\n5\n6\n", other: "1\n2\nB\n3\n4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 2, BaseLines: 0}},
		},
		{
			name:     "insert-at-edge-of-change-conflicts",
			ancestor: six, current: "1\n2\nA3\n4\n5\n6\n", other: "1\n2\n3\nB\n4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 3, BaseLines: 1}},
		},
		{
			name:     "identical-edits-merge",
			ancestor: six, current: "1\nX2\n3\n4\nX5\n6\n", other: "1\nX2\n3\n4\n5\n6\n",
			want: "1\nX2\n3\n4\nX5\n6\n",
		},
		{
			name:     "identical-plus-extra-conflicts",
			ancestor: six, current: "1\n2\nX3\n4\n5\n6\n", other: "1\n2\nX3\nY4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 3, BaseLines: 2}},
		},
		{
			name:     "one-side-only",
			ancestor: six, current: "1\n2\n3\nA4\n5\n6\nA7\n", other: six,
			want: "1\n2\n3\nA4\n5\n6\nA7\n",
		},
		{
			name:     "both-delete-same-line",
			ancestor: six, current: "1\n2\n4\n5\n6\n", other: "1\n2\n4\n5\n6\n",
			want: "1\n2\n4\n5\n6\n",
		},
		{
			name:     "both-delete-same-line-and-other-edits",
			ancestor: six, current: "A1\n2\n4\n5\n6\n", other: "1\n2\n4\n5\nB6\n",
			want: "A1\n2\n4\n5\nB6\n",
		},
		{
			// A line kept between the two edits: the plan's "a\nb" ancestor
			// has B's change on line 1 touching A's change to line 2 (the
			// final newline added), so it conflicts; see the row after.
			name:     "missing-final-newline-append",
			ancestor: "a\nm\nb", current: "a\nm\nb\nc", other: "A\nm\nb",
			want: "A\nm\nb\nc",
		},
		{
			name:     "missing-final-newline-append-adjacent-conflicts",
			ancestor: "a\nb", current: "a\nb\nc", other: "A\nb",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 1, BaseLines: 2}},
		},
		{
			name:     "missing-final-newline-both-touch-last",
			ancestor: "a\nm\nb", current: "a\nm\nb\n", other: "a\nm\nB",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 3, BaseLines: 1}},
		},
		{
			name:     "missing-final-newline-kept-on-both",
			ancestor: "a\nm\nb", current: "A\nm\nb", other: "a\nm\nb\n",
			want: "A\nm\nb\n",
		},
		{
			name:     "crlf-whole-file",
			ancestor: six, current: crlf, other: "1\nB2\n3\n4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 1, BaseLines: 6}},
		},
		{
			name:     "crlf-kept",
			ancestor: crlf, current: "1\r\nA2\r\n3\r\n4\r\n5\r\n6\r\n", other: "1\r\n2\r\n3\r\n4\r\nB5\r\n6\r\n",
			want: "1\r\nA2\r\n3\r\n4\r\nB5\r\n6\r\n",
		},
		{
			name:     "non-utf8-bytes-kept",
			ancestor: "\xff1\n2\n3\n4\n\xfe5\n", current: "\xff1\nA\xc3\n3\n4\n\xfe5\n", other: "\xff1\n2\n3\n4\n\xfe5\xfd\n",
			want: "\xff1\nA\xc3\n3\n4\n\xfe5\xfd\n",
		},
		{
			name:     "empty-ancestor-two-inserts",
			ancestor: "", current: "a\n", other: "b\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 0, BaseLines: 0}},
		},
		{
			name:     "empty-ancestor-identical-inserts",
			ancestor: "", current: "a\n", other: "a\n",
			want: "a\n",
		},
		{
			name:     "empty-current",
			ancestor: six, current: "", other: "1\n2\nB3\n4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 1, BaseLines: 6}},
		},
		{
			name:     "both-empty-the-text",
			ancestor: six, current: "", other: "",
			want: "",
		},
		{
			name:     "two-overlaps-reported-in-order",
			ancestor: six + six, current: "1\nA2\n3\n4\n5\n6\n1\n2\n3\n4\nA5\n6\n", other: "1\nB2\n3\n4\n5\n6\n1\n2\n3\n4\n5\nB6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 2, BaseLines: 1}, {BaseStart: 11, BaseLines: 2}},
		},
		{
			// A region chained across both sides: A's change to 2, B's to
			// 3, A's to 4 touch in turn and form one overlap.
			name:     "chained-region",
			ancestor: six, current: "1\nA2\n3\nA4\n5\n6\n", other: "1\n2\nB3\n4\n5\n6\n",
			reason: LineMergeOverlaps, overlaps: []LineMergeOverlap{{BaseStart: 2, BaseLines: 3}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := MergeLines([]byte(test.ancestor), []byte(test.current), []byte(test.other), RelicLineMergeLimits)
			if got.Reason != test.reason {
				t.Fatalf("reason = %q, want %q (text %q, overlaps %+v)", got.Reason, test.reason, got.Text, got.Overlaps)
			}
			if test.reason != "" {
				if got.Text != nil {
					t.Errorf("text = %q, want nil on %q", got.Text, test.reason)
				}
				if !reflect.DeepEqual(got.Overlaps, test.overlaps) {
					t.Errorf("overlaps = %+v, want %+v", got.Overlaps, test.overlaps)
				}
				return
			}
			if got.Text == nil || string(got.Text) != test.want {
				t.Errorf("text = %q, want %q", got.Text, test.want)
			}
			if got.Overlaps != nil {
				t.Errorf("overlaps = %+v on a clean merge", got.Overlaps)
			}
		})
	}
}

// randomMergeLines is a seeded list of short lines drawn from a small
// alphabet, so that many lines repeat, with mixed terminators.
func randomMergeLines(random *rand.Rand, count int) []string {
	words := []string{"a", "b", "c", "", "same", "same", "x\r", "\xff"}
	lines := make([]string, count)
	for index := range lines {
		lines[index] = words[random.Intn(len(words))] + "\n"
	}
	return lines
}

// randomEdit changes lines at random: inserts, deletes and replacements, and
// sometimes drops the final newline.
func randomEdit(random *rand.Rand, lines []string) string {
	edited := append([]string(nil), lines...)
	for count := random.Intn(6); count > 0; count-- {
		at := random.Intn(len(edited) + 1)
		switch random.Intn(3) {
		case 0:
			edited = append(edited[:at], append([]string{fmt.Sprintf("new %d\n", random.Intn(4))}, edited[at:]...)...)
		case 1:
			if at < len(edited) {
				edited = append(edited[:at], edited[at+1:]...)
			}
		case 2:
			if at < len(edited) {
				edited[at] = fmt.Sprintf("r %d\n", random.Intn(4))
			}
		}
	}
	text := strings.Join(edited, "")
	if random.Intn(5) == 0 {
		text = strings.TrimSuffix(text, "\n")
	}
	return text
}

func TestMergeLinesIsDeterministic(t *testing.T) {
	const seed = 1127
	random := rand.New(rand.NewSource(seed))
	clean := 0
	for run := 0; run < 200; run++ {
		ancestorLines := randomMergeLines(random, 1+random.Intn(30))
		ancestor := strings.Join(ancestorLines, "")
		current, other := randomEdit(random, ancestorLines), randomEdit(random, ancestorLines)

		first := MergeLines([]byte(ancestor), []byte(current), []byte(other), RelicLineMergeLimits)
		second := MergeLines([]byte(ancestor), []byte(current), []byte(other), RelicLineMergeLimits)
		// The same bytes in other backing arrays, with spare capacity
		// holding junk a merge must not read or write.
		copied := func(text string) []byte {
			buffer := make([]byte, len(text), len(text)+16)
			copy(buffer, text)
			copy(buffer[len(text):cap(buffer)], "JUNKJUNKJUNKJUNK")
			return buffer[:len(text)]
		}
		ancestorCopy, currentCopy, otherCopy := copied(ancestor), copied(current), copied(other)
		third := MergeLines(ancestorCopy, currentCopy, otherCopy, RelicLineMergeLimits)
		if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, third) {
			t.Fatalf("seed %d run %d: results differ:\n%+v\n%+v\n%+v", seed, run, first, second, third)
		}
		if string(ancestorCopy) != ancestor || string(currentCopy) != current || string(otherCopy) != other {
			t.Fatalf("seed %d run %d: the merge changed its inputs", seed, run)
		}
		if first.Reason == "" {
			clean++
		}
	}
	if clean == 0 || clean == 200 {
		t.Fatalf("seed %d: %d of 200 merges clean; want a mix", seed, clean)
	}
}

// mergeHunk is a change over the ancestor's lines [start, end), replaced by
// lines, as the test builds it independently of the merge.
type mergeHunk struct {
	start, end int
	lines      []string
}

// hunksFromKinds turns an edit script from ancestor to side into hunks.
func hunksFromKinds(side []string, kinds []DiffKind) []mergeHunk {
	var hunks []mergeHunk
	x, y := 0, 0
	for index := 0; index < len(kinds); {
		if kinds[index] == DiffEqual {
			x, y, index = x+1, y+1, index+1
			continue
		}
		hunk := mergeHunk{start: x}
		startY := y
		for ; index < len(kinds) && kinds[index] != DiffEqual; index++ {
			if kinds[index] == DiffDelete {
				x++
			} else {
				y++
			}
		}
		hunk.end, hunk.lines = x, side[startY:y]
		hunks = append(hunks, hunk)
	}
	return hunks
}

// applyHunks rebuilds a side from the ancestor and its hunks.
func applyHunks(ancestor []string, hunks []mergeHunk) string {
	var builder strings.Builder
	at := 0
	for _, hunk := range hunks {
		builder.WriteString(strings.Join(ancestor[at:hunk.start], ""))
		builder.WriteString(strings.Join(hunk.lines, ""))
		at = hunk.end
	}
	builder.WriteString(strings.Join(ancestor[at:], ""))
	return builder.String()
}

func hunksTouch(a, b mergeHunk) bool { return b.start <= a.end && a.start <= b.end }

// mergeRegion is a run of the ancestor's lines [start, end) that touching
// changes cover, with each side's text for it and which sides changed it.
type mergeRegion struct {
	start, end         int
	ancestor           string
	textA, textB       string
	changedA, changedB bool
}

// mergeRegions groups the two sides' hunks by the transitive closure of
// touching, found by repeated union rather than a sweep, and gives each
// region's text on both sides.
func mergeRegions(ancestor []string, aHunks, bHunks []mergeHunk) []mergeRegion {
	type tagged struct {
		hunk  mergeHunk
		sideA bool
	}
	var all []tagged
	for _, hunk := range aHunks {
		all = append(all, tagged{hunk, true})
	}
	for _, hunk := range bHunks {
		all = append(all, tagged{hunk, false})
	}
	group := make([]int, len(all))
	for index := range group {
		group[index] = index
	}
	for changed := true; changed; {
		changed = false
		for i := range all {
			for j := range all {
				if all[i].sideA && !all[j].sideA && hunksTouch(all[i].hunk, all[j].hunk) && group[i] != group[j] {
					low, high := min(group[i], group[j]), max(group[i], group[j])
					for k := range group {
						if group[k] == high {
							group[k] = low
						}
					}
					changed = true
				}
			}
		}
	}
	var regions []mergeRegion
	for root := range all {
		region := mergeRegion{start: -1}
		var membersA, membersB []mergeHunk
		for index, member := range all {
			if group[index] != root {
				continue
			}
			if region.start < 0 || member.hunk.start < region.start {
				region.start = member.hunk.start
			}
			region.end = max(region.end, member.hunk.end)
			if member.sideA {
				membersA, region.changedA = append(membersA, member.hunk), true
			} else {
				membersB, region.changedB = append(membersB, member.hunk), true
			}
		}
		if region.start < 0 {
			continue
		}
		window := ancestor[region.start:region.end]
		shift := func(hunks []mergeHunk) []mergeHunk {
			shifted := make([]mergeHunk, len(hunks))
			for index, hunk := range hunks {
				shifted[index] = mergeHunk{hunk.start - region.start, hunk.end - region.start, hunk.lines}
			}
			return shifted
		}
		region.ancestor = strings.Join(window, "")
		region.textA, region.textB = applyHunks(window, shift(membersA)), applyHunks(window, shift(membersB))
		regions = append(regions, region)
	}
	// Order by position; regions never share a start.
	for i := range regions {
		for j := i + 1; j < len(regions); j++ {
			if regions[j].start < regions[i].start || (regions[j].start == regions[i].start && regions[j].end < regions[i].end) {
				regions[i], regions[j] = regions[j], regions[i]
			}
		}
	}
	return regions
}

// rebuildMerge joins the ancestor's lines outside the regions with each
// region's text, putting back the ancestor's text for a region only the
// dropped side changed.
func rebuildMerge(ancestor []string, regions []mergeRegion, dropA, dropB bool) string {
	var builder strings.Builder
	at := 0
	for _, region := range regions {
		builder.WriteString(strings.Join(ancestor[at:region.start], ""))
		switch {
		case region.changedA && !(dropA && !region.changedB):
			builder.WriteString(region.textA)
		case region.changedB && !(dropB && !region.changedA):
			builder.WriteString(region.textB)
		default:
			builder.WriteString(region.ancestor)
		}
		at = region.end
	}
	builder.WriteString(strings.Join(ancestor[at:], ""))
	return builder.String()
}

// A clean merge never drops, duplicates or invents a line: it is the
// ancestor with every region's change applied, putting back either side's
// own changes gives the other side, and an overlap is reported exactly
// where both sides changed a region differently.
func TestMergeLinesRebuildsEachSideOutsideOverlaps(t *testing.T) {
	const seed = 20261127
	const cases = 2000
	random := rand.New(rand.NewSource(seed))
	clean, separatedClean, shared := 0, 0, 0
	for run := 0; run < cases; run++ {
		separated := run%2 == 0
		var ancestorLines []string
		var current, other string
		if separated {
			// Distinct lines and edits kept at least two lines apart: the
			// sides' changes never touch, so the merge must be clean.
			for index := range 4 + random.Intn(40) {
				ancestorLines = append(ancestorLines, fmt.Sprintf("o%d\n", index))
			}
			current, other = separatedEdits(random, ancestorLines, run)
		} else {
			ancestorLines = randomMergeLines(random, 1+random.Intn(30))
			current, other = randomEdit(random, ancestorLines), randomEdit(random, ancestorLines)
		}
		ancestor := strings.Join(ancestorLines, "")
		label := fmt.Sprintf("seed %d run %d (ancestor %q, current %q, other %q)", seed, run, ancestor, current, other)

		got := MergeLines([]byte(ancestor), []byte(current), []byte(other), RelicLineMergeLimits)
		baseLines := splitRelicLines([]byte(ancestor))
		currentLines, otherLines := splitRelicLines([]byte(current)), splitRelicLines([]byte(other))
		kindsA, approximateA := relicLineEdits(baseLines, currentLines, maxRelicDiffEditLines, maxLineMergeWork)
		kindsB, approximateB := relicLineEdits(baseLines, otherLines, maxRelicDiffEditLines, maxLineMergeWork)
		if approximateA || approximateB {
			t.Fatalf("%s: small diff approximate", label)
		}
		aHunks, bHunks := hunksFromKinds(currentLines, kindsA), hunksFromKinds(otherLines, kindsB)
		if applyHunks(baseLines, aHunks) != current || applyHunks(baseLines, bHunks) != other {
			t.Fatalf("%s: hunks do not rebuild the sides", label)
		}
		regions := mergeRegions(baseLines, aHunks, bHunks)
		var wantOverlaps []LineMergeOverlap
		for _, region := range regions {
			if region.changedA && region.changedB {
				if region.textA != region.textB {
					start := region.start
					if region.end > region.start {
						start++
					}
					wantOverlaps = append(wantOverlaps, LineMergeOverlap{BaseStart: start, BaseLines: region.end - region.start})
				} else {
					shared++
				}
			}
		}

		if wantOverlaps != nil {
			if separated {
				t.Fatalf("%s: separated edits touch", label)
			}
			if got.Reason != LineMergeOverlaps || got.Text != nil || !reflect.DeepEqual(got.Overlaps, wantOverlaps) {
				t.Fatalf("%s: got %q text %q overlaps %+v, want overlaps %+v", label, got.Reason, got.Text, got.Overlaps, wantOverlaps)
			}
			continue
		}
		if got.Reason != "" || got.Text == nil || got.Overlaps != nil {
			t.Fatalf("%s: got %q text %q overlaps %+v, want a clean merge", label, got.Reason, got.Text, got.Overlaps)
		}
		clean++
		if separated {
			separatedClean++
		}
		if want := rebuildMerge(baseLines, regions, false, false); string(got.Text) != want {
			t.Fatalf("%s: merged %q, want %q", label, got.Text, want)
		}
		if without := rebuildMerge(baseLines, regions, false, true); without != current {
			t.Fatalf("%s: the merge without B's own changes is %q, want A %q", label, without, current)
		}
		if without := rebuildMerge(baseLines, regions, true, false); without != other {
			t.Fatalf("%s: the merge without A's own changes is %q, want B %q", label, without, other)
		}
	}
	t.Logf("seed %d: %d cases, %d clean (%d of %d separated), %d regions both sides changed alike", seed, cases, clean, separatedClean, cases/2, shared)
	if separatedClean != cases/2 || clean == separatedClean || shared == 0 {
		t.Fatalf("seed %d: %d of %d separated cases clean, %d others clean, %d shared regions; want every separated case, some others and some shared", seed, separatedClean, cases/2, clean-separatedClean, shared)
	}
}

// separatedEdits makes two edits of distinct ancestor lines in which every
// change of one side is at least two lines from every change of the other,
// so no change of one touches a change of the other.
func separatedEdits(random *rand.Rand, ancestor []string, run int) (current, other string) {
	kinds := [3]map[int]int{nil, {}, {}}
	last := [3]int{-10, -10, -10}
	for index := range ancestor {
		if random.Intn(3) != 0 {
			continue
		}
		who := 1 + random.Intn(2)
		if index-last[3-who] < 2 {
			continue
		}
		kinds[who][index], last[who] = random.Intn(3), index
	}
	build := func(who int) string {
		var builder strings.Builder
		for index, line := range ancestor {
			kind, ok := kinds[who][index]
			switch {
			case ok && kind == 0: // delete
			case ok && kind == 1: // replace
				fmt.Fprintf(&builder, "s%d r%d.%d\n", who, run, index)
			case ok && kind == 2: // insert before
				fmt.Fprintf(&builder, "s%d i%d.%d\n%s", who, run, index, line)
			default:
				builder.WriteString(line)
			}
		}
		return builder.String()
	}
	return build(1), build(2)
}

func TestMergeLinesWorkBudgetIsDeterministic(t *testing.T) {
	lineCount := (1 << 20) / 2
	lines := make([]string, lineCount)
	for index := range lines {
		lines[index] = "x\n"
	}
	ancestor := strings.Join(lines, "")
	edit := func(changes map[int]string) string {
		edited := append([]string(nil), lines...)
		for line, text := range changes {
			edited[line-1] = text
		}
		return strings.Join(edited, "")
	}
	// Alternating lines with 400 of them replaced: an exact diff of under
	// 2,000 edit lines whose search slides along long snakes on many
	// diagonals, so it passes the work budget and must be too-many-changes.
	alternating := make([]string, lineCount)
	for index := range alternating {
		alternating[index] = "a\n"
		if index%2 == 1 {
			alternating[index] = "b\n"
		}
	}
	replaced := append([]string(nil), alternating...)
	random := rand.New(rand.NewSource(1))
	for range 400 {
		replaced[1+random.Intn(lineCount-2)] = "c\n"
	}
	alternatingText := strings.Join(alternating, "")
	for _, test := range []struct {
		name                     string
		ancestor, current, other string
		want                     LineMergeReason
	}{
		// One edit on each side: the trim leaves almost nothing to search.
		{"one-edit-each", ancestor, edit(map[int]string{10: "a\n"}), edit(map[int]string{500_000: "b\n"}), ""},
		// Two far-apart edits on each side: each diff searches a middle of
		// half a million identical lines, where the budget bounds the work.
		{"two-edits-each", ancestor, edit(map[int]string{10: "a\n", 500_000: "a\n"}), edit(map[int]string{20: "b\n", 499_990: "b\n"}), ""},
		{"past-the-work-budget", alternatingText, strings.Join(replaced, ""), alternatingText, LineMergeTooManyChanges},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			first := MergeLines([]byte(test.ancestor), []byte(test.current), []byte(test.other), RelicLineMergeLimits)
			elapsed := time.Since(started)
			second := MergeLines([]byte(test.ancestor), []byte(test.current), []byte(test.other), RelicLineMergeLimits)
			t.Logf("1 MiB merge %s: reason %q, %d bytes, %s", test.name, first.Reason, len(first.Text), elapsed)
			if first.Reason != test.want {
				t.Fatalf("reason = %q, want %q", first.Reason, test.want)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("two runs differ: %q and %q", first.Reason, second.Reason)
			}
			if test.name == "one-edit-each" {
				if want := edit(map[int]string{10: "a\n", 500_000: "b\n"}); string(first.Text) != want {
					t.Fatal("the merged text is not both edits")
				}
			}
			if elapsed > 2*time.Second {
				t.Fatalf("the merge took %s, want under 2 s", elapsed)
			}
		})
	}
}

// The budget is a count of steps: a tiny budget gives up on a search the
// unbounded search finishes, and the same budget gives the same answer.
func TestMyersLineEditsWorkBudgetIsACount(t *testing.T) {
	a := make([]int32, 200)
	b := make([]int32, 200)
	for index := range a {
		a[index], b[index] = int32(index), int32(index)
	}
	b[50], b[150] = -1, -2
	if _, exact := myersLineEdits(a, b, maxRelicDiffEditLines, 0); !exact {
		t.Fatal("unbounded search is approximate")
	}
	if _, exact := myersLineEdits(a, b, maxRelicDiffEditLines, 1<<20); !exact {
		t.Fatal("a large budget is approximate")
	}
	if _, exact := myersLineEdits(a, b, maxRelicDiffEditLines, 10); exact {
		t.Fatal("a budget of 10 steps finished a search of 200 lines")
	}
}

func TestMergeLinesTooLarge(t *testing.T) {
	ancestor := "1\n2\n3\n4\n5\n6\n"
	current := "1\nA2\n3\n4\n5\n6\n"
	other := "1\n2\n3\n4\nB5\n6\n"
	limits := RelicLineMergeLimits
	limits.Bytes = len("1\nA2\n3\n4\nB5\n6\n")
	if got := MergeLines([]byte(ancestor), []byte(current), []byte(other), limits); got.Reason != "" {
		t.Fatalf("a merge at the limit gives %q", got.Reason)
	}
	limits.Bytes--
	got := MergeLines([]byte(ancestor), []byte(current), []byte(other), limits)
	if got.Reason != LineMergeTooLarge || got.Text != nil || got.Overlaps != nil {
		t.Fatalf("merge one byte over the limit = %+v, want too-large and no text", got)
	}
}

func TestMergeLinesApproximateIsTooManyChanges(t *testing.T) {
	base := numberedLines(5000, "line %d\n")
	lines := splitRelicLines([]byte(base))
	// 2,001 alternating edits on A: every other line replaced, past the
	// edit bound of 2,000 inserted plus deleted lines.
	edited := append([]string(nil), lines...)
	for index := 0; index < 2*2001; index += 2 {
		edited[index] = fmt.Sprintf("A %d\n", index)
	}
	current := strings.Join(edited, "")
	other := strings.Replace(base, "line 4999\n", "B\n", 1)
	got := MergeLines([]byte(base), []byte(current), []byte(other), RelicLineMergeLimits)
	if got.Reason != LineMergeTooManyChanges || got.Text != nil {
		t.Fatalf("2,001 alternating edits = %q (text %d bytes), want too-many-changes", got.Reason, len(got.Text))
	}
	// The other side approximate gives the same.
	got = MergeLines([]byte(base), []byte(other), []byte(current), RelicLineMergeLimits)
	if got.Reason != LineMergeTooManyChanges {
		t.Fatalf("approximate other side = %q, want too-many-changes", got.Reason)
	}
	// A lower edit bound from the caller's limits is honoured.
	limits := RelicLineMergeLimits
	limits.EditLines = 1
	got = MergeLines([]byte(base), []byte(strings.Replace(base, "line 1\n", "A\n", 1)), []byte(other), limits)
	if got.Reason != LineMergeTooManyChanges {
		t.Fatalf("two changed lines with an edit bound of 1 = %q, want too-many-changes", got.Reason)
	}
}

// LineDiff searches with no work budget, so story 10's hunks are unchanged,
// and a budget large enough for the search gives the same edit script.
func TestLineDiffUnchangedByBudget(t *testing.T) {
	base := numberedLines(30, "%d\n")
	for _, test := range []struct {
		name     string
		from, to string
		context  int
		want     []RelicDiffHunk
	}{
		{"one line changed", "a\nb\nc\n", "a\nB\nc\n", 0, []RelicDiffHunk{{FromStart: 2, FromLines: 1, ToStart: 2, ToLines: 1, Lines: []RelicDiffLine{
			{Kind: DiffDelete, Text: "b", From: 2}, {Kind: DiffInsert, Text: "B", To: 2}}}}},
		{"insertion", "a\nb\n", "a\nnew\nb\n", 0, []RelicDiffHunk{{FromStart: 1, FromLines: 0, ToStart: 2, ToLines: 1, Lines: []RelicDiffLine{
			{Kind: DiffInsert, Text: "new", To: 2}}}}},
		{"no final newline", "a\nb", "a\nc", -1, []RelicDiffHunk{{FromStart: 1, FromLines: 2, ToStart: 1, ToLines: 2, Lines: []RelicDiffLine{
			{Kind: DiffEqual, Text: "a", From: 1, To: 1}, {Kind: DiffDelete, Text: "b", From: 2, NoNewline: true}, {Kind: DiffInsert, Text: "c", To: 2, NoNewline: true}}}}},
		{"body added", "", "a\n", 3, []RelicDiffHunk{{FromStart: 0, FromLines: 0, ToStart: 1, ToLines: 1, Lines: []RelicDiffLine{
			{Kind: DiffInsert, Text: "a", To: 1}}}}},
		{"two hunks", base, strings.Replace(strings.Replace(base, "\n5\n", "\n5 changed\n", 1), "\n15\n", "\n15 changed\n", 1), 0, []RelicDiffHunk{
			{FromStart: 5, FromLines: 1, ToStart: 5, ToLines: 1, Lines: []RelicDiffLine{{Kind: DiffDelete, Text: "5", From: 5}, {Kind: DiffInsert, Text: "5 changed", To: 5}}},
			{FromStart: 15, FromLines: 1, ToStart: 15, ToLines: 1, Lines: []RelicDiffLine{{Kind: DiffDelete, Text: "15", From: 15}, {Kind: DiffInsert, Text: "15 changed", To: 15}}},
		}},
	} {
		got, approximate := LineDiff([]byte(test.from), []byte(test.to), test.context)
		if approximate || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: LineDiff = %+v (approximate %v), want %+v", test.name, got, approximate, test.want)
		}
	}

	random := rand.New(rand.NewSource(1127))
	for run := 0; run < 200; run++ {
		fromLines := randomMergeLines(random, 1+random.Intn(60))
		from, to := strings.Join(fromLines, ""), randomEdit(random, fromLines)
		a, b := splitRelicLines([]byte(from)), splitRelicLines([]byte(to))
		unbounded, approximateUnbounded := relicLineEdits(a, b, maxRelicDiffEditLines, 0)
		budgeted, approximateBudgeted := relicLineEdits(a, b, maxRelicDiffEditLines, maxLineMergeWork)
		if approximateUnbounded || approximateBudgeted || !reflect.DeepEqual(unbounded, budgeted) {
			t.Fatalf("run %d: the budget changed the edit script of %q to %q", run, from, to)
		}
	}
}

func TestRelicMergeBoundsArePinned(t *testing.T) {
	if maxLineMergeWork != 1<<26 {
		t.Errorf("maxLineMergeWork = %d, want 1<<26: it may be raised, never lowered", maxLineMergeWork)
	}
	want := LineMergeLimits{EditLines: maxRelicDiffEditLines, Work: maxLineMergeWork, Bytes: MaxRelicBodyBytes}
	if RelicLineMergeLimits != want {
		t.Errorf("RelicLineMergeLimits = %+v, want %+v", RelicLineMergeLimits, want)
	}
	if RelicLineMergeLimits.EditLines != 2000 || RelicLineMergeLimits.Bytes != 1<<20 {
		t.Errorf("RelicLineMergeLimits = %+v, want 2,000 edit lines and 1 MiB", RelicLineMergeLimits)
	}
}
