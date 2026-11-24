package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// relicSubcommands lists the verbs, for the error a bare `workbook relic`
// produces. It is derived from the help schema so a verb cannot be added
// without the refusal learning about it.
func relicSubcommands() []string {
	return commandSchemas["relic"].SubcommandOrder
}

func relicCommandList() string {
	return "the subcommands are " + strings.Join(relicSubcommands(), ", ")
}

func runRelic(ctx context.Context, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return core.Errorf(core.CategoryInvocation, "relic takes a subcommand; %s", relicCommandList())
	}
	subcommand, args := args[0], args[1:]
	switch subcommand {
	case "create":
		return runRelicCreate(ctx, args, cwd, stdin, stdout, stderr)
	case "list":
		return runRelicList(ctx, args, cwd, stdout, stderr)
	case "show":
		return runRelicShow(ctx, args, cwd, stdout, stderr)
	case "update":
		return runRelicUpdate(ctx, args, cwd, stdout, stderr)
	case "delete":
		return runRelicSetDeleted(ctx, "delete", args, cwd, stdout, stderr)
	case "restore":
		return runRelicSetDeleted(ctx, "restore", args, cwd, stdout, stderr)
	case "export":
		return runRelicExport(ctx, args, cwd, stdout, stderr)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown relic command %q; %s", subcommand, relicCommandList())
	}
}

// openRelicService opens the repository and returns a service whose Reader and
// Writer are the repository, recording changes under the acting person. A
// checkout with no acting person is refused before anything is read, as a task
// mutation refuses one.
func openRelicService(ctx context.Context, cwd string, stderr io.Writer) (core.RelicService, error) {
	return openRelicServiceFor(ctx, cwd, stderr, true)
}

// openRelicReadService is openRelicService for a command that only reads, so
// a checkout with no acting person can still read its relics.
func openRelicReadService(ctx context.Context, cwd string, stderr io.Writer) (core.RelicService, error) {
	return openRelicServiceFor(ctx, cwd, stderr, false)
}

func openRelicServiceFor(ctx context.Context, cwd string, stderr io.Writer, withActor bool) (core.RelicService, error) {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return core.RelicService{}, err
	}
	var actor string
	if withActor {
		actor, err = repository.Actor(ctx)
		if err != nil {
			return core.RelicService{}, err
		}
	}
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return core.RelicService{}, err
	}
	return core.RelicService{
		Config: config,
		Keys:   state.Keys,
		Reader: repository,
		Writer: repository,
		IDs:    core.CryptoULIDSource{Now: recordedNow},
		// Relic documents record their times in UTC, as the stored format
		// shows them.
		Now:   func() time.Time { return recordedNow().UTC() },
		Actor: actor,
	}, nil
}

func runRelicCreate(ctx context.Context, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) error {
	title, titleGiven := "", false
	if len(args) > 0 && isRequiredFirstArgument(args[0]) {
		title, titleGiven, args = args[0], true, args[1:]
	}
	flags := newFlagSet("relic", "create")
	relicType := flags.String("type", "", "relic type, such as spec or plan")
	fromFile := flags.String("from-file", "", "create from a whole relic in one JSON document (- reads standard input)")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	typed := map[string]bool{}
	flags.Visit(func(visited *flag.Flag) { typed[visited.Name] = true })
	typeGiven := typed["type"]

	var input core.RelicCreateInput
	if typed["from-file"] {
		document, err := readRelicDocument(*fromFile, stdin)
		if err != nil {
			return err
		}
		input, err = core.RelicCreateInputFromDocument(document)
		if err != nil {
			return err
		}
	} else {
		if !titleGiven {
			return core.Errorf(core.CategoryInvocation,
				"relic create needs a title and --type, or --from-file <file>")
		}
		if !typeGiven {
			return core.Errorf(core.CategoryInvocation,
				"relic create needs --type <type>, such as spec or plan, or --from-file <file>")
		}
	}
	if titleGiven {
		input.Title = title
	}
	if typeGiven {
		input.Type = *relicType
	}

	service, err := openRelicService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	result, err := service.Create(ctx, input)
	if err != nil {
		return err
	}
	writeRelicMutation(stdout, "relic create", result, *jsonMode)
	return nil
}

// readRelicDocument reads and decodes the workbook.relic document at path, or
// on standard input for "-".
func readRelicDocument(path string, stdin io.Reader) (core.RelicDocument, error) {
	var content []byte
	var err error
	if path == "-" {
		content, err = io.ReadAll(stdin)
		if err != nil {
			return core.RelicDocument{}, core.Wrap(core.CategoryOperational, "cannot read the relic document from standard input", err)
		}
	} else {
		content, err = os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return core.RelicDocument{}, core.Wrap(core.CategoryNotFound, "cannot read "+singleLine(path), err)
		}
		if err != nil {
			return core.RelicDocument{}, core.Wrap(core.CategoryOperational, "cannot read "+singleLine(path), err)
		}
	}
	return core.DecodeRelicDocument(content)
}

func runRelicList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("relic", "list")
	relicType := flags.String("type", "", "show only relics of this type")
	all := flags.Bool("all", false, "include deleted relics, each marked deleted")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	service, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	summaries, err := service.List(ctx, core.RelicListFilter{Type: *relicType, IncludeDeleted: *all})
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "relic list", summaries)
		return nil
	}
	writeRelicList(stdout, summaries)
	return nil
}

func runRelicShow(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("relic show", []string{"<relic>[#<part>]"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("relic", "show")
	bodies := flags.Bool("body", false, "include body text")
	removed := flags.Bool("removed", false, "include removed parts")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	address := values[0]
	service, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	document, err := service.Show(ctx, address, core.RelicShowOptions{Bodies: *bodies, Removed: *removed})
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "relic show", document)
		return nil
	}
	if _, _, addressesPart, _ := core.ParsePartAddress(address); addressesPart && *bodies {
		return writeRelicPartBody(stdout, document)
	}
	writeRelicDocument(stdout, document, *bodies)
	return nil
}

// writeRelicPartBody writes the one part's body exactly as stored, and
// nothing else, so it can be piped or redirected to a file.
func writeRelicPartBody(stdout io.Writer, document core.RelicDocument) error {
	part := document.Parts[0]
	if part.Body == nil {
		return core.Errorf(core.CategoryNotFound, "part %s has no body", part.ID)
	}
	var content []byte
	switch {
	case part.Body.Text != nil:
		content = []byte(*part.Body.Text)
	case part.Body.Base64 != nil:
		decoded, err := base64.StdEncoding.DecodeString(*part.Body.Base64)
		if err != nil {
			return core.Wrap(core.CategoryCorruptData, "the body of part "+part.ID+" cannot be decoded", err)
		}
		content = decoded
	}
	_, err := stdout.Write(content)
	return err
}

func runRelicSetDeleted(ctx context.Context, verb string, args []string, cwd string, stdout, stderr io.Writer) error {
	command := "relic " + verb
	values, args, err := requiredArguments(command, []string{"<relic>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("relic", verb)
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	service, err := openRelicService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	change := service.Delete
	if verb == "restore" {
		change = service.Restore
	}
	result, err := change(ctx, values[0])
	if err != nil {
		return err
	}
	writeRelicMutation(stdout, command, result, *jsonMode)
	return nil
}

func runRelicExport(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("relic export", []string{"<relic>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("relic", "export")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	service, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	document, err := service.Export(ctx, values[0])
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "relic export", document)
		return nil
	}
	return writeRelicExport(stdout, document)
}
