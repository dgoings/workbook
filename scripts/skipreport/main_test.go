package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleStream = `{"Action":"run","Package":"github.com/dgoings/workbook/internal/webui","Test":"TestHandlerClient"}
{"Action":"output","Package":"github.com/dgoings/workbook/internal/webui","Test":"TestHandlerClient","Output":"=== RUN   TestHandlerClient\n"}
{"Action":"output","Package":"github.com/dgoings/workbook/internal/webui","Test":"TestHandlerClient","Output":"    handler_test.go:214: missing capability: node is required to execute the embedded client behavior\n"}
{"Action":"output","Package":"github.com/dgoings/workbook/internal/webui","Test":"TestHandlerClient","Output":"--- SKIP: TestHandlerClient (0.00s)\n"}
{"Action":"skip","Package":"github.com/dgoings/workbook/internal/webui","Test":"TestHandlerClient","Elapsed":0}
{"Action":"run","Package":"github.com/dgoings/workbook/cmd/workbook-bench","Test":"TestScalingMatrix"}
{"Action":"output","Package":"github.com/dgoings/workbook/cmd/workbook-bench","Test":"TestScalingMatrix","Output":"    scaling_test.go:321: end-to-end scaling matrix run is slow\n"}
{"Action":"skip","Package":"github.com/dgoings/workbook/cmd/workbook-bench","Test":"TestScalingMatrix","Elapsed":0}
{"Action":"run","Package":"github.com/dgoings/workbook/internal/perf","Test":"TestFixtures/sha256"}
{"Action":"output","Package":"github.com/dgoings/workbook/internal/perf","Test":"TestFixtures/sha256","Output":"--- SKIP: TestFixtures/sha256 (0.00s)\n"}
{"Action":"skip","Package":"github.com/dgoings/workbook/internal/perf","Test":"TestFixtures/sha256","Elapsed":0}
{"Action":"run","Package":"github.com/dgoings/workbook/internal/core","Test":"TestPasses"}
{"Action":"output","Package":"github.com/dgoings/workbook/internal/core","Test":"TestPasses","Output":"--- PASS: TestPasses (0.01s)\n"}
{"Action":"pass","Package":"github.com/dgoings/workbook/internal/core","Test":"TestPasses","Elapsed":0.01}
{"Action":"skip","Package":"github.com/dgoings/workbook/internal/agentdocs"}
not json at all
{"Action":"pass","Package":"github.com/dgoings/workbook/internal/core","Elapsed":0.05}
`

// Mutation witness: losing skip events, their reasons, or the capability
// classification lets the suite shrink without anything in CI noticing.
func TestCollectSkipsFindsEverySkippedTestWithItsReason(t *testing.T) {
	var replay strings.Builder
	collected, err := collect(strings.NewReader(sampleStream), &replay)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	skips := collected.Skips

	want := []skippedTest{
		{
			Package: "github.com/dgoings/workbook/internal/webui",
			Test:    "TestHandlerClient",
			Reason:  "missing capability: node is required to execute the embedded client behavior",
		},
		{
			Package: "github.com/dgoings/workbook/cmd/workbook-bench",
			Test:    "TestScalingMatrix",
			Reason:  "end-to-end scaling matrix run is slow",
		},
		{
			Package: "github.com/dgoings/workbook/internal/perf",
			Test:    "TestFixtures/sha256",
			Reason:  "(no reason recorded)",
		},
	}
	if len(skips) != len(want) {
		t.Fatalf("skips = %+v, want %d entries", skips, len(want))
	}
	for i, skip := range skips {
		if skip != want[i] {
			t.Fatalf("skip[%d] = %+v, want %+v", i, skip, want[i])
		}
	}
	if !skips[0].MissingCapability() {
		t.Fatal("marked node skip was not classified as a missing capability")
	}
	if skips[1].MissingCapability() || skips[2].MissingCapability() {
		t.Fatal("unmarked skips were classified as missing capabilities")
	}
	if !collected.MissingCapabilities() {
		t.Fatal("a marked capability skip did not make the run report missing capabilities")
	}
}

// Mutation witness: once CI demands every capability, a missing one is a
// failure rather than a skip. Reading only skip events would leave the summary
// claiming nothing was lost while the suite silently covered less.
func TestCollectRecordsFailuresCausedByMissingCapabilities(t *testing.T) {
	const stream = `{"Action":"output","Package":"p","Test":"TestNeedsNode","Output":"    handler_test.go:3818: missing capability: node is required (WORKBOOK_TEST_REQUIRE_CAPABILITIES is set)\n"}
{"Action":"output","Package":"p","Test":"TestNeedsNode","Output":"    handler_test.go:3819: an unrelated later log line\n"}
{"Action":"fail","Package":"p","Test":"TestNeedsNode","Elapsed":0}
{"Action":"output","Package":"p","Test":"TestOrdinaryFailure","Output":"    other_test.go:11: want 2, got 3\n"}
{"Action":"fail","Package":"p","Test":"TestOrdinaryFailure","Elapsed":0}
`
	var replay strings.Builder
	collected, err := collect(strings.NewReader(stream), &replay)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(collected.Skips) != 0 {
		t.Fatalf("skips = %+v, want none", collected.Skips)
	}
	if len(collected.CapabilityFailures) != 1 {
		t.Fatalf("capability failures = %+v, want exactly the marked one", collected.CapabilityFailures)
	}
	failure := collected.CapabilityFailures[0]
	if failure.Package != "p" || failure.Test != "TestNeedsNode" {
		t.Fatalf("capability failure = %+v, want p TestNeedsNode", failure)
	}
	// The marked line is not the last one the test logged, and the reason must
	// start at the marker rather than repeat the source position.
	want := "missing capability: node is required (WORKBOOK_TEST_REQUIRE_CAPABILITIES is set)"
	if failure.Reason != want {
		t.Fatalf("capability failure reason = %q, want %q", failure.Reason, want)
	}
	if !collected.MissingCapabilities() {
		t.Fatal("a capability failure did not make the run report missing capabilities")
	}
}

// Mutation witness: swallowing the human-readable stream would leave the CI
// log with nothing but raw JSON, so failures could not be read.
func TestCollectSkipsReplaysHumanReadableOutput(t *testing.T) {
	var replay strings.Builder
	if _, err := collect(strings.NewReader(sampleStream), &replay); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, fragment := range []string{
		"=== RUN   TestHandlerClient\n",
		"--- SKIP: TestHandlerClient (0.00s)\n",
		"--- PASS: TestPasses (0.01s)\n",
		"not json at all\n",
	} {
		if !strings.Contains(replay.String(), fragment) {
			t.Fatalf("replayed output %q does not contain %q", replay.String(), fragment)
		}
	}
}

func TestMarkdownReportListsSkipsAndFlagsMissingCapabilities(t *testing.T) {
	report := markdownReport(outcomes{Skips: []skippedTest{
		{Package: "p", Test: "TestCapability", Reason: "missing capability: node is required | escaped"},
		{Package: "p", Test: "TestIntentional", Reason: "slow"},
	}})
	for _, fragment := range []string{
		"## Skipped tests",
		"2 skipped, 1 marking a missing capability",
		"| p | TestCapability | missing capability: node is required \\| escaped |",
		"| p | TestIntentional | slow |",
	} {
		if !strings.Contains(report, fragment) {
			t.Fatalf("report %q does not contain %q", report, fragment)
		}
	}
}

// Mutation witness: a summary that only tabulates skips says "No tests were
// skipped" for the very run where a required capability went missing.
func TestMarkdownReportNamesCapabilityFailures(t *testing.T) {
	report := markdownReport(outcomes{CapabilityFailures: []skippedTest{
		{Package: "p", Test: "TestNeedsNode", Reason: "missing capability: node is required"},
	}})
	for _, fragment := range []string{
		"## Missing capabilities",
		"1 test(s) failed because this environment lacks a capability",
		"| p | TestNeedsNode | missing capability: node is required |",
	} {
		if !strings.Contains(report, fragment) {
			t.Fatalf("report %q does not contain %q", report, fragment)
		}
	}
}

func TestMarkdownReportOnEmptySuiteSaysSo(t *testing.T) {
	report := markdownReport(outcomes{})
	if !strings.Contains(report, "No tests were skipped.") {
		t.Fatalf("report %q does not state that nothing was skipped", report)
	}
	if strings.Contains(report, "## Missing capabilities") {
		t.Fatalf("report %q invents a missing-capability section", report)
	}
}

// Mutation witness: writing the report anywhere but GITHUB_STEP_SUMMARY buries
// it in the log, which is exactly where a shrinking suite already hides.
func TestPublishReportAppendsToTheJobSummary(t *testing.T) {
	summary := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(summary, []byte("earlier step\n"), 0o644); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", summary)

	var fallback strings.Builder
	if err := publishReport("## Skipped tests\n", &fallback); err != nil {
		t.Fatalf("publishReport: %v", err)
	}

	contents, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if string(contents) != "earlier step\n## Skipped tests\n" {
		t.Fatalf("summary = %q, want the report appended after the existing content", contents)
	}
	if fallback.String() != "" {
		t.Fatalf("fallback = %q, want nothing written when a summary file exists", fallback.String())
	}
}

func TestPublishReportFallsBackWithoutAJobSummary(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	var fallback strings.Builder
	if err := publishReport("## Skipped tests\n", &fallback); err != nil {
		t.Fatalf("publishReport: %v", err)
	}
	if fallback.String() != "## Skipped tests\n" {
		t.Fatalf("fallback = %q, want the report", fallback.String())
	}
}

func TestOutcomesWithoutCapabilityGapsReportNone(t *testing.T) {
	collected := outcomes{Skips: []skippedTest{{Package: "p", Test: "TestIntentional", Reason: "slow"}}}
	if collected.MissingCapabilities() {
		t.Fatal("an intentional skip was reported as a missing capability")
	}
}
