package core

import (
	"context"
	"strconv"
	"strings"
)

// CommentChange is one comment intent riding on an update.
//
// The three shapes are an addition (no identifier, a body), an edit (an
// identifier and a body), and a removal (an identifier and Remove). Remove is
// what separates a removal from an edit rather than an empty body doing it,
// because a blank body is a refusal and not a request to delete.
type CommentChange struct {
	// CommentID names the comment an edit or a removal acts on. An addition
	// leaves it empty: the comment's identifier is minted with the operation.
	CommentID string
	// Body is the text an addition or an edit records.
	Body string
	// Remove turns the change into a removal.
	Remove bool
}

// AttachmentChange is one attachment intent riding on an update.
type AttachmentChange struct {
	// AttachmentID names the attachment a removal acts on.
	AttachmentID string
	// Remove turns the change into a removal.
	Remove bool
	// Kind selects what is being attached.
	Kind AttachmentKind
	// Name is a file's name, and Content its bytes. The bytes are held in
	// memory because they are bounded by MaxAttachmentFileBytes and because the
	// object has to be hashed before the operation naming it can be built.
	Name    string
	Content []byte
	// Media overrides the media type Workbook would derive from the name.
	Media string
	// URL and Label describe a link.
	URL   string
	Label string
}

// CommentAddInput, and the four inputs after it, are the single-intent doors on
// to the same machinery `update` uses.
//
// Each builds an UpdateInput carrying exactly one change and hands it to
// UpdateMutation, so there is one implementation of what a comment operation
// means and one pack shape, whether a caller adds a comment on its own or adds
// one while closing the task. The command line's `update X --status done
// --comment "shipped"` is therefore one pack, one commit, and one refusal
// surface by construction rather than by a second code path that agrees.
type CommentAddInput struct {
	Body string
	// ExpectedHead carries the same meaning it does on UpdateInput.
	ExpectedHead string
}

type CommentEditInput struct {
	CommentID    string
	Body         string
	ExpectedHead string
}

type CommentRemoveInput struct {
	CommentID    string
	ExpectedHead string
}

type AttachmentAddInput struct {
	Kind    AttachmentKind
	Name    string
	Content []byte
	Media   string
	URL     string
	Label   string

	ExpectedHead string
}

type AttachmentRemoveInput struct {
	AttachmentID string
	ExpectedHead string
}

func (s Service) CommentAddMutation(ctx context.Context, idOrPrefix string, input CommentAddInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Comments:     []CommentChange{{Body: input.Body}},
	})
}

func (s Service) CommentEditMutation(ctx context.Context, idOrPrefix string, input CommentEditInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Comments:     []CommentChange{{CommentID: input.CommentID, Body: input.Body}},
	})
}

func (s Service) CommentRemoveMutation(ctx context.Context, idOrPrefix string, input CommentRemoveInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Comments:     []CommentChange{{CommentID: input.CommentID, Remove: true}},
	})
}

func (s Service) AttachmentAddMutation(ctx context.Context, idOrPrefix string, input AttachmentAddInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Attachments: []AttachmentChange{{
			Kind:    input.Kind,
			Name:    input.Name,
			Content: input.Content,
			Media:   input.Media,
			URL:     input.URL,
			Label:   input.Label,
		}},
	})
}

func (s Service) AttachmentRemoveMutation(ctx context.Context, idOrPrefix string, input AttachmentRemoveInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Attachments:  []AttachmentChange{{AttachmentID: input.AttachmentID, Remove: true}},
	})
}

// threadOperations turns an update's comment and attachment intents into the
// operations that carry them.
//
// This is the mutation boundary the design puts every refusal at. Editing or
// removing something that is not there is refused here and tolerated by the
// fold, which is not an inconsistency but the whole rule: a person naming a
// comment that does not exist has made a mistake and can be told, while a pack
// arriving from a peer is history and the only question left is what it means.
func (s Service) threadOperations(ctx context.Context, task TaskData, input UpdateInput) ([]Operation, error) {
	operations := make([]Operation, 0, len(input.Comments)+len(input.Attachments))
	for _, change := range input.Comments {
		operation, err := s.commentOperation(task, change)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	for _, change := range input.Attachments {
		operation, err := s.attachmentOperation(ctx, task, change)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func (s Service) commentOperation(task TaskData, change CommentChange) (Operation, error) {
	if change.Remove {
		if change.CommentID == "" {
			return Operation{}, Errorf(CategoryValidation, "removing a comment requires its ID")
		}
		if findComment(task.Comments, change.CommentID) < 0 {
			return Operation{}, Errorf(CategoryNotFound, "task has no comment %s", change.CommentID)
		}
		return Operation{Type: OperationCommentRemove, CommentID: change.CommentID}, nil
	}

	// The body is trimmed here and nowhere else. Trimming inside the fold would
	// make a stored comment differ from its own canonical form and read as
	// corruption on the clone that wrote it, so what is stored is what the
	// boundary decided to store.
	body := strings.TrimSpace(change.Body)
	if body == "" {
		return Operation{}, Errorf(CategoryValidation, "comment body must not be blank")
	}
	if change.CommentID == "" {
		return Operation{Type: OperationCommentAdd, Body: body}, nil
	}
	index := findComment(task.Comments, change.CommentID)
	if index < 0 {
		return Operation{}, Errorf(CategoryNotFound, "task has no comment %s", change.CommentID)
	}
	if task.Comments[index].Body == body {
		return Operation{}, Errorf(CategoryValidation, "comment %s already reads that way", change.CommentID)
	}
	return Operation{Type: OperationCommentEdit, CommentID: change.CommentID, Body: body}, nil
}

func (s Service) attachmentOperation(ctx context.Context, task TaskData, change AttachmentChange) (Operation, error) {
	if change.Remove {
		if change.AttachmentID == "" {
			return Operation{}, Errorf(CategoryValidation, "removing an attachment requires its ID")
		}
		if findAttachment(task.Attachments, change.AttachmentID) < 0 {
			return Operation{}, Errorf(CategoryNotFound, "task has no attachment %s", change.AttachmentID)
		}
		return Operation{Type: OperationAttachmentRemove, AttachmentID: change.AttachmentID}, nil
	}

	switch change.Kind {
	case AttachmentLink:
		if err := ValidateAttachmentURL(change.URL); err != nil {
			return Operation{}, err
		}
		return Operation{Type: OperationAttachmentAdd, Attachment: &AttachmentData{
			Kind:  AttachmentLink,
			URL:   change.URL,
			Label: strings.TrimSpace(change.Label),
		}}, nil
	case AttachmentFile:
		return s.fileAttachmentOperation(ctx, change)
	default:
		return Operation{}, Errorf(CategoryValidation, "attachment kind must be file or link")
	}
}

func (s Service) fileAttachmentOperation(ctx context.Context, change AttachmentChange) (Operation, error) {
	name := strings.TrimSpace(change.Name)
	if name == "" {
		return Operation{}, Errorf(CategoryValidation, "attached file must have a name")
	}
	if strings.ContainsAny(name, "/\x00") {
		return Operation{}, Errorf(CategoryValidation, "attached file name must not contain a path separator")
	}
	// The size is priced before the bytes are hashed. Staging first would write
	// the object this refusal exists to avoid writing, and the refusal is the
	// same either way, so the cheap order is the correct one.
	size := int64(len(change.Content))
	if size == 0 {
		return Operation{}, Errorf(CategoryValidation, "attached file %s is empty", name)
	}
	if size > MaxAttachmentFileBytes {
		return Operation{}, Errorf(
			CategoryValidation,
			"attached file %s is %d bytes and must not exceed %d; attach a link instead",
			name, size, MaxAttachmentFileBytes,
		)
	}
	media := strings.TrimSpace(change.Media)
	if media == "" {
		media = AttachmentMediaType(name)
	} else if !mediaTypePattern.MatchString(media) {
		// Asked here as well as in the fold, because the two answers are
		// different sentences to different readers: the fold calls a malformed
		// media type corrupt data, which is right about a stored document and
		// wrong about a value somebody just typed.
		return Operation{}, Errorf(CategoryValidation, "attachment media type %q is invalid", media)
	}
	if s.Blobs == nil {
		return Operation{}, Errorf(CategoryOperational, "attachment blob store is not configured")
	}
	blob, err := s.Blobs.StageAttachment(ctx, s.Config, change.Content)
	if err != nil {
		return Operation{}, err
	}
	return Operation{Type: OperationAttachmentAdd, Attachment: &AttachmentData{
		Name:  name,
		Kind:  AttachmentFile,
		Media: media,
		Size:  size,
		Blob:  blob,
	}}, nil
}

// threadChangeSubjects names what a pack did to a task's thread, for the commit
// subject. It reports counts rather than bodies: a commit subject is one line
// that Git shows for every commit, and a comment is prose somebody wrote for
// the task rather than for the log.
func threadChangeSubjects(operations []Operation) []string {
	counts := map[OperationType]int{}
	for _, operation := range operations {
		counts[operation.Type]++
	}
	changes := make([]string, 0, 5)
	for _, entry := range []struct {
		operation OperationType
		noun      string
		verb      string
	}{
		{OperationCommentAdd, "comment", "added"},
		{OperationCommentEdit, "comment", "edited"},
		{OperationCommentRemove, "comment", "removed"},
		{OperationAttachmentAdd, "attachment", "added"},
		{OperationAttachmentRemove, "attachment", "removed"},
	} {
		count := counts[entry.operation]
		if count == 0 {
			continue
		}
		noun := entry.noun
		if count > 1 {
			noun = strconv.Itoa(count) + " " + noun + "s"
		}
		changes = append(changes, noun+" "+entry.verb)
	}
	return changes
}
