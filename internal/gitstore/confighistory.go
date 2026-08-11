package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// ConfigHistoryStart announces the configuration ledger before any of its
// commits.
type ConfigHistoryStart struct {
	Head string
	// Commits is the ledger's whole length, even when this read delivers only
	// the newest part of it. A caller windowing the history needs the total in
	// order to say what it left out, and the commit walk that produces it is
	// one rev-list whatever the window is.
	Commits int
	// Skipped counts the commits before the first one delivered, which is zero
	// for an unbounded read.
	Skipped int
}

// ConfigHistoryCommit is one structurally validated configuration commit.
type ConfigHistoryCommit struct {
	ObjectID  string
	Operation core.ConfigOperationPack
	State     core.ConfigStateDocument
}

// ConfigHistoryFailure attributes a structural or document failure to one
// candidate commit.
type ConfigHistoryFailure struct {
	Commit string
	Err    error
}

// ConfigHistoryResult reports the ledger's validated prefix.
type ConfigHistoryResult struct {
	Head           string
	CheckedCommits int
	Failure        *ConfigHistoryFailure
}

// ConfigHistoryStream receives the configuration ledger one commit at a time.
//
// It is the task history stream's contract, restated for a singleton: Begin
// runs once, Commit runs once per validated commit oldest first, and End runs
// once with the checked count and the first failure. The shape is shared on
// purpose — a caller that folds task history and configuration history in one
// audit should be writing the same kind of handler twice, not two kinds — and
// it exists for the same reason: a fold that only ever needs the parent state
// and the current record must never be made to hold the whole corpus.
type ConfigHistoryStream struct {
	Begin  func(ConfigHistoryStart) error
	Commit func(ConfigHistoryCommit) error
	End    func(ConfigHistoryResult) error
}

// ReadConfigHistoryStream reads the whole configuration ledger, oldest commit
// first, and reports whether the project has one at all.
//
// A project with no ledger is not a failure and never calls a handler: the
// ledger is seeded lazily, so its absence is the ordinary state.
func (r *Repository) ReadConfigHistoryStream(
	ctx context.Context,
	config core.ProjectConfig,
	stream ConfigHistoryStream,
) (bool, error) {
	return r.readConfigHistory(ctx, config, 0, stream)
}

// ReadConfigHistoryTail reads only the newest commits of the ledger, the oldest
// of them first, and still reports how long the whole ledger is.
//
// It exists because reading everything is linear in a history that only grows,
// and the two commands that read the ledger want a bounded slice of it: a
// windowed log wants its window, and a status listing wants recent dates. The
// cost that matters is per commit — two documents decoded and re-encoded to
// compare canonical bytes — so bounding the commits bounds the command. The
// commit walk itself is one rev-list and stays unbounded, which is what keeps
// the reported total exact.
//
// A commits argument of zero or less reads everything, so a caller can pass a
// window straight through without a branch.
func (r *Repository) ReadConfigHistoryTail(
	ctx context.Context,
	config core.ProjectConfig,
	commits int,
	stream ConfigHistoryStream,
) (bool, error) {
	return r.readConfigHistory(ctx, config, commits, stream)
}

func (r *Repository) readConfigHistory(
	ctx context.Context,
	config core.ProjectConfig,
	tail int,
	stream ConfigHistoryStream,
) (bool, error) {
	if stream.Begin == nil || stream.Commit == nil || stream.End == nil {
		return false, core.Errorf(core.CategoryOperational,
			"configuration history stream requires begin, commit, and end handlers")
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return false, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return false, err
	}
	listing, err := r.listConfigRefs(ctx, configRef)
	if err != nil {
		return false, err
	}
	head, found := listing.Heads[configRef]
	if !found {
		return false, nil
	}
	if err := r.rememberGitObjectID(head); err != nil {
		return false, core.Wrap(core.CategoryCorruptData, "Git returned an invalid configuration ref object ID", err)
	}
	decoded, err := decodeObjectID(head)
	if err != nil {
		return false, core.Wrap(core.CategoryCorruptData, "Git returned an invalid configuration ref object ID", err)
	}
	objectIDBytes := len(decoded)

	chain, err := r.configCommitChain(ctx, head)
	if err != nil {
		return false, err
	}
	total := len(chain)
	skipped := 0
	if tail > 0 && tail < len(chain) {
		skipped = len(chain) - tail
		chain = chain[skipped:]
	}

	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		for _, objectID := range chain {
			if _, err := fmt.Fprintf(writer, "%s\n%s^{tree}\n%s:%s\n%s:%s\n",
				objectID, objectID, objectID, configOperationPath, objectID, configStatePath); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	defer batch.Close()

	result := ConfigHistoryResult{Head: head}
	if err := stream.Begin(ConfigHistoryStart{Head: head, Commits: total, Skipped: skipped}); err != nil {
		return true, err
	}
	for _, objectID := range chain {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		// Every requested object is consumed even after a failure, because the
		// response stream is shared with every later commit.
		objects, err := readBatchObjects(batch.Reader())
		if err != nil {
			return true, batch.ReadFailure("cannot read Workbook configuration objects from Git batch", err)
		}
		if result.Failure != nil {
			continue
		}
		result.CheckedCommits++
		record, err := validateConfigObjects(objects, config, configRef, objectID, objectIDBytes)
		if err != nil {
			result.Failure = &ConfigHistoryFailure{Commit: objectID, Err: err}
			continue
		}
		if err := stream.Commit(ConfigHistoryCommit{
			ObjectID:  record.Head,
			Operation: record.Operation,
			State:     record.State,
		}); err != nil {
			return true, err
		}
	}
	if err := stream.End(result); err != nil {
		return true, err
	}
	return true, batch.Finish()
}

// configCommitChain walks the ledger oldest commit first.
func (r *Repository) configCommitChain(ctx context.Context, head string) ([]string, error) {
	var input bytes.Buffer
	fmt.Fprintln(&input, head)
	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot walk the configuration ledger", err)
	}
	graph, err := parseParentGraph(output)
	if err != nil {
		return nil, err
	}
	newestFirst, err := commitChain(graph, head)
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot walk the configuration ledger", err)
	}
	return reverseCommits(newestFirst), nil
}
