package core

import (
	"bytes"
	"strings"
	"testing"
)

// Golden task-ref fixtures.
//
// Every byte below was read out of a real repository with `git show
// <commit>:operation.json` and `git show <commit>:state.json`, not composed by
// hand. That matters: these are the durable interface. A task ref is
// append-only shared history, so a change that makes an already-stored document
// decode differently, encode to different bytes, or stop validating is not a
// refactor — it is a corrupted repository on every clone that already fetched
// it, and no later release can repair it.
//
// The set covers all six built-in statuses at creation plus a field.set status
// replay, because the status field is where the vocabulary work changes the
// rules and the rest of the document is along for the ride.
//
// Three properties are asserted per fixture, and each one fails for a different
// kind of regression:
//
//   - the stored bytes decode, which fails when validation tightens;
//   - the decoded value re-encodes to the same bytes, which fails when a field
//     is added, renamed, reordered, or gains an omitempty it should not have;
//   - the checkpoint validates against its parent, which fails when the fold
//     itself changes.
//
// Do not regenerate this table to make a failing test pass. A failure here is
// the finding.
type goldenTaskRef struct {
	name string
	// parent is the state.json of the commit this pack was applied to, empty
	// for a root commit.
	parent    string
	operation string
	state     string
}

// goldenProjectKey is the key of the repository the fixtures were captured
// from.
const goldenProjectKey = "GD"

var goldenTaskRefs = []goldenTaskRef{
	{
		name: "create backlog",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEV86XKX7R2PATXVQJQM8","historyGeneration":"01KZRTEV86188DB2S77T9Z3W68","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-11T12:28:29.318531-04:00","operations":[{"id":"01KZRTEV86DKE4M9QKZQEXGKKT","type":"task.create","task":{"title":"Task backlog","description":"","status":"backlog","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.318531-04:00","updatedAt":"2026-08-11T12:28:29.318531-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEV86XKX7R2PATXVQJQM8","history":{"generation":"01KZRTEV86188DB2S77T9Z3W68","compactedFrom":null},"logicalClock":1,"task":{"title":"Task backlog","description":"","status":"backlog","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.318531-04:00","updatedAt":"2026-08-11T12:28:29.318531-04:00","deleted":false}}
`,
	},
	{
		name: "field.set status in-review",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEV86XKX7R2PATXVQJQM8","history":{"generation":"01KZRTEV86188DB2S77T9Z3W68","compactedFrom":null},"logicalClock":1,"task":{"title":"Task backlog","description":"","status":"backlog","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.318531-04:00","updatedAt":"2026-08-11T12:28:29.318531-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEV86XKX7R2PATXVQJQM8","historyGeneration":"01KZRTEV86188DB2S77T9Z3W68","actor":{"id":"t@example.test"},"logicalClock":2,"wallTime":"2026-08-11T12:29:07.149215-04:00","operations":[{"id":"01KZRTG06DCKC8XA90WJKDNVA8","type":"field.set","field":"status","value":"in-review"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEV86XKX7R2PATXVQJQM8","history":{"generation":"01KZRTEV86188DB2S77T9Z3W68","compactedFrom":null},"logicalClock":2,"task":{"title":"Task backlog","description":"","status":"in-review","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.318531-04:00","updatedAt":"2026-08-11T12:29:07.149215-04:00","deleted":false}}
`,
	},
	{
		name: "create ready",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEVE3SPTWS494NJT9QZZG","historyGeneration":"01KZRTEVE3EYWGJVX78Q0A9QW8","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-11T12:28:29.507506-04:00","operations":[{"id":"01KZRTEVE3ZXEXSFCTEW14GJSQ","type":"task.create","task":{"title":"Task ready","description":"","status":"ready","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.507506-04:00","updatedAt":"2026-08-11T12:28:29.507506-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEVE3SPTWS494NJT9QZZG","history":{"generation":"01KZRTEVE3EYWGJVX78Q0A9QW8","compactedFrom":null},"logicalClock":1,"task":{"title":"Task ready","description":"","status":"ready","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.507506-04:00","updatedAt":"2026-08-11T12:28:29.507506-04:00","deleted":false}}
`,
	},
	{
		name: "create blocked",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEVKN57XS5VJQGDGQ4PYM","historyGeneration":"01KZRTEVKNMKFW9Q1P6BQFC8Z4","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-11T12:28:29.685515-04:00","operations":[{"id":"01KZRTEVKN2KE3VCFNJYBA3FPK","type":"task.create","task":{"title":"Task blocked","description":"","status":"blocked","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.685515-04:00","updatedAt":"2026-08-11T12:28:29.685515-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEVKN57XS5VJQGDGQ4PYM","history":{"generation":"01KZRTEVKNMKFW9Q1P6BQFC8Z4","compactedFrom":null},"logicalClock":1,"task":{"title":"Task blocked","description":"","status":"blocked","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.685515-04:00","updatedAt":"2026-08-11T12:28:29.685515-04:00","deleted":false}}
`,
	},
	{
		name: "create in-progress",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEVSVR8BM4Z3H3VAGAN0J","historyGeneration":"01KZRTEVSV3P2FGVJ60B8QVB4T","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-11T12:28:29.883033-04:00","operations":[{"id":"01KZRTEVSVYBZCVA0NDS5VZC50","type":"task.create","task":{"title":"Task in-progress","description":"","status":"in-progress","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.883033-04:00","updatedAt":"2026-08-11T12:28:29.883033-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEVSVR8BM4Z3H3VAGAN0J","history":{"generation":"01KZRTEVSV3P2FGVJ60B8QVB4T","compactedFrom":null},"logicalClock":1,"task":{"title":"Task in-progress","description":"","status":"in-progress","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:29.883033-04:00","updatedAt":"2026-08-11T12:28:29.883033-04:00","deleted":false}}
`,
	},
	{
		name: "create in-review",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEW00C102H1G5E10TJST6","historyGeneration":"01KZRTEW00V80BXXKA3ZHQMJRP","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-11T12:28:30.08011-04:00","operations":[{"id":"01KZRTEW00VC0CAMSWVZAT1H29","type":"task.create","task":{"title":"Task in-review","description":"","status":"in-review","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:30.08011-04:00","updatedAt":"2026-08-11T12:28:30.08011-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEW00C102H1G5E10TJST6","history":{"generation":"01KZRTEW00V80BXXKA3ZHQMJRP","compactedFrom":null},"logicalClock":1,"task":{"title":"Task in-review","description":"","status":"in-review","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:30.08011-04:00","updatedAt":"2026-08-11T12:28:30.08011-04:00","deleted":false}}
`,
	},
	{
		name: "create done",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEW6CJ88M7XQ3M30EPM3H","historyGeneration":"01KZRTEW6CSHTCD8ETC13P5X38","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-11T12:28:30.2845-04:00","operations":[{"id":"01KZRTEW6CVM6YB062M9MBVRCM","type":"task.create","task":{"title":"Task done","description":"","status":"done","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:30.2845-04:00","updatedAt":"2026-08-11T12:28:30.2845-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZRTEV04X1HZ0QQR1RKFQVPD","taskId":"GD-01KZRTEW6CJ88M7XQ3M30EPM3H","history":{"generation":"01KZRTEW6CSHTCD8ETC13P5X38","compactedFrom":null},"logicalClock":1,"task":{"title":"Task done","description":"","status":"done","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-11T12:28:30.2845-04:00","updatedAt":"2026-08-11T12:28:30.2845-04:00","deleted":false}}
`,
	},
}

func TestGoldenTaskRefsDecodeToTheSameBytes(t *testing.T) {
	for _, fixture := range goldenTaskRefs {
		t.Run(fixture.name, func(t *testing.T) {
			pack, err := DecodeOperationPack([]byte(fixture.operation))
			if err != nil {
				t.Fatalf("DecodeOperationPack() error = %v", err)
			}
			encodedPack, err := EncodeDocument(pack)
			if err != nil {
				t.Fatalf("EncodeDocument(pack) error = %v", err)
			}
			if !bytes.Equal(encodedPack, []byte(fixture.operation)) {
				t.Fatalf("EncodeDocument(pack) = %s, want %s", encodedPack, fixture.operation)
			}

			state, err := DecodeStateDocument([]byte(fixture.state))
			if err != nil {
				t.Fatalf("DecodeStateDocument() error = %v", err)
			}
			encodedState, err := EncodeDocument(state)
			if err != nil {
				t.Fatalf("EncodeDocument(state) error = %v", err)
			}
			if !bytes.Equal(encodedState, []byte(fixture.state)) {
				t.Fatalf("EncodeDocument(state) = %s, want %s", encodedState, fixture.state)
			}
		})
	}
}

func TestGoldenTaskRefsStillValidateAsCheckpoints(t *testing.T) {
	for _, fixture := range goldenTaskRefs {
		t.Run(fixture.name, func(t *testing.T) {
			pack, err := DecodeOperationPack([]byte(fixture.operation))
			if err != nil {
				t.Fatalf("DecodeOperationPack() error = %v", err)
			}
			state, err := DecodeStateDocument([]byte(fixture.state))
			if err != nil {
				t.Fatalf("DecodeStateDocument() error = %v", err)
			}

			var parent *StateDocument
			if fixture.parent != "" {
				decoded, parentErr := DecodeStateDocument([]byte(fixture.parent))
				if parentErr != nil {
					t.Fatalf("DecodeStateDocument(parent) error = %v", parentErr)
				}
				parent = &decoded
			}
			if err := ValidateCheckpoint(parent, pack, state, goldenProjectKey); err != nil {
				t.Fatalf("ValidateCheckpoint() error = %v", err)
			}
		})
	}
}

// The generated documents below are deliberately built from the same small
// alphabet of statuses a stored ref can hold: the six a pre-ledger project is
// using, well-formed tokens no build knows, and tokens that are not tokens at
// all. `blocked` stays in the list precisely because it left the default set —
// refs holding it exist and this normalization has to keep accepting them.
var propertyStatuses = []Status{
	StatusBacklog, StatusReady, StatusBlocked, StatusInProgress, StatusInReview, StatusDone,
	"awaiting-review", "shipped", "triage", "x", "a1-b2-c3",
	"", "In Progress", "In-Progress", "-leading", "trailing-", "double--dash", "under_score", "Ünicode",
}

var propertyTitles = []string{"Task", "  padded  ", "", "  "}

var propertyLabelSets = [][]string{nil, {}, {"git"}, {"ui", "git", "git"}}

// A stored document's status is data, not a value this build gets to correct.
//
// Normalization may trim a title and deduplicate a label set — those rewrites
// are canonicalization, and every clone performs them identically. A status is
// different: rewriting one would make two clones running different builds fold
// the same history into different states, which is exactly the divergence the
// append-only model exists to prevent. So the only two answers a status may get
// are "accepted unchanged" and "rejected".
func TestNormalizeTaskNeverRewritesAStatus(t *testing.T) {
	for _, status := range propertyStatuses {
		for _, title := range propertyTitles {
			for _, labels := range propertyLabelSets {
				task := TaskData{
					Title:    title,
					Status:   status,
					Priority: PriorityMedium,
					Labels:   append([]string(nil), labels...),
					Rank:     "1/1",
				}
				normalized, err := NormalizeTask("WB", task)
				if err != nil {
					continue
				}
				if normalized.Status != status {
					t.Fatalf("NormalizeTask(%q) status = %q, want it unchanged", status, normalized.Status)
				}
				again, err := NormalizeTask("WB", normalized)
				if err != nil {
					t.Fatalf("NormalizeTask() is not idempotent for status %q: %v", status, err)
				}
				if again.Status != status {
					t.Fatalf("NormalizeTask() second pass status = %q, want %q", again.Status, status)
				}
			}
		}
	}
}

// validateFieldSetOperation is a gate on the replay path, and a gate is allowed
// to say no. It is not allowed to say "not that, this": the operation it
// inspects has already been written to a Git object on some clone, so editing
// it here would make this build's fold disagree with the bytes every other
// clone reads.
func TestValidateFieldSetOperationOnlyRejects(t *testing.T) {
	for _, status := range propertyStatuses {
		operation := Operation{ID: "01K0M6B8A4FTT8C39MXXYTW7C2", Type: OperationFieldSet, Field: "status", Value: string(status)}
		before := operation
		err := validateFieldSetOperation(operation)
		if operation != before {
			t.Fatalf("validateFieldSetOperation(%q) rewrote the operation: %#v, want %#v", status, operation, before)
		}
		if err != nil && CategoryOf(err) != CategoryCorruptData {
			t.Fatalf("validateFieldSetOperation(%q) category = %q, want %q", status, CategoryOf(err), CategoryCorruptData)
		}
	}
}

// A stored status this build has never heard of has to read.
//
// It is the ordinary consequence of a per-project vocabulary: a teammate adds
// "awaiting-review", creates a task in it, and pushes. Every clone that has not
// yet fetched the configuration still has to fetch, fold, validate and render
// that task, because the alternative is a repository that reads on one machine
// and reports corruption on another. Only the shape is enforced, and this
// fixture is the built-in "create backlog" ref with its status substituted, so
// nothing else about the document varies.
func TestAStoredStatusOutsideEveryVocabularyStillReads(t *testing.T) {
	const unknown = `"status":"awaiting-review"`
	operation := strings.ReplaceAll(goldenTaskRefs[0].operation, `"status":"backlog"`, unknown)
	state := strings.ReplaceAll(goldenTaskRefs[0].state, `"status":"backlog"`, unknown)
	if operation == goldenTaskRefs[0].operation || state == goldenTaskRefs[0].state {
		t.Fatal("the fixture substitution matched nothing; the golden table changed shape")
	}

	pack, err := DecodeOperationPack([]byte(operation))
	if err != nil {
		t.Fatalf("DecodeOperationPack() error = %v", err)
	}
	if got, err := EncodeDocument(pack); err != nil {
		t.Fatalf("EncodeDocument(pack) error = %v", err)
	} else if !bytes.Equal(got, []byte(operation)) {
		t.Fatalf("EncodeDocument(pack) = %s, want %s", got, operation)
	}

	decoded, err := DecodeStateDocument([]byte(state))
	if err != nil {
		t.Fatalf("DecodeStateDocument() error = %v", err)
	}
	if got := decoded.Task.Status; got != "awaiting-review" {
		t.Fatalf("decoded status = %q, want it preserved", got)
	}
	if err := ValidateCheckpoint(nil, pack, decoded, goldenProjectKey); err != nil {
		t.Fatalf("ValidateCheckpoint() error = %v", err)
	}
}
