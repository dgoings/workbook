package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Attachment tombstones: what compaction leaves on a task in place of a file
// whose bytes it did not carry forward.

const (
	dropRunID     = "01K0M6B8A4FTT8C39MXXYTWD01"
	dropLinkID    = "01K0M6B8A4FTT8C39MXXYTWA03"
	dropBlobOther = "8f2c3e4d5b6a79880716253443526170899aabbc"
)

var dropTime = time.Date(2026, time.November, 10, 14, 30, 0, 0, time.UTC)

func dropFixture() []Attachment {
	return []Attachment{
		{ID: attachOneID, Author: threadActor, AddedAt: threadWallTime, AttachmentData: AttachmentData{
			Name: "trace.log", Kind: AttachmentFile, Media: "text/plain", Size: 11, Blob: threadBlobOID,
		}},
		{ID: attachTwoID, Author: threadActor, AddedAt: threadWallTime, AttachmentData: AttachmentData{
			Name: "screen.png", Kind: AttachmentFile, Media: "image/png", Size: 2048, Blob: dropBlobOther,
		}},
		{ID: dropLinkID, Author: threadActor, AddedAt: threadWallTime, AttachmentData: AttachmentData{
			Kind: AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
		}},
	}
}

func TestDropFileAttachmentsKeepsLinks(t *testing.T) {
	before := dropFixture()
	original := dropFixture()

	dropped, bytes, out := DropFileAttachments(before, dropRunID, dropTime)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if want := int64(11 + 2048); bytes != want {
		t.Fatalf("bytes = %d, want %d", bytes, want)
	}
	if len(out) != 3 {
		t.Fatalf("out = %#v, want three entries", out)
	}
	for index, attachment := range out {
		if attachment.ID != original[index].ID || attachment.AttachmentData != original[index].AttachmentData ||
			attachment.Author != original[index].Author || !attachment.AddedAt.Equal(original[index].AddedAt) {
			t.Fatalf("out[%d] = %#v, want everything but the drop unchanged from %#v", index, attachment, original[index])
		}
		if attachment.Kind == AttachmentLink {
			if attachment.Dropped != nil || attachment.IsDropped() {
				t.Fatalf("link %s was marked dropped: %#v", attachment.ID, attachment.Dropped)
			}
			continue
		}
		if !attachment.IsDropped() || attachment.Dropped.RunID != dropRunID || !attachment.Dropped.At.Equal(dropTime) {
			t.Fatalf("file %s drop = %#v, want run %s at %s", attachment.ID, attachment.Dropped, dropRunID, dropTime)
		}
	}
	if !SameAttachments(before, original) {
		t.Fatalf("DropFileAttachments mutated its input: %#v", before)
	}
}

// A file a previous run already dropped keeps the drop that run recorded, and
// is not counted again: it held no bytes to drop this time.
func TestDropFileAttachmentsLeavesAnEarlierDropAlone(t *testing.T) {
	earlier := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	_, _, first := DropFileAttachments(dropFixture()[:1], "01K0M6B8A4FTT8C39MXXYTWD00", earlier)
	attachments := append(first, dropFixture()[1:]...)

	dropped, bytes, out := DropFileAttachments(attachments, dropRunID, dropTime)
	if dropped != 1 || bytes != 2048 {
		t.Fatalf("dropped, bytes = %d, %d; want 1, 2048", dropped, bytes)
	}
	if out[0].Dropped.RunID != "01K0M6B8A4FTT8C39MXXYTWD00" || !out[0].Dropped.At.Equal(earlier) {
		t.Fatalf("earlier drop = %#v, want it unchanged", out[0].Dropped)
	}
}

func TestLiveAttachmentBytesIgnoresDropped(t *testing.T) {
	attachments := dropFixture()
	if got := LiveAttachmentBytes(attachments); got != 11+2048 {
		t.Fatalf("LiveAttachmentBytes(live) = %d, want %d", got, 11+2048)
	}
	_, _, out := DropFileAttachments(attachments[:1], dropRunID, dropTime)
	attachments[0] = out[0]
	if got := LiveAttachmentBytes(attachments); got != 2048 {
		t.Fatalf("LiveAttachmentBytes(one dropped) = %d, want 2048", got)
	}
}

func TestSameAttachmentsComparesDrop(t *testing.T) {
	left := dropFixture()
	_, _, right := DropFileAttachments(dropFixture(), dropRunID, dropTime)
	if SameAttachments(left, right) {
		t.Fatal("SameAttachments() = true for a live list and its dropped copy")
	}
	_, _, again := DropFileAttachments(dropFixture(), dropRunID, dropTime.In(time.FixedZone("EST", -5*3600)))
	if !SameAttachments(right, again) {
		t.Fatal("SameAttachments() = false for the same drop at the same instant in another zone")
	}
	_, _, otherRun := DropFileAttachments(dropFixture(), "01K0M6B8A4FTT8C39MXXYTWD02", dropTime)
	if SameAttachments(right, otherRun) {
		t.Fatal("SameAttachments() = true for drops by different runs")
	}
	_, _, otherTime := DropFileAttachments(dropFixture(), dropRunID, dropTime.Add(time.Second))
	if SameAttachments(right, otherTime) {
		t.Fatal("SameAttachments() = true for drops at different times")
	}
}

func TestAttachmentContentRefusesDropped(t *testing.T) {
	service, _, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	reader := &blobReaderSpy{contents: []byte("hello world")}
	service.BlobReads = reader
	_, _, out := DropFileAttachments(dropFixture()[:1], dropRunID, dropTime)

	content, err := service.AttachmentContent(context.Background(), out[0])
	if got := CategoryOf(err); got != CategoryNotFound {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryNotFound, err)
	}
	if content != nil {
		t.Fatalf("a dropped attachment returned %d bytes", len(content))
	}
	want := `attachment "trace.log" was removed when the task was compacted on 2026-11-10`
	if err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err.Error(), want)
	}
	if len(reader.read) != 0 {
		t.Fatalf("a dropped attachment reached the blob reader: %#v", reader.read)
	}
}

// The drop is part of the attachment's stored shape, and an attachment that was
// never dropped encodes to exactly the bytes it always did.
func TestAttachmentDropEncodes(t *testing.T) {
	live, err := json.Marshal(dropFixture()[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(live), "dropped") {
		t.Fatalf("a live attachment encodes a drop: %s", live)
	}
	_, _, out := DropFileAttachments(dropFixture()[:1], dropRunID, dropTime)
	encoded, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := `"dropped":{"runId":"` + dropRunID + `","at":"2026-11-10T14:30:00Z"}`; !strings.Contains(string(encoded), want) {
		t.Fatalf("dropped attachment = %s, want it to carry %s", encoded, want)
	}
	var decoded Attachment
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !SameAttachments([]Attachment{decoded}, out) {
		t.Fatalf("round trip = %#v, want %#v", decoded, out[0])
	}
}

// A dropped file weighs nothing against the live-bytes ceiling, at the
// pre-staging budget and after the fold alike: it neither blocks a new file nor,
// when it is removed, frees room it was never using.
func TestDroppedFilesDoNotCountAgainstTheCeiling(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	_, _, dropped := DropFileAttachments([]Attachment{{
		ID: attachOneID, Author: threadActor, AddedAt: threadWallTime,
		AttachmentData: AttachmentData{Name: "huge.bin", Kind: AttachmentFile, Size: MaxLiveAttachmentBytes, Blob: threadBlobOID},
	}}, dropRunID, dropTime)

	t.Run("a dropped file leaves room", func(t *testing.T) {
		task := base
		task.Attachments = dropped
		service, store, blobs := threadServiceUnderTest(t, task)
		if _, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
			Kind: AttachmentFile, Name: "more.png", Content: []byte("more bytes"),
		}); err != nil {
			t.Fatalf("AttachmentAddMutation() error = %v; a dropped file holds no bytes", err)
		}
		if len(blobs.staged) != 1 || len(store.writes) != 1 {
			t.Fatalf("staged %d blobs and wrote %d packs, want one of each", len(blobs.staged), len(store.writes))
		}
	})

	t.Run("removing a dropped file frees nothing", func(t *testing.T) {
		task := base
		task.Attachments = append(append([]Attachment(nil), dropped...), Attachment{
			ID: attachTwoID, Author: threadActor, AddedAt: threadWallTime,
			AttachmentData: AttachmentData{Name: "full.bin", Kind: AttachmentFile, Size: MaxLiveAttachmentBytes, Blob: dropBlobOther},
		})
		service, store, blobs := threadServiceUnderTest(t, task)
		_, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{
			Attachments: []AttachmentChange{
				{AttachmentID: attachOneID, Remove: true},
				{Kind: AttachmentFile, Name: "more.png", Content: []byte("more bytes")},
			},
		})
		if got := CategoryOf(err); got != CategoryValidation {
			t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
		}
		if len(blobs.staged) != 0 || len(store.writes) != 0 {
			t.Fatalf("a refused swap staged %d blobs and wrote %d packs", len(blobs.staged), len(store.writes))
		}
	})
}
