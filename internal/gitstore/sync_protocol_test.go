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
				t.Fatalf("ignored = %#v, want none for valid records", ignored)
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

// A name on origin that this version cannot read as exactly one task is
// reported and skipped. Origin's task namespace is writable by anyone with
// push access, so failing the whole listing would let one ref stop every
// clone from publishing.
func TestParseRemoteTaskHeadsIgnoresUnreadableNamesAndKeepsValidHeads(t *testing.T) {
	const taskID = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	objectID := strings.Repeat("a", 40)
	valid := objectID + "\trefs/workbook/tasks/" + taskID + "\n"
	tests := []struct {
		name    string
		output  string
		wantRef string
	}{
		{
			name:    "nested task",
			output:  valid + objectID + "\trefs/workbook/tasks/" + taskID + "/nested\n",
			wantRef: "refs/workbook/tasks/" + taskID + "/nested",
		},
		{
			name:    "invalid task ID",
			output:  valid + objectID + "\trefs/workbook/tasks/EVIL\n",
			wantRef: "refs/workbook/tasks/EVIL",
		},
		{
			name:    "peeled ref",
			output:  valid + objectID + "\trefs/workbook/tasks/" + taskID + "^{}\n",
			wantRef: "refs/workbook/tasks/" + taskID + "^{}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &Repository{objectIDBytes: 20}
			heads, ignored, err := repository.parseRemoteTaskHeads(
				core.ProjectConfig{Key: "WB"},
				[]byte(test.output),
			)
			if err != nil {
				t.Fatalf("parseRemoteTaskHeads(%q) error = %v", test.output, err)
			}
			want := map[string]string{taskID: objectID}
			if !reflect.DeepEqual(heads, want) {
				t.Fatalf("remote task heads = %#v, want the well-formed %#v", heads, want)
			}
			if len(ignored) != 1 || ignored[0].Ref != test.wantRef {
				t.Fatalf("ignored = %#v, want one entry naming %q", ignored, test.wantRef)
			}
			if ignored[0].Reason == "" {
				t.Fatalf("ignored[0].Reason is empty; the report must say why %q was skipped", test.wantRef)
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
