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
	// optionalStringFlag is an option whose value may be left out, as
	// `--done-only [<duration>]` does. It takes the next argument only when
	// that argument is a value it accepts, so `--done-only --json` leaves
	// `--json` alone. The flag package cannot express it either, so it is
	// lifted out with takeOptionalValueOption before parsing.
	optionalStringFlag
)

type optionMetadata struct {
	Name        string
	Kind        flagKind
	Value       string
	Description string
}

type commandMetadata struct {
	Name string
	// Synopsis is one line per form of the command, each starting with
	// "workbook <name>". Most commands have one form.
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
				// A subcommand's own positionals are skipped exactly as a
				// top-level command's are. Leaving them in stopped the scan at
				// the first one, so `workbook config set a b --json` and every
				// `workbook status <verb> <status> --json` reported its failure
				// in human text to a caller that had asked for JSON.
				return metadata, trimPositionals(metadata, optionArgs), true
			}
		}
		// The subcommand is missing or unknown, but recognizing options still
		// decides the output format of the resulting error, so fall back to
		// every option any subcommand accepts — and skip as many leading
		// positionals as any of them takes, because an unknown subcommand's
		// arguments are exactly as opaque as a known one's and a caller who
		// asked for JSON asked for it either way.
		return subcommandUnion(root), trimPositionals(deepestPositionals(root), optionArgs), true
	}
	return root, trimPositionals(root, args[1:]), true
}

// deepestPositionals returns metadata carrying as many positionals as the
// wordiest subcommand takes, for a scan that has to get past arguments it
// cannot attribute to a subcommand it does not recognize. Over-skipping is
// safe here: everything past the options is a positional the parser will refuse
// anyway, and the scan stops at the first argument that is not a flag.
func deepestPositionals(root commandMetadata) commandMetadata {
	deepest := commandMetadata{}
	for _, name := range root.SubcommandOrder {
		if subcommand := root.Subcommands[name]; len(subcommand.Positionals) > len(deepest.Positionals) {
			deepest.Positionals = subcommand.Positionals
		}
	}
	return deepest
}

// trimPositionals drops the leading arguments a command takes as positionals,
// leaving the options behind for a scan that only understands options.
func trimPositionals(metadata commandMetadata, args []string) []string {
	for range metadata.Positionals {
		if len(args) == 0 || !isRequiredFirstArgument(args[0]) && args[0] != "-" {
			break
		}
		args = args[1:]
	}
	return args
}

var commandSchemas = map[string]commandMetadata{
	"setup": {
		Name:     "setup",
		Synopsis: "workbook setup [options]",
		Description: "Bootstrap Workbook in the current Git repository: create or validate\nproject identity, install managed agent documentation, and synchronize\nshared task refs with the tasks remote (origin unless configured).\n\n" +
			"With --tasks-only <url>, run in an empty folder, set up a folder that holds\n" +
			"only the project's Workbook refs from that remote: no branches, none of\n" +
			"the code, and no agent documentation. Running it again changes nothing.",
		Options: []optionMetadata{
			{Name: "key", Kind: stringFlag, Value: "<key>", Description: "project key for a new project"},
			{Name: "no-docs", Kind: boolFlag, Description: "skip managed agent documentation"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "skill-dir", Kind: stringFlag, Value: "<dir>", Description: "install the Workbook skill here"},
			{Name: "no-skill", Kind: boolFlag, Description: "leave the Workbook skill alone"},
			{Name: "force", Kind: boolFlag, Description: "overwrite locally modified managed documentation"},
			{Name: "tasks-only", Kind: stringFlag, Value: "<url>", Description: "set up an empty folder that holds only this remote's tasks"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"config": {
		Name:     "config",
		Synopsis: "workbook config <command> [options]",
		Description: "Inspect and record this project's Workbook settings.\n\n" +
			"`auto-sync` is a policy recorded in the tracked `.workbook/config.json`, so\n" +
			"it travels with the branch it is committed on.\n\n" +
			"`tasks-remote` is recorded there too. It names the Git remote, and with --url\n" +
			"its address, of a repository that holds the project's tasks in place of\n" +
			"origin; `workbook move-tasks` moves them there, and every checkout runs\n" +
			"`workbook setup` to add the remote. Unsetting it shares tasks through origin\n" +
			"again.\n\n" +
			"`project-name`, `primary-color` and `text-color` are display settings\n" +
			"recorded in the shared configuration history beside the statuses, so they\n" +
			"synchronize with everyone who fetches. A color is six hexadecimal digits\n" +
			"behind a hash, as in `#1a7f4b`. Clearing one returns the board to its\n" +
			"built-in presentation; nothing stores a default.",
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
				Synopsis:    "workbook config set <setting> <value> [--url <address>] [--no-sync] [--json]",
				Description: "Record a project setting.",
				Positionals: []string{"<setting>", "<value>"},
				Options: []optionMetadata{
					{Name: "url", Kind: stringFlag, Value: "<address>", Description: "the tasks repository's address (tasks-remote only)"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured) (display settings only)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"unset": {
				Name:        "unset",
				Synopsis:    "workbook config unset <setting> [--no-sync] [--json]",
				Description: "Clear a project setting so the default or the user configuration decides.",
				Positionals: []string{"<setting>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured) (display settings only)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
		},
	},
	"status": {
		Name:     "status",
		Synopsis: "workbook status <command> [options]",
		Description: "Inspect and change this project's status columns.\n\n" +
			"Statuses are project configuration, recorded in their own synchronized\n" +
			"history and shared with everyone who fetches. A status has a machine value\n" +
			"typed as `--status in-progress`, a display label, a position, and any of\n" +
			"three tags: `default` for where a new task lands, `next` for what `workbook\n" +
			"next` may return, and `done` for what satisfies a dependency.\n\n" +
			"Renaming or removing a status never rewrites a task. Stored values keep\n" +
			"resolving through the change, and each task settles the next time something\n" +
			"writes to it.\n\n" +
			"Every change regenerates `.workbook/guidelines.md`, which documents these\n" +
			"statuses for agents; `--no-docs` leaves it alone, and a file somebody edited\n" +
			"is reported rather than overwritten.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"list", "add", "rename", "label", "move", "tag", "untag", "delete", "log"},
		Subcommands: map[string]commandMetadata{
			"list": {
				Name:        "list",
				Synopsis:    "workbook status list [--json]",
				Description: "List this project's statuses, with the tasks in each.",
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
			"add": {
				Name:        "add",
				Synopsis:    "workbook status add <status> [--label <label>] [--before <status> | --after <status>] [--tag <tag>]... [--no-sync] [--no-docs] [--json]",
				Description: "Define a status.\n\nIt is added last unless --before or --after names where it goes, and its\nlabel is derived from its name unless --label gives one.",
				Positionals: []string{"<status>"},
				Options: []optionMetadata{
					{Name: "label", Kind: stringFlag, Value: "<label>", Description: "display label (default: derived from the status)"},
					{Name: "before", Kind: stringFlag, Value: "<status>", Description: "place before this status"},
					{Name: "after", Kind: stringFlag, Value: "<status>", Description: "place after this status"},
					{Name: "tag", Kind: stringFlag, Value: "<tag>", Description: "role to give it: default, next, or done (repeatable)"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"rename": {
				Name:        "rename",
				Synopsis:    "workbook status rename <status> <new-status> [--label <label>] [--no-sync] [--no-docs] [--json]",
				Description: "Give a status a new machine value.\n\nTasks stored under the old value keep resolving to this status. The display\nlabel follows the new name when it was the one the old name implied, and is\nkept when somebody chose it.",
				Positionals: []string{"<status>", "<new-status>"},
				Options: []optionMetadata{
					{Name: "label", Kind: stringFlag, Value: "<label>", Description: "display label to set with the rename"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"label": {
				Name:        "label",
				Synopsis:    "workbook status label <status> <display-label> [--no-sync] [--no-docs] [--json]",
				Description: "Change a status's display label. Nothing else changes.",
				Positionals: []string{"<status>", "<display-label>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"move": {
				Name:        "move",
				Synopsis:    "workbook status move <status> (--before <status> | --after <status>) [--no-sync] [--no-docs] [--json]",
				Description: "Move a status among its peers.",
				Positionals: []string{"<status>"},
				Options: []optionMetadata{
					{Name: "before", Kind: stringFlag, Value: "<status>", Description: "move before this status"},
					{Name: "after", Kind: stringFlag, Value: "<status>", Description: "move after this status"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"tag": {
				Name:        "tag",
				Synopsis:    "workbook status tag <status> ([--tag <tag>]... | --clear-tags) [--no-sync] [--no-docs] [--json]",
				Description: "Replace a status's roles with the ones given.\n\nRepeated --tag values are the whole new set, so a tag left out is taken away.\nGiving a status the default tag takes it from whichever status held it, in one\noperation, because a project is never without a default.",
				Positionals: []string{"<status>"},
				Options: []optionMetadata{
					{Name: "tag", Kind: stringFlag, Value: "<tag>", Description: "role to give it: default, next, or done (repeatable)"},
					{Name: "clear-tags", Kind: boolFlag, Description: "replace its roles with an empty set"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"untag": {
				Name:        "untag",
				Synopsis:    "workbook status untag <status> <tag> [--no-sync] [--no-docs] [--json]",
				Description: "Take one role away from a status.",
				Positionals: []string{"<status>", "<tag>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"delete": {
				Name:        "delete",
				Synopsis:    "workbook status delete <status> --into <status> [--no-sync] [--no-docs] [--json]",
				Description: "Remove a status and forward its tasks.\n\n--into is required and never guessed: every task in the removed status reads\nas being in the status it names, on every clone, including the ones that have\nnot fetched the removal yet.",
				Positionals: []string{"<status>"},
				Options: []optionMetadata{
					{Name: "into", Kind: stringFlag, Value: "<status>", Description: "where the removed status's tasks belong"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"log": {
				Name:        "log",
				Synopsis:    "workbook status log [--limit <n>] [--all] [--json]",
				Description: "List the recorded status changes, oldest first, with the command that\nreverses each one.",
				Options: []optionMetadata{
					{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "show this many recent changes (default 10)"},
					{Name: "all", Kind: boolFlag, Description: "show every change"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
		},
	},
	"priority": {
		Name:     "priority",
		Synopsis: "workbook priority <command> [options]",
		Description: "Inspect and change this project's task priorities.\n\n" +
			"Priorities are project configuration, recorded in the same synchronized\n" +
			"history as the statuses and shared with everyone who fetches. A priority has\n" +
			"a machine value typed as `--priority high`, a display label, a position from\n" +
			"most urgent to least, an optional color the board draws it in, and one tag:\n" +
			"`default` for where a task with no priority named lands.\n\n" +
			"Renaming or removing a priority never rewrites a task. Stored values keep\n" +
			"resolving through the change, and each task settles the next time something\n" +
			"writes to it.\n\n" +
			"Every change regenerates `.workbook/guidelines.md`, which documents these\n" +
			"priorities for agents; `--no-docs` leaves it alone, and a file somebody edited\n" +
			"is reported rather than overwritten.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"list", "add", "rename", "label", "move", "tag", "delete", "color", "log"},
		Subcommands: map[string]commandMetadata{
			"list": {
				Name:        "list",
				Synopsis:    "workbook priority list [--json]",
				Description: "List this project's priorities, with the tasks in each.",
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
			"add": {
				Name:        "add",
				Synopsis:    "workbook priority add <priority> [--label <label>] [--before <priority> | --after <priority>] [--no-sync] [--no-docs] [--json]",
				Description: "Define a priority.\n\nIt is added last — least urgent — unless --before or --after names where it\ngoes, and its label is derived from its name unless --label gives one.",
				Positionals: []string{"<priority>"},
				Options: []optionMetadata{
					{Name: "label", Kind: stringFlag, Value: "<label>", Description: "display label (default: derived from the priority)"},
					{Name: "before", Kind: stringFlag, Value: "<priority>", Description: "place before this priority"},
					{Name: "after", Kind: stringFlag, Value: "<priority>", Description: "place after this priority"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"rename": {
				Name:        "rename",
				Synopsis:    "workbook priority rename <priority> <new-priority> [--label <label>] [--no-sync] [--no-docs] [--json]",
				Description: "Give a priority a new machine value.\n\nTasks stored under the old value keep resolving to this priority. The display\nlabel follows the new name when it was the one the old name implied, and is\nkept when somebody chose it.",
				Positionals: []string{"<priority>", "<new-priority>"},
				Options: []optionMetadata{
					{Name: "label", Kind: stringFlag, Value: "<label>", Description: "display label to set with the rename"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"label": {
				Name:        "label",
				Synopsis:    "workbook priority label <priority> <display-label> [--no-sync] [--no-docs] [--json]",
				Description: "Change a priority's display label. Nothing else changes.",
				Positionals: []string{"<priority>", "<display-label>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"move": {
				Name:        "move",
				Synopsis:    "workbook priority move <priority> (--before <priority> | --after <priority>) [--no-sync] [--no-docs] [--json]",
				Description: "Move a priority among its peers, most urgent first.",
				Positionals: []string{"<priority>"},
				Options: []optionMetadata{
					{Name: "before", Kind: stringFlag, Value: "<priority>", Description: "move before this priority"},
					{Name: "after", Kind: stringFlag, Value: "<priority>", Description: "move after this priority"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"tag": {
				Name:        "tag",
				Synopsis:    "workbook priority tag <priority> --tag <tag> [--no-sync] [--no-docs] [--json]",
				Description: "Give a priority a role.\n\nThere is one role: `default`, where a task lands when nobody names a priority.\nGiving it takes it from whichever priority held it, in one operation, because a\nproject is never without a default.",
				Positionals: []string{"<priority>"},
				Options: []optionMetadata{
					{Name: "tag", Kind: stringFlag, Value: "<tag>", Description: "role to give it: default"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"delete": {
				Name:        "delete",
				Synopsis:    "workbook priority delete <priority> --into <priority> [--no-sync] [--no-docs] [--json]",
				Description: "Remove a priority and forward its tasks.\n\n--into is required and never guessed: every task at the removed priority reads\nas being at the one it names, on every clone, including the ones that have not\nfetched the removal yet.",
				Positionals: []string{"<priority>"},
				Options: []optionMetadata{
					{Name: "into", Kind: stringFlag, Value: "<priority>", Description: "where the removed priority's tasks belong"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"color": {
				Name:        "color",
				Synopsis:    "workbook priority color <priority> [<color>] [--no-sync] [--no-docs] [--json]",
				Description: "Choose the color the board draws a priority in.\n\nA color is six hexadecimal digits behind a hash, as in `#b42318`. Giving no\ncolor clears the stored one and returns the priority to the color its position\nimplies; nothing stores a default.",
				Positionals: []string{"<priority>", "<color>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"log": {
				Name:        "log",
				Synopsis:    "workbook priority log [--limit <n>] [--all] [--json]",
				Description: "List the recorded priority changes, oldest first, with the command that\nreverses each one.",
				Options: []optionMetadata{
					{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "show this many recent changes (default 10)"},
					{Name: "all", Kind: boolFlag, Description: "show every change"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
		},
	},
	"key": {
		Name:     "key",
		Synopsis: "workbook key <command> [options]",
		Description: "Inspect and change this project's task-ID keys.\n\n" +
			"A key is the prefix on every task ID, as in `WB-01K0M6…`. A project starts\n" +
			"with the key `workbook setup` gave it and may add more: one key is current,\n" +
			"which is where a new task is minted, others may be active, and a key that has\n" +
			"stopped minting is retired. A retired key is never deleted — a task ID is a\n" +
			"permanent name, so every key this project has ever minted under stays valid\n" +
			"and its tasks stay this project's.\n\n" +
			"Keys are project configuration, recorded in the same synchronized history as\n" +
			"the statuses and shared with everyone who fetches. Every change prints the\n" +
			"command that reverses it.\n\n" +
			"Every change regenerates `.workbook/guidelines.md`, which documents the key a\n" +
			"new task is minted under for agents; `--no-docs` leaves it alone, and a file\n" +
			"somebody edited is reported rather than overwritten.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"list", "add", "current", "retire", "log"},
		Subcommands: map[string]commandMetadata{
			"list": {
				Name:     "list",
				Synopsis: "workbook key list [--no-counts] [--json]",
				Description: "List this project's keys, in the order they were added, with the tasks under each.\n\n" +
					"--no-counts leaves the task counts out, which spares reading every task when\n" +
					"only the keys and the current one are wanted.",
				Options: []optionMetadata{
					{Name: "no-counts", Kind: boolFlag, Description: "leave out the tasks under each key"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"add": {
				Name:     "add",
				Synopsis: "workbook key add <key> [--current] [--no-sync] [--no-docs] [--json]",
				Description: "Add a key new tasks may be minted under.\n\n" +
					"The key is added last and is not current unless --current says so. Adding a\n" +
					"retired key back makes it active again in the place it already had.",
				Positionals: []string{"<key>"},
				Options: []optionMetadata{
					{Name: "current", Kind: boolFlag, Description: "also make it the key new tasks are minted under"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"current": {
				Name:        "current",
				Synopsis:    "workbook key current <key> [--no-sync] [--no-docs] [--json]",
				Description: "Mint new tasks under this key.\n\nThe key that held it gives it up in the same change; exactly one key is current.",
				Positionals: []string{"<key>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"retire": {
				Name:     "retire",
				Synopsis: "workbook key retire <key> [--no-sync] [--no-docs] [--json]",
				Description: "Stop minting new tasks under a key.\n\n" +
					"Its tasks are untouched and stay this project's. The current key and the last\n" +
					"active key cannot be retired.",
				Positionals: []string{"<key>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"log": {
				Name:        "log",
				Synopsis:    "workbook key log [--limit <n>] [--all] [--json]",
				Description: "List the recorded changes to this project's keys, oldest first, with the\ncommand that reverses each one.",
				Options: []optionMetadata{
					{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "show this many recent changes (default 10)"},
					{Name: "all", Kind: boolFlag, Description: "show every change"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
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
		Name:     "create",
		Synopsis: "workbook create <title> [options]",
		Description: "Create a task.\n\n" +
			"--due takes a date as YYYY-MM-DD, or today, tomorrow, or +Nd for N days\n" +
			"from today on this machine. A relative date is resolved when the command\n" +
			"runs, the task records the date it resolved to, and the output names it.",
		Positionals: []string{"<title>"},
		Options: []optionMetadata{
			{Name: "description", Kind: stringFlag, Value: "<text>", Description: "task description"},
			{Name: "status", Kind: stringFlag, Value: "<status>", Description: "task status"},
			{Name: "priority", Kind: stringFlag, Value: "<priority>", Description: "task priority"},
			{Name: "due", Kind: stringFlag, Value: "<date>", Description: "due date: YYYY-MM-DD, today, tomorrow, or +Nd"},
			{Name: "key", Kind: stringFlag, Value: "<key>", Description: "mint the task under this key (default: the current key)"},
			{Name: "label", Kind: stringFlag, Value: "<label>", Description: "task label"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"list": {
		Name:     "list",
		Synopsis: "workbook list [options]",
		Description: "List tasks.\n\n" +
			"A task is overdue when its due date is before today on this machine, and due\n" +
			"soon when it is due today or within the next 3 days. A task in a status\n" +
			"tagged done, or deleted, is neither. --due-before takes the forms create's\n" +
			"--due does and keeps tasks due strictly before that date. --sort due lists\n" +
			"the earliest due date first and tasks with no due date last.",
		Options: []optionMetadata{
			{Name: "status", Kind: stringFlag, Value: "<status>", Description: "task status"},
			{Name: "priority", Kind: stringFlag, Value: "<priority>", Description: "task priority"},
			{Name: "key", Kind: stringFlag, Value: "<key>", Description: "only tasks whose ID carries this key"},
			{Name: "label", Kind: stringFlag, Value: "<label>", Description: "task label"},
			{Name: "find", Kind: stringFlag, Value: "<text>", Description: "only tasks whose title or description contains every word, or whose ID one starts"},
			{Name: "all", Kind: boolFlag, Description: "include tombstoned tasks"},
			{Name: "overdue", Kind: boolFlag, Description: "only tasks past their due date and not done"},
			{Name: "due-before", Kind: stringFlag, Value: "<date>", Description: "only tasks due before this date"},
			{Name: "sort", Kind: stringFlag, Value: "<order>", Description: "order the list: due"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"show": {
		Name:     "show",
		Synopsis: "workbook show <id-or-prefix> [--history [--limit <n>] [--all]] [--compare <commit> <commit>] [--get-attachment <id-or-prefix> [--out <path>]] [--json]",
		Description: "Show a task, with its comment thread and its attachments.\n\n" +
			"With --history, list how the task reached its current state, ordered by the\n" +
			"commit chain rather than by wall time. With --compare, diff two points in that\n" +
			"history in the order given. Both name entries by full Git commit object ID.\n\n" +
			"With --get-attachment, write one attached file's bytes instead of the task:\n" +
			"to standard output, or to the file --out names. A link attachment holds no\n" +
			"bytes and is refused, reported with the URL it points at. A file whose bytes\n" +
			"compaction removed is refused as not found, naming the day the task was\n" +
			"compacted. Because the output is bytes rather than a rendered task, no other\n" +
			"show option may be given beside it.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "history", Kind: boolFlag, Description: "list this task's changes"},
			{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "show this many recent changes (default 10)"},
			{Name: "all", Kind: boolFlag, Description: "show every change"},
			{Name: "compare", Kind: pairFlag, Value: "<commit> <commit>", Description: "diff two commits from this task's history"},
			{Name: "get-attachment", Kind: stringFlag, Value: "<id-or-prefix>", Description: "write this attachment's bytes"},
			{Name: "out", Kind: stringFlag, Value: "<path>", Description: "write the attachment to this file instead of standard output"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"update": {
		Name:     "update",
		Synopsis: "workbook update <id-or-prefix> [options]",
		Description: "Update a task.\n\n" +
			"Comments, attachments and assignments are things a task holds, so they ride\n" +
			"this verb: every flag given is one intent, and one invocation is one pack,\n" +
			"one commit, and one change-log entry. `update <id> --status done --comment\n" +
			"\"shipped\"` is a single change, and so is `--status in-progress --assign\n" +
			"self`.\n\n" +
			"--comment adds a comment on its own, and carries the new body when\n" +
			"--edit-comment names one to rewrite — a flag takes one value, so the body\n" +
			"travels in --comment either way. --edit-comment without --comment is\n" +
			"therefore an error rather than a guess, as is --attach-label without the\n" +
			"--attach-url it labels.\n\n" +
			"A comment or attachment ID may be given as any unambiguous prefix of the one\n" +
			"`workbook show` prints. An attached file is refused before it is read when it\n" +
			"is larger than a task may hold; attach a link instead.\n\n" +
			"--assign and --unassign name `self`, an email address, or either followed\n" +
			"by /label for one agent of that identity. One assignment per invocation,\n" +
			"and not both in the same one. An assignment is additive and is removable\n" +
			"only by the person it names or the person who recorded it.\n\n" +
			"--assign exits 10 and records nothing when somebody else holds the task\n" +
			"and you do not; --force records yours beside theirs instead. Assigning\n" +
			"yourself something your identity already holds changes nothing and says so.\n\n" +
			"--due takes the forms create's does and replaces the due date; --clear-due\n" +
			"removes it.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "title", Kind: stringFlag, Value: "<title>", Description: "task title"},
			{Name: "description", Kind: stringFlag, Value: "<text>", Description: "task description"},
			{Name: "status", Kind: stringFlag, Value: "<status>", Description: "task status"},
			{Name: "priority", Kind: stringFlag, Value: "<priority>", Description: "task priority"},
			{Name: "label", Kind: stringFlag, Value: "<label>", Description: "replacement task label"},
			{Name: "clear-labels", Kind: boolFlag, Description: "replace labels with an empty set"},
			{Name: "due", Kind: stringFlag, Value: "<date>", Description: "due date: YYYY-MM-DD, today, tomorrow, or +Nd"},
			{Name: "clear-due", Kind: boolFlag, Description: "remove the due date"},
			{Name: "comment", Kind: stringFlag, Value: "<body>", Description: "comment to add, or the new body with --edit-comment"},
			{Name: "edit-comment", Kind: stringFlag, Value: "<id-or-prefix>", Description: "replace this comment's body with --comment"},
			{Name: "remove-comment", Kind: stringFlag, Value: "<id-or-prefix>", Description: "remove this comment"},
			{Name: "attach-file", Kind: stringFlag, Value: "<path>", Description: "attach this file's contents"},
			{Name: "attach-url", Kind: stringFlag, Value: "<url>", Description: "attach this http or https link"},
			{Name: "attach-label", Kind: stringFlag, Value: "<text>", Description: "display text for --attach-url"},
			{Name: "remove-attachment", Kind: stringFlag, Value: "<id-or-prefix>", Description: "remove this attachment"},
			{Name: "assign", Kind: stringFlag, Value: "<who>", Description: "assign to self, an email, or either with /label"},
			{Name: "unassign", Kind: stringFlag, Value: "<who>", Description: "withdraw that assignment"},
			{Name: "force", Kind: boolFlag, Description: "assign beside an assignment somebody else holds"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"delete": {
		Name:        "delete",
		Synopsis:    "workbook delete <id-or-prefix> [--json]",
		Description: "Delete a task.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"restore": {
		Name:        "restore",
		Synopsis:    "workbook restore <id-or-prefix> [--into <status>] [--json]",
		Description: "Restore a tombstoned task.\n\nWith --into, the task comes back in the status named rather than the one it was\ndeleted from, recorded as one change. It keeps the position it was deleted with,\nwhich is where `workbook update --status` would leave it too.",
		Positionals: []string{"<id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "into", Kind: stringFlag, Value: "<status>", Description: "restore into this status"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"serve": {
		Name:        "serve",
		Synopsis:    "workbook serve [--addr <address>] [--public-url <url>] [--actor-header <header>]",
		Description: "Serve the local web board.\n\nThe default address is a preference, not a requirement: when 127.0.0.1:7331 is\nalready taken, by another project's board on the same machine for instance,\nserve says so, binds a free port instead, and prints the address it chose. An\naddress given with --addr is a contract and fails rather than moving.\n\nA name given with --addr binds the address it resolves to, and the board pins\nits Host header to that address rather than to the name. Open the board at the\naddress serve prints; a request naming the host instead is refused.\n\nBehind a sign-in proxy, --public-url names the https address people open the\nboard at, and --actor-header names the request header the proxy puts each\nsigned-in person's email address in. With the header on, a request that\ncarries no name is refused, and the board must be reachable only through the\nproxy. WORKBOOK_SERVE_PUBLIC_URL and WORKBOOK_SERVE_ACTOR_HEADER set the same\ntwo options; a flag wins over its variable. See docs/hosting.md.",
		Options: []optionMetadata{
			{Name: "addr", Kind: stringFlag, Value: "<address>", Description: "listener address (default 127.0.0.1:7331, or a free port when that one is taken)"},
			{Name: "public-url", Kind: stringFlag, Value: "<url>", Description: "the https address people open the board at, behind a sign-in proxy"},
			{Name: "actor-header", Kind: stringFlag, Value: "<header>", Description: "the request header the sign-in proxy names each person in"},
		},
	},
	"fetch": {
		Name:        "fetch",
		Synopsis:    "workbook fetch [--remote <remote>] [--json]",
		Description: "Fetch shared task refs from the project's tasks remote, which is origin unless\nthe project's settings name another. --remote names a different Git remote\nfor this run, which is how a checkout reads one it has not set up.",
		Options: []optionMetadata{
			{Name: "remote", Kind: stringFlag, Value: "<remote>", Description: "fetch from this Git remote instead of the project's tasks remote"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"push": {
		Name:        "push",
		Synopsis:    "workbook push [--remote <remote>] [--json]",
		Description: "Push local task refs to the project's tasks remote, which is origin unless the\nproject's settings name another. --remote names a different Git remote for\nthis run; a remote marked as the place the tasks moved away from is refused.",
		Options: []optionMetadata{
			{Name: "remote", Kind: stringFlag, Value: "<remote>", Description: "push to this Git remote instead of the project's tasks remote"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"sync": {
		Name:        "sync",
		Synopsis:    "workbook sync [--watch [--interval <duration>]] [--status] [--remote <remote>] [--json]",
		Description: "Fetch then push shared task refs, once or continuously, with the project's\ntasks remote, which is origin unless the project's settings name another.\n--remote names a different Git remote for one run; a watcher always uses the\nproject's tasks remote.",
		Options: []optionMetadata{
			{Name: "watch", Kind: boolFlag, Description: "synchronize continuously until interrupted"},
			{Name: "interval", Kind: stringFlag, Value: "<duration>", Description: "time between synchronizations while watching, such as 30s, 2m or 1d (default 5s)"},
			{Name: "status", Kind: boolFlag, Description: "report whether a watcher is running for this repository"},
			{Name: "remote", Kind: stringFlag, Value: "<remote>", Description: "synchronize once with this Git remote instead of the project's tasks remote"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"move-tasks": {
		Name:     "move-tasks",
		Synopsis: "workbook move-tasks [--from <remote>] [--allow-branches] [--clean-up] [--no-backup | --backup <path>] [--json]",
		Description: "Move this project's tasks to the tasks remote its settings name.\n\n" +
			"Run it after `workbook config set tasks-remote <name> --url <address>`, or after\n" +
			"`workbook config unset tasks-remote` to move the tasks back to origin. It copies\n" +
			"every task from the old place to the new one, marks the old place so a\n" +
			"checkout still using it is refused and told where the tasks are, copies again\n" +
			"for edits published before the mark, and checks that the new place holds every\n" +
			"operation the old place does. Every step is safe to repeat: an interrupted run\n" +
			"is finished by running it again, and a later run carries any edit that reached\n" +
			"the old place after the move, applying each operation once. Then commit and\n" +
			"push .workbook/config.json; everyone else runs `workbook setup` after pulling it.\n" +
			"Committing and pushing the settings before running this is safe too: a\n" +
			"checkout that pulls them before the move ran is refused and publishes nothing\n" +
			"until it has; its unpublished edits are kept. It is told to run\n" +
			"`workbook move-tasks` when no move ever took the tasks to the place the settings\n" +
			"name, and otherwise to pull and run setup, since its settings may be stale,\n" +
			"with a sentence saying that whoever changed tasks-remote runs move-tasks first.\n\n" +
			"The new place can be one the tasks left earlier, origin included: the copies it\n" +
			"still holds are brought up to date, and it records that the tasks are back. A\n" +
			"copy there of a task compacted since the tasks left is replaced with its\n" +
			"checkpoint, after a backup bundle of the copy is written, as compact writes one;\n" +
			"--no-backup and --backup <path> work as they do for compact.\n\n" +
			"With --clean-up, once the move is complete and verified, it does to the place\n" +
			"the tasks left what `workbook reset --remote` does there, for this project's\n" +
			"refs only: it writes a bundle of them, unless --no-backup, and deletes them,\n" +
			"keeping the moved marker. A task or relic ref there that is not this project's,\n" +
			"another project's or a name that fits no project, is left there, unbundled.\n" +
			"A move that stops early, or sets an edit aside, cleans\n" +
			"nothing; run `workbook move-tasks --clean-up` again afterwards, which also\n" +
			"finishes a clean-up a host stopped by refusing deletions.\n\n" +
			"It refuses a new place that is the same repository as the old one, however its\n" +
			"address is spelled, and one that holds branches unless --allow-branches is given\n" +
			"or it is the remote named origin and holds this checkout's code; a moved\n" +
			"marker at the new place never counts, since a mirror carries it too.\n" +
			"Never give it for a mirror, a repository something updates from the code\n" +
			"repository: that update would overwrite or delete the tasks. The old place is\n" +
			"the one that holds the tasks now, found by following the moved markers.",
		Options: []optionMetadata{
			{Name: "from", Kind: stringFlag, Value: "<remote>", Description: "the Git remote the tasks are in now (default: the place the moved markers lead to)"},
			{Name: "allow-branches", Kind: boolFlag, Description: "accept a tasks remote that holds branches; never a mirror of the code repository"},
			{Name: "clean-up", Kind: boolFlag, Description: "after a verified move, delete this project's Workbook refs at the place left, all but its moved marker"},
			{Name: "no-backup", Kind: boolFlag, Description: "write no backup bundle of the copies a move back replaces or a clean-up deletes"},
			{Name: "backup", Kind: stringFlag, Value: "<path>", Description: "write the backup bundle here, or into this directory"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"reset": {
		Name: "reset",
		Synopsis: "workbook reset [--dry-run] [--no-backup [--discard-unpublished] | --backup <path>] [--json]\n" +
			"workbook reset --remote <remote> [--confirm <remote>] [--dry-run] [--no-backup | --backup <path>] [--json]",
		Description: "Wipe this checkout's Workbook data, or one remote's, to start again from nothing.\n\n" +
			"With no --remote it changes nothing shared. It stops this checkout's watcher,\n" +
			"deletes every local ref under refs/workbook/ (task, tracking, parked and\n" +
			"set-aside refs among them), the caches under <git-common-dir>/workbook, and\n" +
			"the temporary files Workbook's own writes leave under .workbook/. Every other\n" +
			"file is left alone: tracked files, .workbook/config.json and guidelines.md, and\n" +
			"untracked files Workbook did not write, which it counts. A dry run lists the\n" +
			"same refs and files. `workbook setup` then `workbook sync` rebuild everything\n" +
			"from the tasks remote the settings name. First it writes a Git bundle of every\n" +
			"ref it deletes, as compact does, and prints where it is and the git fetch that\n" +
			"restores it; a --backup inside .workbook/ or <git-common-dir>/workbook is\n" +
			"refused. Work the tasks remote does not hold is listed; with --no-backup such\n" +
			"work is refused unless --discard-unpublished is given too, and --no-backup is\n" +
			"refused outright when the tasks remote cannot be checked.\n\n" +
			"With --remote it deletes that remote's refs under refs/workbook/ instead: the\n" +
			"only Workbook command that deletes shared refs. At a place the tasks moved away\n" +
			"from it keeps refs/workbook/project, which carries the moved marker, so a\n" +
			"checkout still on old settings is still refused and told where the tasks went,\n" +
			"and deletes every other Workbook ref there, another project's task and relic\n" +
			"refs included, without asking; `workbook move-tasks --clean-up` deletes only\n" +
			"this project's.\n" +
			"A remote that holds the project's tasks is wiped only when --confirm names it\n" +
			"again, and always after the bundle is written. A checkout that still holds the\n" +
			"tasks publishes them back to a wiped remote on its next sync.\n\n" +
			"Nothing outside refs/workbook/ is touched: no code, branch or tag. Every step\n" +
			"is safe to interrupt; running the command again finishes the job.",
		Options: []optionMetadata{
			{Name: "remote", Kind: stringFlag, Value: "<remote>", Description: "wipe this Git remote's Workbook refs instead of this checkout's"},
			{Name: "confirm", Kind: stringFlag, Value: "<remote>", Description: "with --remote, the remote's name again, to wipe the place the tasks live"},
			{Name: "dry-run", Kind: boolFlag, Description: "report what would be removed and change nothing"},
			{Name: "no-backup", Kind: boolFlag, Description: "write no backup bundle"},
			{Name: "backup", Kind: stringFlag, Value: "<path>", Description: "write the backup bundle here, or into this directory"},
			{Name: "discard-unpublished", Kind: boolFlag, Description: "with --no-backup, remove work that exists only in this checkout"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"relic": {
		Name:     "relic",
		Synopsis: "workbook relic <command> [options]",
		Description: "Create, read and change relics: records that hold specs, plans, decisions and similar material as named parts.\n\n" +
			"A relic ID looks like " + helpProjectKey + "-R-01M9… and a part is addressed as <relic>#<part>. Any\n" +
			"unambiguous prefix of either works. Relics are shared through the tasks remote\n" +
			"(origin unless configured) as tasks are: a command that changes a relic fetches\n" +
			"first and publishes the relic after, unless --no-sync is given or auto-sync is off.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"create", "list", "show", "diff", "update", "migrate", "delete", "restore", "export"},
		Subcommands: map[string]commandMetadata{
			"create": {
				Name:     "create",
				Synopsis: "workbook relic create [<title>] [--type <type>] [--from-file <file>] [--no-sync] [--json]",
				Description: "Create a relic.\n\n" +
					"Without --from-file the title and --type are both required and the relic has\n" +
					"no parts. With it, the relic is created from a whole workbook.relic document,\n" +
					"as relic export writes one, with new IDs; a title or --type given here\n" +
					"replaces the document's.",
				Positionals: []string{"[<title>]"},
				Options: []optionMetadata{
					{Name: "type", Kind: stringFlag, Value: "<type>", Description: "relic type, such as spec or plan"},
					{Name: "from-file", Kind: stringFlag, Value: "<file>", Description: "create from a whole relic in one JSON document (- reads standard input)"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"list": {
				Name:     "list",
				Synopsis: "workbook relic list [--type <type>] [--all] [--linked-to <task|relic[#part]>] [--behind] [--json]",
				Description: "List relics in ID order, which is the order they were created in under one\n" +
					"project key. Deleted relics are left out unless --all is given.\n\n" +
					"--linked-to lists only the relics that link to a task, a relic or a part of one,\n" +
					"each with its links to it. A link to a part counts as a link to its relic.\n\n" +
					"--behind lists only the relics behind their type's newest schema version: those\n" +
					"naming an older version, or none, of a type that has one.",
				Options: []optionMetadata{
					{Name: "type", Kind: stringFlag, Value: "<type>", Description: "show only relics of this type"},
					{Name: "all", Kind: boolFlag, Description: "include deleted relics, each marked deleted"},
					{Name: "linked-to", Kind: stringFlag, Value: "<task|relic[#part]>", Description: "show only relics that link to this task, relic or part"},
					{Name: "behind", Kind: boolFlag, Description: "show only relics behind their type's newest schema version"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"show": {
				Name:     "show",
				Synopsis: "workbook relic show <relic>[#<part>] [--body] [--removed] [--history [--limit <n> | --all]] [--json]",
				Description: "Show a relic, or one of its parts.\n\n" +
					"Bodies are left out unless --body is given. With <relic>#<part> and --body,\n" +
					"and without --json, only that part's body is written: byte for byte into a file\n" +
					"or a pipe, and with control characters replaced on a terminal.\n\n" +
					"--history lists the recorded changes instead, oldest first, the last ten unless\n" +
					"--limit or --all is given: each change's time, author and operations, and for\n" +
					"<relic>#<part> only the changes to that part, with every version of its body. A\n" +
					"save from an out-of-date version is listed and marked as not applied.",
				Positionals: []string{"<relic>[#<part>]"},
				Options: []optionMetadata{
					{Name: "body", Kind: boolFlag, Description: "include body text"},
					{Name: "removed", Kind: boolFlag, Description: "include removed parts"},
					{Name: "history", Kind: boolFlag, Description: "list this relic's changes, or one part's"},
					{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "show this many recent changes"},
					{Name: "all", Kind: boolFlag, Description: "show every change"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"diff": {
				Name:     "diff",
				Synopsis: "workbook relic diff <relic>[#<part>] [<version> [<version>]] [--context <n>|all] [--json]",
				Description: "Compare two versions of a relic, or of one part, line by line and field by field.\n\n" +
					"A version is the ID of a change, as relic show --history lists it, or an\n" +
					"unambiguous prefix of one; a Git commit ID is not a version. With two versions\n" +
					"the first is compared with the second. With one, it is compared with the current\n" +
					"relic. With none, the latest change is shown, for a part the latest change to it.\n" +
					"The address and the versions come before the options.",
				Positionals: []string{"<relic>[#<part>]", "[<version>]", "[<version>]"},
				Options: []optionMetadata{
					{Name: "context", Kind: stringFlag, Value: "<n>|all", Description: "lines of context around each change (3 unless given), or all"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"update": {
				Name: "update",
				// One form per kind of change. They combine freely in one command;
				// a merged line would not show which part options follow which
				// group option.
				Synopsis: "workbook relic update <relic> [--title <title>] [--set <name>=<value>]... [--set-json <name>=<json>]... [--clear <name>]... [--no-sync] [--json]\n" +
					"workbook relic update <relic> --add-part --part-type <type> [--field <name>=<value>]... [--field-json <name>=<json>]... [--body-file <file> [--body-format <label>]] [--before <part> | --after <part>] [--no-sync]\n" +
					"workbook relic update <relic> --part <part> [--field <name>=<value>]... [--field-json <name>=<json>]... [--clear-field <name>]... [--body-file <file> [--body-format <label>] [--base <version>]] [--no-sync]\n" +
					"workbook relic update <relic> --move-part <part> (--before <part> | --after <part>) [--no-sync]\n" +
					"workbook relic update <relic> [--remove-part <part>]... [--restore-part <part>]... [--no-sync]\n" +
					"workbook relic update <relic> [--link <task|relic[#part]> [--from <part>] --rel <rel> [--pointer <json object>]]... [--unlink <link>]... [--no-sync]",
				Description: "Change a relic. Every change given in one command is recorded together, or none is.\n\n" +
					"--add-part, --part, --move-part, --remove-part and --restore-part each start a group,\n" +
					"and the part options after one belong to it. Changes apply in the order given.\n" +
					"--title, --set, --set-json and --clear always act on the relic, wherever they are typed.\n" +
					"After --part, --clear still removes a property of the relic; --clear-field removes a field of the part.\n\n" +
					"Saving a body over an existing one needs --base <version>, the version shown by\n" +
					"\"workbook relic show <relic>#<part>\". A save from an older version is refused: read\n" +
					"the part again and save from the version it is at now.\n\n" +
					"A part's first body takes its format label from the file's ending. A save over a body\n" +
					"keeps the label the body has. --body-format sets the label in either case.\n\n" +
					"--set and --field store text. --set-json and --field-json store any JSON value.\n" +
					"One command names a property once, and a field of a part once; naming one twice is refused.\n\n" +
					"--link starts a group too: it links the relic, or with --from one of its parts, to a task,\n" +
					"a relic or a part of one, named by --rel, such as implemented-by. --pointer adds a JSON\n" +
					"object saying where in the target the link points, such as a quote or a time.\n" +
					"Linking what is already linked changes nothing.\n" +
					"--unlink removes one link, named by the ID relic show lists.",
				Positionals: []string{"<relic>"},
				Options: []optionMetadata{
					{Name: "title", Kind: stringFlag, Value: "<title>", Description: "set the relic's title"},
					{Name: "set", Kind: stringFlag, Value: "<name>=<value>", Description: "set a property to text"},
					{Name: "set-json", Kind: stringFlag, Value: "<name>=<json>", Description: "set a property to a JSON value"},
					{Name: "clear", Kind: stringFlag, Value: "<name>", Description: "remove a property"},
					{Name: "add-part", Kind: boolFlag, Description: "add a part, described by the part options after it"},
					{Name: "part-type", Kind: stringFlag, Value: "<type>", Description: "the new part's type, such as section or decision"},
					{Name: "field", Kind: stringFlag, Value: "<name>=<value>", Description: "set a field of the part to text"},
					{Name: "field-json", Kind: stringFlag, Value: "<name>=<json>", Description: "set a field of the part to a JSON value"},
					{Name: "clear-field", Kind: stringFlag, Value: "<name>", Description: "remove a field of the part"},
					{Name: "body-file", Kind: stringFlag, Value: "<file>", Description: "save this file's contents as the part's body"},
					{Name: "body-format", Kind: stringFlag, Value: "<label>", Description: "body format, such as md, instead of the file's ending or the label the body has; empty for none"},
					{Name: "base", Kind: stringFlag, Value: "<version>", Description: "the body version this save starts from"},
					{Name: "before", Kind: stringFlag, Value: "<part>", Description: "put the part before this one"},
					{Name: "after", Kind: stringFlag, Value: "<part>", Description: "put the part after this one"},
					{Name: "part", Kind: stringFlag, Value: "<part>", Description: "change this part with the part options after it"},
					{Name: "move-part", Kind: stringFlag, Value: "<part>", Description: "move this part to where --before or --after says"},
					{Name: "remove-part", Kind: stringFlag, Value: "<part>", Description: "remove this part; --restore-part brings it back"},
					{Name: "restore-part", Kind: stringFlag, Value: "<part>", Description: "restore this removed part"},
					{Name: "link", Kind: stringFlag, Value: "<task|relic[#part]>", Description: "link to this task, relic or part, with --rel and, from one part, --from"},
					{Name: "from", Kind: stringFlag, Value: "<part>", Description: "make the link from this part instead of the whole relic"},
					{Name: "rel", Kind: stringFlag, Value: "<rel>", Description: "the link's relationship, such as implemented-by"},
					{Name: "pointer", Kind: stringFlag, Value: "<json object>", Description: "where in the target the link points, such as a quote or a time"},
					{Name: "unlink", Kind: stringFlag, Value: "<link>", Description: "remove this link"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"migrate": {
				Name:     "migrate",
				Synopsis: "workbook relic migrate <relic> --to <target> [--no-sync] [--json]",
				Description: "Move a relic to another schema version.\n\n" +
					"--to takes N, for version N of the relic's type; <type>, for that type's newest\n" +
					"version, or an open relic when the type has no schema; or <type>@N. An older\n" +
					"version may be the target. The whole relic, its removed parts aside, must fit the\n" +
					"version it moves to. Moving to the version it already names changes nothing.",
				Positionals: []string{"<relic>"},
				Options: []optionMetadata{
					{Name: "to", Kind: stringFlag, Value: "<target>", Description: "the version to move to: N, <type> or <type>@N"},
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"delete": {
				Name:        "delete",
				Synopsis:    "workbook relic delete <relic> [--no-sync] [--json]",
				Description: "Delete a relic. Its history is kept, and relic restore brings it back.",
				Positionals: []string{"<relic>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"restore": {
				Name:        "restore",
				Synopsis:    "workbook relic restore <relic> [--no-sync] [--json]",
				Description: "Restore a deleted relic.",
				Positionals: []string{"<relic>"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"export": {
				Name:     "export",
				Synopsis: "workbook relic export <relic> [--json]",
				Description: "Write a relic, with its live parts and their bodies, as one workbook.relic\n" +
					"document that relic create --from-file reads.",
				Positionals: []string{"<relic>"},
				Options: []optionMetadata{
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
		},
	},
	"schema": {
		Name:     "schema",
		Synopsis: "workbook schema <command> [options]",
		Description: "Read and save the project's relic schemas, which say what each relic type holds.\n\n" +
			"Schemas are project settings, recorded in their own synchronized history and\n" +
			"shared with everyone who fetches. Saving one adds a version to its type, and\n" +
			"every version is kept: a relic names the version it was written under and is\n" +
			"checked against it when it is saved, until relic migrate moves it to another.\n" +
			"A type with no schema accepts anything. Reading or synchronizing a relic never\n" +
			"refuses it for not fitting its schema.",
		Positionals:     []string{"<command>"},
		SubcommandOrder: []string{"list", "show", "put"},
		Subcommands: map[string]commandMetadata{
			"list": {
				Name:        "list",
				Synopsis:    "workbook schema list [--json]",
				Description: "List the relic types that have a schema, with each one's newest version.",
				Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
			},
			"show": {
				Name:        "show",
				Synopsis:    "workbook schema show <type> [--version <n>] [--json]",
				Description: "Show a relic type's schema: its newest version, or the one --version names.",
				Positionals: []string{"<type>"},
				Options: []optionMetadata{
					{Name: "version", Kind: stringFlag, Value: "<n>", Description: "show this version instead of the newest"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
			"put": {
				Name:     "put",
				Synopsis: "workbook schema put <type> <file|-> [--no-sync] [--no-docs] [--json]",
				Description: "Save the schema in a file (- reads standard input) as a relic type's next\n" +
					"version.\n\n" +
					"A reference to another type's definition is fixed to that type's newest version.\n" +
					"Saving the schema the newest version already holds changes nothing. Where two\n" +
					"people save a version for one type at once, both are kept, and the one that\n" +
					"synchronizes second takes the next number.",
				Positionals: []string{"<type>", "<file|->"},
				Options: []optionMetadata{
					{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing refs with the tasks remote (origin unless configured)"},
					{Name: "no-docs", Kind: boolFlag, Description: "skip regenerating .workbook/guidelines.md"},
					{Name: "json", Kind: boolFlag, Description: "emit JSON"},
				},
			},
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
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"depend": {
		Name:        "depend",
		Synopsis:    "workbook depend <id-or-prefix> <dependency-id-or-prefix> [--json]",
		Description: "Add a task dependency.",
		Positionals: []string{"<id-or-prefix>", "<dependency-id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"free": {
		Name:        "free",
		Synopsis:    "workbook free <id-or-prefix> <dependency-id-or-prefix> [--json]",
		Description: "Remove a task dependency.",
		Positionals: []string{"<id-or-prefix>", "<dependency-id-or-prefix>"},
		Options: []optionMetadata{
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"next": {
		Name:     "next",
		Synopsis: "workbook next [--any] [--claim] [--limit <n>] [--no-sync] [--json]",
		Description: "Show the next eligible task.\n\n" +
			"A task somebody else is assigned to and you are not is skipped, because it\n" +
			"is work already being done; a task you hold too is still offered, however\n" +
			"many people share it. --any offers the whole eligible set instead.\n\n" +
			"With --claim the task next would pick is assigned to you and published in\n" +
			"the same command, so two agents asking at once do not both walk away with\n" +
			"it. Because the skip has already excluded what somebody else holds, there\n" +
			"is nothing left to refuse: a board whose eligible work is all held answers\n" +
			"with no task and says so, and claiming a task you already hold records\n" +
			"nothing. If the publication loses a race, the claim still stands and the\n" +
			"synchronization that replays it reports who it is shared with.\n\n" +
			"--limit <n> offers the first n eligible tasks in the order next walks\n" +
			"them. In --json, data becomes {\"tasks\": [...], \"eligible\": N} rather than\n" +
			"a single task; the text output is a list header and one row per task. It\n" +
			"cannot be combined with --claim, which takes one task.",
		Options: []optionMetadata{
			{Name: "any", Kind: boolFlag, Description: "include tasks somebody else is assigned to"},
			{Name: "claim", Kind: boolFlag, Description: "assign the chosen task to yourself and publish it"},
			{Name: "limit", Kind: stringFlag, Value: "<n>", Description: "offer up to this many eligible tasks, in next's order"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
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
	"compact": {
		Name: "compact",
		// Two forms, one per line: --adopt refuses every selection flag and
		// --dry-run and --no-sync, so a merged line would advertise
		// combinations the command rejects.
		Synopsis: "workbook compact [--done-only [<duration>] | --deleted-only [<duration>]] [--since <duration>] " +
			"[--keep-attachments] [--no-backup | --backup <path>] [--dry-run] [--no-sync] [--json]\n" +
			"workbook compact --adopt <task> [--discard-local] [--no-backup | --backup <path>] [--json]",
		Description: "Replace the history of Done and Deleted tasks with one checkpoint each.\n\n" +
			"A real run first writes a verified backup bundle of the history it drops.\n" +
			"Attachments on compacted tasks are dropped unless --keep-attachments is\n" +
			"given. A duration such as 30d limits the run to tasks that finished more\n" +
			"than that long ago. --dry-run reports what a run would do and changes\n" +
			"nothing.\n\n" +
			"--adopt <task> settles one task a compaction left stuck: one origin\n" +
			"compacted while this clone had changes that would not replay onto it, or\n" +
			"one this clone compacted and deleted while origin changed it. It fetches,\n" +
			"then retries the replay; with --discard-local it instead takes origin's\n" +
			"version, after saving the local history to a bundle.",
		Options: []optionMetadata{
			{Name: "done-only", Kind: optionalStringFlag, Value: "[<duration>]", Description: "compact only Done tasks, optionally only those done longer ago than this"},
			{Name: "deleted-only", Kind: optionalStringFlag, Value: "[<duration>]", Description: "compact only Deleted tasks, optionally only those deleted longer ago than this"},
			{Name: "since", Kind: stringFlag, Value: "<duration>", Description: "compact only tasks that became Done or Deleted longer ago than this"},
			{Name: "keep-attachments", Kind: boolFlag, Description: "keep attachment contents in the checkpoints"},
			{Name: "no-backup", Kind: boolFlag, Description: "write no backup bundle"},
			{Name: "backup", Kind: stringFlag, Value: "<path>", Description: "write the backup bundle here, or into this directory"},
			{Name: "dry-run", Kind: boolFlag, Description: "report what would be compacted and change nothing"},
			{Name: "no-sync", Kind: boolFlag, Description: "skip synchronizing task refs with the tasks remote (origin unless configured)"},
			{Name: "adopt", Kind: stringFlag, Value: "<task>", Description: "settle one task a compaction left stuck, retrying the replay"},
			{Name: "discard-local", Kind: boolFlag, Description: "with --adopt, take the tasks remote's version after saving the local history to a bundle"},
			{Name: "json", Kind: boolFlag, Description: "emit JSON"},
		},
	},
	"version": {
		Name:        "version",
		Synopsis:    "workbook version [--json]",
		Description: "Show Workbook build metadata.",
		Options:     []optionMetadata{{Name: "json", Kind: boolFlag, Description: "emit JSON"}},
	},
	"completion": {
		Name:     "completion",
		Synopsis: "workbook completion <shell>",
		Description: "Print a shell completion script for bash, zsh or fish.\n\n" +
			"Have the shell read the output. For bash, add `eval \"$(workbook completion\n" +
			"bash)\"` to ~/.bashrc — bash 3.2 reads nothing from a sourced process\n" +
			"substitution; for zsh, write `workbook completion zsh` to a `_workbook` file in\n" +
			"a directory on your fpath, or source it from ~/.zshrc after compinit; for fish,\n" +
			"write `workbook completion fish` to ~/.config/fish/completions/workbook.fish.\n\n" +
			"The Homebrew formula installs all three already.",
		Positionals: []string{"<shell>"},
	},
}

var commandOrder = []string{
	"setup", "create", "list", "show", "update", "delete", "restore", "move", "depend", "free", "next", "rebuild", "validate", "compact", "version", "fetch", "push", "sync", "move-tasks", "reset", "relic", "schema", "status", "priority", "key", "config", "docs", "hooks", "serve", "completion",
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
		// Nor has it an optional-value form.
		if option.Kind == pairFlag || option.Kind == optionalStringFlag {
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

// takeOptionalValueOption removes `--name [value]` from args. The argument
// after the option is its value only when it exists, does not start with `-`,
// and accepts it; anything else is left in place for the flag parser, so
// `--done-only --json` keeps `--json`. `--name=value` always names the value,
// accepted or not, so the caller can say what is wrong with it rather than
// leaving it to read as a stray argument. A repeated option is refused.
func takeOptionalValueOption(
	command, name string,
	args []string,
	accepts func(string) bool,
) (present bool, value string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			rest = append(rest, args[index:]...)
			break
		}
		optionName, inline, hasValue, isFlag := splitFlag(argument)
		if !isFlag || optionName != name {
			rest = append(rest, argument)
			continue
		}
		if present {
			return false, "", nil, core.Errorf(core.CategoryInvocation, "%s accepts --%s once", command, name)
		}
		present = true
		switch {
		case hasValue && inline == "":
			return false, "", nil, core.Errorf(core.CategoryInvocation, "%s --%s= requires a value after the =", command, name)
		case hasValue:
			value = inline
		case index+1 < len(args) && args[index+1] != "" && isRequiredFirstArgument(args[index+1]) && accepts(args[index+1]):
			value = args[index+1]
			index++
		}
	}
	if !present {
		return false, "", args, nil
	}
	return true, value, rest, nil
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
		// An optional value is skipped whenever the next argument could be
		// one. The scan cannot ask the command whether it accepts it, and the
		// only cost of a wrong guess is reading past one argument that the
		// parser will refuse anyway.
		if kind == optionalStringFlag {
			if !hasValue && index+1 < len(args) && isRequiredFirstArgument(args[index+1]) {
				index++
			}
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
