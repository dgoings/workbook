package agentdocs

import (
	"strings"
	"testing"
)

func testDocument(body string) Document {
	return Document{Generator: "0.2.0", Body: body}
}

func TestReconcileReportsAbsentWhenTheFileDoesNotExist(t *testing.T) {
	document := Document{Generator: "0.2.0", Preamble: "---\nname: workbook\n---\n", Body: "Guidance.\n"}

	outcome := document.Reconcile(nil)

	if outcome.State != StateAbsent {
		t.Fatalf("Reconcile(nil) state = %q, want %q", outcome.State, StateAbsent)
	}
	if !strings.HasPrefix(string(outcome.Contents), "---\nname: workbook\n---\n") {
		t.Fatalf("Reconcile(nil) contents missing preamble:\n%s", outcome.Contents)
	}
	if !strings.Contains(string(outcome.Contents), "Guidance.\n") {
		t.Fatalf("Reconcile(nil) contents missing body:\n%s", outcome.Contents)
	}
}

func TestReconcileReportsCurrentForItsOwnOutput(t *testing.T) {
	document := testDocument("Guidance.\n")
	first := document.Reconcile(nil)

	outcome := document.Reconcile(first.Contents)

	if outcome.State != StateCurrent {
		t.Fatalf("Reconcile(own output) state = %q, want %q", outcome.State, StateCurrent)
	}
	if outcome.Changed {
		t.Fatal("Reconcile(own output) changed = true, want false")
	}
	if got, want := string(outcome.Contents), string(first.Contents); got != want {
		t.Fatalf("Reconcile(own output) contents = %q, want %q", got, want)
	}
}

// A body carrying this block's own end marker cannot round-trip: findBlock
// stops at the first end marker, so the block read back is a truncated one
// whose hash can never match the recorded one. The file would then be
// StateModified forever — reported as somebody's local edit in every clone,
// refused by every refresh, and grown by every --force, which re-inserts a
// fresh block ahead of the orphaned tail. This is the layer that makes that
// impossible for any body, whatever produced it.
//
// Removing the neutralization in managedBody fails this test on its own; the
// rendering layer that keeps authored labels away from here is tested
// separately, in render_test.go.
func TestReconcileRoundTripsABodyCarryingTheEndMarker(t *testing.T) {
	document := testDocument("Statuses: Next " + endMarker + " Up, and " + beginPrefix + "x -->\nmore.\n")

	first := document.Reconcile(nil)
	written := string(first.Contents)

	if strings.Count(written, endMarker) != 1 {
		t.Fatalf("the written file carries %d end markers, want the block's own:\n%s",
			strings.Count(written, endMarker), written)
	}
	if !strings.Contains(written, "Next &lt;!-- workbook:end --> Up") {
		t.Fatalf("the neutralized marker does not still read as what was written:\n%s", written)
	}
	if outcome := document.Reconcile(first.Contents); outcome.State != StateCurrent {
		t.Fatalf("Reconcile(own output) state = %q, want %q:\n%s", outcome.State, StateCurrent, written)
	}

	// And the next release of the same document refreshes it rather than
	// refusing, which is the state the truncation used to make unreachable.
	next := testDocument("Statuses: Next " + endMarker + " Up, and " + beginPrefix + "x -->\nmore, revised.\n")
	outcome := next.Reconcile(first.Contents)
	if outcome.State != StateStale || !outcome.Changed {
		t.Fatalf("Reconcile(changed body) = %q changed %t, want a stale rewrite", outcome.State, outcome.Changed)
	}
	if second := next.Reconcile(outcome.Contents); second.State != StateCurrent {
		t.Fatalf("the rewrite does not settle: state = %q\n%s", second.State, outcome.Contents)
	}
}

// A comment opener that completes no marker still costs a reader the rest of
// the file. `<!-- note` terminates no block and the drift patterns are
// line-anchored, so the machinery is untouched — but every Markdown renderer
// the committed file is read through opens an HTML comment there and swallows
// everything down to the next `-->`, which in a generated guidelines file is
// usually nothing at all.
//
// So the neutralization is of the opener rather than of the two complete
// markers, and this pins the round-trip for a body that carries only a partial
// one.
func TestReconcileNeutralizesAnOpenerThatCompletesNoMarker(t *testing.T) {
	document := testDocument("Statuses: Next " + markerOpener + " note\nmore.\n")

	first := document.Reconcile(nil)
	written := string(first.Contents)

	// The block's own begin marker is the one comment opener the file may carry.
	if strings.Count(written, markerOpener) != 2 {
		t.Fatalf("the written file carries %d comment openers, want the block's own two:\n%s",
			strings.Count(written, markerOpener), written)
	}
	if !strings.Contains(written, "Next &lt;!-- note") {
		t.Fatalf("the neutralized opener does not still read as what was written:\n%s", written)
	}
	if outcome := document.Reconcile(first.Contents); outcome.State != StateCurrent {
		t.Fatalf("Reconcile(own output) state = %q, want %q:\n%s", outcome.State, StateCurrent, written)
	}

	next := testDocument("Statuses: Next " + markerOpener + " note\nmore, revised.\n")
	outcome := next.Reconcile(first.Contents)
	if outcome.State != StateStale || !outcome.Changed {
		t.Fatalf("Reconcile(changed body) = %q changed %t, want a stale rewrite", outcome.State, outcome.Changed)
	}
	if second := next.Reconcile(outcome.Contents); second.State != StateCurrent {
		t.Fatalf("the rewrite does not settle: state = %q\n%s", second.State, outcome.Contents)
	}
}

// The neutral form does not round-trip through a second pass, which is what
// lets the rendering layer neutralize an authored label before the block format
// neutralizes the whole body without the label growing an entity per layer.
// `&lt;!--` carries no `<`, so it never matches the opener again.
func TestNeutralizeMarkersDoesNotEscapeWhatItAlreadyEscaped(t *testing.T) {
	for _, body := range []string{
		"nothing to do here\n",
		"a label somebody wrote as " + neutralOpener + " already\n",
		neutralizeMarkers("Next " + endMarker + " Up\n"),
		neutralizeMarkers("Next " + markerOpener + " note\n"),
	} {
		if got := neutralizeMarkers(body); got != body {
			t.Errorf("neutralizeMarkers(%q) = %q, want it unchanged", body, got)
		}
	}
}

func TestReconcileReportsStaleWhenTheRenderedBodyChanges(t *testing.T) {
	// Production mutation: comparing only the recorded generator version would
	// miss a project whose configuration changed under a constant binary.
	existing := testDocument("Old guidance.\n").Reconcile(nil).Contents

	outcome := testDocument("New guidance.\n").Reconcile(existing)

	if outcome.State != StateStale {
		t.Fatalf("Reconcile(outdated) state = %q, want %q", outcome.State, StateStale)
	}
	if !outcome.Changed {
		t.Fatal("Reconcile(outdated) changed = false, want true")
	}
	if !strings.Contains(string(outcome.Contents), "New guidance.\n") {
		t.Fatalf("Reconcile(outdated) did not refresh the body:\n%s", outcome.Contents)
	}
}

func TestReconcileIgnoresTheGeneratorWhenTheBodyIsUnchanged(t *testing.T) {
	// Production mutation: treating the recorded generator as a decision input
	// would mark every project stale after a release that changed no generated
	// content, which is the notification noise this design exists to avoid.
	existing := Document{Generator: "0.1.0", Body: "Guidance.\n"}.Reconcile(nil).Contents

	outcome := testDocument("Guidance.\n").Reconcile(existing)

	if outcome.State != StateCurrent {
		t.Fatalf("Reconcile(older generator, same body) state = %q, want %q", outcome.State, StateCurrent)
	}
	if outcome.Changed {
		t.Fatal("Reconcile(older generator, same body) changed = true, want false")
	}
}

func TestReconcileRestampsTheGeneratorWhenTheBodyChanges(t *testing.T) {
	existing := Document{Generator: "0.1.0", Body: "Old guidance.\n"}.Reconcile(nil).Contents

	outcome := testDocument("New guidance.\n").Reconcile(existing)

	if !strings.Contains(string(outcome.Contents), "generator=0.2.0") {
		t.Fatalf("Reconcile(refreshed) did not restamp the generator:\n%s", outcome.Contents)
	}
}

func TestReconcileReportsModifiedWhenTheBodyWasEdited(t *testing.T) {
	// Production mutation: treating an edited body as stale would silently
	// discard the user's work on the next update.
	existing := testDocument("Guidance.\n").Reconcile(nil).Contents
	edited := strings.Replace(string(existing), "Guidance.\n", "Guidance, edited by hand.\n", 1)

	outcome := testDocument("Guidance.\n").Reconcile([]byte(edited))

	if outcome.State != StateModified {
		t.Fatalf("Reconcile(edited) state = %q, want %q", outcome.State, StateModified)
	}
}

func TestReconcileTreatsAnUnusableStampAsModified(t *testing.T) {
	// Production mutation: defaulting a damaged stamp to stale would let the
	// tool clobber a file it can no longer prove it wrote.
	for name, stamp := range map[string]string{
		"missing hash": "<!-- workbook:begin generator=0.2.0 -->",
		"empty hash":   "<!-- workbook:begin generator=0.2.0 sha256= -->",
		"garbage hash": "<!-- workbook:begin generator=0.2.0 sha256=not-a-hash -->",
	} {
		t.Run(name, func(t *testing.T) {
			contents := stamp + "\nGuidance.\n" + endMarker + "\n"

			outcome := testDocument("Guidance.\n").Reconcile([]byte(contents))

			if outcome.State != StateModified {
				t.Fatalf("Reconcile(%s) state = %q, want %q", name, outcome.State, StateModified)
			}
		})
	}
}

func TestReconcilePreservesUserContentAroundTheBlock(t *testing.T) {
	// Production mutation: rewriting the whole file would destroy every
	// user-authored instruction in AGENTS.md.
	user := "# AGENTS.md\n\nMy own rules.\n"
	appended := testDocument("Workbook guidance.\n").Reconcile([]byte(user))
	if appended.State != StateAbsent {
		t.Fatalf("Reconcile(user file) state = %q, want %q", appended.State, StateAbsent)
	}
	if !strings.HasPrefix(string(appended.Contents), user) {
		t.Fatalf("Reconcile(user file) did not preserve user content:\n%s", appended.Contents)
	}

	refreshed := testDocument("Updated guidance.\n").Reconcile(appended.Contents)
	if refreshed.State != StateStale {
		t.Fatalf("Reconcile(refreshed) state = %q, want %q", refreshed.State, StateStale)
	}
	if !strings.HasPrefix(string(refreshed.Contents), user) {
		t.Fatalf("Reconcile(refreshed) did not preserve user content:\n%s", refreshed.Contents)
	}
	if strings.Contains(string(refreshed.Contents), "Workbook guidance.") {
		t.Fatalf("Reconcile(refreshed) kept the outdated body:\n%s", refreshed.Contents)
	}
}

func TestReconcileIsIdempotentAcrossRepeatedUpdates(t *testing.T) {
	document := testDocument("Guidance.\n")
	contents := document.Reconcile([]byte("# AGENTS.md\n\nMy own rules.\n")).Contents

	for range 3 {
		outcome := document.Reconcile(contents)
		if outcome.State != StateCurrent {
			t.Fatalf("repeated Reconcile state = %q, want %q", outcome.State, StateCurrent)
		}
		if got, want := string(outcome.Contents), string(contents); got != want {
			t.Fatalf("repeated Reconcile contents = %q, want %q", got, want)
		}
		contents = outcome.Contents
	}
}

// crlf spells a document the way a Git for Windows checkout with the default
// core.autocrlf=true spells the file Workbook wrote with LF.
func crlf(contents string) string {
	return strings.ReplaceAll(contents, "\n", "\r\n")
}

// carriesLoneLF reports whether a document mixes conventions, which is what
// writing an LF block into a CRLF file would leave behind.
func carriesLoneLF(contents string) bool {
	return strings.Contains(strings.ReplaceAll(contents, "\r\n", ""), "\n")
}

// Production mutation: without the optional carriage return in beginPattern the
// managed block is not found in a CRLF checkout at all, so `workbook docs
// update` reports StateAbsent and leaves a duplicate block behind in the user's
// AGENTS.md — one per fresh checkout, since the copy it appends is spelled with
// LF and the run after that finds that one instead of the block the file
// started with.
func TestReconcileFindsAManagedBlockInACRLFCheckout(t *testing.T) {
	document := testDocument("Guidance.\n")
	written := document.Reconcile([]byte("# AGENTS.md\n\nMy own rules.\n")).Contents
	checkout := []byte(crlf(string(written)))

	first := document.Reconcile(checkout)
	if first.State != StateCurrent {
		t.Fatalf("Reconcile(CRLF checkout) state = %q, want %q", first.State, StateCurrent)
	}
	if first.Changed {
		t.Fatalf("Reconcile(CRLF checkout) changed = true, want false:\n%q", first.Contents)
	}

	second := document.Reconcile(first.Contents)
	if second.State != StateCurrent {
		t.Fatalf("second Reconcile(CRLF checkout) state = %q, want %q", second.State, StateCurrent)
	}
	if got := strings.Count(string(second.Contents), beginPrefix); got != 1 {
		t.Fatalf("after two updates the CRLF file carries %d begin markers, want 1:\n%s",
			got, second.Contents)
	}
	if got, want := string(second.Contents), string(checkout); got != want {
		t.Fatalf("Reconcile(CRLF checkout) contents = %q, want %q", got, want)
	}
}

// A refreshed block is written in the convention the file already uses, so the
// file does not come out half CRLF and half LF — and so the marker lines the
// next run has to find are spelled the way the checkout keeps them.
func TestReconcileRefreshesACRLFBlockWithCRLF(t *testing.T) {
	user := "# AGENTS.md\n\nMy own rules.\n"
	written := testDocument("Guidance.\n").Reconcile([]byte(user)).Contents
	checkout := crlf(string(written))

	outcome := testDocument("Updated guidance.\n").Reconcile([]byte(checkout))

	if outcome.State != StateStale || !outcome.Changed {
		t.Fatalf("Reconcile(CRLF, changed body) = %q changed %t, want a stale rewrite",
			outcome.State, outcome.Changed)
	}
	refreshed := string(outcome.Contents)
	if got := strings.Count(refreshed, beginPrefix); got != 1 {
		t.Fatalf("the refreshed CRLF file carries %d begin markers, want 1:\n%s", got, refreshed)
	}
	if !strings.Contains(refreshed, "Updated guidance.\r\n") {
		t.Fatalf("the refreshed block is not written with CRLF:\n%q", refreshed)
	}
	if carriesLoneLF(refreshed) {
		t.Fatalf("the refreshed CRLF file mixes line endings:\n%q", refreshed)
	}
	if !strings.HasPrefix(refreshed, crlf(user)) {
		t.Fatalf("Reconcile(CRLF) did not preserve user content:\n%q", refreshed)
	}
	if second := testDocument("Updated guidance.\n").Reconcile(outcome.Contents); second.State != StateCurrent {
		t.Fatalf("Reconcile(refreshed CRLF) state = %q, want %q", second.State, StateCurrent)
	}
}

// The document a CRLF checkout used to grow: the block Workbook wrote, a copy
// appended by a `workbook docs update` that could not find it, once per fresh
// checkout, and what somebody wrote in between.
type duplicatedDocument struct {
	contents   string
	user       string
	firstNote  string
	secondNote string
}

func withThreeBlocks(document Document, ending string) duplicatedDocument {
	block := string(document.Reconcile(nil).Contents)
	parts := duplicatedDocument{
		user:       "# AGENTS.md\n\nMy own rules.\n",
		firstNote:  "A note written between the first two blocks.\n",
		secondNote: "And another, below the second.\n",
	}
	parts.contents = parts.user + "\n" + block + "\n" + parts.firstNote +
		"\n" + block + "\n" + parts.secondNote + "\n" + block
	if ending == "\r\n" {
		parts.contents = crlf(parts.contents)
		parts.user = crlf(parts.user)
		parts.firstNote = crlf(parts.firstNote)
		parts.secondNote = crlf(parts.secondNote)
	}
	return parts
}

var lineEndings = []struct{ name, value string }{
	{"CRLF", "\r\n"},
	{"LF", "\n"},
}

// Production mutation: reconciling only the first block would fix the CRLF
// checkout for the future and leave the duplicates it already collected in the
// file forever, as text no `workbook docs` command would ever touch again.
func TestReconcileCollapsesDuplicateBlocksIntoOne(t *testing.T) {
	for _, ending := range lineEndings {
		t.Run(ending.name, func(t *testing.T) {
			document := testDocument("Guidance.\n")
			parts := withThreeBlocks(document, ending.value)
			if got := strings.Count(parts.contents, beginPrefix); got != 3 {
				t.Fatalf("the document under test carries %d begin markers, want 3", got)
			}

			outcome := document.Reconcile([]byte(parts.contents))

			if outcome.State != StateStale || !outcome.Changed {
				t.Fatalf("Reconcile(three blocks) = %q changed %t, want a stale rewrite",
					outcome.State, outcome.Changed)
			}
			result := string(outcome.Contents)
			if got := strings.Count(result, beginPrefix); got != 1 {
				t.Fatalf("Reconcile(three blocks) left %d begin markers, want 1:\n%s", got, result)
			}
			if got := strings.Count(result, endMarker); got != 1 {
				t.Fatalf("Reconcile(three blocks) left %d end markers, want 1:\n%s", got, result)
			}
			if got, want := strings.Index(result, beginPrefix), strings.Index(parts.contents, beginPrefix); got != want {
				t.Fatalf("the block that stayed begins at %d, want the first block's %d:\n%s",
					got, want, result)
			}
			if !strings.HasPrefix(result, parts.user) {
				t.Fatalf("Reconcile(three blocks) did not preserve the user's content:\n%q", result)
			}
			// The text that sat between the blocks is kept, below the one block
			// that stayed and in the order it was written.
			block := strings.Index(result, endMarker)
			first := strings.Index(result, parts.firstNote)
			second := strings.Index(result, parts.secondNote)
			if first < 0 || second < 0 {
				t.Fatalf("Reconcile(three blocks) dropped what was between them:\n%q", result)
			}
			if !(block < first && first < second) {
				t.Fatalf("Reconcile(three blocks) reordered the document:\n%q", result)
			}
			if ending.value == "\r\n" && carriesLoneLF(result) {
				t.Fatalf("Reconcile(three CRLF blocks) mixed line endings:\n%q", result)
			}
			if again := document.Reconcile(outcome.Contents); again.State != StateCurrent {
				t.Fatalf("Reconcile(collapsed) state = %q, want %q", again.State, StateCurrent)
			}
		})
	}
}

// The shape a pre-fix build really left behind: a CRLF checkout whose duplicate
// was appended with LF, because the block was rendered with LF and nothing
// respelled it. Collapsing that has to leave the file in one convention — this
// is the input respell exists for, and the one where taking the surviving
// block's own ending is not the same as taking the file's.
func TestReconcileCollapsesAnLFDuplicateInACRLFFile(t *testing.T) {
	document := testDocument("Guidance.\n")
	block := string(document.Reconcile(nil).Contents)
	user := "# CLAUDE.md\n\nMy own rules.\n"
	checkout := crlf(user + "\n" + block)
	// Exactly what the pre-fix appendBlock did: trim the trailing newlines it
	// knew about, which on a CRLF file leaves a carriage return, and append an
	// LF block below a blank line.
	damaged := strings.TrimRight(checkout, "\n") + "\n\n" + block
	if got := strings.Count(damaged, beginPrefix); got != 2 {
		t.Fatalf("the document under test carries %d begin markers, want 2", got)
	}
	if !carriesLoneLF(damaged) {
		t.Fatalf("the document under test is not the mixed file this is about:\n%q", damaged)
	}

	outcome := document.Reconcile([]byte(damaged))

	if outcome.State != StateStale || !outcome.Changed {
		t.Fatalf("Reconcile(mixed duplicate) = %q changed %t, want a stale rewrite",
			outcome.State, outcome.Changed)
	}
	result := string(outcome.Contents)
	if got := strings.Count(result, beginPrefix); got != 1 {
		t.Fatalf("Reconcile(mixed duplicate) left %d begin markers, want 1:\n%q", got, result)
	}
	if carriesLoneLF(result) {
		t.Fatalf("Reconcile(mixed duplicate) left a lone LF in a CRLF file:\n%q", result)
	}
	if !strings.HasPrefix(result, crlf(user)) {
		t.Fatalf("Reconcile(mixed duplicate) did not preserve the user's content:\n%q", result)
	}
	if again := document.Reconcile(outcome.Contents); again.State != StateCurrent {
		t.Fatalf("Reconcile(collapsed) state = %q, want %q", again.State, StateCurrent)
	}

	// And taking both out closes the file in its own convention as well.
	state, stripped := Strip([]byte(damaged))
	if state != StateCurrent {
		t.Fatalf("Strip(mixed duplicate) state = %q, want %q", state, StateCurrent)
	}
	if got, want := string(stripped), crlf(user); got != want {
		t.Fatalf("Strip(mixed duplicate) = %q, want %q", got, want)
	}
}

// The other half of the mixed file a pre-fix build left behind, and the half
// where the file's convention and the block's own disagree: a CRLF AGENTS.md
// whose block was appended with LF, because nothing respelled it on the way in.
// Refreshing that block writes it the way the file around it is written, and
// removing it closes the file the same way, rather than leaving the LF the block
// happened to carry.
func TestReconcileRewritesAnLFBlockWithTheCRLFFileAroundIt(t *testing.T) {
	block := string(testDocument("Guidance.\n").Reconcile(nil).Contents)
	user := crlf("# CLAUDE.md\n\nMy own rules.\n")
	mixed := user + "\r\n" + block
	if !carriesLoneLF(mixed) {
		t.Fatalf("the document under test is not the mixed file this is about:\n%q", mixed)
	}

	outcome := testDocument("Updated guidance.\n").Reconcile([]byte(mixed))

	if outcome.State != StateStale || !outcome.Changed {
		t.Fatalf("Reconcile(LF block, CRLF file) = %q changed %t, want a stale rewrite",
			outcome.State, outcome.Changed)
	}
	result := string(outcome.Contents)
	if carriesLoneLF(result) {
		t.Fatalf("Reconcile(LF block, CRLF file) left the file mixed:\n%q", result)
	}
	if !strings.Contains(result, "Updated guidance.\r\n") {
		t.Fatalf("the refreshed block is not written with the file's CRLF:\n%q", result)
	}
	if !strings.HasPrefix(result, user) {
		t.Fatalf("Reconcile(LF block, CRLF file) did not preserve the user's content:\n%q", result)
	}

	// And removing it closes the file with CRLF rather than the block's LF.
	state, stripped := Strip([]byte(mixed))
	if state != StateCurrent {
		t.Fatalf("Strip(LF block, CRLF file) state = %q, want %q", state, StateCurrent)
	}
	if got, want := string(stripped), user; got != want {
		t.Fatalf("Strip(LF block, CRLF file) = %q, want %q", got, want)
	}
}

func TestStripRemovesEveryDuplicateBlock(t *testing.T) {
	for _, ending := range lineEndings {
		t.Run(ending.name, func(t *testing.T) {
			parts := withThreeBlocks(testDocument("Guidance.\n"), ending.value)

			state, stripped := Strip([]byte(parts.contents))

			if state != StateCurrent {
				t.Fatalf("Strip(three blocks) state = %q, want %q", state, StateCurrent)
			}
			result := string(stripped)
			if strings.Contains(result, beginPrefix) || strings.Contains(result, endMarker) {
				t.Fatalf("Strip(three blocks) left a marker behind:\n%q", result)
			}
			want := parts.user + ending.value + parts.firstNote + ending.value + parts.secondNote
			if result != want {
				t.Fatalf("Strip(three blocks) = %q, want %q", result, want)
			}
		})
	}
}

func TestStripRemovesACRLFBlock(t *testing.T) {
	user := "# AGENTS.md\n\nMy own rules.\n"
	written := testDocument("Guidance.\n").Reconcile([]byte(user)).Contents

	state, stripped := Strip([]byte(crlf(string(written))))

	if state != StateCurrent {
		t.Fatalf("Strip(CRLF) state = %q, want %q", state, StateCurrent)
	}
	if got, want := string(stripped), crlf(user); got != want {
		t.Fatalf("Strip(CRLF) = %q, want %q", got, want)
	}
}

func TestStripRemovesTheBlockAndPreservesUserContent(t *testing.T) {
	user := "# AGENTS.md\n\nMy own rules.\n"
	contents := testDocument("Guidance.\n").Reconcile([]byte(user)).Contents

	state, stripped := Strip(contents)

	if state != StateCurrent {
		t.Fatalf("Strip() state = %q, want %q", state, StateCurrent)
	}
	if got := string(stripped); got != user {
		t.Fatalf("Strip() = %q, want %q", got, user)
	}
}

func TestStripReportsModifiedForAnEditedBlock(t *testing.T) {
	contents := testDocument("Guidance.\n").Reconcile([]byte("# AGENTS.md\n")).Contents
	edited := strings.Replace(string(contents), "Guidance.\n", "Edited.\n", 1)

	state, _ := Strip([]byte(edited))

	if state != StateModified {
		t.Fatalf("Strip(edited) state = %q, want %q", state, StateModified)
	}
}

func TestStripReportsAbsentWhenThereIsNoBlock(t *testing.T) {
	state, stripped := Strip([]byte("# AGENTS.md\n"))

	if state != StateAbsent {
		t.Fatalf("Strip(no block) state = %q, want %q", state, StateAbsent)
	}
	if got, want := string(stripped), "# AGENTS.md\n"; got != want {
		t.Fatalf("Strip(no block) = %q, want %q", got, want)
	}
}

func TestBodyHashCoversTheBodyWithoutTheMarkerLines(t *testing.T) {
	// Production mutation: hashing the marker lines would fold the generator
	// version into the hash, so writing a stamp would invalidate that same
	// stamp and the document would never converge.
	first := Document{Generator: "0.1.0", Body: "Guidance.\n"}.Reconcile(nil).Contents
	second := Document{Generator: "9.9.9", Body: "Guidance.\n"}.Reconcile(nil).Contents

	if got, want := recordedHashOf(t, second), recordedHashOf(t, first); got != want {
		t.Fatalf("hash with a different generator = %q, want %q", got, want)
	}
}

func recordedHashOf(t *testing.T, contents []byte) string {
	t.Helper()
	block, ok := findBlock(string(contents))
	if !ok {
		t.Fatalf("findBlock() found no block in:\n%s", contents)
	}
	return block.recordedHash
}
