package perf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Workbook ref namespaces. Object classification walks every ref under
// workbookRefPrefix, which in a Workbook repository is the complete set of
// tool-private refs that keep durable task history alive.
const (
	workbookRefPrefix     = "refs/workbook/"
	workbookTaskRefPrefix = "refs/workbook/tasks/"
)

// Durable object classes. Every reachable object lands in exactly one class:
// the Git object type decides commit, tree, and annotated tag, and a blob's
// path inside the task tree decides operation, state, or other.
const (
	ObjectClassOperationBlob = "operation-blob"
	ObjectClassStateBlob     = "state-blob"
	ObjectClassOtherBlob     = "other-blob"
	ObjectClassTree          = "tree"
	ObjectClassCommit        = "commit"
	ObjectClassAnnotatedTag  = "annotated-tag"
)

// Measured resource command names.
const (
	ResourceCommandProjectionRebuild = "projection-rebuild"
	ResourceCommandFullValidation    = "full-validation"
)

const (
	operationDocumentPath = "operation.json"
	stateDocumentPath     = "state.json"

	projectionCacheFilename = "cache.sqlite"
	validationCacheFilename = "validation.sqlite"

	storageObjectSizeSemantics = "rawBytes is the uncompressed Git object content size (%(objectsize)); " +
		"diskBytes is the size of the object's stored representation including delta and header " +
		"(%(objectsize:disk)) and excludes per-pack index and header overhead"
	storageRepositoryState = "refs packed with git pack-refs --all, objects packed with git gc --quiet --prune=now"
)

var objectClassOrder = []string{
	ObjectClassOperationBlob,
	ObjectClassStateBlob,
	ObjectClassOtherBlob,
	ObjectClassTree,
	ObjectClassCommit,
	ObjectClassAnnotatedTag,
}

var sqliteSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// ObjectClassAccount is one durable object class's count and byte totals.
type ObjectClassAccount struct {
	Class     string `json:"class"`
	Objects   int64  `json:"objects"`
	RawBytes  int64  `json:"rawBytes"`
	DiskBytes int64  `json:"diskBytes"`
}

// GitStorageAccount separates durable Git storage by object class and records
// the repository's whole object-directory footprint.
type GitStorageAccount struct {
	ObjectFormat        string               `json:"objectFormat"`
	RefPrefix           string               `json:"refPrefix"`
	WorkbookRefs        int64                `json:"workbookRefs"`
	TaskRefs            int64                `json:"taskRefs"`
	ReachableObjects    int64                `json:"reachableObjects"`
	ClassifiedObjects   int64                `json:"classifiedObjects"`
	UnclassifiedObjects int64                `json:"unclassifiedObjects"`
	Classes             []ObjectClassAccount `json:"classes"`
	ReachableRawBytes   int64                `json:"reachableRawBytes"`
	ReachableDiskBytes  int64                `json:"reachableDiskBytes"`

	Packs                int64 `json:"packs"`
	PackedObjects        int64 `json:"packedObjects"`
	PackFileBytes        int64 `json:"packFileBytes"`
	PackIndexBytes       int64 `json:"packIndexBytes"`
	PackAuxiliaryBytes   int64 `json:"packAuxiliaryBytes"`
	LooseObjects         int64 `json:"looseObjects"`
	LooseObjectFileBytes int64 `json:"looseObjectFileBytes"`
	ObjectDirectoryBytes int64 `json:"objectDirectoryBytes"`
}

// DisposableCacheAccount records the deletable SQLite caches separately from
// durable Git storage. Paths are relative to the measured repository root.
type DisposableCacheAccount struct {
	ProjectionPath         string `json:"projectionPath"`
	ProjectionBytes        int64  `json:"projectionBytes"`
	ProjectionSidecarBytes int64  `json:"projectionSidecarBytes"`
	ValidationPath         string `json:"validationPath"`
	ValidationBytes        int64  `json:"validationBytes"`
	ValidationSidecarBytes int64  `json:"validationSidecarBytes"`
	TotalBytes             int64  `json:"totalBytes"`
}

// StorageResourceDepth holds one fixture depth's complete accounting.
type StorageResourceDepth struct {
	Fixture         FixtureSpec            `json:"fixture"`
	Git             GitStorageAccount      `json:"git"`
	DisposableCache DisposableCacheAccount `json:"disposableCache"`
	Resources       []ResourceMeasurement  `json:"resources"`
}

// StorageResourceReport is the descriptive storage and peak-resource section
// of a benchmark report. It carries no targets and no pass/fail semantics.
type StorageResourceReport struct {
	Platform                 string                 `json:"platform"`
	MaxResidentRawUnit       string                 `json:"maxResidentRawUnit"`
	BlockIOCountersSupported bool                   `json:"blockIoCountersSupported"`
	ObjectSizeSemantics      string                 `json:"objectSizeSemantics"`
	RepositoryState          string                 `json:"repositoryState"`
	Depths                   []StorageResourceDepth `json:"depths"`
}

// StorageResourceSpec configures a storage and peak-resource measurement.
type StorageResourceSpec struct {
	WorkbookBinary  string
	Root            string
	Fixture         FixtureSpec
	OperationDepths []int
	CommandTimeout  time.Duration
	FixtureTimeout  time.Duration
}

// MeasureStorageResources builds one fixture per requested operation depth,
// packs it, separates its durable Git bytes by object class, records its
// disposable cache sizes, and captures peak resident memory and I/O for the
// projection rebuild and full validation commands. Fixture construction and
// packing happen entirely outside every measured command.
func MeasureStorageResources(ctx context.Context, spec StorageResourceSpec) (*StorageResourceReport, error) {
	depths, err := validateStorageResourceSpec(spec)
	if err != nil {
		return nil, err
	}

	report := &StorageResourceReport{
		Platform:                 runtime.GOOS + "/" + runtime.GOARCH,
		MaxResidentRawUnit:       MaxResidentUnitForOS(runtime.GOOS),
		BlockIOCountersSupported: BlockIOCountersSupportedForOS(runtime.GOOS),
		ObjectSizeSemantics:      storageObjectSizeSemantics,
		RepositoryState:          storageRepositoryState,
		Depths:                   make([]StorageResourceDepth, 0, len(depths)),
	}
	for _, operations := range depths {
		depth, err := measureStorageResourceDepth(ctx, spec, operations)
		if err != nil {
			return nil, err
		}
		report.Depths = append(report.Depths, depth)
	}
	return report, nil
}

func validateStorageResourceSpec(spec StorageResourceSpec) ([]int, error) {
	if spec.WorkbookBinary == "" {
		return nil, fmt.Errorf("workbook binary is required")
	}
	if spec.Root == "" {
		return nil, fmt.Errorf("storage measurement root is required")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if spec.FixtureTimeout <= 0 {
		return nil, fmt.Errorf("fixture timeout must be positive")
	}
	if len(spec.OperationDepths) == 0 {
		return nil, fmt.Errorf("at least one operation depth is required")
	}
	seen := make(map[int]struct{}, len(spec.OperationDepths))
	depths := make([]int, 0, len(spec.OperationDepths))
	for _, operations := range spec.OperationDepths {
		if operations < 1 {
			return nil, fmt.Errorf("operation depth %d must be positive", operations)
		}
		if _, duplicate := seen[operations]; duplicate {
			return nil, fmt.Errorf("duplicate operation depth %d", operations)
		}
		seen[operations] = struct{}{}
		depths = append(depths, operations)
	}
	sort.Ints(depths)
	for _, operations := range depths {
		fixture := spec.Fixture
		fixture.OperationsPerTask = operations
		if err := validateFixtureSpec(fixture); err != nil {
			return nil, fmt.Errorf("operation depth %d: %w", operations, err)
		}
	}
	return depths, nil
}

func measureStorageResourceDepth(ctx context.Context, spec StorageResourceSpec, operations int) (StorageResourceDepth, error) {
	fixtureSpec := spec.Fixture
	fixtureSpec.OperationsPerTask = operations
	root := filepath.Join(spec.Root, fmt.Sprintf("operations-%04d", operations))

	fixtureContext, cancel := context.WithTimeout(ctx, spec.FixtureTimeout)
	_, err := BuildFixture(fixtureContext, root, fixtureSpec)
	cancel()
	if err != nil {
		return StorageResourceDepth{}, fmt.Errorf("build %d-operation storage fixture: %w", operations, err)
	}
	if err := prepareStorageRepository(ctx, spec.FixtureTimeout, root); err != nil {
		return StorageResourceDepth{}, fmt.Errorf("prepare %d-operation storage fixture: %w", operations, err)
	}

	account, err := measureGitStorage(ctx, spec.CommandTimeout, root)
	if err != nil {
		return StorageResourceDepth{}, fmt.Errorf("measure %d-operation durable storage: %w", operations, err)
	}

	resources := make([]ResourceMeasurement, 0, 2)
	for _, command := range storageResourceCommands() {
		measurement, err := measureStorageResourceCommand(ctx, spec, root, fixtureSpec, command)
		if err != nil {
			return StorageResourceDepth{}, fmt.Errorf("measure %d-operation %s: %w", operations, command.name, err)
		}
		resources = append(resources, measurement)
	}

	cache, err := measureDisposableCaches(ctx, spec.CommandTimeout, root)
	if err != nil {
		return StorageResourceDepth{}, fmt.Errorf("measure %d-operation disposable caches: %w", operations, err)
	}
	if err := os.RemoveAll(root); err != nil {
		return StorageResourceDepth{}, fmt.Errorf("remove %d-operation storage fixture: %w", operations, err)
	}
	return StorageResourceDepth{
		Fixture:         fixtureSpec,
		Git:             account,
		DisposableCache: cache,
		Resources:       resources,
	}, nil
}

// prepareStorageRepository packs refs and objects so the measured footprint is
// the steady state a fresh clone would carry. It runs outside every measured
// command.
func prepareStorageRepository(ctx context.Context, timeout time.Duration, root string) error {
	if _, err := runStorageGit(ctx, timeout, root, nil, "pack-refs", "--all"); err != nil {
		return err
	}
	if _, err := runStorageGit(ctx, timeout, root, nil, "gc", "--quiet", "--prune=now"); err != nil {
		return err
	}
	return nil
}

// storageResourceCommand is one measured command together with the literal
// oracle its result must satisfy. A peak-memory number is only evidence about a
// command that actually did the work it names, so every measured command is
// checked on content and not on its exit code alone.
type storageResourceCommand struct {
	name   string
	args   []string
	verify func(FixtureSpec, []byte) error
}

func storageResourceCommands() []storageResourceCommand {
	return []storageResourceCommand{
		{
			name: ResourceCommandProjectionRebuild,
			args: []string{"rebuild", "--json"},
			verify: func(fixture FixtureSpec, stdout []byte) error {
				return verifyRebuildResultOutput(stdout, fixture.TotalTasks)
			},
		},
		{
			name: ResourceCommandFullValidation,
			args: []string{"validate", "--full", "--json"},
			verify: func(fixture FixtureSpec, stdout []byte) error {
				return verifyValidationResultOutput("validate-full-history", stdout, fixture)
			},
		},
	}
}

func verifyRebuildResultOutput(stdout []byte, totalTasks int) error {
	var envelope remoteResultEnvelope
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return fmt.Errorf("decode rebuild result: %w", err)
	}
	if envelope.Format != workbookResultFormat || envelope.Version != workbookJSONVersion || envelope.Command != "rebuild" {
		return fmt.Errorf("unexpected rebuild result envelope")
	}
	var result rebuildProjectionResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return fmt.Errorf("decode rebuild data: %w", err)
	}
	if result.TaskCount != totalTasks {
		return fmt.Errorf("rebuild task count = %d, want %d", result.TaskCount, totalTasks)
	}
	return nil
}

func measureStorageResourceCommand(
	ctx context.Context,
	spec StorageResourceSpec,
	root string,
	fixture FixtureSpec,
	command storageResourceCommand,
) (ResourceMeasurement, error) {
	before, err := directoryBytes(root)
	if err != nil {
		return ResourceMeasurement{}, err
	}
	measurement := MeasureCommandResources(ctx, CommandSpec{
		Binary:    spec.WorkbookBinary,
		Args:      command.args,
		Directory: root,
		Timeout:   spec.CommandTimeout,
	})
	after, err := directoryBytes(root)
	if err != nil {
		return ResourceMeasurement{}, err
	}
	measurement.Command = command.name
	measurement.RepositoryBytesDelta = after - before
	if measurement.TimedOut {
		return ResourceMeasurement{}, fmt.Errorf("%s timed out after %s: %s", command.name, spec.CommandTimeout, measurement.Error)
	}
	if measurement.ExitCode != 0 || measurement.Error != "" {
		return ResourceMeasurement{}, fmt.Errorf("%s failed with exit code %d: %s", command.name, measurement.ExitCode, measurement.Error)
	}
	if command.verify != nil {
		if err := command.verify(fixture, measurement.Stdout); err != nil {
			return ResourceMeasurement{}, fmt.Errorf("verify %s: %w", command.name, err)
		}
	}
	measurement.Stdout = nil
	measurement.Stderr = nil
	return measurement, nil
}

type reachableObject struct {
	objectType string
	path       string
	rawBytes   int64
	diskBytes  int64
}

// measureGitStorage classifies every object reachable from the repository's
// Workbook refs and records the object directory's on-disk footprint.
func measureGitStorage(ctx context.Context, timeout time.Duration, root string) (GitStorageAccount, error) {
	objectFormat, err := storageGitLine(ctx, timeout, root, "rev-parse", "--show-object-format")
	if err != nil {
		return GitStorageAccount{}, err
	}

	refOutput, err := runStorageGit(ctx, timeout, root, nil,
		"for-each-ref", "--format=%(refname)%00%(objectname)", workbookRefPrefix)
	if err != nil {
		return GitStorageAccount{}, err
	}
	account := GitStorageAccount{ObjectFormat: objectFormat, RefPrefix: workbookRefPrefix}
	var tips strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(refOutput), "\n"), "\n") {
		if line == "" {
			continue
		}
		refName, objectName, found := strings.Cut(line, "\x00")
		if !found || objectName == "" {
			return GitStorageAccount{}, fmt.Errorf("invalid Workbook ref record %q", line)
		}
		account.WorkbookRefs++
		if strings.HasPrefix(refName, workbookTaskRefPrefix) {
			account.TaskRefs++
		}
		tips.WriteString(objectName)
		tips.WriteByte('\n')
	}
	if account.WorkbookRefs == 0 {
		return GitStorageAccount{}, fmt.Errorf("repository has no refs under %s", workbookRefPrefix)
	}

	objects, order, err := walkReachableObjects(ctx, timeout, root, []byte(tips.String()))
	if err != nil {
		return GitStorageAccount{}, err
	}
	account.ReachableObjects = int64(len(order))

	classes := make(map[string]*ObjectClassAccount, len(objectClassOrder))
	for _, class := range objectClassOrder {
		classes[class] = &ObjectClassAccount{Class: class}
	}
	for _, objectName := range order {
		object := objects[objectName]
		class, err := classifyReachableObject(object)
		if err != nil {
			return GitStorageAccount{}, fmt.Errorf("classify object %s: %w", objectName, err)
		}
		bucket := classes[class]
		bucket.Objects++
		bucket.RawBytes += object.rawBytes
		bucket.DiskBytes += object.diskBytes
		account.ClassifiedObjects++
		account.ReachableRawBytes += object.rawBytes
		account.ReachableDiskBytes += object.diskBytes
	}
	account.UnclassifiedObjects = account.ReachableObjects - account.ClassifiedObjects
	account.Classes = make([]ObjectClassAccount, 0, len(objectClassOrder))
	for _, class := range objectClassOrder {
		account.Classes = append(account.Classes, *classes[class])
	}

	countOutput, err := runStorageGit(ctx, timeout, root, nil, "count-objects", "-v")
	if err != nil {
		return GitStorageAccount{}, err
	}
	counts, err := parseCountObjects(countOutput)
	if err != nil {
		return GitStorageAccount{}, err
	}
	account.LooseObjects = counts.count
	account.PackedObjects = counts.inPack

	objectsDirectory, err := storageGitLine(ctx, timeout, root, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return GitStorageAccount{}, err
	}
	if err := accountObjectDirectory(objectsDirectory, &account); err != nil {
		return GitStorageAccount{}, err
	}
	return account, nil
}

// classifyReachableObject maps one object into exactly one durable class. The
// type switch covers every Git object type, and the blob switch has a default
// bucket, so the classification is total by construction.
func classifyReachableObject(object reachableObject) (string, error) {
	switch object.objectType {
	case "commit":
		return ObjectClassCommit, nil
	case "tree":
		return ObjectClassTree, nil
	case "tag":
		return ObjectClassAnnotatedTag, nil
	case "blob":
		switch object.path {
		case operationDocumentPath:
			return ObjectClassOperationBlob, nil
		case stateDocumentPath:
			return ObjectClassStateBlob, nil
		default:
			return ObjectClassOtherBlob, nil
		}
	default:
		return "", fmt.Errorf("unknown Git object type %q", object.objectType)
	}
}

// walkReachableObjects enumerates the object graph reachable from the supplied
// tips with `git rev-list --objects`, which yields each object once together
// with the tree path it was reached through, and then reads each object's type
// and both size representations with `git cat-file --batch-check`.
func walkReachableObjects(
	ctx context.Context,
	timeout time.Duration,
	root string,
	tips []byte,
) (map[string]reachableObject, []string, error) {
	listed, err := runStorageGit(ctx, timeout, root, tips, "rev-list", "--objects", "--stdin")
	if err != nil {
		return nil, nil, err
	}

	objects := make(map[string]reachableObject)
	var order []string
	var names bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(listed))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		objectName, path, _ := strings.Cut(line, " ")
		if objectName == "" {
			return nil, nil, fmt.Errorf("invalid rev-list record %q", line)
		}
		if _, duplicate := objects[objectName]; duplicate {
			return nil, nil, fmt.Errorf("object %s was listed more than once", objectName)
		}
		objects[objectName] = reachableObject{path: path}
		order = append(order, objectName)
		names.WriteString(objectName)
		names.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read rev-list output: %w", err)
	}
	if len(order) == 0 {
		return nil, nil, fmt.Errorf("no objects are reachable from the Workbook refs")
	}

	checked, err := runStorageGit(ctx, timeout, root, names.Bytes(), "cat-file",
		"--batch-check=%(objectname) %(objecttype) %(objectsize) %(objectsize:disk)")
	if err != nil {
		return nil, nil, err
	}
	described := 0
	scanner = bufio.NewScanner(bytes.NewReader(checked))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, nil, fmt.Errorf("invalid cat-file record %q", line)
		}
		object, known := objects[fields[0]]
		if !known {
			return nil, nil, fmt.Errorf("cat-file described unrequested object %s", fields[0])
		}
		if object.objectType != "" {
			return nil, nil, fmt.Errorf("cat-file described object %s more than once", fields[0])
		}
		rawBytes, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || rawBytes < 0 {
			return nil, nil, fmt.Errorf("invalid object size %q for %s", fields[2], fields[0])
		}
		diskBytes, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || diskBytes < 0 {
			return nil, nil, fmt.Errorf("invalid on-disk object size %q for %s", fields[3], fields[0])
		}
		object.objectType = fields[1]
		object.rawBytes = rawBytes
		object.diskBytes = diskBytes
		objects[fields[0]] = object
		described++
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read cat-file output: %w", err)
	}
	if described != len(order) {
		return nil, nil, fmt.Errorf("cat-file described %d of %d reachable objects", described, len(order))
	}
	return objects, order, nil
}

func accountObjectDirectory(objectsDirectory string, account *GitStorageAccount) error {
	total, err := directoryBytes(objectsDirectory)
	if err != nil {
		return err
	}
	account.ObjectDirectoryBytes = total

	packDirectory := filepath.Join(objectsDirectory, "pack")
	entries, err := os.ReadDir(packDirectory)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read pack directory %q: %w", packDirectory, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat pack file %q: %w", entry.Name(), err)
		}
		switch filepath.Ext(entry.Name()) {
		case ".pack":
			account.Packs++
			account.PackFileBytes += info.Size()
		case ".idx":
			account.PackIndexBytes += info.Size()
		default:
			account.PackAuxiliaryBytes += info.Size()
		}
	}

	looseObjects, looseBytes, err := looseObjectFootprint(objectsDirectory)
	if err != nil {
		return err
	}
	account.LooseObjectFileBytes = looseBytes
	if account.LooseObjects == 0 && looseObjects != 0 {
		account.LooseObjects = looseObjects
	}
	return nil
}

func looseObjectFootprint(objectsDirectory string) (int64, int64, error) {
	entries, err := os.ReadDir(objectsDirectory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("read object directory %q: %w", objectsDirectory, err)
	}
	var objects, bytesTotal int64
	for _, entry := range entries {
		if !entry.IsDir() || !isLooseObjectFanout(entry.Name()) {
			continue
		}
		fanout := filepath.Join(objectsDirectory, entry.Name())
		objectEntries, err := os.ReadDir(fanout)
		if err != nil {
			return 0, 0, fmt.Errorf("read loose object directory %q: %w", fanout, err)
		}
		for _, objectEntry := range objectEntries {
			if objectEntry.IsDir() || !isHexName(objectEntry.Name()) {
				continue
			}
			info, err := objectEntry.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return 0, 0, fmt.Errorf("stat loose object %q: %w", objectEntry.Name(), err)
			}
			objects++
			bytesTotal += info.Size()
		}
	}
	return objects, bytesTotal, nil
}

func isLooseObjectFanout(name string) bool {
	return len(name) == 2 && isHexName(name)
}

func isHexName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		default:
			return false
		}
	}
	return true
}

// measureDisposableCaches sizes the deletable SQLite projection and validation
// caches. Both live under the repository's common Git directory and can be
// removed and rebuilt without losing durable history.
func measureDisposableCaches(ctx context.Context, timeout time.Duration, root string) (DisposableCacheAccount, error) {
	commonDirectory, err := storageGitLine(ctx, timeout, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return DisposableCacheAccount{}, err
	}

	account := DisposableCacheAccount{
		ProjectionPath: filepath.Join(commonDirectory, "workbook", projectionCacheFilename),
		ValidationPath: filepath.Join(commonDirectory, "workbook", validationCacheFilename),
	}
	account.ProjectionBytes, account.ProjectionSidecarBytes, err = sqliteFootprint(root, account.ProjectionPath)
	if err != nil {
		return DisposableCacheAccount{}, err
	}
	account.ValidationBytes, account.ValidationSidecarBytes, err = sqliteFootprint(root, account.ValidationPath)
	if err != nil {
		return DisposableCacheAccount{}, err
	}
	account.TotalBytes = account.ProjectionBytes + account.ProjectionSidecarBytes +
		account.ValidationBytes + account.ValidationSidecarBytes
	return account, nil
}

func sqliteFootprint(root, relativePath string) (int64, int64, error) {
	path := relativePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, relativePath)
	}
	database, err := fileBytes(path)
	if err != nil {
		return 0, 0, err
	}
	var sidecars int64
	for _, suffix := range sqliteSidecarSuffixes {
		size, err := fileBytes(path + suffix)
		if err != nil {
			return 0, 0, err
		}
		sidecars += size
	}
	return database, sidecars, nil
}

func fileBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat %q: %w", path, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%q is a directory", path)
	}
	return info.Size(), nil
}

func storageGitLine(ctx context.Context, timeout time.Duration, root string, args ...string) (string, error) {
	output, err := runStorageGit(ctx, timeout, root, nil, args...)
	if err != nil {
		return "", err
	}
	line := strings.TrimRight(string(output), "\n")
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", fmt.Errorf("git %s did not return one line", strings.Join(args, " "))
	}
	return line, nil
}

func runStorageGit(ctx context.Context, timeout time.Duration, root string, input []byte, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, "git", commandArgs...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = commandWaitDelay
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if commandContext.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("git %s timed out after %s", strings.Join(args, " "), timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// writeStorageResourceMarkdown appends the descriptive storage and resource
// section. A report without a storage measurement writes nothing.
func writeStorageResourceMarkdown(w io.Writer, report *StorageResourceReport) error {
	if report == nil {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n## Storage and peak resources"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\nDescriptive measurements with no target. Platform %s. Repository state: %s.\n",
		report.Platform, report.RepositoryState); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\nObject size semantics: %s.\n", report.ObjectSizeSemantics); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\nRaw `ru_maxrss` unit on this platform: %s. Block I/O counters (`ru_inblock`, `ru_oublock`) maintained: %t.\n",
		report.MaxResidentRawUnit, report.BlockIOCountersSupported); err != nil {
		return err
	}

	for _, depth := range report.Depths {
		if _, err := fmt.Fprintf(w, "\n### %d tasks by %d operations (%s)\n",
			depth.Fixture.TotalTasks, depth.Fixture.OperationsPerTask, depth.Fixture.ObjectFormat); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "\nFixture: %d total tasks, %d active, %d tombstoned, %d operations per task, %s objects.\n",
			depth.Fixture.TotalTasks, depth.Fixture.ActiveTasks, depth.Fixture.TombstonedTasks,
			depth.Fixture.OperationsPerTask, depth.Fixture.ObjectFormat); err != nil {
			return err
		}
		if err := writeObjectClassTable(w, depth.Git); err != nil {
			return err
		}
		if err := writeRepositoryStorageTable(w, depth.Git); err != nil {
			return err
		}
		if err := writeDisposableCacheTable(w, depth.DisposableCache); err != nil {
			return err
		}
		if err := writeResourceTable(w, depth.Resources); err != nil {
			return err
		}
	}
	return nil
}

func writeObjectClassTable(w io.Writer, account GitStorageAccount) error {
	if _, err := fmt.Fprintf(w, "\nDurable objects reachable from `%s` (%d refs, %d task refs): classified %d of %d, %d unclassified.\n\n",
		account.RefPrefix, account.WorkbookRefs, account.TaskRefs,
		account.ClassifiedObjects, account.ReachableObjects, account.UnclassifiedObjects); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Object class | Objects | Raw bytes | On-disk bytes |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: | ---: | ---: |"); err != nil {
		return err
	}
	for _, class := range account.Classes {
		if _, err := fmt.Fprintf(w, "| %s | %d | %d | %d |\n", class.Class, class.Objects, class.RawBytes, class.DiskBytes); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "| **Total reachable** | %d | %d | %d |\n",
		account.ReachableObjects, account.ReachableRawBytes, account.ReachableDiskBytes)
	return err
}

func writeRepositoryStorageTable(w io.Writer, account GitStorageAccount) error {
	if _, err := fmt.Fprintln(w, "\n| Repository storage | Bytes |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: |"); err != nil {
		return err
	}
	for _, row := range [][2]any{
		{fmt.Sprintf("Pack files (packs: %d, packed objects: %d)", account.Packs, account.PackedObjects), account.PackFileBytes},
		{"Pack indexes", account.PackIndexBytes},
		{"Pack auxiliary files", account.PackAuxiliaryBytes},
		{fmt.Sprintf("Loose objects (%d)", account.LooseObjects), account.LooseObjectFileBytes},
		{"Object directory total", account.ObjectDirectoryBytes},
	} {
		if _, err := fmt.Fprintf(w, "| %v | %v |\n", row[0], row[1]); err != nil {
			return err
		}
	}
	return nil
}

func writeDisposableCacheTable(w io.Writer, cache DisposableCacheAccount) error {
	if _, err := fmt.Fprintln(w, "\n| Disposable cache | Bytes |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: |"); err != nil {
		return err
	}
	for _, row := range [][2]any{
		{fmt.Sprintf("SQLite projection (`%s`)", cache.ProjectionPath), cache.ProjectionBytes},
		{"SQLite projection sidecars", cache.ProjectionSidecarBytes},
		{fmt.Sprintf("Validation cache (`%s`)", cache.ValidationPath), cache.ValidationBytes},
		{"Validation cache sidecars", cache.ValidationSidecarBytes},
		{"**Total disposable**", cache.TotalBytes},
	} {
		if _, err := fmt.Fprintf(w, "| %v | %v |\n", row[0], row[1]); err != nil {
			return err
		}
	}
	return nil
}

func writeResourceTable(w io.Writer, resources []ResourceMeasurement) error {
	if _, err := fmt.Fprintln(w, "\n| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"); err != nil {
		return err
	}
	for _, resource := range resources {
		if _, err := fmt.Fprintf(w, "| %s | %.2f | %d | %d %s | %d | %d | %d | %d | %d |\n",
			resource.Command, resource.Milliseconds, resource.MaxResidentBytes,
			resource.MaxResidentRaw, resource.MaxResidentRawUnit,
			resource.BlockInputOperations, resource.BlockOutputOperations,
			resource.MinorPageFaults, resource.MajorPageFaults, resource.RepositoryBytesDelta); err != nil {
			return err
		}
	}
	return nil
}
