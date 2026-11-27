package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
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

// subtreeMode is how a raw tree object records an entry that is a tree, and
// mktreeSubtreeMode is how one is written for git mktree. Both name the same
// mode; Git drops the leading zero when it stores it.
const (
	subtreeMode       = "40000"
	mktreeSubtreeMode = "040000"
)

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
		head, err := canonicalRelicHead(keys, record)
		if err != nil {
			return nil, err
		}
		heads = append(heads, head)
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].RelicID < heads[j].RelicID })
	return heads, nil
}

// InspectRelicHead returns the tip of the exact canonical relic ref, with no
// prefix resolution and no object read: one `for-each-ref` on that ref. A
// name that is not a relic ID is refused before any Git process runs.
func (r *Repository) InspectRelicHead(ctx context.Context, config core.ProjectConfig, relicID string) (core.RelicHead, bool, error) {
	if err := core.ValidateRelicIDShape(relicID); err != nil {
		return core.RelicHead{}, false, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.RelicHead{}, false, err
	}
	objectID, err := r.relicRefValue(ctx, relicRefPrefix+relicID)
	if err != nil {
		return core.RelicHead{}, false, namedRelicError(relicID, err)
	}
	if objectID == "" {
		return core.RelicHead{}, false, nil
	}
	return core.RelicHead{RelicID: relicID, ObjectID: objectID}, true, nil
}

// canonicalRelicHead reads one record of refs/workbook/relics/. Only Workbook
// writes there, so a ref that does not name one relic of this project, or is
// symbolic, is corrupt data.
func canonicalRelicHead(keys core.KeySet, record relicRefRecord) (core.RelicHead, error) {
	relicID := strings.TrimPrefix(record.name, relicRefPrefix)
	if relicID == "" || strings.Contains(relicID, "/") {
		return core.RelicHead{}, core.Errorf(core.CategoryCorruptData, "relic ref %q does not name one relic", record.name)
	}
	if err := core.ValidateRelicIDShape(relicID); err != nil {
		return core.RelicHead{}, core.Wrap(core.CategoryCorruptData, "relic ref ID is invalid", err)
	}
	if !keys.OwnsRelic(relicID) {
		return core.RelicHead{}, core.Errorf(core.CategoryCorruptData,
			"relic ref %q carries a project key this project does not have; its keys are: %s",
			record.name, core.KeyNameList(keys))
	}
	if record.symbolicTarget != "" {
		return core.RelicHead{}, core.Errorf(core.CategoryCorruptData, "relic ref %q must not be symbolic", record.name)
	}
	return core.RelicHead{RelicID: relicID, ObjectID: record.objectID}, nil
}

type relicRefRecord struct {
	name           string
	objectID       string
	symbolicTarget string
}

// relicRefRecords lists the refs at or under each pattern through one run of
// Git's ref enumeration, so packed and loose refs behave the same way. Every
// pattern is a ref name or a namespace ending in a slash.
func (r *Repository) relicRefRecords(ctx context.Context, patterns ...string) ([]relicRefRecord, error) {
	contents, err := r.Git(ctx, nil, append([]string{"for-each-ref", "--format=" + taskRefFormat}, patterns...)...)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	records := make([]relicRefRecord, 0, bytes.Count(contents, []byte{'\n'}))
	err = eachRefRecord(contents, "relic ref", func(refName, objectID, symbolicTarget string) error {
		record := relicRefRecord{name: refName, objectID: objectID, symbolicTarget: symbolicTarget}
		if !slices.ContainsFunc(patterns, func(pattern string) bool { return strings.HasPrefix(record.name, pattern) }) {
			return core.Errorf(core.CategoryCorruptData, "Git returned a ref outside the relic namespace")
		}
		// The width comes from Git's own full-width %(objectname), and every
		// later ID in this process is held to it.
		if err := r.rememberGitObjectID(record.objectID); err != nil {
			return core.Wrap(core.CategoryCorruptData, "relic ref object ID is invalid", err)
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, err
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
		requestRelicTipObjects(&input, head.ObjectID)
	}

	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	for index, head := range heads {
		objects, err := readRelicTipObjects(batch)
		if err != nil {
			return nil, namedRelicError(head.RelicID, err)
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

// requestRelicTipObjects asks a Git batch for the five objects one relic
// commit is judged from, in the order readRelicTipObjects returns them: the
// commit, its tree, and its operation.json, state.json and links.json.
func requestRelicTipObjects(input *bytes.Buffer, commit string) {
	fmt.Fprintf(input, "%s\n%s^{tree}\n", commit, commit)
	for _, path := range []string{relicOperationPath, relicStatePath, relicLinksPath} {
		fmt.Fprintf(input, "%s:%s\n", commit, path)
	}
}

// readRelicTipObjects reads from batch the five objects requestRelicTipObjects
// asked for one commit.
func readRelicTipObjects(batch *objectBatch) ([5]batchObject, error) {
	var objects [5]batchObject
	for position := range objects {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return [5]batchObject{}, batch.ReadFailure("cannot read relic objects from Git batch", err)
		}
		objects[position] = object
	}
	return objects, nil
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
		refusal := corruptRelic(head.RelicID, "the documents do not match the configured project")
		// Both documents naming one other project is that project's relic,
		// which a fetch reports as another project's ref rather than as damage.
		if pack.ProjectID == state.ProjectID {
			return core.RelicSnapshot{}, &foreignProjectDocumentsError{ProjectID: pack.ProjectID, cause: refusal}
		}
		return core.RelicSnapshot{}, refusal
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
	if err := core.ValidateRelicLinks(state, links); err != nil {
		return core.RelicSnapshot{}, corruptRelic(head.RelicID, "%v", err)
	}
	// A history is rooted at the commit that creates the relic or, once the
	// relic is compacted, at the checkpoint that holds what it kept.
	switch pack.Operations[0].Type {
	case core.RelicOperationCreate:
		if parentCount != 0 {
			return core.RelicSnapshot{}, corruptRelic(head.RelicID, "the commit that creates the relic must have no parent")
		}
	case core.RelicOperationCheckpoint:
		if parentCount != 0 {
			return core.RelicSnapshot{}, corruptRelic(head.RelicID, "a checkpoint commit must have no parent")
		}
	default:
		if parentCount != 1 {
			return core.RelicSnapshot{}, corruptRelic(head.RelicID, "a commit that changes the relic must have exactly one parent")
		}
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
		return core.RelicSnapshot{}, &relicValidateRefusal{cause: err}
	}
	return snapshot, err
}

// relicValidateRefusal is GetRelic's refusal of a relic commit that breaks a
// rule: the reason, which it keeps as its cause, then a pointer to `workbook
// validate`.
type relicValidateRefusal struct{ cause error }

func (e *relicValidateRefusal) Error() string {
	return e.cause.Error() + `; run "workbook validate" for details`
}

func (e *relicValidateRefusal) Unwrap() error { return e.cause }

// As gives a caller looking for a *core.Error, as the category and a command's
// message do, the whole refusal, so neither loses the pointer to validate.
func (e *relicValidateRefusal) As(target any) bool {
	typed, ok := target.(**core.Error)
	if ok {
		*typed = &core.Error{Category: core.CategoryCorruptData, Message: e.Error()}
	}
	return ok
}
