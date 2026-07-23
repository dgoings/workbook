package cli

import (
	"flag"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

type stringListValue struct {
	values []string
	set    bool
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

func newFlagSet(command string) *flag.FlagSet {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return core.Wrap(core.CategoryInvocation, "invalid "+flags.Name()+" arguments", err)
	}
	if flags.NArg() != 0 {
		return core.Errorf(core.CategoryInvocation, "%s accepts no additional positional arguments", flags.Name())
	}
	return nil
}

func requiredFirstArgument(command, name string, args []string) (string, []string, error) {
	if len(args) == 0 || len(args[0]) > 0 && args[0][0] == '-' {
		return "", nil, core.Errorf(core.CategoryInvocation, "%s must be the first argument after %s", name, command)
	}
	return args[0], args[1:], nil
}

func requestedJSON(args []string) bool {
	for _, argument := range args {
		if argument == "--json" || argument == "--json=true" {
			return true
		}
	}
	return false
}
