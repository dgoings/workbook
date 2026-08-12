package perf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
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
	Root              string
	Config            core.ProjectConfig
	TaskIDs           []string
	ActiveTaskIDs     []string
	TombstonedTaskIDs []string
	Dependencies      []FixtureDependency
}

type FixtureDependency struct {
	Dependent  string
	Dependency string
}

type fixtureTaskPlan struct {
	TaskID     string
	Generation string
	Initial    core.TaskData
	Operations []core.Operation
	Tombstoned bool
}

type fixtureCommit struct {
	Head  string
	Pack  core.OperationPack
	State core.StateDocument
}

// BuildFixture creates a Git-backed Workbook fixture without replaying each
// operation through the repository writer.
func BuildFixture(ctx context.Context, root string, spec FixtureSpec) (Fixture, error) {
	if err := validateFixtureSpec(spec); err != nil {
		return Fixture{}, err
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Fixture{}, fmt.Errorf("resolve fixture root: %w", err)
	}
	if err := runFixtureGit(ctx, "init", "--quiet", "--object-format="+spec.ObjectFormat, absRoot); err != nil {
		return Fixture{}, err
	}
	if err := configureFixtureRepository(ctx, absRoot); err != nil {
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

	plans, err := newFixtureTaskPlans(config, spec, ids)
	if err != nil {
		return Fixture{}, err
	}
	fixture, writeErr := writeFixtureHistory(input, config, plans, ids)
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
	if countRefLines(refs) != spec.TotalTasks {
		return Fixture{}, fmt.Errorf("task refs = %d, want %d", countRefLines(refs), spec.TotalTasks)
	}

	fixture.Root = absRoot
	fixture.Config = config
	return fixture, nil
}

func validateFixtureSpec(spec FixtureSpec) error {
	if spec.TotalTasks < 1 {
		return fmt.Errorf("total tasks must be positive")
	}
	if spec.ActiveTasks < 0 {
		return fmt.Errorf("active tasks must not be negative")
	}
	if spec.TombstonedTasks < 0 {
		return fmt.Errorf("tombstoned tasks must not be negative")
	}
	if spec.ActiveTasks+spec.TombstonedTasks != spec.TotalTasks {
		return fmt.Errorf("active tasks + tombstoned tasks must equal total tasks")
	}
	if spec.OperationsPerTask < 1 {
		return fmt.Errorf("operations per task must be positive")
	}
	if spec.TombstonedTasks > 0 && spec.OperationsPerTask < 2 {
		return fmt.Errorf("tombstoned tasks require at least two operations per task")
	}
	if spec.ObjectFormat != "sha1" && spec.ObjectFormat != "sha256" {
		return fmt.Errorf("unsupported object format %q", spec.ObjectFormat)
	}
	return nil
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

func newFixtureTaskPlans(config core.ProjectConfig, spec FixtureSpec, ids *fixtureIDs) ([]fixtureTaskPlan, error) {
	plans := make([]fixtureTaskPlan, spec.TotalTasks)
	for taskIndex := range plans {
		taskULID, err := ids.next()
		if err != nil {
			return nil, fmt.Errorf("generate task ID: %w", err)
		}
		generation, err := ids.next()
		if err != nil {
			return nil, fmt.Errorf("generate history generation: %w", err)
		}
		plans[taskIndex].TaskID = config.Key + "-" + taskULID
		plans[taskIndex].Generation = generation
		plans[taskIndex].Tombstoned = taskIndex >= spec.ActiveTasks
	}

	for taskIndex := range plans {
		dependency := ""
		if taskIndex > 0 && spec.ActiveTasks > 0 {
			dependency = plans[(taskIndex-1)%spec.ActiveTasks].TaskID
		}
		plans[taskIndex].Initial = core.TaskData{
			Title:        fmt.Sprintf("Benchmark task %04d", taskIndex),
			Description:  fmt.Sprintf("Representative benchmark task %04d", taskIndex),
			Status:       core.StatusBacklog,
			Priority:     core.PriorityMedium,
			Labels:       []string{},
			Rank:         fmt.Sprintf("%d/1", taskIndex+1),
			Dependencies: fixtureInitialDependencies(dependency),
		}
		plans[taskIndex].Operations = fixturePlanOperations(
			spec.OperationsPerTask,
			plans[taskIndex].Tombstoned,
			taskIndex,
			dependency,
		)
	}
	return plans, nil
}

func fixtureInitialDependencies(dependency string) []string {
	if dependency == "" {
		return []string{}
	}
	return []string{dependency}
}

func fixturePlanOperations(count int, tombstoned bool, taskIndex int, dependency string) []core.Operation {
	operations := make([]core.Operation, count)
	operations[0] = core.Operation{Type: core.OperationTaskCreate}
	for index := 1; index < count; index++ {
		logicalClock := index + 1
		if tombstoned && logicalClock == count {
			operations[index] = core.Operation{Type: core.OperationTaskTombstone}
			continue
		}
		operations[index] = fixtureRepresentativeOperation(taskIndex, logicalClock, dependency)
	}
	return operations
}

func fixtureRepresentativeOperation(taskIndex, logicalClock int, dependency string) core.Operation {
	labels := []string{"benchmark", "git", "projection", "workflow"}
	if logicalClock > 19 {
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "description",
			Value: fmt.Sprintf("Benchmark task %04d update %02d", taskIndex, logicalClock),
		}
	}

	switch logicalClock {
	case 2:
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "description",
			Value: fmt.Sprintf("Benchmark task %04d update %02d", taskIndex, logicalClock),
		}
	case 3, 9, 15:
		// A fixture repository has no configuration ledger, so it is read under
		// the pre-ledger vocabulary and `blocked` is a live status in it. The
		// cycle keeps it deliberately: most repositories Workbook meets in the
		// field are that shape, and a benchmark that only ever stored statuses
		// this build mints a project with would price a narrower population than
		// the one being measured. The order is load-bearing — the first entry is
		// what a fixture's second task settles on, and a scenario pins it.
		statuses := []core.Status{
			core.StatusInProgress,
			core.StatusBlocked,
			core.StatusInReview,
			core.StatusDone,
		}
		phase := (logicalClock - 3) / 6
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "status",
			Value: string(statuses[(taskIndex/2+phase)%len(statuses)]),
		}
	case 4, 10, 16:
		priorities := []core.Priority{core.PriorityLow, core.PriorityHigh}
		phase := (logicalClock - 4) / 6
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "priority",
			Value: string(priorities[(taskIndex/2+phase)%len(priorities)]),
		}
	case 5:
		return core.Operation{
			Type:  core.OperationSetAdd,
			Field: "labels",
			Value: labels[taskIndex%len(labels)],
		}
	case 6:
		if dependency != "" {
			return core.Operation{
				Type:  core.OperationSetRemove,
				Field: "dependencies",
				Value: dependency,
			}
		}
		return core.Operation{
			Type:  core.OperationSetAdd,
			Field: "labels",
			Value: labels[(taskIndex+2)%len(labels)],
		}
	case 7:
		if dependency != "" {
			return core.Operation{
				Type:  core.OperationSetAdd,
				Field: "dependencies",
				Value: dependency,
			}
		}
		return fixtureRankOperation(taskIndex, 0)
	case 8:
		if dependency != "" {
			return fixtureRankOperation(taskIndex, 0)
		}
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "description",
			Value: fmt.Sprintf("Benchmark task %04d update %02d", taskIndex, logicalClock),
		}
	case 13:
		if dependency != "" {
			return core.Operation{
				Type:  core.OperationSetAdd,
				Field: "dependencies",
				Value: dependency,
			}
		}
		return fixtureRankOperation(taskIndex, 1)
	case 14:
		if dependency != "" {
			return fixtureRankOperation(taskIndex, 1)
		}
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "description",
			Value: fmt.Sprintf("Benchmark task %04d update %02d", taskIndex, logicalClock),
		}
	case 19:
		phase := (logicalClock - 7) / 6
		return fixtureRankOperation(taskIndex, phase)
	case 11:
		return core.Operation{
			Type:  core.OperationSetRemove,
			Field: "labels",
			Value: labels[taskIndex%len(labels)],
		}
	case 12:
		if dependency != "" {
			return core.Operation{
				Type:  core.OperationSetRemove,
				Field: "dependencies",
				Value: dependency,
			}
		}
		return core.Operation{
			Type:  core.OperationSetRemove,
			Field: "labels",
			Value: labels[(taskIndex+2)%len(labels)],
		}
	case 17:
		return core.Operation{
			Type:  core.OperationSetAdd,
			Field: "labels",
			Value: labels[(taskIndex+1)%len(labels)],
		}
	case 18:
		return core.Operation{
			Type:  core.OperationSetAdd,
			Field: "labels",
			Value: labels[(taskIndex+3)%len(labels)],
		}
	default:
		return core.Operation{
			Type:  core.OperationFieldSet,
			Field: "description",
			Value: fmt.Sprintf("Benchmark task %04d update %02d", taskIndex, logicalClock),
		}
	}
}

func fixtureRankOperation(taskIndex, phase int) core.Operation {
	return core.Operation{
		Type:  core.OperationFieldSet,
		Field: "rank",
		Value: fmt.Sprintf("%d/2", 2*(taskIndex%4)+1+2*phase),
	}
}

func writeFixtureHistory(w io.Writer, config core.ProjectConfig, plans []fixtureTaskPlan, ids *fixtureIDs) (Fixture, error) {
	fixture := Fixture{
		TaskIDs:           make([]string, 0, len(plans)),
		ActiveTaskIDs:     make([]string, 0, len(plans)),
		TombstonedTaskIDs: make([]string, 0, len(plans)),
	}
	mark := 0
	for taskIndex, plan := range plans {
		var parent *core.StateDocument
		parentMark := 0
		for operationIndex, plannedOperation := range plan.Operations {
			logicalClock := operationIndex + 1
			operationID, err := ids.next()
			if err != nil {
				return Fixture{}, fmt.Errorf("generate operation ID: %w", err)
			}
			timestamp := ids.timestamp()
			pack := fixturePlanOperationPack(config, plan, taskIndex, logicalClock, operationID, timestamp, plannedOperation)
			state, err := core.Apply(parent, pack, config.Key)
			if err != nil {
				return Fixture{}, fmt.Errorf("apply task %q operation %d: %w", plan.TaskID, logicalClock, err)
			}
			operation, err := core.EncodeDocument(pack)
			if err != nil {
				return Fixture{}, fmt.Errorf("encode task %q operation %d: %w", plan.TaskID, logicalClock, err)
			}
			checkpoint, err := core.EncodeDocument(state)
			if err != nil {
				return Fixture{}, fmt.Errorf("encode task %q state %d: %w", plan.TaskID, logicalClock, err)
			}

			mark++
			if err := writeImportedCommit(w, "refs/workbook/tasks/"+plan.TaskID, mark, parentMark, timestamp, fmt.Sprintf("workbook: benchmark task %04d operation %02d", taskIndex, logicalClock), operation, checkpoint); err != nil {
				return Fixture{}, fmt.Errorf("write task %q operation %d: %w", plan.TaskID, logicalClock, err)
			}
			stateCopy := state
			parent = &stateCopy
			parentMark = mark
		}
		fixture.TaskIDs = append(fixture.TaskIDs, plan.TaskID)
		if parent.Task.Deleted {
			fixture.TombstonedTaskIDs = append(fixture.TombstonedTaskIDs, plan.TaskID)
		} else {
			fixture.ActiveTaskIDs = append(fixture.ActiveTaskIDs, plan.TaskID)
		}
		for _, dependency := range parent.Task.Dependencies {
			fixture.Dependencies = append(fixture.Dependencies, FixtureDependency{Dependent: plan.TaskID, Dependency: dependency})
		}
	}
	return fixture, nil
}

// readFixtureCommit reads a fixture commit's independently valid durable
// documents. Callers that need semantic validation must use
// core.ValidateCheckpoint with the commit's parent state.
func readFixtureCommit(ctx context.Context, root, head string) (fixtureCommit, error) {
	operationBytes, err := runFixtureGitOutput(ctx, root, nil, "show", head+":operation.json")
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("read fixture operation %q: %w", head, err)
	}
	pack, err := core.DecodeOperationPack(operationBytes)
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("decode fixture operation %q: %w", head, err)
	}
	stateBytes, err := runFixtureGitOutput(ctx, root, nil, "show", head+":state.json")
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("read fixture state %q: %w", head, err)
	}
	state, err := core.DecodeStateDocument(stateBytes)
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("decode fixture state %q: %w", head, err)
	}
	return fixtureCommit{Head: head, Pack: pack, State: state}, nil
}

// appendFixtureOperation appends one deterministic, valid task operation to an
// explicit parent fixture commit.
func appendFixtureOperation(
	ctx context.Context,
	root string,
	config core.ProjectConfig,
	parent fixtureCommit,
	taskID string,
	generation string,
	taskIndex, logicalClock int,
	ids *fixtureIDs,
) (fixtureCommit, error) {
	operationID, err := ids.next()
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("generate fixture operation ID: %w", err)
	}
	pack := fixtureOperationPack(config, taskID, generation, taskIndex, logicalClock, operationID, ids.timestamp())
	state, err := core.Apply(&parent.State, pack, config.Key)
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("apply fixture operation: %w", err)
	}
	return writeFixtureCommit(ctx, root, parent.Head, pack, state, "workbook: benchmark fixture append")
}

// writeFixtureCommit writes a task-shaped commit from the supplied documents.
// It intentionally does not validate the checkpoint relationship so remote
// fixture tests can construct isolated corrupt histories without relaxing
// production validation.
func writeFixtureCommit(
	ctx context.Context,
	root, parent string,
	pack core.OperationPack,
	state core.StateDocument,
	message string,
) (fixtureCommit, error) {
	operation, err := core.EncodeDocument(pack)
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("encode fixture operation: %w", err)
	}
	checkpoint, err := core.EncodeDocument(state)
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("encode fixture state: %w", err)
	}
	operationBlob, err := fixtureObjectID(ctx, root, operation, "hash-object", "-w", "--stdin")
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("write fixture operation blob: %w", err)
	}
	stateBlob, err := fixtureObjectID(ctx, root, checkpoint, "hash-object", "-w", "--stdin")
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("write fixture state blob: %w", err)
	}
	tree, err := fixtureObjectID(ctx, root, []byte(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob)), "mktree")
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("write fixture task tree: %w", err)
	}
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", message)
	head, err := fixtureCommitObjectID(ctx, root, nil, pack.WallTime, args...)
	if err != nil {
		return fixtureCommit{}, fmt.Errorf("write fixture commit: %w", err)
	}
	return fixtureCommit{Head: head, Pack: pack, State: state}, nil
}

func fixtureObjectID(ctx context.Context, root string, input []byte, args ...string) (string, error) {
	output, err := runFixtureGitOutput(ctx, root, input, args...)
	if err != nil {
		return "", err
	}
	return fixtureSingleLine(output)
}

func fixtureCommitObjectID(ctx context.Context, root string, input []byte, timestamp time.Time, args ...string) (string, error) {
	output, err := runFixtureGitOutputWithEnv(ctx, root, input, fixtureCommitEnvironment(timestamp), args...)
	if err != nil {
		return "", err
	}
	return fixtureSingleLine(output)
}

func runFixtureGitOutput(ctx context.Context, root string, input []byte, args ...string) ([]byte, error) {
	return runFixtureGitOutputWithEnv(ctx, root, input, nil, args...)
}

func runFixtureGitOutputWithEnv(ctx context.Context, root string, input []byte, extraEnv []string, args ...string) ([]byte, error) {
	commandArgs := append(fixtureGitConfig(root), "-C", root)
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Stdin = bytes.NewReader(input)
	command.Env = fixtureGitEnvironment(extraEnv)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func fixtureCommitEnvironment(timestamp time.Time) []string {
	date := timestamp.UTC().Format(time.RFC3339)
	return []string{
		"GIT_AUTHOR_NAME=Workbook Benchmark",
		"GIT_AUTHOR_EMAIL=" + benchmarkActorID,
		"GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=Workbook Benchmark",
		"GIT_COMMITTER_EMAIL=" + benchmarkActorID,
		"GIT_COMMITTER_DATE=" + date,
	}
}

func fixtureGitEnvironment(extra []string) []string {
	overridden := make(map[string]struct{}, len(extra))
	for _, entry := range extra {
		if name, _, found := strings.Cut(entry, "="); found {
			overridden[name] = struct{}{}
		}
	}
	environment := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		if name, _, found := strings.Cut(entry, "="); found {
			if _, replace := overridden[name]; replace {
				continue
			}
		}
		environment = append(environment, entry)
	}
	return append(environment, extra...)
}

func fixtureGitConfig(root string) []string {
	hooksPath := fixtureDisabledHooksPath(root)
	return []string{
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "push.gpgSign=false",
		"-c", "core.hooksPath=" + hooksPath,
	}
}

func fixtureDisabledHooksPath(root string) string {
	if root == "" {
		return filepath.Join(os.TempDir(), "workbook-fixture-hooks-disabled")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return filepath.Join(root, "workbook-fixture-hooks-disabled")
	}
	return filepath.Join(absRoot, "workbook-fixture-hooks-disabled")
}

func configureFixtureRepository(ctx context.Context, root string) error {
	for _, setting := range [][2]string{
		{"user.name", "Workbook Benchmark"},
		{"user.email", benchmarkActorID},
		{"commit.gpgSign", "false"},
		{"tag.gpgSign", "false"},
		{"push.gpgSign", "false"},
		{"core.hooksPath", fixtureDisabledHooksPath(root)},
		{"core.logAllRefUpdates", "always"},
	} {
		if err := runFixtureGitInRoot(ctx, root, "-C", root, "config", "--local", setting[0], setting[1]); err != nil {
			return err
		}
	}
	return nil
}

func fixtureSingleLine(output []byte) (string, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return "", fmt.Errorf("expected one trailing newline")
	}
	line := strings.TrimSuffix(string(output), "\n")
	if strings.ContainsAny(line, "\r\n") || line == "" {
		return "", fmt.Errorf("expected one output line")
	}
	return line, nil
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

	operation := fixtureFieldSetOperation(taskIndex, logicalClock)
	operation.ID = operationID
	pack.Operations = []core.Operation{operation}
	return pack
}

func fixturePlanOperationPack(config core.ProjectConfig, plan fixtureTaskPlan, taskIndex, logicalClock int, operationID string, timestamp time.Time, operation core.Operation) core.OperationPack {
	pack := core.OperationPack{
		Format:            "workbook.operation-pack",
		Version:           1,
		ProjectID:         config.ProjectID,
		TaskID:            plan.TaskID,
		HistoryGeneration: plan.Generation,
		Actor:             core.Actor{ID: benchmarkActorID},
		LogicalClock:      uint64(logicalClock),
		WallTime:          timestamp,
	}
	operation.ID = operationID
	if operation.Type == core.OperationTaskCreate {
		initial := operation
		task := plan.Initial
		task.CreatedAt = timestamp
		task.UpdatedAt = timestamp
		initial.Task = &task
		pack.Operations = []core.Operation{initial}
		return pack
	}
	pack.Operations = []core.Operation{operation}
	return pack
}

func fixtureFieldSetOperation(taskIndex, logicalClock int) core.Operation {
	operation := core.Operation{Type: core.OperationFieldSet}
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
	return operation
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
	return runFixtureGitInRoot(ctx, fixtureGitRoot(args), args...)
}

func runFixtureGitInRoot(ctx context.Context, root string, args ...string) error {
	commandArgs := append(fixtureGitConfig(root), args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = fixtureGitEnvironment(nil)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func fixtureGitRoot(args []string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-C" {
			return args[index+1]
		}
	}
	return ""
}

func countRefLines(refs []byte) int {
	trimmed := strings.TrimSpace(string(refs))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}
