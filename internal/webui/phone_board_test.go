package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// phoneBoard is a whole board held in memory, for the browser checks of the
// phone layout: a reader there sees the board, opens a task, changes its
// status, priority and labels, assigns it to themselves, comments, reads its
// history and creates another, and each of those has to land somewhere the
// next read can see.
//
// Its statuses are a project's own rather than the built-in six: the default
// one is called inbox, the one `workbook next` draws from is called queued and
// sits second, and there is no status named ready at all. A board that opened
// a phone on a status chosen by name rather than by the next tag would open on
// the wrong column here.
//
// The tasks carry the things that widen a page on a narrow screen: a title
// with no break in it, a long label, a long person's name, a wide code block in
// a description and in a comment, and a long attachment name.
type phoneBoard struct {
	mu      sync.Mutex
	tasks   []core.Task
	created int
	edits   int
}

const (
	phoneTaskID    = "WB-01J0000000000000000000PH01"
	phoneWideID    = "WB-01J0000000000000000000PH02"
	phoneIdentity  = "dylan@example.com"
	phoneLongLabel = "a-label-long-enough-to-run-past-the-edge-of-a-narrow-phone-screen"
	phoneLongName  = "someone.with.a.remarkably.long.address.that.has.no.spaces@a-long-subdomain.example.com"
)

// phoneStatuses are the board's columns in order. queued is the one tagged
// next.
var phoneStatuses = []struct {
	status core.Status
	label  string
	tags   []core.StatusTag
}{
	{"inbox", "Inbox", []core.StatusTag{core.StatusTagDefault}},
	{"queued", "Up next", []core.StatusTag{core.StatusTagNext}},
	{"doing", "Doing", nil},
	{"review", "In review", nil},
	{"shipped", "Shipped", []core.StatusTag{core.StatusTagDone}},
}

func phoneVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	definitions := make([]core.StatusDefinition, len(phoneStatuses))
	for index, status := range phoneStatuses {
		definitions[index] = core.StatusDefinition{Status: status.status, Label: status.label, Rank: fmt.Sprintf("%d/1", index+1), Tags: status.tags}
	}
	vocabulary, err := core.NewVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("build the phone board's statuses: %v", err)
	}
	return vocabulary
}

var phoneStamp = time.Date(2026, time.November, 20, 9, 30, 0, 0, time.UTC)

func newPhoneBoard() *phoneBoard {
	wideCode := "```\n" + strings.Repeat("wide_identifier_", 12) + "= compute(" + strings.Repeat("argument, ", 10) + ")\n```"
	task := func(id, title string, status core.Status, priority core.Priority, rank string) core.Task {
		made := clientPlacementTask(id, title, status, priority)
		made.Rank = rank
		made.Head = "head-" + id
		made.CreatedAt = phoneStamp
		made.UpdatedAt = phoneStamp
		return made
	}
	tasks := []core.Task{
		task(phoneTaskID, "Read the board on a phone", "queued", core.PriorityHigh, "1/1"),
		task(phoneWideID, "Averyverylongtitlewithnobreakanywhereinitthatwouldwidenanarrowscreenifitcouldnotwrap", "queued", core.PriorityMedium, "2/1"),
	}
	tasks[0].Description = "What a phone shows.\n\n" + wideCode
	tasks[0].Labels = []string{"mobile", phoneLongLabel}
	tasks[0].Assignments = []core.Assignment{{Principal: phoneLongName, Creator: phoneIdentity, CreatedAt: phoneStamp}}
	tasks[0].Attachments = []core.Attachment{{ID: "att-long", Author: phoneIdentity, AddedAt: phoneStamp, AttachmentData: core.AttachmentData{
		Kind: core.AttachmentFile, Name: strings.Repeat("screenshot-of-the-board-", 5) + ".png", Media: "image/png", Size: 48213,
		Blob: "0123456789abcdef0123456789abcdef01234567",
	}}}
	tasks[0].Comments = []core.Comment{{ID: "comment-wide", Author: phoneLongName, Body: "A comment with a wide block.\n\n" + wideCode, CreatedAt: phoneStamp}}
	tasks[1].Labels = []string{phoneLongLabel}
	// Enough cards in the first column that it is taller than any phone, so a
	// stroke on a card has somewhere to scroll.
	for index := 3; index <= 14; index++ {
		tasks = append(tasks, task(fmt.Sprintf("WB-01J0000000000000000000PH%02d", index), fmt.Sprintf("Queued task %d", index), "queued", core.PriorityLow, fmt.Sprintf("%d/1", index)))
	}
	statusTasks := map[core.Status]int{"inbox": 2, "doing": 3, "review": 1, "shipped": 4}
	next := 20
	for _, status := range phoneStatuses {
		for count := 0; count < statusTasks[status.status]; count++ {
			tasks = append(tasks, task(fmt.Sprintf("WB-01J0000000000000000000PH%02d", next), fmt.Sprintf("%s task %d", status.label, count+1), status.status, core.PriorityMedium, fmt.Sprintf("%d/1", count+1)))
			next++
		}
	}
	return &phoneBoard{tasks: tasks}
}

func (board *phoneBoard) find(id string) (int, error) {
	for index, task := range board.tasks {
		if task.ID == id {
			return index, nil
		}
	}
	return -1, core.Errorf(core.CategoryNotFound, "task %q not found", id)
}

// touched records one change to a task and moves its head, as a real write
// would, so the page's next change is composed against the head this one
// left.
func (board *phoneBoard) touched(index int) core.MutationResult {
	board.edits++
	board.tasks[index].Head = fmt.Sprintf("head-%s-%d", board.tasks[index].ID, board.edits)
	board.tasks[index].UpdatedAt = phoneStamp.Add(time.Duration(board.edits) * time.Minute)
	return core.MutationResult{Task: board.tasks[index]}
}

func (board *phoneBoard) task(id string) core.Task {
	board.mu.Lock()
	defer board.mu.Unlock()
	index, err := board.find(id)
	if err != nil {
		return core.Task{}
	}
	return board.tasks[index]
}

func (board *phoneBoard) options(t *testing.T) Options {
	t.Helper()
	vocabulary := phoneVocabulary(t)
	detail := func(_ context.Context, id string) (core.TaskDetail, error) {
		board.mu.Lock()
		defer board.mu.Unlock()
		index, err := board.find(id)
		if err != nil {
			return core.TaskDetail{}, err
		}
		task := board.tasks[index]
		comments := make([]core.ThreadComment, len(task.Comments))
		for at, comment := range task.Comments {
			comments[at] = core.ThreadComment{Comment: comment, Editable: comment.Author == phoneIdentity}
		}
		return core.TaskDetail{Task: task, Comments: comments}, nil
	}
	return Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{Vocabulary: vocabulary, Head: "phone-head", Display: core.DisplaySettings{Name: "Atlas"}}, nil
		},
		Identity: phoneIdentity,
		List: func(context.Context) ([]core.Task, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			return append([]core.Task(nil), board.tasks...), nil
		},
		Detail: detail,
		History: func(ctx context.Context, id string) (core.TaskDetail, error) {
			read, err := detail(ctx, id)
			if err != nil {
				return read, err
			}
			read.History = &core.ChangeLog{Total: 2, Showing: 2, Changes: []core.Change{
				{Commit: "c2", Actor: phoneLongName, WallTime: phoneStamp.Add(time.Hour), Summary: "Changed the title to " + strings.Repeat("an-unbroken-title-", 6),
					Fields: []core.FieldChange{{Field: "title", Kind: "changed", From: "Read the board", To: strings.Repeat("an-unbroken-title-", 6)}}},
				{Commit: "c1", Actor: phoneIdentity, WallTime: phoneStamp, Summary: "Created", Fields: []core.FieldChange{{Field: "task", Kind: "created", To: "Read the board"}}},
			}}
			return read, nil
		},
		Create: func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			board.created++
			created := clientPlacementTask(fmt.Sprintf("WB-01J0000000000000000000PN%02d", board.created), input.Title, input.Status, input.Priority)
			created.Description = input.Description
			if input.Labels != nil {
				created.Labels = input.Labels
			}
			created.Head = "head-created"
			created.CreatedAt, created.UpdatedAt = phoneStamp, phoneStamp
			board.tasks = append(board.tasks, created)
			return core.MutationResult{Task: created}, nil
		},
		Update: func(_ context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			index, err := board.find(id)
			if err != nil {
				return core.MutationResult{}, err
			}
			task := &board.tasks[index]
			if input.Title != nil {
				task.Title = *input.Title
			}
			if input.Description != nil {
				task.Description = *input.Description
			}
			if input.Status != nil {
				task.Status = *input.Status
			}
			if input.Priority != nil {
				task.Priority = *input.Priority
			}
			if input.Labels != nil {
				task.Labels = append([]string{}, (*input.Labels)...)
			}
			return board.touched(index), nil
		},
		UpdateStatus: func(_ context.Context, id string, status core.Status, _ string) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			index, err := board.find(id)
			if err != nil {
				return core.MutationResult{}, err
			}
			board.tasks[index].Status = status
			return board.touched(index), nil
		},
		AddComment: func(_ context.Context, id string, input core.CommentAddInput) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			index, err := board.find(id)
			if err != nil {
				return core.MutationResult{}, err
			}
			task := &board.tasks[index]
			task.Comments = append(task.Comments, core.Comment{ID: fmt.Sprintf("comment-%d", len(task.Comments)+1), Author: phoneIdentity, Body: input.Body, CreatedAt: phoneStamp})
			return board.touched(index), nil
		},
		Assign: func(_ context.Context, id string, input core.AssignInput) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			index, err := board.find(id)
			if err != nil {
				return core.MutationResult{}, err
			}
			principal := input.To
			if principal == "" {
				principal = phoneIdentity
			}
			board.tasks[index].Assignments = append(board.tasks[index].Assignments, core.Assignment{Principal: principal, Creator: phoneIdentity, CreatedAt: phoneStamp})
			return board.touched(index), nil
		},
		Unassign: func(_ context.Context, id string, input core.UnassignInput) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			index, err := board.find(id)
			if err != nil {
				return core.MutationResult{}, err
			}
			kept := board.tasks[index].Assignments[:0]
			for _, assignment := range board.tasks[index].Assignments {
				if assignment.Value() != input.From {
					kept = append(kept, assignment)
				}
			}
			board.tasks[index].Assignments = kept
			return board.touched(index), nil
		},
	}
}

func (board *phoneBoard) handler(t *testing.T) http.Handler {
	t.Helper()
	return NewHandler(board.options(t))
}
