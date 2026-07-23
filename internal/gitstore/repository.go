// Package gitstore provides the Git-backed repository boundary for Workbook.
package gitstore

import (
	"bytes"
	"context"
	"fmt"
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
		return nil, core.Wrap(core.CategoryInvocation, "cannot find git executable", err)
	}

	root, err := runGit(ctx, gitPath, startDir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, core.Wrap(core.CategoryNotInitialized, "cannot find Git repository", err)
	}
	commonGitDir, err := runGit(ctx, gitPath, startDir, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, core.Wrap(core.CategoryNotInitialized, "cannot find Git common directory", err)
	}

	return &Repository{
		Root:         filepath.Clean(strings.TrimSpace(string(root))),
		CommonGitDir: filepath.Clean(strings.TrimSpace(string(commonGitDir))),
		gitPath:      gitPath,
	}, nil
}

// Git executes Git against this repository's working-tree root.
func (r *Repository) Git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	output, err := runGit(ctx, r.gitPath, r.Root, stdin, args...)
	if err != nil {
		return nil, core.Wrap(core.CategoryInvocation, fmt.Sprintf("git %s failed", strings.Join(args, " ")), err)
	}
	return output, nil
}

// Actor returns the author email configured for this repository.
func (r *Repository) Actor(ctx context.Context) (string, error) {
	output, err := r.Git(ctx, nil, "config", "--get", "user.email")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func runGit(ctx context.Context, gitPath, directory string, stdin []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, gitPath, append([]string{"-C", directory}, args...)...)
	command.Stdin = bytes.NewReader(stdin)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
