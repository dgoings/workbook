package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// What makes a relic commit valid. For a commit with pack P, its parent commit
// (none for the root) and its stored record S:
//
//	R1  S is what folding P onto the parent's record computes, byte for byte.
//	R2  The tree holds operation.json, state.json and links.json as regular
//	    blobs and, exactly when the state lists a part, the tree parts. A root
//	    commit has no parent and opens with relic.create; every other commit
//	    has exactly one parent.
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
// is used once in a relic's history. The fold refuses the reuse the parent's
// state shows, a part's ID or a body's current version taken again. Any other
// reuse is seen only by the walk over the whole history, ValidateRelicHistory,
// which keeps every ID it has passed along that one history and compares
// nothing across refs.
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
// repository and its configuration.
func (r *Repository) readRelicCommit(
	ctx context.Context,
	config core.ProjectConfig,
	relicID, objectID string,
) (RelicCommit, error) {
	decoded, err := decodeObjectID(objectID)
	if err != nil {
		return RelicCommit{}, core.Wrap(core.CategoryValidation, "relic commit object ID is invalid", err)
	}
	// The same five objects a tip read asks for, so the same check judges them.
	var input bytes.Buffer
	fmt.Fprintf(&input, "%s\n%s^{tree}\n", objectID, objectID)
	for _, path := range []string{relicOperationPath, relicStatePath, relicLinksPath} {
		fmt.Fprintf(&input, "%s:%s\n", objectID, path)
	}
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := writer.Write(input.Bytes())
		return err
	})
	if err != nil {
		return RelicCommit{}, err
	}
	defer batch.Close()
	var objects [5]batchObject
	for position := range objects {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return RelicCommit{}, batch.ReadFailure("cannot read relic objects from Git batch", err)
		}
		objects[position] = object
	}
	if err := batch.Finish(); err != nil {
		return RelicCommit{}, err
	}

	snapshot, err := validateRelicTip(objects, config, core.RelicHead{RelicID: relicID, ObjectID: objectID}, len(decoded))
	if err != nil {
		return RelicCommit{}, err
	}
	parent, err := relicCommitParent(objects[0].contents)
	if err != nil {
		return RelicCommit{}, err
	}
	commit := RelicCommit{ObjectID: objectID, Parent: parent, Operation: snapshot.Operation, Record: snapshot.Record}
	if commit.needsNewerReader() {
		return commit, nil
	}
	commit.Parts, err = r.readRelicPartObjects(ctx, objectID, snapshot.Record.State)
	if err != nil {
		return RelicCommit{}, err
	}
	return commit, nil
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

// readRelicPartObjects lists a commit's tree with one Git process and returns
// the objects of every part the state lists, checked against rules R3 and R4.
// Nothing under parts/ is opened: Git reports each blob's size.
func (r *Repository) readRelicPartObjects(
	ctx context.Context,
	commit string,
	state core.RelicStateDocument,
) (map[string]RelicPartObjects, error) {
	if len(state.Relic.Parts) == 0 {
		// Rule R2 has already established that the commit has no parts tree.
		return map[string]RelicPartObjects{}, nil
	}
	// -t lists the subtrees themselves, whose object IDs rule R5 compares. -z
	// ends each record with a NUL and leaves names unquoted.
	listing, err := r.Git(ctx, nil, "ls-tree", "-r", "-t", "-l", "-z", commit)
	if err != nil {
		return nil, err
	}
	return relicPartObjectsFromListing(listing, state)
}

// relicPartObjectsFromListing reads `git ls-tree -r -t -l -z` output for a
// relic commit. Each record is
//
//	<mode> SP <type> SP <object> SP+ <size> TAB <path> NUL
//
// where the size is "-" for a tree.
func relicPartObjectsFromListing(listing []byte, state core.RelicStateDocument) (map[string]RelicPartObjects, error) {
	relicID := state.RelicID
	listed := make(map[string]struct{}, len(state.Relic.Parts))
	for _, part := range state.Relic.Parts {
		listed[part.ID] = struct{}{}
	}
	invalidListing := core.Errorf(core.CategoryOperational, "Git returned an invalid tree listing for relic %s", relicID)
	if len(listing) > 0 && listing[len(listing)-1] != 0 {
		return nil, invalidListing
	}

	parts := make(map[string]RelicPartObjects, len(listed))
	var err error
	for len(listing) > 0 {
		end := bytes.IndexByte(listing, 0)
		record := listing[:end]
		listing = listing[end+1:]
		meta, path, found := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !found || len(fields) != 4 {
			return nil, invalidListing
		}
		mode, kind, objectID, size := fields[0], fields[1], fields[2], fields[3]
		if _, err := decodeObjectID(objectID); err != nil {
			return nil, invalidListing
		}

		segments := strings.Split(string(path), "/")
		if len(segments) == 1 {
			// The top of the tree was judged with the documents, by rule R2.
			continue
		}
		partID := segments[1]
		if _, isListed := listed[partID]; segments[0] != relicPartsPath || !isListed {
			// The name is whatever the tree holds, so it is repeated only when
			// it has the shape of a part ID.
			if segments[0] == relicPartsPath && core.ValidatePartID(partID) == nil {
				return nil, corruptRelic(relicID, "the commit stores part %s, which the relic does not list", partID)
			}
			return nil, corruptRelic(relicID, "the commit stores an entry under %s that is not a part the relic lists", relicPartsPath)
		}
		objects := parts[partID]
		isBlob := mode == regularBlobMode && kind == "blob"
		switch {
		case len(segments) == 2:
			// Git lists a subtree's mode with its leading zero.
			if mode != "040000" || kind != "tree" || objects.Tree != "" {
				return nil, corruptRelic(relicID, "part %s: its entry is not one subtree", partID)
			}
			objects.Tree = objectID
		case len(segments) == 3 && segments[2] == relicFieldsPath:
			if !isBlob || objects.Fields != "" {
				return nil, corruptRelic(relicID, "part %s: its %s is not one regular blob", partID, relicFieldsPath)
			}
			objects.Fields = objectID
			if objects.FieldsBytes, err = relicListedBlobSize(relicID, partID, relicFieldsPath, size); err != nil {
				return nil, err
			}
		case len(segments) == 3 && segments[2] == relicBodyPath:
			if !isBlob || objects.Body != "" {
				return nil, corruptRelic(relicID, "part %s: its %s is not one regular blob", partID, relicBodyPath)
			}
			objects.Body = objectID
			if objects.BodyBytes, err = relicListedBlobSize(relicID, partID, relicBodyPath, size); err != nil {
				return nil, err
			}
		default:
			return nil, corruptRelic(relicID, "part %s: its subtree holds an entry that is neither %s nor %s",
				partID, relicFieldsPath, relicBodyPath)
		}
		parts[partID] = objects
	}

	// The state's own order, so that the same commit always names the same
	// part first.
	for _, part := range state.Relic.Parts {
		objects := parts[part.ID]
		switch {
		case objects.Tree == "":
			return nil, corruptRelic(relicID, "the commit stores no subtree for part %s", part.ID)
		case objects.Fields == "":
			return nil, corruptRelic(relicID, "part %s: it has no %s", part.ID, relicFieldsPath)
		case objects.FieldsBytes > MaxObjectBytes:
			return nil, corruptRelic(relicID, "part %s: its %s is %d bytes; the limit is %d bytes",
				part.ID, relicFieldsPath, objects.FieldsBytes, MaxObjectBytes)
		case part.Body == nil && objects.Body != "":
			return nil, corruptRelic(relicID, "part %s: it stores a body the relic does not record", part.ID)
		case part.Body == nil:
			// Neither records nor stores a body.
		case objects.Body == "":
			return nil, corruptRelic(relicID, "part %s: the relic records a body it does not store", part.ID)
		case objects.BodyBytes > core.MaxRelicBodyBytes:
			return nil, corruptRelic(relicID, "part %s: its body is %d bytes; the limit is %d bytes",
				part.ID, objects.BodyBytes, core.MaxRelicBodyBytes)
		case objects.BodyBytes != part.Body.Bytes:
			return nil, corruptRelic(relicID, "part %s: its body is %d bytes, but the relic records %d bytes",
				part.ID, objects.BodyBytes, part.Body.Bytes)
		}
	}
	return parts, nil
}

// relicListedBlobSize reads the size Git listed for one of a part's blobs. Git
// lists a blob it does not have with a size that is not a number.
func relicListedBlobSize(relicID, partID, what, listed string) (int64, error) {
	size, err := strconv.ParseInt(listed, 10, 64)
	if err != nil || size < 0 {
		return 0, corruptRelic(relicID, "part %s: its %s is missing from Git", partID, what)
	}
	return size, nil
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
		return core.Wrap(core.CategoryCorruptData,
			fmt.Sprintf("relic %s: the stored relic is not what the commit's operations produce", relicID), err)
	}

	named := make(map[string]struct{})
	for _, partID := range core.RelicPartsNamedBy(commit.Operation) {
		named[partID] = struct{}{}
	}
	fieldsEdited := relicPartsWithFieldOperations(commit.Operation)
	for _, part := range commit.Record.State.Relic.Parts {
		objects := commit.Parts[part.ID]
		before, existed := parentParts[part.ID]
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
	return r.verifyRelicFields(ctx, parentParts, commit, fieldsEdited)
}

// relicPartsWithFieldOperations returns the parts a pack sets or clears a
// field of.
func relicPartsWithFieldOperations(pack core.RelicOperationPack) map[string]struct{} {
	edited := make(map[string]struct{})
	for _, operation := range pack.Operations {
		switch operation.Type {
		case core.RelicOperationPartFieldSet, core.RelicOperationPartFieldClear:
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
// the parts a commit's pack adds, and the ones it sets or clears a field of.
// VerifyRelicTransition has already held every other part the pack names to
// the fields.json object it had. The blobs are read two at a time, so the
// check holds one part's fields in memory however many parts the pack names.
func (r *Repository) verifyRelicFields(
	ctx context.Context,
	parentParts map[string]RelicPartObjects,
	commit RelicCommit,
	fieldsEdited map[string]struct{},
) error {
	relicID := commit.Record.State.RelicID
	var checks []relicFieldsCheck
	var input bytes.Buffer
	for _, partID := range core.RelicPartsNamedBy(commit.Operation) {
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

	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := writer.Write(input.Bytes())
		return err
	})
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

// verifiedRelicTip reads the commit a relic's ref points at and checks it
// against its parent by rules R1 to R6 and the case of R7 that object IDs
// settle, which is what a single-relic read promises: a state or a body that
// does not follow from the fold is refused, and so are fields that changed
// with no operation to change them.
//
// A tip that declares a newer generation is returned unverified, as far as it
// can be read. Whoever uses it sees the marker and refuses to change it.
func (r *Repository) verifiedRelicTip(
	ctx context.Context,
	config core.ProjectConfig,
	relicID, head string,
) (core.RelicSnapshot, error) {
	commit, err := r.readRelicCommit(ctx, config, relicID, head)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	snapshot := core.RelicSnapshot{Head: commit.ObjectID, Operation: commit.Operation, Record: commit.Record}
	if commit.needsNewerReader() {
		return snapshot, nil
	}
	var parent *RelicCommit
	if commit.Parent != "" {
		read, err := r.readRelicCommit(ctx, config, relicID, commit.Parent)
		if err != nil {
			return core.RelicSnapshot{}, err
		}
		parent = &read
	}
	if err := r.VerifyRelicTransition(ctx, parent, commit, false); err != nil {
		return core.RelicSnapshot{}, err
	}
	return snapshot, nil
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
			return 0, nil, err
		}
		return 0, nil, core.Errorf(core.CategoryOperational,
			"relic %s: Git did not list the history of commit %s", head.RelicID, head.ObjectID)
	}

	// usedIn is the commit each operation ID of this history first appeared
	// in. It covers this one walk and nothing else: another ref may hold the
	// same commits, and so the same IDs, and that is no fault of either.
	usedIn := map[string]string{}
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
		}
		for _, operation := range commit.Operation.Operations {
			usedIn[operation.ID] = objectID
		}
		checked++
		previous = &commit
	}
	return checked, nil, nil
}
