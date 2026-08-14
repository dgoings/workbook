package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// threadOptions are `update`'s comment and attachment flags.
//
// They ride the existing verb rather than becoming verbs of their own because a
// comment and an attachment are nouns on a task, and because riding `update` is
// what makes `update X --status done --comment "shipped"` one pack: core takes
// every intent in one UpdateInput, so composing them here costs nothing and
// splitting them into their own verbs would have cost the atomicity.
//
// The flag matrix, which the help text also states:
//
//	--comment <body>                          add a comment
//	--edit-comment <id> --comment <body>      replace that comment's body
//	--edit-comment <id>                       invocation error: no new body
//	--remove-comment <id>                     remove a comment
//	--attach-file <path>                      attach a file's bytes
//	--attach-url <url> [--attach-label <t>]   attach a link
//	--attach-label <text>                     invocation error: nothing to label
//	--remove-attachment <id>                  remove an attachment
//
// The body of an edit travels in --comment because a flag carries one value, so
// --comment means "add" alone and "the new body" beside --edit-comment. Every
// other pairing is a separate intent and composes: --remove-comment with
// --comment removes one comment and writes another, in one pack.
type threadOptions struct {
	comment          *string
	editComment      *string
	removeComment    *string
	attachFile       *string
	attachURL        *string
	attachLabel      *string
	removeAttachment *string
}

func registerThreadFlags(flags *commandFlagSet) *threadOptions {
	return &threadOptions{
		comment:          flags.String("comment", "", "comment to add, or the new body with --edit-comment"),
		editComment:      flags.String("edit-comment", "", "replace this comment's body with --comment"),
		removeComment:    flags.String("remove-comment", "", "remove this comment"),
		attachFile:       flags.String("attach-file", "", "attach this file's contents"),
		attachURL:        flags.String("attach-url", "", "attach this link"),
		attachLabel:      flags.String("attach-label", "", "display text for --attach-url"),
		removeAttachment: flags.String("remove-attachment", "", "remove this attachment"),
	}
}

// threadRequest is what the flags asked for, before any identifier a person
// typed has been matched against what the task holds.
type threadRequest struct {
	comments    []core.CommentChange
	attachments []core.AttachmentChange
}

// read turns the flags a caller actually typed into intents, refusing the
// combinations that have no meaning before anything is read or written.
//
// Which flags were typed is read from flags.Visit rather than from the values,
// because an explicitly blank value is not the same as an absent one and the
// two must not be confused: `--edit-comment ""` with a body would otherwise
// reach core as an addition — an identifier core reads as "none given" — and
// silently write a new comment instead of editing one.
//
// A blank value core judges correctly is left to core, which is the mutation
// boundary and owns those refusals. A blank --attach-file is refused here
// instead, because a path is the one value this command interprets itself and
// an empty one never reaches core at all.
func (options *threadOptions) read(flags *commandFlagSet) (threadRequest, error) {
	typed := map[string]bool{}
	flags.Visit(func(visited *flag.Flag) {
		typed[visited.Name] = true
	})

	if typed["edit-comment"] {
		if !typed["comment"] {
			return threadRequest{}, core.Errorf(
				core.CategoryInvocation,
				"update --edit-comment requires --comment for the new body",
			)
		}
		if *options.editComment == "" {
			return threadRequest{}, core.Errorf(core.CategoryInvocation, "update --edit-comment requires a comment ID")
		}
	}
	if typed["attach-label"] && !typed["attach-url"] {
		return threadRequest{}, core.Errorf(core.CategoryInvocation, "update --attach-label requires --attach-url")
	}
	if typed["attach-file"] && *options.attachFile == "" {
		return threadRequest{}, core.Errorf(core.CategoryInvocation, "update --attach-file requires a path")
	}

	var request threadRequest
	switch {
	case typed["edit-comment"]:
		request.comments = append(request.comments, core.CommentChange{
			CommentID: *options.editComment,
			Body:      *options.comment,
		})
	case typed["comment"]:
		request.comments = append(request.comments, core.CommentChange{Body: *options.comment})
	}
	if typed["remove-comment"] {
		request.comments = append(request.comments, core.CommentChange{
			CommentID: *options.removeComment,
			Remove:    true,
		})
	}
	if typed["attach-file"] {
		name, content, err := readAttachmentFile(*options.attachFile)
		if err != nil {
			return threadRequest{}, err
		}
		request.attachments = append(request.attachments, core.AttachmentChange{
			Kind:    core.AttachmentFile,
			Name:    name,
			Content: content,
		})
	}
	if typed["attach-url"] {
		request.attachments = append(request.attachments, core.AttachmentChange{
			Kind:  core.AttachmentLink,
			URL:   *options.attachURL,
			Label: *options.attachLabel,
		})
	}
	if typed["remove-attachment"] {
		request.attachments = append(request.attachments, core.AttachmentChange{
			AttachmentID: *options.removeAttachment,
			Remove:       true,
		})
	}
	return request, nil
}

// readAttachmentFile reads what --attach-file names, and refuses a file too
// large to attach before reading a byte of it.
//
// Core refuses the same file, and its refusal is the authority; this one exists
// so that pointing --attach-file at a gigabyte costs a stat rather than a
// gigabyte of memory. The two sentences are deliberately identical, because a
// file that grows between this stat and the read below is refused by core with
// the message this refusal would have used.
func readAttachmentFile(path string) (string, []byte, error) {
	name := filepath.Base(path)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		// A path nobody has is the caller's mistake and reads as not-found, the
		// same verdict a task ID nobody holds gets. Anything else about the
		// file system — a permission, a broken mount — is this machine's
		// problem rather than the command's, and says so.
		return "", nil, core.Wrap(core.CategoryNotFound, "cannot attach "+singleLine(path), err)
	}
	if err != nil {
		return "", nil, core.Wrap(core.CategoryOperational, "cannot attach "+singleLine(path), err)
	}
	if info.IsDir() {
		return "", nil, core.Errorf(core.CategoryValidation, "cannot attach %s, which is a directory", singleLine(path))
	}
	if info.Size() > core.MaxAttachmentFileBytes {
		return "", nil, core.Errorf(
			core.CategoryValidation,
			"attached file %s is %d bytes and must not exceed %d; attach a link instead",
			singleLine(name), info.Size(), core.MaxAttachmentFileBytes,
		)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, core.Wrap(core.CategoryOperational, "cannot read "+singleLine(path), err)
	}
	return name, content, nil
}

// resolve matches every identifier a person typed against what the task holds,
// and describes what the pack is about to do.
//
// It runs inside the mutation, after the fetch, so the thread it resolves
// against is the one the mutation will apply to. A comment a teammate removed
// in between is still refused — by core, against the same state — so this is a
// convenience rather than a check: what it buys is that an identifier can be
// typed as an unambiguous prefix, exactly as a task ID can be, and that the
// outcome can name the comment a removal removed.
func (request threadRequest) resolve(
	ctx context.Context,
	service core.Service,
	idOrPrefix string,
) (threadRequest, []core.FieldChange, error) {
	if !request.namesStoredItem() {
		return request, request.describe(core.Task{}), nil
	}
	task, err := service.Show(ctx, idOrPrefix)
	if err != nil {
		return threadRequest{}, nil, err
	}

	resolved := threadRequest{
		comments:    append([]core.CommentChange(nil), request.comments...),
		attachments: append([]core.AttachmentChange(nil), request.attachments...),
	}
	for index, change := range resolved.comments {
		if change.CommentID == "" {
			continue
		}
		id, err := resolveThreadID("comment", commentIDs(task), change.CommentID)
		if err != nil {
			return threadRequest{}, nil, err
		}
		resolved.comments[index].CommentID = id
	}
	for index, change := range resolved.attachments {
		if change.AttachmentID == "" {
			continue
		}
		id, err := resolveThreadID("attachment", attachmentIDs(task), change.AttachmentID)
		if err != nil {
			return threadRequest{}, nil, err
		}
		resolved.attachments[index].AttachmentID = id
	}
	return resolved, resolved.describe(task), nil
}

// namesStoredItem reports whether anything here refers to something the task
// already holds. Nothing else needs the task read, so an update that only adds
// a comment costs exactly what it cost before this flag existed.
func (request threadRequest) namesStoredItem() bool {
	for _, change := range request.comments {
		if change.CommentID != "" {
			return true
		}
	}
	for _, change := range request.attachments {
		if change.AttachmentID != "" {
			return true
		}
	}
	return false
}

// describe names what these intents do to the thread, in the shape the change
// log names it in, so that the line a mutation prints and the line `show
// --history` prints later read the same way.
//
// An edit reports the body it replaced, which the change log cannot: the log
// summarizes one commit and recovering the previous body there would mean
// replaying the thread, while this ran against the state the pack applies to
// and simply has it.
func (request threadRequest) describe(task core.Task) []core.FieldChange {
	changes := make([]core.FieldChange, 0, len(request.comments)+len(request.attachments))
	for _, change := range request.comments {
		switch {
		case change.Remove:
			changes = append(changes, core.FieldChange{
				Field: "comment",
				Kind:  core.ChangeRemoved,
				From:  commentBodyOf(task, change.CommentID),
			})
		case change.CommentID != "":
			changes = append(changes, core.FieldChange{
				Field: "comment",
				Kind:  core.ChangeSet,
				From:  commentBodyOf(task, change.CommentID),
				To:    strings.TrimSpace(change.Body),
			})
		default:
			changes = append(changes, core.FieldChange{
				Field: "comment",
				Kind:  core.ChangeAdded,
				To:    strings.TrimSpace(change.Body),
			})
		}
	}
	for _, change := range request.attachments {
		if change.Remove {
			changes = append(changes, core.FieldChange{
				Field: "attachment",
				Kind:  core.ChangeRemoved,
				From:  attachmentSubjectOf(task, change.AttachmentID),
			})
			continue
		}
		changes = append(changes, core.FieldChange{
			Field: "attachment",
			Kind:  core.ChangeAdded,
			To:    attachmentChangeSubject(change),
		})
	}
	return changes
}

func commentIDs(task core.Task) []string {
	ids := make([]string, 0, len(task.Comments))
	for _, comment := range task.Comments {
		ids = append(ids, comment.ID)
	}
	return ids
}

func attachmentIDs(task core.Task) []string {
	ids := make([]string, 0, len(task.Attachments))
	for _, attachment := range task.Attachments {
		ids = append(ids, attachment.ID)
	}
	return ids
}

func commentBodyOf(task core.Task, id string) string {
	for _, comment := range task.Comments {
		if comment.ID == id {
			return comment.Body
		}
	}
	return ""
}

func attachmentSubjectOf(task core.Task, id string) string {
	for _, attachment := range task.Attachments {
		if attachment.ID == id {
			return attachmentSubject(attachment)
		}
	}
	return ""
}

// attachmentSubject is how an attachment reads in one line: a file by its name,
// a link by its label or, having none, by its URL.
func attachmentSubject(attachment core.Attachment) string {
	if attachment.Kind == core.AttachmentLink {
		if attachment.Label != "" {
			return attachment.Label
		}
		return attachment.URL
	}
	return attachment.Name
}

func attachmentChangeSubject(change core.AttachmentChange) string {
	if change.Kind == core.AttachmentLink {
		if label := strings.TrimSpace(change.Label); label != "" {
			return label
		}
		return change.URL
	}
	return change.Name
}

// resolveThreadID expands the identifier a person typed into the one stored,
// on the terms every task ID is already typed under: case-insensitively, by
// unambiguous prefix, with an exact match always winning.
//
// The identifiers are ULIDs minted a few at a time, so two items added within
// the same fraction of a second share a long prefix. That is why `show` prints
// them whole rather than shortened: a display that could not be typed back is
// worse than a long one, and a prefix stays available to anyone who wants it.
func resolveThreadID(noun string, ids []string, typed string) (string, error) {
	needle := strings.ToUpper(strings.TrimSpace(typed))
	if needle == "" {
		return "", core.Errorf(core.CategoryValidation, "%s ID must not be blank", noun)
	}
	for _, id := range ids {
		if id == needle {
			return id, nil
		}
	}
	match := ""
	for _, id := range ids {
		if !strings.HasPrefix(id, needle) {
			continue
		}
		if match != "" {
			return "", core.Errorf(core.CategoryValidation, "%s ID prefix %q is ambiguous", noun, typed)
		}
		match = id
	}
	if match == "" {
		return "", core.Errorf(core.CategoryNotFound, "no %s matches %q", noun, typed)
	}
	return match, nil
}

// attachmentOutput is `show --get-attachment`: one attached file's bytes,
// written where the caller asked for them.
type attachmentOutput struct {
	// Target is the attachment ID or prefix to write.
	Target string
	// Out is the file to write, or empty for standard output.
	Out string
}

// runShowAttachment writes an attachment's bytes rather than a task.
//
// It is a separate path through `show` because its output is bytes rather than
// a rendered task: nothing else about show applies to it, which is why every
// other show option is refused beside it rather than ignored.
func runShowAttachment(
	ctx context.Context,
	cwd, idOrPrefix string,
	output attachmentOutput,
	stdout, stderr io.Writer,
) error {
	service, repository, err := openReadServiceWithRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	task, err := service.Show(ctx, idOrPrefix)
	if err != nil {
		return err
	}
	id, err := resolveThreadID("attachment", attachmentIDs(task), output.Target)
	if err != nil {
		return err
	}
	attachment, found := attachmentByID(task, id)
	if !found {
		return core.Errorf(core.CategoryNotFound, "no attachment matches %q", output.Target)
	}
	if attachment.Kind == core.AttachmentLink {
		// A link stores nothing, so there is nothing to write. The URL is the
		// answer to what the caller was reaching for, so it is what the refusal
		// hands back rather than a bare "wrong kind".
		return core.Errorf(
			core.CategoryValidation,
			"attachment %s is a link and holds no bytes; it points at %s",
			id, singleLine(attachment.URL),
		)
	}
	content, err := repository.ReadAttachment(ctx, service.Config, attachment.Blob)
	if err != nil {
		return err
	}
	if output.Out == "" {
		if _, err := stdout.Write(content); err != nil {
			return core.Wrap(core.CategoryOperational, "cannot write attachment to standard output", err)
		}
		return nil
	}
	if err := os.WriteFile(output.Out, content, 0o644); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot write "+singleLine(output.Out), err)
	}
	fmt.Fprintf(stdout, "Wrote:\t%s\t%s\t%d bytes\n", singleLine(output.Out), singleLine(attachment.Name), len(content))
	return nil
}

func attachmentByID(task core.Task, id string) (core.Attachment, bool) {
	for _, attachment := range task.Attachments {
		if attachment.ID == id {
			return attachment, true
		}
	}
	return core.Attachment{}, false
}

// validateAttachmentOutput reads the two options that make show write bytes,
// and refuses every option that renders a task beside them.
//
// Ignoring them would be worse than refusing: `show X --get-attachment Y
// --json` reads as a request for a JSON document, and answering it with a PNG
// on standard output would corrupt whatever was parsing the stream.
func validateAttachmentOutput(
	flags *commandFlagSet,
	comparing bool,
	target, out string,
) (attachmentOutput, bool, error) {
	typed := map[string]bool{}
	var others []string
	flags.Visit(func(visited *flag.Flag) {
		typed[visited.Name] = true
		if visited.Name != "get-attachment" && visited.Name != "out" {
			others = append(others, visited.Name)
		}
	})
	if !typed["get-attachment"] {
		if typed["out"] {
			return attachmentOutput{}, false, core.Errorf(core.CategoryInvocation, "show --out requires --get-attachment")
		}
		return attachmentOutput{}, false, nil
	}
	if comparing {
		others = append(others, "compare")
	}
	if len(others) > 0 {
		return attachmentOutput{}, false, core.Errorf(
			core.CategoryInvocation,
			"cannot use --%s with --get-attachment",
			others[0],
		)
	}
	if target == "" {
		return attachmentOutput{}, false, core.Errorf(
			core.CategoryInvocation,
			"show --get-attachment requires an attachment ID",
		)
	}
	if typed["out"] && out == "" {
		return attachmentOutput{}, false, core.Errorf(core.CategoryInvocation, "show --out requires a path")
	}
	return attachmentOutput{Target: target, Out: out}, true, nil
}
