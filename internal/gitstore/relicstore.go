package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// relicRefPrefix is the namespace that holds one ref per relic, named by the
// relic's ID. Nothing deletes a relic ref in normal operation.
const relicRefPrefix = "refs/workbook/relics/"

// The names inside a relic commit's tree. They are the stored format:
//
//	operation.json                the operation pack
//	state.json                    the folded relic
//	links.json                    the relic's links
//	parts/<part-id>/fields.json   one part's fields, always present
//	parts/<part-id>/body          one part's body, only when it has one
const (
	relicOperationPath = "operation.json"
	relicStatePath     = "state.json"
	relicLinksPath     = "links.json"
	relicPartsPath     = "parts"
	relicFieldsPath    = "fields.json"
	relicBodyPath      = "body"
)

// subtreeMode is how a raw tree object records an entry that is a tree.
const subtreeMode = "40000"

// relicWriteDirectoryPrefix names the temporary directory one relic write
// keeps its new blobs in while Git hashes them.
const relicWriteDirectoryPrefix = "relic-write-"

// relicRefChanged opens the refusal a write gets when the relic's ref moved
// under it. See relicLostRace.
const relicRefChanged = "relic ref changed concurrently"

// relicLostRace is the refusal for a write that lost: the relic's ref no
// longer pointed where the write started from, so another change was saved
// first and this one was not. Nothing retries it. The command read the relic
// before that change, so only running it again judges the change it wants
// against the relic as it now is, and the message ends by saying so. cause is
// Git's refusal of the ref move when there was one; a command shows only the
// message.
func relicLostRace(ref string, cause error) error {
	message := fmt.Sprintf("%s: another change to relic %s was saved first, so this one was not; run the command again",
		relicRefChanged, strings.TrimPrefix(ref, relicRefPrefix))
	if cause == nil {
		return core.Errorf(core.CategoryStaleWrite, "%s", message)
	}
	return core.Wrap(core.CategoryStaleWrite, message, cause)
}

func corruptRelic(relicID, format string, args ...any) error {
	return core.Errorf(core.CategoryCorruptData, "relic %s: %s", relicID, fmt.Sprintf(format, args...))
}

// namedRelicError makes an error raised while reading one relic say which
// relic it was. A project holds many relics and a command that reads them all
// stops at the first it cannot read, so an error that names none leaves
// nothing to look at. An error that already names the relic is returned as it
// is, and the category is kept either way.
//
// The relic's name goes at the front of one whole message, causes included,
// because a command reports only an error's own message unless the error is
// operational. An operational error keeps its chain, so a caller can still
// find a cancelled context in it.
func namedRelicError(relicID string, err error) error {
	if err == nil || relicID == "" || strings.Contains(err.Error(), relicID) {
		return err
	}
	category := core.CategoryOf(err)
	if category == "" || category == core.CategoryOperational {
		return core.Wrap(core.CategoryOperational, "relic "+relicID, err)
	}
	return core.Errorf(category, "relic %s: %s", relicID, err.Error())
}

// gitExitedWithFailure reports whether err is Git having run and answered
// with a failure of its own, as it does when it meets an object it does not
// have. A command that never ran, or that was stopped because the context
// ended, says nothing about the repository.
func gitExitedWithFailure(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() > 0
}

// ListRelicHeads returns every relic ref tip, ordered by relic ID. The
// namespace is Workbook's own, so a ref in it that does not name one relic of
// this project, or is symbolic, fails the listing.
func (r *Repository) ListRelicHeads(ctx context.Context, config core.ProjectConfig) ([]core.RelicHead, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return nil, err
	}
	records, err := r.relicRefRecords(ctx, relicRefPrefix)
	if err != nil {
		return nil, err
	}

	heads := make([]core.RelicHead, 0, len(records))
	for _, record := range records {
		relicID := strings.TrimPrefix(record.name, relicRefPrefix)
		if relicID == "" || strings.Contains(relicID, "/") {
			return nil, core.Errorf(core.CategoryCorruptData, "relic ref %q does not name one relic", record.name)
		}
		if err := core.ValidateRelicIDShape(relicID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "relic ref ID is invalid", err)
		}
		if !keys.OwnsRelic(relicID) {
			return nil, core.Errorf(core.CategoryCorruptData,
				"relic ref %q carries a project key this project does not have; its keys are: %s",
				record.name, core.KeyNameList(keys))
		}
		if record.symbolicTarget != "" {
			return nil, core.Errorf(core.CategoryCorruptData, "relic ref %q must not be symbolic", record.name)
		}
		heads = append(heads, core.RelicHead{RelicID: relicID, ObjectID: record.objectID})
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].RelicID < heads[j].RelicID })
	return heads, nil
}

type relicRefRecord struct {
	name           string
	objectID       string
	symbolicTarget string
}

// relicRefRecords lists the refs at or under pattern through Git's ref
// enumeration, so packed and loose refs behave the same way.
func (r *Repository) relicRefRecords(ctx context.Context, pattern string) ([]relicRefRecord, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format="+taskRefFormat, pattern)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated relic ref record")
	}
	lines := bytes.Split(contents[:len(contents)-1], []byte{'\n'})
	records := make([]relicRefRecord, 0, len(lines))
	for _, line := range lines {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid relic ref record")
		}
		record := relicRefRecord{name: string(parts[0]), objectID: string(parts[1]), symbolicTarget: string(parts[2])}
		if !strings.HasPrefix(record.name, relicRefPrefix) {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside the relic namespace")
		}
		// The width comes from Git's own full-width %(objectname), and every
		// later ID in this process is held to it.
		if err := r.rememberGitObjectID(record.objectID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "relic ref object ID is invalid", err)
		}
		records = append(records, record)
	}
	return records, nil
}

// relicRefValue returns the object one relic ref points at, or "" when there
// is no such ref. A ref that is symbolic, or has refs nested under its name,
// is corrupt data.
func (r *Repository) relicRefValue(ctx context.Context, ref string) (string, error) {
	records, err := r.relicRefRecords(ctx, ref)
	if err != nil {
		return "", err
	}
	objectID := ""
	for _, record := range records {
		if record.name != ref {
			return "", core.Errorf(core.CategoryCorruptData, "relic ref %q has nested entries", ref)
		}
		if record.symbolicTarget != "" {
			return "", core.Errorf(core.CategoryCorruptData, "relic ref %q must not be symbolic", ref)
		}
		objectID = record.objectID
	}
	return objectID, nil
}

// ReadRelicTips reads the commit each head names and returns its operation
// and record, in the order of heads, through one Git batch process.
//
// It checks what can be checked from the commit alone: the shape of its tree
// and its parents (rule R2), that its documents are the exact bytes this build
// writes, and that they belong to this project and to the relic the head
// names. It does not read parts/, and it does not check the record against the
// fold; that takes the parent commit.
//
// A commit whose pack or state declares a generation this build cannot fold is
// returned as far as it can be read, with only what holds at every generation
// checked: the two documents are there, they name this project and this relic,
// and the commit is not a merge. Its Links is the zero value unless links.json
// is the document this build reads.
func (r *Repository) ReadRelicTips(
	ctx context.Context,
	config core.ProjectConfig,
	heads []core.RelicHead,
) ([]core.RelicSnapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	snapshots := make([]core.RelicSnapshot, 0, len(heads))
	if len(heads) == 0 {
		return snapshots, nil
	}

	objectIDBytes := make([]int, len(heads))
	var input bytes.Buffer
	for index, head := range heads {
		decoded, err := decodeObjectID(head.ObjectID)
		if err != nil {
			return nil, core.Wrap(core.CategoryCorruptData,
				fmt.Sprintf("relic %s: head object ID is invalid", head.RelicID), err)
		}
		objectIDBytes[index] = len(decoded)
		fmt.Fprintf(&input, "%s\n%s^{tree}\n", head.ObjectID, head.ObjectID)
		for _, path := range []string{relicOperationPath, relicStatePath, relicLinksPath} {
			fmt.Fprintf(&input, "%s:%s\n", head.ObjectID, path)
		}
	}

	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	for index, head := range heads {
		var objects [5]batchObject
		for position := range objects {
			object, err := readBatchObject(batch.Reader())
			if err != nil {
				return nil, namedRelicError(head.RelicID,
					batch.ReadFailure("cannot read relic objects from Git batch", err))
			}
			objects[position] = object
		}
		snapshot, err := validateRelicTip(objects, config, head, objectIDBytes[index])
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return snapshots, nil
}

// validateRelicTip judges the five objects read for one commit of a relic, and
// names the relic in whatever it refuses them for.
func validateRelicTip(
	objects [5]batchObject,
	config core.ProjectConfig,
	head core.RelicHead,
	objectIDBytes int,
) (core.RelicSnapshot, error) {
	snapshot, err := validateRelicTipObjects(objects, config, head, objectIDBytes)
	if err != nil {
		return core.RelicSnapshot{}, namedRelicError(head.RelicID, err)
	}
	return snapshot, nil
}

func validateRelicTipObjects(
	objects [5]batchObject,
	config core.ProjectConfig,
	head core.RelicHead,
	objectIDBytes int,
) (core.RelicSnapshot, error) {
	commit, tree, operationBlob, stateBlob, linksBlob := objects[0], objects[1], objects[2], objects[3], objects[4]
	// links.json is judged further down: whether a commit must have one is a
	// rule of this build's generation.
	for _, object := range objects[:4] {
		if object.refused != nil {
			return core.RelicSnapshot{}, object.refused
		}
		if object.missing {
			return core.RelicSnapshot{}, corruptRelic(head.RelicID, "requested object %q is missing", object.objectID)
		}
	}
	if commit.objectID != head.ObjectID || commit.kind != "commit" {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the ref does not point directly to a commit")
	}
	if tree.kind != "tree" {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the commit does not point to a tree")
	}
	if operationBlob.kind != "blob" || stateBlob.kind != "blob" {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the documents are not blobs")
	}
	commitTree, err := commitTreeObjectID(commit.contents, "relic commit")
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if tree.objectID != commitTree {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the commit's tree does not match its batch object")
	}
	entries, err := parseRawWorkbookTree(tree.contents, objectIDBytes, "relic tree")
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if entries.objectID(relicOperationPath) != operationBlob.objectID ||
		entries.objectID(relicStatePath) != stateBlob.objectID {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the tree's entries do not match their batch objects")
	}

	pack, err := core.DecodeRelicOperationPack(operationBlob.contents)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	state, err := core.DecodeRelicStateDocument(stateBlob.contents)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if pack.ProjectID != config.ProjectID || state.ProjectID != config.ProjectID {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the documents do not match the configured project")
	}
	if pack.RelicID != head.RelicID || state.RelicID != head.RelicID {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the documents do not match the relic ref")
	}
	parentCount, err := commitParentCount(commit.contents, "relic commit")
	if err != nil {
		return core.RelicSnapshot{}, err
	}

	// Everything below is a rule of this build's generation. A newer one may
	// put entries in its trees, or members in its documents, that this build
	// has no name for, and calling those corrupt would put a wall in front of
	// the next format change. What stays true is that a relic's history is a
	// chain. The links are left as the zero value unless they read as this
	// build's own document.
	if pack.RequiresNewerReader() || state.RequiresNewerReader() {
		if parentCount > 1 {
			return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the commit must not be a merge")
		}
		var links core.RelicLinksDocument
		if !linksBlob.missing && linksBlob.refused == nil && linksBlob.kind == "blob" {
			if decoded, err := core.DecodeRelicLinksDocument(linksBlob.contents); err == nil {
				links = decoded
			}
		}
		return core.RelicSnapshot{
			Head:      head.ObjectID,
			Operation: pack,
			Record:    core.RelicRecord{State: state, Links: links},
		}, nil
	}

	if err := requireCanonicalRelicDocument("relic operation", operationBlob.contents, pack); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := requireCanonicalRelicDocument("relic state", stateBlob.contents, state); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := validateRelicTreeEntries(entries, len(state.Relic.Parts) > 0); err != nil {
		return core.RelicSnapshot{}, err
	}
	if linksBlob.refused != nil {
		return core.RelicSnapshot{}, linksBlob.refused
	}
	if linksBlob.missing || linksBlob.kind != "blob" || entries.objectID(relicLinksPath) != linksBlob.objectID {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the tree's links.json does not match its batch object")
	}
	links, err := core.DecodeRelicLinksDocument(linksBlob.contents)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := requireCanonicalRelicDocument("relic links", linksBlob.contents, links); err != nil {
		return core.RelicSnapshot{}, err
	}
	if pack.Operations[0].Type == core.RelicOperationCreate {
		if parentCount != 0 {
			return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the commit that creates the relic must have no parent")
		}
	} else if parentCount != 1 {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "a commit that changes the relic must have exactly one parent")
	}
	return core.RelicSnapshot{
		Head:      head.ObjectID,
		Operation: pack,
		Record:    core.RelicRecord{State: state, Links: links},
	}, nil
}

// requireCanonicalRelicDocument proves a stored document is the exact bytes
// this build writes for it.
func requireCanonicalRelicDocument(what string, contents []byte, document any) error {
	canonical, err := core.EncodeDocument(document)
	if err != nil {
		return core.Wrap(core.CategoryCorruptData, "cannot canonicalize "+what, err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.Errorf(core.CategoryCorruptData, "%s is not canonical", what)
	}
	return nil
}

// validateRelicTreeEntries checks the top of a relic commit's tree at this
// build's generation: the three documents as regular blobs, the parts tree
// exactly when the relic has a part, and nothing else. A caller runs it only
// for a commit whose pack and state this build can fold.
func validateRelicTreeEntries(tree workbookTree, hasParts bool) error {
	for name, mode := range tree.modes {
		switch name {
		case relicOperationPath, relicStatePath, relicLinksPath:
			if mode != regularBlobMode {
				return core.Errorf(core.CategoryCorruptData, "relic tree entry %q is not a regular blob", name)
			}
		case relicPartsPath:
			if mode != subtreeMode {
				return core.Errorf(core.CategoryCorruptData, "relic tree entry %q is not a tree", name)
			}
		default:
			return core.Errorf(core.CategoryCorruptData, "relic tree has an unexpected entry")
		}
	}
	if tree.objectID(relicLinksPath) == "" {
		return core.Errorf(core.CategoryCorruptData, "relic tree must contain %s", relicLinksPath)
	}
	if _, hasPartsTree := tree.objects[relicPartsPath]; hasPartsTree != hasParts {
		return core.Errorf(core.CategoryCorruptData,
			"relic tree must contain %s exactly when the relic has a part", relicPartsPath)
	}
	return nil
}

// ReadRelicParts reads the fields of the named parts from snapshot's commit,
// and their bodies when bodies is true, through one Git batch process. Without
// bodies, HasBody is false in every result: the state says whether a part has
// a body, and this says only what was read.
//
// The sizes are checked here because nothing else has: a tip read does not
// open parts/. A body over its limit, a body whose size is not the one the
// state records, a missing blob, a fields.json over the ceiling for one stored
// object, one field value over the limit for one value, and a fields.json that
// is not the document this build writes are each corrupt data naming the part.
// How large a part's fields are together is a total, checked when a change
// makes it grow and never here, so the read is bounded by the object ceiling
// alone, from the size in Git's batch header before anything is allocated.
func (r *Repository) ReadRelicParts(
	ctx context.Context,
	config core.ProjectConfig,
	snapshot core.RelicSnapshot,
	partIDs []string,
	bodies bool,
) (map[string]core.RelicPartContent, error) {
	contents, err := r.readRelicParts(ctx, config, snapshot, partIDs, bodies)
	if err != nil {
		return nil, namedRelicError(snapshot.Record.State.RelicID, err)
	}
	return contents, nil
}

func (r *Repository) readRelicParts(
	ctx context.Context,
	config core.ProjectConfig,
	snapshot core.RelicSnapshot,
	partIDs []string,
	bodies bool,
) (map[string]core.RelicPartContent, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	if _, err := decodeObjectID(snapshot.Head); err != nil {
		return nil, core.Wrap(core.CategoryValidation, "relic head object ID is invalid", err)
	}
	relicID := snapshot.Record.State.RelicID

	type request struct {
		part core.RelicPart
		body bool
	}
	requests := make([]request, 0, len(partIDs))
	requested := make(map[string]struct{}, len(partIDs))
	var input bytes.Buffer
	for _, partID := range partIDs {
		if _, repeated := requested[partID]; repeated {
			continue
		}
		requested[partID] = struct{}{}
		// The ID goes into Git's request stream, so it is held to its shape
		// whatever the state it came from was allowed to carry.
		if err := core.ValidatePartID(partID); err != nil {
			return nil, err
		}
		part, found := snapshot.Record.State.Relic.Part(partID)
		if !found {
			return nil, core.Errorf(core.CategoryNotFound, "relic %s has no part %s", relicID, partID)
		}
		wanted := request{part: part, body: bodies && part.Body != nil}
		fmt.Fprintf(&input, "%s:%s/%s/%s\n", snapshot.Head, relicPartsPath, partID, relicFieldsPath)
		if wanted.body {
			fmt.Fprintf(&input, "%s:%s/%s/%s\n", snapshot.Head, relicPartsPath, partID, relicBodyPath)
		}
		requests = append(requests, wanted)
	}
	contents := make(map[string]core.RelicPartContent, len(requests))
	if len(requests) == 0 {
		return contents, nil
	}

	// A commit written under rules this build does not have is read as far as
	// this build's rules allow, and what they do not allow is the newer
	// build's to judge rather than corruption.
	newer := snapshot.Operation.RequiresNewerReader() || snapshot.Record.State.RequiresNewerReader()
	refuse := func(partID, format string, args ...any) error {
		if newer {
			return core.Errorf(core.CategoryNewerWriter,
				"relic %s was written by a newer workbook; upgrade workbook to read part %s", relicID, partID)
		}
		return corruptRelic(relicID, "part %s: %s", partID, fmt.Sprintf(format, args...))
	}

	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	// readBlob returns one requested blob, refusing it by the size in Git's
	// batch header when it is over limit.
	readBlob := func(partID, what string, limit int64) ([]byte, error) {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return nil, batch.ReadFailure("cannot read relic part objects from Git batch", err)
		}
		switch {
		case object.missing:
			return nil, refuse(partID, "its %s is missing", what)
		case object.kind != "blob":
			return nil, refuse(partID, "its %s is not a blob", what)
		case object.size > limit:
			return nil, refuse(partID, "its %s is %d bytes; the limit is %d bytes", what, object.size, limit)
		case object.refused != nil:
			return nil, object.refused
		}
		return object.contents, nil
	}
	for _, wanted := range requests {
		partID := wanted.part.ID
		stored, err := readBlob(partID, relicFieldsPath, MaxObjectBytes)
		if err != nil {
			return nil, err
		}
		fields, err := core.DecodeRelicFieldsDocument(stored)
		if err != nil {
			return nil, refuse(partID, "its %s is invalid: %v", relicFieldsPath, err)
		}
		if err := requireCanonicalRelicDocument(relicFieldsPath, stored, fields); err != nil {
			return nil, refuse(partID, "its %s is not canonical", relicFieldsPath)
		}
		content := core.RelicPartContent{Fields: fields.Fields}
		if wanted.body {
			body, err := readBlob(partID, relicBodyPath, core.MaxRelicBodyBytes)
			if err != nil {
				return nil, err
			}
			if int64(len(body)) != wanted.part.Body.Bytes {
				return nil, refuse(partID, "its body is %d bytes, but the relic records %d bytes",
					len(body), wanted.part.Body.Bytes)
			}
			content.Body, content.HasBody = body, true
		}
		contents[partID] = content
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return contents, nil
}

// GetRelic returns the relic named relicID as its ref's commit stores it,
// after checking that commit against its parent by rules R1 to R6 (see
// relichistory.go). A commit written straight into Git whose state or body
// does not follow from the fold is refused as corrupt data, with a pointer to
// `workbook validate`, which checks the whole history.
//
// A tip written by a newer Workbook is returned unverified; see
// verifiedRelicTip. An ID that is not one of this project's relic IDs can name
// no relic, so it is not found, like one whose relic was never created.
func (r *Repository) GetRelic(ctx context.Context, config core.ProjectConfig, relicID string) (core.RelicSnapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.RelicSnapshot{}, err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	head := ""
	if keys.OwnsRelic(relicID) {
		if head, err = r.relicRefValue(ctx, relicRefPrefix+relicID); err != nil {
			return core.RelicSnapshot{}, err
		}
	}
	if head == "" {
		return core.RelicSnapshot{}, core.Errorf(core.CategoryNotFound, "relic %q was not found", relicID)
	}
	snapshot, err := r.verifiedRelicTip(ctx, config, relicID, head)
	err = namedRelicError(relicID, err)
	if core.CategoryOf(err) == core.CategoryCorruptData {
		return core.RelicSnapshot{}, core.Errorf(core.CategoryCorruptData, "%v; run \"workbook validate\" for details", err)
	}
	return snapshot, err
}

// WriteRelic stores one relic operation pack and the record it folds to as a
// commit, then moves the relic's ref from parent's head to it, or creates the
// ref when parent is nil.
//
// content carries the fields and body of parts the pack names. A part this
// pack adds takes its fields.json from Fields, an empty one when Fields is nil.
// A part the parent has keeps its fields.json unless the pack carries a
// part.field.set or part.field.clear for it and Fields is not nil, whatever
// content holds otherwise. A body is stored only when this change saved it, as
// core.RelicBodySavedBy judges: the record's body version for the part is the
// ID of an operation in the pack, and the part is new or that version is not
// the one parent records. Otherwise the part keeps the body it had, whatever
// content holds, so text from an edit the fold ignored, or beside a repeated
// part.body.set, is never stored. A part the pack does not name
// keeps its subtree object, and content given for one is refused rather than
// dropped.
//
// Nothing is visible until the ref moves, and the ref moves only if it still
// points at parent's head: a write interrupted before that leaves unreferenced
// objects and changes nothing, and of two writers that started from the same
// tip exactly one lands.
func (r *Repository) WriteRelic(
	ctx context.Context,
	config core.ProjectConfig,
	parent *core.RelicSnapshot,
	pack core.RelicOperationPack,
	record core.RelicRecord,
	content map[string]core.RelicPartContent,
	reason string,
) (core.RelicSnapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.RelicSnapshot{}, err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := core.ValidateRelicIDShape(pack.RelicID); err != nil {
		return core.RelicSnapshot{}, err
	}
	if !keys.OwnsRelic(pack.RelicID) {
		return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation,
			"relic ID %q carries a project key this project does not have; its keys are: %s",
			pack.RelicID, core.KeyNameList(keys))
	}
	if pack.ProjectID != config.ProjectID {
		return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation,
			"relic operation pack project ID does not match configuration")
	}

	var parentRecord *core.RelicRecord
	parentHead := ""
	if parent != nil {
		// Only its form is checked here. That it is a whole object ID, and the
		// right one, follows from it being what the ref holds.
		if _, err := decodeObjectID(parent.Head); err != nil {
			return core.RelicSnapshot{}, core.Wrap(core.CategoryValidation, "parent head must be a canonical object ID", err)
		}
		parentRecord, parentHead = &parent.Record, parent.Head
	}
	if err := core.ValidateRelicCheckpoint(parentRecord, pack, record); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := refuseActorMismatch(ctx, pack.Actor.ID); err != nil {
		return core.RelicSnapshot{}, err
	}

	// Looking at the ref first only saves writing objects for a write that has
	// already lost. The ref move below is what decides.
	ref := relicRefPrefix + pack.RelicID
	current, err := r.relicRefValue(ctx, ref)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if current != parentHead {
		return core.RelicSnapshot{}, relicLostRace(ref, nil)
	}

	head, err := r.writeRelicObjects(ctx, parent, pack, record, content, reason)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.moveRelicRef(ctx, ref, head, parentHead, reason); err != nil {
		return core.RelicSnapshot{}, err
	}
	return core.RelicSnapshot{Head: head, Operation: pack, Record: record}, nil
}

// moveRelicRef points a relic's ref at head, only if it still points at
// expected, or does not exist when expected is empty. A ref that exists
// already or has moved is a stale write.
func (r *Repository) moveRelicRef(ctx context.Context, ref, head, expected, reason string) error {
	reflogReason := reason
	if !strings.HasPrefix(reflogReason, "workbook:") {
		reflogReason = "workbook: " + reflogReason
	}
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	if expected == "" {
		fmt.Fprintf(&input, "create %s %s\n", ref, head)
	} else {
		fmt.Fprintf(&input, "update %s %s %s\n", ref, head, expected)
	}
	input.WriteString("prepare\ncommit\n")
	_, err := r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "--create-reflog", "-m", reflogReason, "--stdin")
	if err == nil {
		return nil
	}
	// Git refused the move. Why is read from the ref itself rather than from
	// Git's words, which differ between versions and ref backends.
	current, readErr := r.relicRefValue(ctx, ref)
	switch {
	case readErr != nil && core.CategoryOf(readErr) == core.CategoryCorruptData:
		return readErr
	case readErr == nil && current != expected:
		return relicLostRace(ref, err)
	}
	return err
}

// relicPartWrite is what one write does to one part its pack names.
type relicPartWrite struct {
	id string
	// existing reports a part the parent commit already has a subtree for.
	existing bool
	// hasBody reports that the new state lists a body for the part.
	hasBody bool
	// fields and body index the write's new blobs, or are -1 to keep the
	// parent's blob.
	fields int
	body   int
}

// relicWritePlan is every new blob one relic write stores and what it does to
// each part. The first three blobs are operation.json, state.json and
// links.json.
type relicWritePlan struct {
	blobs [][]byte
	parts []relicPartWrite
}

func (plan *relicWritePlan) add(blob []byte) int {
	plan.blobs = append(plan.blobs, blob)
	return len(plan.blobs) - 1
}

// planRelicWrite decides what a write stores, and refuses anything over a
// limit, before Git is asked to store any of it.
func planRelicWrite(
	parent *core.RelicSnapshot,
	pack core.RelicOperationPack,
	record core.RelicRecord,
	content map[string]core.RelicPartContent,
) (relicWritePlan, error) {
	var plan relicWritePlan
	for _, document := range []struct {
		name   string
		value  any
		remedy string
	}{
		{relicOperationPath, pack, "make the change in smaller steps"},
		{relicStatePath, record.State, "the relic is too large to add to; put new content in another relic"},
		{relicLinksPath, record.Links, "the relic has too many links to add to"},
	} {
		encoded, err := core.EncodeDocument(document.value)
		if err != nil {
			return relicWritePlan{}, err
		}
		if len(encoded) > MaxObjectBytes {
			return relicWritePlan{}, core.Errorf(core.CategoryValidation,
				"the relic's %s would be %d bytes; the limit for one stored object is %d bytes; %s",
				document.name, len(encoded), MaxObjectBytes, document.remedy)
		}
		plan.add(encoded)
	}

	var parentRecord *core.RelicRecord
	if parent != nil {
		parentRecord = &parent.Record
	}
	named := core.RelicPartsNamedBy(pack)
	fieldsEdited := relicPartsWithFieldOperations(pack)
	isNamed := make(map[string]struct{}, len(named))
	for _, partID := range named {
		isNamed[partID] = struct{}{}
	}
	listed := make(map[string]core.RelicPart, len(record.State.Relic.Parts))
	for _, part := range record.State.Relic.Parts {
		listed[part.ID] = part
	}
	given := make([]string, 0, len(content))
	for partID := range content {
		given = append(given, partID)
	}
	sort.Strings(given)
	for _, partID := range given {
		// Only the subtrees of parts the pack names are rewritten, so this
		// content could only be dropped.
		if _, ok := isNamed[partID]; !ok {
			return relicWritePlan{}, core.Errorf(core.CategoryValidation,
				"content was given for part %s, which this change does not name", partID)
		}
		if _, ok := listed[partID]; !ok {
			return relicWritePlan{}, core.Errorf(core.CategoryValidation,
				"content was given for part %s, which the relic does not have", partID)
		}
	}

	for _, partID := range named {
		part, ok := listed[partID]
		if !ok {
			// The fold ignored an operation naming a part the relic does not
			// have, and the tree holds a subtree only for a part the state lists.
			continue
		}
		write := relicPartWrite{id: partID, hasBody: part.Body != nil, fields: -1, body: -1}
		if parent != nil {
			_, write.existing = parent.Record.State.Relic.Part(partID)
		}
		partContent := content[partID]
		// A part the parent has keeps its fields.json unless the pack sets or
		// clears one of its fields, which is what rule R7 holds a read to.
		_, isEdited := fieldsEdited[partID]
		if !write.existing || (isEdited && partContent.Fields != nil) {
			encoded, err := encodeRelicPartFields(partID, partContent.Fields)
			if err != nil {
				return relicWritePlan{}, err
			}
			write.fields = plan.add(encoded)
		}
		if core.RelicBodySavedBy(parentRecord, pack, record, partID) {
			if !partContent.HasBody {
				return relicWritePlan{}, core.Errorf(core.CategoryValidation,
					"this change saves a body for part %s, but no body was given for it", partID)
			}
			if size := len(partContent.Body); size > core.MaxRelicBodyBytes {
				return relicWritePlan{}, core.Errorf(core.CategoryValidation,
					"the body of part %s is %d bytes; the limit is %d bytes", partID, size, core.MaxRelicBodyBytes)
			}
			if size := int64(len(partContent.Body)); size != part.Body.Bytes {
				return relicWritePlan{}, core.Errorf(core.CategoryValidation,
					"the body of part %s is %d bytes, but the change records %d bytes", partID, size, part.Body.Bytes)
			}
			write.body = plan.add(partContent.Body)
		} else if part.Body != nil {
			// The part keeps the body the parent stores, which has to be the
			// size the state records. The fold leaves the size alone unless it
			// gives the body a new version, and a pack is refused before this
			// if a body edit in it names itself as its base, so only a record
			// that does not follow from the pack is refused here.
			kept := int64(-1)
			if parentRecord != nil {
				if before, existed := parentRecord.State.Relic.Part(partID); existed && before.Body != nil {
					kept = before.Body.Bytes
				}
			}
			if kept != part.Body.Bytes {
				return relicWritePlan{}, core.Errorf(core.CategoryValidation,
					"this change records a body of %d bytes for part %s without saving one", part.Body.Bytes, partID)
			}
		}
		plan.parts = append(plan.parts, write)
	}
	return plan, nil
}

// encodeRelicPartFields returns a part's fields.json, refusing what a reader
// would refuse: one value over the limit for one field value, which
// core.EncodeDocument checks, and a blob over the ceiling for one stored
// object. Neither refusal carries content.
//
// How large one part's fields are together is a total. The service refuses a
// change that makes it grow past its limit, and the store does not judge it:
// a part may already be over, and a change that leaves it no larger has to be
// written.
func encodeRelicPartFields(partID string, fields map[string]json.RawMessage) ([]byte, error) {
	encoded, err := core.EncodeDocument(core.NewRelicFields(fields))
	if err != nil {
		return nil, core.Wrap(core.CategoryValidation, fmt.Sprintf("the fields of part %s are invalid", partID), err)
	}
	if len(encoded) > MaxObjectBytes {
		return nil, core.Errorf(core.CategoryValidation,
			"the fields of part %s would be %d bytes; the limit for one stored object is %d bytes; clear a field first",
			partID, len(encoded), MaxObjectBytes)
	}
	return encoded, nil
}

// writeRelicObjects stores the blobs, trees and commit of one relic write and
// returns the commit, without touching any ref. Unreferenced objects are
// harmless, so a write that stops after this has changed nothing.
//
// The number of Git processes does not depend on the number of parts: one
// read of the parent's trees, one hash-object for every new blob, one mktree
// for every rewritten part subtree, one each for the parts tree and the top
// tree, and one commit-tree.
func (r *Repository) writeRelicObjects(
	ctx context.Context,
	parent *core.RelicSnapshot,
	pack core.RelicOperationPack,
	record core.RelicRecord,
	content map[string]core.RelicPartContent,
	reason string,
) (string, error) {
	plan, err := planRelicWrite(parent, pack, record, content)
	if err != nil {
		return "", err
	}

	parentHead := ""
	inherited := relicParentTrees{parts: map[string]string{}}
	if parent != nil {
		parentHead = parent.Head
		var existing []string
		for _, write := range plan.parts {
			if write.existing {
				existing = append(existing, write.id)
			}
		}
		inherited, err = r.readRelicParentTrees(ctx, *parent, existing)
		if err != nil {
			return "", err
		}
	}

	blobs, err := r.hashRelicBlobs(ctx, plan.blobs)
	if err != nil {
		return "", err
	}

	// A named part gets a new subtree only when one of its blobs is new. An
	// edit the fold ignored, or fields rewritten to the bytes they had, leaves
	// the subtree the parent has.
	var rewritten [][]relicTreeEntry
	var rewrittenIDs []string
	for _, write := range plan.parts {
		stored := inherited.named[write.id]
		fields := stored.fields
		if write.fields >= 0 {
			fields = blobs[write.fields]
		}
		body := ""
		if write.hasBody {
			body = stored.body
			if write.body >= 0 {
				body = blobs[write.body]
			}
		}
		if write.existing && fields == stored.fields && body == stored.body {
			continue
		}
		entries := []relicTreeEntry{{mode: regularBlobMode, kind: "blob", objectID: fields, name: relicFieldsPath}}
		if write.hasBody {
			entries = append(entries, relicTreeEntry{mode: regularBlobMode, kind: "blob", objectID: body, name: relicBodyPath})
		}
		rewritten = append(rewritten, entries)
		rewrittenIDs = append(rewrittenIDs, write.id)
	}

	// The parts tree is the parent's unless a subtree changed, so a change
	// that touches no part's content keeps the parent's parts tree object.
	partsTree := inherited.partsTree
	if len(rewritten) > 0 {
		subtrees, err := r.writeRelicTrees(ctx, rewritten)
		if err != nil {
			return "", err
		}
		for index, partID := range rewrittenIDs {
			inherited.parts[partID] = subtrees[index]
		}
		entries := make([]relicTreeEntry, 0, len(record.State.Relic.Parts))
		for _, part := range record.State.Relic.Parts {
			entries = append(entries, relicTreeEntry{
				mode: "040000", kind: "tree", objectID: inherited.parts[part.ID], name: part.ID,
			})
		}
		trees, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{entries})
		if err != nil {
			return "", err
		}
		partsTree = trees[0]
	}

	top := []relicTreeEntry{
		{mode: regularBlobMode, kind: "blob", objectID: blobs[0], name: relicOperationPath},
		{mode: regularBlobMode, kind: "blob", objectID: blobs[1], name: relicStatePath},
		{mode: regularBlobMode, kind: "blob", objectID: blobs[2], name: relicLinksPath},
	}
	if partsTree != "" {
		top = append(top, relicTreeEntry{mode: "040000", kind: "tree", objectID: partsTree, name: relicPartsPath})
	}
	trees, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{top})
	if err != nil {
		return "", err
	}
	return r.writeCommit(ctx, trees[0], parentHead, reason, pack.Actor.ID, "")
}

// relicStoredPart is the blobs one part has in a commit. body is empty when the
// part has none.
type relicStoredPart struct {
	fields string
	body   string
}

// relicParentTrees is what a write takes over from its parent commit.
type relicParentTrees struct {
	// partsTree is the parent's parts tree, empty when it has no part.
	partsTree string
	// parts maps every part the parent has to its subtree.
	parts map[string]string
	// named holds the blobs of the parts the write asked about.
	named map[string]relicStoredPart
}

// readRelicParentTrees reads, through one Git batch process, the trees a write
// builds on: the parent's top tree, its parts tree and the subtrees of the
// parts in partIDs.
//
// The new commit inherits these objects without reading what is in them, so
// the parent has to be the commit the caller folded onto, and its parts tree
// has to hold exactly the parts its state lists. Building on anything else
// would write a tree that does not follow from its own state.
func (r *Repository) readRelicParentTrees(
	ctx context.Context,
	parent core.RelicSnapshot,
	partIDs []string,
) (relicParentTrees, error) {
	decodedHead, err := decodeObjectID(parent.Head)
	if err != nil {
		return relicParentTrees{}, core.Wrap(core.CategoryValidation, "parent head must be a canonical object ID", err)
	}
	objectIDBytes := len(decodedHead)
	relic := parent.Record.State.Relic
	relicID := parent.Record.State.RelicID
	hasParts := len(relic.Parts) > 0
	wantState, err := core.EncodeDocument(parent.Record.State)
	if err != nil {
		return relicParentTrees{}, err
	}

	var input bytes.Buffer
	fmt.Fprintf(&input, "%s:%s\n%s^{tree}\n", parent.Head, relicStatePath, parent.Head)
	if hasParts {
		fmt.Fprintf(&input, "%s:%s\n", parent.Head, relicPartsPath)
	}
	for _, partID := range partIDs {
		fmt.Fprintf(&input, "%s:%s/%s\n", parent.Head, relicPartsPath, partID)
	}
	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return relicParentTrees{}, err
	}
	defer batch.Close()

	read := func(what, kind string) (batchObject, error) {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return batchObject{}, batch.ReadFailure("cannot read relic objects from Git batch", err)
		}
		if object.refused != nil {
			return batchObject{}, object.refused
		}
		if object.missing || object.kind != kind {
			return batchObject{}, corruptRelic(relicID, "commit %s has no %s", parent.Head, what)
		}
		return object, nil
	}

	stateBlob, err := read(relicStatePath, "blob")
	if err != nil {
		return relicParentTrees{}, err
	}
	if !bytes.Equal(stateBlob.contents, wantState) {
		return relicParentTrees{}, core.Errorf(core.CategoryValidation,
			"the parent given for relic %s is not what commit %s stores; read the relic again", relicID, parent.Head)
	}
	top, err := read("tree", "tree")
	if err != nil {
		return relicParentTrees{}, err
	}
	entries, err := parseRawWorkbookTree(top.contents, objectIDBytes, "relic tree")
	if err != nil {
		return relicParentTrees{}, err
	}
	if err := validateRelicTreeEntries(entries, hasParts); err != nil {
		return relicParentTrees{}, err
	}

	trees := relicParentTrees{
		parts: make(map[string]string, len(relic.Parts)),
		named: make(map[string]relicStoredPart, len(partIDs)),
	}
	if hasParts {
		partsTree, err := read("parts tree", "tree")
		if err != nil {
			return relicParentTrees{}, err
		}
		if partsTree.objectID != entries.objectID(relicPartsPath) {
			return relicParentTrees{}, corruptRelic(relicID, "commit %s parts tree does not match its batch object", parent.Head)
		}
		trees.partsTree = partsTree.objectID
		err = scanRawTreeEntries(partsTree.contents, objectIDBytes, "relic parts tree", func(mode, name, objectID string) error {
			if _, duplicate := trees.parts[name]; duplicate || mode != subtreeMode {
				return corruptRelic(relicID, "commit %s parts tree has an entry that is not one part's subtree", parent.Head)
			}
			trees.parts[name] = objectID
			return nil
		})
		if err != nil {
			return relicParentTrees{}, err
		}
		for _, part := range relic.Parts {
			if _, stored := trees.parts[part.ID]; !stored {
				return relicParentTrees{}, corruptRelic(relicID, "commit %s has no subtree for part %s", parent.Head, part.ID)
			}
		}
		if len(trees.parts) != len(relic.Parts) {
			return relicParentTrees{}, corruptRelic(relicID, "commit %s has a subtree for a part its state does not list", parent.Head)
		}
	}

	for _, partID := range partIDs {
		subtree, err := read("subtree for part "+partID, "tree")
		if err != nil {
			return relicParentTrees{}, err
		}
		part, _ := relic.Part(partID)
		var stored relicStoredPart
		err = scanRawTreeEntries(subtree.contents, objectIDBytes, "relic part tree", func(mode, name, objectID string) error {
			switch {
			case mode == regularBlobMode && name == relicFieldsPath && stored.fields == "":
				stored.fields = objectID
			case mode == regularBlobMode && name == relicBodyPath && stored.body == "":
				stored.body = objectID
			default:
				return corruptRelic(relicID, "commit %s part %s has an unexpected entry", parent.Head, partID)
			}
			return nil
		})
		if err != nil {
			return relicParentTrees{}, err
		}
		if subtree.objectID != trees.parts[partID] || stored.fields == "" || (stored.body != "") != (part.Body != nil) {
			return relicParentTrees{}, corruptRelic(relicID,
				"commit %s part %s does not hold the blobs its state records", parent.Head, partID)
		}
		trees.named[partID] = stored
	}
	if err := batch.Finish(); err != nil {
		return relicParentTrees{}, err
	}
	return trees, nil
}

// hashRelicBlobs writes every blob of one relic write with a single Git
// process and returns their object IDs in order.
//
// Git hashes many objects in one process only from files, so the blobs go to a
// temporary directory in Workbook's private directory under the Git directory,
// which is on the object database's file system, is never in a working tree,
// and is the folder `workbook reset` clears. The directory is removed before
// this returns, whatever happened. A process killed in between leaves it
// behind, holding the content of that one write; nothing reads it again.
func (r *Repository) hashRelicBlobs(ctx context.Context, blobs [][]byte) (objectIDs []string, err error) {
	private, err := filepath.Abs(filepath.Join(r.CommonGitDir, "workbook"))
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot resolve Workbook's private directory", err)
	}
	if err := os.MkdirAll(private, 0o755); err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot create Workbook's private directory", err)
	}
	directory, err := os.MkdirTemp(private, relicWriteDirectoryPrefix)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot create a temporary directory for the relic's content", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(directory); removeErr != nil && err == nil {
			objectIDs = nil
			err = core.Wrap(core.CategoryOperational, "cannot remove the temporary directory for the relic's content", removeErr)
		}
	}()
	// Git reads one path per line.
	if strings.ContainsAny(directory, "\r\n") {
		return nil, core.Errorf(core.CategoryOperational,
			"cannot store the relic's content: the path of the Git directory contains a line break")
	}

	var paths bytes.Buffer
	for index, blob := range blobs {
		path := filepath.Join(directory, strconv.Itoa(index))
		if err := os.WriteFile(path, blob, 0o600); err != nil {
			return nil, core.Wrap(core.CategoryOperational, "cannot write the relic's content to a temporary file", err)
		}
		paths.WriteString(path)
		paths.WriteByte('\n')
	}
	// --no-filters: a body is stored byte for byte, whatever line-ending or
	// clean filter the repository configures for files.
	output, err := r.Git(ctx, paths.Bytes(), "hash-object", "-w", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	return r.relicObjectIDs(output, len(blobs), "blob")
}

type relicTreeEntry struct {
	mode     string
	kind     string
	objectID string
	name     string
}

// writeRelicTrees writes every given tree with a single Git process and
// returns their object IDs in order. No tree is empty.
func (r *Repository) writeRelicTrees(ctx context.Context, trees [][]relicTreeEntry) ([]string, error) {
	var input bytes.Buffer
	for _, entries := range trees {
		for _, entry := range entries {
			fmt.Fprintf(&input, "%s %s %s\t%s\n", entry.mode, entry.kind, entry.objectID, entry.name)
		}
		// A blank line ends one tree.
		input.WriteByte('\n')
	}
	output, err := r.Git(ctx, input.Bytes(), "mktree", "--batch")
	if err != nil {
		return nil, err
	}
	return r.relicObjectIDs(output, len(trees), "tree")
}

// relicObjectIDs reads the object IDs Git printed, one per line, for count
// objects it was asked to write.
func (r *Repository) relicObjectIDs(output []byte, count int, kind string) ([]string, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return nil, core.Errorf(core.CategoryOperational, "Git returned an unterminated list of %s object IDs", kind)
	}
	objectIDs := strings.Split(string(output[:len(output)-1]), "\n")
	if len(objectIDs) != count {
		return nil, core.Errorf(core.CategoryOperational,
			"Git returned %d %s object IDs for %d objects", len(objectIDs), kind, count)
	}
	for index, objectID := range objectIDs {
		objectID = strings.TrimSuffix(objectID, "\r")
		if err := r.rememberGitObjectID(objectID); err != nil {
			return nil, core.Wrap(core.CategoryOperational, "Git returned an invalid "+kind+" object ID", err)
		}
		objectIDs[index] = objectID
	}
	return objectIDs, nil
}
