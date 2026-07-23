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

	"github.com/dgoings/workbook/internal/core"
)

// Repository identifies a working tree and the Git directory shared by its
// worktrees.
type Repository struct {
	Root         string
	CommonGitDir string
	gitPath      string
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
		Root:         filepath.Clean(rootPath),
		CommonGitDir: filepath.Clean(commonGitPath),
		gitPath:      gitPath,
	}, nil
}

func (r *Repository) verifyIdentity(ctx context.Context) error {
	gitPath := r.gitPath
	if gitPath == "" {
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return core.Wrap(core.CategoryOperational, "cannot find git executable", err)
		}
	}

	root, err := runGit(ctx, gitPath, r.Root, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return core.Wrap(core.CategoryNotInitialized, "cannot verify Git repository", err)
	}
	commonGitDir, err := runGit(ctx, gitPath, r.Root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
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
	return nil
}

// Git executes Git against this repository's working-tree root.
func (r *Repository) Git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	gitPath := r.gitPath
	if gitPath == "" {
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return nil, core.Wrap(core.CategoryOperational, "cannot find git executable", err)
		}
	}
	output, err := runGit(ctx, gitPath, r.Root, stdin, args...)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, fmt.Sprintf("git %s failed", strings.Join(args, " ")), err)
	}
	return output, nil
}

// Actor returns the author email configured for this repository.
func (r *Repository) Actor(ctx context.Context) (string, error) {
	output, err := r.Git(ctx, nil, "config", "--get", "user.email")
	if err != nil {
		return "", err
	}
	actor, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid actor email", err)
	}
	return actor, nil
}

func runGit(ctx context.Context, gitPath, directory string, stdin []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, gitPath, append([]string{"-C", directory}, args...)...)
	command.Stdin = bytes.NewReader(stdin)
	command.Env = gitEnvironment(os.Environ())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		detail := strings.TrimSuffix(stderr.String(), "\n")
		detail = strings.TrimSuffix(detail, "\r")
		if detail == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, detail)
	}
	return stdout.Bytes(), nil
}

func gitEnvironment(environ []string) []string {
	const key = "GIT_NO_REPLACE_OBJECTS="
	result := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, key) {
			result = append(result, entry)
		}
	}
	return append(result, key+"1")
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
