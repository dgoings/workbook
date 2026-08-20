package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func benchmarkScriptPaths(t *testing.T) (string, string) {
	t.Helper()
	root, _ := paths(t)
	return root, filepath.Join(root, "scripts", "benchmark.sh")
}

func TestBenchmarkRequiresGoInPath(t *testing.T) {
	root, script := benchmarkScriptPaths(t)
	command := exec.Command("/bin/sh", script)
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+t.TempDir())
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("benchmark.sh succeeded without go in PATH:\n%s", output)
	}
	if !strings.Contains(string(output), "go is required") {
		t.Fatalf("benchmark.sh output = %q, want the missing-go guidance", output)
	}
}

// Mutation witness: dropping the report-path echo or the bench-reports/
// destination breaks the documented run-and-compare workflow, and a run that
// exits zero without both report files has measured nothing.
func TestBenchmarkWritesDatedReportPairAndPrintsMarkdownPath(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end benchmark run is slow")
	}
	root, script := benchmarkScriptPaths(t)
	command := exec.Command(script, "--tasks", "10", "--operations", "4", "--samples", "1", "--scenario", "cli-list")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("benchmark.sh: %v\n%s", err, output)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	markdownPath := lines[len(lines)-1]
	if filepath.Dir(markdownPath) != filepath.Join(root, "bench-reports") {
		t.Fatalf("reported path %q is not under bench-reports/", markdownPath)
	}
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatalf("read Markdown report: %v", err)
	}
	if !strings.Contains(string(markdown), "cli-list") {
		t.Fatalf("Markdown report is missing the measured scenario:\n%s", markdown)
	}
	jsonPath := strings.TrimSuffix(markdownPath, ".md") + ".json"
	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	if !strings.Contains(string(jsonData), `"workbook.performance-report"`) {
		t.Fatalf("JSON report has the wrong format identity:\n%s", jsonData)
	}
	t.Cleanup(func() {
		os.Remove(markdownPath)
		os.Remove(jsonPath)
	})
}
