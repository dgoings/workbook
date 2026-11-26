// Package gitstore provides the Git-backed repository boundary for Workbook.
package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

type gitCommandObserver func([]string)

// gitCommandResult preserves both Git output streams even when the command
// exits nonzero. It is private because public Repository.Git intentionally
// retains its stdout-or-error contract.
type gitCommandResult struct {
	stdout []byte
	stderr []byte
	err    error
	// redactor removed credentials from stderr, and removes the same ones from
	// anything else reported about this command, such as its arguments.
	redactor credentialRedactor
}

// Repository identifies a working tree and the Git directory shared by its
// worktrees.
type Repository struct {
	Root         string
	CommonGitDir string
	// Now, when set, is the clock configuration packs are stamped with in
	// place of the system clock, so a caller importing a project's past can
	// date its configuration changes the way its task operations are dated.
	Now     func() time.Time
	gitPath string

	metadataMu       sync.RWMutex
	identityVerified bool
	configLoaded     bool
	config           core.ProjectConfig
	// identityLoaded and the fields below memoize the canonical project
	// identity for an opened repository, exactly as configLoaded does for the
	// tracked configuration. Resolving it can publish a ref, so doing it once
	// per opened repository is what keeps a command from republishing on every
	// read.
	identityLoaded     bool
	identity           core.ProjectIdentity
	identityHead       string
	identitySource     identitySource
	identityMinted     bool
	identityRefCreated bool
	// identityPublicationReported keeps the one-time migration announcement from
	// repeating when one command synchronizes more than once.
	identityPublicationReported bool
	// originIdentity records what this command has established about origin's
	// copy of the identity, so the publication paths never write a task ref to
	// a remote holding another project.
	originIdentity originIdentityState
	// identityReport carries what establishing that agreement cost, or could
	// not achieve, for a caller whose own result is task shaped.
	identityReport *SyncIdentityResult
	identityDrift  string
	// remoteOverride names the remote one command named with --remote, and
	// remoteName memoizes the remote the project's settings name. See
	// remote() in tasksremote.go.
	remoteOverride string
	remoteName     string
	// moveBackup is set for the length of a move-tasks run, and backs up
	// every task tip a leased push replaces at its destination
	// (tasksmove_backup.go).
	moveBackup *moveBackup
	// moveSourceKeys is the key set of the configuration a move found at the
	// place it leaves, when that configuration has diverged from this
	// checkout's (carryConfig): its keys are this project's too, so a move
	// and its clean-up decide ownership by both sets (moveKeySet).
	moveSourceKeys core.KeySet
	// requireFinishedMove makes synchronization check a remote the tasks
	// came back to (RequireFinishedMove).
	requireFinishedMove bool
	// returningTo names the remote a move is bringing the tasks back to,
	// whose marker is read as live for the length of that move; see
	// MoveTasks.
	returningTo string
	// identityChains memoizes readIdentityChain by tip commit.
	identityChains map[string]identityChain
	// liveIdentities are identity ref tips this handle has read to be live
	// with this clone's project, other than the identity commit itself: a
	// return recorded on a place the tasks came back to (liveIdentityAt).
	liveIdentities map[string]struct{}
	// followed memoizes followMoved by the marker it started from.
	followed map[TasksMoved]movedPath
	// originMoved is the tasks-moved marker this command found on the remote
	// it synchronizes with, which every publication path then refuses.
	originMoved *TasksMoved
	// unconfirmed holds a warning for each publication this handle made that
	// the remote accepted and that it could not check afterwards
	// (strandedPublication).
	unconfirmed []core.Warning
	// trackedIdentity is where this checkout last saw its tasks remote's
	// identity ref, as resolving the identity listed it.
	trackedIdentity trackedIdentityHead
	// relicPackMu guards the fields below, which decide whether a run packs
	// the relic namespaces (relicpack.go): relicRefsMoved is set when this
	// handle moved a relic ref since it last packed, relicPackFormatKnown once
	// the ref backend was read, and relicPackRefused when that backend has no
	// loose refs or this Git refuses pack-refs --include. relicPackRemotes
	// names the remotes whose relic tracking refs the next pack covers.
	relicPackMu          sync.Mutex
	relicRefsMoved       bool
	relicPackRemotes     map[string]bool
	relicPackFormatKnown bool
	relicPackRefused     bool
	// moveStepHook, set only by tests, runs before each step of MoveTasks and
	// stops the run where it returns an error, standing in for a process
	// killed at that point.
	moveStepHook func(step string) error
	// resetStepHook, set only by tests, runs after each step of a reset; an
	// error from it stops the reset there, as an interruption would.
	resetStepHook func(step string) error
	// leasedPushHook, set only by tests, runs after a move's backup of the
	// copies a leased push replaces and before that push, standing in for a
	// teammate who publishes in between.
	leasedPushHook func()
	// vocabularyLoaded and the two fields below memoize the project's status
	// configuration, exactly as configLoaded does for the tracked file. Reading
	// it costs a ref enumeration and, for a project that has one, an object
	// read; doing that once per opened repository is what keeps it off the per
	// task path.
	vocabularyLoaded bool
	vocabulary       core.Vocabulary
	// keysLoaded and keys memoize the project's task-ID keys on the same terms
	// as the vocabulary above, and for a sharper reason: the key set is what
	// every ref listing classifies names against, so a fresh read per listing
	// would add a Git process to every command that touches more than one task.
	// It is dropped, not replaced, wherever this process moves the ledger — see
	// forgetKeySet. keysFounding records which founding key the set was
	// resolved against, and is part of the memo's key rather than its answer,
	// for the reason stateConfig.founding is.
	keysLoaded   bool
	keysFounding string
	keys         core.KeySet
	// keysHead is the ledger tip the memoized set was resolved at, so a
	// long-lived handle can ask whether the memo is still about the ledger in
	// front of it. It is not part of the memo's key — a caller of keySet has no
	// head to offer and must not be made to read one — it is what
	// ForgetKeySetUnlessAt compares against for a caller that has just read one.
	keysHead string
	// stateHead and stateConfig memoize the last checkpoint
	// LoadVocabularyState decoded, keyed by the commit it came from and by the
	// founding key it was decoded for: three of its four sections are a
	// function of the commit alone, but an absent key section is read as
	// whichever founding key the caller supplied, so the tip does not identify
	// that one on its own. See decodedConfig.founding.
	//
	// This is a different memo from the one above and cannot replace it: that
	// one answers "the statuses", this one answers "the whole configuration at
	// this ref tip", and the caller that needs the second is the one that must
	// see a change. Keying on the head is what makes it exact rather than a
	// guess — a commit's contents cannot change, so a tip this process has
	// already decoded decodes to the same configuration forever, and a tip that
	// moved misses. It exists for `workbook serve`, which reads this on every
	// request and would otherwise pay an object read a second for a
	// configuration that changes once a month.
	stateHead   string
	stateConfig decodedConfig
	// The four fields below record what this command last saw of the
	// configuration ledger on each side. The synchronization stage sets both
	// from the ref listing it already makes, and Push sets the remote side from
	// the remote listing it already makes, so the publication paths decide
	// whether origin is behind without asking Git a second time. The two sides
	// are tracked separately because the paths that learn them differ: a fetch
	// learns both at once, a push learns origin's first.
	configLocalKnown  bool
	configLocalHead   string
	configRemoteKnown bool
	configRemoteHead  string
	// configRemoteNeedsUpgrade records that origin's ledger was written by a
	// newer Workbook than this build. It is remembered beside the heads because
	// the paths that would publish over it are not the path that read it, and
	// because a push that origin can only reject is a refusal this run has
	// already reported once.
	configRemoteNeedsUpgrade bool
	// configReport carries what publication established about origin's copy of
	// the ledger, for a caller whose own result is task shaped.
	configReport  *SyncConfigResult
	objectIDBytes int
	// setAsideMemo remembers, by parked ref name, what each parked tip holds
	// against the task tip it was last compared with. It holds one entry per
	// parked ref that exists. See SetAsideEdits.
	setAsideMu   sync.Mutex
	setAsideMemo map[string]parkedVerdict
	// parkedListed is the parked refs SetAsideEdits last listed, and
	// parkedGeneration counts this process's changes to them. See
	// parkedListing.
	parkedListed     *parkedListingMemo
	parkedGeneration uint64
	actorOnce        sync.Once
	actor            string
	actorErr         error
	commandObserver  gitCommandObserver
	// relicObjectObserver is told every object a relic reader asks Git for
	// with its content, as the request Git was given: an object ID, or a
	// commit and a path. An object whose size and type alone are asked for is
	// not reported. It is nil outside tests, which use it to prove what a
	// read opened and what it left closed, a part's body above all.
	relicObjectObserver func(request string)
}

// Open discovers the repository containing startDir without changing the
// caller's process working directory.
func Open(ctx context.Context, startDir string) (*Repository, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot find git executable", err)
	}

	out, err := runGit(ctx, gitPath, startDir, nil, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, core.Wrap(core.CategoryNotInitialized, "cannot find Git repository root and common directory", err)
	}
	rootPath, commonGitPath, err := gitTwoLines(out)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "Git returned an invalid repository root", err)
	}

	return &Repository{
		Root:             filepath.Clean(rootPath),
		CommonGitDir:     filepath.Clean(commonGitPath),
		gitPath:          gitPath,
		identityVerified: true,
	}, nil
}

func (r *Repository) verifyIdentity(ctx context.Context) error {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if r.identityVerified {
		return nil
	}

	out, err := r.Git(ctx, nil, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return core.Wrap(core.CategoryNotInitialized, "cannot verify Git repository root and common directory", err)
	}
	rootPath, commonGitPath, err := gitTwoLines(out)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid repository root", err)
	}
	if filepath.Clean(rootPath) != filepath.Clean(r.Root) ||
		filepath.Clean(commonGitPath) != filepath.Clean(r.CommonGitDir) {
		return core.Errorf(core.CategoryNotInitialized, "repository paths do not match Git metadata")
	}
	r.identityVerified = true
	return nil
}

func (r *Repository) rememberGitObjectID(objectID string) error {
	decoded, err := decodeObjectID(objectID)
	if err != nil {
		return err
	}

	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if r.objectIDBytes == 0 {
		r.objectIDBytes = len(decoded)
		return nil
	}
	if len(decoded) != r.objectIDBytes {
		return fmt.Errorf("Git returned inconsistent object ID lengths")
	}
	return nil
}

// objectIDWidth reports the repository's observed object ID width in bytes.
// Callers that already validated every object ID against it use this instead of
// decoding each ID a second time.
func (r *Repository) objectIDWidth() (int, error) {
	r.metadataMu.RLock()
	objectIDBytes := r.objectIDBytes
	r.metadataMu.RUnlock()
	if objectIDBytes == 0 {
		return 0, fmt.Errorf("repository object ID length has not been observed")
	}
	return objectIDBytes, nil
}

func (r *Repository) validateFullObjectID(objectID string) error {
	decoded, err := decodeObjectID(objectID)
	if err != nil {
		return err
	}

	r.metadataMu.RLock()
	objectIDBytes := r.objectIDBytes
	r.metadataMu.RUnlock()
	if objectIDBytes == 0 {
		return fmt.Errorf("repository object ID length has not been observed")
	}
	if len(decoded) != objectIDBytes {
		return fmt.Errorf("object ID is abbreviated or has the wrong full length")
	}
	return nil
}

// ensureGitObjectIDWidth learns the repository's object-ID width from Git
// itself, without writing an object. Untrusted ref text must never establish
// this process-wide validation boundary.
func (r *Repository) ensureGitObjectIDWidth(ctx context.Context) error {
	r.metadataMu.RLock()
	known := r.objectIDBytes != 0
	r.metadataMu.RUnlock()
	if known {
		return nil
	}

	output, err := r.Git(ctx, nil, "hash-object", "--stdin")
	if err != nil {
		return err
	}
	objectID, err := gitSingleLine(output)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid object ID", err)
	}
	if err := r.rememberGitObjectID(objectID); err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid object ID", err)
	}
	return nil
}

// Git executes Git against this repository's working-tree root.
func (r *Repository) Git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return r.gitWithEnv(ctx, nil, stdin, args...)
}

func (r *Repository) gitWithEnv(ctx context.Context, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	result := r.gitWithEnvResult(ctx, extraEnv, stdin, args...)
	if result.err != nil {
		return nil, core.Wrap(core.CategoryOperational, fmt.Sprintf("git %s failed", result.redactor.redact(strings.Join(args, " "))), gitCommandResultError(result))
	}
	return result.stdout, nil
}

func (r *Repository) gitWithEnvResult(ctx context.Context, extraEnv []string, stdin []byte, args ...string) gitCommandResult {
	gitPath := r.gitPath
	if gitPath == "" {
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return gitCommandResult{err: core.Wrap(core.CategoryOperational, "cannot find git executable", err)}
		}
	}
	r.observeGitCommand(args)
	return runGitWithEnvResult(ctx, gitPath, r.Root, extraEnv, stdin, args...)
}

func (r *Repository) observeGitCommand(args []string) {
	if r.commandObserver != nil {
		r.commandObserver(append([]string(nil), args...))
	}
}

// Actor returns the person this context's edits are recorded under.
//
// That is the actor core.WithActor put on the context, when there is one — the
// command line's WORKBOOK_ACTOR, or a long-running caller's per-request name —
// and otherwise the checkout's own `user.email` (see checkoutEmail). The
// context's actor is answered without asking Git anything, so a folder with no
// `user.email` works when somebody names the person, and it is never memoized:
// one Repository serves contexts naming different people.
func (r *Repository) Actor(ctx context.Context) (string, error) {
	if actor, asserted := core.ActorFrom(ctx); asserted {
		return actor, nil
	}
	return r.checkoutEmail(ctx)
}

// checkoutEmail returns the author email configured for this checkout,
// whatever the context asserts. It is memoized: unlike the acting person, it
// is a fact about the folder.
func (r *Repository) checkoutEmail(ctx context.Context) (string, error) {
	r.actorOnce.Do(func() {
		output, err := r.Git(ctx, nil, "config", "--get", "user.email")
		if err != nil {
			r.actorErr = err
			return
		}
		r.actor, r.actorErr = gitSingleLine(output)
		if r.actorErr != nil {
			r.actorErr = core.Wrap(core.CategoryOperational, "Git returned an invalid actor email", r.actorErr)
		}
	})
	return r.actor, r.actorErr
}

func runGit(ctx context.Context, gitPath, directory string, stdin []byte, args ...string) ([]byte, error) {
	return runGitWithEnv(ctx, gitPath, directory, nil, stdin, args...)
}

func runGitWithEnv(ctx context.Context, gitPath, directory string, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	result := runGitWithEnvResult(ctx, gitPath, directory, extraEnv, stdin, args...)
	if result.err != nil {
		return nil, gitCommandResultError(result)
	}
	return result.stdout, nil
}

func runGitWithEnvResult(ctx context.Context, gitPath, directory string, extraEnv []string, stdin []byte, args ...string) gitCommandResult {
	command := exec.CommandContext(ctx, gitPath, append([]string{"-C", directory}, args...)...)
	command.Stdin = bytes.NewReader(stdin)
	command.Env = gitEnvironment(os.Environ(), extraEnv)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	// Redacted here, where it is captured, so that no reader of Git's stderr —
	// an error, a refusal reason, a warning the board shows — can carry a
	// credential out of a remote URL. See readCredentialRedactor.
	redactor := failureCredentialRedactor(ctx, gitPath, directory, extraEnv, args, err, stderr.Bytes())
	return gitCommandResult{stdout: stdout.Bytes(), stderr: []byte(redactor.redact(stderr.String())), err: err, redactor: redactor}
}

// failureCredentialRedactor chooses how much a command's output needs: the
// configured credentials are read only when the command failed and printed an
// `@`, the one case where Git's words are reported and can hold a userinfo.
func failureCredentialRedactor(ctx context.Context, gitPath, directory string, extraEnv, args []string, err error, stderr []byte) credentialRedactor {
	if err != nil && bytes.IndexByte(stderr, '@') >= 0 {
		return readCredentialRedactor(ctx, gitPath, directory, extraEnv, args)
	}
	return argumentCredentialRedactor(args)
}

func gitCommandResultError(result gitCommandResult) error {
	if result.err == nil {
		return nil
	}
	detail := strings.TrimSuffix(string(result.stderr), "\n")
	detail = strings.TrimSuffix(detail, "\r")
	if detail == "" {
		return result.err
	}
	return fmt.Errorf("%w: %s", result.err, detail)
}

func gitEnvironment(environ []string, extra []string) []string {
	keys := map[string]struct{}{"GIT_NO_REPLACE_OBJECTS": {}}
	for _, entry := range extra {
		if name, _, found := strings.Cut(entry, "="); found {
			keys[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(environ)+len(extra)+1)
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := keys[name]; overridden {
				continue
			}
		}
		result = append(result, entry)
	}
	result = append(result, "GIT_NO_REPLACE_OBJECTS=1")
	return append(result, extra...)
}

// gitTwoLines splits the two-line output of a rev-parse invocation asking two
// questions at once (for example --show-toplevel and --git-common-dir) into
// its answers, in the order asked. It rejects anything but exactly two
// non-empty lines, which is stricter than gitSingleLine: gitSingleLine
// returns ("", nil) for a lone newline, but an empty line here would answer
// one of the two questions with a root or common directory that
// filepath.Clean would turn into ".".
func gitTwoLines(output []byte) (string, string, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return "", "", fmt.Errorf("expected two trailing-newline-terminated lines")
	}
	trimmed := output[:len(output)-1]
	parts := bytes.SplitN(trimmed, []byte("\n"), 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected exactly two output lines")
	}
	first, second := parts[0], parts[1]
	if len(first) > 0 && first[len(first)-1] == '\r' {
		first = first[:len(first)-1]
	}
	if len(second) > 0 && second[len(second)-1] == '\r' {
		second = second[:len(second)-1]
	}
	if len(first) == 0 || len(second) == 0 {
		return "", "", fmt.Errorf("expected two non-empty output lines")
	}
	if bytes.ContainsAny(first, "\r\n") || bytes.ContainsAny(second, "\r\n") {
		return "", "", fmt.Errorf("expected exactly two output lines")
	}
	return string(first), string(second), nil
}

func gitSingleLine(output []byte) (string, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return "", fmt.Errorf("expected one trailing newline")
	}
	line := output[:len(output)-1]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	if bytes.ContainsAny(line, "\r\n") {
		return "", fmt.Errorf("expected exactly one output line")
	}
	return string(line), nil
}
