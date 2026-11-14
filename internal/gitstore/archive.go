package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// archiveManifestRef names the manifest commit inside a backup bundle. It
// exists in this repository only while WriteArchiveBundle runs, because Git
// bundles refs and the manifest has to travel as one.
const archiveManifestRef = "refs/workbook/archive/manifest"

const (
	archiveManifestFormat  = "workbook.archive-manifest"
	archiveManifestVersion = 1
	archiveManifestFile    = "manifest.json"
	archiveDirectory       = "workbook-archive"
	archiveBundleExtension = ".bundle"
)

// ArchiveManifest describes one compaction run's backup bundle, so the bundle
// says what it holds without unpacking every task. It is stored as
// manifest.json in the tree of a parentless commit at archiveManifestRef.
type ArchiveManifest struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	ProjectID string    `json:"projectId"`
	RunID     string    `json:"runId"`
	CreatedAt time.Time `json:"createdAt"`
	// Flags are the compact flags as given.
	Flags []string      `json:"flags"`
	Tasks []ArchiveTask `json:"tasks"`
}

// ArchiveTask is one compacted task in a backup bundle: the tip the bundle
// holds for it under refs/workbook/tasks/<id>, and the generations either side
// of the compaction.
type ArchiveTask struct {
	TaskID        string `json:"taskId"`
	Title         string `json:"title"`
	OldTip        string `json:"oldTip"`
	OldGeneration string `json:"oldGeneration"`
	NewGeneration string `json:"newGeneration"`
	Commits       int    `json:"commits"`
}

// DefaultArchivePath is where a run's bundle goes without --backup: inside the
// common Git directory, so it is never committed and every worktree shares
// one archive, and outside .git/workbook, which holds disposable caches.
func DefaultArchivePath(commonGitDir, runID string) string {
	return filepath.Join(commonGitDir, archiveDirectory, runID+archiveBundleExtension)
}

// ResolveArchivePath turns a --backup value into the bundle path: no value is
// the default path, an existing directory gets the default file name, and
// anything else is a file path used as given.
func ResolveArchivePath(commonGitDir, runID, flagValue string) (string, error) {
	if flagValue == "" {
		return DefaultArchivePath(commonGitDir, runID), nil
	}
	info, err := os.Stat(flagValue)
	switch {
	case err == nil && info.IsDir():
		return filepath.Join(flagValue, runID+archiveBundleExtension), nil
	case err == nil, errors.Is(err, fs.ErrNotExist):
		return flagValue, nil
	default:
		return "", core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot inspect backup path %s", flagValue), err)
	}
}

// WriteArchiveBundle writes a verified Git bundle at path holding every task
// in manifest at its old tip, each task's parked reconcile refs under their
// own names, and the manifest itself at archiveManifestRef. It changes no task
// ref: the run moves refs only after this returns nil.
//
// It never overwrites a file, and it publishes the bundle by renaming a
// verified, synced temporary file beside path, so path either holds a complete
// bundle or nothing. Tasks whose refs no longer point at their OldTip are an
// *ArchiveTasksMovedError naming them all. Every failure removes the temporary file, and
// archiveManifestRef is gone from this repository whenever it returns.
func (r *Repository) WriteArchiveBundle(ctx context.Context, config core.ProjectConfig, path string, manifest ArchiveManifest) error {
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return err
	}
	manifest, err := r.canonicalArchiveManifest(ctx, config, manifest)
	if err != nil {
		return err
	}
	parked, err := r.archiveParkedRefs(ctx, config, manifest.Tasks)
	if err != nil {
		return err
	}

	// Git runs in the working-tree root, not in this process's directory, so a
	// relative path would name two different files.
	path, err = filepath.Abs(path)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	if err := refuseExistingArchive(path); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot create backup directory %s", directory), err)
	}

	manifestCommit, err := r.writeArchiveManifest(ctx, manifest)
	if err != nil {
		return err
	}
	// Cleanup must run even when the caller's context is already canceled.
	cleanupCtx := context.WithoutCancel(ctx)
	if err := r.setArchiveManifestRef(ctx, manifestCommit); err != nil {
		// A failed transaction may still have left the ref behind.
		_ = r.deleteArchiveManifestRef(cleanupCtx, manifestCommit)
		return err
	}
	manifestRefHeld := true
	defer func() {
		if manifestRefHeld {
			_ = r.deleteArchiveManifestRef(cleanupCtx, manifestCommit)
		}
	}()

	temporary := path + ".tmp"
	published := false
	defer func() {
		if !published {
			_ = os.Remove(temporary)
		}
	}()

	args := []string{"bundle", "create", temporary, archiveManifestRef}
	for _, task := range manifest.Tasks {
		args = append(args, taskRefPrefix+task.TaskID)
	}
	args = append(args, parked...)
	if _, err := r.Git(ctx, nil, args...); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot write the backup bundle", err)
	}
	if err := r.deleteArchiveManifestRef(cleanupCtx, manifestCommit); err != nil {
		return err
	}
	manifestRefHeld = false

	if _, err := r.Git(ctx, nil, "bundle", "verify", temporary); err != nil {
		return core.Wrap(core.CategoryOperational, "the backup bundle failed verification", err)
	}
	if err := r.checkArchiveHeads(ctx, temporary, manifestCommit, manifest.Tasks); err != nil {
		return err
	}
	if err := syncFile(temporary); err != nil {
		return err
	}
	// The check at the start leaves a window; a bundle is never overwritten.
	if err := refuseExistingArchive(path); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot publish the backup bundle", err)
	}
	published = true
	return syncDirectory(directory)
}

// canonicalArchiveManifest validates manifest against this repository and
// returns it as it is stored: this build's format and version, its time in
// UTC, and empty lists rather than null ones.
func (r *Repository) canonicalArchiveManifest(ctx context.Context, config core.ProjectConfig, manifest ArchiveManifest) (ArchiveManifest, error) {
	if manifest.ProjectID != config.ProjectID {
		return ArchiveManifest{}, core.Errorf(core.CategoryValidation,
			"archive manifest is for project %q, not %q", manifest.ProjectID, config.ProjectID)
	}
	if strings.TrimSpace(manifest.RunID) == "" {
		return ArchiveManifest{}, core.Errorf(core.CategoryValidation, "archive manifest needs a run ID")
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return ArchiveManifest{}, err
	}
	seen := make(map[string]struct{}, len(manifest.Tasks))
	for _, task := range manifest.Tasks {
		if err := keys.RequireOwned(task.TaskID); err != nil {
			return ArchiveManifest{}, core.Wrap(core.CategoryValidation, "archived task ID is invalid", err)
		}
		if _, duplicate := seen[task.TaskID]; duplicate {
			return ArchiveManifest{}, core.Errorf(core.CategoryValidation, "archive manifest lists task %s twice", task.TaskID)
		}
		seen[task.TaskID] = struct{}{}
		if err := r.validateFullObjectID(task.OldTip); err != nil {
			return ArchiveManifest{}, core.Wrap(core.CategoryValidation,
				fmt.Sprintf("archived task %s must name its full old tip", task.TaskID), err)
		}
	}

	manifest.Format = archiveManifestFormat
	manifest.Version = archiveManifestVersion
	manifest.CreatedAt = manifest.CreatedAt.UTC()
	manifest.Flags = append([]string{}, manifest.Flags...)
	manifest.Tasks = append([]ArchiveTask{}, manifest.Tasks...)
	return manifest, nil
}

// archiveParkedRefs lists every parked reconcile ref of the archived tasks,
// which the bundle keeps under their existing names.
func (r *Repository) archiveParkedRefs(ctx context.Context, config core.ProjectConfig, tasks []ArchiveTask) ([]string, error) {
	var names []string
	for _, task := range tasks {
		parked, err := r.listParkedRefs(ctx, config, reconciledRefPrefix+task.TaskID+"/")
		if err != nil {
			return nil, err
		}
		for _, entry := range parked {
			if !validParkedRefName(task.TaskID, entry.name) {
				return nil, core.Errorf(core.CategoryCorruptData, "parked ref %q does not belong to task %s", entry.name, task.TaskID)
			}
			names = append(names, entry.name)
		}
	}
	return names, nil
}

func refuseExistingArchive(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return core.Errorf(core.CategoryValidation, "backup bundle %s already exists; Workbook never overwrites one", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot inspect backup path %s", path), err)
	}
}

// writeArchiveManifest writes the manifest blob, a tree holding it as
// manifest.json, and a parentless commit of that tree.
func (r *Repository) writeArchiveManifest(ctx context.Context, manifest ArchiveManifest) (string, error) {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot encode the archive manifest", err)
	}
	contents = append(contents, '\n')
	blob, err := r.writeBlob(ctx, contents)
	if err != nil {
		return "", err
	}
	output, err := r.Git(ctx, []byte(fmt.Sprintf("100644 blob %s\t%s\n", blob, archiveManifestFile)), "mktree")
	if err != nil {
		return "", err
	}
	tree, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid tree object ID", err)
	}
	return r.writeCommit(ctx, tree, "", "workbook archive manifest "+manifest.RunID)
}

// setArchiveManifestRef points archiveManifestRef at commit. It replaces any
// value a run that died before its cleanup left behind; a concurrent run that
// replaces this one's value is caught by checkArchiveHeads instead.
func (r *Repository) setArchiveManifestRef(ctx context.Context, commit string) error {
	input := fmt.Sprintf("start\noption no-deref\nupdate %s %s\nprepare\ncommit\n", archiveManifestRef, commit)
	if _, err := r.Git(ctx, []byte(input), "update-ref", "--no-deref", "--stdin"); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create "+archiveManifestRef, err)
	}
	return nil
}

// deleteArchiveManifestRef removes archiveManifestRef when it still points at
// commit. A ref that is already gone, or that another run has replaced, is
// not this run's to remove.
func (r *Repository) deleteArchiveManifestRef(ctx context.Context, commit string) error {
	input := fmt.Sprintf("start\noption no-deref\ndelete %s %s\nprepare\ncommit\n", archiveManifestRef, commit)
	if _, err := r.Git(ctx, []byte(input), "update-ref", "--no-deref", "--stdin"); err != nil {
		if r.refValueDiffers(ctx, archiveManifestRef, commit) {
			return nil
		}
		return core.Wrap(core.CategoryOperational, "cannot delete "+archiveManifestRef, err)
	}
	return nil
}

// checkArchiveHeads reads the bundle's refs back and requires the manifest
// this run wrote and every task at the tip the run observed. A task that moved
// between selection and bundling would leave the bundle without the history
// the run is about to drop.
func (r *Repository) checkArchiveHeads(ctx context.Context, bundle, manifestCommit string, tasks []ArchiveTask) error {
	output, err := r.Git(ctx, nil, "bundle", "list-heads", bundle)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot list the backup bundle's refs", err)
	}
	heads := make(map[string]string)
	for _, line := range strings.Split(string(bytes.TrimRight(output, "\n")), "\n") {
		objectID, name, found := strings.Cut(line, " ")
		if !found {
			return core.Errorf(core.CategoryOperational, "Git listed an invalid bundle ref %q", line)
		}
		heads[name] = objectID
	}
	switch got, found := heads[archiveManifestRef]; {
	case !found:
		return core.Errorf(core.CategoryOperational, "the backup bundle lacks %s", archiveManifestRef)
	case got != manifestCommit:
		return core.Errorf(core.CategoryStaleWrite,
			"%s changed while the backup bundle was written; another compaction may be running", archiveManifestRef)
	}
	var moved *ArchiveTasksMovedError
	for _, task := range tasks {
		got, found := heads[taskRefPrefix+task.TaskID]
		if !found {
			return core.Errorf(core.CategoryOperational, "the backup bundle lacks task %s", task.TaskID)
		}
		if got == task.OldTip {
			continue
		}
		if moved == nil {
			moved = &ArchiveTasksMovedError{err: core.Errorf(core.CategoryStaleWrite,
				"task %s moved from %s to %s since it was selected for compaction", task.TaskID, task.OldTip, got)}
		}
		moved.TaskIDs = append(moved.TaskIDs, task.TaskID)
	}
	if moved != nil {
		return moved
	}
	return nil
}

// ArchiveTasksMovedError is WriteArchiveBundle's refusal of a bundle in which
// some tasks' refs had moved from the tips the manifest names. It is a
// CategoryStaleWrite error naming the first such task, and carries every one,
// in manifest order, so a caller can leave those tasks out and bundle the
// rest. No bundle was written.
type ArchiveTasksMovedError struct {
	TaskIDs []string
	err     error
}

func (e *ArchiveTasksMovedError) Error() string { return e.err.Error() }

func (e *ArchiveTasksMovedError) Unwrap() error { return e.err }

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot open the backup bundle for sync", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return core.Wrap(core.CategoryOperational, "cannot sync the backup bundle", err)
	}
	if err := file.Close(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot close the backup bundle", err)
	}
	return nil
}
