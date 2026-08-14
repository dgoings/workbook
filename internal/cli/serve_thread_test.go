package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The thread routes through the real serve wiring: five mutations, the finder,
// and the blob read behind the download.
//
// A handler test cannot show any of this. It holds its own fakes, so each of
// the seven function literals `runServe` hands the board could be missing, or
// closed over a service built without the half it needs, and the whole webui
// package would stay green. `BlobReads` was the second of those: without the
// read half of the blob store this board accepts an attachment it can never
// hand back, every download answers "attachment blob reader is not configured",
// and only a round trip through the binary's own service says so — verified by
// deleting the field and watching this test alone fail.
func TestRunServeWritesAndReadsAThreadThroughWebRoutes(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Discussed through the board", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	addr := startServeBoard(t, repository)
	base := "http://" + addr + "/api/tasks/" + task.ID

	// A comment, edited and then removed, each against the head the last answer
	// carried — which is the flow the page itself follows.
	body, status := boardRequest(t, http.MethodPost, base+"/comments",
		`{"body":"  Written through the board.  ","expectedHead":"`+task.Head+`"}`)
	commented := decodeServeMutation(t, body, status)
	if len(commented.Comments) != 1 || commented.Comments[0].Body != "Written through the board." {
		t.Fatalf("comment add returned %#v, want one trimmed comment", commented.Comments)
	}
	if commented.Head == task.Head {
		t.Fatal("a comment did not advance the task ref")
	}
	if head := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+task.ID); head != commented.Head {
		t.Fatalf("task ref = %s, want the head the answer reported %s", head, commented.Head)
	}
	comment := commented.Comments[0].ID

	body, status = boardRequest(t, http.MethodPatch, base+"/comments/"+comment,
		`{"body":"Reworded through the board.","expectedHead":"`+commented.Head+`"}`)
	edited := decodeServeMutation(t, body, status)
	if len(edited.Comments) != 1 || edited.Comments[0].Body != "Reworded through the board." ||
		edited.Comments[0].EditedAt == nil {
		t.Fatalf("comment edit returned %#v, want the body replaced and marked edited", edited.Comments)
	}

	// A head the caller has moved past is refused here as everywhere.
	body, status = boardRequest(t, http.MethodDelete, base+"/comments/"+comment,
		`{"expectedHead":"`+task.Head+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("stale comment removal = %d, want %d; body = %s", status, http.StatusConflict, body)
	}

	// A file, uploaded as base64 in JSON because the same-origin guard admits
	// nothing else, and read back byte for byte through the blob store this
	// board opened with.
	contents := []byte("trace line\x00\xff\xfe binary tail\n")
	body, status = boardRequest(t, http.MethodPost, base+"/attachments",
		`{"kind":"file","name":"trace.log","content":"`+base64.StdEncoding.EncodeToString(contents)+`"}`)
	attached := decodeServeMutation(t, body, status)
	if len(attached.Attachments) != 1 {
		t.Fatalf("attachment add returned %#v, want one attachment", attached.Attachments)
	}
	stored := attached.Attachments[0]
	if stored.Name != "trace.log" || stored.Kind != core.AttachmentFile ||
		stored.Media != "text/plain" || stored.Size != int64(len(contents)) {
		t.Fatalf("stored attachment = %#v", stored)
	}

	downloaded, status, header := boardAttachment(t, base+"/attachments/"+stored.ID)
	if status != http.StatusOK {
		t.Fatalf("GET attachment = %d, want %d; body = %s", status, http.StatusOK, downloaded)
	}
	if !bytes.Equal(downloaded, contents) {
		t.Fatalf("downloaded %q, want the stored bytes %q", downloaded, contents)
	}
	// text/plain is not on the inline list, so it downloads opaquely — the same
	// answer the handler tests pin, now shown through the binary's own service.
	if got := header.Get("Content-Type"); got != core.DefaultAttachmentMedia {
		t.Errorf("Content-Type = %q, want %q", got, core.DefaultAttachmentMedia)
	}
	if got := header.Get("Content-Disposition"); got != "attachment; filename=trace.log" {
		t.Errorf("Content-Disposition = %q, want a download naming the file", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := header.Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Errorf("Content-Security-Policy = %q, want the attachment policy", got)
	}

	// An image is the other half of the same rule, and the only kind served
	// inline.
	png := []byte("\x89PNG\r\n\x1a\nnot really a picture")
	body, status = boardRequest(t, http.MethodPost, base+"/attachments",
		`{"kind":"file","name":"screenshot.png","content":"`+base64.StdEncoding.EncodeToString(png)+`"}`)
	withImage := decodeServeMutation(t, body, status)
	image := attachmentNamed(t, withImage, "screenshot.png")
	if image.Media != "image/png" {
		t.Fatalf("stored image media = %q, want image/png", image.Media)
	}
	downloaded, status, header = boardAttachment(t, base+"/attachments/"+image.ID)
	if status != http.StatusOK || !bytes.Equal(downloaded, png) {
		t.Fatalf("GET image = %d, %d bytes; want %d of them", status, len(downloaded), len(png))
	}
	if got := header.Get("Content-Type"); got != "image/png" {
		t.Errorf("image Content-Type = %q, want image/png", got)
	}
	if got := header.Get("Content-Disposition"); got != "inline; filename=screenshot.png" {
		t.Errorf("image Content-Disposition = %q, want it inline", got)
	}

	// A link stores nothing and holds no bytes, and the route says so in a
	// sentence that names where the thing actually is.
	body, status = boardRequest(t, http.MethodPost, base+"/attachments",
		`{"kind":"link","url":"https://example.test/design","label":"Design doc"}`)
	withLink := decodeServeMutation(t, body, status)
	link := attachmentNamed(t, withLink, "")
	body, status, _ = boardAttachment(t, base+"/attachments/"+link.ID)
	if status != http.StatusBadRequest {
		t.Fatalf("GET a link = %d, want %d; body = %s", status, http.StatusBadRequest, body)
	}
	if !strings.Contains(string(body), "https://example.test/design") {
		t.Fatalf("the link refusal does not name the URL: %s", body)
	}

	// Removing an attachment takes it out of the list, and the route stops
	// answering for it: the finder looks in the task's own live attachments, so
	// there is nothing left to read.
	body, status = boardRequest(t, http.MethodDelete, base+"/attachments/"+stored.ID, "")
	shortened := decodeServeMutation(t, body, status)
	for _, remaining := range shortened.Attachments {
		if remaining.ID == stored.ID {
			t.Fatalf("attachment %s survived its removal", stored.ID)
		}
	}
	if _, status, _ = boardAttachment(t, base+"/attachments/"+stored.ID); status != http.StatusNotFound {
		t.Fatalf("GET a removed attachment = %d, want %d", status, http.StatusNotFound)
	}

	body, status = boardRequest(t, http.MethodDelete, base+"/comments/"+comment,
		`{"expectedHead":"`+shortened.Head+`"}`)
	emptied := decodeServeMutation(t, body, status)
	if len(emptied.Comments) != 0 {
		t.Fatalf("comment removal returned %#v, want an empty thread", emptied.Comments)
	}
}

// An attachment belongs to one task, and the route reads it out of that task's
// own list. Neither another task's attachment nor an identifier of the wrong
// kind is something this address can serve, and both answer as not found rather
// than reaching the blob store with an object ID from somewhere else.
func TestRunServeScopesAnAttachmentToItsOwnTask(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Holds the attachment", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	holder := decodeMutationTask(t, stdout, "create")
	code, stdout, stderr = run(t, repository, "create", "Holds nothing", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	other := decodeMutationTask(t, stdout, "create")
	addr := startServeBoard(t, repository)

	body, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks/"+holder.ID+"/attachments",
		`{"kind":"file","name":"secret.log","content":"`+base64.StdEncoding.EncodeToString([]byte("private"))+`"}`)
	attached := decodeServeMutation(t, body, status)
	secret := attached.Attachments[0].ID

	body, status = boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks/"+holder.ID+"/comments",
		`{"body":"A remark whose identifier is not an attachment."}`)
	commented := decodeServeMutation(t, body, status)
	comment := commented.Comments[0].ID

	for _, probe := range []struct {
		what string
		url  string
	}{
		{"another task's attachment", "http://" + addr + "/api/tasks/" + other.ID + "/attachments/" + secret},
		{"a comment in the attachment slot", "http://" + addr + "/api/tasks/" + holder.ID + "/attachments/" + comment},
		{"an identifier of nothing", "http://" + addr + "/api/tasks/" + holder.ID + "/attachments/01K0M6B8A4FTT8C39MXXYTWZZZ"},
	} {
		body, status, _ := boardAttachment(t, probe.url)
		if status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d; body = %s", probe.what, status, http.StatusNotFound, body)
		}
		if bytes.Contains(body, []byte("private")) {
			t.Errorf("GET %s served the bytes it was not asked for", probe.what)
		}
	}
}

// attachmentNamed finds the attachment a task carries under a file name, or the
// one link it carries when the name is empty.
func attachmentNamed(t *testing.T, task core.Task, name string) core.Attachment {
	t.Helper()
	for _, attachment := range task.Attachments {
		if attachment.Name == name {
			return attachment
		}
	}
	t.Fatalf("task carries no attachment named %q: %#v", name, task.Attachments)
	return core.Attachment{}
}

// boardAttachment reads a download the way a browser does — no JSON media type,
// because this is a GET — and hands back the headers, which are the whole of
// this route's security.
func boardAttachment(t *testing.T, url string) ([]byte, int, http.Header) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	contents, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return contents, response.StatusCode, response.Header
}
