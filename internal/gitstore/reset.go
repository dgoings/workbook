package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// A reset removes Workbook's data from one checkout or from one remote, so
// that `workbook setup` and `workbook sync` start again from nothing. It is
// the only thing Workbook does that deletes a ref another person may hold, and
// so the second deliberate exception, after compaction, to shared history
// being append-only. It is run by hand, never implied, and touches nothing
// outside refs/workbook/.
//
// A checkout's reset deletes every local ref under refs/workbook/ and the
// caches beside them. A remote's reset deletes that remote's refs under
// refs/workbook/, except, at a place the tasks moved away from, the identity
// ref carrying its marker, which is what keeps a checkout on old settings
// from publishing the tasks back there unseen:
//
//	place left, before:   refs/workbook/project ──> M (marker) ── I (identity)
//	                      refs/workbook/config, refs/workbook/tasks/*
//	place left, after:    refs/workbook/project ──> M (marker) ── I (identity)
//
// Both write a Git bundle of every ref they are about to delete first, with
// the same manifest, default location and flags compaction uses, so what was
// removed can be fetched back. Two kinds of ref are left out of it: a damaged
// one, unless it is a relic ref or a parked relic tip that resolves to a
// commit, and the manifest
// ref an interrupted backup left; and --no-backup, where it is allowed,
// writes no bundle.
const workbookRefRoot = "refs/workbook/"

// workbookDirectory is the tracked folder of project files in a worktree.
const workbookDirectory = ".workbook"

// The reasons a reset refuses, in the reset member of a JSON error.
const (
	// ResetUnpublished: --no-backup was given and the checkout holds work
	// its tasks remote does not, without --discard-unpublished.
	ResetUnpublished = "unpublished"
	// ResetConfirm: the remote holds the project's tasks, not a marked copy,
	// and --confirm did not name it.
	ResetConfirm = "confirm"
	// ResetBackupRequired: --no-backup was given for a remote that holds the
	// project's tasks.
	ResetBackupRequired = "backup-required"
	// ResetDeletionRefused: the remote refused to delete some refs.
	ResetDeletionRefused = "deletion-refused"
	// ResetWatcherRunning: this checkout's watcher could not be stopped.
	ResetWatcherRunning = "watcher-running"
	// ResetUnchecked: --no-backup was given and the tasks remote could not
	// be checked for what this checkout alone holds.
	ResetUnchecked = "unchecked"
)

// ResetRef is one ref a reset removes.
type ResetRef struct {
	Ref      string `json:"ref"`
	ObjectID string `json:"objectId"`
	// Damaged says what is wrong with the ref: it is symbolic, its object is
	// missing, it is not a commit, or, in a namespace that holds only
	// Workbook commits, it names some other commit. It is removed all the
	// same, and the result names it. A damaged ref is left out of the bundle
	// unless Bundleable is set.
	Damaged string `json:"damaged,omitempty"`
	// Bundleable marks a damaged relic ref or parked relic tip that resolves
	// to a commit. Git can bundle a commit whatever its tree holds, with all
	// the history under it, and sync never publishes a relic it cannot read,
	// so such a ref is kept like an intact one: it is in the bundle, and it
	// counts as work that exists only here unless the tasks remote holds the
	// same commit. Only a relic ref whose object is missing or is not a
	// commit is removed with nothing kept.
	Bundleable bool `json:"bundleable,omitempty"`
}

// kept reports a ref a reset keeps in its bundle and measures for work that
// exists only here: one with nothing wrong with it, or a damaged relic ref or
// parked relic tip that resolves to a commit.
func (ref ResetRef) kept() bool { return ref.Damaged == "" || ref.Bundleable }

// UnpublishedRef is local work the tasks remote did not hold when this
// checkout last synchronized with it: commits on a task ref, a relic ref, or
// the configuration ledger, that none of the remote's tracking refs reach.
// A relic is measured as a task is: sync publishes it, and New says it never
// was.
type UnpublishedRef struct {
	Ref     string `json:"ref"`
	TaskID  string `json:"taskId,omitempty"`
	RelicID string `json:"relicId,omitempty"`
	Title   string `json:"title,omitempty"`
	// New says the tasks remote held no copy of this ref at all.
	New        bool     `json:"new"`
	Commits    int      `json:"commits"`
	Operations []string `json:"operations"`
}

// ResetRefusal is the machine-readable half of a reset's refusal.
type ResetRefusal struct {
	Reason  string `json:"reason"`
	Remote  string `json:"remote,omitempty"`
	Command string `json:"command,omitempty"`
	// Tasks counts the task refs the remote holds, for ResetConfirm.
	Tasks       int              `json:"tasks,omitempty"`
	Unpublished []UnpublishedRef `json:"unpublished,omitempty"`
	SetAside    []string         `json:"setAside,omitempty"`
	// Remaining names the refs the remote still holds, for
	// ResetDeletionRefused.
	Remaining []string `json:"remaining,omitempty"`
	// Bundle is the backup written before the refusal, if any.
	Bundle string `json:"bundle,omitempty"`
}

// ResetRefusalError is a reset's refusal: a core error, for the category and
// message, carrying the refusal.
type ResetRefusalError struct {
	Refusal ResetRefusal
	err     error
}

func (e *ResetRefusalError) Error() string { return e.err.Error() }

func (e *ResetRefusalError) Unwrap() error { return e.err }

// NewResetRefusal builds a reset refusal of category carrying refusal.
func NewResetRefusal(category core.Category, refusal ResetRefusal, format string, args ...any) error {
	return &ResetRefusalError{Refusal: refusal, err: core.Errorf(category, format, args...)}
}

// ResetOptions are a reset's choices.
type ResetOptions struct {
	// NoBackup writes no bundle; Backup names where to write it, as for
	// compact: "" for the default path, a directory, or a file.
	NoBackup bool
	Backup   string
	// Flags are the flags as given, recorded in the bundle's manifest.
	Flags []string
	// RunID names the bundle and Now dates it; a fresh ULID and the clock
	// by default.
	RunID string
	Now   func() time.Time
}

// PrepareResetBackup fixes the run's ID and the bundle's path before a reset
// does anything, and refuses a path the reset could not keep: one inside a
// folder it removes files from (CheckResetBackup), or a file that already
// exists, which a bundle never overwrites. The options it returns name the
// bundle file itself.
func (r *Repository) PrepareResetBackup(options ResetOptions) (ResetOptions, error) {
	options, err := options.withDefaults()
	if err != nil || options.NoBackup {
		return options, err
	}
	if err := r.CheckResetBackup(options.Backup); err != nil {
		return options, err
	}
	path, err := ResolveArchivePath(r.CommonGitDir, options.RunID, options.Backup)
	if err != nil {
		return options, err
	}
	if path, err = filepath.Abs(path); err != nil {
		return options, core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	if err := refuseExistingArchive(path); err != nil {
		return options, err
	}
	options.Backup = path
	return options, nil
}

func (options ResetOptions) withDefaults() (ResetOptions, error) {
	if options.RunID == "" {
		id, err := core.CryptoULIDSource{}.New()
		if err != nil {
			return options, err
		}
		options.RunID = id
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return options, nil
}

// ResetSettings reads what a reset needs from the tracked settings without
// requiring the rest of the project to be readable: the project ID, for the
// bundle's manifest, and the tasks remote the settings name. A checkout whose
// settings cannot be read is reset all the same, compared with origin.
func (r *Repository) ResetSettings() (projectID, remote string) {
	config, exists, err := r.readConfig()
	if err != nil || !exists {
		return "", core.DefaultTasksRemote
	}
	return config.ProjectID, config.TasksRemote.RemoteName()
}

// LocalResetPlan is what a reset of this checkout would remove.
type LocalResetPlan struct {
	// Remote is the tasks remote unpublished work is measured against.
	Remote string
	// Refs is every ref under refs/workbook/, by name.
	Refs []ResetRef
	// Unpublished and SetAside are the work that exists only here: commits
	// the tasks remote did not hold when last synchronized, and the parked
	// refs holding edits a reconciliation set aside.
	Unpublished []UnpublishedRef
	SetAside    []string
	// Unchecked is why the remote could not be read just now, when it could
	// not: Unpublished is then measured by this checkout's tracking refs for
	// it, which can be wrong either way. They name work published since the
	// last fetch as unpublished, and when the remote lost refs since then,
	// they hide work that exists only here. A reset without a bundle is
	// refused while it is set.
	Unchecked string
	// Caches is every file and folder the reset removes besides refs: each
	// entry of the Git directory's workbook/ folder, and each temporary file
	// Workbook's own writes leave under .workbook/. Left counts the other
	// untracked files under .workbook/, which are not Workbook's and stay.
	Caches []string
	Left   int
}

// Holds reports whether the plan removes work that exists only here.
func (plan LocalResetPlan) Holds() bool {
	return len(plan.Unpublished) > 0 || len(plan.SetAside) > 0
}

// PlanLocalReset lists what a reset of this checkout would remove. It reads
// refs and objects only, so it works on a checkout whose Workbook state is
// too damaged for any other command to open.
func (r *Repository) PlanLocalReset(ctx context.Context, remote string) (LocalResetPlan, error) {
	if remote == "" {
		remote = core.DefaultTasksRemote
	}
	plan := LocalResetPlan{Remote: remote}
	refs, err := r.listLocalWorkbookRefs(ctx)
	if err != nil {
		return LocalResetPlan{}, err
	}
	plan.Refs = refs
	for _, ref := range refs {
		if ref.kept() && setAsideNamespace(ref.Ref) {
			plan.SetAside = append(plan.SetAside, ref.Ref)
		}
	}
	// A push does not move the tracking refs; only a fetch does. So what the
	// remote holds is read now, without moving any ref, and only a remote
	// that cannot be read is measured by what this checkout last saw of it.
	heads, err := r.remoteWorkbookHeads(ctx, remote)
	if err != nil {
		plan.Unchecked = err.Error()
		var fetchErr *remoteHistoryError
		if errors.As(err, &fetchErr) {
			// The remote answered; what failed was this checkout taking its
			// history in, which a damaged local ref is the usual cause of.
			var damaged []string
			for _, ref := range refs {
				if ref.Damaged != "" {
					damaged = append(damaged, ref.Ref)
				}
			}
			plan.Unchecked = remote + " was read, but its history could not be fetched into this checkout to compare"
			if len(damaged) > 0 {
				plan.Unchecked += ", which holds damaged refs (" + strings.Join(damaged, ", ") + ")"
			}
			plan.Unchecked += ": " + fetchErr.err.Error()
		}
		heads = map[string]string{}
		tracking := remoteTrackingPrefix(remote)
		for _, ref := range refs {
			if ref.Damaged == "" && strings.HasPrefix(ref.Ref, tracking) {
				heads[workbookRefRoot+strings.TrimPrefix(ref.Ref, tracking)] = ref.ObjectID
			}
		}
	}
	plan.Unpublished, err = r.unpublishedWork(ctx, refs, heads)
	if err != nil {
		return LocalResetPlan{}, err
	}
	if plan.Caches, plan.Left, err = r.localResetCaches(ctx); err != nil {
		return LocalResetPlan{}, err
	}
	return plan, nil
}

// setAsideNamespace reports a ref that holds edits a reconciliation set
// aside: a parked task tip, a parked configuration ledger, or a parked relic
// tip. Nothing publishes them, so each is work that exists only here.
func setAsideNamespace(ref string) bool {
	return strings.HasPrefix(ref, reconciledRefPrefix) || strings.HasPrefix(ref, parkedConfigRefPrefix) ||
		strings.HasPrefix(ref, relicParkedRefPrefix)
}

// remoteWorkbookHeads is what remote holds under refs/workbook/ now, by
// name, with the objects fetched into this checkout so its history can be
// compared, and no ref of this checkout moved.
func (r *Repository) remoteWorkbookHeads(ctx context.Context, remote string) (map[string]string, error) {
	if !r.hasGitRemote(ctx, remote) {
		return nil, core.Errorf(core.CategoryValidation, "this checkout has no Git remote named %s", remote)
	}
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", remote, workbookRefRoot+"*")
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, remote+" could not be read", err)
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
		objectID, name, found := strings.Cut(line, "\t")
		if found && strings.HasPrefix(name, workbookRefRoot) {
			heads[name] = objectID
		}
	}
	if len(heads) == 0 {
		return heads, nil
	}
	// Each name, with no destination, fetches its history and stores
	// nothing; on standard input, so the list is not limited by the length
	// of a command line.
	var names bytes.Buffer
	for name := range heads {
		names.WriteString(name + "\n")
	}
	if _, err := r.Git(ctx, names.Bytes(), "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance",
		"--stdin", remote); err != nil {
		return nil, &remoteHistoryError{err: err}
	}
	return heads, nil
}

// remoteHistoryError is a remote that listed its refs and whose history this
// checkout then failed to fetch: the remote is readable, and the failure is
// this checkout's.
type remoteHistoryError struct{ err error }

func (e *remoteHistoryError) Error() string { return "fetching its history failed: " + e.err.Error() }

func (e *remoteHistoryError) Unwrap() error { return e.err }

// LocalResetResult is what a reset of this checkout did.
type LocalResetResult struct {
	Removed []ResetRef
	// Bundle is the backup's absolute path, or "" when none was written.
	Bundle string
	// Caches names each cache file or directory removed, and Left counts the
	// untracked files under .workbook/ that are not Workbook's and stayed.
	Caches []string
	Left   int
}

// ResetLocal removes every ref plan names and this checkout's caches,
// writing the bundle first unless options.NoBackup. The refs go in one
// transaction, each only while it still names the object plan saw, so a ref
// another command moved since is left for the next run to report. It is safe
// to interrupt and run again: a run that stopped before the transaction
// changed no ref, and one that stopped after it finds only caches left.
func (r *Repository) ResetLocal(ctx context.Context, projectID string, plan LocalResetPlan, options ResetOptions) (LocalResetResult, error) {
	options, err := options.withDefaults()
	if err != nil {
		return LocalResetResult{}, err
	}
	var result LocalResetResult
	if !options.NoBackup {
		if err := r.CheckResetBackup(options.Backup); err != nil {
			return result, err
		}
	}
	if err := r.resetStep("start"); err != nil {
		return result, err
	}
	if len(plan.Refs) > 0 && !options.NoBackup {
		path, err := r.writeResetBundle(ctx, projectID, plan.Refs, "", options)
		if err != nil {
			return result, err
		}
		result.Bundle = path
	}
	if err := r.resetStep("bundle"); err != nil {
		return result, err
	}
	if err := r.deleteLocalRefs(ctx, plan.Refs); err != nil {
		return result, err
	}
	result.Removed = plan.Refs
	if err := r.resetStep("refs"); err != nil {
		return result, err
	}
	caches, left, err := r.removeCaches(ctx)
	result.Caches, result.Left = caches, left
	if err != nil {
		return result, err
	}
	return result, requireBundle(result.Bundle)
}

// requireBundle fails loudly when the bundle a reset wrote is no longer
// where it was written, so a reset never reports a backup that is not there.
func requireBundle(bundle string) error {
	if bundle == "" {
		return nil
	}
	if info, err := os.Stat(bundle); err != nil || !info.Mode().IsRegular() {
		return core.Errorf(core.CategoryOperational,
			"the backup bundle written at %s is not there any more, so the refs removed are not kept in it; "+
				"restore them from another copy of the project, such as a clone or the remote", bundle)
	}
	return nil
}

// CheckResetBackup refuses a --backup value that would put the bundle inside
// a folder a reset of this checkout removes files from: the Git directory's
// workbook/ folder and this worktree's .workbook/. The bundle's own path is
// resolved as the write would resolve it, symbolic links included, and ""
// is the default location, which is outside both. The refusal changes
// nothing, so a reset checks before it does anything else.
func (r *Repository) CheckResetBackup(backup string) error {
	if backup == "" {
		return nil
	}
	path, err := ResolveArchivePath(r.CommonGitDir, "backup", backup)
	if err != nil {
		return err
	}
	if path, err = filepath.Abs(path); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	for _, folder := range r.resetFolders() {
		if pathInside(path, folder) {
			return core.Errorf(core.CategoryInvocation,
				"--backup %s puts the bundle inside %s, which workbook reset removes files from; write it somewhere else", backup, folder)
		}
	}
	return nil
}

// resetFolders is every folder a reset of this checkout removes files from.
func (r *Repository) resetFolders() []string {
	folders := []string{filepath.Join(r.CommonGitDir, "workbook")}
	if r.Root != "" {
		folders = append(folders, filepath.Join(r.Root, workbookDirectory))
	}
	return folders
}

// pathInside reports whether path is folder or lies anywhere under it, once
// both are absolute with every symbolic link resolved, or when one of path's
// folders is folder itself by another spelling, as on a file system that
// ignores case.
func pathInside(path, folder string) bool {
	resolved, resolvedFolder := resolveExisting(path), resolveExisting(folder)
	if resolved == resolvedFolder || strings.HasPrefix(resolved, resolvedFolder+string(filepath.Separator)) {
		return true
	}
	target, err := os.Stat(resolvedFolder)
	if err != nil {
		return false
	}
	for directory := resolved; ; directory = filepath.Dir(directory) {
		if info, err := os.Stat(directory); err == nil && os.SameFile(info, target) {
			return true
		}
		if parent := filepath.Dir(directory); parent == directory {
			return false
		}
	}
}

// resolveExisting is path, absolute, with the symbolic links in the part of
// it that exists resolved and the rest appended as written.
func resolveExisting(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for current := path; ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for index := len(rest) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, rest[index])
			}
			return resolved
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		rest = append(rest, filepath.Base(current))
	}
}

// resetStep runs the test hook after a reset's step.
func (r *Repository) resetStep(step string) error {
	if r.resetStepHook == nil {
		return nil
	}
	return r.resetStepHook(step)
}

// RestoreCommand is the Git command that brings a checkout's reset bundle
// back: every task ref, parked ref, relic ref, parked relic tip and the
// configuration ledger
// it holds, under their own names, never over a ref that already exists with
// other history.
func (r *Repository) RestoreCommand(bundle string, refs []ResetRef) string {
	namespaces := []struct{ prefix, refspec string }{
		{taskRefPrefix, taskRefPrefix + "*:" + taskRefPrefix + "*"},
		{reconciledRefPrefix, reconciledRefPrefix + "*:" + reconciledRefPrefix + "*"},
		{parkedConfigRefPrefix, parkedConfigRefPrefix + "*:" + parkedConfigRefPrefix + "*"},
		{relicRefPrefix, relicRefPrefix + "*:" + relicRefPrefix + "*"},
		{relicParkedRefPrefix, relicParkedRefPrefix + "*:" + relicParkedRefPrefix + "*"},
	}
	held := map[string]bool{}
	for _, ref := range refs {
		if !ref.kept() {
			continue
		}
		for _, namespace := range namespaces {
			if strings.HasPrefix(ref.Ref, namespace.prefix) {
				held[namespace.refspec] = true
			}
		}
		if ref.Ref == configRef {
			held[configRef+":"+configRef] = true
		}
	}
	command := "git fetch " + posixQuote(bundle)
	for _, namespace := range namespaces {
		if held[namespace.refspec] {
			command += " " + posixQuote(namespace.refspec)
		}
	}
	if held[configRef+":"+configRef] {
		command += " " + configRef + ":" + configRef
	}
	return command
}

// RemoteRestoreCommand is the Git command, run in a new empty repository,
// that gives it every ref a remote's reset bundle holds, under the names the
// remote held them by.
func RemoteRestoreCommand(bundle, remote string) string {
	tracking := remoteTrackingPrefix(remote)
	return "git fetch " + posixQuote(bundle) + " " + posixQuote(tracking+"*:"+workbookRefRoot+"*")
}

// posixQuote quotes value for a POSIX shell when it needs it.
func posixQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_./:@+=", c))
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// listLocalWorkbookRefs is every local ref under refs/workbook/, by name,
// with what is wrong with any that cannot be bundled.
func (r *Repository) listLocalWorkbookRefs(ctx context.Context) ([]ResetRef, error) {
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", workbookRefRoot)
	if err != nil {
		return nil, err
	}
	var refs []ResetRef
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 || !strings.HasPrefix(fields[0], workbookRefRoot) {
			return nil, core.Errorf(core.CategoryOperational, "Git listed an invalid ref %q", line)
		}
		ref := ResetRef{Ref: fields[0], ObjectID: fields[1]}
		if fields[2] != "" {
			ref.Damaged = "it is a symbolic ref to " + fields[2]
		}
		refs = append(refs, ref)
	}
	return refs, r.classifyResetRefs(ctx, refs)
}

// trackedWorkbookRef is the name a ref has at the place that holds it: a
// tracking ref's name there, and any other ref's own name.
func trackedWorkbookRef(ref string) string {
	if strings.HasPrefix(ref, remoteTrackingRoot) {
		rest := strings.TrimPrefix(ref, remoteTrackingRoot)
		if _, tail, found := strings.Cut(rest, "/"); found {
			return workbookRefRoot + tail
		}
	}
	return ref
}

// workbookCommitNamespace reports a namespace that holds only Workbook
// operation commits, each with an operation.json at the top of its tree.
func workbookCommitNamespace(ref string) bool {
	ref = trackedWorkbookRef(ref)
	return strings.HasPrefix(ref, taskRefPrefix) || ref == configRef ||
		strings.HasPrefix(ref, reconciledRefPrefix) || strings.HasPrefix(ref, parkedConfigRefPrefix) ||
		strings.HasPrefix(ref, relicRefPrefix) || strings.HasPrefix(ref, relicParkedRefPrefix)
}

// classifyResetRefs marks each ref whose object is missing, is not a commit,
// or, in a namespace of operation commits, has no operation.json, with one
// Git process for all of them. A damaged relic ref or parked relic tip that
// resolves to a commit all the same is marked as one the bundle can hold.
func (r *Repository) classifyResetRefs(ctx context.Context, refs []ResetRef) error {
	if len(refs) == 0 {
		return nil
	}
	var requests bytes.Buffer
	for _, ref := range refs {
		fmt.Fprintf(&requests, "%s\n", ref.ObjectID)
		fmt.Fprintf(&requests, "%s:operation.json\n", ref.ObjectID)
	}
	output, err := r.Git(ctx, requests.Bytes(), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) != 2*len(refs) {
		return core.Errorf(core.CategoryOperational, "Git returned %d object checks, want %d", len(lines), 2*len(refs))
	}
	for index := range refs {
		object := strings.Fields(lines[2*index])
		operation := strings.Fields(lines[2*index+1])
		switch {
		case refs[index].Damaged != "":
			// A symbolic ref, found when the refs were listed.
		case len(object) == 2 && object[1] == "missing":
			refs[index].Damaged = "its object is missing"
		case len(object) != 2:
			refs[index].Damaged = "its object cannot be read"
		case object[1] != "commit":
			refs[index].Damaged = "it names a " + object[1] + ", not a commit"
		case workbookCommitNamespace(refs[index].Ref) && (len(operation) != 2 || operation[1] != "blob"):
			refs[index].Damaged = "it names a commit that is not a Workbook operation commit"
		}
		// Whatever is wrong with a relic ref or a parked relic tip, a commit
		// under it is one Git can bundle, and sync publishes no relic it
		// cannot read.
		tracked := trackedWorkbookRef(refs[index].Ref)
		refs[index].Bundleable = refs[index].Damaged != "" &&
			(strings.HasPrefix(tracked, relicRefPrefix) || strings.HasPrefix(tracked, relicParkedRefPrefix)) &&
			len(object) == 2 && object[1] == "commit"
	}
	return nil
}

// unpublishedWork is every local task ref, relic ref and the configuration
// ledger holding commits that none of remoteHeads reaches, with the
// operations those commits record. A damaged ref is left out, except a relic
// ref that resolves to a commit, which is measured like an intact one: it
// stops counting only once the remote's heads reach every commit on it.
// remoteHeads is what the remote holds now, or, when it could
// not be read, what this checkout's tracking refs say it held at the last
// fetch. Those can be wrong both ways: they name work published since as
// unpublished, and when the remote lost refs since, as a reset of it makes
// it, they reach commits the remote no longer holds and so hide work that
// exists only here.
func (r *Repository) unpublishedWork(ctx context.Context, refs []ResetRef, remoteHeads map[string]string) ([]UnpublishedRef, error) {
	var tips []string
	var candidates []ResetRef
	for _, ref := range refs {
		if ref.kept() && (strings.HasPrefix(ref.Ref, taskRefPrefix) || strings.HasPrefix(ref.Ref, relicRefPrefix) || ref.Ref == configRef) {
			candidates = append(candidates, ref)
			tips = append(tips, ref.ObjectID)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	// A remote head this checkout cannot read, such as one that moved after
	// it was listed, stops nothing: the work it might hold is reported.
	var stops []string
	if len(remoteHeads) > 0 {
		var requests bytes.Buffer
		var names []string
		for name, objectID := range remoteHeads {
			if workbookCommitNamespace(name) {
				names = append(names, name)
				fmt.Fprintf(&requests, "%s\n", objectID)
			}
		}
		output, err := r.Git(ctx, requests.Bytes(), "cat-file", "--batch-check=%(objecttype)")
		if err != nil {
			return nil, err
		}
		types := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
		for index, name := range names {
			if index < len(types) && types[index] == "commit" {
				stops = append(stops, "^"+remoteHeads[name])
			}
		}
	}
	input := strings.Join(append(tips, stops...), "\n") + "\n"
	output, err := r.Git(ctx, []byte(input), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, err
	}
	parents := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			parents[fields[0]] = fields[1:]
		}
	}
	var unpublished []UnpublishedRef
	var commitsByRef [][]string
	for _, ref := range candidates {
		if _, found := parents[ref.ObjectID]; !found {
			continue
		}
		var commits []string
		seen := map[string]bool{}
		queue := []string{ref.ObjectID}
		for len(queue) > 0 {
			commit := queue[0]
			queue = queue[1:]
			if seen[commit] {
				continue
			}
			seen[commit] = true
			commits = append(commits, commit)
			for _, parent := range parents[commit] {
				if _, listed := parents[parent]; listed && !seen[parent] {
					queue = append(queue, parent)
				}
			}
		}
		_, held := remoteHeads[ref.Ref]
		item := UnpublishedRef{Ref: ref.Ref, New: !held, Commits: len(commits), Operations: []string{}}
		if strings.HasPrefix(ref.Ref, taskRefPrefix) {
			item.TaskID = strings.TrimPrefix(ref.Ref, taskRefPrefix)
		}
		if strings.HasPrefix(ref.Ref, relicRefPrefix) {
			item.RelicID = strings.TrimPrefix(ref.Ref, relicRefPrefix)
		}
		unpublished = append(unpublished, item)
		commitsByRef = append(commitsByRef, commits)
	}
	if len(unpublished) == 0 {
		return nil, nil
	}
	if err := r.describeUnpublished(ctx, unpublished, candidates, commitsByRef); err != nil {
		return nil, err
	}
	return unpublished, nil
}

// describeUnpublished fills in each unpublished ref's title and operation
// IDs from its commits' documents, with one Git process. A document that
// cannot be read leaves its operations out; the commit is still counted.
func (r *Repository) describeUnpublished(ctx context.Context, unpublished []UnpublishedRef, candidates []ResetRef, commitsByRef [][]string) error {
	tips := map[string]string{}
	for _, ref := range candidates {
		tips[ref.Ref] = ref.ObjectID
	}
	var requests bytes.Buffer
	for index, item := range unpublished {
		fmt.Fprintf(&requests, "%s:state.json\n", tips[item.Ref])
		for _, commit := range commitsByRef[index] {
			fmt.Fprintf(&requests, "%s:operation.json\n", commit)
		}
	}
	output, err := r.Git(ctx, requests.Bytes(), "cat-file", "--batch")
	if err != nil {
		return err
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	next := func() ([]byte, bool) {
		object, err := readBatchObject(reader)
		if err != nil || object.missing || object.refused != nil {
			return nil, false
		}
		return object.contents, true
	}
	for index := range unpublished {
		if contents, ok := next(); ok && (unpublished[index].TaskID != "" || unpublished[index].RelicID != "") {
			// A task's state names its title under task, a relic's under
			// relic.
			var state struct {
				Task struct {
					Title string `json:"title"`
				} `json:"task"`
				Relic struct {
					Title string `json:"title"`
				} `json:"relic"`
			}
			if json.Unmarshal(contents, &state) == nil {
				if unpublished[index].TaskID != "" {
					unpublished[index].Title = state.Task.Title
				} else {
					unpublished[index].Title = state.Relic.Title
				}
			}
		}
		for range commitsByRef[index] {
			contents, ok := next()
			if !ok {
				continue
			}
			var pack struct {
				Operations []struct {
					ID string `json:"id"`
				} `json:"operations"`
			}
			if json.Unmarshal(contents, &pack) != nil {
				continue
			}
			for _, operation := range pack.Operations {
				if operation.ID != "" {
					unpublished[index].Operations = append(unpublished[index].Operations, operation.ID)
				}
			}
		}
		sort.Strings(unpublished[index].Operations)
	}
	return nil
}

// writeResetBundle writes the bundle of every bundleable ref in refs under
// its own name, with a manifest listing each task ref among them. remote
// names the remote whose tracking refs these are, for a remote's reset, and
// is "" for a checkout's.
func (r *Repository) writeResetBundle(ctx context.Context, projectID string, refs []ResetRef, remote string, options ResetOptions) (string, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return "", err
	}
	taskPrefix := taskRefPrefix
	if remote != "" {
		taskPrefix = remoteTaskRefPrefixFor(remote)
	}
	flags := append([]string{"reset"}, options.Flags...)
	manifest := ArchiveManifest{
		ProjectID: projectID,
		RunID:     options.RunID,
		CreatedAt: options.Now(),
		Flags:     flags,
	}
	var bundled []bundledRef
	var taskRefs []ResetRef
	for _, ref := range refs {
		// A manifest ref an interrupted backup left is not data: the bundle
		// carries its own manifest under that name.
		if !ref.kept() || ref.Ref == archiveManifestRef {
			continue
		}
		bundled = append(bundled, bundledRef{name: ref.Ref, expected: ref.ObjectID})
		if strings.HasPrefix(ref.Ref, taskPrefix) {
			taskRefs = append(taskRefs, ref)
		}
	}
	if len(bundled) == 0 {
		return "", nil
	}
	tasks, err := r.resetManifestTasks(ctx, taskPrefix, taskRefs)
	if err != nil {
		return "", err
	}
	manifest.Tasks = tasks
	path, err := ResolveArchivePath(r.CommonGitDir, options.RunID, options.Backup)
	if err != nil {
		return "", err
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	if err := r.writeRefsBundle(ctx, path, normalizedArchiveManifest(manifest), bundled); err != nil {
		return "", err
	}
	return path, nil
}

// resetManifestTasks describes each task ref for the bundle's manifest: its
// title and generation from its tip's state.json, best effort, and how many
// commits its history holds, with two Git processes for all of them.
func (r *Repository) resetManifestTasks(ctx context.Context, prefix string, refs []ResetRef) ([]ArchiveTask, error) {
	tasks := make([]ArchiveTask, 0, len(refs))
	if len(refs) == 0 {
		return tasks, nil
	}
	var requests, tips bytes.Buffer
	for _, ref := range refs {
		fmt.Fprintf(&requests, "%s:state.json\n", ref.ObjectID)
		fmt.Fprintf(&tips, "%s\n", ref.ObjectID)
	}
	output, err := r.Git(ctx, requests.Bytes(), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	listing, err := r.Git(ctx, tips.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, err
	}
	parents := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(listing), "\n"), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			parents[fields[0]] = fields[1:]
		}
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	for _, ref := range refs {
		task := ArchiveTask{TaskID: strings.TrimPrefix(ref.Ref, prefix), OldTip: ref.ObjectID}
		if object, err := readBatchObject(reader); err == nil && !object.missing && object.refused == nil {
			var state struct {
				History struct {
					Generation string `json:"generation"`
				} `json:"history"`
				Task struct {
					Title string `json:"title"`
				} `json:"task"`
			}
			if json.Unmarshal(object.contents, &state) == nil {
				task.Title, task.OldGeneration = state.Task.Title, state.History.Generation
			}
		}
		seen := map[string]bool{}
		queue := []string{ref.ObjectID}
		for len(queue) > 0 {
			commit := queue[0]
			queue = queue[1:]
			if seen[commit] {
				continue
			}
			seen[commit] = true
			queue = append(queue, parents[commit]...)
		}
		task.Commits = len(seen)
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// deleteLocalRefs deletes refs in one transaction, each only while it still
// names the object listed. A symbolic ref is deleted itself, not its target.
func (r *Repository) deleteLocalRefs(ctx context.Context, refs []ResetRef) error {
	if len(refs) == 0 {
		return nil
	}
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Ref, workbookRefRoot) {
			return core.Errorf(core.CategoryOperational, "refusing to delete %s, which is outside %s", ref.Ref, workbookRefRoot)
		}
		// The manifest ref a backup sets and removes, which an interrupted
		// one may have left, is not leased: this run's own backup replaced
		// and removed it.
		if strings.HasPrefix(ref.Damaged, "it is a symbolic ref") || ref.Ref == archiveManifestRef {
			fmt.Fprintf(&input, "delete %s\n", ref.Ref)
			continue
		}
		fmt.Fprintf(&input, "delete %s %s\n", ref.Ref, ref.ObjectID)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "--stdin"); err != nil {
		return core.Wrap(core.CategoryStaleWrite,
			"cannot remove this checkout's Workbook refs; one may have changed since they were listed, so run workbook reset again", err)
	}
	return nil
}

// workbookTemporaryPattern matches the temporary file Workbook's own write
// of .workbook/config.json leaves when it is interrupted. It is the only file
// Workbook writes under .workbook/ besides config.json and guidelines.md,
// which are project files and stay.
const workbookTemporaryPattern = ".config-*.tmp"

// localResetCaches is what removeCaches would remove now: every entry of the
// Git directory's workbook/ folder, which holds only Workbook's caches, and
// every untracked file directly under .workbook/ that Workbook's own writes
// leave behind. left counts the other untracked files under .workbook/,
// ignored ones included, which are somebody else's and stay.
func (r *Repository) localResetCaches(ctx context.Context) (caches []string, left int, err error) {
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	entries, err := os.ReadDir(cacheDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, 0, core.Wrap(core.CategoryOperational, "cannot read Workbook's cache directory", err)
	default:
		for _, entry := range entries {
			caches = append(caches, filepath.Join(cacheDir, entry.Name()))
		}
	}
	if r.Root == "" {
		return caches, 0, nil
	}
	output, err := r.Git(ctx, nil, "ls-files", "-z", "--others", "--", workbookDirectory+"/")
	if err != nil {
		return caches, 0, err
	}
	folder := filepath.Join(r.Root, workbookDirectory)
	for _, name := range strings.Split(string(output), "\x00") {
		if name == "" {
			continue
		}
		path := filepath.Join(r.Root, filepath.FromSlash(name))
		if !strings.HasPrefix(path, folder+string(filepath.Separator)) {
			return caches, 0, core.Errorf(core.CategoryOperational, "Git listed %q outside %s", name, workbookDirectory)
		}
		// The settings and the guidelines are project files even before
		// anybody commits them, and setup needs the settings to know where
		// the tasks are.
		if name == configPath || name == workbookDirectory+"/guidelines.md" {
			continue
		}
		if matched, _ := filepath.Match(workbookTemporaryPattern, filepath.Base(path)); matched && filepath.Dir(path) == folder {
			caches = append(caches, path)
			continue
		}
		left++
	}
	return caches, left, nil
}

// removeCaches removes what localResetCaches lists, then the Git directory's
// workbook/ folder itself. A tracked file is never removed, and neither is
// any file under .workbook/ that Workbook did not write. It returns the paths
// removed, none that were already gone, and how many files it left.
func (r *Repository) removeCaches(ctx context.Context) ([]string, int, error) {
	caches, left, err := r.localResetCaches(ctx)
	if err != nil {
		return nil, 0, err
	}
	var removed []string
	for _, path := range caches {
		if err := os.RemoveAll(path); err != nil {
			return removed, left, core.Wrap(core.CategoryOperational, "cannot remove "+path, err)
		}
		removed = append(removed, path)
	}
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	if err := os.Remove(cacheDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return removed, left, core.Wrap(core.CategoryOperational, "cannot remove "+cacheDir, err)
	}
	return removed, left, nil
}
