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
	"testing"

	"github.com/dgoings/workbook/internal/perf"
)

func TestRunWritesCompletePerformanceReport(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	outputRoot := t.TempDir()
	jsonPath := filepath.Join(outputRoot, "report.json")
	markdownPath := filepath.Join(outputRoot, "report.md")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"--workbook", workbookBinary,
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
				"--workbook", filepath.Join(outputRoot, "workbook"),
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

func buildWorkbookBinary(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "workbook")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	return binary
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
