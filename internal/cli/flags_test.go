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
	assertInOrder(t, output.String(), []string{
		"  setup", "  create", "  list", "  board", "  show", "  update", "  delete", "  restore", "  move",
		"  depend", "  free", "  next", "  rebuild", "  validate", "  version", "  fetch", "  push", "  sync", "  docs", "  hooks", "  serve", "  help [command]",
	})
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
		{
			name:        "rebuild",
			target:      []string{"rebuild"},
			synopsis:    "Usage: workbook rebuild [--json]",
			positionals: nil,
			options:     []string{"json"},
		},
		{
			name:        "validate",
			target:      []string{"validate"},
			synopsis:    "Usage: workbook validate [--full] [--json]",
			positionals: nil,
			options:     []string{"full", "json"},
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
				if count := strings.Count(got, "  --"+option); count != 1 {
					t.Errorf("help = %q, --%s count = %d, want 1", got, option, count)
				}
			}
			optionLines := make([]string, 0, len(test.options))
			for _, option := range test.options {
				optionLines = append(optionLines, "--"+option)
			}
			assertInOrder(t, got, optionLines)
		})
	}
}

func TestHooksInstallUsesChildMetadataForParserFlags(t *testing.T) {
	hooks := commandSchemas["hooks"]
	if len(hooks.Options) != 0 {
		t.Fatalf("hooks options = %#v, want no top-level options", hooks.Options)
	}
	install := hooks.Subcommands["install"]
	if len(install.Options) != 1 || install.Options[0].Name != "json" || install.Options[0].Kind != boolFlag {
		t.Fatalf("hooks install options = %#v, want JSON bool option", install.Options)
	}

	flags := newFlagSet("hooks", "install")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, []string{"--json"}); err != nil {
		t.Fatalf("parse hooks install JSON: %v", err)
	}
	if !*jsonMode {
		t.Fatal("hooks install JSON flag = false, want true")
	}
}

func TestHelpMetadataMatchesSchemas(t *testing.T) {
	want := map[string]map[string]flagKind{
		"setup":    {"key": stringFlag, "no-docs": boolFlag, "no-sync": boolFlag, "skill-dir": stringFlag, "no-skill": boolFlag, "force": boolFlag, "json": boolFlag},
		"docs":     {},
		"create":   {"description": stringFlag, "status": stringFlag, "priority": stringFlag, "label": stringFlag, "no-sync": boolFlag, "json": boolFlag},
		"list":     {"status": stringFlag, "priority": stringFlag, "label": stringFlag, "all": boolFlag, "json": boolFlag},
		"board":    {"wide": boolFlag, "narrow": boolFlag, "json": boolFlag},
		"show":     {"json": boolFlag},
		"update":   {"title": stringFlag, "description": stringFlag, "status": stringFlag, "priority": stringFlag, "label": stringFlag, "clear-labels": boolFlag, "no-sync": boolFlag, "json": boolFlag},
		"delete":   {"no-sync": boolFlag, "json": boolFlag},
		"restore":  {"no-sync": boolFlag, "json": boolFlag},
		"serve":    {"addr": stringFlag},
		"fetch":    {"json": boolFlag},
		"push":     {"json": boolFlag},
		"sync":     {"json": boolFlag},
		"hooks":    {},
		"move":     {"before": stringFlag, "after": stringFlag, "no-sync": boolFlag, "json": boolFlag},
		"depend":   {"no-sync": boolFlag, "json": boolFlag},
		"free":     {"no-sync": boolFlag, "json": boolFlag},
		"next":     {"no-sync": boolFlag, "json": boolFlag},
		"rebuild":  {"json": boolFlag},
		"validate": {"full": boolFlag, "json": boolFlag},
		"version":  {"json": boolFlag},
	}

	install := commandSchemas["hooks"].Subcommands["install"]
	if len(install.Options) != 1 {
		t.Fatalf("hooks install options = %d, want 1", len(install.Options))
	}
	option := install.Options[0]
	if option.Name != "json" || option.Kind != boolFlag || option.Description == "" {
		t.Errorf("hooks install option = %#v, want documented JSON bool", option)
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

func assertInOrder(t *testing.T, output string, values []string) {
	t.Helper()
	previous := -1
	for _, value := range values {
		index := strings.Index(output, value)
		if index == -1 {
			t.Errorf("output = %q, want %q", output, value)
			continue
		}
		if index <= previous {
			t.Errorf("output = %q, %q appears before its predecessor", output, value)
		}
		previous = index
	}
}
