package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	RelicEditAddLink    RelicEditKind = "add-link"
	RelicEditRemoveLink RelicEditKind = "remove-link"
)

// RelicDocumentLink is one link in a relic document. TargetRemoved says that
// the task it points at is deleted or not there; relic show sets it, and
// relic export never does.
type RelicDocumentLink struct {
	RelicLink
	TargetRemoved bool `json:"targetRemoved,omitempty"`
}

func errNoTaskReader() error {
	return Errorf(CategoryOperational, "relic service has no task reader")
}

// linkTask resolves the task an add-link points at and refuses one that is
// deleted, as workbook depend refuses a deleted dependency. A relic ID is
// refused by its shape before any task is read: a task ID's ULID never starts
// with R, so <KEY>-R... can name only a relic.
func (s RelicService) linkTask(ctx context.Context, typed string) (string, error) {
	if key, rest, separated := strings.Cut(strings.ToUpper(typed), "-"); separated && key != "" && strings.HasPrefix(rest, "R") {
		return "", Errorf(CategoryValidation, "a relic links only to tasks so far; %s is not a task", relicTypedInMessage(typed))
	}
	if s.Tasks == nil {
		return "", errNoTaskReader()
	}
	taskID, err := s.Tasks.Resolve(ctx, s.Config, typed)
	if err != nil {
		return "", err
	}
	task, err := s.Tasks.Get(ctx, s.Config, taskID)
	if err != nil {
		return "", err
	}
	if task.State.Task.Deleted {
		return "", Errorf(CategoryValidation, "task %s is deleted; restore it first", taskID)
	}
	return taskID, nil
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
// the same from, target and relationship, which an earlier edit in this
// update may have added: a repeated command adds nothing.
func (u *relicUpdate) addLink(ctx context.Context, edit RelicEdit) error {
	if err := ValidateRelicRelation(edit.Rel); err != nil {
		return err
	}
	var from string
	if edit.From != "" {
		part, err := u.livePart(edit.From)
		if err != nil {
			return err
		}
		from = part.ID
	}
	taskID, err := u.service.linkTask(ctx, edit.Task)
	if err != nil {
		return err
	}
	change := RelicLinkChange{From: from, To: RelicLinkTarget{Task: taskID}, Rel: edit.Rel}
	for _, link := range u.links.Links {
		if link.From == change.From && link.To == change.To && link.Rel == change.Rel {
			return nil
		}
	}
	return u.record(RelicOperation{Type: RelicOperationLinkAdd, Link: &change})
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
// link to a task that is deleted or not there is marked, from one task
// listing made only when there is a link to mark.
func (s RelicService) relicDocumentLinks(ctx context.Context, links RelicLinksDocument, parts []RelicPart, whole, targets bool) ([]RelicDocumentLink, error) {
	listed := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		listed[part.ID] = struct{}{}
	}
	presented := make([]RelicDocumentLink, 0, len(links.Links))
	for _, link := range links.Links {
		_, fromListed := listed[link.From]
		if fromListed || (whole && link.From == "") {
			presented = append(presented, RelicDocumentLink{RelicLink: link})
		}
	}
	if !targets || len(presented) == 0 {
		return presented, nil
	}
	if s.Tasks == nil {
		return nil, errNoTaskReader()
	}
	tasks, err := s.Tasks.List(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		live[task.State.TaskID] = !task.State.Task.Deleted
	}
	for index := range presented {
		presented[index].TargetRemoved = !live[presented[index].To.Task]
	}
	return presented, nil
}
