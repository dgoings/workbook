package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestGlobalHelpListsHelpAndTopLevelCommands(t *testing.T) {
	var output bytes.Buffer
	renderGlobalHelp(&output)

	if !strings.Contains(output.String(), "help [command]") {
		t.Fatalf("global help = %q, want help command", output.String())
	}
	for name := range commandSchemas {
		if !strings.Contains(output.String(), "  "+name) {
			t.Errorf("global help = %q, want command %q", output.String(), name)
		}
	}
}

func TestCommandHelp(t *testing.T) {
	for _, test := range []struct {
		name        string
		target      []string
		synopsis    string
		positionals []string
		options     []string
	}{
		{
			name:        "create",
			target:      []string{"create"},
			synopsis:    "Usage: workbook create <title> [options]",
			positionals: []string{"<title>"},
			options:     []string{"description", "status", "priority", "label", "json"},
		},
		{
			name:        "update",
			target:      []string{"update"},
			synopsis:    "Usage: workbook update <id-or-prefix> [options]",
			positionals: []string{"<id-or-prefix>"},
			options:     []string{"title", "description", "status", "priority", "label", "clear-labels", "json"},
		},
		{
			name:        "hooks install",
			target:      []string{"hooks", "install"},
			synopsis:    "Usage: workbook hooks install [options]",
			positionals: nil,
			options:     []string{"json"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderCommandHelp(&output, test.target); err != nil {
				t.Fatalf("renderCommandHelp(%q) error = %v", test.target, err)
			}

			got := output.String()
			if !strings.Contains(got, test.synopsis) {
				t.Errorf("help = %q, want synopsis %q", got, test.synopsis)
			}
			for _, positional := range test.positionals {
				if !strings.Contains(got, positional) {
					t.Errorf("help = %q, want positional %q", got, positional)
				}
			}
			if !strings.Contains(got, "Options:\n") {
				t.Errorf("help = %q, want Options section", got)
			}
			for _, option := range test.options {
				if count := strings.Count(got, "--"+option); count != 1 {
					t.Errorf("help = %q, --%s count = %d, want 1", got, option, count)
				}
			}
		})
	}
}

func TestHelpMetadataMatchesSchemas(t *testing.T) {
	want := map[string]map[string]flagKind{
		"init":   {"key": stringFlag, "json": boolFlag},
		"create": {"description": stringFlag, "status": stringFlag, "priority": stringFlag, "label": stringFlag, "json": boolFlag},
		"list":   {"status": stringFlag, "priority": stringFlag, "label": stringFlag, "all": boolFlag, "json": boolFlag},
		"board":  {"wide": boolFlag, "narrow": boolFlag, "json": boolFlag},
		"show":   {"json": boolFlag},
		"update": {"title": stringFlag, "description": stringFlag, "status": stringFlag, "priority": stringFlag, "label": stringFlag, "clear-labels": boolFlag, "json": boolFlag},
		"delete": {"json": boolFlag},
		"serve":  {"addr": stringFlag},
		"fetch":  {"json": boolFlag},
		"push":   {"json": boolFlag},
		"sync":   {"json": boolFlag},
		"hooks":  {"json": boolFlag},
		"move":   {"before": stringFlag, "after": stringFlag, "json": boolFlag},
		"depend": {"json": boolFlag},
		"free":   {"json": boolFlag},
		"next":   {"json": boolFlag},
	}

	for command, schema := range commandSchemas {
		expected, exists := want[command]
		if !exists {
			t.Errorf("%s has no expected parser schema", command)
			continue
		}
		if len(schema.Options) != len(expected) {
			t.Errorf("%s options = %d, want %d", command, len(schema.Options), len(expected))
		}
		for _, option := range schema.Options {
			if option.Description == "" {
				t.Errorf("%s --%s has no help description", command, option.Name)
			}
			kind, ok := expected[option.Name]
			if !ok {
				t.Errorf("%s --%s is not in the expected parser schema", command, option.Name)
				continue
			}
			if kind != option.Kind {
				t.Errorf("%s --%s kind = %v, want %v", command, option.Name, kind, option.Kind)
			}
		}
	}

	var output bytes.Buffer
	err := renderCommandHelp(&output, []string{"unknown"})
	if core.CategoryOf(err) != core.CategoryInvocation {
		t.Fatalf("unknown command error category = %q, want invocation; error = %v", core.CategoryOf(err), err)
	}
	err = renderCommandHelp(&output, []string{"hooks", "unknown"})
	if core.CategoryOf(err) != core.CategoryInvocation {
		t.Fatalf("unknown subcommand error category = %q, want invocation; error = %v", core.CategoryOf(err), err)
	}
}
