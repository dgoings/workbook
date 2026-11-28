package core

import (
	"context"
	"slices"
	"strings"
	"time"
)

// RelicChainCommit is one commit of a relic's first-parent chain: its object
// ID, its first parent's (empty for the root) and its operation pack.
type RelicChainCommit struct {
	Commit, Parent string
	Operation      RelicOperationPack
}

// RelicChain is a relic's first-parent chain ending at Head, oldest first.
// Truncated names the commit a read stopped at; the commits below it are kept.
//
// A compacted relic's chain starts at its checkpoint, whose pack carries no
// content, so Kept holds the relic as the checkpoint stored it, which says
// which parts and bodies the changes above it start from. It is nil for any
// other chain, and the readers leave it nil: History fills it from Git.
type RelicChain struct {
	RelicID, Head string
	Commits       []RelicChainCommit
	Truncated     *HistoryTruncation
	Kept          *RelicData
}

// RelicPartObjectIDs names the Git objects one part's content is stored in at
// one commit: its fields.json blob, and its body blob, empty when it has none.
// Two equal IDs mean equal content, so a comparison opens only the objects
// that differ.
type RelicPartObjectIDs struct {
	Fields, Body string
}

// RelicVersion is one relic commit read back for a comparison: its snapshot and
// the objects each of its parts, live or removed, is stored in.
type RelicVersion struct {
	Snapshot RelicSnapshot
	Parts    map[string]RelicPartObjectIDs
}

// RelicHistoryReader reads what relic history and diffs need: the chain ending
// at a head, from the index when it holds the whole chain and from Git
// otherwise, and the versions at given commits, always from Git.
type RelicHistoryReader interface {
	RelicChain(context.Context, ProjectConfig, RelicHead) (RelicChain, error)
	ReadRelicVersions(context.Context, ProjectConfig, string, []string) ([]RelicVersion, error)
}

// RelicChangeOperation is one stored operation as history lists it. Applied is
// set on every operation that saves a body: whether the fold took the save.
type RelicChangeOperation struct {
	RelicOperation
	Applied *bool `json:"applied,omitempty"`
}

// RelicChange is one relic commit rendered as one entry, its operations in pack
// order. Summary is the operations' distinct types in that order.
type RelicChange struct {
	Commit       string                 `json:"commit"`
	Actor        string                 `json:"actor"`
	WallTime     time.Time              `json:"wallTime"`
	LogicalClock uint64                 `json:"logicalClock"`
	Summary      string                 `json:"summary"`
	Operations   []RelicChangeOperation `json:"operations"`
}

// RelicHistoryBodyVersion is one saved text of a part's body as history lists
// it, named by the operation that saved it. Base is the version the save
// started from, empty for the first body. (RelicBodyVersion is the conflict
// report's body, with its content.)
type RelicHistoryBodyVersion struct {
	Version  string    `json:"version"`
	Base     string    `json:"base,omitempty"`
	Format   string    `json:"format,omitempty"`
	Bytes    int64     `json:"bytes"`
	Actor    string    `json:"actor"`
	WallTime time.Time `json:"wallTime"`
	Commit   string    `json:"commit"`
	Current  bool      `json:"current,omitempty"`
	// Compacted marks the version a checkpoint kept: its commit, actor and
	// wall time are the checkpoint's, because the save that made it was
	// dropped, and with it the version's base.
	Compacted bool `json:"compacted,omitempty"`
}

// RelicHistoryCompaction describes the checkpoint a compacted relic's history
// starts at, which history and diffs list as a point, not as a change: its
// operation ID, which names that point; the run, when it ran and its boundary;
// and how many changes it replaced, with the first and last wall times they
// recorded.
type RelicHistoryCompaction struct {
	Version  string    `json:"version"`
	RunID    string    `json:"runId"`
	At       time.Time `json:"at"`
	Boundary string    `json:"boundary"`
	Changes  int       `json:"changes"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
}

// RelicHistory is the rendered history of a relic, or of one part of it: a
// window onto its changes, oldest first, and, for a part, every body version.
// BodyVersions is never windowed.
type RelicHistory struct {
	Relic        RelicSummary              `json:"relic"`
	Part         string                    `json:"part,omitempty"`
	Total        int                       `json:"total"`
	Showing      int                       `json:"showing"`
	Changes      []RelicChange             `json:"changes"`
	BodyVersions []RelicHistoryBodyVersion `json:"bodyVersions,omitempty"`
	Compacted    *RelicHistoryCompaction   `json:"compacted,omitempty"`
	Truncated    *HistoryTruncation        `json:"truncated,omitempty"`
}

// RelicPoint is one point of a relic's history: the state after a whole commit.
// Index is the commit's place in the chain, and Version the operation ID that
// names the point. The empty side, before the relic, has Index -1, a zero
// Commit and no Version.
type RelicPoint struct {
	Index   int
	Commit  RelicChainCommit
	Version string
}

// emptyRelicPoint is the side before a relic's root commit.
var emptyRelicPoint = RelicPoint{Index: -1}

// relicReadableCommits returns the chain's commits up to the first pack this
// build cannot read, and the truncation that ends them: the chain's own when
// every commit is readable.
func relicReadableCommits(chain RelicChain) ([]RelicChainCommit, *HistoryTruncation) {
	for index, commit := range chain.Commits {
		if commit.Operation.RequiresNewerReader() {
			return chain.Commits[:index], &HistoryTruncation{
				Commit:   commit.Commit,
				Message:  "this change was written by a newer workbook; upgrade workbook to read it",
				Category: CategoryNewerWriter,
			}
		}
	}
	return chain.Commits, chain.Truncated
}

// relicCheckpointRoot returns the summary of the checkpoint the commits start
// at, and false when they start with the relic's creation or hold nothing.
func relicCheckpointRoot(commits []RelicChainCommit) (RelicCheckpointData, bool) {
	if len(commits) == 0 {
		return RelicCheckpointData{}, false
	}
	return commits[0].Operation.Checkpoint()
}

// relicHistoryCompaction describes the checkpoint the commits start at, or is
// nil when they start at none.
func relicHistoryCompaction(commits []RelicChainCommit) *RelicHistoryCompaction {
	summary, ok := relicCheckpointRoot(commits)
	if !ok {
		return nil
	}
	pack := commits[0].Operation
	return &RelicHistoryCompaction{
		Version:  pack.Operations[0].ID,
		RunID:    summary.RunID,
		At:       pack.WallTime,
		Boundary: summary.Boundary,
		Changes:  summary.CompactedCommits,
		From:     summary.FirstWallTime,
		To:       summary.LastWallTime,
	}
}

// savesRelicBody reports whether the operation saves a body, and for which
// part.
func savesRelicBody(operation RelicOperation) (partID string, saves bool) {
	switch {
	case operation.Type == RelicOperationPartAdd && operation.Body != nil:
		return operation.ID, true
	case operation.Type == RelicOperationPartBodySet && operation.Body != nil:
		return operation.Part, true
	}
	return "", false
}

// RelicBodySavesApplied returns, for every operation of the chain that saves a
// body, whether the fold took it. It replays body versions alone, part by part
// in chain and pack order, and folds no state: a part exists from its part.add,
// whose body is always taken, and a part.body.set is taken exactly when the
// part exists, live or removed, and its base is the part's version at that
// point, empty when it has no body. A chain that starts at a checkpoint starts
// from the parts the checkpoint kept (see RelicChain.Kept); a stub is not one,
// because the fold ignores every change to it. These are ApplyRelic's rules, so
// index and Git chains give the same answer. The replay stops where the history
// does.
func RelicBodySavesApplied(chain RelicChain) map[string]bool {
	commits, _ := relicReadableCommits(chain)
	versions := map[string]string{}
	applied := map[string]bool{}
	if chain.Kept != nil {
		for _, part := range chain.Kept.Parts {
			if part.Dropped != nil {
				continue
			}
			versions[part.ID] = ""
			if part.Body != nil {
				versions[part.ID] = part.Body.Version
			}
		}
	}
	for _, commit := range commits {
		for _, operation := range commit.Operation.Operations {
			partID, saves := savesRelicBody(operation)
			switch operation.Type {
			case RelicOperationPartAdd:
				if _, exists := versions[operation.ID]; exists {
					if saves {
						applied[operation.ID] = false
					}
					continue
				}
				versions[operation.ID] = ""
				if saves {
					versions[operation.ID] = operation.ID
					applied[operation.ID] = true
				}
			case RelicOperationPartBodySet:
				if !saves {
					continue
				}
				current, exists := versions[partID]
				took := exists && operation.Body.Base == current
				applied[operation.ID] = took
				if took {
					versions[partID] = operation.ID
				}
			}
		}
	}
	return applied
}

// relicOperationNamesPart reports whether an operation belongs to a part's
// history: it adds or names the part, as RelicPartsNamedBy counts, or it links
// from the part.
func relicOperationNamesPart(operation RelicOperation, partID string) bool {
	if operation.Type == RelicOperationPartAdd && operation.ID == partID {
		return true
	}
	if operation.Part == partID {
		return true
	}
	return operation.Type == RelicOperationLinkAdd && operation.Link != nil && operation.Link.From == partID
}

// relicChangeOperations lists the pack's operations, or only those naming the
// part when partID is set, each with its applied mark. It is never nil.
func relicChangeOperations(operations []RelicOperation, applied map[string]bool, partID string) []RelicChangeOperation {
	listed := make([]RelicChangeOperation, 0, len(operations))
	for _, operation := range operations {
		if partID != "" && !relicOperationNamesPart(operation, partID) {
			continue
		}
		entry := RelicChangeOperation{RelicOperation: operation}
		if _, saves := savesRelicBody(operation); saves {
			took := applied[operation.ID]
			entry.Applied = &took
		}
		listed = append(listed, entry)
	}
	return listed
}

// relicSummaryWords are the operation types a change's summary names in words;
// every other type is named as stored.
var relicSummaryWords = map[RelicOperationType]string{
	RelicOperationRetire:   "retired",
	RelicOperationUnretire: "unretired",
}

// relicChangeSummary joins the operations' distinct types in pack order, a
// retirement and its reversal in words. A type this build does not know is
// named like any other.
func relicChangeSummary(operations []RelicChangeOperation) string {
	seen := map[RelicOperationType]bool{}
	types := make([]string, 0, len(operations))
	for _, operation := range operations {
		if seen[operation.Type] {
			continue
		}
		seen[operation.Type] = true
		name, ok := relicSummaryWords[operation.Type]
		if !ok {
			name = string(operation.Type)
		}
		types = append(types, name)
	}
	return strings.Join(types, ", ")
}

// BuildRelicHistory renders a relic's chain, or one part's history when partID
// is set, keeping the last limit changes (DefaultChangeLimit when limit is not
// positive) unless all is set. A part's history lists only the changes with an
// operation naming the part, each with only those operations, and every body
// version of the part. The listing ends at the first pack written by a newer
// Workbook, which becomes the truncation.
//
// A checkpoint is where a history starts, not a change in it: it is described
// by Compacted and neither listed nor counted, and the body it kept for the
// part is the first body version, marked as compacted.
func BuildRelicHistory(chain RelicChain, summary RelicSummary, partID string, limit int, all bool) RelicHistory {
	commits, truncation := relicReadableCommits(chain)
	applied := RelicBodySavesApplied(chain)
	var versions []RelicHistoryBodyVersion
	compacted := relicHistoryCompaction(commits)
	if compacted != nil {
		if kept, ok := relicKeptBodyVersion(chain.Kept, commits[0], partID); ok {
			versions = append(versions, kept)
		}
		commits = commits[1:]
	}
	changes := make([]RelicChange, 0, len(commits))
	for _, commit := range commits {
		pack := commit.Operation
		operations := relicChangeOperations(pack.Operations, applied, partID)
		if partID != "" && len(operations) == 0 {
			continue
		}
		changes = append(changes, RelicChange{
			Commit:       commit.Commit,
			Actor:        pack.Actor.ID,
			WallTime:     pack.WallTime,
			LogicalClock: pack.LogicalClock,
			Summary:      relicChangeSummary(operations),
			Operations:   operations,
		})
		if partID == "" {
			continue
		}
		for _, operation := range operations {
			if saved, saves := savesRelicBody(operation.RelicOperation); !saves || saved != partID || !applied[operation.ID] {
				continue
			}
			versions = append(versions, RelicHistoryBodyVersion{
				Version:  operation.ID,
				Base:     operation.Body.Base,
				Format:   operation.Body.Format,
				Bytes:    operation.Body.Bytes,
				Actor:    pack.Actor.ID,
				WallTime: pack.WallTime,
				Commit:   commit.Commit,
			})
		}
	}
	if len(versions) > 0 {
		versions[len(versions)-1].Current = true
	}

	history := RelicHistory{
		Relic: summary, Part: partID, Total: len(changes), BodyVersions: versions,
		Compacted: compacted, Truncated: truncation,
	}
	if !all {
		if limit <= 0 {
			limit = DefaultChangeLimit
		}
		if len(changes) > limit {
			changes = changes[len(changes)-limit:]
		}
	}
	history.Changes = changes
	history.Showing = len(changes)
	return history
}

// relicKeptBodyVersion is the body a checkpoint kept for the part, as a body
// version: the checkpoint's commit, actor and wall time stand for the save that
// made it, which was dropped, and its base is not recorded. There is none for a
// whole relic, or for a part kept without a body.
func relicKeptBodyVersion(kept *RelicData, checkpoint RelicChainCommit, partID string) (RelicHistoryBodyVersion, bool) {
	if kept == nil || partID == "" {
		return RelicHistoryBodyVersion{}, false
	}
	part, ok := kept.Part(partID)
	if !ok || part.Body == nil {
		return RelicHistoryBodyVersion{}, false
	}
	return RelicHistoryBodyVersion{
		Version:   part.Body.Version,
		Format:    part.Body.Format,
		Bytes:     part.Body.Bytes,
		Actor:     checkpoint.Operation.Actor.ID,
		WallTime:  checkpoint.Operation.WallTime,
		Commit:    checkpoint.Commit,
		Compacted: true,
	}, true
}

// relicPointAt names the point after the chain's commit at index by the last
// operation ID of that commit, the name Workbook gives a point it chose. The
// point before the first commit is the empty side, except in a compacted
// history, which reaches back only to its checkpoint: that is then the point.
func relicPointAt(chain RelicChain, index int) RelicPoint {
	if index < 0 {
		if _, compacted := relicCheckpointRoot(chain.Commits); !compacted {
			return emptyRelicPoint
		}
		index = 0
	}
	commit := chain.Commits[index]
	version := ""
	if operations := commit.Operation.Operations; len(operations) > 0 {
		version = operations[len(operations)-1].ID
	}
	return RelicPoint{Index: index, Commit: commit, Version: version}
}

// ResolveRelicVersion finds the point a version names: an operation ID of the
// chain, or an unambiguous, case-insensitive prefix of one. Every operation of
// a commit names the same point, so a prefix is ambiguous only when it matches
// operations of more than one commit. The point carries the full ID matched, or
// the commit's last operation ID when a prefix matched several of its
// operations. A Git commit ID is never a version.
//
// In a compacted history, a version that names a change the checkpoint
// dropped, by its ID or by a prefix, is refused as such. Only a whole ID of a
// change still in the chain is taken ahead of that, so that a prefix never
// answers for a different change than the one it named before the compaction.
func ResolveRelicVersion(chain RelicChain, relicID, prefix string) (RelicPoint, error) {
	if strings.TrimSpace(prefix) == "" {
		return RelicPoint{}, Errorf(CategoryValidation, "give a change ID, or a prefix of one")
	}
	wanted := strings.ToUpper(prefix)
	var (
		matched []int
		version string
	)
	for index, commit := range chain.Commits {
		hits := 0
		for _, operation := range commit.Operation.Operations {
			id := strings.ToUpper(operation.ID)
			if id == wanted {
				return RelicPoint{Index: index, Commit: commit, Version: operation.ID}, nil
			}
			if strings.HasPrefix(id, wanted) {
				hits++
				version = operation.ID
			}
		}
		if hits > 0 {
			matched = append(matched, index)
			if hits > 1 {
				version = relicPointAt(chain, index).Version
			}
		}
	}
	// The body a checkpoint kept is listed among a part's body versions under
	// the ID of the save that made it, which the checkpoint dropped. That whole
	// ID names the checkpoint, where the body stands, so an ID copied from the
	// list answers. A prefix of it does not: a prefix names a change.
	if chain.Kept != nil && len(chain.Commits) > 0 {
		for _, part := range chain.Kept.Parts {
			if part.Dropped == nil && part.Body != nil && strings.ToUpper(part.Body.Version) == wanted {
				return RelicPoint{Index: 0, Commit: chain.Commits[0], Version: part.Body.Version}, nil
			}
		}
	}
	if err := refuseDroppedRelicVersion(chain, relicID, prefix); err != nil {
		return RelicPoint{}, err
	}
	switch len(matched) {
	case 0:
		return RelicPoint{}, Errorf(CategoryNotFound,
			"relic %s has no change %q in its history; a version is a change ID from \"workbook relic show %s --history\", not a Git commit",
			relicID, prefix, relicID)
	case 1:
		return RelicPoint{Index: matched[0], Commit: chain.Commits[matched[0]], Version: version}, nil
	default:
		return RelicPoint{}, Errorf(CategoryValidation,
			"change prefix %q matches %d changes of %s; use more characters", prefix, len(matched), relicID)
	}
}

// refuseDroppedRelicVersion refuses a version that names, in full or by a
// prefix, a change the chain's checkpoint dropped. The refusal names the change
// in full when only one matches, and as it was typed otherwise.
func refuseDroppedRelicVersion(chain RelicChain, relicID, prefix string) error {
	summary, compacted := relicCheckpointRoot(chain.Commits)
	if !compacted {
		return nil
	}
	wanted := strings.ToUpper(prefix)
	named, matches := "", 0
	for _, id := range summary.CompactedOperations {
		if strings.HasPrefix(strings.ToUpper(id), wanted) {
			named = id
			matches++
		}
	}
	if matches == 0 {
		return nil
	}
	if matches > 1 {
		named = prefix
	}
	checkpoint := chain.Commits[0].Operation
	return Errorf(CategoryNotFound,
		"change %s of relic %s was dropped when it was compacted on %s; history and diffs reach back only to change %s",
		named, relicID, relicDate(checkpoint.WallTime), checkpoint.Operations[0].ID)
}

// LatestRelicPoints returns the points a diff with no version compares: for
// the whole relic, the head commit against its parent; for a part, the newest
// commit naming the part against that commit's parent. The parent of a first
// commit is the empty side, and a checkpoint, which has nothing before it, is
// compared with itself. A part no change has named since a compaction has no
// latest change.
func LatestRelicPoints(chain RelicChain, partID string) (from, to RelicPoint, err error) {
	if len(chain.Commits) == 0 {
		return RelicPoint{}, RelicPoint{}, Errorf(CategoryCorruptData, "relic %s has no history", chain.RelicID)
	}
	latest := len(chain.Commits) - 1
	if partID != "" {
		latest = -1
		_, compacted := relicCheckpointRoot(chain.Commits)
		oldest := 0
		if compacted {
			oldest = 1
		}
		for index := len(chain.Commits) - 1; index >= oldest && latest < 0; index-- {
			for _, operation := range chain.Commits[index].Operation.Operations {
				if relicOperationNamesPart(operation, partID) {
					latest = index
					break
				}
			}
		}
		if latest < 0 {
			if compacted {
				return RelicPoint{}, RelicPoint{}, Errorf(CategoryNotFound,
					"relic %s has no change to part %s since it was compacted on %s",
					chain.RelicID, partID, relicDate(chain.Commits[0].Operation.WallTime))
			}
			return RelicPoint{}, RelicPoint{}, Errorf(CategoryNotFound, "relic %s has no change to part %s", chain.RelicID, partID)
		}
	}
	return relicPointAt(chain, latest-1), relicPointAt(chain, latest), nil
}

// RelicHistoryOptions chooses the window of changes History lists: the last
// Limit (DefaultChangeLimit when it is not positive), or every one with All.
type RelicHistoryOptions struct {
	Limit int
	All   bool
}

// RelicDiffOptions says how a body's line diff is shown: Context lines around
// each change, or one hunk of every line when it is negative.
type RelicDiffOptions struct {
	Context int
}

// historyReader returns the reader history and diffs read through.
func (s RelicService) historyReader() (RelicHistoryReader, error) {
	if s.HistoryReader == nil {
		return nil, Errorf(CategoryOperational, "relic service has no history reader")
	}
	return s.HistoryReader, nil
}

// relicChainAt resolves an address as Show does, a removed part included, and
// reads the relic's chain at the head it read. partID is empty when the
// address names no part.
func (s RelicService) relicChainAt(ctx context.Context, address string) (RelicSnapshot, string, RelicChain, error) {
	reader, err := s.historyReader()
	if err != nil {
		return RelicSnapshot{}, "", RelicChain{}, err
	}
	relicPrefix, partPrefix, hasPart, err := ParsePartAddress(address)
	if err != nil {
		return RelicSnapshot{}, "", RelicChain{}, err
	}
	snapshot, err := s.getRelic(ctx, relicPrefix)
	if err != nil {
		return RelicSnapshot{}, "", RelicChain{}, err
	}
	relicID := snapshot.Record.State.RelicID
	partID := ""
	if hasPart {
		part, err := resolvePartPrefix(relicID, snapshot.Record.State.Relic.Parts, partPrefix)
		if err != nil {
			return RelicSnapshot{}, "", RelicChain{}, err
		}
		partID = part.ID
	}
	chain, err := reader.RelicChain(ctx, s.Config, RelicHead{RelicID: relicID, ObjectID: snapshot.Head})
	if err != nil {
		return RelicSnapshot{}, "", RelicChain{}, err
	}
	return snapshot, partID, chain, nil
}

// History lists the changes of a relic addressed as <relic> or <relic>#<part>,
// either of them a prefix, oldest first, and for a part every body version.
// It works on a deleted relic and on a removed part. A compacted relic's
// history starts at its checkpoint, whose stored state is read from Git however
// the chain was read.
func (s RelicService) History(ctx context.Context, address string, options RelicHistoryOptions) (RelicHistory, error) {
	snapshot, partID, chain, err := s.relicChainAt(ctx, address)
	if err != nil {
		return RelicHistory{}, err
	}
	if err := s.readKeptRelic(ctx, &chain); err != nil {
		return RelicHistory{}, err
	}
	return BuildRelicHistory(chain, relicSummaryOf(snapshot), partID, options.Limit, options.All), nil
}

// readKeptRelic fills chain.Kept for a history that starts at a checkpoint:
// the relic as the checkpoint stored it, read from Git by the checkpoint's
// commit however the chain was read.
func (s RelicService) readKeptRelic(ctx context.Context, chain *RelicChain) error {
	readable, _ := relicReadableCommits(*chain)
	if len(readable) == 0 {
		return nil
	}
	if _, compacted := relicCheckpointRoot(readable); !compacted {
		return nil
	}
	read, err := s.HistoryReader.ReadRelicVersions(ctx, s.Config, chain.RelicID, []string{readable[0].Commit})
	if err != nil {
		return err
	}
	if len(read) != 1 {
		return Errorf(CategoryCorruptData, "relic version read returned %d versions, want 1", len(read))
	}
	chain.Kept = &read[0].Snapshot.Record.State.Relic
	return nil
}

// Diff compares two points of a relic's history, or of one part's, in the
// order given: from the first version to the second; from the one version to
// the head; or, with none, the latest change against its parent. Versions are
// change IDs or prefixes of them. Both versions are read from Git by commit,
// and only the parts whose objects differ are opened.
func (s RelicService) Diff(ctx context.Context, address string, versions []string, options RelicDiffOptions) (RelicDiff, error) {
	if len(versions) > 2 {
		return RelicDiff{}, Errorf(CategoryInvocation, "relic diff takes at most two versions")
	}
	_, partID, chain, err := s.relicChainAt(ctx, address)
	if err != nil {
		return RelicDiff{}, err
	}
	relicID := chain.RelicID
	if truncation := chain.Truncated; truncation != nil {
		category := CategoryCorruptData
		if truncation.Category == CategoryNewerWriter {
			category = CategoryNewerWriter
		}
		message := truncation.Message
		if !strings.Contains(message, relicID) {
			message = "relic " + relicID + ": " + message
		}
		return RelicDiff{}, Errorf(category, "%s", message)
	}

	var from, to RelicPoint
	switch len(versions) {
	case 0:
		from, to, err = LatestRelicPoints(chain, partID)
	default:
		// A version may be the ID a kept body is listed under, which only
		// the checkpoint's stored state knows.
		if err = s.readKeptRelic(ctx, &chain); err != nil {
			break
		}
		from, err = ResolveRelicVersion(chain, relicID, versions[0])
		if err != nil {
			break
		}
		if len(versions) == 2 {
			to, err = ResolveRelicVersion(chain, relicID, versions[1])
		} else if len(chain.Commits) > 0 {
			to = relicPointAt(chain, len(chain.Commits)-1)
		}
	}
	if err != nil {
		return RelicDiff{}, err
	}

	fromVersion, toVersion, err := s.readRelicPoints(ctx, relicID, from, to)
	if err != nil {
		return RelicDiff{}, err
	}
	fromSide, toSide := fromVersion.side(), toVersion.side()
	fromParts, toParts := RelicPartsToRead(fromSide, toSide, partID)
	if fromSide.Content, err = s.readRelicSideParts(ctx, fromVersion, fromSide, toSide, fromParts); err != nil {
		return RelicDiff{}, err
	}
	if toSide.Content, err = s.readRelicSideParts(ctx, toVersion, toSide, fromSide, toParts); err != nil {
		return RelicDiff{}, err
	}
	diff := CompareRelicVersions(fromSide, toSide, partID, options.Context)
	if err := diff.Err(); err != nil {
		return RelicDiff{}, err
	}
	diff.Relic = relicID
	diff.Compacted = relicHistoryCompaction(chain.Commits)
	diff.From = relicDiffPointOf(from)
	if point := relicDiffPointOf(to); point != nil {
		diff.To = *point
	}
	return diff, nil
}

// readRelicPoints reads the commits of the two points, each once. The side
// before the relic is nil.
func (s RelicService) readRelicPoints(ctx context.Context, relicID string, from, to RelicPoint) (*RelicVersion, *RelicVersion, error) {
	var commits []string
	for _, point := range []RelicPoint{from, to} {
		if point.Index >= 0 && !slices.Contains(commits, point.Commit.Commit) {
			commits = append(commits, point.Commit.Commit)
		}
	}
	read, err := s.HistoryReader.ReadRelicVersions(ctx, s.Config, relicID, commits)
	if err != nil {
		return nil, nil, err
	}
	if len(read) != len(commits) {
		return nil, nil, Errorf(CategoryCorruptData, "relic version read returned %d versions, want %d", len(read), len(commits))
	}
	version := func(point RelicPoint) *RelicVersion {
		if point.Index < 0 {
			return nil
		}
		return &read[slices.Index(commits, point.Commit.Commit)]
	}
	return version(from), version(to), nil
}

// side is the version as one side of a comparison, before any part is read:
// the empty side for nil.
func (version *RelicVersion) side() RelicVersionSide {
	if version == nil {
		return RelicVersionSide{}
	}
	return RelicVersionSide{Record: &version.Snapshot.Record, Objects: version.Parts}
}

// readRelicSideParts reads the parts listed for one side through the relic
// reader, with bodies only when one of them has a body that differs from the
// other side's. It reads nothing when no part is listed.
func (s RelicService) readRelicSideParts(
	ctx context.Context,
	version *RelicVersion,
	side, other RelicVersionSide,
	parts []string,
) (map[string]RelicPartContent, error) {
	if len(parts) == 0 || version == nil {
		return nil, nil
	}
	return s.Reader.ReadRelicParts(ctx, s.Config, version.Snapshot, parts, RelicBodiesToRead(side, other, parts))
}

// relicDiffPointOf describes a point for a diff: nil for the side before the
// relic.
func relicDiffPointOf(point RelicPoint) *RelicDiffPoint {
	if point.Index < 0 {
		return nil
	}
	return &RelicDiffPoint{
		Version:  point.Version,
		Commit:   point.Commit.Commit,
		Actor:    point.Commit.Operation.Actor.ID,
		WallTime: point.Commit.Operation.WallTime,
	}
}
