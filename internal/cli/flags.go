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
	// pairFlag is one option that consumes the two arguments after it, as
	// `--compare <commit> <commit>` does. The flag package cannot express it,
	// so a pair option is lifted out of the argument list before parsing.
	pairFlag
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
	// SubcommandOrder lists Subcommands in the order help should present them.
	SubcommandOrder []string
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

// subcommandUnion returns metadata whose options are the union of every
// subcommand's options, first declaration winning on a repeated name.
func subcommandUnion(root commandMetadata) commandMetadata {
	union := root
	seen := make(map[string]bool, len(root.Options))
	for _, option := range union.Options {
		seen[option.Name] = true
	}
	for _, name := range root.SubcommandOrder {
		for _, option := range root.Subcommands[name].Options {
			if seen[option.Name] {
				continue
			}
			seen[option.Name] = true
			union.Options = append(union.Options, option)
		}
	}
	return union
}

func commandInvocationMetadata(args []string) (commandMetadata, []string, bool) {
	if len(args) == 0 {
		return commandMetadata{}, nil, false
	}
	root, exists := commandMetadataFor(args[:1])
	if !exists {
		return commandMetadata{}, nil, false
	}

	if len(root.Subcommands) > 0 {
		optionArgs := args[1:]
		if len(optionArgs) > 0 && isRequiredFirstArgument(optionArgs[0]) {
			name := optionArgs[0]
			optionArgs = optionArgs[1:]
			if metadata, exists := commandMetadataFor([]string{args[0], name}); exists {
				return metadata, optionArgs, true
			}
		}
		// The subcommand is missing or unknown, but recognizing options still
		// decides the output format of the resulting error, so fall back to
		// every option any subcommand accepts.
		return subcommandUnion(root), optionArgs, true
	}

	optionArgs := args[1:]
	for range root.Positionals {
		if len(optionArgs) == 0 || !isRequiredFirstArgument(optionArgs[0]) {
			break
		}
		optionArgs = optionArgs[1:]
	}
	return root, optionArgs, true
}

var commandSchemas = map[string]commandMetadata{
	"setup": {
		Name:        "setup",
		Synopsis:    "workbook setup [options]",
		Description: "Bootstrap Workbook in the current Git repository: create or validate\nproject identity, install managed agent documentation, and synchronize\nshared task refs with origin.",
		Options: []optionMetadata{
			{Name: "key", Kind: stringFlag, Value: "<key>", Description: "project key"},
			{Name: "no-docs", Kind: boolFlag, Description: "skip managed agent documentation"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "skill-dir", Kind: stringFlag, Value: "<dir>", Description: "install the Workbook skill here"},
			{Name: "no-skill", Kind: boolFlag, Description: "leave the Workbook skill alone"},
			{Name: "force", Kind: boolFlag, Description: "overwrite locally modified managed documentation"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"config": {
		Name:            "config",
		Synopsis:        "workbook config <command> [options]",
		Description:     "Inspect and record this project's Workbook settings.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"show", "set", "unset"},
		Subcommands: map[string]commandMetadata{
			"show": {
				Name:        "show",
				Synopsis:    "workbook config show [--json]",
				Description: "Show the project configuration and resolved settings.",
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
			"set": {
				Name:        "set",
				Synopsis:    "workbook config set <setting> <value> [--json]",
				Description: "Record a project setting.",
				Positionals: []string{"<setting>", "<value>"},
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
			"unset": {
				Name:        "unset",
				Synopsis:    "workbook config unset <setting> [--json]",
				Description: "Clear a project setting so the user configuration decides.",
				Positionals: []string{"<setting>"},
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
		},
	},
	"docs": {
		Name:            "docs",
		Synopsis:        "workbook docs <command> [options]",
		Description:     "Manage the agent documentation Workbook generates for this project.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"install", "update", "status", "remove"},
		Subcommands: map[string]commandMetadata{
			"install": {
				Name:        "install",
				Synopsis:    "workbook docs install [options]",
				Description: "Install or refresh managed agent documentation.",
				Options: []optionMetadata{
					{Name: "create", Kind: stringFlag, Value: "<file>", Description: "also create this documentation target"},
					{Name: "skill-dir", Kind: stringFlag, Value: "<dir>", Description: "install the Workbook skill here"},
					{Name: "no-skill", Kind: boolFlag, Description: "leave the Workbook skill alone"},
					{Name: "force", Kind: boolFlag, Description: "overwrite locally modified files"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"update": {
				Name:        "update",
				Synopsis:    "workbook docs update [options]",
				Description: "Refresh managed agent documentation.",
				Options: []optionMetadata{
					{Name: "skill-dir", Kind: stringFlag, Value: "<dir>", Description: "install the Workbook skill here"},
					{Name: "no-skill", Kind: boolFlag, Description: "leave the Workbook skill alone"},
					{Name: "force", Kind: boolFlag, Description: "overwrite locally modified files"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"status": {
				Name:        "status",
				Synopsis:    "workbook docs status [options]",
				Description: "Report whether managed documentation is current, stale, or modified.",
				Options: []optionMetadata{
					{Name: "skill-dir", Kind: stringFlag, Value: "<dir>", Description: "look for the Workbook skill here"},
					{Name: "no-skill", Kind: boolFlag, Description: "leave the Workbook skill alone"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"remove": {
				Name:        "remove",
				Synopsis:    "workbook docs remove [options]",
				Description: "Remove managed documentation, preserving user-authored content.",
				Options: []optionMetadata{
					{Name: "skill-dir", Kind: stringFlag, Value: "<dir>", Description: "look for the Workbook skill here"},
					{Name: "no-skill", Kind: boolFlag, Description: "leave the Workbook skill alone"},
					{Name: "force", Kind: boolFlag, Description: "remove locally modified files"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
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
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
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
		Synopsis:    "workbook show <id-or-prefix> [--history [--limit <n>] [--all]] [--compare <commit> <commit>] [--json]",
		Description: "Show a task.\n\nWith --history, list how the task reached its current state, ordered by the\ncommit chain rather than by wall time. With --compare, diff two points in that\nhistory in the order given. Both name entries by full Git commit object ID.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "history", Kind: boolFlag, Description: "list this task's changes"},
			{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "show this many recent changes (default 10)"},
			{Name: "all", Kind: boolFlag, Description: "show every change"},
			{Name: "compare", Kind: pairFlag, Value: "<commit> <commit>", Description: "diff two commits from this task's history"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
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
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"delete": {
		Name:        "delete",
		Synopsis:    "workbook delete <id-or-prefix> [--json]",
		Description: "Delete a task.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"restore": {
		Name:        "restore",
		Synopsis:    "workbook restore <id-or-prefix> [--json]",
		Description: "Restore a tombstoned task.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
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
		Synopsis:    "workbook sync [--watch [--interval <duration>]] [--status] [--json]",
		Description: "Fetch then push shared task refs, once or continuously.",
		Options: []optionMetadata{
			{Name: "watch", Kind: boolFlag, Description: "synchronize continuously until interrupted"},
			{Name: "interval", Kind: stringFlag, Value: "<duration>", Description: "time between synchronizations while watching (default 5s)"},
			{Name: "status", Kind: boolFlag, Description: "report whether a watcher is running for this repository"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"hooks": {
		Name:            "hooks",
		Synopsis:        "workbook hooks <command> [options]",
		Description:     "Manage optional Git hooks.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"install"},
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
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"depend": {
		Name:        "depend",
		Synopsis:    "workbook depend <id-or-prefix> <dependency-id-or-prefix> [--json]",
		Description: "Add a task dependency.",
		Positionals: []string{"<id-or-prefix>", "<dependency-id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"free": {
		Name:        "free",
		Synopsis:    "workbook free <id-or-prefix> <dependency-id-or-prefix> [--json]",
		Description: "Remove a task dependency.",
		Positionals: []string{"<id-or-prefix>", "<dependency-id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"next": {
		Name:        "next",
		Synopsis:    "workbook next [--json]",
		Description: "Show the next eligible task.",
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with origin"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"rebuild": {
		Name:        "rebuild",
		Synopsis:    "workbook rebuild [--json]",
		Description: "Rebuild the local SQLite task projection.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"validate": {
		Name:        "validate",
		Synopsis:    "workbook validate [--full] [--json]",
		Description: "Validate complete task histories and stored checkpoints.",
		Options: []optionMetadata{
			{Name: "full", Kind: boolFlag, Description: "bypass cached validation results"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"version": {
		Name:        "version",
		Synopsis:    "workbook version [--json]",
		Description: "Show Workbook build metadata.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
}

var commandOrder = []string{
	"setup", "create", "list", "board", "show", "update", "delete", "restore", "move", "depend", "free", "next", "rebuild", "validate", "version", "fetch", "push", "sync", "config", "docs", "hooks", "serve",
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
		// A pair option never reaches the flag set: it is lifted out of the
		// argument list first, because the flag package has no two-value form.
		if option.Kind == pairFlag {
			continue
		}
		if _, exists := defined[option.Name]; !exists {
			panic("flag schema defines unregistered " + flags.Name() + " --" + option.Name)
		}
	}
}

// takePairOption removes `--name a b` from args and returns its two values.
// Absent option, absent values, and a repeated option are all distinguishable
// so the caller can report each precisely.
func takePairOption(command, name string, args []string) ([2]string, []string, bool, error) {
	var values [2]string
	found := false
	rest := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			rest = append(rest, args[index:]...)
			break
		}
		optionName, _, hasValue, isFlag := splitFlag(argument)
		if !isFlag || optionName != name {
			rest = append(rest, argument)
			continue
		}
		if hasValue {
			return values, nil, false, core.Errorf(
				core.CategoryInvocation,
				"%s --%s takes two separate arguments, not --%s=<value>",
				command, name, name,
			)
		}
		if found {
			return values, nil, false, core.Errorf(core.CategoryInvocation, "%s accepts --%s once", command, name)
		}
		if index+2 >= len(args) || !isRequiredFirstArgument(args[index+1]) || !isRequiredFirstArgument(args[index+2]) {
			return values, nil, false, core.Errorf(
				core.CategoryInvocation,
				"%s --%s requires two arguments",
				command, name,
			)
		}
		values[0], values[1] = args[index+1], args[index+2]
		found = true
		index += 2
	}
	if !found {
		return values, args, false, nil
	}
	return values, rest, true, nil
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
		if kind == stringFlag || kind == pairFlag {
			for skipped := 0; skipped < optionValueCount(kind) && !hasValue && index+1 < len(args); skipped++ {
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

// optionValueCount reports how many following arguments one option consumes.
func optionValueCount(kind flagKind) int {
	switch kind {
	case stringFlag:
		return 1
	case pairFlag:
		return 2
	default:
		return 0
	}
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
