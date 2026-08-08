package scripts_test

import (
	"strings"
	"testing"
)

func TestReleaseBumpLabelSelectsEachReleaseLabel(t *testing.T) {
	for _, testCase := range []struct {
		label string
		want  string
	}{
		{label: "release:patch", want: "patch"},
		{label: "release:minor", want: "minor"},
		{label: "release:major", want: "major"},
	} {
		t.Run(testCase.label, func(t *testing.T) {
			output, err := runReleaseBumpLabel(t, "documentation\n"+testCase.label+"\nbug\n")
			if err != nil {
				t.Fatalf("select bump: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(output); got != testCase.want {
				t.Errorf("bump = %q, want %q", got, testCase.want)
			}
		})
	}
}

// The pull request check runs on every pull request so that it can be marked
// required. One carrying no release label has to pass it, printing nothing so
// the caller can tell there is no release to cut.
func TestReleaseBumpLabelPrintsNothingWithoutAReleaseLabel(t *testing.T) {
	output, err := runReleaseBumpLabel(t, "bug\ngood first issue\ndocumentation\n")
	if err != nil {
		t.Fatalf("select bump: %v\n%s", err, output)
	}
	if strings.TrimSpace(output) != "" {
		t.Errorf("bump = %q, want nothing for a pull request with no release label", output)
	}
}

func TestReleaseBumpLabelAcceptsNoLabelsAtAll(t *testing.T) {
	output, err := runReleaseBumpLabel(t, "")
	if err != nil {
		t.Fatalf("select bump: %v\n%s", err, output)
	}
	if strings.TrimSpace(output) != "" {
		t.Errorf("bump = %q, want nothing for an unlabelled pull request", output)
	}
}

// Production mutation: picking one of two release labels cuts a release of a
// size the author did not ask for, and the wrong choice cannot be undone once
// the tag is published.
func TestReleaseBumpLabelRefusesTwoReleaseLabels(t *testing.T) {
	output, err := runReleaseBumpLabel(t, "release:patch\nrelease:minor\n")
	if err == nil {
		t.Fatalf("select bump chose between two release labels:\n%s", output)
	}
	if !strings.Contains(output, "release:patch") || !strings.Contains(output, "release:minor") {
		t.Errorf("output = %q, want both labels named", output)
	}
}

// GitHub label names may contain spaces, and reading fields rather than whole
// lines would split "good first issue" into three labels.
func TestReleaseBumpLabelReadsWholeLines(t *testing.T) {
	output, err := runReleaseBumpLabel(t, "good first issue\nrelease:minor\nhelp wanted\n")
	if err != nil {
		t.Fatalf("select bump: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(output); got != "minor" {
		t.Errorf("bump = %q, want minor", got)
	}
}

func TestReleaseBumpLabelReadsALastLineWithoutANewline(t *testing.T) {
	output, err := runReleaseBumpLabel(t, "bug\nrelease:major")
	if err != nil {
		t.Fatalf("select bump: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(output); got != "major" {
		t.Errorf("bump = %q, want major", got)
	}
}

// A label that merely starts with a release prefix is not a release label.
func TestReleaseBumpLabelIgnoresNearMisses(t *testing.T) {
	output, err := runReleaseBumpLabel(t, "release\nrelease:patchy\nreleased:patch\n")
	if err != nil {
		t.Fatalf("select bump: %v\n%s", err, output)
	}
	if strings.TrimSpace(output) != "" {
		t.Errorf("bump = %q, want nothing for labels that only resemble release labels", output)
	}
}

func runReleaseBumpLabel(t *testing.T, labels string) (string, error) {
	t.Helper()
	return runReleaseScript(t, "", "release-bump-label.sh", labels)
}
