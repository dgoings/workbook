package gitstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

const managedPrePushMarker = "# workbook-managed-pre-push v1"

const managedPrePushHook = `#!/bin/sh
# workbook-managed-pre-push v1

if [ "${WORKBOOK_PRE_PUSH_ACTIVE:-}" = "1" ]; then
	exit 0
fi

if [ "${1:-}" != "origin" ]; then
	exit 0
fi

WORKBOOK_PRE_PUSH_ACTIVE=1 workbook push
`

type HookStatus string

const (
	HookInstalled HookStatus = "installed"
	HookUnchanged HookStatus = "unchanged"
)

type HookInstallResult struct {
	Hook   string     `json:"hook"`
	Path   string     `json:"path"`
	Status HookStatus `json:"status"`
}

// InstallHooks installs Workbook's optional managed pre-push hook. It never
// overwrites an existing hook that does not carry Workbook's management marker.
func (r *Repository) InstallHooks(ctx context.Context) (HookInstallResult, error) {
	result := HookInstallResult{Hook: "pre-push"}
	if err := r.verifyIdentity(ctx); err != nil {
		return result, err
	}
	pathOutput, err := r.Git(ctx, nil, "rev-parse", "--path-format=absolute", "--git-path", "hooks/pre-push")
	if err != nil {
		return result, err
	}
	hookPath, err := gitSingleLine(pathOutput)
	if err != nil {
		return result, core.Wrap(core.CategoryOperational, "Git returned an invalid pre-push hook path", err)
	}
	result.Path = filepath.Clean(hookPath)

	existing, err := os.ReadFile(result.Path)
	replaceManaged := false
	switch {
	case err == nil:
		if !isManagedPrePush(existing) {
			return result, unmanagedHookError(result.Path)
		}
		replaceManaged = true
		info, statErr := os.Stat(result.Path)
		if statErr != nil {
			return result, core.Wrap(core.CategoryOperational, "cannot inspect managed pre-push hook", statErr)
		}
		if string(existing) == managedPrePushHook && info.Mode().Perm()&0o111 != 0 {
			result.Status = HookUnchanged
			return result, nil
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return result, core.Wrap(core.CategoryOperational, "cannot read pre-push hook", err)
	}

	if err := writeManagedHook(result.Path, replaceManaged); err != nil {
		return result, err
	}
	result.Status = HookInstalled
	return result, nil
}

func isManagedPrePush(contents []byte) bool {
	lines := strings.SplitN(string(contents), "\n", 3)
	return len(lines) >= 2 && lines[1] == managedPrePushMarker
}

func unmanagedHookError(path string) error {
	return core.Errorf(
		core.CategoryOperational,
		"pre-push hook %q is not managed by Workbook; it was preserved. To chain Workbook manually, invoke `workbook push` for origin and stop the hook if it fails",
		path,
	)
}

func writeManagedHook(path string, replaceManaged bool) error {
	if replaceManaged {
		return updateManagedHook(path)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create hooks directory", err)
	}
	temporary, err := os.CreateTemp(directory, ".workbook-pre-push-*")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create temporary pre-push hook", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.WriteString(managedPrePushHook); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot write temporary pre-push hook", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot sync temporary pre-push hook", err)
	}
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot make temporary pre-push hook executable", err)
	}
	if err := temporary.Close(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot close temporary pre-push hook", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			current, readErr := os.ReadFile(path)
			if readErr == nil && !isManagedPrePush(current) {
				return unmanagedHookError(path)
			}
			return core.Wrap(core.CategoryOperational, "pre-push hook appeared during installation; retry", err)
		}
		return core.Wrap(core.CategoryOperational, "cannot install pre-push hook", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return nil
}

func updateManagedHook(path string) error {
	hook, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot open managed pre-push hook", err)
	}
	defer hook.Close()

	current, err := io.ReadAll(hook)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot read managed pre-push hook", err)
	}
	if !isManagedPrePush(current) {
		return unmanagedHookError(path)
	}
	if _, err := hook.Seek(0, 0); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot seek managed pre-push hook", err)
	}
	if _, err := hook.WriteString(managedPrePushHook); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot update managed pre-push hook", err)
	}
	if err := hook.Truncate(int64(len(managedPrePushHook))); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot truncate managed pre-push hook", err)
	}
	if err := hook.Chmod(0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot make managed pre-push hook executable", err)
	}
	if err := hook.Sync(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot sync managed pre-push hook", err)
	}

	installed, err := os.ReadFile(path)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot verify managed pre-push hook update", err)
	}
	if !isManagedPrePush(installed) {
		return unmanagedHookError(path)
	}
	if string(installed) != managedPrePushHook {
		return core.Errorf(core.CategoryOperational, "managed pre-push hook changed concurrently; retry")
	}
	return nil
}
