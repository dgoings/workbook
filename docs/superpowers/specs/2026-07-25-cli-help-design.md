# CLI Help Design

## Goal

Make Workbook discoverable from the command line by providing explicit global,
command, and supported-subcommand help without opening a repository or
changing task state.

## Supported invocation forms

- `workbook`, `workbook --help`, `workbook -h`, and `workbook help` print the
  global command list and exit successfully.
- `workbook <command> --help`, `workbook <command> -h`, and
  `workbook help <command>` print help for a top-level command and exit
  successfully.
- `workbook help hooks` describes the `hooks install` subcommand;
  `workbook hooks install --help` describes the install action itself.
- The `help` command has no flags. In particular, `workbook help create --json`
  is an invocation error rather than a JSON help response.
- `--json` remains listed only in the help for commands that actually support
  it. Help output itself is always human-readable text.

Unknown help targets and malformed help invocations use the existing
invocation-error output and global usage text.

## Command metadata

The existing command schema becomes the authoritative help model. Each
top-level command has a synopsis, concise description, positional argument
names, and option descriptions alongside its flag-kind schema. A command's
registered options and documented options must match exactly. This avoids a
separate hand-maintained usage switch that can drift from parser behavior.

`hooks` is the only POC top-level command with a subcommand. Its parent help
lists `install`; the install action has distinct synopsis and option metadata.

## Dispatch and parsing

`Run` detects global help before ordinary command dispatch. It detects
command-local `-h` and `--help` before repository opening or required
positional-argument validation. The `help` command resolves a zero-, one-, or
two-token help target before dispatching any mutating command.

Normal command parsing does not gain a help flag. This prevents `--help` from
being confused with durable command input and keeps every command's schema
focused on its actual behavior. The dispatcher consumes recognized help
requests instead.

## Output and errors

Global help preserves the current compact command list and adds a discoverable
`help [command]` entry. Command help contains usage, description, positional
arguments, and an options section when options exist. Options are shown in
stable order, with short `-h` and long `--help` described uniformly.

Successful help writes to standard output and returns exit code zero. Invalid
help input writes through the existing invocation-error path to standard error
and includes global usage. No help form calls `openRepository`, starts a
server, performs Git I/O, or changes task refs.

## Verification

CLI tests exercise every global and command-local alias, the `help` command,
the `hooks install` subcommand, exit codes, output stream selection, malformed
help input, and no-repository behavior. Schema/metadata tests prove every
documented option is registered with the expected kind and every registered
option appears in help. README command documentation is updated to mention
the discoverable help forms without implying JSON help support.
