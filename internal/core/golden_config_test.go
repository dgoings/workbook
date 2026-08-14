package core

import (
	"bytes"
	"testing"
)

// Golden configuration-ledger fixtures, the counterpart to the task-ref
// fixtures in golden_test.go.
//
// Every byte below was read out of a real repository with `git show
// <commit>:operation.json` and `git show <commit>:state.json`, not composed by
// hand. The three commits are one chain: the genesis this build mints, a pack
// that adds a status and relabels another, and a pack that renames one status
// and retires another so the alias and retirement arrays are populated rather
// than empty.
//
// The configuration ledger is append-only shared history exactly like a task
// ref, so it earns exactly the same protection, and it did not have it before
// this table existed. That is the gap this closes: nothing was pinning the
// bytes of a `workbook.config-operation-pack`, so a member added to
// ConfigOperationPack or ConfigStateDocument could change what every clone
// reads without a test noticing.
//
// The same three properties are asserted per fixture as for a task ref, and
// each one fails for a different kind of regression:
//
//   - the stored bytes decode, which fails when validation tightens;
//   - the decoded value re-encodes to the same bytes, which fails when a member
//     is added, renamed, reordered, or gains an omitempty it should not have;
//   - the checkpoint validates against its parent, which fails when the fold
//     itself changes.
//
// Do not regenerate this table to make a failing test pass. A failure here is
// the finding.
type goldenConfigCommit struct {
	name string
	// parent is the state.json of the commit this pack was applied to, empty
	// for the genesis commit.
	parent    string
	operation string
	state     string
}

var goldenConfigLedger = []goldenConfigCommit{
	{
		name: "config.genesis",
		operation: `{"format":"workbook.config-operation-pack","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","historyGeneration":"01KZYHVSQJQ7JZP8MT9GD1SZKC","actor":{"id":"workbook@example.test"},"logicalClock":1,"wallTime":"2026-08-13T21:53:43.154379Z","operations":[{"id":"01KZYHVSQJPSJ4YWJ95899DTDN","type":"config.genesis","config":{"vocabulary":{"statuses":[{"status":"backlog","label":"Backlog","rank":"1/1","tags":["default"]},{"status":"ready","label":"Ready","rank":"2/1","tags":["next"]},{"status":"in-progress","label":"In Progress","rank":"3/1","tags":[]},{"status":"in-review","label":"In Review","rank":"4/1","tags":[]},{"status":"done","label":"Done","rank":"5/1","tags":["done"]}],"aliases":[],"retired":[]}}}]}
`,
		state: `{"format":"workbook.config-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","history":{"generation":"01KZYHVSQJQ7JZP8MT9GD1SZKC","compactedFrom":null},"logicalClock":1,"config":{"vocabulary":{"statuses":[{"status":"backlog","label":"Backlog","rank":"1/1","tags":["default"]},{"status":"ready","label":"Ready","rank":"2/1","tags":["next"]},{"status":"in-progress","label":"In Progress","rank":"3/1","tags":[]},{"status":"in-review","label":"In Review","rank":"4/1","tags":[]},{"status":"done","label":"Done","rank":"5/1","tags":["done"]}],"aliases":[],"retired":[]}}}
`,
	},
	{
		name: "status.add and status.relabel",
		parent: `{"format":"workbook.config-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","history":{"generation":"01KZYHVSQJQ7JZP8MT9GD1SZKC","compactedFrom":null},"logicalClock":1,"config":{"vocabulary":{"statuses":[{"status":"backlog","label":"Backlog","rank":"1/1","tags":["default"]},{"status":"ready","label":"Ready","rank":"2/1","tags":["next"]},{"status":"in-progress","label":"In Progress","rank":"3/1","tags":[]},{"status":"in-review","label":"In Review","rank":"4/1","tags":[]},{"status":"done","label":"Done","rank":"5/1","tags":["done"]}],"aliases":[],"retired":[]}}}
`,
		operation: `{"format":"workbook.config-operation-pack","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","historyGeneration":"01KZYHVSQJQ7JZP8MT9GD1SZKC","actor":{"id":"workbook@example.test"},"logicalClock":2,"wallTime":"2026-08-13T21:53:43.327428Z","operations":[{"id":"01KZYHVSVXVP8G5JSZJTKY2C5A","type":"status.add","name":"awaiting-review","label":"Awaiting Review","rank":"7/2","tags":["next"]},{"id":"01KZYHVSVXRTT7T6JZNYWPZF7G","type":"status.relabel","status":"done","label":"Shipped"}]}
`,
		state: `{"format":"workbook.config-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","history":{"generation":"01KZYHVSQJQ7JZP8MT9GD1SZKC","compactedFrom":null},"logicalClock":2,"config":{"vocabulary":{"statuses":[{"status":"backlog","label":"Backlog","rank":"1/1","tags":["default"]},{"status":"ready","label":"Ready","rank":"2/1","tags":["next"]},{"status":"in-progress","label":"In Progress","rank":"3/1","tags":[]},{"status":"awaiting-review","label":"Awaiting Review","rank":"7/2","tags":["next"]},{"status":"in-review","label":"In Review","rank":"4/1","tags":[]},{"status":"done","label":"Shipped","rank":"5/1","tags":["done"]}],"aliases":[],"retired":[]}}}
`,
	},
	{
		name: "status.rename and status.remove",
		parent: `{"format":"workbook.config-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","history":{"generation":"01KZYHVSQJQ7JZP8MT9GD1SZKC","compactedFrom":null},"logicalClock":2,"config":{"vocabulary":{"statuses":[{"status":"backlog","label":"Backlog","rank":"1/1","tags":["default"]},{"status":"ready","label":"Ready","rank":"2/1","tags":["next"]},{"status":"in-progress","label":"In Progress","rank":"3/1","tags":[]},{"status":"awaiting-review","label":"Awaiting Review","rank":"7/2","tags":["next"]},{"status":"in-review","label":"In Review","rank":"4/1","tags":[]},{"status":"done","label":"Shipped","rank":"5/1","tags":["done"]}],"aliases":[],"retired":[]}}}
`,
		operation: `{"format":"workbook.config-operation-pack","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","historyGeneration":"01KZYHVSQJQ7JZP8MT9GD1SZKC","actor":{"id":"workbook@example.test"},"logicalClock":3,"wallTime":"2026-08-13T21:53:43.504891Z","operations":[{"id":"01KZYHVT1DD7DZGSXADVS4W5FA","type":"status.rename","from":"ready","to":"todo"},{"id":"01KZYHVT1D070XVGT7J0M99QAG","type":"status.remove","status":"awaiting-review","destination":"in-review"}]}
`,
		state: `{"format":"workbook.config-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","history":{"generation":"01KZYHVSQJQ7JZP8MT9GD1SZKC","compactedFrom":null},"logicalClock":3,"config":{"vocabulary":{"statuses":[{"status":"backlog","label":"Backlog","rank":"1/1","tags":["default"]},{"status":"todo","label":"Ready","rank":"2/1","tags":["next"]},{"status":"in-progress","label":"In Progress","rank":"3/1","tags":[]},{"status":"in-review","label":"In Review","rank":"4/1","tags":[]},{"status":"done","label":"Shipped","rank":"5/1","tags":["done"]}],"aliases":[{"from":"ready","to":"todo"}],"retired":[{"status":"awaiting-review","destination":"in-review"}]}}}
`,
	},
}

func TestGoldenConfigLedgerDecodesToTheSameBytes(t *testing.T) {
	for _, fixture := range goldenConfigLedger {
		t.Run(fixture.name, func(t *testing.T) {
			pack, err := DecodeConfigOperationPack([]byte(fixture.operation))
			if err != nil {
				t.Fatalf("DecodeConfigOperationPack() error = %v", err)
			}
			encodedPack, err := EncodeDocument(pack)
			if err != nil {
				t.Fatalf("EncodeDocument(pack) error = %v", err)
			}
			if !bytes.Equal(encodedPack, []byte(fixture.operation)) {
				t.Fatalf("EncodeDocument(pack) = %s, want %s", encodedPack, fixture.operation)
			}

			state, err := DecodeConfigStateDocument([]byte(fixture.state))
			if err != nil {
				t.Fatalf("DecodeConfigStateDocument() error = %v", err)
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

func TestGoldenConfigLedgerStillValidatesAsCheckpoints(t *testing.T) {
	for _, fixture := range goldenConfigLedger {
		t.Run(fixture.name, func(t *testing.T) {
			pack, err := DecodeConfigOperationPack([]byte(fixture.operation))
			if err != nil {
				t.Fatalf("DecodeConfigOperationPack() error = %v", err)
			}
			state, err := DecodeConfigStateDocument([]byte(fixture.state))
			if err != nil {
				t.Fatalf("DecodeConfigStateDocument() error = %v", err)
			}

			var parent *ConfigStateDocument
			if fixture.parent != "" {
				decoded, parentErr := DecodeConfigStateDocument([]byte(fixture.parent))
				if parentErr != nil {
					t.Fatalf("DecodeConfigStateDocument(parent) error = %v", parentErr)
				}
				parent = &decoded
			}
			if err := ValidateConfigCheckpoint(parent, pack, state); err != nil {
				t.Fatalf("ValidateConfigCheckpoint() error = %v", err)
			}
		})
	}
}
