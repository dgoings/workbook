package cli

import (
	"io"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// completionShells lists the shells `workbook completion` generates scripts
// for, in the order its help and its errors name them.
var completionShells = []string{"bash", "zsh", "fish"}

// completionOption is one option a shell may complete, reduced to what a
// completion script needs: the name to offer, how many words its value takes,
// and one line to describe it with.
type completionOption struct {
	Name string
	// Values is how many words follow the option, so a script knows not to
	// complete them. It is 2 for a pair option such as `--compare`.
	Values      int
	Description string
}

// completionCommand is one word a shell may complete, with the words and the
// options that may follow it. Subcommands are one level deep, as the CLI is.
type completionCommand struct {
	Name        string
	Description string
	Options     []completionOption
	Subcommands []completionCommand
}

// completionPath pairs a command with the words that reach it — `status`, or
// `status add` — which is the key every generated script switches on.
type completionPath struct {
	Path    string
	Command completionCommand
}

func runCompletion(args []string, stdout io.Writer) error {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return core.Errorf(core.CategoryInvocation, "completion requires a shell: %s", completionShellList())
	}
	shell := args[0]
	// Nothing follows the shell, but parsing the rest still reports a stray
	// option the way every other command does rather than ignoring it.
	if err := parseFlags(newFlagSet("completion"), args[1:]); err != nil {
		return err
	}
	script, err := completionScript(shell)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, script)
	return err
}

// completionShellList names the supported shells the way prose does, so one
// error message reads as a sentence.
func completionShellList() string {
	if len(completionShells) < 2 {
		return strings.Join(completionShells, "")
	}
	leading := completionShells[:len(completionShells)-1]
	return strings.Join(leading, ", ") + " or " + completionShells[len(completionShells)-1]
}

func completionScript(shell string) (string, error) {
	commands := completionModel()
	switch shell {
	case "bash":
		return bashCompletionScript(commands), nil
	case "zsh":
		return zshCompletionScript(commands), nil
	case "fish":
		return fishCompletionScript(commands), nil
	default:
		return "", core.Errorf(core.CategoryInvocation, "unknown shell %q: completion supports %s", shell, completionShellList())
	}
}

// completionModel is the whole completable command surface, read from the same
// schema `workbook help` is rendered from. Generating the scripts from it is
// the point of the command: a verb or an option added to the schema is
// completable the moment it is declared, and no hand-written list can go stale.
func completionModel() []completionCommand {
	commands := make([]completionCommand, 0, len(commandOrder)+1)
	for _, name := range commandOrder {
		commands = append(commands, completionCommandFor(commandSchemas[name]))
	}
	return append(commands, completionHelpCommand())
}

func completionCommandFor(metadata commandMetadata) completionCommand {
	command := completionCommand{
		Name:        metadata.Name,
		Description: summarizeDescription(metadata.Description),
		Options:     completionOptionsFor(metadata),
	}
	for _, name := range metadata.SubcommandOrder {
		subcommand, exists := metadata.Subcommands[name]
		if !exists {
			continue
		}
		command.Subcommands = append(command.Subcommands, completionCommand{
			Name:        name,
			Description: summarizeDescription(subcommand.Description),
			Options:     completionOptionsFor(subcommand),
		})
	}
	// `completion` has no subcommands, but its one positional is a closed set
	// the schema already carries, and a command that could not complete its own
	// shells would be a poor advertisement. Every other positional is a task
	// ID, a status or a title that only the repository could supply, and is
	// deliberately left alone rather than completed as a file name.
	if metadata.Name == "completion" {
		command.Subcommands = completionShellWords()
	}
	return command
}

func completionOptionsFor(metadata commandMetadata) []completionOption {
	options := make([]completionOption, 0, len(metadata.Options)+1)
	for _, option := range metadata.Options {
		options = append(options, completionOption{
			Name:        option.Name,
			Values:      optionValueCount(option.Kind),
			Description: summarizeDescription(option.Description),
		})
	}
	// Every command answers --help. renderCommandHelp documents it for all of
	// them rather than repeating it in each schema entry, so completion adds it
	// here for the same reason.
	return append(options, completionOption{Name: "help", Description: "show help"})
}

// completionHelpCommand models `workbook help [command]`. The help renderer
// answers it rather than commandSchemas, so the schema walk cannot produce it,
// but it is in the global help every user reads and completion offers it too.
// Its argument is a command name, which is why the commands are its words.
func completionHelpCommand() completionCommand {
	help := completionCommand{Name: "help", Description: "Show help for a command."}
	for _, name := range commandOrder {
		metadata := commandSchemas[name]
		help.Subcommands = append(help.Subcommands, completionCommand{
			Name:        name,
			Description: summarizeDescription(metadata.Description),
		})
	}
	return help
}

func completionShellWords() []completionCommand {
	words := make([]completionCommand, 0, len(completionShells))
	for _, shell := range completionShells {
		words = append(words, completionCommand{
			Name:        shell,
			Description: "print the " + shell + " completion script",
		})
	}
	return words
}

// completionPaths flattens the model into every completable command path, in
// the order help presents them: a command, then each of its subcommands.
func completionPaths(commands []completionCommand) []completionPath {
	paths := make([]completionPath, 0, len(commands)*2)
	for _, command := range commands {
		paths = append(paths, completionPath{Path: command.Name, Command: command})
		for _, subcommand := range command.Subcommands {
			paths = append(paths, completionPath{Path: command.Name + " " + subcommand.Name, Command: subcommand})
		}
	}
	return paths
}

// summarizeDescription reduces a schema description to the one line a shell can
// show beside a candidate: the first sentence of its first paragraph, unwrapped.
// Schema descriptions are wrapped prose of several paragraphs, so neither the
// first line nor the whole text is usable as it stands.
func summarizeDescription(description string) string {
	paragraph := description
	if end := strings.Index(paragraph, "\n\n"); end >= 0 {
		paragraph = paragraph[:end]
	}
	paragraph = strings.Join(strings.Fields(paragraph), " ")
	if end := strings.Index(paragraph, ". "); end >= 0 {
		paragraph = paragraph[:end+1]
	}
	return paragraph
}

// optionNames lists a command's options as a shell completes them.
func optionNames(options []completionOption) []string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		names = append(names, "--"+option.Name)
	}
	return names
}

// optionNamesTakingValues lists the options whose value follows as separate
// words, optionally restricted to the pair options that take two of them.
func optionNamesTakingValues(options []completionOption, values int) []string {
	var names []string
	for _, option := range options {
		if option.Values == 0 || (values > 0 && option.Values != values) {
			continue
		}
		names = append(names, "--"+option.Name)
	}
	return names
}

func commandNames(commands []completionCommand) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	return names
}

// posixQuote wraps a string for bash and zsh, where a single-quoted string ends
// at its first quote and nothing else inside is special.
func posixQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// fishQuote wraps a string for fish, where a single-quoted string takes both a
// quote and a backslash as escapes.
func fishQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return "'" + strings.ReplaceAll(value, "'", `\'`) + "'"
}

// generatedBanner heads every script with where it came from, so a copy found
// in a dotfile repository says how to regenerate it.
func generatedBanner(shell string) string {
	return "# " + shell + " completion for workbook, generated by `workbook completion " + shell + "`.\n" +
		"#\n" +
		"# Every candidate below is generated from the command schema `workbook help`\n" +
		"# is rendered from, so the two cannot drift apart. Rerun the command above\n" +
		"# after upgrading Workbook.\n"
}

func bashCompletionScript(commands []completionCommand) string {
	var script strings.Builder
	script.WriteString(generatedBanner("bash"))
	script.WriteString(`
_workbook_contains() {
	local needle=$1 item
	shift
	for item in "$@"; do
		if [ "$item" = "$needle" ]; then
			return 0
		fi
	done
	return 1
}

# The top-level commands.
_workbook_commands() {
	echo `)
	script.WriteString(posixQuote(strings.Join(commandNames(commands), " ")))
	script.WriteString(`
}

# The words that may follow a command: its subcommands, or the closed set its
# one positional accepts.
_workbook_arguments() {
	case "$1" in
`)
	for _, entry := range completionPaths(commands) {
		if strings.Contains(entry.Path, " ") || len(entry.Command.Subcommands) == 0 {
			continue
		}
		script.WriteString("\t" + posixQuote(entry.Path) + ") echo " +
			posixQuote(strings.Join(commandNames(entry.Command.Subcommands), " ")) + " ;;\n")
	}
	script.WriteString(`	esac
}

# The options a command path accepts.
_workbook_options() {
	case "$1" in
`)
	for _, entry := range completionPaths(commands) {
		if len(entry.Command.Options) == 0 {
			continue
		}
		script.WriteString("\t" + posixQuote(entry.Path) + ") echo " +
			posixQuote(strings.Join(optionNames(entry.Command.Options), " ")) + " ;;\n")
	}
	script.WriteString(`	esac
}

# The options that take a value. Nothing is completed for one: Workbook's
# values are task IDs, statuses, labels and durations this script has no static
# set for, and the file names bash would otherwise guess are wrong for nearly
# all of them.
_workbook_value_options() {
	case "$1" in
`)
	script.WriteString(bashOptionCases(commands, 0))
	script.WriteString(`	esac
}

# The options that take two values, so the second one is recognized too.
_workbook_pair_options() {
	case "$1" in
`)
	script.WriteString(bashOptionCases(commands, 2))
	script.WriteString(`	esac
}

_workbook() {
	local current previous command path candidates
	COMPREPLY=()
	current=${COMP_WORDS[COMP_CWORD]}

	if [ "$COMP_CWORD" -eq 1 ]; then
		COMPREPLY=($(compgen -W "$(_workbook_commands)" -- "$current"))
		return 0
	fi

	# A subcommand is always the word right after the command. Requiring it to
	# be one the command actually has keeps a positional — a task ID, say —
	# from being read as one, which would lose the command's own options.
	command=${COMP_WORDS[1]}
	path=$command
	if [ "$COMP_CWORD" -gt 2 ] && _workbook_contains "${COMP_WORDS[2]}" $(_workbook_arguments "$command"); then
		path="$command ${COMP_WORDS[2]}"
	fi

	previous=${COMP_WORDS[COMP_CWORD - 1]}
	if _workbook_contains "$previous" $(_workbook_value_options "$path"); then
		return 0
	fi
	if [ "$COMP_CWORD" -gt 2 ] && _workbook_contains "${COMP_WORDS[COMP_CWORD - 2]}" $(_workbook_pair_options "$path"); then
		return 0
	fi

	candidates=$(_workbook_options "$path")
	if [ "$COMP_CWORD" -eq 2 ]; then
		candidates="$(_workbook_arguments "$command") $candidates"
	fi
	COMPREPLY=($(compgen -W "$candidates" -- "$current"))
	return 0
}

# Registered with no fallback option of any kind, so an empty reply offers
# nothing rather than the contents of the working directory.
complete -F _workbook workbook
`)
	return script.String()
}

// bashOptionCases renders one case branch per command path that has options
// taking values, for the given number of values, or every count when 0.
func bashOptionCases(commands []completionCommand, values int) string {
	var cases strings.Builder
	for _, entry := range completionPaths(commands) {
		names := optionNamesTakingValues(entry.Command.Options, values)
		if len(names) == 0 {
			continue
		}
		cases.WriteString("\t" + posixQuote(entry.Path) + ") echo " + posixQuote(strings.Join(names, " ")) + " ;;\n")
	}
	return cases.String()
}

func zshCompletionScript(commands []completionCommand) string {
	var script strings.Builder
	script.WriteString("#compdef workbook\n\n")
	script.WriteString(generatedBanner("zsh"))
	script.WriteString(`
# The top-level commands.
_workbook_set_commands() {
	typeset -ga _workbook_command_words
	_workbook_command_words=`)
	script.WriteString(zshDescribedWords(commands))
	script.WriteString(`
}

# The words that may follow a command: its subcommands, or the closed set its
# one positional accepts.
_workbook_set_arguments() {
	typeset -ga _workbook_argument_words
	case "$1" in
`)
	for _, entry := range completionPaths(commands) {
		if strings.Contains(entry.Path, " ") || len(entry.Command.Subcommands) == 0 {
			continue
		}
		script.WriteString("\t" + posixQuote(entry.Path) + ") _workbook_argument_words=" +
			zshDescribedWords(entry.Command.Subcommands) + " ;;\n")
	}
	script.WriteString(`	*) _workbook_argument_words=() ;;
	esac
}

# The options a command path accepts.
_workbook_set_options() {
	typeset -ga _workbook_option_words
	case "$1" in
`)
	for _, entry := range completionPaths(commands) {
		if len(entry.Command.Options) == 0 {
			continue
		}
		script.WriteString("\t" + posixQuote(entry.Path) + ") _workbook_option_words=" +
			zshDescribedOptions(entry.Command.Options) + " ;;\n")
	}
	script.WriteString(`	*) _workbook_option_words=() ;;
	esac
}

# The options that take a value. Nothing is completed for one: Workbook's
# values are task IDs, statuses, labels and durations this script has no static
# set for, and the file names zsh would otherwise offer are wrong for nearly
# all of them.
_workbook_takes_value() {
	local -a options
	case "$1" in
`)
	script.WriteString(zshOptionCases(commands, 0))
	script.WriteString(`	esac
	(( ${options[(Ie)$2]} ))
}

# The options that take two values, so the second one is recognized too.
_workbook_takes_pair() {
	local -a options
	case "$1" in
`)
	script.WriteString(zshOptionCases(commands, 2))
	script.WriteString(`	esac
	(( ${options[(Ie)$2]} ))
}

_workbook() {
	local command path previous
	local -a names

	if (( CURRENT == 2 )); then
		_workbook_set_commands
		_describe -t commands 'workbook command' _workbook_command_words
		return
	fi

	# A subcommand is always the word right after the command. Requiring it to
	# be one the command actually has keeps a positional — a task ID, say —
	# from being read as one, which would lose the command's own options.
	command=${words[2]}
	path=$command
	_workbook_set_arguments "$command"
	names=( ${_workbook_argument_words%%:*} )
	if (( CURRENT > 3 )) && (( ${names[(Ie)${words[3]}]} )); then
		path="$command ${words[3]}"
	fi

	previous=${words[CURRENT-1]}
	if _workbook_takes_value "$path" "$previous"; then
		return
	fi
	if (( CURRENT > 3 )) && _workbook_takes_pair "$path" "${words[CURRENT-2]}"; then
		return
	fi

	if (( CURRENT == 3 )) && (( ${#_workbook_argument_words} )); then
		_describe -t arguments "$command command" _workbook_argument_words
	fi
	_workbook_set_options "$path"
	if (( ${#_workbook_option_words} )); then
		_describe -t options option _workbook_option_words
	fi
}

# Sourced from a shell startup file, the script registers itself; dropped into
# fpath as _workbook, zsh autoloads it and calls it instead.
if [[ ${zsh_eval_context[-1]} == loadautofunc ]]; then
	_workbook "$@"
elif (( $+functions[compdef] )); then
	compdef _workbook workbook
fi
`)
	return script.String()
}

// zshDescribedWords renders candidates as zsh's `word:description` pairs, one
// per line: a description is a whole sentence, so a single line per array would
// run to several hundred columns.
func zshDescribedWords(commands []completionCommand) string {
	words := make([]string, 0, len(commands))
	for _, command := range commands {
		words = append(words, posixQuote(command.Name+":"+command.Description))
	}
	return zshWordList(words)
}

func zshDescribedOptions(options []completionOption) string {
	words := make([]string, 0, len(options))
	for _, option := range options {
		words = append(words, posixQuote("--"+option.Name+":"+option.Description))
	}
	return zshWordList(words)
}

func zshWordList(words []string) string {
	return "(\n\t\t" + strings.Join(words, "\n\t\t") + "\n\t)"
}

func zshOptionCases(commands []completionCommand, values int) string {
	var cases strings.Builder
	for _, entry := range completionPaths(commands) {
		names := optionNamesTakingValues(entry.Command.Options, values)
		if len(names) == 0 {
			continue
		}
		quoted := make([]string, 0, len(names))
		for _, name := range names {
			quoted = append(quoted, posixQuote(name))
		}
		cases.WriteString("\t" + posixQuote(entry.Path) + ") options=( " + strings.Join(quoted, " ") + " ) ;;\n")
	}
	return cases.String()
}

func fishCompletionScript(commands []completionCommand) string {
	var script strings.Builder
	script.WriteString(generatedBanner("fish"))
	script.WriteString(`
# Files are never offered, after a command or for an option's value. Nearly
# every Workbook argument is a task ID, a status or a title, so a directory
# listing would be the wrong answer almost every time it was given.
complete -c workbook -f

function __fish_workbook_path --description 'Print the workbook command path typed before the cursor'
	# Only these commands take a second word. Anything else following a command
	# is a positional — a task ID, a status, a title — that is not completed.
	set -l nested`)
	for _, command := range commands {
		if len(command.Subcommands) > 0 {
			script.WriteString(" " + command.Name)
		}
	}
	script.WriteString(`
	# --tokenize rather than --tokens-expanded, which fish 3 does not have.
	set -l tokens (commandline --current-process --tokenize --cut-at-cursor)
	set -q tokens[1]; and set -e tokens[1]
	set -l command
	for token in $tokens
		if string match -q -- '-*' $token
			continue
		end
		if test -z "$command"
			set command $token
			if not contains -- $command $nested
				break
			end
		else
			echo $command $token
			return
		end
	end
	test -n "$command"; and echo $command
end

function __fish_workbook_at --description 'True when the command path typed so far is exactly its arguments'
	set -l path (__fish_workbook_path)
	test "$argv" = "$path"
end

`)
	for _, entry := range completionPaths(commands) {
		if strings.Contains(entry.Path, " ") {
			continue
		}
		script.WriteString("complete -c workbook -n __fish_workbook_at -a " +
			fishQuote(entry.Command.Name) + " -d " + fishQuote(entry.Command.Description) + "\n")
	}
	for _, entry := range completionPaths(commands) {
		condition := fishQuote("__fish_workbook_at " + entry.Path)
		if entry.Path == entry.Command.Name {
			for _, subcommand := range entry.Command.Subcommands {
				script.WriteString("complete -c workbook -n " + condition + " -a " +
					fishQuote(subcommand.Name) + " -d " + fishQuote(subcommand.Description) + "\n")
			}
		}
		for _, option := range entry.Command.Options {
			line := "complete -c workbook -n " + condition + " -l " + fishQuote(option.Name)
			if option.Values > 0 {
				// -r takes a value and -f keeps fish from offering files for it.
				line += " -r -f"
			}
			script.WriteString(line + " -d " + fishQuote(option.Description) + "\n")
		}
	}
	return script.String()
}
