package core

import "math"

// LineMergeReason says why a three-way line merge gave no text. It is empty
// for a clean merge.
type LineMergeReason string

const (
	// LineMergeOverlaps reports changes of the two sides that touch and
	// differ. (The name keeps LineMergeOverlap free for the range type.)
	LineMergeOverlaps LineMergeReason = "overlap"
	// LineMergeTooManyChanges reports a diff of either side that passed the
	// edit bound or the work budget.
	LineMergeTooManyChanges LineMergeReason = "too-many-changes"
	// LineMergeTooLarge reports a clean merge longer than the byte bound.
	LineMergeTooLarge LineMergeReason = "too-large"
)

// maxLineMergeWork bounds each of a merge's two diffs: the Myers diagonal
// visits plus snake comparisons it may make. It is a count, not a clock, so
// the same texts give the same answer on every machine. It may be raised,
// never lowered.
const maxLineMergeWork = 1 << 26

// LineMergeLimits bounds a merge. EditLines is the inserted plus deleted
// lines each diff may search for; Work is each diff's step budget, as
// maxLineMergeWork counts it; Bytes is the longest merged text. A zero or
// negative limit is no limit.
type LineMergeLimits struct {
	EditLines int
	Work      int64
	Bytes     int
}

// RelicLineMergeLimits are the bounds a relic body merge runs under.
var RelicLineMergeLimits = LineMergeLimits{EditLines: maxRelicDiffEditLines, Work: maxLineMergeWork, Bytes: MaxRelicBodyBytes}

// LineMergeOverlap is one run of ancestor lines both sides changed
// differently, numbered as a unified diff numbers the from side: BaseStart is
// the first line (1-based), or, when BaseLines is 0, the line before the
// changes (0 at the top).
type LineMergeOverlap struct {
	BaseStart int `json:"baseStart"`
	BaseLines int `json:"baseLines"`
}

// LineMergeResult is a merge's outcome. Text is the merged text, never nil,
// for a clean merge (Reason ""), and nil otherwise. Overlaps lists every
// overlap, in ancestor order, when Reason is LineMergeOverlaps.
type LineMergeResult struct {
	Text     []byte
	Reason   LineMergeReason
	Overlaps []LineMergeOverlap
}

// lineMergeHunk is one side's change over the ancestor's lines [start, end),
// which it replaces with lines. A pure insertion has start == end.
type lineMergeHunk struct {
	start, end int
	lines      []string
}

// MergeLines merges two edits of ancestor line by line: current, the version
// that stands, and other, the edit merged into it. Lines split as the line
// diff splits them and compare byte for byte, terminators included, so line
// endings, a missing final newline and bytes that are not UTF-8 are kept as
// they are.
//
// Each side's diff from the ancestor gives change hunks. A hunk of one side
// touches a hunk of the other when their ancestor ranges meet or overlap,
// counting an insertion at either edge of a change and two insertions at one
// point. Touching hunks, transitively, form a region. A region one side
// changed takes that side's lines; a region both sides changed takes them only
// when both give the same bytes for it, and is otherwise an overlap. Lines
// outside every region are the ancestor's. These are git merge-file's
// non-zealous rules, where changes on adjacent lines conflict.
//
// The result depends only on the three texts and the limits: the inputs are
// only read, and no map order, clock or randomness reaches it.
func MergeLines(ancestor, current, other []byte, limits LineMergeLimits) LineMergeResult {
	editLines := limits.EditLines
	if editLines <= 0 {
		editLines = math.MaxInt
	}
	base := splitRelicLines(ancestor)
	currentHunks, ok := lineMergeHunks(base, splitRelicLines(current), editLines, limits.Work)
	if !ok {
		return LineMergeResult{Reason: LineMergeTooManyChanges}
	}
	otherHunks, ok := lineMergeHunks(base, splitRelicLines(other), editLines, limits.Work)
	if !ok {
		return LineMergeResult{Reason: LineMergeTooManyChanges}
	}

	merged := make([]byte, 0, max(len(current), len(other)))
	var overlaps []LineMergeOverlap
	at := 0 // the next ancestor line to copy
	for len(currentHunks) > 0 || len(otherHunks) > 0 {
		// A region starts at the earliest remaining hunk and takes in every
		// hunk of either side that touches it. Hunks of one side never touch
		// each other (an equal line separates them), so a hunk that starts
		// within the region's end touches the other side's hunk that set it.
		start := math.MaxInt
		if len(currentHunks) > 0 {
			start = currentHunks[0].start
		}
		if len(otherHunks) > 0 {
			start = min(start, otherHunks[0].start)
		}
		end := start
		takenCurrent, takenOther := 0, 0
		for {
			if takenCurrent < len(currentHunks) && currentHunks[takenCurrent].start <= end {
				end = max(end, currentHunks[takenCurrent].end)
				takenCurrent++
			} else if takenOther < len(otherHunks) && otherHunks[takenOther].start <= end {
				end = max(end, otherHunks[takenOther].end)
				takenOther++
			} else {
				break
			}
		}
		regionCurrent, regionOther := currentHunks[:takenCurrent], otherHunks[:takenOther]
		currentHunks, otherHunks = currentHunks[takenCurrent:], otherHunks[takenOther:]

		for _, line := range base[at:start] {
			merged = append(merged, line...)
		}
		at = end
		switch {
		case len(regionOther) == 0:
			merged = appendLineMergeRegion(merged, base, start, end, regionCurrent)
		case len(regionCurrent) == 0:
			merged = appendLineMergeRegion(merged, base, start, end, regionOther)
		default:
			currentText := appendLineMergeRegion(nil, base, start, end, regionCurrent)
			otherText := appendLineMergeRegion(nil, base, start, end, regionOther)
			if string(currentText) == string(otherText) {
				merged = append(merged, currentText...)
				continue
			}
			overlap := LineMergeOverlap{BaseStart: start, BaseLines: end - start}
			if overlap.BaseLines > 0 {
				overlap.BaseStart++
			}
			overlaps = append(overlaps, overlap)
		}
	}
	if overlaps != nil {
		return LineMergeResult{Reason: LineMergeOverlaps, Overlaps: overlaps}
	}
	for _, line := range base[at:] {
		merged = append(merged, line...)
	}
	if limits.Bytes > 0 && len(merged) > limits.Bytes {
		return LineMergeResult{Reason: LineMergeTooLarge}
	}
	return LineMergeResult{Text: merged}
}

// lineMergeHunks diffs the ancestor's lines against a side's and returns the
// side's change hunks in ancestor order: each run of non-equal lines of the
// edit script is one hunk. It reports false when the diff passed the edit
// bound or the work budget.
func lineMergeHunks(base, side []string, editLines int, work int64) ([]lineMergeHunk, bool) {
	kinds, approximate := relicLineEdits(base, side, editLines, work)
	if approximate {
		return nil, false
	}
	var hunks []lineMergeHunk
	x, y := 0, 0
	for index := 0; index < len(kinds); {
		if kinds[index] == DiffEqual {
			x, y, index = x+1, y+1, index+1
			continue
		}
		startX, startY := x, y
		for ; index < len(kinds) && kinds[index] != DiffEqual; index++ {
			if kinds[index] == DiffDelete {
				x++
			} else {
				y++
			}
		}
		hunks = append(hunks, lineMergeHunk{start: startX, end: x, lines: side[startY:y]})
	}
	return hunks, true
}

// appendLineMergeRegion appends one side's text for the ancestor's lines
// [start, end): its hunks there, and the ancestor's lines between them.
func appendLineMergeRegion(merged []byte, base []string, start, end int, hunks []lineMergeHunk) []byte {
	at := start
	for _, hunk := range hunks {
		for _, line := range base[at:hunk.start] {
			merged = append(merged, line...)
		}
		for _, line := range hunk.lines {
			merged = append(merged, line...)
		}
		at = hunk.end
	}
	for _, line := range base[at:end] {
		merged = append(merged, line...)
	}
	return merged
}
