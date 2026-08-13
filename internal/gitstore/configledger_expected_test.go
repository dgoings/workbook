package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// WriteConfigOperationOnto records a change only where its author meant it.
//
// The caller it exists for is a long-lived surface — the board's status panel —
// whose change is composed against a vocabulary somebody read seconds earlier,
// with a teammate's `workbook status` free to land in between. Recording the
// change onto whatever tip is current at the moment the write runs is not
// "applying it to current state": a rename authored against one set of statuses
// and replayed onto another answers a question nobody asked, and tells neither
// author about it.
func TestWriteConfigOperationOntoRefusesASupersededTip(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	first := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)

	// Onto the tip the caller named: recorded.
	second, err := repo.WriteConfigOperationOnto(ctx, config, core.CryptoULIDSource{},
		configOperations(relabelOperation("todo", "Up Next")), "", first.Head)
	if err != nil {
		t.Fatalf("WriteConfigOperationOnto() onto the current tip error = %v", err)
	}
	if second.Head == first.Head {
		t.Fatal("the write did not move the ledger")
	}
	if got := second.Vocabulary().Label("todo"); got != "Up Next" {
		t.Fatalf("todo label = %q, want the recorded one", got)
	}

	// Onto the tip it named a moment ago: refused, and nothing recorded.
	_, err = repo.WriteConfigOperationOnto(ctx, config, core.CryptoULIDSource{},
		configOperations(relabelOperation("todo", "Next Up")), "", first.Head)
	if err == nil {
		t.Fatal("WriteConfigOperationOnto() recorded a change onto a superseded tip")
	}
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q; error = %v", core.CategoryOf(err), core.CategoryStaleWrite, err)
	}
	if !strings.Contains(err.Error(), first.Head) || !strings.Contains(err.Error(), second.Head) {
		t.Fatalf("error = %q, want it to name what the caller composed against and what the ledger holds", err)
	}
	if got := refValue(t, repo, configRef); got != second.Head {
		t.Fatalf("%s = %q, want the refused write to have left it at %q", configRef, got, second.Head)
	}

	// The unexpecting write is unchanged: every command takes it, and it takes
	// whatever this clone holds.
	third := writeConfig(t, repo, config, configOperations(relabelOperation("todo", "Next Up"))...)
	if third.Head == second.Head {
		t.Fatal("WriteConfigOperation() did not move the ledger")
	}
}

// The empty head is an expectation too: it names a project that has never
// recorded a status change, which is what such a project's board reports and
// what its first change has to be composed against.
func TestWriteConfigOperationOntoExpectsNoLedgerAsAHead(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	if refExists(t, repo, configRef) {
		t.Fatalf("%s exists before anything configured a status", configRef)
	}

	seeded, err := repo.WriteConfigOperationOnto(ctx, config, core.CryptoULIDSource{},
		configOperations(renameOperation("ready", "todo")), "", "")
	if err != nil {
		t.Fatalf("WriteConfigOperationOnto() with no ledger error = %v", err)
	}
	if !seeded.Seeded {
		t.Fatal("the first change did not seed the ledger")
	}
	records := configChain(t, repo, config)
	if len(records) != 2 {
		t.Fatalf("ledger holds %d commit(s), want a genesis root and the author's pack", len(records))
	}

	// And a caller still expecting no ledger, now that there is one, is refused
	// rather than appended: it composed its change against a project that had
	// none.
	_, err = repo.WriteConfigOperationOnto(ctx, config, core.CryptoULIDSource{},
		configOperations(relabelOperation("todo", "Up Next")), "", "")
	if err == nil {
		t.Fatal("WriteConfigOperationOnto() appended a change composed against no ledger at all")
	}
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q; error = %v", core.CategoryOf(err), core.CategoryStaleWrite, err)
	}
	if !strings.Contains(err.Error(), "no configuration ledger") {
		t.Fatalf("error = %q, want it to name the state the caller expected", err)
	}
	if got := refValue(t, repo, configRef); got != seeded.Head {
		t.Fatalf("%s = %q, want the refused write to have left it at %q", configRef, got, seeded.Head)
	}
}
