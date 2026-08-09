package gitstore

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestParseRemoteTaskHeadsAcceptsFullSHA1AndSHA256Records(t *testing.T) {
	const (
		firstTask  = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
		secondTask = "WB-01K0M6B8A4FTT8C39MXXYTW7D2"
	)
	for _, objectID := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		t.Run(fmt.Sprintf("%d-hex", len(objectID)), func(t *testing.T) {
			repository := &Repository{objectIDBytes: len(objectID) / 2}
			output := []byte(objectID + "\trefs/workbook/tasks/" + firstTask + "\n" +
				objectID + "\trefs/workbook/tasks/" + secondTask + "\n")

			got, ignored, err := repository.parseRemoteTaskHeads(core.ProjectConfig{Key: "WB"}, output)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{firstTask: objectID, secondTask: objectID}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("remote task heads = %#v, want %#v", got, want)
			}
			if len(ignored) != 0 {
				t.Fatalf("ignored refs = %#v, want none", ignored)
			}
		})
	}
}

func TestParseRemoteTaskHeadsRejectsInvalidRecords(t *testing.T) {
	const taskID = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	objectID := strings.Repeat("a", 40)
	valid := objectID + "\trefs/workbook/tasks/" + taskID + "\n"
	tests := []struct {
		name   string
		output string
	}{
		{name: "unterminated", output: strings.TrimSuffix(valid, "\n")},
		{name: "wrong prefix", output: objectID + "\trefs/heads/main\n"},
		{name: "duplicate task", output: valid + valid},
		{name: "abbreviated object ID", output: objectID[:38] + "\trefs/workbook/tasks/" + taskID + "\n"},
		{name: "extra field", output: objectID + "\trefs/workbook/tasks/" + taskID + "\textra\n"},
		{name: "symbolic record", output: "ref: refs/heads/main\trefs/workbook/tasks/" + taskID + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &Repository{objectIDBytes: 20}
			if _, _, err := repository.parseRemoteTaskHeads(
				core.ProjectConfig{Key: "WB"},
				[]byte(test.output),
			); err == nil {
				t.Fatalf("parseRemoteTaskHeads(%q) error = nil", test.output)
			}
		})
	}
}

// Origin's task namespace is writable by every collaborator, so a name this
// version does not recognize is skipped and reported instead of failing the
// listing that publication depends on. The report separates a name no project
// can own from one a newer version or a second project's key would produce,
// because only the first may ever be offered for deletion.
func TestParseRemoteTaskHeadsSkipsAndReportsUnrecognizedNames(t *testing.T) {
	const taskID = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	objectID := strings.Repeat("a", 40)
	valid := objectID + "\trefs/workbook/tasks/" + taskID + "\n"
	tests := []struct {
		name          string
		output        string
		want          string
		wantPlausible bool
	}{
		{name: "nested task", output: objectID + "\trefs/workbook/tasks/" + taskID + "/nested\n", want: "refs/workbook/tasks/" + taskID + "/nested", wantPlausible: true},
		{name: "invalid task ID", output: objectID + "\trefs/workbook/tasks/EVIL\n", want: "refs/workbook/tasks/EVIL"},
		{name: "peeled ref", output: objectID + "\trefs/workbook/tasks/" + taskID + "^{}\n", want: "refs/workbook/tasks/" + taskID + "^{}", wantPlausible: true},
		{name: "bare namespace", output: objectID + "\trefs/workbook/tasks/\n", want: "refs/workbook/tasks/"},
		{name: "another project's key", output: objectID + "\trefs/workbook/tasks/" + foreignTaskID + "\n", want: "refs/workbook/tasks/" + foreignTaskID, wantPlausible: true},
		// This branch is the one place a peeled name reaches the report, and a
		// second project's history must survive it exactly as this project's
		// does.
		{name: "peeled ref under another project's key", output: objectID + "\trefs/workbook/tasks/" + foreignTaskID + "^{}\n", want: "refs/workbook/tasks/" + foreignTaskID + "^{}", wantPlausible: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &Repository{objectIDBytes: 20}
			heads, ignored, err := repository.parseRemoteTaskHeads(
				core.ProjectConfig{Key: "WB"},
				[]byte(valid+test.output),
			)
			if err != nil {
				t.Fatalf("parseRemoteTaskHeads(%q) error = %v", test.output, err)
			}
			if want := map[string]string{taskID: objectID}; !reflect.DeepEqual(heads, want) {
				t.Fatalf("remote task heads = %#v, want %#v", heads, want)
			}
			if len(ignored) != 1 || ignored[0].Ref != test.want {
				t.Fatalf("ignored refs = %#v, want exactly %q", ignored, test.want)
			}
			if strings.TrimSpace(ignored[0].Reason) == "" {
				t.Fatalf("ignored ref %q has no reason", ignored[0].Ref)
			}
			if ignored[0].PlausibleTask != test.wantPlausible {
				t.Fatalf("ignored ref %q plausible = %t, want %t", ignored[0].Ref, ignored[0].PlausibleTask, test.wantPlausible)
			}
		})
	}
}

func TestParsePushPorcelainAccountsForEachExpectedDestination(t *testing.T) {
	expected := map[string]string{
		"refs/workbook/tasks/task-create":   "task-create",
		"refs/workbook/tasks/task-forward":  "task-forward",
		"refs/workbook/tasks/task-current":  "task-current",
		"refs/workbook/tasks/task-rejected": "task-rejected",
	}
	output := []byte("To /private/tmp/origin.git\n" +
		"*\tabc:refs/workbook/tasks/task-create\t[new reference]\n" +
		" \tdef:refs/workbook/tasks/task-forward\tabc..def\n" +
		"=\tghi:refs/workbook/tasks/task-current\t[up to date]\n" +
		"!\tjkl:refs/workbook/tasks/task-rejected\t[rejected] (fetch first)\n" +
		"Done\n")

	got, err := parsePushPorcelain(output, expected, errors.New("exit status 1"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]SyncTaskResult{
		"task-create":   {TaskID: "task-create", Status: SyncPublished},
		"task-forward":  {TaskID: "task-forward", Status: SyncPublished},
		"task-current":  {TaskID: "task-current", Status: SyncUpToDate},
		"task-rejected": {TaskID: "task-rejected", Status: SyncRejected, Detail: "[rejected] (fetch first)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("porcelain outcomes = %#v, want %#v", got, want)
	}
}

func TestParsePushPorcelainRejectsIncompleteOrUnsafeAccounting(t *testing.T) {
	expected := map[string]string{
		"refs/workbook/tasks/task-a": "task-a",
		"refs/workbook/tasks/task-b": "task-b",
	}
	validA := "*\tabc:refs/workbook/tasks/task-a\t[new reference]\n"
	validB := "=\tdef:refs/workbook/tasks/task-b\t[up to date]\n"
	tests := []struct {
		name       string
		output     string
		commandErr error
	}{
		{name: "missing", output: validA},
		{name: "duplicate", output: validA + validA + validB},
		{name: "unexpected task", output: validA + "*\tdef:refs/workbook/tasks/task-c\t[new reference]\n"},
		{name: "code destination", output: validA + "*\tdef:refs/heads/main\t[new branch]\n"},
		{name: "malformed tabs", output: validA + "=\tdef:refs/workbook/tasks/task-b [up to date]\n"},
		{name: "force", output: "+\tabc:refs/workbook/tasks/task-a\tforced update\n" + validB},
		{name: "deletion", output: "-\t:refs/workbook/tasks/task-a\t[deleted]\n" + validB},
		{name: "incomplete command failure", output: validA, commandErr: errors.New("exit status 1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parsePushPorcelain([]byte(test.output), expected, test.commandErr); err == nil {
				t.Fatalf("parsePushPorcelain(%q) error = nil", test.output)
			}
		})
	}
}
