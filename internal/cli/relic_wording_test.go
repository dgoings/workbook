package cli

import (
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A JSON null is a value like any other, and relic show printed it as
// nothing: "reviewed: " with an empty right-hand side.
func TestRelicShowPrintsANullValue(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Values", "--type", "spec")
	id := created.Relic.ID
	added := mustUpdateRelic(t, repository, id, "--set-json", "reviewed=null", "--set", "owner=",
		"--add-part", "--part-type", "note", "--field-json", "checked=null")
	part := added.Parts[0].ID

	code, stdout, stderr := run(t, repository, "relic", "show", id)
	if code != 0 || stderr != "" {
		t.Fatalf("relic show = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, want := range []string{"\n  reviewed: null\n", "\n  checked: null\n", "\n  owner: \n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("relic show = %q, want it to hold %q", stdout, want)
		}
	}
	code, stdout, stderr = run(t, repository, "relic", "show", id+"#"+part)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "\n  checked: null\n") {
		t.Errorf("relic show of the part = (%d, %q, %q), want its null field printed as null", code, stdout, stderr)
	}
}

// A document with no type, created with no --type, was refused for a type ""
// that "must be lowercase letters and digits". It is told the document has no
// type, and how to give one.
func TestRelicCreateFromADocumentWithNoTypeSaysSo(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for label, document := range map[string]string{
		"no type member":    `{"format":"workbook.relic","version":1,"title":"Untyped"}`,
		"an empty type":     `{"format":"workbook.relic","version":1,"title":"Untyped","type":""}`,
		"a part and a type": `{"format":"workbook.relic","version":1,"title":"Untyped","parts":[{"type":"note","fields":{}}]}`,
	} {
		file := writeRelicDocumentFile(t, []byte(document))
		message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "create", "--from-file", file)
		for _, want := range []string{"has no type", "--type"} {
			if !strings.Contains(message, want) {
				t.Errorf("a document with %s: message %q does not hold %q", label, message, want)
			}
		}
		if strings.Contains(message, "lowercase") {
			t.Errorf("a document with %s: message %q describes a type nobody gave", label, message)
		}
	}
	if refs := refsUnder(t, repository, "refs/workbook/relics/"); refs != "" {
		t.Fatalf("a refused document wrote a relic:\n%s", refs)
	}
	file := writeRelicDocumentFile(t, []byte(`{"format":"workbook.relic","version":1,"title":"Untyped"}`))
	if created := mustCreateRelic(t, repository, "--from-file", file, "--type", "spec"); created.Relic.Type != "spec" {
		t.Fatalf("create --from-file --type spec made a relic of type %q", created.Relic.Type)
	}
}

// What a person reads when a relic's stored data is damaged: one line that
// names the relic once and the cause once. The wrappers used to say the cause
// twice ("a stored property name is invalid: a property name must not ..."),
// and validate's line named the relic twice.
func TestACorruptRelicIsReportedOnceAndBriefly(t *testing.T) {
	t.Parallel()
	const emptyProperties = `"properties":{}`
	set := func(properties string) func(string) string {
		return func(state string) string {
			return strings.Replace(state, emptyProperties, `"properties":{`+properties+`}`, 1)
		}
	}
	tests := map[string]struct {
		change func(state string) string
		cause  string
	}{
		"a state that is not canonical": {
			func(state string) string { return strings.TrimSuffix(state, "\n") + " \n" },
			"relic state is not canonical",
		},
		"a name that breaks the rules": {
			set(`" owner":"x"`),
			"a stored property name must not begin or end with white space",
		},
		"a value over its limit": {
			set(`"big":"` + strings.Repeat("x", 70000) + `"`),
			"a stored property value is 70002 bytes; the limit is 65536 bytes",
		},
		"a value nested too deep": {
			set(`"deep":` + strings.Repeat("[", 40) + strings.Repeat("]", 40)),
			"a stored property value nests arrays and objects 40 levels deep; the limit is 32 levels",
		},
		"a state its operations do not produce": {
			func(state string) string {
				return strings.Replace(state, `"title":"A new title"`, `"title":"Another title"`, 1)
			},
			"the stored relic is not what its operations produce",
		},
		"a missing object": {nil, "COMMIT: a directory it names is missing from Git or cannot be read"},
	}
	for label, test := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			repository := initializedRepository(t)
			fixture := openRelicFixture(t, repository)
			root := fixture.createRelic(t, 2, []byte("the first text\n"))
			relicID := root.Operation.RelicID
			pack := nextRelicPack(root, core.RelicOperation{ID: relicTestID(22), Type: core.RelicOperationTitleSet, Title: "A new title"})
			record, err := core.ApplyRelic(&root.Record, pack)
			if err != nil {
				t.Fatalf("ApplyRelic() error = %v", err)
			}
			state := string(encodedRelicDocument(t, record.State))
			parts := gitOutput(t, repository, "rev-parse", root.Head+":parts")
			if test.change != nil {
				changed := test.change(state)
				if changed == state {
					t.Fatalf("the change left the state as it was: %s", state)
				}
				state = changed
			} else {
				absent := strings.Repeat("0", len(root.Head)-1) + "1"
				parts = gitInput(t, repository,
					[]byte("040000 tree "+absent+"\t"+root.Record.State.Relic.Parts[0].ID+"\n"), "mktree", "--missing")
			}
			forged := fixture.forgeRelicCommit(t, root.Head, encodedRelicDocument(t, pack), []byte(state), parts)
			gitInput(t, repository, nil, "update-ref", relicRef(relicID), forged, root.Head)
			cause := strings.Replace(test.cause, "COMMIT", "commit "+forged, 1)

			code, stdout, stderr := run(t, repository, "relic", "show", relicID)
			want := "workbook: relic " + relicID + ": " + cause + "; run \"workbook validate\" for details\n"
			if code != 7 || stdout != "" || stderr != want {
				t.Errorf("relic show = (%d, %q, %q), want exit 7 and %q", code, stdout, stderr, want)
			}
			// Two lines of a terminal 100 columns wide.
			if len(stderr) > 200 {
				t.Errorf("relic show's message is %d bytes long: %q", len(stderr), stderr)
			}

			// validate's line names the relic and the commit itself, so the
			// message under it names neither again.
			code, stdout, _ = run(t, repository, "validate")
			line := "Invalid " + relicID + " at " + forged + " [corrupt-data]: " +
				strings.TrimPrefix(cause, "commit "+forged+": ") + "\n"
			if code != 7 || !strings.Contains(stdout, line) {
				t.Errorf("validate = (%d, %q), want exit 7 and the line %q", code, stdout, line)
			}
			if count := strings.Count(stdout, relicID); count != 1 {
				t.Errorf("validate names the relic %d times: %q", count, stdout)
			}
			if count := strings.Count(stdout, forged); count != 1 {
				t.Errorf("validate names the commit %d times: %q", count, stdout)
			}
		})
	}
}
