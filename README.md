# Workbook

Workbook is a lightweight, repository-native project tracker for humans and coding agents.

The goal is to keep structured project state close to the code without requiring
a hosted issue tracker. The CLI stores task operations durably in Git objects
and refs and explicitly synchronizes them through `origin`. A disposable SQLite
materialized view accelerates normal task reads while Git remains canonical.

Workbook is published for macOS and Linux through the `dgoings/homebrew-tap`
Homebrew tap, and builds from source anywhere Go does.

## Why Workbook?

`TODO.md` files travel with a repository and are easy for agents to read, but they lack structure, validation, dependency modeling, useful queries, and safe concurrent updates. Hosted trackers provide that rigor, but introduce another account, API, network dependency, and source of context that can drift away from the code.

Workbook aims for a middle ground:

- project state travels through the same Git remote as the code;
- developers can clone a repository and bootstrap with one command;
- agents can discover, claim, and update work through a small CLI with JSON output;
- task history is append-only, inspectable, and mergeable, with a manual `workbook compact` for finished work;
- no hosted service or MCP server is required for the default workflow;
- SQLite provides efficient local task reads and filtering without becoming a
  second source of truth.

## Installation

Workbook runs the `git` command and needs Git 2.29 or later. Its fetches use
options and refspecs that first shipped in that release.

### Homebrew

```sh
brew install dgoings/tap/workbook
```

The tap serves macOS and Linux bottles for Apple Silicon and Intel.

### Building from source

A Go 1.26 (or newer) toolchain and Git 2.29 (or newer) are the only
requirements:

```sh
git clone https://github.com/dgoings/workbook.git
cd workbook
./scripts/install.sh
```

`scripts/install.sh [destination] [name]` builds `cmd/workbook` and installs it
into `$HOME/.local/bin` as `workbook` by default; pass a destination directory
and an alternate binary name to put it anywhere else. Source builds are stamped from
`git describe`, so `workbook version` reports the commit they came from rather
than `dev (unknown)`. A source build reports a leading `v`, for example
`v0.2.0-3-g86281c9`, and gains a `-dirty` suffix when built from a modified
tree; a released artifact reports a bare `0.2.0`, so the two are always
distinguishable.

To modify Workbook itself, see [CONTRIBUTING.md](CONTRIBUTING.md) — it covers
the two-build development environment, the test suite, and how releases are
cut.

### Shell completions

`workbook completion <shell>` prints a completion script for bash, zsh or fish.
The Homebrew formula installs all three, so this is for a source build. Pick
the line for your shell:

```sh
# bash, in ~/.bashrc
eval "$(workbook completion bash)"

# zsh, into a directory you own
mkdir -p ~/.zfunc && workbook completion zsh > ~/.zfunc/_workbook

# fish
workbook completion fish > ~/.config/fish/completions/workbook.fish
```

The bash line is `eval` rather than `source <(…)` because bash 3.2 — still
`/bin/bash` on macOS — reads nothing from a process substitution it is told to
source, and leaves no completion behind without reporting anything.

`~/.zfunc` has to be on `fpath` before `compinit` runs, so `~/.zshrc` needs
`fpath=(~/.zfunc $fpath)` above the line that calls it. Sourcing works on zsh if
you would rather not keep a file: `source <(workbook completion zsh)`, after
`compinit`.

The scripts are generated from the same command schema `workbook help` is
rendered from, so they complete every command, subcommand and option the
version that printed them accepts. Regenerate them after upgrading.

### The desktop app

Workbench, under [`desktop/`](desktop/README.md), is a desktop shell for
Workbook: one window with every imported project's board in a sidebar, a Next
view of what each of those projects would hand out next, and the CLI bundled so
an install is self-contained. It is built from this checkout and released
separately from the CLI; see its README for building it from source.

## Getting started

After cloning a repository, bootstrap Workbook with one command. It creates or
validates project identity, installs managed agent documentation, and exchanges
shared task refs with `origin`:

```sh
git clone <repository>
cd <repository>
workbook setup
```

Setup asks for the project key, the prefix a task ID carries, only when it
creates a new project, and suggests one from the directory name; a clone of an
existing project takes that project's key without asking. A project can add
keys later — `workbook key add`, `workbook key current` — so a repository made
of several subprojects can give each one its own prefix. Existing task IDs
never change.

Then work with tasks from the terminal:

```sh
workbook create "Fix the login redirect" --priority high
workbook list
workbook board                 # terminal board, one column per status
workbook serve                 # local web board
workbook show <task>
workbook update <task> --status in-progress
```

Every mutating command supports `--json` for a versioned machine-readable
result envelope, which is what makes the CLI scriptable and agent-friendly.
Use `workbook --help`, `workbook help <command>`, or the complete
[command reference](docs/reference.md) to go deeper.

Task changes synchronize themselves. A command that creates or updates a task
fetches shared task refs from `origin`, applies its change to the refreshed
tip, and publishes the single ref it changed. An unreachable `origin` is a
warning, not a failure: the change is recorded locally and the command still
succeeds. A repository with no `origin` is a local-only project and
synchronizes nothing. Turn synchronization off for one command with
`--no-sync`, for one project with `workbook config set auto-sync false`, or for
every project in the user configuration; the
[command reference](docs/reference.md#explicit-task-sharing) covers the
policy layers, divergence reconciliation, and the explicit `fetch`, `push`,
and `sync` commands.

## Target workflows

### Solo development

A developer keeps task state in the repository's Git object database and queries it locally. A remote is optional, although pushing Workbook refs provides backup and portability across clones.

### Small-team workflow

Team members explicitly share task refs through the repository's `origin`
remote. Workbook fetches only its private task namespace, validates current
task tips and their safe ancestry relationships in isolated tracking refs, and
leaves the checked-out code branch untouched. Local work that `origin` does not
have is replayed onto the fetched tip and published; the few concurrent
situations Workbook will not decide are reported rather than resolved for you.
A team can require synchronization by committing a tracked project policy that
outranks personal preferences.

### Coding agents

An agent discovers, claims, and updates work through the same CLI with `--json`
output. The loop it runs continuously is:

```sh
workbook next --claim --json   # take the next eligible task: fetch, pick,
                               # assign, and publish in one command, so two
                               # agents cannot walk away with the same one
workbook show <task> --json    # read its full context
workbook update <task> --status in-progress --json
# ... implement ...
workbook update <task> --status in-review --json
```

Bare `workbook next` shows what it would pick without taking it, and skips
tasks another identity already holds. `--claim` is what assigns and publishes.

`workbook setup` installs managed agent documentation and a Workbook skill into
the project so agents learn this loop on their own; see
[claiming work as an agent](docs/reference.md#claiming-work-as-an-agent) for
the assignment and contention rules.

### Hosting the board for a team

A server that shows and edits a project's tasks does not need the project's
code. In an empty folder, one command sets up a folder that holds only the
project's Workbook refs:

```sh
mkdir board && cd board
workbook setup --tasks-only <repository-url>
git config user.email board@example.com   # who edits made here are recorded as
workbook serve
```

The folder has no branch and none of the code, and a stray `git fetch` or `git
pull` in it downloads none either. Task commands and the web board work there
as they do in a clone, and changes publish to the same remote. Running the
command again changes nothing. Agent documentation, the skill and `workbook
hooks install` are left out, and `workbook docs` and `workbook hooks` say they
are unavailable there. See
[Tasks-only folders](docs/reference.md#tasks-only-folders) for exactly what it
sets up and refuses.

The board caps how much work it does at once, so a flood of requests from one
person cannot run the server out of processes or memory: it works on 16
requests at a time, writes take at most half, and a request past that waits up
to five seconds and is then refused with `503` and `Retry-After`. The board's
own page keeps a refused change and sends it again. `workbook serve
--max-requests <n>` or `WORKBOOK_SERVE_MAX_REQUESTS` changes the number. The cap
protects the server, not each person's share of it; see
[Local web board](docs/reference.md#local-web-board) for the limits to set at a
sign-in proxy.

## Architecture

Workbook separates its data model, transport, and query engine:

- **Operation model** — task history is a chain of immutable, append-only
  operations with tip checkpoints; deletes are tombstones, and history is
  inspectable and mergeable. `workbook compact`, run by hand, replaces the
  history of Done and Deleted tasks with one new checkpoint commit each, after
  saving the old history to a local backup bundle; it changes no existing
  commit.
- **Git storage** — operations live in tool-private refs under
  `refs/workbook/`, synchronized explicitly and only with `origin`. Task state
  never touches the checked-out code branch.
- **SQLite projection** — a disposable materialized view accelerates reads and
  filtering; it can be deleted at any time and rebuilt entirely from Git refs,
  which remain canonical.

[docs/architecture.md](docs/architecture.md) covers the storage model in
detail: ref layout, operation packs, concurrency and synchronization
semantics, the projection, bootstrap and portability, design principles, and
non-goals.

## Performance

Development performance is measured with the reproducible `workbook-bench`
harness. One command builds the working tree, runs the whole scenario
registry, and writes a dated report pair under the gitignored `bench-reports/`
directory:

```sh
scripts/benchmark.sh
```

Reports are descriptive — no thresholds, no pass/fail — and name the commit
they measured; comparing a run's report with an earlier one is how a baseline
shift is noticed. The [command reference](docs/reference.md) documents the
harness flags alongside the commands it measures.

## Related work

- [git-bug](https://github.com/git-bug/git-bug) stores distributed issue operations in Git objects under per-entity refs.
- [Fossil](https://fossil-scm.org/) synchronizes ticket-change artifacts and reconstructs SQL tables as projections.
- [Beads](https://github.com/gastownhall/beads) explores dependency-aware task memory for coding agents.
- [Automerge](https://automerge.org/) and [Yjs](https://yjs.dev/) provide general-purpose CRDT models for local-first collaboration.

## Contributing

Bug reports, questions, and pull requests are welcome. Start with
[CONTRIBUTING.md](CONTRIBUTING.md) for the development environment, testing
expectations, and release process; [AGENTS.md](AGENTS.md) records the
architectural invariants every change is held to.

## License

Workbook is released under the [MIT License](LICENSE).
