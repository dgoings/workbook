package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	RelicEditAddLink    RelicEditKind = "add-link"
	RelicEditRemoveLink RelicEditKind = "remove-link"
)

// The states a link's target can be in, as TargetState names them.
const (
	// RelicLinkTargetDeleted is a deleted task or relic.
	RelicLinkTargetDeleted = "deleted"
	// RelicLinkTargetRemoved is a removed part.
	RelicLinkTargetRemoved = "removed"
	// RelicLinkTargetMissing is a task or relic this clone does not have, or
	// a part the local copy of its relic does not have.
	RelicLinkTargetMissing = "missing"
	// RelicLinkTargetRetired is a retired relic, which is not removed.
	RelicLinkTargetRetired = "retired"
)

// RelicDocumentLink is one link in a relic document. TargetRemoved says that
// what it points at is deleted, removed or not there, and TargetState says
// which, or that a relic it points at is retired; relic show sets both, and
// relic export never does.
type RelicDocumentLink struct {
	RelicLink
	TargetRemoved bool   `json:"targetRemoved,omitempty"`
	TargetState   string `json:"targetState,omitempty"`
	// RemovedAt is the date the target's content was removed, when a
	// compaction dropped it. relic show prints it; it is not in the document.
	RemovedAt *time.Time `json:"-"`
}

// markTarget records the state of a link's target.
func (link *RelicDocumentLink) markTarget(state string) {
	link.TargetState = state
	link.TargetRemoved = state == RelicLinkTargetDeleted || state == RelicLinkTargetRemoved || state == RelicLinkTargetMissing
}

func errNoTaskReader() error {
	return Errorf(CategoryOperational, "relic service has no task reader")
}

// IsRelicAddress reports whether what a command was given to link to, or to
// look links up for, names a relic or a part of one rather than a task.
func IsRelicAddress(typed string) bool {
	return typedRelicAddress(typed)
}

// typedRelicAddress reports whether what an add-link names is a relic or a
// part of one: a task ID's ULID never starts with R, so <KEY>-R... can name
// only a relic.
func typedRelicAddress(typed string) bool {
	key, rest, separated := strings.Cut(strings.ToUpper(typed), "-")
	return separated && key != "" && strings.HasPrefix(rest, "R")
}

// linkTask resolves the task an add-link points at, and gives the refusal of
// a new link to one that is deleted, as workbook depend refuses a deleted
// dependency, separately: a link that already exists is not refused.
func (s RelicService) linkTask(ctx context.Context, typed string) (string, error, error) {
	if s.Tasks == nil {
		return "", nil, errNoTaskReader()
	}
	taskID, err := s.Tasks.Resolve(ctx, s.Config, typed)
	if err != nil {
		return "", nil, err
	}
	task, err := s.Tasks.Get(ctx, s.Config, taskID)
	if err != nil {
		return "", nil, err
	}
	if task.State.Task.Deleted {
		return taskID, Errorf(CategoryValidation, "task %s is deleted; restore it first", taskID), nil
	}
	return taskID, nil, nil
}

// relicTypedInMessage quotes something typed for a refusal, so nothing in it
// reaches a terminal as it is, or gives its size alone when it is too long to
// repeat.
func relicTypedInMessage(typed string) string {
	if len(typed) > relicMessageNameLimit {
		return fmt.Sprintf("(%d bytes long)", len(typed))
	}
	return strconv.Quote(typed)
}

// addLink records a link.add, unless the relic already has a live link with
// the same from, target, relationship and pointer, which an earlier edit in
// this update may have added: a repeated command adds nothing.
func (u *relicUpdate) addLink(ctx context.Context, edit RelicEdit) error {
	if err := ValidateRelicRelation(edit.Rel); err != nil {
		return err
	}
	var pointer json.RawMessage
	if edit.Pointer != nil {
		canonical, err := CanonicalRelicLinkPointer(edit.Pointer)
		if err != nil {
			return err
		}
		pointer = canonical
	}
	var from string
	if edit.From != "" {
		part, err := u.livePart(edit.From)
		if err != nil {
			return err
		}
		from = part.ID
	}
	target, seen, gone, err := u.linkTarget(ctx, edit.Target)
	if err != nil {
		return err
	}
	if target.Relic == u.relicID && target.Part == from {
		return Errorf(CategoryValidation, "a link cannot point at what it starts from")
	}
	change := RelicLinkChange{From: from, To: target, Rel: edit.Rel, Seen: seen, Pointer: pointer}
	for _, link := range u.links.Links {
		if link.From == change.From && link.To == change.To && link.Rel == change.Rel && bytes.Equal(link.Pointer, change.Pointer) {
			return nil
		}
	}
	// A target removed or deleted since is refused only for a new link, so a
	// command repeated after the target went changes nothing.
	if gone != nil {
		return gone
	}
	return u.record(RelicOperation{Type: RelicOperationLinkAdd, Link: &change})
}

// linkTarget resolves what an add-link points at, and for a relic target the
// version it sees: the last operation ID of the relic's tip, or, for the
// relic being updated, of the tip this update starts from. A relic target is
// one of this project's relics, found by prefix and read once per update;
// the relic being updated is its working copy, so a part an earlier edit
// added can be the target. A deleted task or relic and a removed part come
// back as gone, the refusal of a new link to them, which the caller gives
// only when the link does not exist already.
func (u *relicUpdate) linkTarget(ctx context.Context, typed string) (target RelicLinkTarget, seen string, gone, err error) {
	if !typedRelicAddress(typed) {
		taskID, gone, err := u.service.linkTask(ctx, typed)
		return RelicLinkTarget{Task: taskID}, "", gone, err
	}
	relicPrefix, partPrefix, hasPart, err := ParsePartAddress(typed)
	if err != nil {
		return RelicLinkTarget{}, "", nil, err
	}
	if key, _, _ := strings.Cut(strings.ToUpper(relicPrefix), "-"); !u.service.keys().Contains(key) {
		return RelicLinkTarget{}, "", nil, Errorf(CategoryValidation,
			"relic %s is in another project; a link stays within one project", relicTypedInMessage(relicPrefix))
	}
	if u.heads == nil {
		heads, err := u.service.Reader.ListRelicHeads(ctx, u.service.Config)
		if err != nil {
			return RelicLinkTarget{}, "", nil, err
		}
		u.heads = heads
	}
	head, err := ResolveRelicPrefix(u.heads, relicPrefix)
	if err != nil {
		return RelicLinkTarget{}, "", nil, err
	}
	var relic RelicData
	var tip RelicSnapshot
	if head.RelicID == u.relicID {
		relic, tip = u.relic, u.parent
	} else {
		snapshot, read := u.targets[head.RelicID]
		if !read {
			if snapshot, err = u.service.Reader.GetRelic(ctx, u.service.Config, head.RelicID); err != nil {
				return RelicLinkTarget{}, "", nil, err
			}
			if u.targets == nil {
				u.targets = map[string]RelicSnapshot{}
			}
			u.targets[head.RelicID] = snapshot
		}
		relic, tip = snapshot.Record.State.Relic, snapshot
	}
	if relic.Deleted {
		gone = Errorf(CategoryValidation, "relic %s is deleted; restore it first", head.RelicID)
	}
	target = RelicLinkTarget{Relic: head.RelicID}
	if hasPart {
		part, err := resolvePartPrefix(head.RelicID, relic.Parts, partPrefix)
		if err != nil {
			return RelicLinkTarget{}, "", nil, err
		}
		if part.Dropped != nil && gone == nil {
			gone = relicStubRefusal(head.RelicID, part)
		}
		if part.Removed && gone == nil {
			gone = Errorf(CategoryValidation, "part %s of relic %s is removed; restore it first", part.ID, head.RelicID)
		}
		target.Part = part.ID
	}
	operations := tip.Operation.Operations
	if len(operations) == 0 {
		return RelicLinkTarget{}, "", nil, corrupt("relic %s has a tip that records no operation", head.RelicID)
	}
	return target, operations[len(operations)-1].ID, gone, nil
}

// removeLink records a link.remove of the one live link whose ID is the one
// given or starts with it, ignoring case. A full link ID the relic does not
// hold leaves no operation: the link was removed already, so a retry is safe.
func (u *relicUpdate) removeLink(edit RelicEdit) error {
	if strings.TrimSpace(edit.Link) == "" {
		return Errorf(CategoryValidation, "give a link ID, or a prefix of one")
	}
	wanted := strings.ToUpper(edit.Link)
	var matches []string
	for _, link := range u.links.Links {
		if link.ID == wanted {
			matches = []string{link.ID}
			break
		}
		if strings.HasPrefix(link.ID, wanted) {
			matches = append(matches, link.ID)
		}
	}
	switch len(matches) {
	case 0:
		if isCanonicalULID(wanted) {
			return nil
		}
		return Errorf(CategoryNotFound, "relic %s has no link %s", u.relicID, relicTypedInMessage(edit.Link))
	case 1:
		return u.record(RelicOperation{Type: RelicOperationLinkRemove, LinkID: matches[0]})
	default:
		return Errorf(CategoryValidation,
			"link prefix %s matches %d links of %s; use more characters", relicTypedInMessage(edit.Link), len(matches), u.relicID)
	}
}

// relicDocumentLinks returns the links a document lists of the given parts:
// on a whole-relic view (whole true) the links of the relic itself and of the
// parts listed, and on a one-part view only that part's. With targets, each
// link is marked with the state of what it points at: a task from one task
// listing, made only when there is a link to a task, and a relic or part from
// one relic listing and one read of each relic linked to, made only when
// there is a link to a relic. A listing that fails leaves its links unmarked,
// with a warning that says why: a relic read does not fail because the
// records beside it cannot be read. Only the caller's cancellation is
// returned.
func (s RelicService) relicDocumentLinks(ctx context.Context, snapshot RelicSnapshot, parts []RelicPart, whole, targets bool) ([]RelicDocumentLink, []Warning, error) {
	relicID, links := snapshot.Record.State.RelicID, snapshot.Record.Links
	listed := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		listed[part.ID] = struct{}{}
	}
	presented := make([]RelicDocumentLink, 0, len(links.Links))
	toTasks, toRelics := false, false
	for _, link := range links.Links {
		_, fromListed := listed[link.From]
		if fromListed || (whole && link.From == "") {
			presented = append(presented, RelicDocumentLink{RelicLink: link})
			toTasks = toTasks || link.To.Task != ""
			toRelics = toRelics || link.To.Relic != ""
		}
	}
	if !targets || len(presented) == 0 {
		return presented, nil, nil
	}
	var warnings []Warning
	if toTasks {
		warning, err := s.markTaskLinkTargets(ctx, relicID, presented)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, warning...)
	}
	if toRelics {
		warning, err := s.markRelicLinkTargets(ctx, snapshot, presented)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, warning...)
	}
	return presented, warnings, nil
}

// markTaskLinkTargets marks each link to a task that is deleted or not there.
func (s RelicService) markTaskLinkTargets(ctx context.Context, relicID string, presented []RelicDocumentLink) ([]Warning, error) {
	if s.Tasks == nil {
		return nil, errNoTaskReader()
	}
	tasks, err := s.Tasks.List(ctx, s.Config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []Warning{relicLinkTargetsUnreadWarning(relicID, err)}, nil
	}
	deleted := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		deleted[task.State.TaskID] = task.State.Task.Deleted
	}
	for index := range presented {
		taskID := presented[index].To.Task
		if taskID == "" {
			continue
		}
		switch isDeleted, present := deleted[taskID]; {
		case !present:
			presented[index].markTarget(RelicLinkTargetMissing)
		case isDeleted:
			presented[index].markTarget(RelicLinkTargetDeleted)
		}
	}
	return nil, nil
}

// markRelicLinkTargets marks each link to a relic or part that is deleted,
// removed, retired or not in this clone. A relic linked to that cannot be
// read is marked missing, with one warning naming it. A link to the shown
// relic itself is marked from the snapshot in hand, which is not read again.
func (s RelicService) markRelicLinkTargets(ctx context.Context, shown RelicSnapshot, presented []RelicDocumentLink) ([]Warning, error) {
	relicID := shown.Record.State.RelicID
	heads, err := s.Reader.ListRelicHeads(ctx, s.Config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []Warning{relicTargetsUnreadWarning(relicID, err)}, nil
	}
	present := make(map[string]bool, len(heads))
	for _, head := range heads {
		present[head.RelicID] = true
	}
	var warnings []Warning
	read := map[string]*RelicData{relicID: &shown.Record.State.Relic}
	for index := range presented {
		target := presented[index].To
		if target.Relic == "" {
			continue
		}
		relic, done := read[target.Relic]
		if !done && present[target.Relic] {
			snapshot, err := s.Reader.GetRelic(ctx, s.Config, target.Relic)
			switch {
			case err == nil:
				relic = &snapshot.Record.State.Relic
			case ctx.Err() != nil:
				return nil, ctx.Err()
			default:
				warnings = append(warnings, RelicLinksUnreadWarning(target.Relic))
			}
			read[target.Relic] = relic
		}
		if relic == nil {
			presented[index].markTarget(RelicLinkTargetMissing)
			continue
		}
		if relic.Compacted != nil && relic.Compacted.Boundary == RelicBoundaryDeleted && relic.Deleted {
			since := relic.Compacted.Since
			presented[index].RemovedAt = &since
		}
		if part, ok := relic.Part(target.Part); target.Part != "" && ok && part.Dropped != nil {
			removed := part.Dropped.RemovedAt
			presented[index].RemovedAt = &removed
		}
		if relic.Deleted {
			presented[index].markTarget(RelicLinkTargetDeleted)
			continue
		}
		if target.Part != "" {
			part, ok := relic.Part(target.Part)
			if !ok {
				presented[index].markTarget(RelicLinkTargetMissing)
				continue
			}
			if part.Removed {
				presented[index].markTarget(RelicLinkTargetRemoved)
				continue
			}
		}
		if relic.Retired {
			presented[index].markTarget(RelicLinkTargetRetired)
		}
	}
	return warnings, nil
}

// relicTargetsUnreadWarning is the warning a relic read gives when the relics
// its links point at cannot be listed.
func relicTargetsUnreadWarning(relicID string, err error) Warning {
	return Warning{
		Code: WarningRelicLinkTargetsUnread,
		Message: fmt.Sprintf("the relics cannot be listed, so the targets of relic %s's links to relics are not checked and links to deleted relics or removed parts are not marked: %s; run `workbook validate`",
			relicID, err.Error()),
	}
}

// relicLinkTargetsUnreadWarning is the warning a relic read gives when the
// tasks its links point at cannot be listed.
func relicLinkTargetsUnreadWarning(relicID string, err error) Warning {
	return Warning{
		Code: WarningRelicLinkTargetsUnread,
		Message: fmt.Sprintf("the tasks cannot be read, so the targets of relic %s's links are not checked and links to removed tasks are not marked: %s; run `workbook validate`",
			relicID, err.Error()),
	}
}
