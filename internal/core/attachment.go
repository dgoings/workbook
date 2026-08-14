package core

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AttachmentKind names the two things a task can carry.
type AttachmentKind string

const (
	// AttachmentFile is bytes stored in the adding commit's tree.
	AttachmentFile AttachmentKind = "file"
	// AttachmentLink is a URL, and costs the repository nothing.
	AttachmentLink AttachmentKind = "link"
)

// DefaultAttachmentMedia is what a file whose name implies nothing is labelled.
const DefaultAttachmentMedia = "application/octet-stream"

// AttachmentData is what an attachment.add operation carries, and what the
// checkpoint stores about an attachment beyond its provenance.
//
// One list holds both kinds because a person attaching something is doing one
// thing, and a reader listing what is attached wants one list. The members each
// kind does not use are absent rather than empty: a link has no size and no
// blob, and a file has no URL.
type AttachmentData struct {
	// Name is a file's name, as the person attaching it had it. It is display
	// text and a download filename, never a path this build resolves.
	Name string         `json:"name,omitempty"`
	Kind AttachmentKind `json:"kind"`
	// Media is the media type a file's bytes should be served as.
	Media string `json:"media,omitempty"`
	// Size is a file's length in bytes, recorded so that a reader can price a
	// download without fetching the blob.
	Size int64 `json:"size,omitempty"`
	// Blob is the Git object ID of a file's bytes, which live in the tree of
	// the commit that added it. Storing the object ID rather than a path is
	// what makes reading an attachment one `cat-file` instead of a tree walk.
	Blob string `json:"blob,omitempty"`
	// URL is a link's destination.
	URL string `json:"url,omitempty"`
	// Label is a link's display text, and is optional: a link with none is
	// shown as its URL.
	Label string `json:"label,omitempty"`
}

// Attachment is one live attachment as the checkpoint materializes it: what was
// attached, plus who attached it and when. The provenance comes from the
// operation pack, exactly as a comment's does.
type Attachment struct {
	ID      string    `json:"id"`
	Author  string    `json:"author"`
	AddedAt time.Time `json:"addedAt"`
	AttachmentData
}

// normalizeAttachments orders and shapes the list, for the reasons
// normalizeComments records: identifier order is creation order and is the same
// on every clone, and an empty list is nil so a task with nothing attached
// encodes to the bytes it always did.
func normalizeAttachments(attachments []Attachment) []Attachment {
	if len(attachments) == 0 {
		return nil
	}
	normalized := append([]Attachment(nil), attachments...)
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].ID < normalized[j].ID
	})
	return normalized
}

func copyAttachments(attachments []Attachment) []Attachment {
	if attachments == nil {
		return nil
	}
	return append([]Attachment(nil), attachments...)
}

// SameAttachments compares two attachment lists member by member, for the
// reason SameComments records.
func SameAttachments(left, right []Attachment) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID ||
			left[index].Author != right[index].Author ||
			!left[index].AddedAt.Equal(right[index].AddedAt) ||
			left[index].AttachmentData != right[index].AttachmentData {
			return false
		}
	}
	return true
}

func findAttachment(attachments []Attachment, id string) int {
	for index, attachment := range attachments {
		if attachment.ID == id {
			return index
		}
	}
	return -1
}

// LiveAttachmentBytes reports how much file storage a task's live attachments
// account for. Links count nothing, because they store nothing.
//
// It is exported because it is the number the mutation boundary prices a new
// attachment against and the number a reader is entitled to see; deriving it
// twice would let the two disagree about what "live" means.
func LiveAttachmentBytes(attachments []Attachment) int64 {
	var total int64
	for _, attachment := range attachments {
		if attachment.Kind == AttachmentFile {
			total += attachment.Size
		}
	}
	return total
}

// applyAttachmentAdd records an attachment, tolerating one this task already
// holds under the same identifier for the reason applyCommentAdd records.
func applyAttachmentAdd(task *TaskData, operation Operation, authored authorship) error {
	if findAttachment(task.Attachments, operation.ID) >= 0 {
		return nil
	}
	task.Attachments = append(copyAttachments(task.Attachments), Attachment{
		ID:             operation.ID,
		Author:         authored.actor,
		AddedAt:        authored.at,
		AttachmentData: *operation.Attachment,
	})
	return nil
}

// applyAttachmentRemove drops an attachment from the live list and tolerates
// one that is already gone.
//
// Removal hides; it reclaims nothing. The bytes stay in the commit that added
// them, because that commit is shared append-only history and no clone may
// rewrite it. Reclaiming them is the compaction verb's job, and the reachability
// rule this design keeps — a blob is reachable only through its own task's ref
// history — is what will let compaction strip one task's attachments without
// reasoning about any other task.
func applyAttachmentRemove(task *TaskData, operation Operation) error {
	index := findAttachment(task.Attachments, operation.AttachmentID)
	if index < 0 {
		return nil
	}
	attachments := make([]Attachment, 0, len(task.Attachments)-1)
	attachments = append(attachments, task.Attachments[:index]...)
	attachments = append(attachments, task.Attachments[index+1:]...)
	task.Attachments = attachments
	return nil
}

// mediaTypePattern is the shape a stored media type must have. It is
// deliberately narrower than RFC 6838 — no parameters, no quoted strings —
// because the value is written into an HTTP response header by the web board,
// and the intersection of "a media type" and "a header value that needs no
// escaping" is what may be stored.
var mediaTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// objectIDPattern is the shape of a Git object ID, without deciding which hash
// produced it. Workbook supports SHA-1 and SHA-256 repositories and must not
// assume either, so what is checked is that the value is lowercase hexadecimal
// of an even, plausible width.
var objectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// attachmentMediaTypes maps a file extension to the media type Workbook labels
// it with when the caller names none.
//
// The table is built in rather than read from the operating system's mime.types
// on purpose. A media type is written into shared history, so two clones
// attaching the same file must label it the same way, and a lookup that depends
// on which packages a machine has installed would not.
//
// SVG is absent, and its absence is the point. The web board serves image/*
// inline and everything else as a download, and an SVG is a document that can
// carry script — labelling one as an image would put a stored cross-site
// scripting vector on the board's origin. An SVG is still attachable; it is
// simply served as bytes to save rather than as a picture to render.
var attachmentMediaTypes = map[string]string{
	".css":   "text/css",
	".csv":   "text/csv",
	".diff":  "text/x-diff",
	".gif":   "image/gif",
	".gz":    "application/gzip",
	".jpeg":  "image/jpeg",
	".jpg":   "image/jpeg",
	".json":  "application/json",
	".log":   "text/plain",
	".md":    "text/markdown",
	".patch": "text/x-diff",
	".pdf":   "application/pdf",
	".png":   "image/png",
	".tar":   "application/x-tar",
	".txt":   "text/plain",
	".webp":  "image/webp",
	".zip":   "application/zip",
}

// AttachmentMediaType reports the media type a file name implies.
//
// It is exported so that every surface that attaches a file — the command line
// today, the web board next — labels the same bytes the same way rather than
// each inventing a rule.
func AttachmentMediaType(name string) string {
	if media, found := attachmentMediaTypes[strings.ToLower(path.Ext(name))]; found {
		return media
	}
	return DefaultAttachmentMedia
}

// validateAttachmentOperation checks an attachment operation's shape and never
// its size, for the reason validateCommentOperation records.
func validateAttachmentOperation(operation Operation) error {
	if operation.Task != nil || operation.CommentID != "" ||
		operation.Body != "" || operation.Field != "" || operation.Value != "" {
		return corrupt("%s must not contain a payload for another operation", operation.Type)
	}
	switch operation.Type {
	case OperationAttachmentAdd:
		if operation.AttachmentID != "" {
			return corrupt("attachment.add must not name an attachment")
		}
		if operation.Attachment == nil {
			return corrupt("attachment.add must contain attachment data")
		}
		return validateAttachmentData(*operation.Attachment)
	case OperationAttachmentRemove:
		if operation.Attachment != nil {
			return corrupt("attachment.remove must not contain attachment data")
		}
		return validateCanonicalULID("attachment ID", operation.AttachmentID)
	}
	return nil
}

func validateAttachmentData(data AttachmentData) error {
	switch data.Kind {
	case AttachmentFile:
		if strings.TrimSpace(data.Name) == "" {
			return corrupt("attachment name must not be blank")
		}
		if strings.ContainsAny(data.Name, "/\x00") {
			return corrupt("attachment name must not contain a path separator")
		}
		if !objectIDPattern.MatchString(data.Blob) {
			return corrupt("attachment blob must be a full lowercase Git object ID")
		}
		if data.Size < 0 {
			return corrupt("attachment size %d is invalid", data.Size)
		}
		if data.Media != "" && !mediaTypePattern.MatchString(data.Media) {
			return corrupt("attachment media type %q is invalid", data.Media)
		}
		if data.URL != "" || data.Label != "" {
			return corrupt("a file attachment must not carry a link")
		}
	case AttachmentLink:
		if strings.TrimSpace(data.URL) == "" {
			return corrupt("attachment URL must not be blank")
		}
		if data.Blob != "" || data.Size != 0 || data.Media != "" || data.Name != "" {
			return corrupt("a link attachment must not carry file data")
		}
	default:
		return corrupt("unsupported attachment kind %q", data.Kind)
	}
	return nil
}

// ValidateAttachmentURL rejects a link Workbook will not store.
//
// The scheme rule is a boundary rule rather than a fold rule, so it is exported
// for the callers that author links and is deliberately not asked of a stored
// document. What it refuses is everything that is not http or https: a
// `javascript:` link is a script the board would run on somebody's click, a
// `file:` link points at a path that means something different on every
// machine, and a `data:` link is a document smuggled into a field sized for a
// URL.
func ValidateAttachmentURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return Errorf(CategoryValidation, "attachment URL must not be blank")
	}
	if len(raw) > MaxAttachmentURLBytes {
		return Errorf(
			CategoryValidation,
			"attachment URL is %d bytes and must not exceed %d",
			len(raw), MaxAttachmentURLBytes,
		)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return Wrap(CategoryValidation, "attachment URL is invalid", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Errorf(CategoryValidation, "attachment URL must use http or https")
	}
	if parsed.Host == "" {
		return Errorf(CategoryValidation, "attachment URL must name a host")
	}
	return nil
}
