package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/gitstore"
)

const (
	removalAdvice = `remove a "no project's task" ref with: git push origin --delete <ref>`
	keepWarning   = `deleting a "may be another Workbook's task" ref would destroy`
)

// ignoredRefLine is the whole line the report writes for one ref, so a test
// that matches it proves the verdict sits on that name's own line rather than
// somewhere in the report.
func ignoredRefLine(ignored gitstore.IgnoredRef) string {
	verdict := ignoredRefRemovable
	if ignored.PlausibleTask {
		verdict = ignoredRefPlausible
	}
	return "Ignored:\t" + ignored.Ref + "\t" + verdict + "\t" + ignored.Reason + "\n"
}

// The ignored-ref report is the only advice Workbook gives to delete something
// from a shared remote, so the command appears for a name no project's ID
// format can produce and for nothing else. A name a newer version or a second
// project's key would produce is real append-only history to everyone but this
// build, and gets a warning in place of a command.
//
// The verdict rides on each ref's own line. A mixed list is the case that
// matters: two names, one deletable and one not, and a footer for each. Without
// a per-line verdict the reader would have to guess which name the deletion
// command meant, and guessing wrong destroys shared history.
func TestWriteIgnoredRefsOffersRemovalOnlyForNamesNoProjectCanOwn(t *testing.T) {
	junk := gitstore.IgnoredRef{
		Ref:    "refs/workbook/tasks/EVIL",
		Reason: "the ref does not name one task",
	}
	foreign := gitstore.IgnoredRef{
		Ref:           "refs/workbook/tasks/OPS-01K0M6B8A4FTT8C39MXXYTW7D9",
		Reason:        `task ID "OPS-01K0M6B8A4FTT8C39MXXYTW7D9" must begin with "WB-"`,
		PlausibleTask: true,
	}

	for _, test := range []struct {
		name        string
		ignored     []gitstore.IgnoredRef
		wantRemoval bool
		wantWarning bool
	}{
		{name: "none"},
		{name: "junk only", ignored: []gitstore.IgnoredRef{junk}, wantRemoval: true},
		{name: "plausible only", ignored: []gitstore.IgnoredRef{foreign}, wantWarning: true},
		{
			name:        "both",
			ignored:     []gitstore.IgnoredRef{junk, foreign},
			wantRemoval: true,
			wantWarning: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writeIgnoredRefs(&output, "origin", test.ignored)
			got := output.String()

			if len(test.ignored) == 0 {
				if got != "" {
					t.Fatalf("output = %q, want nothing reported without ignored refs", got)
				}
				return
			}
			for _, ignored := range test.ignored {
				if want := ignoredRefLine(ignored); !strings.Contains(got, want) {
					t.Fatalf("output = %q, want it to contain the line %q", got, want)
				}
			}
			if want := "kept on origin"; !strings.Contains(got, want) {
				t.Fatalf("output = %q, want it to contain %q", got, want)
			}
			if strings.Contains(got, removalAdvice) != test.wantRemoval {
				t.Fatalf("output = %q, removal advice present = %t, want %t",
					got, !test.wantRemoval, test.wantRemoval)
			}
			if strings.Contains(got, keepWarning) != test.wantWarning {
				t.Fatalf("output = %q, keep warning present = %t, want %t",
					got, !test.wantWarning, test.wantWarning)
			}
		})
	}
}

// A mutation that synchronizes inline prints one sync line, and that line was
// the whole report: the fetch behind it had already skipped a ref origin holds,
// and said so only in the JSON envelope. The names travel rather than a count,
// because a reader can act on a report only if it says which ref it is about.
func TestWriteSyncReportNamesTheRefsItsFetchIgnored(t *testing.T) {
	ignored := gitstore.IgnoredRef{
		Ref:    "refs/workbook/tasks/EVIL",
		Reason: "the ref does not name one task",
	}
	var output bytes.Buffer
	writeSyncReport(&output, &syncReport{
		Enabled: true,
		Status:  syncStatusCompleted,
		Fetch:   &gitstore.SyncResult{Remote: "origin", Ignored: []gitstore.IgnoredRef{ignored}},
	})

	got := output.String()
	if want := "Sync:\t" + syncStatusCompleted + "\n"; !strings.HasPrefix(got, want) {
		t.Fatalf("output = %q, want it to start with %q", got, want)
	}
	if want := ignoredRefLine(ignored); !strings.Contains(got, want) {
		t.Fatalf("output = %q, want it to contain the line %q", got, want)
	}
	if !strings.Contains(got, removalAdvice) {
		t.Fatalf("output = %q, want the removal advice for a name no project can own", got)
	}
}

// The report is additive: a fetch that skipped nothing must print exactly what
// it printed before, because a line saying so on every mutation is noise.
func TestWriteSyncReportStaysOneLineWithoutIgnoredRefs(t *testing.T) {
	var output bytes.Buffer
	writeSyncReport(&output, &syncReport{
		Enabled: true,
		Status:  syncStatusCompleted,
		Fetch:   &gitstore.SyncResult{Remote: "origin"},
	})

	if want := "Sync:\t" + syncStatusCompleted + "\n"; output.String() != want {
		t.Fatalf("output = %q, want exactly %q", output.String(), want)
	}
}

// The command names a placeholder, never the ref itself: a ref name is not
// shell-quoted here, and the reader has to choose which ref they mean anyway.
func TestWriteIgnoredRefsNeverInterpolatesARefIntoTheCommand(t *testing.T) {
	var output bytes.Buffer
	writeIgnoredRefs(&output, "origin", []gitstore.IgnoredRef{{
		Ref:    "refs/workbook/tasks/$(rm -rf ~)",
		Reason: "the ref does not name one task",
	}})

	got := output.String()
	if !strings.Contains(got, removalAdvice) {
		t.Fatalf("output = %q, want the removal advice for a name no project can own", got)
	}
	if strings.Contains(got, "--delete refs/workbook/tasks/$(rm -rf ~)") {
		t.Fatalf("output = %q, want the command to keep its <ref> placeholder", got)
	}
}
