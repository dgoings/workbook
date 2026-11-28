package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicBodyFiles is the readFile a parser test gives parseRelicUpdateArgs: it
// serves files by name, answers not-found for any other, and counts the reads.
func relicBodyFiles(files map[string]string) (func(string) ([]byte, error), *int) {
	reads := 0
	return func(name string) ([]byte, error) {
		reads++
		content, exists := files[name]
		if !exists {
			return nil, core.Errorf(core.CategoryNotFound, "cannot read %s", name)
		}
		return []byte(content), nil
	}, &reads
}

func parseRelicUpdate(t *testing.T, files map[string]string, args ...string) (relicUpdateRequest, error) {
	t.Helper()
	readFile, _ := relicBodyFiles(files)
	return parseRelicUpdateArgs(args, readFile)
}

func TestParseRelicUpdateArgsKeepsOrderAndGroups(t *testing.T) {
	t.Parallel()
	request, err := parseRelicUpdate(t, map[string]string{"notes.md": "# Notes\n", "b.txt": "text\n"},
		"WB-R-X", "--title", "T",
		"--add-part", "--part-type", "section", "--field", "heading=Goals", "--body-file", "notes.md",
		"--part", "P1", "--field-json", "n=3", "--clear-field", "old", "--body-file", "b.txt", "--base", "V1",
		"--move-part", "P2", "--before", "P1",
		"--remove-part", "P3",
		"--restore-part", "P4",
		"--set", "status=approved", "--json")
	if err != nil {
		t.Fatalf("parseRelicUpdateArgs() error = %v", err)
	}
	want := relicUpdateRequest{
		Relic: "WB-R-X",
		Edits: []core.RelicEdit{
			{Kind: core.RelicEditTitle, Title: "T"},
			{
				Kind:     core.RelicEditAddPart,
				PartType: "section",
				Fields:   []core.RelicFieldEdit{{Name: "heading", Value: json.RawMessage(`"Goals"`)}},
				Body:     &core.RelicBodyInput{Format: "md", Content: []byte("# Notes\n")},
			},
			{
				Kind: core.RelicEditPart,
				Part: "P1",
				Fields: []core.RelicFieldEdit{
					{Name: "n", Value: json.RawMessage(`3`)},
					{Name: "old", Clear: true},
				},
				// A --part group names no format of its own: the file's name
				// goes along for the relic to use if the part has no body yet.
				Body:     &core.RelicBodyInput{Content: []byte("text\n")},
				BodyFile: "b.txt",
				Base:     "V1",
			},
			{Kind: core.RelicEditMovePart, Part: "P2", Before: "P1"},
			{Kind: core.RelicEditRemovePart, Part: "P3"},
			{Kind: core.RelicEditRestorePart, Part: "P4"},
			{Kind: core.RelicEditSetProperty, Name: "status", Value: json.RawMessage(`"approved"`)},
		},
		JSON: true,
	}
	if !reflect.DeepEqual(request, want) {
		t.Fatalf("request = %+v\nwant      %+v", request, want)
	}
}

func TestParseRelicUpdateArgsAcceptsEveryFlagForm(t *testing.T) {
	t.Parallel()
	files := map[string]string{"notes.md": "# Notes\n", "draft.my_notes": "draft\n", "plain": "plain\n", "Notes.MD": "caps\n",
		"some/where/Notes.MD": "deep\n"}
	text := func(value string) json.RawMessage { return json.RawMessage(strconv.Quote(value)) }
	for _, test := range []struct {
		name string
		args []string
		want relicUpdateRequest
	}{
		{
			name: "a value joined with an equals sign, and one dash for two",
			args: []string{"WB-R-X", "--title=T", "-set", "a=b", "-json"},
			want: relicUpdateRequest{Relic: "WB-R-X", JSON: true, Edits: []core.RelicEdit{
				{Kind: core.RelicEditTitle, Title: "T"},
				{Kind: core.RelicEditSetProperty, Name: "a", Value: text("b")},
			}},
		},
		{
			name: "a name=value splits at the first equals sign",
			args: []string{"WB-R-X", "--set", "a=b=c", "--set=d=", "--set-json", `e={"f":"g=h"}`},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{
				{Kind: core.RelicEditSetProperty, Name: "a", Value: text("b=c")},
				{Kind: core.RelicEditSetProperty, Name: "d", Value: text("")},
				{Kind: core.RelicEditSetProperty, Name: "e", Value: json.RawMessage(`{"f":"g=h"}`)},
			}},
		},
		{
			name: "text is stored as a JSON string and JSON in its stored form",
			args: []string{"WB-R-X", "--set", "version=1.0", "--set-json", "count= 3 ", "--set-json", `tags=[ "a", "b" ]`, "--clear", "old"},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{
				{Kind: core.RelicEditSetProperty, Name: "version", Value: text("1.0")},
				{Kind: core.RelicEditSetProperty, Name: "count", Value: json.RawMessage(`3`)},
				{Kind: core.RelicEditSetProperty, Name: "tags", Value: json.RawMessage(`["a","b"]`)},
				{Kind: core.RelicEditClearProperty, Name: "old"},
			}},
		},
		{
			name: "an option that takes a value takes the next argument, whatever it is",
			args: []string{"WB-R-X", "--title", "--json", "--part", "P1", "--field", "--field=x"},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{
				{Kind: core.RelicEditTitle, Title: "--json"},
				{Kind: core.RelicEditPart, Part: "P1", Fields: []core.RelicFieldEdit{{Name: "--field", Value: text("x")}}},
			}},
		},
		{
			name: "an option of the relic inside a group leaves the group open",
			args: []string{"WB-R-X", "--part", "P1", "--field", "a=b", "--set", "s=t", "--json", "--title", "T", "--clear-field", "c"},
			want: relicUpdateRequest{Relic: "WB-R-X", JSON: true, Edits: []core.RelicEdit{
				{Kind: core.RelicEditPart, Part: "P1", Fields: []core.RelicFieldEdit{
					{Name: "a", Value: text("b")},
					{Name: "c", Clear: true},
				}},
				{Kind: core.RelicEditSetProperty, Name: "s", Value: text("t")},
				{Kind: core.RelicEditTitle, Title: "T"},
			}},
		},
		{
			name: "each group opener starts a new group, the same one twice too",
			args: []string{"WB-R-X", "--add-part", "--part-type", "a", "--add-part", "--part-type", "b", "--after", "P1",
				"--part", "P1", "--field", "a=b", "--part", "P1", "--clear-field", "a"},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{
				{Kind: core.RelicEditAddPart, PartType: "a"},
				{Kind: core.RelicEditAddPart, PartType: "b", After: "P1"},
				{Kind: core.RelicEditPart, Part: "P1", Fields: []core.RelicFieldEdit{{Name: "a", Value: text("b")}}},
				{Kind: core.RelicEditPart, Part: "P1", Fields: []core.RelicFieldEdit{{Name: "a", Clear: true}}},
			}},
		},
		{
			name: "the body format is the file ending unless --body-format is given, before or after the file",
			args: []string{"WB-R-X",
				"--add-part", "--part-type", "a", "--body-file", "Notes.MD",
				"--add-part", "--part-type", "a", "--body-file", "plain",
				"--add-part", "--part-type", "a", "--body-file", "notes.md", "--body-format", "rst",
				"--add-part", "--part-type", "a", "--body-format", "txt", "--body-file", "draft.my_notes",
				"--part", "P1", "--body-file", "notes.md", "--body-format", ""},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{
				{Kind: core.RelicEditAddPart, PartType: "a", Body: &core.RelicBodyInput{Format: "md", Content: []byte("caps\n")}},
				{Kind: core.RelicEditAddPart, PartType: "a", Body: &core.RelicBodyInput{Content: []byte("plain\n")}},
				{Kind: core.RelicEditAddPart, PartType: "a", Body: &core.RelicBodyInput{Format: "rst", Content: []byte("# Notes\n")}},
				{Kind: core.RelicEditAddPart, PartType: "a", Body: &core.RelicBodyInput{Format: "txt", Content: []byte("draft\n")}},
				{Kind: core.RelicEditPart, Part: "P1", Body: &core.RelicBodyInput{Content: []byte("# Notes\n")}},
			}},
		},
		{
			name: "a --part group passes the file's name and no format, unless --body-format is given",
			args: []string{"WB-R-X",
				"--part", "P1", "--body-file", "notes.md",
				"--part", "P2", "--body-file", "draft.my_notes",
				"--part", "P3", "--body-file", "some/where/Notes.MD",
				"--part", "P4", "--body-file", "notes.md", "--body-format", "txt"},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{
				{Kind: core.RelicEditPart, Part: "P1", BodyFile: "notes.md", Body: &core.RelicBodyInput{Content: []byte("# Notes\n")}},
				{Kind: core.RelicEditPart, Part: "P2", BodyFile: "draft.my_notes", Body: &core.RelicBodyInput{Content: []byte("draft\n")}},
				{Kind: core.RelicEditPart, Part: "P3", BodyFile: "Notes.MD", Body: &core.RelicBodyInput{Content: []byte("deep\n")}},
				{Kind: core.RelicEditPart, Part: "P4", Body: &core.RelicBodyInput{Format: "txt", Content: []byte("# Notes\n")}},
			}},
		},
		{
			name: "a part address gives its part, and its relic to check",
			args: []string{"WB-R-X", "--part", "wb-r-x#P1", "--field", "a=b", "--part", "P2", "--field", "a=b"},
			want: relicUpdateRequest{Relic: "WB-R-X", PartRelics: []string{"wb-r-x"}, Edits: []core.RelicEdit{
				{Kind: core.RelicEditPart, Part: "P1", Fields: []core.RelicFieldEdit{{Name: "a", Value: text("b")}}},
				{Kind: core.RelicEditPart, Part: "P2", Fields: []core.RelicFieldEdit{{Name: "a", Value: text("b")}}},
			}},
		},
		{
			name: "--json takes a truth value, and a bare -- ends the options",
			args: []string{"WB-R-X", "--json", "--title", "T", "--json=false", "--"},
			want: relicUpdateRequest{Relic: "WB-R-X", Edits: []core.RelicEdit{{Kind: core.RelicEditTitle, Title: "T"}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, err := parseRelicUpdate(t, files, test.args...)
			if err != nil {
				t.Fatalf("parseRelicUpdateArgs(%q) error = %v", test.args, err)
			}
			if !reflect.DeepEqual(request, test.want) {
				t.Fatalf("parseRelicUpdateArgs(%q)\n   = %+v\nwant %+v", test.args, request, test.want)
			}
		})
	}
}

func TestParseRelicUpdateArgsRefusals(t *testing.T) {
	t.Parallel()
	files := map[string]string{"notes.md": "# Notes\n", "draft.my_notes": "draft\n"}
	for _, test := range []struct {
		name     string
		args     []string
		category core.Category
		// Every string in want is in the message.
		want []string
	}{
		{"no relic", nil, core.CategoryInvocation, []string{"<relic>", "relic update"}},
		{"an option where the relic goes", []string{"--title", "T"}, core.CategoryInvocation, []string{"<relic>"}},
		{"no change at all", []string{"WB-R-X"}, core.CategoryInvocation, []string{"relic update needs at least one change"}},
		{"only --json", []string{"WB-R-X", "--json"}, core.CategoryInvocation, []string{"relic update needs at least one change"}},
		{"a part option before any group", []string{"WB-R-X", "--field", "a=b"}, core.CategoryInvocation,
			[]string{"--field", "--add-part or --part"}},
		{"--part-type before any group", []string{"WB-R-X", "--part-type", "section"}, core.CategoryInvocation,
			[]string{"--part-type", "--add-part"}},
		{"--before before any group", []string{"WB-R-X", "--before", "P"}, core.CategoryInvocation,
			[]string{"--before", "--add-part or --move-part"}},
		{"--base in an add-part group", []string{"WB-R-X", "--add-part", "--part-type", "section", "--base", "V"},
			core.CategoryInvocation, []string{"--base", "--add-part", "--part"}},
		{"--clear-field in an add-part group", []string{"WB-R-X", "--add-part", "--part-type", "section", "--clear-field", "a"},
			core.CategoryInvocation, []string{"--clear-field", "--add-part"}},
		{"--before in a --part group", []string{"WB-R-X", "--part", "P", "--before", "Q"}, core.CategoryInvocation,
			[]string{"--before", "--part", "--add-part or --move-part"}},
		{"--part-type in a --part group", []string{"WB-R-X", "--part", "P", "--part-type", "section"}, core.CategoryInvocation,
			[]string{"--part-type", "--part"}},
		{"--field in a --move-part group", []string{"WB-R-X", "--move-part", "P", "--before", "Q", "--field", "a=b"},
			core.CategoryInvocation, []string{"--field", "--move-part"}},
		{"--field in a --remove-part group", []string{"WB-R-X", "--remove-part", "P", "--field", "a=b"}, core.CategoryInvocation,
			[]string{"--field", "--remove-part"}},
		{"--after in a --restore-part group", []string{"WB-R-X", "--restore-part", "P", "--after", "Q"}, core.CategoryInvocation,
			[]string{"--after", "--restore-part"}},
		{"--part-type missing", []string{"WB-R-X", "--add-part", "--field", "a=b"}, core.CategoryInvocation,
			[]string{"--add-part", "--part-type"}},
		{"--part-type missing from the first of two groups", []string{"WB-R-X", "--add-part", "--add-part", "--part-type", "a"},
			core.CategoryInvocation, []string{"--add-part", "--part-type"}},
		{"--body-format without --body-file", []string{"WB-R-X", "--part", "P", "--field", "a=b", "--body-format", "md"},
			core.CategoryInvocation, []string{"--body-format", "--body-file"}},
		{"--body-format whose --body-file is in another group",
			[]string{"WB-R-X", "--part", "P", "--body-file", "notes.md", "--part", "Q", "--field", "a=b", "--body-format", "md"},
			core.CategoryInvocation, []string{"--body-format", "--body-file"}},
		{"--base without --body-file", []string{"WB-R-X", "--part", "P", "--field", "a=b", "--base", "V"},
			core.CategoryInvocation, []string{"--base", "--body-file"}},
		{"--part alone", []string{"WB-R-X", "--part", "P"}, core.CategoryInvocation, []string{"--part", "--field"}},
		{"--part with only an option of the relic", []string{"WB-R-X", "--part", "P", "--title", "T"}, core.CategoryInvocation,
			[]string{"--part", "--field"}},
		{"--move-part with nowhere to go", []string{"WB-R-X", "--move-part", "P"}, core.CategoryInvocation,
			[]string{"--move-part", "--before", "--after"}},
		{"--move-part with both anchors", []string{"WB-R-X", "--move-part", "P1", "--before", "P2", "--after", "P3"},
			core.CategoryInvocation, []string{"--move-part", "--before or --after, not both"}},
		{"--add-part with both anchors", []string{"WB-R-X", "--add-part", "--part-type", "a", "--after", "P3", "--before", "P2"},
			core.CategoryInvocation, []string{"--add-part", "--before or --after, not both"}},
		{"--set with no equals sign", []string{"WB-R-X", "--set", "novalue"}, core.CategoryInvocation,
			[]string{"--set", "<name>=<value>"}},
		{"--set-json with no equals sign", []string{"WB-R-X", "--set-json", "novalue"}, core.CategoryInvocation,
			[]string{"--set-json", "<name>=<json>"}},
		{"--field with no equals sign", []string{"WB-R-X", "--part", "P", "--field", "novalue"}, core.CategoryInvocation,
			[]string{"--field", "<name>=<value>"}},
		{"--field-json with no equals sign", []string{"WB-R-X", "--part", "P", "--field-json", "novalue"}, core.CategoryInvocation,
			[]string{"--field-json", "<name>=<json>"}},
		{"--title twice", []string{"WB-R-X", "--title", "A", "--title", "B"}, core.CategoryInvocation, []string{"--title", "once"}},
		{"--part-type twice in a group", []string{"WB-R-X", "--add-part", "--part-type", "a", "--part-type", "b"},
			core.CategoryInvocation, []string{"--part-type", "once", "--add-part"}},
		{"--body-file twice in a group", []string{"WB-R-X", "--part", "P", "--body-file", "notes.md", "--body-file", "notes.md"},
			core.CategoryInvocation, []string{"--body-file", "once", "--part"}},
		{"--before twice in a group", []string{"WB-R-X", "--move-part", "P", "--before", "A", "--before", "B"},
			core.CategoryInvocation, []string{"--before", "once", "--move-part"}},
		{"an empty --before", []string{"WB-R-X", "--add-part", "--part-type", "a", "--before", ""}, core.CategoryInvocation,
			[]string{"--before", "part"}},
		{"an empty --after", []string{"WB-R-X", "--move-part", "P", "--after="}, core.CategoryInvocation,
			[]string{"--after", "part"}},
		{"an empty --body-file", []string{"WB-R-X", "--part", "P", "--body-file", ""}, core.CategoryInvocation,
			[]string{"--body-file", "file"}},
		{"standard input as a body file", []string{"WB-R-X", "--part", "P", "--body-file", "-"}, core.CategoryInvocation,
			[]string{"--body-file", "standard input"}},
		{"an unknown option", []string{"WB-R-X", "--title", "T", "--bogus"}, core.CategoryInvocation,
			[]string{"relic update has no option", `"--bogus"`}},
		{"an option with its value missing", []string{"WB-R-X", "--title"}, core.CategoryInvocation,
			[]string{"relic update --title requires a value"}},
		{"a group opener with its value missing", []string{"WB-R-X", "--title", "T", "--part"}, core.CategoryInvocation,
			[]string{"relic update --part requires a value"}},
		{"--json with a value that is not a truth value", []string{"WB-R-X", "--title", "T", "--json=maybe"},
			core.CategoryInvocation, []string{"--json", "true or false"}},
		{"--add-part with a value", []string{"WB-R-X", "--add-part=section"},
			core.CategoryInvocation, []string{"--add-part takes no value", "--part-type"}},
		{"--add-part with a truth value", []string{"WB-R-X", "--add-part=true", "--part-type", "section"},
			core.CategoryInvocation, []string{"--add-part takes no value", "--part-type"}},
		{"a second positional argument", []string{"WB-R-X", "--title", "T", "extra"}, core.CategoryInvocation,
			[]string{"relic update", "positional"}},
		{"an argument after --", []string{"WB-R-X", "--title", "T", "--", "--json"}, core.CategoryInvocation,
			[]string{"relic update", "positional"}},

		{"--set-json that is not JSON", []string{"WB-R-X", "--set-json", "a={"}, core.CategoryValidation,
			[]string{"--set-json", `"a"`, "not valid JSON"}},
		{"--set-json holding two values", []string{"WB-R-X", "--set-json", "a=1 2"}, core.CategoryValidation,
			[]string{"--set-json", "exactly one JSON value"}},
		{"--field-json that is not JSON", []string{"WB-R-X", "--part", "P", "--field-json", "a=nope"}, core.CategoryValidation,
			[]string{"--field-json", `"a"`, "not valid JSON"}},
		{"--set text that is not UTF-8", []string{"WB-R-X", "--set", "a=\xff"}, core.CategoryValidation,
			[]string{"--set", `"a"`, "UTF-8"}},
		{"--field text that is not UTF-8", []string{"WB-R-X", "--part", "P", "--field", "a=\xff"}, core.CategoryValidation,
			[]string{"--field", `"a"`, "UTF-8"}},
		{"a part address with no part", []string{"WB-R-X", "--part", "WB-R-X#", "--field", "a=b"}, core.CategoryValidation,
			[]string{"--part", "<relic>#<part>"}},
		{"a part address with no relic", []string{"WB-R-X", "--part", "#P", "--field", "a=b"}, core.CategoryValidation,
			[]string{"--part", "<relic>#<part>"}},
		{"a part address with two parts", []string{"WB-R-X", "--part", "WB-R-X#P#Q", "--field", "a=b"}, core.CategoryValidation,
			[]string{"--part", "<relic>#<part>"}},
		{"a new part's body file whose ending is no format label",
			[]string{"WB-R-X", "--add-part", "--part-type", "a", "--body-file", "draft.my_notes"},
			core.CategoryValidation, []string{"--body-format"}},
		{"a body file that is not there", []string{"WB-R-X", "--part", "P", "--body-file", "missing.md"},
			core.CategoryNotFound, []string{"missing.md"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, err := parseRelicUpdate(t, files, test.args...)
			if err == nil {
				t.Fatalf("parseRelicUpdateArgs(%q) = %+v, want it refused", test.args, request)
			}
			if got := core.CategoryOf(err); got != test.category {
				t.Fatalf("parseRelicUpdateArgs(%q) category = %q, want %q; error = %v", test.args, got, test.category, err)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("parseRelicUpdateArgs(%q) error = %q, want it to hold %q", test.args, err, want)
				}
			}
		})
	}
}

// A command line that is put together wrongly is refused as that, exit 2,
// before any value on it is judged and before any file is read.
func TestParseRelicUpdateArgsChecksTheCommandLineBeforeItsValues(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"WB-R-X", "--set-json", "a={", "--bogus"},
		{"WB-R-X", "--part", "P", "--body-file", "missing.md", "--before", "Q"},
		{"WB-R-X", "--part", "P", "--body-file", "notes.md", "--add-part"},
		{"WB-R-X", "--part", "WB-R-X#", "--field", "a=b", "--move-part", "Q"},
		{"WB-R-X", "--part", "P", "--body-file", "notes.md", "--set", "novalue"},
	} {
		readFile, reads := relicBodyFiles(map[string]string{"notes.md": "# Notes\n"})
		_, err := parseRelicUpdateArgs(args, readFile)
		if core.CategoryOf(err) != core.CategoryInvocation {
			t.Errorf("parseRelicUpdateArgs(%q) error = %v, want an invocation error", args, err)
		}
		if *reads != 0 {
			t.Errorf("parseRelicUpdateArgs(%q) read %d files before refusing the command line", args, *reads)
		}
	}
}

func TestParseRelicUpdateArgsNeverEchoesAnOversizedValue(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", 100_000)
	for _, args := range [][]string{
		{"WB-R-X", "--set", huge},
		{"WB-R-X", "--set-json", "a={" + huge},
		{"WB-R-X", "--set-json", huge + "={"},
		{"WB-R-X", "--set", huge + "=\xff"},
		{"WB-R-X", "--part", "P", "--field", huge},
		{"WB-R-X", "--part", "P", "--field-json", huge + "=nope"},
		{"WB-R-X", "--title", "T", huge},
		{"WB-R-X", "--part", huge + "#" + huge, "--field", "a=b", "--base", "V"},
		{"WB-R-X", "--part", huge + "#", "--field", "a=b"},
		{"WB-R-X", "--title", "T", "--" + huge},
		{"WB-R-X", "--title", "T", "--json=" + huge},
		// What the relic would repeat in saying that it matches nothing.
		{huge, "--title", "T"},
		{"WB-R-X", "--part", huge, "--field", "a=b"},
		{"WB-R-X", "--move-part", huge, "--before", "P"},
		{"WB-R-X", "--move-part", "P", "--after", huge},
		{"WB-R-X", "--add-part", "--part-type", "note", "--before", huge},
		{"WB-R-X", "--remove-part", huge},
		{"WB-R-X", "--restore-part", huge},
		{"WB-R-X", "--part", "P", "--body-file", "notes.md", "--base", huge},
		{"WB-R-X", "--part", "P", "--body-file", huge + ".my_notes"},
		{"WB-R-X", "--part", "P", "--body-file", huge},
		{"WB-R-X", "--add-part", "--part-type", huge, "--base", huge},
	} {
		_, err := parseRelicUpdate(t, map[string]string{"notes.md": "# Notes\n"}, args...)
		if err == nil {
			t.Fatalf("parseRelicUpdateArgs(a %d-byte value) was accepted", len(huge))
		}
		if category := core.CategoryOf(err); category != core.CategoryInvocation && category != core.CategoryValidation {
			t.Errorf("a refusal of a %d-byte value has category %q: %.200s", len(huge), category, err.Error())
		}
		if len(err.Error()) > 400 {
			t.Errorf("a refusal is %d bytes long, so it echoes the value: %.200s…", len(err.Error()), err.Error())
		}
	}
}

func relicRef(id string) string {
	return "refs/workbook/relics/" + id
}

func relicHeadOf(t *testing.T, repository, id string) string {
	t.Helper()
	return gitOutput(t, repository, "rev-parse", "--verify", relicRef(id))
}

func relicCommitCount(t *testing.T, repository, id string) int {
	t.Helper()
	count, err := strconv.Atoi(gitOutput(t, repository, "rev-list", "--count", relicRef(id)))
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func mustUpdateRelic(t *testing.T, repository, id string, args ...string) core.RelicMutationResult {
	t.Helper()
	var updated core.RelicMutationResult
	mustRelic(t, repository, &updated, append([]string{"update", id}, args...)...)
	if head := relicHeadOf(t, repository, updated.Relic.ID); head != updated.Relic.Head {
		t.Fatalf("relic update head = %q, the ref is at %q", updated.Relic.Head, head)
	}
	return updated
}

func shownRelic(t *testing.T, repository, address string, options ...string) core.RelicDocument {
	t.Helper()
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, append([]string{"show", address}, options...)...)
	return shown
}

func relicPartIDs(parts []core.RelicDocumentPart) []string {
	ids := make([]string, len(parts))
	for index, part := range parts {
		ids[index] = part.ID
	}
	return ids
}

// relicBodyText runs `relic show <relic>#<part> --body`, which writes the body
// byte for byte.
func relicBodyText(t *testing.T, repository, id, part string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "relic", "show", id+"#"+part, "--body")
	if code != 0 || stderr != "" {
		t.Fatalf("relic show %s#%s --body = (%d, %q, %q)", id, part, code, stdout, stderr)
	}
	return stdout
}

func writeRelicBodyFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertRelicHistoriesValid runs `workbook validate --json` and requires every
// one of the repository's relics to pass, commit by commit.
func assertRelicHistoriesValid(t *testing.T, repository string, relicCount int) {
	t.Helper()
	code, _, relics, stderr := relicValidation(t, repository)
	if code != 0 || relics == nil || relics.RelicCount != relicCount || relics.Valid != relicCount ||
		relics.Invalid != 0 || relics.NewerWriter != 0 || len(relics.Failures) != 0 {
		t.Fatalf("validate --json = (%d, relics %+v), want %d valid relic(s) and no failure; stderr = %q",
			code, relics, relicCount, stderr)
	}
}

// addRelicParts adds one bodiless part per type, one command each, and returns
// their IDs in order.
func addRelicParts(t *testing.T, repository, id string, types ...string) []string {
	t.Helper()
	ids := make([]string, len(types))
	for index, partType := range types {
		added := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", partType)
		if !added.Changed || len(added.Parts) != 1 {
			t.Fatalf("relic update --add-part = %+v, want one added part", added)
		}
		ids[index] = added.Parts[0].ID
	}
	return ids
}

func TestRelicSetStoresTextAndSetJSONStoresValues(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	updated := mustUpdateRelic(t, repository, id,
		"--set", "version=1.0", "--set-json", "count=3", "--set-json", `tags=["a","b"]`, "--set", "draft=true")
	if !updated.Changed || len(updated.Parts) != 0 {
		t.Fatalf("relic update = %+v, want a change that names no part", updated)
	}
	properties := shownRelic(t, repository, id).Properties
	if got, want := mustJSON(t, properties), `{"count":3,"draft":"true","tags":["a","b"],"version":"1.0"}`; got != want {
		t.Fatalf("properties = %s, want %s", got, want)
	}

	mustUpdateRelic(t, repository, id, "--clear", "draft", "--set-json", "version=2")
	properties = shownRelic(t, repository, id).Properties
	if got, want := mustJSON(t, properties), `{"count":3,"tags":["a","b"],"version":2}`; got != want {
		t.Fatalf("properties after --clear = %s, want %s", got, want)
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateEditsPartByPart(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
			notes := writeRelicBodyFile(t, "notes.md", "# Notes\n")

			var sections []string
			for _, heading := range []string{"Goals", "Scope", "Risks"} {
				added := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "section", "--field", "heading="+heading)
				if !added.Changed || len(added.Parts) != 1 {
					t.Fatalf("relic update --add-part = %+v, want one added part", added)
				}
				part := added.Parts[0]
				if core.ValidatePartID(part.ID) != nil || part.Type != "section" || part.Body != nil ||
					mustJSON(t, part.Fields) != mustJSON(t, map[string]string{"heading": heading}) {
					t.Fatalf("added part = %+v, want a bodiless section headed %q", part, heading)
				}
				sections = append(sections, part.ID)
			}

			both := mustUpdateRelic(t, repository, id,
				"--add-part", "--part-type", "decision", "--field-json", "votes=[1,2]",
				"--add-part", "--part-type", "note", "--body-file", notes)
			if len(both.Parts) != 2 || both.Parts[0].Type != "decision" || both.Parts[1].Type != "note" {
				t.Fatalf("two --add-part groups reported parts %+v, want the decision then the note", both.Parts)
			}
			decision, note := both.Parts[0], both.Parts[1]
			if decision.Body != nil || mustJSON(t, decision.Fields) != `{"votes":[1,2]}` {
				t.Fatalf("added decision = %+v", decision)
			}
			// The first body of a part added with one has the part's ID as its version.
			if note.Body == nil || note.Body.Version != note.ID || note.Body.Format != "md" || note.Body.Bytes != 8 {
				t.Fatalf("added note body = %+v, want version %s, format md, 8 bytes", note.Body, note.ID)
			}

			mustUpdateRelic(t, repository, id, "--part", sections[0], "--field", "owner=dylan", "--field-json", "weight=3")
			cleared := mustUpdateRelic(t, repository, id, "--part", sections[0], "--clear-field", "owner")
			if len(cleared.Parts) != 1 || cleared.Parts[0].ID != sections[0] ||
				mustJSON(t, cleared.Parts[0].Fields) != `{"heading":"Goals","weight":3}` {
				t.Fatalf("relic update --clear-field parts = %+v, want the edited part with its fields", cleared.Parts)
			}

			saved := mustUpdateRelic(t, repository, id, "--part", sections[1], "--body-file", notes)
			if len(saved.Parts) != 1 || saved.Parts[0].Body == nil || saved.Parts[0].Body.Format != "md" ||
				core.ValidatePartID(saved.Parts[0].Body.Version) != nil || saved.Parts[0].Body.Version == sections[1] {
				t.Fatalf("relic update --body-file parts = %+v, want the part with a new md body version", saved.Parts)
			}
			scopeVersion := saved.Parts[0].Body.Version

			// One command that adds a part with a field and a body after another
			// part, and edits a field of a part the relic already had.
			placed := mustUpdateRelic(t, repository, id,
				"--add-part", "--part-type", "note", "--field", "heading=Between", "--body-file", notes, "--after", sections[0],
				"--part", sections[2], "--field", "status=open")
			if len(placed.Parts) != 2 || placed.Parts[1].ID != sections[2] ||
				mustJSON(t, placed.Parts[1].Fields) != `{"heading":"Risks","status":"open"}` {
				t.Fatalf("add and edit in one command reported parts %+v", placed.Parts)
			}
			between := placed.Parts[0]
			if between.Body == nil || between.Body.Version != between.ID || mustJSON(t, between.Fields) != `{"heading":"Between"}` {
				t.Fatalf("part added between = %+v", between)
			}
			assertRelicHistoriesValid(t, repository, 1)

			// A later command edits a field of the part that command added.
			mustUpdateRelic(t, repository, id, "--part", between.ID, "--field", "heading=Moved in", "--field-json", "pinned=true")
			assertRelicHistoriesValid(t, repository, 1)

			shown := shownRelic(t, repository, id, "--body")
			type part struct{ id, partType, fields, format, version, text string }
			want := []part{
				{sections[0], "section", `{"heading":"Goals","weight":3}`, "", "", ""},
				{between.ID, "note", `{"heading":"Moved in","pinned":true}`, "md", between.ID, "# Notes\n"},
				{sections[1], "section", `{"heading":"Scope"}`, "md", scopeVersion, "# Notes\n"},
				{sections[2], "section", `{"heading":"Risks","status":"open"}`, "", "", ""},
				{decision.ID, "decision", `{"votes":[1,2]}`, "", "", ""},
				{note.ID, "note", `{}`, "md", note.ID, "# Notes\n"},
			}
			if len(shown.Parts) != len(want) {
				t.Fatalf("show parts = %q, want %d parts", relicPartIDs(shown.Parts), len(want))
			}
			for index, wanted := range want {
				got := part{id: shown.Parts[index].ID, partType: shown.Parts[index].Type, fields: mustJSON(t, shown.Parts[index].Fields)}
				if body := shown.Parts[index].Body; body != nil {
					got.format, got.version = body.Format, body.Version
					if body.Text != nil {
						got.text = *body.Text
					}
				}
				if got != wanted {
					t.Errorf("show part %d = %+v, want %+v", index, got, wanted)
				}
			}
		})
	}
}

func TestRelicUpdateTextOutput(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	notes := writeRelicBodyFile(t, "notes.md", "# Notes\n")
	code, stdout, stderr := run(t, repository, "relic", "update", id, "--title", "Archive plan, revised",
		"--add-part", "--part-type", "note", "--body-file", notes, "--add-part", "--part-type", "decision", "--no-sync")
	if code != 0 || stderr != "" {
		t.Fatalf("relic update = (%d, %q, %q)", code, stdout, stderr)
	}
	const ulid = `[0-7][0-9A-HJKMNP-TV-Z]{25}`
	want := regexp.MustCompile(`^` + regexp.QuoteMeta(id) + "\tspec\tArchive plan, revised\n" +
		"part\t(" + ulid + ")\tnote\t(" + ulid + ")\n" +
		"part\t" + ulid + "\tdecision\t-\n$")
	match := want.FindStringSubmatch(stdout)
	if match == nil || match[1] != match[2] {
		t.Fatalf("relic update output = %q, want the relic line and one line per added part", stdout)
	}
}

func TestRelicBodyEditNeedsTheCurrentVersion(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	first := writeRelicBodyFile(t, "first.md", "the first text\n")
	second := writeRelicBodyFile(t, "second.md", "the second text\n")
	third := writeRelicBodyFile(t, "third.md", "the third text\n")
	part := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "section", "--body-file", first).Parts[0].ID

	shown := shownRelic(t, repository, id+"#"+part)
	if shown.Parts[0].Body == nil || shown.Parts[0].Body.Version != part {
		t.Fatalf("show body = %+v, want version %s", shown.Parts[0].Body, part)
	}
	v1 := shown.Parts[0].Body.Version

	saved := mustUpdateRelic(t, repository, id, "--part", part, "--body-file", second, "--base", v1)
	if !saved.Changed || len(saved.Parts) != 1 || saved.Parts[0].Body == nil {
		t.Fatalf("save from the current version = %+v", saved)
	}
	v2 := saved.Parts[0].Body.Version
	if v2 == v1 || core.ValidatePartID(v2) != nil {
		t.Fatalf("saved body version = %q, want a new version after %q", v2, v1)
	}
	assertRelicHistoriesValid(t, repository, 1)
	head := relicHeadOf(t, repository, id)

	// A save from the version the body has moved past writes nothing.
	message := relicErrorMessage(t, repository, 6, core.CategoryStaleWrite,
		"update", id, "--part", part, "--body-file", third, "--base", v1)
	if !strings.Contains(message, v2) || !strings.Contains(message, "relic show "+id+"#"+part) {
		t.Fatalf("stale save message = %q, want the current version %s and how to read it", message, v2)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a stale save moved the relic from %s to %s", head, after)
	}
	if text := relicBodyText(t, repository, id, part); text != "the second text\n" {
		t.Fatalf("body after a stale save = %q, want the second text", text)
	}

	// So does a save that names no version at all, and one command with a good
	// change beside the stale save.
	message = relicErrorMessage(t, repository, 6, core.CategoryStaleWrite, "update", id, "--part", part, "--body-file", third)
	if !strings.Contains(message, "--base "+v2) {
		t.Fatalf("save with no base message = %q, want --base %s", message, v2)
	}
	relicErrorMessage(t, repository, 6, core.CategoryStaleWrite,
		"update", id, "--title", "Never recorded", "--part", part, "--body-file", third, "--base", v1)
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused save moved the relic from %s to %s", head, after)
	}
	if text := relicBodyText(t, repository, id, part); text != "the second text\n" {
		t.Fatalf("body after refused saves = %q, want the second text", text)
	}
	if title := shownRelic(t, repository, id).Title; title != "Archive plan" {
		t.Fatalf("title after a refused update = %q, want it unchanged", title)
	}

	again := mustUpdateRelic(t, repository, id, "--part", part, "--body-file", third, "--base", v2)
	if v3 := again.Parts[0].Body.Version; v3 == v2 || v3 == v1 {
		t.Fatalf("third body version = %q, want a new one", v3)
	}
	if text := relicBodyText(t, repository, id, part); text != "the third text\n" {
		t.Fatalf("body = %q, want the third text", text)
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateReordersParts(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	parts := addRelicParts(t, repository, id, "section", "section", "section")
	a, b, c := parts[0], parts[1], parts[2]

	moved := mustUpdateRelic(t, repository, id, "--move-part", c, "--before", a)
	if !moved.Changed {
		t.Fatalf("relic update --move-part = %+v, want a change", moved)
	}
	if got := relicPartIDs(shownRelic(t, repository, id).Parts); !reflect.DeepEqual(got, []string{c, a, b}) {
		t.Fatalf("parts after --move-part C --before A = %q, want %q", got, []string{c, a, b})
	}
	assertRelicHistoriesValid(t, repository, 1)

	added := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "note", "--after", a).Parts[0].ID
	if got := relicPartIDs(shownRelic(t, repository, id).Parts); !reflect.DeepEqual(got, []string{c, a, added, b}) {
		t.Fatalf("parts after --add-part --after A = %q, want %q", got, []string{c, a, added, b})
	}

	head := relicHeadOf(t, repository, id)
	message := relicErrorMessage(t, repository, 2, core.CategoryInvocation, "update", id, "--move-part", a)
	if !strings.Contains(message, "--before") || !strings.Contains(message, "--after") {
		t.Fatalf("--move-part with nowhere to go message = %q, want --before and --after", message)
	}
	// Both anchors is a mistake in the command line, as no anchor is.
	for _, args := range [][]string{
		{"--add-part", "--part-type", "note", "--before", a, "--after", b},
		{"--move-part", c, "--before", a, "--after", b},
	} {
		message := relicErrorMessage(t, repository, 2, core.CategoryInvocation, append([]string{"update", id}, args...)...)
		if !strings.Contains(message, "--before or --after, not both") {
			t.Fatalf("%v message = %q, want it to say to give one anchor", args, message)
		}
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}

	// A move back, by a prefix of each part ID in lower case.
	mustUpdateRelic(t, repository, id, "--move-part", strings.ToLower(c), "--after", strings.ToLower(b))
	if got := relicPartIDs(shownRelic(t, repository, id).Parts); !reflect.DeepEqual(got, []string{a, added, b, c}) {
		t.Fatalf("parts after --move-part C --after B = %q, want %q", got, []string{a, added, b, c})
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateRemoveAndRestorePart(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	notes := writeRelicBodyFile(t, "notes.md", "# Notes\n")
	kept := addRelicParts(t, repository, id, "section")[0]
	part := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "note", "--field", "heading=Gone", "--body-file", notes).Parts[0].ID

	removed := mustUpdateRelic(t, repository, id, "--remove-part", part)
	if !removed.Changed || len(removed.Parts) != 0 || removed.Relic.Parts != 1 {
		t.Fatalf("relic update --remove-part = %+v, want a change that leaves one live part", removed)
	}
	if got := relicPartIDs(shownRelic(t, repository, id).Parts); !reflect.DeepEqual(got, []string{kept}) {
		t.Fatalf("parts after --remove-part = %q, want only %q", got, kept)
	}
	all := shownRelic(t, repository, id, "--removed").Parts
	if len(all) != 2 || all[1].ID != part || !all[1].Removed || all[0].Removed {
		t.Fatalf("show --removed parts = %+v, want the removed part marked", all)
	}
	assertRelicHistoriesValid(t, repository, 1)

	head := relicHeadOf(t, repository, id)
	for _, args := range [][]string{
		{"update", id, "--part", part, "--field", "a=b"},
		{"update", id, "--move-part", part, "--before", kept},
		{"update", id, "--add-part", "--part-type", "note", "--after", part},
	} {
		message := relicErrorMessage(t, repository, 5, core.CategoryValidation, args...)
		if !strings.Contains(message, "--restore-part "+part) {
			t.Errorf("%s message = %q, want it to name --restore-part", strings.Join(args, " "), message)
		}
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}

	restored := mustUpdateRelic(t, repository, id, "--restore-part", part)
	if !restored.Changed || restored.Relic.Parts != 2 {
		t.Fatalf("relic update --restore-part = %+v, want two live parts", restored)
	}
	back := shownRelic(t, repository, id, "--body").Parts
	if len(back) != 2 || back[1].ID != part || back[1].Removed || back[1].Body == nil || back[1].Body.Version != part ||
		back[1].Body.Text == nil || *back[1].Body.Text != "# Notes\n" || mustJSON(t, back[1].Fields) != `{"heading":"Gone"}` {
		t.Fatalf("parts after --restore-part = %+v, want the part back with its ID, fields and body version", back)
	}
	assertRelicHistoriesValid(t, repository, 1)

	// Removing and restoring in one command, in that order, comes out even and
	// is still recorded; a part can be edited in the command that restores it.
	mustUpdateRelic(t, repository, id, "--remove-part", part, "--restore-part", part, "--part", part, "--field", "heading=Back")
	if fields := shownRelic(t, repository, id+"#"+part).Parts[0].Fields; mustJSON(t, fields) != `{"heading":"Back"}` {
		t.Fatalf("fields after remove, restore and edit in one command = %s", mustJSON(t, fields))
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateSeveralChangesAreOneCommit(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	part := addRelicParts(t, repository, id, "section")[0]
	notes := writeRelicBodyFile(t, "notes.md", "# Notes\n")
	before, head := relicCommitCount(t, repository, id), relicHeadOf(t, repository, id)

	updated := mustUpdateRelic(t, repository, id, "--title", "Archive plan, revised", "--set", "status=approved",
		"--part", part, "--field", "heading=Goals", "--body-file", notes,
		"--add-part", "--part-type", "decision", "--field", "outcome=accepted", "--before", part)
	if got := relicCommitCount(t, repository, id); got != before+1 {
		t.Fatalf("one update added %d commits, want 1", got-before)
	}
	if parent := gitOutput(t, repository, "rev-parse", updated.Relic.Head+"^"); parent != head {
		t.Fatalf("the update's parent = %s, want the head before it, %s", parent, head)
	}
	if len(updated.Parts) != 2 || updated.Parts[0].ID != part || updated.Parts[1].Type != "decision" ||
		updated.Relic.Title != "Archive plan, revised" || updated.Relic.Parts != 2 {
		t.Fatalf("update = %+v, want the edited part then the added one", updated)
	}
	shown := shownRelic(t, repository, id)
	if got := relicPartIDs(shown.Parts); !reflect.DeepEqual(got, []string{updated.Parts[1].ID, part}) ||
		mustJSON(t, shown.Properties) != `{"status":"approved"}` {
		t.Fatalf("show = parts %q properties %s", got, mustJSON(t, shown.Properties))
	}

	// One refused change refuses them all.
	before, head = relicCommitCount(t, repository, id), relicHeadOf(t, repository, id)
	message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "update", id, "--title", "Never recorded",
		"--set", "status=rejected", "--add-part", "--part-type", "note", "--part", "7ZZZZZZZZZ", "--field", "a=b")
	if !strings.Contains(message, "no part of "+id) {
		t.Fatalf("unknown part message = %q, want no part of the relic", message)
	}
	if got, after := relicCommitCount(t, repository, id), relicHeadOf(t, repository, id); got != before || after != head {
		t.Fatalf("a refused update left %d commits at %s, want %d at %s", got, after, before, head)
	}
	if shown := shownRelic(t, repository, id); shown.Title != "Archive plan, revised" || len(shown.Parts) != 2 ||
		mustJSON(t, shown.Properties) != `{"status":"approved"}` {
		t.Fatalf("relic after a refused update = %+v, want it unchanged", shown)
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateThatChangesNothing(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	part := addRelicParts(t, repository, id, "section")[0]
	args := []string{"--title", "Archive plan", "--set", "status=approved", "--part", part, "--field", "heading=Goals"}
	if first := mustUpdateRelic(t, repository, id, args...); !first.Changed {
		t.Fatalf("first update = %+v, want a change", first)
	}
	count, head := relicCommitCount(t, repository, id), relicHeadOf(t, repository, id)

	repeated := mustUpdateRelic(t, repository, id, args...)
	if repeated.Changed || len(repeated.Parts) != 0 || repeated.Relic.Head != head {
		t.Fatalf("repeated update = %+v, want changed false at the same head", repeated)
	}
	code, stdout, stderr := run(t, repository, append(append([]string{"relic", "update", id}, args...), "--no-sync")...)
	if code != 0 || stderr != "" || stdout != "No change: "+id+"\n" {
		t.Fatalf("repeated update in text = (%d, %q, %q), want No change", code, stdout, stderr)
	}
	if got, after := relicCommitCount(t, repository, id), relicHeadOf(t, repository, id); got != count || after != head {
		t.Fatalf("repeated updates left %d commits at %s, want %d at %s", got, after, count, head)
	}
}

func TestRelicUpdateRefusesADeletedRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	mustRelic(t, repository, nil, "delete", id)
	head := relicHeadOf(t, repository, id)

	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", id, "--title", "Edited")
	if !strings.Contains(message, "workbook relic restore "+id) {
		t.Fatalf("update of a deleted relic message = %q, want workbook relic restore", message)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}
	mustRelic(t, repository, nil, "restore", id)
	if updated := mustUpdateRelic(t, repository, id, "--title", "Edited"); updated.Relic.Title != "Edited" {
		t.Fatalf("update after restore = %+v", updated)
	}
}

func TestRelicUpdateRefusesANewerWritersRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	fixture := openRelicFixture(t, repository)
	tip := fixture.createRelic(t, 1, []byte("the first text\n"))
	id := tip.Record.State.RelicID
	forged := fixture.forgeNewerRelicCommit(t, tip, 700001)

	if code, stdout, stderr := run(t, repository, "relic", "show", id, "--json"); code != 0 {
		t.Fatalf("relic show of a newer writer's relic = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	message := relicErrorMessage(t, repository, 9, core.CategoryNewerWriter, "update", id, "--title", "x")
	if !strings.Contains(message, id) {
		t.Fatalf("update of a newer writer's relic message = %q, want it to name the relic", message)
	}
	if head := relicHeadOf(t, repository, id); head != forged {
		t.Fatalf("a refused update moved the relic from %s to %s", forged, head)
	}
}

func TestRelicUpdateRefusesANameGivenTwice(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	part := addRelicParts(t, repository, id, "section")[0]
	head := relicHeadOf(t, repository, id)

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--set", "status=a", "--clear", "status"}, `this update sets and clears property "status"`},
		{[]string{"--set", "status=a", "--set", "status=b"}, `this update sets property "status" twice`},
		{[]string{"--set", "status=a", "--set-json", "status=1"}, `this update sets property "status" twice`},
		{[]string{"--title", "Edited", "--clear", "status", "--clear", "status"}, `this update clears property "status" twice`},
		{[]string{"--part", part, "--field", "heading=x", "--clear-field", "heading"},
			`this update sets and clears field "heading" of part ` + part},
		{[]string{"--part", part, "--field", "heading=x", "--part", strings.ToLower(part), "--field-json", "heading=2"},
			`this update sets field "heading" of part ` + part + " twice"},
	} {
		message := relicErrorMessage(t, repository, 5, core.CategoryValidation, append([]string{"update", id}, test.args...)...)
		if !strings.Contains(message, test.want) {
			t.Errorf("relic update %q message = %q, want %q", test.args, message, test.want)
		}
		if after := relicHeadOf(t, repository, id); after != head {
			t.Fatalf("relic update %q moved the relic from %s to %s", test.args, head, after)
		}
	}

	// In text the service's message is what is printed.
	code, stdout, stderr := run(t, repository, "relic", "update", id, "--set", "status=a", "--clear", "status")
	if code != 5 || stdout != "" || !strings.Contains(stderr, `this update sets and clears property "status"; name a property once in one update`) {
		t.Fatalf("relic update in text = (%d, %q, %q), want exit 5 with the service's message", code, stdout, stderr)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}
}

func TestRelicUpdatePartAddress(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	other := mustCreateRelic(t, repository, "Another", "--type", "spec").Relic.ID
	part := addRelicParts(t, repository, id, "section")[0]

	// The relic side may be written any way that finds the same relic.
	for index, relic := range []string{id, strings.ToLower(id), id[:len(id)-1]} {
		value := fmt.Sprintf("v%d", index)
		updated := mustUpdateRelic(t, repository, id[:len(id)-2], "--part", relic+"#"+part, "--field", "a="+value)
		if !updated.Changed || len(updated.Parts) != 1 || updated.Parts[0].ID != part {
			t.Fatalf("relic update --part %s#<part> = %+v, want the part edited", relic, updated)
		}
	}

	head := relicHeadOf(t, repository, id)
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", id, "--part", other+"#"+part, "--field", "a=b")
	if !strings.Contains(message, other) || !strings.Contains(message, id) {
		t.Fatalf("--part of another relic message = %q, want both relics named", message)
	}
	message = relicErrorMessage(t, repository, 4, core.CategoryNotFound, "update", id, "--part", "WB-R-7ZZZ#"+part, "--field", "a=b")
	if !strings.Contains(message, "no relic matches") {
		t.Fatalf("--part of no relic message = %q, want no relic matches", message)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}
}

func TestRelicUpdateBodyFiles(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	directory := t.TempDir()
	write := func(name string, content []byte) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	binary := []byte{0xff, 0x00, 0xfe}

	for _, test := range []struct {
		name   string
		file   string
		format []string
		want   string
	}{
		{"an upper-case ending", write("Notes.MD", []byte("caps\n")), nil, "md"},
		{"no ending", write("plain", []byte("plain\n")), nil, ""},
		{"the last of two endings", write("archive.tar.gz", binary), nil, "gz"},
		{"an ending that is no label, with --body-format", write("draft.my_notes", []byte("draft\n")), []string{"--body-format", "txt"}, "txt"},
		{"an ending set aside for no label", write("notes.md", []byte("# Notes\n")), []string{"--body-format="}, ""},
		{"an empty file", write("empty.txt", nil), nil, "txt"},
	} {
		args := append([]string{"--add-part", "--part-type", "note", "--body-file", test.file}, test.format...)
		added := mustUpdateRelic(t, repository, id, args...)
		if len(added.Parts) != 1 || added.Parts[0].Body == nil || added.Parts[0].Body.Format != test.want {
			t.Fatalf("%s: added part = %+v, want body format %q", test.name, added.Parts, test.want)
		}
		content, err := os.ReadFile(test.file)
		if err != nil {
			t.Fatal(err)
		}
		if text := relicBodyText(t, repository, id, added.Parts[0].ID); text != string(content) {
			t.Fatalf("%s: stored body = %q, want the file's bytes %q", test.name, text, content)
		}
	}
	assertRelicHistoriesValid(t, repository, 1)

	head := relicHeadOf(t, repository, id)
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation,
		"update", id, "--add-part", "--part-type", "note", "--body-file", filepath.Join(directory, "draft.my_notes"))
	if !strings.Contains(message, "--body-format") {
		t.Fatalf("unusable file ending message = %q, want it to name --body-format", message)
	}
	message = relicErrorMessage(t, repository, 4, core.CategoryNotFound,
		"update", id, "--add-part", "--part-type", "note", "--body-file", filepath.Join(directory, "missing.md"))
	if !strings.Contains(message, "missing.md") {
		t.Fatalf("missing body file message = %q, want it to name the file", message)
	}
	message = relicErrorMessage(t, repository, 5, core.CategoryValidation,
		"update", id, "--add-part", "--part-type", "note", "--body-file", directory, "--body-format", "md")
	if !strings.Contains(message, "a directory") {
		t.Fatalf("directory as a body file message = %q, want it to say it is a directory", message)
	}
	oversized := write("oversized.md", make([]byte, core.MaxRelicBodyBytes+1))
	message = relicErrorMessage(t, repository, 5, core.CategoryValidation,
		"update", id, "--add-part", "--part-type", "note", "--body-file", oversized)
	if !strings.Contains(message, strconv.Itoa(core.MaxRelicBodyBytes+1)) || !strings.Contains(message, strconv.Itoa(core.MaxRelicBodyBytes)) {
		t.Fatalf("oversized body file message = %q, want its size and the limit", message)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}
}

// The ordinary way to edit a body is to write it to a scratch file, change it
// and save that file. The scratch file's name does not decide the body's
// label: a save over a body keeps the label it has unless --body-format asks
// for another, and the file's ending labels only a part's first body.
func TestRelicBodySaveKeepsItsFormatLabel(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	directory := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	added := mustUpdateRelic(t, repository, id,
		"--add-part", "--part-type", "section", "--body-file", write("goals.md", "# Goals\n"),
		"--add-part", "--part-type", "decision")
	goals, decision := added.Parts[0].ID, added.Parts[1].ID
	body := func(part string) *core.RelicDocumentBody {
		t.Helper()
		return shownRelic(t, repository, id+"#"+part).Parts[0].Body
	}
	save := func(part, file string, more ...string) *core.RelicDocumentBody {
		t.Helper()
		args := []string{"--part", part, "--body-file", file}
		if current := body(part); current != nil {
			args = append(args, "--base", current.Version)
		}
		saved := mustUpdateRelic(t, repository, id, append(args, more...)...)
		if !saved.Changed || len(saved.Parts) != 1 || saved.Parts[0].Body == nil {
			t.Fatalf("save of %s from %s = %+v, want the part with its new body", part, file, saved)
		}
		return saved.Parts[0].Body
	}

	// The reviewer's sequence: a name mktemp gave, a name with no ending, and
	// a .txt file, each saved over a Markdown body.
	for _, name := range []string{"tmp.QdhVaHD4", "edit", "body.txt"} {
		text := "# Goals\n\nSaved from " + name + ".\n"
		if saved := save(goals, write(name, text)); saved.Format != "md" {
			t.Fatalf("a save from %s relabeled the md body %q", name, saved.Format)
		}
		if stored := body(goals); stored.Format != "md" {
			t.Fatalf("after a save from %s the body is labeled %q, want md", name, stored.Format)
		}
		if got := relicBodyText(t, repository, id, goals); got != text {
			t.Fatalf("after a save from %s the body is %q, want %q", name, got, text)
		}
	}
	// A file whose ending could not be a label saves over a body all the same.
	if saved := save(goals, write("draft.my_notes", "draft\n")); saved.Format != "md" {
		t.Fatalf("a save from draft.my_notes relabeled the md body %q", saved.Format)
	}

	if saved := save(goals, write("page", "<p>Goals</p>\n"), "--body-format", "html"); saved.Format != "html" {
		t.Fatalf("--body-format html left the label %q", saved.Format)
	}
	if saved := save(goals, write("again.md", "<p>Goals</p>\n<p>More</p>\n")); saved.Format != "html" {
		t.Fatalf("a save from again.md relabeled the html body %q", saved.Format)
	}
	if saved := save(goals, write("plain.md", "Goals\n"), "--body-format", ""); saved.Format != "" {
		t.Fatalf("--body-format with an empty label left the label %q, want none", saved.Format)
	}
	if saved := save(goals, write("still.md", "Goals, still\n")); saved.Format != "" {
		t.Fatalf("a save from still.md labeled the unlabeled body %q", saved.Format)
	}

	// The decision has no body yet, so its first body takes the file's ending.
	head := relicHeadOf(t, repository, id)
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation,
		"update", id, "--part", decision, "--body-file", filepath.Join(directory, "draft.my_notes"))
	if !strings.Contains(message, "--body-format") || !strings.Contains(message, "draft.my_notes") {
		t.Fatalf("a first body from draft.my_notes: message %q, want it to name the file and --body-format", message)
	}
	if now := relicHeadOf(t, repository, id); now != head {
		t.Fatalf("a refused first body moved the relic from %s to %s", head, now)
	}
	if saved := save(decision, write("Notes.MD", "# Why\n")); saved.Format != "md" {
		t.Fatalf("a first body saved from Notes.MD is labeled %q, want md", saved.Format)
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateRefusesBadInvocations(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	part := addRelicParts(t, repository, id, "section")[0]
	head := relicHeadOf(t, repository, id)

	for _, args := range [][]string{
		{"update"},
		{"update", id},
		{"update", id, "--field", "a=b"},
		{"update", id, "--part", part},
		{"update", id, "--part", part, "--before", part},
		{"update", id, "--add-part", "--field", "a=b"},
		{"update", id, "--set", "novalue"},
		{"update", id, "--move-part", part},
		{"update", id, "--part", part, "--body-file", "-"},
	} {
		relicErrorMessage(t, repository, 2, core.CategoryInvocation, args...)
	}
	if message := relicErrorMessage(t, repository, 2, core.CategoryInvocation, "update", id); message != "relic update needs at least one change" {
		t.Fatalf("relic update with no change message = %q", message)
	}
	if message := relicErrorMessage(t, repository, 2, core.CategoryInvocation, "update", id, "--title", "T", "--bogus"); message != `relic update has no option "--bogus"` {
		t.Fatalf("relic update --bogus message = %q, want the option named", message)
	}
	relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", id, "--set-json", "a={")
	// Without --json the refusal is plain text.
	code, stdout, stderr := run(t, repository, "relic", "update", id, "--part", part, "--base", "V")
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "workbook: ") || !strings.Contains(stderr, "--body-file") {
		t.Fatalf("relic update --base without --body-file = (%d, %q, %q), want exit 2 naming --body-file", code, stdout, stderr)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}

	// A task ID is no relic.
	code, stdout, stderr = run(t, repository, "create", "A task", "--json")
	if code != 0 {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "update", task.ID, "--title", "T"); !strings.Contains(message, "no relic matches") {
		t.Fatalf("relic update <task> message = %q, want no relic matches", message)
	}
}

func TestRelicUpdateHelp(t *testing.T) {
	t.Parallel()
	output := assertHelpOutput(t, []string{"relic", "update", "--help"},
		"Usage: workbook relic update <relic> [--title <title>] [--set <name>=<value>]... [--set-json <name>=<json>]... [--clear <name>]... [--no-sync] [--json]\n"+
			"       workbook relic update <relic> --add-part --part-type <type> [--field <name>=<value>]... [--field-json <name>=<json>]... [--body-file <file> [--body-format <label>]] [--before <part> | --after <part>] [--no-sync]\n"+
			"       workbook relic update <relic> --part <part> [--field <name>=<value>]... [--field-json <name>=<json>]... [--clear-field <name>]... [--body-file <file> [--body-format <label>] [--base <version>]] [--no-sync]\n"+
			"       workbook relic update <relic> --move-part <part> (--before <part> | --after <part>) [--no-sync]\n"+
			"       workbook relic update <relic> [--remove-part <part>]... [--restore-part <part>]... [--no-sync]\n"+
			"       workbook relic update <relic> [--link <task|relic[#part]> [--from <part>] --rel <rel> [--pointer <json object>]]... [--unlink <link>]... [--no-sync]\n")
	for _, want := range []string{
		"Change a relic. Every change given in one command is recorded together, or none is.",
		"--add-part, --part, --move-part, --remove-part and --restore-part each start a group,",
		"--title, --set, --set-json and --clear always act on the relic, wherever they are typed.",
		"After --part, --clear still removes a property of the relic; --clear-field removes a field of the part.",
		"Saving a body over an existing one needs --base <version>, the version shown by",
		"--set and --field store text. --set-json and --field-json store any JSON value.",
		"One command names a property once, and a field of a part once; naming one twice is refused.",
		"--link starts a group too: it links the relic, or with --from one of its parts, to a task,",
		"--unlink removes one link, named by the ID relic show lists.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("relic update help = %q, want %q", output, want)
		}
	}
	metadata, exists := commandMetadataFor([]string{"relic", "update"})
	if !exists {
		t.Fatal("relic update has no schema")
	}
	var names []string
	for _, option := range metadata.Options {
		names = append(names, option.Name)
		want := stringFlag
		if option.Name == "add-part" || option.Name == "no-sync" || option.Name == "json" {
			want = boolFlag
		}
		if option.Kind != want || option.Description == "" || (want == stringFlag) != (option.Value != "") {
			t.Errorf("relic update --%s = %+v, want a described option of kind %v", option.Name, option, want)
		}
		if !strings.Contains(output, "\n  --"+option.Name+" ") {
			t.Errorf("relic update help = %q, want option --%s", output, option.Name)
		}
	}
	want := []string{"title", "set", "set-json", "clear", "add-part", "part-type", "field", "field-json", "clear-field",
		"body-file", "body-format", "base", "before", "after", "part", "move-part", "remove-part", "restore-part", "link", "from", "rel", "pointer", "unlink", "no-sync", "json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("relic update options = %q, want %q", names, want)
	}
	if order := relicSubcommands(); !reflect.DeepEqual(order, []string{"create", "list", "show", "diff", "update", "migrate", "delete", "restore", "retire", "unretire", "export"}) {
		t.Fatalf("relic subcommands = %q, want diff and update after show", order)
	}
}

func TestConcurrentRelicUpdatesLeaveAValidHistory(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := initializedRepository(t)
	id := mustCreateRelic(t, repository, "Contested by processes", "--type", "spec").Relic.ID

	const writers = 8
	outcomes := runConcurrently(t, writers, func(index int) (int, string, string) {
		return runBinary(t, binary, repository, "relic", "update", id, "--set", fmt.Sprintf("k%d=v%d", index, index), "--json")
	})
	want := map[string]string{}
	for index, outcome := range outcomes {
		if outcome.code != 0 {
			// Losing the race is exit 6 and nothing written, which the commit
			// count below proves.
			assertRefusedUnderContention(t, outcome, fmt.Sprintf("concurrent relic update %d", index))
			continue
		}
		var updated core.RelicMutationResult
		if err := json.Unmarshal(assertJSONResult(t, outcome.stdout, "relic update").Data, &updated); err != nil {
			t.Fatal(err)
		}
		if !updated.Changed || outcome.stderr != "" {
			t.Fatalf("concurrent relic update %d = %+v, stderr %q, want a quiet change", index, updated, outcome.stderr)
		}
		want[fmt.Sprintf("k%d", index)] = fmt.Sprintf("v%d", index)
	}
	if len(want) == 0 {
		t.Fatalf("no concurrent relic update was accepted: %#v", outcomes)
	}

	if got := mustJSON(t, shownRelic(t, repository, id).Properties); got != mustJSON(t, want) {
		t.Fatalf("properties = %s, want exactly the accepted updates' %s", got, mustJSON(t, want))
	}
	if got := relicCommitCount(t, repository, id); got != 1+len(want) {
		t.Fatalf("the relic has %d commits after %d accepted updates, want %d", got, len(want), 1+len(want))
	}
	for _, line := range strings.Split(gitOutput(t, repository, "rev-list", "--parents", relicRef(id)), "\n") {
		if fields := strings.Fields(line); len(fields) > 2 {
			t.Fatalf("the relic's history has a merge commit: %q", line)
		}
	}
	assertRelicHistoriesValid(t, repository, 1)
}

// relicNestedJSON returns a JSON value that nests arrays the given number of
// levels deep.
func relicNestedJSON(depth int) string {
	return strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)
}

// A value may nest arrays and objects 32 levels deep. One level more is a
// validation refusal that gives the depth and the limit, at any depth: the
// 9,998 levels that once reached the encoder and came back as corrupt data,
// and more than Go's own decoder allows.
func TestRelicValuesNestAtMost32Levels(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Nesting", "--type", "spec")
	id := created.Relic.ID
	added := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "note")
	part := added.Parts[0].ID
	head := added.Relic.Head

	for _, depth := range []int{33, 9998, 10001} {
		for name, args := range map[string][]string{
			"a property":            {"update", id, "--set-json", "deep=" + relicNestedJSON(depth)},
			"a field":               {"update", id, "--part", part, "--field-json", "deep=" + relicNestedJSON(depth)},
			"a field of a new part": {"update", id, "--add-part", "--part-type", "note", "--field-json", "deep=" + relicNestedJSON(depth)},
		} {
			message := relicErrorMessage(t, repository, 5, core.CategoryValidation, args...)
			for _, want := range []string{strconv.Itoa(depth) + " levels", "the limit is 32"} {
				if !strings.Contains(message, want) {
					t.Fatalf("%s nested %d levels: message %q does not hold %q", name, depth, message, want)
				}
			}
			if len(message) > 300 {
				t.Fatalf("%s nested %d levels: the message is %d bytes long; it must not repeat the value", name, depth, len(message))
			}
		}
	}
	if now := relicHeadOf(t, repository, id); now != head {
		t.Fatalf("a refused value moved the relic from %s to %s", head, now)
	}
	// A whole relic read from a document is held to the same limit.
	document := bytes.Replace(relicTestDocument(t), []byte(`"weight":3`), []byte(`"weight":`+relicNestedJSON(33)), 1)
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "create", "--from-file", writeRelicDocumentFile(t, document))
	if !strings.Contains(message, "33 levels") || !strings.Contains(message, "the limit is 32") {
		t.Fatalf("a document with a property nested 33 levels: message %q does not give the depth and the limit", message)
	}

	atLimit := relicNestedJSON(32)
	mustUpdateRelic(t, repository, id, "--set-json", "deep="+atLimit, "--part", part, "--field-json", "deep="+atLimit)
	shown := shownRelic(t, repository, id)
	if got := string(shown.Properties["deep"]); got != atLimit {
		t.Fatalf("the property nested 32 levels reads back as %s", got)
	}
	if code, _, stderr := run(t, repository, "validate"); code != 0 {
		t.Fatalf("validate after a value nested 32 levels = %d; stderr = %q", code, stderr)
	}
}

// A property or field name is at most 100 bytes.
func TestRelicNamesAreAtMost100Bytes(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Names", "--type", "spec")
	id := created.Relic.ID
	added := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "note")
	part := added.Parts[0].ID
	head := added.Relic.Head

	long := strings.Repeat("n", 101)
	for name, args := range map[string][]string{
		"a property":         {"update", id, "--set", long + "=x"},
		"a property cleared": {"update", id, "--clear", long},
		"a field":            {"update", id, "--part", part, "--field", long + "=x"},
		"a field cleared":    {"update", id, "--part", part, "--clear-field", long},
	} {
		message := relicErrorMessage(t, repository, 5, core.CategoryValidation, args...)
		if !strings.Contains(message, "name is 101 bytes") || !strings.Contains(message, "the limit is 100 bytes") ||
			strings.Contains(message, long) {
			t.Fatalf("%s named in 101 bytes: message %q must give the size and the limit and not repeat the name", name, message)
		}
	}
	if now := relicHeadOf(t, repository, id); now != head {
		t.Fatalf("a refused name moved the relic from %s to %s", head, now)
	}
	// A whole relic read from a document is held to the same limit.
	document := bytes.Replace(relicTestDocument(t), []byte(`"weight":3`), []byte(`"`+long+`":3`), 1)
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "create", "--from-file", writeRelicDocumentFile(t, document))
	if !strings.Contains(message, "name is 101 bytes") || strings.Contains(message, long) {
		t.Fatalf("a document with a property named in 101 bytes: message %q must give the size and not repeat the name", message)
	}
	atLimit := strings.Repeat("n", 100)
	mustUpdateRelic(t, repository, id, "--set", atLimit+"=x", "--part", part, "--field", atLimit+"=y")
	if shown := shownRelic(t, repository, id); string(shown.Properties[atLimit]) != `"x"` {
		t.Fatalf("the property named in 100 bytes reads back as %s", shown.Properties[atLimit])
	}
}
