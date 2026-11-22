package core

import (
	"errors"
	"fmt"
)

type Category string

const (
	CategoryInvocation     Category = "invalid-invocation"
	CategoryNotInitialized Category = "not-initialized"
	CategoryNotFound       Category = "not-found"
	CategoryValidation     Category = "validation"
	// CategoryStaleWrite reports a race that the caller did not cause and
	// cannot fix by changing anything: a local ref or a validation-cache row
	// moved between observation and write. Retrying the identical command is
	// the correct response and will usually succeed.
	CategoryStaleWrite  Category = "stale-write"
	CategoryCorruptData Category = "corrupt-data"
	// CategoryConflict reports concurrent intent that Workbook refuses to
	// decide on the caller's behalf. Retrying the identical command reproduces
	// it; the caller must read the envelope's conflict list, choose a
	// resolution, and issue the ordinary command again with new input.
	CategoryConflict    Category = "conflict"
	CategoryOperational Category = "operational"
	// CategoryNewerWriter reports history a newer Workbook wrote and this build
	// is too old to fold.
	//
	// It exists to be told apart from CategoryCorruptData, which is the only
	// answer the format had before it. The two say opposite things about whose
	// problem this is: corrupt data says a stored document does not follow from
	// what Workbook wrote and somebody has to repair the repository, while this
	// says the document is exactly what a teammate's build wrote and the repair
	// is to upgrade this one. Reporting the second as the first sends people to
	// fix a repository that is not broken, and — worse — invites them to delete
	// history they cannot read.
	//
	// It is scoped like every other refusal: to the one task or the one ledger
	// whose history carries the marker. Nothing about it holds back a ref,
	// another task, or synchronization.
	CategoryNewerWriter Category = "newer-writer"
	// CategoryAssigned reports that a task is already assigned to somebody else
	// and the caller asked not to record a second assignment beside theirs.
	//
	// It has a category of its own so that an agent picking up work can tell it
	// from every other reason an assignment can fail without reading a message.
	// That is the whole point of the claim behavior: a fleet member that meets
	// this should quietly pick another task, while a validation refusal means
	// it typed something wrong and a not-found means the task is gone. Folding
	// all three into CategoryValidation would leave the one case a program has
	// to branch on indistinguishable from the two it cannot do anything about.
	//
	// Nothing was written when this is returned, which is the promise the exit
	// code carries: an agent that sees it holds no assignment on that task and
	// owes nobody a cleanup. An assignment that lands beside somebody else's —
	// because it was forced, or because a race arrived between the check and
	// the push — succeeds and is reported as a warning instead.
	CategoryAssigned Category = "assigned"
)

type Error struct {
	Category Category
	Message  string
	Cause    error
	// TasksRemote, when set, says which remote a refusal is about and what to
	// do, for a caller reading JSON rather than the message.
	TasksRemote *TasksRemoteRefusal
}

// TasksRemoteRefusal is the machine-readable half of a refusal about the
// remote a project's tasks are shared through.
type TasksRemoteRefusal struct {
	// Reason is one of the TasksRemote* reasons below.
	Reason string `json:"reason"`
	// Remote is the Git remote the refusal is about.
	Remote string `json:"remote"`
	// URL is that remote's address, when one is known.
	URL string `json:"url,omitempty"`
	// MovedTo is where the tasks went, for a remote marked as moved.
	MovedTo *TasksRemoteTarget `json:"movedTo,omitempty"`
	// Command is what to run next, when one command is the answer.
	Command string `json:"command,omitempty"`
	// Tasks names the tasks a refusal is about, when it is about some: the
	// task refs a published-after-move report says reached the old place.
	Tasks []string `json:"tasks,omitempty"`
}

// TasksRemoteTarget names a remote and its address.
type TasksRemoteTarget struct {
	Remote string `json:"remote"`
	URL    string `json:"url"`
}

const (
	// TasksRemoteMissing: the project's settings name a remote this checkout
	// has not added.
	TasksRemoteMissing = "remote-missing"
	// TasksRemoteNotRead: this checkout has never read the tasks from the
	// remote its settings name.
	TasksRemoteNotRead = "not-read"
	// TasksRemoteUnreachable: the remote could not be read just now.
	TasksRemoteUnreachable = "unreachable"
	// TasksRemoteMoved: the remote is marked as one the tasks moved away from.
	TasksRemoteMoved = "tasks-moved"
	// TasksRemoteNotMoved: the settings name a tasks remote that holds nothing
	// yet while the tasks are still on the remote they are moving from.
	TasksRemoteNotMoved = "move-not-done"
	// TasksRemotePublishedAfterMove: the remote accepted task refs from this
	// checkout and, listed again straight after, was marked as moved. The
	// edits are kept locally, and a later move-tasks carries them across.
	TasksRemotePublishedAfterMove = "published-after-move"
	// TasksRemoteSameRepository: the tasks remote is the same repository as
	// the one the tasks would move from, whatever its address says.
	TasksRemoteSameRepository = "same-repository"
	// TasksRemoteMovedForGood: the settings would share tasks through a
	// remote marked as moved, which this version cannot move them back to.
	TasksRemoteMovedForGood = "moved-for-good"
	// TasksRemoteReadOnly: --remote named a remote other than the project's
	// tasks remote for a command that publishes. Such a remote is only read.
	TasksRemoteReadOnly = "read-only-remote"
)

// TasksRemoteError builds a refusal that carries its remote detail.
func TasksRemoteError(category Category, refusal TasksRemoteRefusal, format string, args ...any) error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...), TasksRemote: &refusal}
}

// TasksRemoteRefusalOf finds the remote detail anywhere in err's chain.
func TasksRemoteRefusalOf(err error) (*TasksRemoteRefusal, bool) {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if typed, ok := current.(*Error); ok && typed.TasksRemote != nil {
			refusal := *typed.TasksRemote
			return &refusal, true
		}
	}
	return nil, false
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return e.Message + ": " + e.Cause.Error()
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func Errorf(category Category, format string, args ...any) error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...)}
}

func Wrap(category Category, message string, cause error) error {
	return &Error{Category: category, Message: message, Cause: cause}
}

func CategoryOf(err error) Category {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}

func ExitCode(err error) int {
	switch CategoryOf(err) {
	case CategoryInvocation:
		return 2
	case CategoryNotInitialized:
		return 3
	case CategoryNotFound:
		return 4
	case CategoryValidation:
		return 5
	case CategoryStaleWrite:
		return 6
	case CategoryCorruptData:
		return 7
	case CategoryConflict:
		return 8
	case CategoryNewerWriter:
		return 9
	case CategoryAssigned:
		return 10
	case CategoryOperational:
		return 1
	}

	var typed *Error
	if errors.As(err, &typed) {
		return 7
	}
	return 1
}
