package webui

import (
	"context"
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// The five thread mutations a board may be built with. Each takes core's own
// single-intent input, so this package decides nothing about what a comment or
// an attachment means: it decodes a request into the input the command line
// fills in from its flags, and both surfaces reach the same planner.
type (
	TaskCommentAdder      func(context.Context, string, core.CommentAddInput) (core.MutationResult, error)
	TaskCommentEditor     func(context.Context, string, core.CommentEditInput) (core.MutationResult, error)
	TaskCommentRemover    func(context.Context, string, core.CommentRemoveInput) (core.MutationResult, error)
	TaskAttachmentAdder   func(context.Context, string, core.AttachmentAddInput) (core.MutationResult, error)
	TaskAttachmentRemover func(context.Context, string, core.AttachmentRemoveInput) (core.MutationResult, error)
)

// TaskAttachmentFinder answers what one attachment on one task is. A task that
// carries no attachment under that identifier is a not-found answer, exactly as
// an unknown task is.
type TaskAttachmentFinder func(context.Context, string, string) (core.Attachment, error)

// AttachmentContentReader returns a file attachment's stored bytes. It takes
// the attachment rather than its identifier because the finder above has
// already read it, for the reason core.Service.AttachmentContent records.
type AttachmentContentReader func(context.Context, core.Attachment) ([]byte, error)

// The comment bodies. expectedHead is a plain string rather than the pointer
// the vocabulary bodies take, and optional as it is on every other task
// mutation: a comment is a change to one task, so a client that omits it gets
// exactly what omitting it on PATCH /api/tasks/{id} has always got, and the
// asymmetry vocabularyHead documents is preserved rather than re-decided here.
type addCommentRequest struct {
	Body         string `json:"body"`
	ExpectedHead string `json:"expectedHead"`
}

type editCommentRequest struct {
	Body         string `json:"body"`
	ExpectedHead string `json:"expectedHead"`
}

// removeCommentRequest and removeAttachmentRequest are bodies a client may omit
// entirely, for the reason deleteTaskRequest is: every member is optional, so a
// bare DELETE is a legal request and keeps meaning what it meant.
type removeCommentRequest struct {
	ExpectedHead string `json:"expectedHead"`
}

type removeAttachmentRequest struct {
	ExpectedHead string `json:"expectedHead"`
}

// addAttachmentRequest is one route for both kinds, because attaching is one
// act and the task holds one list.
//
// A file's bytes travel as base64 in a JSON member rather than as a multipart
// part. That is the guard's decision rather than a taste: GuardSameOrigin
// requires every mutating request to declare application/json precisely because
// multipart/form-data is one of the three types a cross-site form can send with
// no preflight, so a multipart upload route would be the one address on this
// board a drive-by page could post to.
type addAttachmentRequest struct {
	// Kind is "file" or "link", stated rather than inferred from which members
	// are present: the two kinds refuse different things, and a request that
	// carried a name and a URL would otherwise be silently read as one of them.
	Kind string `json:"kind"`
	// Name is the file's name, and Content its bytes as standard base64.
	Name    string `json:"name"`
	Content string `json:"content"`
	// Media names the media type these bytes should be stored as. It is
	// accepted and never required: the board's own upload leaves it out so that
	// the name decides through core's table, which is the only way two clones
	// attaching the same file label it the same way. A caller that does name one
	// is held to what core stores, which refuses the scriptable image types.
	Media string `json:"media"`
	// URL and Label describe a link.
	URL          string `json:"url"`
	Label        string `json:"label"`
	ExpectedHead string `json:"expectedHead"`
}

// inlineAttachmentMediaTypes is the allow-list of media types the download
// route may serve inline, and it is a list rather than a test on the type.
//
// It holds exactly the image types core.AttachmentMediaType can derive. Two
// properties of that set matter and neither survives a prefix test: every
// member is a raster image, which a browser renders as pixels and nothing else,
// and no member is a document. `image/svg+xml` is the reason the rule is
// written this way — an SVG is markup that can carry script, so "starts with
// image/" would put stored cross-site scripting on the board's own origin the
// day anything hands the board one. Core refuses to store that type at all;
// this list is the second lock, and it is a lock that holds for a type nobody
// has thought of yet, because a type that is not written here is served as a
// download whatever it is called.
var inlineAttachmentMediaTypes = map[string]struct{}{
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

// attachmentSecurityPolicy is the content policy an attachment's own bytes are
// served under, and it is deliberately not the page's.
//
// The page's policy permits inline script, because the board is one HTML file
// whose script is inline. An attachment is somebody's file: it must never be
// evaluated under a policy that permits anything, and `sandbox` denies whatever
// it might be an origin to act on even if a browser were persuaded to render it.
const attachmentSecurityPolicy = "default-src 'none'; sandbox"

func (handler *handler) addTaskComment(writer http.ResponseWriter, request *http.Request) {
	if handler.AddComment == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "comment addition is not configured"))
		return
	}
	var body addCommentRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode comment add", err))
		return
	}
	result, err := handler.AddComment(request.Context(), taskCollectionID(request, taskCommentsPathID), core.CommentAddInput{
		Body:         body.Body,
		ExpectedHead: body.ExpectedHead,
	})
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) editTaskComment(writer http.ResponseWriter, request *http.Request) {
	if handler.EditComment == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "comment editing is not configured"))
		return
	}
	var body editCommentRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode comment edit", err))
		return
	}
	taskID, commentID := taskMemberIDs(request, "comment", taskCommentPathIDs)
	result, err := handler.EditComment(request.Context(), taskID, core.CommentEditInput{
		CommentID:    commentID,
		Body:         body.Body,
		ExpectedHead: body.ExpectedHead,
	})
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) removeTaskComment(writer http.ResponseWriter, request *http.Request) {
	if handler.RemoveComment == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "comment removal is not configured"))
		return
	}
	var body removeCommentRequest
	if err := decodeOptionalRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode comment removal", err))
		return
	}
	taskID, commentID := taskMemberIDs(request, "comment", taskCommentPathIDs)
	result, err := handler.RemoveComment(request.Context(), taskID, core.CommentRemoveInput{
		CommentID:    commentID,
		ExpectedHead: body.ExpectedHead,
	})
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) addTaskAttachment(writer http.ResponseWriter, request *http.Request) {
	if handler.AddAttachment == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "attachment addition is not configured"))
		return
	}
	var body addAttachmentRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode attachment add", err))
		return
	}
	input, err := body.input()
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	result, err := handler.AddAttachment(request.Context(), taskCollectionID(request, taskAttachmentsPathID), input)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

// input turns one upload into core's own attachment input, refusing the two
// things this route can see and core cannot: a body that mixes the kinds, and
// bytes that are not base64 or are past the ceiling.
//
// The size is refused here rather than only in core because refusing it here
// refuses it before the service is called at all — the same order core itself
// keeps, and for the same reason: an over-sized upload must not write the very
// megabyte the ceiling exists to keep out, leaving it for a later `git gc` to
// find. The number and the wording are core's, so the two cannot disagree about
// what is allowed.
//
// It is the size rule alone that runs ahead of staging, here as in core. The
// ceilings asked after the fold — an attachment name past its own, most of all
// — stage the blob and then decline the attachment, which is the mutation
// boundary's order rather than this route's, and is why nothing here claims
// that a refused upload never leaves an object behind.
func (body addAttachmentRequest) input() (core.AttachmentAddInput, error) {
	input := core.AttachmentAddInput{
		Media:        body.Media,
		ExpectedHead: body.ExpectedHead,
	}
	switch core.AttachmentKind(strings.TrimSpace(body.Kind)) {
	case core.AttachmentFile:
		if body.URL != "" || body.Label != "" {
			return core.AttachmentAddInput{}, core.Errorf(core.CategoryInvocation,
				"an attached file carries no url or label; attach a link for that")
		}
		content, err := decodeAttachmentContent(body.Name, body.Content)
		if err != nil {
			return core.AttachmentAddInput{}, err
		}
		input.Kind = core.AttachmentFile
		input.Name = body.Name
		input.Content = content
	case core.AttachmentLink:
		if body.Name != "" || body.Content != "" || body.Media != "" {
			return core.AttachmentAddInput{}, core.Errorf(core.CategoryInvocation,
				"an attached link carries no name, content or media type; attach a file for that")
		}
		input.Kind = core.AttachmentLink
		input.URL = body.URL
		input.Label = body.Label
	default:
		return core.AttachmentAddInput{}, core.Errorf(core.CategoryValidation,
			"attachment kind must be %q or %q", core.AttachmentFile, core.AttachmentLink)
	}
	return input, nil
}

func decodeAttachmentContent(name, encoded string) ([]byte, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, core.Errorf(core.CategoryValidation,
			"an attached file carries its bytes as base64 in content")
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, core.Errorf(core.CategoryValidation,
			"attachment content must be standard base64")
	}
	if len(content) > core.MaxAttachmentFileBytes {
		return nil, core.Errorf(
			core.CategoryValidation,
			"attached file %s is %d bytes and must not exceed %d; attach a link instead",
			strings.TrimSpace(name), len(content), core.MaxAttachmentFileBytes,
		)
	}
	return content, nil
}

func (handler *handler) removeTaskAttachment(writer http.ResponseWriter, request *http.Request) {
	if handler.RemoveAttachment == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "attachment removal is not configured"))
		return
	}
	var body removeAttachmentRequest
	if err := decodeOptionalRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode attachment removal", err))
		return
	}
	taskID, attachmentID := taskMemberIDs(request, "attachment", taskAttachmentPathIDs)
	result, err := handler.RemoveAttachment(request.Context(), taskID, core.AttachmentRemoveInput{
		AttachmentID: attachmentID,
		ExpectedHead: body.ExpectedHead,
	})
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

// serveTaskAttachment hands back one attached file's bytes.
//
// It is the one route on this board that answers with somebody else's content
// rather than with this board's own JSON, so it is the one route where a header
// is a security decision:
//
//   - The media type is served only if it is in the inline allow-list above,
//     and every other attachment is served as application/octet-stream. A
//     download does not need a type, and an opaque one cannot be rendered by any
//     browser under any disposition.
//   - Content-Disposition is `attachment` for everything outside that list, so a
//     file is saved rather than opened, and the file name inside it is formatted
//     rather than concatenated — an attachment name is text somebody typed, and
//     core forbids only a path separator in it.
//   - nosniff is set here as well as by writeSecurityHeaders, because on this
//     route it is load-bearing rather than hygienic: without it a browser may
//     decide for itself that an octet-stream is HTML.
//   - The content policy is replaced with the attachment one, which permits
//     nothing at all.
//
// A link refuses: it holds no bytes, and the refusal names the URL so a client
// that followed the wrong address is told where the thing actually is. An
// attachment whose blob this clone does not have is core's own not-found, which
// is what a compacted history will produce.
func (handler *handler) serveTaskAttachment(writer http.ResponseWriter, request *http.Request) {
	if handler.Attachment == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "attachment reading is not configured"))
		return
	}
	taskID, attachmentID := taskMemberIDs(request, "attachment", taskAttachmentPathIDs)
	attachment, err := handler.Attachment(request.Context(), taskID, attachmentID)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	if attachment.Kind != core.AttachmentFile {
		handler.writeError(writer, core.Errorf(core.CategoryValidation,
			"attachment %s is a link to %s and holds no bytes to download; open the link itself",
			attachment.ID, attachment.URL))
		return
	}
	if handler.AttachmentContent == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "attachment content reading is not configured"))
		return
	}
	content, err := handler.AttachmentContent(request.Context(), attachment)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	media, disposition := attachmentDelivery(attachment.Media)
	writer.Header().Set("Content-Type", media)
	writer.Header().Set("Content-Disposition", contentDisposition(disposition, attachment.Name))
	// Kept, though writeSecurityHeaders has already set it for every response on
	// this board. Elsewhere it is hygiene over JSON this package wrote; here it
	// is the rule that stops a browser deciding for itself that somebody else's
	// bytes are HTML, and a rule that load-bearing is stated where it is relied
	// on rather than inherited from a helper a later edit could narrow.
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", attachmentSecurityPolicy)
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

// attachmentDelivery decides the two headers together, so that no future edit
// can move one without the other: a type that is served inline is a type from
// the allow-list, and everything else is opaque bytes to save.
func attachmentDelivery(stored string) (string, string) {
	media := strings.ToLower(strings.TrimSpace(stored))
	if _, inline := inlineAttachmentMediaTypes[media]; inline {
		return media, "inline"
	}
	return core.DefaultAttachmentMedia, "attachment"
}

// contentDisposition formats the header, name and all.
//
// mime.FormatMediaType is what does the formatting rather than a quoted
// concatenation, because an attachment name is somebody's text: core refuses a
// path separator and a NUL in it and nothing else, so a name may hold a quote,
// a backslash, a newline, or a right-to-left override. Formatting escapes the
// quotable ones and percent-encodes the rest as RFC 2231, and a name it cannot
// format at all costs the response its file name rather than its safety.
func contentDisposition(disposition, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return disposition
	}
	formatted := mime.FormatMediaType(disposition, map[string]string{"filename": name})
	if formatted == "" {
		return disposition
	}
	return formatted
}

// taskCollectionID and taskMemberIDs read the identifiers a route addresses,
// from the mux's pattern where there is one and from the path where a caller
// built the request itself — the fallback every route in this package keeps.
func taskCollectionID(request *http.Request, fromPath func(string) string) string {
	if id := request.PathValue("id"); id != "" {
		return id
	}
	return fromPath(request.URL.Path)
}

func taskMemberIDs(request *http.Request, member string, fromPath func(string) (string, string, bool)) (string, string) {
	taskID, memberID := request.PathValue("id"), request.PathValue(member)
	if taskID != "" && memberID != "" {
		return taskID, memberID
	}
	pathTaskID, pathMemberID, ok := fromPath(request.URL.Path)
	if !ok {
		return taskID, memberID
	}
	return pathTaskID, pathMemberID
}
