package core

import (
	"strings"
	"unicode"
)

// DiffKind labels one span of a word-level text diff.
type DiffKind string

const (
	DiffEqual  DiffKind = "equal"
	DiffDelete DiffKind = "delete"
	DiffInsert DiffKind = "insert"
)

// DiffSpan is one contiguous run of text sharing a diff kind. Concatenating
// every equal and delete span reproduces the old text exactly, and every equal
// and insert span reproduces the new text, so a renderer never has to guess
// where whitespace belongs.
type DiffSpan struct {
	Kind DiffKind `json:"kind"`
	Text string   `json:"text"`
}

// maxDiffTokens bounds the edit-script search. Myers costs O((N+M)D), which is
// fast for prose but unbounded for two unrelated documents of arbitrary size.
// Past the bound the whole differing middle is reported as one replacement,
// which is still a true description of the change.
const maxDiffTokens = 20000

// WordDiff compares two texts a word at a time and returns the spans that turn
// before into after. Words and the whitespace between them are separate tokens,
// so the result is exact rather than normalized.
func WordDiff(before, after string) []DiffSpan {
	spans := make([]DiffSpan, 0, 8)
	if before == after {
		if before == "" {
			return spans
		}
		return append(spans, DiffSpan{Kind: DiffEqual, Text: before})
	}

	old, current := splitWords(before), splitWords(after)
	prefix := 0
	for prefix < len(old) && prefix < len(current) && old[prefix] == current[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(current)-prefix &&
		old[len(old)-1-suffix] == current[len(current)-1-suffix] {
		suffix++
	}

	appendSpan := func(kind DiffKind, text string) {
		if text == "" {
			return
		}
		if last := len(spans) - 1; last >= 0 && spans[last].Kind == kind {
			spans[last].Text += text
			return
		}
		spans = append(spans, DiffSpan{Kind: kind, Text: text})
	}

	appendSpan(DiffEqual, strings.Join(old[:prefix], ""))
	middleOld, middleNew := old[prefix:len(old)-suffix], current[prefix:len(current)-suffix]
	if len(middleOld)+len(middleNew) > maxDiffTokens {
		appendSpan(DiffDelete, strings.Join(middleOld, ""))
		appendSpan(DiffInsert, strings.Join(middleNew, ""))
	} else {
		for _, edit := range diffTokens(middleOld, middleNew) {
			appendSpan(edit.kind, edit.text)
		}
	}
	appendSpan(DiffEqual, strings.Join(old[len(old)-suffix:], ""))
	return spans
}

// splitWords tokenizes text into alternating runs of whitespace and
// non-whitespace. Keeping whitespace as its own token means the tokens
// concatenate back into the original text with nothing added or lost.
func splitWords(value string) []string {
	runes := []rune(value)
	tokens := make([]string, 0, len(runes)/4+1)
	for start := 0; start < len(runes); {
		space := unicode.IsSpace(runes[start])
		end := start + 1
		for end < len(runes) && unicode.IsSpace(runes[end]) == space {
			end++
		}
		tokens = append(tokens, string(runes[start:end]))
		start = end
	}
	return tokens
}

type tokenEdit struct {
	kind DiffKind
	text string
}

// diffTokens returns the shortest edit script between two token sequences using
// Myers' greedy algorithm, recording one search frontier per edit distance so
// the script can be recovered by walking the frontiers backwards.
func diffTokens(old, current []string) []tokenEdit {
	oldLength, currentLength := len(old), len(current)
	if oldLength == 0 && currentLength == 0 {
		return nil
	}
	maximum := oldLength + currentLength
	offset := maximum
	frontier := make([]int, 2*maximum+1)
	trace := make([][]int, 0, maximum+1)

	for distance := 0; distance <= maximum; distance++ {
		trace = append(trace, append([]int(nil), frontier...))
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			var x int
			if diagonal == -distance ||
				(diagonal != distance && frontier[offset+diagonal-1] < frontier[offset+diagonal+1]) {
				x = frontier[offset+diagonal+1]
			} else {
				x = frontier[offset+diagonal-1] + 1
			}
			y := x - diagonal
			for x < oldLength && y < currentLength && old[x] == current[y] {
				x++
				y++
			}
			frontier[offset+diagonal] = x
			if x >= oldLength && y >= currentLength {
				return backtrackTokens(old, current, trace, offset)
			}
		}
	}
	return backtrackTokens(old, current, trace, offset)
}

func backtrackTokens(old, current []string, trace [][]int, offset int) []tokenEdit {
	edits := make([]tokenEdit, 0, len(old)+len(current))
	x, y := len(old), len(current)
	for distance := len(trace) - 1; distance >= 0 && (x > 0 || y > 0); distance-- {
		previousX, previousY := 0, 0
		if distance > 0 {
			frontier := trace[distance]
			diagonal := x - y
			previousDiagonal := diagonal - 1
			if diagonal == -distance ||
				(diagonal != distance && frontier[offset+diagonal-1] < frontier[offset+diagonal+1]) {
				previousDiagonal = diagonal + 1
			}
			previousX = frontier[offset+previousDiagonal]
			previousY = previousX - previousDiagonal
		}
		for x > previousX && y > previousY {
			edits = append(edits, tokenEdit{kind: DiffEqual, text: old[x-1]})
			x--
			y--
		}
		if distance == 0 {
			break
		}
		if x > previousX {
			edits = append(edits, tokenEdit{kind: DiffDelete, text: old[x-1]})
			x--
		} else if y > previousY {
			edits = append(edits, tokenEdit{kind: DiffInsert, text: current[y-1]})
			y--
		}
	}
	for left, right := 0, len(edits)-1; left < right; left, right = left+1, right-1 {
		edits[left], edits[right] = edits[right], edits[left]
	}
	return edits
}
