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

type commandSchema struct {
	requiredArguments int
	flags             map[string]flagKind
}

var commandSchemas = map[string]commandSchema{
	"init": {
		flags: map[string]flagKind{
			"key":  stringFlag,
			"json": boolFlag,
		},
	},
	"create": {
		requiredArguments: 1,
		flags: map[string]flagKind{
			"description": stringFlag,
			"status":      stringFlag,
			"priority":    stringFlag,
			"label":       stringFlag,
			"json":        boolFlag,
		},
	},
	"list": {
		flags: map[string]flagKind{
			"status":   stringFlag,
			"priority": stringFlag,
			"label":    stringFlag,
			"all":      boolFlag,
			"json":     boolFlag,
		},
	},
	"board": {
		flags: map[string]flagKind{
			"wide":   boolFlag,
			"narrow": boolFlag,
			"json":   boolFlag,
		},
	},
	"show": {
		requiredArguments: 1,
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"update": {
		requiredArguments: 1,
		flags: map[string]flagKind{
			"title":        stringFlag,
			"description":  stringFlag,
			"status":       stringFlag,
			"priority":     stringFlag,
			"label":        stringFlag,
			"clear-labels": boolFlag,
			"json":         boolFlag,
		},
	},
	"delete": {
		requiredArguments: 1,
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"serve": {
		flags: map[string]flagKind{
			"addr": stringFlag,
		},
	},
	"fetch": {
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"push": {
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"sync": {
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"hooks": {
		requiredArguments: 1,
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"move": {
		requiredArguments: 1,
		flags: map[string]flagKind{
			"before": stringFlag,
			"after":  stringFlag,
			"json":   boolFlag,
		},
	},
	"depend": {
		requiredArguments: 2,
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"free": {
		requiredArguments: 2,
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
	"next": {
		flags: map[string]flagKind{
			"json": boolFlag,
		},
	},
}

type commandFlagSet struct {
	*flag.FlagSet
	schema commandSchema
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

func newFlagSet(command string) *commandFlagSet {
	schema, exists := commandSchemas[command]
	if !exists {
		panic("missing flag schema for " + command)
	}
	flagSet := flag.NewFlagSet(command, flag.ContinueOnError)
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
	if got, exists := flags.schema.flags[name]; !exists || got != wanted {
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
	for name := range flags.schema.flags {
		if _, exists := defined[name]; !exists {
			panic("flag schema defines unregistered " + flags.Name() + " --" + name)
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

	schema, exists := commandSchemas[args[0]]
	if !exists {
		schema = commandSchema{flags: map[string]flagKind{"json": boolFlag}}
	}
	args = args[1:]
	for range schema.requiredArguments {
		if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
			break
		}
		args = args[1:]
	}

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

		kind, known := schema.flags[name]
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
