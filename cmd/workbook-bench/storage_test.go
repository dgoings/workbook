package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

func parseStorageOptions(t *testing.T, extra ...string) (*options, error) {
	t.Helper()
	outputRoot := t.TempDir()
	var stderr bytes.Buffer
	flags, parsed := newFlagSet(&stderr)
	args := append([]string{
		"--workbook", buildWorkbookBinary(t),
		"--output-json", filepath.Join(outputRoot, "report.json"),
		"--output-markdown", filepath.Join(outputRoot, "report.md"),
	}, extra...)
	if err := flags.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return parsed, validateOptions(flags, parsed)
}

// Mutation witness: dropping either default depth, or accepting depths in the
// order typed rather than a validated ascending set, changes which fixtures
// the evidence run measures.
func TestValidateOptionsResolvesStorageOperationDepths(t *testing.T) {
	parsed, err := parseStorageOptions(t, "--storage-resources")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.storageDepths, []int{20, 100}) {
		t.Fatalf("default storage depths = %v, want [20 100]", parsed.storageDepths)
	}
	if len(parsed.scenarios) != 0 {
		t.Fatalf("storage-only run selected scenarios %v", parsed.scenarios)
	}

	parsed, err = parseStorageOptions(t, "--storage-resources", "--storage-operations", "100,20")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.storageDepths, []int{20, 100}) {
		t.Fatalf("storage depths = %v, want ascending [20 100]", parsed.storageDepths)
	}
}

func TestValidateOptionsRejectsInvalidStorageInvocations(t *testing.T) {
	for name, testCase := range map[string]struct {
		args []string
		want string
	}{
		"with scenario selector": {
			args: []string{"--storage-resources", "--scenario", "cli-update"},
			want: "--storage-resources cannot be combined with --scenario",
		},
		"empty depth list": {
			args: []string{"--storage-resources", "--storage-operations", ""},
			want: "--storage-operations requires at least one operation depth",
		},
		"non-numeric depth": {
			args: []string{"--storage-resources", "--storage-operations", "20,many"},
			want: "invalid --storage-operations value",
		},
		"depth below minimum": {
			args: []string{"--storage-resources", "--storage-operations", "1"},
			want: "--storage-operations values must be at least 2",
		},
		"duplicate depth": {
			args: []string{"--storage-resources", "--storage-operations", "20,20"},
			want: "duplicate --storage-operations value 20",
		},
		"acceptance depth below minimum": {
			args: []string{"--storage-resources", "--storage-operations", "19,100", "--phase", "acceptance", "--tasks", "500"},
			want: "acceptance requires at least 20 operations per task at every storage depth",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseStorageOptions(t, testCase.args...)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("validateOptions error = %v, want %q", err, testCase.want)
			}
		})
	}
}

// Mutation witness: running the scenario families alongside the storage
// accounting, or omitting the storage section from the assembled report,
// destroys the isolation the story requires.
func TestRunBenchmarkProducesStorageResourceReportOnly(t *testing.T) {
	report, err := runBenchmark(context.Background(), options{
		workbookBinary: buildWorkbookBinary(t),
		tasks:          6,
		tombstones:     1,
		operations:     3,
		samples:        1,
		timeout:        120 * time.Second,
		objectFormat:   "sha1",
		phase:          "baseline",
		storage:        true,
		storageDepths:  []int{3, 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 0 {
		t.Fatalf("storage-only report ran scenarios %#v", report.Scenarios)
	}
	if report.StorageResources == nil {
		t.Fatal("storage-only report has no storage section")
	}
	if len(report.StorageResources.Depths) != 2 {
		t.Fatalf("storage section has %d depths, want 2", len(report.StorageResources.Depths))
	}
	if report.Fixture.TotalTasks != 6 || report.Fixture.ActiveTasks != 5 ||
		report.Fixture.TombstonedTasks != 1 || report.Fixture.ObjectFormat != "sha1" {
		t.Fatalf("report fixture metadata = %#v", report.Fixture)
	}
	if report.Fixture.OperationsPerTask != 3 {
		t.Fatalf("report fixture depth = %d, want the shallowest measured depth", report.Fixture.OperationsPerTask)
	}
	if report.Environment.WorkbookCommit == "" {
		t.Fatal("report has no measured Workbook commit")
	}
}

func TestRunWritesStorageResourceReportsEndToEnd(t *testing.T) {
	outputRoot := t.TempDir()
	jsonPath := filepath.Join(outputRoot, "storage.json")
	markdownPath := filepath.Join(outputRoot, "storage.md")
	var stdout, stderr bytes.Buffer

	code := run(context.Background(), []string{
		"--workbook", buildWorkbookBinary(t),
		"--tasks", "12",
		"--tombstones", "1",
		"--samples", "1",
		"--timeout", "120s",
		"--object-format", "sha1",
		"--phase", "baseline",
		"--storage-resources",
		"--storage-operations", "3,5",
		"--output-json", jsonPath,
		"--output-markdown", markdownPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	encoded, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded perf.Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Format != perf.ReportFormat || decoded.Version != perf.ReportVersion {
		t.Fatalf("report envelope = %s v%d", decoded.Format, decoded.Version)
	}
	if decoded.StorageResources == nil || len(decoded.StorageResources.Depths) != 2 {
		t.Fatalf("decoded storage section = %#v", decoded.StorageResources)
	}
	if !strings.Contains(string(encoded), `"scenarios":[]`) {
		t.Fatalf("storage-only report does not encode an empty scenario list:\n%s", encoded)
	}

	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"## Storage and peak resources",
		"12 tasks by 3 operations (sha1)",
		"12 tasks by 5 operations (sha1)",
		"operation-blob",
		"projection-rebuild",
		"full-validation",
	} {
		if !strings.Contains(string(markdown), fragment) {
			t.Fatalf("Markdown report missing %q:\n%s", fragment, markdown)
		}
	}
}
