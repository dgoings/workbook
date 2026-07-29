package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadTaskHeadsPartialConsumesAllRecordsAfterMissingMiddleHead(t *testing.T) {
	repository, config := writeRepository(t)
	first, _, _ := writeRoot(t, repository, config)
	missingObject := gitOutputWithInput(t, repository, []byte("missing task tip"), "hash-object", "-w", "--stdin")
	third := writeIndependentRoot(
		t,
		repository,
		config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Third task",
	)

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	results, err := repository.readTaskHeadsPartial(context.Background(), config, []TaskHead{
		{TaskID: first.Operation.TaskID, ObjectID: first.Head},
		{TaskID: "WB-01K0M6B8A4FTT8C39MXXYTW7C5", ObjectID: missingObject},
		{TaskID: third.Operation.TaskID, ObjectID: third.Head},
	})
	if err != nil {
		t.Fatalf("readTaskHeadsPartial() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].Err != nil || !reflect.DeepEqual(results[0].Snapshot, first) {
		t.Fatalf("first result = %#v, want valid first snapshot", results[0])
	}
	if got, want := core.CategoryOf(results[1].Err), core.CategoryCorruptData; got != want {
		t.Fatalf("middle result category = %q, want %q; error = %v", got, want, results[1].Err)
	}
	if results[2].Err != nil || !reflect.DeepEqual(results[2].Snapshot, third) {
		t.Fatalf("third result = %#v, want valid third snapshot after missing middle head", results[2])
	}
	if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
		t.Fatalf("cat-file --batch commands = %d, want 1; commands = %v", got, commands)
	}
}

func TestReadTaskHeadsPartialRejectsMalformedBatchFramingForAllHeads(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repository, config)
	fakeGit := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nprintf 'not a Git batch header\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repository.gitPath = fakeGit

	_, err := repository.readTaskHeadsPartial(context.Background(), config, []TaskHead{{
		TaskID:   snapshot.Operation.TaskID,
		ObjectID: snapshot.Head,
	}})
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("readTaskHeadsPartial() category = %q, want %q; error = %v", got, want, err)
	}
}
