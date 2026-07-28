package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

func TestRunResolvesRelativeWorkbookBinaryAndWritesCompletePerformanceReport(t *testing.T) {
	binaryDirectory, err := os.MkdirTemp(".", ".workbook-bench-relative-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(binaryDirectory) })
	workbookBinary, err := filepath.Abs(filepath.Join(binaryDirectory, "workbook"))
	if err != nil {
		t.Fatal(err)
	}
	buildWorkbookBinaryAt(t, workbookBinary)
	workbookArgument := filepath.Join(".", filepath.Base(binaryDirectory), "workbook")
	outputRoot := t.TempDir()
	jsonPath := filepath.Join(outputRoot, "report.json")
	markdownPath := filepath.Join(outputRoot, "report.md")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"--workbook", workbookArgument,
		"--tasks", "10",
		"--operations", "4",
		"--samples", "1",
		"--timeout", "5s",
		"--object-format", "sha1",
		"--output-json", jsonPath,
		"--output-markdown", markdownPath,
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run exit code = %d, want 0; stdout = %q; stderr = %q", exitCode, stdout.String(), stderr.String())
	}

	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	var report perf.Report
	if err := json.Unmarshal(jsonData, &report); err != nil {
		t.Fatalf("decode JSON report: %v", err)
	}
	if report.Format != perf.ReportFormat || report.Version != perf.ReportVersion {
		t.Fatalf("report identity = %q v%d, want %q v%d", report.Format, report.Version, perf.ReportFormat, perf.ReportVersion)
	}
	if report.Phase != "baseline" {
		t.Errorf("report phase = %q, want baseline", report.Phase)
	}
	if report.Fixture != (perf.FixtureSpec{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"}) {
		t.Errorf("report fixture = %#v, want ten tasks and four operations using SHA-1", report.Fixture)
	}

	gotScenarios := make([]string, len(report.Scenarios))
	for index, scenario := range report.Scenarios {
		gotScenarios[index] = scenario.Name
	}
	sort.Strings(gotScenarios)
	wantScenarios := []string{
		"api-burst-independent-10",
		"api-burst-same-task-10",
		"api-update",
		"cli-burst-independent-10",
		"cli-burst-same-task-10",
		"cli-create",
		"cli-delete",
		"cli-depend",
		"cli-free",
		"cli-move",
		"cli-restore",
		"cli-update",
		"projection-rebuild",
		"projection-refresh-one-changed",
		"projection-refresh-unchanged",
		"sync-initial-local-bare",
		"sync-unchanged-local-bare",
	}
	if !reflect.DeepEqual(gotScenarios, wantScenarios) {
		t.Fatalf("scenario names = %#v, want %#v", gotScenarios, wantScenarios)
	}

	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatalf("read Markdown report: %v", err)
	}
	if len(markdown) == 0 {
		t.Fatal("Markdown report is empty")
	}
}

func TestRunRejectsInvalidInvocation(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name   string
		change func([]string) []string
	}{
		{
			name: "missing Workbook binary",
			change: func(args []string) []string {
				return removeFlag(args, "--workbook")
			},
		},
		{
			name: "tasks below ten",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--tasks", "9")
			},
		},
		{
			name: "operations below two",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--operations", "1")
			},
		},
		{
			name: "samples below one",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--samples", "0")
			},
		},
		{
			name: "nonpositive timeout",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--timeout", "0s")
			},
		},
		{
			name: "unsupported object format",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--object-format", "sha512")
			},
		},
		{
			name: "identical output paths",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--output-markdown", flagValue(args, "--output-json"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			args := []string{
				"--workbook", workbookBinary,
				"--tasks", "10",
				"--operations", "4",
				"--samples", "1",
				"--timeout", "5s",
				"--object-format", "sha1",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := run(context.Background(), test.change(args), &stdout, &stderr)
			if exitCode == 0 {
				t.Fatalf("run accepted invalid invocation; stdout = %q; stderr = %q", stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("invalid invocation produced empty stderr")
			}
		})
	}
}

func TestValidateOptionsEnforcesAcceptanceMinimum(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	rejected := []struct {
		name       string
		tasks      string
		operations string
	}{
		{name: "diagnostic task count", tasks: "499", operations: "20"},
		{name: "short history", tasks: "500", operations: "19"},
	}
	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			err := flags.Parse([]string{
				"--workbook", workbookBinary,
				"--tasks", test.tasks,
				"--operations", test.operations,
				"--phase", "acceptance",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			})
			if err != nil {
				t.Fatalf("parse flags: %v", err)
			}
			err = validateOptions(flags, options)
			if err == nil || !strings.Contains(err.Error(), "acceptance requires at least 500 tasks and 20 operations per task") {
				t.Fatalf("validateOptions error = %v, want acceptance minimum guidance", err)
			}
		})
	}

	t.Run("larger future workload", func(t *testing.T) {
		outputRoot := t.TempDir()
		var stderr bytes.Buffer
		flags, options := newFlagSet(&stderr)
		err := flags.Parse([]string{
			"--workbook", workbookBinary,
			"--tasks", "501",
			"--operations", "21",
			"--phase", "acceptance",
			"--output-json", filepath.Join(outputRoot, "report.json"),
			"--output-markdown", filepath.Join(outputRoot, "report.md"),
		})
		if err != nil {
			t.Fatalf("parse flags: %v", err)
		}
		if err := validateOptions(flags, options); err != nil {
			t.Fatalf("validateOptions rejected larger acceptance workload: %v", err)
		}
	})
}

func TestBenchmarkEnvironmentBoundsEveryMetadataCommand(t *testing.T) {
	tests := []string{"git", "go", "workbook"}
	for _, hungCommand := range tests {
		t.Run(hungCommand, func(t *testing.T) {
			binaryDirectory := t.TempDir()
			gitPath := filepath.Join(binaryDirectory, "git")
			goPath := filepath.Join(binaryDirectory, "go")
			workbookPath := filepath.Join(binaryDirectory, "workbook")
			writeExecutableScript(t, gitPath, "printf 'git version test\\n'")
			writeExecutableScript(t, goPath, "printf 'go version test\\n'")
			writeExecutableScript(t, workbookPath, `printf '%s\n' '{"format":"workbook.result","version":1,"command":"version","data":{"version":"dev","commit":"test"}}'`)
			switch hungCommand {
			case "git":
				writeExecutableScript(t, gitPath, "/bin/sleep 5")
			case "go":
				writeExecutableScript(t, goPath, "/bin/sleep 5")
			case "workbook":
				writeExecutableScript(t, workbookPath, "/bin/sleep 5")
			}
			t.Setenv("PATH", binaryDirectory)

			startedAt := time.Now()
			_, err := benchmarkEnvironment(context.Background(), workbookPath, 20*time.Millisecond)
			elapsed := time.Since(startedAt)
			if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
				t.Fatalf("benchmarkEnvironment error = %v, want %s timeout", err, hungCommand)
			}
			if elapsed > 2*time.Second {
				t.Fatalf("benchmarkEnvironment returned after %s, want bounded descendant termination", elapsed)
			}
		})
	}
}

func buildWorkbookBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "workbook")
	buildWorkbookBinaryAt(t, binary)
	return binary
}

func buildWorkbookBinaryAt(t *testing.T, binary string) {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
}

func writeExecutableScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func removeFlag(args []string, name string) []string {
	for index := range args {
		if args[index] == name {
			return append(append([]string(nil), args[:index]...), args[index+2:]...)
		}
	}
	panic("missing test flag " + name)
}

func replaceFlagValue(args []string, name, value string) []string {
	result := append([]string(nil), args...)
	for index := range result {
		if result[index] == name {
			result[index+1] = value
			return result
		}
	}
	panic("missing test flag " + name)
}

func flagValue(args []string, name string) string {
	for index := range args {
		if args[index] == name {
			return args[index+1]
		}
	}
	panic("missing test flag " + name)
}
