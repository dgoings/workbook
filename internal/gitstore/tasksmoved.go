package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// When a project moves its tasks to another repository, the repository they
// left is marked, so a checkout that has not picked up the new setting is
// refused with a message saying where the tasks went instead of saving edits
// to the old place.
//
// The mark rides the project's identity ref, refs/workbook/project, as one
// commit on top of the identity commit:
//
//	refs/workbook/project ──> M  "workbook: tasks moved"
//	                          ├── parent: I  (the identity commit, unchanged)
//	                          └── tree
//	                               ├── project.json  (I's document, byte for byte)
//	                               └── moved.json    (workbook.tasks-moved, version 1)
//
// Building on I makes publishing the mark an ordinary fast-forward: no
// commit is changed and nothing is force-pushed, and the identity itself is
// untouched, because project.json is the same blob. Every input is fixed, as
// it is for I, so two runs marking the same move write the same commit and
// the second is up to date.
//
// A Workbook build from before the mark existed refuses the commit outright —
// its identity reader requires a root commit with one entry — so its fetch
// stops before any task is synchronized and its publication refuses before any
// task ref is pushed. It says the identity is invalid rather than where the
// tasks went, but it saves nothing to the old place.
const (
	tasksMovedDocumentPath  = "moved.json"
	tasksMovedFormat        = "workbook.tasks-moved"
	tasksMovedVersion       = 1
	tasksMovedCommitMessage = "workbook: tasks moved"
)

// TasksMoved is the marker document: which project, and where its tasks went.
type TasksMoved struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	ProjectID string `json:"projectId"`
	// Remote is the remote name the project's settings give the new place.
	Remote string `json:"remote"`
	// URL is the new place's address, as the project's settings record it.
	URL string `json:"url"`
}

func encodeTasksMoved(moved TasksMoved) ([]byte, error) {
	if err := validateTasksMoved(moved); err != nil {
		return nil, err
	}
	contents, err := json.Marshal(moved)
	if err != nil {
		return nil, core.Wrap(core.CategoryValidation, "cannot encode the tasks-moved marker", err)
	}
	return append(contents, '\n'), nil
}

func decodeTasksMoved(contents []byte) (TasksMoved, error) {
	var moved TasksMoved
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&moved); err != nil {
		return TasksMoved{}, core.Wrap(core.CategoryCorruptData, "cannot decode the tasks-moved marker", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return TasksMoved{}, core.Errorf(core.CategoryCorruptData, "the tasks-moved marker holds more than one JSON value")
	}
	if moved.Format == tasksMovedFormat && moved.Version > tasksMovedVersion {
		return TasksMoved{}, core.Errorf(core.CategoryNewerWriter,
			"the tasks-moved marker is version %d, written by a newer Workbook; upgrade Workbook", moved.Version)
	}
	if err := validateTasksMoved(moved); err != nil {
		return TasksMoved{}, core.Wrap(core.CategoryCorruptData, "the tasks-moved marker is invalid", err)
	}
	canonical, err := encodeTasksMoved(moved)
	if err != nil || !bytes.Equal(canonical, contents) {
		return TasksMoved{}, core.Errorf(core.CategoryCorruptData, "the tasks-moved marker is not canonical")
	}
	return moved, nil
}

func validateTasksMoved(moved TasksMoved) error {
	if moved.Format != tasksMovedFormat || moved.Version != tasksMovedVersion {
		return core.Errorf(core.CategoryValidation, "unsupported tasks-moved marker %q version %d", moved.Format, moved.Version)
	}
	if err := core.ValidateProjectID(moved.ProjectID); err != nil {
		return err
	}
	if err := core.ValidateTasksRemoteName(moved.Remote); err != nil {
		return err
	}
	return core.ValidateTasksRemoteURL(moved.URL)
}

// writeTasksMovedCommit builds the marker commit on top of an identity root.
func (r *Repository) writeTasksMovedCommit(ctx context.Context, root identityRecord, moved TasksMoved) (string, error) {
	marker, err := encodeTasksMoved(moved)
	if err != nil {
		return "", err
	}
	document, err := r.writeBlob(ctx, root.Document)
	if err != nil {
		return "", err
	}
	markerBlob, err := r.writeBlob(ctx, marker)
	if err != nil {
		return "", err
	}
	var entries bytes.Buffer
	fmt.Fprintf(&entries, "100644 blob %s\t%s\n", markerBlob, tasksMovedDocumentPath)
	fmt.Fprintf(&entries, "100644 blob %s\t%s\n", document, identityDocumentPath)
	output, err := r.Git(ctx, entries.Bytes(), "mktree")
	if err != nil {
		return "", err
	}
	tree, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid tree object ID", err)
	}
	environment := []string{
		"GIT_AUTHOR_NAME=" + identityCommitName,
		"GIT_AUTHOR_EMAIL=" + identityCommitEmail,
		"GIT_AUTHOR_DATE=" + identityCommitDate,
		"GIT_COMMITTER_NAME=" + identityCommitName,
		"GIT_COMMITTER_EMAIL=" + identityCommitEmail,
		"GIT_COMMITTER_DATE=" + identityCommitDate,
	}
	output, err = r.gitWithEnv(ctx, environment, nil,
		"-c", "commit.gpgsign=false", "commit-tree", tree, "-p", root.Head, "-m", tasksMovedCommitMessage)
	if err != nil {
		return "", err
	}
	commit, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid commit object ID", err)
	}
	return commit, nil
}

// readTasksMovedObjects completes the read of a marker commit: its marker
// document, and its parent, which has to be the identity root carrying the
// same project document.
func (r *Repository) readTasksMovedObjects(ctx context.Context, ref, head, documentBlob, markerBlob string) (TasksMoved, error) {
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := fmt.Fprintf(writer, "%s\n%s^1\n%s^1:%s\n", markerBlob, head, head, identityDocumentPath)
		return err
	})
	if err != nil {
		return TasksMoved{}, err
	}
	defer batch.Close()
	var objects [3]batchObject
	for i := range objects {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return TasksMoved{}, batch.ReadFailure("cannot read the tasks-moved marker from Git batch", err)
		}
		objects[i] = object
	}
	marker, parent, parentDocument := objects[0], objects[1], objects[2]
	for _, object := range objects {
		if object.refused != nil {
			return TasksMoved{}, object.refused
		}
		if object.missing {
			return TasksMoved{}, core.Errorf(core.CategoryCorruptData,
				"%s carries a tasks-moved marker whose objects are missing", ref)
		}
	}
	if marker.kind != "blob" || parent.kind != "commit" || parentDocument.kind != "blob" {
		return TasksMoved{}, core.Errorf(core.CategoryCorruptData, "%s carries a malformed tasks-moved marker", ref)
	}
	parents, err := commitParentCount(parent.contents, "the Workbook project identity commit")
	if err != nil {
		return TasksMoved{}, err
	}
	if parents != 0 || parentDocument.objectID != documentBlob {
		return TasksMoved{}, core.Errorf(core.CategoryCorruptData,
			"%s carries a tasks-moved marker that is not built on the project's identity commit", ref)
	}
	if err := batch.Finish(); err != nil {
		return TasksMoved{}, err
	}
	return decodeTasksMoved(marker.contents)
}

// trackedMoveMarker reports the marker this checkout last saw on remote's
// identity ref, whose tracking ref names head, if any. It runs no Git process
// in the ordinary case, where the tracking ref and the local identity ref name
// the same commit.
func (r *Repository) trackedMoveMarker(ctx context.Context, remote, head string) (TasksMoved, bool, error) {
	r.metadataMu.RLock()
	loaded, localHead := r.identityLoaded, r.identityHead
	r.metadataMu.RUnlock()
	if loaded && head == localHead {
		return TasksMoved{}, false, nil
	}
	record, err := r.identityRecordAt(ctx, remoteIdentityRefFor(remote), head)
	if err != nil {
		return TasksMoved{}, false, err
	}
	if record.Moved == nil {
		return TasksMoved{}, false, nil
	}
	return *record.Moved, true, nil
}

// tasksMovedRefusalHere is tasksMovedRefusal for this handle's remote, with the
// advice a tasks-only folder needs in place of a pull.
func (r *Repository) tasksMovedRefusalHere(ctx context.Context, moved TasksMoved) error {
	tasksOnly, err := r.TasksOnly(ctx)
	if err == nil && tasksOnly {
		return core.TasksRemoteError(core.CategoryNotInitialized,
			core.TasksRemoteRefusal{
				Reason:  core.TasksRemoteMoved,
				Remote:  r.remote(),
				MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
				Command: "workbook setup --tasks-only " + moved.URL,
			},
			"this project's tasks moved from this folder's origin to %s, so nothing is read from or saved to origin any more; "+
				"run workbook setup --tasks-only %s in a new, empty folder and serve the board from there",
			moved.URL, moved.URL)
	}
	return tasksMovedRefusal(r.remote(), moved)
}

// tasksMovedRefusal is what every command says in a checkout whose tasks
// remote is marked as moved: where the tasks went, and what to do.
func tasksMovedRefusal(remote string, moved TasksMoved) error {
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMoved,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
			Command: "workbook setup",
		},
		"this project's tasks moved from %s to %s (remote %q), so nothing is read from or saved to %s any more; "+
			"pull the commit that sets tasks-remote in %s, then run workbook setup; "+
			"edits this checkout has not published yet are kept and go to the new place then",
		remote, moved.URL, moved.Remote, remote, configPath)
}

// movedRemotePublication is the refusal for publishing to a remote marked as
// moved, which a command reaches only by naming that remote with --remote.
func movedRemotePublication(remote string, moved TasksMoved) error {
	return core.TasksRemoteError(core.CategoryValidation,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMoved,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
		},
		"%s is marked as the place this project's tasks moved away from, to %s (remote %q); "+
			"it can still be read, but nothing is published to it", remote, moved.URL, moved.Remote)
}
