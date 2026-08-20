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
- task history is append-only, inspectable, and mergeable;
- no hosted service or MCP server is required for the default workflow;
- SQLite provides efficient local task reads and filtering without becoming a
  second source of truth.

## Installation

### Homebrew

```sh
brew install dgoings/tap/workbook
```

The tap serves macOS and Linux bottles for Apple Silicon and Intel.

### Building from source

A Go 1.26 (or newer) toolchain and Git are the only requirements:

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

## Getting started

After cloning a repository, bootstrap Workbook with one command. It creates or
validates project identity, installs managed agent documentation, and exchanges
shared task refs with `origin`:

```sh
git clone <repository>
cd <repository>
workbook setup
```

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
workbook next --json           # acquire the next eligible task; fetches first
                               # so two agents cannot claim the same one
workbook show <task> --json    # read its full context
workbook update <task> --status in-progress --json
# ... implement ...
workbook update <task> --status in-review --json
```

`workbook setup` installs managed agent documentation and a Workbook skill into
the project so agents learn this loop on their own; see
[claiming work as an agent](docs/reference.md#claiming-work-as-an-agent) for
the assignment and contention rules.

## Architecture

Workbook separates its data model, transport, and query engine:

- **Operation model** — task history is a chain of immutable, append-only
  operations with tip checkpoints; deletes are tombstones, and history is
  inspectable and mergeable.
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
