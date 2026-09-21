package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// configGenesisGeneration is the writer-format generation a ledger this build
// creates actually spends: the highest one any section its genesis records
// requires, which is the priorities section's three.
//
// It is a literal rather than core.SupportedFormatGeneration because the two
// parted company when the key section arrived. Nothing seeds that section — a
// project's founding key lives in its identity ref, so a genesis records no
// keys and no project this build creates asks a reader for generation four —
// while the constant names what this build can fold, which is four. Comparing
// a stored marker against the constant would now assert that every new project
// parks every generation-three clone on the team, which is exactly the
// regression these assertions exist to catch, so it is named here rather than
// relaxed to "at least": an under-marked genesis is invisible to ">=".
//
// Whoever adds the next section a genesis does record raises this line with it.
const configGenesisGeneration = 3

// storedDocument is one operation.json or state.json as a clone will fetch it,
// together with the ref it belongs to — the marker's rules differ per ref, so
// the ref is part of the fact.
type storedDocument struct {
	ref      string
	name     string
	label    string
	contents string
}

// storedWorkbookDocuments returns every operation.json and state.json this
// repository holds, across every task ref and the configuration ledger.
//
// It walks the commits rather than the tips because the durable interface is
// every document a clone will ever fetch, not just the newest one.
func storedWorkbookDocuments(t *testing.T, repository string) []storedDocument {
	t.Helper()
	refs := strings.Fields(gitOutput(t, repository, "for-each-ref", "--format=%(refname)",
		"refs/workbook/tasks/", "refs/workbook/config"))
	if len(refs) == 0 {
		t.Fatal("the repository holds no Workbook refs to inspect")
	}
	var documents []storedDocument
	for _, ref := range refs {
		for _, commit := range strings.Fields(gitOutput(t, repository, "rev-list", ref)) {
			for _, name := range []string{"operation.json", "state.json"} {
				documents = append(documents, storedDocument{
					ref:      ref,
					name:     name,
					label:    ref + " " + commit + ":" + name,
					contents: gitOutput(t, repository, "show", commit+":"+name),
				})
			}
		}
	}
	return documents
}

// storedMinReader reports the writer-format marker a stored document carries,
// and whether it carries one at all. Absence is a distinct answer from zero:
// absence is the canonical spelling of generation zero, and an explicit zero is
// a document this build would refuse.
func storedMinReader(t *testing.T, document storedDocument) (int, bool) {
	t.Helper()
	var marker struct {
		MinReader *int `json:"minReader"`
	}
	if err := json.Unmarshal([]byte(document.contents), &marker); err != nil {
		t.Fatalf("decode %s: %v; document = %s", document.label, err, document.contents)
	}
	if marker.MinReader == nil {
		return 0, false
	}
	return *marker.MinReader, true
}

// The writer-format marker is spent per ref, and only where this build means to
// spend it.
//
// The marker names the minimum reader generation a document needs, and absence
// means generation zero. On task refs it is still free: no task document this
// build writes carries it, so every clone that could ever fold a task still
// can, byte for byte. On the configuration ledger it is spent exactly once, at
// the genesis — every ledger this build opens records the built-in priorities,
// and a genesis carrying a section an older build has never heard of must say
// so or that build reads the project as corrupt rather than as out of date.
// Having been spent there, it is not spendable again by an ordinary verb: a
// status pack asks for generation zero, the same as it always did.
//
// That last claim is why this test is worth its runtime. It is the only
// execution-level check that every status entry in configOperationMinReader is
// zero; one entry set wrong changes bytes on a real ledger, fails no unit test,
// and silently parks every older clone on every project that renames a status.
// The checkpoints are checked too, against a rule with no exceptions: a
// checkpoint carries the running maximum, so it is never absent once the
// genesis has stamped one and never above what this build can read back.
//
// Both genesis assertions below read configGenesisGeneration rather than
// core.SupportedFormatGeneration, because the key section made the two
// different numbers: a genesis records no keys, so a ledger this build creates
// still spends exactly three. That constant's comment says what raising it
// costs and why the comparison stays exact.
func TestTheWriterFormatMarkerIsSpentOnlyAtTheConfigurationGenesis(t *testing.T) {
	repository := initializedRepository(t)

	commands := [][]string{
		{"create", "First task", "--json"},
		{"create", "Second task", "--json"},
		{"status", "add", "awaiting-review", "--label", "Awaiting Review", "--json"},
		{"status", "rename", "ready", "todo", "--json"},
		{"status", "label", "todo", "Up Next", "--json"},
		{"status", "move", "todo", "--after", "in-progress", "--json"},
		{"status", "tag", "in-progress", "--tag", "next", "--json"},
		{"status", "untag", "in-progress", "next", "--json"},
		{"status", "delete", "awaiting-review", "--into", "in-review", "--json"},
	}
	for _, command := range commands {
		if code, _, stderr := run(t, repository, command...); code != 0 {
			t.Fatalf("%v code = %d, want 0; stderr = %q", command, code, stderr)
		}
	}

	code, stdout, stderr := run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	var ids []string
	for _, id := range strings.Split(stdout, `"id":"`)[1:] {
		ids = append(ids, id[:strings.Index(id, `"`)])
	}
	if len(ids) != 2 {
		t.Fatalf("list returned %d task IDs, want 2; output = %q", len(ids), stdout)
	}
	for _, command := range [][]string{
		{"update", ids[0], "--title", "Renamed", "--description", "Prose", "--json"},
		{"update", ids[0], "--label", "storage", "--json"},
		{"move", ids[0], "--after", ids[1], "--json"},
		{"update", ids[0], "--status", "in-progress", "--json"},
		{"depend", ids[0], ids[1], "--json"},
		{"free", ids[0], ids[1], "--json"},
		{"delete", ids[1], "--json"},
		{"restore", ids[1], "--json"},
	} {
		if code, _, stderr := run(t, repository, command...); code != 0 {
			t.Fatalf("%v code = %d, want 0; stderr = %q", command, code, stderr)
		}
	}

	documents := storedWorkbookDocuments(t, repository)
	if len(documents) < 20 {
		t.Fatalf("inspected %d documents, want the whole history", len(documents))
	}

	genesisPacks := 0
	for _, document := range documents {
		generation, marked := storedMinReader(t, document)
		switch {
		case strings.HasPrefix(document.ref, "refs/workbook/tasks/"):
			if marked {
				t.Fatalf("%s carries a writer-format marker (%d); task refs are still free: %s",
					document.label, generation, document.contents)
			}
		case document.name == "operation.json" && strings.Contains(document.contents, `"type":"`+string(core.ConfigGenesis)+`"`):
			genesisPacks++
			if !marked || generation != configGenesisGeneration {
				t.Fatalf("%s is the genesis pack and carries marker %d (present = %v), want exactly %d: %s",
					document.label, generation, marked, configGenesisGeneration, document.contents)
			}
		case document.name == "operation.json":
			if marked {
				t.Fatalf("%s carries a writer-format marker (%d); an ordinary configuration verb spends none: %s",
					document.label, generation, document.contents)
			}
		default:
			// Both halves of the checkpoint rule, and the second is not
			// implied by the first only because the first is the half that
			// moves: configGenesisGeneration rises whenever a genesis records
			// a new section, and "never above what this build can read back"
			// has to keep holding after it does.
			if !marked || generation != configGenesisGeneration || generation > core.SupportedFormatGeneration {
				t.Fatalf("%s is a configuration checkpoint and carries marker %d (present = %v), want exactly %d "+
					"and no more than this build folds (%d): %s",
					document.label, generation, marked, configGenesisGeneration,
					core.SupportedFormatGeneration, document.contents)
			}
		}
	}
	if genesisPacks != 1 {
		t.Fatalf("inspected %d genesis packs, want exactly 1; the ledger's shape changed", genesisPacks)
	}
}
