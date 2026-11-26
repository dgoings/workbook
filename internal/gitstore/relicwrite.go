package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

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
		r.noteRelicRefsMoved()
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
				mode: mktreeSubtreeMode, kind: "tree", objectID: inherited.parts[part.ID], name: part.ID,
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
		top = append(top, relicTreeEntry{mode: mktreeSubtreeMode, kind: "tree", objectID: partsTree, name: relicPartsPath})
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
