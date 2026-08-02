package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildFormulaTool(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "formula-tool")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build formula tool: %v\n%s", err, output)
	}
	return binary
}

func TestFormulaToolRendersToStandardOutput(t *testing.T) {
	binary := buildFormulaTool(t)

	output, err := exec.Command(binary,
		"0.2.0",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		"dgoings/workbook",
	).Output()
	if err != nil {
		t.Fatalf("render formula: %v", err)
	}

	// The formula carries no "version" stanza; Homebrew scans the version from
	// the release-tag segment of the active platform's URL, and declaring it
	// again fails "brew audit" as redundant. Assert the segment that actually
	// determines the version, because dropping it fails silently: Homebrew
	// falls back to filename heuristics and reads
	// "workbook_0.2.0_linux_amd64.tar.gz" as version "64".
	for _, want := range []string{"class Workbook < Formula", "/download/v0.2.0/", "def caveats", "on_linux do"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("rendered formula missing %q:\n%s", want, output)
		}
	}
}

func TestFormulaToolRejectsWrongArgumentCount(t *testing.T) {
	// Production mutation: accepting a short argument list would render a
	// formula from whatever happened to be present and publish it.
	binary := buildFormulaTool(t)

	for name, args := range map[string][]string{
		"none":        {},
		"few":         {"0.2.0"},
		"darwin only": {"0.2.0", "a", "b", "dgoings/workbook"},
		"many":        {"0.2.0", "a", "b", "c", "d", "dgoings/workbook", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := exec.Command(binary, args...).CombinedOutput()
			if err == nil {
				t.Fatalf("formula tool accepted %v; output = %q", args, output)
			}
			if !strings.Contains(string(output), "usage:") {
				t.Fatalf("formula tool did not report usage for %v: %q", args, output)
			}
		})
	}
}

func TestFormulaToolRejectsUnsafeInput(t *testing.T) {
	binary := buildFormulaTool(t)
	valid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for name, args := range map[string][]string{
		"version":         {"v0.2.0", valid, valid, valid, valid, "dgoings/workbook"},
		"darwin checksum": {"0.2.0", "short", valid, valid, valid, "dgoings/workbook"},
		"linux checksum":  {"0.2.0", valid, valid, valid, "short", "dgoings/workbook"},
		"repository":      {"0.2.0", valid, valid, valid, valid, "workbook"},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := exec.Command(binary, args...).CombinedOutput()
			if err == nil {
				t.Fatalf("formula tool accepted %v; output = %q", args, output)
			}
			if strings.Contains(string(output), "class Workbook") {
				t.Fatalf("formula tool rendered output for %v:\n%s", args, output)
			}
		})
	}
}
