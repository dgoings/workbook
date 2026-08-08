package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadBatchObjectRejectsAnObjectOverTheCeilingBeforeReadingIt(t *testing.T) {
	// Production mutation: trusting the size in a batch header and allocating it
	// lets one pushed object exhaust memory in every clone that reads it.
	//
	// Only the header is supplied. An implementation that allocates and then
	// reads the body fails on the missing body instead, with an unexpected-EOF
	// error that carries no category at all, so the assertion below is what
	// separates rejecting the object from reading it.
	header := fmt.Sprintf("%s blob %d\n", strings.Repeat("ab", 20), int64(MaxObjectBytes)+1)
	reader := bufio.NewReader(strings.NewReader(header))

	_, err := readBatchObject(reader)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("readBatchObject() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("readBatchObject() error = %v, want the object ceiling named", err)
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
