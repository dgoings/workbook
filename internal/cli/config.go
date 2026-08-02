package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/dgoings/workbook/internal/autosync"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

// autoSyncSettingName is the only project setting the config command manages.
// It exists so that recording a project policy does not require hand-editing
// .workbook/config.json, which the project's own guidelines forbid.
const autoSyncSettingName = "auto-sync"

type configAutoSyncReport struct {
	Enabled bool            `json:"enabled"`
	Source  autosync.Source `json:"source"`
	Project string          `json:"project"`
	User    string          `json:"user"`
}

type configShowResult struct {
	Path     string               `json:"path"`
	Version  int                  `json:"version"`
	AutoSync configAutoSyncReport `json:"autoSync"`
}

type configWriteResult struct {
	Path     string `json:"path"`
	Version  int    `json:"version"`
	Setting  string `json:"setting"`
	Value    string `json:"value"`
	Upgraded bool   `json:"upgraded"`
}

func runConfig(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	subcommand, args, err := requiredFirstArgument("config", "config command", args)
	if err != nil {
		return err
	}
	switch subcommand {
	case "show":
		return runConfigShow(ctx, args, cwd, stdout)
	case "set":
		return runConfigSet(ctx, args, cwd, stdout)
	case "unset":
		return runConfigUnset(ctx, args, cwd, stdout)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown config command %q", subcommand)
	}
}

func runConfigShow(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("config", "show")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return err
	}
	user, err := userconfig.Load()
	if err != nil {
		return err
	}
	policy, err := autosync.Resolve(false, config, user)
	if err != nil {
		return err
	}

	result := configShowResult{
		Path:    repository.Root + "/.workbook/config.json",
		Version: config.Version,
		AutoSync: configAutoSyncReport{
			Enabled: policy.Enabled,
			Source:  policy.Source,
			Project: settingDisplay(config.AutoSync),
			User:    userSettingDisplay(user),
		},
	}
	if *jsonMode {
		writeResult(stdout, "config show", result)
		return nil
	}
	fmt.Fprintf(stdout, "Config:\t%s\n", result.Path)
	fmt.Fprintf(stdout, "Version:\t%d\n", result.Version)
	fmt.Fprintf(stdout, "Auto sync:\t%t\t(%s)\n", result.AutoSync.Enabled, result.AutoSync.Source)
	fmt.Fprintf(stdout, "  project:\t%s\n", result.AutoSync.Project)
	fmt.Fprintf(stdout, "  user:\t%s\n", result.AutoSync.User)
	return nil
}

func runConfigSet(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	values, args, err := requiredArguments("config set", []string{"<setting>", "<value>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("config", "set")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if values[0] != autoSyncSettingName {
		return core.Errorf(core.CategoryInvocation, "unknown project setting %q", values[0])
	}
	enabled, parseErr := strconv.ParseBool(values[1])
	if parseErr != nil {
		return core.Errorf(core.CategoryValidation, "%s must be true or false", autoSyncSettingName)
	}
	setting := core.AutoSyncDisabled
	if enabled {
		setting = core.AutoSyncEnabled
	}
	return writeProjectSetting(ctx, cwd, stdout, setting, strconv.FormatBool(enabled), *jsonMode)
}

func runConfigUnset(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	values, args, err := requiredArguments("config unset", []string{"<setting>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("config", "unset")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if values[0] != autoSyncSettingName {
		return core.Errorf(core.CategoryInvocation, "unknown project setting %q", values[0])
	}
	return writeProjectSetting(ctx, cwd, stdout, core.AutoSyncUnset, "unset", *jsonMode)
}

// writeProjectSetting records a policy and reports whether doing so also
// upgraded the document, because that is what makes the project unreadable to
// an older Workbook binary.
func writeProjectSetting(
	ctx context.Context,
	cwd string,
	stdout io.Writer,
	setting core.AutoSyncSetting,
	display string,
	jsonMode bool,
) error {
	repository, before, err := openRepository(ctx, cwd)
	if err != nil {
		return err
	}
	updated, err := repository.SetProjectAutoSync(ctx, setting)
	if err != nil {
		return err
	}

	result := configWriteResult{
		Path:     repository.Root + "/.workbook/config.json",
		Version:  updated.Version,
		Setting:  autoSyncSettingName,
		Value:    display,
		Upgraded: before.Version != updated.Version,
	}
	if jsonMode {
		writeResult(stdout, "config set", result)
		return nil
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", result.Setting, result.Value, result.Path)
	if result.Upgraded {
		fmt.Fprintf(stdout, "Upgraded the project configuration to version %d; commit it, and note that older Workbook versions cannot read it.\n", result.Version)
	}
	return nil
}

func settingDisplay(setting core.AutoSyncSetting) string {
	switch setting {
	case core.AutoSyncEnabled:
		return "true"
	case core.AutoSyncDisabled:
		return "false"
	default:
		return "unset"
	}
}

func userSettingDisplay(user userconfig.Config) string {
	value, found := user.Preferences[autosync.PreferenceKey]
	if !found {
		return "unset"
	}
	enabled, ok := value.(bool)
	if !ok {
		return "invalid"
	}
	return strconv.FormatBool(enabled)
}
