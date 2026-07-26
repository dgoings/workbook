# CLI help final-fix report

## Scope completed

- Added `workbook help [command]` to the existing README implemented-command
  expectation and command-policy allowlist.
- Restored compact JSON invocation errors for `workbook hooks --json` and
  `workbook hooks unknown --json` while keeping `hooks install` as the parser
  and help metadata source for hook options.
- Classified malformed command-local `-h` and `--help` aliases before JSON
  error selection, including when `--json` precedes the alias.
- Kept help-looking string-option values and help-looking positional input
  after `--` on the normal command parsing path.
- Added `-h, --help` to every command help Options section, including the
  optionless `hooks` parent and the `hooks install` subcommand.
- Updated the installed-binary integration expectation for the implemented
  successful bare global-help invocation.

## Regression coverage

The tests exercise real `Run` behavior for:

- missing and unknown hook subcommands with `--json`;
- malformed short and long local help after `--json` for an option-only
  command, a positional command, and `hooks install`;
- a help-looking string flag value after `--json`;
- a help-looking positional argument after the `--` terminator;
- all explicit, short, and long top-level command-help forms;
- `hooks` and `hooks install` help, including visible short and long aliases;
- the README implemented-command list and policy; and
- the installed executable's bare global-help behavior.

## RED evidence

Before the fixes, focused regression tests failed because:

- hook invocation errors were human-readable instead of compact JSON;
- malformed local help after `--json` emitted JSON errors;
- command help omitted `-h, --help`;
- the README command test did not expect `workbook help [command]`; and
- the installed-binary test still expected bare invocation to exit 2.

The first full-suite run also caught an intermediate resolver defect that
treated ordinary positional values as subcommand targets and disabled JSON
intent. The resolver was corrected to use top-level option metadata for
ordinary commands and the `hooks install` child metadata only for hook
invocations.

## GREEN evidence

These commands completed successfully:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run 'TestRun(MalformedLocalHelpAfterJSONIsPlainText|LocalHelpRecognitionRespectsStringValuesAndTerminator|HooksInvocationErrorsRetainJSONIntent|HelpAliasesForEveryTopLevelCommand|JSONIntent)|TestREADMEImplementedCommands' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Repository-wide tests passed for every package, including
`internal/cli`, `internal/gitstore`, `internal/webui`, and `scripts`.
`go vet` and `git diff --check` produced no findings.

`gopls` is not installed in this environment, so a separate LSP diagnostic
command was unavailable. The full test and vet commands completed cleanly.
