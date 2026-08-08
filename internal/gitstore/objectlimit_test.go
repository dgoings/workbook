package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadBatchObjectRefusesAnObjectOverTheCeilingWithoutKeepingIt(t *testing.T) {
	// Production mutation: trusting the size in a batch header and allocating it
	// lets one pushed object exhaust memory in every clone that reads it.
	//
	// The refused record is followed by a healthy one, because refusing is only
	// half the requirement: the body is skipped rather than read, so the reader
	// must still land exactly on the next header. An implementation that keeps
	// the contents, or that stops consuming at the header, fails one of the two
	// assertions below.
	objectID := strings.Repeat("ab", 20)
	var stream bytes.Buffer
	fmt.Fprintf(&stream, "%s blob %d\n", objectID, int64(MaxObjectBytes)+1)
	stream.Write(bytes.Repeat([]byte("x"), MaxObjectBytes+1))
	stream.WriteByte('\n')
	fmt.Fprintf(&stream, "%s blob 2\nhi\n", strings.Repeat("cd", 20))
	reader := bufio.NewReader(&stream)

	refused, err := readBatchObject(reader)
	if err != nil {
		t.Fatalf("readBatchObject() error = %v, want the oversized record consumed and refused", err)
	}
	if got, want := core.CategoryOf(refused.refused), core.CategoryCorruptData; got != want {
		t.Fatalf("readBatchObject() refusal category = %q, want %q; error = %v", got, want, refused.refused)
	}
	if !strings.Contains(refused.refused.Error(), "ceiling") {
		t.Fatalf("readBatchObject() refusal = %v, want the object ceiling named", refused.refused)
	}
	if refused.contents != nil {
		t.Fatalf("readBatchObject() kept %d bytes of a refused object, want none", len(refused.contents))
	}

	next, err := readBatchObject(reader)
	if err != nil {
		t.Fatalf("readBatchObject() error = %v, want the stream resynchronized after a refusal", err)
	}
	if string(next.contents) != "hi" {
		t.Fatalf("readBatchObject() next contents = %q, want %q", next.contents, "hi")
	}
}

func TestReadBatchObjectRejectsASizeNoObjectCouldHave(t *testing.T) {
	// Production mutation: a size that cannot be skipped is framing, not a
	// record, and treating it as one would make the span to discard overflow.
	header := fmt.Sprintf("%s blob %d\n", strings.Repeat("ab", 20), uint64(math.MaxUint64))
	reader := bufio.NewReader(strings.NewReader(header))

	if _, err := readBatchObject(reader); err == nil {
		t.Fatal("readBatchObject() error = nil, want an unusable size rejected as framing")
	}
}

func TestReadBatchObjectAcceptsAnObjectExactlyAtTheCeiling(t *testing.T) {
	// Production mutation: an off-by-one in the ceiling would reject a document
	// a previous version wrote and stored.
	objectID := strings.Repeat("ab", 20)
	var stream bytes.Buffer
	fmt.Fprintf(&stream, "%s blob %d\n", objectID, MaxObjectBytes)
	stream.Write(bytes.Repeat([]byte("x"), MaxObjectBytes))
	stream.WriteByte('\n')

	object, err := readBatchObject(bufio.NewReader(&stream))
	if err != nil {
		t.Fatalf("readBatchObject() error = %v", err)
	}
	if len(object.contents) != MaxObjectBytes {
		t.Fatalf("readBatchObject() contents = %d bytes, want %d", len(object.contents), MaxObjectBytes)
	}
}

func TestReadTaskHeadRejectsATipDocumentOverTheCeiling(t *testing.T) {
	// Production mutation: a hand-built task tip whose operation document is
	// larger than any task could legitimately be must not be read into memory
	// just because a collaborator pushed it.
	//
	// The category alone does not prove anything here: an oversized blob is also
	// not a canonical operation document, so a reader that allocates it and then
	// fails to decode it reports corrupt-data too. Naming the ceiling in the
	// message is what separates refusing the object from reading it first.
	repository, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repository, config)
	oversized := gitOutputWithInput(
		t,
		repository,
		bytes.Repeat([]byte("x"), MaxObjectBytes+1),
		"hash-object", "-w", "--stdin",
	)
	tree := gitOutputWithInput(t, repository, []byte(
		"100644 blob "+oversized+"\toperation.json\n"+
			"100644 blob "+gitOutput(t, repository, "rev-parse", snapshot.Head+":state.json")+"\tstate.json\n",
	), "mktree")
	commit := gitOutput(t, repository, "commit-tree", tree, "-m", "oversized operation document")

	_, err := repository.ReadTaskHead(context.Background(), config, TaskHead{
		TaskID:   snapshot.Operation.TaskID,
		ObjectID: commit,
	})
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("ReadTaskHead() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("ReadTaskHead() error = %v, want the object ceiling named", err)
	}
}

// TestReadTaskHeadsPartialAttributesAnOverCeilingObjectToItsOwnRequest holds the
// refusal to the boundary readTaskHeadsPartial documents: an object failure
// belongs to one request, and only unreadable framing may condemn the batch.
//
// The ceiling closes a memory exhaustion, and aborting the whole batch would
// pay for that with an availability failure that is worse: one pushed ref would
// stop `list`, both `sync` phases and `rebuild` everywhere, with no command left
// that can read past it. Git states the size before the body, so the record can
// be skipped without being allocated and the stream stays synchronized.
func TestReadTaskHeadsPartialAttributesAnOverCeilingObjectToItsOwnRequest(t *testing.T) {
	repository, config := writeRepository(t)
	first, _, _ := writeRoot(t, repository, config)
	oversized := gitOutputWithInput(
		t,
		repository,
		bytes.Repeat([]byte("x"), MaxObjectBytes+1),
		"hash-object", "-w", "--stdin",
	)
	tree := gitOutputWithInput(t, repository, []byte(
		"100644 blob "+oversized+"\toperation.json\n"+
			"100644 blob "+gitOutput(t, repository, "rev-parse", first.Head+":state.json")+"\tstate.json\n",
	), "mktree")
	commit := gitOutput(t, repository, "commit-tree", tree, "-m", "oversized operation document")
	third := writeIndependentRoot(
		t,
		repository,
		config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Third task",
	)

	results, err := repository.readTaskHeadsPartial(context.Background(), config, []TaskHead{
		{TaskID: first.Operation.TaskID, ObjectID: first.Head},
		{TaskID: first.Operation.TaskID, ObjectID: commit},
		{TaskID: third.Operation.TaskID, ObjectID: third.Head},
	})
	if err != nil {
		t.Fatalf("readTaskHeadsPartial() error = %v, want the oversized object attributed to one request", err)
	}
	if results[0].Err != nil || !reflect.DeepEqual(results[0].Snapshot, first) {
		t.Fatalf("first result = %#v, want the valid snapshot preceding the oversized head", results[0])
	}
	if got, want := core.CategoryOf(results[1].Err), core.CategoryCorruptData; got != want {
		t.Fatalf("oversized result category = %q, want %q; error = %v", got, want, results[1].Err)
	}
	if results[1].Err == nil || !strings.Contains(results[1].Err.Error(), "ceiling") {
		t.Fatalf("oversized result error = %v, want the object ceiling named", results[1].Err)
	}
	if results[2].Err != nil || !reflect.DeepEqual(results[2].Snapshot, third) {
		t.Fatalf("third result = %#v, want the batch to resynchronize after the oversized object", results[2])
	}
}

// TestReadTaskHeadsPartialReportsGitStderrWhenTheBatchProcessDies keeps the
// diagnosis the buffered `r.Git` call used to produce.
//
// A batch process that dies mid-stream hands the reader io.EOF, which names
// nothing. Git's stderr is the only text that says what went wrong, so a read
// failure must reap the process before reporting, not return past it.
func TestReadTaskHeadsPartialReportsGitStderrWhenTheBatchProcessDies(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repository, config)
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf 'fatal: loose object is corrupt\\n' >&2\nexit 128\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repository.gitPath = fakeGit

	_, err := repository.readTaskHeadsPartial(context.Background(), config, []TaskHead{{
		TaskID:   snapshot.Operation.TaskID,
		ObjectID: snapshot.Head,
	}})
	if err == nil {
		t.Fatal("readTaskHeadsPartial() error = nil, want the dead batch process reported")
	}
	if !strings.Contains(err.Error(), "loose object is corrupt") {
		t.Fatalf("readTaskHeadsPartial() error = %v, want Git's stderr included", err)
	}
	if !strings.Contains(err.Error(), "exit status 128") {
		t.Fatalf("readTaskHeadsPartial() error = %v, want Git's exit status included", err)
	}
}
