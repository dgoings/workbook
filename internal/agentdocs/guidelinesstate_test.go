package agentdocs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// GuidelinesState reads and never writes, in every state the file can be in.
//
// The promise is the whole reason it exists. `workbook serve` calls it to find
// out whether a status change it has just recorded left the generated file
// describing statuses this project no longer has — and the board is a
// long-running server that may be answering while somebody rebases the checkout
// it lives in, so a function that quietly refreshed the file would put a write
// into an HTTP request that must not have one. A caller cannot check that for
// itself: it would have to know what this was going to write.
func TestGuidelinesStateReadsWithoutWriting(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Options)
		want    State
	}{
		{name: "no file at all", want: StateAbsent},
		{
			name: "a file Workbook did not write",
			prepare: func(t *testing.T, options *Options) {
				writeFile(t, filepath.Join(options.Root, GuidelinesPath), "somebody's own notes\n")
			},
			want: StateAbsent,
		},
		{
			name: "a block whose inputs have changed",
			prepare: func(t *testing.T, options *Options) {
				if _, err := Apply(*options); err != nil {
					t.Fatalf("Apply() error = %v", err)
				}
				// The project renames a status, which is exactly the change the
				// board records without rewriting this file.
				options.Vocabulary = renamedVocabulary(t)
			},
			want: StateStale,
		},
		{
			// The state the board's warning names --force for. Its agreement
			// with the writer is pinned below.
			name: "a block somebody edited",
			prepare: func(t *testing.T, options *Options) {
				if _, err := Apply(*options); err != nil {
					t.Fatalf("Apply() error = %v", err)
				}
				writeFile(t, filepath.Join(options.Root, GuidelinesPath),
					"<!-- workbook:begin guidelines 0.2.0 -->\nedited by hand\n<!-- workbook:end -->\n")
			},
			want: StateModified,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions(t)
			options.Vocabulary = core.DefaultVocabulary()
			path := filepath.Join(options.Root, GuidelinesPath)
			if test.prepare != nil {
				test.prepare(t, &options)
				// A modification time in the past, so a rewrite that happened to
				// produce identical bytes would still be visible below.
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			before := statOrAbsent(t, path)

			state, err := GuidelinesState(options)
			if err != nil {
				t.Fatalf("GuidelinesState() error = %v", err)
			}
			if state != test.want {
				t.Fatalf("GuidelinesState() = %q, want %q", state, test.want)
			}
			if after := statOrAbsent(t, path); after != before {
				t.Fatalf("the guidelines went from %s to %s; GuidelinesState wrote to the working tree", before, after)
			}
			// Nothing else was created either: the directory this would have had
			// to make is the tell for a write that produced no file yet.
			if test.prepare == nil {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("stat %s = %v, want the directory left uncreated", filepath.Dir(path), err)
				}
			}
		})
	}
}

// A state this reports must be a state the writer agrees with, or a caller that
// warns on it is warning about something `workbook docs update` will not fix.
func TestGuidelinesStateAgreesWithTheWriter(t *testing.T) {
	options := testOptions(t)
	options.Vocabulary = core.DefaultVocabulary()
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	state, err := GuidelinesState(options)
	if err != nil {
		t.Fatalf("GuidelinesState() error = %v", err)
	}
	if state != StateCurrent {
		t.Fatalf("GuidelinesState() = %q after a refresh, want %q", state, StateCurrent)
	}

	// Somebody edits the generated file: the state the board reports is the one
	// that names --force, and the writer refuses it without one.
	writeFile(t, filepath.Join(options.Root, GuidelinesPath),
		"<!-- workbook:begin guidelines 0.2.0 -->\nedited by hand\n<!-- workbook:end -->\n")
	state, err = GuidelinesState(options)
	if err != nil {
		t.Fatalf("GuidelinesState() error = %v", err)
	}
	if state != StateModified {
		t.Fatalf("GuidelinesState() = %q for an edited file, want %q", state, StateModified)
	}
	report, err := ApplyGuidelines(options)
	if err == nil {
		t.Fatal("ApplyGuidelines() overwrote an edited file without --force")
	}
	if got := stateOf(t, report, GuidelinesPath); got != state {
		t.Fatalf("the writer reports %q where the read reports %q", got, state)
	}
}

// renamedVocabulary is the default statuses with one renamed, which is the
// smallest change that makes generated guidelines describe a status this
// project no longer has.
func renamedVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	document := core.DefaultVocabulary().Document()
	definitions := document.Statuses
	definitions[len(definitions)-1].Status = "landed"
	definitions[len(definitions)-1].Label = "Landed"
	vocabulary, err := core.NewVocabulary(definitions, document.Aliases, document.Retired)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// statOrAbsent renders a file as one comparable string: absent, or its
// modification time and its contents. Both, because a rewrite that reproduced
// the same bytes is still a write into somebody's working tree.
func statOrAbsent(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s (%d bytes) %q", info.ModTime().Format(time.RFC3339Nano), info.Size(), contents)
}
