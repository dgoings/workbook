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
	// Dropped marks a file whose bytes a compaction did not carry forward. The
	// entry keeps everything else it was, so a reader still learns that the file
	// existed, what it was called and how large it was. Only a history.checkpoint
	// can carry one: attachment.add carries AttachmentData, which has no drop.
	Dropped *AttachmentDrop `json:"dropped,omitempty"`
}

// AttachmentDrop records which compaction run dropped a file's bytes, and when.
// The time is display metadata, the way every wall time in a task is.
type AttachmentDrop struct {
	RunID string    `json:"runId"`
	At    time.Time `json:"at"`
}

// IsDropped reports a file whose bytes compaction removed.
func (a Attachment) IsDropped() bool {
	return a.Dropped != nil
}

// DropFileAttachments marks every live file attachment as dropped by run at
// at, and reports how many it marked and the bytes they held. Links store
// nothing and are left unchanged, and a file an earlier run dropped keeps that
// run's drop and is not counted again. The input is not modified.
func DropFileAttachments(attachments []Attachment, runID string, at time.Time) (dropped int, bytes int64, out []Attachment) {
	out = copyAttachments(attachments)
	for index := range out {
		if out[index].Kind != AttachmentFile || out[index].IsDropped() {
			continue
		}
		out[index].Dropped = &AttachmentDrop{RunID: runID, At: at}
		dropped++
		bytes += out[index].Size
	}
	return dropped, bytes, out
}

// DroppedAttachmentError is the refusal every surface gives for a dropped
// file's bytes. It is not-found rather than a validation failure because the
// caller asked for something real that no longer exists, and it names the day
// so a reader can find the compaction that removed it. The attachment must be
// dropped.
func DroppedAttachmentError(attachment Attachment) error {
	return Errorf(CategoryNotFound,
		"attachment %q was removed when the task was compacted on %s",
		attachment.Name, attachment.Dropped.At.Format("2006-01-02"))
}

// sameDrop compares two drops by value; the times are compared as instants.
func sameDrop(left, right *AttachmentDrop) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.RunID == right.RunID && left.At.Equal(right.At)
}

// validateAttachmentDrops checks the drops a checkpoint carries. Only a file
// has bytes to drop, and a drop names its run and its time.
func validateAttachmentDrops(attachments []Attachment) error {
	for _, attachment := range attachments {
		if attachment.Dropped == nil {
			continue
		}
		if attachment.Kind != AttachmentFile {
			return corrupt("attachment %s is a %s and cannot be dropped", attachment.ID, attachment.Kind)
		}
		if err := validateCanonicalULID("attachment drop run ID", attachment.Dropped.RunID); err != nil {
			return err
		}
		if attachment.Dropped.At.IsZero() {
			return corrupt("attachment %s drop requires a time", attachment.ID)
		}
	}
	return nil
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
			left[index].AttachmentData != right[index].AttachmentData ||
			!sameDrop(left[index].Dropped, right[index].Dropped) {
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
// account for. Links count nothing, because they store nothing, and neither
// does a file compaction dropped, whose bytes are no longer kept.
//
// It is exported because it is the number the mutation boundary prices a new
// attachment against and the number a reader is entitled to see; deriving it
// twice would let the two disagree about what "live" means.
func LiveAttachmentBytes(attachments []Attachment) int64 {
	var total int64
	for _, attachment := range attachments {
		if attachment.Kind == AttachmentFile && !attachment.IsDropped() {
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

// IsScriptableImageMediaType reports a media type Workbook refuses to store,
// because storing it would be storing a lie with consequences.
//
// An SVG is a document that can carry script, and it is the one image type for
// which "serve every image inline" and "never serve attacker-controlled markup
// on our own origin" contradict each other. The extension table above already
// declines to *derive* an SVG type, but a caller may name one — the web upload
// will pass a browser-supplied type straight through — so the rule cannot live
// in the table alone. It is asked at the authoring boundary, where somebody can
// be told, and again of any stored document, so that no clone can hand the web
// board an attachment labelled as an image that a browser would execute.
//
// The comparison is case-insensitive even though the stored-document pattern
// only admits lowercase: this is a security rule, and a security rule that
// depends on another rule's charset is one refactor from being no rule.
func IsScriptableImageMediaType(media string) bool {
	switch strings.ToLower(strings.TrimSpace(media)) {
	case "image/svg+xml", "image/svg":
		return true
	}
	return false
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
		if IsScriptableImageMediaType(data.Media) {
			return corrupt("attachment media type %q is not storable", data.Media)
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
