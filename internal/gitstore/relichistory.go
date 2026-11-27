package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// What makes a relic commit valid. For a commit with pack P, its parent commit
// (none for the root) and its stored record S:
//
//	R1  S is what folding P onto the parent's record computes, byte for byte.
//	R2  The tree holds operation.json, state.json and links.json as regular
//	    blobs and, exactly when the state lists a part, the tree parts. A root
//	    commit has no parent and opens with relic.create or, for a compacted
//	    relic, is one history.checkpoint; every other commit has exactly one
//	    parent.
//	R3  parts/ holds one subtree per part the state lists, named by part ID.
//	    Each holds fields.json, body exactly when the state records a body,
//	    and nothing else.
//	R4  A fields.json is at most 4194304 bytes, the ceiling for one stored
//	    object, and each value in it is at most 16384 bytes. A body is at
//	    most 1048576 bytes and is the size the state records.
//	R5  A part P does not name has the subtree it had in the parent.
//	R6  A part's body differs from the parent's only when this commit saved
//	    it: the state's body version for it is the ID of an operation in P,
//	    and the part is new in this commit or that version is not the one in
//	    the parent's state. A repeated part.body.set, which leaves the
//	    version as it was, therefore cannot carry new text. (A repeated
//	    part.add never gets here: the fold refuses it, which is R1.)
//	R7  A part's fields.json is its parent's with P's field operations for it
//	    applied in order. For a part P adds, it is a canonical fields document
//	    that agrees with any field operations P carries for it.
//
// A stub, a part whose content a checkpoint dropped, has a subtree that never
// changes after the checkpoint: the fold ignores every operation naming one,
// so R5 holds it to its parent's subtree whether P names it or not, and R7
// never applies P's field operations to it.
//
// A checkpoint root has no parent to follow from, so R1, R5, R6 and R7 give
// way to the root rules core.ValidateRelicCheckpointRoot holds its pack and
// record to, and to one more judged by object ID:
//
//	R7  Each stub's fields.json is the canonical empty fields document, the
//	    object Git names it by in this repository's format. Every other
//	    part's fields.json is a canonical fields document, which takes a read
//	    and is checked where the rest of R7 is.
//
// R1 to R6 are judged from object IDs and sizes, except the limit on one field
// value, which takes reading the fields.json that holds it. So is one case of
// R7: a part that P names without setting or clearing a field, and that the
// parent has, keeps its fields.json object. The rest of R7 reads the
// fields.json of the parts P names, and only `workbook validate` pays for
// that; it is also where the limit on one field value is checked for a
// history, since a value enters a fields.json only in a commit that names its
// part. A read of a part's fields checks it for the part read.
//
// No rule limits how large one part's fields are together, or one relic's
// properties: those are totals, checked when a change makes one grow and
// never when a relic is read, like the number of parts.
//
// One thing no rule about a commit and its parent can hold: an operation ID
// is used once in a relic's history, the operations a checkpoint lists as
// replaced included. The fold refuses the reuse the parent's state shows, a
// part's ID or a body's current version taken again. Any other reuse is seen
// only by the walk over the whole history, ValidateRelicHistory, which keeps
// every ID it has passed along that one history, and every ID the checkpoint
// it is rooted at lists, and compares nothing across refs.
//
// The rules are this build's. A commit whose pack or state declares a newer
// generation is not judged by them: it is neither valid nor corrupt here, and
// the commands that would have to know which refuse it as written by a newer
// Workbook.

// RelicPartObjects names what one part stores in one commit: its subtree and
// the blobs in it, with their sizes as Git reports them. Body is empty, and
// BodyBytes zero, when the part has no body.
type RelicPartObjects struct {
	Tree, Fields, Body     string
	FieldsBytes, BodyBytes int64
}

// RelicCommit is one relic commit read for checking against the rules: its
// documents, its parent and the objects of every part its state lists. Parent
// is empty for a root. Parts is nil for a commit this build cannot judge
// because it declares a newer generation.
type RelicCommit struct {
	ObjectID  string
	Parent    string
	Operation core.RelicOperationPack
	Record    core.RelicRecord
	Parts     map[string]RelicPartObjects
}

// RelicHistoryFailure names the first commit of a relic's history that breaks
// a rule, or that this build is too old to check.
type RelicHistoryFailure struct {
	Commit string
	Err    error
}

func (commit RelicCommit) needsNewerReader() bool {
	return commit.Operation.RequiresNewerReader() || commit.Record.State.RequiresNewerReader()
}

func newerRelicHistory(relicID string) error {
	return core.Errorf(core.CategoryNewerWriter,
		"relic %s was written by a newer workbook; upgrade workbook to check its history", relicID)
}

// ReadRelicCommit reads one commit of the relic named relicID and checks what
// can be checked from that commit alone: rules R2, R3 and R4, that its
// documents are the bytes this build writes, and that they belong to this
// project and this relic.
//
// A commit that declares a newer generation is returned as far as it can be
// read, with no Parts and none of this build's rules applied.
func (r *Repository) ReadRelicCommit(
	ctx context.Context,
	config core.ProjectConfig,
	relicID, objectID string,
) (RelicCommit, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return RelicCommit{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return RelicCommit{}, err
	}
	return r.readRelicCommit(ctx, config, relicID, objectID)
}

// readRelicCommit is ReadRelicCommit for a caller that has already checked the
// repository and its configuration. Whatever it cannot read, the error names
// the relic.
func (r *Repository) readRelicCommit(
	ctx context.Context,
	config core.ProjectConfig,
	relicID, objectID string,
) (RelicCommit, error) {
	commits, errs := r.readRelicCommitDocuments(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: objectID}})
	if errs[0] != nil {
		return RelicCommit{}, errs[0]
	}
	commit := commits[0]
	if commit.needsNewerReader() {
		return commit, nil
	}
	parts, err := r.readRelicPartObjects(ctx, objectID, commit.Record.State)
	if err != nil {
		return RelicCommit{}, namedRelicError(relicID, err)
	}
	commit.Parts = parts
	return commit, nil
}

// readRelicCommitsPartial is readRelicCommit for many commits at once, each
// read as the relic its head names. The results are in the order of heads. One
// failure never fails another head: a commit that cannot be read, or that
// breaks rule R2, R3 or R4, leaves a zero commit and an error naming its relic
// in its own place, and every other commit is returned.
//
// Git is started at most four times however many heads there are: once for
// every commit's documents, and three times for their parts, as
// readRelicPartObjectsBatch describes.
func (r *Repository) readRelicCommitsPartial(
	ctx context.Context,
	config core.ProjectConfig,
	heads []core.RelicHead,
) ([]RelicCommit, []error) {
	commits, errs := r.readRelicCommitDocuments(ctx, config, heads)
	r.readRelicCommitParts(ctx, commits, errs)
	return commits, errs
}

// readRelicCommitDocuments reads the commit each head names through one Git
// batch process and checks what a tip read checks, which is everything
// ReadRelicCommit checks short of the parts: each commit comes back with Parts
// nil. A head that cannot be read fails alone. Only a response this reader
// cannot follow fails the heads after it too, because nothing after a garbled
// record can be trusted.
func (r *Repository) readRelicCommitDocuments(
	ctx context.Context,
	config core.ProjectConfig,
	heads []core.RelicHead,
) ([]RelicCommit, []error) {
	commits := make([]RelicCommit, len(heads))
	errs := make([]error, len(heads))
	type request struct{ index, objectIDBytes int }
	requests := make([]request, 0, len(heads))
	var input bytes.Buffer
	for index, head := range heads {
		decoded, err := decodeObjectID(head.ObjectID)
		if err != nil {
			errs[index] = namedRelicError(head.RelicID,
				core.Wrap(core.CategoryValidation, "relic commit object ID is invalid", err))
			continue
		}
		requests = append(requests, request{index: index, objectIDBytes: len(decoded)})
		// The same five objects a tip read asks for, so the same check judges them.
		requestRelicTipObjects(&input, head.ObjectID)
	}
	if len(requests) == 0 {
		return commits, errs
	}
	fail := func(failed []request, err error) {
		for _, request := range failed {
			commits[request.index] = RelicCommit{}
			errs[request.index] = namedRelicError(heads[request.index].RelicID, err)
		}
	}

	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		fail(requests, err)
		return commits, errs
	}
	defer batch.Close()
	for position, request := range requests {
		head := heads[request.index]
		objects, err := readRelicTipObjects(batch)
		if err != nil {
			fail(requests[position:], err)
			return commits, errs
		}
		snapshot, err := validateRelicTip(objects, config, head, request.objectIDBytes)
		if err != nil {
			errs[request.index] = err
			continue
		}
		parent, err := relicCommitParent(objects[0].contents)
		if err != nil {
			errs[request.index] = namedRelicError(head.RelicID, err)
			continue
		}
		commits[request.index] = RelicCommit{
			ObjectID: head.ObjectID, Parent: parent, Operation: snapshot.Operation, Record: snapshot.Record,
		}
	}
	if err := batch.Finish(); err != nil {
		// Git and this reader disagree about the stream, so no record read
		// from it can be trusted.
		fail(requests, err)
	}
	return commits, errs
}

// readRelicCommitParts fills in Parts for every commit that was read, which is
// one whose error is nil, and that this build can judge, through one call of
// readRelicPartObjectsBatch. A commit whose parts break rule R3 or R4, or
// cannot be read, becomes a zero commit with the error in its place.
func (r *Repository) readRelicCommitParts(ctx context.Context, commits []RelicCommit, errs []error) {
	indexes := make([]int, 0, len(commits))
	requests := make([]relicPartsRequest, 0, len(commits))
	for index, commit := range commits {
		if errs[index] != nil || commit.needsNewerReader() {
			continue
		}
		indexes = append(indexes, index)
		requests = append(requests, relicPartsRequest{Commit: commit.ObjectID, State: commit.Record.State})
	}
	parts, partErrs := r.readRelicPartObjectsBatch(ctx, requests)
	for position, index := range indexes {
		if partErrs[position] != nil {
			commits[index], errs[index] = RelicCommit{}, partErrs[position]
			continue
		}
		commits[index].Parts = parts[position]
	}
}

// startRelicObjectBatch starts one Git batch process for requests, which holds
// one object name to a line, and tells the test seam what is being asked for.
func (r *Repository) startRelicObjectBatch(ctx context.Context, requests []byte) (*objectBatch, error) {
	if r.relicObjectObserver != nil {
		for _, request := range bytes.Split(bytes.TrimSuffix(requests, []byte{'\n'}), []byte{'\n'}) {
			r.relicObjectObserver(string(request))
		}
	}
	return r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := writer.Write(requests)
		return err
	})
}

// relicCommitParent returns the parent a commit object names, or "" for a
// commit with none. A relic commit is never a merge.
func relicCommitParent(contents []byte) (string, error) {
	headerEnd := bytes.Index(contents, []byte("\n\n"))
	if headerEnd < 0 {
		return "", core.Errorf(core.CategoryCorruptData, "relic commit has no header terminator")
	}
	parent := ""
	for _, line := range bytes.Split(contents[:headerEnd], []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("parent ")) {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			return "", core.Errorf(core.CategoryCorruptData, "relic commit has an invalid parent header")
		}
		if parent != "" {
			return "", core.Errorf(core.CategoryCorruptData, "relic commit must not be a merge")
		}
		parent = string(fields[1])
		if _, err := decodeObjectID(parent); err != nil {
			return "", core.Wrap(core.CategoryCorruptData, "relic commit parent ID is invalid", err)
		}
	}
	return parent, nil
}

// VerifyRelicTransition checks a commit against its parent, nil for a root.
// Rules R1, R5 and R6, and the case of R7 that object IDs settle, need only
// what ReadRelicCommit returned for the two, and are always checked. The rest
// of R7 is checked when deep is true, by reading the fields.json of the parts
// the commit's pack names through one Git batch process.
//
// Either commit declaring a newer generation is CategoryNewerWriter: this
// build cannot say whether one follows from the other.
func (r *Repository) VerifyRelicTransition(ctx context.Context, parent *RelicCommit, commit RelicCommit, deep bool) error {
	relicID := commit.Record.State.RelicID
	parentID := ""
	if parent != nil {
		parentID = parent.ObjectID
	}
	if commit.Parent != parentID {
		return core.Errorf(core.CategoryValidation,
			"relic %s: commit %s cannot be checked against a commit that is not its parent", relicID, commit.ObjectID)
	}
	if commit.needsNewerReader() || (parent != nil && parent.needsNewerReader()) {
		return newerRelicHistory(relicID)
	}

	var parentRecord *core.RelicRecord
	var parentParts map[string]RelicPartObjects
	if parent != nil {
		parentRecord, parentParts = &parent.Record, parent.Parts
	}
	if err := core.ValidateRelicCheckpoint(parentRecord, commit.Operation, commit.Record); err != nil {
		if core.CategoryOf(err) == core.CategoryNewerWriter {
			return err
		}
		// The fold's refusal is the whole cause: what the pack may not do, or
		// that the stored relic is not what its operations produce.
		return corruptRelic(relicID, "%s", err.Error())
	}

	if _, isCheckpoint := commit.Operation.Checkpoint(); parent == nil && isCheckpoint {
		return r.verifyRelicCheckpointRoot(ctx, commit, deep)
	}

	named := make(map[string]struct{})
	for _, partID := range core.RelicPartsNamedBy(commit.Operation) {
		named[partID] = struct{}{}
	}
	fieldsEdited := relicPartsWithFieldOperations(commit.Operation, commit.Record.State.Relic)
	for _, part := range commit.Record.State.Relic.Parts {
		objects := commit.Parts[part.ID]
		before, existed := parentParts[part.ID]
		if part.Dropped != nil {
			// Only a checkpoint makes a stub, so the parent has it too.
			if !existed || objects.Tree != before.Tree {
				return corruptRelic(relicID, "part %s changed, but its content was dropped when the relic was compacted",
					part.ID)
			}
			continue
		}
		if _, isNamed := named[part.ID]; !isNamed {
			if !existed || objects.Tree != before.Tree {
				return corruptRelic(relicID, "part %s changed, but no operation in the commit names it", part.ID)
			}
			continue
		}
		// before.Body is empty for a part the commit adds, which then has a
		// body only if the commit saved one.
		if objects.Body != before.Body && !core.RelicBodySavedBy(parentRecord, commit.Operation, commit.Record, part.ID) {
			return corruptRelic(relicID, "the body of part %s changed, but no operation in the commit saved it", part.ID)
		}
		// The case of rule R7 that takes no read. A part the commit adds is
		// not held to it: its fields arrive in the tree.
		if _, isEdited := fieldsEdited[part.ID]; existed && !isEdited && objects.Fields != before.Fields {
			return corruptRelic(relicID,
				"the fields of part %s changed, but no operation in the commit sets or clears one", part.ID)
		}
	}
	if !deep {
		return nil
	}
	return r.verifyRelicFields(ctx, parentParts, commit, core.RelicPartsNamedBy(commit.Operation), fieldsEdited)
}

// verifyRelicCheckpointRoot checks the part of a checkpoint root's rules that
// is Git's to judge, once core has held its pack and record to the root
// rules: each stub's subtree holds the empty fields.json and nothing else,
// which R3 has already held to fields.json and no body, and, when deep is
// true, every other part's fields.json is a canonical fields document.
func (r *Repository) verifyRelicCheckpointRoot(ctx context.Context, commit RelicCommit, deep bool) error {
	relicID := commit.Record.State.RelicID
	var live []string
	var emptyFields string
	for _, part := range commit.Record.State.Relic.Parts {
		if part.Dropped == nil {
			live = append(live, part.ID)
			continue
		}
		if emptyFields == "" {
			var err error
			if emptyFields, err = r.emptyRelicFieldsObjectID(ctx); err != nil {
				return err
			}
		}
		if objects := commit.Parts[part.ID]; objects.Fields != emptyFields || objects.Body != "" {
			return corruptRelic(relicID,
				"part %s was dropped when the relic was compacted, so its %s must be the empty one, alone",
				part.ID, relicFieldsPath)
		}
	}
	if !deep {
		return nil
	}
	// Every live part is held to its fields as a part a commit adds is: they
	// arrive in the tree, and nothing in the pack says otherwise.
	return r.verifyRelicFields(ctx, nil, commit, live, nil)
}

// emptyRelicFieldsObjectID returns the object ID Git gives the canonical
// fields.json of a part with no fields, the one a stub stores, in this
// repository's object format. Git computes it without writing the object, once
// for each opened repository.
func (r *Repository) emptyRelicFieldsObjectID(ctx context.Context) (string, error) {
	r.metadataMu.RLock()
	known := r.emptyRelicFields
	r.metadataMu.RUnlock()
	if known != "" {
		return known, nil
	}
	encoded, err := core.EncodeDocument(core.NewRelicFields(nil))
	if err != nil {
		return "", err
	}
	output, err := r.Git(ctx, encoded, "hash-object", "--no-filters", "--stdin")
	if err != nil {
		return "", err
	}
	objectID, err := gitSingleLine(output)
	if err == nil {
		_, err = decodeObjectID(objectID)
	}
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid object ID", err)
	}
	r.metadataMu.Lock()
	r.emptyRelicFields = objectID
	r.metadataMu.Unlock()
	return objectID, nil
}

// relicPartsWithFieldOperations returns the parts a pack sets or clears a
// field of, leaving out every stub of relic, the record the pack folds to. A
// stub's fields never change: the fold ignores an operation naming one, and so
// do the writer, the replay and the rules that read fields.json, which all
// take the parts to rewrite from here.
func relicPartsWithFieldOperations(pack core.RelicOperationPack, relic core.RelicData) map[string]struct{} {
	edited := make(map[string]struct{})
	for _, operation := range pack.Operations {
		switch operation.Type {
		case core.RelicOperationPartFieldSet, core.RelicOperationPartFieldClear:
			if part, listed := relic.Part(operation.Part); listed && part.Dropped != nil {
				continue
			}
			edited[operation.Part] = struct{}{}
		}
	}
	return edited
}

// relicFieldsCheck is one fields.json rule R7 has to read to judge.
type relicFieldsCheck struct {
	partID string
	// before is the parent's fields.json, empty for a part the commit adds.
	before string
	after  string
}

// verifyRelicFields checks the part of rule R7 that has to read fields.json:
// of the parts given, which are the parts a commit's pack names, the ones it
// adds, and the ones it sets or clears a field of. VerifyRelicTransition has
// already held every other part the pack names to the fields.json object it
// had. The blobs are read two at a time, so the check holds one part's fields
// in memory however many parts the pack names.
func (r *Repository) verifyRelicFields(
	ctx context.Context,
	parentParts map[string]RelicPartObjects,
	commit RelicCommit,
	parts []string,
	fieldsEdited map[string]struct{},
) error {
	relicID := commit.Record.State.RelicID
	var checks []relicFieldsCheck
	var input bytes.Buffer
	for _, partID := range parts {
		objects, listed := commit.Parts[partID]
		if !listed {
			// The fold ignored an operation naming a part the relic does not have.
			continue
		}
		before, existed := parentParts[partID]
		if _, isEdited := fieldsEdited[partID]; existed && !isEdited {
			continue
		}
		check := relicFieldsCheck{partID: partID, after: objects.Fields}
		if existed {
			check.before = before.Fields
			fmt.Fprintf(&input, "%s\n", check.before)
		}
		fmt.Fprintf(&input, "%s\n", check.after)
		checks = append(checks, check)
	}
	if len(checks) == 0 {
		return nil
	}

	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return err
	}
	defer batch.Close()
	readFields := func(partID string) ([]byte, error) {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return nil, batch.ReadFailure("cannot read relic part fields from Git batch", err)
		}
		switch {
		case object.missing:
			return nil, corruptRelic(relicID, "part %s: its %s is missing from Git", partID, relicFieldsPath)
		case object.kind != "blob":
			return nil, corruptRelic(relicID, "part %s: its %s is not a blob", partID, relicFieldsPath)
		case object.size > MaxObjectBytes:
			return nil, corruptRelic(relicID, "part %s: its %s is %d bytes; the limit is %d bytes",
				partID, relicFieldsPath, object.size, MaxObjectBytes)
		case object.refused != nil:
			return nil, object.refused
		}
		return object.contents, nil
	}
	for _, check := range checks {
		var before []byte
		if check.before != "" {
			if before, err = readFields(check.partID); err != nil {
				return err
			}
		}
		after, err := readFields(check.partID)
		if err != nil {
			return err
		}
		if err := checkRelicFields(relicID, check, commit.Operation.Operations, before, after); err != nil {
			return err
		}
	}
	return batch.Finish()
}

// checkRelicFields judges one part's fields.json by rule R7. before is the
// parent's fields.json, and is not used for a part the commit adds.
func checkRelicFields(relicID string, check relicFieldsCheck, operations []core.RelicOperation, before, after []byte) error {
	partID := check.partID
	if check.before == "" {
		// A new part's fields arrive in the tree, so there is nothing for them
		// to follow from. They must be the document this build writes, and a
		// field operation in the same pack must not say otherwise.
		stored, err := core.DecodeRelicFieldsDocument(after)
		if err != nil {
			return corruptRelic(relicID, "part %s: its %s is invalid: %v", partID, relicFieldsPath, err)
		}
		if err := requireCanonicalRelicDocument(relicFieldsPath, after, stored); err != nil {
			return corruptRelic(relicID, "part %s: its %s is not canonical", partID, relicFieldsPath)
		}
		if !relicFieldsAre(after, stored.Fields, partID, operations) {
			return corruptRelic(relicID,
				"the fields of part %s disagree with the field operations in the commit that adds it", partID)
		}
		return nil
	}
	previous, err := core.DecodeRelicFieldsDocument(before)
	if err != nil {
		return corruptRelic(relicID, "part %s: the %s it had before the commit is invalid: %v", partID, relicFieldsPath, err)
	}
	if !relicFieldsAre(after, previous.Fields, partID, operations) {
		return corruptRelic(relicID,
			"the fields of part %s are not the ones it had with the commit's field operations applied", partID)
	}
	return nil
}

// relicFieldsAre reports whether stored is byte for byte the fields.json of
// fields with the part's field operations applied in order.
func relicFieldsAre(stored []byte, fields map[string]json.RawMessage, partID string, operations []core.RelicOperation) bool {
	// Fields that cannot be encoded, for one because a value is over the limit
	// for one value, are not what any stored blob a reader accepts holds.
	expected, err := core.EncodeDocument(core.NewRelicFields(core.ApplyRelicFields(fields, partID, operations)))
	return err == nil && bytes.Equal(expected, stored)
}

// verifyRelicTips reads the commit each head names and checks it as a
// single-relic read does: alone by rules R2 to R4, and against its first
// parent by rules R1, R5 and R6 and the case of R7 that object IDs settle. A
// state or a body that does not follow from the fold is refused, and so are
// fields that changed with no operation to change them. The rest of R7, and
// anything further back in the history, is `workbook validate`'s to check.
//
// The results are in the order of heads, and one failure never fails another
// head: a tip that fails leaves a zero commit and an error naming its relic.
// A tip that declares a newer generation cannot be checked. Its error is
// CategoryNewerWriter, and it alone is returned beside its error, as far as it
// could be read, so that a caller can tell it from a commit of this build's
// generation that sits on top of a newer one, which fails the same way with a
// zero commit.
//
// The caller has checked the repository and its configuration. Git is started
// at most five times however many heads there are: for the tips' documents,
// for their parents' documents, and three times for the parts of both
// together, so that a subtree a tip shares with its parent is read once.
func (r *Repository) verifyRelicTips(
	ctx context.Context,
	config core.ProjectConfig,
	heads []core.RelicHead,
) ([]RelicCommit, []error) {
	commits, errs := r.readRelicCommitDocuments(ctx, config, heads)
	// parentAt is where each tip's parent is read to, after the tips, or -1
	// for a tip with no parent to read.
	parentAt := make([]int, len(heads))
	var parentHeads []core.RelicHead
	for index, commit := range commits {
		parentAt[index] = -1
		if errs[index] != nil || commit.needsNewerReader() || commit.Parent == "" {
			continue
		}
		parentAt[index] = len(heads) + len(parentHeads)
		parentHeads = append(parentHeads, core.RelicHead{RelicID: heads[index].RelicID, ObjectID: commit.Parent})
	}
	parents, parentErrs := r.readRelicCommitDocuments(ctx, config, parentHeads)
	commits, errs = append(commits, parents...), append(errs, parentErrs...)
	r.readRelicCommitParts(ctx, commits, errs)

	for index, head := range heads {
		if errs[index] != nil {
			continue
		}
		if commits[index].needsNewerReader() {
			errs[index] = newerRelicHistory(head.RelicID)
			continue
		}
		var parent *RelicCommit
		if at := parentAt[index]; at >= 0 {
			if errs[at] != nil {
				commits[index], errs[index] = RelicCommit{}, errs[at]
				continue
			}
			parent = &commits[at]
		}
		if err := r.VerifyRelicTransition(ctx, parent, commits[index], false); err != nil {
			commits[index], errs[index] = RelicCommit{}, namedRelicError(head.RelicID, err)
		}
	}
	return commits[:len(heads):len(heads)], errs[:len(heads):len(heads)]
}

// verifiedRelicTip reads the commit a relic's ref points at and checks it as
// verifyRelicTips does, which is what a single-relic read promises.
//
// A tip that declares a newer generation is returned unverified, as far as it
// can be read. Whoever uses it sees the marker and refuses to change it.
func (r *Repository) verifiedRelicTip(
	ctx context.Context,
	config core.ProjectConfig,
	relicID, head string,
) (core.RelicSnapshot, error) {
	commits, errs := r.verifyRelicTips(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: head}})
	commit, err := commits[0], errs[0]
	// Only a newer writer's own tip comes back beside its error.
	if err != nil && !commit.needsNewerReader() {
		return core.RelicSnapshot{}, err
	}
	return core.RelicSnapshot{Head: commit.ObjectID, Operation: commit.Operation, Record: commit.Record}, nil
}

// relicHistoryGap finds why Git could not walk a relic's history from head: a
// commit that cannot be read, or one that names a parent Git does not have. It
// returns the commit to report and the reason, or a nil reason when every
// commit down to the one that created the relic is there, so that the walk
// failed for some cause this cannot see.
//
// It starts from the oldest commit Git reached before it gave up, which a walk
// that is not reversed prints as it goes, and follows parents from there by
// hand. That listing is only a place to start: nothing is concluded from it.
func (r *Repository) relicHistoryGap(ctx context.Context, config core.ProjectConfig, head core.RelicHead) (string, error) {
	objectID := head.ObjectID
	reached := strings.Fields(string(r.gitWithEnvResult(ctx, nil, nil, "rev-list", "--first-parent", head.ObjectID).stdout))
	if len(reached) > 0 {
		if _, err := decodeObjectID(reached[len(reached)-1]); err == nil {
			objectID = reached[len(reached)-1]
		}
	}
	for ctx.Err() == nil {
		commit, err := r.readRelicCommit(ctx, config, head.RelicID, objectID)
		if err != nil {
			return objectID, err
		}
		if commit.Parent == "" {
			return "", nil
		}
		if _, err := r.Git(ctx, nil, "cat-file", "-e", commit.Parent); err != nil {
			if !gitExitedWithFailure(ctx, err) {
				return "", nil
			}
			return objectID, corruptRelic(head.RelicID,
				"commit %s: its parent %s is missing from Git, so the history cannot be followed past it", objectID, commit.Parent)
		}
		objectID = commit.Parent
	}
	return "", nil
}

// ValidateRelicHistory checks every commit of one relic's history, root to
// tip, against all seven rules and for an operation ID an earlier commit of
// the same history already used, and stops at the first that fails. checked is
// the number of commits that passed before it, which is the whole history when
// failure is nil.
//
// A commit that breaks a rule, and a commit this build is too old to check
// (CategoryNewerWriter), are reported as the failure. err is for a history
// that could not be read at all.
func (r *Repository) ValidateRelicHistory(
	ctx context.Context,
	config core.ProjectConfig,
	head core.RelicHead,
) (checked int, failure *RelicHistoryFailure, err error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return 0, nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return 0, nil, err
	}
	if _, err := decodeObjectID(head.ObjectID); err != nil {
		return 0, nil, core.Wrap(core.CategoryValidation, "relic head object ID is invalid", err)
	}
	// failed sorts a refusal into the failure this history is reported with,
	// or an error that says nothing about the history.
	failed := func(commit string, refusal error) (int, *RelicHistoryFailure, error) {
		refusal = namedRelicError(head.RelicID, refusal)
		switch core.CategoryOf(refusal) {
		case core.CategoryCorruptData, core.CategoryNewerWriter:
			if ctx.Err() == nil {
				return checked, &RelicHistoryFailure{Commit: commit, Err: refusal}, nil
			}
		}
		return checked, nil, refusal
	}

	output, err := r.Git(ctx, nil, "rev-list", "--reverse", "--first-parent", head.ObjectID)
	commits := strings.Fields(string(output))
	if err != nil || len(commits) == 0 || commits[len(commits)-1] != head.ObjectID {
		// Git walks only from a commit it has: it fails for an object it does
		// not have, and lists nothing for a tree or a blob. Why this head is
		// not a relic commit is the head's failure to report, and reading it
		// says why.
		if _, readErr := r.readRelicCommit(ctx, config, head.RelicID, head.ObjectID); readErr != nil {
			return failed(head.ObjectID, readErr)
		}
		if err != nil {
			// The head is a relic commit, and Git still could not walk from
			// it: some commit further back is not in the object store. Which
			// one is this relic's failure to report, in words of our own.
			if gitExitedWithFailure(ctx, err) {
				if commit, gap := r.relicHistoryGap(ctx, config, head); gap != nil {
					return failed(commit, gap)
				}
			}
			return 0, nil, namedRelicError(head.RelicID, err)
		}
		return 0, nil, core.Errorf(core.CategoryOperational,
			"relic %s: Git did not list the history of commit %s", head.RelicID, head.ObjectID)
	}

	// usedIn is the commit each operation ID of this history first appeared
	// in. It covers this one walk and nothing else: another ref may hold the
	// same commits, and so the same IDs, and that is no fault of either.
	usedIn := map[string]string{}
	// compactedBy is the checkpoint the history is rooted at, when it is, and
	// compacted the operations that checkpoint replaced. No later commit may
	// record one of them again.
	compactedBy, compacted := "", map[string]struct{}{}
	var previous *RelicCommit
	for _, objectID := range commits {
		commit, err := r.readRelicCommit(ctx, config, head.RelicID, objectID)
		if err != nil {
			return failed(objectID, err)
		}
		if commit.needsNewerReader() {
			return failed(objectID, newerRelicHistory(head.RelicID))
		}
		if previous == nil && commit.Parent != "" {
			// Git listed this commit first, so it does not have the parent the
			// commit names.
			return failed(objectID, corruptRelic(head.RelicID,
				"the history does not reach the commit that created the relic"))
		}
		if err := r.VerifyRelicTransition(ctx, previous, commit, true); err != nil {
			return failed(objectID, err)
		}
		// An operation ID is used once in a relic's history. The rules above
		// judge a commit against its parent alone, and the parent's state
		// keeps only some IDs: a body's earlier versions and most operations
		// leave no trace in it, so only the walk can see one used again.
		for _, operation := range commit.Operation.Operations {
			if first, used := usedIn[operation.ID]; used {
				return failed(objectID, corruptRelic(head.RelicID,
					"operation ID %s is used in commit %s and again in commit %s; an operation ID is used once in a relic's history",
					operation.ID, first, objectID))
			}
			if _, replaced := compacted[operation.ID]; replaced {
				return failed(objectID, corruptRelic(head.RelicID,
					"operation ID %s is used in commit %s, but checkpoint %s lists it among the changes it replaced; an operation ID is used once in a relic's history",
					operation.ID, objectID, compactedBy))
			}
		}
		if summary, isCheckpoint := commit.Operation.Checkpoint(); previous == nil && isCheckpoint {
			compactedBy = objectID
			for _, id := range summary.CompactedOperations {
				compacted[id] = struct{}{}
			}
		}
		for _, operation := range commit.Operation.Operations {
			usedIn[operation.ID] = objectID
		}
		checked++
		previous = &commit
	}
	return checked, nil, nil
}
