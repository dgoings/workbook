package gitstore

import (
	"bytes"
	"context"
	"fmt"

	"github.com/dgoings/workbook/internal/core"
)

// This file reads relics for the local index in batches whose number of Git
// processes does not grow with the number of relics: verified tips with their
// part objects and fields, first-parent operation chains, and body blobs by
// object ID. The index stores what these return and reads bodies back by the
// object IDs it stored, so body text never has to be copied anywhere.

// RelicIndexRequest asks for one relic tip to index. KnownFields holds
// fields.json object IDs the caller already has, which are not read again.
type RelicIndexRequest struct {
	Head        core.RelicHead
	KnownFields map[string]struct{}
}

// IndexedRelic is one verified relic tip as the index stores it: its snapshot,
// the objects of every part its state lists, and the fields.json bytes of the
// parts whose fields object the request did not already know, by part ID.
type IndexedRelic struct {
	Snapshot core.RelicSnapshot
	Parts    map[string]RelicPartObjects
	Fields   map[string][]byte
}

// ReadIndexedRelics reads and verifies each requested tip as GetRelic does,
// then reads every distinct fields.json the requests do not already know
// through one more Git batch process. No body is read.
//
// The results are in the order of requests, and one relic's failure fills
// only its own slot: a tip that fails its checks, or whose fields cannot be
// read, leaves a zero IndexedRelic and an error naming the relic. A tip
// written by a newer Workbook gets a CategoryNewerWriter error. Git is started
// at most six times however many relics there are.
func (r *Repository) ReadIndexedRelics(
	ctx context.Context,
	config core.ProjectConfig,
	requests []RelicIndexRequest,
) ([]IndexedRelic, []error) {
	relics := make([]IndexedRelic, len(requests))
	errs := make([]error, len(requests))
	if len(requests) == 0 {
		return relics, errs
	}
	failAll := func(err error) ([]IndexedRelic, []error) {
		for index, request := range requests {
			errs[index] = namedRelicError(request.Head.RelicID, err)
		}
		return relics, errs
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return failAll(err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failAll(err)
	}

	heads := make([]core.RelicHead, len(requests))
	for index, request := range requests {
		heads[index] = request.Head
	}
	commits, verifyErrs := r.verifyRelicTips(ctx, config, heads)

	// wanted is every fields object to read, once however many parts of
	// however many relics store it, with the parts that do.
	type fieldsUser struct {
		index  int
		partID string
	}
	wanted := map[string][]fieldsUser{}
	var order []string
	for index, commit := range commits {
		if verifyErrs[index] != nil {
			errs[index] = verifyErrs[index]
			continue
		}
		relics[index] = IndexedRelic{
			Snapshot: core.RelicSnapshot{Head: commit.ObjectID, Operation: commit.Operation, Record: commit.Record},
			Parts:    commit.Parts,
			Fields:   map[string][]byte{},
		}
		for _, part := range commit.Record.State.Relic.Parts {
			objects := commit.Parts[part.ID]
			if _, known := requests[index].KnownFields[objects.Fields]; known {
				continue
			}
			if _, seen := wanted[objects.Fields]; !seen {
				order = append(order, objects.Fields)
			}
			wanted[objects.Fields] = append(wanted[objects.Fields], fieldsUser{index: index, partID: part.ID})
		}
	}
	if len(order) == 0 {
		return relics, errs
	}

	// fail refuses one relic. The first refusal is the one it is reported
	// with.
	fail := func(index int, err error) {
		if errs[index] == nil {
			relics[index], errs[index] = IndexedRelic{}, err
		}
	}
	failUsers := func(objectID string, err func(user fieldsUser) error) {
		for _, user := range wanted[objectID] {
			fail(user.index, err(user))
		}
	}
	failRemaining := func(remaining []string, err error) {
		for _, objectID := range remaining {
			failUsers(objectID, func(user fieldsUser) error {
				return namedRelicError(requests[user.index].Head.RelicID, err)
			})
		}
	}

	var input bytes.Buffer
	for _, objectID := range order {
		fmt.Fprintln(&input, objectID)
	}
	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		failRemaining(order, err)
		return relics, errs
	}
	defer batch.Close()
	for position, objectID := range order {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			failRemaining(order[position:], batch.ReadFailure("cannot read relic fields from Git batch", err))
			return relics, errs
		}
		refuse := func(format string, args ...any) {
			failUsers(objectID, func(user fieldsUser) error {
				return corruptRelic(requests[user.index].Head.RelicID, "part %s: %s", user.partID, fmt.Sprintf(format, args...))
			})
		}
		switch {
		case object.missing:
			refuse("its %s is missing", relicFieldsPath)
			continue
		case object.kind != "blob":
			refuse("its %s is not a blob", relicFieldsPath)
			continue
		case object.size > MaxObjectBytes:
			refuse("its %s is %d bytes; the limit is %d bytes", relicFieldsPath, object.size, int64(MaxObjectBytes))
			continue
		case object.refused != nil:
			failUsers(objectID, func(user fieldsUser) error {
				return namedRelicError(requests[user.index].Head.RelicID, object.refused)
			})
			continue
		}
		fields, err := core.DecodeRelicFieldsDocument(object.contents)
		if err != nil {
			refuse("its %s is invalid: %v", relicFieldsPath, err)
			continue
		}
		if err := requireCanonicalRelicDocument(relicFieldsPath, object.contents, fields); err != nil {
			refuse("its %s is not canonical", relicFieldsPath)
			continue
		}
		for _, user := range wanted[objectID] {
			if errs[user.index] == nil {
				relics[user.index].Fields[user.partID] = object.contents
			}
		}
	}
	if err := batch.Finish(); err != nil {
		// Git and this reader disagree about the stream, so nothing read
		// from it can be trusted.
		failRemaining(order, err)
	}
	return relics, errs
}

// RelicHistoryRequest asks for one relic's operation chain from Head down to,
// and not including, StopAt. An empty StopAt reads the whole chain.
type RelicHistoryRequest struct {
	Head   core.RelicHead
	StopAt string
}

// RelicOperationCommit is one relic commit's operation pack with its address
// in the chain. Parent is empty for a root.
type RelicOperationCommit struct {
	ObjectID, Parent string
	Operation        core.RelicOperationPack
}

// RelicOperationsResult is one relic's operation chain, oldest first.
//
// BoundaryReached reports whether the walk stopped at the requested StopAt. It
// is false when a StopAt was named and the head does not descend from it along
// first parents, and the chain then runs to the root. Truncated names the
// commit whose pack could not be read, with the prefix below it returned;
// its TaskID holds the relic ID.
type RelicOperationsResult struct {
	RelicID, Head   string
	BoundaryReached bool
	Commits         []RelicOperationCommit
	Truncated       *HistoryFailure
}

// ReadRelicOperations reads the requested operation chains through one shared
// parent-graph walk and one batch of operation blobs, so Git is started twice
// however many relics there are.
//
// Packs are decoded and not verified, as ReadTaskOperations does for tasks:
// the tips are verified by whoever asks, and the write path and fetch check
// what arrives. `workbook validate` checks whole histories.
func (r *Repository) ReadRelicOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []RelicHistoryRequest,
) ([]RelicOperationsResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	results := make([]RelicOperationsResult, len(requests))
	if len(requests) == 0 {
		return results, nil
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(requests))
	var input bytes.Buffer
	for index, request := range requests {
		relicID := request.Head.RelicID
		if err := core.ValidateRelicIDShape(relicID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "relic history request ID is invalid", err)
		}
		if _, duplicate := seen[relicID]; duplicate {
			return nil, core.Errorf(core.CategoryCorruptData, "relic history requests name relic %s twice", relicID)
		}
		seen[relicID] = struct{}{}
		if err := r.validateFullObjectID(request.Head.ObjectID); err != nil {
			return nil, namedRelicError(relicID, core.Wrap(core.CategoryCorruptData, "relic head object ID is invalid", err))
		}
		if request.StopAt != "" {
			if err := r.validateFullObjectID(request.StopAt); err != nil {
				return nil, namedRelicError(relicID, core.Wrap(core.CategoryCorruptData, "relic history boundary is invalid", err))
			}
		}
		results[index] = RelicOperationsResult{RelicID: relicID, Head: request.Head.ObjectID}
		fmt.Fprintln(&input, request.Head.ObjectID)
	}

	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot read relic history parent graph", err)
	}
	graph, err := parseParentGraph(output)
	if err != nil {
		return nil, err
	}
	for objectID, parents := range graph {
		for _, id := range append([]string{objectID}, parents...) {
			if err := r.validateFullObjectID(id); err != nil {
				return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid history graph object ID", err)
			}
		}
	}
	if err := validateCompleteParentGraph(graph); err != nil {
		return nil, err
	}

	// chains holds each request's commits oldest first, with their first
	// parents.
	type link struct{ objectID, parent string }
	chains := make([][]link, len(requests))
	var packs bytes.Buffer
	for index, request := range requests {
		var newestFirst []link
		walked := map[string]struct{}{}
		for current := request.Head.ObjectID; ; {
			if current == request.StopAt {
				results[index].BoundaryReached = true
				break
			}
			parents, found := graph[current]
			if !found {
				return nil, corruptRelic(request.Head.RelicID, "Git's history graph omits commit %s", current)
			}
			if _, again := walked[current]; again {
				return nil, corruptRelic(request.Head.RelicID, "its history returns to commit %s", current)
			}
			walked[current] = struct{}{}
			parent := ""
			if len(parents) > 0 {
				parent = parents[0]
			}
			newestFirst = append(newestFirst, link{objectID: current, parent: parent})
			if parent == "" {
				break
			}
			current = parent
		}
		chain := make([]link, len(newestFirst))
		for offset, commit := range newestFirst {
			chain[len(newestFirst)-1-offset] = commit
		}
		chains[index] = chain
		for _, commit := range chain {
			fmt.Fprintf(&packs, "%s:%s\n", commit.objectID, relicOperationPath)
		}
	}
	if packs.Len() == 0 {
		return results, nil
	}

	batch, err := r.startRelicObjectBatch(ctx, packs.Bytes())
	if err != nil {
		return nil, err
	}
	defer batch.Close()
	for index, request := range requests {
		relicID := request.Head.RelicID
		result := &results[index]
		for _, commit := range chains[index] {
			// Every record is read, even past a truncation, so the stream
			// stays in step for the next relic.
			object, err := readBatchObject(batch.Reader())
			if err != nil {
				return nil, namedRelicError(relicID, batch.ReadFailure("cannot read relic operations from Git batch", err))
			}
			if result.Truncated != nil {
				continue
			}
			pack, err := relicOperationPackOf(object)
			if err == nil && (pack.RelicID != relicID || pack.ProjectID != config.ProjectID) {
				err = core.Errorf(core.CategoryCorruptData, "commit %s records an operation for a different relic or project", commit.objectID)
			}
			if err != nil {
				result.Truncated = &HistoryFailure{TaskID: relicID, Commit: commit.objectID, Err: namedRelicError(relicID, err)}
				continue
			}
			result.Commits = append(result.Commits, RelicOperationCommit{
				ObjectID: commit.objectID, Parent: commit.parent, Operation: pack,
			})
		}
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return results, nil
}

// relicOperationPackOf decodes one operation.json read from a batch.
func relicOperationPackOf(object batchObject) (core.RelicOperationPack, error) {
	switch {
	case object.refused != nil:
		return core.RelicOperationPack{}, object.refused
	case object.missing:
		return core.RelicOperationPack{}, core.Errorf(core.CategoryCorruptData, "relic commit has no %s", relicOperationPath)
	case object.kind != "blob":
		return core.RelicOperationPack{}, core.Errorf(core.CategoryCorruptData, "relic %s is not a blob", relicOperationPath)
	}
	pack, err := core.DecodeRelicOperationPack(object.contents)
	if err != nil {
		if core.CategoryOf(err) == "" {
			err = core.Wrap(core.CategoryCorruptData, "relic "+relicOperationPath+" is invalid", err)
		}
		return core.RelicOperationPack{}, err
	}
	return pack, nil
}

// RelicBodyRequest asks for one part's body by the object ID its commit
// stores it under. Bytes is the size the relic's state records.
type RelicBodyRequest struct {
	RelicID, PartID, ObjectID string
	Bytes                     int64
}

// ReadRelicBodies reads the requested bodies, in order, through one Git batch
// process of bare object IDs. A body over the limit for one body is refused
// from the size in Git's header, before anything is allocated for it. A
// missing object, an object that is not a blob, and a blob of a size other
// than the one requested are each corrupt data naming the relic and the part,
// in the words ReadRelicParts uses.
func (r *Repository) ReadRelicBodies(
	ctx context.Context,
	config core.ProjectConfig,
	requests []RelicBodyRequest,
) ([][]byte, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	bodies := make([][]byte, len(requests))
	if len(requests) == 0 {
		return bodies, nil
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, err
	}
	var input bytes.Buffer
	for _, request := range requests {
		// The ID goes into Git's request stream on its own, so it is held to
		// a full object ID and nothing Git would read as a revision.
		if err := r.validateFullObjectID(request.ObjectID); err != nil {
			return nil, corruptRelic(request.RelicID, "part %s: its body object ID is invalid: %v", request.PartID, err)
		}
		fmt.Fprintln(&input, request.ObjectID)
	}

	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		return nil, err
	}
	defer batch.Close()
	for index, request := range requests {
		refuse := func(format string, args ...any) error {
			return corruptRelic(request.RelicID, "part %s: %s", request.PartID, fmt.Sprintf(format, args...))
		}
		object, err := readBatchObjectWithin(batch.Reader(), core.MaxRelicBodyBytes)
		if err != nil {
			return nil, namedRelicError(request.RelicID, batch.ReadFailure("cannot read relic bodies from Git batch", err))
		}
		switch {
		case object.missing:
			return nil, refuse("its %s is missing", relicBodyPath)
		case object.kind != "blob":
			return nil, refuse("its %s is not a blob", relicBodyPath)
		case object.size > core.MaxRelicBodyBytes:
			return nil, refuse("its %s is %d bytes; the limit is %d bytes", relicBodyPath, object.size, int64(core.MaxRelicBodyBytes))
		case object.refused != nil:
			return nil, namedRelicError(request.RelicID, object.refused)
		case object.size != request.Bytes:
			return nil, refuse("its body is %d bytes, but the relic records %d bytes", object.size, request.Bytes)
		}
		bodies[index] = object.contents
		if bodies[index] == nil {
			bodies[index] = []byte{}
		}
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return bodies, nil
}
