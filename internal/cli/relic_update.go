package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dgoings/workbook/internal/core"
)

// relicUpdateRequest is what a relic update command line asks for.
type relicUpdateRequest struct {
	Relic string
	Edits []core.RelicEdit
	JSON  bool
	// PartRelics holds the relic side of each --part given as <relic>#<part>.
	// Either side may be a prefix, so only a look at the repository can say
	// whether one names the relic being changed; the command checks.
	PartRelics []string
}

// relicUpdateGroups holds, for each option that starts a group, the edit the
// group becomes and the part options that may follow it. Every other option
// with a value belongs to the relic and may appear anywhere.
var relicUpdateGroups = map[string]struct {
	kind  core.RelicEditKind
	takes []string
}{
	"add-part":     {core.RelicEditAddPart, []string{"part-type", "field", "field-json", "body-file", "body-format", "before", "after"}},
	"part":         {core.RelicEditPart, []string{"field", "field-json", "clear-field", "body-file", "body-format", "base"}},
	"move-part":    {core.RelicEditMovePart, []string{"before", "after"}},
	"remove-part":  {core.RelicEditRemovePart, nil},
	"restore-part": {core.RelicEditRestorePart, nil},
}

// relicUpdateGroupOrder lists the group options in the order help does, for
// a message that names the ones a part option may follow.
var relicUpdateGroupOrder = []string{"add-part", "part", "move-part", "remove-part", "restore-part"}

// relicUpdateEchoLimit is the longest thing typed that a refusal repeats. It
// is also longer than any ID: the longest thing that names a relic, a part or
// a body version is a part address, a relic ID and a part ID around a '#'.
const relicUpdateEchoLimit = 100

// relicBodyFilePathLimit is the longest --body-file path that is looked at.
// No system this runs on opens a longer one.
const relicBodyFilePathLimit = 4096

// relicUpdateChange is one change as it was typed, before any value in it is
// judged or any file read: an option of the relic, or the option that starts
// a group together with the part options given after it.
type relicUpdateChange struct {
	option string // title, set, set-json, clear, or the option that starts a group
	name   string // the property, or the part the group is about
	value  string // the title, or the property's value as typed
	// fields holds a group's --field, --field-json and --clear-field options
	// in the order given. once holds its other options, each of which a group
	// takes one of.
	fields []relicUpdateField
	once   map[string]string
}

type relicUpdateField struct{ option, name, value string }

// parseRelicUpdateArgs keeps the order of the arguments, which flag.FlagSet
// does not: --add-part, --part, --move-part, --remove-part and --restore-part
// each start a group that the part options after it belong to, and every
// change becomes one edit, in the order given.
//
// A command line that is put together wrongly is refused as an invocation
// error before any value on it is judged and before readFile is called, so
// exit 2 always means nothing was looked at.
func parseRelicUpdateArgs(args []string, readFile func(string) ([]byte, error)) (relicUpdateRequest, error) {
	values, args, err := requiredArguments("relic update", []string{"<relic>"}, args)
	if err != nil {
		return relicUpdateRequest{}, err
	}
	changes, jsonMode, err := splitRelicUpdateArgs(args)
	if err != nil {
		return relicUpdateRequest{}, err
	}
	if len(changes) == 0 {
		return relicUpdateRequest{}, core.Errorf(core.CategoryInvocation, "relic update needs at least one change")
	}
	for _, change := range changes {
		if err := change.check(); err != nil {
			return relicUpdateRequest{}, err
		}
	}

	request := relicUpdateRequest{Relic: values[0], JSON: jsonMode}
	if err := checkRelicUpdateID("<relic>", request.Relic); err != nil {
		return relicUpdateRequest{}, err
	}
	for _, change := range changes {
		edit, partRelic, err := change.edit(readFile)
		if err != nil {
			return relicUpdateRequest{}, err
		}
		request.Edits = append(request.Edits, edit)
		if partRelic != "" {
			request.PartRelics = append(request.PartRelics, partRelic)
		}
	}
	return request, nil
}

// splitRelicUpdateArgs walks the arguments after the relic once, sorting each
// option into the change it belongs to. The schema says which options take a
// value; one that does takes the next argument whatever it looks like, as the
// flag package has it.
func splitRelicUpdateArgs(args []string) (changes []relicUpdateChange, jsonMode bool, err error) {
	schema, exists := commandMetadataFor([]string{"relic", "update"})
	if !exists {
		panic("missing flag schema for relic update")
	}
	group := -1 // the index in changes of the group the part options now belong to
	titled := false
	for index := 0; index < len(args); index++ {
		name, value, hasValue, isFlag := splitFlag(args[index])
		if !isFlag {
			// A bare -- ends the options, and relic update takes nothing after them.
			if args[index] == "--" && index == len(args)-1 {
				break
			}
			return nil, false, core.Errorf(core.CategoryInvocation, "relic update accepts no additional positional arguments")
		}
		kind, known := schema.optionKind(name)
		if !known {
			return nil, false, core.Errorf(core.CategoryInvocation, "relic update has no option %s", relicUpdateTyped("--"+name))
		}

		if kind == boolFlag {
			if name == "json" {
				jsonMode = true
				if hasValue {
					if jsonMode, err = strconv.ParseBool(value); err != nil {
						return nil, false, core.Errorf(core.CategoryInvocation, "relic update --json=<value> takes true or false")
					}
				}
				continue
			}
			// --add-part. A value on it is most likely the type, put where
			// none goes.
			if hasValue {
				return nil, false, core.Errorf(core.CategoryInvocation,
					"relic update --add-part takes no value; give the part's type with --part-type <type>")
			}
			changes = append(changes, relicUpdateChange{option: name})
			group = len(changes) - 1
			continue
		}

		if !hasValue {
			if index+1 >= len(args) {
				return nil, false, core.Errorf(core.CategoryInvocation, "relic update --%s requires a value", name)
			}
			index++
			value = args[index]
		}
		switch name {
		case "title":
			if titled {
				return nil, false, core.Errorf(core.CategoryInvocation, "relic update accepts --title once")
			}
			titled = true
			changes = append(changes, relicUpdateChange{option: name, value: value})
		case "set", "set-json":
			property, text, err := splitRelicNameValue(name, value)
			if err != nil {
				return nil, false, err
			}
			changes = append(changes, relicUpdateChange{option: name, name: property, value: text})
		case "clear":
			changes = append(changes, relicUpdateChange{option: name, name: value})
		default:
			if _, starts := relicUpdateGroups[name]; starts {
				changes = append(changes, relicUpdateChange{option: name, name: value})
				group = len(changes) - 1
				continue
			}
			if group < 0 {
				return nil, false, core.Errorf(core.CategoryInvocation,
					"relic update --%s applies to one part; give it after %s", name, relicUpdateGroupsTaking(name))
			}
			if err := changes[group].add(name, value); err != nil {
				return nil, false, err
			}
		}
	}
	return changes, jsonMode, nil
}

// relicUpdateGroupsTaking names the group options a part option may follow,
// as "--add-part or --part".
func relicUpdateGroupsTaking(option string) string {
	var groups []string
	for _, group := range relicUpdateGroupOrder {
		if slices.Contains(relicUpdateGroups[group].takes, option) {
			groups = append(groups, "--"+group)
		}
	}
	return strings.Join(groups, " or ")
}

// splitRelicNameValue splits what --set, --set-json, --field and --field-json
// take at its first '=', so a value may hold more of them.
func splitRelicNameValue(option, typed string) (name, value string, err error) {
	name, value, found := strings.Cut(typed, "=")
	if !found {
		form := "<name>=<value>"
		if strings.HasSuffix(option, "-json") {
			form = "<name>=<json>"
		}
		return "", "", core.Errorf(core.CategoryInvocation, "relic update --%s takes %s", option, form)
	}
	return name, value, nil
}

// add gives a group one of the part options typed after it.
func (change *relicUpdateChange) add(option, value string) error {
	if !slices.Contains(relicUpdateGroups[change.option].takes, option) {
		return core.Errorf(core.CategoryInvocation, "relic update --%s does not apply to --%s; give it after %s",
			option, change.option, relicUpdateGroupsTaking(option))
	}
	switch option {
	case "field", "field-json":
		name, text, err := splitRelicNameValue(option, value)
		if err != nil {
			return err
		}
		change.fields = append(change.fields, relicUpdateField{option: option, name: name, value: text})
		return nil
	case "clear-field":
		change.fields = append(change.fields, relicUpdateField{option: option, name: value})
		return nil
	}
	// Each of the rest says one thing about the part. A second one is far more
	// likely a group that was not started than a wish to have the first ignored.
	if _, given := change.once[option]; given {
		return core.Errorf(core.CategoryInvocation,
			"relic update accepts --%s once after each --%s; start another group for another part", option, change.option)
	}
	if change.once == nil {
		change.once = map[string]string{}
	}
	change.once[option] = value
	return nil
}

// check refuses a group that is missing something or holds an option that
// needs another. The service ignores what does not apply to an edit's kind and
// reads an empty anchor as none, so these are refused here or not at all.
func (change relicUpdateChange) check() error {
	file, hasFile := change.once["body-file"]
	for _, option := range []string{"body-format", "base"} {
		if _, given := change.once[option]; given && !hasFile {
			return core.Errorf(core.CategoryInvocation,
				"relic update --%s requires --body-file after the same --%s", option, change.option)
		}
	}
	switch {
	case hasFile && file == "":
		return core.Errorf(core.CategoryInvocation, "relic update --body-file requires a file")
	case hasFile && file == "-":
		return core.Errorf(core.CategoryInvocation, "relic update --body-file takes a file; it does not read standard input")
	}
	for _, option := range []string{"before", "after"} {
		if anchor, given := change.once[option]; given && anchor == "" {
			return core.Errorf(core.CategoryInvocation, "relic update --%s requires a part", option)
		}
	}

	_, hasBefore := change.once["before"]
	_, hasAfter := change.once["after"]
	switch change.option {
	case "add-part":
		if _, given := change.once["part-type"]; !given {
			return core.Errorf(core.CategoryInvocation, "relic update --add-part requires --part-type <type>")
		}
	case "part":
		if len(change.fields) == 0 && !hasFile {
			return core.Errorf(core.CategoryInvocation,
				"relic update --part needs a change after it: --field, --field-json, --clear-field or --body-file")
		}
	case "move-part":
		// Both at once is left to the relic to refuse, as it is for --add-part.
		if !hasBefore && !hasAfter {
			return core.Errorf(core.CategoryInvocation, "relic update --move-part requires --before <part> or --after <part>")
		}
	}
	return nil
}

// edit turns a checked change into the edit the service applies, reading the
// values it holds. partRelic is the relic side of a --part given as
// <relic>#<part>, and empty otherwise.
func (change relicUpdateChange) edit(readFile func(string) ([]byte, error)) (edit core.RelicEdit, partRelic string, err error) {
	switch change.option {
	case "title":
		return core.RelicEdit{Kind: core.RelicEditTitle, Title: change.value}, "", nil
	case "set", "set-json":
		value, err := relicUpdateStoredValue(change.option, change.name, change.value)
		if err != nil {
			return core.RelicEdit{}, "", err
		}
		return core.RelicEdit{Kind: core.RelicEditSetProperty, Name: change.name, Value: value}, "", nil
	case "clear":
		return core.RelicEdit{Kind: core.RelicEditClearProperty, Name: change.name}, "", nil
	}

	if change.option != "add-part" {
		if err := checkRelicUpdateID("--"+change.option, change.name); err != nil {
			return core.RelicEdit{}, "", err
		}
	}
	for _, option := range []string{"base", "before", "after"} {
		if err := checkRelicUpdateID("--"+option, change.once[option]); err != nil {
			return core.RelicEdit{}, "", err
		}
	}
	edit = core.RelicEdit{
		Kind:     relicUpdateGroups[change.option].kind,
		Part:     change.name,
		PartType: change.once["part-type"],
		Base:     change.once["base"],
		Before:   change.once["before"],
		After:    change.once["after"],
	}
	if relic, part, addressed := strings.Cut(change.name, "#"); addressed && change.option == "part" {
		if relic == "" || part == "" || strings.Contains(part, "#") {
			return core.RelicEdit{}, "", core.Errorf(core.CategoryValidation,
				"relic update --part takes a part ID, or a part address written <relic>#<part>")
		}
		edit.Part, partRelic = part, relic
	}
	for _, field := range change.fields {
		fieldEdit := core.RelicFieldEdit{Name: field.name, Clear: field.option == "clear-field"}
		if !fieldEdit.Clear {
			if fieldEdit.Value, err = relicUpdateStoredValue(field.option, field.name, field.value); err != nil {
				return core.RelicEdit{}, "", err
			}
		}
		edit.Fields = append(edit.Fields, fieldEdit)
	}
	if file, given := change.once["body-file"]; given {
		if len(file) > relicBodyFilePathLimit {
			return core.RelicEdit{}, "", core.Errorf(core.CategoryValidation,
				"the --body-file path is %d bytes long; the limit is %d bytes", len(file), relicBodyFilePathLimit)
		}
		// --body-format wins over the file's ending, and an empty one means no
		// label, so a file whose ending is no label can still be saved.
		format, labeled := change.once["body-format"]
		if !labeled {
			if format, err = core.BodyFormatFromFileName(filepath.Base(file)); err != nil {
				return core.RelicEdit{}, "", err
			}
		}
		content, err := readFile(file)
		if err != nil {
			return core.RelicEdit{}, "", err
		}
		edit.Body = &core.RelicBodyInput{Format: format, Content: content}
	}
	return edit, partRelic, nil
}

// relicUpdateStoredValue returns a property or field value in its stored
// form: text as a JSON string for --set and --field, and any JSON value for
// --set-json and --field-json.
func relicUpdateStoredValue(option, name, typed string) (json.RawMessage, error) {
	if strings.HasSuffix(option, "-json") {
		value, err := core.CanonicalRelicValue([]byte(typed))
		if err != nil {
			return nil, core.Errorf(core.CategoryValidation, "--%s %s: %s", option, relicUpdateTyped(name), err.Error())
		}
		return value, nil
	}
	// json.Marshal would store a replacement character in place of each byte
	// that is not UTF-8, which is not the text that was given.
	if !utf8.ValidString(typed) {
		return nil, core.Errorf(core.CategoryValidation, "--%s %s: the value must be valid UTF-8", option, relicUpdateTyped(name))
	}
	value, err := json.Marshal(typed)
	if err != nil {
		return nil, core.Wrap(core.CategoryValidation, "--"+option+" "+relicUpdateTyped(name)+": the value cannot be encoded", err)
	}
	return value, nil
}

// checkRelicUpdateID refuses a relic, part or body version too long to be
// one. It is refused here because the service's refusal of an ID that matches
// nothing repeats what was typed, however long.
func checkRelicUpdateID(what, typed string) error {
	if len(typed) > relicUpdateEchoLimit {
		return core.Errorf(core.CategoryValidation, "the %s given is %d bytes long; no ID is longer than %d bytes",
			what, len(typed), relicUpdateEchoLimit)
	}
	return nil
}

// relicUpdateTyped quotes something typed on the command line for a refusal,
// or gives only its size when it is too long to repeat.
func relicUpdateTyped(text string) string {
	if len(text) > relicUpdateEchoLimit {
		return fmt.Sprintf("(%d bytes long)", len(text))
	}
	return strconv.Quote(text)
}

// readRelicBodyFile reads what --body-file names. Only a regular file is read,
// and one over the body limit is refused from its size alone, for the reasons
// readAttachmentFile gives: the limit core holds a body to is asked of bytes
// already in memory.
func readRelicBodyFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		// Said in full, because only an operational error is shown with its cause.
		return nil, core.Errorf(core.CategoryNotFound, "the body file %s does not exist", singleLine(path))
	}
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot read the body file "+singleLine(path), err)
	}
	if !info.Mode().IsRegular() {
		return nil, core.Errorf(core.CategoryValidation, "the body file %s is %s",
			singleLine(path), describeIrregularFile(info.Mode()))
	}
	if info.Size() > core.MaxRelicBodyBytes {
		return nil, core.Errorf(core.CategoryValidation, "the body file %s is %d bytes; the limit is %d bytes",
			singleLine(path), info.Size(), core.MaxRelicBodyBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot read the body file "+singleLine(path), err)
	}
	return content, nil
}

// checkRelicUpdatePartRelics refuses a --part address whose relic side is not
// the relic the command changes, before anything is written.
func checkRelicUpdatePartRelics(ctx context.Context, service core.RelicService, request relicUpdateRequest) error {
	if len(request.PartRelics) == 0 {
		return nil
	}
	heads, err := service.Reader.ListRelicHeads(ctx, service.Config)
	if err != nil {
		return err
	}
	changed, err := core.ResolveRelicPrefix(heads, request.Relic)
	if err != nil {
		return err
	}
	for _, relic := range request.PartRelics {
		named, err := core.ResolveRelicPrefix(heads, relic)
		if err != nil {
			return err
		}
		if named.RelicID != changed.RelicID {
			return core.Errorf(core.CategoryValidation,
				"--part names a part of relic %s, but this command changes relic %s; one command changes one relic",
				named.RelicID, changed.RelicID)
		}
	}
	return nil
}

func runRelicUpdate(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	request, err := parseRelicUpdateArgs(args, readRelicBodyFile)
	if err != nil {
		return err
	}
	service, err := openRelicService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	if err := checkRelicUpdatePartRelics(ctx, service, request); err != nil {
		return err
	}
	result, err := service.Update(ctx, request.Relic, request.Edits)
	if err != nil {
		return err
	}
	writeRelicMutation(stdout, "relic update", result, request.JSON)
	return nil
}
