package gitstore

import (
	"bytes"
	"context"
	"fmt"

	"github.com/dgoings/workbook/internal/core"
)

// A relic whose local and fetched tips have diverged is reconciled by replay:
// each pack only this clone holds, oldest first, is written again on top of the
// fetched tip, as core.PlanRelicReplay decides. The shape of the result is
//
//	shared - r1 - r2                 fetched tip (origin's side)
//	       \                \
//	        l1 - l2          l1' - l2'   Next
//
// l1' records l1's operations byte for byte except the logical clock, in a
// commit of its own whose parent is r2. A pack origin already holds, or that
// would change nothing, is not written. When replay stops on a conflict, Next
// is the fetched tip plus the packs written before the stop, and the old local
// tip l2 is returned as ParkedHead for the caller to keep.
//
// Nothing here moves a ref or talks to a remote. Every object written is
// unreferenced until the caller moves the relic's ref, so a replay that stops
// halfway has changed nothing anyone can see.

// relicReconcileRequest names one relic's two tips: Local is the canonical
// ref's, Remote the fetched one's.
type relicReconcileRequest struct {
	RelicID, Local, Remote string
}

// relicReconcileOutcome is what replaying one relic's local packs produced.
// Next is the fetched tip plus every pack replayed. ParkedHead is the old local
// tip, set only when Conflict is set. With Err set, Next is empty and nothing
// may move. Skipped counts the local packs not written because origin already
// holds them or because they would change nothing.
type relicReconcileOutcome struct {
	RelicID, Next, ParkedHead string
	Replayed, Skipped         int
	Conflict                  *core.RelicConflict
	Warnings                  []core.Warning
	Err                       error
}

// classifyRelicHeads sorts pairs of tips by how they relate, with one Git
// ancestry walk over every tip that differs: ahead is local ahead of remote,
// behind is remote ahead of local, and diverged is neither. A pair whose tips
// are equal is in none of them. graph is the walk's parent graph, which holds
// everything reconcileDivergentRelics needs to find each diverged relic's
// shared commit and both sides' own commits.
func (r *Repository) classifyRelicHeads(
	ctx context.Context,
	pairs []relicReconcileRequest,
) (ahead, behind, diverged []relicReconcileRequest, graph map[string][]string, err error) {
	graph = map[string][]string{}
	if len(pairs) == 0 {
		return nil, nil, nil, graph, nil
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, nil, nil, nil, err
	}
	seen := make(map[string]struct{}, len(pairs))
	unequal := make([]relicReconcileRequest, 0, len(pairs))
	seenHeads := make(map[string]struct{}, len(pairs)*2)
	var input bytes.Buffer
	for _, pair := range pairs {
		if _, duplicate := seen[pair.RelicID]; duplicate {
			return nil, nil, nil, nil, core.Errorf(core.CategoryCorruptData,
				"relic head pairs name relic %s twice", pair.RelicID)
		}
		seen[pair.RelicID] = struct{}{}
		for _, head := range []string{pair.Local, pair.Remote} {
			if err := r.validateFullObjectID(head); err != nil {
				return nil, nil, nil, nil, core.Wrap(core.CategoryCorruptData,
					fmt.Sprintf("relic %s: head object ID is invalid", pair.RelicID), err)
			}
		}
		if pair.Local == pair.Remote {
			continue
		}
		unequal = append(unequal, pair)
		for _, head := range []string{pair.Local, pair.Remote} {
			if _, repeated := seenHeads[head]; repeated {
				continue
			}
			seenHeads[head] = struct{}{}
			fmt.Fprintln(&input, head)
		}
	}
	if len(unequal) == 0 {
		return nil, nil, nil, graph, nil
	}

	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, nil, nil, nil, core.Wrap(core.CategoryCorruptData, "cannot classify relic head relationships", err)
	}
	graph, err = parseParentGraph(output)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for objectID, parents := range graph {
		for _, id := range append([]string{objectID}, parents...) {
			if err := r.validateFullObjectID(id); err != nil {
				return nil, nil, nil, nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid parent graph object ID", err)
			}
		}
	}
	if err := validateCompleteParentGraph(graph); err != nil {
		return nil, nil, nil, nil, err
	}

	for _, pair := range unequal {
		for _, head := range []string{pair.Local, pair.Remote} {
			if _, found := graph[head]; !found {
				return nil, nil, nil, nil, core.Errorf(core.CategoryCorruptData,
					"relic %s: Git parent graph omitted head %s", pair.RelicID, head)
			}
		}
		switch {
		case graphReaches(graph, pair.Local, pair.Remote):
			ahead = append(ahead, pair)
		case graphReaches(graph, pair.Remote, pair.Local):
			behind = append(behind, pair)
		default:
			diverged = append(diverged, pair)
		}
	}
	return ahead, behind, diverged, graph, nil
}

// relicDivergence returns the commit a relic's two histories share and each
// side's own commits, oldest first. Both are single-parent chains, so one walk
// of the graph answers it without a Git process.
func relicDivergence(graph map[string][]string, request relicReconcileRequest) (base string, local, remote []string, err error) {
	remoteChain, err := commitChain(graph, request.Remote)
	if err != nil {
		return "", nil, nil, corruptRelic(request.RelicID, "cannot walk the fetched history: %v", err)
	}
	localChain, err := commitChain(graph, request.Local)
	if err != nil {
		return "", nil, nil, corruptRelic(request.RelicID, "cannot walk the local history: %v", err)
	}
	position := make(map[string]int, len(remoteChain))
	for index, objectID := range remoteChain {
		position[objectID] = index
	}
	for index, objectID := range localChain {
		at, shared := position[objectID]
		if !shared {
			continue
		}
		return objectID, oldestFirst(localChain[:index]), oldestFirst(remoteChain[:at]), nil
	}
	return "", nil, nil, corruptRelic(request.RelicID, "the local and fetched histories share no commit")
}

// relicChainPacks reads the operation packs of commits, all of the relic
// named relicID, in order, through one Git batch process. It is for origin's
// side of a divergence, whose parts a replay never needs.
func (r *Repository) relicChainPacks(
	ctx context.Context,
	config core.ProjectConfig,
	relicID string,
	commits []string,
) ([]core.RelicOperationPack, error) {
	heads := make([]core.RelicHead, len(commits))
	for index, commit := range commits {
		heads[index] = core.RelicHead{RelicID: relicID, ObjectID: commit}
	}
	read, errs := r.readRelicCommitDocuments(ctx, config, heads)
	packs := make([]core.RelicOperationPack, len(read))
	for index, commit := range read {
		if errs[index] != nil {
			return nil, errs[index]
		}
		packs[index] = commit.Operation
	}
	return packs, nil
}

// reconcileDivergentRelics replays each diverged relic's local packs onto its
// fetched tip, as relicReconcileOutcome describes, and returns one outcome per
// request in the same order. graph is classifyRelicHeads's. One relic's
// failure never fails another's.
//
// The commits each replay builds on are read together, through the readers
// readRelicCommitsPartial uses: the shared commit, the local commits and the
// fetched tip. Origin's own commits are read for their packs alone. No body
// is opened; a part's fields.json is read only when both sides changed that
// part's fields.
func (r *Repository) reconcileDivergentRelics(
	ctx context.Context,
	config core.ProjectConfig,
	requests []relicReconcileRequest,
	graph map[string][]string,
) []relicReconcileOutcome {
	outcomes := make([]relicReconcileOutcome, len(requests))
	type divergence struct {
		remote []string
		// at is where the shared commit is among heads; the local commits and
		// then the fetched tip follow it.
		at, locals int
	}
	divergences := make([]divergence, len(requests))
	var heads []core.RelicHead
	for index, request := range requests {
		outcomes[index].RelicID = request.RelicID
		base, local, remote, err := relicDivergence(graph, request)
		if err == nil && (len(local) == 0 || len(remote) == 0) {
			err = core.Errorf(core.CategoryCorruptData,
				"relic %s: the local and fetched tips have not diverged, so there is nothing to replay", request.RelicID)
		}
		if err != nil {
			outcomes[index].Err = err
			continue
		}
		divergences[index] = divergence{remote: remote, at: len(heads), locals: len(local)}
		heads = append(heads, core.RelicHead{RelicID: request.RelicID, ObjectID: base})
		for _, commit := range local {
			heads = append(heads, core.RelicHead{RelicID: request.RelicID, ObjectID: commit})
		}
		heads = append(heads, core.RelicHead{RelicID: request.RelicID, ObjectID: request.Remote})
	}
	if len(heads) == 0 {
		return outcomes
	}
	commits, errs := r.readRelicCommitsPartial(ctx, config, heads)

	for index, request := range requests {
		if outcomes[index].Err != nil {
			continue
		}
		divergence := divergences[index]
		end := divergence.at + divergence.locals + 2
		var err error
		for _, readErr := range errs[divergence.at:end] {
			if readErr != nil {
				err = readErr
				break
			}
		}
		if err != nil {
			outcomes[index].Err = namedRelicError(request.RelicID, err)
			continue
		}
		outcomes[index] = r.replayRelic(ctx, config, request, commits[divergence.at:end], divergence.remote)
	}
	return outcomes
}

// replayRelic replays one relic. read is the shared commit, then the local
// commits oldest first, then the fetched tip; remote is origin's own commits.
func (r *Repository) replayRelic(
	ctx context.Context,
	config core.ProjectConfig,
	request relicReconcileRequest,
	read []RelicCommit,
	remote []string,
) relicReconcileOutcome {
	relicID := request.RelicID
	fail := func(err error) relicReconcileOutcome {
		return relicReconcileOutcome{RelicID: relicID, Err: namedRelicError(relicID, err)}
	}
	base, locals, tip := read[0], read[1:len(read)-1], read[len(read)-1]

	// A replay reuses the blobs of the local commits, so each has to follow
	// from its parent as a read of it would demand. This reads nothing more.
	for index := range locals {
		parent := &base
		if index > 0 {
			parent = &locals[index-1]
		}
		if err := r.VerifyRelicTransition(ctx, parent, locals[index], false); err != nil {
			return fail(err)
		}
	}

	remotePacks, err := r.relicChainPacks(ctx, config, relicID, remote)
	if err != nil {
		return fail(err)
	}
	held := map[string]core.RelicOperation{}
	for _, pack := range remotePacks {
		for _, operation := range pack.Operations {
			held[operation.ID] = operation
		}
	}

	outcome := relicReconcileOutcome{RelicID: relicID}
	parent := tip
	var replayed []core.RelicOperationPack
	for index, source := range locals {
		sourceParent := base
		if index > 0 {
			sourceParent = locals[index-1]
		}
		pack := source.Operation
		step, err := core.PlanRelicReplay(parent.Record, pack, core.RelicReplayInput{
			Held: held, SameBody: sameRelicBodies(parent, source, pack),
		})
		if err != nil {
			return fail(err)
		}
		if step.Conflict != nil {
			conflict := *step.Conflict
			conflict.Unreplayed = len(locals) - index
			conflict.Unapplied = relicUnappliedOperations(locals[index:], step.ConflictOperation, held)
			if conflict.Type == core.RelicConflictBody {
				conflict.Body = relicBodyConflictAt(base, locals[len(locals)-1], parent, conflict.Part)
			}
			outcome.Conflict, outcome.ParkedHead = &conflict, request.Local
			break
		}
		if step.Skip {
			outcome.Skipped++
			continue
		}

		replay := pack
		replay.LogicalClock = parent.Record.State.LogicalClock + 1
		written, warnings, err := r.writeReplayedRelic(ctx, parent, source, sourceParent, replay, step.Record)
		if err != nil {
			return fail(err)
		}
		if written.ObjectID == parent.ObjectID {
			// The pack changes neither the state nor any part's content.
			outcome.Skipped++
			continue
		}
		outcome.Replayed++
		outcome.Warnings = append(outcome.Warnings, warnings...)
		replayed = append(replayed, replay)
		parent = written
	}

	outcome.Next = parent.ObjectID
	outcome.Warnings = append(outcome.Warnings, core.RelicRemovedPartWarnings(relicID, replayed, remotePacks, parent.Record)...)
	outcome.Warnings = append(outcome.Warnings, core.RelicOverLimitWarnings(relicID, tip.Record, parent.Record)...)
	if len(outcome.Warnings) == 0 {
		outcome.Warnings = nil
	}
	return outcome
}

// relicUnappliedOperations lists, oldest first, the operations of the local
// commits a stopped replay did not apply: every one but named, the operation
// the conflict names, and those the fetched history already holds.
func relicUnappliedOperations(stopped []RelicCommit, named string, held map[string]core.RelicOperation) []core.RelicUnappliedOperation {
	var unapplied []core.RelicUnappliedOperation
	for _, commit := range stopped {
		for _, operation := range commit.Operation.Operations {
			if _, holds := held[operation.ID]; holds || operation.ID == named {
				continue
			}
			unapplied = append(unapplied, core.RelicUnappliedOperation{
				OperationID: operation.ID, Operation: operation.Type, Commit: commit.ObjectID,
			})
		}
	}
	return unapplied
}

// sameRelicBodies says, for each part the pack saves a body for, whether the
// body the local commit stores for it is the one parent stores: the same blob
// under the same stored format label.
func sameRelicBodies(parent, source RelicCommit, pack core.RelicOperationPack) map[string]bool {
	same := map[string]bool{}
	for _, operation := range pack.Operations {
		if operation.Type != core.RelicOperationPartBodySet {
			continue
		}
		saved, savedOK := relicBodyVersionAt(source, operation.Part)
		current, currentOK := relicBodyVersionAt(parent, operation.Part)
		same[operation.Part] = savedOK && currentOK && saved.Object == current.Object && saved.Format == current.Format
	}
	return same
}

// relicBodyVersionAt describes the body a commit stores for a part, without
// its content. ok is false when the part has no body there.
func relicBodyVersionAt(commit RelicCommit, partID string) (version core.RelicBodyVersion, ok bool) {
	part, listed := commit.Record.State.Relic.Part(partID)
	if !listed || part.Body == nil {
		return core.RelicBodyVersion{}, false
	}
	return core.RelicBodyVersion{
		Version: part.Body.Version,
		Format:  part.Body.Format,
		Bytes:   part.Body.Bytes,
		Object:  commit.Parts[partID].Body,
	}, true
}

// relicBodyConflictAt describes a body conflict's three versions: the shared
// commit's, which is nil when the part had no body there, the local tip's and
// the one that stands.
func relicBodyConflictAt(base, local, stands RelicCommit, partID string) *core.RelicBodyConflict {
	conflict := &core.RelicBodyConflict{}
	if version, ok := relicBodyVersionAt(base, partID); ok {
		conflict.Base = &version
	}
	conflict.Ours, _ = relicBodyVersionAt(local, partID)
	conflict.Theirs, _ = relicBodyVersionAt(stands, partID)
	return conflict
}

// writeReplayedRelic writes the commit that records pack on top of parent,
// with record as its state, and returns it with its parts. source is the local
// commit the pack was first recorded in and sourceParent that commit's parent;
// their blobs are reused wherever they are still right, so no body is read:
//
//   - A body the fold applied, and a new part's fields.json, are source's.
//   - An existing part's fields.json, when the pack sets or clears a field of
//     it, is source's when sourceParent and parent hold the same fields.json
//     for the part. Otherwise both sides changed the part's fields, and it is
//     parent's fields.json, read under the object ceiling, with the pack's
//     field operations applied, written in the stored form.
//   - Every other part keeps parent's subtree.
//
// Fields combined that way may pass their total. They are written all the
// same and returned with a warning, since two honest edits can pass it
// together. Fields a reader would refuse, a value over its own limit or a form
// no writer stores, are an error, and then nothing is written.
//
// A pack that changes neither the state, leaving the logical clock and the
// time of the last change aside, nor any part's subtree writes nothing, and
// parent itself is returned.
func (r *Repository) writeReplayedRelic(
	ctx context.Context,
	parent RelicCommit,
	source RelicCommit,
	sourceParent RelicCommit,
	pack core.RelicOperationPack,
	record core.RelicRecord,
) (RelicCommit, []core.Warning, error) {
	relicID := record.State.RelicID
	if err := core.ValidateRelicCheckpoint(&parent.Record, pack, record); err != nil {
		if core.CategoryOf(err) == core.CategoryNewerWriter {
			return RelicCommit{}, nil, err
		}
		return RelicCommit{}, nil, corruptRelic(relicID, "%s", err.Error())
	}

	named := make(map[string]struct{})
	for _, partID := range core.RelicPartsNamedBy(pack) {
		named[partID] = struct{}{}
	}
	fieldsEdited := relicPartsWithFieldOperations(pack, record.State.Relic)
	parts := make(map[string]RelicPartObjects, len(record.State.Relic.Parts))
	var combine []string
	for _, part := range record.State.Relic.Parts {
		before, existed := parent.Parts[part.ID]
		if _, isNamed := named[part.ID]; !isNamed {
			if !existed {
				return RelicCommit{}, nil, corruptRelic(relicID, "commit %s has no subtree for part %s", parent.ObjectID, part.ID)
			}
			parts[part.ID] = before
			continue
		}
		objects := before
		saved, inSource := source.Parts[part.ID]
		_, isEdited := fieldsEdited[part.ID]
		switch {
		case !existed:
			if !inSource {
				return RelicCommit{}, nil, corruptRelic(relicID,
					"commit %s does not hold part %s, which its pack adds", source.ObjectID, part.ID)
			}
			objects.Fields, objects.FieldsBytes = saved.Fields, saved.FieldsBytes
		case isEdited:
			if previous, ok := sourceParent.Parts[part.ID]; ok && inSource && previous.Fields == before.Fields {
				objects.Fields, objects.FieldsBytes = saved.Fields, saved.FieldsBytes
			} else {
				combine = append(combine, part.ID)
			}
		}

		objects.Body, objects.BodyBytes = "", 0
		if part.Body != nil {
			switch {
			case core.RelicBodySavedBy(&parent.Record, pack, record, part.ID):
				stored, listed := source.Record.State.Relic.Part(part.ID)
				if !inSource || !listed || stored.Body == nil || stored.Body.Version != part.Body.Version || saved.Body == "" {
					return RelicCommit{}, nil, corruptRelic(relicID,
						"commit %s does not hold the body of part %s that its pack saves", source.ObjectID, part.ID)
				}
				objects.Body, objects.BodyBytes = saved.Body, saved.BodyBytes
			case existed && before.Body != "":
				objects.Body, objects.BodyBytes = before.Body, before.BodyBytes
			default:
				return RelicCommit{}, nil, corruptRelic(relicID,
					"commit %s has no body for part %s, which the replayed relic records", parent.ObjectID, part.ID)
			}
		}
		parts[part.ID] = objects
	}

	// Fields both sides changed are combined, and checked as a reader would
	// check them, before anything is written.
	var warnings []core.Warning
	var fieldBlobs [][]byte
	var fieldParts []string
	if len(combine) > 0 {
		stored, err := r.readRelicFieldsBlobs(ctx, relicID, parent, combine)
		if err != nil {
			return RelicCommit{}, nil, err
		}
		for index, partID := range combine {
			previous, err := core.DecodeRelicFieldsDocument(stored[index])
			if err != nil {
				return RelicCommit{}, nil, corruptRelic(relicID, "part %s: its %s is invalid: %v", partID, relicFieldsPath, err)
			}
			encoded, err := encodeRelicPartFields(partID, core.ApplyRelicFields(previous.Fields, partID, pack.Operations))
			if err == nil {
				_, err = core.DecodeRelicFieldsDocument(encoded)
			}
			if err != nil {
				return RelicCommit{}, nil, corruptRelic(relicID,
					"the fields of part %s cannot be combined with the fields from the other side: %v", partID, err)
			}
			if bytes.Equal(encoded, stored[index]) {
				continue
			}
			// Any part this replay rewrites with fields over the total warns,
			// whether the replay crossed the total, grew a part already over
			// it, or shrank one that is still over it: the warning is about
			// the bytes this replay wrote, not about a change in size.
			if len(encoded) > core.MaxRelicPartFieldsBytes {
				warnings = append(warnings, core.RelicFieldsOverLimitWarning(relicID, partID, int64(len(encoded))))
			}
			fieldBlobs = append(fieldBlobs, encoded)
			fieldParts = append(fieldParts, partID)
		}
	}

	changedSubtree := len(fieldBlobs) > 0
	for partID := range named {
		objects, listed := parts[partID]
		before, existed := parent.Parts[partID]
		if listed && (!existed || objects.Fields != before.Fields || objects.Body != before.Body) {
			changedSubtree = true
		}
	}
	if !changedSubtree && !relicRecordChanged(parent.Record, record) {
		return parent, nil, nil
	}

	blobs := make([][]byte, 0, 3+len(fieldBlobs))
	for _, document := range []any{pack, record.State, record.Links} {
		encoded, err := core.EncodeDocument(document)
		if err != nil {
			return RelicCommit{}, nil, corruptRelic(relicID, "the replayed commit cannot be encoded: %v", err)
		}
		if len(encoded) > MaxObjectBytes {
			return RelicCommit{}, nil, corruptRelic(relicID,
				"the replayed commit's documents are %d bytes; the limit for one stored object is %d bytes", len(encoded), MaxObjectBytes)
		}
		blobs = append(blobs, encoded)
	}
	blobs = append(blobs, fieldBlobs...)
	blobIDs, err := r.hashRelicBlobs(ctx, blobs)
	if err != nil {
		return RelicCommit{}, nil, err
	}
	for index, partID := range fieldParts {
		objects := parts[partID]
		objects.Fields, objects.FieldsBytes = blobIDs[3+index], int64(len(fieldBlobs[index]))
		parts[partID] = objects
	}

	// A part gets a new subtree when one of its blobs changed.
	var subtrees [][]relicTreeEntry
	var subtreeParts []string
	for _, part := range record.State.Relic.Parts {
		objects := parts[part.ID]
		before, existed := parent.Parts[part.ID]
		if existed && objects.Fields == before.Fields && objects.Body == before.Body {
			continue
		}
		entries := []relicTreeEntry{{mode: regularBlobMode, kind: "blob", objectID: objects.Fields, name: relicFieldsPath}}
		if objects.Body != "" {
			entries = append(entries, relicTreeEntry{mode: regularBlobMode, kind: "blob", objectID: objects.Body, name: relicBodyPath})
		}
		subtrees = append(subtrees, entries)
		subtreeParts = append(subtreeParts, part.ID)
	}
	if len(subtrees) > 0 {
		treeIDs, err := r.writeRelicTrees(ctx, subtrees)
		if err != nil {
			return RelicCommit{}, nil, err
		}
		for index, partID := range subtreeParts {
			objects := parts[partID]
			objects.Tree = treeIDs[index]
			parts[partID] = objects
		}
	}

	top := []relicTreeEntry{
		{mode: regularBlobMode, kind: "blob", objectID: blobIDs[0], name: relicOperationPath},
		{mode: regularBlobMode, kind: "blob", objectID: blobIDs[1], name: relicStatePath},
		{mode: regularBlobMode, kind: "blob", objectID: blobIDs[2], name: relicLinksPath},
	}
	if len(record.State.Relic.Parts) > 0 {
		entries := make([]relicTreeEntry, 0, len(record.State.Relic.Parts))
		for _, part := range record.State.Relic.Parts {
			entries = append(entries, relicTreeEntry{mode: mktreeSubtreeMode, kind: "tree", objectID: parts[part.ID].Tree, name: part.ID})
		}
		partsTree, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{entries})
		if err != nil {
			return RelicCommit{}, nil, err
		}
		top = append(top, relicTreeEntry{mode: mktreeSubtreeMode, kind: "tree", objectID: partsTree[0], name: relicPartsPath})
	}
	tree, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{top})
	if err != nil {
		return RelicCommit{}, nil, err
	}

	written := RelicCommit{Parent: parent.ObjectID, Operation: pack, Record: record, Parts: parts}
	// The commit has to pass what a read of it checks; this costs no Git
	// process, and a commit that fails is never written.
	if err := r.VerifyRelicTransition(ctx, &parent, written, false); err != nil {
		return RelicCommit{}, nil, err
	}
	written.ObjectID, err = r.writeCommit(ctx, tree[0], parent.ObjectID, "replay relic "+relicID, pack.Actor.ID, source.ObjectID)
	if err != nil {
		return RelicCommit{}, nil, err
	}
	return written, warnings, nil
}

// relicRecordChanged reports whether next records any change from previous,
// in its state or its links, beyond the logical clock and the time of the
// last change, which every pack moves.
func relicRecordChanged(previous, next core.RelicRecord) bool {
	next.State.LogicalClock = previous.State.LogicalClock
	next.State.Relic.UpdatedAt = previous.State.Relic.UpdatedAt
	for _, documents := range [][2]any{{previous.State, next.State}, {previous.Links, next.Links}} {
		before, errBefore := core.EncodeDocument(documents[0])
		after, errAfter := core.EncodeDocument(documents[1])
		if errBefore != nil || errAfter != nil || !bytes.Equal(before, after) {
			return true
		}
	}
	return false
}

// readRelicFieldsBlobs reads the fields.json commit stores for each part,
// through one Git batch process, each under the ceiling for one stored object.
func (r *Repository) readRelicFieldsBlobs(ctx context.Context, relicID string, commit RelicCommit, partIDs []string) ([][]byte, error) {
	var input bytes.Buffer
	for _, partID := range partIDs {
		fmt.Fprintf(&input, "%s\n", commit.Parts[partID].Fields)
	}
	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return nil, err
	}
	defer batch.Close()
	contents := make([][]byte, len(partIDs))
	for index, partID := range partIDs {
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
		contents[index] = object.contents
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return contents, nil
}
