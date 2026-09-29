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
	// The entity renders as the same four characters in ordinary prose, so a
	// value carrying one still says what it said — it just no longer opens a
	// comment or terminates this block.
	//
	// Inside a code span it does not: CommonMark takes an entity reference in
	// code as the literal text it is spelled with, so a neutralized value quoted
	// as code reaches the reader as `&lt;!--`. That is accepted rather than
	// worked around. The pass is over the whole body, which by then is Markdown
	// with no record of which spans are code, and the alternative — leaving the
	// opener raw where it is quoted — swallows the document instead of showing
	// somebody five extra characters.
	markerOpener  = "<!--"
	neutralOpener = "&lt;!--"
)

var (
	// The optional carriage return is what lets a managed block be found in a
	// Windows checkout. A Git for Windows clone with the default
	// core.autocrlf=true writes every line of the user's AGENTS.md, CLAUDE.md
	// and SKILL.md with CRLF, and in multi-line mode `$` matches only just
	// before a newline — so without it the marker line ends `-->\r\n`, the block
	// is not found, and `workbook docs update` leaves a duplicate managed block
	// behind: one per fresh CRLF checkout, because the copy it appended was
	// spelled with LF and the run after that found that one instead, which also
	// left `workbook docs remove` removing the copy rather than the block the
	// file started with. The carriage return is part of the match so that the
	// marker line is replaced whole rather than leaving one behind ahead of the
	// rewritten block.
	beginPattern = regexp.MustCompile(`(?m)^<!-- workbook:begin [^\n]*-->\r?$`)
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
	blocks := findBlocks(contents)
	if len(blocks) == 0 {
		return Outcome{State: StateAbsent, Contents: []byte(appendBlock(contents, rendered)), Changed: true}
	}

	first := blocks[0]
	// The document's own convention rather than the first block's, because the
	// file this has to repair is the one a pre-fix build left mixed: a CRLF
	// checkout carrying a copy of its block spelled with LF. Everything this
	// write puts back — the block itself and the text closing over each copy
	// removed — then reads the way the rest of the file reads.
	ending := lineEnding(contents)

	// Every duplicate a CRLF checkout collected is removed by the same write
	// that refreshes the one block that stays, and the one that stays is the
	// first: a document Workbook wrote has its block where Workbook put it. The
	// first block is replaced where it stands, which is what keeps the text
	// ahead of it byte for byte, and the copies are removed from the tail below
	// it — back to front so the earlier offsets stay valid, and separately from
	// the head so that closing those holes can never reach back into the block
	// that stays.
	tail := contents[first.end:]
	for index := len(blocks) - 1; index >= 1; index-- {
		later := blocks[index]
		later.start -= first.end
		later.end -= first.end
		tail = removeBlock(tail, later, ending)
	}
	updated := contents[:first.start] + respell(rendered, ending) + tail

	// The generator version is deliberately excluded here. A release that does
	// not change generated content must not mark every project stale.
	//
	// Both sides of this comparison are the body as it is written, never the
	// body as it was handed in; see managedBody.
	//
	// A document carrying more than one block is never current, however well the
	// first one matches: the duplicates are exactly what this write is for.
	if len(blocks) == 1 && first.body == body && first.recordedHash == hashBody(body) {
		return Outcome{State: StateCurrent, Contents: existing}
	}
	if stamped(blocks) {
		return Outcome{State: StateStale, Contents: []byte(updated), Changed: true}
	}
	return Outcome{State: StateModified, Contents: []byte(updated), Changed: true}
}

// Strip removes the managed block, preserving surrounding content. The
// returned state reports what was removed so callers can refuse to discard a
// modified block without an explicit override.
func Strip(existing []byte) (State, []byte) {
	contents := string(existing)
	blocks := findBlocks(contents)
	if len(blocks) == 0 {
		return StateAbsent, existing
	}

	state := StateModified
	if stamped(blocks) {
		state = StateCurrent
	}

	// Every block, not the first: a document that collected duplicates is
	// cleaned up by the command that was meant to remove the block, rather than
	// needing one run per copy. Back to front, so each removal leaves the
	// earlier blocks' offsets valid, and closed with the line ending the file
	// itself uses, which is not the block's own on a file a pre-fix build left
	// mixed.
	ending := lineEnding(contents)
	for index := len(blocks) - 1; index >= 0; index-- {
		contents = removeBlock(contents, blocks[index], ending)
	}
	if contents == "" {
		return state, nil
	}
	return state, []byte(contents)
}

// removeBlock cuts one block out and closes the hole it leaves: the newlines
// that belonged to the block go with it, and text on both sides is separated by
// a blank line, so what surrounded a block reads as it did before the block was
// written between them.
func removeBlock(contents string, b block, ending string) string {
	remainder := strings.TrimRight(contents[:b.start], "\r\n")
	trailing := strings.TrimLeft(contents[b.end:], "\r\n")
	switch {
	case remainder == "" && trailing == "":
		return ""
	case remainder == "":
		return trailing
	case trailing == "":
		return remainder + ending
	default:
		return remainder + ending + ending + trailing
	}
}

// stamped reports whether every block in a document is one Workbook wrote and
// nobody has edited since, which is what makes rewriting or removing all of
// them safe. One edited copy among duplicates is somebody's text, and reporting
// the document modified is what keeps a refresh from discarding it without an
// explicit override.
func stamped(blocks []block) bool {
	for _, b := range blocks {
		if b.recordedHash == "" || b.recordedHash != hashBody(b.body) {
			return false
		}
	}
	return true
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
	start, end int
	beginLine  string
	// body is the managed content with CRLF folded back to LF when the block is
	// written that way, because every hash Workbook recorded covers an LF body.
	// A block spelled with LF is read byte for byte, so nothing about an
	// existing project's stamp changes.
	body string
	// ending is the line ending this block is written with, so a refreshed or
	// stripped block is spelled the way the file that carries it is spelled.
	ending       string
	recordedHash string
}

// findBlocks reports every managed block in a document, in the order they
// appear. There should be one, and a Windows checkout could collect a second
// while the markers went unrecognized, so the reconciler reads all of them
// rather than assuming the format's own invariant held.
func findBlocks(contents string) []block {
	var blocks []block
	for offset := 0; offset < len(contents); {
		found, ok := findBlock(contents[offset:])
		if !ok {
			break
		}
		found.start += offset
		found.end += offset
		blocks = append(blocks, found)
		offset = found.end
	}
	return blocks
}

func findBlock(contents string) (block, bool) {
	begin := beginPattern.FindStringIndex(contents)
	if begin == nil {
		return block{}, false
	}
	beginLine := contents[begin[0]:begin[1]]
	ending := "\n"
	if strings.HasSuffix(beginLine, "\r") {
		ending = "\r\n"
	}
	bodyStart := begin[1] + breakLength(contents, begin[1])
	relative := strings.Index(contents[bodyStart:], endMarker)
	if relative < 0 {
		return block{}, false
	}
	bodyEnd := bodyStart + relative
	end := bodyEnd + len(endMarker)
	// The end marker's own line ending, which is CRLF in a Windows checkout and
	// belongs to the block rather than to what follows it.
	end += breakLength(contents, end)

	recorded := ""
	if match := hashPattern.FindStringSubmatch(beginLine); match != nil {
		recorded = match[1]
	}
	body := contents[bodyStart:bodyEnd]
	if ending == "\r\n" {
		body = strings.ReplaceAll(body, "\r\n", "\n")
	}
	return block{
		start:        begin[0],
		end:          end,
		beginLine:    beginLine,
		body:         body,
		ending:       ending,
		recordedHash: recorded,
	}, true
}

// breakLength reports the length of the line ending at index: two for CRLF, one
// for LF, and zero for a file that ends without one.
func breakLength(contents string, index int) int {
	switch {
	case strings.HasPrefix(contents[index:], "\r\n"):
		return 2
	case strings.HasPrefix(contents[index:], "\n"):
		return 1
	default:
		return 0
	}
}

// lineEnding reports the line ending a file is written with: CRLF when it
// carries one anywhere, which is what a Git for Windows checkout with the
// default core.autocrlf=true produces, and LF otherwise.
func lineEnding(contents string) string {
	if strings.Contains(contents, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// respell writes a rendered block with the line ending the file uses. A block
// is always rendered with LF, so writing one into a CRLF file unchanged would
// leave the file carrying two conventions — and would leave the very marker
// lines the next run has to find in whichever convention this run chose, rather
// than the one the checkout will keep converting them to.
func respell(rendered, ending string) string {
	if ending == "\n" {
		return rendered
	}
	return strings.ReplaceAll(strings.ReplaceAll(rendered, "\r\n", "\n"), "\n", ending)
}

func appendBlock(contents, rendered string) string {
	ending := lineEnding(contents)
	trimmed := strings.TrimRight(contents, "\r\n")
	if trimmed == "" {
		return respell(rendered, ending)
	}
	return trimmed + ending + ending + respell(rendered, ending)
}

func hashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
