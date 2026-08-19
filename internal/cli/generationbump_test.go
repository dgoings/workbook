package cli

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
)

// buildPatchedGenerationZeroBinary builds this source tree with
// SupportedFormatGeneration set back to zero, which is the reader every clone
// running the build before generation 1 is.
//
// There are two ways to get such a binary and this repository now uses both.
// The assignment work compiled one out of a `git archive` of the default
// branch, checking that the extracted tree really still folded generation zero
// — a genuinely older build, and one that retires itself: now that the bump is
// on main, that helper finds no qualifying revision and its tests skip. This
// one patches a copy of the current tree instead, so it keeps running after the
// bump lands and does not depend on which refs a checkout happens to have.
//
// LIMITATION, stated because it cost a review to find. This binary is not a
// previous build; it is this build with one constant moved. Everything else in
// it is this branch's code, including the tree parser — so it tolerates a tree
// entry that a genuinely older parser would have called corrupt, and it can
// therefore never fail on the axis where tree shape and the marker interact.
// That is exactly the axis a reviewer found a real hole on, by building the
// merge base instead. Anything this proxy might seem to say about tree shape is
// consequently worth nothing; what it does prove is the marker's own behavior —
// refuse, advise, keep synchronizing — because the constant is the entire gate
// for that: a reader refuses a pack whose marker exceeds this value before it
// looks at anything inside it. The tree-shape contract is pinned in
// internal/gitstore/treeshape_test.go against forged commits, where the reader
// under test is the real one.
func buildPatchedGenerationZeroBinary(t *testing.T) string {
	t.Helper()
	return buildPatchedGenerationBinary(t, 0)
}

// buildPatchedGenerationBinary builds this tree with SupportedFormatGeneration
// pinned to a generation this build has since left behind.
//
// It is parameterized because there is more than one older reader worth
// standing up now. Generation zero is every clone from before assignments;
// generation one is every clone running v0.5.0, which is the reader a project
// that records a display setting has to keep working for. The same LIMITATION
// stated above applies to both, and for the same reason: this is not a previous
// build, it is this build with one constant moved, so what it proves is the
// marker's own behavior and nothing about tree shape.
func buildPatchedGenerationBinary(t *testing.T, generation int) string {
	t.Helper()
	root := repositoryRoot(t)
	staging := t.TempDir()
	for _, tree := range []string{"cmd", "internal", "skills"} {
		copyTreeForBuild(t, filepath.Join(root, tree), filepath.Join(staging, tree))
	}
	for _, file := range []string{"go.mod", "go.sum"} {
		copyFileForBuild(t, filepath.Join(root, file), filepath.Join(staging, file))
	}

	operationPath := filepath.Join(staging, "internal", "core", "operation.go")
	source, err := os.ReadFile(operationPath)
	if err != nil {
		t.Fatalf("read %s: %v", operationPath, err)
	}
	const marker = "const SupportedFormatGeneration = "
	index := strings.Index(string(source), marker)
	if index < 0 {
		t.Fatal("SupportedFormatGeneration is no longer declared where this test patches it")
	}
	end := strings.Index(string(source[index:]), "\n") + index
	patched := string(source[:index]) + marker + strconv.Itoa(generation) + string(source[end:])
	if patched == string(source) {
		t.Fatalf("the generation is already %d; there is no bump for this test to cross", generation)
	}
	if err := os.WriteFile(operationPath, []byte(patched), 0o644); err != nil {
		t.Fatalf("write %s: %v", operationPath, err)
	}

	binary := filepath.Join(t.TempDir(), "workbook-generation-"+strconv.Itoa(generation))
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, "./cmd/workbook")
	command.Dir = staging
	if len(toolchainEnvironment) == 0 {
		t.Fatal("toolchainEnvironment is empty; TestMain must record it before replacing HOME")
	}
	command.Env = toolchainEnvironment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build (generation %d): %v\n%s", generation, err, output)
	}
	return binary
}

func copyTreeForBuild(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", from, err)
	}
}

func copyFileForBuild(t *testing.T, from, to string) {
	t.Helper()
	contents, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	if err := os.WriteFile(to, contents, 0o644); err != nil {
		t.Fatalf("write %s: %v", to, err)
	}
}

// commentThroughTheService adds a comment to a task in the named repository.
//
// The command line does not carry comments yet — that is the next pull request
// — so this is how a generation-one history gets written in a test of the
// generation-zero reader. The pack it produces is the pack the flag will
// produce, because the flag will call this method.
func commentThroughTheService(t *testing.T, repository, taskID, body string) core.Task {
	t.Helper()
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	store, err := projection.Open(ctx, repo, config)
	if err != nil {
		t.Fatalf("open projection: %v", err)
	}
	actor, err := repo.Actor(ctx)
	if err != nil {
		t.Fatalf("read actor: %v", err)
	}
	vocabulary, err := repo.LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("load vocabulary: %v", err)
	}
	service := core.Service{
		Config: config, Vocabulary: vocabulary, Reader: store, Writer: repo, Blobs: repo,
		Projection: store, History: store, IDs: core.CryptoULIDSource{}, Now: time.Now, Actor: actor,
	}
	result, err := service.CommentAddMutation(ctx, taskID, core.CommentAddInput{Body: body})
	if err != nil {
		t.Fatalf("CommentAddMutation() error = %v", err)
	}
	return result.Task
}

// The whole older-reader contract, against a real generation-zero process and a
// real comment written by this build.
//
// Everything asserted here is what the writer-format marker was built to buy,
// now that something actually sets it: the older clone reads the commented task
// from its checkpoint and says so, refuses to change it with the upgrade
// message rather than with a corruption report, keeps synchronizing everything
// else, and loses nothing.
func TestAGenerationZeroReaderRefusesACommentedHistoryAsNewerWriter(t *testing.T) {
	binary := buildPatchedGenerationZeroBinary(t)
	writer, older := cliSyncRepositories(t)

	fetched := cliCreateTask(t, writer, "Task that will be commented")
	diverged := cliCreateTask(t, writer, "Task the older clone also changed")
	ordinary := cliCreateTask(t, writer, "Ordinary task")
	if code, _, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync code = %d; stderr = %q", code, stderr)
	}
	// The older clone starts from a history it understands completely.
	if code, _, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone initial sync code = %d; stderr = %q", code, stderr)
	}
	// One local, unpublished change on a task the comment is about to land on.
	// This is the hard case: publishing it would mean replaying onto a history
	// this build cannot read.
	if code, _, stderr := runBinary(t, binary, older, "update", diverged.ID, "--title", "Renamed locally", "--no-sync"); code != 0 {
		t.Fatalf("older clone local update code = %d; stderr = %q", code, stderr)
	}
	localDivergedHead := cliGitOutput(t, older, "rev-parse", "refs/workbook/tasks/"+diverged.ID)

	commentThroughTheService(t, writer, fetched.ID, "this needs a newer workbook to fold")
	commentThroughTheService(t, writer, diverged.ID, "and so does this one")
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}

	// The stored pack carries the marker, and the checkpoint carries the
	// watermark. Everything below follows from these two bytes.
	head := cliGitOutput(t, writer, "rev-parse", "refs/workbook/tasks/"+fetched.ID)
	operation := cliGitOutput(t, writer, "show", head+":operation.json")
	if !strings.Contains(operation, `"minReader":1`) {
		t.Fatalf("the comment pack carries no marker: %s", operation)
	}
	state := cliGitOutput(t, writer, "show", head+":state.json")
	if !strings.Contains(state, `"minReader":1`) {
		t.Fatalf("the checkpoint carries no watermark: %s", state)
	}
	// This build audits the history it just wrote, replaying every commit
	// rather than trusting a checkpoint.
	if code, _, stderr := run(t, writer, "validate", "--full"); code != 0 {
		t.Fatalf("validate on the writing clone code = %d, want 0; stderr = %q", code, stderr)
	}

	// Synchronization advances what it can and refuses what it must, in one
	// run, with the upgrade signal rather than a corruption report.
	code, stdout, stderr := runBinary(t, binary, older, "sync", "--json")
	if code != 9 {
		t.Fatalf("older clone sync code = %d, want 9; stdout = %q stderr = %q", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	if !strings.Contains(stderr, "newer workbook") {
		t.Fatalf("older clone sync error = %q, want it to name a newer workbook", stderr)
	}
	// Which is emphatically not a corruption report.
	if strings.Contains(strings.ToLower(stderr), "corrupt") {
		t.Fatalf("older clone reported corruption: %q", stderr)
	}
	// The untouched task's ref advanced to origin's tip: a fetch that cannot
	// fold a task must not leave the clone behind on it.
	remote := cliGitOutput(t, writer, "rev-parse", "refs/workbook/tasks/"+fetched.ID)
	if got := cliGitOutput(t, older, "rev-parse", "refs/workbook/tasks/"+fetched.ID); got != remote {
		t.Fatalf("older clone task ref = %q, want origin's tip %q", got, remote)
	}
	// And the divergent task kept its local operation rather than losing it to
	// a replay this build could not perform.
	if got := cliGitOutput(t, older, "rev-parse", "refs/workbook/tasks/"+diverged.ID); got != localDivergedHead {
		t.Fatalf("divergent task ref = %q, want the local head %q; local work must be preserved",
			got, localDivergedHead)
	}
	// The ordinary task published in the same run, so one refusal did not wedge
	// synchronization.
	if got := cliGitOutput(t, writer, "ls-remote", "origin", "refs/workbook/tasks/"+ordinary.ID); !strings.Contains(got, ordinary.ID) {
		t.Fatalf("origin does not hold the ordinary task: %q", got)
	}

	// Reads serve the task from its checkpoint, with the advisory.
	code, stdout, stderr = runBinary(t, binary, older, "show", fetched.ID, "--json")
	if code != 0 {
		t.Fatalf("older clone show code = %d, want 0; stderr = %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "show")
	var shown core.Task
	if err := json.Unmarshal(envelope.Data, &shown); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if !shown.NewerWriter {
		t.Fatal("the older clone does not report the task as written by a newer workbook")
	}
	if shown.Title != fetched.Title {
		t.Fatalf("shown title = %q, want the checkpoint's %q", shown.Title, fetched.Title)
	}
	if len(envelope.Warnings) == 0 || envelope.Warnings[0].Code != core.WarningNewerWriter {
		t.Fatalf("show warnings = %#v, want a %q advisory", envelope.Warnings, core.WarningNewerWriter)
	}

	// A mutation of that task is refused with the upgrade message and exit 9.
	code, stdout, stderr = runBinary(t, binary, older, "update", fetched.ID, "--title", "Renamed", "--json")
	if code != 9 {
		t.Fatalf("older clone update code = %d, want 9; stdout = %q stderr = %q", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	if !strings.Contains(stderr, "upgrade workbook") {
		t.Fatalf("older clone update error = %q, want it to say to upgrade", stderr)
	}

	// `validate` reports the same way rather than auditing a history it could
	// not fold and calling the result corruption.
	code, stdout, stderr = runBinary(t, binary, older, "validate", "--json")
	if code != 9 {
		t.Fatalf("older clone validate code = %d, want 9; stdout = %q stderr = %q", code, stdout, stderr)
	}

	// And every other task is untouched by any of it.
	if code, _, stderr := runBinary(t, binary, older, "update", ordinary.ID, "--title", "Still editable", "--json"); code != 0 {
		t.Fatalf("older clone update of the ordinary task code = %d, want 0; stderr = %q", code, stderr)
	}
}

// The other half of the contract, and the reason the marker is per operation
// type: a generation-zero reader keeps folding everything this build writes
// that does not use the new semantics.
func TestAGenerationZeroReaderStillFoldsOrdinaryHistory(t *testing.T) {
	binary := buildPatchedGenerationZeroBinary(t)
	writer, older := cliSyncRepositories(t)

	task := cliCreateTask(t, writer, "Ordinary task")
	for _, command := range [][]string{
		{"update", task.ID, "--title", "Renamed", "--description", "Prose", "--label", "storage"},
		{"update", task.ID, "--status", "in-progress"},
		{"delete", task.ID},
		{"restore", task.ID, "--into", "done"},
	} {
		if code, _, stderr := run(t, writer, append(command, "--json")...); code != 0 {
			t.Fatalf("%v code = %d; stderr = %q", command, code, stderr)
		}
	}
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}

	if code, _, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone sync code = %d, want 0; stderr = %q", code, stderr)
	}
	// `validate` folds every commit rather than trusting checkpoints, so it is
	// the strongest statement available that nothing this build wrote needs a
	// newer reader.
	if code, _, stderr := runBinary(t, binary, older, "validate", "--full"); code != 0 {
		t.Fatalf("older clone validate code = %d, want 0; stderr = %q", code, stderr)
	}
	if code, _, stderr := runBinary(t, binary, older, "update", task.ID, "--priority", "high"); code != 0 {
		t.Fatalf("older clone update code = %d, want 0; stderr = %q", code, stderr)
	}
}
