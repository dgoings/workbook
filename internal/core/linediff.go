package core

import "bytes"

// maxRelicDiffEditLines bounds the line diff's search: the inserted plus
// deleted lines it looks for once the common prefix and suffix are trimmed.
// Past it the differing middle is reported as one delete run and one insert
// run, which still rebuilds both texts. The search keeps one frontier of 2d+1
// entries per step d, so its memory is about 16 MB at the bound. It may be
// raised, never lowered.
const maxRelicDiffEditLines = 2000

// RelicDiffLine is one line of a body diff. Text has no line terminator; a
// carriage return stays in it. From and To are 1-based line numbers on each
// side, left out on the side the line is not on. NoNewline marks the last line
// of a body that does not end in a newline.
type RelicDiffLine struct {
	Kind      DiffKind `json:"kind"`
	Text      string   `json:"text"`
	From      int      `json:"from,omitempty"`
	To        int      `json:"to,omitempty"`
	NoNewline bool     `json:"noNewline,omitempty"`
}

// RelicDiffHunk is a run of diff lines in unified style. A start is the first
// line's number on that side, or, when the hunk has no line on that side, the
// number of the line before it (0 at the top), as a unified diff writes it.
type RelicDiffHunk struct {
	FromStart int             `json:"fromStart"`
	FromLines int             `json:"fromLines"`
	ToStart   int             `json:"toStart"`
	ToLines   int             `json:"toLines"`
	Lines     []RelicDiffLine `json:"lines"`
}

// splitRelicLines splits a body into lines, each keeping its "\n". The last
// line of a body that does not end in one has none, and an empty body has no
// lines.
func splitRelicLines(content []byte) []string {
	lines := make([]string, 0, bytes.Count(content, []byte{'\n'})+1)
	for len(content) > 0 {
		end := bytes.IndexByte(content, '\n')
		if end < 0 {
			end = len(content) - 1
		}
		lines = append(lines, string(content[:end+1]))
		content = content[end+1:]
	}
	return lines
}

// LineDiff compares two bodies line by line and returns unified hunks with the
// given lines of context, or one hunk spanning both bodies when context is
// negative. Equal bodies give no hunks. Lines split on "\n"; a line is equal to
// another only with the same terminator, so the from side's equal and delete
// lines rebuild from exactly, and the to side's equal and insert lines rebuild
// to, when every line is shown. approximate reports that the edit bound was
// passed and the differing middle is one replacement.
func LineDiff(from, to []byte, context int) (hunks []RelicDiffHunk, approximate bool) {
	if bytes.Equal(from, to) {
		return nil, false
	}
	fromLines, toLines := splitRelicLines(from), splitRelicLines(to)
	kinds, approximate := relicLineEdits(fromLines, toLines)
	lines := relicDiffLines(fromLines, toLines, kinds)
	// No hunk reaches further than every line, and the bound keeps the hunk
	// arithmetic from overflowing.
	context = min(context, len(lines))
	return relicDiffHunks(lines, context), approximate
}

// relicLineEdits returns the edit script turning from into to, one kind per
// line of the result in order: equal lines consume a line of each side, delete
// lines one of from and insert lines one of to.
func relicLineEdits(from, to []string) ([]DiffKind, bool) {
	ids := map[string]int32{}
	intern := func(lines []string) []int32 {
		interned := make([]int32, len(lines))
		for index, line := range lines {
			id, ok := ids[line]
			if !ok {
				id = int32(len(ids))
				ids[line] = id
			}
			interned[index] = id
		}
		return interned
	}
	a, b := intern(from), intern(to)

	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	middleA, middleB := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]

	kinds := make([]DiffKind, 0, len(a)+len(b))
	for range prefix {
		kinds = append(kinds, DiffEqual)
	}
	middle, exact := myersLineEdits(middleA, middleB, maxRelicDiffEditLines)
	if !exact {
		middle = middle[:0]
		for range middleA {
			middle = append(middle, DiffDelete)
		}
		for range middleB {
			middle = append(middle, DiffInsert)
		}
	}
	kinds = append(kinds, middle...)
	for range suffix {
		kinds = append(kinds, DiffEqual)
	}
	return kinds, !exact
}

// myersLineEdits runs Myers' greedy search for the shortest edit script, giving
// up when it needs more than bound inserted plus deleted lines. It records the
// frontier of each step, 2d+1 entries for step d, and walks them back.
func myersLineEdits(a, b []int32, bound int) ([]DiffKind, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, true
	}
	maximum := n + m
	if maximum > bound {
		if difference := n - m; difference > bound || -difference > bound {
			return nil, false
		}
		maximum = bound
	}
	offset := maximum + 1
	frontier := make([]int32, 2*offset+1)
	trace := make([][]int32, 0, 16)
	for d := 0; d <= maximum; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && frontier[offset+k-1] < frontier[offset+k+1]) {
				x = int(frontier[offset+k+1])
			} else {
				x = int(frontier[offset+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			frontier[offset+k] = int32(x)
			if x >= n && y >= m {
				trace = append(trace, append([]int32(nil), frontier[offset-d:offset+d+1]...))
				return backtrackLineEdits(trace, n, m), true
			}
		}
		trace = append(trace, append([]int32(nil), frontier[offset-d:offset+d+1]...))
	}
	return nil, false
}

// backtrackLineEdits recovers the edit script from the recorded frontiers.
// trace[d] holds the furthest x on each diagonal -d..d after step d.
func backtrackLineEdits(trace [][]int32, n, m int) []DiffKind {
	reversed := make([]DiffKind, 0, n+m)
	x, y := n, m
	for d := len(trace) - 1; d > 0; d-- {
		previous := trace[d-1]
		at := func(k int) int { return int(previous[k+d-1]) }
		k := x - y
		var previousK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			previousK = k + 1
		} else {
			previousK = k - 1
		}
		previousX := at(previousK)
		previousY := previousX - previousK
		startX, startY := previousX, previousY
		if previousK == k+1 {
			startY++
		} else {
			startX++
		}
		for x > startX && y > startY {
			reversed = append(reversed, DiffEqual)
			x--
			y--
		}
		if previousK == k+1 {
			reversed = append(reversed, DiffInsert)
		} else {
			reversed = append(reversed, DiffDelete)
		}
		x, y = previousX, previousY
	}
	for x > 0 && y > 0 {
		reversed = append(reversed, DiffEqual)
		x--
		y--
	}
	kinds := make([]DiffKind, len(reversed))
	for index, kind := range reversed {
		kinds[len(reversed)-1-index] = kind
	}
	return kinds
}

// relicDiffLines turns an edit script into numbered lines.
func relicDiffLines(from, to []string, kinds []DiffKind) []RelicDiffLine {
	lines := make([]RelicDiffLine, 0, len(kinds))
	x, y := 0, 0
	for _, kind := range kinds {
		var raw string
		line := RelicDiffLine{Kind: kind}
		switch kind {
		case DiffEqual:
			raw = from[x]
			x++
			y++
			line.From, line.To = x, y
		case DiffDelete:
			raw = from[x]
			x++
			line.From = x
		case DiffInsert:
			raw = to[y]
			y++
			line.To = y
		}
		if text, ok := cutNewline(raw); ok {
			line.Text = text
		} else {
			line.Text, line.NoNewline = raw, true
		}
		lines = append(lines, line)
	}
	return lines
}

func cutNewline(line string) (string, bool) {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		return line[:len(line)-1], true
	}
	return line, false
}

// relicDiffHunks groups numbered lines into hunks: every changed line with up
// to context equal lines on each side, merging hunks whose context touches. A
// negative context gives one hunk of every line.
func relicDiffHunks(lines []RelicDiffLine, context int) []RelicDiffHunk {
	if context < 0 {
		return []RelicDiffHunk{relicDiffHunk(lines, 0, len(lines))}
	}
	var hunks []RelicDiffHunk
	start, end := -1, -1
	for index, line := range lines {
		if line.Kind == DiffEqual {
			continue
		}
		low := max(index-context, 0)
		if start >= 0 && low > end {
			hunks = append(hunks, relicDiffHunk(lines, start, end))
			start = -1
		}
		if start < 0 {
			start = low
		}
		end = min(index+1+context, len(lines))
	}
	if start >= 0 {
		hunks = append(hunks, relicDiffHunk(lines, start, end))
	}
	return hunks
}

// relicDiffHunk is lines[start:end] as one hunk, with unified starts. Every
// line outside a hunk is equal, so the line before a hunk is numbered on both
// sides and gives the numbers before it.
func relicDiffHunk(lines []RelicDiffLine, start, end int) RelicDiffHunk {
	hunk := RelicDiffHunk{Lines: append([]RelicDiffLine(nil), lines[start:end]...)}
	for _, line := range hunk.Lines {
		if line.From != 0 {
			hunk.FromLines++
		}
		if line.To != 0 {
			hunk.ToLines++
		}
	}
	if start > 0 {
		before := lines[start-1]
		hunk.FromStart, hunk.ToStart = before.From, before.To
	}
	if hunk.FromLines > 0 {
		hunk.FromStart++
	}
	if hunk.ToLines > 0 {
		hunk.ToStart++
	}
	return hunk
}
