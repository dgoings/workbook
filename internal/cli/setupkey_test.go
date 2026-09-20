package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestPromptProjectKeyTakesTheSuggestionOnEnter(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(strings.NewReader("\n"), &out, "ACME")
	if err != nil {
		t.Fatalf("promptProjectKey() error = %v", err)
	}
	if key != "ACME" {
		t.Fatalf("promptProjectKey() = %q, want the suggestion %q", key, "ACME")
	}
	if !strings.Contains(out.String(), "Project key [ACME]: ") {
		t.Fatalf("prompt %q does not offer the suggestion", out.String())
	}
}

func TestPromptProjectKeyTakesTheSuggestionAtEndOfInput(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(strings.NewReader(""), &out, "ACME")
	if err != nil {
		t.Fatalf("promptProjectKey() error = %v", err)
	}
	if key != "ACME" {
		t.Fatalf("promptProjectKey() = %q, want the suggestion %q", key, "ACME")
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("prompt %q left the report on the prompt's line", out.String())
	}
}

func TestPromptProjectKeyUppercasesAndTrimsTheAnswer(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(strings.NewReader("  myapp \n"), &out, "ACME")
	if err != nil {
		t.Fatalf("promptProjectKey() error = %v", err)
	}
	if key != "MYAPP" {
		t.Fatalf("promptProjectKey() = %q, want %q", key, "MYAPP")
	}
}

func TestPromptProjectKeyExplainsAndAsksAgain(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(strings.NewReader("1bad\nok\n"), &out, "ACME")
	if err != nil {
		t.Fatalf("promptProjectKey() error = %v", err)
	}
	if key != "OK" {
		t.Fatalf("promptProjectKey() = %q, want %q", key, "OK")
	}
	if got := strings.Count(out.String(), "Project key [ACME]: "); got != 2 {
		t.Fatalf("prompt asked %d times, want 2:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), `project key "1BAD" must match`) {
		t.Fatalf("prompt %q does not say why the first answer was refused", out.String())
	}
}

func TestPromptProjectKeyGivesUpAfterRepeatedBadAnswers(t *testing.T) {
	var out bytes.Buffer
	_, err := promptProjectKey(strings.NewReader(strings.Repeat("1\n", projectKeyAttempts+3)), &out, "ACME")
	if core.CategoryOf(err) != core.CategoryInvocation {
		t.Fatalf("promptProjectKey() error = %v, want an invocation failure", err)
	}
	if got := strings.Count(out.String(), "Project key [ACME]: "); got != projectKeyAttempts {
		t.Fatalf("prompt asked %d times, want %d", got, projectKeyAttempts)
	}
	if !strings.Contains(err.Error(), "--key") {
		t.Fatalf("error %q does not name the flag that avoids the prompt", err)
	}
}

func TestInteractiveTerminalIsFalseForBuffers(t *testing.T) {
	if interactiveTerminal(strings.NewReader(""), &bytes.Buffer{}) {
		t.Fatal("interactiveTerminal() = true for a reader and a buffer")
	}
}

func TestSetupDerivesTheKeyFromTheDirectoryWhenNothingAsks(t *testing.T) {
	repository := testrepo.New(t, testrepo.WithName("acme-site"))

	// The typed answer proves a piped stdin is never read as a prompt answer.
	code, stdout, stderr := runWithInput(t, repository, strings.NewReader("TYPED\n"), "setup", "--no-docs")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Key:\tACMESITE\n") {
		t.Fatalf("setup stdout = %q, want the key derived from the directory", stdout)
	}
	if strings.Contains(stdout, "Project key [") {
		t.Fatalf("setup prompted without a terminal:\n%s", stdout)
	}

	code, stdout, stderr = run(t, repository, "create", "First task", "--json", "--no-sync")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	if task := decodeMutationTask(t, stdout, "create"); !strings.HasPrefix(task.ID, "ACMESITE-") {
		t.Fatalf("task ID = %q, want the derived key as its prefix", task.ID)
	}
}

func TestSetupJSONTakesTheDerivedKeyWithoutAsking(t *testing.T) {
	repository := testrepo.New(t, testrepo.WithName("my_app"))

	code, stdout, stderr := run(t, repository, "setup", "--no-docs", "--json")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	var result setupResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "setup").Data, &result); err != nil {
		t.Fatalf("decode setup result: %v", err)
	}
	if result.Key != "MYAPP" {
		t.Fatalf("setup key = %q, want %q", result.Key, "MYAPP")
	}
}

func TestSetupFallsBackToTheDefaultKeyForAMeaninglessDirectory(t *testing.T) {
	repository := testrepo.New(t, testrepo.WithName("2024"))

	code, stdout, stderr := run(t, repository, "setup", "--no-docs")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Key:\t"+core.DefaultProjectKey+"\n") {
		t.Fatalf("setup stdout = %q, want the default key", stdout)
	}
}

func TestSetupJoinsAnExistingProjectWithoutNamingItsKey(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--key", "PROJ", "--no-docs"); code != 0 {
		t.Fatalf("first setup code = %d, want 0; stderr = %q", code, stderr)
	}

	// Before setup learned to adopt, this run requested the built-in default
	// and was refused for disagreeing with the project it was joining.
	code, stdout, stderr := run(t, repository, "setup", "--no-docs")
	if code != 0 {
		t.Fatalf("second setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Key:\tPROJ\n") {
		t.Fatalf("second setup stdout = %q, want the project's own key", stdout)
	}
}

func TestSetupStillRefusesAKeyThatDisagreesWithTheProject(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--key", "PROJ", "--no-docs"); code != 0 {
		t.Fatalf("first setup code = %d, want 0; stderr = %q", code, stderr)
	}

	code, _, stderr := run(t, repository, "setup", "--key", "OTHER", "--no-docs")
	if code == 0 {
		t.Fatal("setup with a disagreeing key succeeded, want a refusal")
	}
	if !strings.Contains(stderr, `project key "PROJ"`) {
		t.Fatalf("setup stderr = %q, want it to name the project's key", stderr)
	}
}

func TestSetupRejectsAMalformedKeyBeforeTouchingAnything(t *testing.T) {
	repository := testrepo.New(t)

	code, _, stderr := run(t, repository, "setup", "--key", "bad key", "--no-docs")
	if code == 0 {
		t.Fatal("setup with a malformed key succeeded, want a refusal")
	}
	if !strings.Contains(stderr, "must match") {
		t.Fatalf("setup stderr = %q, want the key grammar", stderr)
	}
	if code, _, _ := run(t, repository, "list"); code != 3 {
		t.Fatalf("list after a refused setup code = %d, want 3 (not initialized)", code)
	}
}
