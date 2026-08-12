package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	createValidationTask(t, repository, "second")
	cachePath := validationCachePath(t, repository)

	fresh := runValidationJSON(t, repository)
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
	// A project minted by this build carries a configuration genesis, so the
	// ledger audit is part of an ordinary validate rather than something only a
	// project that customized its statuses ever sees.
	if fresh.Config == nil || !fresh.Config.Valid || fresh.Config.CommitsChecked != 1 {
		t.Fatalf("config audit = %#v, want the genesis this project was minted with", fresh.Config)
	}

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

	t.Run("schema and result assertions reject omitted or wrong fields", func(t *testing.T) {
		// Production mutation: adding omitempty to a zero-valued Result field or
		// changing CachePath would silently break machine consumers without these
		// raw-envelope and field-by-field checks.
		encoded, err := json.Marshal(fresh)
		if err != nil {
			t.Fatalf("encode fresh result: %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatalf("decode fresh result fields: %v", err)
		}
		delete(fields, "full")
		missingZeroField, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("encode missing field witness: %v", err)
		}
		if err := validationResultSchemaError(missingZeroField); err == nil {
			t.Fatal("schema accepted a result without zero-valued full field")
		}

		want := historyvalidation.Result{CachePath: cachePath}
		blankCachePath := want
		blankCachePath.CachePath = ""
		if validationResultDifference(blankCachePath, want) == "" {
			t.Fatal("result comparison accepted a blank cache path")
		}
		wrongCachePath := want
		wrongCachePath.CachePath = filepath.Join(repository, "wrong.sqlite")
		if validationResultDifference(wrongCachePath, want) == "" {
			t.Fatal("result comparison accepted a wrong cache path")
		}
	})
}

func TestValidateFullBypassesWarmCacheAndChecksCompleteHistory(t *testing.T) {
	// Production mutation: always passing false to Validator.Validate makes
	// --full reuse cache hits instead of auditing every complete history.
	repository := initializedRepository(t)
	first := createValidationTask(t, repository, "first")
	second := createValidationTask(t, repository, "second")
	updateValidationTask(t, repository, first.ID, "first updated")
	updateValidationTask(t, repository, second.ID, "second updated")
	cachePath := validationCachePath(t, repository)

	warm := runValidationJSON(t, repository)
	assertValidationResult(t, warm, historyvalidation.Result{
		ValidatorVersion: historyvalidation.ValidatorVersion,
		Full:             false,
		TaskCount:        2,
		TasksChecked:     2,
		CommitsChecked:   4,
		CacheHits:        0,
		Valid:            2,
		Invalid:          0,
		Pending:          0,
		CachePath:        cachePath,
	})

	code, stdout, stderr := run(t, repository, "validate", "--full", "--json")
	if code != 0 {
		t.Fatalf("validate --full --json code = %d; stderr = %q", code, stderr)
	}
	full := decodeValidationResult(t, stdout)
	assertValidationResult(t, full, historyvalidation.Result{
		ValidatorVersion: historyvalidation.ValidatorVersion,
		Full:             true,
		TaskCount:        2,
		TasksChecked:     2,
		CommitsChecked:   4,
		CacheHits:        0,
		Valid:            2,
		Invalid:          0,
		Pending:          0,
		CachePath:        cachePath,
	})
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
			"Invalid " + first.ID + " at " + firstHead + " [corrupt-data]: stored checkpoint differs from computed state\n" +
			"Configuration ledger: 1 commit(s) checked; valid.\n"
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
	if strings.Contains(string(logged), "rev-list --reverse --topo-order --parents --stdin") {
		t.Fatalf("cached invalid Git commands = %q, want no history walk", logged)
	}
	// One object batch is expected and bounded: reading the canonical project
	// identity, which every command does before it touches a task. What must
	// not happen is a batch after task work begins, because that is the history
	// read the cache hit exists to avoid.
	assertNoObjectBatchAfterTaskWork(t, string(logged))
}

// assertNoObjectBatchAfterTaskWork checks the window this cache hit is about:
// between the first task-ref enumeration and the configuration ledger's own,
// there must be no object batch.
//
// The ledger audit is bounded by its own history and has no task cache to hit,
// so its object batch is expected — a project minted by this build has a genesis
// from its first moment, and `validate` reads it every run. What must not appear
// is a batch inside the task window, because that is the history read the cache
// hit exists to avoid.
//
// The window is stated as a pair of boundaries rather than as a flag that a
// config-ref line clears. A cleared flag reads the same for the log this command
// produces today and for a log where the ledger audit ran first and the task
// batch came after it — the second is precisely the regression this guards, and
// it would pass. Bounding the window keeps the rule armed however the stages are
// ordered: a task batch outside the window is a batch after the config read,
// which is a different check's business, and a config read that never happens
// leaves the window open to the end of the log.
func assertNoObjectBatchAfterTaskWork(t *testing.T, logged string) {
	t.Helper()
	lines := strings.Split(logged, "\n")
	start := -1
	end := len(lines)
	for index, line := range lines {
		if start < 0 && strings.Contains(line, "refs/workbook/tasks/") {
			start = index
			continue
		}
		if strings.Contains(line, "refs/workbook/config") {
			end = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("cached invalid Git commands = %q, want a task ref enumeration to bound the window", logged)
	}
	for _, line := range lines[start:min(end, len(lines))] {
		if strings.Contains(line, "cat-file --batch") {
			t.Fatalf("cached invalid Git commands = %q, want no history batch once task work began", logged)
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
	if err := validationResultSchemaError(resultDocument.Data); err != nil {
		t.Fatalf("validation JSON result schema: %v; data = %s", err, resultDocument.Data)
	}
	var result historyvalidation.Result
	if err := json.Unmarshal(resultDocument.Data, &result); err != nil {
		t.Fatalf("decode validation result: %v; data = %s", err, resultDocument.Data)
	}
	return result
}

func assertValidationResult(t *testing.T, got, want historyvalidation.Result) {
	t.Helper()
	if difference := validationResultDifference(got, want); difference != "" {
		t.Fatalf("validation result %s: got %#v, want %#v", difference, got, want)
	}
}

func validationResultDifference(got, want historyvalidation.Result) string {
	switch {
	case got.ValidatorVersion != want.ValidatorVersion:
		return "validatorVersion differs"
	case got.Full != want.Full:
		return "full differs"
	case got.TaskCount != want.TaskCount:
		return "taskCount differs"
	case got.TasksChecked != want.TasksChecked:
		return "tasksChecked differs"
	case got.CommitsChecked != want.CommitsChecked:
		return "commitsChecked differs"
	case got.CacheHits != want.CacheHits:
		return "cacheHits differs"
	case got.Valid != want.Valid:
		return "valid differs"
	case got.Invalid != want.Invalid:
		return "invalid differs"
	case got.Pending != want.Pending:
		return "pending differs"
	case got.CachePath != want.CachePath:
		return "cachePath differs"
	case !equalFailures(got.Failures, want.Failures):
		return "failures differs"
	default:
		return ""
	}
}

var validationResultJSONKeys = map[string]struct{}{
	"validatorVersion": {},
	"full":             {},
	"taskCount":        {},
	"tasksChecked":     {},
	"commitsChecked":   {},
	"cacheHits":        {},
	"valid":            {},
	"invalid":          {},
	"pending":          {},
	"cachePath":        {},
	"failures":         {},
}

// validationResultOptionalJSONKeys are members a result carries only when the
// repository has the thing they report on. `config` is one: a project with no
// configuration ledger has no ledger audit, and one minted by this build has a
// genesis from its first moment and therefore always does.
var validationResultOptionalJSONKeys = map[string]struct{}{
	"config": {},
}

func validationResultSchemaError(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode result object: %w", err)
	}
	for key := range validationResultJSONKeys {
		if _, found := fields[key]; !found {
			return fmt.Errorf("missing key %q", key)
		}
	}
	for key := range fields {
		_, expected := validationResultJSONKeys[key]
		_, optional := validationResultOptionalJSONKeys[key]
		if !expected && !optional {
			return fmt.Errorf("unexpected key %q", key)
		}
	}
	return nil
}

func validationCachePath(t *testing.T, repository string) string {
	t.Helper()
	commonGitDir := gitOutput(t, repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return filepath.Join(commonGitDir, "workbook", "validation.sqlite")
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
