// Command skipreport reads `go test -json` output on stdin, replays the
// human-readable test output to stdout, and reports every skipped test with
// its recorded reason so a silently shrinking suite stays visible.
//
// The report is Markdown. When GITHUB_STEP_SUMMARY names a file the report is
// appended there, which is how CI surfaces it in the job summary; otherwise
// it is written to stderr. A skip whose reason carries the
// internal/testenv missing-capability marker means the environment lost a
// capability the suite depends on, so skipreport exits non-zero to fail the
// run rather than let the gap pass as green. The same marker on a failing
// test — what internal/testenv produces once
// WORKBOOK_TEST_REQUIRE_CAPABILITIES demands every capability — is reported
// under its own heading, so the summary names the missing capability whichever
// way the environment was configured to react to it.
//
// Usage, preserving `go test` failures via pipefail:
//
//	set -o pipefail
//	go test ./... -json | go run ./scripts/skipreport
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/dgoings/workbook/internal/testenv"
)

// testEvent is the subset of the `go test -json` event stream skipreport
// reads. See `go doc test2json` for the full format.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

type skippedTest struct {
	Package string
	Test    string
	Reason  string
}

// MissingCapability reports whether the skip carries the marker
// internal/testenv attaches to skips caused by a missing environment
// capability rather than an intentional exclusion.
func (s skippedTest) MissingCapability() bool {
	return strings.Contains(s.Reason, testenv.MissingCapabilityPrefix)
}

// logLine matches a line a test logged through t.Skipf, t.Logf, and friends:
// indentation, the source position, then the message.
var logLine = regexp.MustCompile(`^\s+[^\s:]+_test\.go:\d+: (.*)$`)

// outcomes is what one `go test -json` stream says about coverage the suite
// did not actually exercise.
type outcomes struct {
	// Skips lists every skipped test with the last reason it logged, in the
	// order the skips were reported.
	Skips []skippedTest
	// CapabilityFailures lists tests that failed carrying the missing-
	// capability marker, which is how a required capability reports itself
	// absent.
	CapabilityFailures []skippedTest
}

// MissingCapabilities reports whether the run left coverage unexecuted for
// want of an environment capability, either way the environment chose to
// react to it.
func (o outcomes) MissingCapabilities() bool {
	if len(o.CapabilityFailures) > 0 {
		return true
	}
	for _, skip := range o.Skips {
		if skip.MissingCapability() {
			return true
		}
	}
	return false
}

// collect reads a `go test -json` stream and replays the human-readable output
// to replay.
func collect(stream io.Reader, replay io.Writer) (outcomes, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	reasons := make(map[string]string)
	capabilityReasons := make(map[string]string)
	var collected outcomes
	for scanner.Scan() {
		line := scanner.Bytes()
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not every line of a failed run is JSON; keep it readable.
			if _, err := fmt.Fprintf(replay, "%s\n", line); err != nil {
				return outcomes{}, err
			}
			continue
		}
		if event.Output != "" {
			if _, err := io.WriteString(replay, event.Output); err != nil {
				return outcomes{}, err
			}
		}
		if event.Test == "" {
			// Package-level events, including "no test files" skips.
			continue
		}
		key := event.Package + "\x00" + event.Test
		switch event.Action {
		case "output":
			match := logLine.FindStringSubmatch(strings.TrimRight(event.Output, "\n"))
			if match == nil {
				continue
			}
			reasons[key] = match[1]
			// A failing test logs many lines, so remember the marked one
			// rather than whichever happened to come last.
			if marker := strings.Index(match[1], testenv.MissingCapabilityPrefix); marker >= 0 {
				capabilityReasons[key] = match[1][marker:]
			}
		case "skip":
			reason := reasons[key]
			if reason == "" {
				reason = "(no reason recorded)"
			}
			collected.Skips = append(collected.Skips, skippedTest{Package: event.Package, Test: event.Test, Reason: reason})
		case "fail":
			if reason := capabilityReasons[key]; reason != "" {
				collected.CapabilityFailures = append(collected.CapabilityFailures,
					skippedTest{Package: event.Package, Test: event.Test, Reason: reason})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return outcomes{}, err
	}
	return collected, nil
}

// markdownReport renders the skip list for a job summary, flagging skips that
// mark a missing capability and naming every test that failed for want of one.
func markdownReport(collected outcomes) string {
	var report strings.Builder
	report.WriteString("## Skipped tests\n\n")
	if len(collected.Skips) == 0 {
		report.WriteString("No tests were skipped.\n")
	} else {
		missing := 0
		for _, skip := range collected.Skips {
			if skip.MissingCapability() {
				missing++
			}
		}
		fmt.Fprintf(&report, "%d skipped, %d marking a missing capability.\n\n", len(collected.Skips), missing)
		writeOutcomeTable(&report, collected.Skips)
		if missing > 0 {
			fmt.Fprintf(&report, "\n**%d skip(s) mark a capability this environment is missing; the suite did not fully run.**\n", missing)
		}
	}
	if len(collected.CapabilityFailures) > 0 {
		fmt.Fprintf(&report, "\n## Missing capabilities\n\n**%d test(s) failed because this environment lacks a capability the suite requires.**\n\n",
			len(collected.CapabilityFailures))
		writeOutcomeTable(&report, collected.CapabilityFailures)
	}
	return report.String()
}

func writeOutcomeTable(report *strings.Builder, entries []skippedTest) {
	report.WriteString("| Package | Test | Reason |\n| --- | --- | --- |\n")
	escape := strings.NewReplacer("|", `\|`, "\n", " ")
	for _, entry := range entries {
		fmt.Fprintf(report, "| %s | %s | %s |\n",
			escape.Replace(entry.Package), escape.Replace(entry.Test), escape.Replace(entry.Reason))
	}
}

// publishReport appends the report to the GITHUB_STEP_SUMMARY file when the
// environment provides one, and writes it to fallback otherwise.
func publishReport(report string, fallback io.Writer) error {
	summaryPath := os.Getenv("GITHUB_STEP_SUMMARY")
	if summaryPath == "" {
		_, err := io.WriteString(fallback, report)
		return err
	}
	summary, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(summary, report); err != nil {
		summary.Close()
		return err
	}
	return summary.Close()
}

func main() {
	collected, err := collect(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipreport: %v\n", err)
		os.Exit(2)
	}
	if err := publishReport(markdownReport(collected), os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "skipreport: %v\n", err)
		os.Exit(2)
	}
	for _, skip := range collected.Skips {
		if skip.MissingCapability() {
			fmt.Fprintf(os.Stderr, "skipreport: %s %s skipped for a missing capability: %s\n", skip.Package, skip.Test, skip.Reason)
		}
	}
	for _, failure := range collected.CapabilityFailures {
		fmt.Fprintf(os.Stderr, "skipreport: %s %s failed for a missing capability: %s\n", failure.Package, failure.Test, failure.Reason)
	}
	if collected.MissingCapabilities() {
		os.Exit(1)
	}
}
