package perf

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/oklog/ulid/v2"
)

const (
	benchmarkProjectKey = "WB"
	benchmarkActorID    = "benchmark@example.invalid"
)

var benchmarkOrigin = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// Fixture is a deterministic Workbook repository populated with valid task
// operation histories.
type Fixture struct {
	Root    string
	Config  core.ProjectConfig
	TaskIDs []string
}

// BuildFixture creates a Git-backed Workbook fixture without replaying each
// operation through the repository writer.
func BuildFixture(ctx context.Context, root string, spec FixtureSpec) (Fixture, error) {
	if spec.ActiveTasks < 1 {
		return Fixture{}, fmt.Errorf("active tasks must be positive")
	}
	if spec.OperationsPerTask < 1 {
		return Fixture{}, fmt.Errorf("operations per task must be positive")
	}
	if spec.ObjectFormat != "sha1" && spec.ObjectFormat != "sha256" {
		return Fixture{}, fmt.Errorf("unsupported object format %q", spec.ObjectFormat)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Fixture{}, fmt.Errorf("resolve fixture root: %w", err)
	}
	if err := runFixtureGit(ctx, "init", "--quiet", "--object-format="+spec.ObjectFormat, absRoot); err != nil {
		return Fixture{}, err
	}
	if err := runFixtureGit(ctx, "-C", absRoot, "config", "user.name", "Workbook Benchmark"); err != nil {
		return Fixture{}, err
	}
	if err := runFixtureGit(ctx, "-C", absRoot, "config", "user.email", benchmarkActorID); err != nil {
		return Fixture{}, err
	}
	if err := runFixtureGit(ctx, "-C", absRoot, "config", "core.logAllRefUpdates", "always"); err != nil {
		return Fixture{}, err
	}

	ids := newFixtureIDs()
	repository, err := gitstore.Open(ctx, absRoot)
	if err != nil {
		return Fixture{}, err
	}
	config, _, err := repository.Init(ctx, benchmarkProjectKey, core.IDSourceFunc(ids.next))
	if err != nil {
		return Fixture{}, err
	}

	command := exec.CommandContext(ctx, "git", "-C", absRoot, "fast-import", "--quiet")
	input, err := command.StdinPipe()
	if err != nil {
		return Fixture{}, fmt.Errorf("open git fast-import input: %w", err)
	}
	if err := command.Start(); err != nil {
		return Fixture{}, fmt.Errorf("start git fast-import: %w", err)
	}

	taskIDs, writeErr := writeFixtureHistory(input, config, spec, ids)
	closeErr := input.Close()
	waitErr := command.Wait()
	if writeErr != nil {
		return Fixture{}, writeErr
	}
	if closeErr != nil {
		return Fixture{}, fmt.Errorf("close git fast-import input: %w", closeErr)
	}
	if waitErr != nil {
		return Fixture{}, fmt.Errorf("git fast-import: %w", waitErr)
	}

	refs, err := repository.Git(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/")
	if err != nil {
		return Fixture{}, err
	}
	if countRefLines(refs) != spec.ActiveTasks {
		return Fixture{}, fmt.Errorf("task refs = %d, want %d", countRefLines(refs), spec.ActiveTasks)
	}

	return Fixture{Root: absRoot, Config: config, TaskIDs: taskIDs}, nil
}

type fixtureIDs struct {
	entropy *rand.Rand
	nextAt  time.Time
}

func newFixtureIDs() *fixtureIDs {
	return &fixtureIDs{
		entropy: rand.New(rand.NewSource(1)),
		nextAt:  benchmarkOrigin,
	}
}

func (ids *fixtureIDs) next() (string, error) {
	id, err := ulid.New(ulid.Timestamp(ids.nextAt), ids.entropy)
	if err != nil {
		return "", err
	}
	ids.nextAt = ids.nextAt.Add(time.Millisecond)
	return id.String(), nil
}

func (ids *fixtureIDs) timestamp() time.Time {
	timestamp := ids.nextAt
	ids.nextAt = ids.nextAt.Add(time.Millisecond)
	return timestamp
}

func writeFixtureHistory(w io.Writer, config core.ProjectConfig, spec FixtureSpec, ids *fixtureIDs) ([]string, error) {
	taskIDs := make([]string, 0, spec.ActiveTasks)
	mark := 0
	for taskIndex := 0; taskIndex < spec.ActiveTasks; taskIndex++ {
		taskULID, err := ids.next()
		if err != nil {
			return nil, fmt.Errorf("generate task ID: %w", err)
		}
		generation, err := ids.next()
		if err != nil {
			return nil, fmt.Errorf("generate history generation: %w", err)
		}
		taskID := config.Key + "-" + taskULID
		taskIDs = append(taskIDs, taskID)

		var parent *core.StateDocument
		parentMark := 0
		for logicalClock := 1; logicalClock <= spec.OperationsPerTask; logicalClock++ {
			operationID, err := ids.next()
			if err != nil {
				return nil, fmt.Errorf("generate operation ID: %w", err)
			}
			timestamp := ids.timestamp()
			pack := fixtureOperationPack(config, taskID, generation, taskIndex, logicalClock, operationID, timestamp)
			state, err := core.Apply(parent, pack, config.Key)
			if err != nil {
				return nil, fmt.Errorf("apply task %q operation %d: %w", taskID, logicalClock, err)
			}
			operation, err := core.EncodeDocument(pack)
			if err != nil {
				return nil, fmt.Errorf("encode task %q operation %d: %w", taskID, logicalClock, err)
			}
			checkpoint, err := core.EncodeDocument(state)
			if err != nil {
				return nil, fmt.Errorf("encode task %q state %d: %w", taskID, logicalClock, err)
			}

			mark++
			if err := writeImportedCommit(w, "refs/workbook/tasks/"+taskID, mark, parentMark, timestamp, fmt.Sprintf("workbook: benchmark task %04d operation %02d", taskIndex, logicalClock), operation, checkpoint); err != nil {
				return nil, fmt.Errorf("write task %q operation %d: %w", taskID, logicalClock, err)
			}
			stateCopy := state
			parent = &stateCopy
			parentMark = mark
		}
	}
	return taskIDs, nil
}

func fixtureOperationPack(config core.ProjectConfig, taskID, generation string, taskIndex, logicalClock int, operationID string, timestamp time.Time) core.OperationPack {
	pack := core.OperationPack{
		Format:            "workbook.operation-pack",
		Version:           1,
		ProjectID:         config.ProjectID,
		TaskID:            taskID,
		HistoryGeneration: generation,
		Actor:             core.Actor{ID: benchmarkActorID},
		LogicalClock:      uint64(logicalClock),
		WallTime:          timestamp,
	}
	if logicalClock == 1 {
		pack.Operations = []core.Operation{{
			ID:   operationID,
			Type: core.OperationTaskCreate,
			Task: &core.TaskData{
				Title:        fmt.Sprintf("Benchmark task %04d", taskIndex),
				Status:       core.StatusBacklog,
				Priority:     core.PriorityMedium,
				Labels:       []string{},
				Rank:         fmt.Sprintf("%d/1", taskIndex+1),
				Dependencies: []string{},
				CreatedAt:    timestamp,
				UpdatedAt:    timestamp,
			},
		}}
		return pack
	}

	operation := core.Operation{ID: operationID, Type: core.OperationFieldSet}
	switch (logicalClock - 2) % 3 {
	case 0:
		operation.Field = "description"
		operation.Value = fmt.Sprintf("Benchmark task %04d update %02d", taskIndex, logicalClock)
	case 1:
		operation.Field = "status"
		operation.Value = string(core.StatusInProgress)
	case 2:
		operation.Field = "priority"
		operation.Value = string(core.PriorityHigh)
	}
	pack.Operations = []core.Operation{operation}
	return pack
}

func writeImportedCommit(
	writer io.Writer,
	ref string,
	mark int,
	parentMark int,
	timestamp time.Time,
	message string,
	operation []byte,
	state []byte,
) error {
	if _, err := fmt.Fprintf(writer, "commit %s\nmark :%d\n", ref, mark); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "author Workbook Benchmark <%s> %d +0000\n", benchmarkActorID, timestamp.Unix()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "committer Workbook Benchmark <%s> %d +0000\n", benchmarkActorID, timestamp.Unix()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data %d\n%s\n", len(message), message); err != nil {
		return err
	}
	if parentMark != 0 {
		if _, err := fmt.Fprintf(writer, "from :%d\n", parentMark); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(writer, "deleteall"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "M 100644 inline operation.json"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data %d\n", len(operation)); err != nil {
		return err
	}
	if _, err := writer.Write(operation); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "M 100644 inline state.json"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data %d\n", len(state)); err != nil {
		return err
	}
	if _, err := writer.Write(state); err != nil {
		return err
	}
	_, err := fmt.Fprintln(writer)
	return err
}

func runFixtureGit(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func countRefLines(refs []byte) int {
	trimmed := strings.TrimSpace(string(refs))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}
