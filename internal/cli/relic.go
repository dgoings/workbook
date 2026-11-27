package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
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
	case "diff":
		return runRelicDiff(ctx, args, cwd, stdout, stderr)
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

// openRelicServiceFor opens the repository and a relic service whose Reader is
// that repository, and returns the repository too, for a caller that puts
// another reader in front of it.
func openRelicServiceFor(ctx context.Context, cwd string, stderr io.Writer, withActor bool) (core.RelicService, *gitstore.Repository, error) {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return core.RelicService{}, nil, err
	}
	var actor string
	if withActor {
		actor, err = repository.Actor(ctx)
		if err != nil {
			return core.RelicService{}, nil, err
		}
	}
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return core.RelicService{}, nil, err
	}
	return newRelicService(repository, config, state.Keys, actor), repository, nil
}

func runRelicCreate(ctx context.Context, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) error {
	title, titleGiven := "", false
	if len(args) > 0 && isRequiredFirstArgument(args[0]) {
		title, titleGiven, args = args[0], true, args[1:]
	}
	flags := newFlagSet("relic", "create")
	relicType := flags.String("type", "", "relic type, such as spec or plan")
	fromFile := flags.String("from-file", "", "create from a whole relic in one JSON document (- reads standard input)")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with the tasks remote (origin unless configured)")
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
		// Without this the refusal is the type rule's, about a type "" that
		// nobody gave.
		if input.Type == "" && !typeGiven {
			return core.Errorf(core.CategoryValidation,
				"the relic document has no type; add a \"type\" member to it, or pass --type <type>, such as spec or plan")
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

	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	result, err := session.mutateRelic(ctx, "", func(ctx context.Context, service core.RelicService) (core.RelicMutationResult, error) {
		return service.Create(ctx, input)
	})
	return writeRelicMutationOutcome(stdout, stderr, "relic create", session, result, err, *jsonMode)
}

// maxRelicDocumentBytes is the largest workbook.relic document relic create
// --from-file reads. It is an input limit, not part of the stored format. It
// holds the export of a relic of ordinary text at the stored limits with room
// to spare:
//
//	16 MiB of live bodies, which a document may carry as base64  22.4 MiB
//	2,000 live parts with 16 KiB of fields each                 31.3 MiB
//	64 KiB of properties                                         0.1 MiB
//
// which is under 54 MiB before the document's own punctuation, its links and
// any indentation. Stored values are measured with their escapes, so the
// fields and properties cannot grow on the way out. A text body can: it is a
// JSON string, where relic export writes '<', '>' and '&' as themselves (see
// writeRelicExport) but a quote, a backslash, a tab or a line break takes two
// bytes, every other control character six, and U+2028 or U+2029 six for its
// three. A relic whose bodies are mostly such characters, or one over a total
// after two clones merged, can export to a document over this limit, which
// --from-file then refuses with exit 5 like any other.
const maxRelicDocumentBytes = 64 * 1024 * 1024

// readRelicDocument reads and decodes the workbook.relic document at path, or
// on standard input for "-". It reads at most one byte more than
// maxRelicDocumentBytes, which is enough to know a document is too large, so
// input that never ends is refused after a bounded read.
func readRelicDocument(path string, stdin io.Reader) (core.RelicDocument, error) {
	source, reader := "standard input", stdin
	if path != "-" {
		source = singleLine(path)
		file, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			return core.RelicDocument{}, core.Wrap(core.CategoryNotFound, "cannot read "+source, err)
		}
		if err != nil {
			return core.RelicDocument{}, core.Wrap(core.CategoryOperational, "cannot read "+source, err)
		}
		defer file.Close()
		reader = file
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxRelicDocumentBytes+1))
	if err != nil {
		if path == "-" {
			return core.RelicDocument{}, core.Wrap(core.CategoryOperational, "cannot read the relic document from standard input", err)
		}
		return core.RelicDocument{}, core.Wrap(core.CategoryOperational, "cannot read "+source, err)
	}
	if len(content) > maxRelicDocumentBytes {
		return core.RelicDocument{}, core.Errorf(core.CategoryValidation,
			"the relic document from %s is more than %d bytes; the limit is %d bytes",
			source, maxRelicDocumentBytes, maxRelicDocumentBytes)
	}
	return core.DecodeRelicDocument(content)
}

func runRelicList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("relic", "list")
	relicType := flags.String("type", "", "show only relics of this type")
	all := flags.Bool("all", false, "include deleted relics, each marked deleted")
	linkedTo := flags.String("linked-to", "", "show only relics that link to this task")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	service, release, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	defer release()
	filter := core.RelicListFilter{Type: *relicType, IncludeDeleted: *all}
	if flagGiven(flags, "linked-to") {
		return runRelicListLinkedTo(ctx, service, *linkedTo, filter, *jsonMode, stdout, stderr)
	}
	summaries, err := service.List(ctx, filter)
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
	history := flags.Bool("history", false, "list this relic's changes, or one part's")
	limit := flags.String("limit", "", "show this many recent changes")
	all := flags.Bool("all", false, "show every change")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	address := values[0]
	window, err := relicHistoryRequest(flags, *history, *bodies, *removed, *all, *limit)
	if err != nil {
		return err
	}
	if *history {
		return runRelicHistory(ctx, address, cwd, window, *jsonMode, stdout, stderr)
	}
	service, release, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	defer release()
	document, err := service.Show(ctx, address, core.RelicShowOptions{Bodies: *bodies, Removed: *removed})
	if err != nil {
		return err
	}
	warnings := make([]core.Warning, 0, len(document.Warnings))
	for _, warning := range document.Warnings {
		warnings = append(warnings, core.Warning{Code: warning.Code, Message: singleLine(warning.Message)})
	}
	if *jsonMode {
		writeResultWithWarnings(stdout, "relic show", document, warnings)
		return nil
	}
	defer writeWarnings(stderr, warnings)
	if _, _, addressesPart, _ := core.ParsePartAddress(address); addressesPart && *bodies {
		return writeRelicPartBody(stdout, document, writerIsTerminal(stdout))
	}
	writeRelicDocument(stdout, document, *bodies)
	return nil
}

// writeRelicPartBody writes the one part's body, and nothing else.
//
// Into a file or a pipe it writes the body exactly as stored, which is what a
// redirect needs. On a terminal it writes what the whole relic's view writes
// of the same body: each line through singleLine, so nothing the body holds
// can move the cursor, color the screen or retitle the window. A body is
// written by whoever can change the relic, and this is the command a person
// is told to run to read one. A body that is not UTF-8 text is not written to
// a terminal at all.
func writeRelicPartBody(stdout io.Writer, document core.RelicDocument, terminal bool) error {
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
	if !terminal {
		_, err := stdout.Write(content)
		return err
	}
	if part.Body.Text == nil {
		_, err := fmt.Fprintf(stdout, "The body of part %s is not text (%d bytes), so it is not shown on a terminal; send the output to a file or a pipe to write its bytes.\n",
			part.ID, len(content))
		return err
	}
	if len(content) == 0 {
		return nil
	}
	shown := core.DisplayText(string(content))
	if !strings.HasSuffix(shown, "\n") {
		shown += "\n"
	}
	_, err := io.WriteString(stdout, shown)
	return err
}

func runRelicSetDeleted(ctx context.Context, verb string, args []string, cwd string, stdout, stderr io.Writer) error {
	command := "relic " + verb
	values, args, err := requiredArguments(command, []string{"<relic>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("relic", verb)
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with the tasks remote (origin unless configured)")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	result, err := session.mutateRelic(ctx, values[0], func(ctx context.Context, service core.RelicService) (core.RelicMutationResult, error) {
		if verb == "restore" {
			return service.Restore(ctx, values[0])
		}
		return service.Delete(ctx, values[0])
	})
	return writeRelicMutationOutcome(stdout, stderr, command, session, result, err, *jsonMode)
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
	service, release, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	defer release()
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
