package cli

import (
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The tag set is a replacement, exactly as `update --label` is, and --clear-tags
// is how an empty set is spelled. Both together is a contradiction rather than a
// precedence rule to remember.
func TestStatusTagReplacesTheWholeSet(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	replaced := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--tag", "next", "--tag", "done", "--json")
	if got := replaced.Change.Tags; len(got) != 2 || got[0] != "done" || got[1] != "next" {
		t.Fatalf("tags = %#v, want the canonical done,next", got)
	}

	narrowed := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--tag", "next", "--json")
	if got := narrowed.Change.Tags; len(got) != 1 || got[0] != "next" {
		t.Fatalf("tags = %#v, want the tag left out taken away", got)
	}

	cleared := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--clear-tags", "--json")
	if len(cleared.Change.Tags) != 0 {
		t.Fatalf("tags = %#v, want an empty set", cleared.Change.Tags)
	}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "both",
			args: []string{"status", "tag", "ready", "--tag", "next", "--clear-tags"},
			want: "cannot use --tag with --clear-tags",
		},
		{
			name: "neither",
			args: []string{"status", "tag", "ready"},
			want: "status tag requires --tag or --clear-tags",
		},
		{
			name: "add with both anchors",
			args: []string{"status", "add", "triage", "--before", "ready", "--after", "backlog"},
			want: "status add accepts --before or --after, not both",
		},
		{
			name: "move with neither anchor",
			args: []string{"status", "move", "ready"},
			want: "status move requires exactly one of --before or --after",
		},
		{
			name: "move with both anchors",
			args: []string{"status", "move", "ready", "--before", "done", "--after", "backlog"},
			want: "status move requires exactly one of --before or --after",
		},
		{
			name: "log windowing",
			args: []string{"status", "log", "--limit", "2", "--all"},
			want: "cannot use --limit with --all",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, _, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("%v = code %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("%v stderr = %q, want %q", test.args, stderr, test.want)
			}
		})
	}
}

// An unknown tag is a typo, not corrupt data, and the refusal names the three
// tags that exist rather than sending somebody to the help.
func TestStatusRefusesAnUnknownTagAsAValidationFailure(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{"status", "add", "triage", "--tag", "urgent", "--json"},
		{"status", "tag", "ready", "--tag", "urgent", "--json"},
		{"status", "untag", "ready", "urgent", "--json"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 5 {
			t.Fatalf("%v = code %d, want 5; stderr = %q", args, code, stderr)
		}
		if stdout != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation,
			`unsupported status tag "urgent"; the tags are: default, done, next`)
	}
}
