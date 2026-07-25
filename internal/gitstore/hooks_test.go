package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

func TestInstallHooksCreatesExecutableManagedHookAndIsIdempotent(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}

	first, err := repo.InstallHooks(context.Background())
	if err != nil {
		t.Fatalf("InstallHooks(first) error = %v", err)
	}
	if first.Status != HookInstalled {
		t.Fatalf("InstallHooks(first).Status = %q, want %q", first.Status, HookInstalled)
	}
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("hook mode = %o, want executable", info.Mode().Perm())
	}
	before, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}

	second, err := repo.InstallHooks(context.Background())
	if err != nil {
		t.Fatalf("InstallHooks(second) error = %v", err)
	}
	if second.Status != HookUnchanged || second.Path != first.Path {
		t.Fatalf("InstallHooks(second) = %#v, want unchanged at %q", second, first.Path)
	}
	after, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("idempotent install rewrote hook contents")
	}
}

func TestInstallHooksUpdatesOlderManagedHook(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(repo.CommonGitDir, "hooks", "pre-push")
	older := "#!/bin/sh\n" + managedPrePushMarker + "\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := repo.InstallHooks(context.Background())
	if err != nil {
		t.Fatalf("InstallHooks(older managed) error = %v", err)
	}
	if result.Status != HookInstalled {
		t.Fatalf("InstallHooks(older managed).Status = %q, want %q", result.Status, HookInstalled)
	}
	contents, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != managedPrePushHook {
		t.Fatalf("updated hook = %q, want current managed hook", contents)
	}
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("updated hook mode = %o, want executable", info.Mode().Perm())
	}
}

func TestManagedPrePushHookPublishesOnlyOriginAndHonorsRecursionGuard(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := repo.InstallHooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls")
	fakeWorkbook := filepath.Join(bin, "workbook")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$WORKBOOK_TEST_LOG\"\nexit \"${WORKBOOK_TEST_EXIT:-0}\"\n"
	if err := os.WriteFile(fakeWorkbook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKBOOK_TEST_LOG", logPath)

	runHook(t, repo.Root, installed.Path, nil, "upstream", "unused")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("non-origin hook invoked workbook; stat error = %v", err)
	}

	runHook(t, repo.Root, installed.Path, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, "origin", "unused")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("guarded hook invoked workbook; stat error = %v", err)
	}

	runHook(t, repo.Root, installed.Path, nil, "origin", "unused")
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "push\n"; got != want {
		t.Fatalf("origin hook workbook calls = %q, want %q", got, want)
	}
}

func TestManagedPrePushHookBlocksPushWhenWorkbookPushFails(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := repo.InstallHooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeWorkbook := filepath.Join(bin, "workbook")
	if err := os.WriteFile(fakeWorkbook, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	command := exec.Command(installed.Path, "origin", "unused")
	command.Dir = repo.Root
	err = command.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 9 {
		t.Fatalf("hook failure = %v, want exit 9", err)
	}
}

func TestInstallHooksPreservesUnmanagedHookAndGivesChainingGuidance(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(repo.CommonGitDir, "hooks", "pre-push")
	original := []byte("#!/bin/sh\nprintf existing\n")
	if err := os.WriteFile(hookPath, original, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := repo.InstallHooks(context.Background())
	if err == nil {
		t.Fatalf("InstallHooks(unmanaged) error = nil; result = %#v", result)
	}
	if !strings.Contains(err.Error(), "chain") || !strings.Contains(err.Error(), "workbook push") {
		t.Fatalf("InstallHooks(unmanaged) error = %q, want chaining guidance", err)
	}
	after, readErr := os.ReadFile(hookPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(original) {
		t.Fatalf("unmanaged hook changed to %q", after)
	}
}

func runHook(t *testing.T, directory, path string, env []string, args ...string) {
	t.Helper()
	command := exec.Command(path, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), env...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook %v: %v\n%s", args, err, output)
	}
}
