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
}

// Repository identifies a working tree and the Git directory shared by its
// worktrees.
type Repository struct {
	Root         string
	CommonGitDir string
	gitPath      string

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
	identityReport  *SyncIdentityResult
	identityDrift   string
	objectIDBytes   int
	actorOnce       sync.Once
	actor           string
	actorErr        error
	commandObserver gitCommandObserver
}

// Open discovers the repository containing startDir without changing the
// caller's process working directory.
func Open(ctx context.Context, startDir string) (*Repository, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot find git executable", err)
	}

	root, err := runGit(ctx, gitPath, startDir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, core.Wrap(core.CategoryNotInitialized, "cannot find Git repository", err)
	}
	commonGitDir, err := runGit(ctx, gitPath, startDir, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, core.Wrap(core.CategoryNotInitialized, "cannot find Git common directory", err)
	}
	rootPath, err := gitSingleLine(root)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "Git returned an invalid repository root", err)
	}
	commonGitPath, err := gitSingleLine(commonGitDir)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "Git returned an invalid common directory", err)
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

	root, err := r.Git(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return core.Wrap(core.CategoryNotInitialized, "cannot verify Git repository", err)
	}
	commonGitDir, err := r.Git(ctx, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return core.Wrap(core.CategoryNotInitialized, "cannot verify Git common directory", err)
	}
	rootPath, err := gitSingleLine(root)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid repository root", err)
	}
	commonGitPath, err := gitSingleLine(commonGitDir)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid common directory", err)
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
		return nil, core.Wrap(core.CategoryOperational, fmt.Sprintf("git %s failed", strings.Join(args, " ")), gitCommandResultError(result))
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

// HasOrigin reports whether an origin remote is configured.
//
// Any failure reads as "not configured" because that is the only actionable
// distinction: a caller consults this to decide whether attempting the network
// is worth a timeout, and a repository too broken to answer has nothing to
// synchronize with either.
func (r *Repository) HasOrigin(ctx context.Context) bool {
	_, err := r.Git(ctx, nil, "remote", "get-url", "origin")
	return err == nil
}

// Actor returns the author email configured for this repository.
func (r *Repository) Actor(ctx context.Context) (string, error) {
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
	return gitCommandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err}
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
