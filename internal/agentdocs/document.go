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
	// markerOpener is what makes either marker a marker and what makes any four
	// bytes an HTML comment, and neutralOpener is what it becomes inside a body.
	// The entity renders as the same four characters everywhere Markdown is read,
	// so a value carrying one still says what it said — it just no longer opens a
	// comment or terminates this block.
	markerOpener  = "<!--"
	neutralOpener = "&lt;!--"
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
	body := d.managedBody()

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
	//
	// Both sides of this comparison are the body as it is written, never the
	// body as it was handed in; see managedBody.
	if block.body == body && block.recordedHash == hashBody(body) {
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
	body := d.managedBody()
	return beginPrefix + "generator=" + d.Generator + " sha256=" + hashBody(body) + " -->\n" +
		body + endMarker + "\n"
}

// managedBody is the body as it is written into a managed block: the block's
// own markers neutralized so that the block can be found again.
//
// This is the format defending its own invariant rather than a rendering
// nicety, and it belongs here because a body carrying the end marker cannot
// round-trip by construction. findBlock takes the first end marker after the
// begin marker, so such a body truncates its own block on the next read: the
// recorded hash covers the whole body, the hash of what is read back covers the
// truncated one, and the two never agree again. The file is then permanently
// StateModified — reported as somebody's local edit, refused by every refresh,
// and re-inserted ahead of its own tail by --force, which grows the file on
// every run. One authored display label carrying twenty-one bytes would do that
// to every clone of a project, including breaking `workbook setup` in each of
// them, so the marker is neutralized rather than trusted not to appear.
//
// Escaping rather than refusing is the deliberate half. A refusal would fail
// somebody's command over a value a teammate chose, which is the outcome this
// exists to prevent. Only the four bytes of a `<!--` opener are rewritten, and
// into an entity that renders as the same four characters, so the text still
// reads as what was written and no HTML comment starts anywhere in the body —
// including where one would swallow the rest of the document without going near
// this block's markers.
func (d Document) managedBody() string {
	return neutralizeMarkers(d.Body)
}

// neutralizeMarkers rewrites every comment opener a managed body carries. It
// touches nothing else: a body without one is returned byte for byte, which is
// what keeps every existing project's stamp valid.
//
// Every opener rather than only the two complete markers, because an opener
// that completes neither is still an opener. `<!-- note` terminates no block,
// and the drift patterns are line-anchored so nothing here notices it — but
// every Markdown renderer the committed file is read through opens a comment at
// it and swallows the file from there to the next `-->`, which in a generated
// document is usually the end of it. Two markers made this a rule with an
// exception nobody could see; one opener makes it the rule managedBody states.
//
// The rewrite is idempotent by construction, which is what lets a value be
// neutralized where it is rendered and again where the body is written without
// growing an entity per layer: `&lt;!--` carries no `<`, so neutralized text
// never matches an opener again — it does not even reach the replacement.
func neutralizeMarkers(body string) string {
	if !strings.Contains(body, markerOpener) {
		return body
	}
	return strings.ReplaceAll(body, markerOpener, neutralOpener)
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
