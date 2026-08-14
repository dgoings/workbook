package core

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newerWriterPack rewrites a golden task pack so it claims the given writer
// format generation, and optionally renames its operation type.
//
// Editing the golden bytes is the point: these fixtures are what a future
// build's pack looks like from here — the same envelope, a marker this build
// does not meet, and possibly an operation type it has never heard of.
func newerWriterPack(t *testing.T, generation int, operationType string) string {
	t.Helper()
	document := goldenTaskRefs[1].operation
	marked := strings.Replace(document, `"version":1,`, `"version":1,"minReader":`+itoa(generation)+`,`, 1)
	if marked == document {
		t.Fatal("the fixture substitution matched nothing; the golden table changed shape")
	}
	if operationType == "" {
		return marked
	}
	replaced := strings.Replace(marked, `"type":"field.set","field":"status","value":"in-review"`,
		`"type":"`+operationType+`","body":"looks good"`, 1)
	if replaced == marked {
		t.Fatal("the operation substitution matched nothing; the golden table changed shape")
	}
	return replaced
}

func newerWriterState(t *testing.T, document string, generation int) string {
	t.Helper()
	marked := strings.Replace(document, `"version":1,`, `"version":1,"minReader":`+itoa(generation)+`,`, 1)
	if marked == document {
		t.Fatal("the fixture substitution matched nothing; the golden table changed shape")
	}
	return marked
}

func itoa(value int) string { return strconv.Itoa(value) }

// A marker at or below the supported generation folds normally.
//
// "Normally" means the operations take effect and nothing is refused. It does
// not mean the marker is discarded: a pack that declares generation N leaves N
// in the checkpoint's watermark, which is exactly what the watermark is for —
// once one pack in a task's history has needed a reader of generation N, every
// checkpoint after it does, and a later ordinary pack does not take that back.
//
// Generation zero is the one case where the claim is also a byte-level claim,
// and there it is false in an instructive direction. An explicit `"minReader":0`
// says what absence says, so it folds identically — but the only canonical
// encoding of generation zero is absence, so a stored document that spells it
// is refused by the canonicality rule that refuses every other byte difference.
// Storage is stricter than semantics here, exactly as it is for a stored task
// whose fields are in the wrong order.
func TestAMarkerAtOrBelowTheSupportedGenerationFoldsNormally(t *testing.T) {
	parent, err := DecodeStateDocument([]byte(goldenTaskRefs[1].parent))
	if err != nil {
		t.Fatalf("DecodeStateDocument(parent) error = %v", err)
	}
	for _, generation := range []int{0, SupportedFormatGeneration} {
		t.Run("generation "+itoa(generation), func(t *testing.T) {
			pack, err := DecodeOperationPack([]byte(newerWriterPack(t, generation, "")))
			if err != nil {
				t.Fatalf("DecodeOperationPack() error = %v", err)
			}
			if pack.RequiresNewerReader() {
				t.Fatalf("pack with minReader %d requires a newer reader", pack.MinReader)
			}
			state, err := Apply(&parent, pack, goldenProjectKey)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if state.Task.Status != "in-review" {
				t.Fatalf("folded status = %q, want in-review", state.Task.Status)
			}
			if state.MinReader != generation {
				t.Fatalf("folded checkpoint minReader = %d, want the declared %d", state.MinReader, generation)
			}
		})
	}

	// The byte-level half, which only generation zero has. Re-encoding drops
	// the explicit zero, so the document is not its own canonical form — which
	// is what the storage layer refuses, and is why the sentence above says
	// "the fold" rather than "nothing".
	packDocument := newerWriterPack(t, 0, "")
	pack, err := DecodeOperationPack([]byte(packDocument))
	if err != nil {
		t.Fatalf("DecodeOperationPack() error = %v", err)
	}
	encoded, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	if bytes.Equal(encoded, []byte(packDocument)) {
		t.Fatal("an explicit minReader:0 round-tripped; the canonical encoding of generation zero must be absence")
	}
	if bytes.Contains(encoded, []byte("minReader")) {
		t.Fatalf("EncodeDocument() emitted the marker for generation zero: %s", encoded)
	}
}

// An ordinary pack folded onto a checkpoint that already carries a watermark
// does not bury it.
//
// The test above proves a pack's own generation reaches the checkpoint it
// produces. This is the other half of the running maximum, and the half the
// whole design rests on: a newer build may write a generation-one pack and then
// perfectly ordinary generation-zero packs on top of it, and a reader looking
// only at the tip pack would see nothing and fold a history it does not
// understand. The watermark is a claim about the whole chain, so a later pack
// that needs nothing special does not take it back.
func TestAnOrdinaryPackDoesNotBuryAnEarlierWatermark(t *testing.T) {
	if SupportedFormatGeneration == 0 {
		t.Skip("this build folds only generation zero, so there is no watermark to bury")
	}
	parent, err := DecodeStateDocument([]byte(goldenTaskRefs[1].parent))
	if err != nil {
		t.Fatalf("DecodeStateDocument(parent) error = %v", err)
	}
	marked, err := DecodeOperationPack([]byte(newerWriterPack(t, SupportedFormatGeneration, "")))
	if err != nil {
		t.Fatalf("DecodeOperationPack(marked) error = %v", err)
	}
	state, err := Apply(&parent, marked, goldenProjectKey)
	if err != nil {
		t.Fatalf("Apply(marked) error = %v", err)
	}
	if state.MinReader != SupportedFormatGeneration {
		t.Fatalf("folded checkpoint minReader = %d, want %d", state.MinReader, SupportedFormatGeneration)
	}

	ordinary, err := DecodeOperationPack([]byte(newerWriterPack(t, 0, "")))
	if err != nil {
		t.Fatalf("DecodeOperationPack(ordinary) error = %v", err)
	}
	ordinary.LogicalClock = state.LogicalClock + 1
	ordinary.Operations[0].Value = "done"
	buried, err := Apply(&state, ordinary, goldenProjectKey)
	if err != nil {
		t.Fatalf("Apply(ordinary) error = %v", err)
	}
	if buried.MinReader != SupportedFormatGeneration {
		t.Fatalf("checkpoint after an ordinary pack has minReader = %d, want the watermark %d",
			buried.MinReader, SupportedFormatGeneration)
	}
}

// A pack above the supported generation is refused as newer-writer, whether or
// not this build recognizes the operations inside it.
func TestAPackAboveTheSupportedGenerationIsRefusedAsNewerWriter(t *testing.T) {
	parent, err := DecodeStateDocument([]byte(goldenTaskRefs[1].parent))
	if err != nil {
		t.Fatalf("DecodeStateDocument(parent) error = %v", err)
	}
	for _, testCase := range []struct {
		name          string
		operationType string
	}{
		{name: "known operation type", operationType: ""},
		{name: "operation type this build has never heard of", operationType: "comment.add"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := newerWriterPack(t, SupportedFormatGeneration+1, testCase.operationType)
			pack, err := DecodeOperationPack([]byte(document))
			if err != nil {
				t.Fatalf("DecodeOperationPack() error = %v", err)
			}
			if !pack.RequiresNewerReader() {
				t.Fatal("the decoded pack does not require a newer reader")
			}
			if pack.TaskID != "GD-01KZRTEV86XKX7R2PATXVQJQM8" {
				t.Fatalf("decoded task ID = %q, want the fixture's", pack.TaskID)
			}

			_, err = Apply(&parent, pack, goldenProjectKey)
			assertNewerWriterRefusal(t, err, pack.TaskID)

			if _, err := EncodeDocument(pack); CategoryOf(err) != CategoryNewerWriter {
				t.Fatalf("EncodeDocument() category = %q, want %q", CategoryOf(err), CategoryNewerWriter)
			}
		})
	}
}

// An unknown operation type with no marker stays corrupt data. That is the
// whole distinction the marker draws: without one, a type nobody recognizes is
// tampering or a bug, and calling it a version skew would tell somebody to
// upgrade their way out of a broken ref.
func TestAnUnknownOperationTypeWithoutAMarkerStaysCorruptData(t *testing.T) {
	document := strings.Replace(goldenTaskRefs[1].operation,
		`"type":"field.set","field":"status","value":"in-review"`,
		`"type":"comment.add","field":"body","value":"looks good"`, 1)
	if document == goldenTaskRefs[1].operation {
		t.Fatal("the operation substitution matched nothing; the golden table changed shape")
	}
	_, err := DecodeOperationPack([]byte(document))
	if CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("DecodeOperationPack() category = %q, want %q; error = %v", CategoryOf(err), CategoryCorruptData, err)
	}
}

// A checkpoint carrying the watermark refuses every later fold, which is what
// covers a mixed history: old packs, then a newer-generation pack, then
// whatever the newer build wrote on top of it.
func TestACheckpointWatermarkRefusesEveryLaterFold(t *testing.T) {
	document := newerWriterState(t, goldenTaskRefs[1].state, SupportedFormatGeneration+1)
	parent, err := DecodeStateDocument([]byte(document))
	if err != nil {
		t.Fatalf("DecodeStateDocument() error = %v", err)
	}
	if !parent.RequiresNewerReader() {
		t.Fatal("the decoded checkpoint does not require a newer reader")
	}
	if parent.Task.Title != "Task backlog" {
		t.Fatalf("decoded title = %q, want the checkpoint to still be readable", parent.Task.Title)
	}

	ordinary := NewOperationPack(
		parent.ProjectID, parent.TaskID, parent.History.Generation, "t@example.test",
		parent.LogicalClock+1, parent.Task.UpdatedAt,
		[]Operation{{ID: "01KZRTG06DCKC8XA90WJKDNVA9", Type: OperationFieldSet, Field: "title", Value: "Renamed"}},
	)
	if ordinary.MinReader != 0 {
		t.Fatalf("an ordinary pack carries minReader %d, want 0", ordinary.MinReader)
	}
	_, err = Apply(&parent, ordinary, goldenProjectKey)
	assertNewerWriterRefusal(t, err, parent.TaskID)
}

// A newer checkpoint still decodes into something every reader can show. This
// is what makes "reads serve from the checkpoint" true rather than aspirational:
// the document carries a member this build has never heard of, and the task
// still comes back whole.
func TestANewerCheckpointStillReadsAsATask(t *testing.T) {
	marked := newerWriterState(t, goldenTaskRefs[1].state, SupportedFormatGeneration+1)
	document := strings.Replace(marked, `"task":{`, `"comments":[{"body":"looks good"}],"task":{`, 1)
	if document == marked {
		t.Fatal("the fixture substitution matched nothing; the golden table changed shape")
	}
	state, err := DecodeStateDocument([]byte(document))
	if err != nil {
		t.Fatalf("DecodeStateDocument() error = %v", err)
	}
	if !state.RequiresNewerReader() {
		t.Fatal("the decoded checkpoint does not require a newer reader")
	}
	if state.Task.Status != "in-review" || state.Task.Title != "Task backlog" {
		t.Fatalf("decoded task = %#v, want the stored values", state.Task)
	}
	if state.Task.Labels == nil || state.Task.Dependencies == nil {
		t.Fatal("decoded task carries null collections; a reader needs total values")
	}

	// The same document without the marker is corrupt, because then the extra
	// member is not explained by anything.
	unmarked := strings.Replace(document, `"minReader":`+itoa(SupportedFormatGeneration+1)+`,`, "", 1)
	if _, err := DecodeStateDocument([]byte(unmarked)); CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("DecodeStateDocument(unmarked) category = %q, want %q", CategoryOf(err), CategoryCorruptData)
	}
}

// A document that is not JSON at all cannot claim a marker, so it stays corrupt.
func TestAMalformedDocumentCannotClaimAMarker(t *testing.T) {
	for _, document := range []string{"", "not json", `{"minReader":1`, `{"minReader":-1,"format":"workbook.operation-pack"}`} {
		if _, err := DecodeOperationPack([]byte(document)); CategoryOf(err) != CategoryCorruptData {
			t.Fatalf("DecodeOperationPack(%q) category = %q, want %q", document, CategoryOf(err), CategoryCorruptData)
		}
	}
}

// A negative generation is refused by the guard that exists for it, and this
// reaches that guard rather than stopping at some earlier rule.
//
// The distinction matters because it is easy to write a probe that never
// arrives: an otherwise-empty document with a negative marker fails on its
// format or its version long before the generation is looked at, so it proves
// the guard is unreachable rather than that it works. These fixtures are
// well-formed golden documents with only the marker changed, so the negative
// value is the single reason each one is refused — and the refusal is corrupt
// data rather than newer-writer, because a negative generation is not a claim
// about the future, it is a malformed number.
func TestANegativeMarkerIsRefusedByItsOwnGuard(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		decode  func([]byte) error
		message string
	}{
		{
			name:    "operation pack",
			decode:  func(data []byte) error { _, err := DecodeOperationPack(data); return err },
			message: "operation pack minimum reader generation -1 is invalid",
		},
		{
			name:    "task state",
			decode:  func(data []byte) error { _, err := DecodeStateDocument(data); return err },
			message: "task state minimum reader generation -1 is invalid",
		},
		{
			name:    "configuration operation pack",
			decode:  func(data []byte) error { _, err := DecodeConfigOperationPack(data); return err },
			message: "configuration operation pack minimum reader generation -1 is invalid",
		},
		{
			name:    "configuration state",
			decode:  func(data []byte) error { _, err := DecodeConfigStateDocument(data); return err },
			message: "configuration state minimum reader generation -1 is invalid",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := map[string]string{
				"operation pack":               newerWriterPack(t, -1, ""),
				"task state":                   newerWriterState(t, goldenTaskRefs[1].state, -1),
				"configuration operation pack": newerWriterState(t, goldenConfigLedger[1].operation, -1),
				"configuration state":          newerWriterState(t, goldenConfigLedger[1].state, -1),
			}[testCase.name]

			err := testCase.decode([]byte(document))
			if err == nil {
				t.Fatalf("a negative generation was accepted; want %q", testCase.message)
			}
			if CategoryOf(err) != CategoryCorruptData {
				t.Fatalf("category = %q, want %q; error = %v", CategoryOf(err), CategoryCorruptData, err)
			}
			if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("error = %q, want it to reach the guard that says %q", err, testCase.message)
			}
		})
	}
}

// The configuration ledger answers exactly the same way as a task history.
func TestTheConfigurationLedgerRefusesANewerGenerationTheSameWay(t *testing.T) {
	packDocument := newerWriterState(t, goldenConfigLedger[1].operation, SupportedFormatGeneration+1)
	pack, err := DecodeConfigOperationPack([]byte(packDocument))
	if err != nil {
		t.Fatalf("DecodeConfigOperationPack() error = %v", err)
	}
	if !pack.RequiresNewerReader() {
		t.Fatal("the decoded configuration pack does not require a newer reader")
	}
	parent, err := DecodeConfigStateDocument([]byte(goldenConfigLedger[1].parent))
	if err != nil {
		t.Fatalf("DecodeConfigStateDocument(parent) error = %v", err)
	}
	_, err = ApplyConfig(&parent, pack)
	assertNewerWriterConfigRefusal(t, err)

	// And the checkpoint watermark refuses an ordinary later pack, while the
	// vocabulary it carries still resolves.
	stateDocument := newerWriterState(t, goldenConfigLedger[1].state, SupportedFormatGeneration+1)
	newer, err := DecodeConfigStateDocument([]byte(stateDocument))
	if err != nil {
		t.Fatalf("DecodeConfigStateDocument() error = %v", err)
	}
	if got := newer.Vocabulary().Label("awaiting-review"); got != "Awaiting Review" {
		t.Fatalf("vocabulary label = %q, want the checkpoint's; resolution must survive", got)
	}
	ordinary, err := NewConfigOperationPack(newer.ProjectID, newer.History.Generation, "t@example.test",
		newer.LogicalClock+1, time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC),
		[]ConfigOperation{{
			ID:     "01KZYHVSVXRTT7T6JZNYWPZF7H",
			Type:   ConfigStatusRelabel,
			Status: "done",
			Label:  "Delivered",
		}})
	if err != nil {
		t.Fatalf("NewConfigOperationPack() error = %v", err)
	}
	if ordinary.MinReader != 0 {
		t.Fatalf("an ordinary configuration pack carries minReader %d, want 0", ordinary.MinReader)
	}
	_, err = ApplyConfig(&newer, ordinary)
	assertNewerWriterConfigRefusal(t, err)
}

func assertNewerWriterRefusal(t *testing.T, err error, taskID string) {
	t.Helper()
	if err == nil {
		t.Fatal("the fold succeeded; a newer-writer history must be refused")
	}
	if CategoryOf(err) != CategoryNewerWriter {
		t.Fatalf("category = %q, want %q; error = %v", CategoryOf(err), CategoryNewerWriter, err)
	}
	message := err.Error()
	if !strings.Contains(message, taskID) {
		t.Fatalf("message = %q, want it to name task %s", message, taskID)
	}
	if !strings.Contains(message, "newer workbook") || !strings.Contains(message, "upgrade workbook") {
		t.Fatalf("message = %q, want it to say a newer workbook wrote it and to upgrade", message)
	}
	for _, forbidden := range []string{"corrupt", "damaged", "invalid", "unreadable"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("message = %q, want it not to imply damage with %q", message, forbidden)
		}
	}
}

func assertNewerWriterConfigRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the configuration fold succeeded; a newer-writer ledger must be refused")
	}
	if CategoryOf(err) != CategoryNewerWriter {
		t.Fatalf("category = %q, want %q; error = %v", CategoryOf(err), CategoryNewerWriter, err)
	}
	message := err.Error()
	if !strings.Contains(message, "newer workbook") || !strings.Contains(message, "upgrade workbook") {
		t.Fatalf("message = %q, want it to say a newer workbook wrote it and to upgrade", message)
	}
}

// The newer-writer category has its own exit code, distinct from corrupt data.
func TestNewerWriterHasItsOwnExitCode(t *testing.T) {
	if got := ExitCode(newerWriterTask("WB-01K0M6B8A4FTT8C39MXXYTW7C2")); got != 9 {
		t.Fatalf("ExitCode(newer-writer) = %d, want 9", got)
	}
	if ExitCode(corrupt("broken")) == 9 {
		t.Fatal("corrupt data shares the newer-writer exit code")
	}
}
