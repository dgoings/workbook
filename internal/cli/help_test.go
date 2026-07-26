package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestRunHelpAliasesRenderPlainTextWithoutInitializingWorkbook(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "bare invocation", want: "Usage: workbook <command> [arguments]"},
		{name: "short global flag", args: []string{"-h"}, want: "Usage: workbook <command> [arguments]"},
		{name: "long global flag", args: []string{"--help"}, want: "Usage: workbook <command> [arguments]"},
		{name: "explicit global command", args: []string{"help"}, want: "Usage: workbook <command> [arguments]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertHelpOutput(t, test.args, test.want)
		})
	}
}

func TestRunHelpAliasesForEveryTopLevelCommand(t *testing.T) {
	for _, command := range commandOrder {
		for _, test := range []struct {
			name string
			args []string
		}{
			{name: "explicit help", args: []string{"help", command}},
			{name: "short local flag", args: []string{command, "-h"}},
			{name: "long local flag", args: []string{command, "--help"}},
		} {
			t.Run(command+"/"+test.name, func(t *testing.T) {
				output := assertHelpOutput(t, test.args, "Usage: workbook "+command)
				if !strings.Contains(output, "-h, --help") {
					t.Fatalf("Run(%q) stdout = %q, want both command help aliases", test.args, output)
				}
			})
		}
	}
}

func TestRunHelpHandlesHooksAndInstall(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "help hooks", args: []string{"help", "hooks"}, want: "Usage: workbook hooks <command> [options]"},
		{name: "help hooks install", args: []string{"help", "hooks", "install"}, want: "Usage: workbook hooks install [options]"},
		{name: "hooks install short local flag", args: []string{"hooks", "install", "-h"}, want: "Usage: workbook hooks install [options]"},
		{name: "hooks install long local flag", args: []string{"hooks", "install", "--help"}, want: "Usage: workbook hooks install [options]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := assertHelpOutput(t, test.args, test.want)
			if !strings.Contains(output, "-h, --help") {
				t.Fatalf("Run(%q) stdout = %q, want both command help aliases", test.args, output)
			}
		})
	}
}

func TestRunMalformedLocalHelpAfterJSONIsPlainText(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "option-only command short help", args: []string{"init", "--json", "-h"}},
		{name: "option-only command long help", args: []string{"init", "--json", "--help"}},
		{name: "positional command", args: []string{"create", "Title", "--json", "--help"}},
		{name: "hooks install", args: []string{"hooks", "install", "--json", "--help"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := testrepo.New(t)
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("Run(%q) code = %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("Run(%q) stdout = %q, want empty", test.args, stdout)
			}
			if !strings.Contains(stderr, "Usage: workbook <command> [arguments]") {
				t.Fatalf("Run(%q) stderr = %q, want global usage", test.args, stderr)
			}
			if strings.Contains(stderr, "workbook.error") {
				t.Fatalf("Run(%q) stderr = %q, want plain-text error", test.args, stderr)
			}
			assertNoWorkbookDirectory(t, repository)
		})
	}
}

func TestRunLocalHelpRecognitionRespectsStringValuesAndTerminator(t *testing.T) {
	t.Run("help-looking string flag value remains normal JSON invocation", func(t *testing.T) {
		repository := testrepo.New(t)
		code, stdout, stderr := run(t, repository, "init", "--json", "--key", "--help")
		if code != 5 {
			t.Fatalf("Run() code = %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run() stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "")
		assertNoWorkbookDirectory(t, repository)
	})

	t.Run("help after terminator remains positional input", func(t *testing.T) {
		repository := testrepo.New(t)
		code, stdout, stderr := run(t, repository, "init", "--json", "--", "--help")
		if code != 2 {
			t.Fatalf("Run() code = %d, want 2; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run() stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, "init accepts no additional positional arguments")
		assertNoWorkbookDirectory(t, repository)
	})
}

func TestRunMalformedHelpIsAnInvocationErrorWithoutJSONOrInitialization(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "help command rejects JSON", args: []string{"help", "create", "--json"}},
		{name: "short global flag rejects JSON", args: []string{"-h", "--json"}},
		{name: "long global flag rejects JSON", args: []string{"--help", "--json"}},
		{name: "unknown explicit target", args: []string{"help", "unknown"}},
		{name: "unknown hooks target", args: []string{"help", "hooks", "unknown"}},
		{name: "local help rejects trailing positional", args: []string{"create", "--help", "title"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := testrepo.New(t)
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("Run(%q) code = %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("Run(%q) stdout = %q, want empty", test.args, stdout)
			}
			if !strings.Contains(stderr, "Usage: workbook <command> [arguments]") {
				t.Fatalf("Run(%q) stderr = %q, want global usage", test.args, stderr)
			}
			if strings.Contains(stderr, "workbook.error") {
				t.Fatalf("Run(%q) stderr = %q, want plain-text error", test.args, stderr)
			}
			assertNoWorkbookDirectory(t, repository)
		})
	}
}

func assertHelpOutput(t *testing.T, args []string, want string) string {
	t.Helper()
	repository := testrepo.New(t)
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 {
		t.Fatalf("Run(%q) code = %d, want 0; stderr = %q", args, code, stderr)
	}
	if stdout == "" || !strings.Contains(stdout, want) {
		t.Fatalf("Run(%q) stdout = %q, want populated help containing %q", args, stdout, want)
	}
	if stderr != "" {
		t.Fatalf("Run(%q) stderr = %q, want empty", args, stderr)
	}
	assertNoWorkbookDirectory(t, repository)
	return stdout
}

func assertNoWorkbookDirectory(t *testing.T, repository string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(repository, ".workbook"))
	if !os.IsNotExist(err) {
		t.Fatalf(".workbook stat error = %v, want no .workbook directory", err)
	}
}
