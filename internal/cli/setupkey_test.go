package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestPromptProjectKeyTakesTheSuggestionOnEnter(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(context.Background(), strings.NewReader("\n"), &out, "ACME")
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

func TestPromptProjectKeyAbortsAtEndOfInput(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(context.Background(), strings.NewReader(""), &out, "ACME")
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("promptProjectKey() error = %v, want a validation failure", err)
	}
	if key != "" {
		t.Fatalf("promptProjectKey() = %q, want no key", key)
	}
	if !strings.Contains(err.Error(), "nothing was created") {
		t.Fatalf("error %q does not say nothing was created", err)
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("prompt %q left the refusal on the prompt's line", out.String())
	}
}

func TestPromptProjectKeyUppercasesAndTrimsTheAnswer(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(context.Background(), strings.NewReader("  myapp \n"), &out, "ACME")
	if err != nil {
		t.Fatalf("promptProjectKey() error = %v", err)
	}
	if key != "MYAPP" {
		t.Fatalf("promptProjectKey() = %q, want %q", key, "MYAPP")
	}
}

func TestPromptProjectKeyExplainsAndAsksAgain(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(context.Background(), strings.NewReader("1bad\nok\n"), &out, "ACME")
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

func TestPromptProjectKeyAbortsAfterABadAnswerWithNoTrailingNewline(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(context.Background(), strings.NewReader("1bad"), &out, "ACME")
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("promptProjectKey() error = %v, want a validation failure", err)
	}
	if key != "" {
		t.Fatalf("promptProjectKey() = %q, want no key", key)
	}
	if !strings.Contains(out.String(), `project key "1BAD" must match`) {
		t.Fatalf("prompt %q does not say why the answer was refused", out.String())
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("prompt %q left the refusal on the prompt's line", out.String())
	}
}

func TestPromptProjectKeyAbortsAfterABadAnswerWithATrailingNewline(t *testing.T) {
	var out bytes.Buffer
	key, err := promptProjectKey(context.Background(), strings.NewReader("1bad\n"), &out, "ACME")
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("promptProjectKey() error = %v, want a validation failure", err)
	}
	if key != "" {
		t.Fatalf("promptProjectKey() = %q, want no key", key)
	}
	if !strings.Contains(out.String(), `project key "1BAD" must match`) {
		t.Fatalf("prompt %q does not say why the answer was refused", out.String())
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("prompt %q left the refusal on the prompt's line", out.String())
	}
}

// TestPromptProjectKeyStopsWhenTheContextIsCanceled is Ctrl-C at the prompt:
// the read never finishes, and the prompt has to return anyway.
func TestPromptProjectKeyStopsWhenTheContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := promptProjectKey(ctx, reader, &out, "ACME")
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("promptProjectKey() did not return on a canceled context")
	}
	if err == nil {
		t.Fatal("promptProjectKey() on a canceled context returned no error")
	}
	if core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("promptProjectKey() error = %v, want an operational failure", err)
	}
	if !strings.Contains(err.Error(), "nothing was created") {
		t.Fatalf("error %q does not say nothing was created", err)
	}
	if got := out.String(); got != "Project key [ACME]: " {
		t.Fatalf("prompt wrote %q, want nothing after the prompt itself", got)
	}
}

func TestPromptProjectKeyGivesUpAfterRepeatedBadAnswers(t *testing.T) {
	var out bytes.Buffer
	_, err := promptProjectKey(context.Background(), strings.NewReader(strings.Repeat("1\n", projectKeyAttempts+3)), &out, "ACME")
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("promptProjectKey() error = %v, want a validation failure", err)
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

func TestInteractiveTerminalIsFalseForANilReader(t *testing.T) {
	if interactiveTerminal(nil, &bytes.Buffer{}) {
		t.Fatal("interactiveTerminal() = true for a nil reader")
	}
}

func TestSetupDerivesTheKeyFromTheDirectoryWhenNothingAsks(t *testing.T) {
	repository := testrepo.New(t, testrepo.WithName("acme-site"))

	// The typed answer proves a piped stdin is never read as a prompt answer.
	code, stdout, stderr := runWithInput(t, repository, strings.NewReader("TYPED\n"), "setup", "--no-docs")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Key:\tAS\n") {
		t.Fatalf("setup stdout = %q, want the key derived from the directory", stdout)
	}
	if strings.Contains(stdout, "Project key [") {
		t.Fatalf("setup prompted without a terminal:\n%s", stdout)
	}

	code, stdout, stderr = run(t, repository, "create", "First task", "--json", "--no-sync")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	if task := decodeMutationTask(t, stdout, "create"); !strings.HasPrefix(task.ID, "AS-") {
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
	if result.Key != "MA" {
		t.Fatalf("setup key = %q, want %q", result.Key, "MA")
	}
}

func TestSetupFallsBackToTheDefaultKeyForAMeaninglessDirectory(t *testing.T) {
	repository := testrepo.New(t, testrepo.WithName("---"))

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

func TestSetupRejectsAnExplicitlyEmptyKey(t *testing.T) {
	repository := testrepo.New(t)

	code, _, stderr := run(t, repository, "setup", "--key", "", "--no-docs")
	if code != 5 {
		t.Fatalf("setup --key '' code = %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "must match") {
		t.Fatalf("setup stderr = %q, want the key grammar", stderr)
	}
	if code, _, _ := run(t, repository, "list"); code != 3 {
		t.Fatalf("list after a refused setup code = %d, want 3 (not initialized)", code)
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
