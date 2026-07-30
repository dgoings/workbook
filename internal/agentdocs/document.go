// Package agentdocs installs and refreshes the Workbook documentation that
// coding agents read, keeping user-authored content intact.
//
// Every managed artifact carries its own stamp, so no documentation state is
// recorded in project configuration. The stamp's generator version is
// diagnostic only: staleness is decided by re-rendering the expected body and
// comparing it, and the recorded hash exists solely to decide whether
// overwriting is safe.
package agentdocs

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// State describes how a managed artifact compares to its expected content.
type State string

const (
	// StateAbsent means the artifact carries no managed block.
	StateAbsent State = "absent"
	// StateCurrent means the managed block already matches.
	StateCurrent State = "current"
	// StateStale means Workbook wrote the block and its inputs have changed.
	StateStale State = "stale"
	// StateModified means the block no longer matches what Workbook recorded,
	// so overwriting it would discard someone's edit.
	StateModified State = "modified"
)

const (
	beginPrefix = "<!-- workbook:begin "
	endMarker   = "<!-- workbook:end -->"
)

var (
	beginPattern = regexp.MustCompile(`(?m)^<!-- workbook:begin [^\n]*-->$`)
	hashPattern  = regexp.MustCompile(`\bsha256=([0-9a-f]{64})\b`)
)

// Document is a managed artifact: a body Workbook generates, optionally
// preceded by a preamble written only when the file is first created.
type Document struct {
	// Generator is the Workbook version recorded in the stamp. It is shown to
	// humans reading the file and never used as a decision input.
	Generator string
	// Preamble is written ahead of the managed block when the file is created.
	// It is never rewritten afterwards.
	Preamble string
	// Body is the managed content between the markers.
	Body string
}

// Outcome is the result of comparing a document against a file.
type Outcome struct {
	State State
	// Contents is the file content that reflects this document. It equals the
	// input when the state is StateCurrent.
	Contents []byte
	// Changed reports whether Contents differs from the input.
	Changed bool
}

// Reconcile compares existing file contents against the document. A nil or
// empty existing value means the file does not exist. Callers decide whether
// to write Contents; a StateModified outcome should only be written on an
// explicit override.
func (d Document) Reconcile(existing []byte) Outcome {
	rendered := d.render()

	if len(existing) == 0 {
		return Outcome{State: StateAbsent, Contents: []byte(d.Preamble + rendered), Changed: true}
	}

	contents := string(existing)
	block, ok := findBlock(contents)
	if !ok {
		return Outcome{State: StateAbsent, Contents: []byte(appendBlock(contents, rendered)), Changed: true}
	}

	updated := contents[:block.start] + rendered + contents[block.end:]
	// The generator version is deliberately excluded here. A release that does
	// not change generated content must not mark every project stale.
	if block.body == d.Body && block.recordedHash == hashBody(d.Body) {
		return Outcome{State: StateCurrent, Contents: existing}
	}
	if block.recordedHash != "" && block.recordedHash == hashBody(block.body) {
		return Outcome{State: StateStale, Contents: []byte(updated), Changed: true}
	}
	return Outcome{State: StateModified, Contents: []byte(updated), Changed: true}
}

// Strip removes the managed block, preserving surrounding content. The
// returned state reports what was removed so callers can refuse to discard a
// modified block without an explicit override.
func Strip(existing []byte) (State, []byte) {
	contents := string(existing)
	block, ok := findBlock(contents)
	if !ok {
		return StateAbsent, existing
	}

	state := StateModified
	if block.recordedHash != "" && block.recordedHash == hashBody(block.body) {
		state = StateCurrent
	}

	remainder := strings.TrimRight(contents[:block.start], "\n")
	trailing := strings.TrimLeft(contents[block.end:], "\n")
	switch {
	case remainder == "" && trailing == "":
		return state, nil
	case remainder == "":
		return state, []byte(trailing)
	case trailing == "":
		return state, []byte(remainder + "\n")
	default:
		return state, []byte(remainder + "\n\n" + trailing)
	}
}

func (d Document) render() string {
	return beginPrefix + "generator=" + d.Generator + " sha256=" + hashBody(d.Body) + " -->\n" +
		d.Body + endMarker + "\n"
}

type block struct {
	// start and end bound the whole block including both marker lines.
	start, end   int
	beginLine    string
	body         string
	recordedHash string
}

func findBlock(contents string) (block, bool) {
	begin := beginPattern.FindStringIndex(contents)
	if begin == nil {
		return block{}, false
	}
	bodyStart := begin[1]
	if bodyStart < len(contents) && contents[bodyStart] == '\n' {
		bodyStart++
	}
	relative := strings.Index(contents[bodyStart:], endMarker)
	if relative < 0 {
		return block{}, false
	}
	bodyEnd := bodyStart + relative
	end := bodyEnd + len(endMarker)
	if end < len(contents) && contents[end] == '\n' {
		end++
	}

	beginLine := contents[begin[0]:begin[1]]
	recorded := ""
	if match := hashPattern.FindStringSubmatch(beginLine); match != nil {
		recorded = match[1]
	}
	return block{
		start:        begin[0],
		end:          end,
		beginLine:    beginLine,
		body:         contents[bodyStart:bodyEnd],
		recordedHash: recorded,
	}, true
}

func appendBlock(contents, rendered string) string {
	trimmed := strings.TrimRight(contents, "\n")
	if trimmed == "" {
		return rendered
	}
	return trimmed + "\n\n" + rendered
}

func hashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
