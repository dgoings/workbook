package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/historyvalidation"
)

func TestValidateRejectsPositionalsAndUnknownFlags(t *testing.T) {
	// Production mutation: accepting parser leftovers lets validate silently audit
	// a different scope than the caller requested.
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{"validate", "--json", "unexpected"},
		{"validate", "--unknown", "--json"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 2 {
			t.Fatalf("Run(%q) code = %d, want 2; stderr = %q", args, code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run(%q) stdout = %q, want empty", args, stdout)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, "")
	}
}

func TestValidateJSONReportsFreshCachedAndIncrementalCounts(t *testing.T) {
	// Production mutation: ignoring cache boundaries or changed heads makes the
	// audit replay complete histories and report misleading JSON accounting.
	repository := initializedRepository(t)
	first := createValidationTask(t, repository, "first")
	second := createValidationTask(t, repository, "second")

	fresh := runValidationJSON(t, repository)
	cachePath := fresh.CachePath
	if cachePath == "" || !strings.HasSuffix(cachePath, filepath.Join("workbook", "validation.sqlite")) {
		t.Fatalf("fresh cache path = %q, want shared validation cache", cachePath)
	}
	assertValidationResult(t, fresh, historyvalidation.Result{
		ValidatorVersion: historyvalidation.ValidatorVersion,
		Full:             false,
		TaskCount:        2,
		TasksChecked:     2,
		CommitsChecked:   2,
		CacheHits:        0,
		Valid:            2,
		Invalid:          0,
		Pending:          0,
		CachePath:        cachePath,
	})

	cached := runValidationJSON(t, repository)
	assertValidationResult(t, cached, historyvalidation.Result{
		ValidatorVersion: historyvalidation.ValidatorVersion,
		Full:             false,
		TaskCount:        2,
		TasksChecked:     0,
		CommitsChecked:   0,
		CacheHits:        2,
		Valid:            2,
		Invalid:          0,
		Pending:          0,
		CachePath:        cachePath,
	})

	updateValidationTask(t, repository, first.ID, "first updated")
	incremental := runValidationJSON(t, repository)
	assertValidationResult(t, incremental, historyvalidation.Result{
		ValidatorVersion: historyvalidation.ValidatorVersion,
		Full:             false,
		TaskCount:        2,
		TasksChecked:     1,
		CommitsChecked:   1,
		CacheHits:        1,
		Valid:            2,
		Invalid:          0,
		Pending:          0,
		CachePath:        cachePath,
	})
	if second.ID == "" {
		t.Fatal("second task ID is empty")
	}
}

func TestValidateHumanOutputListsEveryFailureInTaskOrder(t *testing.T) {
	// Production mutation: dropping cached failures or iterating a map directly
	// hides corruption or produces unstable human audit output.
	t.Run("summary and exact failure", func(t *testing.T) {
		repository := initializedRepository(t)
		first := createValidationTask(t, repository, "first")
		second := createValidationTask(t, repository, "second")
		updateValidationTask(t, repository, first.ID, "first updated")
		updateValidationTask(t, repository, second.ID, "second updated")
		firstHead := corruptValidationCheckpoint(t, repository, first.ID)

		code, stdout, stderr := run(t, repository, "validate")
		if code != 7 {
			t.Fatalf("validate code = %d, want 7; stdout = %q; stderr = %q", code, stdout, stderr)
		}
		want := "Validated 2 task(s): 4 commit(s) checked, 0 cache hit(s); 1 valid, 1 invalid, 0 pending.\n" +
			"Invalid " + first.ID + " at " + firstHead + " [corrupt-data]: stored checkpoint differs from computed state\n"
		if stdout != want {
			t.Fatalf("validate stdout = %q, want %q", stdout, want)
		}
		assertHumanError(t, stderr, "semantic history validation found 1 invalid task(s)")
	})

	t.Run("every failure is task ordered", func(t *testing.T) {
		repository := initializedRepository(t)
		first := createValidationTask(t, repository, "first")
		second := createValidationTask(t, repository, "second")
		firstHead := corruptValidationCheckpoint(t, repository, first.ID)
		secondHead := corruptValidationCheckpoint(t, repository, second.ID)

		code, stdout, stderr := run(t, repository, "validate")
		if code != 7 {
			t.Fatalf("validate code = %d, want 7; stdout = %q; stderr = %q", code, stdout, stderr)
		}
		pairs := []struct{ id, head string }{{first.ID, firstHead}, {second.ID, secondHead}}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].id < pairs[j].id })
		previous := 0
		for _, pair := range pairs {
			line := "Invalid " + pair.id + " at " + pair.head + " [corrupt-data]: stored checkpoint differs from computed state\n"
			index := strings.Index(stdout, line)
			if index < previous {
				t.Fatalf("validate stdout = %q, want ordered exact failure %q", stdout, line)
			}
			previous = index
		}
		assertHumanError(t, stderr, "semantic history validation found 2 invalid task(s)")
	})
}

func TestValidateJSONWritesResultAndErrorOnInvalidHistory(t *testing.T) {
	// Production mutation: returning before output loses the machine-readable
	// failure inventory that callers need despite the nonzero corrupt-data exit.
	repository := initializedRepository(t)
	task := createValidationTask(t, repository, "broken")
	head := corruptValidationCheckpoint(t, repository, task.ID)

	code, stdout, stderr := run(t, repository, "validate", "--json")
	if code != 7 {
		t.Fatalf("validate --json code = %d, want 7; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	result := decodeValidationResult(t, stdout)
	assertValidationResult(t, result, historyvalidation.Result{
		ValidatorVersion: historyvalidation.ValidatorVersion,
		Full:             false,
		TaskCount:        1,
		TasksChecked:     1,
		CommitsChecked:   1,
		CacheHits:        0,
		Valid:            0,
		Invalid:          1,
		Pending:          0,
		CachePath:        result.CachePath,
		Failures: []historyvalidation.Failure{{
			TaskID: task.ID, Commit: head, Category: "corrupt-data", Message: "stored checkpoint differs from computed state",
		}},
	})
	assertJSONError(t, stderr, core.CategoryCorruptData, "semantic history validation found 1 invalid task(s)")
}

func TestValidateCachedInvalidHeadStillExitsNonzeroWithoutHistoryBatch(t *testing.T) {
	// Production mutation: rereading unchanged invalid heads wastes bounded Git
	// work and risks replacing a previously reported exact failure.
	repository := initializedRepository(t)
	task := createValidationTask(t, repository, "broken")
	corruptValidationCheckpoint(t, repository, task.ID)
	code, _, _ := run(t, repository, "validate", "--json")
	if code != 7 {
		t.Fatalf("initial validate code = %d, want 7", code)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	wrapperDir := t.TempDir()
	logPath := filepath.Join(wrapperDir, "git.log")
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$WORKBOOK_VALIDATE_GIT_LOG\"\nexec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("WORKBOOK_VALIDATE_GIT_LOG", logPath)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	code, stdout, stderr := run(t, repository, "validate", "--json")
	if code != 7 {
		t.Fatalf("cached validate code = %d, want 7; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	result := decodeValidationResult(t, stdout)
	if result.TasksChecked != 0 || result.CommitsChecked != 0 || result.CacheHits != 1 || result.Invalid != 1 {
		t.Fatalf("cached invalid result = %#v, want one cached invalid result without reads", result)
	}
	assertJSONError(t, stderr, core.CategoryCorruptData, "semantic history validation found 1 invalid task(s)")
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read Git wrapper log: %v", err)
	}
	for _, forbidden := range []string{"rev-list --reverse --topo-order --parents --stdin", "cat-file --batch"} {
		if strings.Contains(string(logged), forbidden) {
			t.Fatalf("cached invalid Git commands = %q, want no history batch %q", logged, forbidden)
		}
	}
}

func createValidationTask(t *testing.T, repository, title string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--json")
	if code != 0 {
		t.Fatalf("create %q code = %d; stderr = %q", title, code, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

func updateValidationTask(t *testing.T, repository, id, title string) {
	t.Helper()
	code, _, stderr := run(t, repository, "update", id, "--title", title, "--json")
	if code != 0 {
		t.Fatalf("update %q code = %d; stderr = %q", id, code, stderr)
	}
}

func runValidationJSON(t *testing.T, repository string) historyvalidation.Result {
	t.Helper()
	code, stdout, stderr := run(t, repository, "validate", "--json")
	if code != 0 {
		t.Fatalf("validate --json code = %d; stderr = %q", code, stderr)
	}
	return decodeValidationResult(t, stdout)
}

func decodeValidationResult(t *testing.T, stdout string) historyvalidation.Result {
	t.Helper()
	resultDocument := assertJSONResult(t, stdout, "validate")
	var result historyvalidation.Result
	if err := json.Unmarshal(resultDocument.Data, &result); err != nil {
		t.Fatalf("decode validation result: %v; data = %s", err, resultDocument.Data)
	}
	return result
}

func assertValidationResult(t *testing.T, got, want historyvalidation.Result) {
	t.Helper()
	if got.ValidatorVersion != want.ValidatorVersion || got.Full != want.Full || got.TaskCount != want.TaskCount ||
		got.TasksChecked != want.TasksChecked || got.CommitsChecked != want.CommitsChecked || got.CacheHits != want.CacheHits ||
		got.Valid != want.Valid || got.Invalid != want.Invalid || got.Pending != want.Pending || !equalFailures(got.Failures, want.Failures) {
		t.Fatalf("validation result = %#v, want %#v", got, want)
	}
}

func equalFailures(left, right []historyvalidation.Failure) bool {
	return bytes.Equal(mustMarshalFailures(left), mustMarshalFailures(right))
}

func mustMarshalFailures(failures []historyvalidation.Failure) []byte {
	encoded, err := json.Marshal(failures)
	if err != nil {
		panic(err)
	}
	return encoded
}

func corruptValidationCheckpoint(t *testing.T, repository, taskID string) string {
	t.Helper()
	ctx := context.Background()
	head := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+taskID)
	stateBytes := []byte(gitOutput(t, repository, "show", head+":state.json"))
	state, err := core.DecodeStateDocument(stateBytes)
	if err != nil {
		t.Fatalf("decode stored checkpoint: %v", err)
	}
	state.Task.Title += " tampered"
	encodedState, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("encode tampered checkpoint: %v", err)
	}
	operationBlob := gitOutput(t, repository, "rev-parse", head+":operation.json")
	stateBlob := gitInput(t, repository, encodedState, "hash-object", "-w", "--stdin")
	tree := gitInput(t, repository, []byte("100644 blob "+operationBlob+"\toperation.json\n100644 blob "+stateBlob+"\tstate.json\n"), "mktree")
	parentFields := strings.Fields(gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", head))
	commitArgs := []string{"commit-tree", tree}
	for _, parent := range parentFields[1:] {
		commitArgs = append(commitArgs, "-p", parent)
	}
	commit := gitInput(t, repository, nil, commitArgs...)
	_ = ctx
	gitInput(t, repository, nil, "update-ref", "refs/workbook/tasks/"+taskID, commit, head)
	return commit
}

func gitInput(t *testing.T, repository string, input []byte, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
