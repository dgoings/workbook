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
//
// Four flags are set only by reconcileCompactedRelics, for two tips in
// different history generations. Adopted is a move onto a checkpoint origin
// holds (the relic's reflog is expired once the ref moves). Dropped is this
// clone's unpublished checkpoint left behind for origin's history, with the
// warning. Unrelated is two histories neither accounts for, set beside Err.
// AwaitsPublication is a local checkpoint that accounts for everything origin
// holds: nothing moves, and the push publishes it with a lease.
type relicReconcileOutcome struct {
	RelicID, Next, ParkedHead string
	Replayed, Skipped         int
	Conflict                  *core.RelicConflict
	Warnings                  []core.Warning
	Err                       error

	Adopted, Dropped, Unrelated, AwaitsPublication bool
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
		remotePacks, err := r.relicChainPacks(ctx, config, request.RelicID, divergence.remote)
		if err != nil {
			outcomes[index].Err = namedRelicError(request.RelicID, err)
			continue
		}
		outcomes[index] = r.replayRelic(ctx, request, commits[divergence.at:end], remotePacks, nil)
	}
	return outcomes
}

// replayRelic replays one relic. read is the commit the local commits build
// on, then the local commits oldest first, then the fetched tip; remotePacks
// are the packs of origin's own commits, which origin holds with their
// content. listed is operation IDs origin holds without their content, those
// a checkpoint on either side lists as replaced: a local operation with one
// of those IDs counts as held, since an operation ID is used once.
//
// Each pack is written in the fetched tip's history generation. Within one
// generation that changes nothing; across a checkpoint it is what puts a
// local change from the old history onto the new one.
func (r *Repository) replayRelic(
	ctx context.Context,
	request relicReconcileRequest,
	read []RelicCommit,
	remotePacks []core.RelicOperationPack,
	listed []string,
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

	held := map[string]core.RelicOperation{}
	for _, pack := range remotePacks {
		for _, operation := range pack.Operations {
			held[operation.ID] = operation
		}
	}
	if len(listed) > 0 {
		replaced := make(map[string]struct{}, len(listed))
		for _, id := range listed {
			replaced[id] = struct{}{}
		}
		for _, local := range locals {
			for _, operation := range local.Operation.Operations {
				if _, isListed := replaced[operation.ID]; isListed {
					if _, holds := held[operation.ID]; !holds {
						held[operation.ID] = operation
					}
				}
			}
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
		pack.HistoryGeneration = parent.Record.State.History.Generation
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

// Two tips of one relic in different history generations meet when either
// side compacted it, which is a parentless history.checkpoint that opens a
// new generation:
//
//	origin   K' ── e1              this clone   C1 ── C2 ── C3 ── l1
//	         (checkpoint of C3)                 (the old generation)
//
// They share no commit, so replay cannot find a base by ancestry. What ties
// them is the checkpoint's list of the operations it replaced: an operation
// ID is used once, so a commit whose operations a checkpoint lists, or that
// the other side's chain holds, is a change both sides have.
// reconcileCompactedRelics settles each such pair by the first rule that
// applies:
//
//  1. This clone's tip is rooted at a checkpoint that accounts for every
//     operation on origin's chain (relicSides.localAccountsForRemote): it
//     waits for its leased push, and nothing moves.
//  2. Origin's tip is rooted at a checkpoint (adoption). Local commits that
//     origin's list or chain holds are dropped; the rest, from the first one
//     origin lacks, are replayed onto origin's tip. With none left, the ref
//     moves to origin's tip.
//  3. This clone's tip is rooted at a checkpoint origin never received, and
//     origin's tip descends from the tip it replaced (compactedFrom): origin
//     moved on with changes the checkpoint lacks. The local commits above
//     the checkpoint are replayed onto origin's tip and the checkpoint is left
//     unreachable, with the relic-compaction-dropped warning. Origin's chain
//     reaches compactedFrom, so it holds every operation the checkpoint
//     listed, and nothing is lost.
//  4. Otherwise neither history accounts for the other, and the relic is
//     withheld as unrelated.
//
// A replay that stops parks the old local tip as any relic replay does.
// Wall-clock time decides nothing here: ancestry, generations and operation
// IDs do. Nothing moves a ref; each outcome is for the caller to apply.

// relicSides is both chains of a pair in different generations, oldest first,
// with their packs.
type relicSides struct {
	localChain, remoteChain []string
	localPacks, remotePacks []core.RelicOperationPack
}

// readRelicSides walks both chains of request in graph and reads their packs.
func (r *Repository) readRelicSides(
	ctx context.Context,
	config core.ProjectConfig,
	graph map[string][]string,
	request relicReconcileRequest,
) (relicSides, error) {
	var sides relicSides
	localChain, err := commitChain(graph, request.Local)
	if err != nil {
		return sides, corruptRelic(request.RelicID, "cannot walk the local history: %v", err)
	}
	remoteChain, err := commitChain(graph, request.Remote)
	if err != nil {
		return sides, corruptRelic(request.RelicID, "cannot walk the fetched history: %v", err)
	}
	sides.localChain, sides.remoteChain = oldestFirst(localChain), oldestFirst(remoteChain)
	if sides.localPacks, err = r.relicChainPacks(ctx, config, request.RelicID, sides.localChain); err != nil {
		return sides, err
	}
	if sides.remotePacks, err = r.relicChainPacks(ctx, config, request.RelicID, sides.remoteChain); err != nil {
		return sides, err
	}
	return sides, nil
}

// rootCheckpoint is the summary of the checkpoint a chain's packs are rooted
// at, if they are.
func rootCheckpoint(packs []core.RelicOperationPack) (core.RelicCheckpointData, bool) {
	if len(packs) == 0 {
		return core.RelicCheckpointData{}, false
	}
	return packs[0].Checkpoint()
}

// relicOperationSet is every operation ID of packs, with extra.
func relicOperationSet(packs []core.RelicOperationPack, extra []string) map[string]struct{} {
	set := make(map[string]struct{}, len(extra))
	for _, id := range extra {
		set[id] = struct{}{}
	}
	for _, pack := range packs {
		for _, operation := range pack.Operations {
			set[operation.ID] = struct{}{}
		}
	}
	return set
}

// localAccountsForRemote reports whether the local chain is rooted at a
// checkpoint and every operation on origin's chain is in that checkpoint's
// list or on the local chain. That is the condition for publishing the local
// tip over origin's with a lease (Decision 20), and for a fetch to leave it
// waiting for that push.
func (sides relicSides) localAccountsForRemote() bool {
	summary, ok := rootCheckpoint(sides.localPacks)
	if !ok {
		return false
	}
	accounted := relicOperationSet(sides.localPacks, summary.CompactedOperations)
	for _, pack := range sides.remotePacks {
		for _, operation := range pack.Operations {
			if _, found := accounted[operation.ID]; !found {
				return false
			}
		}
	}
	return true
}

// relicCannotCombine is the refusal for two histories of a relic neither of
// which accounts for the other.
func relicCannotCombine(relicID, remote string) error {
	return core.Errorf(core.CategoryCorruptData,
		"relic %s: cannot combine this clone's history of it with %s's: they share no commit, and no checkpoint on "+
			"either side accounts for the other's changes; it was left where it is and not published", relicID, remote)
}

// relicCompactionDroppedWarning says that this clone's unpublished checkpoint
// gave way to origin's history.
func relicCompactionDroppedWarning(relicID, remote string) core.Warning {
	return core.Warning{
		Code: WarningRelicCompactionDropped,
		Message: fmt.Sprintf("relic %s: this clone compacted it, but %s changed it before the checkpoint was published, "+
			"so the compaction was dropped and %s's history kept, with every change the checkpoint replaced and "+
			"this clone's later changes replayed onto it; run `workbook compact --relics` to compact it again",
			relicID, remote, remote),
	}
}

// reconcileCompactedRelics settles each pair, whose tips are in different
// history generations, as the comment above says, and returns one outcome per
// request in the same order. graph is classifyRelicHeads's. One relic's
// failure never fails another's.
func (r *Repository) reconcileCompactedRelics(
	ctx context.Context,
	config core.ProjectConfig,
	requests []relicReconcileRequest,
	graph map[string][]string,
) []relicReconcileOutcome {
	outcomes := make([]relicReconcileOutcome, len(requests))
	for index, request := range requests {
		outcomes[index] = r.reconcileCompactedRelic(ctx, config, request, graph)
	}
	return outcomes
}

func (r *Repository) reconcileCompactedRelic(
	ctx context.Context,
	config core.ProjectConfig,
	request relicReconcileRequest,
	graph map[string][]string,
) relicReconcileOutcome {
	relicID := request.RelicID
	fail := func(err error) relicReconcileOutcome {
		return relicReconcileOutcome{RelicID: relicID, Err: namedRelicError(relicID, err)}
	}
	unrelated := func() relicReconcileOutcome {
		return relicReconcileOutcome{RelicID: relicID, Err: relicCannotCombine(relicID, r.remote()), Unrelated: true}
	}
	sides, err := r.readRelicSides(ctx, config, graph, request)
	if err != nil {
		return fail(err)
	}
	local, localRooted := rootCheckpoint(sides.localPacks)
	remote, remoteRooted := rootCheckpoint(sides.remotePacks)

	switch {
	case sides.localAccountsForRemote():
		return relicReconcileOutcome{RelicID: relicID, AwaitsPublication: true}

	case remoteRooted:
		// Adoption: what origin holds is its checkpoint's list and its chain.
		held := relicOperationSet(sides.remotePacks, remote.CompactedOperations)
		first := -1
		for index, pack := range sides.localPacks {
			if summary, ok := pack.Checkpoint(); ok && index == 0 {
				// This clone's own checkpoint is no change of anyone's; what
				// it stands for is the operations it lists.
				for _, id := range summary.CompactedOperations {
					if _, found := held[id]; !found {
						return unrelated()
					}
				}
				continue
			}
			if !relicPackHeldBy(pack, held) {
				first = index
				break
			}
		}
		if first < 0 {
			return relicReconcileOutcome{RelicID: relicID, Next: request.Remote, Adopted: true}
		}
		if first == 0 {
			return unrelated()
		}
		outcome := r.replayAcrossGenerations(ctx, config, request,
			sides.localChain[first-1:], sides.remotePacks, remote.CompactedOperations)
		outcome.Adopted = outcome.Err == nil
		return outcome

	case localRooted && graphReaches(graph, request.Remote, local.CompactedFrom):
		// Origin moved on from the tip this clone compacted. Its own commits
		// since then are what it holds with content; the list is the rest.
		above := sides.remotePacks
		for index, commit := range sides.remoteChain {
			if commit == local.CompactedFrom {
				above = sides.remotePacks[index+1:]
				break
			}
		}
		var outcome relicReconcileOutcome
		if len(sides.localChain) == 1 {
			outcome = relicReconcileOutcome{RelicID: relicID, Next: request.Remote}
		} else {
			outcome = r.replayAcrossGenerations(ctx, config, request, sides.localChain, above, local.CompactedOperations)
		}
		if outcome.Err == nil {
			outcome.Dropped = true
			outcome.Warnings = append([]core.Warning{relicCompactionDroppedWarning(relicID, r.remote())}, outcome.Warnings...)
		}
		return outcome

	default:
		return unrelated()
	}
}

// relicPackHeldBy reports whether held names every operation of pack.
func relicPackHeldBy(pack core.RelicOperationPack, held map[string]struct{}) bool {
	for _, operation := range pack.Operations {
		if _, found := held[operation.ID]; !found {
			return false
		}
	}
	return true
}

// replayAcrossGenerations replays chain[1:] onto request.Remote, with chain[0]
// as the commit they build on. remotePacks are what origin holds with
// content and listed what it holds by ID alone (replayRelic).
func (r *Repository) replayAcrossGenerations(
	ctx context.Context,
	config core.ProjectConfig,
	request relicReconcileRequest,
	chain []string,
	remotePacks []core.RelicOperationPack,
	listed []string,
) relicReconcileOutcome {
	heads := make([]core.RelicHead, 0, len(chain)+1)
	for _, commit := range append(append([]string(nil), chain...), request.Remote) {
		heads = append(heads, core.RelicHead{RelicID: request.RelicID, ObjectID: commit})
	}
	commits, errs := r.readRelicCommitsPartial(ctx, config, heads)
	for _, err := range errs {
		if err != nil {
			return relicReconcileOutcome{RelicID: request.RelicID, Err: namedRelicError(request.RelicID, err)}
		}
	}
	return r.replayRelic(ctx, request, commits, remotePacks, listed)
}
