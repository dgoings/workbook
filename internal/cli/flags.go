package cli

import (
	"flag"
	"io"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

type stringListValue struct {
	values []string
	set    bool
}

type flagKind uint8

const (
	stringFlag flagKind = iota
	boolFlag
)

type optionMetadata struct {
	Name        string
	Kind        flagKind
	Value       string
	Description string
}

type commandMetadata struct {
	Name        string
	Synopsis    string
	Description string
	Positionals []string
	Options     []optionMetadata
	Subcommands map[string]commandMetadata
}

func (metadata commandMetadata) optionKind(name string) (flagKind, bool) {
	for _, option := range metadata.Options {
		if option.Name == name {
			return option.Kind, true
		}
	}
	return 0, false
}

func commandMetadataFor(target []string) (commandMetadata, bool) {
	if len(target) == 0 {
		return commandMetadata{}, false
	}
	metadata, exists := commandSchemas[target[0]]
	if !exists {
		return commandMetadata{}, false
	}
	for _, name := range target[1:] {
		metadata, exists = metadata.Subcommands[name]
		if !exists {
			return commandMetadata{}, false
		}
	}
	return metadata, true
}

func commandInvocationMetadata(args []string) (commandMetadata, []string, bool) {
	if len(args) == 0 {
		return commandMetadata{}, nil, false
	}
	root, exists := commandMetadataFor(args[:1])
	if !exists {
		return commandMetadata{}, nil, false
	}

	optionArgs := args[1:]
	for range root.Positionals {
		if len(optionArgs) == 0 || !isRequiredFirstArgument(optionArgs[0]) {
			break
		}
		optionArgs = optionArgs[1:]
	}

	if root.Name != "hooks" {
		return root, optionArgs, true
	}
	metadata, exists := commandMetadataFor([]string{"hooks", "install"})
	return metadata, optionArgs, exists
}

var commandSchemas = map[string]commandMetadata{
	"init": {
		Name:        "init",
		Synopsis:    "workbook init [options]",
		Description: "Initialize Workbook in the current Git repository.",
		Options: []optionMetadata{
			{Name: "key", Kind: stringFlag, Value: "<key>", Description: "project key"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"create": {
		Name:        "create",
		Synopsis:    "workbook create <title> [options]",
		Description: "Create a task.",
		Positionals: []string{"<title>"},
		Options: []optionMetadata{
			{Name: "description", Kind: stringFlag, Value: "<text>", Description: "task description"},
			{Name: "status", Kind: stringFlag, Value: "<status>", Description: "task status"},
			{Name: "priority", Kind: stringFlag, Value: "<priority>", Description: "task priority"},
			{Name: "label", Kind: stringFlag, Value: "<label>", Description: "task label"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"list": {
		Name:        "list",
		Synopsis:    "workbook list [options]",
		Description: "List tasks.",
		Options: []optionMetadata{
			{Name: "status", Kind: stringFlag, Value: "<status>", Description: "task status"},
			{Name: "priority", Kind: stringFlag, Value: "<priority>", Description: "task priority"},
			{Name: "label", Kind: stringFlag, Value: "<label>", Description: "task label"},
			{Name: "all", Kind: boolFlag, Description: "include tombstoned tasks"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"board": {
		Name:        "board",
		Synopsis:    "workbook board [--wide | --narrow] [--json]",
		Description: "Show the task board.",
		Options: []optionMetadata{
			{Name: "wide", Kind: boolFlag, Description: "force wide board layout"},
			{Name: "narrow", Kind: boolFlag, Description: "force narrow board layout"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"show": {
		Name:        "show",
		Synopsis:    "workbook show <id-or-prefix> [--json]",
		Description: "Show a task.",
		Positionals: []string{"<id-or-prefix>"},
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"update": {
		Name:        "update",
		Synopsis:    "workbook update <id-or-prefix> [options]",
		Description: "Update a task.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "title", Kind: stringFlag, Value: "<title>", Description: "task title"},
			{Name: "description", Kind: stringFlag, Value: "<text>", Description: "task description"},
			{Name: "status", Kind: stringFlag, Value: "<status>", Description: "task status"},
			{Name: "priority", Kind: stringFlag, Value: "<priority>", Description: "task priority"},
			{Name: "label", Kind: stringFlag, Value: "<label>", Description: "replacement task label"},
			{Name: "clear-labels", Kind: boolFlag, Description: "replace labels with an empty set"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"delete": {
		Name:        "delete",
		Synopsis:    "workbook delete <id-or-prefix> [--json]",
		Description: "Delete a task.",
		Positionals: []string{"<id-or-prefix>"},
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"serve": {
		Name:        "serve",
		Synopsis:    "workbook serve [--addr <address>]",
		Description: "Serve the local web board.",
		Options:     []optionMetadata{{Name: "addr", Kind: stringFlag, Value: "<address>", Description: "listener address"}},
	},
	"fetch": {
		Name:        "fetch",
		Synopsis:    "workbook fetch [--json]",
		Description: "Fetch shared task refs from origin.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"push": {
		Name:        "push",
		Synopsis:    "workbook push [--json]",
		Description: "Push local task refs to origin.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"sync": {
		Name:        "sync",
		Synopsis:    "workbook sync [--json]",
		Description: "Fetch then push shared task refs.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"hooks": {
		Name:        "hooks",
		Synopsis:    "workbook hooks <command> [options]",
		Description: "Manage optional Git hooks.",
		Positionals: []string{"<command>"},
		Subcommands: map[string]commandMetadata{
			"install": {
				Name:        "install",
				Synopsis:    "workbook hooks install [options]",
				Description: "Install the Workbook pre-push hook.",
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
		},
	},
	"move": {
		Name:        "move",
		Synopsis:    "workbook move <id-or-prefix> (--before <id-or-prefix> | --after <id-or-prefix>) [--json]",
		Description: "Move a task within its status and priority bucket.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "before", Kind: stringFlag, Value: "<id-or-prefix>", Description: "move before task ID"},
			{Name: "after", Kind: stringFlag, Value: "<id-or-prefix>", Description: "move after task ID"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"depend": {
		Name:        "depend",
		Synopsis:    "workbook depend <id-or-prefix> <dependency-id-or-prefix> [--json]",
		Description: "Add a task dependency.",
		Positionals: []string{"<id-or-prefix>", "<dependency-id-or-prefix>"},
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"free": {
		Name:        "free",
		Synopsis:    "workbook free <id-or-prefix> <dependency-id-or-prefix> [--json]",
		Description: "Remove a task dependency.",
		Positionals: []string{"<id-or-prefix>", "<dependency-id-or-prefix>"},
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"next": {
		Name:        "next",
		Synopsis:    "workbook next [--json]",
		Description: "Show the next eligible task.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"rebuild": {
		Name:        "rebuild",
		Synopsis:    "workbook rebuild [--json]",
		Description: "Rebuild the local SQLite task projection.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"version": {
		Name:        "version",
		Synopsis:    "workbook version [--json]",
		Description: "Show Workbook build metadata.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
}

var commandOrder = []string{
	"init", "create", "list", "board", "show", "update", "delete", "move", "depend", "free", "next", "rebuild", "version", "fetch", "push", "sync", "hooks", "serve",
}

type commandFlagSet struct {
	*flag.FlagSet
	schema commandMetadata
}

func (value *stringListValue) String() string {
	if len(value.values) == 0 {
		return ""
	}
	return value.values[len(value.values)-1]
}

func (value *stringListValue) Set(item string) error {
	value.values = append(value.values, item)
	value.set = true
	return nil
}

func newFlagSet(target ...string) *commandFlagSet {
	schema, exists := commandMetadataFor(target)
	if !exists {
		panic("missing flag schema for " + strings.Join(target, " "))
	}
	flagSet := flag.NewFlagSet(target[0], flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	return &commandFlagSet{FlagSet: flagSet, schema: schema}
}

func (flags *commandFlagSet) String(name, value, usage string) *string {
	flags.requireKind(name, stringFlag)
	return flags.FlagSet.String(name, value, usage)
}

func (flags *commandFlagSet) Bool(name string, value bool, usage string) *bool {
	flags.requireKind(name, boolFlag)
	return flags.FlagSet.Bool(name, value, usage)
}

func (flags *commandFlagSet) Var(value flag.Value, name, usage string) {
	flags.requireKind(name, stringFlag)
	flags.FlagSet.Var(value, name, usage)
}

func (flags *commandFlagSet) requireKind(name string, wanted flagKind) {
	if got, exists := flags.schema.optionKind(name); !exists || got != wanted {
		panic("flag schema mismatch for " + flags.Name() + " --" + name)
	}
}

func parseFlags(flags *commandFlagSet, args []string) error {
	flags.validateSchema()
	if err := flags.Parse(args); err != nil {
		return core.Wrap(core.CategoryInvocation, "invalid "+flags.Name()+" arguments", err)
	}
	if flags.NArg() != 0 {
		return core.Errorf(core.CategoryInvocation, "%s accepts no additional positional arguments", flags.Name())
	}
	return nil
}

func (flags *commandFlagSet) validateSchema() {
	defined := make(map[string]struct{}, flags.NFlag())
	flags.VisitAll(func(item *flag.Flag) {
		defined[item.Name] = struct{}{}
	})
	for _, option := range flags.schema.Options {
		if _, exists := defined[option.Name]; !exists {
			panic("flag schema defines unregistered " + flags.Name() + " --" + option.Name)
		}
	}
}

func requiredFirstArgument(command, name string, args []string) (string, []string, error) {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation, "%s must be the first argument after %s", name, command)
	}
	return args[0], args[1:], nil
}

func requiredArguments(command string, names []string, args []string) ([]string, []string, error) {
	values := make([]string, 0, len(names))
	for _, name := range names {
		if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
			return nil, nil, core.Errorf(core.CategoryInvocation, "%s must be the first argument after %s", name, command)
		}
		values = append(values, args[0])
		args = args[1:]
	}
	return values, args, nil
}

func requestedJSON(args []string) bool {
	if len(args) == 0 {
		return false
	}

	schema, optionArgs, exists := commandInvocationMetadata(args)
	if !exists {
		schema = commandMetadata{Options: []optionMetadata{{Name: "json", Kind: boolFlag}}}
		optionArgs = args[1:]
	}
	args = optionArgs

	jsonMode := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			break
		}
		name, value, hasValue, isFlag := splitFlag(argument)
		if !isFlag {
			break
		}

		kind, known := schema.optionKind(name)
		if !known {
			continue
		}
		if kind == stringFlag {
			if !hasValue && index+1 < len(args) {
				index++
			}
			continue
		}

		if !hasValue {
			if name == "json" {
				jsonMode = true
			}
			continue
		}

		parsed, err := strconv.ParseBool(value)
		if err != nil {
			if name == "json" {
				return true
			}
			return jsonMode
		}
		if name == "json" {
			jsonMode = parsed
		}
	}
	return jsonMode
}

func isRequiredFirstArgument(argument string) bool {
	return argument == "" || argument[0] != '-'
}

func splitFlag(argument string) (name, value string, hasValue, ok bool) {
	if len(argument) < 2 || argument[0] != '-' {
		return "", "", false, false
	}
	prefixLength := 1
	if argument[1] == '-' {
		prefixLength = 2
	}
	if len(argument) == prefixLength {
		return "", "", false, false
	}
	name, value, hasValue = strings.Cut(argument[prefixLength:], "=")
	return name, value, hasValue, name != ""
}
