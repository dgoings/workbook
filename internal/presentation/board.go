package presentation

import (
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

type TaskView struct {
	Task     core.Task
	IDPrefix string
	// ResolvedStatus is the status this task is drawn under: the live status its
	// stored one now means, or the stored one itself when the chains lead
	// nowhere.
	//
	// It is carried on the view rather than recomputed by each renderer because
	// a column and a card that answered the question separately could disagree
	// about the same task — the drift internal/presentation exists to prevent.
	// core.Task.StoredStatus already reports the other half for a task that came
	// through Service.Project; a board handed raw stored tasks resolves them
	// here and gets the same answer either way.
	ResolvedStatus core.Status
	// StatusUnresolved reports that the status resolves to nothing this project
	// defines — not that it is merely stale. A stale token is drawn in a real
	// column; only a token no rename and no removal leads out of has no column
	// to be drawn in.
	StatusUnresolved      bool
	DependenciesComplete  int
	DependenciesTotal     int
	WaitingOnDependencies bool
}

type Column struct {
	Status core.Status
	Label  string
	Tasks  []TaskView
}

// Board is the split every renderer works from. Columns holds the tasks this
// project has a status column for; UnknownTasks holds the rest, and the two
// together are always the whole set the board was handed.
//
// "The rest" is a narrower set than it used to be. It once meant any status
// outside the six this build shipped, which made a renamed status look like a
// stranded one. It now means a status that resolves to nothing — no live
// status, and no chain of renames or removals leading to one — which is the
// only case where there is genuinely no column to draw the task in.
//
// A renderer must consume both. Rendering Columns alone silently deletes tasks
// from the reader's view, and a task that is invisible reads as a task that was
// deleted rather than one that is merely unsorted. Both renderers therefore
// give UnknownTasks its own labeled region — the terminal an UNKNOWN STATUS
// section, the web board an "Unknown status" area below the columns — rather
// than folding it into a column that would misreport the status. Keeping it out
// of the columns matters as much as showing it: the region is a display, not a
// seventh status, so nothing drops into it — there is no status such a drop
// would name. Its cards do drag out on the web board, which is the recovery:
// dropping one on a column is an ordinary status change naming a status the
// project defines.
//
// This is routine rather than exotic, because a clone whose configuration this
// one has not fetched produces it on its own. internal/presentation/parity_test.go
// holds the decision and asserts it against both renderers at once.
type Board struct {
	Columns      []Column
	UnknownTasks []TaskView
}

// boardVocabulary substitutes the pre-ledger statuses for the empty vocabulary,
// the way core.Service does and for the same reason: the zero value means "this
// caller did not configure one", and a board that drew no columns at all for it
// would turn a caller that never had a vocabulary to pass into a blank page. It
// reads core.LegacyVocabulary rather than the minting default because such a
// caller may be drawing a project older than this build.
func boardVocabulary(vocabulary core.Vocabulary) core.Vocabulary {
	if vocabulary.IsZero() {
		return core.LegacyVocabulary()
	}
	return vocabulary
}

// TaskViews derives every fact both boards show about a task set, resolving
// each status through the project's own vocabulary.
//
// Dependency progress and the waiting-on-dependencies flag read the vocabulary's
// tags rather than the names `done` and `ready`, which is the same question
// `workbook next` asks: a project whose finished column is called "shipped"
// gets an honest prerequisite count, and one that tags two statuses next has
// both of them warn about unmet prerequisites.
func TaskViews(tasks []core.Task, vocabulary core.Vocabulary) []TaskView {
	vocabulary = boardVocabulary(vocabulary)
	active := make(map[string]core.Task, len(tasks))
	for _, task := range tasks {
		if !task.Deleted {
			active[task.ID] = task
		}
	}
	views := make([]TaskView, len(tasks))
	for i, task := range tasks {
		complete := 0
		for _, dependencyID := range task.Dependencies {
			dependency, ok := active[dependencyID]
			if !ok {
				continue
			}
			if resolved, _ := vocabulary.Resolve(dependency.Status); vocabulary.IsDone(resolved) {
				complete++
			}
		}
		total := len(task.Dependencies)
		resolved, live := vocabulary.Resolve(task.Status)
		views[i] = TaskView{
			Task:                  task,
			IDPrefix:              shortestUniquePrefix(i, tasks),
			ResolvedStatus:        resolved,
			StatusUnresolved:      !live,
			DependenciesComplete:  complete,
			DependenciesTotal:     total,
			WaitingOnDependencies: live && vocabulary.IsNext(resolved) && complete < total,
		}
	}
	return views
}

// NewBoard splits a task set into the project's own columns.
//
// The columns are the vocabulary's, in the vocabulary's order and under the
// vocabulary's labels — not a fixed array — so a project that renamed, reordered
// or invented a status sees it here without either renderer knowing anything
// about it. A task lands in the column its status resolves to, so a clone that
// has not yet settled a renamed token still draws the task where a reader
// expects it; only a status the chains lead out of nowhere lands in
// UnknownTasks.
func NewBoard(tasks []core.Task, vocabulary core.Vocabulary) Board {
	vocabulary = boardVocabulary(vocabulary)
	definitions := vocabulary.Definitions()
	board := Board{Columns: make([]Column, 0, len(definitions))}
	for _, definition := range definitions {
		board.Columns = append(board.Columns, Column{Status: definition.Status, Label: definition.Label})
	}

	for _, task := range TaskViews(tasks, vocabulary) {
		matched := false
		if !task.StatusUnresolved {
			for i := range board.Columns {
				if board.Columns[i].Status == task.ResolvedStatus {
					board.Columns[i].Tasks = append(board.Columns[i].Tasks, task)
					matched = true
					break
				}
			}
		}
		if !matched {
			board.UnknownTasks = append(board.UnknownTasks, task)
		}
	}
	return board
}

func shortestUniquePrefix(index int, tasks []core.Task) string {
	id := tasks[index].ID
	separatorEnd := strings.IndexByte(id, '-') + 1
	minimumEnd := min(separatorEnd+8, len(id))

	for end := minimumEnd; end <= len(id); end++ {
		prefix := id[:end]
		if isUniquePrefix(index, prefix, tasks) {
			return prefix
		}
	}
	return id
}

func isUniquePrefix(index int, prefix string, tasks []core.Task) bool {
	for otherIndex, task := range tasks {
		if otherIndex != index && strings.HasPrefix(task.ID, prefix) {
			return false
		}
	}
	return true
}

// AssignmentChip is the compact form a card shows an assignment in.
//
// A card has a dozen columns to work with, and a full assignment value —
// dylan@example.com/impl-1 — spends most of them on a domain everybody on the
// project shares. The chip keeps the two parts that differ between the people
// and the agents working on one project: the local part of the address, and the
// label that says which of that person's agents holds it.
//
// It is lossy on purpose and is never the only place an assignment is shown.
// Two people whose addresses differ only by domain produce the same chip, and
// `workbook show` prints the whole value beside the time it was recorded. A
// principal with no at sign — which the fold accepts, because a teammate's
// identity is not this clone's to judge — is chipped whole rather than guessed
// at.
func AssignmentChip(assignment core.Assignment) string {
	principal := assignment.Principal
	if local, _, found := strings.Cut(principal, "@"); found && local != "" {
		principal = local
	}
	return core.AssignmentValue(principal, assignment.Label)
}

// AssignmentChips renders a task's assignments in stored order, which is by
// principal and then label, so two boards drawn from one task agree about which
// chip comes first.
func AssignmentChips(assignments []core.Assignment) []string {
	if len(assignments) == 0 {
		return nil
	}
	chips := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		chips = append(chips, AssignmentChip(assignment))
	}
	return chips
}

// AssignedAgo says how long an assignment has stood, which is the whole of what
// this design means by staleness.
//
// Nothing expires, no clock enforces anything, and this is not a warning: an
// assignment recorded three weeks ago on a task nobody has touched since is a
// fact for people to act on between themselves, and the display is the only
// place that fact ever appears. Whole units only, because "assigned 3 days ago"
// is what a reader acts on and a decimal place would not change it.
//
// A creation time in the future reads as "just now" rather than as a negative
// age. Two clones' clocks disagreeing by a few seconds is ordinary — the
// timestamp is the recording pack's wall time, written wherever the assignment
// was made — and this is not the place to raise it.
func AssignedAgo(assignment core.Assignment, now time.Time) string {
	elapsed := now.Sub(assignment.CreatedAt)
	switch {
	case elapsed < time.Minute:
		return "assigned just now"
	case elapsed < time.Hour:
		return "assigned " + plural(int(elapsed.Minutes()), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return "assigned " + plural(int(elapsed.Hours()), "hour") + " ago"
	default:
		return "assigned " + plural(int(elapsed.Hours()/24), "day") + " ago"
	}
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(count) + " " + noun + "s"
}
