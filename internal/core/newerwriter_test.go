package core

import (
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

// A marker at or below the supported generation changes nothing at all.
//
// This is the half of the contract that is easy to get wrong in the permissive
// direction: an explicit `"minReader":0` is a document that says "any reader",
// which is the same claim absence makes, so it must fold exactly as it would
// without the member. Its bytes differ from the golden's, which is why the
// canonicality check belongs to the storage layer and not to the fold.
func TestAMarkerAtOrBelowTheSupportedGenerationFoldsNormally(t *testing.T) {
	packDocument := newerWriterPack(t, SupportedFormatGeneration, "")
	pack, err := DecodeOperationPack([]byte(packDocument))
	if err != nil {
		t.Fatalf("DecodeOperationPack() error = %v", err)
	}
	if pack.RequiresNewerReader() {
		t.Fatalf("pack with minReader %d requires a newer reader", pack.MinReader)
	}
	parent, err := DecodeStateDocument([]byte(goldenTaskRefs[1].parent))
	if err != nil {
		t.Fatalf("DecodeStateDocument(parent) error = %v", err)
	}
	state, err := Apply(&parent, pack, goldenProjectKey)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if state.Task.Status != "in-review" {
		t.Fatalf("folded status = %q, want in-review", state.Task.Status)
	}
	if state.MinReader != 0 {
		t.Fatalf("folded checkpoint minReader = %d, want 0", state.MinReader)
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
