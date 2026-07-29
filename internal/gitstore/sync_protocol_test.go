package gitstore

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseRemoteTaskHeadsAcceptsFullSHA1AndSHA256Records(t *testing.T) {
	for _, objectID := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		t.Run(fmt.Sprintf("%d-hex", len(objectID)), func(t *testing.T) {
			repository := &Repository{objectIDBytes: len(objectID) / 2}
			output := []byte(objectID + "\trefs/workbook/tasks/task-a\n" + objectID + "\trefs/workbook/tasks/task-b\n")

			got, err := repository.parseRemoteTaskHeads(output)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"task-a": objectID, "task-b": objectID}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("remote task heads = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseRemoteTaskHeadsRejectsInvalidRecords(t *testing.T) {
	objectID := strings.Repeat("a", 40)
	valid := objectID + "\trefs/workbook/tasks/task-a\n"
	tests := []struct {
		name   string
		output string
	}{
		{name: "unterminated", output: strings.TrimSuffix(valid, "\n")},
		{name: "wrong prefix", output: objectID + "\trefs/heads/main\n"},
		{name: "nested task", output: objectID + "\trefs/workbook/tasks/task-a/nested\n"},
		{name: "duplicate task", output: valid + valid},
		{name: "abbreviated object ID", output: objectID[:38] + "\trefs/workbook/tasks/task-a\n"},
		{name: "extra field", output: objectID + "\trefs/workbook/tasks/task-a\textra\n"},
		{name: "symbolic record", output: "ref: refs/heads/main\trefs/workbook/tasks/task-a\n"},
		{name: "peeled ref", output: objectID + "\trefs/workbook/tasks/task-a^{}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &Repository{objectIDBytes: 20}
			if _, err := repository.parseRemoteTaskHeads([]byte(test.output)); err == nil {
				t.Fatalf("parseRemoteTaskHeads(%q) error = nil", test.output)
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
