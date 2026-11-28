package core

import (
	"bytes"
	"context"
	"slices"
)

// relicOlderBody is the version a body save started from when that is not the
// part's current version but one it had before: an applied version of the
// part's history, read from the commit it stands at, or, with dropped set, a
// version a compaction dropped, whose text is gone.
type relicOlderBody struct {
	version RelicHistoryBodyVersion
	dropped bool
}

// checkBodyBase is checkRelicBodyBase for an update that can merge. A base
// that is an older version of the part, or one a compaction dropped, is
// returned as older, with no error, for mergeBody. Any other base is refused
// as checkRelicBodyBase refuses it, worded the same, and so is an older base
// when the service has no history to read it from.
func (u *relicUpdate) checkBodyBase(ctx context.Context, part RelicPart, typed string) (string, *relicOlderBody, error) {
	base, refused := checkRelicBodyBase(u.relicID, part, typed)
	if refused == nil || typed == "" || part.Body == nil {
		return base, nil, refused
	}
	// A body this update already saved has no version in the history, so a
	// later save of the part in the same update starts from that one or from
	// nothing the history holds.
	if _, saved := u.bodies[part.ID]; saved {
		return "", nil, refused
	}
	older, err := u.olderBodyVersion(ctx, part.ID, relicVersionAsTyped(typed))
	if err != nil {
		return "", nil, err
	}
	if older == nil {
		return "", nil, refused
	}
	return "", older, nil
}

// olderBodyVersion finds version in the part's history: among the body
// versions the fold applied, the one a checkpoint kept included, or among the
// operations the chain's checkpoint dropped. It is nil when the history holds
// neither, or when the service has no history reader.
func (u *relicUpdate) olderBodyVersion(ctx context.Context, partID, version string) (*relicOlderBody, error) {
	service := u.service
	if service.HistoryReader == nil {
		return nil, nil
	}
	chain, err := service.HistoryReader.RelicChain(ctx, service.Config, RelicHead{RelicID: u.relicID, ObjectID: u.parent.Head})
	if err != nil {
		return nil, err
	}
	if err := service.readKeptRelic(ctx, &chain); err != nil {
		return nil, err
	}
	for _, saved := range BuildRelicHistory(chain, RelicSummary{}, partID, 0, true).BodyVersions {
		if saved.Version == version {
			return &relicOlderBody{version: saved}, nil
		}
	}
	if summary, compacted := relicCheckpointRoot(chain.Commits); compacted && slices.Contains(summary.CompactedOperations, version) {
		return &relicOlderBody{version: RelicHistoryBodyVersion{Version: version}, dropped: true}, nil
	}
	return nil, nil
}

// mergeBody records a save of part's body from an older version: the save
// itself, from that version, which the fold does not apply, and the
// part.body.merge that merges its text onto the version that stands. A save
// that cannot be merged records nothing and fails with the body conflict,
// which carries the three texts and why. A merge that comes to the text that
// stands records nothing, since the save adds no edit to it.
func (u *relicUpdate) mergeBody(ctx context.Context, part RelicPart, body RelicBodyInput, older relicOlderBody) error {
	service := u.service
	commits := []string{u.parent.Head}
	if !older.dropped && older.version.Commit != u.parent.Head {
		commits = append(commits, older.version.Commit)
	}
	versions, err := service.HistoryReader.ReadRelicVersions(ctx, service.Config, u.relicID, commits)
	if err != nil {
		return err
	}
	if len(versions) != len(commits) {
		return Errorf(CategoryCorruptData, "relic version read returned %d versions, want %d", len(versions), len(commits))
	}
	theirs, current, err := u.bodyAt(ctx, versions[0], part.ID, *part.Body)
	if err != nil {
		return err
	}
	ours := RelicBodyVersion{Format: body.Format, Bytes: int64(len(body.Content))}
	SetRelicBodyVersionContent(&ours, body.Content)
	if older.dropped {
		return u.bodyConflict(part.ID, nil, ours, theirs, RelicMergeBaseDropped, nil)
	}

	ancestorVersion := versions[len(versions)-1]
	stood, ok := ancestorVersion.Snapshot.Record.State.Relic.Part(part.ID)
	if !ok || stood.Body == nil || stood.Body.Version != older.version.Version {
		// The commit the version was saved in stores a later body of the part:
		// the version's own text is not kept, so there is nothing to merge
		// from, and the save is refused as one from a version not at hand.
		_, refused := checkRelicBodyBase(u.relicID, part, older.version.Version)
		return refused
	}
	ancestor, ancestorText, err := u.bodyAt(ctx, ancestorVersion, part.ID, *stood.Body)
	if err != nil {
		return err
	}

	id, err := service.newID(u.minted)
	if err != nil {
		return err
	}
	need := RelicBodyMergeNeed{
		Part:           part.ID,
		Ancestor:       *stood.Body,
		Current:        *part.Body,
		Other:          RelicBody{Format: body.Format, Version: id, Bytes: int64(len(body.Content))},
		OtherOperation: id,
		Stale:          id,
	}
	texts := RelicBodyMergeTexts{Ancestor: ancestorText, Current: current, Other: body.Content}
	merged, reason, overlaps := MergeRelicBody(need, texts)
	if reason != "" {
		return u.bodyConflict(part.ID, &ancestor, ours, theirs, reason, overlaps)
	}
	if bytes.Equal(merged, texts.Current) {
		return nil
	}
	if err := u.record(RelicOperation{
		ID:   id,
		Type: RelicOperationPartBodySet,
		Part: part.ID,
		Body: &RelicBodyChange{Format: body.Format, Bytes: int64(len(body.Content)), Base: older.version.Version},
	}); err != nil {
		return err
	}
	merge := RelicBodyMergeOperation(u.relicID, need, merged, u.wallTime)
	if err := u.record(merge); err != nil {
		return err
	}
	u.bodies[part.ID] = merged
	u.inputs[part.ID] = body.Content
	u.warnings = append(u.warnings, RelicBodyMergedByUpdateWarning(u.relicID, merge))
	return nil
}

// bodyAt reads a part's body at one version of the relic: as a conflict
// reports it, with its version, label, size, blob and content, and as bytes.
func (u *relicUpdate) bodyAt(ctx context.Context, version RelicVersion, partID string, stored RelicBody) (RelicBodyVersion, []byte, error) {
	read, err := u.service.Reader.ReadRelicParts(ctx, u.service.Config, version.Snapshot, []string{partID}, true)
	if err != nil {
		return RelicBodyVersion{}, nil, err
	}
	content := read[partID].Body
	body := RelicBodyVersion{Version: stored.Version, Format: stored.Format, Bytes: stored.Bytes, Object: version.Parts[partID].Body}
	SetRelicBodyVersionContent(&body, content)
	return body, content, nil
}

// bodyConflict is the failure of an update whose save from an older version
// cannot be merged: one body conflict, with nothing parked, since nothing was
// recorded.
func (u *relicUpdate) bodyConflict(
	partID string,
	base *RelicBodyVersion,
	ours, theirs RelicBodyVersion,
	reason RelicMergeReason,
	overlaps []LineMergeOverlap,
) error {
	return RelicConflictFailure{Conflicts: []RelicConflict{{
		RelicID: u.relicID,
		Type:    RelicConflictBody,
		Part:    partID,
		Body:    &RelicBodyConflict{Base: base, Ours: ours, Theirs: theirs, Reason: reason, Overlaps: overlaps},
	}}}
}
