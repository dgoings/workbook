package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// relicPartsRequest asks for the objects of the parts one commit stores. State
// is that commit's own state, which says which parts those are.
type relicPartsRequest struct {
	Commit string
	State  core.RelicStateDocument
}

// relicPartsRead is one relicPartsRequest on its way through the three passes
// of readRelicPartObjectsBatch.
type relicPartsRead struct {
	relicPartsRequest
	// index is the request's place among the requests.
	index         int
	objectIDBytes int
	// subtrees maps every part the state lists to its subtree, once the parts
	// tree has been read.
	subtrees map[string]string
	// parts is what the request is answered with.
	parts map[string]RelicPartObjects
	// err is why this request failed. A failed request takes no further part.
	err error
}

// relicPartSubtree is one part's subtree as Git returned it. It is read once
// however many parts and commits name it, and judged for each part that does,
// because what it may hold depends on what the state says of that part.
type relicPartSubtree struct {
	// unreadable is set for an object Git does not have or that is not a tree.
	unreadable bool
	// size is the tree's size, and tooLarge that it is over the size of the
	// largest subtree a part may have, so that it was not read.
	size     int64
	tooLarge bool
	// malformed is why the tree could not be walked.
	malformed error
	// fields and body are the blobs its fields.json and body entries name.
	fields, body string
	// irregular names an entry called fields.json or body that is not one
	// regular blob, and stranger is set when an entry has any other name.
	irregular string
	stranger  bool
}

// relicBlobInfo is what Git reports of one blob without opening it.
type relicBlobInfo struct {
	missing bool
	kind    string
	size    int64
}

// relicPartsTreeBytes is the size of the parts tree of a commit with this
// state: one subtree entry per part the state lists, and nothing else.
func relicPartsTreeBytes(state core.RelicStateDocument, objectIDBytes int) int64 {
	var size int64
	for _, part := range state.Relic.Parts {
		size += rawTreeEntryBytes(subtreeMode, part.ID, objectIDBytes)
	}
	return size
}

// relicPartSubtreeBytes is the size of the largest subtree a part may have,
// one that holds fields.json and a body. The only other size is that of one
// that holds fields.json alone.
func relicPartSubtreeBytes(objectIDBytes int) int64 {
	return rawTreeEntryBytes(regularBlobMode, relicFieldsPath, objectIDBytes) +
		rawTreeEntryBytes(regularBlobMode, relicBodyPath, objectIDBytes)
}

// relicDirectoryMissing is the refusal for a commit that names a tree Git does
// not have, or names as a tree an object that is not one: the directory of all
// its parts, or one part's. That is the relic's damage, and it is said in
// words of our own.
func relicDirectoryMissing(relicID, commit string) error {
	return corruptRelic(relicID, "commit %s: a directory it names is missing from Git or cannot be read", commit)
}

// readRelicPartObjectsBatch returns, for each request, the objects of every
// part its state lists, checked against rules R3 and R4. The results are in
// the order of requests, and a request that fails does so alone, with an error
// that names its relic.
//
// Whoever can push to the tasks remote wrote these trees, so nothing is read
// that the state has not already sized. Git is started three times, however
// many requests there are and however many parts they have:
//
//  1. `cat-file --batch` for each commit's parts tree. Its size is known to
//     the byte from the parts the state lists, and a tree of any other size is
//     refused. A larger one is refused from Git's header, before it is read.
//  2. `cat-file --batch` for each part's subtree. A subtree holds fields.json
//     and at most a body, so one larger than those two entries is refused
//     from Git's header in the same way.
//  3. `cat-file --batch-check` for each blob those subtrees name. Git answers
//     with one line giving its type and size. No blob is opened.
//
// A subtree or a blob that several parts or commits name is asked for once:
// a commit and its parent share every part the commit did not change. A pass
// with nothing to ask starts no process.
func (r *Repository) readRelicPartObjectsBatch(
	ctx context.Context,
	requests []relicPartsRequest,
) ([]map[string]RelicPartObjects, []error) {
	results := make([]map[string]RelicPartObjects, len(requests))
	errs := make([]error, len(requests))
	reads := make([]*relicPartsRead, 0, len(requests))
	for index, request := range requests {
		if len(request.State.Relic.Parts) == 0 {
			// Rule R2 has already established that the commit has no parts tree.
			results[index] = map[string]RelicPartObjects{}
			continue
		}
		read := &relicPartsRead{relicPartsRequest: request, index: index}
		if decoded, err := decodeObjectID(request.Commit); err != nil {
			read.err = core.Wrap(core.CategoryValidation, "relic commit object ID is invalid", err)
		} else {
			read.objectIDBytes = len(decoded)
		}
		reads = append(reads, read)
	}

	r.readRelicPartsTrees(ctx, liveRelicPartsReads(reads))
	r.readRelicPartSubtrees(ctx, liveRelicPartsReads(reads))
	r.measureRelicPartBlobs(ctx, liveRelicPartsReads(reads))
	for _, read := range reads {
		if read.err != nil {
			errs[read.index] = namedRelicError(read.State.RelicID, read.err)
			continue
		}
		results[read.index] = read.parts
	}
	return results, errs
}

// liveRelicPartsReads returns the reads that have not failed.
func liveRelicPartsReads(reads []*relicPartsRead) []*relicPartsRead {
	live := make([]*relicPartsRead, 0, len(reads))
	for _, read := range reads {
		if read.err == nil {
			live = append(live, read)
		}
	}
	return live
}

func failRelicPartsReads(reads []*relicPartsRead, err error) {
	for _, read := range reads {
		read.err = err
	}
}

// readRelicPartsTrees is the first pass of readRelicPartObjectsBatch: it reads
// each commit's parts tree and finds the subtree of every part its state
// lists.
func (r *Repository) readRelicPartsTrees(ctx context.Context, reads []*relicPartsRead) {
	if len(reads) == 0 {
		return
	}
	var input bytes.Buffer
	for _, read := range reads {
		fmt.Fprintf(&input, "%s:%s\n", read.Commit, relicPartsPath)
	}
	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		failRelicPartsReads(reads, err)
		return
	}
	defer batch.Close()
	for position, read := range reads {
		expected := relicPartsTreeBytes(read.State, read.objectIDBytes)
		object, err := readBatchObjectWithin(batch.Reader(), expected)
		if err != nil {
			failRelicPartsReads(reads[position:], batch.ReadFailure("cannot read relic parts trees from Git batch", err))
			return
		}
		read.subtrees, read.err = relicPartSubtreesIn(read, object, expected)
	}
	if err := batch.Finish(); err != nil {
		failRelicPartsReads(reads, err)
	}
}

// relicPartSubtreesIn judges one commit's parts tree by rule R3 and returns
// the subtree of each part the state lists. expected is the size the state
// gives the tree.
func relicPartSubtreesIn(read *relicPartsRead, object batchObject, expected int64) (map[string]string, error) {
	relicID := read.State.RelicID
	wrongSize := corruptRelic(relicID,
		"the commit's %s directory is %d bytes; one that holds only the parts the relic lists is %d bytes",
		relicPartsPath, object.size, expected)
	switch {
	case object.missing || object.kind != "tree":
		return nil, relicDirectoryMissing(relicID, read.Commit)
	case object.size > expected:
		// It was not read, so which entries it has too many of is not known.
		return nil, wrongSize
	case object.refused != nil:
		return nil, object.refused
	}

	listed := make(map[string]struct{}, len(read.State.Relic.Parts))
	for _, part := range read.State.Relic.Parts {
		listed[part.ID] = struct{}{}
	}
	subtrees := make(map[string]string, len(listed))
	err := scanRawTreeEntries(object.contents, read.objectIDBytes, "relic parts tree", func(mode, name, objectID string) error {
		if _, isListed := listed[name]; !isListed {
			// The name is whatever the tree holds, so it is repeated only when
			// it has the shape of a part ID.
			if core.ValidatePartID(name) == nil {
				return corruptRelic(relicID, "the commit stores part %s, which the relic does not list", name)
			}
			return corruptRelic(relicID, "the commit stores an entry under %s that is not a part the relic lists", relicPartsPath)
		}
		if _, repeated := subtrees[name]; repeated || mode != subtreeMode {
			return corruptRelic(relicID, "part %s: its entry is not one subtree", name)
		}
		subtrees[name] = objectID
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The state's own order, so that the same commit always names the same
	// part first.
	for _, part := range read.State.Relic.Parts {
		if subtrees[part.ID] == "" {
			return nil, corruptRelic(relicID, "the commit stores no subtree for part %s", part.ID)
		}
	}
	if object.size != expected {
		return nil, wrongSize
	}
	return subtrees, nil
}

// readRelicPartSubtrees is the second pass of readRelicPartObjectsBatch: it
// reads each distinct part subtree once and judges it by rule R3 for every
// part that names it.
func (r *Repository) readRelicPartSubtrees(ctx context.Context, reads []*relicPartsRead) {
	if len(reads) == 0 {
		return
	}
	subtrees := map[string]*relicPartSubtree{}
	var order []string
	var input bytes.Buffer
	for _, read := range reads {
		for _, part := range read.State.Relic.Parts {
			objectID := read.subtrees[part.ID]
			if _, asked := subtrees[objectID]; asked {
				continue
			}
			subtrees[objectID] = &relicPartSubtree{}
			order = append(order, objectID)
			fmt.Fprintf(&input, "%s\n", objectID)
		}
	}
	batch, err := r.startRelicObjectBatch(ctx, input.Bytes())
	if err != nil {
		failRelicPartsReads(reads, err)
		return
	}
	defer batch.Close()
	for _, objectID := range order {
		// The ID was read out of a tree, so its width is the repository's.
		objectIDBytes := len(objectID) / 2
		limit := relicPartSubtreeBytes(objectIDBytes)
		object, err := readBatchObjectWithin(batch.Reader(), limit)
		if err != nil {
			failRelicPartsReads(reads, batch.ReadFailure("cannot read relic part trees from Git batch", err))
			return
		}
		*subtrees[objectID] = parseRelicPartSubtree(object, limit, objectIDBytes)
	}
	if err := batch.Finish(); err != nil {
		failRelicPartsReads(reads, err)
		return
	}

	for _, read := range reads {
		read.parts = make(map[string]RelicPartObjects, len(read.State.Relic.Parts))
		for _, part := range read.State.Relic.Parts {
			objectID := read.subtrees[part.ID]
			subtree := subtrees[objectID]
			if read.err = subtree.judge(read.State.RelicID, read.Commit, part, relicPartSubtreeBytes(len(objectID)/2)); read.err != nil {
				break
			}
			read.parts[part.ID] = RelicPartObjects{Tree: objectID, Fields: subtree.fields, Body: subtree.body}
		}
	}
}

// parseRelicPartSubtree reads what one part's subtree holds, leaving the
// judgment to judge.
func parseRelicPartSubtree(object batchObject, limit int64, objectIDBytes int) relicPartSubtree {
	subtree := relicPartSubtree{size: object.size}
	switch {
	case object.missing || object.kind != "tree":
		subtree.unreadable = true
		return subtree
	case object.size > limit:
		subtree.tooLarge = true
		return subtree
	case object.refused != nil:
		subtree.malformed = object.refused
		return subtree
	}
	seen := map[string]struct{}{}
	subtree.malformed = scanRawTreeEntries(object.contents, objectIDBytes, "relic part tree", func(mode, name, objectID string) error {
		if name != relicFieldsPath && name != relicBodyPath {
			subtree.stranger = true
			return nil
		}
		if _, repeated := seen[name]; (repeated || mode != regularBlobMode) && subtree.irregular == "" {
			subtree.irregular = name
		}
		seen[name] = struct{}{}
		if name == relicFieldsPath {
			subtree.fields = objectID
		} else {
			subtree.body = objectID
		}
		return nil
	})
	return subtree
}

// judge holds a subtree to rule R3 for one part that names it: fields.json,
// body exactly when the state records a body for the part, and nothing else.
// A subtree that passes holds one entry or two, each a regular blob, so its
// size is one of the two a part's subtree can have.
func (subtree *relicPartSubtree) judge(relicID, commit string, part core.RelicPart, limit int64) error {
	switch {
	case subtree.unreadable:
		return relicDirectoryMissing(relicID, commit)
	case subtree.tooLarge:
		// It was not read, so what else it holds is not known.
		return corruptRelic(relicID, "part %s: its subtree is %d bytes, so it holds more than %s and %s, which take %d bytes",
			part.ID, subtree.size, relicFieldsPath, relicBodyPath, limit)
	case subtree.malformed != nil:
		return subtree.malformed
	case subtree.irregular != "":
		return corruptRelic(relicID, "part %s: its %s is not one regular blob", part.ID, subtree.irregular)
	case subtree.stranger:
		return corruptRelic(relicID, "part %s: its subtree holds an entry that is neither %s nor %s",
			part.ID, relicFieldsPath, relicBodyPath)
	case subtree.fields == "":
		return corruptRelic(relicID, "part %s: it has no %s", part.ID, relicFieldsPath)
	case part.Body == nil && subtree.body != "":
		return corruptRelic(relicID, "part %s: it stores a body the relic does not record", part.ID)
	case part.Body != nil && subtree.body == "":
		return corruptRelic(relicID, "part %s: the relic records a body it does not store", part.ID)
	}
	return nil
}

// measureRelicPartBlobs is the third pass of readRelicPartObjectsBatch: it
// asks Git for the type and size of each distinct blob the subtrees name and
// judges them by rule R4. Nothing is opened.
func (r *Repository) measureRelicPartBlobs(ctx context.Context, reads []*relicPartsRead) {
	if len(reads) == 0 {
		return
	}
	asked := map[string]struct{}{}
	var order []string
	var input bytes.Buffer
	for _, read := range reads {
		for _, part := range read.State.Relic.Parts {
			objects := read.parts[part.ID]
			for _, objectID := range []string{objects.Fields, objects.Body} {
				if _, repeated := asked[objectID]; repeated || objectID == "" {
					continue
				}
				asked[objectID] = struct{}{}
				order = append(order, objectID)
				fmt.Fprintf(&input, "%s\n", objectID)
			}
		}
	}
	output, err := r.Git(ctx, input.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		failRelicPartsReads(reads, err)
		return
	}
	blobs, err := parseRelicBlobInfo(output, order)
	if err != nil {
		failRelicPartsReads(reads, err)
		return
	}

	for _, read := range reads {
		relicID := read.State.RelicID
		// The state's own order, so that the same commit always names the same
		// part first.
		for _, part := range read.State.Relic.Parts {
			objects := read.parts[part.ID]
			fields := blobs[objects.Fields]
			if read.err = relicBlobPresent(relicID, part.ID, relicFieldsPath, fields); read.err != nil {
				break
			}
			if fields.size > MaxObjectBytes {
				read.err = corruptRelic(relicID, "part %s: its %s is %d bytes; the limit is %d bytes",
					part.ID, relicFieldsPath, fields.size, MaxObjectBytes)
				break
			}
			objects.FieldsBytes = fields.size
			if part.Body != nil {
				body := blobs[objects.Body]
				if read.err = relicBlobPresent(relicID, part.ID, relicBodyPath, body); read.err != nil {
					break
				}
				if body.size > core.MaxRelicBodyBytes {
					read.err = corruptRelic(relicID, "part %s: its body is %d bytes; the limit is %d bytes",
						part.ID, body.size, core.MaxRelicBodyBytes)
					break
				}
				if body.size != part.Body.Bytes {
					read.err = corruptRelic(relicID, "part %s: its body is %d bytes, but the relic records %d bytes",
						part.ID, body.size, part.Body.Bytes)
					break
				}
				objects.BodyBytes = body.size
			}
			read.parts[part.ID] = objects
		}
	}
}

// relicBlobPresent refuses a blob a part's subtree names that Git does not
// have, or that is not a blob at all.
func relicBlobPresent(relicID, partID, what string, blob relicBlobInfo) error {
	switch {
	case blob.missing:
		return corruptRelic(relicID, "part %s: its %s is missing from Git", partID, what)
	case blob.kind != "blob":
		return corruptRelic(relicID, "part %s: its %s is not one regular blob", partID, what)
	}
	return nil
}

// parseRelicBlobInfo reads `git cat-file --batch-check` output for the object
// IDs asked, in order. Each line is
//
//	<object> SP <type> SP <size>
//
// or, for an object Git does not have, `<object> SP missing`.
func parseRelicBlobInfo(output []byte, asked []string) (map[string]relicBlobInfo, error) {
	invalid := core.Errorf(core.CategoryOperational, "Git returned an invalid listing of relic object sizes")
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return nil, invalid
	}
	lines := strings.Split(string(output[:len(output)-1]), "\n")
	if len(lines) != len(asked) {
		return nil, invalid
	}
	blobs := make(map[string]relicBlobInfo, len(asked))
	for index, line := range lines {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[0] == asked[index] && fields[1] == "missing":
			blobs[asked[index]] = relicBlobInfo{missing: true}
		case len(fields) == 3 && fields[0] == asked[index]:
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil || size < 0 {
				return nil, invalid
			}
			blobs[asked[index]] = relicBlobInfo{kind: fields[1], size: size}
		default:
			return nil, invalid
		}
	}
	return blobs, nil
}
