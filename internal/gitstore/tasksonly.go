package gitstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// A tasks-only folder is an ordinary, non-bare Git folder that holds a
// project's Workbook refs and nothing else: no branch, no tag, nothing checked
// out, and none of the code. It exists for a server that shows and edits tasks
// and has no use for the code.
//
// What makes a folder one is recorded in that folder's own Git settings rather
// than in the tracked .workbook/config.json, because the shape belongs to this
// one copy of the project and not to everybody who clones it.
const (
	// tasksOnlySetting is the marker. Later commands read it to say what is
	// unavailable without the code instead of answering from history the
	// folder does not hold.
	tasksOnlySetting = "workbook.tasksOnly"

	// tasksOnlyFetchRefspec is the only fetch refspec a tasks-only folder's
	// origin carries. A negative refspec on its own matches nothing, so a
	// plain `git fetch` or `git pull` in the folder downloads nothing at all.
	//
	// Leaving the refspec out is not the same thing: with no refspec
	// configured, `git fetch origin` fetches origin's HEAD into FETCH_HEAD,
	// which is the default branch's code, and `git pull` then checks it out.
	// Pointing the refspec at Workbook's own tracking namespace would be no
	// better, because Workbook treats those refs as what origin held at its
	// last synchronization, and a compaction leases its push against them.
	//
	// The configured refspec does not narrow Workbook's own fetches. Each one
	// names its refspecs on the command line, and Git applies a configured
	// negative refspec only to the configured positive ones.
	tasksOnlyFetchRefspec = "^refs/*"

	// tasksOnlyTagOption keeps tags from following a fetch. Nothing a
	// tasks-only folder fetches points at code, so no tag could follow one,
	// but the setting says so for anybody reading the folder's configuration.
	tasksOnlyTagOption = "--no-tags"
)

// TasksOnlyFolder reports what preparing a tasks-only folder did.
type TasksOnlyFolder struct {
	// Root is the folder's working-tree root.
	Root string
	// Remote is the origin URL the folder synchronizes with.
	Remote string
	// Initialized reports that this run made the folder a Git folder.
	Initialized bool
	// Changed names each Git setting this run wrote, in the order written. A
	// folder that was already set up reports none.
	Changed []string
	// Restored names the settings in Changed that the folder already had with
	// a different value, which is to say somebody changed them. The others
	// were absent and this run set them for the first time.
	Restored []string
}

// PrepareTasksOnlyFolder makes dir a tasks-only folder whose origin is
// remoteURL, or confirms that it already is one.
//
// It accepts three shapes of dir:
//
//   - an empty folder outside any Git repository, which it initializes;
//   - a Git folder this function set up for the same remote, which it leaves
//     alone apart from restoring any of its settings somebody changed; and
//   - a Git folder with nothing in it at all — no ref, no remote, no file
//     besides .git — which is what an interrupted first run or a hand-run
//     `git init` leaves, and which it adopts.
//
// Anything else is refused before it is touched: a folder holding files, a
// repository it did not set up, a folder inside another repository, and a
// tasks-only folder set up for a different remote. Turning somebody's working
// copy into a tasks-only folder would hide their branches from every later
// fetch, so the refusal is the safe answer to a mistaken folder.
//
// Origin is asked before anything is created, so a wrong URL, an unreachable
// remote or a remote with no Workbook project leaves an empty folder empty.
// The settings are written marker first and URL last: until the URL is
// written the folder has no origin to fetch from, so no interruption leaves a
// folder whose plain `git fetch` downloads code.
//
// A non-empty key is the project key the caller expects origin's project to
// have. It is checked against origin's identity before anything is created or
// written, so a wrong key also leaves the folder as it was.
//
// Every setting is read from and written to the folder's own Git
// configuration only. A marker or URL supplied by the user's global settings
// or the environment describes no particular folder, so it must neither make
// an ordinary clone look like a tasks-only folder nor stand in for one.
func PrepareTasksOnlyFolder(ctx context.Context, dir, remoteURL, key string) (TasksOnlyFolder, error) {
	folder := TasksOnlyFolder{Remote: remoteURL, Changed: []string{}, Restored: []string{}}
	if strings.TrimSpace(remoteURL) == "" {
		return folder, core.Errorf(core.CategoryInvocation, "--tasks-only needs the URL of the project's remote")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return folder, core.Wrap(core.CategoryOperational, "cannot find git executable", err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return folder, core.Wrap(core.CategoryOperational, "cannot resolve the folder to set up", err)
	}

	existing, err := tasksOnlyExistingRoot(ctx, gitPath, dir)
	if err != nil {
		return folder, err
	}
	if existing == "" {
		empty, err := folderIsEmpty(dir)
		if err != nil {
			return folder, err
		}
		if !empty {
			return folder, core.Errorf(core.CategoryValidation,
				"workbook setup --tasks-only needs an empty folder, and %s already holds files; run it in a new, empty folder", dir)
		}
		format, err := probeTasksOnlyOrigin(ctx, gitPath, dir, remoteURL)
		if err != nil {
			return folder, err
		}
		if err := checkTasksOnlyKey(ctx, gitPath, remoteURL, format, key); err != nil {
			return folder, err
		}
		args := []string{"init", "--quiet"}
		if format != "" {
			args = append(args, "--object-format="+format)
		}
		if _, err := runGit(ctx, gitPath, dir, nil, args...); err != nil {
			return folder, core.Wrap(core.CategoryOperational, "cannot create a Git folder for tasks", err)
		}
		folder.Initialized = true
		existing = dir
	} else {
		if !sameFolder(existing, dir) {
			return folder, core.Errorf(core.CategoryValidation,
				"%s is inside the Git repository at %s; workbook setup --tasks-only needs an empty folder outside any repository", dir, existing)
		}
		marked, err := gitConfigBool(ctx, gitPath, existing, tasksOnlySetting)
		if err != nil {
			return folder, err
		}
		current, err := gitConfigValue(ctx, gitPath, existing, "remote.origin.url")
		if err != nil {
			return folder, err
		}
		if marked {
			// The comparison is exact on purpose. Two spellings of one
			// remote cannot be told from two remotes without asking both,
			// and guessing wrong would point this folder's tasks at another
			// project. The person who knows says so by changing the URL.
			if current != "" && current != remoteURL {
				return folder, core.Errorf(core.CategoryValidation,
					"%s is a tasks-only folder for %s, not %s; if %s is the same repository under another spelling, or the repository has moved, run git config remote.origin.url %s in this folder and run this again; otherwise set up %s in another empty folder",
					existing, current, remoteURL, remoteURL, remoteURL, remoteURL)
			}
			// The two settings that keep code out are put back before origin
			// is asked. A folder somebody set back to fetching branches must
			// not stay that way because origin could not be reached just
			// now: the next plain git fetch would download the code.
			for _, setting := range []struct {
				name  string
				value string
				all   bool
			}{
				{name: "remote.origin.tagOpt", value: tasksOnlyTagOption},
				{name: "remote.origin.fetch", value: tasksOnlyFetchRefspec, all: true},
			} {
				changed, restored, err := ensureGitSetting(ctx, gitPath, existing, setting.name, setting.value, setting.all)
				if err != nil {
					return folder, err
				}
				if changed {
					folder.Changed = append(folder.Changed, setting.name)
				}
				if restored {
					folder.Restored = append(folder.Restored, setting.name)
				}
			}
		} else {
			pristine, err := pristineGitFolder(ctx, gitPath, existing)
			if err != nil {
				return folder, err
			}
			if !pristine {
				return folder, core.Errorf(core.CategoryValidation,
					"%s is already a Git repository that workbook setup --tasks-only did not set up; run it in a new, empty folder", existing)
			}
		}
		// Origin is asked on every run, not only the first: a folder whose
		// object format differs from origin's can never fetch from it, and
		// the settings below would make it a tasks-only folder that fails
		// every synchronization.
		format, err := probeTasksOnlyOrigin(ctx, gitPath, existing, remoteURL)
		if err != nil {
			return folder, err
		}
		if err := checkTasksOnlyObjectFormat(ctx, gitPath, existing, remoteURL, format, marked); err != nil {
			return folder, err
		}
		if err := checkTasksOnlyKey(ctx, gitPath, remoteURL, format, key); err != nil {
			return folder, err
		}
	}
	folder.Root = existing

	settings := []struct {
		name  string
		value string
		all   bool
	}{
		{name: tasksOnlySetting, value: "true"},
		{name: "remote.origin.tagOpt", value: tasksOnlyTagOption},
		{name: "remote.origin.fetch", value: tasksOnlyFetchRefspec, all: true},
		{name: "remote.origin.url", value: remoteURL},
	}
	for _, setting := range settings {
		changed, restored, err := ensureGitSetting(ctx, gitPath, existing, setting.name, setting.value, setting.all)
		if err != nil {
			return folder, err
		}
		if changed {
			folder.Changed = append(folder.Changed, setting.name)
		}
		if restored {
			folder.Restored = append(folder.Restored, setting.name)
		}
	}
	return folder, nil
}

// RestoreTasksOnlySettings puts back any tasks-only setting somebody changed
// in an already marked folder, keeping its origin URL. It is what a plain
// `workbook setup` in a tasks-only folder runs, so the one bootstrap command
// keeps the folder from fetching code whichever way it is invoked.
func (r *Repository) RestoreTasksOnlySettings(ctx context.Context) (TasksOnlyFolder, error) {
	folder := TasksOnlyFolder{Root: r.Root, Changed: []string{}, Restored: []string{}}
	gitPath, err := r.resolvedGitPath()
	if err != nil {
		return folder, err
	}
	folder.Remote, err = gitConfigValue(ctx, gitPath, r.Root, "remote.origin.url")
	if err != nil {
		return folder, err
	}
	if folder.Remote == "" {
		// A first run interrupted before it wrote the URL leaves this. There
		// is nothing to protect yet, and writing fetch settings for a remote
		// with no URL would create half of one; only the command that names
		// the remote can finish the folder.
		return folder, tasksOnlyFolderWithoutRemote(r.Root)
	}
	settings := []struct {
		name  string
		value string
		all   bool
	}{
		{name: "remote.origin.tagOpt", value: tasksOnlyTagOption},
		{name: "remote.origin.fetch", value: tasksOnlyFetchRefspec, all: true},
	}
	for _, setting := range settings {
		changed, restored, err := ensureGitSetting(ctx, gitPath, r.Root, setting.name, setting.value, setting.all)
		if err != nil {
			return folder, err
		}
		if changed {
			folder.Changed = append(folder.Changed, setting.name)
		}
		if restored {
			folder.Restored = append(folder.Restored, setting.name)
		}
	}
	return folder, nil
}

// tasksOnlyFolderWithoutRemote is the refusal for a marked folder that has no
// origin URL yet.
func tasksOnlyFolderWithoutRemote(root string) error {
	return core.Errorf(core.CategoryValidation,
		"%s is a tasks-only folder with no remote yet; run workbook setup --tasks-only <url> here with the URL of the project's remote", root)
}

// TasksOnly reports whether this repository is a tasks-only folder.
//
// Only the folder's own Git configuration answers. The same setting in the
// user's global configuration, the system configuration or the environment
// is ignored, because it would otherwise turn every clone on the machine into
// a tasks-only folder.
//
// Commands that derive anything from code history consult it and report that
// they are unavailable rather than answer from history the folder does not
// hold.
func (r *Repository) TasksOnly(ctx context.Context) (bool, error) {
	gitPath, err := r.resolvedGitPath()
	if err != nil {
		return false, err
	}
	return gitConfigBool(ctx, gitPath, r.Root, tasksOnlySetting)
}

// TasksOnlyUnavailable is the refusal a command gives in a tasks-only folder
// when what it does needs the project's code. It is a validation refusal: the
// same command fails the same way on every retry in this folder, and works in
// a clone of the code.
func TasksOnlyUnavailable(what, why string) error {
	return core.Errorf(core.CategoryValidation,
		"%s is unavailable in a tasks-only folder: %s; run it in a clone of the project's code", what, why)
}

func (r *Repository) resolvedGitPath() (string, error) {
	if r.gitPath != "" {
		return r.gitPath, nil
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot find git executable", err)
	}
	return gitPath, nil
}

// tasksOnlyExistingRoot returns the working-tree root of the repository dir
// sits in, or "" when dir is in none.
func tasksOnlyExistingRoot(ctx context.Context, gitPath, dir string) (string, error) {
	result := runGitWithEnvResult(ctx, gitPath, dir, nil, nil, "rev-parse", "--show-toplevel")
	if result.err == nil {
		root, err := gitSingleLine(result.stdout)
		if err != nil || root == "" {
			return "", core.Errorf(core.CategoryOperational, "Git returned an invalid repository root")
		}
		return filepath.Clean(root), nil
	}
	var exitErr *exec.ExitError
	if !errors.As(result.err, &exitErr) {
		return "", core.Wrap(core.CategoryOperational, "cannot ask Git whether the folder is a repository", gitCommandResultError(result))
	}
	// Git answers "not a git repository" for a folder in no repository, and
	// something else for a bare repository or the inside of a .git folder.
	// Only the first is a folder this command may initialize; the others hold
	// files, which the emptiness check refuses with a message that says so.
	return "", nil
}

func folderIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, core.Wrap(core.CategoryOperational, "cannot read the folder to set up", err)
	}
	return len(entries) == 0, nil
}

// pristineGitFolder reports a Git folder with no ref, no remote, and no file
// beside .git: nothing anybody could lose by its becoming a tasks-only folder.
func pristineGitFolder(ctx context.Context, gitPath, root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, core.Wrap(core.CategoryOperational, "cannot read the folder to set up", err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return false, nil
		}
	}
	refs, err := runGit(ctx, gitPath, root, nil, "for-each-ref", "--count=1", "--format=%(refname)")
	if err != nil {
		return false, core.Wrap(core.CategoryOperational, "cannot list the folder's Git refs", err)
	}
	if strings.TrimSpace(string(refs)) != "" {
		return false, nil
	}
	remotes, err := runGit(ctx, gitPath, root, nil, "remote")
	if err != nil {
		return false, core.Wrap(core.CategoryOperational, "cannot list the folder's Git remotes", err)
	}
	return strings.TrimSpace(string(remotes)) == "", nil
}

// probeTasksOnlyOrigin asks remoteURL whether it carries a Workbook project
// this folder can join, and returns the object format its IDs suggest so the
// new folder can match it.
//
// Only a published project identity will do. The older way of finding a
// project — reading .workbook/config.json off origin's default branch — would
// download the code this folder exists to leave behind, so a project that has
// not published its identity is sent to a clone of the code to publish it.
func probeTasksOnlyOrigin(ctx context.Context, gitPath, dir, remoteURL string) (string, error) {
	listing, err := runGit(ctx, gitPath, dir, nil, "ls-remote", remoteURL, identityRef, taskRefPrefix+"*")
	if err != nil {
		return "", core.Wrap(core.CategoryOperational,
			"cannot ask "+remoteURL+" for its Workbook project", err)
	}
	identity, tasks := false, false
	format := ""
	for _, line := range strings.Split(string(listing), "\n") {
		oid, refname, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch len(oid) {
		case 40:
			format = "sha1"
		case 64:
			format = "sha256"
		}
		if refname == identityRef {
			identity = true
		}
		if strings.HasPrefix(refname, taskRefPrefix) {
			tasks = true
		}
	}
	if identity {
		return format, nil
	}
	if tasks {
		return "", core.Errorf(core.CategoryValidation,
			"%s holds Workbook tasks but has not published %s; run workbook sync in a clone of the project's code with this version of Workbook to publish it, then run this again",
			remoteURL, identityRef)
	}
	return "", core.Errorf(core.CategoryValidation,
		"%s has no Workbook project to join; run workbook setup in a clone of the project's code to create one, then run this again", remoteURL)
}

// checkTasksOnlyObjectFormat refuses an existing folder whose object format
// differs from the one origin's IDs show. Git cannot fetch between the two, so
// adopting the folder would leave one that fails every synchronization.
func checkTasksOnlyObjectFormat(ctx context.Context, gitPath, root, remoteURL, originFormat string, marked bool) error {
	if originFormat == "" {
		return nil
	}
	out, err := runGit(ctx, gitPath, root, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot read the folder's Git object format", err)
	}
	local, err := gitSingleLine(out)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid object format", err)
	}
	if local == originFormat {
		return nil
	}
	advice := "run workbook setup --tasks-only " + remoteURL + " in a new, empty folder"
	if !marked {
		advice = "delete " + filepath.Join(root, ".git") + ", which holds nothing yet, and run this again, or " + advice
	}
	return core.Errorf(core.CategoryValidation,
		"%s uses %s object IDs and %s uses %s, and Git cannot fetch between the two; %s",
		root, local, remoteURL, originFormat, advice)
}

// checkTasksOnlyKey refuses a key that is not the founding key of origin's
// project, before the folder is touched. Reading the key means fetching
// origin's identity, so it is fetched into a temporary Git folder that is
// removed afterwards; only the identity ref is fetched, never the code.
func checkTasksOnlyKey(ctx context.Context, gitPath, remoteURL, format, key string) error {
	if key == "" {
		return nil
	}
	scratch, err := os.MkdirTemp("", "workbook-tasks-only-key-")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create a temporary folder to read origin's project key", err)
	}
	defer os.RemoveAll(scratch)
	args := []string{"init", "--quiet"}
	if format != "" {
		args = append(args, "--object-format="+format)
	}
	if _, err := runGit(ctx, gitPath, scratch, nil, args...); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create a temporary Git folder to read origin's project key", err)
	}
	probe, err := Open(ctx, scratch)
	if err != nil {
		return err
	}
	if _, err := probe.Git(ctx, nil,
		"fetch", "--no-tags", "--no-auto-maintenance", remoteURL, identityFetchRefspec); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot fetch origin's Workbook project identity", err)
	}
	record, found, err := probe.readIdentityRef(ctx, remoteIdentityRef)
	if err != nil {
		return err
	}
	if !found {
		return core.Errorf(core.CategoryOperational,
			"origin listed %s but did not deliver it; rerun workbook setup", identityRef)
	}
	if record.Identity.Key != key {
		return foundingKeyMismatch(record.Identity.Key)
	}
	return nil
}

// ensureGitSetting gives name the single value in the folder's own Git
// configuration. It reports whether it wrote anything, and whether what it
// replaced was a value somebody had set rather than nothing at all.
func ensureGitSetting(ctx context.Context, gitPath, root, name, value string, all bool) (changed, restored bool, err error) {
	args := []string{"config", "--local", "--get-all", name}
	result := runGitWithEnvResult(ctx, gitPath, root, nil, nil, args...)
	current := ""
	present := false
	if result.err == nil {
		current = strings.TrimRight(string(result.stdout), "\n")
		present = true
	} else if !gitConfigMissing(result) {
		return false, false, core.Wrap(core.CategoryOperational, "cannot read Git setting "+name, gitCommandResultError(result))
	}
	if current == value {
		return false, false, nil
	}
	write := []string{"config", "--local"}
	if all || strings.Contains(current, "\n") {
		write = append(write, "--replace-all")
	}
	write = append(write, name, value)
	if _, err := runGit(ctx, gitPath, root, nil, write...); err != nil {
		return false, false, core.Wrap(core.CategoryOperational, "cannot write Git setting "+name, err)
	}
	return true, present, nil
}

// gitConfigValue and gitConfigBool read name from the folder's own Git
// configuration only; see TasksOnly for why.
func gitConfigValue(ctx context.Context, gitPath, root, name string) (string, error) {
	result := runGitWithEnvResult(ctx, gitPath, root, nil, nil, "config", "--local", "--get", name)
	if result.err == nil {
		value, err := gitSingleLine(result.stdout)
		if err != nil {
			return "", core.Wrap(core.CategoryOperational, "Git returned an invalid "+name, err)
		}
		return value, nil
	}
	if gitConfigMissing(result) {
		return "", nil
	}
	return "", core.Wrap(core.CategoryOperational, "cannot read Git setting "+name, gitCommandResultError(result))
}

func gitConfigBool(ctx context.Context, gitPath, root, name string) (bool, error) {
	result := runGitWithEnvResult(ctx, gitPath, root, nil, nil, "config", "--local", "--type=bool", "--get", name)
	if result.err == nil {
		value, err := gitSingleLine(result.stdout)
		if err != nil {
			return false, core.Wrap(core.CategoryOperational, "Git returned an invalid "+name, err)
		}
		return value == "true", nil
	}
	if gitConfigMissing(result) {
		return false, nil
	}
	return false, core.Wrap(core.CategoryOperational, "cannot read Git setting "+name, gitCommandResultError(result))
}

// gitConfigMissing reports `git config --get`'s answer for a setting that is
// not set, which is exit status 1 and nothing else.
func gitConfigMissing(result gitCommandResult) bool {
	var exitErr *exec.ExitError
	return errors.As(result.err, &exitErr) && exitErr.ExitCode() == 1
}

// sameFolder compares two paths after resolving symbolic links, because Git
// reports the resolved root and a caller may name the folder through a link —
// on macOS, every temporary directory is one.
func sameFolder(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(resolvedA) == filepath.Clean(resolvedB)
}
