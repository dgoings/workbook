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
			options: []string{
				"title", "description", "status", "priority", "label", "clear-labels",
				"comment", "edit-comment", "remove-comment",
				"attach-file", "attach-url", "attach-label", "remove-attachment",
				"json",
			},
		},
		{
			name:   "show",
			target: []string{"show"},
			synopsis: "Usage: workbook show <id-or-prefix> [--history [--limit <n>] [--all]] " +
				"[--compare <commit> <commit>] [--get-attachment <id-or-prefix> [--out <path>]] [--json]",
			positionals: []string{"<id-or-prefix>"},
			options:     []string{"history", "limit", "all", "compare", "get-attachment", "out", "json"},
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
			header := strings.Index(got, "Options:\n")
			if header == -1 {
				t.Fatalf("help = %q, want Options section", got)
			}
			// The count and the ordering are properties of the options list
			// itself, so they are asked of the list rather than of the whole
			// page. A description that names the flags it explains — which the
			// update help does, because the flag matrix is the thing that needs
			// explaining — would otherwise be read as a duplicate listing.
			options := got[header:]
			for _, option := range test.options {
				if count := strings.Count(options, "  --"+option); count != 1 {
					t.Errorf("help options = %q, --%s count = %d, want 1", options, option, count)
				}
			}
			optionLines := make([]string, 0, len(test.options))
			for _, option := range test.options {
				optionLines = append(optionLines, "--"+option)
			}
			assertInOrder(t, options, optionLines)
		})
	}
}

// usageSynopsisExceptions are the two commands the global usage deliberately
// spells differently from their schema synopsis, with the reason each one does.
// Everything else must agree, and an entry here that stops matching fails as
// stale rather than quietly covering for a real divergence.
var usageSynopsisExceptions = map[string]string{
	// serve prints the address it will try rather than <address>, because the
	// port is the thing a reader of the usage wants to know.
	"serve": "  serve [--addr 127.0.0.1:7331]",
	// hooks names its one subcommand, because <command> would tell a reader
	// nothing about a verb that has exactly one.
	"hooks": "  hooks install [--json]",
}

// The global usage constant is the fourth place a verb's shape is written down,
// after the schema synopsis, the schema options, and `workbook help <verb>`. It
// is what an invocation error prints, so a flag added everywhere else and not
// here leaves the one surface a caller sees when they get the command wrong
// describing a command that no longer exists.
func TestGlobalUsageAgreesWithEverySynopsis(t *testing.T) {
	for _, name := range commandOrder {
		want := "  " + strings.TrimPrefix(commandSchemas[name].Synopsis, "workbook ")
		if exception, listed := usageSynopsisExceptions[name]; listed {
			if !strings.Contains(usage, exception+"\n") {
				t.Errorf("usage = %q, want the %s exception line %q", usage, name, exception)
			}
			if strings.Contains(usage, want+"\n") {
				t.Errorf("usage carries both the %s synopsis and its exception; drop the exception", name)
			}
			continue
		}
		if !strings.Contains(usage, want+"\n") {
			t.Errorf("usage = %q, want the line %q", usage, want)
		}
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
		"setup":  {"key": stringFlag, "no-docs": boolFlag, "no-sync": boolFlag, "skill-dir": stringFlag, "no-skill": boolFlag, "force": boolFlag, "json": boolFlag},
		"config": {},
		"docs":   {},
		"status": {},
		"create": {"description": stringFlag, "status": stringFlag, "priority": stringFlag, "label": stringFlag, "no-sync": boolFlag, "json": boolFlag},
		"list":   {"status": stringFlag, "priority": stringFlag, "label": stringFlag, "all": boolFlag, "json": boolFlag},
		"board":  {"wide": boolFlag, "narrow": boolFlag, "json": boolFlag},
		"show": {
			"history": boolFlag, "limit": stringFlag, "all": boolFlag, "compare": pairFlag,
			"get-attachment": stringFlag, "out": stringFlag, "json": boolFlag,
		},
		"update": {
			"title": stringFlag, "description": stringFlag, "status": stringFlag, "priority": stringFlag,
			"label": stringFlag, "clear-labels": boolFlag,
			"comment": stringFlag, "edit-comment": stringFlag, "remove-comment": stringFlag,
			"attach-file": stringFlag, "attach-url": stringFlag, "attach-label": stringFlag,
			"remove-attachment": stringFlag,
			"no-sync":           boolFlag, "json": boolFlag,
		},
		"delete":   {"no-sync": boolFlag, "json": boolFlag},
		"restore":  {"into": stringFlag, "no-sync": boolFlag, "json": boolFlag},
		"serve":    {"addr": stringFlag},
		"fetch":    {"json": boolFlag},
		"push":     {"json": boolFlag},
		"sync":     {"json": boolFlag, "watch": boolFlag, "interval": stringFlag, "status": boolFlag},
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
