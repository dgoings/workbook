package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/autosync"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

// autoSyncSettingName is the project setting recorded in the tracked
// configuration file. It exists so that recording a project policy does not
// require hand-editing .workbook/config.json, which the project's own
// guidelines forbid.
const autoSyncSettingName = "auto-sync"

// configSettingKind says where a project setting is recorded, which is the one
// thing `config set` has to know before it can do anything.
//
// The two kinds are genuinely different writes and not an implementation
// detail: a tracked-file setting is a line in a committed file that Git moves
// with the branch, and a ledger setting is an operation in shared configuration
// history that synchronizes on its own and can conflict with a teammate's. One
// command family covers both because they are the same question from where
// somebody stands — "what is this project's setting" — and the difference shows
// up only in what the result reports.
type configSettingKind uint8

const (
	trackedFileSetting configSettingKind = iota
	ledgerSetting
)

// configSettings is the whole table of project settings. Adding one is an entry
// here plus the code that reads it; nothing else in this file names a setting.
var configSettings = map[string]configSettingKind{
	autoSyncSettingName:      trackedFileSetting,
	core.DisplayProjectName:  ledgerSetting,
	core.DisplayPrimaryColor: ledgerSetting,
	core.DisplayTextColor:    ledgerSetting,
}

// configSettingOrder lists them the way `config show` prints them and the way
// a refusal lists them, so the two never drift apart.
var configSettingOrder = append([]string{autoSyncSettingName}, core.DisplaySettingNames...)

// settingKind resolves a word somebody typed, and refuses one that names no
// setting with the list.
//
// The category is validation rather than invocation, which is a deliberate
// change from the shape this command had when `auto-sync` was the only setting.
// A misspelled setting is a bad value for an argument the command does accept,
// which is the same thing `workbook status add "Not A Token"` is; reporting it
// as a bad invocation would exit 2 where every other bad value exits 5.
func settingKind(setting string) (configSettingKind, error) {
	kind, known := configSettings[setting]
	if !known {
		return 0, core.Errorf(core.CategoryValidation,
			"unknown project setting %q; the settings are %s",
			setting, strings.Join(configSettingOrder, ", "))
	}
	return kind, nil
}

type configAutoSyncReport struct {
	Enabled bool            `json:"enabled"`
	Source  autosync.Source `json:"source"`
	Project string          `json:"project"`
	User    string          `json:"user"`
}

// displaySettingView is one display setting as `config show` reports it.
//
// Source is what makes the report readable: an empty value means nothing at
// all until it says whether that is a decision or the absence of one. Default
// carries what the board falls back to where there is a value to name, which is
// the project name; the colors' fallback is a whole palette rather than a
// string, so naming one would be a lie by omission.
type displaySettingView struct {
	Setting string `json:"setting"`
	Value   string `json:"value,omitempty"`
	Source  string `json:"source"`
	Default string `json:"default,omitempty"`
}

// displayView is the project's display settings as every display envelope
// reports them, the sibling of vocabularyView.
type displayView struct {
	// Head is the configuration ledger's tip, empty for a project with none.
	Head string `json:"head"`
	// Seeded reports that a ledger supplied these values. False means the
	// project has never recorded any configuration at all, which is every
	// project until somebody changes a status or a display setting.
	Seeded   bool                 `json:"seeded"`
	Settings []displaySettingView `json:"settings"`
}

type configShowResult struct {
	Path     string               `json:"path"`
	Version  int                  `json:"version"`
	AutoSync configAutoSyncReport `json:"autoSync"`
	// Display is added beside the existing members rather than replacing any of
	// them, so a caller already parsing this document keeps working.
	Display displayView `json:"display"`
}

type configWriteResult struct {
	Path     string `json:"path"`
	Version  int    `json:"version"`
	Setting  string `json:"setting"`
	Value    string `json:"value"`
	Upgraded bool   `json:"upgraded"`
}

// configDisplayChange is what one display command did, in the shape statusChange
// has for the status family: the verb, the setting, and both values where they
// exist.
type configDisplayChange struct {
	// Operation is the verb — set or unset — rather than the durable operation
	// type, for the same reason statusChange reports one.
	Operation string `json:"operation"`
	Setting   string `json:"setting"`
	// Value is what the setting is now, absent for a clearing.
	Value string `json:"value,omitempty"`
	// From is what it was, absent when nothing was configured.
	From string `json:"from,omitempty"`
}

// configDisplayResult is the data member of every mutating display envelope.
type configDisplayResult struct {
	Change  configDisplayChange `json:"change"`
	Display displayView         `json:"display"`
	Inverse statusInverse       `json:"inverse"`
}

func runConfig(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	subcommand, args, err := requiredFirstArgument("config", "config command", args)
	if err != nil {
		return err
	}
	switch subcommand {
	case "show":
		return runConfigShow(ctx, args, cwd, stdout, stderr)
	case "set":
		return runConfigSet(ctx, args, cwd, stdout, stderr)
	case "unset":
		return runConfigUnset(ctx, args, cwd, stdout, stderr)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown config command %q", subcommand)
	}
}

func runConfigShow(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("config", "show")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
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
	// One read of the ledger answers for all three display settings, which is
	// the same read `status list` makes and never a second one: the values are
	// one commit's, or they are not this project's.
	state, err := repository.LoadVocabularyState(ctx, config)
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
		Display: newDisplayView(state.Head, state.Seeded, state.Display),
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
	writeDisplayView(stdout, result.Display)
	return nil
}

// newDisplayView resolves the three settings for reporting, saying of each one
// whether anybody decided it.
func newDisplayView(head string, seeded bool, settings core.DisplaySettings) displayView {
	view := displayView{Head: head, Seeded: seeded, Settings: make([]displaySettingView, 0, len(core.DisplaySettingNames))}
	for _, setting := range core.DisplaySettingNames {
		value, _ := settings.Value(setting)
		entry := displaySettingView{Setting: setting, Value: value, Source: "default"}
		if entry.Value != "" {
			entry.Source = "configured"
		}
		if setting == core.DisplayProjectName {
			entry.Default = core.DefaultProjectName
		}
		view.Settings = append(view.Settings, entry)
	}
	return view
}

func writeDisplayView(output io.Writer, view displayView) {
	fmt.Fprintf(output, "Display:\t%s\n", displayLedgerLine(view))
	for _, setting := range view.Settings {
		fmt.Fprintf(output, "  %s:\t%s\n", setting.Setting, displaySettingLine(setting))
	}
}

func displayLedgerLine(view displayView) string {
	if !view.Seeded {
		return "no configuration ledger"
	}
	return view.Head
}

// displaySettingLine says the value and where it came from, and never prints an
// empty column: a setting nobody configured reads as its default, named where
// there is a name for it.
func displaySettingLine(setting displaySettingView) string {
	if setting.Value != "" {
		return singleLine(setting.Value) + "\t(configured)"
	}
	if setting.Default != "" {
		return singleLine(setting.Default) + "\t(default)"
	}
	return "(default)"
}

func runConfigSet(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("config set", []string{"<setting>", "<value>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("config", "set")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	kind, err := settingKind(values[0])
	if err != nil {
		return err
	}
	if kind == ledgerSetting {
		return runDisplayMutation(ctx, cwd, "config set", values[0], values[1], false, *noSync, *jsonMode, stdout, stderr)
	}
	enabled, parseErr := strconv.ParseBool(values[1])
	if parseErr != nil {
		return core.Errorf(core.CategoryValidation, "%s must be true or false", autoSyncSettingName)
	}
	setting := core.AutoSyncDisabled
	if enabled {
		setting = core.AutoSyncEnabled
	}
	return writeProjectSetting(ctx, cwd, stdout, stderr, "config set", setting, strconv.FormatBool(enabled), *jsonMode)
}

func runConfigUnset(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("config unset", []string{"<setting>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("config", "unset")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	kind, err := settingKind(values[0])
	if err != nil {
		return err
	}
	if kind == ledgerSetting {
		return runDisplayMutation(ctx, cwd, "config unset", values[0], "", true, *noSync, *jsonMode, stdout, stderr)
	}
	return writeProjectSetting(ctx, cwd, stdout, stderr, "config unset", core.AutoSyncUnset, "unset", *jsonMode)
}

// runDisplayMutation records one display setting in the configuration ledger.
//
// It is runStatusMutation's shape and for the same reasons: open the session,
// fetch first so the change is authored against what a teammate may have just
// published, validate what somebody typed where they can still be told the
// rule, write one pack, publish the ledger, and report the whole thing in one
// envelope. What it does not have is a documentation hook — nothing generated
// states a project's name or colors, so there is no `--no-docs` here and
// nothing to regenerate.
func runDisplayMutation(
	ctx context.Context,
	cwd string,
	command string,
	setting string,
	value string,
	clear bool,
	noSync bool,
	jsonMode bool,
	stdout, stderr io.Writer,
) error {
	session, err := openTaskSession(ctx, cwd, noSync, true, stderr)
	if err != nil {
		return err
	}
	session.fetchBefore(ctx)
	// Read after the fetch, exactly as the status verbs refresh the vocabulary
	// after theirs: the previous value this reports and inverts is the one the
	// write is about to replace, not the one this clone opened with.
	state, err := session.repository.LoadVocabularyState(ctx, session.config)
	if err != nil {
		return err
	}
	before, _ := state.Display.Value(setting)

	operation := core.ConfigOperation{Type: core.ConfigDisplayUnset, Setting: setting}
	change := configDisplayChange{Operation: "unset", Setting: setting, From: before}
	if !clear {
		// Canonicalized here, at the boundary, so `#ABC123` and a name typed
		// with a stray space are accepted from a person and stored as one value.
		canonical, err := core.CanonicalDisplayValue(setting, value)
		if err != nil {
			return err
		}
		operation = core.ConfigOperation{Type: core.ConfigDisplaySet, Setting: setting, Value: canonical}
		change = configDisplayChange{Operation: "set", Setting: setting, Value: canonical, From: before}
	}
	if err := refuseUnchangedDisplay(setting, before, operation); err != nil {
		return err
	}

	written, err := session.repository.WriteConfigOperation(
		ctx, session.config, core.CryptoULIDSource{}, []core.ConfigOperation{operation},
		displayCommitSubject(change))
	if err != nil {
		return displayWriteError(err)
	}
	session.publishConfig(ctx)

	result := configDisplayResult{
		Change:  change,
		Display: newDisplayView(written.Head, true, written.State.Display()),
		Inverse: displayChangeInverse(state.Display, operation),
	}
	writeDisplayMutation(stdout, stderr, command, result, session, jsonMode)
	return nil
}

// refuseUnchangedDisplay refuses a display command whose operation would record
// nothing, which is what `status label` and `status untag` already do when they
// are asked for a state a status is already in.
//
// The doctrine is the same and the stake is higher. A display operation carries
// a generation-two marker, and a marker in a project's checkpoint is permanent:
// every clone running v0.5.0 stops being able to change that project's
// configuration from the moment one is recorded. Authoring a pack for a clearing
// of something nobody configured would therefore spend the whole cost of the
// bump — on every teammate, for the life of the project — to record an operation
// whose own inverse renderer already answers "this changed nothing". The cost is
// only opt-in while opting in takes an actual change.
func refuseUnchangedDisplay(setting, before string, operation core.ConfigOperation) error {
	switch operation.Type {
	case core.ConfigDisplaySet:
		if operation.Value == before {
			return core.Errorf(core.CategoryValidation, "%s is already %q", setting, before)
		}
	case core.ConfigDisplayUnset:
		if before == "" {
			return core.Errorf(core.CategoryValidation,
				"%s is not configured, so there is nothing to clear", setting)
		}
	}
	return nil
}

// displayCommitSubject writes what the ledger's `git log` says about this
// change, in the same voice a status change uses.
func displayCommitSubject(change configDisplayChange) string {
	if change.Operation == "unset" {
		return "workbook: clear " + change.Setting
	}
	return fmt.Sprintf("workbook: set %s to %s", change.Setting, change.Value)
}

// displayWriteError rewords a lost compare-and-swap, the way statusWriteError
// does and for the same reason: the answer is always to run the same command
// again, and saying so is the difference between an error a script retries and
// one it reports.
func displayWriteError(err error) error {
	if core.CategoryOf(err) == core.CategoryStaleWrite {
		return core.Wrap(core.CategoryStaleWrite,
			"another process changed this project's configuration while this command was writing; "+
				"nothing was recorded, so run it again",
			err)
	}
	return err
}

func writeDisplayMutation(
	stdout, stderr io.Writer,
	command string,
	result configDisplayResult,
	session *taskSession,
	jsonMode bool,
) {
	var warnings []core.Warning
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the display setting was recorded locally, but " + session.report.Detail,
		})
	}
	if jsonMode {
		envelope := ResultEnvelope{
			Format:         "workbook.result",
			Version:        1,
			Command:        command,
			Data:           result,
			Conflict:       session.conflicts,
			ConfigConflict: session.report.configConflicts,
			Warnings:       warnings,
			Sync:           &session.report,
		}
		_ = json.NewEncoder(stdout).Encode(envelope)
		return
	}
	writeDisplayChange(stdout, result)
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeWarnings(stderr, warnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
}

// writeDisplayChange renders a change as a heading and its details, the shape
// writeStatusChange uses: one column-zero line that cannot be forged from
// inside a value, then tab-indented fields.
func writeDisplayChange(output io.Writer, result configDisplayResult) {
	change := result.Change
	fmt.Fprintf(output, "Display:\t%s\t%s\n", change.Operation, change.Setting)
	if change.Value != "" {
		fmt.Fprintf(output, "\tvalue:\t%s\n", singleLine(change.Value))
	}
	if change.From != "" {
		fmt.Fprintf(output, "\twas:\t%s\n", singleLine(change.From))
	}
	if result.Inverse.Command != "" {
		exactness := "\t(not exact)"
		if result.Inverse.Exact {
			exactness = ""
		}
		fmt.Fprintf(output, "\tinverse:\t%s%s\n", singleLine(result.Inverse.Command), exactness)
	}
}

// writeProjectSetting records a policy and reports whether doing so also
// upgraded the document, because that is what makes the project unreadable to
// an older Workbook binary. Both writing verbs share this body, so the caller
// names itself: a result envelope has to report the command that was run.
func writeProjectSetting(
	ctx context.Context,
	cwd string,
	stdout io.Writer,
	stderr io.Writer,
	command string,
	setting core.AutoSyncSetting,
	display string,
	jsonMode bool,
) error {
	repository, before, err := openRepository(ctx, cwd, stderr)
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
		writeResult(stdout, command, result)
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

// displayChangeInverse is the verb path's inverse, and displayPackInverse is
// the log's. They are the same computation over the same operation, so a
// command and the log entry it later produces cannot disagree.
func displayChangeInverse(before core.DisplaySettings, operation core.ConfigOperation) statusInverse {
	if inverse := displayPackInverse(before, operation); inverse != nil {
		return *inverse
	}
	return statusInverse{}
}

// displayPackInverse is the command that undoes one recorded display operation.
//
// Both directions are exact whenever the previous value is known, which for
// this family is always: the section holds three independent values, so undoing
// one is a single command and nothing else in the pack can change what it has
// to say. Setting something that was unconfigured inverts to the clearing verb
// rather than to a set of an empty value, because there is no such value —
// "nothing is configured" is the absence of one.
//
// Clearing something already clear has no inverse at all, and reports none: the
// operation changed nothing, and printing a command that would also change
// nothing reads as advice. The verb path no longer authors such an operation —
// refuseUnchangedDisplay stops it before anything is written — but the log path
// renders whatever a ledger holds, including a pack some other writer recorded.
func displayPackInverse(before core.DisplaySettings, operation core.ConfigOperation) *statusInverse {
	previous, _ := before.Value(operation.Setting)
	switch operation.Type {
	case core.ConfigDisplaySet:
		if previous == "" {
			return &statusInverse{Command: configCommand("unset", operation.Setting), Exact: true}
		}
		return &statusInverse{Command: configCommand("set", operation.Setting, previous), Exact: true}
	case core.ConfigDisplayUnset:
		if previous == "" {
			return nil
		}
		return &statusInverse{Command: configCommand("set", operation.Setting, previous), Exact: true}
	default:
		return nil
	}
}

// configCommand renders a runnable `workbook config` line, quoting exactly what
// statusCommand quotes. It is a second renderer rather than a parameter on that
// one because the prefix is the whole difference and a family that printed the
// wrong verb would print a command that does not exist.
func configCommand(parts ...string) string {
	quoted := make([]string, 0, len(parts)+2)
	quoted = append(quoted, "workbook", "config")
	for _, part := range parts {
		quoted = append(quoted, quoteStatusArgument(part))
	}
	return strings.Join(quoted, " ")
}
