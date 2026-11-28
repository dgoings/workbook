package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// The schema commands read and save the relic schemas in the project settings.
// A saved version is added to the configuration ledger, so schema put opens the
// ordinary task session, as status add does: --no-sync, the auto-sync setting
// and a running watcher apply to it exactly as to a status change.

// decodedRelicSchemas is the relic service's schema store over settings that
// were already read. The listing is the one decoded with the keys the command
// is using, so a relic command asks Git nothing more for it; a version's stored
// document is read only when a relic is judged against it.
type decodedRelicSchemas struct {
	repository *gitstore.Repository
	listing    core.RelicSchemaListing
}

func (s decodedRelicSchemas) RelicSchemaListing(context.Context, core.ProjectConfig) (core.RelicSchemaListing, error) {
	return s.listing, nil
}

func (s decodedRelicSchemas) ReadRelicSchema(
	ctx context.Context,
	config core.ProjectConfig,
	version core.RelicSchemaVersion,
) (core.RelicSchemaDocument, *core.RelicSchema, error) {
	return s.repository.ReadRelicSchema(ctx, config, version)
}

// schemaSubcommands lists the verbs, derived from the help schema as the other
// command groups' are.
func schemaSubcommands() []string {
	return commandSchemas["schema"].SubcommandOrder
}

func schemaCommandList() string {
	return "the subcommands are " + strings.Join(schemaSubcommands(), ", ")
}

func runSchema(ctx context.Context, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return core.Errorf(core.CategoryInvocation, "schema takes a subcommand; %s", schemaCommandList())
	}
	subcommand, args := args[0], args[1:]
	switch subcommand {
	case "list":
		return runSchemaList(ctx, args, cwd, stdout, stderr)
	case "show":
		return runSchemaShow(ctx, args, cwd, stdout, stderr)
	case "put":
		return runSchemaPut(ctx, args, cwd, stdin, stdout, stderr)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown schema command %q; %s", subcommand, schemaCommandList())
	}
}

// schemaListEntry is one relic type with a schema, described by its newest
// version. A type with no version of the project's own is described by its
// built-in: no version number, and no saved versions.
type schemaListEntry struct {
	Type     string `json:"type"`
	Version  int    `json:"version,omitempty"`
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
	ID       string `json:"id"`
	BuiltIn  bool   `json:"builtIn,omitempty"`
}

// schemaVersionView is one version, saved or built in: what a listing holds of
// it and the stored document's references and schema. A built-in has no
// version number.
type schemaVersionView struct {
	Type       string            `json:"type"`
	Version    int               `json:"version,omitempty"`
	ID         string            `json:"id"`
	Bytes      int64             `json:"bytes"`
	BuiltIn    bool              `json:"builtIn,omitempty"`
	References map[string]string `json:"references"`
	Schema     json.RawMessage   `json:"schema"`
}

// schemaPutResult is the version schema put saved, or the newest one it found
// already holding the same schema.
type schemaPutResult struct {
	schemaVersionView
	Changed bool `json:"changed"`
}

func schemaView(version core.RelicSchemaVersion, document core.RelicSchemaDocument) schemaVersionView {
	references := document.References
	if references == nil {
		references = map[string]string{}
	}
	view := schemaVersionView{
		Type:       version.Type,
		Version:    version.Version,
		ID:         version.ID,
		Bytes:      version.Bytes,
		References: references,
		Schema:     document.Schema,
	}
	if version.IsBuiltIn() {
		view.Version, view.BuiltIn = 0, true
	}
	return view
}

// readSchemaListing opens the project and reads the saved versions, which reads
// no schema.
func readSchemaListing(
	ctx context.Context,
	cwd string,
	stderr io.Writer,
) (*gitstore.Repository, core.ProjectConfig, core.RelicSchemaListing, error) {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return nil, core.ProjectConfig{}, core.RelicSchemaListing{}, err
	}
	listing, err := repository.RelicSchemaListing(ctx, config)
	if err != nil {
		return nil, core.ProjectConfig{}, core.RelicSchemaListing{}, err
	}
	return repository, config, listing, nil
}

func runSchemaList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("schema", "list")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	_, _, listing, err := readSchemaListing(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	listing = listing.WithBuiltIns()
	entries := []schemaListEntry{}
	for _, relicType := range listing.EffectiveTypes() {
		newest, _ := listing.Newest(relicType)
		count := 0
		for _, version := range listing.Versions {
			if version.Type == relicType {
				count++
			}
		}
		entry := schemaListEntry{
			Type: relicType, Version: newest.Version, Versions: count, Bytes: newest.Bytes, ID: newest.ID,
		}
		if newest.IsBuiltIn() {
			entry.Version, entry.BuiltIn = 0, true
		}
		entries = append(entries, entry)
	}
	if *jsonMode {
		writeResult(stdout, "schema list", entries)
		return nil
	}
	for _, entry := range entries {
		if entry.BuiltIn {
			writeBuiltInSchemaLine(stdout, entry.Type, entry.ID, entry.Bytes)
			continue
		}
		plural := "versions"
		if entry.Versions == 1 {
			plural = "version"
		}
		fmt.Fprintf(stdout, "%s\tversion %d\t%d %s\t%d bytes\n",
			singleLine(entry.Type), entry.Version, entry.Versions, plural, entry.Bytes)
	}
	return nil
}

// writeBuiltInSchemaLine is the line that names a built-in, in schema list
// and above the schema in schema show.
func writeBuiltInSchemaLine(output io.Writer, relicType, id string, size int64) {
	fmt.Fprintf(output, "%s\tbuilt in %s\t%d bytes\n", singleLine(relicType), singleLine(id), size)
}

func runSchemaShow(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("schema show", []string{"<type|built-in name>"}, args)
	if err != nil {
		return err
	}
	// A built-in's name holds ':', which no relic type does.
	named := values[0]
	byName := core.IsBuiltInRelicSchemaID(named)
	flags := newFlagSet("schema", "show")
	numberText := flags.String("version", "", "show this version instead of the newest")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	number := 0
	if flagGiven(flags, "version") {
		if byName {
			return core.Errorf(core.CategoryInvocation,
				"schema show takes no --version with a built-in schema's name, which already names one version")
		}
		number, err = strconv.Atoi(*numberText)
		if err != nil || number < 1 {
			return core.Errorf(core.CategoryInvocation,
				"schema show --version %q is not a version number; give 1 or more", *numberText)
		}
	}
	if !byName {
		if err := core.ValidateRelicType(named); err != nil {
			return err
		}
	}
	repository, config, listing, err := readSchemaListing(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	version, err := schemaToShow(listing.WithBuiltIns(), named, byName, number)
	if err != nil {
		return err
	}
	document, _, err := core.ReadRelicSchemaVersion(ctx, repository, config, version)
	if err != nil {
		return err
	}
	view := schemaView(version, document)
	if *jsonMode {
		writeResult(stdout, "schema show", view)
		return nil
	}
	return writeSchemaVersion(stdout, view)
}

// schemaToShow picks the version schema show was asked for: the built-in
// named; or, for a type, its newest version (its built-in when the project has
// saved none), or the project's version of the given number when number is not
// zero.
func schemaToShow(listing core.RelicSchemaListing, named string, byName bool, number int) (core.RelicSchemaVersion, error) {
	if byName {
		version, found := listing.ByID(named)
		if !found {
			return core.RelicSchemaVersion{}, core.Errorf(core.CategoryNotFound,
				"this Workbook has no built-in schema %s", named)
		}
		return version, nil
	}
	newest, found := listing.Newest(named)
	if !found {
		return core.RelicSchemaVersion{}, core.Errorf(core.CategoryNotFound, "type %s has no schema", named)
	}
	if number == 0 {
		return newest, nil
	}
	if version, found := listing.Number(named, number); found {
		return version, nil
	}
	if newest.IsBuiltIn() {
		return core.RelicSchemaVersion{}, core.Errorf(core.CategoryNotFound,
			"type %s has no schema version %d of this project's own; its built-in schema is %s", named, number, newest.ID)
	}
	return core.RelicSchemaVersion{}, core.Errorf(core.CategoryNotFound,
		"type %s has no schema version %d", named, number)
}

// writeSchemaVersion prints a version's line and then its schema indented. The
// schema's strings are written by anyone who can push, so the text passes
// through the control-character filter, which keeps its layout.
func writeSchemaVersion(output io.Writer, view schemaVersionView) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, view.Schema, "  ", "  "); err != nil {
		return core.Wrap(core.CategoryCorruptData, "the stored schema cannot be indented", err)
	}
	if view.BuiltIn {
		writeBuiltInSchemaLine(output, view.Type, view.ID, view.Bytes)
	} else {
		fmt.Fprintf(output, "%s\tversion %d\t%s\t%d bytes\n",
			singleLine(view.Type), view.Version, singleLine(view.ID), view.Bytes)
	}
	fmt.Fprintf(output, "  %s\n", core.DisplayText(indented.String()))
	return nil
}

// readSchemaFile reads the schema file at path, or standard input for "-". It
// reads at most one byte more than the largest stored schema, which is enough
// to know a file is too large.
func readSchemaFile(relicType, path string, stdin io.Reader) ([]byte, error) {
	source, reader := "standard input", stdin
	if path != "-" {
		source = singleLine(path)
		file, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, core.Wrap(core.CategoryNotFound, "cannot read "+source, err)
		}
		if err != nil {
			return nil, core.Wrap(core.CategoryOperational, "cannot read "+source, err)
		}
		defer file.Close()
		reader = file
	}
	content, err := io.ReadAll(io.LimitReader(reader, core.MaxRelicSchemaBytes+1))
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot read the schema from "+source, err)
	}
	if len(content) > core.MaxRelicSchemaBytes {
		return nil, core.Errorf(core.CategoryValidation,
			"schema for %s: the file from %s is more than %d bytes; the limit is %d bytes",
			relicType, source, core.MaxRelicSchemaBytes, core.MaxRelicSchemaBytes)
	}
	return content, nil
}

func runSchemaPut(ctx context.Context, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("schema put", []string{"<type>"}, args)
	if err != nil {
		return err
	}
	relicType := values[0]
	// The file may be "-", standard input, which requiredArguments would take
	// for an option.
	if len(args) == 0 || args[0] != "-" && !isRequiredFirstArgument(args[0]) {
		return core.Errorf(core.CategoryInvocation, "<file|-> must be the second argument after schema put")
	}
	path, args := args[0], args[1:]
	flags := newFlagSet("schema", "put")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with the tasks remote (origin unless configured)")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if err := core.ValidateRelicType(relicType); err != nil {
		return err
	}
	source, err := readSchemaFile(relicType, path, stdin)
	if err != nil {
		return err
	}

	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := session.fetchBefore(ctx); err != nil {
		return err
	}
	// The fetch may have brought a teammate's versions, which the references
	// are pinned against, and new statuses, which the guidelines document.
	if err := session.refreshConfiguration(ctx); err != nil {
		return err
	}
	result, err := putRelicSchema(ctx, session.repository, session.config, session.schemas, relicType, source)
	if err != nil {
		return err
	}
	// Published even when nothing was written, so that running the same put
	// again sends a version an earlier run could not.
	session.publishConfig(ctx)
	if err := session.publicationRefusal(); err != nil {
		return err
	}
	docs, docsErr := regenerateGuidelines(ctx, session, session.service.Vocabulary, session.service.Priorities,
		session.service.KeySet(), *noDocs)
	writeSchemaPut(stdout, stderr, result, session, docs, docsErr, *jsonMode)
	return nil
}

// putRelicSchema compiles source against the newest version of each type it
// refers to and saves it as relicType's next version.
func putRelicSchema(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	listing core.RelicSchemaListing,
	relicType string,
	source []byte,
) (schemaPutResult, error) {
	newest := func(other string) (core.RelicSchemaVersion, *core.RelicSchema, bool, error) {
		version, found := listing.Newest(other)
		if !found {
			return core.RelicSchemaVersion{}, nil, false, nil
		}
		_, schema, err := repository.ReadRelicSchema(ctx, config, version)
		if err != nil {
			return core.RelicSchemaVersion{}, nil, false, err
		}
		return version, schema, true, nil
	}
	document, _, err := core.CompileRelicSchema(relicType, source, newest)
	if err != nil {
		return schemaPutResult{}, err
	}
	version, changed, err := repository.WriteRelicSchema(ctx, config, core.CryptoULIDSource{Now: recordedNow},
		document, "workbook: put schema for "+relicType)
	if err != nil {
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			return schemaPutResult{}, core.Wrap(core.CategoryStaleWrite,
				"another process changed this project's settings while this command was writing; nothing was recorded, so run it again",
				err)
		}
		return schemaPutResult{}, err
	}
	return schemaPutResult{schemaVersionView: schemaView(version, document), Changed: changed}, nil
}

// writeSchemaPut reports a put in the envelope a configuration change uses.
func writeSchemaPut(
	stdout, stderr io.Writer,
	result schemaPutResult,
	session *taskSession,
	docs *agentdocs.Report,
	docsErr error,
	jsonMode bool,
) {
	var warnings []core.Warning
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the schema is recorded locally, but " + session.report.Detail,
		})
	}
	warnings = append(warnings, schemaDocsWarning(docs, docsErr)...)
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(ResultEnvelope{
			Format:         "workbook.result",
			Version:        1,
			Command:        "schema put",
			Data:           result,
			Conflict:       session.conflicts,
			ConfigConflict: session.report.configConflicts,
			RelicConflict:  session.relicConflicts,
			Warnings:       withRelicWarnings(warnings, session.relicWarnings),
			Sync:           &session.report,
		})
		return
	}
	if result.Changed {
		fmt.Fprintf(stdout, "%s\tversion %d\t%s\t%d bytes\n",
			singleLine(result.Type), result.Version, singleLine(result.ID), result.Bytes)
	} else {
		fmt.Fprintf(stdout, "No change: %s version %d already holds this schema\n",
			singleLine(result.Type), result.Version)
	}
	if docs != nil {
		for _, artifact := range docs.Artifacts {
			fmt.Fprintf(stdout, "\tdocs:\t%s\t%s\n", artifact.Path, artifactAction(artifact))
		}
	}
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeRelicConflicts(stdout, session.relicConflicts)
	writeWarnings(stderr, warnings)
	writeRelicWarnings(stderr, session.relicWarnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
}

// schemaDocsWarning is docsWarning for a saved schema.
func schemaDocsWarning(report *agentdocs.Report, err error) []core.Warning {
	if report == nil || err == nil {
		return nil
	}
	message := "the schema was recorded, but the guidelines could not be regenerated: " + publicErrorMessage(err)
	if blocked := report.Blocked(); len(blocked) > 0 {
		names := make([]string, 0, len(blocked))
		for _, artifact := range blocked {
			names = append(names, artifact.Path)
		}
		message = "the schema was recorded, but " + strings.Join(names, ", ") +
			" was modified locally and was not regenerated; overwrite it with: workbook docs update --force"
	}
	return []core.Warning{{Code: core.WarningDocsRefresh, Message: message}}
}
