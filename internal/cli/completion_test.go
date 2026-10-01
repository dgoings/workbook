package cli

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// completionScriptReader reads back the candidate lists one generated script
// declares. Every expectation below is derived by walking commandSchemas, so a
// command or an option added to the schema fails here until the generator
// offers it, which is the whole reason the scripts are generated rather than
// written by hand.
type completionScriptReader struct {
	shell string
	// arguments returns the words completed after the given command path. The
	// empty path is the top-level command list.
	arguments func(t *testing.T, script, path string) []string
	// options returns the options completed for the given command path.
	options func(t *testing.T, script, path string) []string
	// valueOptions returns the options the script knows take a value.
	valueOptions func(t *testing.T, script, path string) []string
	// pairOptions returns the options the script knows take two values, or nil
	// for a shell with no two-value form of its own.
	pairOptions func(t *testing.T, script, path string) []string
}

func completionScriptReaders() []completionScriptReader {
	return []completionScriptReader{
		{
			shell: "bash",
			arguments: func(t *testing.T, script, path string) []string {
				if path == "" {
					return bashEchoedWords(t, script, "_workbook_commands", "")
				}
				return bashEchoedWords(t, script, "_workbook_arguments", path)
			},
			options: func(t *testing.T, script, path string) []string {
				return bashEchoedWords(t, script, "_workbook_options", path)
			},
			valueOptions: func(t *testing.T, script, path string) []string {
				return bashEchoedWords(t, script, "_workbook_value_options", path)
			},
			pairOptions: func(t *testing.T, script, path string) []string {
				return bashEchoedWords(t, script, "_workbook_pair_options", path)
			},
		},
		{
			shell: "zsh",
			arguments: func(t *testing.T, script, path string) []string {
				if path == "" {
					return zshDescribedNames(t, script, "_workbook_set_commands", "\t_workbook_command_words=(")
				}
				return zshDescribedNames(t, script, "_workbook_set_arguments", "\t'"+path+"') _workbook_argument_words=(")
			},
			options: func(t *testing.T, script, path string) []string {
				return zshDescribedNames(t, script, "_workbook_set_options", "\t'"+path+"') _workbook_option_words=(")
			},
			valueOptions: func(t *testing.T, script, path string) []string {
				return zshCaseOptions(t, script, "_workbook_takes_value", path)
			},
			pairOptions: func(t *testing.T, script, path string) []string {
				return zshCaseOptions(t, script, "_workbook_takes_pair", path)
			},
		},
		{
			shell: "fish",
			arguments: func(t *testing.T, script, path string) []string {
				return fishRuleNames(t, script, path, "-a", false)
			},
			options: func(t *testing.T, script, path string) []string {
				return fishRuleNames(t, script, path, "-l", false)
			},
			valueOptions: func(t *testing.T, script, path string) []string {
				return fishRuleNames(t, script, path, "-l", true)
			},
			// fish has no two-value option form, so a pair option is completed
			// as taking one value and nothing distinguishes it.
			pairOptions: nil,
		},
	}
}

func TestCompletionScriptsOfferEveryCommandSubcommandAndOption(t *testing.T) {
	t.Parallel()
	// Production mutation: dropping `help`, a subcommand, or an option from the
	// generator leaves users completing a command surface that is not the one
	// the CLI has, with no error to tell them which half is wrong.
	for _, reader := range completionScriptReaders() {
		t.Run(reader.shell, func(t *testing.T) {
			script := generateCompletionScript(t, reader.shell)

			wantCommands := append(append([]string(nil), commandOrder...), "help")
			assertSameWords(t, reader.shell+" top-level commands", reader.arguments(t, script, ""), wantCommands)

			for _, name := range commandOrder {
				metadata := commandSchemas[name]
				assertSameWords(t, reader.shell+" "+name+" words",
					reader.arguments(t, script, name), wantCompletionWords(name, metadata))
				assertSameWords(t, reader.shell+" "+name+" options",
					reader.options(t, script, name), wantCompletionOptions(metadata))
				for _, subcommand := range metadata.SubcommandOrder {
					path := name + " " + subcommand
					assertSameWords(t, reader.shell+" "+path+" options",
						reader.options(t, script, path), wantCompletionOptions(metadata.Subcommands[subcommand]))
				}
			}

			// `help` is answered by the help renderer rather than by a schema,
			// so its words are the commands themselves and it carries none of
			// the options a schema entry would have given it.
			assertSameWords(t, reader.shell+" help words", reader.arguments(t, script, "help"), commandOrder)
			if options := reader.options(t, script, "help"); len(options) != 0 {
				t.Errorf("%s help options = %q, want none", reader.shell, options)
			}
		})
	}
}

func TestCompletionScriptsMarkEveryOptionThatTakesValues(t *testing.T) {
	t.Parallel()
	// Production mutation: forgetting that an option takes a value makes the
	// shell complete the value as another option or as a file name, so
	// `workbook show <id> --compare <tab>` offers the working directory.
	for _, reader := range completionScriptReaders() {
		t.Run(reader.shell, func(t *testing.T) {
			script := generateCompletionScript(t, reader.shell)
			for _, path := range completionSchemaPaths() {
				metadata, exists := commandMetadataFor(strings.Fields(path))
				if !exists {
					t.Fatalf("no schema for %q", path)
				}
				assertSameWords(t, reader.shell+" "+path+" value options",
					reader.valueOptions(t, script, path), schemaOptionNames(metadata, stringFlag, pairFlag))
				if reader.pairOptions == nil {
					continue
				}
				assertSameWords(t, reader.shell+" "+path+" pair options",
					reader.pairOptions(t, script, path), schemaOptionNames(metadata, pairFlag))
			}
		})
	}
}

func TestCompletionScriptsNeverFallBackToFileNames(t *testing.T) {
	t.Parallel()
	// Production mutation: letting a shell fall back to file names turns every
	// task ID, status and title into a listing of the working directory, which
	// is never what the argument accepts.
	bash := generateCompletionScript(t, "bash")
	for _, forbidden := range []string{"-o default", "-o filenames", "-o dirnames", "compgen -f", "compgen -A file", "_filedir"} {
		if strings.Contains(bash, forbidden) {
			t.Errorf("bash script offers file names through %q", forbidden)
		}
	}
	if !strings.Contains(bash, "complete -F _workbook workbook\n") {
		t.Error("bash script does not register _workbook for workbook")
	}

	zsh := generateCompletionScript(t, "zsh")
	for _, forbidden := range []string{"_files", "_path_files", "_default"} {
		if strings.Contains(zsh, forbidden) {
			t.Errorf("zsh script offers file names through %q", forbidden)
		}
	}
	if !strings.HasPrefix(zsh, "#compdef workbook\n") {
		t.Error("zsh script does not start with its #compdef line")
	}
	// Sourcing and autoloading are both supported, so the file works from a
	// startup file and from fpath.
	for _, want := range []string{"compdef _workbook workbook", "loadautofunc"} {
		if !strings.Contains(zsh, want) {
			t.Errorf("zsh script is missing %q", want)
		}
	}

	fish := generateCompletionScript(t, "fish")
	if !strings.Contains(fish, "complete -c workbook -f\n") {
		t.Error("fish script does not disable file completion for workbook")
	}
	for _, line := range strings.Split(fish, "\n") {
		if strings.Contains(line, " -r ") && !strings.Contains(line, " -r -f ") {
			t.Errorf("fish rule takes a value without disabling files: %q", line)
		}
	}
}

func TestCompletionScriptDescriptionsAreOneQuotedLine(t *testing.T) {
	t.Parallel()
	// Production mutation: a description carried into a script as the schema
	// wraps it — several lines, with quotes and backticks in them — makes the
	// script a syntax error rather than a completion.
	for _, name := range commandOrder {
		metadata := commandSchemas[name]
		assertOneLineSummary(t, name, metadata.Description)
		for _, option := range metadata.Options {
			assertOneLineSummary(t, name+" --"+option.Name, option.Description)
		}
		for _, subcommand := range metadata.SubcommandOrder {
			child := metadata.Subcommands[subcommand]
			assertOneLineSummary(t, name+" "+subcommand, child.Description)
			for _, option := range child.Options {
				assertOneLineSummary(t, name+" "+subcommand+" --"+option.Name, option.Description)
			}
		}
	}
}

func TestCompletionQuotingEscapesWhatEachShellTreatsAsSpecial(t *testing.T) {
	t.Parallel()
	// Production mutation: a single quote reaching a script unescaped ends the
	// string it was in and the rest of the line becomes code.
	if got, want := posixQuote(`this project's board`), `'this project'\''s board'`; got != want {
		t.Errorf("posixQuote() = %q, want %q", got, want)
	}
	if got, want := posixQuote(`a \ and a `+"`backtick`"), "'a \\ and a `backtick`'"; got != want {
		t.Errorf("posixQuote() = %q, want %q", got, want)
	}
	if got, want := fishQuote(`this project's board`), `'this project\'s board'`; got != want {
		t.Errorf("fishQuote() = %q, want %q", got, want)
	}
	// fish takes a backslash as an escape inside single quotes, where bash and
	// zsh do not, so it is the one shell that has to double them.
	if got, want := fishQuote(`a \ and a `+"`backtick`"), "'a \\\\ and a `backtick`'"; got != want {
		t.Errorf("fishQuote() = %q, want %q", got, want)
	}
}

func TestCompletionWritesOnlyTheScriptAndTouchesNoRepository(t *testing.T) {
	t.Parallel()
	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			repository := testrepo.New(t)
			code, stdout, stderr := run(t, repository, "completion", shell)
			if code != 0 {
				t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr)
			}
			if stderr != "" {
				t.Errorf("Run() stderr = %q, want empty", stderr)
			}
			if stdout != generateCompletionScript(t, shell) {
				t.Errorf("Run() stdout is not the generated %s script", shell)
			}
			if !strings.HasSuffix(stdout, "\n") {
				t.Error("script does not end with a newline")
			}
			// Printing a script needs no project, so it must not create one.
			assertNoWorkbookDirectory(t, repository)
		})
	}
}

func TestCompletionRejectsAMissingOrUnknownShell(t *testing.T) {
	t.Parallel()
	// Production mutation: an error that does not name the shells leaves the
	// caller guessing which spellings the command takes.
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing shell", args: []string{"completion"}},
		{name: "unknown shell", args: []string{"completion", "powershell"}},
		{name: "option in place of a shell", args: []string{"completion", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := testrepo.New(t)
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("Run(%q) code = %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Errorf("Run(%q) stdout = %q, want empty", test.args, stdout)
			}
			for _, shell := range completionShells {
				if !strings.Contains(stderr, shell) {
					t.Errorf("Run(%q) stderr = %q, want it to name %q", test.args, stderr, shell)
				}
			}
			assertNoWorkbookDirectory(t, repository)
		})
	}
}

func TestCompletionHelpNamesEveryShellAndHowToInstallIt(t *testing.T) {
	t.Parallel()
	// Production mutation: a command whose help does not say how to load its
	// output leaves the user with a script and no idea where to put it.
	output := assertHelpOutput(t, []string{"help", "completion"}, "Usage: workbook completion <shell>")
	for _, want := range []string{
		"bash, zsh or fish",
		"~/.bashrc",
		`eval "$(workbook completion`,
		"fpath",
		"~/.config/fish/completions/workbook.fish",
		"Homebrew formula installs all three",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("completion help = %q, want %q", output, want)
		}
	}
}

// The bash instruction is load-bearing and its wrong form fails silently, so
// both places that give it are held to the working one.
func TestBashCompletionIsInstalledWithEvalNotProcessSubstitution(t *testing.T) {
	t.Parallel()
	// Production mutation: `source <(workbook completion bash)` defines nothing
	// in bash 3.2, which is still /bin/bash on macOS. It reports no error, so a
	// reader who follows the instruction gets file-name completion and no sign
	// that the line did nothing.
	for name, document := range map[string]string{
		"completion help": commandSchemas["completion"].Description,
		"README.md":       repositoryDoc(t, "README.md"),
	} {
		// Help descriptions are wrapped prose, so the instruction is matched
		// against the text with its line breaks collapsed.
		document = strings.Join(strings.Fields(document), " ")
		if !strings.Contains(document, `eval "$(workbook completion bash)"`) {
			t.Errorf("%s does not install the bash completion with eval", name)
		}
		if strings.Contains(document, "source <(workbook completion bash)") {
			t.Errorf("%s tells bash users to source a process substitution", name)
		}
	}

	// The zsh instruction has to name a directory the reader owns: a stock
	// macOS zsh resolves fpath[1] to /usr/local/share/zsh/site-functions, which
	// does not exist and could not be written if it did.
	readme := repositoryDoc(t, "README.md")
	if strings.Contains(readme, "${fpath[1]}") {
		t.Error("README installs the zsh completion into fpath[1], which a source install cannot write")
	}
	for _, want := range []string{"~/.zfunc/_workbook", "fpath=(~/.zfunc $fpath)"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README is missing the zsh instruction %q", want)
		}
	}
}

func generateCompletionScript(t *testing.T, shell string) string {
	t.Helper()
	script, err := completionScript(shell)
	if err != nil {
		t.Fatalf("generate %s completion: %v", shell, err)
	}
	return script
}

// completionSchemaPaths lists every command path the schema defines, which is
// every path a generated script has to know options for.
func completionSchemaPaths() []string {
	paths := make([]string, 0, len(commandOrder)*2)
	for _, name := range commandOrder {
		paths = append(paths, name)
		for _, subcommand := range commandSchemas[name].SubcommandOrder {
			paths = append(paths, name+" "+subcommand)
		}
	}
	return paths
}

// wantCompletionWords is the words a command completes after its name: its
// subcommands, or for `completion` the shells its one positional accepts.
func wantCompletionWords(name string, metadata commandMetadata) []string {
	if name == "completion" {
		return completionShells
	}
	return metadata.SubcommandOrder
}

// wantCompletionOptions is a command's schema options plus the --help every
// command answers and renderCommandHelp documents for all of them.
func wantCompletionOptions(metadata commandMetadata) []string {
	options := make([]string, 0, len(metadata.Options)+1)
	for _, option := range metadata.Options {
		options = append(options, "--"+option.Name)
	}
	return append(options, "--help")
}

func schemaOptionNames(metadata commandMetadata, kinds ...flagKind) []string {
	var names []string
	for _, option := range metadata.Options {
		for _, kind := range kinds {
			if option.Kind == kind {
				names = append(names, "--"+option.Name)
				break
			}
		}
	}
	return names
}

func assertSameWords(t *testing.T, what string, got, want []string) {
	t.Helper()
	sorted := func(words []string) []string {
		copied := append([]string(nil), words...)
		sort.Strings(copied)
		return copied
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Errorf("%s = %q, want exactly %q", what, sorted(got), sorted(want))
	}
}

func assertOneLineSummary(t *testing.T, what, description string) {
	t.Helper()
	summary := summarizeDescription(description)
	if summary == "" {
		t.Errorf("%s summary is empty", what)
	}
	if strings.ContainsAny(summary, "\n\r\t") {
		t.Errorf("%s summary = %q, want one line", what, summary)
	}
}

// shellFunctionBody returns the body of a generated shell function, so a case
// branch is read from the function that declares it rather than from anywhere
// in the script that happens to spell the same path.
func shellFunctionBody(t *testing.T, script, name string) string {
	t.Helper()
	header := "\n" + name + "() {\n"
	start := strings.Index(script, header)
	if start < 0 {
		t.Fatalf("script has no %s function:\n%s", name, script)
	}
	body := script[start+len(header):]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("%s function is unterminated:\n%s", name, script)
	}
	return body[:end]
}

var bashEchoPattern = regexp.MustCompile(`^\t(?:'([^']*)'\) )?echo '([^']*)'(?: ;;)?$`)

// bashEchoedWords returns the words one bash case branch echoes, or the words
// an unconditional function echoes when path is empty.
func bashEchoedWords(t *testing.T, script, function, path string) []string {
	t.Helper()
	for _, line := range strings.Split(shellFunctionBody(t, script, function), "\n") {
		match := bashEchoPattern.FindStringSubmatch(line)
		if match != nil && match[1] == path {
			return strings.Fields(match[2])
		}
	}
	return nil
}

// zshDescribedNames returns the candidate names of the zsh array opened by the
// line with the given prefix, dropping the description after each colon.
func zshDescribedNames(t *testing.T, script, function, prefix string) []string {
	t.Helper()
	lines := strings.Split(shellFunctionBody(t, script, function), "\n")
	for index, line := range lines {
		if line != prefix {
			continue
		}
		var names []string
		for _, entry := range lines[index+1:] {
			entry = strings.TrimSpace(entry)
			if !strings.HasPrefix(entry, "'") {
				break
			}
			names = append(names, strings.SplitN(strings.Trim(entry, "'"), ":", 2)[0])
		}
		return names
	}
	return nil
}

var zshCasePattern = regexp.MustCompile(`^\t'([^']*)'\) options=\( (.*) \) ;;$`)
var shellQuotedOption = regexp.MustCompile(`'(--[a-z0-9-]+)'`)

// zshCaseOptions returns the options one zsh case branch collects.
func zshCaseOptions(t *testing.T, script, function, path string) []string {
	t.Helper()
	for _, line := range strings.Split(shellFunctionBody(t, script, function), "\n") {
		match := zshCasePattern.FindStringSubmatch(line)
		if match == nil || match[1] != path {
			continue
		}
		var names []string
		for _, option := range shellQuotedOption.FindAllStringSubmatch(match[2], -1) {
			names = append(names, option[1])
		}
		return names
	}
	return nil
}

var fishRulePattern = regexp.MustCompile(`^complete -c workbook -n (__fish_workbook_at|'__fish_workbook_at [a-z ]+') (-a|-l) '([^']*)'( -r -f)? -d '`)

// fishRuleNames returns the candidates one fish condition offers: the words it
// adds with -a, or the options it adds with -l, optionally only those that take
// a value.
func fishRuleNames(t *testing.T, script, path, kind string, valuesOnly bool) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(script, "\n") {
		match := fishRulePattern.FindStringSubmatch(line)
		if match == nil || match[2] != kind {
			continue
		}
		condition := strings.TrimPrefix(strings.Trim(match[1], "'"), "__fish_workbook_at")
		if strings.TrimSpace(condition) != path {
			continue
		}
		if valuesOnly && match[4] == "" {
			continue
		}
		name := match[3]
		if kind == "-l" {
			name = "--" + name
		}
		names = append(names, name)
	}
	return names
}
